package api

import (
	"encoding/json"
	"strings"
	"testing"

	"happy-relay/internal/config"
	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// newTestServer 用内存 store + 无网络引擎构造最小 Server，专门测不依赖上游的纯逻辑。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("打开内存 store 失败：%v", err)
	}
	eng := relay.NewEngine(st, 3, 60, 30)
	return &Server{st: st, eng: eng, cfg: &config.Config{}}
}

// TestApplyAgentSettings 验证协议合一的核心：声明式 settings 字典能热生效到 store。
func TestApplyAgentSettings(t *testing.T) {
	s := newTestServer(t)

	step := s.applyAgentSettings(map[string]any{"probe_interval": float64(90), "compact": "off"})
	if !step.OK {
		t.Fatalf("applyAgentSettings 失败：%s", step.Error)
	}
	if step.Action != "save_settings" {
		t.Fatalf("期望 action=save_settings，实际 %q", step.Action)
	}
	if got := s.st.GetSetting("probe_interval", ""); got != "90" {
		t.Fatalf("probe_interval 未生效，got %q", got)
	}
	if got := s.st.GetSetting("compact", ""); got != "off" {
		t.Fatalf("compact 未生效，got %q", got)
	}
}

// TestApplyAgentSettingsEmpty 空 settings 应标记失败，不静默跳过。
func TestApplyAgentSettingsEmpty(t *testing.T) {
	s := newTestServer(t)
	step := s.applyAgentSettings(map[string]any{})
	if step.OK {
		t.Fatalf("空 settings 不应成功")
	}
}

// TestAgentTurnSettingsParsed 验证模型返回的 JSON 里 settings 字段可被正确反序列化，
// 这是「协议合一」能被后端消费的前提。
func TestAgentTurnSettingsParsed(t *testing.T) {
	var turn agentTurn
	raw := `{"reply":"已改好","settings":{"probe_interval":90,"compact":"off"}}`
	if err := json.Unmarshal([]byte(raw), &turn); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if turn.Reply != "已改好" {
		t.Fatalf("reply 解析错误：%q", turn.Reply)
	}
	if len(turn.Settings) != 2 {
		t.Fatalf("settings 长度应为 2，实际 %d", len(turn.Settings))
	}
	if v, _ := turn.Settings["probe_interval"].(float64); v != 90 {
		t.Fatalf("probe_interval 应为 90，实际 %v", turn.Settings["probe_interval"])
	}
}

// TestAgentStepDetailCarriesFullJSON 验证工具步骤的 Detail 字段携带未截断的完整 JSON，
// summary 仍保留带前缀的截断预览——前端「详情」展开依赖该字段。
func TestAgentStepDetailCarriesFullJSON(t *testing.T) {
	s := newTestServer(t)
	prov, err := s.st.SaveProvider(store.Provider{Name: "p-test", Kind: "openai", BaseURLs: []string{"https://example.invalid/v1"}, Enabled: true})
	if err != nil {
		t.Fatalf("保存测试渠道失败：%v", err)
	}
	if _, err := s.st.SaveModel(store.Model{
		Name:       "m1",
		Capability: "text",
		Enabled:    true,
		Inputs:     []store.ModelInput{{ProviderID: prov.ID, UpstreamName: "up-1"}},
	}); err != nil {
		t.Fatalf("保存测试模型失败：%v", err)
	}
	step := s.agentExec("list_models", nil)
	if !step.OK {
		t.Fatalf("list_models 失败：%s", step.Error)
	}
	if step.Detail == "" {
		t.Fatal("Detail 不应为空（前端需要完整 JSON）")
	}
	all, err := s.st.ListModels()
	if err != nil {
		t.Fatalf("ListModels 失败：%v", err)
	}
	full, _ := json.Marshal(all)
	if step.Detail != string(full) {
		t.Fatalf("Detail 应为未截断的完整 JSON：got %q want %q", step.Detail, string(full))
	}
	if !json.Valid([]byte(step.Detail)) {
		t.Fatal("Detail 不是合法 JSON")
	}
	if !strings.HasPrefix(step.Summary, "共 ") {
		t.Fatalf("Summary 应保留人类可读预览，实际 %q", step.Summary)
	}
}
