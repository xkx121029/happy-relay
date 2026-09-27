package relay

import (
	"path/filepath"
	"testing"

	"happy-relay/internal/store"
)

// TestStatGuardDeprioritize 验证画像降权：
// 连续失败 >= 3 的目标被稳定排到队尾；全部失败或无人失败时顺序不变。
func TestStatGuardDeprioritize(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer st.Close()

	g := newStatGuard(st)
	targets := []Target{
		{Provider: store.Provider{ID: 1}, UpstreamName: "a"},
		{Provider: store.Provider{ID: 1}, UpstreamName: "b"},
		{Provider: store.Provider{ID: 1}, UpstreamName: "c"},
	}

	// 无画像时顺序不变。
	if got := g.deprioritize(targets); len(got) != 3 || got[0].UpstreamName != "a" {
		t.Fatalf("无画像时不应重排：%v", names(got))
	}

	// a 连续失败 3 次 → 排到队尾。
	for i := 0; i < 3; i++ {
		g.report(1, "a", false, 100, "boom")
	}
	got := g.deprioritize(targets)
	if got[2].UpstreamName != "a" || got[0].UpstreamName != "b" {
		t.Fatalf("连续失败 3 次的 a 应排到队尾：%v", names(got))
	}

	// 全部失败 3 次 → 顺序不变（避免死局）。
	for _, name := range []string{"b", "c"} {
		for i := 0; i < 3; i++ {
			g.report(1, name, false, 100, "boom")
		}
	}
	got = g.deprioritize(targets)
	if names(got) != "a|b|c" {
		t.Fatalf("全部失败时应保持原序：%v", names(got))
	}

	// a 成功一次 → 连续失败清零，回到队首。
	g.report(1, "a", true, 120, "")
	if s, ok := g.snapshot(1, "a"); !ok || s.ConsecFails != 0 {
		t.Fatalf("成功后连续失败应清零：%+v", s)
	}
	got = g.deprioritize(targets)
	if got[0].UpstreamName != "a" {
		t.Fatalf("恢复健康后 a 应回到队首：%v", names(got))
	}
}

// TestStatGuardEWMAPersist 验证 EWMA 延迟平滑与落盘持久化：
// 重启（重建 statGuard）后画像从库里装回，累计不丢。
func TestStatGuardEWMAPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}

	g := newStatGuard(st)
	g.report(2, "glm-4.6v", true, 100, "")
	g.report(2, "glm-4.6v", true, 200, "")
	s, ok := g.snapshot(2, "glm-4.6v")
	if !ok || s.OKCount != 2 {
		t.Fatalf("应累计 2 次成功：%+v", s)
	}
	// EWMA：首次取原值 100，第二次 100*0.7+200*0.3=130。
	if s.AvgLatencyMS < 129 || s.AvgLatencyMS > 131 {
		t.Fatalf("EWMA 应为 130，实际 %f", s.AvgLatencyMS)
	}

	// 强制落盘：直接构造 flush（防抖 5 秒太久，手动触发一次）。
	g.mu.Lock()
	for k, item := range g.items {
		if item.dirty {
			_ = st.SaveUpstreamStat(store.UpstreamStat{
				ProviderID: k.providerID, UpstreamName: k.upstream,
				OKCount: item.OKCount, FailCount: item.FailCount, ConsecFails: item.ConsecFails,
				AvgLatencyMS: item.AvgLatency, LastOKAt: item.LastOKAt, LastFailAt: item.LastFailAt,
				LastError: item.LastError, UpdatedAt: "t",
			})
			item.dirty = false
		}
	}
	g.mu.Unlock()
	st.Close()

	// 「重启」：重开库、重建守卫，画像应完整装回。
	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("重开数据库失败：%v", err)
	}
	defer st2.Close()
	g2 := newStatGuard(st2)
	s2, ok := g2.snapshot(2, "glm-4.6v")
	if !ok || s2.OKCount != 2 || s2.AvgLatencyMS < 129 || s2.AvgLatencyMS > 131 {
		t.Fatalf("重启后画像应保留：%+v ok=%v", s2, ok)
	}
}

func names(ts []Target) string {
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += "|"
		}
		out += t.UpstreamName
	}
	return out
}
