package relay

import (
	"strings"
	"testing"

	"happy-relay/internal/store"
)

// TestPreferTextOnlyTargets 验证多模态聚合的成本优先排序：
// 纯文本请求时，纯文本上游稳定排在多模态上游前面；带图请求不受影响。
func TestPreferTextOnlyTargets(t *testing.T) {
	// 聚合模型：视觉主上游在前，两个纯文本上游在后。
	m := store.Model{
		Capability: store.CapChat,
		Inputs: []store.ModelInput{
			{ProviderID: 1, UpstreamName: "glm-4.6v-flash", InputModalities: []string{"text", "image"}},
			{ProviderID: 1, UpstreamName: "glm-4.7-flash", InputModalities: []string{"text"}},
			{ProviderID: 1, UpstreamName: "glm-4-flash", InputModalities: []string{"text"}},
		},
	}
	ps := []store.Provider{{ID: 1}}
	e := &Engine{}

	// 纯文本请求：文本上游应排到前面，且组内保持配置顺序。
	got := e.expandTargets(m, ps, []string{store.ModalityText})
	if got[0].UpstreamName != "glm-4.7-flash" || got[1].UpstreamName != "glm-4-flash" {
		t.Fatalf("纯文本请求应先走文本上游：%v", upstreamNames(got))
	}
	if got[2].UpstreamName != "glm-4.6v-flash" {
		t.Fatalf("视觉上游应兜底在最后：%v", upstreamNames(got))
	}

	// 带图请求：只匹配视觉上游，顺序不受影响。
	got = e.expandTargets(m, ps, []string{store.ModalityText, store.ModalityImage})
	if len(got) != 1 || got[0].UpstreamName != "glm-4.6v-flash" {
		t.Fatalf("带图请求应只走视觉上游：%v", upstreamNames(got))
	}

	// 单上游时排序是 no-op。
	single := store.Model{Capability: store.CapChat, Inputs: []store.ModelInput{
		{ProviderID: 1, UpstreamName: "glm-4.6v-flash", InputModalities: []string{"text", "image"}},
	}}
	got = e.expandTargets(single, ps, []string{store.ModalityText})
	if len(got) != 1 || got[0].UpstreamName != "glm-4.6v-flash" {
		t.Fatalf("单上游不应受排序影响：%v", upstreamNames(got))
	}
}

// TestEffectiveContextLengthMin 验证聚合模型的上下文按最差情况（最小窗口）校验。
func TestEffectiveContextLengthMin(t *testing.T) {
	// deepseek → 65536，gpt-4o → 128000：聚合后应取 65536 而不是先遇到的 128000。
	m := store.Model{
		Capability: store.CapChat,
		Inputs: []store.ModelInput{
			{ProviderID: 1, UpstreamName: "gpt-4o"},
			{ProviderID: 2, UpstreamName: "deepseek-chat"},
		},
	}
	if got := m.EffectiveContextLength(); got != 65536 {
		t.Fatalf("聚合模型上下文应取最小值 65536，实际 %d", got)
	}

	// 显式配置优先于推断。
	m.Config.ContextLength = 32000
	if got := m.EffectiveContextLength(); got != 32000 {
		t.Fatalf("显式配置应优先生效，实际 %d", got)
	}
}

// TestImageTokenCost 验证图片 token 估档。
func TestImageTokenCost(t *testing.T) {
	cases := []struct {
		bytes int
		want  int
	}{
		{50 * 1024, imageTokensSmall},
		{100 * 1024, imageTokensSmall},
		{500 * 1024, imageTokensMedium},
		{700 * 1024, imageTokensMedium},
		{2 * 1024 * 1024, imageTokensLarge},
	}
	for _, c := range cases {
		if got := imageTokenCost(c.bytes); got != c.want {
			t.Fatalf("imageTokenCost(%d) = %d, want %d", c.bytes, got, c.want)
		}
	}
}

// TestEstimateChatTokensImages 验证带图消息的 token 估算随体积增长。
func TestEstimateChatTokensImages(t *testing.T) {
	big := strings.Repeat("a", 2*1024*1024)
	small := make([]byte, 50*1024)
	in := &ChatInput{Messages: []ChatMessage{{
		Role: "user",
		Parts: []ContentPart{
			{Type: "image", Data: small},
			{Type: "image", Data: []byte(big[:len(big)])},
		},
	}}}
	got := EstimateChatTokens(in)
	// 800 + 2200 + 消息固定开销，至少应超过两张小图的总和。
	if got < imageTokensSmall+imageTokensLarge {
		t.Fatalf("带大图的估算应 ≥ %d，实际 %d", imageTokensSmall+imageTokensLarge, got)
	}
}

func upstreamNames(ts []Target) string {
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += "|"
		}
		out += t.UpstreamName
	}
	return out
}
