package store

import "testing"

// BenchmarkStatsAggregateMiss 量 Stats 缓存未命中时的真实成本：
// 对全表做 COUNT/AVG/SUM 聚合。5 万行日志下这是面板最贵的一击。
func BenchmarkStatsAggregateMiss(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 4, 5, 20, 50000)
	q := `SELECT COUNT(*),COALESCE(AVG(latency_ms),0),
		COALESCE(SUM(prompt_tokens),0),COALESCE(SUM(completion_tokens),0),
		COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0)
		FROM usage_logs`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var n, avg, pt, ct, failed int
		var avgF float64
		if err := s.db.QueryRow(q).Scan(&n, &avgF, &pt, &ct, &failed); err != nil {
			b.Fatal(err)
		}
		_ = avg
	}
}

// BenchmarkProviderModelCounts 量「每个渠道挂几个模型」的聚合（渠道列表页用）。
func BenchmarkProviderModelCounts(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.providerModelCounts(); err != nil {
			b.Fatal(err)
		}
	}
}

// TestStatsAggregatePlan 打印聚合查询的执行计划，确认是否走了覆盖索引。
func TestStatsAggregatePlan(t *testing.T) {
	s := newPerfStore(t)
	seedPerf(t, s, 4, 5, 20, 5000)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN
		SELECT COUNT(*),COALESCE(AVG(latency_ms),0),
		COALESCE(SUM(prompt_tokens),0),COALESCE(SUM(completion_tokens),0),
		COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0)
		FROM usage_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("计划: %s", detail)
	}
}

// BenchmarkStatsAggregateForcedIndex 强制走覆盖索引的聚合成本。
func BenchmarkStatsAggregateForcedIndex(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 4, 5, 20, 50000)
	q := `SELECT COUNT(*),COALESCE(AVG(latency_ms),0),
		COALESCE(SUM(prompt_tokens),0),COALESCE(SUM(completion_tokens),0),
		COALESCE(SUM(CASE WHEN status>=400 THEN 1 ELSE 0 END),0)
		FROM usage_logs INDEXED BY idx_usage_logs_stats`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var n, pt, ct, failed int
		var avgF float64
		if err := s.db.QueryRow(q).Scan(&n, &avgF, &pt, &ct, &failed); err != nil {
			b.Fatal(err)
		}
	}
}
