package api

import (
	"context"
	"log"
	"time"

	"happy-relay/internal/store"
)

// StartBackground 启动后台任务：视频任务轮询与日志裁剪。
func (s *Server) StartBackground(ctx context.Context) {
	go s.videoLoop(ctx)
	go s.logTrimLoop(ctx)
}

// videoLoop 周期性刷新未完成的视频任务状态。
// 每轮重新读取间隔，这样在面板上调整轮询频率后无需重启即可生效。
func (s *Server) videoLoop(ctx context.Context) {
	for {
		interval := time.Duration(s.options().VideoPollSec) * time.Second
		if interval < time.Second {
			interval = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			s.pollPendingVideos(ctx)
		}
	}
}

func (s *Server) pollPendingVideos(ctx context.Context) {
	tasks, err := s.st.ListPendingVideoTasks()
	if err != nil {
		log.Printf("视频任务轮询失败：%v", err)
		return
	}
	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		updated, err := s.eng.PollVideo(pollCtx, t)
		cancel()
		if err != nil {
			// 单次查询失败不改变任务状态，下个周期继续尝试。
			continue
		}
		if updated.Status == store.TaskSucceeded || updated.Status == store.TaskFailed {
			log.Printf("视频任务 %s 结束，状态 %s", updated.TaskID, updated.Status)
		}
	}
}

// logTrimLoop 按配置上限裁剪调用日志，避免数据库无限增长。
func (s *Server) logTrimLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.st.TrimUsageLogs(s.options().MaxLogRows)
		}
	}
}
