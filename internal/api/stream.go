package api

import (
	"sync"
	"time"

	"happy-relay/internal/relay"
)

const (
	// smoothChunkRunes 是平滑输出时每段的字符数，越小越细腻。
	smoothChunkRunes = 4
	// maxSmoothLag 是允许的最大积压时长；超过则放弃限速直接下发，避免拖慢整体响应。
	// 600ms：积压多半秒就意味着模型实际速度远超设定速率，限速已无意义。
	maxSmoothLag = 600 * time.Millisecond
)

// smoothEmit 把上游一次吐出的长文本拆成小段，按目标速率匀速下发，
// 让客户端的流式观感更均匀。思考内容、工具调用与结束标记等元信息不会丢失。
func (s *Server) smoothEmit(emit relay.StreamFunc) relay.StreamFunc {
	opts := s.options()
	if emit == nil || !opts.StreamSmooth || opts.StreamRate <= 0 {
		return emit
	}
	rate := float64(opts.StreamRate)
	start := time.Now()
	sent := 0
	return func(d relay.StreamDelta) error {
		// 空正文的增量（结束标记、用量、纯工具调用）直接透传。
		if d.Content == "" {
			return emit(d)
		}
		runes := []rune(d.Content)
		for i := 0; i < len(runes); i += smoothChunkRunes {
			end := i + smoothChunkRunes
			if end > len(runes) {
				end = len(runes)
			}
			sent += end - i
			expected := time.Duration(float64(sent) / rate * float64(time.Second))
			if wait := expected - time.Since(start); wait > 0 {
				if wait > maxSmoothLag {
					// 上游一次性给了很长的内容，积压过多，放弃限速。
					start = time.Now().Add(-expected)
				} else {
					time.Sleep(wait)
				}
			}
			part := relay.StreamDelta{Content: string(runes[i:end])}
			if i == 0 {
				part.Role = d.Role
				part.Reasoning = d.Reasoning
			}
			if end >= len(runes) {
				part.ToolCalls = d.ToolCalls
				part.FinishReason = d.FinishReason
				part.Usage = d.Usage
			}
			if err := emit(part); err != nil {
				return err
			}
		}
		return nil
	}
}

// startHeartbeat 周期发送 SSE 注释行，防止中间代理因长时间空闲而断开连接。
// 返回的停止函数会等待心跳协程退出，确保之后不再有并发写入。
func (s *Server) startHeartbeat(sse *sseWriter) func() {
	interval := s.options().HeartbeatSec
	if interval <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sse.ping()
			}
		}
	}()
	return func() {
		close(stop)
		wg.Wait()
	}
}
