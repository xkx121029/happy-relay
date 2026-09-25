package api

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"sync"
)

// sseWriter 负责把结构化数据写成 OpenAI 风格的 SSE 事件流。
// 心跳协程与业务协程会并发写入，因此所有写出都加锁。
type sseWriter struct {
	w    http.ResponseWriter
	f    http.Flusher
	once sync.Once
	mu   sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	s := &sseWriter{w: w}
	if f, ok := w.(http.Flusher); ok {
		s.f = f
	}
	return s
}

func (s *sseWriter) start() {
	s.once.Do(func() {
		h := s.w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache, no-transform")
		h.Set("Connection", "keep-alive")
		// 关闭 Nginx 等反向代理的缓冲，保证流式内容即时可见。
		h.Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
		s.flush()
	})
}

func (s *sseWriter) send(v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	if _, err := s.w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := s.w.Write(buf); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *sseWriter) done() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	_, _ = s.w.Write([]byte("data: [DONE]\n\n"))
	s.flush()
}

// ping 发送一条 SSE 注释行，用于在长时间无输出时保持连接。
func (s *sseWriter) ping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	_, _ = s.w.Write([]byte(": keep-alive\n\n"))
	s.flush()
}

func (s *sseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

func randRead(b []byte) (int, error) { return rand.Read(b) }
