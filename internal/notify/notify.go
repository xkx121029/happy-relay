// Package notify 提供统一的对外通知能力：同一个事件可以同时推送到
// 飞书机器人、企业微信机器人和邮件，各通道独立失败、互不阻塞。
package notify

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Message 是一条待发送的通知。Title 用于邮件主题与 Webhook 首行，
// Lines 是可选的正文条目（按行渲染），URL 可选（邮件里带一个「打开面板」按钮）。
type Message struct {
	Title string
	Lines []string
	URL   string
}

// Text 把消息渲染成纯文本，供 Webhook 与邮件正文共用。
func (m Message) Text() string {
	var b strings.Builder
	b.WriteString(m.Title)
	for _, l := range m.Lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(l)
	}
	if m.URL != "" {
		b.WriteString("\n")
		b.WriteString(m.URL)
	}
	return b.String()
}

// Sender 是单个通道的发送实现。Send 必须自己控制超时，不得阻塞调用方。
type Sender interface {
	// Name 返回通道名，用于日志区分。
	Name() string
	// Send 推送一条消息。
	Send(msg Message) error
}

// Notifier 按顺序把消息推给所有已配置的通道。
// 只有配置出错（例如邮件参数不全）才在构建期判定不可用；
// 运行期通道失败只记日志，绝不向上抛，避免通知故障影响主流程。
type Notifier struct {
	senders []Sender
	// onErr 在通道推送失败时回调，用于记录日志。可为 nil。
	onErr func(channel string, err error)
}

// New 创建一个通知器。onErr 可为 nil。
func New(onErr func(channel string, err error)) *Notifier {
	return &Notifier{onErr: onErr}
}

// Add 追加一个通道；s 为 nil 时忽略。
func (n *Notifier) Add(s Sender) {
	if n == nil || s == nil {
		return
	}
	n.senders = append(n.senders, s)
}

// Empty 表示当前没有任何可用通道。
func (n *Notifier) Empty() bool {
	return n == nil || len(n.senders) == 0
}

// Names 返回所有已配置通道的名字，供面板展示。
func (n *Notifier) Names() []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.senders))
	for _, s := range n.senders {
		out = append(out, s.Name())
	}
	return out
}

// Send 推送给所有通道。返回成功推送的通道数；单个通道失败不影响其他通道。
func (n *Notifier) Send(msg Message) int {
	if n == nil {
		return 0
	}
	okCount := 0
	for _, s := range n.senders {
		if err := s.Send(msg); err != nil {
			if n.onErr != nil {
				n.onErr(s.Name(), err)
			}
			continue
		}
		okCount++
	}
	return okCount
}

// ---------- Webhook 通道 ----------

// WebhookSender 兼容飞书 / 企业微信 / 钉钉 / Server酱 的群机器人 Webhook。
// 各家报文格式略有差异，这里按域名自动选择：格式不认识时退化成通用 JSON，
// 服务端至少不会因为格式不符整条丢弃。
type WebhookSender struct {
	URL    string
	client *http.Client
}

// NewWebhook 创建一个 Webhook 通道。url 为空时返回 nil。
func NewWebhook(url string) *WebhookSender {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	return &WebhookSender{URL: url, client: &http.Client{Timeout: 8 * time.Second}}
}

func (w *WebhookSender) Name() string { return "webhook" }

func (w *WebhookSender) Send(msg Message) error {
	payload := buildPayload(w.URL, msg)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 只读一小段用于排障，避免异常上游返回大体积响应。
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(head)))
	}
	// 飞书/企业微信即使 HTTP 200 也可能在 body 里报错，这里顺带识别。
	if s := strings.TrimSpace(string(head)); s != "" && !strings.Contains(s, `"code":0`) &&
		!strings.Contains(s, `"errcode":0`) && !strings.Contains(s, `"StatusCode":0`) {
		if strings.Contains(s, `"code":`) || strings.Contains(s, `"errcode":`) {
			return fmt.Errorf("上游返回错误: %s", s)
		}
	}
	return nil
}

// buildPayload 按 Webhook 域名选择机器人消息格式。
func buildPayload(webhook string, msg Message) map[string]any {
	text := msg.Text()
	lower := strings.ToLower(webhook)
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = "通知"
	}
	switch {
	case strings.Contains(lower, "open.feishu") || strings.Contains(lower, "feishu") ||
		strings.Contains(lower, "larksuite"):
		// 飞书自定义机器人卡片：比纯文本更醒目，长文也不会被折叠得太难看。
		return map[string]any{
			"msg_type": "interactive",
			"card": map[string]any{
				"header": map[string]any{
					"title":    map[string]any{"tag": "plain_text", "content": title},
					"template": "blue",
				},
				"elements": []map[string]any{
					{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": strings.Join(msg.Lines, "\n")}},
				},
			},
		}
	case strings.Contains(lower, "qyapi.weixin") || strings.Contains(lower, "weixin"):
		// 企业微信 markdown 消息
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{
				"content": "**" + title + "**\n" + strings.Join(msg.Lines, "\n"),
			},
		}
	case strings.Contains(lower, "dingtalk"):
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{
				"title": title,
				"text":  "### " + title + "\n\n" + strings.Join(msg.Lines, "\n\n"),
			},
		}
	case strings.Contains(lower, "sctapi.ftqq.com") || strings.Contains(lower, "pushplus"):
		// Server酱 / PushPlus：表单风格
		return map[string]any{"title": title, "desp": text}
	default:
		return map[string]any{"msg_type": "text", "content": map[string]any{"text": text}}
	}
}

// ---------- 邮件通道 ----------

// EmailConfig 是 SMTP 发信配置。端口 465 走隐式 TLS，其余端口优先 STARTTLS，
// 服务端不支持 STARTTLS 时按明文发送（内网自建邮件网关常见这种情况）。
type EmailConfig struct {
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	To       []string
	Insecure bool // 跳过证书校验，自签证书场景
}

// Valid 判断配置是否完整到可以发信。
func (c EmailConfig) Valid() bool {
	return strings.TrimSpace(c.Host) != "" && c.Port > 0 &&
		strings.TrimSpace(c.User) != "" && strings.TrimSpace(c.Pass) != "" &&
		len(c.To) > 0
}

// EmailSender 通过 SMTP 发送纯文本邮件。零依赖，只用标准库。
type EmailSender struct {
	cfg EmailConfig
}

// NewEmail 创建邮件通道。配置不完整或没有收件人时返回 nil。
func NewEmail(cfg EmailConfig) *EmailSender {
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.User = strings.TrimSpace(cfg.User)
	cfg.From = strings.TrimSpace(cfg.From)
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	if !cfg.Valid() {
		return nil
	}
	return &EmailSender{cfg: cfg}
}

func (e *EmailSender) Name() string { return "email" }

func (e *EmailSender) Send(msg Message) error {
	subject := strings.TrimSpace(msg.Title)
	if subject == "" {
		subject = "Happy Relay 通知"
	}
	body := e.buildBody(msg)
	raw := buildMIME(e.cfg.From, e.cfg.To, subject, body)

	addr := fmt.Sprintf("%s:%d", e.cfg.Host, e.cfg.Port)
	auth := smtp.PlainAuth("", e.cfg.User, e.cfg.Pass, e.cfg.Host)

	// 465 是隐式 TLS（SMTPS），必须直接建 TLS 连接，不能用裸连接 + STARTTLS。
	if e.cfg.Port == 465 {
		return e.sendImplicitTLS(addr, auth, raw)
	}
	return e.sendStartTLS(addr, auth, raw)
}

// sendImplicitTLS 处理 465 端口的隐式 TLS 连接。
func (e *EmailSender) sendImplicitTLS(addr string, auth smtp.Auth, raw []byte) error {
	tlsCfg := &tls.Config{
		ServerName:         e.cfg.Host,
		InsecureSkipVerify: e.cfg.Insecure,
	}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("TLS 连接失败: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	c, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if err = c.Auth(auth); err != nil {
		return fmt.Errorf("SMTP 认证失败: %w", err)
	}
	return writeMail(c, e.cfg.From, e.cfg.To, raw)
}

// sendStartTLS 处理 587/25 等端口的明文 + STARTTLS 升级。
func (e *EmailSender) sendStartTLS(addr string, auth smtp.Auth, raw []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{
			ServerName:         e.cfg.Host,
			InsecureSkipVerify: e.cfg.Insecure,
		}
		if err = c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
	}
	if ok, _ := c.Extension("AUTH"); ok {
		if err = c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	return writeMail(c, e.cfg.From, e.cfg.To, raw)
}

// writeMail 完成 MAIL/RCPT/DATA 三步投递。
func writeMail(c *smtp.Client, from string, to []string, raw []byte) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = wc.Write(raw); err != nil {
		_ = wc.Close()
		return err
	}
	if err = wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildBody 生成邮件正文：条目之间加空行，保留 URL 便于点开面板。
func (e *EmailSender) buildBody(msg Message) string {
	var b strings.Builder
	for _, l := range msg.Lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.WriteString(l)
		b.WriteString("\r\n\r\n")
	}
	if msg.URL != "" {
		b.WriteString("打开面板：\r\n")
		b.WriteString(msg.URL)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n-- \r\nHappy Relay · 模型中转站\r\n")
	return b.String()
}

// buildMIME 组装一封 UTF-8 纯文本邮件；主题用 RFC 2047 Base64 编码，
// 否则中文主题会被部分邮件客户端显示成乱码。
func buildMIME(from string, to []string, subject, body string) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + encodeSubject(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")
	// 正文按 76 字符折行，符合 RFC 2045 对 base64 的要求。
	enc := base64Std(body)
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\r\n")
	}
	return b.Bytes()
}
