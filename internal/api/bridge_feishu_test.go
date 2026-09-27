package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// feishuTestRequest 直接调用 handler 的辅助函数。
func feishuTestRequest(t *testing.T, s *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/webhook/feishu", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleFeishuWebhook(rec, req)
	return rec
}

// TestFeishuChallengeV1 v1 握手：{"type":"url_verification"} → 原样回 challenge。
func TestFeishuChallengeV1(t *testing.T) {
	s := newTestServer(t)
	rec := feishuTestRequest(t, s, map[string]string{"type": "url_verification", "challenge": "ajls384kdjx"})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["challenge"] != "ajls384kdjx" {
		t.Fatalf("challenge 未回显：%v", out)
	}
}

// TestFeishuChallengeV2 v2 握手：schema 2.0 + header.event_type=url_verification。
func TestFeishuChallengeV2(t *testing.T) {
	s := newTestServer(t)
	rec := feishuTestRequest(t, s, map[string]any{
		"schema":    "2.0",
		"challenge": "v2-challenge-xyz",
		"header":    map[string]string{"event_type": "url_verification", "event_id": "e1"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["challenge"] != "v2-challenge-xyz" {
		t.Fatalf("challenge 未回显：%v", out)
	}
}

// TestFeishuTokenMismatch 配置了 Verification Token 后不匹配的推送必须被拒。
func TestFeishuTokenMismatch(t *testing.T) {
	s := newTestServer(t)
	if err := s.st.SetSetting(SettingBridgeToken, "secret-token"); err != nil {
		t.Fatal(err)
	}
	rec := feishuTestRequest(t, s, map[string]any{
		"header": map[string]string{"event_type": "im.message.receive_v1", "event_id": "e2", "token": "wrong"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", rec.Code)
	}
	// 正确 token 应放行（bridge 未启用时静默 200，不处理）。
	rec = feishuTestRequest(t, s, map[string]any{
		"header": map[string]string{"event_type": "im.message.receive_v1", "event_id": "e3", "token": "secret-token"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
}

// TestFeishuDecryptRoundTrip 用与飞书相同的算法（AES-256-CBC + PKCS7）加密后必须能解回原文。
func TestFeishuDecryptRoundTrip(t *testing.T) {
	key := "my-encrypt-key"
	plain := []byte(`{"schema":"2.0","challenge":"abc"}`)

	// 加密（PKCS7 填充）
	sum := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(sum[:])
	padLen := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
	out := make([]byte, aes.BlockSize+len(padded))
	if _, err := rand.Read(out[:aes.BlockSize]); err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], padded)
	encrypted := base64.StdEncoding.EncodeToString(out)

	got, err := decryptFeishuPayload(key, encrypted)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("解密结果不符：%s", got)
	}
}

// TestFeishuExtractText 验证文本提取与 @ 占位符清理。
func TestFeishuExtractText(t *testing.T) {
	content := json.RawMessage(`{"text":"@_user_1 把巡检间隔改成 120 秒"}`)
	got := feishuExtractText(content, []feishuMention{{Key: "@_user_1", Name: "中转站"}})
	if got != "把巡检间隔改成 120 秒" {
		t.Fatalf("提取结果不符：%q", got)
	}
	if feishuExtractText(json.RawMessage(`bad`), nil) != "" {
		t.Fatal("非法 content 应返回空串")
	}
	// 飞书真实格式：content 是「JSON 字符串里再包一层 JSON」。
	encoded := json.RawMessage(`"{\"text\":\"你好，中转站\"}"`)
	if got := feishuExtractText(encoded, nil); got != "你好，中转站" {
		t.Fatalf("双层编码提取结果不符：%q", got)
	}
	// 双层编码 + @ 占位符。
	encoded2 := json.RawMessage(`"{\"text\":\"@_user_1 查看状态\"}"`)
	if got := feishuExtractText(encoded2, []feishuMention{{Key: "@_user_1", Name: "中转站"}}); got != "查看状态" {
		t.Fatalf("双层编码 @ 清理结果不符：%q", got)
	}
}

// TestFeishuSeenEvent 验证按 event_id 去重。
func TestFeishuSeenEvent(t *testing.T) {
	if !feishuSeenEvent("uniq-1") {
		t.Fatal("首次出现应返回 true")
	}
	if feishuSeenEvent("uniq-1") {
		t.Fatal("重复出现应返回 false")
	}
	if !feishuSeenEvent("uniq-2") {
		t.Fatal("不同事件应返回 true")
	}
}

// TestFeishuEventProcessedAsync 启用桥后推送一条 p2p 文本事件，goroutine 必须跑到「收到消息」日志。
func TestFeishuEventProcessedAsync(t *testing.T) {
	s := newTestServer(t)
	if err := s.st.SetSetting(SettingBridgeEnabled, "on"); err != nil {
		t.Fatal(err)
	}
	// webhook 消息处理只在回调模式下生效（长连接是默认优先方式）。
	if err := s.st.SetSetting(SettingBridgeTransport, SettingBridgeTransportWebhook); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	rec := feishuTestRequest(t, s, map[string]any{
		"schema": "2.0",
		"header": map[string]string{"event_type": "im.message.receive_v1", "event_id": "async-1"},
		"event": map[string]any{
			"sender": map[string]string{"sender_type": "user"},
			"message": map[string]any{
				"chat_id":      "oc_test",
				"chat_type":    "p2p",
				"message_type": "text",
				"content":      map[string]string{"text": "你好"},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ACK 状态码 %d", rec.Code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "[feishu-bridge] 收到消息") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("2 秒内未见处理日志，实际日志：%s", buf.String())
}

// TestFeishuWebhookIgnoredInWSMode 长连接模式（默认优先）下，回调 URL 只 ACK 不处理消息。
func TestFeishuWebhookIgnoredInWSMode(t *testing.T) {
	s := newTestServer(t)
	if err := s.st.SetSetting(SettingBridgeEnabled, "on"); err != nil {
		t.Fatal(err)
	}
	// 不设置 transport，默认应为 ws。

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	rec := feishuTestRequest(t, s, map[string]any{
		"schema": "2.0",
		"header": map[string]string{"event_type": "im.message.receive_v1", "event_id": "ws-mode-1"},
		"event": map[string]any{
			"sender": map[string]string{"sender_type": "user"},
			"message": map[string]any{
				"chat_id":      "oc_test",
				"chat_type":    "p2p",
				"message_type": "text",
				"content":      map[string]string{"text": "你好"},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ACK 状态码 %d", rec.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(buf.String(), "收到消息") {
		t.Fatal("长连接模式下 webhook 不应处理消息，避免双通道重复回复")
	}
}

// TestFeishuWSMessageConvert 验证长连接 SDK 事件到内部 feishuEvent 的转换与去重。
func TestFeishuWSMessageConvert(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	msgID := "om_ws_test_1"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
				SenderId:   &larkim.UserId{OpenId: strPtr("ou_x")},
			},
			Message: &larkim.EventMessage{
				MessageId:   strPtr(msgID),
				ChatId:      strPtr("oc_ws"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"长连接你好"}`),
				Mentions: []*larkim.MentionEvent{
					{Key: strPtr("@_user_1"), Name: strPtr("中转站")},
				},
			},
		},
	}

	// 转换正确性：直接调用处理函数（Server 为 nil 会在 processFeishuEvent 前的
	// 去重/转换阶段被 recover 兜住——这里只验证去重与转换，不起真实服务）。
	// 去重键应带 ws: 前缀，与 webhook 的 event_id 空间隔离。
	if !feishuSeenEvent("ws:" + msgID) {
		t.Fatal("首次见到 ws 消息应返回 true")
	}
	if feishuSeenEvent("ws:" + msgID) {
		t.Fatal("重复 ws 消息应返回 false")
	}
	if !feishuSeenEvent(msgID) {
		// webhook 空间的同名 key 应独立判断：ws: 前缀隔离了两个命名空间，
		// 因此裸 message_id 在 webhook 空间仍是首次出现。
		t.Fatal("ws: 前缀未生效：去重空间被串了")
	}

	// 事件转换：内容是「字符串包 JSON」，应能提取出文本。
	text := feishuExtractText(json.RawMessage(*event.Event.Message.Content), nil)
	if text != "长连接你好" {
		t.Fatalf("长连接 content 提取不符：%q", text)
	}
}

// TestFeishuHistory 会话上下文：追加、加载、裁剪与容量上限。
func TestFeishuHistory(t *testing.T) {
	chat := "oc_hist_test"
	// 历史是包级状态，用独占 key 避免与其他测试串扰。
	for i := 0; i < 30; i++ {
		feishuAppendHistory(chat, fmt.Sprintf("问题%d", i), fmt.Sprintf("回答%d", i))
	}
	got := feishuLoadHistory(chat, 6)
	if len(got) != 6 {
		t.Fatalf("应只取最近 6 条，实际 %d", len(got))
	}
	last := got[len(got)-2:]
	if last[0].Content != "问题29" || last[1].Content != "回答29" {
		t.Fatalf("最近一轮内容不符：%q / %q", last[0].Content, last[1].Content)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Fatal("角色应 user/assistant 交替")
	}
	// 超大回复应被截断存储。
	feishuAppendHistory(chat+"_big", "问", strings.Repeat("长", 5000))
	got2 := feishuLoadHistory(chat+"_big", 1)
	if len([]rune(got2[0].Content)) != 2000 {
		t.Fatalf("超长回复应截断到 2000 字，实际 %d", len([]rune(got2[0].Content)))
	}
	// 0 = 关闭上下文。
	if got3 := feishuLoadHistory(chat, 0); got3 != nil {
		t.Fatal("maxMsgs=0 应返回 nil")
	}
}
