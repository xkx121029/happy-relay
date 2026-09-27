package api

// ---------- 飞书深度整合：入站事件桥 ----------
//
// 飞书开放平台「事件订阅」把用户消息推到本端点，中继处理后经飞书 IM API 回复：
//   chat  模式：回环调用本站自己的 /v1/chat/completions（完整走一遍路由/熔断/日志统计）；
//   agent 模式：调用站内 agent（系统级主模型 + 工具循环），在飞书里直接指挥面板改配置。
//
// 安全：url_verification 握手原样回 challenge；配置了 Encrypt Key 则解密事件体，
// 配置了 Verification Token 则校验 token；按 event_id 去重防止飞书重推导致重复回复；
// 只处理 sender_type=user 的消息（忽略机器人自己的发言，避免自问自答死循环）。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"happy-relay/internal/relay"
)

// feishuMention 是群消息里的 @ 占位信息。
type feishuMention struct {
	Key  string `json:"key"` // 文本里的占位符，如 @_user_1
	Name string `json:"name"`
}

const feishuBaseURL = "https://open.feishu.cn"

// feishuEvent 是一条具体的事件负载（im.message.receive_v1）。
type feishuEvent struct {
	Sender struct {
		SenderType string `json:"sender_type"` // user | app
		SenderID   struct {
			OpenID string `json:"open_id"`
		} `json:"sender_id"`
	} `json:"sender"`
	Message struct {
		MessageID   string          `json:"message_id"`
		ChatID      string          `json:"chat_id"`
		ChatType    string          `json:"chat_type"` // p2p | group
		MessageType string          `json:"message_type"`
		Content     json.RawMessage `json:"content"`
		Mentions    []feishuMention `json:"mentions"`
	} `json:"message"`
}

// feishuEnvelope 是飞书事件推送的外层结构（v1 与 v2 兼容读取）。
type feishuEnvelope struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`      // v1 url_verification
	Challenge string `json:"challenge"` // v1 / v2 握手挑战值
	Token     string `json:"token"`     // v1 校验 token
	Encrypt   string `json:"encrypt"`   // 开启加密时的密文
	Header    struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Token     string `json:"token"`
	} `json:"header"`
	Event feishuEvent `json:"event"`
}

// handleFeishuWebhook 飞书事件订阅回调端点。飞书要求快速响应，事件处理全部异步。
func (s *Server) handleFeishuWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid_request_error", "仅支持 POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}

	var env feishuEnvelope
	payload := body
	// 开启了 Encrypt Key 时，飞书只推 {"encrypt":"..."}，先解密再解析。
	if s.st.GetSetting(SettingBridgeEncryptKey, "") != "" {
		var enc struct {
			Encrypt string `json:"encrypt"`
		}
		if json.Unmarshal(body, &enc) == nil && enc.Encrypt != "" {
			plain, derr := decryptFeishuPayload(s.st.GetSetting(SettingBridgeEncryptKey, ""), enc.Encrypt)
			if derr != nil {
				log.Printf("[feishu-bridge] 事件解密失败：%v", derr)
				writeErr(w, http.StatusBadRequest, "invalid_request_error", "事件解密失败")
				return
			}
			payload = plain
		}
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		log.Printf("[feishu-bridge] 事件解析失败：%v", err)
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "事件格式不合法")
		return
	}

	// 兼容 v1 / v2 的 url_verification 握手：原样回 challenge。
	eventType := env.Header.EventType
	if eventType == "" {
		eventType = env.Type
	}
	if eventType == "url_verification" {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": env.Challenge})
		return
	}

	// Verification Token 双保险：配置了就必须匹配。
	if want := s.st.GetSetting(SettingBridgeToken, ""); want != "" {
		got := env.Header.Token
		if got == "" {
			got = env.Token
		}
		if got != want {
			log.Printf("[feishu-bridge] verification token 校验失败")
			writeErr(w, http.StatusForbidden, "invalid_request_error", "token 校验失败")
			return
		}
	}

	// 长连接模式优先：transport=ws 时回调 URL 只保留握手应答，不再处理消息，
	// 避免开发者后台残留的旧回调配置与长连接双通道重复回复。
	if s.st.GetSetting(SettingBridgeTransport, SettingBridgeTransportWS) == SettingBridgeTransportWS {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}

	// 去重：飞书对同一事件可能重推。
	log.Printf("[feishu-bridge] 事件进入：type=%s id=%s enabled=%s", eventType, env.Header.EventID, s.st.GetSetting(SettingBridgeEnabled, ""))
	if env.Header.EventID != "" && !feishuSeenEvent(env.Header.EventID) {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}

	if s.st.GetSetting(SettingBridgeEnabled, "") != "on" {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}

	// 快速 ACK，事件在后台处理（回复走 IM API，不依赖本次 HTTP 响应）。
	writeJSON(w, http.StatusOK, map[string]string{})
	event := env.Event
	go s.processFeishuEvent(event)
}

// processFeishuEvent 处理一条飞书消息：提取文本 → 生成回复 → 推回飞书。
func (s *Server) processFeishuEvent(event feishuEvent) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[feishu-bridge] 处理事件 panic：%v", rec)
		}
	}()

	msg := event.Message
	// 只回文本消息；忽略机器人自己的发言，防止回环自答。
	if event.Sender.SenderType != "user" || msg.MessageType != "text" || msg.ChatID == "" {
		log.Printf("[feishu-bridge] 跳过消息：sender=%s type=%s chat_id=%q", event.Sender.SenderType, msg.MessageType, msg.ChatID)
		return
	}
	// 群聊必须 @机器人才响应；私聊（p2p）直接响应。
	if msg.ChatType == "group" && len(msg.Mentions) == 0 {
		log.Printf("[feishu-bridge] 跳过群消息：未 @ 机器人（chat_id=%s）", msg.ChatID)
		return
	}

	text := feishuExtractText(msg.Content, msg.Mentions)
	if text == "" {
		log.Printf("[feishu-bridge] 跳过消息：content 无有效文本（chat_id=%s）", msg.ChatID)
		return
	}
	log.Printf("[feishu-bridge] 收到消息（%s，chat_id=%s）：%s", msg.ChatType, msg.ChatID, truncateStr(text, 120))

	reply := s.feishuGenerateReply(text, msg.ChatID, msg.ChatType)
	feishuAppendHistory(msg.ChatID, text, reply)
	if err := s.feishuSendText(msg.ChatID, reply); err != nil {
		log.Printf("[feishu-bridge] 回复失败：%v", err)
	}
}

// ---------- 会话上下文（按 chat_id 记忆最近若干轮，30 分钟无消息自动清空） ----------

type feishuTurn struct {
	Role    string // user | assistant
	Content string
	At      time.Time
}

var (
	feishuHistMu   sync.Mutex
	feishuHist     = map[string][]feishuTurn{}
	feishuHistCap  = 40              // 每会话最多保留的消息条数（含双方）
	feishuHistTTL  = 30 * time.Minute
	feishuHistMaps = 256             // 会话数上限，超出时清理过期会话
)

// feishuHistoryTurns 读取面板配置的上下文轮数（条数 = 轮数×2，0=关闭）。
func (s *Server) feishuHistoryTurns() int {
	n, err := strconv.Atoi(strings.TrimSpace(s.st.GetSetting(SettingBridgeHistory, "")))
	if err != nil || n < 0 {
		return 10
	}
	if n > 25 {
		n = 25
	}
	return n
}

// feishuLoadHistory 取某会话最近的消息（跳过过期项），最多 maxMsgs 条。
func feishuLoadHistory(chatID string, maxMsgs int) []relay.ChatMessage {
	if maxMsgs <= 0 {
		return nil
	}
	feishuHistMu.Lock()
	defer feishuHistMu.Unlock()
	turns := feishuHist[chatID]
	now := time.Now()
	live := turns[:0]
	for _, t := range turns {
		if now.Sub(t.At) <= feishuHistTTL {
			live = append(live, t)
		}
	}
	feishuHist[chatID] = live
	if len(live) > maxMsgs {
		live = live[len(live)-maxMsgs:]
	}
	out := make([]relay.ChatMessage, 0, len(live))
	for _, t := range live {
		out = append(out, relay.ChatMessage{Role: t.Role, Content: t.Content})
	}
	return out
}

// feishuAppendHistory 追加一轮对话（双方各一条），超限裁剪最旧的。
func feishuAppendHistory(chatID, userText, reply string) {
	feishuHistMu.Lock()
	defer feishuHistMu.Unlock()
	now := time.Now()
	// 会话数过多时先清一遍过期会话，防止长期运行内存膨胀。
	if len(feishuHist) > feishuHistMaps {
		for id, turns := range feishuHist {
			if len(turns) == 0 || now.Sub(turns[len(turns)-1].At) > feishuHistTTL {
				delete(feishuHist, id)
			}
		}
	}
	trunc := func(s string) string {
		if len([]rune(s)) > 2000 {
			return string([]rune(s)[:2000])
		}
		return s
	}
	hist := append(feishuHist[chatID],
		feishuTurn{Role: "user", Content: trunc(userText), At: now},
		feishuTurn{Role: "assistant", Content: trunc(reply), At: now},
	)
	if len(hist) > feishuHistCap {
		hist = hist[len(hist)-feishuHistCap:]
	}
	feishuHist[chatID] = hist
}

// feishuExtractText 从 content JSON 里取纯文本并去掉 @ 占位符。
// 飞书实际推送的 content 是「JSON 字符串里再包一层 JSON」："content": "{\"text\":\"hi\"}"，
// 因此先按字符串解一次引号，再解析内层对象；也兼容直接推对象的情况。
func feishuExtractText(content json.RawMessage, mentions []feishuMention) string {
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &c); err != nil {
		var inner string
		if json.Unmarshal(content, &inner) != nil {
			return ""
		}
		if err := json.Unmarshal([]byte(inner), &c); err != nil {
			return ""
		}
	}
	text := c.Text
	for _, m := range mentions {
		if m.Key != "" {
			text = strings.ReplaceAll(text, m.Key, "")
		}
	}
	return strings.TrimSpace(text)
}

// feishuGenerateReply 按当前模式生成回复：agent（主模型工具循环）或 chat（回环本站 /v1）。
// chatID/chatType 作为会话上下文注入提示词，模型因此能回答「我的 chat_id 是多少」这类问题。
func (s *Server) feishuGenerateReply(text, chatID, chatType string) string {
	// 会话上下文附注：让模型知道消息来自哪个会话（告警推送配置需要它）。
	ctxNote := ""
	if chatID != "" {
		kind := "私聊"
		if chatType == "group" {
			kind = "群聊"
		}
		ctxNote = "\n\n[系统附注：本条消息来自飞书" + kind + "会话，chat_id=" + chatID +
			"。当用户询问会话 ID、告警推送目标等配置时，直接引用该值，不要说不知道。]"
	}

	mode := s.st.GetSetting(SettingBridgeMode, "chat")
	// 会话上下文：历史轮 + 本轮消息一起送给模型，让飞书里能连续追问。
	maxMsgs := s.feishuHistoryTurns() * 2
	hist := feishuLoadHistory(chatID, maxMsgs)
	if mode == "agent" {
		systemModel := s.st.GetSetting(SettingSystemModel, "")
		if systemModel == "" {
			return "尚未设置系统级 AI：请先在面板「设置 → 通用 → 系统级 AI 模型」里选择一个模型。"
		}
		m, ps, err := s.eng.ResolveAll(systemModel)
		if err != nil {
			return "系统级 AI 不可用：" + err.Error()
		}
		history := make([]relay.ChatMessage, 0, len(hist)+2)
		history = append(history, relay.ChatMessage{Role: "system", Content: agentSystemPrompt})
		history = append(history, hist...)
		history = append(history, relay.ChatMessage{Role: "user", Content: text + ctxNote})
		resp, loopErr := s.agentCore(context.Background(), systemModel, m, ps, history, nil)
		if loopErr != nil {
			return loopErr.Error()
		}
		return resp.Reply
	}

	// chat 模式：回环调用本站 /v1/chat/completions，复用全部路由、熔断与日志统计。
	return s.feishuLoopbackChat(text, ctxNote, hist)
}

// feishuLoopbackChat 用配置的秘钥回环调用本站 OpenAI 兼容端点。ctxNote 是会话上下文附注（可为空）。
func (s *Server) feishuLoopbackChat(text, ctxNote string, hist []relay.ChatMessage) string {
	model := s.st.GetSetting(SettingBridgeModel, "")
	if model == "" {
		return "尚未配置「飞书回复模型」：请到面板「设置 → 通知与告警 → 飞书整合」选择一个对外模型。"
	}
	key := s.feishuLoopbackKey()
	if key == "" {
		return "没有可用的对外秘钥：请到面板「设置 → 通知与告警 → 飞书整合」填写回环秘钥，或先创建一把启用中的秘钥。"
	}

	port := s.loopbackPort()
	messages := make([]map[string]string, 0, len(hist)+2)
	for _, m := range hist {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": text + ctxNote})
	if p := s.st.GetSetting(SettingBridgePrompt, ""); p != "" {
		messages = append([]map[string]string{{"role": "system", "content": p}}, messages...)
	}
	payload, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
	})

	client := &http.Client{Timeout: 180 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "构造回环请求失败：" + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return "回环调用失败：" + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("回环调用返回 %d：%s", resp.StatusCode, truncateStr(string(body), 300))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return "回环响应解析失败：" + truncateStr(string(body), 300)
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		content = "（模型返回了空回复）"
	}
	return content
}

// loopbackPort 从监听地址里解析出本机回环端口。
func (s *Server) loopbackPort() string {
	addr := ""
	if s.cfg != nil {
		addr = s.cfg.Addr
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "8787"
}

// feishuLoopbackKey 取回环秘钥：优先用显式配置，否则取第一把启用的对外秘钥。
func (s *Server) feishuLoopbackKey() string {
	if k := strings.TrimSpace(s.st.GetSetting(SettingBridgeKey, "")); k != "" {
		return k
	}
	keys, err := s.st.ListAPIKeys()
	if err != nil {
		return ""
	}
	for _, k := range keys {
		if k.Enabled && k.Key != "" {
			return k.Key
		}
	}
	return ""
}

// ---------- 飞书 IM API：取 token 与发消息 ----------

type feishuTokenCache struct {
	mu       sync.Mutex
	token    string
	expireAt time.Time
	appID    string
	secret   string
}

var feishuTokens feishuTokenCache

// feishuTenantToken 获取（带缓存）应用身份 token。
func (s *Server) feishuTenantToken() (string, error) {
	appID := s.st.GetSetting(SettingBridgeAppID, "")
	secret := s.st.GetSetting(SettingBridgeAppSecret, "")
	if appID == "" || secret == "" {
		return "", fmt.Errorf("未配置飞书应用 App ID / App Secret")
	}

	feishuTokens.mu.Lock()
	defer feishuTokens.mu.Unlock()
	now := time.Now()
	if feishuTokens.token != "" && feishuTokens.appID == appID && feishuTokens.secret == secret && now.Before(feishuTokens.expireAt) {
		return feishuTokens.token, nil
	}

	payload, _ := json.Marshal(map[string]string{"app_id": appID, "app_secret": secret})
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(feishuBaseURL+"/open-apis/auth/v3/tenant_access_token/internal", "application/json", bytes.NewReader(payload))
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
		return "", fmt.Errorf("token 响应解析失败：%w", err)
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("获取 tenant_access_token 失败（code=%d）：%s", out.Code, out.Msg)
	}
	feishuTokens.token = out.TenantAccessToken
	feishuTokens.appID = appID
	feishuTokens.secret = secret
	// 提前 5 分钟过期，避免临界点失效。
	feishuTokens.expireAt = now.Add(time.Duration(out.Expire-300) * time.Second)
	return feishuTokens.token, nil
}

// feishuSendText 向指定会话（chat_id）发送一条文本消息。
func (s *Server) feishuSendText(chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("缺少 chat_id")
	}
	token, err := s.feishuTenantToken()
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	payload, _ := json.Marshal(map[string]string{
		"receive_id": chatID,
		"msg_type":   "text",
		"content":    string(content),
	})
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, feishuBaseURL+"/open-apis/im/v1/messages?receive_id_type=chat_id", bytes.NewReader(payload))
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
	if resp.StatusCode != http.StatusOK || out.Code != 0 {
		return fmt.Errorf("发送消息失败（%d, code=%d）：%s", resp.StatusCode, out.Code, truncateStr(string(body), 300))
	}
	return nil
}

// ---------- 事件去重 ----------

var (
	feishuEventMu    sync.Mutex
	feishuEventSeen  = map[string]time.Time{}
	feishuEventTTL   = 6 * time.Hour
	feishuEventMaxN  = 4096
)

// feishuSeenEvent 返回 true 表示首次见到该事件（应处理）；重复推回 false。
func feishuSeenEvent(eventID string) bool {
	feishuEventMu.Lock()
	defer feishuEventMu.Unlock()
	now := time.Now()
	// 顺带清理过期项，防止长期运行内存膨胀。
	if len(feishuEventSeen) > feishuEventMaxN {
		for k, ts := range feishuEventSeen {
			if now.Sub(ts) > feishuEventTTL {
				delete(feishuEventSeen, k)
			}
		}
	}
	if ts, ok := feishuEventSeen[eventID]; ok && now.Sub(ts) < feishuEventTTL {
		return false
	}
	feishuEventSeen[eventID] = now
	return true
}

// ---------- 解密 ----------

// decryptFeishuPayload 用 Encrypt Key 解密飞书事件密文：AES-256-CBC（key=sha256(encryptKey)）+ PKCS7 去填充。
func decryptFeishuPayload(encryptKey, b64 string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败：%w", err)
	}
	if len(data) < aes.BlockSize {
		return nil, fmt.Errorf("密文过短")
	}
	sum := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	iv := data[:aes.BlockSize]
	data = data[aes.BlockSize:]
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("密文长度不是块大小的整数倍")
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(data, data)
	// PKCS7 去填充
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > aes.BlockSize || pad > len(data) {
		return nil, fmt.Errorf("填充非法")
	}
	return data[:len(data)-pad], nil
}

// ---------- 面板辅助端点 ----------

// handleBridgeFeishuStatus 配置向导状态总览：汇总整合链路各环节的当前配置与连接状态。
func (s *Server) handleBridgeFeishuStatus(w http.ResponseWriter, r *http.Request) {
	wsConnected, wsErr := s.feishuWSStatus()
	mode := s.st.GetSetting(SettingBridgeMode, "chat")
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        s.st.GetSetting(SettingBridgeEnabled, "") == "on",
		"transport":      s.st.GetSetting(SettingBridgeTransport, SettingBridgeTransportWS),
		"credentials_ok": strings.TrimSpace(s.st.GetSetting(SettingBridgeAppID, "")) != "" && s.st.GetSetting(SettingBridgeAppSecret, "") != "",
		"ws_connected":   wsConnected,
		"ws_last_error":  wsErr,
		"mode":           mode,
		"agent_ready":    s.st.GetSetting(SettingSystemModel, "") != "",
		"chat_ready":     s.st.GetSetting(SettingBridgeModel, "") != "",
		"notify_chat_id": strings.TrimSpace(s.st.GetSetting(SettingNotifyFeishuChatID, "")),
		"alert_enabled":  s.st.GetSetting(SettingAlertEnabled, "") == "on",
		"report_enabled": s.st.GetSetting(SettingReportEnabled, "") == "on",
		"report_weekday": strings.TrimSpace(s.st.GetSetting(SettingReportWeekday, "")),
		"report_hour":    strings.TrimSpace(s.st.GetSetting(SettingReportHour, "")),
	})
}

// handleBridgeFeishuTest 校验飞书应用凭据，可选发送一条测试消息。
// body: {chat_id?: string}
func (s *Server) handleBridgeFeishuTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatID string `json:"chat_id"`
	}
	_ = decodeBody(r, &req)

	if _, err := s.feishuTenantToken(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	result := map[string]any{"token_ok": true}
	if req.ChatID != "" {
		if err := s.feishuSendText(req.ChatID, "【Happy Relay】飞书整合测试消息，收到即代表双向链路打通 ✅"); err != nil {
			result["send_ok"] = false
			result["error"] = err.Error()
			writeJSON(w, http.StatusOK, result)
			return
		}
		result["send_ok"] = true
	}
	writeJSON(w, http.StatusOK, result)
}
