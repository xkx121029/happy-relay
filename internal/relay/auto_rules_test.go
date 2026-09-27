package relay

// auto 自定义路由规则的测试：强制档位、排除模型、自定义提档关键词。

import (
	"path/filepath"
	"strings"
	"testing"

	"happy-relay/internal/store"
)

func newAutoTestEngine(t *testing.T) *Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auto.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewEngine(st, 3, 60, 60)
}

// seedProvider 建 one 个可用渠道，返回其 ID。
func seedProvider(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	p, err := st.SaveProvider(store.Provider{
		Name:     name,
		Kind:     "openai",
		BaseURLs: []string{"http://127.0.0.1:1"},
		APIKeys:  []string{"test-key"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// seedModel 建一个指定档位的启用文本模型。
func seedModel(t *testing.T, st *store.Store, name, tier string, providerID int64) store.Model {
	t.Helper()
	m, err := st.SaveModel(store.Model{
		Name:         name,
		UpstreamName: name,
		Capability:   store.CapChat,
		Enabled:      true,
		Config:       store.ModelConfig{Tier: tier},
		ProviderID:   providerID,
		ProviderIDs:  []int64{providerID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func autoInput(text string) *ChatInput {
	return &ChatInput{Model: AutoModelName, Messages: []ChatMessage{{Role: "user", Content: text}}}
}

// TestAssessDifficultyExtraKeywords 自定义提档关键词与内置关键词同权。
func TestAssessDifficultyExtraKeywords(t *testing.T) {
	in := autoInput("今天天气不错")
	base := AssessDifficulty(in)
	if base.Tier != store.TierLight {
		t.Fatalf("简短无关键词应为 light，实际 %s", base.Tier)
	}
	hit := AssessDifficulty(in, "天气", "毛肚")
	if hit.Tier == store.TierLight {
		t.Fatalf("命中 2 个自定义关键词应提档，实际仍是 %s", hit.Tier)
	}
}

// TestAutoForceTier 强制档位跳过难度评估。
func TestAutoForceTier(t *testing.T) {
	eng := newAutoTestEngine(t)
	pid := seedProvider(t, eng.st, "p1")
	seedModel(t, eng.st, "m-light", store.TierLight, pid)
	seedModel(t, eng.st, "m-pro", store.TierPro, pid)

	if err := eng.st.SetSetting(AutoSettingForceTier, store.TierPro); err != nil {
		t.Fatal(err)
	}
	cs, d, err := eng.PickAutoCandidates(autoInput("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Reason, "强制档位") {
		t.Fatalf("Reason 应注明强制档位：%s", d.Reason)
	}
	if cs[0].Model.Name != "m-pro" {
		t.Fatalf("强制 pro 档后首个候选应是 m-pro，实际 %s", cs[0].Model.Name)
	}
	// 非法档位值应被忽略，回到按难度评估。
	if err := eng.st.SetSetting(AutoSettingForceTier, "ultra"); err != nil {
		t.Fatal(err)
	}
	cs, _, err = eng.PickAutoCandidates(autoInput("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if cs[0].Model.Name != "m-light" {
		t.Fatalf("非法强制档位应被忽略（简短输入 → light 优先），实际 %s", cs[0].Model.Name)
	}
}

// TestAutoExcludeModels 排除的模型不进候选，全部被排除时报错。
func TestAutoExcludeModels(t *testing.T) {
	eng := newAutoTestEngine(t)
	pid := seedProvider(t, eng.st, "p1")
	seedModel(t, eng.st, "m-a", store.TierLight, pid)
	seedModel(t, eng.st, "m-b", store.TierStandard, pid)

	if err := eng.st.SetSetting(AutoSettingExcludeModels, "M-A , m_x"); err != nil {
		t.Fatal(err)
	}
	cs, _, err := eng.PickAutoCandidates(autoInput("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Model.Name != "m-b" {
		t.Fatalf("排除 M-A（大小写不敏感）后应只剩 m-b，实际 %d 个", len(cs))
	}
	if err := eng.st.SetSetting(AutoSettingExcludeModels, "m-a,m-b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.PickAutoCandidates(autoInput("hi")); err == nil {
		t.Fatal("全部模型被排除时应报错")
	}
}
