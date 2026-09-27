package notify

// ---------- 飞书应用通道（IM API 推送） ----------
//
// 通过飞书自建应用的身份（tenant_access_token）把消息以交互卡片形式
// 推送到指定会话（chat_id）。复用中转站「飞书整合」已配置的 App ID/Secret，
// 无需额外创建群机器人；告警与日报共用本通道。
//
// chat_id 获取方式：把机器人拉进群（群信息里有 chat_id），
// 或私聊机器人发一条消息，服务日志 [feishu-bridge] 会打印 chat_id。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FeishuAppSender 是飞书应用通道。零值不可用，请用 NewFeishuApp 创建。
type FeishuAppSender struct {
	AppID     string
	AppSecret string
	ChatID    string
	BaseURL   string // 留空用 https://open.feishu.cn

	mu          sync.Mutex
	token       string
	tokenExpire time.Time
}

// NewFeishuApp 创建飞书应用通道；任一必填参数为空返回 nil（表示通道未配置）。
func NewFeishuApp(appID, appSecret, chatID string) *FeishuAppSender {
	appID, appSecret, chatID = strings.TrimSpace(appID), strings.TrimSpace(appSecret), strings.TrimSpace(chatID)
	if appID == "" || appSecret == "" || chatID == "" {
		return nil
	}
	return &FeishuAppSender{AppID: appID, AppSecret: appSecret, ChatID: chatID}
}

func (f *FeishuAppSender) Name() string { return "feishu-app" }

func (f *FeishuAppSender) base() string {
	if f.BaseURL != "" {
		return strings.TrimRight(f.BaseURL, "/")
	}
	return "https://open.feishu.cn"
}

// tenantToken 获取（带缓存）应用身份 token，提前 5 分钟过期。
func (f *FeishuAppSender) tenantToken() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && time.Now().Before(f.tokenExpire) {
		return f.token, nil
	}
	payload, _ := json.Marshal(map[string]string{"app_id": f.AppID, "app_secret": f.AppSecret})
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Post(f.base()+"/open-apis/auth/v3/tenant_access_token/internal", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("token 响应解析失败: %w", err)
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("获取 tenant_access_token 失败（code=%d）：%s", out.Code, out.Msg)
	}
	f.token = out.TenantAccessToken
	f.tokenExpire = time.Now().Add(time.Duration(out.Expire-300) * time.Second)
	return f.token, nil
}

// feishuCard 组装与群机器人同风格的交互卡片。
func feishuCard(msg Message) map[string]any {
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = "通知"
	}
	template := "blue"
	if strings.Contains(title, "失败") || strings.Contains(title, "不可用") ||
		strings.Contains(title, "告警") || strings.Contains(title, "异常") {
		template = "red"
	}
	elements := []map[string]any{
		{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": strings.Join(msg.Lines, "\n")}},
	}
	if msg.URL != "" {
		elements = append(elements, map[string]any{
			"tag": "action",
			"actions": []map[string]any{
				{
					"tag": "button",
					"text": map[string]any{"tag": "plain_text", "content": "打开面板"},
					"type": "primary",
					"url":  msg.URL,
				},
			},
		})
	}
	return map[string]any{
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": title},
			"template": template,
		},
		"elements": elements,
	}
}

// Send 推送一条卡片消息到配置的会话。
func (f *FeishuAppSender) Send(msg Message) error {
	token, err := f.tenantToken()
	if err != nil {
		return err
	}
	content, err := json.Marshal(feishuCard(msg))
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{
		"receive_id": f.ChatID,
		"msg_type":   "interactive",
		"content":    string(content),
	})
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodPost, f.base()+"/open-apis/im/v1/messages?receive_id_type=chat_id", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.Code != 0 {
		head := string(body)
		if len(head) > 300 {
			head = head[:300]
		}
		return fmt.Errorf("发送卡片失败（HTTP %d, code=%d）：%s", resp.StatusCode, out.Code, strings.TrimSpace(head))
	}
	return nil
}
