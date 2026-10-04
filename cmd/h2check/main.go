// h2check 临时探针：验证 h2c（明文 HTTP/2 先验知识）与 TLS HTTP/2 协商。
// 用法：go run ./cmd/h2check -mode h2c -url http://127.0.0.1:9000/healthz
//
//	go run ./cmd/h2check -mode tls -url https://127.0.0.1:9443/healthz
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

func main() {
	mode := flag.String("mode", "h2c", "h2c | tls")
	url := flag.String("url", "", "目标 URL")
	flag.Parse()
	if *url == "" {
		flag.Usage()
		return
	}

	tr := &http2.Transport{}
	switch *mode {
	case "h2c":
		tr.AllowHTTP = true
		tr.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr) // 明文连接直接当"TLS 连接"返回（h2c 先验知识模式）
		}
	case "tls":
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Get(*url)
	if err != nil {
		fmt.Println("FAIL:", err)
		return
	}
	defer resp.Body.Close()
	fmt.Printf("OK proto=%s status=%d\n", resp.Proto, resp.StatusCode)
}
