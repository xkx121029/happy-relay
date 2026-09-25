package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"time"

	"happy-relay/internal/store"
)

// GeminiAdapter 适配 Google Gemini 原生协议：
// 对话用 generateContent / streamGenerateContent，图片用 predict，向量用 embedContent。
type GeminiAdapter struct{}

// ---------- 公共辅助 ----------

// geminiModelName 去掉上游模型名里多余的 "models/" 前缀，避免拼出 /models/models/xxx。
func geminiModelName(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), "models/")
}

func geminiURL(up Upstream, model, action string) string {
	return strings.TrimRight(up.BaseURL, "/") + "/models/" + geminiModelName(model) + ":" + action
}

// geminiHeaders 使用 Gemini 官方的 x-goog-api-key 鉴权；密钥为空时不设该请求头。
func geminiHeaders(up Upstream) map[string]string {
	if up.APIKey == "" {
		return nil
	}
	return map[string]string{"x-goog-api-key": up.APIKey}
}

// geminiGuessMIME 从图片地址后缀猜测 MIME，猜不出时按 JPEG 处理。
func geminiGuessMIME(rawURL string) string {
	u := rawURL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	switch strings.ToLower(path.Ext(u)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".heic":
		return "image/heic"
	}
	return "image/jpeg"
}

// toolArgObject 把工具调用的参数 JSON 字符串还原成对象；
// 参数字符串不是合法 JSON 时包一层 raw 字段，保证请求体依然合法。
func toolArgObject(args string) any {
	s := strings.TrimSpace(args)
	if s == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return map[string]any{"raw": args}
	}
	return v
}

// stopSequences 归一化 stop 参数：单个字符串或字符串数组都接受。
func stopSequences(in *ChatInput) []string {
	v, ok := in.Options["stop"]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			if s, ok := it.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ---------- 请求构造 ----------

// geminiParts 把一条归一化消息转成 Gemini 的 parts 数组。
func geminiParts(msg ChatMessage) []any {
	// 工具结果必须以 functionResponse 形式返回。
	if msg.Role == "tool" {
		return []any{map[string]any{"functionResponse": map[string]any{
			"name":     firstNonEmpty(msg.Name, "tool"),
			"response": map[string]any{"result": msg.Content},
		}}}
	}
	var parts []any
	for _, p := range msg.Parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case "image":
			if len(p.Data) > 0 {
				parts = append(parts, map[string]any{"inlineData": map[string]any{
					"mimeType": firstNonEmpty(p.MIME, "image/jpeg"),
					"data":     base64.StdEncoding.EncodeToString(p.Data),
				}})
			} else if p.Remote != "" {
				// 远程图片无法内联成 base64，改用 fileData 直接透传地址。
				parts = append(parts, map[string]any{"fileData": map[string]any{
					"mimeType": geminiGuessMIME(p.Remote),
					"fileUri":  p.Remote,
				}})
			}
		}
	}
	if len(parts) == 0 && msg.Content != "" {
		parts = append(parts, map[string]any{"text": msg.Content})
	}
	for _, c := range msg.ToolCalls {
		parts = append(parts, map[string]any{"functionCall": map[string]any{
			"name": c.Name,
			"args": toolArgObject(c.Arguments),
		}})
	}
	return parts
}

// geminiContents 把对话消息整理成 contents：角色只有 user/model，相邻同角色合并成一个块。
func geminiContents(in *ChatInput) []any {
	var contents []any
	for _, msg := range in.DialogMessages() {
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}
		parts := geminiParts(msg)
		if len(parts) == 0 {
			continue
		}
		if n := len(contents); n > 0 {
			prev := contents[n-1].(map[string]any)
			if prev["role"] == role {
				prev["parts"] = append(prev["parts"].([]any), parts...)
				continue
			}
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	return contents
}

// geminiGenerationConfig 把通用参数映射到 Gemini 的 generationConfig。
func geminiGenerationConfig(in *ChatInput) map[string]any {
	cfg := map[string]any{}
	if v, ok := in.FloatOption("temperature"); ok {
		cfg["temperature"] = v
	}
	if v, ok := in.FloatOption("top_p"); ok {
		cfg["topP"] = v
	}
	maxTokens := 0
	if v, ok := in.IntOption("max_tokens"); ok {
		maxTokens = v
	} else if v, ok := in.IntOption("max_completion_tokens"); ok {
		maxTokens = v
	}
	if maxTokens > 0 {
		cfg["maxOutputTokens"] = maxTokens
	}
	if seqs := stopSequences(in); len(seqs) > 0 {
		cfg["stopSequences"] = seqs
	}
	return cfg
}

// geminiToolDecls 把 OpenAI 风格的 tools 转成 Gemini 的 functionDeclarations。
func geminiToolDecls(in *ChatInput) []any {
	tools, ok := in.Options["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	decls := make([]any, 0, len(tools))
	for _, tv := range tools {
		tool, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		name := asString(fn["name"])
		if name == "" {
			continue
		}
		decl := map[string]any{"name": name}
		if d := asString(fn["description"]); d != "" {
			decl["description"] = d
		}
		if p, ok := fn["parameters"]; ok && p != nil {
			decl["parameters"] = p
		}
		decls = append(decls, decl)
	}
	return decls
}

func geminiChatBody(in *ChatInput) map[string]any {
	body := map[string]any{"contents": geminiContents(in)}
	if sys := in.SystemPrompt(); sys != "" {
		body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": sys}}}
	}
	if cfg := geminiGenerationConfig(in); len(cfg) > 0 {
		body["generationConfig"] = cfg
	}
	if decls := geminiToolDecls(in); len(decls) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	return body
}

// ---------- 对话 ----------

func (a *GeminiAdapter) Chat(ctx context.Context, up Upstream, m store.Model, in *ChatInput, emit StreamFunc) (*ChatOutput, error) {
	body := geminiChatBody(in)
	if in.Stream && emit != nil {
		resp, err := doJSON(ctx, up, "POST",
			geminiURL(up, m.UpstreamName, "streamGenerateContent")+"?alt=sse", geminiHeaders(up), body)
		if err != nil {
			return nil, err
		}
		return a.consumeStream(resp, m, emit)
	}
	resp, err := doJSON(ctx, up, "POST", geminiURL(up, m.UpstreamName, "generateContent"), geminiHeaders(up), body)
	if err != nil {
		return nil, err
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	return parseGeminiChat(raw, m.Name)
}

// consumeStream 逐条读取 Gemini 的 SSE 事件并下发文本增量。
func (a *GeminiAdapter) consumeStream(resp *http.Response, m store.Model, emit StreamFunc) (*ChatOutput, error) {
	defer resp.Body.Close()
	reader := newSSEReader(resp.Body)
	out := &ChatOutput{ID: "gemini-relay", Model: m.Name}
	var content strings.Builder
	sentRole := false
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
		if id := jsonPathString(chunk, "responseId"); id != "" {
			out.ID = id
		}
		delta := StreamDelta{}
		has := false
		if !sentRole {
			delta.Role = "assistant"
			sentRole = true
			has = true
		}
		candidates, _ := chunk["candidates"].([]any)
		for _, cv := range candidates {
			c, _ := cv.(map[string]any)
			if c == nil {
				continue
			}
			if texts := geminiTextParts(c["content"]); len(texts) > 0 {
				s := strings.Join(texts, "")
				delta.Content += s
				content.WriteString(s)
				has = true
			}
			if calls := geminiFunctionCalls(c["content"]); len(calls) > 0 {
				delta.ToolCalls = calls
				out.ToolCalls = append(out.ToolCalls, calls...)
				has = true
			}
			if fr := geminiFinishReason(asString(c["finishReason"])); fr != "" {
				delta.FinishReason = fr
				out.FinishReason = fr
				has = true
			}
		}
		if u, ok := chunk["usageMetadata"].(map[string]any); ok {
			if parsed := geminiUsage(u); parsed != nil {
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
	if out.FinishReason == "" {
		if len(out.ToolCalls) > 0 {
			out.FinishReason = "tool_calls"
		} else {
			out.FinishReason = "stop"
		}
	}
	return out, nil
}

// ---------- 响应解析 ----------

// geminiTextParts 取出 content 里所有文本片段。
func geminiTextParts(content any) []string {
	obj, ok := content.(map[string]any)
	if !ok {
		return nil
	}
	parts, _ := obj["parts"].([]any)
	var out []string
	for _, pv := range parts {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		if t := asString(p["text"]); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// geminiFunctionCalls 把 content 里的 functionCall 片段转成统一的 ToolCall。
func geminiFunctionCalls(content any) []ToolCall {
	obj, ok := content.(map[string]any)
	if !ok {
		return nil
	}
	parts, _ := obj["parts"].([]any)
	var out []ToolCall
	for _, pv := range parts {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		fc, ok := p["functionCall"].(map[string]any)
		if !ok {
			continue
		}
		call := ToolCall{Name: asString(fc["name"])}
		if call.Name == "" {
			continue
		}
		if args, ok := fc["args"]; ok && args != nil {
			b, _ := json.Marshal(args)
			call.Arguments = string(b)
		}
		out = append(out, call)
	}
	return out
}

func geminiIntOf(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}

func geminiUsage(u map[string]any) *Usage {
	prompt := geminiIntOf(u["promptTokenCount"])
	completion := geminiIntOf(u["candidatesTokenCount"])
	total := geminiIntOf(u["totalTokenCount"])
	if total == 0 {
		total = prompt + completion
	}
	return &Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
}

func geminiFinishReason(v string) string {
	switch v {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII":
		return "content_filter"
	case "":
		return ""
	}
	return "stop"
}

func parseGeminiChat(raw map[string]any, model string) (*ChatOutput, error) {
	out := &ChatOutput{ID: "gemini-relay", Model: model}
	if id := jsonPathString(raw, "responseId"); id != "" {
		out.ID = id
	}
	candidates, _ := raw["candidates"].([]any)
	if len(candidates) > 0 {
		c, _ := candidates[0].(map[string]any)
		if c != nil {
			out.Content = strings.Join(geminiTextParts(c["content"]), "")
			out.ToolCalls = geminiFunctionCalls(c["content"])
			out.FinishReason = geminiFinishReason(asString(c["finishReason"]))
		}
	}
	if u, ok := raw["usageMetadata"].(map[string]any); ok {
		if parsed := geminiUsage(u); parsed != nil {
			out.Usage = *parsed
		}
	}
	if out.FinishReason == "" {
		if len(out.ToolCalls) > 0 {
			out.FinishReason = "tool_calls"
		} else {
			out.FinishReason = "stop"
		}
	}
	// Gemini 被安全策略拦截时可能以 200 + error 字段返回，这里转成上游错误。
	if len(candidates) == 0 && out.Content == "" {
		if msg := jsonPathString(raw, "error.message"); msg != "" {
			b, _ := json.Marshal(raw)
			return nil, &UpstreamError{StatusCode: 200, Body: string(b), Message: "Gemini 返回错误：" + msg}
		}
	}
	return out, nil
}

// ---------- 图片生成 ----------

func (a *GeminiAdapter) Image(ctx context.Context, up Upstream, m store.Model, in *ImageInput) (*ImageOutput, error) {
	params := map[string]any{}
	// Options 里的附加参数原样透传成 predict 参数，便于设置 outputOptions 等扩展项。
	for k, v := range in.Options {
		switch k {
		case "parameters", "size", "n", "response_format", "prompt", "model":
			continue
		}
		params[k] = v
	}
	if in.N > 0 {
		params["sampleCount"] = in.N
	}
	if in.Size != "" {
		params["aspectRatio"] = in.Size
	}
	// Options["parameters"] 作为显式覆盖项最后合并，优先级最高。
	if extra, ok := in.Options["parameters"].(map[string]any); ok {
		for k, v := range extra {
			params[k] = v
		}
	}
	body := map[string]any{
		"instances":  []any{map[string]any{"prompt": in.Prompt}},
		"parameters": params,
	}
	resp, err := doJSON(ctx, up, "POST", geminiURL(up, m.UpstreamName, "predict"), geminiHeaders(up), body)
	if err != nil {
		return nil, err
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	out := &ImageOutput{Created: time.Now().Unix()}
	predictions, _ := raw["predictions"].([]any)
	for _, pv := range predictions {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		b64 := firstNonEmpty(asString(p["bytesBase64Encoded"]), jsonPathString(p, "image.imageBytes"))
		if b64 == "" {
			continue
		}
		out.Items = append(out.Items, ImageItem{B64: b64})
	}
	if len(out.Items) == 0 {
		b, _ := json.Marshal(raw)
		return nil, &UpstreamError{
			StatusCode: 200,
			Body:       string(b),
			Message:    "上游未返回任何图片数据，响应：" + truncate(string(b), 300),
		}
	}
	return out, nil
}

// ---------- 向量化 ----------

func (a *GeminiAdapter) Embedding(ctx context.Context, up Upstream, m store.Model, in *EmbeddingInput) (*EmbeddingOutput, error) {
	out := &EmbeddingOutput{}
	// embedContent 一次只接受一条内容，多条输入按顺序循环调用。
	for _, text := range in.Input {
		body := map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{"text": text}}},
		}
		resp, err := doJSON(ctx, up, "POST", geminiURL(up, m.UpstreamName, "embedContent"), geminiHeaders(up), body)
		if err != nil {
			return nil, err
		}
		raw, err := decodeJSONBytes(resp)
		if err != nil {
			return nil, err
		}
		values, _ := jsonPath(raw, "embedding.values").([]any)
		vec := make([]float32, 0, len(values))
		for _, v := range values {
			if f, ok := v.(float64); ok {
				vec = append(vec, float32(f))
			}
		}
		if len(vec) == 0 {
			b, _ := json.Marshal(raw)
			return nil, &UpstreamError{
				StatusCode: 200,
				Body:       string(b),
				Message:    "上游未返回向量数据，响应：" + truncate(string(b), 300),
			}
		}
		out.Vectors = append(out.Vectors, vec)
	}
	return out, nil
}