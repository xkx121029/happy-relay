package relay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"happy-relay/internal/store"
)

// AutoModelName 是内置的对外自动模型名：由服务端按任务难度挑选合适的上游模型。
const AutoModelName = "auto"

// Difficulty 是一次请求的难度评估结果。
type Difficulty struct {
	Score  int    `json:"score"`
	Tier   string `json:"tier"`
	Reason string `json:"reason"`
}

// hardKeywords 命中即视为偏复杂的任务。
var hardKeywords = []string{
	"代码", "重构", "调试", "架构", "设计模式", "算法", "证明", "推导", "数学",
	"详细分析", "逐步推理", "长文", "论文", "综述", "对比分析",
	"code", "refactor", "debug", "architect", "algorithm", "prove", "reason step",
}

// AssessDifficulty 按请求内容评估难度，产出分数与档位。
// 打分维度：输入长度、是否含图片、是否需要工具调用、系统提示长度、复杂任务关键词。
// extraKeywords 是用户自定义的提档关键词（面板可配），与内置 hardKeywords 同权计分。
func AssessDifficulty(in *ChatInput, extraKeywords ...string) Difficulty {
	if in == nil {
		return Difficulty{Tier: store.TierLight, Reason: "无请求内容"}
	}

	textLen := 0
	imageCount := 0
	var text strings.Builder
	for _, msg := range in.Messages {
		textLen += len([]rune(msg.Content))
		text.WriteString(msg.Content)
		text.WriteString("\n")
		for _, part := range msg.Parts {
			if part.Type == "image" {
				imageCount++
			}
		}
	}

	score := 0
	reasons := make([]string, 0, 4)
	switch {
	case textLen >= 1200:
		score += 3
		reasons = append(reasons, "超长输入")
	case textLen >= 400:
		score += 2
		reasons = append(reasons, "长输入")
	case textLen >= 100:
		score += 1
		reasons = append(reasons, "中等长度输入")
	}
	if imageCount > 0 {
		score += 2
		reasons = append(reasons, "包含图片")
	}
	if tools, ok := in.Options["tools"].([]any); ok && len(tools) > 0 {
		score += 1
		reasons = append(reasons, "需要工具调用")
	}
	if len([]rune(in.SystemPrompt())) >= 500 {
		score += 1
		reasons = append(reasons, "长系统提示")
	}
	lower := strings.ToLower(text.String())
	keywordHits := 0
	for _, kw := range hardKeywords {
		if strings.Contains(lower, kw) {
			keywordHits++
		}
	}
	// 用户自定义提档关键词与内置关键词同权计分。
	for _, kw := range extraKeywords {
		if kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
			keywordHits++
		}
	}
	switch {
	case keywordHits >= 3:
		score += 2
		reasons = append(reasons, "涉及多个复杂任务关键词")
	case keywordHits >= 1:
		score += 1
		reasons = append(reasons, "含复杂任务关键词")
	}

	tier := store.TierStandard
	switch {
	case score == 0:
		tier = store.TierLight
	case score <= 2:
		tier = store.TierStandard
	default:
		tier = store.TierPro
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "简短问答")
	}
	return Difficulty{Score: score, Tier: tier, Reason: strings.Join(reasons, "、")}
}

// ---------- 用户自定义路由规则（面板「设置 → 通用 → auto 路由规则」） ----------

// auto 路由规则的设置键。
const (
	// AutoSettingForceTier 非空时强制按该档位路由（light/standard/pro），跳过难度评估。
	AutoSettingForceTier = "auto_force_tier"
	// AutoSettingExcludeModels 逗号分隔的模型名，这些模型不参与 auto 候选。
	AutoSettingExcludeModels = "auto_exclude_models"
	// AutoSettingHardKeywords 逗号分隔的自定义复杂关键词，命中即加分提档（与内置关键词同权）。
	AutoSettingHardKeywords = "auto_hard_keywords"
)

// AutoRules 是一次 auto 路由生效的自定义规则。
type AutoRules struct {
	ForceTier     string
	ExcludeModels map[string]bool
	ExtraKeywords []string
}

// autoRules 从设置读取本次生效的路由规则。
func (e *Engine) autoRules() AutoRules {
	r := AutoRules{ExcludeModels: map[string]bool{}}
	if t := strings.TrimSpace(e.st.GetSetting(AutoSettingForceTier, "")); t == store.TierLight ||
		t == store.TierStandard || t == store.TierPro {
		r.ForceTier = t
	}
	r.ExcludeModels = csvSet(e.st.GetSetting(AutoSettingExcludeModels, ""))
	r.ExtraKeywords = csvList(e.st.GetSetting(AutoSettingHardKeywords, ""))
	return r
}

// csvList 把逗号分隔字符串切成去空白的非空列表。
func csvList(s string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// csvSet 把逗号分隔字符串切成小写键集合（模型名比较不区分大小写）。
func csvSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, p := range csvList(s) {
		out[strings.ToLower(p)] = true
	}
	return out
}

// AutoCandidate 是 auto 路由的一个候选模型及其可用渠道。
type AutoCandidate struct {
	Model     store.Model
	Providers []store.Provider
}

// PickAutoCandidates 按请求难度与上下文容量，把全部可用文本模型排成候选序列。
// 顺序即尝试顺序：目标档位优先，同档位内「窗口装得下」的排在前面，之后依次降级到相邻档位。
// 面板可配规则：强制档位（跳过评估）、排除模型、自定义提档关键词。
// 调用方应逐个尝试，全部失败才报错。
func (e *Engine) PickAutoCandidates(in *ChatInput) ([]AutoCandidate, Difficulty, error) {
	rules := e.autoRules()
	d := AssessDifficulty(in, rules.ExtraKeywords...)
	if rules.ForceTier != "" {
		d.Tier = rules.ForceTier
		d.Reason = "面板强制档位 " + rules.ForceTier
	}
	need := EstimateChatTokens(in)

	all, err := e.st.ListModels()
	if err != nil {
		return nil, d, err
	}
	candidates := make([]store.Model, 0, len(all))
	for _, m := range all {
		if !m.Enabled || m.Capability != store.CapChat || m.Name == AutoModelName {
			continue
		}
		if rules.ExcludeModels[strings.ToLower(m.Name)] {
			continue // 面板排除的模型不参与 auto
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return nil, d, errors.New("还没有可用的文本模型，无法使用 auto")
	}

	seen := map[int64]bool{}
	out := make([]AutoCandidate, 0, len(candidates))
	for _, tier := range tierFallback(d.Tier) {
		// 同一档位内先放容量足够的，装不下的排到后面兜底。
		fits := make([]store.Model, 0, 4)
		over := make([]store.Model, 0, 4)
		for _, m := range candidates {
			if seen[m.ID] || m.EffectiveTier() != tier {
				continue
			}
			limit := m.EffectiveContextLength()
			// 窗口未知（0）时不作否决，保持原有的难度择优行为。
			if limit > 0 && limit < need {
				over = append(over, m)
			} else {
				fits = append(fits, m)
			}
		}
		// 同档位内按智力分降序：主模型测评标记过的模型优先接任务，
		// 未测评的（0 分）自然排在已测评的后面，配置顺序作最终平局裁决。
		sort.SliceStable(fits, func(i, j int) bool {
			return fits[i].Config.Intelligence > fits[j].Config.Intelligence
		})
		for _, m := range append(fits, over...) {
			ps, err := e.providersFor(m)
			if err != nil {
				continue
			}
			seen[m.ID] = true
			out = append(out, AutoCandidate{Model: m, Providers: ps})
		}
	}
	if len(out) == 0 {
		return nil, d, errors.New("没有可用的文本模型，无法使用 auto")
	}
	if len(out) > 1 {
		d.Reason = fmt.Sprintf("%s；共 %d 个候选模型可依次尝试", d.Reason, len(out))
	}
	return out, d, nil
}

// PickAuto 返回候选序列中的第一个模型，保留给不需要故障转移的调用方。
func (e *Engine) PickAuto(in *ChatInput) (store.Model, []store.Provider, Difficulty, error) {
	cs, d, err := e.PickAutoCandidates(in)
	if err != nil {
		return store.Model{}, nil, d, err
	}
	return cs[0].Model, cs[0].Providers, d, nil
}

// ChatAuto 依次尝试 auto 的候选模型，直到某个模型成功返回。
//
// 切换的前提是「尚未向客户端下发任何内容」：一旦有 delta 发出就立即停止切换，
// 并原样返回错误，避免客户端收到两个模型拼接起来的重复内容。
// 因此对调用方而言，故障转移只表现为响应变慢。
// onAttempt 会在每次尝试某个模型前被调用，便于调用方掌握"当前正用哪个模型"
// （流式响应需要在首个 chunk 就把 model 字段写对）。
func (e *Engine) ChatAuto(ctx context.Context, in *ChatInput, emit StreamFunc,
	onAttempt func(store.Model)) (*ChatOutput, store.Model, store.Provider, Difficulty, error) {
	cs, d, err := e.PickAutoCandidates(in)
	if err != nil {
		return nil, store.Model{}, store.Provider{}, d, err
	}

	emitted := false
	var lastErr error
	var lastModel store.Model
	var lastProvider store.Provider
	for _, c := range cs {
		lastModel = c.Model
		if len(c.Providers) > 0 {
			lastProvider = c.Providers[0]
		}
		if onAttempt != nil {
			onAttempt(c.Model)
		}
		var wrapped StreamFunc
		if emit != nil {
			wrapped = func(delta StreamDelta) error {
				emitted = true
				return emit(delta)
			}
		}
		out, p, err := e.Chat(ctx, c.Model, c.Providers, in, wrapped)
		if err == nil {
			return out, c.Model, p, d, nil
		}
		lastErr = err
		if p.Name != "" {
			lastProvider = p
		}
		// 已经有内容下发，不能再换模型，否则用户会看到重复内容。
		if emitted {
			return nil, c.Model, p, d, err
		}
		if ctx != nil && ctx.Err() != nil {
			return nil, c.Model, p, d, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的文本模型，无法使用 auto")
	}
	return nil, lastModel, lastProvider, d, lastErr
}

// tierFallback 返回档位尝试顺序：目标档位优先，然后是相邻档位。
func tierFallback(tier string) []string {
	switch tier {
	case store.TierLight:
		return []string{store.TierLight, store.TierStandard, store.TierPro}
	case store.TierPro:
		return []string{store.TierPro, store.TierStandard, store.TierLight}
	default:
		return []string{store.TierStandard, store.TierPro, store.TierLight}
	}
}
