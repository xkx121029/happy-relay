package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// 设置项键名。
const (
	SettingSystemModel = "system_model" // 系统级 AI 使用的对外模型名
	SettingPanelTitle  = "panel_title"
)

// ---------- 元信息 ----------

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.st.AllSettings()
	writeJSON(w, http.StatusOK, map[string]any{
		"kinds": []map[string]any{
			{"value": store.KindOpenAI, "label": "OpenAI 兼容", "hint": "绝大多数中转商与自部署服务，接口为 /v1/chat/completions"},
			{"value": store.KindGemini, "label": "Google Gemini", "hint": "官方 generateContent 协议，密钥走 x-goog-api-key"},
			{"value": store.KindAnthropic, "label": "Anthropic Claude", "hint": "官方 Messages 协议，密钥走 x-api-key"},
			{"value": store.KindTemplate, "label": "自定义模板", "hint": "手填请求体模板与响应字段路径，可接任意 HTTP 上游"},
		},
		"capabilities": []map[string]any{
			{"value": store.CapChat, "label": "文本对话", "endpoint": "POST /v1/chat/completions"},
			{"value": store.CapImage, "label": "图片生成", "endpoint": "POST /v1/images/generations"},
			{"value": store.CapVideo, "label": "视频生成", "endpoint": "POST /v1/videos"},
			{"value": store.CapEmbedding, "label": "文本向量", "endpoint": "POST /v1/embeddings"},
		},
		"strategies": []map[string]any{
			{"value": "round_robin", "label": "轮询", "hint": "多个入口与密钥依次轮转，均摊压力"},
			{"value": "random", "label": "随机", "hint": "每次随机挑一个入口与密钥"},
			{"value": "failover", "label": "主备", "hint": "始终优先第一个入口，失败才切到后面的"},
		},
		"video_presets": videoPresets(),
		"settings":      settings,
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
	logs, _ := s.st.ListUsageLogs(8, "", "")
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
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	kind := req.Kind
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
	client := &http.Client{Timeout: 20 * time.Second}
	for _, raw := range urls {
		results = append(results, probeUpstream(r.Context(), client, kind, raw, key))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// probeResult 是单个入口地址的连通性探测结果。
type probeResult struct {
	URL     string   `json:"url"`
	OK      bool     `json:"ok"`
	Status  int      `json:"status"`
	Latency int64    `json:"latency_ms"`
	Detail  string   `json:"detail"`
	Models  []string `json:"models,omitempty"`
}

func probeUpstream(ctx context.Context, client *http.Client, kind, baseURL, key string) probeResult {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base != "" && !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	out := probeResult{URL: baseURL}
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
		url = strings.TrimRight(base, "/")
		if !strings.HasSuffix(url, "/v1") {
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
			out.Models = extractModelIDs(parsed)
		}
		if len(out.Models) > 0 {
			out.Detail = fmt.Sprintf("连通正常，发现 %d 个可用模型", len(out.Models))
		}
	} else {
		out.Detail = fmt.Sprintf("HTTP %d：%s", resp.StatusCode, truncateStr(string(body), 300))
	}
	return out
}

// extractModelIDs 兼容 OpenAI 与 Gemini 两种模型列表结构。
func extractModelIDs(raw map[string]any) []string {
	out := []string{}
	push := func(v any) {
		if obj, ok := v.(map[string]any); ok {
			if id, ok := obj["id"].(string); ok && id != "" {
				out = append(out, id)
				return
			}
			if name, ok := obj["name"].(string); ok && name != "" {
				out = append(out, strings.TrimPrefix(name, "models/"))
			}
		}
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
	sort.Strings(out)
	return out
}

// ---------- 模型 ----------

func (s *Server) handleModelList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListModels()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
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

// ---------- 日志 ----------

func (s *Server) handleLogList(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	list, err := s.st.ListUsageLogs(limit, r.URL.Query().Get("capability"), r.URL.Query().Get("q"))
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
	return []map[string]any{
		{
			"name": "通用任务式（提交返回 task_id）",
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
			"name": "OpenAI 风格视频接口（/v1/videos）",
			"description": "对齐 OpenAI 视频生成写法的上游：POST 建任务，GET /v1/videos/{id} 查询。",
			"capability":  store.CapVideo,
			"video": store.TemplateSpec{
				Method: "POST", Path: "/videos",
				Body: map[string]any{"model": "{{upstream_model}}", "prompt": "{{prompt}}", "size": "{{size}}"},
				Response: store.ResponseMap{TaskID: "id", Status: "status", VideoURL: "video_url"},
			},
			"video_query": store.TemplateSpec{
				Method: "GET", Path: "/videos/{{task_id}}",
				Response: store.ResponseMap{TaskID: "id", Status: "status", Progress: "progress",
					VideoURL: "video_url"},
			},
		},
		{
			"name": "同步返回视频地址",
			"description": "上游等待生成完成后直接返回视频链接，无需轮询。",
			"capability":  store.CapVideo,
			"video": store.TemplateSpec{
				Method: "POST", Path: "/generate",
				Body:   map[string]any{"model": "{{upstream_model}}", "prompt": "{{prompt}}"},
				Response: store.ResponseMap{VideoURL: "data.video_url", Status: "data.status",
					SuccessValue: "success"},
			},
		},
	}
}

// errUnsupportedKind 用于提示渠道类型配置不当。
var errUnsupportedKind = errors.New("该渠道类型不支持所选能力，请改用「自定义模板」渠道")