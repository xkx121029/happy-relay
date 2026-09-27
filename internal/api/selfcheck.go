package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"happy-relay/internal/notify"
	"happy-relay/internal/relay"
	"happy-relay/internal/store"
)

// ---------- 启动自检与配置自省 ----------

// CheckLevel 是自检项的严重程度。
type CheckLevel string

const (
	// CheckOK 无问题。
	CheckOK CheckLevel = "ok"
	// CheckWarn 配置能跑，但存在会踩坑的隐患（例如没有可用渠道兜底）。
	CheckWarn CheckLevel = "warn"
	// CheckError 配置有明确错误，会导致功能不可用。
	CheckError CheckLevel = "error"
)

// CheckItem 是一条自检结论。Hint 给出「怎么修」，不让用户对着结论猜。
type CheckItem struct {
	ID      string     `json:"id"`
	Level   CheckLevel `json:"level"`
	Title   string     `json:"title"`
	Detail  string     `json:"detail,omitempty"`
	Hint    string     `json:"hint,omitempty"`
	Related []string   `json:"related,omitempty"`
}

// SelfCheck 是完整的一次自检结果。
type SelfCheck struct {
	CheckedAt string      `json:"checked_at"`
	OK        int         `json:"ok_count"`
	Warn      int         `json:"warn_count"`
	Error     int         `json:"error_count"`
	Items     []CheckItem `json:"items"`
}

// Ready 表示没有阻断级问题。
func (c SelfCheck) Ready() bool { return c.Error == 0 }

// RunSelfCheck 检查当前配置是否自洽，并给出修复建议。
// 这里只做「静态」检查（读库 + 看设置），不发任何外部请求——
// 自检必须能秒回并且不依赖网络，否则面板一打开就卡住。
func (s *Server) RunSelfCheck() SelfCheck {
	out := SelfCheck{CheckedAt: time.Now().Format(time.RFC3339)}
	add := func(it CheckItem) {
		switch it.Level {
		case CheckError:
			out.Error++
		case CheckWarn:
			out.Warn++
		default:
			out.OK++
		}
		out.Items = append(out.Items, it)
	}

	providers, err := s.st.ListProviders()
	if err != nil {
		add(CheckItem{ID: "db", Level: CheckError, Title: "无法读取渠道列表",
			Detail: err.Error(), Hint: "数据库可能损坏，检查数据目录权限或从备份恢复。"})
		return out
	}
	models, err := s.st.ListModels()
	if err != nil {
		add(CheckItem{ID: "db.models", Level: CheckError, Title: "无法读取模型列表",
			Detail: err.Error(), Hint: "数据库可能损坏，检查数据目录权限或从备份恢复。"})
		return out
	}
	keys, _ := s.st.ListAPIKeys()

	// ---- 1. 渠道可用性 ----

	enabled := make([]store.Provider, 0, len(providers))
	for _, p := range providers {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}
	switch {
	case len(providers) == 0:
		add(CheckItem{ID: "provider.none", Level: CheckError, Title: "还没有配置任何渠道",
			Detail: "中转站至少需要一个上游渠道才能工作。",
			Hint:   "到「渠道」页新建一个，或点「内置模型」从预设里一键添加。"})
	case len(enabled) == 0:
		add(CheckItem{ID: "provider.disabled", Level: CheckError, Title: "所有渠道都被停用了",
			Detail: fmt.Sprintf("共 %d 个渠道，全部处于停用状态。", len(providers)),
			Hint:   "到「渠道」页把要用的渠道打开。"})
	default:
		add(CheckItem{ID: "provider.ok", Level: CheckOK, Title: "渠道配置正常",
			Detail: fmt.Sprintf("共 %d 个渠道，其中 %d 个启用。", len(providers), len(enabled))})
	}

	// 逐个检查启用渠道的入口地址与密钥。
	var noBase, noKey, placeholder []string
	for _, p := range enabled {
		base := p.BaseURLs
		if len(base) == 0 && relay.DefaultBaseURL(p.Kind) == "" {
			noBase = append(noBase, p.Name)
		}
		if len(p.APIKeys) == 0 {
			noKey = append(noKey, p.Name)
		}
		for _, k := range p.APIKeys {
			// 占位密钥是最常见的手滑：文档里抄了示例就直接保存。
			if isPlaceholderKey(k) {
				placeholder = append(placeholder, p.Name)
				break
			}
		}
	}
	if len(noBase) > 0 {
		add(CheckItem{ID: "provider.base", Level: CheckError, Title: "渠道缺少入口地址",
			Detail:  "这些渠道既没有填写入口地址，协议也没有内置默认地址：" + strings.Join(noBase, "、"),
			Hint:    "到「渠道」页为它们补上 Base URL。",
			Related: noBase})
	}
	if len(noKey) > 0 {
		add(CheckItem{ID: "provider.key", Level: CheckWarn, Title: "渠道缺少 API 密钥",
			Detail:  "这些渠道没有配置任何密钥，调用时会直接失败：" + strings.Join(noKey, "、"),
			Hint:    "到「渠道」页补上密钥；部分自建服务确实不需要密钥，可忽略本条。",
			Related: noKey})
	}
	if len(placeholder) > 0 {
		add(CheckItem{ID: "provider.placeholder", Level: CheckWarn, Title: "渠道密钥疑似占位符",
			Detail:  "这些渠道的密钥看起来还是示例值：" + strings.Join(placeholder, "、"),
			Hint:    "示例密钥必然鉴权失败，请换成真实密钥。",
			Related: placeholder})
	}

	// ---- 2. 模型与上游绑定 ----

	enabledID := map[int64]bool{}
	for _, p := range enabled {
		enabledID[p.ID] = true
	}
	var noUpstream, orphanInput []string
	for _, m := range models {
		inputs := m.AllInputs()
		if len(inputs) == 0 {
			noUpstream = append(noUpstream, m.Name)
			continue
		}
		usable := false
		for _, in := range inputs {
			if enabledID[in.ProviderID] {
				usable = true
				break
			}
		}
		if !usable {
			orphanInput = append(orphanInput, m.Name)
		}
	}
	switch {
	case len(models) == 0:
		add(CheckItem{ID: "model.none", Level: CheckError, Title: "还没有配置任何对外模型",
			Detail: "客户端只能调用已配置的对外模型名。",
			Hint:   "到「模型」页新建，或点「内置模型」从预设里一键添加。"})
	default:
		add(CheckItem{ID: "model.ok", Level: CheckOK, Title: "模型配置正常",
			Detail: fmt.Sprintf("共 %d 个对外模型。", len(models))})
	}
	if len(noUpstream) > 0 {
		add(CheckItem{ID: "model.no_upstream", Level: CheckError, Title: "模型没有绑定任何上游",
			Detail:  "这些模型配好了对外名字，但没有指向任何渠道的上游模型，调用必然失败：" + strings.Join(noUpstream, "、"),
			Hint:    "到「模型」页为它们添加至少一个上游。",
			Related: noUpstream})
	}
	if len(orphanInput) > 0 {
		add(CheckItem{ID: "model.orphan", Level: CheckWarn, Title: "模型的上游全部不可用",
			Detail:  "这些模型绑定的渠道已被删除或停用：" + strings.Join(orphanInput, "、"),
			Hint:    "到「模型」页重新绑定到启用中的渠道，或把对应渠道打开。",
			Related: orphanInput})
	}

	// ---- 3. 访问秘钥 ----

	activeKeys := 0
	for _, k := range keys {
		if k.Enabled {
			activeKeys++
		}
	}
	if activeKeys == 0 {
		add(CheckItem{ID: "key.none", Level: CheckWarn, Title: "没有启用中的访问秘钥",
			Detail: "调用 /v1/* 接口需要有效的访问秘钥。",
			Hint:   "到「秘钥」页新建一把；面板自身的调试页不受影响。"})
	} else {
		add(CheckItem{ID: "key.ok", Level: CheckOK, Title: "访问秘钥正常",
			Detail: fmt.Sprintf("共 %d 把秘钥，其中 %d 把启用。", len(keys), activeKeys)})
	}

	// ---- 4. 通知通道 ----

	o := s.options()
	channels := s.notifier().Names()
	if s.alertEnabled() && len(channels) == 0 {
		add(CheckItem{ID: "notify.channel", Level: CheckWarn, Title: "开启了告警但没有可用通道",
			Detail: "告警开关是打开的，但 Webhook 与邮件都没配置完整，出问题时会通知不到你。",
			Hint:   "到「设置 → 无人值守」填 Webhook，或配好邮箱并打开邮件通道。"})
	} else if len(channels) > 0 {
		add(CheckItem{ID: "notify.ok", Level: CheckOK, Title: "通知通道已就绪",
			Detail: "当前生效通道：" + strings.Join(channels, "、")})
	}
	if o.EmailEnabled {
		cfg := notify.EmailConfig{
			Host: o.EmailHost, Port: o.EmailPort, User: o.EmailUser,
			Pass: s.st.GetSetting(SettingEmailPass, ""),
			To:   splitEmails(o.EmailTo),
		}
		if !cfg.Valid() {
			add(CheckItem{ID: "notify.email", Level: CheckError, Title: "邮件通道配置不完整",
				Detail: "已打开邮件通道，但 SMTP 服务器、账号、密码或收件人缺失。",
				Hint:   "到「设置 → 无人值守」补全邮件配置，然后点「发送测试通知」验证。"})
		}
	}

	// ---- 5. 无人值守相关设置的自省 ----

	if o.HealthEnabled && o.HealthInterval > 0 {
		// 巡检 + 熔断的配合：巡检间隔远大于熔断冷却时，恢复会明显滞后。
		cool := s.cfg.BreakerCoolSec
		if cool > 0 && o.HealthInterval > cool*4 {
			add(CheckItem{ID: "health.interval", Level: CheckWarn, Title: "巡检间隔远大于熔断冷却",
				Detail: fmt.Sprintf("巡检间隔 %d 秒，熔断冷却 %d 秒。渠道恢复后最长要等一个巡检周期才会被自动解锁。",
					o.HealthInterval, cool),
				Hint: "把巡检间隔调到熔断冷却的 4 倍以内，自动恢复会更及时。"})
		}
	}
	if !o.HealthEnabled && o.BalanceMode != relay.BalanceOff {
		add(CheckItem{ID: "balance.no_health", Level: CheckWarn, Title: "负载再平衡缺少数据来源",
			Detail: "再平衡依据的是巡检可用率，但巡检已关闭，排序会退化成「没数据」状态。",
			Hint:   "要么打开健康巡检，要么把再平衡模式设为 off。"})
	}
	if o.ReportEnabled && len(channels) == 0 {
		add(CheckItem{ID: "report.no_channel", Level: CheckWarn, Title: "日报开着但没人收",
			Detail: "日报已启用，但没有配置任何可用的通知通道。",
			Hint:   "到「设置 → 无人值守」配置 Webhook 或邮箱。"})
	}

	// ---- 6. 容量与运行环境 ----

	if n, err := s.st.HealthCheckCount(); err == nil && o.HealthKeep > 0 {
		// 巡检记录条数明显超过上限，说明裁剪没跟上（例如巡检间隔被调得很小）。
		if budget := int64(o.HealthKeep) * int64(len(enabled)+1); n > budget*3 {
			add(CheckItem{ID: "storage.health", Level: CheckWarn, Title: "巡检记录堆积",
				Detail: fmt.Sprintf("当前 %d 条巡检记录，远超每渠道保留 %d 条的预期。", n, o.HealthKeep),
				Hint:   "把巡检间隔调大，或调小「每渠道保留条数」。"})
		}
	}
	if o.CacheTTL > 0 {
		add(CheckItem{ID: "cache.on", Level: CheckOK, Title: "响应缓存已开启",
			Detail: fmt.Sprintf("相同请求在 %d 秒内会直接命中缓存。", o.CacheTTL)})
	}

	return out
}

// handleSelfCheck 返回一次完整自检结果，供面板「配置自省」卡片展示。
func (s *Server) handleSelfCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.RunSelfCheck())
}

// handleNotifyTest 发送一条测试通知，验证通知通道配置。
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	ok, channels, err := s.SendTestNotification(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if ok == 0 {
		writeErr(w, http.StatusBadGateway, "upstream_error",
			"所有通道都推送失败（"+strings.Join(channels, "、")+"），请检查配置，详情见服务端日志")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"sent":     ok,
		"channels": channels,
	})
}

// handleHealthHistory 返回巡检记录与可用率摘要，供面板展示渠道健康趋势。
func (s *Server) handleHealthHistory(w http.ResponseWriter, r *http.Request) {
	var id int64
	if v := strings.TrimSpace(r.URL.Query().Get("provider_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			id = n
		}
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	hours := 24
	if v := strings.TrimSpace(r.URL.Query().Get("hours")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}

	checks, err := s.st.RecentHealthChecks(id, limit)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	uptime, err := s.st.ProviderUptime(hours)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checks": checks,
		"uptime": uptime,
	})
}

// isPlaceholderKey 判断密钥是否还是文档里的示例值。
// 只做明显的模式匹配，宁可漏判也不误伤真实密钥。
func isPlaceholderKey(k string) bool {
	s := strings.ToLower(strings.TrimSpace(k))
	if s == "" {
		return false
	}
	for _, p := range []string{
		"your-api-key", "your_api_key", "yourkey", "xxx", "sk-xxx", "sk-xxxx",
		"apikey", "api_key", "replace-me", "changeme", "todo", "test-key",
	} {
		if s == p || strings.Contains(s, p) {
			return true
		}
	}
	return false
}
