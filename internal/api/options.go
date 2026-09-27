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

	SettingAgentFullAccess = "agent_full_access" // on/off：Agent 最大权限（删除/清空类免 confirm，直接执行）

	SettingMaxLogRows   = "max_log_rows"   // 调用日志保留条数上限
	SettingLogBody      = "log_body"       // on/off：是否把请求与响应体落库
	SettingLogBodyLimit = "log_body_limit_kb" // 单条日志请求/响应体的记录上限（KB），超出截断
	SettingStreamSmooth = "stream_smooth"  // on/off：流式输出是否平滑限速
	SettingStreamRate   = "stream_rate"    // 平滑速率上限（字符/秒）
	SettingHeartbeatSec = "heartbeat_sec"  // SSE 心跳间隔（秒），0 表示关闭
	SettingVideoPollSec = "video_poll_sec" // 视频任务轮询间隔（秒）

	SettingRequestTimeout   = "request_timeout_sec" // 全局上游请求兜底超时（秒）
	SettingLogRetentionDays = "log_retention_days"  // 调用日志按天保留天数，0 表示不按天清理

	SettingCacheTTL      = "cache_ttl"       // 响应缓存 TTL（秒），0 表示关闭
	SettingAlertWebhook  = "alert_webhook"   // 告警 Webhook（飞书/企业微信机器人）
	SettingAlertEnabled  = "alert_enabled"   // on/off：渠道失败是否推送告警
	SettingAlertMinFails = "alert_min_fails" // 连续失败多少次才告警（避免抖动）

	SettingTheme         = "theme"            // dark | light | system
	SettingDefaultPage   = "default_page"     // 打开面板时默认进入的页面
	SettingAutoRefresh   = "auto_refresh_sec" // 概览自动刷新间隔（秒），0 表示关闭
	SettingCompact       = "compact"          // on/off：紧凑列表
	SettingConfirmDanger = "confirm_danger"   // on/off：危险操作二次确认

	SettingContextPolicy        = "context_policy"         // off | reject | truncate
	SettingContextSafetyPercent = "context_safety_percent" // 上下文安全系数（50-100）

	// ---- 无人值守：健康巡检 ----

	SettingHealthEnabled  = "health_enabled"   // on/off：是否开启后台巡检
	SettingHealthInterval = "health_interval"  // 巡检间隔（秒）
	SettingHealthRecover  = "health_recover"   // on/off：巡检发现恢复后自动清除熔断
	SettingHealthKeep     = "health_keep"      // 每渠道保留多少条巡检记录
	SettingHealthTimeout  = "health_timeout"   // 单次巡检超时（秒）

	// ---- 无人值守：日报与预警 ----

	SettingReportEnabled  = "report_enabled"   // on/off：是否推送日报
	SettingReportHour     = "report_hour"      // 日报推送时刻（0-23，本地时间）
	SettingReportWeekday  = "report_weekday"   // 日报推送星期：空=每天，0-6=周日..周六（1=每周一）
	SettingReportFailRate = "report_fail_rate" // 失败率超过该百分比时预警（0 关闭）
	SettingReportKeyDays  = "report_key_days"  // 秘钥剩余多少天到期时提醒（0 关闭）
	SettingReportLastDay  = "report_last_day"  // 上一次推送日报的日期，用于避免当天重复推送

	// 飞书应用推送（告警与日报共用，凭据复用「飞书整合」的 App ID/Secret）。
	SettingNotifyFeishuChatID = "notify_feishu_chat_id" // 推送目标会话 chat_id，留空不启用该通道

	// ---- 无人值守：自动备份 ----

	SettingBackupAuto = "backup_auto_enabled" // on/off：每天自动把配置备份到数据目录
	SettingBackupHour = "backup_auto_hour"    // 自动备份时刻（0-23，本地时间）
	SettingBackupKeep = "backup_auto_keep"    // 保留最近多少份自动备份文件

	// ---- 无人值守：负载再平衡 ----

	SettingBalanceMode = "balance_mode" // off | prefer_fast | prefer_healthy
	SettingBalanceBad  = "balance_bad"  // 可用率低于该百分比视为劣质渠道（0-100）

	// ---- 通知：邮件 ----

	SettingEmailHost     = "email_host"      // SMTP 服务器
	SettingEmailPort     = "email_port"      // SMTP 端口（465 走 SSL，其余走 STARTTLS）
	SettingEmailUser     = "email_user"      // SMTP 账号
	SettingEmailPass     = "email_pass"      // SMTP 密码 / 授权码
	SettingEmailFrom     = "email_from"      // 发件人显示地址，留空则用账号
	SettingEmailTo       = "email_to"        // 收件人，多个用逗号分隔
	SettingEmailEnabled  = "email_enabled"   // on/off：是否启用邮件通道
	SettingEmailInsecure = "email_insecure"  // on/off：跳过 TLS 证书校验（自签证书场景）

	SettingPublicURL = "public_url" // 对外访问地址，用于通知里回链到面板

	// ---- 飞书深度整合（入站 webhook 桥） ----

	SettingBridgeEnabled    = "bridge_feishu_enabled"     // on/off：是否启用飞书事件接入
	SettingBridgeTransport  = "bridge_feishu_transport"   // ws | webhook：事件接入方式，ws=长连接（无需公网，默认优先）
	SettingBridgeAppID      = "bridge_feishu_app_id"      // 飞书应用 App ID（用于取 tenant_access_token 回消息）
	SettingBridgeAppSecret  = "bridge_feishu_app_secret"  // 飞书应用 App Secret
	SettingBridgeEncryptKey = "bridge_feishu_encrypt_key" // 事件订阅 Encrypt Key（webhook 模式配置了才校验/解密）
	SettingBridgeToken      = "bridge_feishu_token"       // 事件订阅 Verification Token（webhook 模式可选双保险）
	SettingBridgeMode       = "bridge_feishu_mode"        // chat | agent：chat=回环走对外模型；agent=站内主模型工具循环
	SettingBridgeModel      = "bridge_feishu_model"       // chat 模式使用的对外模型名
	SettingBridgeKey        = "bridge_feishu_key"         // chat 模式回环用的对外秘钥（留空取第一把启用的秘钥）
	SettingBridgePrompt     = "bridge_feishu_prompt"      // chat 模式附加系统提示词（可选）
	SettingBridgeHistory    = "bridge_feishu_history"     // 会话上下文轮数（0=不带上下文，默认 10；30 分钟无消息自动清空）

	// 飞书事件接入方式取值。
	SettingBridgeTransportWS     = "ws"      // 长连接（SDK WebSocket，默认优先）
	SettingBridgeTransportWebhook = "webhook" // 回调 URL 推送
)

// Options 是可由面板动态调整的服务端运行参数。
// 它覆盖 config.Config 中与运行期行为相关的字段，来源优先级为：面板设置 > 环境变量。
type Options struct {
	MaxLogRows   int  `json:"max_log_rows"`
	LogBody      bool `json:"log_body"`
	// LogBodyLimit 是单条日志里请求体/响应体的记录上限（KB），
	// 超出部分截断并注明，避免长文对话把数据库撑爆。
	LogBodyLimit int  `json:"log_body_limit_kb"`
	StreamSmooth bool `json:"stream_smooth"`
	StreamRate   int  `json:"stream_rate"`
	HeartbeatSec int  `json:"heartbeat_sec"`
	VideoPollSec int  `json:"video_poll_sec"`
	CacheTTL     int  `json:"cache_ttl"` // 响应缓存 TTL（秒），0 关闭
	// AlertEnabled 控制渠道失败是否推送告警（飞书/企业微信机器人）。
	AlertEnabled bool `json:"alert_enabled"`
	// AlertMinFails 是同一模型近 5 分钟内连续失败多少次才推送，避免抖动刷屏。
	AlertMinFails int `json:"alert_min_fails"`
	// ContextPolicy 决定估算输入超出模型上下文时的处理方式：
	// off（不校验）| reject（默认，直接报错）| truncate（自动裁剪最早的历史消息）。
	ContextPolicy string `json:"context_policy"`
	// ContextSafetyPercent 是上下文安全系数：只把窗口的该比例用于输入，
	// 其余留给输出与估算误差。
	ContextSafetyPercent int `json:"context_safety_percent"`

	// RequestTimeout 是全局上游请求兜底超时（秒）：渠道未单独配置 TimeoutSec
	// 时生效。保存后热更新进引擎，无需重启。
	RequestTimeout int `json:"request_timeout_sec"`
	// LogRetentionDays 是调用日志按天保留天数：超过该天数的旧日志在裁剪轮里
	// 被删除。0 表示不按天清理（仍受 MaxLogRows 条数上限约束）。
	LogRetentionDays int `json:"log_retention_days"`

	// ---- 无人值守：健康巡检 ----

	// HealthEnabled 控制是否开启后台渠道巡检。关闭后只能靠真实调用失败来发现渠道挂了。
	HealthEnabled bool `json:"health_enabled"`
	// HealthInterval 是巡检轮次间隔（秒）。过短会浪费上游配额，建议 60-600。
	HealthInterval int `json:"health_interval"`
	// HealthRecover 为真时，巡检发现渠道已恢复正常会自动清除熔断，无需人工干预。
	HealthRecover bool `json:"health_recover"`
	// HealthKeep 是每个渠道保留的巡检记录条数上限。
	HealthKeep int `json:"health_keep"`
	// HealthTimeout 是单次巡检请求的超时（秒）。
	HealthTimeout int `json:"health_timeout"`

	// ---- 无人值守：日报与预警 ----

	ReportEnabled bool `json:"report_enabled"`
	// ReportHour 是每日推送时刻（0-23，服务器本地时间）。
	ReportHour int `json:"report_hour"`
	// ReportFailRate 是失败率预警线（百分比）。当日失败率高于它时立即追加一条预警，0 关闭。
	ReportFailRate int `json:"report_fail_rate"`
	// ReportKeyDays 是秘钥到期提前提醒天数，0 关闭。
	ReportKeyDays int `json:"report_key_days"`

	// ---- 无人值守：自动备份 ----

	// BackupAuto 为真时，每天到达 BackupHour 时刻把全部配置导出到
	// 数据目录 backups/ 下，保留最近 BackupKeep 份。
	BackupAuto bool `json:"backup_auto_enabled"`
	BackupHour int `json:"backup_auto_hour"`
	BackupKeep int `json:"backup_auto_keep"`

	// ---- 无人值守：负载再平衡 ----

	// BalanceMode 决定多上游/多渠道的排序依据：
	// off（按原顺序）| prefer_fast（按近期平均延迟）| prefer_healthy（按可用率，默认）。
	BalanceMode string `json:"balance_mode"`
	// BalanceBad 是「劣质」判定线：可用率低于该百分比的渠道会被降权到候选末尾。
	BalanceBad int `json:"balance_bad"`

	// ---- 通知：邮件 ----

	EmailEnabled bool   `json:"email_enabled"`
	EmailHost    string `json:"email_host"`
	EmailPort    int    `json:"email_port"`
	EmailUser    string `json:"email_user"`
	// EmailPass 不回传给前端明文，只用 HasEmailPass 表示是否已配置。
	EmailFrom     string `json:"email_from"`
	EmailTo       string `json:"email_to"`
	EmailInsecure bool   `json:"email_insecure"`
	// HasEmailPass 为真表示库里已存有 SMTP 密码，面板据此显示「已配置」。
	HasEmailPass bool `json:"has_email_pass"`

	// PublicURL 是面板的对外地址，用于通知里附带可直接点开的链接。
	PublicURL string `json:"public_url"`
}

// defaultOptions 返回由环境变量推导出的默认值，供面板展示与回落使用。
func (s *Server) defaultOptions() Options {
	return Options{
		MaxLogRows:   s.cfg.MaxLogRows,
		LogBody:      !s.cfg.DisableLogBody,
		LogBodyLimit: 64,
		StreamSmooth: s.cfg.StreamSmooth,
		StreamRate:   s.cfg.StreamRate,
		HeartbeatSec: s.cfg.HeartbeatSec,
		VideoPollSec: s.cfg.VideoPollSec,
		CacheTTL:     settingInt(s.st, SettingCacheTTL, 0, 0, 86400),
		AlertEnabled: settingBool(s.st, SettingAlertEnabled, false),
		AlertMinFails: settingInt(s.st, SettingAlertMinFails, 1, 1, 100),

		ContextPolicy:        relay.ContextPolicyReject,
		ContextSafetyPercent: relay.DefaultSafetyPercent,

		// 全局上游超时沿用环境变量 RELAY_TIMEOUT 的值（未配置时 Load() 已给默认 600）。
		RequestTimeout: s.cfg.TimeoutSec,
		// 日志默认不按天清理，只按条数；按天是可选项。
		LogRetentionDays: 0,

		// 巡检默认开启：无人值守的核心就是「没人用也能发现问题」。
		HealthEnabled:  true,
		HealthInterval: 300,
		HealthRecover:  true,
		HealthKeep:     200,
		HealthTimeout:  20,

		// 日报默认关闭：推送目标（Webhook / 邮件）没配好之前推了也没人收。
		ReportEnabled:  false,
		ReportHour:     9,
		ReportFailRate: 20,
		ReportKeyDays:  7,

		// 自动备份默认关闭：凌晨 3 点备份一份、保留最近 7 份，打开开关即用。
		BackupAuto: false,
		BackupHour: 3,
		BackupKeep: 7,

		BalanceMode: relay.BalancePreferHealthy,
		BalanceBad:  70,

		EmailPort: 465,
	}
}

// loadOptions 读取面板设置并合并默认值，得到当前应当生效的运行参数。
// 仅在启动与保存设置时调用；请求热路径请用 options() 读取缓存。
func (s *Server) loadOptions() *Options {
	def := s.defaultOptions()
	return &Options{
		MaxLogRows:   settingInt(s.st, SettingMaxLogRows, def.MaxLogRows, 100, 2000000),
		LogBody:      settingBool(s.st, SettingLogBody, def.LogBody),
		LogBodyLimit: settingInt(s.st, SettingLogBodyLimit, def.LogBodyLimit, 1, 2048),
		StreamSmooth: settingBool(s.st, SettingStreamSmooth, def.StreamSmooth),
		StreamRate:   settingInt(s.st, SettingStreamRate, def.StreamRate, 5, 100000),
		HeartbeatSec: settingInt(s.st, SettingHeartbeatSec, def.HeartbeatSec, 0, 3600),
		VideoPollSec: settingInt(s.st, SettingVideoPollSec, def.VideoPollSec, 1, 3600),
		CacheTTL:     settingInt(s.st, SettingCacheTTL, def.CacheTTL, 0, 86400),
		AlertEnabled: settingBool(s.st, SettingAlertEnabled, def.AlertEnabled),
		AlertMinFails: settingInt(s.st, SettingAlertMinFails, def.AlertMinFails, 1, 100),

		ContextPolicy: settingEnum(s.st, SettingContextPolicy, def.ContextPolicy,
			[]string{relay.ContextPolicyOff, relay.ContextPolicyReject, relay.ContextPolicyTruncate}),
		ContextSafetyPercent: settingInt(s.st, SettingContextSafetyPercent, def.ContextSafetyPercent, 50, 100),

		RequestTimeout:   settingInt(s.st, SettingRequestTimeout, def.RequestTimeout, 10, 3600),
		LogRetentionDays: settingInt(s.st, SettingLogRetentionDays, def.LogRetentionDays, 0, 3650),

		HealthEnabled:  settingBool(s.st, SettingHealthEnabled, def.HealthEnabled),
		HealthInterval: settingInt(s.st, SettingHealthInterval, def.HealthInterval, 30, 86400),
		HealthRecover:  settingBool(s.st, SettingHealthRecover, def.HealthRecover),
		HealthKeep:     settingInt(s.st, SettingHealthKeep, def.HealthKeep, 20, 5000),
		HealthTimeout:  settingInt(s.st, SettingHealthTimeout, def.HealthTimeout, 3, 300),

		ReportEnabled:  settingBool(s.st, SettingReportEnabled, def.ReportEnabled),
		ReportHour:     settingInt(s.st, SettingReportHour, def.ReportHour, 0, 23),
		ReportFailRate: settingInt(s.st, SettingReportFailRate, def.ReportFailRate, 0, 100),
		ReportKeyDays:  settingInt(s.st, SettingReportKeyDays, def.ReportKeyDays, 0, 365),

		BackupAuto: settingBool(s.st, SettingBackupAuto, def.BackupAuto),
		BackupHour: settingInt(s.st, SettingBackupHour, def.BackupHour, 0, 23),
		BackupKeep: settingInt(s.st, SettingBackupKeep, def.BackupKeep, 1, 365),

		BalanceMode: settingEnum(s.st, SettingBalanceMode, def.BalanceMode,
			[]string{relay.BalanceOff, relay.BalancePreferFast, relay.BalancePreferHealthy}),
		BalanceBad: settingInt(s.st, SettingBalanceBad, def.BalanceBad, 0, 100),

		EmailEnabled:  settingBool(s.st, SettingEmailEnabled, def.EmailEnabled),
		EmailHost:     strings.TrimSpace(s.st.GetSetting(SettingEmailHost, "")),
		EmailPort:     settingInt(s.st, SettingEmailPort, def.EmailPort, 1, 65535),
		EmailUser:     strings.TrimSpace(s.st.GetSetting(SettingEmailUser, "")),
		EmailFrom:     strings.TrimSpace(s.st.GetSetting(SettingEmailFrom, "")),
		EmailTo:       strings.TrimSpace(s.st.GetSetting(SettingEmailTo, "")),
		EmailInsecure: settingBool(s.st, SettingEmailInsecure, def.EmailInsecure),
		// 密码绝不回传：只暴露「有没有配置」。
		HasEmailPass: strings.TrimSpace(s.st.GetSetting(SettingEmailPass, "")) != "",
		PublicURL:    strings.TrimSpace(s.st.GetSetting(SettingPublicURL, "")),
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
	o := s.loadOptions()
	s.opts.Store(o)
	// 全局上游超时存在引擎里（原子值），保存后直接写进去，无需重启。
	s.eng.SetDefaultTimeout(o.RequestTimeout)
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
