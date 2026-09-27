package api

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// StartHealth 启动后台健康巡检；由 StartBackground 调用。
func (s *Server) StartHealth(ctx context.Context) {
	// 巡检关闭也起一个看守循环：用户可能稍后在面板上打开，
	// 循环每轮重新读设置，开了就自动开始干。
	go s.healthLoop(ctx, !s.options().HealthEnabled)
}

// healthLoop 周期性探测所有启用的渠道。
// 每轮重新读取间隔，面板调整后无需重启即可生效。
// idle 为真表示启动时巡检是关闭的：此时只等待、不探测，直到设置被打开。
func (s *Server) healthLoop(ctx context.Context, idle bool) {
	first := true
	for {
		interval := time.Duration(s.options().HealthInterval) * time.Second
		if interval < 30*time.Second {
			interval = 30 * time.Second
		}
		// 首轮给一个短暂延迟：进程刚起来时渠道可能还没配置好，
		// 立刻全量探测容易在启动日志里刷一堆假失败。
		wait := interval
		if first && !idle {
			first = false
			wait = 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if !s.options().HealthEnabled {
			continue
		}
		s.runHealthRound(ctx)
	}
}

// runHealthRound 对每个启用的渠道做一次探测并落库。
// 探测走的是各渠道自己的第一个入口地址，
// 与真实调用链路一致，这样才能发现「平台端已失效但域名还能连通」的问题。
func (s *Server) runHealthRound(ctx context.Context) {
	providers, err := s.st.ListProviders()
	if err != nil {
		log.Printf("[health] 读取渠道失败: %v", err)
		return
	}
	o := s.options()
	timeout := time.Duration(o.HealthTimeout) * time.Second
	if timeout < 3*time.Second {
		timeout = 3 * time.Second
	}
	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		// 没有密钥的渠道必然 401，探了也只是往库里灌噪音数据，
		// 反而会把「可用率」这个指标拉垮、误导再平衡。直接跳过。
		if len(p.APIKeys) == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		res := probeProviderHealth(probeCtx, p)
		cancel()

		h := store.HealthCheck{
			ProviderID: p.ID,
			OK:         res.ok,
			Status:     res.status,
			LatencyMS:  int(res.latencyMS),
			Error:      res.errText,
			CheckedAt:  store.NowString(),
		}
		if err := s.st.SaveHealthCheck(h); err != nil {
			log.Printf("[health] 写入巡检记录失败: %v", err)
		}
		s.handleHealthResult(p, res)
	}
	// 巡检完顺手裁剪历史，避免表无限增长。
	if keep := o.HealthKeep; keep > 0 {
		_ = s.st.TrimHealthChecks(keep)
	}
	// 刷新再平衡参考数据，让下一轮选路用上最新表现。
	s.refreshBalance()
}

// healthProbeResult 是一次渠道探测的结果。
type healthProbeResult struct {
	ok        bool
	status    int
	latencyMS int64
	errText   string
}

// probeProviderHealth 探测单个渠道：复用渠道连通性测试的逻辑，
// 但只取第一个入口地址，保证巡检开销可控（每渠道一次请求）。
func probeProviderHealth(ctx context.Context, p store.Provider) healthProbeResult {
	urls := p.BaseURLs
	if len(urls) == 0 {
		if def := relay.DefaultBaseURL(p.Kind); def != "" {
			urls = []string{def}
		}
	}
	if len(urls) == 0 {
		return healthProbeResult{errText: "未配置入口地址"}
	}
	key := ""
	if len(p.APIKeys) > 0 {
		key = strings.TrimSpace(p.APIKeys[0])
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r := probeUpstream(pctx, probeClient, p.Kind, urls[0], key, p.NoVersionPath)
	return healthProbeResult{
		ok:        r.OK,
		status:    r.Status,
		latencyMS: r.Latency,
		errText:   trimProbeDetail(r.Detail),
	}
}

// trimProbeDetail 把探测详情压成一句人话，用于巡检记录。
// 成功时不需要存「连通正常」这类噪音。
func trimProbeDetail(detail string) string {
	d := strings.TrimSpace(detail)
	if d == "" || d == "连通正常" {
		return ""
	}
	return d
}

// handleHealthResult 根据巡检结果做自动恢复与告警。
func (s *Server) handleHealthResult(p store.Provider, res healthProbeResult) {
	o := s.options()
	if res.ok {
		// 自动恢复：巡检通了且当前处于熔断中，说明平台已经活过来，
		// 主动清除熔断，不用等冷却时间走完，也不用人工点「重置」。
		if o.HealthRecover {
			if h := s.eng.Health(p.ID); h.Open || h.Fails > 0 {
				s.eng.ResetHealth(p.ID)
				log.Printf("[health] 渠道「%s」已恢复，自动清除熔断（此前连续失败 %d 次）", p.Name, h.Fails)
			}
		}
		return
	}
	// 判定为「连续失败」才告警：单次网络抖动由巡检记录与去抖窗口共同兜底。
	up, err := s.st.ProviderUptime(6)
	if err != nil {
		return
	}
	u, ok := up[p.ID]
	if !ok {
		return
	}
	// 连续 3 次失败才算渠道级故障，避免把偶发超时推成人人都会收到的告警。
	if u.ConsecutiveFails < 3 {
		return
	}
	s.notifyAlert("health:"+p.Name, "渠道持续不可用",
		fmt.Sprintf("渠道：%s\n连续失败：%d 次\n近 6 小时可用率：%s\n最近错误：%s",
			p.Name, u.ConsecutiveFails, pct(u.Uptime),
			firstNonEmptyAPI(res.errText, u.LastError, "未知")))
}

// refreshBalance 把近期巡检表现折算成再平衡输入，写回引擎。
func (s *Server) refreshBalance() {
	o := s.options()
	if o.BalanceMode == relay.BalanceOff {
		s.eng.SetBalance(relay.BalanceOff, o.BalanceBad, nil)
		return
	}
	uptime, err := s.st.ProviderUptime(6)
	if err != nil {
		return
	}
	stats := make(map[int64]relay.BalanceStat, len(uptime))
	for id, u := range uptime {
		stats[id] = relay.BalanceStat{
			ProviderID: id,
			Total:      int(u.Total),
			OK:         int(u.OKCount),
			Uptime:     u.Uptime,
			AvgLatency: int(u.AvgLatency),
		}
	}
	s.eng.SetBalance(o.BalanceMode, o.BalanceBad, stats)
}

// pct 把一个可用率数值渲染成百分比文本；无样本（<0）显示为「暂无数据」。
func pct(v float64) string {
	if v < 0 {
		return "暂无数据"
	}
	return fmt.Sprintf("%.1f%%", v)
}

// firstNonEmptyAPI 是 api 包内的「取第一个非空字符串」helper。
func firstNonEmptyAPI(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
