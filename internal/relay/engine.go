package relay

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"happy-relay/internal/store"
)

// ErrUnsupported 表示当前渠道类型不支持该能力。
var ErrUnsupported = errors.New("该渠道不支持此能力")

// Adapter 是某一类上游协议的适配器。
type Adapter interface {
	Chat(ctx context.Context, up Upstream, m store.Model, in *ChatInput, emit StreamFunc) (*ChatOutput, error)
	Image(ctx context.Context, up Upstream, m store.Model, in *ImageInput) (*ImageOutput, error)
	Embedding(ctx context.Context, up Upstream, m store.Model, in *EmbeddingInput) (*EmbeddingOutput, error)
	Speech(ctx context.Context, up Upstream, m store.Model, in *SpeechInput) (*SpeechOutput, error)
	Transcription(ctx context.Context, up Upstream, m store.Model, in *TranscriptionInput) (*TranscriptionOutput, error)
}

// Engine 负责解析模型、挑选渠道与入口、调用适配器并做故障转移。
type Engine struct {
	st       *store.Store
	client   *http.Client
	mu       sync.Mutex
	rr       map[int64]int // 渠道内轮询游标（按渠道 ID）
	rrModel  map[int64]int // 跨渠道轮询游标（按模型 ID）
	adapters map[string]Adapter
	breaker  *breakerGuard // 渠道级熔断
	stats    *statGuard    // 上游健康画像（持久化，路由自学习）
	// timeoutSec 是全局兜底超时（秒）。初始来自 RELAY_TIMEOUT，之后可由面板
	// 设置页热更新（SetDefaultTimeout）；渠道未单独配置 TimeoutSec 时生效。
	// 用原子值存秒数：读路径在每个请求上，不值得为它加锁。
	timeoutSec atomic.Int64

	// balMu 保护再平衡配置。配置由 api 层在设置保存后写入（热更新），
	// 读路径在每次选路时取快照，避免为一次读取去抢锁。
	balMu   sync.RWMutex
	balMode string
	balBad  int
	balStat map[int64]BalanceStat

	// UsableFilter 是可选的渠道可用性判定（由 api 层注入）：
	// 返回 false 的渠道在选路时直接跳过——典型场景是没配密钥、
	// 连通巡检也没通过过的内置渠道，调用它们注定 401，只会浪费
	// 故障转移的一次尝试。为 nil 时不做过滤（保持旧行为）。
	UsableFilter func(store.Provider) bool
}

func NewEngine(st *store.Store, breakerFails, breakerCoolSec, timeoutSec int) *Engine {
	dt := time.Duration(timeoutSec) * time.Second
	if dt <= 0 {
		dt = 10 * time.Minute
	}
	e := &Engine{
		st:       st,
		client:   &http.Client{Transport: PooledTransport()},
		rr:       map[int64]int{},
		rrModel:  map[int64]int{},
		breaker:  newBreakerGuard(breakerFails, breakerCoolSec),
		stats:    newStatGuard(st),
	}
	e.timeoutSec.Store(int64(dt / time.Second))
	e.adapters = map[string]Adapter{
		store.KindOpenAI:    &OpenAIAdapter{},
		store.KindZhipu:     &OpenAIAdapter{}, // 智谱 GLM 与 OpenAI 协议一致，仅端点与默认模型名不同
		store.KindGemini:    &GeminiAdapter{},
		store.KindAnthropic: &AnthropicAdapter{},
		store.KindTemplate:  &TemplateAdapter{},
	}
	return e
}

// SetBalance 更新负载再平衡配置。mode 为空视为关闭；
// stats 可以为 nil（此时只剩按模式降级排序）。由 api 层在设置保存与巡检结束后调用。
func (e *Engine) SetBalance(mode string, badPercent int, stats map[int64]BalanceStat) {
	e.balMu.Lock()
	defer e.balMu.Unlock()
	e.balMode = mode
	e.balBad = badPercent
	e.balStat = stats
}

// balance 返回当前再平衡配置的快照。返回的 map 只读，调用方不得修改。
func (e *Engine) balance() (string, int, map[int64]BalanceStat) {
	e.balMu.RLock()
	defer e.balMu.RUnlock()
	return e.balMode, e.balBad, e.balStat
}

// DefaultBaseURL 返回各协议在未填写入口地址时的官方默认地址。
func DefaultBaseURL(kind string) string {
	switch kind {
	case store.KindOpenAI:
		return "https://api.openai.com/v1"
	case store.KindZhipu:
		return "https://open.bigmodel.cn/api/paas/v4"
	case store.KindGemini:
		return "https://generativelanguage.googleapis.com/v1beta"
	case store.KindAnthropic:
		return "https://api.anthropic.com/v1"
	}
	return ""
}

// normalizeBase 去掉末尾斜杠，并在需要时补上版本前缀。
// noVersionPath 为真时表示上游接口直接挂在根路径下（例如 GitHub Models：
// models.inference.ai.azure.com/chat/completions），不再强补 /v1，
// 否则会被拼成 .../v1/chat/completions 而 404。
func normalizeBase(raw string, kind string, noVersionPath bool) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	// 部分 OpenAI 兼容端点没有版本前缀，标记后不再强补。
	if noVersionPath {
		return base
	}
	known := HasVersionPath(base)
	// 用户常只填到域名，这里按协议补齐默认路径。
	if kind == store.KindOpenAI && !known {
		base += "/v1"
	}
	// 智谱的接口挂在 /api/paas/v4 下，只填域名时补全。
	if kind == store.KindZhipu && !known {
		base += "/api/paas/v4"
	}
	return base
}

// HasVersionPath 判断地址路径里是否已经包含 API 版本段，例如 /v1、/v4、/v1beta。
// 命中时不再自动追加 /v1，否则会把 .../api/paas/v4 拼成 .../api/paas/v4/v1。
func HasVersionPath(base string) bool {
	lower := strings.ToLower(strings.TrimSpace(base))
	i := strings.Index(lower, "://")
	if i < 0 {
		return false
	}
	rest := lower[i+3:]
	j := strings.IndexByte(rest, '/')
	if j < 0 {
		return false
	}
	for _, seg := range strings.Split(rest[j:], "/") {
		// 形如 v1 / v2 / v4 / v1beta：v 之后必须以数字开头，其余只允许字母数字。
		if len(seg) < 2 || seg[0] != 'v' || seg[1] < '0' || seg[1] > '9' {
			continue
		}
		ok := true
		for _, r := range seg[2:] {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				continue
			}
			ok = false
			break
		}
		if ok {
			return true
		}
	}
	return false
}

// candidates 生成该渠道下 "(入口地址, 密钥)" 的候选调用列表，按渠道策略排序。
func (e *Engine) candidates(p store.Provider) []Upstream {
	bases := make([]string, 0, len(p.BaseURLs)+1)
	def := DefaultBaseURL(p.Kind)
	if len(p.BaseURLs) == 0 && def != "" {
		bases = append(bases, def)
	}
	for _, b := range p.BaseURLs {
		if nb := normalizeBase(b, p.Kind, p.NoVersionPath); nb != "" {
			bases = append(bases, nb)
		}
	}
	keys := p.APIKeys
	if len(keys) == 0 {
		keys = []string{""}
	}
	out := make([]Upstream, 0, len(bases)*len(keys))
	timeout := time.Duration(e.timeoutSec.Load()) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	for _, b := range bases {
		for _, k := range keys {
			out = append(out, Upstream{BaseURL: b, APIKey: k, Headers: p.Headers, Provider: p, Client: e.client, Timeout: timeout})
		}
	}
	if len(out) == 0 {
		return nil
	}
	switch p.Strategy {
	case "random":
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	case "failover":
		// 保持填写顺序，始终优先第一个入口
	default: // round_robin | weighted（渠道内候选权重相同，按轮询处理）
		e.mu.Lock()
		start := e.rr[p.ID] % len(out)
		e.rr[p.ID]++
		e.mu.Unlock()
		out = append(out[start:], out[:start]...)
	}
	return out
}

// clientFor 返回进程级共享的连接池客户端。超时不再挂在 Client 上
// （那会绑定整个客户端、无法共享），而是随 Upstream.Timeout 走 request context。
func (e *Engine) clientFor(p store.Provider) *http.Client {
	return e.client
}

// callCtx 给单次上游调用叠加全局兜底超时，语义与旧的 http.Client.Timeout 一致
// （覆盖整个请求-响应周期，含流式读取），但随 ctx 传播、允许共享客户端连接池。
func callCtx(ctx context.Context, up Upstream) (context.Context, context.CancelFunc) {
	if d := up.Timeout; d > 0 {
		return context.WithTimeout(withContext(ctx), d)
	}
	return withContext(ctx), func() {}
}

// SetDefaultTimeout 热更新全局兜底超时（秒）。由 api 层在面板设置保存后调用，
// 无需重启即可对「未单独配置超时的渠道」生效。
func (e *Engine) SetDefaultTimeout(sec int) {
	if sec <= 0 {
		sec = 600 // 与 NewEngine 的兜底一致：10 分钟
	}
	e.timeoutSec.Store(int64(sec))
}

// Resolve 按对外模型名解析出模型与首个渠道（兼容旧调用方）。
func (e *Engine) Resolve(name string) (store.Model, store.Provider, error) {
	m, ps, err := e.ResolveAll(name)
	if err != nil {
		return store.Model{}, store.Provider{}, err
	}
	return m, ps[0], nil
}

// ResolveAll 按对外模型名解析出模型及其绑定的全部可用渠道（保持绑定顺序）。
func (e *Engine) ResolveAll(name string) (store.Model, []store.Provider, error) {
	m, err := e.st.ResolveModel(name)
	if err != nil {
		return store.Model{}, nil, err
	}
	ps, err := e.providersFor(m)
	if err != nil {
		return store.Model{}, nil, err
	}
	return m, ps, nil
}

// providersFor 加载模型绑定的渠道，跳过已删除或已停用的项。
func (e *Engine) providersFor(m store.Model) ([]store.Provider, error) {
	ids := m.AllProviderIDs()
	out := make([]store.Provider, 0, len(ids))
	var disabled []string
	for _, id := range ids {
		p, err := e.st.GetProvider(id)
		if err != nil {
			continue
		}
		if !p.Enabled {
			disabled = append(disabled, p.Name)
			continue
		}
		if e.UsableFilter != nil && !e.UsableFilter(p) {
			// 渠道启用但「不可能调通」（如内置渠道未配密钥），与停用同等对待。
			disabled = append(disabled, p.Name+"（未配置密钥或连通测试未通过）")
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		if len(disabled) > 0 {
			return nil, errors.New("该模型绑定的渠道均已停用：" + strings.Join(disabled, "、"))
		}
		return nil, errors.New("该模型未绑定任何可用渠道，请在「模型」里选择渠道")
	}
	return out, nil
}

// orderProviders 按模型的跨渠道调度策略调整尝试顺序：
// failover（默认）按绑定顺序；round_robin 依次轮转；random 随机打乱。
func (e *Engine) orderProviders(m store.Model, ps []store.Provider) []store.Provider {
	out := append([]store.Provider(nil), ps...)
	if len(out) <= 1 {
		return out
	}
	switch m.Config.RouteStrategy {
	case "random":
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	case "round_robin":
		e.mu.Lock()
		start := e.rrModel[m.ID] % len(out)
		e.rrModel[m.ID]++
		e.mu.Unlock()
		out = append(out[start:], out[:start]...)
	default: // failover
	}
	return out
}

// Target 是一次调用的目标：某个渠道下的某个具体上游模型。
// 一个对外模型可以在同一个渠道里挂多个上游模型，运行时按配置顺序逐个尝试。
type Target struct {
	Provider     store.Provider
	UpstreamName string
}

// expandTargets 把模型的输入列表展开成调用目标。
// 同一个渠道可以挂多个上游模型，因此这里以「输入」为主序；
// needInputs 非空时只保留输入模态匹配的目标，例如带图的请求只走视觉模型。
// 纯文本请求额外做「成本优先」稳定排序：多模态聚合模型里，纯文本流量
// 先走纯文本上游（更便宜更快），多模态上游只做兜底——带图/音频请求不受影响。
func (e *Engine) expandTargets(m store.Model, ps []store.Provider, needInputs []string) []Target {
	byID := make(map[int64]store.Provider, len(ps))
	for _, p := range ps {
		byID[p.ID] = p
	}
	all := make([]Target, 0, len(m.Inputs))
	matched := make([]Target, 0, len(m.Inputs))
	for _, in := range m.AllInputs() {
		p, ok := byID[in.ProviderID]
		if !ok {
			continue // 渠道已删除或已停用
		}
		t := Target{Provider: p, UpstreamName: in.UpstreamName}
		all = append(all, t)
		if inputModalitiesMatch(in, m.Capability, needInputs) {
			matched = append(matched, t)
		}
	}
	if len(needInputs) == 0 {
		return all
	}
	matched = preferTextOnlyTargets(m, matched, needInputs)
	return matched
}

// preferTextOnlyTargets 在纯文本请求时，把「只会吃纯文本」的上游稳定地排到
// 多模态上游前面。每组内部仍保持配置顺序，failover 语义不变：
// 文本上游全挂了，请求依然会落到多模态上游兜底。
func preferTextOnlyTargets(m store.Model, ts []Target, need []string) []Target {
	if len(ts) <= 1 || len(need) != 1 || need[0] != store.ModalityText {
		return ts
	}
	textOnly := make([]Target, 0, len(ts))
	multi := make([]Target, 0, len(ts))
	for _, t := range ts {
		found := false
		for _, in := range m.AllInputs() {
			if in.ProviderID == t.Provider.ID && in.UpstreamName == t.UpstreamName {
				supported, _ := in.Modalities(m.Capability)
				found = len(supported) == 1 && supported[0] == store.ModalityText
				break
			}
		}
		if found {
			textOnly = append(textOnly, t)
		} else {
			multi = append(multi, t)
		}
	}
	if len(textOnly) == 0 || len(multi) == 0 {
		return ts
	}
	return append(textOnly, multi...)
}

// inputModalitiesMatch 判断某个上游输入能否处理本次请求的输入模态。
func inputModalitiesMatch(in store.ModelInput, capability string, need []string) bool {
	if len(need) == 0 {
		return true
	}
	supported, _ := in.Modalities(capability)
	for _, want := range need {
		found := false
		for _, has := range supported {
			if has == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// orderTargets 先按调度策略排序，再按再平衡策略重排，然后按画像把
// 「最近连续失败」的上游稳定地排到队尾（自学习），最后剔除熔断中的渠道；
// 若全部处于熔断状态则忽略熔断，避免出现完全不可用的死局。
func (e *Engine) orderTargets(m store.Model, ts []Target) []Target {
	ordered := e.orderTargetList(m, ts)
	// 再平衡放在熔断过滤之前：健康的渠道先排到前面，再剔除正在冷却的，顺序才符合预期。
	mode, bad, stats := e.balance()
	ordered = rebalanceTargets(ordered, mode, stats, float64(bad))
	// 画像降权：连续失败 >= 3 的上游让路给健康的，但只是排尾不剔除，
	// 与渠道级熔断（硬切断）形成两级容错。
	ordered = e.stats.deprioritize(ordered)
	if filtered := e.filterOpenTargets(ordered); len(filtered) > 0 {
		return filtered
	}
	return ordered
}

// orderTargetList 按模型的调度策略调整目标顺序：
// failover（默认）保持配置顺序；round_robin 依次轮转；random 随机打乱；
// weighted 按各渠道权重展开后轮转，权重高的渠道被选中概率更大。
func (e *Engine) orderTargetList(m store.Model, ts []Target) []Target {
	out := append([]Target(nil), ts...)
	if len(out) <= 1 {
		return out
	}
	switch m.Config.RouteStrategy {
	case "random":
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	case "round_robin":
		e.mu.Lock()
		start := e.rrModel[m.ID] % len(out)
		e.rrModel[m.ID]++
		e.mu.Unlock()
		out = append(out[start:], out[:start]...)
	case "weighted":
		out = expandWeightedTargets(out)
		e.mu.Lock()
		start := e.rrModel[m.ID] % len(out)
		e.rrModel[m.ID]++
		e.mu.Unlock()
		out = append(out[start:], out[:start]...)
	default: // failover
	}
	return out
}

// expandWeightedTargets 按渠道权重把目标重复展开；权重 <=0 视为 1。
// 展开后与 round_robin 配合，即可实现按权重的近似加权调度。
func expandWeightedTargets(ts []Target) []Target {
	total := 0
	for _, t := range ts {
		w := t.Provider.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	out := make([]Target, 0, total)
	for _, t := range ts {
		w := t.Provider.Weight
		if w <= 0 {
			w = 1
		}
		for i := 0; i < w; i++ {
			out = append(out, t)
		}
	}
	return out
}

// filterOpenTargets 过滤掉处于熔断冷却中的渠道所对应的目标。
func (e *Engine) filterOpenTargets(ts []Target) []Target {
	if !e.breaker.enabled() {
		return ts
	}
	out := make([]Target, 0, len(ts))
	for _, t := range ts {
		if e.breaker.allow(t.Provider.ID) {
			out = append(out, t)
		}
	}
	return out
}

// ChatInputModalities 返回一次对话请求实际用到的输入模态。
// 只要任意一条消息带了图片，就必须交给支持读图的上游处理。
func ChatInputModalities(in *ChatInput) []string {
	mods := []string{store.ModalityText}
	if in == nil {
		return mods
	}
	for _, msg := range in.Messages {
		for _, part := range msg.Parts {
			if part.Type == "image" {
				return append(mods, store.ModalityImage)
			}
		}
	}
	return mods
}

// adapterFor 按当前渠道类型选择适配器；视频能力固定走模板适配器。
func (e *Engine) adapterFor(kind, capability string) (Adapter, error) {
	if capability == store.CapChat || capability == store.CapImage || capability == store.CapEmbedding ||
		capability == store.CapSpeech || capability == store.CapTranscription {
		if kind == store.KindTemplate {
			return e.adapters[store.KindTemplate], nil
		}
		a, ok := e.adapters[kind]
		if !ok {
			return nil, errors.New("未知的渠道类型：" + kind)
		}
		return a, nil
	}
	if capability == store.CapVideo {
		return e.adapters[store.KindTemplate], nil
	}
	return nil, ErrUnsupported
}

// Chat 执行一次对话调用：先在渠道内切换入口/密钥，再跨渠道切换。
// 一旦流式内容已经开始下发，则立即停止切换，避免客户端收到重复内容。
func (e *Engine) Chat(ctx context.Context, m store.Model, ps []store.Provider, in *ChatInput, emit StreamFunc) (*ChatOutput, store.Provider, error) {
	mergeDefaults(m, in)
	if sp := m.Config.SystemPrompt; sp != "" && in.SystemPrompt() == "" {
		in.Messages = append([]ChatMessage{{Role: "system", Content: sp}}, in.Messages...)
	}
	var lastErr error
	var lastProvider store.Provider
	targets := e.expandTargets(m, ps, ChatInputModalities(in))
	if len(targets) == 0 {
		return nil, store.Provider{},
			errors.New("该模型没有能处理本次输入模态的上游，请为它添加支持图片输入的模型")
	}
	for _, t := range e.orderTargets(m, targets) {
		p := t.Provider
		lastProvider = p
		a, err := e.adapterFor(p.Kind, store.CapChat)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			e.stats.report(p.ID, t.UpstreamName, false, 0, err.Error())
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			e.stats.report(p.ID, t.UpstreamName, false, 0, lastErr.Error())
			continue
		}
		// 同一个渠道里可能挂了多个上游模型，这里用当前目标自己的上游名。
		mu := m
		mu.UpstreamName = t.UpstreamName
		var providerErr error
		attemptStart := time.Now()
		for _, up := range cands {
			emitted := false
			var wrapped StreamFunc
			if emit != nil {
				wrapped = func(d StreamDelta) error {
					emitted = true
					return emit(d)
				}
			}
			cctx, cancel := callCtx(ctx, up)
			out, err := a.Chat(cctx, up, mu, in, wrapped)
			cancel()
			if err == nil {
				e.breaker.report(p.ID, nil)
				e.stats.report(p.ID, t.UpstreamName, true, time.Since(attemptStart).Milliseconds(), "")
				out.UpstreamName = t.UpstreamName // 回填实际命中的上游，供调试台展示路由结果
				return out, up.Provider, nil
			}
			providerErr = err
			lastErr = err
			if emitted {
				return nil, up.Provider, err
			}
			if ctx != nil && ctx.Err() != nil {
				return nil, up.Provider, err
			}
		}
		e.breaker.report(p.ID, providerErr)
		e.stats.report(p.ID, t.UpstreamName, false, time.Since(attemptStart).Milliseconds(), errText(providerErr))
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的上游渠道")
	}
	return nil, lastProvider, lastErr
}

// Image 执行一次图片生成调用，失败时在渠道与入口之间自动切换。
func (e *Engine) Image(ctx context.Context, m store.Model, ps []store.Provider, in *ImageInput) (*ImageOutput, store.Provider, error) {
	mergeImageDefaults(m, in)
	// 带参考图时要求上游支持图片输入（图生图 / 多图合成）。
	need := []string{store.ModalityText}
	if len(in.Images) > 0 {
		need = append(need, store.ModalityImage)
	}
	targets := e.expandTargets(m, ps, need)
	if len(targets) == 0 {
		return nil, store.Provider{},
			errors.New("该模型没有能处理本次输入的上游，请为它添加支持参考图输入的模型")
	}
	var lastErr error
	var lastProvider store.Provider
	for _, t := range e.orderTargets(m, targets) {
		p := t.Provider
		lastProvider = p
		a, err := e.adapterFor(p.Kind, store.CapImage)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			e.stats.report(p.ID, t.UpstreamName, false, 0, err.Error())
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			e.stats.report(p.ID, t.UpstreamName, false, 0, lastErr.Error())
			continue
		}
		mu := m
		mu.UpstreamName = t.UpstreamName
		var providerErr error
		attemptStart := time.Now()
		for _, up := range cands {
			cctx, cancel := callCtx(ctx, up)
			out, err := a.Image(cctx, up, mu, in)
			cancel()
			if err == nil {
				e.breaker.report(p.ID, nil)
				e.stats.report(p.ID, t.UpstreamName, true, time.Since(attemptStart).Milliseconds(), "")
				return out, up.Provider, nil
			}
			providerErr = err
			lastErr = err
			if ctx != nil && ctx.Err() != nil {
				return nil, up.Provider, err
			}
		}
		e.breaker.report(p.ID, providerErr)
		e.stats.report(p.ID, t.UpstreamName, false, time.Since(attemptStart).Milliseconds(), errText(providerErr))
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的上游渠道")
	}
	return nil, lastProvider, lastErr
}

// Embedding 执行一次向量化调用，失败时在渠道与入口之间自动切换。
func (e *Engine) Embedding(ctx context.Context, m store.Model, ps []store.Provider, in *EmbeddingInput) (*EmbeddingOutput, store.Provider, error) {
	var lastErr error
	var lastProvider store.Provider
	for _, t := range e.orderTargets(m, e.expandTargets(m, ps, nil)) {
		p := t.Provider
		lastProvider = p
		a, err := e.adapterFor(p.Kind, store.CapEmbedding)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			e.stats.report(p.ID, t.UpstreamName, false, 0, err.Error())
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			e.stats.report(p.ID, t.UpstreamName, false, 0, lastErr.Error())
			continue
		}
		mu := m
		mu.UpstreamName = t.UpstreamName
		var providerErr error
		attemptStart := time.Now()
		for _, up := range cands {
			cctx, cancel := callCtx(ctx, up)
			out, err := a.Embedding(cctx, up, mu, in)
			cancel()
			if err == nil {
				e.breaker.report(p.ID, nil)
				e.stats.report(p.ID, t.UpstreamName, true, time.Since(attemptStart).Milliseconds(), "")
				return out, up.Provider, nil
			}
			providerErr = err
			lastErr = err
			if ctx != nil && ctx.Err() != nil {
				return nil, up.Provider, err
			}
		}
		e.breaker.report(p.ID, providerErr)
		e.stats.report(p.ID, t.UpstreamName, false, time.Since(attemptStart).Milliseconds(), errText(providerErr))
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的上游渠道")
	}
	return nil, lastProvider, lastErr
}

// runAudio 是语音类能力的通用故障转移循环：渠道 → 入口 → 密钥逐层尝试，并复用渠道级熔断。
func runAudio[T any](e *Engine, ctx context.Context, m store.Model, ps []store.Provider, capability string,
	call func(ctx context.Context, a Adapter, up Upstream, mu store.Model) (T, error)) (T, store.Provider, error) {
	var zero T
	var lastErr error
	var lastProvider store.Provider
	for _, t := range e.orderTargets(m, e.expandTargets(m, ps, nil)) {
		p := t.Provider
		lastProvider = p
		a, err := e.adapterFor(p.Kind, capability)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			e.stats.report(p.ID, t.UpstreamName, false, 0, err.Error())
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			e.stats.report(p.ID, t.UpstreamName, false, 0, lastErr.Error())
			continue
		}
		mu := m
		mu.UpstreamName = t.UpstreamName
		var providerErr error
		attemptStart := time.Now()
		for _, up := range cands {
			cctx, cancel := callCtx(ctx, up)
			out, err := call(cctx, a, up, mu)
			cancel()
			if err == nil {
				e.breaker.report(p.ID, nil)
				e.stats.report(p.ID, t.UpstreamName, true, time.Since(attemptStart).Milliseconds(), "")
				return out, up.Provider, nil
			}
			providerErr = err
			lastErr = err
			if ctx != nil && ctx.Err() != nil {
				return zero, up.Provider, err
			}
		}
		e.breaker.report(p.ID, providerErr)
		e.stats.report(p.ID, t.UpstreamName, false, time.Since(attemptStart).Milliseconds(), errText(providerErr))
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的上游渠道")
	}
	return zero, lastProvider, lastErr
}

// Speech 执行一次语音合成调用，失败时在渠道与入口之间自动切换。
// 返回的音频流由调用方负责关闭。
func (e *Engine) Speech(ctx context.Context, m store.Model, ps []store.Provider, in *SpeechInput) (*SpeechOutput, store.Provider, error) {
	mergeSpeechDefaults(m, in)
	return runAudio(e, ctx, m, ps, store.CapSpeech,
		func(c context.Context, a Adapter, up Upstream, mu store.Model) (*SpeechOutput, error) {
			return a.Speech(c, up, mu, in)
		})
}

// Transcription 执行一次语音识别调用，失败时在渠道与入口之间自动切换。
func (e *Engine) Transcription(ctx context.Context, m store.Model, ps []store.Provider, in *TranscriptionInput) (*TranscriptionOutput, store.Provider, error) {
	return runAudio(e, ctx, m, ps, store.CapTranscription,
		func(c context.Context, a Adapter, up Upstream, mu store.Model) (*TranscriptionOutput, error) {
			return a.Transcription(c, up, mu, in)
		})
}

// mergeDefaults 把模型配置里的默认参数补进请求（不覆盖用户显式传入的值）。
func mergeDefaults(m store.Model, in *ChatInput) {
	for k, v := range m.Config.Defaults {
		if _, exists := in.Options[k]; !exists {
			in.Options[k] = v
		}
	}
}

func mergeImageDefaults(m store.Model, in *ImageInput) {
	if in.Options == nil {
		in.Options = map[string]any{}
	}
	for k, v := range m.Config.Defaults {
		if _, exists := in.Options[k]; !exists {
			in.Options[k] = v
		}
	}
	if in.Size == "" {
		if s, ok := in.Options["size"].(string); ok {
			in.Size = s
		}
	}
	if in.N == 0 {
		if n, ok := in.Options["n"].(float64); ok {
			in.N = int(n)
		}
	}
}

// mergeSpeechDefaults 把模型默认参数补进语音请求：
// 音色、语速、音频格式这类参数很适合配在模型上，调用方就不用每次传。
func mergeSpeechDefaults(m store.Model, in *SpeechInput) {
	if in.Options == nil {
		in.Options = map[string]any{}
	}
	for k, v := range m.Config.Defaults {
		if _, exists := in.Options[k]; !exists {
			in.Options[k] = v
		}
	}
	if in.Voice == "" {
		if s, ok := in.Options["voice"].(string); ok {
			in.Voice = s
		}
	}
	if in.Format == "" {
		if s, ok := in.Options["response_format"].(string); ok {
			in.Format = s
		}
	}
}

// HTTPClient 暴露底层客户端，供连通性测试等场景使用。
func (e *Engine) HTTPClient(p store.Provider) *http.Client { return e.clientFor(p) }

// Upstreams 暴露候选列表，供面板做连通性测试。
func (e *Engine) Upstreams(p store.Provider) []Upstream { return e.candidates(p) }
