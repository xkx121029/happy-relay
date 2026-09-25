package api

import (
	"strconv"
	"strings"

	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// 设置项键名。面板「设置」页据此读写。
// 前六项是服务端运行参数，保存后立即生效、无需重启；其余为纯前端偏好，服务端只负责存储。
const (
	SettingPanelTitle  = "panel_title"  // 面板标题
	SettingSystemModel = "system_model" // 系统级 AI 使用的对外模型名

	SettingMaxLogRows   = "max_log_rows"   // 调用日志保留条数上限
	SettingLogBody      = "log_body"       // on/off：是否把请求与响应体落库
	SettingStreamSmooth = "stream_smooth"  // on/off：流式输出是否平滑限速
	SettingStreamRate   = "stream_rate"    // 平滑速率上限（字符/秒）
	SettingHeartbeatSec = "heartbeat_sec"  // SSE 心跳间隔（秒），0 表示关闭
	SettingVideoPollSec = "video_poll_sec" // 视频任务轮询间隔（秒）

	SettingTheme         = "theme"            // dark | light | system
	SettingDefaultPage   = "default_page"     // 打开面板时默认进入的页面
	SettingAutoRefresh   = "auto_refresh_sec" // 概览自动刷新间隔（秒），0 表示关闭
	SettingCompact       = "compact"          // on/off：紧凑列表
	SettingConfirmDanger = "confirm_danger"   // on/off：危险操作二次确认

	SettingContextPolicy        = "context_policy"         // off | reject | truncate
	SettingContextSafetyPercent = "context_safety_percent" // 上下文安全系数（50-100）
)

// Options 是可由面板动态调整的服务端运行参数。
// 它覆盖 config.Config 中与运行期行为相关的字段，来源优先级为：面板设置 > 环境变量。
type Options struct {
	MaxLogRows   int  `json:"max_log_rows"`
	LogBody      bool `json:"log_body"`
	StreamSmooth bool `json:"stream_smooth"`
	StreamRate   int  `json:"stream_rate"`
	HeartbeatSec int  `json:"heartbeat_sec"`
	VideoPollSec int  `json:"video_poll_sec"`
	// ContextPolicy 决定估算输入超出模型上下文时的处理方式：
	// off（不校验）| reject（默认，直接报错）| truncate（自动裁剪最早的历史消息）。
	ContextPolicy string `json:"context_policy"`
	// ContextSafetyPercent 是上下文安全系数：只把窗口的该比例用于输入，
	// 其余留给输出与估算误差。
	ContextSafetyPercent int `json:"context_safety_percent"`
}

// defaultOptions 返回由环境变量推导出的默认值，供面板展示与回落使用。
func (s *Server) defaultOptions() Options {
	return Options{
		MaxLogRows:   s.cfg.MaxLogRows,
		LogBody:      !s.cfg.DisableLogBody,
		StreamSmooth: s.cfg.StreamSmooth,
		StreamRate:   s.cfg.StreamRate,
		HeartbeatSec: s.cfg.HeartbeatSec,
		VideoPollSec: s.cfg.VideoPollSec,

		ContextPolicy:        relay.ContextPolicyReject,
		ContextSafetyPercent: relay.DefaultSafetyPercent,
	}
}

// loadOptions 读取面板设置并合并默认值，得到当前应当生效的运行参数。
// 仅在启动与保存设置时调用；请求热路径请用 options() 读取缓存。
func (s *Server) loadOptions() *Options {
	def := s.defaultOptions()
	return &Options{
		MaxLogRows:   settingInt(s.st, SettingMaxLogRows, def.MaxLogRows, 100, 2000000),
		LogBody:      settingBool(s.st, SettingLogBody, def.LogBody),
		StreamSmooth: settingBool(s.st, SettingStreamSmooth, def.StreamSmooth),
		StreamRate:   settingInt(s.st, SettingStreamRate, def.StreamRate, 5, 100000),
		HeartbeatSec: settingInt(s.st, SettingHeartbeatSec, def.HeartbeatSec, 0, 3600),
		VideoPollSec: settingInt(s.st, SettingVideoPollSec, def.VideoPollSec, 1, 3600),

		ContextPolicy: settingEnum(s.st, SettingContextPolicy, def.ContextPolicy,
			[]string{relay.ContextPolicyOff, relay.ContextPolicyReject, relay.ContextPolicyTruncate}),
		ContextSafetyPercent: settingInt(s.st, SettingContextSafetyPercent, def.ContextSafetyPercent, 50, 100),
	}
}

// options 返回当前生效的运行参数，开销仅一次原子读。
func (s *Server) options() *Options {
	if o := s.opts.Load(); o != nil {
		return o
	}
	return s.loadOptions()
}

// reloadOptions 在设置保存后刷新缓存，使新参数立即生效。
func (s *Server) reloadOptions() {
	s.opts.Store(s.loadOptions())
}

// settingBool 读取布尔型设置："on"/"1"/"true"/"yes" 视为真，其余视为假；
// 未设置时返回 def。
func settingBool(st *store.Store, key string, def bool) bool {
	raw := strings.ToLower(strings.TrimSpace(st.GetSetting(key, "")))
	switch raw {
	case "":
		return def
	case "on", "1", "true", "yes":
		return true
	}
	return false
}

// settingEnum 读取枚举型设置；取值不在白名单内时返回 def。
func settingEnum(st *store.Store, key, def string, allowed []string) string {
	raw := strings.ToLower(strings.TrimSpace(st.GetSetting(key, "")))
	if raw == "" {
		return def
	}
	for _, a := range allowed {
		if raw == a {
			return raw
		}
	}
	return def
}

// settingInt 读取整型设置并夹取到 [min,max] 区间；未设置或无法解析时返回 def。
func settingInt(st *store.Store, key string, def, min, max int) int {
	raw := strings.TrimSpace(st.GetSetting(key, ""))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
