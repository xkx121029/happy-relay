package relay

import (
	"encoding/json"
	"unicode"
)

// imageTokensEstimate 是单张图片的上下文开销估值。
// 各供应商算法差异较大（低细节约 85，高细节可达上千），这里取偏保守的中间值，
// 仅用于容量规划，不参与计费。
const imageTokensEstimate = 800

// EstimateTokens 估算一段文本的 token 数。
// 采用「CJK 每字约 1 token、其余每 4 字符约 1 token」的通用经验值，
// 对中英混排足够接近，且不引入 tokenizer 依赖。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, r := range s {
		switch {
		case isCJK(r):
			cjk++
		case !unicode.IsSpace(r):
			other++
		}
	}
	n := cjk + (other+3)/4
	if n == 0 {
		n = 1
	}
	return n
}

// isCJK 判断是否为中日韩文字或全角标点（这类字符普遍接近 1 字 1 token）。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, // 中日韩统一表意文字
		r >= 0x3400 && r <= 0x4DBF, // 扩展 A 区
		r >= 0x3000 && r <= 0x303F, // 中文标点
		r >= 0xFF00 && r <= 0xFFEF, // 全角字符
		r >= 0x3040 && r <= 0x30FF, // 日文假名
		r >= 0xAC00 && r <= 0xD7AF: // 韩文
		return true
	}
	return false
}

// EstimateChatTokens 估算一次对话请求的输入 token 总量。
// 除正文外，还会计入消息结构、图片、工具调用与工具定义的开销。
func EstimateChatTokens(in *ChatInput) int {
	if in == nil {
		return 0
	}
	total := 0
	for _, msg := range in.Messages {
		total += 4 // 每条消息的角色与分隔符开销
		total += EstimateTokens(msg.Content)
		total += EstimateTokens(msg.Reasoning)
		for _, part := range msg.Parts {
			if part.Type == "image" {
				total += imageTokensEstimate
			}
		}
		for _, tc := range msg.ToolCalls {
			total += EstimateTokens(tc.Name) + EstimateTokens(tc.Arguments) + 8
		}
	}
	// tools 定义同样占用上下文，按 JSON 长度估算。
	if tools, ok := in.Options["tools"].([]any); ok && len(tools) > 0 {
		if b, err := json.Marshal(tools); err == nil {
			total += EstimateTokens(string(b))
		}
	}
	return total
}
