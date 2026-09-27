package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- /v1/models ----------

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	// 只暴露当前可调用的模型：绑定渠道全部不可用的模型不对外展示，
	// 客户端不会选到一个注定 401 的模型名。
	models := s.filterUsableModels(mustListModels(s))
	created := time.Now().Unix()
	data := []map[string]any{}
	hasChat := false
	for _, m := range models {
		if !m.Enabled || !keyAllowed(key, m.Name) {
			continue
		}
		if m.Capability == store.CapChat && m.Name != relay.AutoModelName {
			hasChat = true
		}
		entry := map[string]any{
			"id":         m.Name,
			"object":     "model",
			"created":    created,
			"owned_by":   m.ProviderName,
			"capability": m.Capability,
		}
		// 附带上下文窗口，便于客户端自行规划请求长度；未知时不输出该字段。
		if n := m.EffectiveContextLength(); n > 0 {
			entry["context_length"] = n
		}
		data = append(data, entry)
	}
	// 自动模型：按任务难度在已启用的文本模型里挑选。
	if hasChat && keyAllowed(key, relay.AutoModelName) {
		data = append([]map[string]any{{
			"id":         relay.AutoModelName,
			"object":     "model",
			"created":    created,
			"owned_by":   "relay",
			"capability": store.CapChat,
			"auto":       true,
		}}, data...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ---------- /v1/skills ----------

// handleSkillCatalog 返回可用的站内技能及其说明，供客户端在调用前查询。
func (s *Server) handleSkillCatalog(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	list, err := s.st.ListSkills()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	data := make([]map[string]any, 0, len(list))
	for _, sk := range list {
		if !sk.Enabled {
			continue
		}
		data = append(data, map[string]any{
			"slug":        sk.Slug,
			"name":        sk.Name,
			"description": sk.Description,
			"category":    sk.Category,
			"capability":  sk.Capability,
			"params":      sk.Params,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
		"usage":  "在 /v1/chat/completions、/v1/images/generations、/v1/videos 里带上 skill=<slug> 与 skill_params={...} 即可套用",
	})
}

// ---------- /v1/chat/completions ----------

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	if err := s.applySkill(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
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
	s.runChat(w, r, key, in, false)
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
	if err := s.applySkill(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	in := relay.ParseChatInput(raw)
	in.Model = model
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	s.runChat(w, r, key, in, false)
}

// runChat 执行对话请求；withMeta 为 true 时（面板调试台）会额外回传命中的上游信息。
func (s *Server) runChat(w http.ResponseWriter, r *http.Request, key store.APIKey, in *relay.ChatInput, withMeta bool) {
	start := time.Now()
	requested := in.Model
	var m store.Model
	var ps []store.Provider
	var difficulty *relay.Difficulty
	// auto 会拿到一整串候选模型，调用时逐个尝试直到某个成功。
	var autoCandidates []relay.AutoCandidate
	var err error
	if in.Model == relay.AutoModelName {
		var d relay.Difficulty
		autoCandidates, d, err = s.eng.PickAutoCandidates(in)
		if err == nil && len(autoCandidates) > 0 {
			// 先用首个候选做能力校验与上下文规划。
			m, ps = autoCandidates[0].Model, autoCandidates[0].Providers
		} else if err == nil {
			err = fmt.Errorf("没有可用的文本模型，无法使用 auto")
		}
		difficulty = &d
	} else {
		m, ps, err = s.eng.ResolveAll(in.Model)
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	// provider_id / upstream_name 是面板调试台的专用字段：
	// 无论是否生效都要从请求体里摘掉，否则会被原样透传给上游。
	pinProvider, pinName := takeUpstreamPin(in.Raw)
	if withMeta && (pinProvider > 0 || pinName != "") {
		m, ps, err = s.pinUpstream(m, ps, pinProvider, pinName)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}
	if m.Capability != store.CapChat {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("模型 %s 的能力是 %s，请改用对应的接口", m.Name, m.Capability))
		return
	}

	// 上下文规划：按模型窗口估算输入占用，必要时自动裁剪历史或提前给出可读错误。
	opts := s.options()
	planned, plan, planErr := relay.PlanContext(in, m.EffectiveContextLength(),
		opts.ContextSafetyPercent, opts.ContextPolicy)
	if planErr != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapChat, Model: m.Name, ProviderName: providerNameByID(ps, m.ProviderID),
			APIKeyName: key.Name, Status: 400, Stream: in.Stream,
			LatencyMS: time.Since(start).Milliseconds(), Error: planErr.Error(),
		})
		writeErr(w, http.StatusBadRequest, "context_length_exceeded", planErr.Error())
		return
	}
	in = planned

	// 按对外模型限速（RPM/TPM），超限直接拒绝。
	if !s.checkModelLimit(w, m) {
		return
	}

	requestBody := ""
	if opts.LogBody {
		requestBody = previewOf(in.Raw, s.logBodyLimit())
	}

	// activeModel 记录当前正在尝试的模型，流式 chunk 需要靠它把 model 字段写对。
	activeModel := m
	// callChat 统一发起对话调用：auto 模式会逐个尝试候选模型，
	// 只要还没向客户端下发内容，切换对客户端而言就只是"响应慢了一点"。
	callChat := func(emit relay.StreamFunc) (*relay.ChatOutput, store.Provider, string, error) {
		if len(autoCandidates) > 0 {
			out, used, p, _, callErr := s.eng.ChatAuto(r.Context(), in, emit, func(mm store.Model) {
				activeModel = mm
			})
			name := used.Name
			if name == "" {
				name = m.Name
			}
			return out, p, name, callErr
		}
		out, p, callErr := s.eng.Chat(r.Context(), m, ps, in, emit)
		return out, p, m.Name, callErr
	}

	if in.Stream {
		sse := newSSEWriter(w)
		created := time.Now().Unix()
		id := "chatcmpl-" + shortID()
		first := true
		var contentBuf, reasoningBuf strings.Builder
		stopHeartbeat := s.startHeartbeat(sse)
		out, usedProvider, usedModel, err := callChat(s.smoothEmit(func(d relay.StreamDelta) error {
			chunk := map[string]any{
				"id": id, "object": "chat.completion.chunk", "created": created, "model": activeModel.Name,
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
				contentBuf.WriteString(d.Content)
			}
			if d.Reasoning != "" {
				delta["reasoning_content"] = d.Reasoning
				reasoningBuf.WriteString(d.Reasoning)
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
		}))
		stopHeartbeat()
		latency := time.Since(start).Milliseconds()
		if err != nil {
			sse.send(map[string]any{"error": map[string]any{"message": err.Error(), "type": "upstream_error"}})
			if withMeta {
				sse.send(relayMeta(usedModel, requested, usedProvider, difficulty))
			}
			sse.done()
			s.notifyAlert(usedModel, "对话请求失败", fmt.Sprintf("模型：%s\n渠道：%s\n错误：%s",
				usedModel, safeName(usedProvider, firstProvider(ps)), err.Error()))
			s.logUsage(store.UsageLog{
				Capability: store.CapChat, Model: usedModel, ProviderName: safeName(usedProvider, firstProvider(ps)),
				APIKeyName: key.Name, Status: 502, Stream: true, LatencyMS: latency,
				Error: err.Error(), RequestBody: requestBody,
			})
			return
		}
		if out != nil && out.Usage.TotalTokens > 0 {
			sse.send(map[string]any{
				"id": id, "object": "chat.completion.chunk", "created": created, "model": usedModel,
				"choices": []any{},
				"usage":   usageMap(out.Usage),
			})
		}
		if withMeta {
			meta := relayMeta(usedModel, requested, usedProvider, difficulty)
			if out != nil && out.UpstreamName != "" {
				meta["upstream"] = out.UpstreamName
			}
			sse.send(meta)
		}
		sse.done()
		usage := relay.Usage{}
		if out != nil {
			usage = out.Usage
		}
		s.logUsage(store.UsageLog{
			Capability: store.CapChat, Model: usedModel, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 200, Stream: true, LatencyMS: latency,
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			RequestBody:     requestBody,
			ResponsePreview: s.fullLogBody(contextNote(plan), contentBuf.String(), reasoningBuf.String()),
		})
		return
	}

	// 响应缓存：非流式、非 auto 时先查缓存，命中直接回包。
	if in.Model != relay.AutoModelName && s.options().CacheTTL > 0 {
		if s.tryCache(w, cacheKey(in.Model, in.Raw)) {
			return
		}
	}
	out, usedProvider, usedModel, err := callChat(nil)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.notifyAlert(usedModel, "对话请求失败", fmt.Sprintf("模型：%s\n渠道：%s\n错误：%s",
			usedModel, safeName(usedProvider, firstProvider(ps)), err.Error()))
		s.logUsage(store.UsageLog{
			Capability: store.CapChat, Model: usedModel, ProviderName: safeName(usedProvider, firstProvider(ps)),
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
		"model":   usedModel,
		"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": out.FinishReason,
		}},
	}
	if out.Usage.TotalTokens > 0 {
		resp["usage"] = usageMap(out.Usage)
	}
	if withMeta {
		resp["provider"] = usedProvider.Name
		if out.UpstreamName != "" && out.UpstreamName != usedModel {
			resp["upstream"] = out.UpstreamName
		}
		if requested != usedModel {
			resp["requested"] = requested
		}
		if difficulty != nil {
			resp["difficulty"] = map[string]any{
				"score": difficulty.Score, "tier": difficulty.Tier, "reason": difficulty.Reason,
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
	// 写入缓存（仅非 auto 且已开启）。
	if in.Model != relay.AutoModelName && s.options().CacheTTL > 0 {
		if b, e := json.Marshal(resp); e == nil {
			s.cache.set(cacheKey(in.Model, in.Raw), b, "application/json", s.options().CacheTTL)
		}
	}
	cost := computeCost(m, out.Usage.PromptTokens, out.Usage.CompletionTokens, 0)
	s.mlimiter.record(m.ID, out.Usage.TotalTokens)
	s.logUsage(store.UsageLog{
		Capability: store.CapChat, Model: usedModel, ProviderName: safeName(usedProvider, firstProvider(ps)),
		APIKeyName: key.Name, Status: 200, LatencyMS: latency, Cost: cost,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens,
		RequestBody: requestBody, ResponsePreview: s.fullLogBody(contextNote(plan), out.Content, out.Reasoning),
	})
}

// ---------- /v1/embeddings ----------

// maxAudioBytes 限制单个音频文件大小，避免超大上传拖垮内存。
const maxAudioBytes = 64 << 20

// parseTranscription 解析 multipart 上传：音频文件与其余文本字段分开保存，
// 转发时由适配器重新编码，这样既能把 model 换成本次命中的上游模型名，其余参数也不丢失。
func parseTranscription(r *http.Request) (*relay.TranscriptionInput, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errors.New("请求需为 multipart/form-data 格式：" + err.Error())
	}
	in := &relay.TranscriptionInput{Options: map[string]any{}}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("读取上传内容失败：" + err.Error())
		}
		name := part.FormName()
		fileName := part.FileName()
		if fileName != "" {
			// 只取第一个音频文件，多余的忽略。
			if len(in.FileData) > 0 {
				part.Close()
				continue
			}
			data, err := io.ReadAll(io.LimitReader(part, maxAudioBytes+1))
			part.Close()
			if err != nil {
				return nil, errors.New("读取音频文件失败：" + err.Error())
			}
			if len(data) > maxAudioBytes {
				return nil, fmt.Errorf("音频文件不能超过 %d MB", maxAudioBytes>>20)
			}
			in.FileName, in.FileData = fileName, data
			continue
		}
		val, err := io.ReadAll(io.LimitReader(part, 64<<10))
		part.Close()
		if err != nil {
			return nil, errors.New("读取表单字段失败：" + err.Error())
		}
		switch name {
		case "model":
			in.Model = strings.TrimSpace(string(val))
		case "":
			// 没有名字的字段忽略
		default:
			in.Options[name] = strings.TrimSpace(string(val))
		}
	}
	return in, nil
}

// handleSpeech 处理 POST /v1/audio/speech（文本转语音）。
// 音频是二进制流，这里不解析，校验通过后边读边转发给客户端。
func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	s.runSpeech(w, r, key, raw, false)
}

// runSpeech 是语音合成的公共实现。withMeta 为真时走面板调试台，可以指定具体上游。
func (s *Server) runSpeech(w http.ResponseWriter, r *http.Request, key store.APIKey, raw map[string]any, withMeta bool) {
	// 调试台专用字段先摘掉，避免被透传给上游。
	pinProvider, pinName := takeUpstreamPin(raw)
	model, _ := raw["model"].(string)
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	input, _ := raw["input"].(string)
	if strings.TrimSpace(input) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 input")
		return
	}
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	if withMeta && (pinProvider > 0 || pinName != "") {
		m, ps, err = s.pinUpstream(m, ps, pinProvider, pinName)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}
	in := &relay.SpeechInput{Model: model, Input: input, Options: map[string]any{}}
	in.Voice, _ = raw["voice"].(string)
	in.Format, _ = raw["response_format"].(string)
	if v, ok := raw["speed"].(float64); ok {
		in.Speed = v
	}
	for k, v := range raw {
		switch k {
		case "model", "input", "voice", "response_format", "speed":
			continue
		}
		in.Options[k] = v
	}

	start := time.Now()
	out, usedProvider, err := s.eng.Speech(r.Context(), m, ps, in)
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapSpeech, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 502, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer out.Body.Close()

	ct := strings.TrimSpace(out.ContentType)
	if ct == "" {
		ct = "audio/mpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	written, copyErr := io.Copy(w, out.Body)

	entry := store.UsageLog{
		Capability: store.CapSpeech, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
		APIKeyName: key.Name, LatencyMS: time.Since(start).Milliseconds(),
		ResponsePreview: fmt.Sprintf("音频 %d 字节（%s）", written, ct),
	}
	if copyErr != nil {
		// 响应头已经发出，无法再改写状态码，只能如实记日志。
		entry.Status = 502
		entry.Error = "下发音频失败：" + copyErr.Error()
		s.logUsage(entry)
		return
	}
	entry.Status = 200
	s.logUsage(entry)
}

// handleTranscription 处理 POST /v1/audio/transcriptions（语音转文字）。
func (s *Server) handleTranscription(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	s.runTranscription(w, r, key, false)
}

// runTranscription 是语音识别的公共实现。withMeta 为真时走面板调试台，可以指定具体上游。
func (s *Server) runTranscription(w http.ResponseWriter, r *http.Request, key store.APIKey, withMeta bool) {
	in, err := parseTranscription(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if strings.TrimSpace(in.Model) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 model")
		return
	}
	if !keyAllowed(key, in.Model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+in.Model)
		return
	}
	if len(in.FileData) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少必填字段 file（要识别的音频文件）")
		return
	}
	m, ps, err := s.eng.ResolveAll(in.Model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	// 调试台专用字段先摘掉，避免被当作表单参数转发给上游。
	pinProvider, pinName := takeUpstreamPin(in.Options)
	if withMeta && (pinProvider > 0 || pinName != "") {
		m, ps, err = s.pinUpstream(m, ps, pinProvider, pinName)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}

	start := time.Now()
	out, usedProvider, err := s.eng.Transcription(r.Context(), m, ps, in)
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapTranscription, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
			APIKeyName: key.Name, Status: 502, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if out.Raw == nil {
		out.Raw = map[string]any{"text": out.Text}
	}
	writeJSON(w, http.StatusOK, out.Raw)
	s.logUsage(store.UsageLog{
		Capability: store.CapTranscription, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
		APIKeyName: key.Name, Status: 200, LatencyMS: time.Since(start).Milliseconds(),
		ResponsePreview: truncateStr(out.Text, s.logBodyLimit()),
	})
}

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
	if !s.checkModelLimit(w, m) {
		return
	}
	start := time.Now()
	out, usedProvider, err := s.eng.Embedding(r.Context(), m, ps, &relay.EmbeddingInput{Model: model, Input: inputs})
	if err != nil {
		s.notifyAlert(m.Name, "向量化请求失败", fmt.Sprintf("模型：%s\n渠道：%s\n错误：%s",
			m.Name, safeName(usedProvider, firstProvider(ps)), err.Error()))
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
	s.mlimiter.record(m.ID, out.Usage.TotalTokens)
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
	if err := s.applySkill(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.runImage(w, r, key, raw, false)
}

// runImage 执行图片生成；raw 已由调用方解析并完成技能渲染。
// withMeta 为 true 时（面板调试台）会在响应里带上命中的上游。
func (s *Server) runImage(w http.ResponseWriter, r *http.Request, key store.APIKey, raw map[string]any, withMeta bool) {
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
	in.Images = parseImageRefs(raw)
	for k, v := range raw {
		switch k {
		case "model", "prompt", "n", "size", "response_format",
			"image", "images", "image_url", "input_image", "reference_images",
			// 面板调试台专用的「指定上游」字段。
			"provider_id", "upstream_name":
			continue
		}
		in.Options[k] = v
	}
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	if !s.checkModelLimit(w, m) {
		return
	}
	// 调试台专用字段：无论是否生效都要摘掉，避免透传给上游。
	pinProvider, pinName := takeUpstreamPin(raw)
	if withMeta && (pinProvider > 0 || pinName != "") {
		m, ps, err = s.pinUpstream(m, ps, pinProvider, pinName)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}
	start := time.Now()
	requestBody := ""
	if s.options().LogBody {
		requestBody = previewOf(raw, s.logBodyLimit())
	}
	out, usedProvider, err := s.eng.Image(r.Context(), m, ps, in)
	if err != nil {
		s.notifyAlert(m.Name, "图片生成请求失败", fmt.Sprintf("模型：%s\n渠道：%s\n错误：%s",
			m.Name, safeName(usedProvider, firstProvider(ps)), err.Error()))
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
	if withMeta {
		resp["provider"] = usedProvider.Name
	}
	writeJSON(w, http.StatusOK, resp)
	var imgTokens int
	if out.Usage != nil {
		imgTokens = out.Usage.TotalTokens
	}
	cost := computeCost(m, 0, 0, len(out.Items))
	s.mlimiter.record(m.ID, imgTokens)
	s.logUsage(store.UsageLog{
		Capability: store.CapImage, Model: m.Name, ProviderName: safeName(usedProvider, firstProvider(ps)),
		APIKeyName: key.Name, Status: 200, LatencyMS: time.Since(start).Milliseconds(),
		RequestBody: requestBody, ResponsePreview: fmt.Sprintf("生成 %d 张图片", len(data)),
		Cost: cost,
	})
}

// ---------- /v1/videos ----------

func (s *Server) handleVideoCreate(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	if err := s.applySkill(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.runVideoCreate(w, r, key, raw, false)
}

// runVideoCreate 提交视频任务；raw 已由调用方解析并完成技能渲染。
// withMeta 为 true 时（面板调试台）会在响应里带上命中的上游。
func (s *Server) runVideoCreate(w http.ResponseWriter, r *http.Request, key store.APIKey, raw map[string]any, withMeta bool) {
	model, _ := raw["model"].(string)
	if !keyAllowed(key, model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权调用模型 "+model)
		return
	}
	in := &relay.VideoInput{Model: model, Extra: map[string]any{}}
	in.Prompt, _ = raw["prompt"].(string)
	// 兼容常见写法：单图、首尾帧、参考图 / 音频列表。
	in.FirstFrame = firstString(raw, "first_frame", "first_frame_image", "image", "image_url", "input_reference")
	in.LastFrame = firstString(raw, "last_frame", "last_frame_image")
	in.ImageURL = in.FirstFrame
	in.Mode, _ = raw["mode"].(string)
	in.Images = stringList(raw, "images", "reference_images")
	in.Audios = stringList(raw, "audios", "reference_audios")
	in.Size, _ = raw["size"].(string)
	in.AspectRatio = firstString(raw, "aspect_ratio", "ratio")
	// 时长：数字与字符串两种写法都接受（Agnes 的 seconds 是字符串）。
	in.Duration = intOf(raw, "duration", "seconds")
	if strings.TrimSpace(in.Prompt) == "" && in.FirstFrame == "" && len(in.Images) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"缺少必填字段 prompt（或 image / images 用于图生视频）")
		return
	}
	for k, v := range raw {
		switch k {
		case "model", "prompt", "mode",
			"image", "image_url", "images", "reference_images",
			"first_frame", "first_frame_image", "last_frame", "last_frame_image",
			"audios", "reference_audios", "input_reference",
			"size", "duration", "seconds", "aspect_ratio", "ratio",
			// 面板调试台专用的「指定上游」字段。
			"provider_id", "upstream_name":
			continue
		}
		in.Extra[k] = v
	}
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "模型不可用："+err.Error())
		return
	}
	// 调试台专用字段：无论是否生效都要摘掉，避免透传给上游。
	pinProvider, pinName := takeUpstreamPin(raw)
	if withMeta && (pinProvider > 0 || pinName != "") {
		m, ps, err = s.pinUpstream(m, ps, pinProvider, pinName)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}
	start := time.Now()
	task, err := s.eng.CreateVideo(r.Context(), m, ps, in)
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: store.CapVideo, Model: m.Name, ProviderName: firstProvider(ps).Name,
			APIKeyName: key.Name, Status: 502, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	resp := s.taskResponse(task)
	if withMeta {
		resp["provider"] = providerNameByID(ps, task.ProviderID)
	}
	writeJSON(w, http.StatusOK, resp)
	s.logUsage(store.UsageLog{
		Capability: store.CapVideo, Model: m.Name, ProviderName: providerNameByID(ps, task.ProviderID),
		APIKeyName: key.Name, Status: 200, LatencyMS: time.Since(start).Milliseconds(),
		RequestBody: previewOf(raw, s.logBodyLimit()), ResponsePreview: "已提交视频任务 " + task.TaskID,
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

// handleVideoList 列出该秘钥可见的视频任务。
func (s *Server) handleVideoList(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	list, err := s.st.ListVideoTasks(50)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	data := make([]map[string]any, 0, len(list))
	for _, t := range list {
		if !keyAllowed(key, t.Model) {
			continue
		}
		data = append(data, s.taskResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// handleVideoDelete 删除本地任务记录，可用于取消或清理。
func (s *Server) handleVideoDelete(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	id := r.PathValue("id")
	task, err := s.st.GetVideoTaskByTaskID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "invalid_request_error", "视频任务不存在："+id)
		return
	}
	if !keyAllowed(key, task.Model) {
		writeErr(w, http.StatusForbidden, "invalid_request_error", "该 API 秘钥无权操作此任务")
		return
	}
	if err := s.st.DeleteVideoTask(task.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video.generation", "deleted": true})
}

// handleVideoStream 以 SSE 持续推送任务状态，直到结束或超过 30 分钟。
// 状态由后台轮询器刷新，这里只读取数据库，不额外给上游施压。
func (s *Server) handleVideoStream(w http.ResponseWriter, r *http.Request, key store.APIKey) {
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

	sse := newSSEWriter(w)
	defer sse.done()
	stopHeartbeat := s.startHeartbeat(sse)
	defer stopHeartbeat()

	last := ""
	push := func(cur store.VideoTask) bool {
		signature := cur.Status + ":" + fmt.Sprint(cur.Progress)
		finished := cur.Status == store.TaskSucceeded || cur.Status == store.TaskFailed
		if signature != last || finished {
			last = signature
			_ = sse.send(s.taskResponse(cur))
		}
		return finished
	}
	if push(task) {
		return
	}

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Minute)
	defer deadline.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			cur, err := s.st.GetVideoTaskByTaskID(id)
			if err != nil {
				return
			}
			if push(cur) {
				return
			}
		}
	}
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

// logUsage 写入调用日志。
// 隐私模式下（设置里关闭「记录请求与响应体」）统一丢弃请求体与响应预览，
// 这样所有调用点无需各自判断，也不会遗漏。
func (s *Server) logUsage(l store.UsageLog) {
	if !s.options().LogBody {
		l.RequestBody = ""
		l.ResponsePreview = ""
	}
	s.st.AddUsageLog(l)
}

// logBodyLimit 返回单条日志请求/响应体的记录上限（字符数）。
// 设置以 KB 配置；按 rune 截断，中文场景实际字节数只会更小，偏安全。
func (s *Server) logBodyLimit() int {
	n := s.options().LogBodyLimit * 1024
	if n <= 0 {
		n = 64 * 1024
	}
	return n
}

// fullLogBody 把正文与思考过程拼成一条完整的输出记录（含上下文裁剪备注），
// 超出记录上限时截断。流式与非流式对话共用，保证日志里能看到模型到底说了什么。
func (s *Server) fullLogBody(note, content, reasoning string) string {
	if reasoning != "" {
		content = "【思考过程】\n" + reasoning + "\n\n【回复正文】\n" + content
	}
	return truncateStr(note+content, s.logBodyLimit())
}

func safeName(p store.Provider, fallback store.Provider) string {
	if p.Name != "" {
		return p.Name
	}
	return fallback.Name
}

// contextNote 在发生了上下文裁剪时给日志预览加前缀，便于排查「历史为何变短」。
func contextNote(plan relay.ContextPlan) string {
	if plan.Applied && plan.Trimmed > 0 {
		return fmt.Sprintf("[已裁剪 %d 条历史消息] ", plan.Trimmed)
	}
	return ""
}

// firstProvider 取模型的默认渠道（绑定列表首个），用于日志兜底。
func firstProvider(ps []store.Provider) store.Provider {
	if len(ps) > 0 {
		return ps[0]
	}
	return store.Provider{}
}

// parseImageRefs 收集图片请求里的输入参考图（图生图 / 多图合成）。
// 兼容常见字段名，并额外识别 Agnes 等把输入图放在 extra_body.image 的写法。
func parseImageRefs(raw map[string]any) []string {
	out := []string{}
	seen := map[string]bool{}
	appendRef := func(v any) {
		push := func(s string) {
			if s = strings.TrimSpace(s); s == "" || seen[s] {
				return
			}
			seen[s] = true
			out = append(out, s)
		}
		switch t := v.(type) {
		case string:
			push(t)
		case []any:
			for _, it := range t {
				if s, ok := it.(string); ok {
					push(s)
				}
			}
		}
	}
	for _, key := range []string{"image", "images", "image_url", "input_image", "reference_images"} {
		appendRef(raw[key])
	}
	if extra, ok := raw["extra_body"].(map[string]any); ok {
		appendRef(extra["image"])
		appendRef(extra["images"])
	}
	return out
}

// providerNameByID 找出本次实际命中的渠道名，找不到时回退到首个渠道。
func providerNameByID(ps []store.Provider, id int64) string {
	for _, p := range ps {
		if p.ID == id {
			return p.Name
		}
	}
	return firstProvider(ps).Name
}

// relayMeta 是面板调试台专用的元信息分片，用于展示本次调用最终命中的上游。
// requested 与 difficulty 仅在 auto 自动模型下有意义。
func relayMeta(model, requested string, p store.Provider, d *relay.Difficulty) map[string]any {
	out := map[string]any{
		"object":      "relay.meta",
		"model":       model,
		"provider":    p.Name,
		"provider_id": p.ID,
	}
	if requested != "" && requested != model {
		out["requested"] = requested
	}
	if d != nil {
		out["difficulty"] = map[string]any{"score": d.Score, "tier": d.Tier, "reason": d.Reason}
	}
	return out
}

func usageMap(u relay.Usage) map[string]any {
	m := map[string]any{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.TotalTokens,
	}
	if u.CacheReadTokens > 0 {
		m["cache_read_tokens"] = u.CacheReadTokens
	}
	if u.CacheCreationTokens > 0 {
		m["cache_creation_tokens"] = u.CacheCreationTokens
	}
	return m
}

// computeCost 按模型单价估算本次调用成本（元/美元自定）。
// 文本按输入/输出 token 各算；图片按张数算（有单价时）。
func computeCost(m store.Model, promptTokens, completionTokens, images int) float64 {
	c := m.Config
	var cost float64
	if promptTokens > 0 && c.PriceIn > 0 {
		cost += float64(promptTokens) / 1000.0 * c.PriceIn
	}
	if completionTokens > 0 && c.PriceOut > 0 {
		cost += float64(completionTokens) / 1000.0 * c.PriceOut
	}
	if images > 0 && c.PriceImage > 0 {
		cost += float64(images) * c.PriceImage
	}
	return cost
}

// checkModelLimit 校验对外模型的 RPM/TPM 限速；超限时直接写 429 并返回 false。
func (s *Server) checkModelLimit(w http.ResponseWriter, m store.Model) bool {
	rpm, tpm := m.Config.RPM, m.Config.TPM
	if rpm <= 0 && tpm <= 0 {
		return true
	}
	if !s.mlimiter.allow(m.ID, rpm, tpm, 0) {
		writeErr(w, http.StatusTooManyRequests, "rate_limit_error", "该模型每分钟请求/Token 已达上限")
		return false
	}
	return true
}

// tryCache 命中则返回缓存的响应；未命中返回 false。仅用于非流式请求。
func (s *Server) tryCache(w http.ResponseWriter, key string) bool {
	if data, ctype, ok := s.cache.get(key); ok {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return true
	}
	return false
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

// intOf 从请求体里取整数，兼容数字与数字字符串两种写法。
func intOf(raw map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := raw[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return 0
}

// stringList 收集多个字段名下的字符串列表，兼容只传单个字符串的写法。
func stringList(raw map[string]any, keys ...string) []string {
	out := []string{}
	for _, k := range keys {
		switch v := raw[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				out = append(out, s)
			}
		case []any:
			for _, it := range v {
				s, ok := it.(string)
				if !ok {
					continue
				}
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
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
