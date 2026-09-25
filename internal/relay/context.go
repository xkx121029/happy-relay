package relay

import "fmt"

// 上下文超限的处理策略。
const (
	// ContextPolicyOff 不做校验，完全交给上游判断。
	ContextPolicyOff = "off"
	// ContextPolicyReject 估算超出时直接返回可读的错误（默认）。
	ContextPolicyReject = "reject"
	// ContextPolicyTruncate 自动裁剪最早的历史消息后继续调用。
	ContextPolicyTruncate = "truncate"
)

// DefaultSafetyPercent 是默认的上下文安全系数（百分比）：
// 只用窗口的该比例容纳输入，其余留给输出与估算误差。
const DefaultSafetyPercent = 90

// ContextPlan 是一次请求的上下文容量规划结果。
type ContextPlan struct {
	Limit   int  `json:"limit"`   // 模型上下文窗口，0 表示未知
	Budget  int  `json:"budget"`  // 本次可用于输入的预算
	Used    int  `json:"used"`    // 估算的输入 token
	Fits    bool `json:"fits"`    // 是否装得下
	OverBy  int  `json:"over_by"` // 超出预算多少
	Trimmed int  `json:"trimmed"` // 被裁掉的消息条数
	Applied bool `json:"applied"` // 是否真的执行了裁剪
}

// ContextError 表示估算输入超过了模型可用的上下文预算。
type ContextError struct {
	Limit  int
	Budget int
	Used   int
}

func (e *ContextError) Error() string {
	return fmt.Sprintf("输入约 %d tokens，超过模型上下文可用预算 %d（窗口 %d）。"+
		"可精简历史消息或输入内容，或改用上下文更大的模型；"+
		"也可以在「设置 → 上下文超限策略」里改为自动裁剪。",
		e.Used, e.Budget, e.Limit)
}

// PlanContext 评估请求相对模型上下文窗口的占用，并按策略调整消息列表。
// 返回的输入是调整后的副本，原输入不会被修改；limit<=0 或策略为 off 时原样返回。
func PlanContext(in *ChatInput, limit, safetyPercent int, policy string) (*ChatInput, ContextPlan, error) {
	plan := ContextPlan{Limit: limit}
	if in == nil {
		plan.Fits = true
		return in, plan, nil
	}
	plan.Used = EstimateChatTokens(in)
	if limit <= 0 || policy == ContextPolicyOff {
		// 窗口未知或已关闭校验：不拦截，只回报估算结果。
		plan.Fits = true
		plan.Budget = limit
		return in, plan, nil
	}
	if safetyPercent <= 0 || safetyPercent > 100 {
		safetyPercent = DefaultSafetyPercent
	}
	budget := limit * safetyPercent / 100
	plan.Budget = budget
	if plan.Used <= budget {
		plan.Fits = true
		return in, plan, nil
	}

	plan.OverBy = plan.Used - budget
	if policy == ContextPolicyTruncate {
		if msgs, removed := trimMessages(in, budget); removed > 0 {
			cp := *in
			cp.Messages = msgs
			plan.Used = EstimateChatTokens(&cp)
			plan.Trimmed = removed
			plan.Fits = true
			plan.OverBy = 0
			plan.Applied = true
			return &cp, plan, nil
		}
	}
	plan.Fits = false
	return in, plan, &ContextError{Limit: limit, Budget: budget, Used: plan.Used}
}

// trimMessages 从最早的历史消息开始裁剪，保留系统提示与最近的对话，
// 直到估算用量落入预算；返回裁剪后的消息与删除条数。
// 若裁到只剩一条非系统消息仍放不下，返回 removed=0，交由上层报错。
func trimMessages(in *ChatInput, budget int) ([]ChatMessage, int) {
	msgs := append([]ChatMessage(nil), in.Messages...)
	removed := 0
	for nonSystemCount(msgs) > 1 {
		if estimateForMessages(in, msgs) <= budget {
			break
		}
		idx := firstNonSystemIndex(msgs)
		if idx < 0 {
			break
		}
		msgs = append(msgs[:idx], msgs[idx+1:]...)
		removed++
		// 裁掉带 tool_calls 的 assistant 后，紧随其后的工具结果也应一并移除，
		// 否则会留下没有对应调用的 tool 消息，部分上游会直接拒绝。
		for {
			t := firstNonSystemIndex(msgs)
			if t < 0 || msgs[t].Role != "tool" {
				break
			}
			msgs = append(msgs[:t], msgs[t+1:]...)
			removed++
		}
	}
	if removed == 0 {
		return nil, 0
	}
	return msgs, removed
}

// estimateForMessages 复用整体估算逻辑，只替换消息列表。
func estimateForMessages(in *ChatInput, msgs []ChatMessage) int {
	cp := *in
	cp.Messages = msgs
	return EstimateChatTokens(&cp)
}

// firstNonSystemIndex 返回第一条非系统消息的下标，没有则返回 -1。
func firstNonSystemIndex(msgs []ChatMessage) int {
	for i, m := range msgs {
		if m.Role != "system" && m.Role != "developer" {
			return i
		}
	}
	return -1
}

// nonSystemCount 统计非系统消息条数，裁剪时至少要保留一条。
func nonSystemCount(msgs []ChatMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Role != "system" && m.Role != "developer" {
			n++
		}
	}
	return n
}
