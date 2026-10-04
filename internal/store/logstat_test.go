package store

import (
	"path/filepath"
	"testing"
)

func newLogTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestUsageLogStatusInvariant 「有 error 必是失败状态」这条不变量必须成立。
//
// 概览统计的"失败数"聚合只读 status 一个窄列（配合覆盖索引免去回表读 128KB 正文），
// 靠的就是这条不变量。谁要是写了 error 却忘了填失败状态，统计就会把失败算成成功。
func TestUsageLogStatusInvariant(t *testing.T) {
	s := newLogTestStore(t)
	cases := []struct {
		name string
		in   UsageLog
		want int
	}{
		{"正常成功", UsageLog{Model: "m", Status: 200}, 200},
		{"有错误但状态漏填", UsageLog{Model: "m", Error: "上游超时"}, 502},
		{"有错误且状态填了 200", UsageLog{Model: "m", Status: 200, Error: "boom"}, 502},
		{"有错误且已是 4xx", UsageLog{Model: "m", Status: 400, Error: "参数错"}, 400},
		{"有错误且已是 5xx", UsageLog{Model: "m", Status: 503, Error: "不可用"}, 503},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s.AddUsageLog(c.in)
			s.FlushUsageLogs()
			got, err := s.ListUsageLogs(1, 0, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("应有 1 条日志，实际 %d", len(got))
			}
			if got[0].Status != c.want {
				t.Fatalf("状态码应为 %d，实际 %d", c.want, got[0].Status)
			}
		})
	}
}

// TestStatsFailedCount 统计里的失败数必须等于真实失败条数。
func TestStatsFailedCount(t *testing.T) {
	s := newLogTestStore(t)
	s.AddUsageLog(UsageLog{Model: "m", Status: 200})
	s.AddUsageLog(UsageLog{Model: "m", Status: 200})
	s.AddUsageLog(UsageLog{Model: "m", Error: "上游 500"}) // 兜底成 502
	s.AddUsageLog(UsageLog{Model: "m", Status: 429, Error: "限流"})
	s.FlushUsageLogs()

	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalCalls != 4 {
		t.Fatalf("总调用应为 4，实际 %d", st.TotalCalls)
	}
	if st.FailedCalls != 2 {
		t.Fatalf("失败数应为 2，实际 %d", st.FailedCalls)
	}
	if st.SuccessRate < 49.9 || st.SuccessRate > 50.1 {
		t.Fatalf("成功率应为 50%%，实际 %.1f", st.SuccessRate)
	}
}

// TestStatsAggregateWithCoveringIndex 覆盖索引缺失时统计仍要能出数（自动回退）。
func TestStatsAggregateWithCoveringIndex(t *testing.T) {
	s := newLogTestStore(t)
	s.AddUsageLog(UsageLog{Model: "m", Status: 200})
	s.FlushUsageLogs()
	// 模拟"老库没迁移"：把索引删掉再统计
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_usage_logs_stats`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stats(); err != nil {
		t.Fatalf("索引缺失时统计应回退而不是报错：%v", err)
	}
}
