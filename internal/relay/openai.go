package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"

	"happy-relay/internal/store"
)

// OpenAIAdapter 适配 OpenAI 风格的 /v1 接口，也是绝大多数中转商与自部署服务的通用协议。
type OpenAIAdapter struct{}

func (a *OpenAIAdapter) url(up Upstream, path string) string {
	return strings.TrimRight(up.BaseURL, "/") + path
}

func authHeader(up Upstream) map[string]string {
	if up.APIKey == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + up.APIKey}
}

// buildBody 以原始请求体为基底，套用模型默认参数并替换上游模型名。
// 这样未知字段也能原样透传给上游，兼容各家扩展参数。
// 若调用方只填了结构化的 in.Messages（程序化调用，Raw 为空），
// 这里兜底把消息序列化成 OpenAI wire 格式，避免上游收到空 messages。
func buildBody(m store.Model, in *ChatInput) map[string]any {
	body := make(map[string]any, len(in.Raw)+len(in.Options)+2)
	for k, v := range in.Raw {
		body[k] = v
	}
	for k, v := range in.Options {
		body[k] = v
	}
	if _, ok := body["messages"]; !ok && len(in.Messages) > 0 {
		body["messages"] = openAIMessages(in.Messages)
	}
	body["model"] = m.UpstreamName
	return body
}

// openAIMessages 把归一化消息序列化回 OpenAI chat completions 的 wire 格式。
func openAIMessages(msgs []ChatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		m := map[string]any{"role": msg.Role}
		switch {
		case hasImagePart(msg.Parts):
			// 多模态消息：content 是分段数组。
			parts := make([]map[string]any, 0, len(msg.Parts))
			for _, p := range msg.Parts {
				if p.Type == "image" {
					url := p.Remote
					if url == "" && len(p.Data) > 0 {
						url = "data:" + p.MIME + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
					}
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
				} else {
					parts = append(parts, map[string]any{"type": "text", "text": p.Text})
				}
			}
			m["content"] = parts
		default:
			m["content"] = msg.Content
		}
		if msg.Name != "" {
			m["name"] = msg.Name
		}
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id": tc.ID, "type": "function",
					"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
				})
			}
			m["tool_calls"] = tcs
		}
		out = append(out, m)
	}
	return out
}

func hasImagePart(parts []ContentPart) bool {
	for _, p := range parts {
		if p.Type == "image" {
			return true
		}
	}
	return false
}

func (a *OpenAIAdapter) Chat(ctx context.Context, up Upstream, m store.Model, in *ChatInput, emit StreamFunc) (*ChatOutput, error) {
	resp, err := doJSON(ctx, up, "POST", a.url(up, "/chat/completions"), authHeader(up), buildBody(m, in))
	if err != nil {
		return nil, err
	}
	if in.Stream && emit != nil {
		return a.consumeStream(resp, m, in, emit)
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	return parseOpenAIChat(raw, m.Name), nil
}

// consumeStream 逐条读取上游 SSE，把增量回调给上层。
// 上游已是 OpenAI 格式，这里做归一化后转发，未知字段直接丢弃。
func (a *OpenAIAdapter) consumeStream(resp *http.Response, m store.Model, in *ChatInput, emit StreamFunc) (*ChatOutput, error) {
	defer resp.Body.Close()
	reader := newSSEReader(resp.Body)
	out := &ChatOutput{ID: "chatcmpl-relay", Model: m.Name}
	var content, reasoning strings.Builder
	acc := newToolAccumulator()

	for {
		ev, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if id := asString(chunk["id"]); id != "" {
			out.ID = id
		}
		delta := StreamDelta{}
		has := false
		choices, _ := chunk["choices"].([]any)
		for _, cv := range choices {
			choice, _ := cv.(map[string]any)
			if fr := asString(choice["finish_reason"]); fr != "" {
				delta.FinishReason = fr
				out.FinishReason = fr
				has = true
			}
			d, _ := choice["delta"].(map[string]any)
			if d == nil {
				continue
			}
			if role := asString(d["role"]); role != "" {
				delta.Role = role
				has = true
			}
			if c := contentString(d["content"]); c != "" {
				delta.Content = c
				content.WriteString(c)
				has = true
			}
			if r := firstNonEmpty(asString(d["reasoning_content"]), asString(d["reasoning"])); r != "" {
				delta.Reasoning = r
				reasoning.WriteString(r)
				has = true
			}
			if calls := acc.add(d["tool_calls"]); len(calls) > 0 {
				delta.ToolCalls = calls
				has = true
			}
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			if parsed := parseUsage(u); parsed != nil {
				delta.Usage = parsed
				out.Usage = *parsed
				has = true
			}
		}
		if has {
			if err := emit(delta); err != nil {
				return nil, err
			}
		}
	}
	out.Content = content.String()
	out.Reasoning = reasoning.String()
	out.ToolCalls = acc.result()
	if out.FinishReason == "" {
		out.FinishReason = "stop"
	}
	return out, nil
}

// toolAccumulator 处理流式 tool_calls 分片：同 index 的 name/arguments 需要拼接。
type toolAccumulator struct {
	byIndex map[int]*ToolCall
	order   []int
}

func newToolAccumulator() *toolAccumulator {
	return &toolAccumulator{byIndex: map[int]*ToolCall{}}
}

func (t *toolAccumulator) add(v any) []ToolCall {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]ToolCall, 0, len(arr))
	for _, it := range arr {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		idx := 0
		if n, ok := obj["index"].(float64); ok {
			idx = int(n)
		}
		cur, exists := t.byIndex[idx]
		if !exists {
			cur = &ToolCall{}
			t.byIndex[idx] = cur
			t.order = append(t.order, idx)
		}
		if id := asString(obj["id"]); id != "" {
			cur.ID = id
		}
		if fn, ok := obj["function"].(map[string]any); ok {
			if name := asString(fn["name"]); name != "" {
				cur.Name = name
			}
			switch args := fn["arguments"].(type) {
			case string:
				cur.Arguments += args
			case map[string]any, []any:
				b, _ := json.Marshal(args)
				cur.Arguments += string(b)
			}
		}
		out = append(out, *cur)
	}
	return out
}

func (t *toolAccumulator) result() []ToolCall {
	if len(t.order) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(t.order))
	for _, idx := range t.order {
		if c, ok := t.byIndex[idx]; ok {
			out = append(out, *c)
		}
	}
	return out
}

// buildImageBody 组装图片生成请求体。
//
// 默认（standard）遵循 OpenAI 标准：response_format 与参考图放请求体顶层。
// 当模型配置 image_mode=extra_body 时（如 Agnes Image 2.1 Flash 明确要求），
// 这两类字段会被合并进 extra_body，避免顶层出现 response_format 导致上游报错。
// 客户端自己传入的 extra_body 字段优先，不会被覆盖。
func buildImageBody(m store.Model, in *ImageInput) map[string]any {
	body := map[string]any{
		"model":  m.UpstreamName,
		"prompt": in.Prompt,
	}
	if in.N > 0 {
		body["n"] = in.N
	}
	if in.Size != "" {
		body["size"] = in.Size
	}

	extra := map[string]any{}
	if raw, ok := in.Options["extra_body"].(map[string]any); ok {
		for k, v := range raw {
			extra[k] = v
		}
	}
	useExtra := m.Config.ImageMode == store.ImageModeExtraBody

	if len(in.Images) > 0 {
		if useExtra {
			if _, exists := extra["image"]; !exists {
				extra["image"] = in.Images
			}
		} else {
			body["image"] = in.Images
		}
	}
	if in.ResponseFormat != "" {
		if useExtra {
			if _, exists := extra["response_format"]; !exists {
				extra["response_format"] = in.ResponseFormat
			}
		} else {
			body["response_format"] = in.ResponseFormat
		}
	}

	// 其余参数原样透传，已显式设置的字段优先。
	for k, v := range in.Options {
		if k == "extra_body" {
			continue
		}
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
	if len(extra) > 0 {
		body["extra_body"] = extra
	}
	return body
}

func (a *OpenAIAdapter) Image(ctx context.Context, up Upstream, m store.Model, in *ImageInput) (*ImageOutput, error) {
	body := buildImageBody(m, in)
	resp, err := doJSON(ctx, up, "POST", a.url(up, "/images/generations"), authHeader(up), body)
	if err != nil {
		return nil, err
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	out := &ImageOutput{}
	if v, ok := raw["created"].(float64); ok {
		out.Created = int64(v)
	}
	items, _ := raw["data"].([]any)
	for _, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		item := ImageItem{}
		item.URL, _ = obj["url"].(string)
		item.B64, _ = obj["b64_json"].(string)
		item.Revised, _ = obj["revised_prompt"].(string)
		out.Items = append(out.Items, item)
	}
	if u, ok := raw["usage"].(map[string]any); ok {
		out.Usage = parseUsage(u)
	}
	if len(out.Items) == 0 {
		return nil, &UpstreamError{StatusCode: 200, Message: "上游未返回任何图片数据"}
	}
	return out, nil
}

func (a *OpenAIAdapter) Embedding(ctx context.Context, up Upstream, m store.Model, in *EmbeddingInput) (*EmbeddingOutput, error) {
	body := map[string]any{"model": m.UpstreamName, "input": in.Input}
	resp, err := doJSON(ctx, up, "POST", a.url(up, "/embeddings"), authHeader(up), body)
	if err != nil {
		return nil, err
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	out := &EmbeddingOutput{}
	items, _ := raw["data"].([]any)
	for _, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		arr, _ := obj["embedding"].([]any)
		vec := make([]float32, 0, len(arr))
		for _, v := range arr {
			if f, ok := v.(float64); ok {
				vec = append(vec, float32(f))
			}
		}
		out.Vectors = append(out.Vectors, vec)
	}
	if u, ok := raw["usage"].(map[string]any); ok {
		if parsed := parseUsage(u); parsed != nil {
			out.Usage = *parsed
		}
	}
	return out, nil
}

// ---------- 语音 ----------

// Speech 调用文本转语音接口。上游返回的是音频字节流，
// 这里只做状态码校验就把流交出去，不做解析，避免大音频占内存。
func (a *OpenAIAdapter) Speech(ctx context.Context, up Upstream, m store.Model, in *SpeechInput) (*SpeechOutput, error) {
	body := map[string]any{"model": m.UpstreamName, "input": in.Input}
	if in.Voice != "" {
		body["voice"] = in.Voice
	}
	if in.Format != "" {
		body["response_format"] = in.Format
	}
	if in.Speed > 0 {
		body["speed"] = in.Speed
	}
	for k, v := range in.Options {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
	resp, err := doJSON(ctx, up, "POST", a.url(up, "/audio/speech"), authHeader(up), body)
	if err != nil {
		return nil, err
	}
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = "audio/mpeg"
	}
	return &SpeechOutput{ContentType: ct, Body: resp.Body}, nil
}

// Transcription 调用语音转文字接口。
// 请求以 multipart 重新编码，因此这里要把客户端传来的对外模型名换成本次命中的上游模型名；
// 其余字段原样带上，language、prompt、response_format 等参数不会丢失。
func (a *OpenAIAdapter) Transcription(ctx context.Context, up Upstream, m store.Model, in *TranscriptionInput) (*TranscriptionOutput, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if len(in.FileData) > 0 {
		name := in.FileName
		if name == "" {
			name = "audio.mp3"
		}
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(in.FileData); err != nil {
			return nil, err
		}
	}
	if err := mw.WriteField("model", m.UpstreamName); err != nil {
		return nil, err
	}
	for k, v := range in.Options {
		if k == "model" || k == "file" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		if err := mw.WriteField(k, s); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	resp, err := doRaw(ctx, up, "POST", a.url(up, "/audio/transcriptions"),
		mw.FormDataContentType(), authHeader(up), buf.Bytes())
	if err != nil {
		return nil, err
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	return &TranscriptionOutput{Text: jsonPathString(raw, "text"), Raw: raw}, nil
}

// ---------- 响应解析 ----------

func parseUsage(u map[string]any) *Usage {
	get := func(k string) int {
		if v, ok := u[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	usage := &Usage{
		PromptTokens:     get("prompt_tokens"),
		CompletionTokens: get("completion_tokens"),
		TotalTokens:      get("total_tokens"),
		// Anthropic 原生 / 透传上游常把缓存字段放在顶层。
		CacheReadTokens:     get("cache_read_input_tokens"),
		CacheCreationTokens: get("cache_creation_input_tokens"),
	}
	// OpenAI 系把缓存命中放到 prompt_tokens_details 里。
	if det, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if v, ok := det["cached_tokens"].(float64); ok && v > 0 {
			usage.CacheReadTokens = int(v)
		}
		if v, ok := det["cache_read_input_tokens"].(float64); ok && v > 0 {
			usage.CacheReadTokens = int(v)
		}
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

// contentString 处理 content 既可能是字符串也可能是分片数组的情况。
func contentString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, p := range t {
			if obj, ok := p.(map[string]any); ok {
				if s, ok := obj["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func parseOpenAIChat(raw map[string]any, model string) *ChatOutput {
	out := &ChatOutput{Model: model}
	out.ID, _ = raw["id"].(string)
	if out.ID == "" {
		out.ID = "chatcmpl-relay"
	}
	choices, _ := raw["choices"].([]any)
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		msg, _ := choice["message"].(map[string]any)
		if msg == nil {
			msg, _ = choice["delta"].(map[string]any)
		}
		if msg != nil {
			out.Content = contentString(msg["content"])
			out.Reasoning = firstNonEmpty(
				asString(msg["reasoning_content"]), asString(msg["reasoning"]))
			out.ToolCalls = parseToolCalls(msg["tool_calls"])
		}
		out.FinishReason, _ = choice["finish_reason"].(string)
	}
	if out.FinishReason == "" {
		out.FinishReason = "stop"
	}
	if u, ok := raw["usage"].(map[string]any); ok {
		if parsed := parseUsage(u); parsed != nil {
			out.Usage = *parsed
		}
	}
	if out.Model == "" {
		out.Model, _ = raw["model"].(string)
	}
	return out
}

func parseToolCalls(v any) []ToolCall {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]ToolCall, 0, len(arr))
	for _, it := range arr {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		call := ToolCall{}
		call.ID, _ = obj["id"].(string)
		if fn, ok := obj["function"].(map[string]any); ok {
			call.Name, _ = fn["name"].(string)
			switch args := fn["arguments"].(type) {
			case string:
				call.Arguments = args
			case nil:
			default:
				b, _ := json.Marshal(args)
				call.Arguments = string(b)
			}
		}
		out = append(out, call)
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
