package relay

import (
	"context"
	"encoding/json"
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
func buildBody(m store.Model, in *ChatInput) map[string]any {
	body := make(map[string]any, len(in.Raw)+len(in.Options)+1)
	for k, v := range in.Raw {
		body[k] = v
	}
	for k, v := range in.Options {
		body[k] = v
	}
	body["model"] = m.UpstreamName
	return body
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

func (a *OpenAIAdapter) Image(ctx context.Context, up Upstream, m store.Model, in *ImageInput) (*ImageOutput, error) {
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
	if in.ResponseFormat != "" {
		body["response_format"] = in.ResponseFormat
	}
	for k, v := range in.Options {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
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