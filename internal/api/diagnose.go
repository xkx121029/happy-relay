package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------- 主模型穿透：AI 诊断 / AI 解读 ----------
//
// 让系统级 AI（主模型）参与运维排障：
//   1. 调用日志诊断：POST /api/logs/diagnose {"id":123} —— 基于该条日志的事实
//      （模型/渠道/秘钥/状态码/延迟/错误/请求响应片段）给出根因解释与修复建议；
//   2. 自检解读：POST /api/selfcheck/interpret —— 把自检 findings 交给主模型，
//      输出一份「先修哪个、怎么修」的行动建议。
//
// 两者都只喂系统里查得到的事实，禁止编造；主模型未配置或失败时返回明确错误，
// 前端按钮置灰/提示，不影响原有功能。

// diagCacheKey 日志诊断的 settings kv 缓存前缀：日志不可变，诊断结果可永久缓存。
const diagCacheKey = "log_diag:"

// truncRunes 按 rune 截断，避免把多字节字符拦腰斩断。
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…(截断)"
}

// systemModelOrErr 取主模型；未配置时返回 false 并已写好 400 响应。
func (s *Server) systemModelOrErr(w http.ResponseWriter) (string, bool) {
	sm := s.st.GetSetting(SettingSystemModel, "")
	if sm == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"尚未指定主模型，请先在「设置 → 通用 → 系统级 AI 模型」里选择一个文本模型")
		return "", false
	}
	return sm, true
}

// handleLogDiagnose 诊断一条调用日志。
func (s *Server) handleLogDiagnose(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.systemModelOrErr(w)
	if !ok {
		return
	}
	var req struct {
		ID     int64 `json:"id"`
		Refresh bool `json:"refresh"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请提供要诊断的日志 id")
		return
	}
	log, err := s.st.GetUsageLog(req.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}

	cacheKey := diagCacheKey + strconv.FormatInt(req.ID, 10)
	if !req.Refresh {
		if v := s.st.GetSetting(cacheKey, ""); v != "" {
			writeJSON(w, http.StatusOK, map[string]any{"diagnosis": v, "cached": true})
			return
		}
	}

	// 只给事实，错误与请求/响应片段截断，防止超长日志撑爆上下文。
	facts := fmt.Sprintf(
		"- 时间：%s\n- 能力：%s\n- 对外模型：%s\n- 命中渠道：%s\n- 秘钥：%s\n- 状态码：%d（0 代表本地处理失败，未到上游）\n- 流式：%v\n- 延迟：%dms\n- prompt/completion tokens：%d/%d",
		log.CreatedAt, log.Capability, log.Model, log.ProviderName, log.APIKeyName,
		log.Status, log.Stream, log.LatencyMS, log.PromptTokens, log.CompletionTokens,
	)
	if t := strings.TrimSpace(log.Error); t != "" {
		facts += "\n- 错误信息：" + truncRunes(t, 600)
	}
	if t := strings.TrimSpace(log.RequestBody); t != "" {
		facts += "\n- 请求体片段：\n" + truncRunes(t, 1200)
	}
	if t := strings.TrimSpace(log.ResponsePreview); t != "" {
		facts += "\n- 响应片段：\n" + truncRunes(t, 800)
	}

	sys := "你是 API 中转平台的排障专家。请根据给定的一次调用日志事实，输出中文 markdown 诊断（直接输出正文，不要代码块包裹）。要求：\n" +
		"1. 结构固定三段：**根因判断**（一两句话点明最可能的原因）、**依据**（引用日志里的关键证据）、**修复建议**（最多三条，按优先级排序）；\n" +
		"2. 只能基于给定事实推理，不确定的要明说「可能」，禁止编造日志里没有的细节；\n" +
		"3. 总长不超过 200 字，务实、不废话。"

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, _, err := s.callSystemAI(ctx, sm, sys, facts)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "api_error", "主模型诊断失败："+err.Error())
		return
	}
	diag := cleanMDReply(out.Content)
	if diag == "" {
		writeErr(w, http.StatusBadGateway, "api_error", "主模型返回了空诊断，请重试")
		return
	}
	_ = s.st.SetSetting(cacheKey, diag)
	writeJSON(w, http.StatusOK, map[string]any{"diagnosis": diag, "cached": false})
}

// handleSelfcheckInterpret 解读当前自检结果（现场跑一次，秒回且不发外部请求）。
func (s *Server) handleSelfcheckInterpret(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.systemModelOrErr(w)
	if !ok {
		return
	}
	sc := s.RunSelfCheck()
	if len(sc.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "自检没有产出任何结果")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "自检汇总：%d 项正常、%d 项提醒（warn）、%d 项问题（error）。\n", sc.OK, sc.Warn, sc.Error)
	for _, it := range sc.Items {
		if it.Level == "ok" {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s", it.Level, it.Title)
		if it.Detail != "" {
			fmt.Fprintf(&b, "：%s", truncRunes(it.Detail, 300))
		}
		if it.Hint != "" {
			fmt.Fprintf(&b, "（官方建议：%s）", truncRunes(it.Hint, 200))
		}
		b.WriteByte('\n')
	}

	sys := "你是 API 中转平台的运维顾问。以下是最近一次系统自检的发现（warn=提醒，err=问题）。" +
		"请输出中文 markdown 行动建议（直接输出正文，不要代码块包裹）：\n" +
		"1. 一句话总体评价；\n2. 按「先修哪个」排序的处理清单，每条给出具体操作路径（如「设置 → 渠道页」）；\n" +
		"3. 没有严重问题时直接说明可以放心运行。只解读给定事实，总长不超过 180 字。"

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, _, err := s.callSystemAI(ctx, sm, sys, b.String())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "api_error", "主模型解读失败："+err.Error())
		return
	}
	text := cleanMDReply(out.Content)
	if text == "" {
		writeErr(w, http.StatusBadGateway, "api_error", "主模型返回了空解读，请重试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interpretation": text})
}

// cleanMDReply 剥掉主模型偶尔包上的 markdown 围栏。
func cleanMDReply(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```markdown")
	s = strings.TrimPrefix(s, "```md")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
