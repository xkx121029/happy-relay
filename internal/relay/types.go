// Package relay 实现中转核心：把统一的 v1 请求派发到不同协议的上游。
package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"happy-relay/internal/store"
)

// ---------- 统一输入 ----------

// Upstream 是一次具体调用的目标：某个渠道的某个入口地址 + 某个密钥。
type Upstream struct {
	BaseURL  string
	APIKey   string
	Headers  map[string]string
	Provider store.Provider
	// Client 由引擎按渠道超时配置注入。
	Client *http.Client
}

// Usage 是统一的用量统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ContentPart 是对话中的一段内容，已归一化为文本或图片。
type ContentPart struct {
	Type   string // text | image
	Text   string
	MIME   string
	Data   []byte // 图片二进制（data URL 已解码）
	Remote string // 图片远程地址（http(s) URL 原样透传）
}

// ToolCall 表示模型请求调用的工具。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatMessage 是一条归一化后的消息。
type ChatMessage struct {
	Role       string
	Content    string
	Parts      []ContentPart
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
	Reasoning  string
}

// ChatInput 是解析后的统一对话请求。
type ChatInput struct {
	Model    string
	Stream   bool
	Messages []ChatMessage
	// Options 保存 temperature / top_p / max_tokens / tools 等可选参数，原样透传。
	Options map[string]any
	Raw     map[string]any
}

// ChatOutput 是统一的对话结果。
type ChatOutput struct {
	ID           string
	Model        string
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

// StreamDelta 是流式过程中的一次增量。
type StreamDelta struct {
	Role         string
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
}

// StreamFunc 由上层提供，用于把增量写成 OpenAI 风格的 SSE。
type StreamFunc func(d StreamDelta) error

// UpstreamError 携带上游返回的状态码与响应体，便于上层决定是否切换密钥重试。
type UpstreamError struct {
	StatusCode int
	Body       string
	Message    string
}

func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "上游返回错误"
}

// ImageInput / ImageOutput 是统一的图片生成请求与结果。
type ImageInput struct {
	Model          string
	Prompt         string
	N              int
	Size           string
	ResponseFormat string // url | b64_json
	// Images 是输入参考图（公共 URL 或 data URI），用于图生图与多图合成。
	Images  []string
	Options map[string]any
}

type ImageOutput struct {
	Created int64       `json:"created"`
	Items   []ImageItem `json:"items"`
	Usage   *Usage      `json:"usage,omitempty"`
	Extra   interface{} `json:"-"`
}

type ImageItem struct {
	URL     string `json:"url,omitempty"`
	B64     string `json:"b64_json,omitempty"`
	Revised string `json:"revised_prompt,omitempty"`
}

// EmbeddingInput 是统一的向量化请求。
type EmbeddingInput struct {
	Model string
	Input []string
}

type EmbeddingOutput struct {
	Vectors [][]float32
	Usage   Usage
}

// ---------- 请求解析 ----------

// ParseChatInput 把 OpenAI 风格的请求体解析成统一输入。
func ParseChatInput(raw map[string]any) *ChatInput {
	in := &ChatInput{Raw: raw, Options: map[string]any{}}
	if v, ok := raw["model"].(string); ok {
		in.Model = v
	}
	if v, ok := raw["stream"].(bool); ok {
		in.Stream = v
	}
	msgs, _ := raw["messages"].([]any)
	for _, mv := range msgs {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		msg := ChatMessage{}
		msg.Role, _ = m["role"].(string)
		msg.Name, _ = m["name"].(string)
		msg.ToolCallID, _ = m["tool_call_id"].(string)
		if rc, ok := m["reasoning_content"].(string); ok {
			msg.Reasoning = rc
		}
		switch content := m["content"].(type) {
		case string:
			msg.Content = content
			msg.Parts = []ContentPart{{Type: "text", Text: content}}
		case []any:
			for _, pv := range content {
				part, ok := pv.(map[string]any)
				if !ok {
					continue
				}
				switch part["type"] {
				case "text":
					text, _ := part["text"].(string)
					msg.Content += text
					msg.Parts = append(msg.Parts, ContentPart{Type: "text", Text: text})
				case "image_url":
					url := imageURLOf(part)
					if url == "" {
						continue
					}
					msg.Parts = append(msg.Parts, ParseImageURL(url))
				}
			}
		}
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, tv := range tcs {
				tc, ok := tv.(map[string]any)
				if !ok {
					continue
				}
				call := ToolCall{}
				call.ID, _ = tc["id"].(string)
				if fn, ok := tc["function"].(map[string]any); ok {
					call.Name, _ = fn["name"].(string)
					switch args := fn["arguments"].(type) {
					case string:
						call.Arguments = args
					default:
						b, _ := json.Marshal(args)
						call.Arguments = string(b)
					}
				}
				msg.ToolCalls = append(msg.ToolCalls, call)
			}
		}
		in.Messages = append(in.Messages, msg)
	}
	for k, v := range raw {
		switch k {
		case "model", "messages", "stream", "stream_options", "user":
			continue
		}
		in.Options[k] = v
	}
	return in
}

func imageURLOf(part map[string]any) string {
	switch v := part["image_url"].(type) {
	case string:
		return v
	case map[string]any:
		if s, ok := v["url"].(string); ok {
			return s
		}
	}
	return ""
}

var dataURLRe = regexp.MustCompile(`^data:([^;,]+)?(;base64)?,(.*)$`)

// ParseImageURL 把图片地址归一化：data URL 解码成二进制，http(s) 原样保留。
func ParseImageURL(url string) ContentPart {
	part := ContentPart{Type: "image"}
	if m := dataURLRe.FindStringSubmatch(strings.TrimSpace(url)); m != nil {
		part.MIME = m[1]
		if part.MIME == "" {
			part.MIME = "image/png"
		}
		if m[2] == ";base64" {
			if data, err := base64.StdEncoding.DecodeString(m[3]); err == nil {
				part.Data = data
			}
		}
		return part
	}
	part.Remote = url
	return part
}

// SystemPrompt 返回请求中的系统提示（若有）。
func (in *ChatInput) SystemPrompt() string {
	var parts []string
	for _, m := range in.Messages {
		if m.Role == "system" || m.Role == "developer" {
			if m.Content != "" {
				parts = append(parts, m.Content)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// DialogMessages 返回去掉系统提示后的对话消息。
func (in *ChatInput) DialogMessages() []ChatMessage {
	out := make([]ChatMessage, 0, len(in.Messages))
	for _, m := range in.Messages {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// FloatOption 读取浮点型可选参数。
func (in *ChatInput) FloatOption(key string) (float64, bool) {
	v, ok := in.Options[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

// IntOption 读取整型可选参数（兼容浮点与数字字符串）。
func (in *ChatInput) IntOption(key string) (int, bool) {
	v, ok := in.Options[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// withContext 便于适配器统一处理超时上下文。
func withContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
