package api

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"happy-relay/internal/notify"
)

// alertGate 按 scope（通常是模型名）统计近期失败次数，达到阈值才允许推送，
// 避免偶发抖动触发告警轰炸。默认 5 分钟滑动窗口。
type alertGate struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newAlertGate() *alertGate { return &alertGate{hits: make(map[string][]time.Time)} }

// hit 记录一次失败并返回窗口内的累计次数（含本次）。
func (g *alertGate) hit(scope string, window time.Duration) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	cut := now.Add(-window)
	hs := g.hits[scope]
	kept := hs[:0]
	for _, t := range hs {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	g.hits[scope] = kept
	return len(kept)
}

// alertEnabled 判断告警是否开启：环境变量配置了 Webhook、或面板里配了
// Webhook / 邮件任一通道，即视为开启。
func (s *Server) alertEnabled() bool {
	if !s.options().AlertEnabled {
		return false
	}
	return !s.notifier().Empty()
}

// alertWebhook 返回当前生效的告警 Webhook：面板设置优先，其次环境变量。
func (s *Server) alertWebhook() string {
	wh := strings.TrimSpace(s.st.GetSetting(SettingAlertWebhook, ""))
	if wh != "" {
		return wh
	}
	return strings.TrimSpace(s.cfg.AlertWebhook)
}

// notifier 按当前设置组装一个通知器（Webhook + 邮件）。
// 每次调用都重新读取设置，这样面板改动立即生效，无需重启；
// 组装本身开销极小（就是几个结构体），不值得为它加缓存。
func (s *Server) notifier() *notify.Notifier {
	o := s.options()
	n := notify.New(func(channel string, err error) {
		log.Printf("[notify] %s 通道推送失败: %v", channel, err)
	})
	if wh := s.alertWebhook(); wh != "" {
		n.Add(notify.NewWebhook(wh))
	}
	// 飞书应用通道：复用「飞书整合」的应用凭据，把告警/日报以卡片推到指定会话。
	// 不要求事件接入开启——发 IM 消息只需要 App ID/Secret 与目标 chat_id。
	if fs := notify.NewFeishuApp(
		s.st.GetSetting(SettingBridgeAppID, ""),
		s.st.GetSetting(SettingBridgeAppSecret, ""),
		s.st.GetSetting(SettingNotifyFeishuChatID, ""),
	); fs != nil {
		n.Add(fs)
	}
	if o.EmailEnabled {
		n.Add(notify.NewEmail(notify.EmailConfig{
			Host:     o.EmailHost,
			Port:     o.EmailPort,
			User:     o.EmailUser,
			Pass:     s.st.GetSetting(SettingEmailPass, ""),
			From:     o.EmailFrom,
			To:       splitEmails(o.EmailTo),
			Insecure: o.EmailInsecure,
		}))
	}
	return n
}

// notifyAlert 向所有已配置通道推送一条告警。
// scope 用于去抖：仅在近 5 分钟内该 scope 失败次数达到 alert_min_fails 时才推送，
// 避免单次抖动刷屏。未配置或关闭时静默返回；推送失败仅记日志，绝不阻塞主流程。
func (s *Server) notifyAlert(scope, title, detail string) {
	if !s.alertEnabled() {
		return
	}
	minFails := s.options().AlertMinFails
	if minFails < 1 {
		minFails = 1
	}
	if minFails > 1 && s.ag.hit(scope, 5*time.Minute) < minFails {
		return
	}
	n := s.notifier()
	if n.Empty() {
		return
	}
	n.Send(notify.Message{
		Title: title,
		Lines: splitLines(detail),
		URL:   s.panelURL(),
	})
}

// sendDirectAlert 绕过去抖闸门直接推送一条通知，用于低频但需要立刻知道的事件
// （如自动备份失败）。未配置通道或总开关关闭时静默返回。
func (s *Server) sendDirectAlert(title, detail string) {
	if !s.alertEnabled() {
		return
	}
	n := s.notifier()
	if n.Empty() {
		return
	}
	n.Send(notify.Message{
		Title: title,
		Lines: splitLines(detail),
		URL:   s.panelURL(),
	})
}

// SendTestNotification 是面板「发送测试通知」按钮的后端实现。
// 用一次真实推送验证配置是否正确，比让用户等到下次真出事更靠谱。
func (s *Server) SendTestNotification(ctx context.Context) (int, []string, error) {
	n := s.notifier()
	if n.Empty() {
		return 0, nil, errNoChannel
	}
	type result struct {
		ok int
	}
	done := make(chan result, 1)
	go func() {
		ok := n.Send(notify.Message{
			Title: "【Happy Relay】测试通知",
			Lines: []string{
				"这是一条测试消息，收到即说明通知通道配置正确。",
				"发送时间：" + time.Now().Format("2006-01-02 15:04:05"),
			},
			URL: s.panelURL(),
		})
		done <- result{ok: ok}
	}()
	select {
	case <-ctx.Done():
		return 0, n.Names(), ctx.Err()
	case r := <-done:
		return r.ok, n.Names(), nil
	}
}

// panelURL 返回面板访问地址，用于通知里带一个可直接点开的链接。
func (s *Server) panelURL() string {
	u := strings.TrimSpace(s.st.GetSetting(SettingPublicURL, ""))
	if u != "" {
		return u
	}
	return ""
}

// splitLines 把多行文本拆成条目；同时兼容已经用 \n 拼好的 detail。
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// splitEmails 解析逗号 / 分号 / 空白分隔的收件人列表。
func splitEmails(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
