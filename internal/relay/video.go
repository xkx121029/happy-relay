package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/store"
)

// VideoInput 是统一的视频生成入参。
// 各家上游对「图生视频」的表达差异很大，这里同时保留单图与多素材两组字段，
// 由模板按需取用：Agnes 的 keyframe 模式用首尾帧，reference 模式用参考图与音频。
type VideoInput struct {
	Model  string
	Prompt string
	// ImageURL 是通用的首帧图，兼容早期只支持单图的上游。
	ImageURL string
	// Mode 对应 Agnes 的 text / keyframe / reference，留空按 text 处理。
	Mode string
	// FirstFrame / LastFrame 用于首尾帧控制。
	FirstFrame string
	LastFrame  string
	// Images / Audios 是多素材参考模式的输入列表。
	Images   []string
	Audios   []string
	Duration int
	Size     string
	// AspectRatio 是画幅比例（如 16:9），留空则由上游决定。
	AspectRatio string
	Extra       map[string]any
}

// 视频供应商的状态值五花八门，这里统一归一化。
var videoStatusAlias = map[string]string{
	"queued": "queued", "pending": "queued", "submitted": "queued", "created": "queued",
	"waiting": "queued", "in_queue": "queued", "not_start": "queued",
	"running": "running", "processing": "running", "in_progress": "running",
	"generating": "running", "started": "running", "working": "running",
	"succeeded": "succeeded", "success": "succeeded", "completed": "succeeded",
	"complete": "succeeded", "done": "succeeded", "finished": "succeeded", "ok": "succeeded",
	"failed": "failed", "error": "failed", "cancelled": "failed", "canceled": "failed",
	"rejected": "failed", "expired": "failed",
}

// NewTaskID 生成本地视频任务 ID。
func NewTaskID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	return "task_" + hex.EncodeToString(buf)
}

// CreateVideo 提交一次视频生成任务到上游：先跨渠道尝试，再在渠道内切换入口/密钥。
func (e *Engine) CreateVideo(ctx context.Context, m store.Model, ps []store.Provider, in *VideoInput) (store.VideoTask, error) {
	if m.Config.Video == nil {
		return store.VideoTask{}, errors.New("该视频模型未配置提交任务模板，请在模型编辑里填写")
	}
	// 带首帧或参考图时要求上游支持图片输入。
	need := []string{store.ModalityText}
	if in.ImageURL != "" || in.FirstFrame != "" || len(in.Images) > 0 {
		need = append(need, store.ModalityImage)
	}
	var lastErr error
	for _, t := range e.orderTargets(m, e.expandTargets(m, ps, need)) {
		p := t.Provider
		mu := m
		mu.UpstreamName = t.UpstreamName
		var providerErr error
		for _, up := range e.candidates(p) {
			vars := videoVars(up, mu, in, "")
			raw, err := callTemplate(ctx, up, m.Config.Video, vars)
			if err != nil {
				providerErr = err
				lastErr = err
				continue
			}
			spec := m.Config.Video
			upstreamID := jsonPathString(raw, spec.Response.TaskID)
			status := normalizeVideoStatus(jsonPathString(raw, spec.Response.Status), spec.Response)
			task := store.VideoTask{
				TaskID:         NewTaskID(),
				Model:          m.Name,
				ProviderID:     p.ID,
				UpstreamTaskID: upstreamID,
				UpstreamURL:    up.BaseURL,
				Status:         status,
				Request:        mustJSON(map[string]any{"prompt": in.Prompt, "image_url": in.ImageURL}),
				Result:         mustJSON(map[string]any{"raw": raw}),
			}
			if errMsg := jsonPathString(raw, spec.Response.Error); errMsg != "" && spec.Response.Error != "" {
				task.Status = store.TaskFailed
				task.Error = errMsg
			}
			// 少数上游同步返回结果，此时直接落最终态。
			if task.Status == store.TaskQueued && upstreamID == "" {
				if u := jsonPathString(raw, spec.Response.VideoURL); u != "" {
					task.Status = store.TaskSucceeded
				}
			}
			task.Result = mustJSON(buildVideoResult(raw, spec))
			e.breaker.report(p.ID, nil)
			return e.st.CreateVideoTask(task)
		}
		e.breaker.report(p.ID, providerErr)
	}
	if lastErr == nil {
		lastErr = errors.New("渠道未配置任何入口地址")
	}
	return store.VideoTask{}, lastErr
}

// PollVideo 向上游查询一次任务状态，并返回更新后的记录。
func (e *Engine) PollVideo(ctx context.Context, t store.VideoTask) (store.VideoTask, error) {
	m, err := e.st.ResolveModel(t.Model)
	if err != nil {
		return t, err
	}
	// 优先沿用提交任务时命中的渠道，保证查询打到同一上游。
	p, err := e.st.GetProvider(t.ProviderID)
	if err != nil {
		p, err = e.st.GetProvider(m.ProviderID)
		if err != nil {
			return t, err
		}
	}
	spec := m.Config.VideoQuery
	if spec == nil {
		spec = m.Config.Video
	}
	if spec == nil || spec.Path == "" {
		return t, errors.New("该视频模型未配置查询模板")
	}
	// 查询地址优先沿用提交时实际命中的入口。
	ups := e.candidates(p)
	if t.UpstreamURL != "" {
		for i := range ups {
			if ups[i].BaseURL == t.UpstreamURL {
				ups = append([]Upstream{ups[i]}, ups...)
				break
			}
		}
	}
	in := &VideoInput{Model: t.Model}
	if raw := t.Request; len(raw) > 0 {
		var parsed map[string]any
		if json.Unmarshal(raw, &parsed) == nil {
			in.Prompt, _ = parsed["prompt"].(string)
			in.ImageURL, _ = parsed["image_url"].(string)
		}
	}
	var lastErr error
	mu := m
	mu.UpstreamName = m.UpstreamNameFor(p.ID)
	for _, up := range ups {
		vars := videoVars(up, mu, in, t.UpstreamTaskID)
		raw, err := callTemplate(ctx, up, spec, vars)
		if err != nil {
			lastErr = err
			continue
		}
		status := normalizeVideoStatus(jsonPathString(raw, spec.Response.Status), spec.Response)
		result := buildVideoResult(raw, spec)
		if errMsg := jsonPathString(raw, spec.Response.Error); errMsg != "" && spec.Response.Error != "" {
			status = store.TaskFailed
			t.Error = errMsg
		}
		if status == "" {
			status = t.Status
		}
		t.Progress = jsonPathInt(raw, spec.Response.Progress)
		if t.Progress > 100 {
			t.Progress = 100
		}
		if url := jsonPathString(raw, spec.Response.VideoURL); url != "" {
			result["video_url"] = url
			// 部分上游（如智谱的异步结果）不返回状态字段，
			// 此时以「是否已产出视频地址」作为完成依据。
			if status == store.TaskQueued || status == store.TaskRunning {
				status = store.TaskSucceeded
			}
		}
		if status == store.TaskSucceeded && t.Progress < 100 {
			t.Progress = 100
		}
		// 状态必须在所有判定完成后再落库，否则会出现
		// 「状态 running 但已有 video_url」这种前后矛盾的结果。
		t.Status = status
		t.Result = mustJSON(result)
		if err := e.st.UpdateVideoTask(t); err != nil {
			return t, err
		}
		return t, nil
	}
	if lastErr == nil {
		lastErr = errors.New("渠道未配置任何入口地址")
	}
	return t, lastErr
}

// videoVars 组装模板占位符。
func videoVars(up Upstream, m store.Model, in *VideoInput, upstreamTaskID string) map[string]any {
	// 首帧图：显式指定优先，其次回退到通用的 image 字段。
	first := firstNonEmpty(in.FirstFrame, in.ImageURL)
	seconds := ""
	if in.Duration > 0 {
		seconds = strconv.Itoa(in.Duration)
	}
	vars := map[string]any{
		"prompt":         in.Prompt,
		"input":          in.Prompt,
		"model":          m.Name,
		"upstream_model": m.UpstreamName,
		"base_root":      baseRoot(up.BaseURL),
		"image":          first,
		"image_url":      first,
		"first_frame":    first,
		"last_frame":     in.LastFrame,
		"images":         in.Images,
		"audios":         in.Audios,
		"mode":           firstNonEmpty(in.Mode, "text"),
		"aspect_ratio":   in.AspectRatio,
		// 部分上游（如 Agnes）要求时长是字符串，这里同时给出数字与文本两种形式。
		"duration_text": seconds,
		"seconds":       seconds,
		"key":           up.APIKey,
		"task_id":       upstreamTaskID,
	}
	if in.Duration > 0 {
		vars["duration"] = in.Duration
	}
	if in.Size != "" {
		vars["size"] = in.Size
	}
	for k, v := range in.Extra {
		if _, exists := vars[k]; !exists {
			vars[k] = v
		}
	}
	return vars
}

// normalizeVideoStatus 把上游状态值归一化成 queued/running/succeeded/failed。
// 先查 StatusMap 显式映射，再查内置别名表，最后比对兜底字段。
func normalizeVideoStatus(raw string, rm store.ResponseMap) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return ""
	}
	if mapped, ok := rm.StatusMap[key]; ok && mapped != "" {
		key = strings.ToLower(strings.TrimSpace(mapped))
	}
	switch {
	case strings.EqualFold(key, rm.SuccessValue) && rm.SuccessValue != "":
		return store.TaskSucceeded
	case strings.EqualFold(key, rm.FailedValue) && rm.FailedValue != "":
		return store.TaskFailed
	case strings.EqualFold(key, rm.PendingValue) && rm.PendingValue != "":
		return store.TaskRunning
	}
	return videoStatusAlias[key]
}

// renderTemplateString 渲染字符串模板：整串恰好是占位符时直接取原值，否则做字符串替换。
func renderTemplateString(tpl string, vars map[string]any) string {
	trimmed := strings.TrimSpace(tpl)
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") &&
		strings.Count(trimmed, "{{") == 1 {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{"), "}}"))
		if v, ok := vars[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprint(v)
		}
	}
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", fmt.Sprint(v))
	}
	return out
}

// buildVideoResult 拼出对外返回的结果体。
func buildVideoResult(raw map[string]any, spec *store.TemplateSpec) map[string]any {
	out := map[string]any{"raw": raw}
	if u := jsonPathString(raw, spec.Response.VideoURL); u != "" {
		out["video_url"] = u
	}
	if u := jsonPathString(raw, spec.Response.ImageURL); u != "" {
		out["image_url"] = u
	}
	return out
}

// callTemplate 按下发的模板规格发起一次上游调用。
func callTemplate(ctx context.Context, up Upstream, spec *store.TemplateSpec, vars map[string]any) (map[string]any, error) {
	method := strings.ToUpper(strings.TrimSpace(spec.Method))
	if method == "" {
		method = "POST"
	}
	endpoint, err := url.Parse(resolveEndpoint(up.BaseURL, renderTemplateString(spec.Path, vars)))
	if err != nil {
		return nil, fmt.Errorf("模板路径无法解析：%w", err)
	}
	if len(spec.Query) > 0 {
		q := endpoint.Query()
		for k, v := range spec.Query {
			q.Set(k, renderTemplateString(v, vars))
		}
		endpoint.RawQuery = q.Encode()
	}
	headers := map[string]string{}
	for k, v := range spec.Headers {
		headers[k] = renderTemplateString(v, vars)
	}
	if up.APIKey != "" && headers["Authorization"] == "" && headers["x-goog-api-key"] == "" && headers["x-api-key"] == "" {
		headers["Authorization"] = "Bearer " + up.APIKey
	}
	var body any
	if method != "GET" && len(spec.Body) > 0 {
		body = renderTemplate(spec.Body, vars)
		if spec.OmitEmpty {
			body = omitEmptyValues(body)
		}
	}
	resp, err := doJSON(ctx, up, method, endpoint.String(), headers, body)
	if err != nil {
		return nil, err
	}
	return decodeJSONBytes(resp)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// TryTemplate 用给定模板对上游发起一次真实调用，供面板验证自定义连接器是否配对。
func (e *Engine) TryTemplate(ctx context.Context, p store.Provider, spec *store.TemplateSpec, in *VideoInput, upstreamModel string) (map[string]any, error) {
	ups := e.candidates(p)
	if len(ups) == 0 {
		return nil, errors.New("渠道未配置任何入口地址")
	}
	m := store.Model{Name: in.Model, UpstreamName: upstreamModel}
	vars := videoVars(ups[0], m, in, "")
	return callTemplate(ctx, ups[0], spec, vars)
}
