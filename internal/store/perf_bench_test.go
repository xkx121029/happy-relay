package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// 性能基准：用接近真实运营的规模铺数据，量热路径的真实耗时。
// 规模：20 个渠道、300 个模型、每渠道 250 条上游目录（共 5000）、5 万条调用日志。
//
//	go test ./internal/store -run XXX -bench . -benchtime 200x
func newPerfStore(t testing.TB) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "perf.db"))
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	// Windows 上不关句柄会导致 TempDir 清理失败，基准跑完必须显式关闭。
	t.Cleanup(func() { s.Close() })
	return s
}

func seedPerf(t testing.TB, s *Store, providers, modelsPerProvider, catalogPerProvider, logs int) {
	t.Helper()
	names := make([]string, 0, providers*modelsPerProvider)
	for p := 0; p < providers; p++ {
		prov := Provider{
			Name:     fmt.Sprintf("perf-provider-%02d", p),
			Kind:     KindOpenAI,
			BaseURLs: []string{fmt.Sprintf("https://upstream-%02d.example.com/v1", p)},
			APIKeys:  []string{"sk-perf-" + fmt.Sprint(p)},
			Enabled:  true,
		}
		saved, err := s.SaveProvider(prov)
		if err != nil {
			t.Fatalf("建渠道失败：%v", err)
		}
		for c := 0; c < catalogPerProvider; c++ {
			if _, err := s.SaveCatalogModel(CatalogModel{
				ProviderID:    saved.ID,
				ModelID:       fmt.Sprintf("up-%02d-model-%03d", p, c),
				Capability:    CapChat,
				ContextLength: 32768,
			}); err != nil {
				t.Fatalf("写上游目录失败：%v", err)
			}
		}
		for m := 0; m < modelsPerProvider; m++ {
			name := fmt.Sprintf("perf-%02d-%03d", p, m)
			upstream := fmt.Sprintf("up-%02d-model-%03d", p, m)
			if _, err := s.SaveModel(Model{
				Name:         name,
				UpstreamName: upstream,
				Capability:   CapChat,
				Enabled:      true,
				Inputs: []ModelInput{{
					ProviderID:   saved.ID,
					UpstreamName: upstream,
				}},
			}); err != nil {
				t.Fatalf("建模型失败：%v", err)
			}
			names = append(names, name)
		}
	}
	for i := 0; i < logs; i++ {
		s.AddUsageLog(UsageLog{
			APIKeyName:   "perf",
			Model:        names[i%len(names)],
			ProviderName: "perf",
			Status:       200,
			CreatedAt:    time.Now().Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}
}

// BenchmarkResolveModel 量 /v1 每请求必经的模型解析（含 catalog 上下文补全）。
func BenchmarkResolveModel(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ResolveModel("perf-00-000"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListModels 量「列全部模型」的成本（/v1/models、门户模型页都调它）。
func BenchmarkListModels(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ListModels(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetProvider 量按 id 取单个渠道。
func BenchmarkGetProvider(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetProvider(1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCatalogContextIndex 单量「全表扫 catalog_models 建索引」本身。
func BenchmarkCatalogContextIndex(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.catalogContextIndex()
	}
}

// BenchmarkAPIKeyDailyUsage 量配额校验用的当日用量聚合。
func BenchmarkAPIKeyDailyUsage(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 4, 5, 20, 50000)
	if _, err := s.SaveAPIKey(APIKey{Name: "perf", Key: "sk-bench-perf", Enabled: true, Models: []string{"*"}}); err != nil {
		b.Fatalf("建密钥失败：%v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.APIKeyDailyUsage("perf"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStats 量面板概览的聚合（多路统计）。
func BenchmarkStats(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 4, 5, 20, 50000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Stats(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListUsageLogsRecent 量日志列表页的分页查询（5 万行库里取最新 100）。
func BenchmarkListUsageLogsRecent(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 4, 5, 20, 50000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ListUsageLogs(100, 0, "", ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListProvidersWithCounts 量面板渠道列表（含每渠道模型数聚合）。
func BenchmarkListProvidersWithCounts(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ListProviders(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetModelByName 量按名取单个模型（面板编辑、文档页用）。
func BenchmarkGetModelByName(b *testing.B) {
	s := newPerfStore(b)
	seedPerf(b, s, 20, 15, 250, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetModelByName("perf-00-000"); err != nil {
			b.Fatal(err)
		}
	}
}
