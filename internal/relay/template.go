package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/store"
)

// TemplateAdapter 是模板驱动适配器：按模型配置里的 TemplateSpec 拼装请求，
// 从而接入任意 HTTP 上游（自建服务、聚合平台、非标准协议等）。
type TemplateAdapter struct{}

// templatePlaceholderRe 匹配模板占位符，允许 {{name}} 与 {{ name }} 两种写法。
var templatePlaceholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)

// ---------- 模板渲染 ----------

// renderTemplate 递归渲染模板节点。
// 字符串整值恰好是一个占位符时（如 {{messages}}、{{image}}）直接返回变量原值，
// 以便把数组、对象等非字符串类型原样放进请求体；数字、布尔等类型原样保留。
func renderTemplate(node any, vars map[string]any) any {
	switch t := node.(type) {
	case string:
		trimmed := strings.TrimSpace(t)
		if m := templatePlaceholderRe.FindStringSubmatch(trimmed); m != nil && m[0] == trimmed {
			if v, ok := vars[m[1]]; ok {
				return v
			}
			return t
		}
		return templateReplaceStr(t, vars)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			out[k] = renderTemplate(v, vars)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, v := range t {
			out = append(out, renderTemplate(v, vars))
		}
		return out
	default:
		return node
	}
}

// templateReplaceStr 把字符串里的占位符就地替换成文本形式，未命中的占位符原样保留。
func templateReplaceStr(s string, vars map[string]any) string {
	if s == "" || !strings.Contains(s, "{{") {
		return s
	}
	return templatePlaceholderRe.ReplaceAllStringFunc(s, func(p string) string {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(p, "{{"), "}}"))
		if v, ok := vars[name]; ok {
			return templateStringify(v)
		}
		return p
	})
}

// templateStringify 把变量转成可嵌入模板的字符串。
func templateStringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case []byte:
		return string(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// ---------- 变量 ----------

// buildTemplateVars 汇总模板可用变量；chat 与 image 二者只传其一。
func buildTemplateVars(up Upstream, m store.Model, chat *ChatInput, image *ImageInput) map[string]any {
	vars := map[string]any{
		// {{model}} 与 {{upstream_model}} 均为上游模型名，保持与路径占位符语义一致。
		"model":          m.UpstreamName,
		"upstream_model": m.UpstreamName,
		"key":            up.APIKey,
		"prompt":         "",
		"input":          "",
		"system":         "",
		"image":          "",
		"image_url":      "",
		"size":           "",
		"n":              0,
		"duration":       "",
	}
	if chat != nil {
		prompt := templateLastUserText(chat)
		vars["prompt"] = prompt
		vars["input"] = prompt
		vars["system"] = chat.SystemPrompt()
		vars["messages"] = chat.Messages
		if ref := templateFirstImage(chat); ref != "" {
			vars["image"] = ref
			vars["image_url"] = ref
		}
		vars["size"] = templateOptionString(chat.Options, "size")
		vars["n"] = templateOptionInt(chat.Options, "n")
		vars["duration"] = templateOptionString(chat.Options, "duration")
	}
	if image != nil {
		vars["prompt"] = image.Prompt
		vars["input"] = image.Prompt
		vars["size"] = image.Size
		vars["n"] = image.N
		vars["duration"] = templateOptionString(image.Options, "duration")
		if ref := firstNonEmpty(
			templateOptionString(image.Options, "image"),
			templateOptionString(image.Options, "image_url"),
		); ref != "" {
			vars["image"] = ref
			vars["image_url"] = ref
		}
	}
	return vars
}

// templateLastUserText 取最后一条 user 消息的纯文本。
func templateLastUserText(in *ChatInput) string {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		msg := in.Messages[i]
		if msg.Role != "user" {
			continue
		}
		if msg.Content != "" {
			return msg.Content
		}
		var b strings.Builder
		for _, p := range msg.Parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// templateFirstImage 取对话中第一张图片的引用：优先远程地址，否则还原成 data URL。
func templateFirstImage(in *ChatInput) string {
	for _, msg := range in.Messages {
		for _, p := range msg.Parts {
			if p.Type != "image" {
				continue
			}
			if p.Remote != "" {
				return p.Remote
			}
			if len(p.Data) > 0 {
				return "data:" + firstNonEmpty(p.MIME, "image/png") + ";base64," +
					base64.StdEncoding.EncodeToString(p.Data)
			}
		}
	}
	return ""
}

func templateOptionString(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	if s, ok := opts[key].(string); ok {
		return s
	}
	return ""
}

func templateOptionInt(opts map[string]any, key string) int {
	if opts == nil {
		return 0
	}
	switch v := opts[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// ---------- 请求 ----------

// doTemplateRequest 按 spec 发起一次上游请求，并返回解析后的响应体。
func (a *TemplateAdapter) doTemplateRequest(ctx context.Context, up Upstream, spec *store.TemplateSpec, vars map[string]any) (map[string]any, error) {
	method := strings.ToUpper(strings.TrimSpace(spec.Method))
	if method == "" {
		method = "POST"
	}
	pathTpl := strings.TrimSpace(spec.Path)
	if pathTpl == "" {
		pathTpl = "/"
	}
	endpoint := strings.TrimRight(up.BaseURL, "/") + "/" +
		strings.TrimLeft(templateReplaceStr(pathTpl, vars), "/")
	if q := templateQuery(spec.Query, vars); q != "" {
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		endpoint += sep + q
	}
	headers := make(map[string]string, len(spec.Headers))
	for k, v := range spec.Headers {
		headers[k] = templateReplaceStr(v, vars)
	}
	// GET 请求不带请求体，其余方法按模板渲染 Body。
	var body any
	if method != "GET" && spec.Body != nil {
		body = renderTemplate(spec.Body, vars)
	}
	resp, err := doJSON(ctx, up, method, endpoint, headers, body)
	if err != nil {
		return nil, err
	}
	return decodeJSONBytes(resp)
}

// templateQuery 渲染并编码 URL 查询参数。
func templateQuery(query map[string]string, vars map[string]any) string {
	if len(query) == 0 {
		return ""
	}
	values := url.Values{}
	for k, v := range query {
		values.Set(k, templateReplaceStr(v, vars))
	}
	return values.Encode()
}

// templateUpstreamError 构造带响应体摘要的上游错误。
func templateUpstreamError(raw map[string]any, msg string) error {
	b, _ := json.Marshal(raw)
	return &UpstreamError{
		StatusCode: 200,
		Body:       string(b),
		Message:    msg + "，响应：" + truncate(string(b), 300),
	}
}

// ---------- 对话 ----------

func (a *TemplateAdapter) Chat(ctx context.Context, up Upstream, m store.Model, in *ChatInput, emit StreamFunc) (*ChatOutput, error) {
	spec := m.Config.Chat
	if spec == nil {
		return nil, errors.New("模型未配置请求模板")
	}
	raw, err := a.doTemplateRequest(ctx, up, spec, buildTemplateVars(up, m, in, nil))
	if err != nil {
		return nil, err
	}
	if msg := jsonPathString(raw, spec.Response.Error); msg != "" {
		return nil, templateUpstreamError(raw, "上游返回错误："+msg)
	}
	out := &ChatOutput{ID: "template-relay", Model: m.Name, FinishReason: "stop"}
	out.Content = jsonPathString(raw, spec.Response.Content)
	if out.Content == "" {
		// 未配置 content 取值路径时，回退到常见的 OpenAI 风格响应结构。
		out.Content = firstNonEmpty(
			jsonPathString(raw, "choices[0].message.content"),
			jsonPathString(raw, "choices[0].delta.content"),
			contentString(raw["content"]),
		)
	}
	if out.Content == "" {
		return nil, templateUpstreamError(raw, "上游响应中未找到文本内容")
	}
	if u, ok := raw["usage"].(map[string]any); ok {
		if parsed := parseUsage(u); parsed != nil {
			out.Usage = *parsed
		}
	}
	if in.Stream && emit != nil {
		// 有意为之的降级：模板上游不保证支持 SSE，因此仍只发一次普通请求，
		// 再把完整内容作为单个 delta 下发，避免客户端因缺少流式结束帧而挂起。
		if err := emit(StreamDelta{
			Role:         "assistant",
			Content:      out.Content,
			FinishReason: out.FinishReason,
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---------- 图片生成 ----------

func (a *TemplateAdapter) Image(ctx context.Context, up Upstream, m store.Model, in *ImageInput) (*ImageOutput, error) {
	spec := m.Config.Image
	if spec == nil {
		return nil, errors.New("模型未配置请求模板")
	}
	raw, err := a.doTemplateRequest(ctx, up, spec, buildTemplateVars(up, m, nil, in))
	if err != nil {
		return nil, err
	}
	if msg := jsonPathString(raw, spec.Response.Error); msg != "" {
		return nil, templateUpstreamError(raw, "上游返回错误："+msg)
	}
	value := jsonPathString(raw, spec.Response.ImageURL)
	if value == "" {
		// 未配置取值路径时兜底尝试常见的图片字段。
		value = firstNonEmpty(
			jsonPathString(raw, "data[0].url"),
			jsonPathString(raw, "data[0].b64_json"),
			jsonPathString(raw, "url"),
			jsonPathString(raw, "image"),
		)
	}
	if value == "" {
		return nil, templateUpstreamError(raw, "上游响应中未找到图片数据")
	}
	item := ImageItem{}
	switch {
	case strings.HasPrefix(value, "data:"):
		// data URL 形式的图片，取逗号后面的 base64 部分。
		if c := strings.IndexByte(value, ','); c >= 0 {
			item.B64 = value[c+1:]
		} else {
			item.B64 = value
		}
	case strings.HasPrefix(value, "http://"), strings.HasPrefix(value, "https://"):
		item.URL = value
	default:
		// 既不是 data URL 也不是 http 地址，按上游直出的 base64 处理。
		item.B64 = value
	}
	return &ImageOutput{Created: time.Now().Unix(), Items: []ImageItem{item}}, nil
}

// ---------- 向量化 ----------

func (a *TemplateAdapter) Embedding(ctx context.Context, up Upstream, m store.Model, in *EmbeddingInput) (*EmbeddingOutput, error) {
	return nil, ErrUnsupported
}
