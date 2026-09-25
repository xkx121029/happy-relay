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

// ---------- /v1/models ----------

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	models, err := s.st.ListModels()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	created := time.Now().Unix()
	data := []map[string]any{}
	for _, m := range models {
		if !m.Enabled || !keyAllowed(key, m.Name) {
			continue
		}
		data = append(data, map[string]any{
			"id":         m.Name,
			"object":     "model",
			"created":    created,
			"owned_by":   m.ProviderName,
			"capability": m.Capability,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ---------- /v1/chat/completions ----------

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	in := relay.ParseChatInput(raw)
	if in.Model == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 model")
		return
	}
	if !keyAllowed(key, in.Model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+in.Model)
		return
	}
	s.runChat(w, r, key, in)
}

// handleLegacyCompletion 兼容老式 /v1/completions：把 prompt 包成一条 user 消息后走对话链路。
func (s *Server) handleLegacyCompletion(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	prompt, _ := raw["prompt"].(string)
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 prompt，或请改用 /v1/chat/completions")
		return
	}
	model, _ := raw["model"].(string)
	raw["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
	delete(raw, "prompt")
	in := relay.ParseChatInput(raw)
	in.Model = model
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	s.runChat(w, r, key, in)
}

func (s *Server) runChat(w http.ResponseWriter, r *http.Request, key store.APIKey, in *relay.ChatInput) {
	start := time.Now()
	m, ps, err := s.eng.ResolveAll(in.Model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	if m.Capability != store.CapChat {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("模型 %s 的能力是 %s，请改用对应的接口", m.Name, m.Capability))
		return
	}

	requestBody := ""
	if !s.cfg.DisableLogBody {
		requestBody = previewOf(in.Raw, 4000)
	}

	if in.Stream {
		sse := newSSEWriter(w)
		created := time.Now().Unix()
		id := "chatcmpl-" + shortID()
		first := true
		var contentLen, reasoningLen int
		out, usedProvider, err := s.eng.Chat(r.Context(), m, ps, in, func(d relay.StreamDelta) error {
			chunk := map[string]any{
				"id": id, "object": "chat.completion.chunk", "created": created, "model": m.Name,
			}
			delta := map[string]any{}
			if first {
				role := d.Role
				if role == "" {
					role = "assistant"
				}
				delta["role"] = role
				first = false
			}
			if d.Content != "" {
				delta["content"] = d.Content
				contentLen += len(d.Content)
			}
			if d.Reasoning != "" {
				delta["reasoning_content"] = d.Reasoning
				reasoningLen += len(d.Reasoning)
			}
			if len(d.ToolCalls) > 0 {
				delta["tool_calls"] = toolCallChunks(d.ToolCalls)
			}
			choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
			if d.FinishReason != "" {
				choice["finish_reason"] = d.FinishReason
			}
			chunk["choices"] = []any{choice}
			if d.Usage != nil {
				chunk["usage"] = usageMap(*d.Usage)
			}
			if len(delta) == 0 {
				return nil
			}
			return sse.send(chunk)
		})
		latency := time.Since(start).Milliseconds()
		if err != nil {
			sse.send(map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}})
			sse.done()
			s.logUsage(store.UsageLog{
				Capability: store.CapChat, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
				APIKeyName: key.Name, Status: 502, Stream: true, LatencyMS: latency,
				Error: err.Error(), RequestBody: requestBody,
			})
			return
		}
		if out != nil && out.Usage.TotalTokens > 0 {
			sse.send(map[string]any{
				"id": id, "object": "chat.completion.chunk", "created": created, "model": m.Name,
				"choices": []any{},
				"usage":   usageMap(out.Usage),
			})
		}
		sse.done()
		usage := relay.Usage{}
		if out != nil {
			usage = out.Usage
		}
		s.logUsage(store.UsageLog{
			Capability: store.CapChat, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 200, Stream: true, LatencyMS: latency,
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			RequestBody:     requestBody,
			ResponsePreview: fmt.Sprintf("流式输出：正文 %d 字符，思考 %d 字符", contentLen, reasoningLen),
		})
		return
	}

	out, usedProvider, err := s.eng.Chat(r.Context(), m, ps, in, nil)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapChat, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 502, LatencyMS: latency, Error: err.Error(),
			RequestBody: requestBody,
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	message := map[string]any{"role": "assistant", "content": out.Content}
	if out.Reasoning != "" {
		message["reasoning_content"] = out.Reasoning
	}
	if len(out.ToolCalls) > 0 {
		message["tool_calls"] = toolCallsJSON(out.ToolCalls)
	}
	resp := map[string]any{
		"id":      out.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   m.Name,
		"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": out.FinishReason,
		}},
	}
	if out.Usage.TotalTokens > 0 {
		resp["usage"] = usageMap(out.Usage)
	}
	writeJSON(w, http.StatusOK, resp)
	s.logUsage(store.UsageLog{
		Capability: store.CapChat, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
		APIKeyName: key.Name, Status: 200, LatencyMS: latency,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens,
		RequestBody: requestBody, ResponsePreview: truncateStr(out.Content, 2000),
	})
}

// ---------- /v1/embeddings ----------

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	model, _ := raw["model"].(string)
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	inputs := toStringList(raw["input"])
	if len(inputs) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 input")
		return
	}
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	start := time.Now()
	out, usedProvider, err := s.eng.Embedding(r.Context(), m, ps, &relay.EmbeddingInput{Model: model, Input: inputs})
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapEmbedding, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 502, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	data := make([]map[string]any, 0, len(out.Vectors))
	for i, vec := range out.Vectors {
		data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": vec})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list", "data": data, "model": m.Name, "usage": usageMap(out.Usage),
	})
	s.logUsage(store.UsageLog{
		Capability: store.CapEmbedding, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
		APIKeyName: key.Name, Status: 200, LatencyMS: time.Since(start).Milliseconds(),
		PromptTokens: out.Usage.PromptTokens,
	})
}

// ---------- /v1/images/generations ----------

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	model, _ := raw["model"].(string)
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	prompt, _ := raw["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 prompt")
		return
	}
	in := &relay.ImageInput{Model: model, Prompt: prompt, Options: map[string]any{}}
	in.Size, _ = raw["size"].(string)
	in.ResponseFormat, _ = raw["response_format"].(string)
	if n, ok := raw["n"].(float64); ok {
		in.N = int(n)
	}
	for k, v := range raw {
		switch k {
		case "model", "prompt", "n", "size", "response_format":
			continue
		}
		in.Options[k] = v
	}
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	start := time.Now()
	requestBody := ""
	if !s.cfg.DisableLogBody {
		requestBody = previewOf(raw, 2000)
	}
	out, usedProvider, err := s.eng.Image(r.Context(), m, ps, in)
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapImage, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 502, LatencyMS: time.Since(start).Milliseconds(),
			Error: err.Error(), RequestBody: requestBody,
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	data := make([]map[string]any, 0, len(out.Items))
	for _, it := range out.Items {
		entry := map[string]any{}
		if it.URL != "" {
			entry["url"] = it.URL
		}
		// 只要不是显式要求 b64，统一补一份 data URL，方便网页直接预览。
		if it.B64 != "" {
			if in.ResponseFormat == "b64_json" {
				entry["b64_json"] = it.B64
			} else {
				entry["b64_json"] = it.B64
			}
			if it.URL == "" {
				entry["url"] = "data:image/png;base64," + it.B64
			}
		}
		if it.Revised != "" {
			entry["revised_prompt"] = it.Revised
		}
		entry["index"] = len(data)
		data = append(data, entry)
	}
	resp := map[string]any{"created": out.Created, "data": data}
	if out.Created == 0 {
		resp["created"] = time.Now().Unix()
	}
	if out.Usage != nil {
		resp["usage"] = usageMap(*out.Usage)
	}
	writeJSON(w, http.StatusOK, resp)
	s.logUsage(store.UsageLog{
		Capability: store.CapImage, Model: m.Name, ProviderName: safeName(usedProvider, p),
		APIKeyName: key.Name, Status: 200, LatencyMS: time.Since(start).Milliseconds(),
		RequestBody: requestBody, ResponsePreview: fmt.Sprintf("生成 %d 张图片", len(data)),
	})
}

// ---------- /v1/videos ----------

func (s *Server) handleVideoCreate(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	model, _ := raw["model"].(string)
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	in := &relay.VideoInput{Model: model, Extra: map[string]any{}}
	in.Prompt, _ = raw["prompt"].(string)
	in.ImageURL = firstString(raw, "image", "image_url", "first_frame_image", "input_reference")
	in.Size, _ = raw["size"].(string)
	if d, ok := raw["duration"].(float64); ok {
		in.Duration = int(d)
	}
	if strings.TrimSpace(in.Prompt) == "" && in.ImageURL == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 prompt（或 image 用于图生视频）")
		return
	}
	for k, v := range raw {
		switch k {
		case "model", "prompt", "image", "image_url", "first_frame_image", "input_reference", "size", "duration":
			continue
		}
		in.Extra[k] = v
	}
	m, p, err := s.eng.Resolve(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	start := time.Now()
	task, err := s.eng.CreateVideo(r.Context(), m, p, in)
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapVideo, Model: m.Name, ProviderName: p.Name,
			APIKeyName: key.Name, Status: 502, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(task))
	s.logUsage(store.UsageLog{
		Capability: store.CapVideo, Model: m.Name, ProviderName: p.Name,
		APIKeyName: key.Name, Status: 200, LatencyMS: time.Since(start).Milliseconds(),
		RequestBody: previewOf(raw, 2000), ResponsePreview: "已提交视频任务 " + task.TaskID,
	})
	// 交给后台轮询器继续跟进
	s.pumpVideoTask(task.ID)
}

func (s *Server) handleVideoGet(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	id := r.PathValue("id")
	task, err := s.st.GetVideoTaskByTaskID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "视频任务不存在："+id)
		return
	}
	if !keyAllowed(key, task.Model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权查询此任务")
		return
	}
	// 客户端主动查询时顺带刷新一次状态，减少等待。
	if task.Status == store.TaskQueued || task.Status == store.TaskRunning {
		if updated, err := s.eng.PollVideo(r.Context(), task); err == nil {
			task = updated
		}
	}
	writeJSON(w, http.StatusOK, s.taskResponse(task))
}

// taskResponse 是视频任务的统一对外结构。
func (s *Server) taskResponse(t store.VideoTask) map[string]any {
	out := map[string]any{
		"id":         t.TaskID,
		"object":     "video.generation",
		"model":      t.Model,
		"status":     t.Status,
		"progress":   t.Progress,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}
	if t.Error != "" {
		out["error"] = map[string]any{"message": t.Error, "type": "upstream_error"}
	}
	if len(t.Result) > 0 {
		var res map[string]any
		if json.Unmarshal(t.Result, &res) == nil {
			if url, ok := res["video_url"].(string); ok && url != "" {
				out["video_url"] = url
			}
			if url, ok := res["image_url"].(string); ok && url != "" {
				out["image_url"] = url
			}
		}
	}
	return out
}

// pumpVideoTask 立即触发一次后台轮询，让短任务更快出结果。
func (s *Server) pumpVideoTask(id int64) {
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tasks, err := s.st.ListPendingVideoTasks()
		if err != nil {
			return
		}
		for _, t := range tasks {
			if t.ID != id {
				continue
			}
			updated, err := s.eng.PollVideo(ctx, t)
			if err != nil {
				return
			}
			if updated.Status == store.TaskQueued || updated.Status == store.TaskRunning {
				s.st.SetSetting("video_watch:"+updated.TaskID, updated.UpdatedAt)
			}
			return
		}
	}()
}

// ---------- 日志 ----------

func (s *Server) logUsage(l store.UsageLog) {
	s.st.AddUsageLog(l)
}

func safeName(p store.Provider, fallback store.Provider) string {
	if p.Name != "" {
		return p.Name
	}
	return fallback.Name
}

func usageMap(u relay.Usage) map[string]any {
	return map[string]any{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.TotalTokens,
	}
}

func toolCallsJSON(calls []relay.ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for i, c := range calls {
		out = append(out, map[string]any{
			"index": i, "id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
		})
	}
	return out
}

func toolCallChunks(calls []relay.ToolCall) []map[string]any {
	return toolCallsJSON(calls)
}

func toStringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			if s, ok := it.(string); ok {
				out = append(out, s)
			} else {
				b, _ := json.Marshal(it)
				out = append(out, string(b))
			}
		}
		return out
	}
	return nil
}

func firstString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := raw[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func previewOf(v any, limit int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return truncateStr(string(b), limit)
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func shortID() string {
	buf := make([]byte, 6)
	if _, err := randRead(buf); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(buf)*2)
	for i, b := range buf {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}
