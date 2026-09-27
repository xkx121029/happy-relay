package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- 健康检查 ----------

// handleHealth 供部署探活使用，不鉴权也不触达数据库。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// ---------- 元信息 ----------

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.st.AllSettings()
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds": []map[string]any{
			{"value": store.KindOpenAI, "label": "OpenAI 兼容", "hint": "绝大多数中转商与自部署服务，接口为 /v1/chat/completions"},
			{"value": store.KindZhipu, "label": "智谱 GLM", "hint": "智谱开放平台，端点为 /api/paas/v4，协议与 OpenAI 一致"},
			{"value": store.KindGemini, "label": "Google Gemini", "hint": "官方 generateContent 协议，密钥走 x-goog-api-key"},
			{"value": store.KindAnthropic, "label": "Anthropic Claude", "hint": "官方 Messages 协议，密钥走 x-api-key"},
			{"value": store.KindTemplate, "label": "自定义模板", "hint": "手填请求体模板与响应字段路径，可接任意 HTTP 上游"},
		},
		"capabilities": []map[string]any{
			{"value": store.CapChat, "label": "文本对话", "endpoint": "POST /v1/chat/completions"},
			{"value": store.CapImage, "label": "图片生成", "endpoint": "POST /v1/images/generations"},
			{"value": store.CapVideo, "label": "视频生成", "endpoint": "POST /v1/videos"},
			{"value": store.CapEmbedding, "label": "文本向量", "endpoint": "POST /v1/embeddings"},
			{"value": store.CapSpeech, "label": "语音合成", "endpoint": "POST /v1/audio/speech"},
			{"value": store.CapTranscription, "label": "语音识别", "endpoint": "POST /v1/audio/transcriptions"},
		},
		"strategies": []map[string]any{
			{"value": "round_robin", "label": "轮询", "hint": "多个入口与密钥依次轮转，均摊压力"},
			{"value": "random", "label": "随机", "hint": "每次随机挑一个入口与密钥"},
			{"value": "failover", "label": "主备", "hint": "始终优先第一个入口，失败才切到后面的"},
		},
		"video_presets": videoPresets(),
		"model_presets": store.ModelPresets(),
		"settings":      settings,
		// runtime 暴露当前生效的运行参数与默认值，供设置页展示「留空即用默认」。
		"runtime": map[string]any{
			"values":   s.options(),
			"defaults": s.defaultOptions(),
		},
	})
}

// ---------- 概览 ----------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	stats, err := s.st.Stats()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	daily, _ := s.st.DailyCalls(14)
	logs, _ := s.st.ListUsageLogs(8, 0, "", "")
	tasks, _ := s.st.ListVideoTasks(5)
	writeJSON(w, http.StatusOK, map[string]any{
		"stats": stats, "daily": daily, "recent_logs": logs, "recent_tasks": tasks,
	})
}

// ---------- 渠道 ----------

func (s *Server) handleProviderList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListProviders()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleProviderSave(w http.ResponseWriter, r *http.Request) {
	var p store.Provider
	if err := decodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if id := pathID(r); id > 0 {
		p.ID = id
	}
	saved, err := s.st.SaveProvider(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteProvider(pathID(r)); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProviderTest 探测渠道连通性：逐个入口地址 + 首个密钥试一次轻量请求。
func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID            int64  `json:"id"`
		Kind          string `json:"kind"`
		BaseURL       string `json:"base_url"`
		APIKey        string `json:"api_key"`
		NoVersionPath bool   `json:"no_version_path"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	kind := req.Kind
	noVersionPath := req.NoVersionPath
	var urls []string
	key := strings.TrimSpace(req.APIKey)
	if req.ID > 0 {
		p, err := s.st.GetProvider(req.ID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if kind == "" {
			kind = p.Kind
		}
		noVersionPath = p.NoVersionPath
		urls = p.BaseURLs
		if key == "" && len(p.APIKeys) > 0 {
			key = p.APIKeys[0]
		}
	}
	if len(urls) == 0 && req.BaseURL != "" {
		urls = []string{req.BaseURL}
	}
	if len(urls) == 0 {
		if def := relay.DefaultBaseURL(kind); def != "" {
			urls = []string{def}
		}
	}
	if len(urls) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请至少填写一个入口地址")
		return
	}

	type result = probeResult
	results := make([]result, 0, len(urls))
	pctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	for _, raw := range urls {
		results = append(results, probeUpstream(pctx, probeClient, kind, raw, key, noVersionPath))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// probeModel 是探测到的某个上游模型及其推断出的能力。
type probeModel struct {
	ID         string `json:"id"`
	Capability string `json:"capability"` // chat | image | video | embedding，空表示未识别
	Hint       string `json:"hint,omitempty"`
	// ContextLength 是从上游模型列表里读到的上下文窗口，读不到时为 0。
	ContextLength int `json:"context_length,omitempty"`
	// InputModalities / OutputModalities 是推断出的输入与输出模态。
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

// contextLengthKeys 是上游模型列表里可能出现的上下文长度字段。
// 顺序上把「明确的上下文窗口」排在「输出上限」之前。
var contextLengthKeys = []string{
	"context_length", "context_window", "max_context_length", "context_size",
	"max_input_tokens", "input_token_limit", "inputTokenLimit",
	"max_model_len", "max_tokens",
}

// pickContextLength 从模型元数据里取出上下文长度，兼容 OpenAI 兼容层、
// OpenRouter 与 Gemini 的常见字段命名；取不到时返回 0。
func pickContextLength(obj map[string]any) int {
	for _, key := range contextLengthKeys {
		if n := toPositiveInt(obj[key]); n > 0 {
			return n
		}
	}
	// 部分上游把信息放在嵌套对象里。
	for _, holder := range []string{"top_provider", "model_info", "metadata", "architecture", "limits"} {
		if sub, ok := obj[holder].(map[string]any); ok {
			if n := pickContextLength(sub); n > 0 {
				return n
			}
		}
	}
	return 0
}

// toPositiveInt 把可能是数字或数字字符串的取值转成正整数，无法转换时返回 0。
func toPositiveInt(v any) int {
	switch t := v.(type) {
	case float64:
		if t > 0 {
			return int(t)
		}
	case int:
		if t > 0 {
			return t
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// probeResult 是单个入口地址的连通性探测结果。
type probeResult struct {
	URL     string       `json:"url"`
	OK      bool         `json:"ok"`
	Status  int          `json:"status"`
	Latency int64        `json:"latency_ms"`
	Detail  string       `json:"detail"`
	Models  []probeModel `json:"models,omitempty"`
}

// probeUpstream 探测某个入口地址是否连通。
// 返回值必须命名，否则 defer 里写回的延迟只会改到局部副本上，
// 调用方拿到的永远是 0——这个坑在巡检功能上线时才暴露出来。
func probeUpstream(ctx context.Context, client *http.Client, kind, baseURL, key string, noVersionPath bool) (out probeResult) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base != "" && !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	out = probeResult{URL: baseURL}
	start := time.Now()
	defer func() { out.Latency = time.Since(start).Milliseconds() }()

	var method, url string
	headers := map[string]string{}
	switch kind {
	case store.KindGemini:
		url = base + "/models"
		method = http.MethodGet
		if key != "" {
			headers["x-goog-api-key"] = key
		}
	case store.KindAnthropic:
		url = base + "/models"
		method = http.MethodGet
		if key != "" {
			headers["x-api-key"] = key
		}
		headers["anthropic-version"] = "2023-06-01"
	default:
		// 已带版本段的端点（如智谱的 /api/paas/v4）不再补 /v1；
		// 标记 no_version_path 的端点（如 GitHub Models）接口挂在根路径，也不补。
		url = strings.TrimRight(base, "/")
		if !noVersionPath && !relay.HasVersionPath(url) {
			url += "/v1"
		}
		url += "/models"
		method = http.MethodGet
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
	}
	if base == "" {
		out.Detail = "未填写入口地址"
		return out
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		out.Detail = err.Error()
		return out
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		out.Detail = "连接失败：" + err.Error()
		return out
	}
	defer resp.Body.Close()
	out.Status = resp.StatusCode
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out.OK = true
		out.Detail = "连通正常"
		var parsed map[string]any
		if json.Unmarshal(body, &parsed) == nil {
			out.Models = extractModels(parsed, kind)
		}
		if len(out.Models) > 0 {
			out.Detail = fmt.Sprintf("连通正常，发现 %d 个可用模型", len(out.Models))
		}
	} else {
		out.Detail = fmt.Sprintf("HTTP %d：%s", resp.StatusCode, truncateStr(string(body), 300))
	}
	return out
}

// extractModels 兼容 OpenAI / Gemini / Anthropic 的模型列表结构，
// 并对每个模型推断能力，便于面板一键导入为对外模型。
func extractModels(raw map[string]any, kind string) []probeModel {
	out := []probeModel{}
	seen := map[string]bool{}
	push := func(v any) {
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		id := ""
		if s, ok := obj["id"].(string); ok {
			id = s
		} else if s, ok := obj["name"].(string); ok {
			id = strings.TrimPrefix(s, "models/")
		}
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		capability, hint := inferCapability(id, obj, kind)
		inputs, outputs := store.GuessModalities(id, capability)
		out = append(out, probeModel{
			ID: id, Capability: capability, Hint: hint,
			ContextLength:    pickContextLength(obj),
			InputModalities:  inputs,
			OutputModalities: outputs,
		})
	}
	switch data := raw["data"].(type) {
	case []any:
		for _, it := range data {
			push(it)
		}
	case map[string]any:
		if arr, ok := data["models"].([]any); ok {
			for _, it := range arr {
				push(it)
			}
		}
	}
	if arr, ok := raw["models"].([]any); ok {
		for _, it := range arr {
			push(it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// capabilityRules 按名称特征推断模型能力，顺序即优先级。
var capabilityRules = []struct {
	capability string
	keywords   []string
	hint       string
}{
	{store.CapEmbedding,
		[]string{"embed", "bge-", "gte-", "m3e", "jina-embeddings", "text-embedding"},
		"名称含向量化关键词"},
	{store.CapVideo,
		[]string{"veo", "sora", "kling", "wan2", "wanx", "runway", "pika", "hailuo", "luma", "cogvideo", "seedance", "vidu", "mochi", "video"},
		"名称含视频关键词"},
	{store.CapImage,
		[]string{"dall-e", "dalle", "gpt-image", "imagen", "flux", "stable-diffusion", "sdxl", "sd3", "kolors", "seedream", "midjourney", "mj-", "banana", "image", "draw"},
		"名称含图片关键词"},
}

// unsupportedModelKeywords 命中的模型不属于四类能力，导入时默认不勾选。
var unsupportedModelKeywords = []string{
	"whisper", "tts", "audio", "speech", "moderation", "rerank", "transcribe",
}

// inferCapability 推断上游模型的能力：优先采信上游声明的字段，其次按名称特征判断。
func inferCapability(id string, obj map[string]any, kind string) (string, string) {
	if capability, hint := explicitCapability(obj); capability != "" {
		return capability, hint
	}
	lower := strings.ToLower(id)
	for _, kw := range unsupportedModelKeywords {
		if strings.Contains(lower, kw) {
			return "", "非对话/图片/视频/向量类模型"
		}
	}
	for _, rule := range capabilityRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				return rule.capability, rule.hint
			}
		}
	}
	if kind == store.KindAnthropic {
		return store.CapChat, "Anthropic 模型默认按对话处理"
	}
	return store.CapChat, "未识别到特征，默认按对话处理"
}

// explicitCapability 读取上游返回里可能存在的显式能力标记。
func explicitCapability(obj map[string]any) (string, string) {
	for _, key := range []string{"capability", "type", "model_type", "modelType"} {
		raw, ok := obj[key].(string)
		if !ok {
			continue
		}
		if capability := normalizeCapability(raw); capability != "" {
			return capability, "上游声明 " + key + "=" + raw
		}
	}
	// Gemini 用 supportedGenerationMethods 描述可用方法。
	if methods, ok := obj["supportedGenerationMethods"].([]any); ok {
		for _, mv := range methods {
			method, _ := mv.(string)
			switch {
			case strings.Contains(method, "embedContent"),
				strings.Contains(method, "embedText"),
				strings.Contains(method, "batchEmbed"):
				return store.CapEmbedding, "上游声明 methods 含 " + method
			case strings.Contains(method, "predictLongRunning"),
				strings.Contains(method, "generateVideos"):
				return store.CapVideo, "上游声明 methods 含 " + method
			case strings.Contains(method, "generateContent"):
				return store.CapChat, "上游声明 methods 含 " + method
			}
		}
	}
	return "", ""
}

func normalizeCapability(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "chat", "text", "completion", "llm", "text-generation":
		return store.CapChat
	case "image", "images", "text-to-image", "image-generation":
		return store.CapImage
	case "video", "videos", "text-to-video", "video-generation":
		return store.CapVideo
	case "embedding", "embeddings", "embed":
		return store.CapEmbedding
	}
	return ""
}

// ---------- 模型 ----------

// modelView 在模型行上附加「当前是否可用」标记（是否有可用渠道兜底）。
type modelView struct {
	store.Model
	Usable bool `json:"usable"`
}

// handleModelList 返回模型列表。
// 默认只返回可用模型（至少绑定一个可用渠道），把调不通的挡在列表外；
// ?all=1 返回全量并给每行附带 usable 字段，供模型页做「可用/不可用」筛选与管理。
func (s *Server) handleModelList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListModels()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if r.URL.Query().Get("all") == "1" {
		flags := s.modelUsableFlags(list)
		out := make([]modelView, len(list))
		for i, m := range list {
			out[i] = modelView{Model: m, Usable: flags[i]}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, s.filterUsableModels(list))
}

func (s *Server) handleModelSave(w http.ResponseWriter, r *http.Request) {
	var m store.Model
	if err := decodeBody(r, &m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if id := pathID(r); id > 0 {
		m.ID = id
	}
	saved, err := s.st.SaveModel(m)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteModel(pathID(r)); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 对外秘钥 ----------

// handleUpstreamStats 返回上游健康画像：以引擎内存实时值为主（含启动时载入的
// 历史基线 + 本次进程的增量），库里多余的条目兜底，重启不丢、面板看实时。
func (s *Server) handleUpstreamStats(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListUpstreamStats()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	mem := s.eng.StatsSnapshot()
	out := make([]map[string]any, 0, len(list))
	seen := map[string]bool{}
	for _, st := range list {
		key := fmt.Sprintf("%d::%s", st.ProviderID, st.UpstreamName)
		if v, ok := mem[key]; ok {
			st.OKCount, st.FailCount, st.ConsecFails = v.OKCount, v.FailCount, v.ConsecFails
			st.AvgLatencyMS = v.AvgLatencyMS
			st.LastOKAt, st.LastFailAt, st.LastError = v.LastOKAt, v.LastFailAt, v.LastError
		}
		seen[key] = true
		out = append(out, statToMap(st))
	}
	// 内存里有、库里还没有（尚未落盘）的条目也补上。
	for key, v := range mem {
		if seen[key] {
			continue
		}
		pid := int64(0)
		var name string
		fmt.Sscanf(key, "%d::%s", &pid, &name)
		if pid <= 0 || name == "" {
			continue
		}
		out = append(out, map[string]any{
			"provider_id": pid, "upstream_name": name,
			"ok_count": v.OKCount, "fail_count": v.FailCount, "consec_fails": v.ConsecFails,
			"avg_latency_ms": v.AvgLatencyMS, "last_ok_at": v.LastOKAt,
			"last_fail_at": v.LastFailAt, "last_error": v.LastError,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func statToMap(st store.UpstreamStat) map[string]any {
	return map[string]any{
		"provider_id": st.ProviderID, "provider_name": st.ProviderName, "upstream_name": st.UpstreamName,
		"ok_count": st.OKCount, "fail_count": st.FailCount, "consec_fails": st.ConsecFails,
		"avg_latency_ms": st.AvgLatencyMS, "success_rate": st.SuccessRate(),
		"last_ok_at": st.LastOKAt, "last_fail_at": st.LastFailAt, "last_error": st.LastError,
		"updated_at": st.UpdatedAt,
	}
}

func (s *Server) handleKeyList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListAPIKeys()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleKeySave(w http.ResponseWriter, r *http.Request) {
	var k store.APIKey
	if err := decodeBody(r, &k); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if id := pathID(r); id > 0 {
		k.ID = id
		if existing, err := s.st.FindAPIKeyByID(id); err == nil {
			k.Key = existing.Key
		}
	}
	saved, err := s.st.SaveAPIKey(k)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleKeyDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteAPIKey(pathID(r)); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 技能 ----------

func (s *Server) handleSkillList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListSkills()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSkillSave(w http.ResponseWriter, r *http.Request) {
	var sk store.Skill
	if err := decodeBody(r, &sk); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if id := pathID(r); id > 0 {
		sk.ID = id
	}
	saved, err := s.st.SaveSkill(sk)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteSkill(pathID(r)); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 视频任务 ----------

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	list, err := s.st.ListVideoTasks(limit)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, s.taskResponse(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTaskDelete 删除本地任务记录（面板用于取消任务）。
func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.st.GetVideoTaskByTaskID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "视频任务不存在："+id)
		return
	}
	if err := s.st.DeleteVideoTask(task.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 日志 ----------

func (s *Server) handleLogList(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	// before = 上一页最后一条的 id，实现「加载更多」游标分页。
	var before int64
	fmt.Sscanf(r.URL.Query().Get("before"), "%d", &before)
	list, err := s.st.ListUsageLogs(limit, before, r.URL.Query().Get("capability"), r.URL.Query().Get("q"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleLogClear(w http.ResponseWriter, r *http.Request) {
	if err := s.st.ClearUsageLogs(); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 设置 ----------

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.st.AllSettings()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	var payload map[string]string
	if err := decodeBody(r, &payload); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	for k, v := range payload {
		if err := s.st.SetSetting(k, v); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	// 让运行参数立即生效，无需重启服务。
	s.reloadOptions()
	// 再平衡的模式与阈值也来自设置，保存后立即刷新，
	// 否则用户改了模式要等下一轮巡检才会生效。
	s.refreshBalance()
	// 飞书长连接按最新设置同步（配置变了会自动关旧起新）。
	s.FeishuSyncWSClient()
	settings, _ := s.st.AllSettings()
	writeJSON(w, http.StatusOK, settings)
}

// ---------- 静态资源 ----------

func (s *Server) staticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, "invalid_request_error", "接口不存在："+r.URL.Path)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		// Vite 的 assets/ 产物文件名带内容 hash，可以放心缓存一年；
		// 其余（index.html 与 SPA 路由回落）标记 no-cache，
		// 保证发版后浏览器立刻拿到新 HTML，再由它引用新 hash 资源。
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if _, err := s.static.Open(path); err != nil {
			// 前端为单页应用，未命中的路径统一回落到 index.html
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// videoPresets 提供常见视频上游的模板预设，方便一把导入。
func videoPresets() []map[string]any {
	zhipuSubmit, zhipuQuery := store.ZhipuVideoSpecs()
	return []map[string]any{
		{
			"name": "智谱 CogVideoX",
			"description": "创建走 /videos/generations，查询走 /async-result/{id}；" +
				"查询响应不含状态字段，由是否产出 video_result[0].url 判定完成。",
			"capability":  store.CapVideo,
			"video":       zhipuSubmit,
			"video_query": zhipuQuery,
		},
		{
			"name": "Agnes Video 2.5 Flash",
			"description": "创建走 /v1/videos，查询走根路径 /agnesapi；支持 text / keyframe / reference " +
				"三种模式，size 固定 720P，秒数为字符串。",
			"capability": store.CapVideo,
			"video": store.TemplateSpec{
				Method: "POST", Path: "/videos",
				// 空参数一律不下发：text 模式不允许出现图片/音频字段，传空会被判错。
				OmitEmpty: true,
				Body: map[string]any{
					"model":        "{{upstream_model}}",
					"prompt":       "{{prompt}}",
					"mode":         "{{mode}}",
					"seconds":      "{{duration_text}}",
					"size":         "720P",
					"aspect_ratio": "{{aspect_ratio}}",
					"first_frame":  "{{first_frame}}",
					"last_frame":   "{{last_frame}}",
					"images":       "{{images}}",
					"audios":       "{{audios}}",
				},
				Response: store.ResponseMap{
					TaskID: "video_id", Status: "status", Progress: "progress", VideoURL: "url",
				},
			},
			"video_query": store.TemplateSpec{
				Method: "GET", Path: "{{base_root}}/agnesapi",
				Query: map[string]string{
					"video_id":   "{{task_id}}",
					"model_name": "{{upstream_model}}",
				},
				Response: store.ResponseMap{
					TaskID: "id", Status: "status", Progress: "progress",
					VideoURL: "url", Error: "error",
				},
			},
		},
		{
			"name":        "通用任务式（提交返回 task_id）",
			"description": "适用于「POST 提交返回任务 ID，再 GET 查询」的绝大多数视频接口。",
			"capability":  store.CapVideo,
			"video": store.TemplateSpec{
				Method: "POST", Path: "/videos/generations",
				Body: map[string]any{"model": "{{upstream_model}}", "prompt": "{{prompt}}",
					"image": "{{image_url}}", "duration": "{{duration}}"},
				Response: store.ResponseMap{TaskID: "data.task_id", Status: "data.status",
					VideoURL: "data.video_url", Error: "message"},
			},
			"video_query": store.TemplateSpec{
				Method: "GET", Path: "/videos/generations/{{task_id}}",
				Response: store.ResponseMap{Status: "data.status", Progress: "data.progress",
					VideoURL: "data.video_url", Error: "message",
					StatusMap: map[string]string{"processing": "running", "done": "succeeded"}},
			},
		},
		{
			"name":        "OpenAI 风格视频接口（/v1/videos）",
			"description": "对齐 OpenAI 视频生成写法的上游：POST 建任务，GET /v1/videos/{id} 查询。",
			"capability":  store.CapVideo,
			"video": store.TemplateSpec{
				Method: "POST", Path: "/videos",
				Body:     map[string]any{"model": "{{upstream_model}}", "prompt": "{{prompt}}", "size": "{{size}}"},
				Response: store.ResponseMap{TaskID: "id", Status: "status", VideoURL: "video_url"},
			},
			"video_query": store.TemplateSpec{
				Method: "GET", Path: "/videos/{{task_id}}",
				Response: store.ResponseMap{TaskID: "id", Status: "status", Progress: "progress",
					VideoURL: "video_url"},
			},
		},
		{
			"name":        "同步返回视频地址",
			"description": "上游等待生成完成后直接返回视频链接，无需轮询。",
			"capability":  store.CapVideo,
			"video": store.TemplateSpec{
				Method: "POST", Path: "/generate",
				Body: map[string]any{"model": "{{upstream_model}}", "prompt": "{{prompt}}"},
				Response: store.ResponseMap{VideoURL: "data.video_url", Status: "data.status",
					SuccessValue: "success"},
			},
		},
	}
}

// errUnsupportedKind 用于提示渠道类型配置不当。
var errUnsupportedKind = errors.New("该渠道类型不支持所选能力，请改用「自定义模板」渠道")
