package api

import (
	"encoding/json"
	"fmt"
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

// TestCompactAgentHistory 裁剪保留 system 头与最近若干条，并追加裁剪说明；短历史原样返回。
func TestCompactAgentHistory(t *testing.T) {
	short := []relay.ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "hi"}}
	if got := compactAgentHistory(short, 12); len(got) != 2 {
		t.Fatalf("短历史不应裁剪：got %d 条", len(got))
	}
	hist := []relay.ChatMessage{{Role: "system", Content: "sys"}}
	for i := 0; i < 40; i++ {
		hist = append(hist, relay.ChatMessage{Role: "user", Content: fmt.Sprintf("m%d", i)})
	}
	got := compactAgentHistory(hist, 12)
	if got[0].Content != "sys" {
		t.Fatal("裁剪后必须保留 system 首条")
	}
	if len(got) != 14 { // 1 + 12 + 1 说明
		t.Fatalf("裁剪后应为 14 条：got %d", len(got))
	}
	if !strings.Contains(got[len(got)-1].Content, "已裁剪") {
		t.Fatal("末尾应附裁剪说明")
	}
	if got[1].Content != "m28" {
		t.Fatalf("最近 12 条应为 m28..m39，首条 %q", got[1].Content)
	}
}

// TestCtxOverflowRe 上下文长度类错误识别（各家措辞）。
func TestCtxOverflowRe(t *testing.T) {
	hits := []string{
		"This model's maximum context length is 8192 tokens",
		"context_length_exceeded",
		"prompt is too long: 200000 tokens > 8192 maximum",
		"输入的上下文长度超出限制",
		"请求体过长",
	}
	for _, s := range hits {
		if !ctxOverflowRe.MatchString(s) {
			t.Fatalf("应命中上下文超限：%q", s)
		}
	}
	misses := []string{"connection refused", "401 unauthorized", "rate limit exceeded"}
	for _, s := range misses {
		if ctxOverflowRe.MatchString(s) {
			t.Fatalf("不应命中上下文超限：%q", s)
		}
	}
}

// TestSanitizeJSONControlChars 字符串内的裸换行/Tab 被转义，结构空白原样保留。
func TestSanitizeJSONControlChars(t *testing.T) {
	bad := "{\n  \"think\": \"第一行\n第二行\t制表\",\n  \"action\": \"list_models\"\n}"
	fixed := sanitizeJSONControlChars(bad)
	var turn agentTurn
	if err := json.Unmarshal([]byte(fixed), &turn); err != nil {
		t.Fatalf("消毒后应可解析：%v", err)
	}
	if turn.Action != "list_models" {
		t.Fatalf("action 应保留：%q", turn.Action)
	}
	if !strings.Contains(turn.Think, "第二行") {
		t.Fatalf("think 内容应完整：%q", turn.Think)
	}
}

// TestReplyNudge 预告式回复与漏 ask 卡的纠偏判定。
func TestReplyNudge(t *testing.T) {
	if n := replyNudge(agentTurn{Reply: "已收到密钥，正在探测该上游服务的模型清单。"}, 0, false); n == "" {
		t.Fatal("预告式回复应触发纠偏")
	}
	if n := replyNudge(agentTurn{Reply: "请勾选要接入的模型。"}, 0, false); n == "" {
		t.Fatal("要求选择但无 ask 应触发纠偏")
	}
	if n := replyNudge(agentTurn{Reply: "接入完成，共创建 2 个模型。", Ask: &agentAsk{Kind: "choice"}}, 3, true); n != "" {
		t.Fatal("正常完成回复不应纠偏")
	}
	if n := replyNudge(agentTurn{Reply: "请勾选要接入的模型。", Ask: &agentAsk{Kind: "choice", Options: []string{"a"}}}, 0, false); n != "" {
		t.Fatal("已带 ask 卡的选择请求不应纠偏")
	}
}

// TestReplyNudgeDoneClaim 声称创建/接入完成但没调过写入工具应触发纠偏；动过手则放行。
func TestReplyNudgeDoneClaim(t *testing.T) {
	if n := replyNudge(agentTurn{Reply: "接入完成，渠道与模型都已创建。"}, 3, false); n == "" {
		t.Fatal("声称完成但零写入工具应触发纠偏")
	}
	if n := replyNudge(agentTurn{Reply: "渠道「X」已创建。"}, 1, true); n != "" {
		t.Fatal("已实际写入的完成汇报不应纠偏")
	}
}

// TestReplyNudgeFailClaim 声称保存失败但零工具调用应触发纠偏；有工具轨迹则放行。
func TestReplyNudgeFailClaim(t *testing.T) {
	if n := replyNudge(agentTurn{Reply: "模型保存失败（GLM-4.7-Flash）：模型库中尚未包含。"}, 0, false); n == "" {
		t.Fatal("零工具调用却声称失败应触发纠偏")
	}
	if n := replyNudge(agentTurn{Reply: "模型保存失败：名称重复。"}, 2, false); n != "" {
		t.Fatal("已有工具轨迹的失败汇报不应纠偏")
	}
}

// TestRepairAsk 引导卡兜底：模型不给卡时按上下文补卡，给了错的选项时以探测清单为准。
func TestRepairAsk(t *testing.T) {
	s := &Server{}

	// 1. 索要秘钥但没给卡 → 补 secret 卡
	if a := s.repairAsk(nil, "请把该服务的 API 秘钥粘贴给我", nil, false, false, false); a == nil || a.Kind != "secret" {
		t.Fatalf("索要秘钥时应补 secret 卡，实际 %+v", a)
	}
	// 2. 刚交完秘钥、探测有清单、模型让用户选却没给卡 → 补 multi choice 卡
	models := []string{"gpt-4o", "gpt-4o-mini"}
	if a := s.repairAsk(nil, "探测成功，请选择要接入的模型", models, true, true, false); a == nil || a.Kind != "choice" || !a.Multi || len(a.Options) != 2 {
		t.Fatalf("应补 multi choice 卡，实际 %+v", a)
	}
	// 3. 模型给了选项但与探测清单不符 → 以清单为准纠正
	bad := &agentAsk{Kind: "choice", Options: []string{"凭空捏造的模型"}}
	a := s.repairAsk(bad, "", models, true, true, false)
	if a == nil || len(a.Options) != 2 || a.Options[0] != "gpt-4o" {
		t.Fatalf("选项应被探测清单纠正，实际 %+v", a)
	}
	// 4. 已有可用卡 → 原样保留，不干预
	good := &agentAsk{Kind: "choice", Options: models}
	if a := s.repairAsk(good, "", models, true, true, false); a != good {
		t.Fatal("已正确的卡不应被改动")
	}
	// 5. 无关回复 → 不补卡
	if a := s.repairAsk(nil, "已全部完成", nil, false, true, false); a != nil {
		t.Fatalf("无关回复不应补卡，实际 %+v", a)
	}
	// 6. 退化回复（空/只有引号）且处于接入流程 → 补卡，别把用户卡死
	if a := s.repairAsk(nil, `""`, nil, false, true, false); a == nil || a.Kind != "secret" {
		t.Fatalf("退化回复应补 secret 卡，实际 %+v", a)
	}
	if a := s.repairAsk(nil, "。", models, true, true, false); a == nil || a.Kind != "choice" {
		t.Fatalf("退化回复 + 已有清单应补 choice 卡，实际 %+v", a)
	}
	// 7. 非标准 kind 归一化：模型写 select/input 时也要能渲染
	for _, raw := range []string{"select", "choice_list", "input", "多选"} {
		q := &agentAsk{Kind: raw, Options: []string{"x"}}
		if a := s.repairAsk(q, "", nil, false, false, false); a == nil || (a.Kind != "secret" && a.Kind != "choice") {
			t.Fatalf("kind=%s 应被归一化，实际 %+v", raw, a)
		}
	}
	// 8. 没有探测清单却给选择卡 → 一定是编造的，改成要秘钥
	fake := &agentAsk{Kind: "choice", Multi: true, Options: []string{"deepseek-chat", "glm-4.7"}}
	if a := s.repairAsk(fake, "请勾选模型", nil, false, false, false); a == nil || a.Kind != "secret" {
		t.Fatalf("无探测清单的选项卡应改为秘钥卡，实际 %+v", a)
	}
	// 9. 已经动过手且汇报完成 → 终局答复，不该再挂引导卡
	if a := s.repairAsk(nil, "✅ 接入完成！", models, false, true, true); a != nil {
		t.Fatalf("完成汇报后不应补卡，实际 %+v", a)
	}
}

// TestProbeModelIDsFromHistory 能从历史工具结果里回捞探测到的模型清单。
func TestProbeModelIDsFromHistory(t *testing.T) {
	detail, _ := json.Marshal(probeResult{Models: []probeModel{
		{ID: "gpt-4o", Capability: "chat"},
		{ID: "gpt-4o", Capability: "chat"},
		{ID: "gpt-4o-mini", Capability: "chat"},
	}})
	history := []relay.ChatMessage{
		{Role: "user", Content: `我要接入 ` + "https://api.example.com/v1"},
		{Role: "assistant", Content: `{"action":"probe_upstream","params":{"base_url":"https://api.example.com/v1","api_key":"sk-x"}}`},
		{Role: "user", Content: `[工具结果] {"ok":true,"result":` + string(detail) + `,"error":""}`},
	}
	got := probeModelIDsFromHistory(history)
	if len(got) != 2 || got[0] != "gpt-4o" || got[1] != "gpt-4o-mini" {
		t.Fatalf("应回捞去重后的 2 个模型，实际 %v", got)
	}
}
