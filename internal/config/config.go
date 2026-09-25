// Package config 负责从环境变量装载服务运行配置。
// 自用场景下所有配置均可用环境变量覆盖，便于 Docker 部署。
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr           string // HTTP 监听地址，默认 :8787
	DataDir        string // 数据目录
	DBPath         string // SQLite 文件路径
	PublicURL      string // 对外可访问地址，用于拼接生成结果链接
	MaxLogRows     int    // 请求日志保留条数上限
	TimeoutSec     int    // 单次上游请求兜底超时（秒）
	VideoPollSec   int    // 视频任务后台轮询间隔（秒）
	DisableLogBody bool   // 是否关闭请求/响应体落库（保护隐私）
	StreamSmooth   bool   // 是否把流式输出平滑成匀速下发
	StreamRate     int    // 平滑输出速率上限（字符/秒）
	HeartbeatSec   int    // SSE 心跳间隔（秒），0 表示关闭
	BreakerFails   int    // 渠道连续失败多少次后熔断（0 关闭）
	BreakerCoolSec int    // 熔断冷却时间（秒）
}

func Load() *Config {
	dataDir := getenv("RELAY_DATA_DIR", "./data")
	cfg := &Config{
		Addr:           getenv("RELAY_ADDR", ":8787"),
		DataDir:        dataDir,
		DBPath:         getenv("RELAY_DB", filepath.Join(dataDir, "relay.db")),
		PublicURL:      strings.TrimRight(getenv("RELAY_PUBLIC_URL", ""), "/"),
		MaxLogRows:     getenvInt("RELAY_MAX_LOG_ROWS", 20000),
		TimeoutSec:     getenvInt("RELAY_TIMEOUT", 600),
		VideoPollSec:   getenvInt("RELAY_VIDEO_POLL", 10),
		DisableLogBody: getenvBool("RELAY_DISABLE_LOG_BODY", false),
		StreamSmooth:   getenvBool("RELAY_STREAM_SMOOTH", true),
		StreamRate:     getenvInt("RELAY_STREAM_RATE", 120),
		HeartbeatSec:   getenvIntZero("RELAY_SSE_HEARTBEAT", 15),
		BreakerFails:   getenvIntZero("RELAY_BREAKER_FAILS", 5),
		BreakerCoolSec: getenvInt("RELAY_BREAKER_COOLDOWN", 60),
	}
	return cfg
}

// Ensure 创建必要目录。
func (c *Config) Ensure() error {
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(c.DataDir, "files"), 0o755)
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// getenvIntZero 与 getenvInt 类似，但允许显式配置为 0（用于关闭某项功能）。
func getenvIntZero(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func getenvBool(key string, def bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
