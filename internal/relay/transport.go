package relay

import (
	"net"
	"net/http"
	"time"
)

// PooledTransport 返回一个带连接池的 Transport，供进程内所有上游调用共享。
//
// 此前热路径（聊天/透传/探测）每个请求都 new 一个 http.Client，底层 Transport
// 也随之新建：keep-alive 连接完全无法复用，每次请求都要重新 TCP 建连 + TLS 握手，
// 对国内到海外上游这类高 RTT 链路首字延迟影响明显。收敛为共享 Transport 后，
// 连接按 (host, TLS配置) 复用，MaxIdleConnsPerHost 放大到 32（默认 2），
// 避免多密钥并发打同一上游时互相挤掉空闲连接。
//
// 注意：共享 Transport 不能再依赖 http.Client.Timeout（它会绑定整个客户端），
// 单次超时改由调用方通过 request context 控制。
func PooledTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
