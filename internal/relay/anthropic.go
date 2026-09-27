package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"happy-relay/internal/store"
)

// AnthropicAdapter 适配 Anthropic Claude 原生 Messages 接口。
// 该协议只提供对话能力，图片与向量化返回 ErrUnsupported。
type AnthropicAdapter struct{}

// anthropicHeaders 使用 x-api-key 鉴权，并固定协议版本。
func anthropicHeaders(up Upstream) map[string]string {
	h := map[string]string{"anthropic-version": "2023-06-01"}
	if up.APIKey == "" {
		return h
	}
	h["x-api-key"] = up.APIKey
	return h
}

// ---------- 请求构造 ----------

// anthropicMaxTokens 取 max_tokens，缺省 4096（该字段对 Anthropic 是必填）。
func anthropicMaxTokens(in *ChatInput) int {
	if v, ok := in.IntOption("max_tokens"); ok && v > 0 {
		return v
	}
	if v, ok := in.IntOption("max_completion_tokens"); ok && v > 0 {
		return v
	}
	return 4096
}

// anthropicBlocks 把一条归一化消息转成 Anthropic 的 content 块数组。
func anthropicBlocks(msg ChatMessage) []any {
	// 工具结果必须以 tool_result 块返回。
	if msg.Role == "tool" {
		return []any{map[string]any{
			"type":        "tool_result",
			"tool_use_id": msg.ToolCallID,
			"content":     msg.Content,
		}}
	}
	var blocks []any
	for _, p := range msg.Parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
			}
		case "image":
			if len(p.Data) > 0 {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
					"type":       "base64",
					"media_type": firstNonEmpty(p.MIME, "image/jpeg"),
					"data":       base64.StdEncoding.EncodeToString(p.Data),
				}})
			} else if p.Remote != "" {
				// Anthropic 也支持 url 型图片来源，直接透传地址。
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
					"type": "url",
					"url":  p.Remote,
				}})
			}
		}
	}
	if len(blocks) == 0 && msg.Content != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": msg.Content})
	}
	for _, c := range msg.ToolCalls {
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    firstNonEmpty(c.ID, "toolu_"+c.Name),
			"name":  c.Name,
			"input": toolArgObject(c.Arguments),
		})
	}
	return blocks
}

// anthropicMessages 整理消息：role 只允许 user/assistant，相邻同 role 必须合并。
func anthropicMessages(in *ChatInput) []any {
	var out []any
	for _, msg := range in.DialogMessages() {
		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
		}
		blocks := anthropicBlocks(msg)
		if len(blocks) == 0 {
			continue
		}
		if n := len(out); n > 0 {
			prev := out[n-1].(map[string]any)
			if prev["role"] == role {
				prev["content"] = append(prev["content"].([]any), blocks...)
				continue
			}
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	return out
}

// anthropicTools 把 OpenAI 风格的 tools 转成 Anthropic 的工具定义。
func anthropicTools(in *ChatInput) []any {
	tools, ok := in.Options["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
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
		item := map[string]any{"name": name}
		if d := asString(fn["description"]); d != "" {
			item["description"] = d
		}
		if p, ok := fn["parameters"]; ok && p != nil {
			item["input_schema"] = p
		} else {
			item["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, item)
	}
	return out
}

// anthropicToolChoice 把 tool_choice 大致映射到 Anthropic 的取值；无法映射时返回 nil 表示不发送。
func anthropicToolChoice(v any) map[string]any {
	switch t := v.(type) {
	case string:
		switch t {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required", "any":
			return map[string]any{"type": "any"}
		}
	case map[string]any:
		if fn, ok := t["function"].(map[string]any); ok {
			if name := asString(fn["name"]); name != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		}
	}
	return nil
}

func anthropicChatBody(m store.Model, in *ChatInput, stream bool) map[string]any {
	body := map[string]any{
		"model":      m.UpstreamName,
		"max_tokens": anthropicMaxTokens(in),
		"messages":   anthropicMessages(in),
	}
	// Anthropic 不接受 system 出现在 messages 里，统一抽到顶层。
	if sys := in.SystemPrompt(); sys != "" {
		body["system"] = sys
	}
	if v, ok := in.FloatOption("temperature"); ok {
		body["temperature"] = v
	}
	if v, ok := in.FloatOption("top_p"); ok {
		body["top_p"] = v
	}
	if seqs := stopSequences(in); len(seqs) > 0 {
		body["stop_sequences"] = seqs
	}
	if stream {
		body["stream"] = true
	}
	if tools := anthropicTools(in); len(tools) > 0 && asString(in.Options["tool_choice"]) != "none" {
		body["tools"] = tools
		if choice := anthropicToolChoice(in.Options["tool_choice"]); choice != nil {
			body["tool_choice"] = choice
		}
	}
	return body
}

// ---------- 对话 ----------

func (a *AnthropicAdapter) Chat(ctx context.Context, up Upstream, m store.Model, in *ChatInput, emit StreamFunc) (*ChatOutput, error) {
	stream := in.Stream && emit != nil
	body := anthropicChatBody(m, in, stream)
	resp, err := doJSON(ctx, up, "POST", strings.TrimRight(up.BaseURL, "/")+"/messages", anthropicHeaders(up), body)
	if err != nil {
		return nil, err
	}
	if stream {
		return a.consumeStream(resp, m, emit)
	}
	raw, err := decodeJSONBytes(resp)
	if err != nil {
		return nil, err
	}
	return parseAnthropicChat(raw, m.Name), nil
}

// consumeStream 逐条读取 Anthropic 的 SSE 事件（事件名在 event: 行上）并下发给上层。
func (a *AnthropicAdapter) consumeStream(resp *http.Response, m store.Model, emit StreamFunc) (*ChatOutput, error) {
	defer resp.Body.Close()
	reader := newSSEReader(resp.Body)
	out := &ChatOutput{ID: "msg-relay", Model: m.Name}
	var content, reasoning strings.Builder
	calls := map[int]*ToolCall{}
	var order []int
	sentRole := false

	// send 保证首帧带上 assistant 角色。
	send := func(d StreamDelta) error {
		if !sentRole {
			d.Role = "assistant"
			sentRole = true
		}
		return emit(d)
	}

	for {
		ev, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		event := ev.Event
		if event == "" {
			event = asString(payload["type"])
		}
		switch event {
		case "message_start":
			msg, _ := payload["message"].(map[string]any)
			if msg == nil {
				continue
			}
			if id := asString(msg["id"]); id != "" {
				out.ID = id
			}
			if u, ok := msg["usage"].(map[string]any); ok {
				if parsed := anthropicUsage(u); parsed != nil {
					out.Usage = *parsed
					if err := send(StreamDelta{Usage: parsed}); err != nil {
						return nil, err
					}
				}
			}
		case "content_block_start":
			idx := anthropicIntOf(payload["index"])
			block, _ := payload["content_block"].(map[string]any)
			if asString(block["type"]) != "tool_use" {
				continue
			}
			call := &ToolCall{ID: asString(block["id"]), Name: asString(block["name"])}
			calls[idx] = call
			order = append(order, idx)
			if err := send(StreamDelta{ToolCalls: []ToolCall{*call}}); err != nil {
				return nil, err
			}
		case "content_block_delta":
			idx := anthropicIntOf(payload["index"])
			delta, _ := payload["delta"].(map[string]any)
			switch asString(delta["type"]) {
			case "text_delta":
				if t := asString(delta["text"]); t != "" {
					content.WriteString(t)
					if err := send(StreamDelta{Content: t}); err != nil {
						return nil, err
					}
				}
			case "thinking_delta":
				if t := asString(delta["thinking"]); t != "" {
					reasoning.WriteString(t)
					if err := send(StreamDelta{Reasoning: t}); err != nil {
						return nil, err
					}
				}
			case "input_json_delta":
				// tool_use 的参数是分批下发的，按块序号拼接。
				if c := calls[idx]; c != nil {
					c.Arguments += asString(delta["partial_json"])
					if err := send(StreamDelta{ToolCalls: []ToolCall{*c}}); err != nil {
						return nil, err
					}
				}
			}
		case "message_delta":
			d := StreamDelta{}
			if delta, ok := payload["delta"].(map[string]any); ok {
				if fr := anthropicStopReason(asString(delta["stop_reason"])); fr != "" {
					d.FinishReason = fr
					out.FinishReason = fr
				}
			}
			if u, ok := payload["usage"].(map[string]any); ok {
				if parsed := anthropicUsage(u); parsed != nil {
					// 此处通常只带 output_tokens，需要与 message_start 的 input_tokens 合并。
					if parsed.PromptTokens > 0 {
						out.Usage.PromptTokens = parsed.PromptTokens
					}
					out.Usage.CompletionTokens = parsed.CompletionTokens
					out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
					merged := out.Usage
					d.Usage = &merged
				}
			}
			if d.FinishReason != "" || d.Usage != nil {
				if err := send(d); err != nil {
					return nil, err
				}
			}
		case "message_stop":
			// 流正常结束，无需额外处理。
		}
	}

	out.Content = content.String()
	out.Reasoning = reasoning.String()
	for _, idx := range order {
		c, ok := calls[idx]
		if !ok {
			continue
		}
		if c.Arguments == "" {
			c.Arguments = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, *c)
	}
	if out.FinishReason == "" {
		if len(out.ToolCalls) > 0 {
			out.FinishReason = "tool_calls"
		} else {
			out.FinishReason = "stop"
		}
	}
	if out.Usage.TotalTokens == 0 {
		out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
	}
	return out, nil
}

// ---------- 响应解析 ----------

func anthropicIntOf(v any) int {
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

func anthropicUsage(u map[string]any) *Usage {
	prompt := anthropicIntOf(u["input_tokens"])
	completion := anthropicIntOf(u["output_tokens"])
	return &Usage{
		PromptTokens:         prompt,
		CompletionTokens:     completion,
		TotalTokens:          prompt + completion,
		CacheReadTokens:     anthropicIntOf(u["cache_read_input_tokens"]),
		CacheCreationTokens:  anthropicIntOf(u["cache_creation_input_tokens"]),
	}
}

func anthropicStopReason(v string) string {
	switch v {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "":
		return ""
	}
	return "stop"
}

func parseAnthropicChat(raw map[string]any, model string) *ChatOutput {
	out := &ChatOutput{ID: firstNonEmpty(asString(raw["id"]), "msg-relay"), Model: model}
	content, _ := raw["content"].([]any)
	for _, bv := range content {
		b, ok := bv.(map[string]any)
		if !ok {
			continue
		}
		switch asString(b["type"]) {
		case "text":
			out.Content += asString(b["text"])
		case "thinking":
			out.Reasoning += asString(b["thinking"])
		case "tool_use":
			call := ToolCall{ID: asString(b["id"]), Name: asString(b["name"])}
			if args, ok := b["input"]; ok && args != nil {
				bs, _ := json.Marshal(args)
				call.Arguments = string(bs)
			}
			out.ToolCalls = append(out.ToolCalls, call)
		}
	}
	out.FinishReason = anthropicStopReason(asString(raw["stop_reason"]))
	if u, ok := raw["usage"].(map[string]any); ok {
		if parsed := anthropicUsage(u); parsed != nil {
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
	return out
}

// ---------- 不支持的能力 ----------

func (a *AnthropicAdapter) Image(ctx context.Context, up Upstream, m store.Model, in *ImageInput) (*ImageOutput, error) {
	return nil, ErrUnsupported
}

func (a *AnthropicAdapter) Embedding(ctx context.Context, up Upstream, m store.Model, in *EmbeddingInput) (*EmbeddingOutput, error) {
	return nil, ErrUnsupported
}

// Anthropic 协议没有语音合成与识别接口，需要时请改用 OpenAI 兼容渠道。
func (a *AnthropicAdapter) Speech(context.Context, Upstream, store.Model, *SpeechInput) (*SpeechOutput, error) {
	return nil, ErrUnsupported
}

func (a *AnthropicAdapter) Transcription(context.Context, Upstream, store.Model, *TranscriptionInput) (*TranscriptionOutput, error) {
	return nil, ErrUnsupported
}
