package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeUpstreamLatency 验证 probeUpstream 的延迟字段确实被填充：
// 之前因为「无名返回值 + defer 写局部副本」，调用方拿到的 latency 永远是 0。
// 这个 bug 在巡检功能上线前一直没暴露，因为连通性测试页面根本没展示延迟。
// 用本地 httptest 服务，不依赖外网，确定性高。
func TestProbeUpstreamLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 15 * time.Second}
	r := probeUpstream(context.Background(), client, "openai", srv.URL, "sk-bogus", false)
	if r.Latency <= 0 {
		t.Fatalf("延迟未被记录：got %d，期望 > 0", r.Latency)
	}
	if !r.OK {
		t.Fatalf("本应探测成功，却得到 ok=%v，status=%d", r.OK, r.Status)
	}
	t.Logf("延迟记录正常：%dms", r.Latency)
}
