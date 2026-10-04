// Command relay 启动自用的模型中转站服务。
//
// 该二进制把「渠道（上游）」与「模型（对外名）」解耦，
// 对外暴露一套标准的 v1 文本 / 图片 / 视频 / 向量接口，
// 并内嵌一个网页面板用于日常管理与调试。
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"happy-relay/internal/api"
	"happy-relay/internal/config"
	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[relay] ")

	cfg := config.Load()
	if err := cfg.Ensure(); err != nil {
		log.Fatalf("初始化数据目录失败：%v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败：%v", err)
	}
	defer st.Close()

	eng := relay.NewEngine(st, cfg.BreakerFails, cfg.BreakerCoolSec, cfg.TimeoutSec)
	// 登记自己的监听地址：渠道若被配成指向本服务，引擎会直接跳过，
	// 避免「调用自己 → 又路由回自己」的递归挂死。
	relay.SetSelfAddrs(cfg.Addr)
	srv := api.New(cfg, st, eng, api.StaticFS())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.StartBackground(ctx)
	// 飞书长连接桥：按设置启动/保持 WebSocket 事件通道（配置变更时热同步）。
	srv.FeishuSyncWSClient()

	handler := srv.Handler()
	// 明文 HTTP/2（h2c）：挂在支持 h2c 透传的公网代理/CDN 后面时，
	// 客户端到边缘是 HTTP/2，边缘到源站也能走 h2，避免协议降级；
	// 普通 HTTP/1.1 客户端完全不受影响。TLS 模式由 stdlib 自动协商，无需包装。
	if cfg.H2C && (cfg.TLSCert == "" || cfg.TLSKey == "") {
		handler = h2c.NewHandler(handler, &http2.Server{})
	}

	httpSrv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// 只限制读取头部的时间；不设置 WriteTimeout，
		// 否则会掐断长时间运行的 SSE 流式响应与视频轮询。
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := listenTCP(cfg.Addr)
	if err != nil {
		log.Fatalf("监听 %s 失败：%v", cfg.Addr, err)
	}
	scheme := "http"
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		scheme = "https"
	}

	go func() {
		log.Printf("模型中转站已启动：%s://%s（%s/%s，数据目录 %s）",
			scheme, displayAddr(cfg.Addr), runtime.GOOS, runtime.GOARCH, cfg.DataDir)
		var serveErr error
		if scheme == "https" {
			// TLS 模式：stdlib 自动协商 HTTP/2（公网高延迟链路下多路复用收益最大）。
			serveErr = httpSrv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
		} else {
			serveErr = httpSrv.Serve(ln)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("服务启动失败：%v", serveErr)
		}
	}()

	<-ctx.Done()
	log.Println("收到退出信号，正在关闭服务……")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭服务超时：%v", err)
	}
	log.Println("已退出")
}

// displayAddr 把 ":8787" 这类监听地址转成便于点击的展示形式。
func displayAddr(addr string) string {
	if addr == "" {
		return "0.0.0.0:8787"
	}
	if addr[0] == ':' {
		return "0.0.0.0" + addr
	}
	return addr
}

// listenTCP 创建显式配置 TCP keepalive 的监听。公网/NAT 环境下空闲连接容易被
// 中间设备静默掐断（表现为 SSE 流莫名断流、长轮询失联），主动 keepalive 既保活
// 长连接又能及时探测死链回收资源。
func listenTCP(addr string) (net.Listener, error) {
	lc := net.ListenConfig{
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     60 * time.Second, // 空闲 60s 后开始探测
			Interval: 15 * time.Second, // 每 15s 探测一次
			Count:    6,                // 连续 6 次无响应判定死链（约 90s）
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}
