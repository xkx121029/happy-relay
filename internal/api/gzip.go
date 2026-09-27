package api

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// gzipWriterPool 复用 gzip.Writer，避免每个请求新建内部缓冲。
var gzipWriterPool = sync.Pool{
	New: func() any { return gzip.NewWriter(nil) },
}

// compressible 判断该 Content-Type 是否值得压缩。
// 只压文本类：JSON 接口（含日志列表这类带完整输入输出的大响应）、
// 面板 JS/CSS/HTML。SSE（text/event-stream）、音视频、图片天然不命中。
func compressible(ct string) bool {
	ct, _, _ = strings.Cut(ct, ";")
	ct = strings.TrimSpace(strings.ToLower(ct))
	switch {
	case ct == "application/json":
		return true
	case ct == "text/html" || ct == "text/css":
		return true
	case ct == "text/javascript" || ct == "application/javascript":
		return true
	}
	return false
}

// gzipMiddleware 对可压缩响应做透明 gzip。
// 判定发生在 WriteHeader 时、依据最终 Content-Type：业务方无需逐个标注，
// 流式接口、二进制接口自动跳过；请求未带 Accept-Encoding: gzip 时零开销直通。
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer func() {
			if gw.gzw != nil {
				_ = gw.gzw.Close()
				gzipWriterPool.Put(gw.gzw)
				gw.gzw = nil
			}
		}()
		next.ServeHTTP(gw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gzw *gzip.Writer
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.gzw == nil && status != http.StatusNoContent && compressible(g.Header().Get("Content-Type")) {
		w := gzipWriterPool.Get().(*gzip.Writer)
		w.Reset(g.ResponseWriter)
		g.gzw = w
		h := g.Header()
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Del("Content-Length")
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if g.gzw == nil {
		// 处理器没显式调 WriteHeader 的场景，按 200 补一次判定。
		g.WriteHeader(http.StatusOK)
	}
	if g.gzw != nil {
		return g.gzw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Flush 透传：压缩状态下先刷 gzip 流再刷底层连接，
// 保证即使压缩也不会拖慢需要逐步下发的响应。
func (g *gzipResponseWriter) Flush() {
	if g.gzw != nil {
		_ = g.gzw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
