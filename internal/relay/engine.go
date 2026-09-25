package relay

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strings"
	"sync"
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
}

func NewEngine(st *store.Store, breakerFails, breakerCoolSec int) *Engine {
	e := &Engine{
		st:       st,
		client:   &http.Client{},
		rr:       map[int64]int{},
		rrModel:  map[int64]int{},
		breaker:  newBreakerGuard(breakerFails, breakerCoolSec),
	}
	e.adapters = map[string]Adapter{
		store.KindOpenAI:    &OpenAIAdapter{},
		store.KindGemini:    &GeminiAdapter{},
		store.KindAnthropic: &AnthropicAdapter{},
		store.KindTemplate:  &TemplateAdapter{},
	}
	return e
}

// DefaultBaseURL 返回各协议在未填写入口地址时的官方默认地址。
func DefaultBaseURL(kind string) string {
	switch kind {
	case store.KindOpenAI:
		return "https://api.openai.com/v1"
	case store.KindGemini:
		return "https://generativelanguage.googleapis.com/v1beta"
	case store.KindAnthropic:
		return "https://api.anthropic.com/v1"
	}
	return ""
}

// normalizeBase 去掉末尾斜杠，并在需要时补上 /v1。
func normalizeBase(raw string, kind string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	// 用户常只填到域名，这里按协议补齐默认路径。
	if kind == store.KindOpenAI && !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		if !strings.Contains(base, "/v2") && !strings.Contains(base, "/v3") {
			base += "/v1"
		}
	}
	return base
}

// candidates 生成该渠道下 "(入口地址, 密钥)" 的候选调用列表，按渠道策略排序。
func (e *Engine) candidates(p store.Provider) []Upstream {
	bases := make([]string, 0, len(p.BaseURLs)+1)
	def := DefaultBaseURL(p.Kind)
	if len(p.BaseURLs) == 0 && def != "" {
		bases = append(bases, def)
	}
	for _, b := range p.BaseURLs {
		if nb := normalizeBase(b, p.Kind); nb != "" {
			bases = append(bases, nb)
		}
	}
	keys := p.APIKeys
	if len(keys) == 0 {
		keys = []string{""}
	}
	out := make([]Upstream, 0, len(bases)*len(keys))
	client := e.clientFor(p)
	for _, b := range bases {
		for _, k := range keys {
			out = append(out, Upstream{BaseURL: b, APIKey: k, Headers: p.Headers, Provider: p, Client: client})
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
	default: // round_robin
		e.mu.Lock()
		start := e.rr[p.ID] % len(out)
		e.rr[p.ID]++
		e.mu.Unlock()
		out = append(out[start:], out[:start]...)
	}
	return out
}

func (e *Engine) clientFor(p store.Provider) *http.Client {
	timeout := time.Duration(p.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &http.Client{Timeout: timeout}
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

// adapterFor 按当前渠道类型选择适配器；视频能力固定走模板适配器。
func (e *Engine) adapterFor(kind, capability string) (Adapter, error) {
	if capability == store.CapChat || capability == store.CapImage || capability == store.CapEmbedding {
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
	for _, p := range e.orderForTry(m, ps) {
		lastProvider = p
		a, err := e.adapterFor(p.Kind, store.CapChat)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			continue
		}
		// 同一个对外模型在不同渠道上可以对应不同的上游模型名。
		mu := m
		mu.UpstreamName = m.UpstreamNameFor(p.ID)
		var providerErr error
		for _, up := range cands {
			emitted := false
			var wrapped StreamFunc
			if emit != nil {
				wrapped = func(d StreamDelta) error {
					emitted = true
					return emit(d)
				}
			}
			out, err := a.Chat(withContext(ctx), up, mu, in, wrapped)
			if err == nil {
				e.breaker.report(p.ID, nil)
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
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的上游渠道")
	}
	return nil, lastProvider, lastErr
}

// Image 执行一次图片生成调用，失败时在渠道与入口之间自动切换。
func (e *Engine) Image(ctx context.Context, m store.Model, ps []store.Provider, in *ImageInput) (*ImageOutput, store.Provider, error) {
	mergeImageDefaults(m, in)
	var lastErr error
	var lastProvider store.Provider
	for _, p := range e.orderForTry(m, ps) {
		lastProvider = p
		a, err := e.adapterFor(p.Kind, store.CapImage)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			continue
		}
		mu := m
		mu.UpstreamName = m.UpstreamNameFor(p.ID)
		var providerErr error
		for _, up := range cands {
			out, err := a.Image(withContext(ctx), up, mu, in)
			if err == nil {
				e.breaker.report(p.ID, nil)
				return out, up.Provider, nil
			}
			providerErr = err
			lastErr = err
			if ctx != nil && ctx.Err() != nil {
				return nil, up.Provider, err
			}
		}
		e.breaker.report(p.ID, providerErr)
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
	for _, p := range e.orderForTry(m, ps) {
		lastProvider = p
		a, err := e.adapterFor(p.Kind, store.CapEmbedding)
		if err != nil {
			lastErr = err
			e.breaker.report(p.ID, err)
			continue
		}
		cands := e.candidates(p)
		if len(cands) == 0 {
			lastErr = errors.New("渠道「" + p.Name + "」未配置任何入口地址")
			e.breaker.report(p.ID, lastErr)
			continue
		}
		mu := m
		mu.UpstreamName = m.UpstreamNameFor(p.ID)
		var providerErr error
		for _, up := range cands {
			out, err := a.Embedding(withContext(ctx), up, mu, in)
			if err == nil {
				e.breaker.report(p.ID, nil)
				return out, up.Provider, nil
			}
			providerErr = err
			lastErr = err
			if ctx != nil && ctx.Err() != nil {
				return nil, up.Provider, err
			}
		}
		e.breaker.report(p.ID, providerErr)
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的上游渠道")
	}
	return nil, lastProvider, lastErr
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

// HTTPClient 暴露底层客户端，供连通性测试等场景使用。
func (e *Engine) HTTPClient(p store.Provider) *http.Client { return e.clientFor(p) }

// Upstreams 暴露候选列表，供面板做连通性测试。
func (e *Engine) Upstreams(p store.Provider) []Upstream { return e.candidates(p) }
