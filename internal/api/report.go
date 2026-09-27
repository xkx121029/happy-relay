package api

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"happy-relay/internal/notify"
)

// StartReport 启动日报循环；由 StartBackground 调用。
func (s *Server) StartReport(ctx context.Context) {
	go s.reportLoop(ctx)
}

// reportLoop 每天在配置的时刻（本地时间）推送一次用量日报。
// 用「日期戳」而不是「距离上次多久」来判定，避免进程重启导致当天重复推送，
// 也避免重启后错过当天的推送。
func (s *Server) reportLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(60 * time.Second):
		}
		o := s.options()
		if !o.ReportEnabled {
			continue
		}
		now := time.Now()
		if now.Hour() != o.ReportHour {
			continue
		}
		// 推送星期过滤：留空=每天；0-6=周日..周六（如 1=每周一）。
		if wd := strings.TrimSpace(s.st.GetSetting(SettingReportWeekday, "")); wd != "" {
			if wd != fmt.Sprint(int(now.Weekday())) {
				continue
			}
		}
		day := now.Format("2006-01-02")
		if s.st.GetSetting(SettingReportLastDay, "") == day {
			continue // 今天已经推过了
		}
		// 先占位再推送：推送可能耗时数秒，期间下一轮循环不该重复触发。
		if err := s.st.SetSetting(SettingReportLastDay, day); err != nil {
			log.Printf("[report] 记录推送日期失败: %v", err)
			continue
		}
		s.sendDailyReport()
	}
}

// sendDailyReport 汇总最近 24 小时的数据并推送。
// 即便通道没配（notifier 为空），依然会检查异常预警阈值，
// 因为预警本身可能触发其他通道；没通道时 notifyAlert 内部会静默返回。
func (s *Server) sendDailyReport() {
	o := s.options()
	models, err := s.st.ModelStats(1)
	if err != nil {
		log.Printf("[report] 读取模型统计失败: %v", err)
		return
	}
	providers, err := s.st.ProviderStats(1)
	if err != nil {
		log.Printf("[report] 读取渠道统计失败: %v", err)
		return
	}

	var total, failed int64
	for _, m := range models {
		total += m.Calls
		failed += m.Failed
	}
	lines := []string{
		fmt.Sprintf("统计窗口：%s", time.Now().AddDate(0, 0, -1).Format("2006-01-02")+" 至 "+time.Now().Format("2006-01-02")),
		fmt.Sprintf("调用总数：%d", total),
		fmt.Sprintf("失败次数：%d（失败率 %s）", failed, rateText(failed, total)),
	}

	// Top 模型（最多 5 个），失败非零的标注出来。
	if len(models) > 0 {
		lines = append(lines, "", "▎模型用量 Top")
		for i, m := range models {
			if i >= 5 {
				break
			}
			seg := fmt.Sprintf("%d. %s — %d 次", i+1, m.Model, m.Calls)
			if m.Failed > 0 {
				seg += fmt.Sprintf("，失败 %d 次", m.Failed)
			}
			if m.AvgLatency > 0 {
				seg += fmt.Sprintf("，平均 %.0fms", m.AvgLatency)
			}
			lines = append(lines, seg)
		}
	}

	// 渠道表现：把可用率低于阈值的渠道单列，这是「该换渠道了」的信号。
	if len(providers) > 0 {
		lines = append(lines, "", "▎渠道表现")
		bad := make([]string, 0, 4)
		for i, p := range providers {
			if i >= 5 {
				break
			}
			seg := fmt.Sprintf("· %s — %d 次", p.Provider, p.Calls)
			if p.Failed > 0 {
				seg += fmt.Sprintf("，失败 %d 次（%s）", p.Failed, rateText(p.Failed, p.Calls))
			}
			lines = append(lines, seg)
			if o.ReportFailRate > 0 && p.Calls >= 20 {
				if pct := float64(p.Failed) / float64(p.Calls) * 100; pct >= float64(o.ReportFailRate) {
					bad = append(bad, fmt.Sprintf("· %s 失败率 %s", p.Provider, rateText(p.Failed, p.Calls)))
				}
			}
		}
		if len(bad) > 0 {
			lines = append(lines, "", "⚠️ 异常渠道（超过预警线 "+fmt.Sprintf("%d%%", o.ReportFailRate)+"）")
			lines = append(lines, bad...)
		}
	}

	// 巡检可用率：把「真实调用成功但平台端已经半死」的渠道也带出来。
	if up, err := s.st.ProviderUptime(24); err == nil && len(up) > 0 {
		byID := map[int64]string{}
		if ps, err := s.st.ListProviders(); err == nil {
			for _, p := range ps {
				byID[p.ID] = p.Name
			}
		}
		var segs []string
		for _, u := range up {
			name := byID[u.ProviderID]
			if name == "" || u.Total == 0 {
				continue
			}
			segs = append(segs, fmt.Sprintf("· %s 可用率 %s（%d 次探测）", name, pct(u.Uptime), u.Total))
		}
		if len(segs) > 0 {
			lines = append(lines, "", "▎巡检可用率（近 24 小时）")
			lines = append(lines, segs...)
		}
	}

	// 秘钥到期提醒。
	if o.ReportKeyDays > 0 {
		if expiring, err := s.st.ExpiringAPIKeys(o.ReportKeyDays); err == nil && len(expiring) > 0 {
			lines = append(lines, "", "⚠️ 即将到期的秘钥")
			for _, k := range expiring {
				lines = append(lines, fmt.Sprintf("· %s — 到期 %s", k.Name, k.ExpiresAt))
			}
		}
	}

	if len(lines) == 0 {
		return
	}
	s.notifier().Send(notify.Message{
		Title: "【Happy Relay】昨日用量日报",
		Lines: lines,
		URL:   s.panelURL(),
	})
}

// rateText 把失败次数折算成百分比文本；分母为 0 时显示「--」。
func rateText(failed, total int64) string {
	if total <= 0 {
		return "--"
	}
	return fmt.Sprintf("%.1f%%", float64(failed)/float64(total)*100)
}
