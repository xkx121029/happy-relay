package relay

import (
	"errors"
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
func AssessDifficulty(in *ChatInput) Difficulty {
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
	case textLen >= 4000:
		score += 3
		reasons = append(reasons, "超长输入")
	case textLen >= 1200:
		score += 2
		reasons = append(reasons, "长输入")
	case textLen >= 300:
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
	for _, kw := range hardKeywords {
		if strings.Contains(lower, kw) {
			score += 1
			reasons = append(reasons, "含复杂任务关键词「"+kw+"」")
			break
		}
	}

	tier := store.TierStandard
	switch {
	case score <= 1:
		tier = store.TierLight
	case score <= 3:
		tier = store.TierStandard
	default:
		tier = store.TierPro
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "简短问答")
	}
	return Difficulty{Score: score, Tier: tier, Reason: strings.Join(reasons, "、")}
}

// PickAuto 按请求难度挑选一个对外文本模型，并返回它绑定的渠道。
func (e *Engine) PickAuto(in *ChatInput) (store.Model, []store.Provider, Difficulty, error) {
	d := AssessDifficulty(in)

	all, err := e.st.ListModels()
	if err != nil {
		return store.Model{}, nil, d, err
	}
	candidates := make([]store.Model, 0, len(all))
	for _, m := range all {
		if !m.Enabled || m.Capability != store.CapChat || m.Name == AutoModelName {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return store.Model{}, nil, d, errors.New("还没有可用的文本模型，无法使用 auto")
	}

	// 先找目标档位，再向相邻档位降级。
	for _, tier := range tierFallback(d.Tier) {
		for _, m := range candidates {
			if m.EffectiveTier() != tier {
				continue
			}
			if ps, err := e.providersFor(m); err == nil {
				return m, ps, d, nil
			}
		}
	}
	// 兜底：退回到第一个可用模型。
	for _, m := range candidates {
		if ps, err := e.providersFor(m); err == nil {
			return m, ps, d, nil
		}
	}
	return store.Model{}, nil, d, errors.New("没有可用的文本模型，无法使用 auto")
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
