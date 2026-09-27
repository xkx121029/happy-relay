package relay

import (
	"sort"
	"time"

	"happy-relay/internal/store"
)

// 负载再平衡模式。多上游/多渠道默认顺序是「配置顺序 + 故障转移」，
// 这种顺序在长期运行后会偏科：第一个渠道被打爆、后面的渠道一直闲着。
// 再平衡依据历史调用数据动态调整顺序，把请求推给又快又稳的渠道。
const (
	// BalanceOff 关闭再平衡，完全按模型里配置的顺序尝试。
	BalanceOff = "off"
	// BalancePreferFast 按近期平均延迟升序，延迟低的渠道优先。
	BalancePreferFast = "prefer_fast"
	// BalancePreferHealthy 按近期可用率降序（可用率相同时延迟低者优先）。
	BalancePreferHealthy = "prefer_healthy"
)

// BalanceStat 是参与再平衡的单个目标的近期表现。
type BalanceStat struct {
	ProviderID int64   `json:"provider_id"`
	Total      int     `json:"total"`
	OK         int     `json:"ok"`
	Uptime     float64 `json:"uptime"` // 0-100，无样本时为 -1
	AvgLatency int     `json:"avg_latency_ms"`
}

// BalanceMode 是当前生效的再平衡模式。模式与阈值都在运行期可改，
// 因此这里用可变字段而非包级常量，方便面板热更新。
type BalanceMode string

// balanceScore 把一次调用结果折算成分数，用于比较目标优劣。
// 返回 (优先级, 延迟)，数值越小越优先。
//
// 设计取舍：
//   - 只有「劣质渠道」（可用率低于 bad 阈值）才被真正降权到末尾，而不是简单地
//     按可用率排序。100% 与 95% 可用率的渠道在绝大多数场景下没有区别，
//     但单纯按可用率排序会让它们每一次调用都相互颠倒，导致调度抖动、缓存命中率下降。
//   - 劣质渠道之间仍按延迟排序，避免完全失去区分度。
func balanceScore(st BalanceStat, bad float64) (int, int) {
	if st.Total == 0 || st.Uptime < 0 {
		// 没有历史样本（新渠道）：排在有数据的渠道之后、劣质渠道之前。
		// 这样既不会让新渠道凭空气抢占，也不会永远轮不到它。
		return 1, 0
	}
	if bad > 0 && st.Uptime < bad {
		return 2, st.AvgLatency
	}
	return 0, st.AvgLatency
}

// rebalanceTargets 按再平衡策略重排目标顺序。
// stats 是「渠道 ID -> 近期表现」，缺失的渠道视为无样本。
// 返回值保证是原切片的稳定重排：同一优先级内的相对顺序保持不变。
func rebalanceTargets(ts []Target, mode string, stats map[int64]BalanceStat, bad float64) []Target {
	if mode == "" || mode == BalanceOff || len(ts) <= 1 {
		return ts
	}
	out := append([]Target(nil), ts...)
	if mode == BalancePreferFast {
		// 纯延迟导向：没有样本的渠道视为延迟未知，放在最后。
		sort.SliceStable(out, func(i, j int) bool {
			si, sj := stats[out[i].Provider.ID], stats[out[j].Provider.ID]
			return latencyKey(si) < latencyKey(sj)
		})
		return out
	}
	// prefer_healthy（默认）
	sort.SliceStable(out, func(i, j int) bool {
		pi, li := balanceScore(stats[out[i].Provider.ID], bad)
		pj, lj := balanceScore(stats[out[j].Provider.ID], bad)
		if pi != pj {
			return pi < pj
		}
		return li < lj
	})
	return out
}

// latencyKey 把统计数据折算成可比较的延迟值；无样本给一个很大的兜底值。
func latencyKey(st BalanceStat) int {
	if st.Total == 0 || st.AvgLatency <= 0 {
		return 1 << 30
	}
	return st.AvgLatency
}

// rebalanceProviders 是 rebalanceTargets 的渠道级版本，供渠道级调度使用。
func rebalanceProviders(ps []store.Provider, mode string, stats map[int64]BalanceStat, bad float64) []store.Provider {
	if mode == "" || mode == BalanceOff || len(ps) <= 1 {
		return ps
	}
	ts := make([]Target, 0, len(ps))
	for _, p := range ps {
		ts = append(ts, Target{Provider: p})
	}
	re := rebalanceTargets(ts, mode, stats, bad)
	out := make([]store.Provider, 0, len(re))
	for _, t := range re {
		out = append(out, t.Provider)
	}
	return out
}

// balanceWindow 是再平衡参考的时间窗口。取 6 小时：
// 太短会被个别突发失败带偏，太长则无法反映渠道刚刚变差。
const balanceWindow = 6 * time.Hour
