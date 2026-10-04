package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------- 渠道（上游） ----------

// 渠道类型：决定使用哪个协议适配器。
const (
	KindOpenAI    = "openai"    // OpenAI 兼容（/v1/chat/completions 等）
	KindZhipu     = "zhipu"     // 智谱 GLM（/api/paas/v4，协议与 OpenAI 一致）
	KindGemini    = "gemini"    // Google Gemini 原生 generateContent
	KindAnthropic = "anthropic" // Anthropic Claude 原生 Messages
	KindTemplate  = "template"  // 模板驱动，可接任意 HTTP 上游
)

// Provider 是一个上游服务商，可挂多个入口地址与多个密钥。
type Provider struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	BaseURLs      []string          `json:"base_urls"`
	APIKeys       []string          `json:"api_keys"`
	Headers       map[string]string `json:"headers"`
	Strategy      string            `json:"strategy"` // round_robin | random | failover | weighted
	TimeoutSec    int               `json:"timeout_sec"`
	Weight        int               `json:"weight"` // 加权负载均衡权重，<=0 视为 1
	Enabled       bool              `json:"enabled"`
	Remark        string            `json:"remark"`
	NoVersionPath bool              `json:"no_version_path"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`

	ModelCount int `json:"model_count,omitempty"`
}

func (s *Store) ListProviders() ([]Provider, error) {
	// 渠道表整体读一次，顺手把快照喂给 GetProvider 的缓存：
	// 面板刷新一次后，后续 /v1 调用都能命中内存。
	rows, err := s.db.Query(`SELECT id,name,kind,base_urls,api_keys,headers,strategy,
		timeout_sec,enabled,remark,weight,no_version_path,created_at,updated_at,0 FROM providers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	out := []Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Provider, len(out))
	for _, p := range out {
		byID[p.ID] = p
	}
	s.storeProvidersSnapshot(byID)
	counts, err := s.providerModelCounts()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ModelCount = counts[out[i].ID]
	}
	return out, nil
}

// providerModelCounts 统计每个渠道被多少个模型引用（含多渠道绑定的模型）。
func (s *Store) providerModelCounts() (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT provider_id,provider_ids FROM models`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var primary int64
		var raw string
		if err := rows.Scan(&primary, &raw); err != nil {
			return nil, err
		}
		ids := cleanIDs(unmarshal(raw, []int64{}))
		if len(ids) == 0 && primary > 0 {
			ids = []int64{primary}
		}
		for _, id := range ids {
			counts[id]++
		}
	}
	return counts, rows.Err()
}

func (s *Store) GetProvider(id int64) (Provider, error) {
	// 渠道缓存：一次 /v1 调用里 ResolveAll 会对每个上游调一次本函数，
	// 命中内存比走单连接库便宜一个数量级。
	// 只认"这一条我确实缓存过"——缺席不等于不存在，miss 仍要查库。
	if p, ok := s.lookupProvider(id); ok {
		return p, nil
	}
	row := s.db.QueryRow(`SELECT id,name,kind,base_urls,api_keys,headers,strategy,timeout_sec,
		enabled,remark,weight,no_version_path,created_at,updated_at,0 FROM providers WHERE id=?`, id)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err == nil {
		// 顺手回填：ResolveAll 会连续问好几个渠道，第二个开始就命中内存。
		// 注意这只是局部命中，不算完整快照（providersComplete 仍为 false），
		// 否则查不到的其他渠道会被误判成"不存在"。
		s.cacheProvider(p)
	}
	return p, err
}

// lookupProvider 按 id 查渠道缓存；版本号不匹配或未缓存时返回 ok=false。
func (s *Store) lookupProvider(id int64) (Provider, bool) {
	gen := s.gen.Load()
	s.providersMu.Lock()
	defer s.providersMu.Unlock()
	if s.providersGen != gen || s.providersByID == nil {
		return Provider{}, false
	}
	p, hit := s.providersByID[id]
	return p, hit
}

// cacheProvider 回填单个渠道到缓存。版本号已变的场合直接丢弃，避免缓存旧值。
func (s *Store) cacheProvider(p Provider) {
	gen := s.gen.Load()
	s.providersMu.Lock()
	defer s.providersMu.Unlock()
	if s.providersGen != gen || s.providersByID == nil {
		if s.gen.Load() != gen {
			return
		}
		s.providersByID = make(map[int64]Provider, 8)
		s.providersGen = gen
	}
	s.providersByID[p.ID] = p
}

// storeProvidersSnapshot 落完整渠道快照（来自 ListProviders 的全表读）。
func (s *Store) storeProvidersSnapshot(byID map[int64]Provider) {
	gen := s.gen.Load()
	s.providersMu.Lock()
	defer s.providersMu.Unlock()
	if s.gen.Load() != gen {
		return
	}
	s.providersByID = byID
	s.providersGen = gen
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(sc scanner) (Provider, error) {
	var p Provider
	var baseURLs, apiKeys, headers string
	var enabled, noVersionPath int
	if err := sc.Scan(&p.ID, &p.Name, &p.Kind, &baseURLs, &apiKeys, &headers, &p.Strategy,
		&p.TimeoutSec, &enabled, &p.Remark, &p.Weight, &noVersionPath, &p.CreatedAt, &p.UpdatedAt, &p.ModelCount); err != nil {
		return Provider{}, err
	}
	p.BaseURLs = unmarshal(baseURLs, []string{})
	p.APIKeys = unmarshal(apiKeys, []string{})
	p.Headers = unmarshal(headers, map[string]string{})
	p.Enabled = enabled == 1
	p.NoVersionPath = noVersionPath == 1
	return p, nil
}

func (s *Store) SaveProvider(p Provider) (Provider, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, errors.New("渠道名称不能为空")
	}
	if p.Kind == "" {
		p.Kind = KindOpenAI
	}
	p.BaseURLs = cleanList(p.BaseURLs)
	p.APIKeys = cleanList(p.APIKeys)
	if p.Strategy == "" {
		p.Strategy = "round_robin"
	}
	if p.TimeoutSec <= 0 {
		p.TimeoutSec = 600
	}
	if p.Weight <= 0 {
		p.Weight = 1
	}
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	ts := now()
	if p.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO providers
			(name,kind,base_urls,api_keys,headers,strategy,timeout_sec,weight,enabled,remark,no_version_path,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Name, p.Kind, marshal(p.BaseURLs), marshal(p.APIKeys), marshal(p.Headers),
			p.Strategy, p.TimeoutSec, p.Weight, boolInt(p.Enabled), p.Remark, boolInt(p.NoVersionPath), ts, ts)
		if err != nil {
			return p, err
		}
		p.ID, _ = res.LastInsertId()
		p.CreatedAt, p.UpdatedAt = ts, ts
		s.bumpGen() // 写操作后让模型/目录读缓存失效
		return p, nil
	}
	_, err := s.db.Exec(`UPDATE providers SET name=?,kind=?,base_urls=?,api_keys=?,headers=?,
		strategy=?,timeout_sec=?,weight=?,enabled=?,remark=?,no_version_path=?,updated_at=? WHERE id=?`,
		p.Name, p.Kind, marshal(p.BaseURLs), marshal(p.APIKeys), marshal(p.Headers),
		p.Strategy, p.TimeoutSec, p.Weight, boolInt(p.Enabled), p.Remark, boolInt(p.NoVersionPath), ts, p.ID)
	if err != nil {
		return p, err
	}
	p.UpdatedAt = ts
	s.bumpGen() // 写操作后让模型/目录读缓存失效
	return p, nil
}

func (s *Store) DeleteProvider(id int64) error {
	// 多渠道绑定时，先把该渠道从模型的绑定列表里摘除，
	// 只有「仅绑定该渠道」的模型才会被外键级联删除。
	if err := s.detachProviderFromModels(id); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM providers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.bumpGen() // 写操作后让模型/目录读缓存失效
	return nil
}

// detachProviderFromModels 把指定渠道从所有模型的绑定列表中移除。
// 若模型还绑定了其它渠道，则保留该模型并改写主渠道；否则保持原样交给外键级联删除。
func (s *Store) detachProviderFromModels(id int64) error {
	rows, err := s.db.Query(`SELECT id,provider_id,provider_ids FROM models`)
	if err != nil {
		return err
	}
	type bound struct {
		id      int64
		primary int64
		ids     []int64
	}
	var items []bound
	for rows.Next() {
		var modelID, primary int64
		var raw string
		if err := rows.Scan(&modelID, &primary, &raw); err != nil {
			rows.Close()
			return err
		}
		ids := cleanIDs(unmarshal(raw, []int64{}))
		if len(ids) == 0 && primary > 0 {
			ids = []int64{primary}
		}
		items = append(items, bound{id: modelID, primary: primary, ids: ids})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		remain := make([]int64, 0, len(it.ids))
		for _, v := range it.ids {
			if v != id {
				remain = append(remain, v)
			}
		}
		if len(remain) == len(it.ids) || len(remain) == 0 {
			continue
		}
		if _, err := s.db.Exec(`UPDATE models SET provider_id=?,provider_ids=? WHERE id=?`,
			remain[0], marshal(remain), it.id); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 对外模型 ----------

// 能力类型。
const (
	CapChat      = "chat"
	CapImage     = "image"
	CapVideo     = "video"
	CapEmbedding = "embedding"
	// CapSpeech 是文本转语音（语音合成），CapTranscription 是语音转文字（语音识别）。
	// 两者方向相反，实际上游通常由不同模型承担，因此拆成两种能力分别配置与路由。
	CapSpeech        = "speech"
	CapTranscription = "transcription"
)

// TemplateSpec 描述如何把统一的 v1 请求翻译成一次上游 HTTP 调用。
// 用于视频任务与自定义上游，占位符形如 {{prompt}}、{{model}}，取值路径支持 a.b[0].c。
type TemplateSpec struct {
	Method string `json:"method,omitempty"` // POST / GET，默认 POST
	// Path 相对 base_url；若以 http(s):// 开头则视为绝对地址直接使用，
	// 便于对接「创建在 /v1 下、查询在根路径」的上游（如 Agnes 的 /agnesapi）。
	Path     string            `json:"path,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"` // 额外请求头
	Body     map[string]any    `json:"body,omitempty"`    // 请求体模板
	Query    map[string]string `json:"query,omitempty"`   // URL 查询参数模板
	Response ResponseMap       `json:"response,omitempty"`
	// OmitEmpty 为真时，渲染后剔除请求体中值为空串、空数组或 null 的字段。
	// 用于「不同模式只允许传对应参数、传了空值会报错」的上游。
	OmitEmpty bool `json:"omit_empty,omitempty"`
}

// ResponseMap 描述从上游响应 JSON 中取值的方式。
type ResponseMap struct {
	TaskID   string `json:"task_id,omitempty"`
	Status   string `json:"status,omitempty"`
	Progress string `json:"progress,omitempty"`
	VideoURL string `json:"video_url,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
	// 状态归一化：把上游状态值映射到 queued/running/succeeded/failed
	StatusMap map[string]string `json:"status_map,omitempty"`
	// 未命中 StatusMap 时的兜底判定
	SuccessValue string `json:"success_value,omitempty"`
	PendingValue string `json:"pending_value,omitempty"`
	FailedValue  string `json:"failed_value,omitempty"`
}

// ModelConfig 是模型的适配参数。
type ModelConfig struct {
	SystemPrompt string         `json:"system_prompt,omitempty"`
	Defaults     map[string]any `json:"defaults,omitempty"` // 合并进上游请求体的默认参数
	// RouteStrategy 决定一个对外模型绑定多个渠道时的调度方式：
	// failover（默认，按绑定顺序逐个尝试）| round_robin | random
	RouteStrategy string `json:"route_strategy,omitempty"`
	// Tier 是模型档位，供 auto 自动路由按任务难度挑选：light | standard | pro。
	// 留空时按上游模型名启发式推断。
	Tier string `json:"tier,omitempty"`
	// Intelligence 是「智力分」：由主模型对模型做标准化测评后给出的 0-100 综合分，
	// 0 表示未测评。auto 路由在同档位内优先把任务派给高分模型。
	Intelligence int `json:"intelligence,omitempty"`
	// BenchmarkedAt 是最近一次智力测评时间（RFC3339），供面板展示时效。
	BenchmarkedAt string `json:"benchmarked_at,omitempty"`
	// ImageMode 决定图片请求体的组装方式：
	// standard（默认，OpenAI 标准：response_format 与参考图放顶层）
	// extra_body（Agnes Image 2.1 Flash 等要求把 response_format / image 放进 extra_body）
	ImageMode string `json:"image_mode,omitempty"`
	// ContextLength 是模型的上下文窗口（token）。留空（0）时回退到模型库探测值，
	// 仍取不到则按上游模型名启发式推断；用于调用前的容量校验与 auto 路由。
	ContextLength int `json:"context_length,omitempty"`
	// 以下仅在渠道类型为 template，或能力为 video 时生效
	Chat       *TemplateSpec `json:"chat,omitempty"`
	Image      *TemplateSpec `json:"image,omitempty"`
	Video      *TemplateSpec `json:"video,omitempty"`
	VideoQuery *TemplateSpec `json:"video_query,omitempty"`

	// 限速（仅对对外模型生效，0 表示不限制）。
	RPM int `json:"rpm,omitempty"` // 每分钟请求上限
	TPM int `json:"tpm,omitempty"` // 每分钟 token 上限（按实际消耗累加）

	// 单价（计费用，单位：元 / 美元均可，自行统一；0 表示不计费）。
	PriceIn    float64 `json:"price_in,omitempty"`    // 每 1K 输入 token 价格
	PriceOut   float64 `json:"price_out,omitempty"`   // 每 1K 输出 token 价格
	PriceImage float64 `json:"price_image,omitempty"` // 每张生成图片价格
}

// 模型档位：auto 自动模型会按任务难度在对应档位里挑选。
const (
	TierLight    = "light"
	TierStandard = "standard"
	TierPro      = "pro"
)

// 图片请求体组装模式。
const (
	// ImageModeStandard 是默认值：遵循 OpenAI 标准，response_format 与参考图放请求体顶层。
	ImageModeStandard = "standard"
	// ImageModeExtraBody 把 response_format 与参考图合并进 extra_body，
	// 用于 Agnes Image 2.1 Flash 这类明确禁止顶层 response_format 的上游。
	ImageModeExtraBody = "extra_body"
)

// ModelInput 是模型的一个上游输入：某渠道下的某个具体上游模型。
// 一个对外模型可以配置多个输入，按顺序尝试，失败自动切换。
// 同一个渠道也可以挂多个不同的上游模型（例如同为智谱平台下的
// glm-4.7-flash 与 glm-4.6v-flash），运行时按输入模态自动择一。
type ModelInput struct {
	ProviderID   int64  `json:"provider_id"`
	UpstreamName string `json:"upstream_name"`
	// InputModalities / OutputModalities 描述该上游模型接受的输入与产出的输出模态，
	// 如 ["text","image"] / ["text"]。留空时按上游模型名与能力自动推断。
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

// 模态：描述模型能接受的输入与能产出的输出。
const (
	ModalityText  = "text"
	ModalityImage = "image"
	ModalityVideo = "video"
	ModalityFile  = "file"
	ModalityAudio = "audio"
)

// visionModelKeywords 命中表示模型具备视觉能力（能读图）。
var visionModelKeywords = []string{
	"4v", "4.6v", "4.1v", "-vl", "vl-", "vision", "llava", "pixtral",
	"internvl", "moondream", "minicpm-v", "qwen-vl", "gemini", "gpt-4o",
	"gpt-4.1", "gpt-4-turbo", "gpt-4v", "claude-3", "claude-4", "step-1v",
}

// videoInputKeywords 命中表示模型还能直接理解视频输入。
var videoInputKeywords = []string{"4.6v", "4.1v", "video", "gemini-1.5", "gemini-2"}

// fileInputKeywords 命中表示模型能解析文档类输入。
var fileInputKeywords = []string{"4.6v", "4.1v", "file", "document", "pdf"}

// GuessModalities 按上游模型名与能力推断输入输出模态。
// 名称特征优先（如 -v / 4.6v 表示能读图甚至读视频），取不到时回退到能力对应的默认组合。
func GuessModalities(name, capability string) ([]string, []string) {
	n := strings.ToLower(strings.TrimSpace(name))
	text := []string{ModalityText}

	switch capability {
	case CapImage:
		return text, []string{ModalityImage}
	case CapVideo:
		// 视频模型普遍支持以一张图作为首帧。
		return []string{ModalityText, ModalityImage}, []string{ModalityVideo}
	case CapEmbedding:
		return text, text
	case CapSpeech:
		// 语音合成：吃文本，吐音频。
		return text, []string{ModalityAudio}
	case CapTranscription:
		// 语音识别：吃音频，吐文本。
		return []string{ModalityAudio}, text
	}

	inputs := []string{ModalityText}
	if containsAny(n, visionModelKeywords) {
		inputs = append(inputs, ModalityImage)
		if containsAny(n, videoInputKeywords) {
			inputs = append(inputs, ModalityVideo)
		}
		if containsAny(n, fileInputKeywords) {
			inputs = append(inputs, ModalityFile)
		}
	}
	return inputs, text
}

// containsAny 判断字符串是否命中任一关键词。
func containsAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// CleanModalities 清洗模态列表：去空白、转小写、去重，保持原顺序。
func CleanModalities(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		m := strings.ToLower(strings.TrimSpace(v))
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// Model 是对外暴露的一个模型名，可聚合多个渠道与上游模型。
// Inputs 为输入列表（有序，首个为默认）；ProviderIDs / ProviderID 为冗余字段，
// 由 Inputs 推导，用于兼容旧数据与维持外键约束。
type Model struct {
	ID           int64        `json:"id"`
	ProviderID   int64        `json:"provider_id"`
	ProviderIDs  []int64      `json:"provider_ids"`
	Inputs       []ModelInput `json:"inputs"`
	ProviderName string       `json:"provider_name,omitempty"`
	ProviderKind string       `json:"provider_kind,omitempty"`
	Name         string       `json:"name"`
	UpstreamName string       `json:"upstream_name"`
	Capability   string       `json:"capability"`
	Config       ModelConfig  `json:"config"`
	// CatalogContextLength 是模型库里探测到的上游上下文长度，只读，作为生效值的回退。
	CatalogContextLength int    `json:"catalog_context_length,omitempty"`
	Enabled              bool   `json:"enabled"`
	Remark               string `json:"remark"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

// AllInputs 返回模型配置的全部上游输入（有序去重）。
// 旧数据没有 inputs，用 provider_ids 与统一的 upstream_name 推导。
func (m Model) AllInputs() []ModelInput {
	out := make([]ModelInput, 0, len(m.Inputs))
	seen := map[inputKey]bool{}
	for _, in := range m.Inputs {
		if in.ProviderID <= 0 {
			continue
		}
		name := strings.TrimSpace(in.UpstreamName)
		if name == "" {
			name = m.UpstreamName
		}
		// 同一个渠道可以挂多个上游模型，因此按「渠道 + 上游名」去重。
		k := inputKey{providerID: in.ProviderID, upstream: name}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ModelInput{
			ProviderID:       in.ProviderID,
			UpstreamName:     name,
			InputModalities:  CleanModalities(in.InputModalities),
			OutputModalities: CleanModalities(in.OutputModalities),
		})
	}
	if len(out) == 0 {
		for _, id := range m.AllProviderIDs() {
			out = append(out, ModelInput{ProviderID: id, UpstreamName: m.UpstreamName})
		}
	}
	return out
}

// inputKey 是输入项的去重键：同一渠道 + 同一上游模型视为同一条。
type inputKey struct {
	providerID int64
	upstream   string
}

// InputModalities 返回模型全部输入能接受的模态并集。
func (m Model) InputModalities() []string {
	out := []string{}
	for _, in := range m.AllInputs() {
		ins, _ := in.Modalities(m.Capability)
		out = append(out, ins...)
	}
	if len(out) == 0 {
		ins, _ := GuessModalities(m.UpstreamName, m.Capability)
		return ins
	}
	return CleanModalities(out)
}

// OutputModalities 返回模型可能产出的输出模态并集。
func (m Model) OutputModalities() []string {
	out := []string{}
	for _, in := range m.AllInputs() {
		_, outs := in.Modalities(m.Capability)
		out = append(out, outs...)
	}
	if len(out) == 0 {
		_, outs := GuessModalities(m.UpstreamName, m.Capability)
		return outs
	}
	return CleanModalities(out)
}

// Modalities 返回单个输入项的输入与输出模态：显式声明优先，否则按名称与能力推断。
func (in ModelInput) Modalities(capability string) ([]string, []string) {
	inputs := CleanModalities(in.InputModalities)
	outputs := CleanModalities(in.OutputModalities)
	if len(inputs) == 0 || len(outputs) == 0 {
		gi, go_ := GuessModalities(in.UpstreamName, capability)
		if len(inputs) == 0 {
			inputs = gi
		}
		if len(outputs) == 0 {
			outputs = go_
		}
	}
	return inputs, outputs
}

// AllProviderIDs 返回模型绑定的全部渠道 ID（有序去重）。旧数据只有 provider_id 时回退到单渠道。
func (m Model) AllProviderIDs() []int64 {
	ids := cleanIDs(m.ProviderIDs)
	if len(ids) == 0 && m.ProviderID > 0 {
		ids = []int64{m.ProviderID}
	}
	return ids
}

// UpstreamNameFor 返回指定渠道对应的上游模型名，找不到时回退到默认值。
func (m Model) UpstreamNameFor(providerID int64) string {
	for _, in := range m.AllInputs() {
		if in.ProviderID == providerID {
			return in.UpstreamName
		}
	}
	return m.UpstreamName
}

// EffectiveTier 返回模型档位：优先取配置值，否则按上游模型名启发式推断。
func (m Model) EffectiveTier() string {
	switch m.Config.Tier {
	case TierLight, TierStandard, TierPro:
		return m.Config.Tier
	}
	// 顶层 UpstreamName 只是首个输入的冗余备份，未必是真正的上游模型名，
	// 多渠道绑定时以实际输入为准。
	if ins := m.AllInputs(); len(ins) > 0 && ins[0].UpstreamName != "" {
		return GuessTier(ins[0].UpstreamName)
	}
	return GuessTier(m.UpstreamName)
}

// GuessTier 按模型名里的常见标记推断档位，供 auto 路由与面板默认值使用。
func GuessTier(name string) string {
	n := strings.ToLower(name)
	for _, kw := range []string{"mini", "small", "flash", "lite", "nano", "tiny", "8b", "7b", "haiku", "air"} {
		if strings.Contains(n, kw) {
			return TierLight
		}
	}
	for _, kw := range []string{"pro", "max", "ultra", "opus", "large", "70b", "72b", "405b", "sonnet", "reasoner", "thinking"} {
		if strings.Contains(n, kw) {
			return TierPro
		}
	}
	return TierStandard
}

// EffectiveContextLength 返回模型可用的上下文窗口（token），0 表示未知。
// 优先级：模型显式配置 > 模型库探测值 > 按上游模型名启发式推断。
// 聚合多上游时，探测与推断都取全部输入里的最小值（按最差情况校验容量）。
func (m Model) EffectiveContextLength() int {
	if m.Config.ContextLength > 0 {
		return m.Config.ContextLength
	}
	if m.CatalogContextLength > 0 {
		return m.CatalogContextLength
	}
	// 与 EffectiveTier 同理：以实际绑定的上游模型名为准，
	// 否则多渠道绑定时会拿着对外模型名去推断，永远命中不了规则表。
	// 聚合模型取最小值：请求可能落到任何一个上游，容量按短板校验。
	min := 0
	for _, in := range m.AllInputs() {
		if n := GuessContextLength(in.UpstreamName); n > 0 {
			if min == 0 || n < min {
				min = n
			}
		}
	}
	if min > 0 {
		return min
	}
	return GuessContextLength(m.UpstreamName)
}

// contextLengthRules 按模型名特征给出常见上下文窗口，作为探测不到时的兜底。
// 取值刻意偏保守：宁可提前提示用户，也不要让上游返回难以理解的报错。
var contextLengthRules = []struct {
	keywords []string
	length   int
}{
	{[]string{"gemini-2", "gemini-1.5", "gemini-exp"}, 1048576},
	{[]string{"glm-4.7", "glm-4.6", "claude", "o1-", "o3-", "o4-"}, 200000},
	{[]string{"gpt-4.1", "gpt-4o", "gpt-4-turbo", "chatgpt-4o"}, 128000},
	{[]string{"qwen3", "qwen2.5", "qwen2", "qwen-max", "yi-"}, 131072},
	{[]string{"glm-4", "kimi", "moonshot", "command-r", "llama-3", "llama3"}, 131072},
	{[]string{"deepseek", "mistral", "mixtral"}, 65536},
	{[]string{"gpt-3.5"}, 16385},
	{[]string{"gpt-4"}, 8192},
}

// GuessContextLength 按模型名推断上下文窗口，识别不出时返回 0（未知）。
func GuessContextLength(name string) int {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return 0
	}
	for _, rule := range contextLengthRules {
		for _, kw := range rule.keywords {
			if strings.Contains(n, kw) {
				return rule.length
			}
		}
	}
	return 0
}

const modelSelect = `SELECT m.id,m.provider_id,COALESCE(p.name,''),COALESCE(p.kind,''),m.name,
	m.upstream_name,m.capability,m.config,m.enabled,m.remark,m.created_at,m.updated_at,m.provider_ids,m.inputs
	FROM models m LEFT JOIN providers p ON p.id=m.provider_id`

func (s *Store) ListModels() ([]Model, error) {
	// 走缓存：模型列表在一次 /v1 调用链里会被反复读（ListModels → 按名字查 →
	// 可用性过滤），而它自身是「全表 + 逐行 JSON 反序列化」，是这条链上最贵的一步。
	// 缓存由写操作版本号失效，不存在 TTL 那种「刚改完还读到旧值」的窗口。
	if list, byName, ok := s.modelsSnapshot(); ok {
		_ = byName
		return list, nil
	}
	rows, err := s.db.Query(modelSelect + ` ORDER BY m.id DESC`)
	if err != nil {
		return nil, err
	}
	out := []Model{}
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	err = rows.Err()
	// 连接池限定为单连接，必须先关闭游标再发起下一次查询。
	rows.Close()
	if err != nil {
		return nil, err
	}
	// 用模型库里探测到的上下文长度补全未显式声明的模型。
	index := s.catalogContextIndex()
	for i := range out {
		fillCatalogContext(&out[i], index)
	}
	s.storeModelsSnapshot(out, index)
	return out, nil
}

// modelsSnapshot 返回缓存的模型列表；版本号不匹配或尚未建立时返回 ok=false。
func (s *Store) modelsSnapshot() ([]Model, map[string]Model, bool) {
	gen := s.gen.Load()
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	if s.modelsList == nil || s.modelsGen != gen {
		return nil, nil, false
	}
	return s.modelsList, s.modelsByName, true
}

// storeModelsSnapshot 落缓存。调用方需已用 catalogIndex 补全上下文。
// 若期间发生过写操作则丢弃这份结果，避免把旧数据当成新的缓存起来。
func (s *Store) storeModelsSnapshot(list []Model, index map[string]int) {
	gen := s.gen.Load()
	byName := make(map[string]Model, len(list))
	for i := range list {
		fillCatalogContext(&list[i], index)
		byName[list[i].Name] = list[i]
	}
	s.modelsMu.Lock()
	if s.gen.Load() == gen {
		s.modelsList = list
		s.modelsByName = byName
		s.modelsGen = gen
	}
	s.modelsMu.Unlock()
}

func (s *Store) GetModel(id int64) (Model, error) {
	m, err := scanModel(s.db.QueryRow(modelSelect+` WHERE m.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, err
	}
	fillCatalogContext(&m, s.catalogContextIndex())
	return m, nil
}

// GetModelByName 按对外模型名精确查找。
// 名称在库里是唯一约束，创建前用它做一次预检，
// 好过把 SQLite 的 UNIQUE 约束错误直接抛给用户。
func (s *Store) GetModelByName(name string) (Model, error) {
	m, err := scanModel(s.db.QueryRow(modelSelect+` WHERE m.name=?`, strings.TrimSpace(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, err
	}
	fillCatalogContext(&m, s.catalogContextIndex())
	return m, nil
}

// ResolveModel 按对外模型名解析出可用模型（含渠道信息）。
func (s *Store) ResolveModel(name string) (Model, error) {
	// 命中模型列表缓存时直接给，省掉一次库查询 + JSON 反序列化。
	// 找不到也直接返回 ErrNotFound：写操作会让版本号失效，不会出现
	// 「刚建好的模型查不到」。
	if _, byName, ok := s.modelsSnapshot(); ok {
		if m, hit := byName[name]; hit {
			if !m.Enabled {
				return Model{}, ErrNotFound
			}
			return m, nil
		}
		return Model{}, ErrNotFound
	}
	m, err := scanModel(s.db.QueryRow(modelSelect+` WHERE m.name=? AND m.enabled=1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, err
	}
	fillCatalogContext(&m, s.catalogContextIndex())
	return m, nil
}

func scanModel(sc scanner) (Model, error) {
	var m Model
	var cfg, providerIDs, inputs string
	var enabled int
	if err := sc.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderKind, &m.Name,
		&m.UpstreamName, &m.Capability, &cfg, &enabled, &m.Remark, &m.CreatedAt, &m.UpdatedAt,
		&providerIDs, &inputs); err != nil {
		return Model{}, err
	}
	m.Config = unmarshal(cfg, ModelConfig{})
	m.ProviderIDs = cleanIDs(unmarshal(providerIDs, []int64{}))
	m.Inputs = cleanInputs(unmarshal(inputs, []ModelInput{}))
	m.Enabled = enabled == 1
	return m, nil
}

// cleanInputs 去掉无效项并按「渠道 + 上游模型名」去重，保持顺序。
// 注意这里不能只按渠道去重：同一个渠道下允许挂多个不同的上游模型。
func cleanInputs(in []ModelInput) []ModelInput {
	out := make([]ModelInput, 0, len(in))
	seen := map[inputKey]bool{}
	for _, v := range in {
		if v.ProviderID <= 0 {
			continue
		}
		upstream := strings.TrimSpace(v.UpstreamName)
		k := inputKey{providerID: v.ProviderID, upstream: upstream}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ModelInput{
			ProviderID:       v.ProviderID,
			UpstreamName:     upstream,
			InputModalities:  CleanModalities(v.InputModalities),
			OutputModalities: CleanModalities(v.OutputModalities),
		})
	}
	return out
}

func (s *Store) SaveModel(m Model) (Model, error) {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return m, errors.New("模型名称不能为空")
	}
	if m.UpstreamName == "" {
		m.UpstreamName = m.Name
	}
	switch m.Capability {
	case CapChat, CapImage, CapVideo, CapEmbedding, CapSpeech, CapTranscription:
	default:
		m.Capability = CapChat
	}
	inputs := m.AllInputs()
	if len(inputs) == 0 {
		return m, errors.New("请至少选择一个上游")
	}
	for _, in := range inputs {
		if _, err := s.GetProvider(in.ProviderID); err != nil {
			return m, errors.New("绑定的渠道不存在，请重新选择")
		}
	}
	ids := make([]int64, 0, len(inputs))
	for _, in := range inputs {
		ids = append(ids, in.ProviderID)
	}
	m.Inputs = inputs
	m.ProviderIDs = ids
	// provider_id 冗余保存首个渠道，维持外键约束并兼容旧数据。
	m.ProviderID = ids[0]
	if strings.TrimSpace(m.UpstreamName) == "" {
		m.UpstreamName = inputs[0].UpstreamName
	}
	ts := now()
	if m.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO models
			(provider_id,provider_ids,inputs,name,upstream_name,capability,config,enabled,remark,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			m.ProviderID, marshal(m.ProviderIDs), marshal(m.Inputs), m.Name, m.UpstreamName,
			m.Capability, marshal(m.Config), boolInt(m.Enabled), m.Remark, ts, ts)
		if err != nil {
			return m, err
		}
		m.ID, _ = res.LastInsertId()
		m.CreatedAt, m.UpdatedAt = ts, ts
	} else {
		if _, err := s.db.Exec(`UPDATE models SET provider_id=?,provider_ids=?,inputs=?,name=?,upstream_name=?,
			capability=?,config=?,enabled=?,remark=?,updated_at=? WHERE id=?`,
			m.ProviderID, marshal(m.ProviderIDs), marshal(m.Inputs), m.Name, m.UpstreamName,
			m.Capability, marshal(m.Config), boolInt(m.Enabled), m.Remark, ts, m.ID); err != nil {
			return m, err
		}
		m.UpdatedAt = ts
	}
	s.bumpGen() // 写操作后让模型/目录读缓存失效
	return s.GetModel(m.ID)
}

func (s *Store) DeleteModel(id int64) error {
	res, err := s.db.Exec(`DELETE FROM models WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.bumpGen() // 写操作后让模型/目录读缓存失效
	return nil
}

// ---------- 对外秘钥 ----------

type APIKey struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Key        string   `json:"key"`
	Models     []string `json:"models"` // 允许的对外模型名，空表示全部
	Enabled    bool     `json:"enabled"`
	ExpiresAt  string   `json:"expires_at"`
	Remark     string   `json:"remark"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt string   `json:"last_used_at"`

	// 配额，0 表示不限制
	RPM         int `json:"rpm"`          // 每分钟请求上限
	DailyQuota  int `json:"daily_quota"`  // 每日请求上限
	DailyTokens int `json:"daily_tokens"` // 每日 token 上限

	Calls int64 `json:"calls,omitempty"`
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.db.Query(`SELECT k.id,k.name,k.key,k.models,k.enabled,k.expires_at,k.remark,
		k.created_at,k.last_used_at,k.rpm,k.daily_quota,k.daily_tokens,
		(SELECT COUNT(*) FROM usage_logs l WHERE l.api_key_name=k.name) FROM api_keys k ORDER BY k.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		var models string
		var enabled int
		if err := rows.Scan(&k.ID, &k.Name, &k.Key, &models, &enabled, &k.ExpiresAt,
			&k.Remark, &k.CreatedAt, &k.LastUsedAt, &k.RPM, &k.DailyQuota, &k.DailyTokens,
			&k.Calls); err != nil {
			return nil, err
		}
		k.Models = unmarshal(models, []string{})
		k.Enabled = enabled == 1
		out = append(out, k)
	}
	return out, rows.Err()
}

// FindAPIKey 按明文秘钥查询可用秘钥。
func (s *Store) FindAPIKey(key string) (APIKey, error) {
	// 热路径缓存：/v1 每次调用都要查这把秘钥，内存命中省掉一次单连接库上的
	// SELECT。秘钥的增删改都会整体作废缓存（操作频率极低，无一致性压力）。
	s.keyCacheMu.RLock()
	if k, ok := s.keyCache[key]; ok {
		s.keyCacheMu.RUnlock()
		return k, nil
	}
	s.keyCacheMu.RUnlock()
	k, err := s.scanAPIKey(s.db.QueryRow(`SELECT id,name,key,models,enabled,expires_at,remark,created_at,last_used_at,
		rpm,daily_quota,daily_tokens,0 FROM api_keys WHERE key=?`, key))
	if err != nil {
		return APIKey{}, err
	}
	s.keyCacheMu.Lock()
	if s.keyCache == nil {
		s.keyCache = make(map[string]APIKey)
	}
	s.keyCache[key] = k
	s.keyCacheMu.Unlock()
	return k, nil
}

// invalidateKeyCache 整体作废秘钥缓存（SaveAPIKey / DeleteAPIKey 后调用）。
func (s *Store) invalidateKeyCache() {
	s.keyCacheMu.Lock()
	s.keyCache = make(map[string]APIKey)
	s.keyCacheMu.Unlock()
}

// FindAPIKeyByID 按主键查询秘钥。
func (s *Store) FindAPIKeyByID(id int64) (APIKey, error) {
	return s.scanAPIKey(s.db.QueryRow(`SELECT id,name,key,models,enabled,expires_at,remark,created_at,last_used_at,
		rpm,daily_quota,daily_tokens,0 FROM api_keys WHERE id=?`, id))
}

func (s *Store) scanAPIKey(row *sql.Row) (APIKey, error) {
	var k APIKey
	var models string
	var enabled int
	err := row.Scan(&k.ID, &k.Name, &k.Key, &models, &enabled, &k.ExpiresAt, &k.Remark,
		&k.CreatedAt, &k.LastUsedAt, &k.RPM, &k.DailyQuota, &k.DailyTokens, &k.Calls)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	k.Models = unmarshal(models, []string{})
	k.Enabled = enabled == 1
	return k, nil
}

func (s *Store) SaveAPIKey(k APIKey) (APIKey, error) {
	k.Name = strings.TrimSpace(k.Name)
	if k.Name == "" {
		return k, errors.New("秘钥名称不能为空")
	}
	if k.ID == 0 && k.Key == "" {
		k.Key = GenerateKey()
	}
	ts := now()
	if k.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO api_keys
			(name,key,models,enabled,expires_at,remark,rpm,daily_quota,daily_tokens,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			k.Name, k.Key, marshal(cleanList(k.Models)), boolInt(k.Enabled), k.ExpiresAt, k.Remark,
			k.RPM, k.DailyQuota, k.DailyTokens, ts)
		if err != nil {
			return k, err
		}
		k.ID, _ = res.LastInsertId()
		k.CreatedAt = ts
		s.invalidateKeyCache()
		return k, nil
	}
	_, err := s.db.Exec(`UPDATE api_keys SET name=?,models=?,enabled=?,expires_at=?,remark=?,
		rpm=?,daily_quota=?,daily_tokens=? WHERE id=?`,
		k.Name, marshal(cleanList(k.Models)), boolInt(k.Enabled), k.ExpiresAt, k.Remark,
		k.RPM, k.DailyQuota, k.DailyTokens, k.ID)
	if err != nil {
		return k, err
	}
	s.invalidateKeyCache()
	return s.FindAPIKey(k.Key)
}

func (s *Store) DeleteAPIKey(id int64) error {
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.invalidateKeyCache()
	return nil
}

// TouchAPIKey 更新秘钥最近使用时间。节流：同一把秘钥 30 秒内不重复写库——
// last_used_at 只需要分钟级展示精度，没必要每次调用都打一次写事务。
func (s *Store) TouchAPIKey(key string) {
	ts := time.Now()
	s.touchMu.Lock()
	if last, ok := s.touchAt[key]; ok && ts.Sub(last) < 30*time.Second {
		s.touchMu.Unlock()
		return
	}
	s.touchAt[key] = ts
	s.touchMu.Unlock()
	_, _ = s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE key=?`, ts.Format(time.RFC3339), key)
}

// GenerateKey 生成 sk- 前缀的随机秘钥。
func GenerateKey() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "sk-" + fmt.Sprint(now())
	}
	return "sk-" + hex.EncodeToString(buf)
}

// ---------- 工具 ----------

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cleanIDs 去重并剔除非正数，保持原有顺序。
func cleanIDs(in []int64) []int64 {
	out := make([]int64, 0, len(in))
	seen := map[int64]bool{}
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
