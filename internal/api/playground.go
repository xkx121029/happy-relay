package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// panelKey 是面板调试台使用的虚拟秘钥：不受模型白名单限制，也不参与秘钥校验。
var panelKey = store.APIKey{Name: "面板调试台", Enabled: true}

// ---------- 调试台 ----------

func (s *Server) handlePlaygroundChat(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请先选择要调试的模型")
		return
	}
	s.runChat(w, r, panelKey, in)
}

func (s *Server) handlePlaygroundImage(w http.ResponseWriter, r *http.Request) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	if err := s.applySkill(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.runImage(w, r, panelKey, raw)
}

func (s *Server) handlePlaygroundVideo(w http.ResponseWriter, r *http.Request) {
	raw, err := decodeMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON："+err.Error())
		return
	}
	if err := s.applySkill(raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.runVideoCreate(w, r, panelKey, raw)
}

// applySkill 把技能模板渲染成实际的 prompt / messages。
// 技能由面板传入 {"skill":"slug","skill_params":{...}}。
func (s *Server) applySkill(raw map[string]any) error {
	slug, _ := raw["skill"].(string)
	if strings.TrimSpace(slug) == "" {
		return nil
	}
	skills, err := s.st.ListSkills()
	if err != nil {
		return err
	}
	var target *store.Skill
	for i := range skills {
		if skills[i].Slug == slug && skills[i].Enabled {
			target = &skills[i]
			break
		}
	}
	if target == nil {
		return errors.New("技能不存在或已停用：" + slug)
	}
	params := map[string]string{}
	if m, ok := raw["skill_params"].(map[string]any); ok {
		for k, v := range m {
			if str, ok := v.(string); ok {
				params[k] = str
			} else {
				b, _ := json.Marshal(v)
				params[k] = strings.Trim(string(b), `"`)
			}
		}
	}
	// 未填写的参数回落到技能默认值
	for _, p := range target.Params {
		if _, ok := params[p.Key]; !ok && p.Default != "" {
			params[p.Key] = p.Default
		}
	}
	prompt := renderSkillPrompt(target.Prompt, params)
	switch target.Capability {
	case store.CapImage, store.CapVideo:
		raw["prompt"] = prompt
		if v, ok := params["size"]; ok && raw["size"] == nil {
			raw["size"] = v
		}
		if v, ok := params["duration"]; ok && raw["duration"] == nil {
			raw["duration"] = v
		}
	default:
		raw["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
	}
	delete(raw, "skill")
	delete(raw, "skill_params")
	return nil
}

// renderSkillPrompt 渲染技能模板里的 {{占位符}}。
func renderSkillPrompt(tpl string, params map[string]string) string {
	out := tpl
	for k, v := range params {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// ---------- 系统级 AI 生成连接器 ----------

type connectorRequest struct {
	Description    string `json:"description"`     // 上游接口的自然语言描述
	BaseURL        string `json:"base_url"`        // 上游地址，仅用于给 AI 参考
	SampleRequest  string `json:"sample_request"`  // 示例请求（curl 或 JSON）
	SampleResponse string `json:"sample_response"` // 示例响应
	Capability     string `json:"capability"`      // chat | image | video
}

// handleConnectorGenerate 用系统级 AI 把一段接口描述翻译成连接器配置草案。
func (s *Server) handleConnectorGenerate(w http.ResponseWriter, r *http.Request) {
	var req connectorRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	systemModel := s.st.GetSetting(SettingSystemModel, "")
	if systemModel == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"尚未指定系统级 AI，请先在「设置」里选择一个已创建的文本模型")
		return
	}
	if strings.TrimSpace(req.Description) == "" && strings.TrimSpace(req.SampleRequest) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请至少填写接口描述或示例请求")
		return
	}
	if req.Capability == "" {
		req.Capability = store.CapVideo
	}

	start := time.Now()
	out, provider, err := s.callSystemAI(r.Context(), systemModel, connectorSystemPrompt, buildConnectorUserPrompt(req))
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: "connector", Model: systemModel, ProviderName: provider,
			APIKeyName: panelKey.Name, Status: 502,
			LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", "系统级 AI 调用失败："+err.Error())
		return
	}
	// 解析失败时也返回 200：把 AI 原文一并交给用户，方便手工修正模板。
	draft, _ := parseConnectorDraft(out.Content, req)
	writeJSON(w, http.StatusOK, map[string]any{
		"draft": draft, "raw_reply": out.Content, "system_model": systemModel,
	})
	s.logUsage(store.UsageLog{
		Capability: "connector", Model: systemModel, ProviderName: provider,
		APIKeyName: panelKey.Name, Status: 200,
		LatencyMS:    time.Since(start).Milliseconds(),
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens,
		ResponsePreview: truncateStr(out.Content, 1200),
	})
}

// callSystemAI 通过中转引擎调用系统级模型。
func (s *Server) callSystemAI(ctx context.Context, model, systemPrompt, userPrompt string) (*relay.ChatOutput, string, error) {
	m, ps, err := s.eng.ResolveAll(model)
	if err != nil {
		return nil, "", err
	}
	in := &relay.ChatInput{
		Model: model,
		Messages: []relay.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Options: map[string]any{"temperature": 0.1},
		Raw:     map[string]any{},
	}
	out, provider, err := s.eng.Chat(ctx, m, ps, in, nil)
	if err != nil {
		return nil, provider.Name, err
	}
	return out, provider.Name, nil
}

// parseConnectorDraft 解析 AI 返回的 JSON，并补齐默认值。
// 解析失败时返回一份可空的草案，让用户手工填写。
func parseConnectorDraft(reply string, req connectorRequest) (map[string]any, error) {
	text := extractJSONObject(reply)
	if text == "" {
		return map[string]any{
			"name":          "未命名连接器",
			"upstream_name": "",
			"capability":    req.Capability,
			"parse_error":   "AI 未返回可识别的 JSON，请参考原文手工填写模板",
		}, errors.New("AI 未返回可识别的 JSON")
	}
	var draft map[string]any
	if err := json.Unmarshal([]byte(text), &draft); err != nil {
		return map[string]any{"parse_error": err.Error()}, err
	}
	if _, ok := draft["capability"]; !ok {
		draft["capability"] = req.Capability
	}
	return draft, nil
}

// extractJSONObject 从模型回复里截出第一个完整的 JSON 对象（容忍 ```json 包裹与前后废话）。
func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		rest = strings.TrimPrefix(strings.TrimSpace(rest), "json")
		s = strings.TrimSpace(rest)
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// handleConnectorTry 用草案配置对上游发起一次真实调用，验证配对是否正确。
func (s *Server) handleConnectorTry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID   int64               `json:"provider_id"`
		UpstreamName string              `json:"upstream_name"`
		Prompt       string              `json:"prompt"`
		Duration     int                 `json:"duration"`
		Size         string              `json:"size"`
		Spec         *store.TemplateSpec `json:"spec"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if req.Spec == nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "缺少要验证的模板 spec")
		return
	}
	p, err := s.st.GetProvider(req.ProviderID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	raw, err := s.eng.TryTemplate(ctx, p, req.Spec, &relay.VideoInput{
		Model: "connector-try", Prompt: req.Prompt, Duration: req.Duration, Size: req.Size,
	}, req.UpstreamName)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "response": raw})
}

const connectorSystemPrompt = `你是一个 API 连接器配置生成器。用户会给你某个 AI 上游接口的说明、示例请求和示例响应，
你要输出一份 JSON 配置，指导中转站如何调用这个接口。

严格输出单个 JSON 对象，不要输出任何解释文字，不要用 Markdown 代码块包裹。

JSON 结构：
{
  "name": "对外模型名（英文小写与短横线，例如 kling-video）",
  "upstream_name": "上游真实模型名",
  "capability": "chat | image | video",
  "remark": "一句话中文说明",
  "video": {
    "method": "POST",
    "path": "相对 base_url 的路径，例如 /v1/videos/generations",
    "headers": {"可选的额外请求头": "值"},
    "query": {"可选的 URL 查询参数": "值"},
    "body": {"请求体模板": "字符串值里可用占位符"}
  },
  "video_query": {
    "method": "GET",
    "path": "查询任务状态的路径",
    "query": {},
    "body": {},
    "response": {"status": "数组/字段路径", "progress": "路径", "video_url": "路径"}
  },
  "response": {
    "task_id": "从提交响应中取任务 ID 的字段路径",
    "status": "取状态的字段路径",
    "progress": "取进度的字段路径",
    "video_url": "取视频地址的字段路径",
    "image_url": "取图片地址的字段路径",
    "error": "取错误信息的字段路径",
    "status_map": {"上游状态值": "queued|running|succeeded|failed"},
    "success_value": "上游表示成功的原始值",
    "pending_value": "上游表示进行中的原始值",
    "failed_value": "上游表示失败的原始值"
  }
}

字段路径写法：用点号表示层级，用方括号表示数组下标，例如 data.task_id、outputs[0].url。

可用占位符（写在 body / path / headers / query 的字符串值里）：
{{prompt}} 提示词、{{upstream_model}} 上游模型名、{{model}} 对外模型名、
{{image_url}} 参考图地址、{{size}} 尺寸、{{duration}} 时长秒数、{{task_id}} 查询时用的任务 ID、{{key}} 上游密钥。

注意：
- 图文生视频类接口用 {{image_url}}；如果上游叫 first_frame_image、image 之类，直接放到对应字段即可。
- 如果接口是「提交返回任务 ID，另有一个查询接口」，必须同时给出 video 与 video_query，并在 response.task_id 指明任务 ID 的路径。
- 如果接口同步返回结果，只给 video 即可，并把 response.status 指向状态字段、success_value 填成功值。
- 不要臆造字段：只能依据用户给出的信息推断；不确定的路径留空字符串。`

func buildConnectorUserPrompt(req connectorRequest) string {
	var b strings.Builder
	b.WriteString("接口说明：\n")
	if strings.TrimSpace(req.Description) != "" {
		b.WriteString(req.Description)
	} else {
		b.WriteString("（未提供，请仅根据示例推断）")
	}
	b.WriteString("\n\n目标能力：")
	b.WriteString(req.Capability)
	if req.BaseURL != "" {
		b.WriteString("\n\n上游地址：")
		b.WriteString(req.BaseURL)
	}
	if strings.TrimSpace(req.SampleRequest) != "" {
		b.WriteString("\n\n示例请求：\n")
		b.WriteString(truncateStr(req.SampleRequest, 6000))
	}
	if strings.TrimSpace(req.SampleResponse) != "" {
		b.WriteString("\n\n示例响应：\n")
		b.WriteString(truncateStr(req.SampleResponse, 6000))
	}
	return b.String()
}

// ---------- 系统级 AI 生成技能 ----------

type skillRequest struct {
	Description string `json:"description"`
	Capability  string `json:"capability"`
}

// handleSkillGenerate 用系统级 AI 把一段自然语言描述翻译成技能草案。
func (s *Server) handleSkillGenerate(w http.ResponseWriter, r *http.Request) {
	var req skillRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请先描述这个技能要做什么")
		return
	}
	systemModel := s.st.GetSetting(SettingSystemModel, "")
	if systemModel == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"尚未指定系统级 AI，请先在「设置」里选择一个已创建的文本模型")
		return
	}
	if req.Capability == "" {
		req.Capability = store.CapChat
	}

	start := time.Now()
	out, provider, err := s.callSystemAI(r.Context(), systemModel, skillSystemPrompt, buildSkillUserPrompt(req))
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: "skill", Model: systemModel, ProviderName: provider,
			APIKeyName: panelKey.Name, Status: 502,
			LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", "系统级 AI 调用失败："+err.Error())
		return
	}
	draft, _ := parseSkillDraft(out.Content, req)
	writeJSON(w, http.StatusOK, map[string]any{
		"draft": draft, "raw_reply": out.Content, "system_model": systemModel,
	})
	s.logUsage(store.UsageLog{
		Capability: "skill", Model: systemModel, ProviderName: provider,
		APIKeyName: panelKey.Name, Status: 200,
		LatencyMS:       time.Since(start).Milliseconds(),
		PromptTokens:    out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		ResponsePreview: truncateStr(out.Content, 1200),
	})
}

// parseSkillDraft 解析 AI 返回的 JSON；解析失败时把原文当提示词，方便手工修正。
func parseSkillDraft(reply string, req skillRequest) (map[string]any, error) {
	text := extractJSONObject(reply)
	if text == "" {
		return map[string]any{
			"name":        "",
			"capability":  req.Capability,
			"prompt":      strings.TrimSpace(reply),
			"parse_error": "AI 未返回可识别的 JSON，已把原文填入提示词，请手工调整",
		}, errors.New("AI 未返回可识别的 JSON")
	}
	var draft map[string]any
	if err := json.Unmarshal([]byte(text), &draft); err != nil {
		return map[string]any{"parse_error": err.Error()}, err
	}
	if _, ok := draft["capability"]; !ok {
		draft["capability"] = req.Capability
	}
	return draft, nil
}

func buildSkillUserPrompt(req skillRequest) string {
	var b strings.Builder
	b.WriteString("技能用途描述：\n")
	b.WriteString(strings.TrimSpace(req.Description))
	b.WriteString("\n\n适用能力：")
	b.WriteString(req.Capability)
	return b.String()
}

const skillSystemPrompt = `你是一个提示词模板设计师。用户会描述一个希望复用的任务，你要把它设计成一个可参数化的「技能」。
严格输出单个 JSON 对象，不要输出任何解释文字，不要用 Markdown 代码块包裹。

JSON 结构：
{
  "name": "简短的中文技能名，例如 周报生成",
  "slug": "小写英文短横线标识，例如 weekly-report",
  "description": "一句话说明这个技能做什么、适合什么场景",
  "category": "文本 | 图片 | 视频 之一",
  "capability": "chat | image | video",
  "icon": "可选：chat / wand / list / globe / braces / image / film / sparkles 之一",
  "prompt": "完整提示词模板，用 {{参数key}} 作为占位符",
  "params": [
    {
      "key": "英文参数名",
      "label": "中文显示名",
      "type": "text | textarea | number | select",
      "default": "可选默认值",
      "options": ["仅 type=select 时填写候选值"],
      "placeholder": "可选输入提示"
    }
  ]
}

要求：
- prompt 必须包含至少一个 {{参数}} 占位符，且每个占位符都能在 params 里找到同名 key。
- 参数不宜过多，2 到 4 个即可；核心输入用 textarea，选择项用 select 并给 options。
- prompt 要写成能直接投喂给模型的完整指令，包含角色、任务与输出格式约束。
- capability 为 image 或 video 时，同样用 {{input}} 之类的占位符描述画面内容。
- 不要臆造用户没提到的字段，只依据描述设计。`
