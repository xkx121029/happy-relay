package relay

import (
	"sync"
	"time"

	"happy-relay/internal/store"
)

// breaker 记录单个渠道的连续失败次数与熔断状态。
type breaker struct {
	fails    int
	openTill time.Time
	lastErr  string
}

// breakerGuard 实现渠道级熔断：连续失败达到阈值后，
// 在冷却时间内跳过该渠道，冷却结束后放行一次试探（半开）。
type breakerGuard struct {
	mu        sync.Mutex
	items     map[int64]*breaker
	threshold int
	cooldown  time.Duration
}

func newBreakerGuard(threshold, cooldownSec int) *breakerGuard {
	if cooldownSec <= 0 {
		cooldownSec = 60
	}
	return &breakerGuard{
		items:     map[int64]*breaker{},
		threshold: threshold,
		cooldown:  time.Duration(cooldownSec) * time.Second,
	}
}

// enabled 表示熔断是否开启（阈值 <= 0 视为关闭）。
func (g *breakerGuard) enabled() bool {
	return g != nil && g.threshold > 0
}

// allow 判断该渠道当前是否可用；冷却到期后会放行，交给调用结果决定是否重新熔断。
func (g *breakerGuard) allow(id int64) bool {
	if !g.enabled() {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.items[id]
	if b == nil || b.openTill.IsZero() {
		return true
	}
	return time.Now().After(b.openTill)
}

// report 记录一次渠道调用的最终结果。
func (g *breakerGuard) report(id int64, err error) {
	if !g.enabled() {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.items[id]
	if b == nil {
		b = &breaker{}
		g.items[id] = b
	}
	if err == nil {
		b.fails = 0
		b.openTill = time.Time{}
		b.lastErr = ""
		return
	}
	b.fails++
	b.lastErr = err.Error()
	if b.fails >= g.threshold {
		b.openTill = time.Now().Add(g.cooldown)
	}
}

// snapshot 返回某个渠道的健康快照。
func (g *breakerGuard) snapshot(id int64) (open bool, fails int, remainSec int, lastErr string) {
	if !g.enabled() {
		return false, 0, 0, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.items[id]
	if b == nil {
		return false, 0, 0, ""
	}
	if !b.openTill.IsZero() && time.Now().Before(b.openTill) {
		return true, b.fails, int(time.Until(b.openTill).Seconds()) + 1, b.lastErr
	}
	return false, b.fails, 0, b.lastErr
}

// reset 清除熔断状态；id <= 0 时清除全部。
func (g *breakerGuard) reset(id int64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if id <= 0 {
		g.items = map[int64]*breaker{}
		return
	}
	delete(g.items, id)
}

// ProviderHealth 是渠道当前的健康状态，供面板展示。
type ProviderHealth struct {
	ProviderID int64  `json:"provider_id"`
	Open       bool   `json:"open"`
	Fails      int    `json:"fails"`
	RemainSec  int    `json:"remain_sec"`
	LastError  string `json:"last_error,omitempty"`
}

// Health 返回指定渠道的健康状态。
func (e *Engine) Health(id int64) ProviderHealth {
	open, fails, remain, lastErr := e.breaker.snapshot(id)
	return ProviderHealth{ProviderID: id, Open: open, Fails: fails, RemainSec: remain, LastError: lastErr}
}

// ResetHealth 清除熔断状态；id <= 0 时清除全部。
func (e *Engine) ResetHealth(id int64) { e.breaker.reset(id) }

// filterOpen 过滤掉处于熔断冷却中的渠道。
func (e *Engine) filterOpen(ps []store.Provider) []store.Provider {
	if !e.breaker.enabled() {
		return ps
	}
	out := make([]store.Provider, 0, len(ps))
	for _, p := range ps {
		if e.breaker.allow(p.ID) {
			out = append(out, p)
		}
	}
	return out
}

// orderForTry 先按策略排序，再做负载再平衡，最后剔除熔断中的渠道；
// 若全部处于熔断状态则忽略熔断，避免出现完全不可用的死局。
func (e *Engine) orderForTry(m store.Model, ps []store.Provider) []store.Provider {
	ordered := e.orderProviders(m, ps)
	mode, bad, stats := e.balance()
	ordered = rebalanceProviders(ordered, mode, stats, float64(bad))
	if filtered := e.filterOpen(ordered); len(filtered) > 0 {
		return filtered
	}
	return ordered
}
