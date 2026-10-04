package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

const agentSystemPromptBase = `你是「模型中转站」的站内管理智能体，拥有与面板用户相同的管理权限：
查询与创建/修改/删除模型、渠道、秘钥、技能，读取与修改任意设置项，查看日志与上游画像，执行多模态自动聚合。

工作方式：你每轮只能输出一个 JSON 对象（不要 markdown 围栏、不要多余文字）：
1. 需要调用工具时：{"think":"简短思路","action":"工具名","params":{...}} —— 决定调工具就直接输出 action，严禁输出「我将调用某工具」「让我先探测」之类只说不做的 reply。
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
- list_tasks                     {limit(默认20)}             最近视频任务（状态/模型/耗时）
- delete_task                    {task_id,confirm}           删除视频任务记录（不可逆，需 confirm）
- test_provider                  {id}                        探测渠道连通性（逐入口轻量请求，返回状态与模型列表）
- probe_upstream                 {base_url,api_key}          用任意地址+秘钥直接探测 OpenAI 兼容上游（自动规范化 /v1），返回入口地址与模型清单（含能力推断）
- reset_provider                 {id(0=全部)}                复位渠道熔断状态（误熔断后立即恢复用）
- list_health                    {provider_id(可选),limit}    最近健康巡检记录
- self_check                     {}                          配置自省（发现的问题与修复建议）
- test_notify                    {}                          发送测试通知验证告警通道
- feishu_status                  {}                          飞书整合链路状态（凭据/长连接/回复通道）
- export_backup                  {}                          导出全量配置备份到数据目录，返回文件路径

原则：先查再改（改之前用 list/ get 确认现状）；改动尽量小；一次任务里的多个工具按依赖顺序调用；
能用 settings 字段一次完成的设置变更，优先在最终 reply 里用 settings 字段声明，不必再调 save_settings 工具；
删除与清空默认要先征得用户同意（见下方权限模式说明）；对「整合所有模型」类需求用 save_model 建聚合模型。
所有面向用户的文字都用中文。

【一体式接入流程】用户粘贴一个上游服务地址（URL，可能带 /v1 或只有域名）想接入时，走引导式配置，
每步等用户回复再继续，一次只问一件事；最终 reply 可带 "ask" 字段渲染交互卡（用户点选/填写后作为普通消息回来，对话不中断）：
1. 有地址没秘钥：reply 简述将接入该地址（一句话），带 "ask":{"kind":"secret","label":"请粘贴该服务的 API 秘钥","placeholder":"sk-…"}。
2. 拿到秘钥：立刻直接调用 probe_upstream {base_url, api_key}（输出 action，不要先用 reply 预告）；失败时 reply 说明原因并让用户检查秘钥/地址（可再次 ask secret）。
3. 探测成功：reply 概述发现的模型（按能力归类一句话），带 "ask":{"kind":"choice","multi":true,"label":"勾选要接入的模型","options":[模型id,...]}（最多列 30 个，挑常用的放前面）。
4. 用户选完：调 save_provider 建渠道（name 从域名推断如「SiliconFlow」，base_urls 用 probe 返回的规范化地址，api_keys 带秘钥，enabled true），再对所选模型逐个 save_model（capability 按探测推断填，含糊就 chat；inputs 的 upstream_name 用模型在上游的原名）。
   重要：probe_upstream 返回的 models 清单是唯一权威来源，选项卡里的 id 全部来自它——用户从选项卡点选后不要质疑、不要声称「模型在上游不存在」，save_model 的 upstream_name 原样使用用户选中的 id。
5. 全部完成：reply 汇报建了什么，并提示可去「模型」页查看或直接分发秘钥使用。
流程进行中用户问了别的事：先正常回答，再一句话提醒接入进行到哪一步、下一步缺什么，不要丢流程状态。
需要用户重新选择/补充信息时，reply 同样要带对应的 ask 卡，不能只说话不给卡。
ask 只能出现在最终 reply 里（和 action 互斥）；秘钥是敏感信息，reply 里永远不要完整回显。`

// agentSystemPrompt 组装系统提示词：fullAccess 开启时放宽 confirm 协议。
func agentSystemPrompt(fullAccess bool) string {
	if !fullAccess {
		return agentSystemPromptBase
	}
	return agentSystemPromptBase + "\n【当前权限模式：最大权限】删除、清空、复位等不可逆操作无需先征得用户确认，" +
		"直接携带 confirm:true 执行即可，但执行前的 think/reply 里必须用一句话说明将要删除什么、后果是什么。"
}

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

// agentAsk 是最终回复附带的交互引导卡：前端渲染成秘钥输入框/可点选的选项卡。
// 用户点选或提交后作为一条普通用户消息继续对话——流程与闲聊交织，互不阻塞。
type agentAsk struct {
	Kind        string   `json:"kind"`                  // secret=秘钥输入框 choice=选项卡
	Label       string   `json:"label,omitempty"`       // 引导文案（一句话说清要用户提供什么）
	Options     []string `json:"options,omitempty"`     // kind=choice 的候选项
	Multi       bool     `json:"multi,omitempty"`       // choice 是否允许多选
	Placeholder string   `json:"placeholder,omitempty"` // 输入框占位文案
}

type agentResponse struct {
	Reply       string      `json:"reply"`
	Steps       []agentStep `json:"steps"`
	SystemModel string      `json:"system_model"`
	LatencyMS   int64       `json:"latency_ms"`
	Ask         *agentAsk   `json:"ask,omitempty"` // 需要用户下一步输入时附带引导卡
}

type agentTurn struct {
	Think    string         `json:"think"`
	Action   string         `json:"action"`
	Params   map[string]any `json:"params"`
	Reply    string         `json:"reply"`
	Settings map[string]any `json:"settings"`
	Ask      *agentAsk      `json:"ask"`
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
	// 不在这里预检主模型可用性：上游临时挂掉时 agentChatSelfHeal 会自动
	// 裁剪上下文/切换备用模型，比入口直接 502 体验好得多。

	history := make([]relay.ChatMessage, 0, len(req.Messages)+2)
	history = append(history, relay.ChatMessage{Role: "system", Content: agentSystemPrompt(s.agentFullAccess())})
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
	resp, loopErr := s.agentCore(r.Context(), systemModel, history, emitFn)
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
			"ask":          resp.Ask,
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
func (s *Server) agentCore(ctx context.Context, systemModel string, history []relay.ChatMessage, emitFn func(agentStep)) (*agentResponse, error) {
	resp := agentResponse{SystemModel: systemModel, Steps: []agentStep{}}
	start := time.Now()
	emitStep := func(step agentStep) {
		resp.Steps = append(resp.Steps, step)
		if emitFn != nil {
			emitFn(step)
		}
	}
	reply := ""
	wroteTool := false      // 本轮请求是否真正执行过写入类工具（防「口头汇报成功但没动手」）
	doneNudged := false     // 「声称成功」类纠偏只回灌一次，避免与只读任务的真实陈述死磕
	protocolNudged := false // 「未守 JSON 协议」补救只试一次，仍失败就按原文回复避免死循环
	probed := historyProbed(history)
	needChoice := awaitingChoice(history)
	installNudged := false  // 接入流程「探测后未落库」纠偏只催一次
	lastProviderID := int64(0)
	lastProbeModels := probeModelIDsFromHistory(history)

	for round := 0; round < agentMaxRounds; round++ {
		out, usedModel, callErr := s.agentChatSelfHeal(ctx, systemModel, history, emitStep)
		if callErr != nil {
			return nil, fmt.Errorf("系统级 AI 调用失败（含备用模型接力仍失败）：%w", callErr)
		}
		_ = usedModel
		text := extractJSONObject(out.Content)
		var turn agentTurn
		parsed := false
		if text != "" {
			if json.Unmarshal([]byte(text), &turn) == nil && (turn.Action != "" || turn.Reply != "") {
				parsed = true
			} else if json.Unmarshal([]byte(sanitizeJSONControlChars(text)), &turn) == nil && (turn.Action != "" || turn.Reply != "") {
				// 宽松重试：模型常在 think 字符串里带裸换行/Tab，严格模式会拒收，
				// 导致该执行的工具被当成纯文本回复展示。转义字符串内部的控制字符后再解一次。
				parsed = true
			}
		}
		if !parsed {
			// 不守协议：把原文当最终回复，避免死循环。但首轮就「纯文本索要信息/
			// 预告工具」是典型的偷懒输出，回灌一次催促改用 JSON 指令再试；
			// 仍不守协议则按原文回复。
			reply = strings.TrimSpace(out.Content)
			if round < agentMaxRounds-1 && !protocolNudged &&
				(askRequiredRe.MatchString(reply) || replyPromisesRe.MatchString(reply) || doneClaimRe.MatchString(reply)) {
				protocolNudged = true
				history = append(history, relay.ChatMessage{Role: "assistant", Content: out.Content})
				history = append(history, relay.ChatMessage{Role: "user", Content: "[系统提示] 你的上一条回复没有遵循 JSON 指令协议（应为 {\"think\":...,\"action\":...} 或 {\"think\":...,\"reply\":...}）。需要用户提供信息时必须带 ask 字段（kind=secret 或 choice）。请重新输出正确的 JSON 指令。"})
				continue
			}
			break
		}
		// 协议合一：最终回复可携带 settings，一次性完成「对话 + 热生效设置」。
		if len(turn.Settings) > 0 {
			emitStep(s.applyAgentSettings(turn.Settings))
		}

		if turn.Reply != "" && turn.Action == "" {
			// 兜底纠偏：模型偶尔「只说不做」（预告要调工具却不带 action）、
			// 要求用户选择却忘了带 ask 卡、或「声称创建/接入完成」却没调过写入
			// 工具。这类回复不发给用户，回灌系统催促让它在下一轮真正执行——
			// 流程不中断，用户无感。
			if round < agentMaxRounds-1 {
				nudge := replyNudge(turn, len(resp.Steps), wroteTool)
				if nudge != "" && doneClaimRe.MatchString(turn.Reply) {
					if doneNudged {
						nudge = "" // 同类纠偏只催一次，放行避免消耗轮次
					}
					doneNudged = true
				}
				// 结构性兜底：上文已探测成功，本轮却一个写入工具都没调就收尾——
				// 不管措辞如何，接入流程就是没落库，催它把渠道/模型真正建出来。
				if nudge == "" && probed && !wroteTool && !installNudged {
					nudge = "你上文已经完成上游探测，但本轮没有调用任何写入工具，接入流程并未真正落库。" +
						"如果用户已给出秘钥或选好了模型，必须输出 action=save_provider 建立渠道（用 probe 返回的规范化地址与用户给的秘钥），" +
						"再用 save_model 绑定所选模型，然后才汇报完成；如果用户只是要探测结果、不需要建渠道，直接汇报即可。"
					installNudged = true
				}
				if nudge != "" {
					history = append(history, relay.ChatMessage{Role: "assistant", Content: text})
					history = append(history, relay.ChatMessage{Role: "user", Content: "[系统提示] " + nudge + "\n请立即重新输出正确的 JSON 指令。"})
					continue
				}
			}
			reply = turn.Reply
			resp.Ask = turn.Ask // 引导卡随最终答复下发给前端
			// 引导卡兜底：模型只说要秘钥/要选择却没给 ask 时，按上下文自己补一张。
			// 这是「粘贴 URL → 填秘钥 → 勾模型」这条主流程能否走通的关键——
			// 不能因为模型少写一个字段就把用户卡死在半路。
			if fixed := s.repairAsk(turn.Ask, turn.Reply, lastProbeModels, needChoice, probed, wroteTool); fixed != nil {
				resp.Ask = fixed
			}
			// 最终回复留痕：排查「模型说了什么却没做/卡在哪」时最需要的就是这段。
			askInfo := "nil"
			if resp.Ask != nil {
				askInfo = fmt.Sprintf("kind=%s options=%d multi=%v", resp.Ask.Kind, len(resp.Ask.Options), resp.Ask.Multi)
			}
			log.Printf("[agent] final reply ask=[%s] rawAsk=%q reply=%.160s", askInfo, rawAskKind(turn.Ask), turn.Reply)
			break
		}

		// 流程硬约束：刚收到秘钥这一轮不许直接建渠道/存模型，先把模型清单
		// 做成 choice 引导卡交给用户勾选。模型自己跳过这一步等于剥夺用户选择权。
		if needChoice && (turn.Action == "save_provider" || turn.Action == "save_model") {
			step := agentStep{
				Action: turn.Action, Params: turn.Params, OK: false,
				Error: "用户刚提供秘钥，本轮禁止直接落库：请先把探测到的模型做成 choice 引导卡（多选）让用户勾选，" +
					"等用户提交选择后再调用 save_provider / save_model。",
			}
			log.Printf("[agent] round %d blocked action=%s (等待用户选择模型)", round, turn.Action)
			emitStep(step)
			history = append(history, relay.ChatMessage{Role: "assistant", Content: text})
			history = append(history, relay.ChatMessage{
				Role:    "user",
				Content: "[工具结果] " + step.Error + "\n请改为输出带 ask（kind=choice, multi=true）的 reply，options 用 probe 返回的模型清单。",
			})
			continue
		}

		// 先纠正参数再执行：模型接着调 save_model 时常把 provider_id 填错
		// （旧 id、字符串、拍脑袋的数字），一律纠正成它刚建好的那个渠道。
		if turn.Action == "save_model" && lastProviderID > 0 {
			repairProviderIDs(turn.Params, lastProviderID)
		}

		step := s.agentExec(turn.Action, turn.Params)
		if isWriteAction(turn.Action) && step.OK {
			wroteTool = true
		}
		// 记住本轮刚建好的渠道 id，供后续 save_model 绑定使用。
		if turn.Action == "save_provider" && step.OK {
			if m := savedProviderIDRe.FindStringSubmatch(step.Summary); len(m) == 2 {
				if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					lastProviderID = id
				}
			}
		}
		// 探测成功的模型清单是后续「让用户勾选」的唯一权威来源：
		// 既用于纠正模型编造的选项，也用于模型没给卡时兜底补卡。
		if turn.Action == "probe_upstream" && step.OK {
			if ids := probeModelIDs(step.Detail); len(ids) > 0 {
				lastProbeModels = ids
			}
		}
		log.Printf("[agent] round %d action=%s ok=%v err=%q", round, turn.Action, step.OK, step.Error)
		emitStep(step)

		// 工具结果回灌：assistant 的指令 + user 角色的结果标注。
		// 回灌完整结果（agentResultText，优先 Detail 全量 JSON），此前只回
		// 截断版 Summary（1.5k~3k 字符），列表类工具的结果模型永远看不全，
		// 导致它基于残缺数据继续决策或反复重查。
		history = append(history, relay.ChatMessage{Role: "assistant", Content: text})
		resultJSON, _ := json.Marshal(map[string]any{
			"ok":      step.OK,
			"summary": step.Summary,
			"result":  agentResultText(step),
			"error":   step.Error,
		})
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

// agentChatSelfHeal 带自愈的主模型调用，解决「调用中途上游挂了/上下文超限导致整轮失败」：
//  1. 常规调用失败且历史偏长 → 裁剪历史（保留 system + 最近若干条）后用原模型重试一次；
//  2. 仍失败 → 在已启用且支持对话的模型里挑备用模型接力（最多 2 个），命中上下文类错误时
//     同样先裁剪再发。
//
// 每次降级都发一条 failover 轨迹，前端时间线可见；全部失败才把最后一个错误抛回循环。
// 只服务 Agent 自身（/v1 对外的模型是调用方指定的，不能擅自换）。
func (s *Server) agentChatSelfHeal(ctx context.Context, systemModel string, history []relay.ChatMessage, emitStep func(agentStep)) (*relay.ChatOutput, string, error) {
	try := func(modelName string, msgs []relay.ChatMessage) (*relay.ChatOutput, error) {
		m, ps, err := s.eng.ResolveAll(modelName)
		if err != nil {
			return nil, err
		}
		in := &relay.ChatInput{
			Model:    modelName,
			Messages: msgs,
			Options:  map[string]any{"temperature": 0.2},
			Raw:      map[string]any{},
		}
		// 单轮主模型调用有独立上限：全局超时是给长流式请求用的（默认 600s），
		// 拿来当 agent 的一轮预算等于「界面卡十分钟」。挂起时尽快失败，
		// 让后面的整理上下文重试 / 备用模型接力有机会生效。
		callCtx := ctx
		if ctx != nil {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, agentCallBudget())
			defer cancel()
		}
		out, _, err := s.eng.Chat(callCtx, m, ps, in, nil)
		return out, err
	}

	out, err := try(systemModel, history)
	if err == nil {
		return out, systemModel, nil
	}

	// 1) 整理上下文重试：历史够长才值得裁，短历史重试也省不出多少。
	if compacted := compactAgentHistory(history, 12); len(compacted) < len(history) {
		emitStep(agentStep{Action: "failover", OK: true,
			Summary: fmt.Sprintf("主模型「%s」调用失败（%s），已整理上下文 %d→%d 条重试",
				systemModel, truncateStr(err.Error(), 120), len(history), len(compacted))})
		if out2, err2 := try(systemModel, compacted); err2 == nil {
			return out2, systemModel, nil
		} else {
			err = err2
		}
	}

	// 2) 备用模型接力：启用的对话模型（排除当前主模型），最多试 2 个。
	list, lerr := s.st.ListModels()
	if lerr != nil {
		return nil, systemModel, err
	}
	msgs := history
	if ctxOverflowRe.MatchString(err.Error()) {
		msgs = compactAgentHistory(history, 12)
	}
	tried := 0
	for _, mm := range list {
		if tried >= 2 {
			break
		}
		if !mm.Enabled || (mm.Capability != "" && mm.Capability != store.CapChat) || mm.Name == systemModel {
			continue
		}
		tried++
		emitStep(agentStep{Action: "failover", OK: true,
			Summary: fmt.Sprintf("切换备用模型「%s」重试（原模型错误：%s）", mm.Name, truncateStr(err.Error(), 120))})
		if out3, err3 := try(mm.Name, msgs); err3 == nil {
			return out3, mm.Name, nil
		} else {
			err = err3
		}
	}
	return nil, systemModel, err
}

// ctxOverflowRe 识别上下文长度类错误（各家措辞不一，中英都收）。
var ctxOverflowRe = regexp.MustCompile(`(?i)(context.{0,16}(length|window|overflow|exceed)|maximum context|too many tokens|token limit|prompt is too long|input is too long|length exceed|上下文(长度|超限|过长|溢出)|令牌(超出|超限)|超出.*上下文|请求体过长)`)

// agentCallBudgetSec 是一轮主模型调用的时间上限（秒）。超时即视为这次失败，
// 交给自愈逻辑重试或切备用模型，避免用户在界面上干等。
const agentCallBudgetSec = 90

func agentCallBudget() time.Duration {
	return time.Duration(agentCallBudgetSec) * time.Second
}

// compactAgentHistory 裁剪对话历史：保留首条（system 提示词）与最近 keep 条，
// 并追加一条裁剪说明让模型知道上文被截断过。历史不够长时原样返回。
func compactAgentHistory(history []relay.ChatMessage, keep int) []relay.ChatMessage {
	if len(history) <= keep+2 {
		return history
	}
	merged := make([]relay.ChatMessage, 0, keep+3)
	merged = append(merged, history[0])
	merged = append(merged, history[len(history)-keep:]...)
	merged = append(merged, relay.ChatMessage{Role: "user",
		Content: "[系统提示] 因上下文长度限制，早前对话已裁剪；请基于以上最近上下文继续当前任务。"})
	return merged
}

// agentResultText 组装回灌给主模型的工具结果文本：优先完整 Detail（各工具的
// 全量 JSON），没有再用人类可读的 Summary。带 20k 字符保护性截断——超过说明
// 该让工具缩小查询范围，而不是默默喂半截数据让模型瞎猜。
func agentResultText(step agentStep) string {
	text := strings.TrimSpace(step.Detail)
	if text == "" {
		text = step.Summary
	}
	const agentResultMaxRunes = 20000
	if r := []rune(text); len(r) > agentResultMaxRunes {
		text = string(r[:agentResultMaxRunes]) + "\n…(结果过长已截断：请换用更精确的查询或分页获取，不要基于不完整数据下结论)"
	}
	return text
}

// replyPromisesRe 命中「预告式回复」：嘴上说正在/将要调用工具，实际没带 action。
var replyPromisesRe = regexp.MustCompile(`正在(探测|调用|执行|获取|检查)|将(通过|调用|执行|先|立即)|让我(来|先)|我先(调用|探测|查看|确认)|接下来(调用|探测|执行)`)

// askRequiredRe 命中「向用户索要输入但应带 ask 卡」的回复。
// 覆盖「粘贴/提供/填 Key」以及「把 Key 给我」这类更口语的说法。
var askRequiredRe = regexp.MustCompile(`请(您)?(勾选|选择|挑选|粘贴|提供|输入|填写|确认|核对|告知|回复|发|给)|把.{0,6}(秘钥|密钥|key|Key|KEY).{0,4}(给我|发我|贴上|提供)|需要.{0,4}(秘钥|密钥|api[_ ]?key)`)

// failClaimRe 命中「声称写操作失败」的回复（防止模型臆测失败而不实际调用工具）。
var failClaimRe = regexp.MustCompile(`(保存|创建|写入|删除|修改|接入|同步)(模型|渠道|秘钥|设置)?失败`)

// doneClaimRe 命中「声称写操作已完成」的回复（防止口头汇报成功但实际没调写入工具）。
var doneClaimRe = regexp.MustCompile(`(已|已经|成功|完成)(创建|接入|添加|保存|配置|写入)|(创建|接入|添加|保存|配置|写入)(完成|成功|好了?)`)

// isWriteAction 判断是否为会改动数据的写入类工具。
func isWriteAction(a string) bool {
	switch a {
	case "save_provider", "save_model", "save_key", "save_skill", "save_settings":
		return true
	}
	return false
}

// normalizeProviderIDs 归一化 save_model 参数里的渠道 id：
// 模型常把 id 写成字符串（"26"），这里直接纠正为数字；
// 同时返回无效/缺失的 id 列表（0 表示没填），供错误提示回灌。
func (s *Server) normalizeProviderIDs(params map[string]any) []int64 {
	list, _ := params["inputs"].([]any)
	bad := []int64{}
	for _, it := range list {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if sv, isStr := obj["provider_id"].(string); isStr {
			if n, err := strconv.ParseInt(strings.TrimSpace(sv), 10, 64); err == nil {
				obj["provider_id"] = float64(n)
			}
		}
		id, _ := obj["provider_id"].(float64)
		switch {
		case id <= 0:
			bad = append(bad, 0)
		default:
			if _, err := s.st.GetProvider(int64(id)); err != nil {
				bad = append(bad, int64(id))
			}
		}
	}
	return bad
}

// providerIDList 列出当前可用的渠道 id 与名字，最多 30 条，
// 让模型在绑定失败时能直接拿到正确的 id 去重试。
func (s *Server) providerIDList() string {
	list, err := s.st.ListProviders()
	if err != nil {
		return "（读取失败：" + err.Error() + "）"
	}
	parts := make([]string, 0, len(list))
	for i, p := range list {
		if i >= 30 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%d=%s", p.ID, p.Name))
	}
	if len(parts) == 0 {
		return "（一个渠道都没有）"
	}
	return strings.Join(parts, "、")
}

// probeModelIDs 从 probe_upstream 的 Detail JSON 里取出模型 id 清单。
func probeModelIDs(detail string) []string {
	if strings.TrimSpace(detail) == "" {
		return nil
	}
	var res probeResult
	if err := json.Unmarshal([]byte(detail), &res); err != nil {
		return nil
	}
	return modelIDsOf(res.Models)
}

// probeModelIDsFromHistory 从历史里的探测结果回捞模型清单：
// 探测发生在上一轮请求时，本轮的 history 仍带着那份工具结果。
func probeModelIDsFromHistory(history []relay.ChatMessage) []string {
	const key = `"models"`
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		// 工具结果是以 [工具结果] 开头的 user 消息，探测清单就在其中。
		if msg.Role != "user" || !strings.Contains(msg.Content, "[工具结果]") {
			continue
		}
		idx := strings.Index(msg.Content, key)
		if idx < 0 {
			continue
		}
		// 工具结果是一大段 JSON，这里只截出 models 数组本身（括号配平，跳过字符串内的括号）。
		rest := msg.Content[idx+len(key):]
		start := strings.Index(rest, "[")
		if start < 0 {
			continue
		}
		if arr, ok := matchJSONArray(rest[start:]); ok {
			var models []probeModel
			if json.Unmarshal([]byte(arr), &models) == nil && len(models) > 0 {
				return modelIDsOf(models)
			}
		}
	}
	return nil
}

// matchJSONArray 从 s 开头截出一个完整的 JSON 数组字面量。
func matchJSONArray(s string) (string, bool) {
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[:i+1], true
			}
		}
	}
	return "", false
}

func modelIDsOf(models []probeModel) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// agentAsk 之外，这里再定义补卡用的判定辅助。
//
// repairAsk 给模型的引导卡做三件事：
//  1. 模型说要秘钥却没给卡 → 补一张 secret 卡（粘贴 URL 后的第一道门）；
//  2. 模型说让用户选模型却没给卡，或给了空/对不上的选项 → 用最近一次探测到的
//     模型清单补一张 multi choice 卡（探测结果才是唯一权威来源）；
//  3. 已经带了能用的卡 → 原样返回 nil，不干预。
func (s *Server) repairAsk(ask *agentAsk, reply string, probeModels []string, needChoice, probed, wrote bool) *agentAsk {
	// 已经真动过手、并且汇报的是"完成"：这是终局答复，绝不能再挂引导卡
	//（否则用户会看到"接入完成"下面还摆着一个要填秘钥的框）。
	if ask == nil && wrote && doneClaimRe.MatchString(reply) {
		return nil
	}
	// 归一化 kind：模型有时写成 select/choice_list/input 之类，前端只认
	// secret/choice，值不对整张卡就渲染不出来（表现为流程"卡住不动"）。
	if ask != nil {
		switch strings.ToLower(strings.TrimSpace(ask.Kind)) {
		case "secret":
			ask.Kind = "secret"
		case "choice":
			ask.Kind = "choice"
		case "":
			// 缺 kind：按上下文补一个（下面统一处理）
		default:
			if len(probeModels) > 0 {
				ask.Kind = "choice"
			} else {
				ask.Kind = "secret"
			}
		}
	}
	if ask != nil && ask.Kind == "choice" && len(probeModels) > 0 {
		// 选项与探测清单不符时以清单为准：探测结果才是唯一权威来源。
		if !sameOptions(ask.Options, probeModels) {
			ask.Options = probeModels
			ask.Multi = true
			ask.Label = firstNonEmptyStr(ask.Label, "选择要接入的模型")
		}
		return ask
	}
	if ask != nil && ask.Kind == "choice" && len(probeModels) == 0 {
		// 没有探测结果却给出选项卡：这些选项一定是模型凭空编的（用户也还没给秘钥，
		// 根本无从探测）。丢掉它，直接改问秘钥，让流程回到正确的起点。
		return &agentAsk{Kind: "secret", Label: "粘贴该服务的 API 秘钥", Placeholder: "sk-…"}
	}
	if ask != nil && ask.Kind == "secret" {
		return ask
	}
	// 没有可用的卡：按当前该要什么来补。
	if needChoice && len(probeModels) > 0 {
		return &agentAsk{
			Kind: "choice", Multi: true, Label: "选择要接入的模型",
			Options: probeModels,
		}
	}
	if wantSecretRe.MatchString(reply) {
		return &agentAsk{Kind: "secret", Label: "粘贴该服务的 API 秘钥", Placeholder: "sk-…"}
	}
	// 兜底：回复没有实质内容（空、只有引号或标点）——实测模型偶尔会回一句
	// `""` 之类的退化内容，用户就卡在"没有下一步可点"的死路上。
	// 只要上下文明显处在接入流程，就给一张能推进的卡。
	if isDegenerateReply(reply) && (len(probeModels) > 0 || needChoice || probed) {
		if len(probeModels) > 0 && needChoice {
			return &agentAsk{Kind: "choice", Multi: true, Label: "选择要接入的模型", Options: probeModels}
		}
		return &agentAsk{Kind: "secret", Label: "粘贴该服务的 API 秘钥", Placeholder: "sk-…"}
	}
	return nil
}

// isDegenerateReply 判断回复是否没有实质内容：去掉空白、引号、常见标点后几乎为空。
func isDegenerateReply(reply string) bool {
	cleaned := strings.TrimSpace(reply)
	cleaned = strings.Trim(cleaned, "\"'“”「」『』` \t\r\n")
	cleaned = strings.Trim(cleaned, "。.，,、；;：:-—…!！?？")
	return len([]rune(strings.TrimSpace(cleaned))) < 4
}

// sameOptions 判断模型给的选项是否已经覆盖了探测清单（顺序无关、忽略大小写）。
func sameOptions(got, want []string) bool {
	if len(got) == 0 {
		return false
	}
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[strings.ToLower(strings.TrimSpace(g))] = true
	}
	for _, w := range want {
		if !set[strings.ToLower(strings.TrimSpace(w))] {
			return false
		}
	}
	return true
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// wantSecretRe 判断回复是否在向用户要秘钥（用于补 secret 引导卡）。
var wantSecretRe = regexp.MustCompile(`(?i)(秘钥|密钥|api[\s_-]?key|token)`)

// rawAskKind 供日志用：打印模型原始给的 ask.kind（归一化前的值）。
func rawAskKind(a *agentAsk) string {
	if a == nil {
		return ""
	}
	return a.Kind
}

// savedProviderIDRe 从 save_provider 的成功摘要里抠出新建渠道的 id。
var savedProviderIDRe = regexp.MustCompile(`已保存渠道 #(\d+)`)

// secretHandoffRe 判断用户这条消息是不是「刚把秘钥交过来」。
// 引导卡的秘钥提交会带上"秘钥："前缀，正是探测前的那个交接点。
var secretHandoffRe = regexp.MustCompile(`^秘钥[:：]\s*\S+`)

// autoAllRe 用户明确说了"全都要/直接接入/不用问我"，此时不该再挡选择卡。
var autoAllRe = regexp.MustCompile(`全部|全都|所有模型|直接(接入|创建|保存)|不用问|自动(接入|创建)`)

// awaitingChoice 判断本轮是否处于「刚拿到秘钥、还没让用户选模型」的阶段。
// 模型常在这时候自作主张跳过选项卡直接建渠道+存模型，用户失去了选择权；
// 这里按流程强制：必须先出 choice 引导卡，收到用户选择后才允许落库。
func awaitingChoice(history []relay.ChatMessage) bool {
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	if last.Role != "user" || !secretHandoffRe.MatchString(strings.TrimSpace(last.Content)) {
		return false
	}
	return !autoAllRe.MatchString(last.Content)
}

// repairProviderIDs 把 save_model 参数里明显不存在的 provider_id 纠正为
// 本轮刚创建的渠道 id。模型拿到的是 save_provider 的文本摘要，填 id 极易
// 出错（沿用旧 id、写成字符串、凭空数字）；与其反复报错让它重试，不如
// 直接按「刚建好的那个渠道」纠正——一体式接入流程的语义就是这么唯一。
func repairProviderIDs(params map[string]any, providerID int64) {
	if providerID <= 0 {
		return
	}
	list, _ := params["inputs"].([]any)
	for _, it := range list {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		bad := true
		switch v := obj["provider_id"].(type) {
		case float64:
			bad = v != float64(providerID)
		case string:
			bad = true
		default:
			bad = true
		}
		if bad {
			obj["provider_id"] = float64(providerID)
		}
	}
	if len(list) == 0 {
		// 干脆没给 inputs：补一条指向新渠道的上游，省得它再试错。
		params["inputs"] = []any{map[string]any{"provider_id": float64(providerID)}}
	}
}

// historyProbed 判断历史里是否已经发生过 probe_upstream 探测。
// 探测成功意味着「一体式接入流程」已经进入建渠道阶段：此时若模型直接
// 给出最终回复却没调任何写入工具，无论它措辞如何（"已为你接入"、
// "接入完成"……），都属于只说不做，必须回灌催它真正落库。
func historyProbed(history []relay.ChatMessage) bool {
	for _, msg := range history {
		if strings.Contains(msg.Content, "probe_upstream") {
			return true
		}
	}
	return false
}

// replyNudge 判断最终回复是否需要纠偏回灌，返回催促文案（不需要则返回空串）。
// stepsSoFar 是本轮请求已执行的工具步数：声称失败但一步没调，必然是臆测。
// wrote 表示本轮请求是否真正执行过写入类工具：声称「已创建/接入完成」却没动手，同样是臆测。
func replyNudge(turn agentTurn, stepsSoFar int, wrote bool) string {
	switch {
	case failClaimRe.MatchString(turn.Reply) && stepsSoFar == 0:
		return "你声称操作失败，但本轮还没有调用过任何工具，这是臆测。请立刻实际调用对应工具（输出 action），并根据真实执行结果回复。"
	case doneClaimRe.MatchString(turn.Reply) && !wrote:
		return "你声称已创建/接入/保存完成，但本轮从未执行过任何写入类工具（save_provider/save_model/save_key/save_skill/save_settings），这是臆测。请立刻实际调用对应工具完成操作，再根据真实执行结果回复。"
	case replyPromisesRe.MatchString(turn.Reply):
		return "你的回复只是预告了要调用工具（或正在执行），但没有真正调用。请立刻输出 {\"think\":...,\"action\":\"工具名\",\"params\":{...}} 执行该操作，不要再用 reply 预告。"
	case turn.Ask == nil && askRequiredRe.MatchString(turn.Reply):
		return "你在要求用户提供信息或做选择，但没有附带 ask 引导卡。请重新输出 reply 并带上 ask 字段（kind=secret 或 choice，choice 的 options 必须来自上文工具结果里的清单）。"
	}
	return ""
}

// sanitizeJSONControlChars 转义 JSON 字符串内部的裸控制字符（\n/\r/\t）。
// 只在「当前位于字符串内」时替换：用未转义的引号切换进出字符串状态，
// 结构位置的换行是合法空白、原样保留。修复模型 think 里带换行导致整条指令解析失败。
func sanitizeJSONControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inStr, esc := false, false
	for _, r := range s {
		if !inStr {
			if r == '"' {
				inStr = true
			}
			b.WriteRune(r)
			continue
		}
		if esc {
			esc = false
			b.WriteRune(r)
			continue
		}
		switch r {
		case '\\':
			esc = true
			b.WriteRune(r)
		case '"':
			inStr = false
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			// 直接丢弃
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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

// agentFullAccess 是否开启「最大权限」：删除/清空类操作免 confirm 直接执行。
func (s *Server) agentFullAccess() bool {
	return s.st.GetSetting(SettingAgentFullAccess, "off") == "on"
}

// confirmOK 判定不可逆操作是否放行：显式 confirm=true，或已开启最大权限。
func (s *Server) confirmOK(params map[string]any) bool {
	return paramsBool(params, "confirm") || s.agentFullAccess()
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
		// 先归一化并校验 inputs 里的渠道 id：模型常把 id 写成字符串，
		// 或干脆填一个不存在的 id。报错时把可用渠道列出来，它下一轮能自纠。
		if bad := s.normalizeProviderIDs(params); len(bad) > 0 {
			return fail("绑定的渠道 id 不存在或未填：%v。当前可用渠道：%s", bad, s.providerIDList())
		}
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
		// 接入幂等：同名模型已存在时不新建（会撞 UNIQUE(name)），而是把新渠道
		// 并入它的上游列表——多一路故障转移，原有配置原样保留。
		if m.ID == 0 {
			if existing, err := s.st.GetModelByName(m.Name); err == nil && existing.ID > 0 {
				merged := append([]store.ModelInput{}, existing.Inputs...)
				merged = append(merged, m.Inputs...)
				existing.Inputs = merged
				m = existing
			}
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
		if !s.confirmOK(params) {
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
			ID      int64    `json:"id"`
			Name    string   `json:"name"`
			Models  []string `json:"models"`
			Enabled bool     `json:"enabled"`
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
		if !s.confirmOK(params) {
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
		if !s.confirmOK(params) {
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
		if !s.confirmOK(params) {
			return fail("清空调用日志是不可逆操作，已拒绝执行——请先向用户说明并征得同意后，携带 confirm:true 重试")
		}
		if err := s.st.ClearUsageLogs(); err != nil {
			return fail("清空失败：%v", err)
		}
		step.Summary = "已清空全部调用日志"

	// ---------- 最大权限扩展工具：任务 / 渠道探测 / 巡检 / 自省 / 通知 / 飞书 / 备份 ----------

	case "list_tasks":
		limit := paramsInt(params, "limit")
		if limit <= 0 {
			limit = 20
		}
		list, err := s.st.ListVideoTasks(int(limit))
		if err != nil {
			return fail("读取任务失败：%v", err)
		}
		out := make([]map[string]any, 0, len(list))
		for _, t := range list {
			out = append(out, s.taskResponse(t))
		}
		b, _ := json.Marshal(out)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("最近 %d 个视频任务：%s", len(list), truncateStr(string(b), 2000))

	case "delete_task":
		tid := strings.TrimSpace(fmt.Sprint(params["task_id"]))
		if tid == "" || tid == "<nil>" {
			return fail("缺少任务 task_id（可先用 list_tasks 查询）")
		}
		task, err := s.st.GetVideoTaskByTaskID(tid)
		if err != nil {
			return fail("任务不存在：%s", tid)
		}
		if !s.confirmOK(params) {
			return fail("删除任务「%s」是不可逆操作，已拒绝执行——请先向用户说明并征得同意后，携带 confirm:true 重试", tid)
		}
		if err := s.st.DeleteVideoTask(task.ID); err != nil {
			return fail("删除失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已删除视频任务「%s」", tid)

	case "test_provider":
		id := paramsInt(params, "id")
		if id <= 0 {
			return fail("缺少渠道 id（可先用 list_providers 查询）")
		}
		p, err := s.st.GetProvider(id)
		if err != nil {
			return fail("渠道不存在：#%d", id)
		}
		urls := p.BaseURLs
		if len(urls) == 0 {
			if def := relay.DefaultBaseURL(p.Kind); def != "" {
				urls = []string{def}
			}
		}
		if len(urls) == 0 {
			return fail("渠道「%s」没有可探测的入口地址", p.Name)
		}
		key := ""
		if len(p.APIKeys) > 0 {
			key = p.APIKeys[0]
		}
		pctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		results := make([]probeResult, 0, len(urls))
		for _, raw := range urls {
			results = append(results, probeUpstream(pctx, probeClient, p.Kind, raw, key, p.NoVersionPath))
		}
		b, _ := json.Marshal(results)
		step.Detail = string(b)
		okN := 0
		for _, r := range results {
			if r.OK {
				okN++
			}
		}
		step.Summary = fmt.Sprintf("渠道「%s」探测完成：%d/%d 个入口连通", p.Name, okN, len(results))

	case "probe_upstream":
		// 引导式接入的核心工具：用用户给的地址+秘钥直接探测 OpenAI 兼容上游，
		// 返回规范化后的入口与可用模型清单（含能力推断），供后续 save_provider/save_model。
		baseURL := paramsString(params, "base_url")
		apiKey := paramsString(params, "api_key")
		kind := paramsString(params, "kind")
		if baseURL == "" {
			return fail("缺少 base_url（用户消息里提取的接入地址）")
		}
		if kind == "" {
			kind = store.KindOpenAI
		}
		pctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		result := probeUpstream(pctx, probeClient, kind, baseURL, apiKey, false)
		if !result.OK {
			return fail("探测失败（HTTP %d）：%s", result.Status, result.Detail)
		}
		b, _ := json.Marshal(result)
		step.Detail = string(b)
		// 秘钥不出现在结果里；规范化地址放进 summary 供模型直接引用
		step.Summary = fmt.Sprintf("探测成功：%s，%d ms，发现 %d 个模型", result.URL, result.Latency, len(result.Models))

	case "reset_provider":
		id := paramsInt(params, "id")
		if id < 0 {
			return fail("参数不合法：id 应为渠道 id（0 表示全部）")
		}
		s.eng.ResetHealth(id)
		if id == 0 {
			step.Summary = "已复位全部渠道熔断状态"
		} else {
			step.Summary = fmt.Sprintf("已复位渠道 #%d 熔断状态", id)
		}

	case "list_health":
		id := paramsInt(params, "provider_id")
		limit := paramsInt(params, "limit")
		if limit <= 0 {
			limit = 30
		}
		checks, err := s.st.RecentHealthChecks(id, int(limit))
		if err != nil {
			return fail("读取巡检记录失败：%v", err)
		}
		b, _ := json.Marshal(checks)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("最近 %d 条巡检记录：%s", len(checks), truncateStr(string(b), 2000))

	case "self_check":
		res := s.RunSelfCheck()
		b, _ := json.Marshal(res)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("自检完成：%d 个问题、%d 个提醒（共 %d 项检查）", res.Error, res.Warn, res.OK+res.Warn+res.Error)

	case "test_notify":
		ok, channels, err := s.SendTestNotification(context.Background())
		if err != nil {
			return fail("发送测试通知失败：%v", err)
		}
		if ok == 0 {
			return fail("所有通道都推送失败（%s），请检查告警配置，详情见服务端日志", strings.Join(channels, "、"))
		}
		step.Summary = fmt.Sprintf("测试通知已发送到 %d 个通道（%s）", ok, strings.Join(channels, "、"))

	case "feishu_status":
		wsConnected, wsErr := s.feishuWSStatus()
		st := map[string]any{
			"enabled":        s.st.GetSetting(SettingBridgeEnabled, "") == "on",
			"transport":      s.st.GetSetting(SettingBridgeTransport, SettingBridgeTransportWS),
			"credentials_ok": strings.TrimSpace(s.st.GetSetting(SettingBridgeAppID, "")) != "" && s.st.GetSetting(SettingBridgeAppSecret, "") != "",
			"ws_connected":   wsConnected,
			"ws_last_error":  wsErr,
			"mode":           s.st.GetSetting(SettingBridgeMode, "chat"),
			"agent_ready":    s.st.GetSetting(SettingSystemModel, "") != "",
			"chat_ready":     s.st.GetSetting(SettingBridgeModel, "") != "",
			"notify_chat_id": strings.TrimSpace(s.st.GetSetting(SettingNotifyFeishuChatID, "")),
			"alert_enabled":  s.st.GetSetting(SettingAlertEnabled, "") == "on",
			"report_enabled": s.st.GetSetting(SettingReportEnabled, "") == "on",
		}
		b, _ := json.Marshal(st)
		step.Detail = string(b)
		step.Summary = fmt.Sprintf("飞书整合：启用=%v 接入=%s 长连接=%v 模式=%s", st["enabled"], st["transport"], wsConnected, st["mode"])

	case "export_backup":
		backup, err := s.st.ExportAll()
		if err != nil {
			return fail("导出失败：%v", err)
		}
		b, _ := json.MarshalIndent(backup, "", "  ")
		name := "backup-agent-" + time.Now().Format("20060102-150405") + ".json"
		path := filepath.Join(s.cfg.DataDir, name)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return fail("写入备份文件失败：%v", err)
		}
		step.Summary = fmt.Sprintf("已导出全量配置备份到数据目录：%s（%d 字节）", name, len(b))

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
