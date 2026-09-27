package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"happy-relay/internal/store"
)

// ---------- 主 AI 深度集成：自然语言创建模型 ----------
//
// 「帮我把所有 NIM 的模型整合为一个 NIMout 模型」——主 AI 拿到渠道/模型库/现有模型
// 三份上下文，产出结构化的模型配置草案；前端预览确认后走标准 SaveModel 落库。
// AI 只出草案，不直接写库：防止幻觉上游、误删配置，所有变更可预览。

const aiModelSystemPrompt = `你是「模型中转站」的配置助手。用户会用自然语言描述想要的对外模型（可能创建一个或多个），
你必须只输出一个 JSON 对象，不要输出任何其他文字、注释或 markdown 代码块围栏。

JSON 格式：
{
  "reply": "一句话中文说明你打算怎么做",
  "models": [
    {
      "name": "对外模型名",
      "capability": "chat|image|video|embedding|speech|transcription",
      "remark": "一句话备注",
      "inputs": [
        {"provider_id": 1, "upstream_name": "上游模型名", "input_modalities": ["text"]}
      ]
    }
  ]
}

规则：
1. provider_id 和 upstream_name 只能从下方给出的「可用渠道与上游清单」里选，禁止编造。
2. input_modalities 从清单里该上游的模态标注照抄；清单没标注时留空数组（系统会按名字推断）。
3. 多上游聚合时把纯文本上游排在前面、多模态上游排后面（纯文本请求优先走便宜上游）。
4. 同名模型已存在时换一个新名字，不要复用已有名字。
5. 用户没说清楚能力时默认 chat。用户要求「整合某渠道的全部模型」时，把该渠道的
   chat 上游聚合为一个模型；数量极多（>8 个）时挑选最有代表性的子集并分档。
6. models 可以为空（用户的要求无法满足时，在 reply 里解释原因）。`

// aiModelDraft 是主 AI 产出的单个模型草案。
type aiModelDraft struct {
	Name        string `json:"name"`
	Capability  string `json:"capability"`
	Remark      string `json:"remark"`
	UpstreamName string `json:"upstream_name"` // 兼容单上游旧格式
	Inputs      []struct {
		ProviderID      int64    `json:"provider_id"`
		UpstreamName    string   `json:"upstream_name"`
		InputModalities []string `json:"input_modalities"`
	} `json:"inputs"`
}

type aiModelReply struct {
	Reply  string         `json:"reply"`
	Models []aiModelDraft `json:"models"`
}

type aiCreateRequest struct {
	Prompt string `json:"prompt"`
}

type aiCreateResponse struct {
	Reply       string           `json:"reply"`
	Models      []store.Model    `json:"models"`
	Warnings    []string         `json:"warnings"`
	SystemModel string           `json:"system_model"`
	LatencyMS   int64            `json:"latency_ms"`
	Usage       map[string]int64 `json:"usage,omitempty"`
}

// handleModelAICreate 用主 AI 把自然语言转成模型配置草案（不落库）。
func (s *Server) handleModelAICreate(w http.ResponseWriter, r *http.Request) {
	var req aiCreateRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "参数解析失败："+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "请用一句话描述你想要的模型")
		return
	}
	systemModel := s.st.GetSetting(SettingSystemModel, "")
	if systemModel == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error",
			"尚未指定系统级 AI，请先在「设置 → 通用 → 系统级 AI 模型」里选择一个文本模型")
		return
	}

	userPrompt, ctxWarn := s.buildAIModelContext(req.Prompt)

	start := time.Now()
	out, providerName, err := s.callSystemAI(r.Context(), systemModel, aiModelSystemPrompt, userPrompt)
	if err != nil {
		s.logUsage(store.UsageLog{
			Capability: "ai-config", Model: systemModel, ProviderName: providerName,
			APIKeyName: panelKey.Name, Status: 502,
			LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		writeErr(w, http.StatusBadGateway, "upstream_error", "系统级 AI 调用失败："+err.Error())
		return
	}
	latency := time.Since(start).Milliseconds()

	warnings := append([]string{}, ctxWarn...)
	reply, drafts, parseWarnings := parseAIModelReply(out.Content)
	warnings = append(warnings, parseWarnings...)

	// 校验草案：渠道存在、上游在模型库里、名字不冲突，产出可直接 SaveModel 的配置。
	models := make([]store.Model, 0, len(drafts))
	existingNames := map[string]bool{}
	if list, err := s.st.ListModels(); err == nil {
		for _, m := range list {
			existingNames[m.Name] = true
		}
	}
	catalogSet := map[string]string{} // key: providerID::upstream → 模态标注
	if cat, err := s.st.ListCatalogModels(0, ""); err == nil {
		for _, c := range cat {
			if c.Enabled {
				catalogSet[upstreamKey(c.ProviderID, c.ModelID)] = strings.Join(catalogInputs(c), ",")
			}
		}
	}

	for i, d := range drafts {
		cap := strings.TrimSpace(d.Capability)
		switch cap {
		case store.CapChat, store.CapImage, store.CapVideo, store.CapEmbedding, store.CapSpeech, store.CapTranscription:
		default:
			cap = store.CapChat
		}
		name := strings.TrimSpace(d.Name)
		if name == "" {
			name = fmt.Sprintf("ai-model-%d", i+1)
		}
		// 草案之间 + 与现有模型之间都不重名。
		base := name
		for suffix := 2; existingNames[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		existingNames[name] = true

		// 上游清单：渠道存在 + 在模型库里，双重校验后才采纳。
		var mi []store.ModelInput
		for _, in := range d.Inputs {
			if in.ProviderID <= 0 || strings.TrimSpace(in.UpstreamName) == "" {
				continue
			}
			if _, err := s.st.GetProvider(in.ProviderID); err != nil {
				warnings = append(warnings, fmt.Sprintf("「%s」的上游 #%d::%s 渠道不存在，已剔除", name, in.ProviderID, in.UpstreamName))
				continue
			}
			mods, known := catalogSet[upstreamKey(in.ProviderID, in.UpstreamName)]
			if !known {
				// 上游不在模型库：大概率是幻觉，直接剔除而不是放过去浪费一次调用。
				warnings = append(warnings, fmt.Sprintf("「%s」的上游 %s 不在模型库里（渠道 #%d），已剔除", name, in.UpstreamName, in.ProviderID))
				continue
			}
			modsList := in.InputModalities
			if len(modsList) == 0 && mods != "" {
				modsList = strings.Split(mods, ",")
			}
			mi = append(mi, store.ModelInput{
				ProviderID:      in.ProviderID,
				UpstreamName:    in.UpstreamName,
				InputModalities: store.CleanModalities(modsList),
			})
		}
		if len(mi) == 0 {
			warnings = append(warnings, fmt.Sprintf("「%s」没有可用上游，已丢弃", name))
			continue
		}
		models = append(models, store.Model{
			Name:         name,
			UpstreamName: mi[0].UpstreamName,
			Capability:   cap,
			Enabled:      true,
			Remark:       strings.TrimSpace(d.Remark),
			Inputs:       mi,
		})
		models[len(models)-1].Config.RouteStrategy = "failover"
	}

	resp := aiCreateResponse{
		Reply:       reply,
		Models:      models,
		Warnings:    warnings,
		SystemModel: systemModel,
		LatencyMS:   latency,
	}
	if out != nil && out.Usage.TotalTokens > 0 {
		resp.Usage = map[string]int64{
			"prompt_tokens":     int64(out.Usage.PromptTokens),
			"completion_tokens": int64(out.Usage.CompletionTokens),
		}
	}
	writeJSON(w, http.StatusOK, resp)

	s.logUsage(store.UsageLog{
		Capability: "ai-config", Model: systemModel, ProviderName: providerName,
		APIKeyName: panelKey.Name, Status: 200, LatencyMS: latency,
		PromptTokens:    out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		RequestBody:     truncateStr(req.Prompt, s.logBodyLimit()),
		ResponsePreview: truncateStr(out.Content, s.logBodyLimit()),
	})
}

// buildAIModelContext 组装给主 AI 的用户提示：渠道、模型库、现有模型名 + 用户诉求。
// 模型库压成每行一条，控制 token 开销。
func (s *Server) buildAIModelContext(prompt string) (string, []string) {
	var warn []string
	var b strings.Builder
	b.WriteString("## 可用渠道\n")
	provs, err := s.st.ListProviders()
	if err != nil {
		warn = append(warn, "读取渠道失败："+err.Error())
	}
	for _, p := range provs {
		if !p.Enabled {
			continue
		}
		fmt.Fprintf(&b, "- 渠道 #%d：%s（%s）\n", p.ID, p.Name, p.Kind)
	}

	b.WriteString("\n## 模型库上游（渠道#ID 上游名 [能力 输入模态]）\n")
	cat, err := s.st.ListCatalogModels(0, "")
	if err != nil {
		warn = append(warn, "读取模型库失败："+err.Error())
	}
	n := 0
	for _, c := range cat {
		if !c.Enabled {
			continue
		}
		if n >= 400 {
			warn = append(warn, "模型库条目过多，只列出前 400 条——如需其他上游请缩小描述范围")
			break
		}
		fmt.Fprintf(&b, "#%d %s [%s %s]\n", c.ProviderID, c.ModelID, c.Capability, strings.Join(catalogInputs(c), ","))
		n++
	}

	b.WriteString("\n## 已存在的对外模型名（禁止重名）\n")
	if models, err := s.st.ListModels(); err == nil {
		names := make([]string, 0, len(models))
		for _, m := range models {
			names = append(names, m.Name)
		}
		b.WriteString(strings.Join(names, ", ") + "\n")
	}

	b.WriteString("\n## 用户需求\n")
	b.WriteString(strings.TrimSpace(prompt))
	return b.String(), warn
}

// parseAIModelReply 解析主 AI 的 JSON 回复；解析失败时保留原文给用户看。
func parseAIModelReply(reply string) (string, []aiModelDraft, []string) {
	var out aiModelReply
	text := extractJSONObject(reply)
	if text == "" {
		return strings.TrimSpace(reply), nil, []string{"AI 未返回可识别的 JSON，原始回复见 reply 字段"}
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return strings.TrimSpace(reply), nil, []string{"AI 返回的 JSON 解析失败：" + err.Error()}
	}
	if out.Reply == "" {
		out.Reply = "已生成草案，请确认后创建"
	}
	return out.Reply, out.Models, nil
}
