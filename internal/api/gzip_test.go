package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGzipMiddleware 验证：JSON 响应压缩、无 Accept-Encoding 直通、SSE 不压缩。
func TestGzipMiddleware(t *testing.T) {
	jsonHandler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true,"msg":"hello"}`))
	}))

	// 1. 带 gzip 头：应压缩且解压后内容一致。
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	jsonHandler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("期望 Content-Encoding=gzip，实际 %q", got)
	}
	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("响应体不是合法 gzip 流：%v", err)
	}
	out, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("解压失败：%v", err)
	}
	if string(out) != `{"ok":true,"msg":"hello"}` {
		t.Fatalf("解压内容不一致：%s", out)
	}

	// 2. 不带 gzip 头：直通，不压缩。
	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec2 := httptest.NewRecorder()
	jsonHandler.ServeHTTP(rec2, req2)
	if rec2.Header().Get("Content-Encoding") != "" {
		t.Fatal("未声明 Accept-Encoding 时不应压缩")
	}
	if rec2.Body.String() != `{"ok":true,"msg":"hello"}` {
		t.Fatalf("直通内容不一致：%s", rec2.Body.String())
	}

	// 3. SSE（text/event-stream）：跳过压缩，保持逐事件下发。
	sseHandler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hi\n\n"))
	}))
	req3 := httptest.NewRequest(http.MethodGet, "/sse", nil)
	req3.Header.Set("Accept-Encoding", "gzip")
	rec3 := httptest.NewRecorder()
	sseHandler.ServeHTTP(rec3, req3)
	if rec3.Header().Get("Content-Encoding") != "" {
		t.Fatal("SSE 不应被压缩")
	}
	if rec3.Body.String() != "data: hi\n\n" {
		t.Fatalf("SSE 内容不一致：%s", rec3.Body.String())
	}
}

// TestCompressible 验证 Content-Type 判定边界。
func TestCompressible(t *testing.T) {
	yes := []string{"application/json", "application/json; charset=utf-8", "text/css", "text/javascript", "application/javascript", "text/html"}
	for _, ct := range yes {
		if !compressible(ct) {
			t.Errorf("%s 应可压缩", ct)
		}
	}
	no := []string{"text/event-stream", "audio/mpeg", "video/mp4", "image/png", "application/octet-stream", ""}
	for _, ct := range no {
		if compressible(ct) {
			t.Errorf("%s 不应压缩", ct)
		}
	}
}
