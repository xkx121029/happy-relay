package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/store"
)

// ---------- 用户门户 ----------
//
// 面板本身是管理员页面；这套端点面向「持秘钥的最终用户」：
// 用对外秘钥（sk-...）认证，只能看到与这把秘钥相关的配额、模型与调用记录。
// 认证方式：Authorization: Bearer <key> 或 X-API-Key: <key>，与 /v1 一致。

// userKey 从请求里解析并校验秘钥：存在、启用、未过期。
func (s *Server) userKey(r *http.Request) (store.APIKey, error) {
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if key == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		key = strings.TrimPrefix(auth, "Bearer ")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return store.APIKey{}, fmt.Errorf("缺少秘钥：请在请求头携带 Authorization: Bearer <你的秘钥>")
	}
	k, err := s.st.FindAPIKey(key)
	if err != nil {
		return store.APIKey{}, fmt.Errorf("秘钥无效或不存在")
	}
	if !k.Enabled {
		return store.APIKey{}, fmt.Errorf("秘钥已被禁用，请联系管理员")
	}
	if t := strings.TrimSpace(k.ExpiresAt); t != "" {
		if exp, err := time.Parse(time.RFC3339, t); err == nil && time.Now().After(exp) {
			return store.APIKey{}, fmt.Errorf("秘钥已于 %s 过期，请联系管理员", exp.Format("2006-01-02"))
		}
	}
	return k, nil
}

func (s *Server) userKeyErr(w http.ResponseWriter, err error) {
	writeErr(w, http.StatusUnauthorized, "invalid_request_error", err.Error())
}

// handleUserMe 当前秘钥的账号信息与今日用量。
// handleUserPublic 门户公开信息：**不需要秘钥**，任何人打开门户都能看到服务本身的样子。
//
// 只暴露"门面"信息：服务名、可选模型的能力分布、公告、以及"要调用请先拿秘钥"的提示。
// 严格不含任何敏感内容：秘钥、渠道地址与名称、调用记录、用量、内部统计一律不出现在这里，
// 拿不到秘钥的用户就算知道这个地址也调不动任何模型。
func (s *Server) handleUserPublic(w http.ResponseWriter, r *http.Request) {
	// 门户公告（管理员在门户里写的公开说明），没有就留空。
	notice := s.st.GetSetting(SettingPortalNotice, "")

	// 能力分布：只统计"对外可用"的模型，按能力归类计数。
	type capCount struct {
		Capability string `json:"capability"`
		Count      int    `json:"count"`
	}
	byCap := map[string]int{}
	total := 0
	models, err := s.st.ListModels()
	if err == nil {
		for _, m := range models {
			if !m.Enabled {
				continue
			}
			cap := m.Capability
			if cap == "" {
				cap = store.CapChat
			}
			byCap[cap]++
			total++
		}
	}
	caps := make([]capCount, 0, len(byCap))
	for c, n := range byCap {
		caps = append(caps, capCount{Capability: c, Count: n})
	}
	// 稳定排序：对话优先，其次按数量倒序。
	sort.Slice(caps, func(i, j int) bool {
		if caps[i].Capability == store.CapChat {
			return true
		}
		if caps[j].Capability == store.CapChat {
			return false
		}
		return caps[i].Count > caps[j].Count
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"service":       s.panelTitle(),
		"notice":        notice,
		"total_models":  total,
		"capabilities":  caps,
		"chat_endpoint": "/v1/chat/completions",
		"image_endpoint": "/v1/images/generations",
		"base_path":     "/v1",
		"docs":          "兼容 OpenAI 官方 SDK：把 base_url 指向本服务，api_key 填秘钥即可。",
	})
}

// panelTitle 返回面板标题（设置页可改），没有则用默认值。
func (s *Server) panelTitle() string {
	if t := strings.TrimSpace(s.st.GetSetting("panel_title", "")); t != "" {
		return t
	}
	return "模型中转站"
}

func (s *Server) handleUserMe(w http.ResponseWriter, r *http.Request) {
	k, err := s.userKey(r)
	if err != nil {
		s.userKeyErr(w, err)
		return
	}
	calls, tokens, _ := s.st.APIKeyDailyUsage(k.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         k.Name,
		"models":       k.Models,
		"enabled":      k.Enabled,
		"expires_at":   k.ExpiresAt,
		"remark":       k.Remark,
		"rpm":          k.RPM,
		"daily_quota":  k.DailyQuota,
		"daily_tokens": k.DailyTokens,
		"today_calls":  calls,
		"today_tokens": tokens,
		"last_used_at": k.LastUsedAt,
	})
}

// handleUserModels 这把秘钥可用的对外模型列表。
func (s *Server) handleUserModels(w http.ResponseWriter, r *http.Request) {
	k, err := s.userKey(r)
	if err != nil {
		s.userKeyErr(w, err)
		return
	}
	list, err := s.st.ListModels()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	allowed := map[string]bool{}
	for _, m := range k.Models {
		allowed[m] = true
	}
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		if !m.Enabled {
			continue
		}
		if len(k.Models) > 0 && !allowed[m.Name] {
			continue
		}
		upstreams := len(m.Inputs)
		if upstreams == 0 && m.ProviderID > 0 {
			upstreams = 1
		}
		// 聚合全部上游的输入/输出模态，给自动生成文档用。
		inMod, outMod := map[string]bool{}, map[string]bool{}
		for _, in := range m.Inputs {
			for _, x := range in.InputModalities {
				inMod[x] = true
			}
			for _, x := range in.OutputModalities {
				outMod[x] = true
			}
		}
		ctxLen := m.EffectiveContextLength()
		out = append(out, map[string]any{
			"name":       m.Name,
			"capability": m.Capability,
			"upstreams":  upstreams,
			"remark":     m.Remark,
			"context":    ctxLen,
			"in_mod":     mapKeys(inMod),
			"out_mod":    mapKeys(outMod),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// handleUserLogs 这把秘钥的最近调用记录（不含请求/响应体）。
func (s *Server) handleUserLogs(w http.ResponseWriter, r *http.Request) {
	k, err := s.userKey(r)
	if err != nil {
		s.userKeyErr(w, err)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	list, err := s.st.ListUsageLogsByKey(k.Name, limit)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// 汇总成功/失败，方便前端直接展示。
	ok, fail := 0, 0
	for _, l := range list {
		if l.Status == 0 {
			ok++
		} else {
			fail++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logs": list,
		"ok":   ok,
		"fail": fail,
	})
}

// ---------- 模型介绍（主模型自动撰写） ----------

// settingModelIntro 介绍缓存的 settings kv 键前缀：model_intro:<模型名>。
const settingModelIntro = "model_intro:"

// handleUserModelIntro 返回模型介绍（markdown）：
// 优先读缓存；没有缓存时用「系统级 AI 模型」（主模型）现场撰写一份并缓存。
// POST 同路径表示强制重新生成（清缓存重写）。
// 主模型未配置或生成失败时返回空 intro，前端回退到内置的一句话简介——文档永远可用。
func (s *Server) handleUserModelIntro(w http.ResponseWriter, r *http.Request) {
	if _, err := s.userKey(r); err != nil {
		s.userKeyErr(w, err)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少 name 参数")
		return
	}
	m, err := s.st.GetModelByName(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不存在："+name)
		return
	}

	cacheKey := settingModelIntro + name

	// POST 带 intro 字段：管理员在门户里手动编辑介绍，直接覆盖保存。
	// 不带字段的 POST 仍是「强制 AI 重写」。
	if r.Method == http.MethodPost {
		var body struct {
			Intro string `json:"intro"`
		}
		if err := decodeBody(r, &body); err == nil && strings.TrimSpace(body.Intro) != "" {
			intro := strings.TrimSpace(body.Intro)
			if err := s.st.SetSetting(cacheKey, intro); err != nil {
				writeStoreErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"intro": intro, "cached": true})
			return
		}
	}

	force := r.Method == http.MethodPost
	if !force {
		if v := s.st.GetSetting(cacheKey, ""); v != "" {
			writeJSON(w, http.StatusOK, map[string]any{"intro": v, "cached": true})
			return
		}
	}

	// 主模型未配置：直接回退，不报错（门户不能因为可选功能挂掉）。
	// 主模型给自己写介绍是允许的——不少实例只有一个可用模型，就是主模型自己。
	systemModel := s.st.GetSetting(SettingSystemModel, "")
	if systemModel == "" {
		writeJSON(w, http.StatusOK, map[string]any{"intro": "", "cached": false})
		return
	}

	intro := s.generateModelIntro(r.Context(), systemModel, m)
	if intro == "" {
		writeJSON(w, http.StatusOK, map[string]any{"intro": "", "cached": false})
		return
	}
	_ = s.st.SetSetting(cacheKey, intro)
	writeJSON(w, http.StatusOK, map[string]any{"intro": intro, "cached": false})
}

// generateModelIntro 让主模型为指定模型写一段 markdown 简介。
// 只把「能从系统里查到的事实」喂给它（能力/上下文/模态/上游数/备注），
// 并在提示词里明确禁止编造参数——宁缺毋滥。
func (s *Server) generateModelIntro(ctx context.Context, systemModel string, m store.Model) string {
	capName := map[string]string{
		"chat":      "对话",
		"image":     "绘图",
		"video":     "视频生成",
		"embedding": "向量/embedding",
		"speech":    "语音合成",
		"map":       "地图",
	}[m.Capability]
	if capName == "" {
		capName = m.Capability
	}
	facts := fmt.Sprintf(
		"- 模型名：%s\n- 能力类型：%s\n- 接入点：POST /v1/%s\n- 上游链路：%d 个（自动主备/轮询）",
		m.Name, capName, introEndpoint(m.Capability), len(m.Inputs),
	)
	if m.Config.ContextLength > 0 {
		facts += fmt.Sprintf("\n- 上下文窗口：%d tokens", m.Config.ContextLength)
	} else if m.CatalogContextLength > 0 {
		facts += fmt.Sprintf("\n- 上下文窗口：%d tokens", m.CatalogContextLength)
	}
	// 模态是每个上游输入各自声明的，取并集。
	inMod, outMod := map[string]bool{}, map[string]bool{}
	for _, in := range m.AllInputs() {
		for _, x := range in.InputModalities {
			inMod[x] = true
		}
		for _, x := range in.OutputModalities {
			outMod[x] = true
		}
	}
	if len(inMod) > 0 {
		facts += "\n- 输入模态：" + strings.Join(mapKeys(inMod), "/")
	}
	if len(outMod) > 0 {
		facts += "\n- 输出模态：" + strings.Join(mapKeys(outMod), "/")
	}
	if t := strings.TrimSpace(m.Remark); t != "" {
		facts += "\n- 管理员备注：" + t
	}

	sys := "你是 API 服务平台「模型中转站」的文档写手。请根据给定事实，为模型写一段面向开发者的中文介绍，" +
		"直接输出 markdown 正文（不要代码块包裹、不要一级标题）。要求：\n" +
		"1. 100~180 字：一句话定位 + 两个典型使用场景 + 一条实用小贴士；\n" +
		"2. 只能使用给定事实，禁止编造价格、版本号、参数量、上下文长度等任何数字；\n" +
		"3. 语气务实，可用 **加粗** 标关键词，不要列表堆砌。"
	out, _, err := s.callSystemAI(ctx, systemModel, sys, facts)
	if err != nil {
		return ""
	}
	intro := strings.TrimSpace(out.Content)
	// 主模型偶尔会用 ```markdown 围栏包住，剥掉。
	intro = strings.TrimPrefix(intro, "```markdown")
	intro = strings.TrimPrefix(intro, "```md")
	intro = strings.TrimPrefix(intro, "```")
	intro = strings.TrimSuffix(intro, "```")
	return strings.TrimSpace(intro)
}

// introEndpoint 按能力给出主接入点路径（与前端文档一致）。
func introEndpoint(capability string) string {
	switch capability {
	case "image":
		return "images/generations"
	case "video":
		return "videos"
	case "embedding":
		return "embeddings"
	case "speech":
		return "audio/speech"
	default:
		return "chat/completions"
	}
}

// ---------- 门户公告 ----------

// handlePortalSave 门户公告的可视化编辑保存端点（POST /api/portal/links）。
// 只写公告一项：外链功能已整体下线，公告是门户上唯一可由管理员就地编辑的内容。
func (s *Server) handlePortalSave(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Notice *string `json:"notice"` // 指针：不传=不改，传空串=清空
	}
	if err := decodeBody(r, &payload); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if payload.Notice != nil {
		if err := s.st.SetSetting(SettingPortalNotice, strings.TrimSpace(*payload.Notice)); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePortalLinks 返回门户公告。公告是公共信息，不需要秘钥。
func (s *Server) handlePortalLinks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"notice": s.st.GetSetting(SettingPortalNotice, ""),
	})
}

