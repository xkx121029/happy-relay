package relay

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"happy-relay/internal/store"
)

// ---------- 上游健康画像（越用越聪明） ----------
//
// 每次真实调用都给「渠道 × 上游」记一笔：成功/失败、EWMA 延迟、连续失败数。
// 内存里实时更新，脏数据防抖落盘到 upstream_stats 表，重启不丢——
// 跑得越久，画像越准，路由就越懂得避开常挂的上游。

const (
	// statEWMA 平滑系数：新延迟占 30%，旧均值占 70%，兼顾灵敏度与抗抖动。
	statEWMA = 0.3
	// statDeprioritizeFails 连续失败达到该次数的上游，在尝试队列里排到尾部
	//（不剔除——熔断负责硬切断，画像只做软降权，避免误杀偶发失败的上游）。
	statDeprioritizeFails = 3
	// statFlushDelay 脏数据防抖落盘间隔。
	statFlushDelay = 5 * time.Second
)

// errText 安全取错误文本（nil 返回空串）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type statKey struct {
	providerID int64
	upstream   string
}

type upstreamStat struct {
	OKCount     int64
	FailCount   int64
	ConsecFails int
	AvgLatency  float64
	LastOKAt    string
	LastFailAt  string
	LastError   string
	dirty       bool
}

// statGuard 是画像的内存索引 + 落盘调度。
type statGuard struct {
	mu    sync.Mutex
	items map[statKey]*upstreamStat
	st    *store.Store
	timer *time.Timer // 防抖：脏数据聚齐后一次写入
}

func newStatGuard(st *store.Store) *statGuard {
	g := &statGuard{items: map[statKey]*upstreamStat{}, st: st}
	// 启动时把历史画像装回内存——这就是「积累」：跨重启继续生效。
	if list, err := st.ListUpstreamStats(); err == nil {
		for _, s := range list {
			g.items[statKey{s.ProviderID, s.UpstreamName}] = &upstreamStat{
				OKCount: s.OKCount, FailCount: s.FailCount, ConsecFails: s.ConsecFails,
				AvgLatency: s.AvgLatencyMS, LastOKAt: s.LastOKAt, LastFailAt: s.LastFailAt,
				LastError: s.LastError,
			}
		}
	}
	return g
}

// report 记录一次目标尝试的结果。ok=true 时 errMsg 被忽略。
func (g *statGuard) report(providerID int64, upstream string, ok bool, latencyMS int64, errMsg string) {
	if g == nil || providerID <= 0 || strings.TrimSpace(upstream) == "" {
		return
	}
	now := time.Now().Format(time.RFC3339)
	g.mu.Lock()
	defer g.mu.Unlock()
	k := statKey{providerID, upstream}
	s := g.items[k]
	if s == nil {
		s = &upstreamStat{}
		g.items[k] = s
	}
	if ok {
		s.OKCount++
		s.ConsecFails = 0
		s.LastOKAt = now
	} else {
		s.FailCount++
		s.ConsecFails++
		s.LastFailAt = now
		// 只留最近一条错误、截断长度，画像不是日志。
		s.LastError = errMsg
		if len(s.LastError) > 200 {
			s.LastError = s.LastError[:200]
		}
	}
	if latencyMS > 0 {
		lat := float64(latencyMS)
		if s.AvgLatency <= 0 {
			s.AvgLatency = lat
		} else {
			s.AvgLatency = s.AvgLatency*(1-statEWMA) + lat*statEWMA
		}
	}
	if !s.dirty {
		s.dirty = true
		g.scheduleFlushLocked()
	}
}

// scheduleFlushLocked 防抖落盘：有脏数据时 5 秒后统一写库，高频调用不会打爆 SQLite。
func (g *statGuard) scheduleFlushLocked() {
	if g.timer != nil {
		return
	}
	g.timer = time.AfterFunc(statFlushDelay, func() {
		g.mu.Lock()
		g.timer = nil
		dirty := make([]struct {
			k statKey
			s *upstreamStat
		}, 0, 8)
		for k, s := range g.items {
			if s.dirty {
				dirty = append(dirty, struct {
					k statKey
					s *upstreamStat
				}{k, s})
			}
		}
		// 先清标记再写库：写库期间新到的调用会重新标脏，不会丢更新。
		for _, d := range dirty {
			d.s.dirty = false
		}
		g.mu.Unlock()

		now := time.Now().Format(time.RFC3339)
		for _, d := range dirty {
			_ = g.st.SaveUpstreamStat(store.UpstreamStat{
				ProviderID: d.k.providerID, UpstreamName: d.k.upstream,
				OKCount: d.s.OKCount, FailCount: d.s.FailCount, ConsecFails: d.s.ConsecFails,
				AvgLatencyMS: d.s.AvgLatency, LastOKAt: d.s.LastOKAt, LastFailAt: d.s.LastFailAt,
				LastError: d.s.LastError, UpdatedAt: now,
			})
		}
	})
}

// deprioritize 把连续失败达到阈值的目标稳定地移到队尾：
// 原顺序里靠前的仍然尽量先试，只是「最近老挂的」让路。全部失败时顺序不变，不会死局。
func (g *statGuard) deprioritize(ts []Target) []Target {
	if g == nil || len(ts) <= 1 {
		return ts
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	head := make([]Target, 0, len(ts))
	tail := make([]Target, 0, len(ts))
	for _, t := range ts {
		s := g.items[statKey{t.Provider.ID, t.UpstreamName}]
		if s != nil && s.ConsecFails >= statDeprioritizeFails {
			tail = append(tail, t)
		} else {
			head = append(head, t)
		}
	}
	if len(head) == 0 || len(tail) == 0 {
		return ts
	}
	return append(head, tail...)
}

// Snapshot 返回某个目标的画像（面板展示用）。
func (g *statGuard) snapshot(providerID int64, upstream string) (UpstreamStatView, bool) {
	if g == nil {
		return UpstreamStatView{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.items[statKey{providerID, upstream}]
	if s == nil {
		return UpstreamStatView{}, false
	}
	return UpstreamStatView{
		OKCount: s.OKCount, FailCount: s.FailCount, ConsecFails: s.ConsecFails,
		AvgLatencyMS: s.AvgLatency, LastOKAt: s.LastOKAt, LastFailAt: s.LastFailAt,
		LastError: s.LastError,
	}, true
}

// UpstreamStatView 是内存画像的只读快照（不含持久化字段）。
type UpstreamStatView struct {
	OKCount      int64   `json:"ok_count"`
	FailCount    int64   `json:"fail_count"`
	ConsecFails  int     `json:"consec_fails"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	LastOKAt     string  `json:"last_ok_at,omitempty"`
	LastFailAt   string  `json:"last_fail_at,omitempty"`
	LastError    string  `json:"last_error,omitempty"`
}

// StatsSnapshot 导出引擎当前的全部画像（API 用）。
func (e *Engine) StatsSnapshot() map[string]UpstreamStatView {
	out := map[string]UpstreamStatView{}
	if e.stats == nil {
		return out
	}
	e.stats.mu.Lock()
	defer e.stats.mu.Unlock()
	for k, s := range e.stats.items {
		out[formatStatKey(k.providerID, k.upstream)] = UpstreamStatView{
			OKCount: s.OKCount, FailCount: s.FailCount, ConsecFails: s.ConsecFails,
			AvgLatencyMS: s.AvgLatency, LastOKAt: s.LastOKAt, LastFailAt: s.LastFailAt,
			LastError: s.LastError,
		}
	}
	return out
}

func formatStatKey(providerID int64, upstream string) string {
	return strconv.FormatInt(providerID, 10) + "::" + upstream
}
