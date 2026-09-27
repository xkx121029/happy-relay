package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- 主 AI Agent：站内 OpenClaw ----------
//
// 主模型以「与用户相同的管理权限」执行工具：建模型、改渠道、改任意设置项、
// 管秘钥与技能……后端在一个请求里跑工具循环（AI 出 JSON 指令 → 执行 → 结果回灌 →
// 继续，最多 8 轮），最后返回给用户的既有最终答复也有每一步执行轨迹。
// 不可逆操作（删除/清空）要求 AI 先征得用户确认（params.confirm=true），双层保险。

const agentSystemPrompt = `你是「模型中转站」的站内管理智能体，拥有与面板用户相同的管理权限：
查询与创建/修改/删除模型、渠道、秘钥、技能，读取与修改任意设置项，查看日志与上游画像，执行多模态自动聚合。

工作方式：你每轮只能输出一个 JSON 对象（不要 markdown 围栏、不要多余文字）：
1. 需要调用工具时：{"think":"简短思路","action":"工具名","params":{...}}
2. 信息足够回答/任务完成时：{"reply":"给用户的最终中文回复","settings":{"k":"v"}}（settings 可选：带上会一次性修改对应设置项并立即热生效，与回复同时完成；能用 settings 直接改的就不必再调 save_settings 工具）
3. 需要用户确认删除类操作时：先输出 reply 说明将删除什么并请求确认；用户同意后再次调用同一工具并带 "confirm": true。

可用工具：
- list_models                    {}                          列出全部对外模型（含上游、模态、启停）
- save_model                     {name,capability,enabled,remark,inputs:[{provider_id,upstream_name,input_modalities}],config:{route_strategy}}  创建或更新模型（inputs 至少 1 条；聚合时纯文本上游排前面）
- delete_model                   {id,confirm}                删除模型（不可逆，需 confirm）
- list_providers                 {}                          列出渠道（含密钥数量、启停）
- save_provider                  {id(0=新建),name,kind,base_urls:[...],api_keys:[...],enabled,remark}  创建/更新渠道
- list_catalog                   {provider_id(可选)}          列出模型库上游（含能力与模态标注）
- get_settings                   {}                          读取全部设置项
- save_settings                  {k:v,...}                   修改任意设置项并立即热生效
- list_keys                      {}                          列出对外秘钥
- save_key                       {id(0=新建),name,models:[...],rpm,daily_quota,enabled}  创建/更新秘钥
- delete_key                     {id,confirm}                删除秘钥（需 confirm）
- list_skills                    {}                          列出技能
- save_skill                     {slug(空=新建),name,capability,prompt,params:[{key,title,default}],enabled}  创建/更新技能
- delete_skill                   {id,confirm}                删除技能（需 confirm）
- auto_aggregate                 {apply:bool}                多模态自动聚合（false=只出计划）
- list_logs                      {limit(默认50)}             最近调用日志
- upstream_stats                 {}                          上游健康画像
- clear_logs                     {confirm}                   清空调用日志（需 confirm）

原则：先查再改（改之前用 list/ get 确认现状）；改动尽量小；一次任务里的多个工具按依赖顺序调用；
能用 settings 字段一次完成的设置变更，优先在最终 reply 里用 settings 字段声明，不必再调 save_settings 工具；
删除与清空必须先征得用户同意；对「整合所有模型」类需求用 save_model 建聚合模型。
所有面向用户的文字都用中文。`

const agentMaxRounds = 8

type agentMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type agentRequest struct {
	Messages []agentMessage `json:"messages"`
	Stream   bool           `json:"stream"`
}

// agentStep 是一次工具执行的轨迹，前端逐条展示。
type agentStep struct {
	Action  string         `json:"action"`
	Params  map[string]any `json:"params,omitempty"`
	OK      bool           `json:"ok"`
	Summary string         `json:"summary,omitempty"`
	Error   string         `json:"error,omitempty"`
	Detail  string         `json:"detail,omitempty"` // 完整原始结果，前端可展开查看（summary 已截断）
}

type agentResponse struct {
	Reply       string      `json:"reply"`
	Steps       []agentStep `json:"steps"`
	SystemModel string      `json:"system_model"`
	LatencyMS   int64       `json:"latency_ms"`
}

type agentTurn struct {
	Think    string         `json:"think"`
	Action   string         `json:"action"`
	Params   map[string]any `json:"params"`
	Reply    string         `json:"reply"`
	Settings map[string]any `json:"settings"`
}

// handleAgentChat 主 AI 的工具循环入口。前端传完整对话历史，后端无状态。
func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	var req agentRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "对话内容为空")
		return
	}
	systemModel := s.st.GetSetting(SettingSystemModel, "")
	if systemModel == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"尚未指定系统级 AI，请先在「设置 → 通用 → 系统级 AI 模型」里选择一个文本模型")
		return
	}

	m, ps, err := s.eng.ResolveAll(systemModel)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream_error", "系统级 AI 不可用："+err.Error())
		return
	}

	history := make([]relay.ChatMessage, 0, len(req.Messages)+2)
	history = append(history, relay.ChatMessage{Role: "system", Content: agentSystemPrompt})
	for _, msg := range req.Messages {
		role := msg.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		history = append(history, relay.ChatMessage{Role: role, Content: msg.Content})
	}

	// 流式：每完成一步工具就实时推一个 step 事件，最终回复单独推 reply 事件。
	var sse *sseWriter
	if req.Stream {
		sse = newSSEWriter(w)
	}
	emitFn := func(step agentStep) {
		if sse != nil {
			_ = sse.send(map[string]any{"type": "step", "step": step})
		}
	}
	resp, loopErr := s.agentCore(r.Context(), systemModel, m, ps, history, emitFn)
	if loopErr != nil {
		if sse != nil {
			_ = sse.send(map[string]any{"type": "error", "message": loopErr.Error()})
			sse.done()
			return
		}
		// 非流式：把失败原因当最终答复返回，调用方能看到具体错误。
		writeJSON(w, http.StatusOK, &agentResponse{
			SystemModel: systemModel,
			Steps:       []agentStep{},
			Reply:       loopErr.Error(),
		})
		return
	}
	if sse != nil {
		_ = sse.send(map[string]any{
			"type":         "reply",
			"reply":        resp.Reply,
			"system_model": systemModel,
			"latency_ms":   resp.LatencyMS,
		})
		sse.done()
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// agentCore 跑完整的 agent 工具循环：主模型出 JSON 指令 → 执行 → 结果回灌 → 继续（最多 agentMaxRounds 轮）。
// emitFn 在每步完成后回调（可为 nil），供流式推送；resp 里始终累计完整步骤。
// 返回 error 仅表示循环中途调用系统级 AI 失败（上游报错/限流等），此时没有可用答复。
// handleAgentChat（面板）与飞书桥（bridge_feishu.go）共用本实现，保证行为一致。
func (s *Server) agentCore(ctx context.Context, systemModel string, m store.Model, ps []store.Provider, history []relay.ChatMessage, emitFn func(agentStep)) (*agentResponse, error) {
	resp := agentResponse{SystemModel: systemModel, Steps: []agentStep{}}
	start := time.Now()
	emitStep := func(step agentStep) {
		resp.Steps = append(resp.Steps, step)
		if emitFn != nil {
			emitFn(step)
		}
	}
	reply := ""

	for round := 0; round < agentMaxRounds; round++ {
		in := &relay.ChatInput{
			Model:    systemModel,
			Messages: history,
			Options:  map[string]any{"temperature": 0.2},
			Raw:      map[string]any{},
		}
		out, _, callErr := s.eng.Chat(ctx, m, ps, in, nil)
		if callErr != nil {
			return nil, fmt.Errorf("系统级 AI 调用失败：%w", callErr)
		}
		text := extractJSONObject(out.Content)
		var turn agentTurn
		if text == "" || json.Unmarshal([]byte(text), &turn) != nil || (turn.Action == "" && turn.Reply == "") {
			// 不守协议：把原文当最终回复，避免死循环。
			reply = strings.TrimSpace(out.Content)
			break
		}
		// 协议合一：最终回复可携带 settings，一次性完成「对话 + 热生效设置」。
		if len(turn.Settings) > 0 {
			emitStep(s.applyAgentSettings(turn.Settings))
		}

		if turn.Reply != "" && turn.Action == "" {
			reply = turn.Reply
			break
		}

		step := s.agentExec(turn.Action, turn.Params)
		emitStep(step)

		// 工具结果回灌：assistant 的指令 + user 角色的结果标注。
		history = append(history, relay.ChatMessage{Role: "assistant", Content: text})
		resultJSON, _ := json.Marshal(map[string]any{"ok": step.OK, "summary": step.Summary, "error": step.Error})
		history = append(history, relay.ChatMessage{
			Role:    "user",
			Content: "[工具结果] " + string(resultJSON) + "\n请继续：任务未完成就调用下一个工具，已完成就输出 reply。",
		})
	}

	resp.LatencyMS = time.Since(start).Milliseconds()
	if reply == "" {
		reply = "已执行 " + fmt.Sprint(len(resp.Steps)) + " 步工具调用，但没有给出最终答复。"
	}
	resp.Reply = reply
	return &resp, nil
}

// mapToStruct 把工具参数 map 转成目标结构体（等价 decodeBody 的反序列化路径）。
func mapToStruct(params map[string]any, target any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// paramsString 取字符串参数。
func paramsString(params map[string]any, key string) string {
	v, _ := params[key].(string)
	return strings.TrimSpace(v)
}

// paramsInt 取数字参数（容忍字符串数字）。
func paramsInt(params map[string]any, key string) int64 {
	switch v := params[key].(type) {
	case float64:
		return int64(v)
	case string:
		var n int64
		fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	}
	return 0
}

func paramsBool(params map[string]any, key string) bool {
	v, _ := params[key].(bool)
	return v
}

// agentExec 工具分发与执行。与面板 handler 走同一套 store 方法 = 与用户相同权限。
func (s *Server) agentExec(action string, params map[string]any) agentStep {
	step := agentStep{Action: action, Params: params, OK: true}
	fail := func(format string, args ...any) agentStep {
		step.OK = false
		step.Error = fmt.Sprintf(format, args...)
		return step
	}

	switch action {
	case "list_models":
		list, err := s.st.ListModels()
		if err != nil {
			return fail("读取模型失败：%v", err)
		}
		step.Summary = fmt.Sprintf("共 %d 个模型", len(list))
		step.Params = nil
		b, _ := json.Marshal(list)
		step.Detail = string(b)
		step.Summary += "：" + truncateStr(string(b), 3000)

	case "save_model":
		var m store.Model
		if err := mapToStruct(params, &m); err != nil {
			return fail("参数不合法：%v", err)
		}
		if m.ID == 0 && m.Name == "" {
			return fail("缺少模型名 name")
		}
		if !paramsBool(params, "enabled_set") && params["enabled"] == nil {
			m.Enabled = true
		}
		saved, err := s.st.SaveModel(m)
		if err != nil {
			return fail("保存模型失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已保存模型「%s」（%s，%d 个上游，启用=%v）", saved.Name, saved.Capability, len(saved.Inputs), saved.Enabled)

	case "delete_model":
		id := paramsInt(params, "id")
		if id <= 0 {
			return fail("缺少模型 id")
		}
		if !paramsBool(params, "confirm") {
			return fail("删除模型「id=%d」是不可逆操作，已拒绝执行——请先向用户说明并征得同意后，携带 confirm:true 重试", id)
		}
		if err := s.st.DeleteModel(id); err != nil {
			return fail("删除失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已删除模型 #%d", id)

	case "list_providers":
		list, err := s.st.ListProviders()
		if err != nil {
			return fail("读取渠道失败：%v", err)
		}
		type provView struct {
			ID       int64    `json:"id"`
			Name     string   `json:"name"`
			Kind     string   `json:"kind"`
			Enabled  bool     `json:"enabled"`
			BaseURLs []string `json:"base_urls"`
			Keys     int      `json:"api_key_count"`
		}
		out := make([]provView, 0, len(list))
		for _, p := range list {
			out = append(out, provView{p.ID, p.Name, p.Kind, p.Enabled, p.BaseURLs, len(p.APIKeys)})
		}
		b, _ := json.Marshal(out)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("共 %d 个渠道：%s", len(list), truncateStr(string(b), 2500))

	case "save_provider":
		var p store.Provider
		if err := mapToStruct(params, &p); err != nil {
			return fail("参数不合法：%v", err)
		}
		if p.Name == "" {
			return fail("缺少渠道名 name")
		}
		saved, err := s.st.SaveProvider(p)
		if err != nil {
			return fail("保存渠道失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已保存渠道 #%d「%s」（%s，启用=%v）", saved.ID, saved.Name, saved.Kind, saved.Enabled)

	case "list_catalog":
		providerID := paramsInt(params, "provider_id")
		list, err := s.st.ListCatalogModels(providerID, "")
		if err != nil {
			return fail("读取模型库失败：%v", err)
		}
		b, _ := json.Marshal(list)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("共 %d 条：%s", len(list), truncateStr(string(b), 3000))

	case "get_settings":
		all, err := s.st.AllSettings()
		if err != nil {
			return fail("读取设置失败：%v", err)
		}
		b, _ := json.Marshal(all)
		step.Detail = string(b)
		step.Summary = truncateStr(string(b), 2500)

	case "save_settings":
		return s.applyAgentSettings(params)

	case "list_keys":
		list, err := s.st.ListAPIKeys()
		if err != nil {
			return fail("读取秘钥失败：%v", err)
		}
		type keyView struct {
			ID     int64    `json:"id"`
			Name   string   `json:"name"`
			Models []string `json:"models"`
			Enabled bool    `json:"enabled"`
		}
		out := make([]keyView, 0, len(list))
		for _, k := range list {
			out = append(out, keyView{k.ID, k.Name, k.Models, k.Enabled})
		}
		b, _ := json.Marshal(out)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("共 %d 把秘钥：%s", len(list), truncateStr(string(b), 1500))

	case "save_key":
		var k store.APIKey
		if err := mapToStruct(params, &k); err != nil {
			return fail("参数不合法：%v", err)
		}
		if k.Name == "" {
			return fail("缺少秘钥名 name")
		}
		saved, err := s.st.SaveAPIKey(k)
		if err != nil {
			return fail("保存秘钥失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已保存秘钥 #%d「%s」", saved.ID, saved.Name)

	case "delete_key":
		id := paramsInt(params, "id")
		if id <= 0 {
			return fail("缺少秘钥 id")
		}
		if !paramsBool(params, "confirm") {
			return fail("删除秘钥是不可逆操作，已拒绝执行——请先向用户说明并征得同意后，携带 confirm:true 重试")
		}
		if err := s.st.DeleteAPIKey(id); err != nil {
			return fail("删除失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已删除秘钥 #%d", id)

	case "list_skills":
		list, err := s.st.ListSkills()
		if err != nil {
			return fail("读取技能失败：%v", err)
		}
		b, _ := json.Marshal(list)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("共 %d 个技能：%s", len(list), truncateStr(string(b), 2000))

	case "save_skill":
		var sk store.Skill
		if err := mapToStruct(params, &sk); err != nil {
			return fail("参数不合法：%v", err)
		}
		if sk.Name == "" {
			return fail("缺少技能名 name")
		}
		saved, err := s.st.SaveSkill(sk)
		if err != nil {
			return fail("保存技能失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已保存技能「%s」（%s）", saved.Name, saved.Capability)

	case "delete_skill":
		id := paramsInt(params, "id")
		if id <= 0 {
			return fail("缺少技能 id")
		}
		if !paramsBool(params, "confirm") {
			return fail("删除技能是不可逆操作，已拒绝执行——请先向用户说明并征得同意后，携带 confirm:true 重试")
		}
		if err := s.st.DeleteSkill(id); err != nil {
			return fail("删除失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已删除技能 #%d", id)

	case "auto_aggregate":
		plan, err := s.autoAggregatePlan()
		if err != nil {
			return fail("生成聚合计划失败：%v", err)
		}
		if !paramsBool(params, "apply") {
			b, _ := json.Marshal(plan)
			step.Detail = string(b)
			step.Summary = fmt.Sprintf("计划：%d 项补声明、%d 个新建（未应用）：%s",
				len(plan.Declares), len(plan.Creates), truncateStr(string(b), 1500))
			return step
		}
		keys := make([]string, 0, len(plan.Declares)+len(plan.Creates))
		for _, d := range plan.Declares {
			keys = append(keys, d.Key)
		}
		for _, c := range plan.Creates {
			keys = append(keys, c.Key)
		}
		applied, err := s.autoAggregateApply(plan, keys)
		if err != nil {
			return fail("应用聚合失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已应用 %d 项聚合配置", applied)

	case "list_logs":
		limit := paramsInt(params, "limit")
		if limit <= 0 {
			limit = 50
		}
		list, err := s.st.ListUsageLogs(int(limit), 0, "", "")
		if err != nil {
			return fail("读取日志失败：%v", err)
		}
		b, _ := json.Marshal(list)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("最近 %d 条日志：%s", len(list), truncateStr(string(b), 2500))

	case "upstream_stats":
		list, err := s.st.ListUpstreamStats()
		if err != nil {
			return fail("读取画像失败：%v", err)
		}
		b, _ := json.Marshal(list)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("共 %d 条画像：%s", len(list), truncateStr(string(b), 2000))

	case "clear_logs":
		if !paramsBool(params, "confirm") {
			return fail("清空调用日志是不可逆操作，已拒绝执行——请先向用户说明并征得同意后，携带 confirm:true 重试")
		}
		if err := s.st.ClearUsageLogs(); err != nil {
			return fail("清空失败：%v", err)
		}
		step.Summary = "已清空全部调用日志"

	default:
		return fail("未知工具：%s", action)
	}
	return step
}

// applyAgentSettings 把声明式 settings 字典热生效到 store。
// 协议合一：模型可在最终 reply 的 settings 字段里直接声明要改的设置项，
// 后端在此一次性写入并触发 reloadOptions / refreshBalance，与对话同时完成。
// 同一个实现也供 save_settings 工具复用，保证两条路径行为一致。
func (s *Server) applyAgentSettings(settings map[string]any) agentStep {
	step := agentStep{Action: "save_settings", Params: settings, OK: true}
	if len(settings) == 0 {
		step.OK = false
		step.Error = "没有要修改的设置项"
		return step
	}
	payload := make(map[string]string, len(settings))
	for k, v := range settings {
		switch t := v.(type) {
		case string:
			payload[k] = t
		case bool, float64:
			payload[k] = fmt.Sprint(t)
		default:
			b, _ := json.Marshal(t)
			payload[k] = string(b)
		}
	}
	for k, v := range payload {
		if err := s.st.SetSetting(k, v); err != nil {
			step.OK = false
			step.Error = fmt.Sprintf("保存设置 %s 失败：%v", k, err)
			return step
		}
	}
	s.reloadOptions()
	s.refreshBalance()
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	step.Summary = "已修改并热生效：" + strings.Join(keys, ", ")
	return step
}
