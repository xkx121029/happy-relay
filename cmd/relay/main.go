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
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

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

	eng := relay.NewEngine(st)
	srv := api.New(cfg, st, eng, api.StaticFS())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.StartBackground(ctx)

	httpSrv := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Handler(),
		// 只限制读取头部的时间；不设置 WriteTimeout，
		// 否则会掐断长时间运行的 SSE 流式响应与视频轮询。
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("模型中转站已启动：http://%s（%s/%s，数据目录 %s）",
			displayAddr(cfg.Addr), runtime.GOOS, runtime.GOARCH, cfg.DataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务启动失败：%v", err)
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