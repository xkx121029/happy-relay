package api

// ---------- 飞书长连接（WebSocket）事件接入 ----------
//
// 优先使用的接入方式：通过官方 SDK（larkws）与飞书开放平台建立 WebSocket 全双工通道，
// 无需公网 IP/域名、无需配置回调 URL、无需自行处理加解密与验签。
// 前提：开发者后台「事件与回调 → 事件配置」订阅方式选择「使用长连接接收事件」。
//
// 生命周期：feishuSyncWSClient 按当前设置（enabled + transport + app_id/secret）
// 计算期望状态，与运行中客户端比对，不一致就关旧起新——面板保存设置后即时生效。
// 事件处理复用 processFeishuEvent，与 webhook 模式共享同一套
// 「提取文本 → chat 回环 / agent 工具循环 → IM API 回复」链路。

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type feishuWSManager struct {
	mu     sync.Mutex
	client *larkws.Client
	key    string // appID|secret|enabled，用于判断配置是否变化
	ready  bool   // 当前连接是否就绪（OnReady/OnDisconnected 维护）
	lastErr string
}

// feishuWSStatus 返回长连接当前状态，供面板配置向导展示。
func (s *Server) feishuWSStatus() (connected bool, lastErr string) {
	feishuWS.mu.Lock()
	defer feishuWS.mu.Unlock()
	return feishuWS.ready, feishuWS.lastErr
}

var feishuWS feishuWSManager

// FeishuSyncWSClient 按当前设置同步长连接客户端（服务启动与设置保存后调用）。
// 期望状态：bridge 开启 + transport=ws + 配了 App ID/Secret 才运行客户端。
func (s *Server) FeishuSyncWSClient() {
	appID := strings.TrimSpace(s.st.GetSetting(SettingBridgeAppID, ""))
	secret := s.st.GetSetting(SettingBridgeAppSecret, "")
	enabled := s.st.GetSetting(SettingBridgeEnabled, "") == "on"
	transport := s.st.GetSetting(SettingBridgeTransport, SettingBridgeTransportWS)

	wantKey := ""
	if enabled && transport == SettingBridgeTransportWS && appID != "" && secret != "" {
		wantKey = appID + "|" + secret
	}

	feishuWS.mu.Lock()
	defer feishuWS.mu.Unlock()
	if feishuWS.key == wantKey {
		return // 无变化（含两边都为空）
	}

	if feishuWS.client != nil {
		feishuWS.client.Close()
		log.Printf("[feishu-bridge] 长连接已关闭（配置变更）")
		feishuWS.client = nil
		feishuWS.key = ""
		feishuWS.ready = false
	}
	if wantKey == "" {
		return
	}

	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			s.processFeishuWSMessage(event)
			return nil // 错误已在本方法内记录，返回 nil 避免触发重推
		})
	cli := larkws.NewClient(appID, secret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
	)
	cli.SetOnReady(func() {
		feishuWS.mu.Lock()
		feishuWS.ready = true
		feishuWS.lastErr = ""
		feishuWS.mu.Unlock()
		log.Printf("[feishu-bridge] 飞书长连接已建立（App ID=%s）", appID)
	})
	cli.SetOnDisconnected(func() {
		feishuWS.mu.Lock()
		feishuWS.ready = false
		feishuWS.mu.Unlock()
		log.Printf("[feishu-bridge] 飞书长连接断开（SDK 将自动重连）")
	})
	cli.SetOnError(func(err error) {
		feishuWS.mu.Lock()
		feishuWS.ready = false
		feishuWS.lastErr = err.Error()
		feishuWS.mu.Unlock()
		log.Printf("[feishu-bridge] 飞书长连接错误：%v", err)
	})

	go func() {
		// Start 阻塞直至连接被 Close 或出错；断线由 SDK 内部自动重连。
		if err := cli.Start(context.Background()); err != nil {
			log.Printf("[feishu-bridge] 长连接客户端退出：%v", err)
		}
	}()
	feishuWS.client = cli
	feishuWS.key = wantKey
	log.Printf("[feishu-bridge] 飞书长连接启动中（App ID=%s，模式=%s）", appID, s.st.GetSetting(SettingBridgeMode, "chat"))
}

// processFeishuWSMessage 把 SDK 事件转成与 webhook 相同的 feishuEvent 后复用处理链路。
func (s *Server) processFeishuWSMessage(event *larkim.P2MessageReceiveV1) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[feishu-bridge] 长连接处理事件 panic：%v", rec)
		}
	}()

	ev := feishuEvent{}
	if event == nil || event.Event == nil {
		return
	}
	if snd := event.Event.Sender; snd != nil {
		ev.Sender.SenderType = wsDeref(snd.SenderType)
		if snd.SenderId != nil {
			ev.Sender.SenderID.OpenID = wsDeref(snd.SenderId.OpenId)
		}
	}
	if m := event.Event.Message; m != nil {
		ev.Message.MessageID = wsDeref(m.MessageId)
		ev.Message.ChatID = wsDeref(m.ChatId)
		ev.Message.ChatType = wsDeref(m.ChatType)
		ev.Message.MessageType = wsDeref(m.MessageType)
		if m.Content != nil {
			ev.Message.Content = json.RawMessage(*m.Content)
		}
		for _, men := range m.Mentions {
			if men == nil {
				continue
			}
			ev.Message.Mentions = append(ev.Message.Mentions, feishuMention{
				Key:  wsDeref(men.Key),
				Name: wsDeref(men.Name),
			})
		}
	}

	// 去重：长连接超时会触发重推，按 message_id 去重（加 ws: 前缀与 webhook 的 event_id 空间隔离）。
	if ev.Message.MessageID != "" && !feishuSeenEvent("ws:"+ev.Message.MessageID) {
		log.Printf("[feishu-bridge] 长连接重复消息，跳过：%s", ev.Message.MessageID)
		return
	}
	s.processFeishuEvent(ev)
}

// wsDeref 安全取字符串指针。
func wsDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
