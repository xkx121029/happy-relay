package api

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- 秘钥限流 ----------

// keyLimiter 是每把秘钥的分钟级滑动窗口限流器。
type keyLimiter struct {
	mu   sync.Mutex
	hits map[int64][]time.Time
}

func newKeyLimiter() *keyLimiter {
	return &keyLimiter{hits: map[int64][]time.Time{}}
}

// allow 判断是否放行一次请求；limit <= 0 表示不限制。
func (l *keyLimiter) allow(id int64, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	cut := time.Now().Add(-time.Minute)
	kept := make([]time.Time, 0, len(l.hits[id])+1)
	for _, t := range l.hits[id] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.hits[id] = kept
		return false
	}
	l.hits[id] = append(kept, time.Now())
	return true
}

// ---------- 渠道健康 ----------

// handleProviderHealth 返回每个渠道的熔断状态。
func (s *Server) handleProviderHealth(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListProviders()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]relay.ProviderHealth, 0, len(list))
	for _, p := range list {
		out = append(out, s.eng.Health(p.ID))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProviderReset 手动清除某个渠道的熔断状态。
func (s *Server) handleProviderReset(w http.ResponseWriter, r *http.Request) {
	s.eng.ResetHealth(pathID(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProviderResetAll 清除全部渠道的熔断状态。
func (s *Server) handleProviderResetAll(w http.ResponseWriter, r *http.Request) {
	s.eng.ResetHealth(0)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 用量报表 ----------

// handleStats 按模型 / 渠道 / 日期返回用量聚合，供面板做报表。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		fmt.Sscanf(v, "%d", &days)
	}
	if days <= 0 || days > 90 {
		days = 7
	}
	byModel, err := s.st.ModelStats(days)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	byProvider, err := s.st.ProviderStats(days)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	daily, _ := s.st.DailyCalls(days)
	writeJSON(w, http.StatusOK, map[string]any{
		"days":        days,
		"by_model":    byModel,
		"by_provider": byProvider,
		"by_day":      daily,
	})
}

// ---------- Prometheus 指标 ----------

// handleMetrics 以 Prometheus 文本格式导出调用统计与运行时指标。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.MetricRows()
	if err != nil {
		writeStoreErr(w, err)
		return
	}

	var b strings.Builder
	b.WriteString("# HELP relay_requests_total 累计调用次数（按能力 / 模型 / 状态）\n")
	b.WriteString("# TYPE relay_requests_total counter\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "relay_requests_total{capability=%q,model=%q,status=%q} %d\n",
			row.Capability, row.Model, row.Status, row.Calls)
	}

	b.WriteString("# HELP relay_request_duration_ms_avg 平均耗时（毫秒）\n")
	b.WriteString("# TYPE relay_request_duration_ms_avg gauge\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "relay_request_duration_ms_avg{capability=%q,model=%q} %.2f\n",
			row.Capability, row.Model, row.AvgLatency)
	}

	b.WriteString("# HELP relay_goroutines 当前 goroutine 数量\n")
	b.WriteString("# TYPE relay_goroutines gauge\n")
	fmt.Fprintf(&b, "relay_goroutines %d\n", runtime.NumGoroutine())

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	b.WriteString("# HELP relay_memory_bytes 已分配的内存字节数\n")
	b.WriteString("# TYPE relay_memory_bytes gauge\n")
	fmt.Fprintf(&b, "relay_memory_bytes %d\n", ms.Alloc)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// ---------- 配置备份 ----------

// handleBackupExport 导出全部配置为 JSON 文件。
func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	backup, err := s.st.ExportAll()
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="happy-relay-backup.json"`)
	writeJSON(w, http.StatusOK, backup)
}

// handleBackupImport 合并导入配置：同名更新，不删除已有数据。
func (s *Server) handleBackupImport(w http.ResponseWriter, r *http.Request) {
	var backup store.Backup
	if err := decodeBody(r, &backup); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "备份文件格式不正确："+err.Error())
		return
	}
	if backup.Version == 0 && len(backup.Providers) == 0 && len(backup.Models) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "备份文件里没有可导入的内容")
		return
	}
	summary, err := s.st.ImportAll(&backup)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
