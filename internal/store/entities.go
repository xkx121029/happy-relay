package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ---------- 渠道（上游） ----------

// 渠道类型：决定使用哪个协议适配器。
const (
	KindOpenAI    = "openai"    // OpenAI 兼容（/v1/chat/completions 等）
	KindGemini    = "gemini"    // Google Gemini 原生 generateContent
	KindAnthropic = "anthropic" // Anthropic Claude 原生 Messages
	KindTemplate  = "template"  // 模板驱动，可接任意 HTTP 上游
)

// Provider 是一个上游服务商，可挂多个入口地址与多个密钥。
type Provider struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	BaseURLs   []string          `json:"base_urls"`
	APIKeys    []string          `json:"api_keys"`
	Headers    map[string]string `json:"headers"`
	Strategy   string            `json:"strategy"` // round_robin | random | failover
	TimeoutSec int               `json:"timeout_sec"`
	Enabled    bool              `json:"enabled"`
	Remark     string            `json:"remark"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`

	ModelCount int `json:"model_count,omitempty"`
}

func (s *Store) ListProviders() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT id,name,kind,base_urls,api_keys,headers,strategy,
		timeout_sec,enabled,remark,created_at,updated_at,0 FROM providers ORDER BY id DESC`)
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
	row := s.db.QueryRow(`SELECT id,name,kind,base_urls,api_keys,headers,strategy,timeout_sec,
		enabled,remark,created_at,updated_at,0 FROM providers WHERE id=?`, id)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	return p, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(sc scanner) (Provider, error) {
	var p Provider
	var baseURLs, apiKeys, headers string
	var enabled int
	if err := sc.Scan(&p.ID, &p.Name, &p.Kind, &baseURLs, &apiKeys, &headers, &p.Strategy,
		&p.TimeoutSec, &enabled, &p.Remark, &p.CreatedAt, &p.UpdatedAt, &p.ModelCount); err != nil {
		return Provider{}, err
	}
	p.BaseURLs = unmarshal(baseURLs, []string{})
	p.APIKeys = unmarshal(apiKeys, []string{})
	p.Headers = unmarshal(headers, map[string]string{})
	p.Enabled = enabled == 1
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
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	ts := now()
	if p.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO providers
			(name,kind,base_urls,api_keys,headers,strategy,timeout_sec,enabled,remark,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			p.Name, p.Kind, marshal(p.BaseURLs), marshal(p.APIKeys), marshal(p.Headers),
			p.Strategy, p.TimeoutSec, boolInt(p.Enabled), p.Remark, ts, ts)
		if err != nil {
			return p, err
		}
		p.ID, _ = res.LastInsertId()
		p.CreatedAt, p.UpdatedAt = ts, ts
		return p, nil
	}
	_, err := s.db.Exec(`UPDATE providers SET name=?,kind=?,base_urls=?,api_keys=?,headers=?,
		strategy=?,timeout_sec=?,enabled=?,remark=?,updated_at=? WHERE id=?`,
		p.Name, p.Kind, marshal(p.BaseURLs), marshal(p.APIKeys), marshal(p.Headers),
		p.Strategy, p.TimeoutSec, boolInt(p.Enabled), p.Remark, ts, p.ID)
	if err != nil {
		return p, err
	}
	p.UpdatedAt = ts
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
)

// TemplateSpec 描述如何把统一的 v1 请求翻译成一次上游 HTTP 调用。
// 用于视频任务与自定义上游，占位符形如 {{prompt}}、{{model}}，取值路径支持 a.b[0].c。
type TemplateSpec struct {
	Method   string            `json:"method,omitempty"`  // POST / GET，默认 POST
	Path     string            `json:"path,omitempty"`    // 相对 base_url 的路径
	Headers  map[string]string `json:"headers,omitempty"` // 额外请求头
	Body     map[string]any    `json:"body,omitempty"`    // 请求体模板
	Query    map[string]string `json:"query,omitempty"`   // URL 查询参数模板
	Response ResponseMap       `json:"response,omitempty"`
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
	// ImageMode 决定图片请求体的组装方式：
	// standard（默认，OpenAI 标准：response_format 与参考图放顶层）
	// extra_body（Agnes Image 2.1 Flash 等要求把 response_format / image 放进 extra_body）
	ImageMode string `json:"image_mode,omitempty"`
	// 以下仅在渠道类型为 template，或能力为 video 时生效
	Chat       *TemplateSpec `json:"chat,omitempty"`
	Image      *TemplateSpec `json:"image,omitempty"`
	Video      *TemplateSpec `json:"video,omitempty"`
	VideoQuery *TemplateSpec `json:"video_query,omitempty"`
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
type ModelInput struct {
	ProviderID   int64  `json:"provider_id"`
	UpstreamName string `json:"upstream_name"`
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
	Enabled      bool         `json:"enabled"`
	Remark       string       `json:"remark"`
	CreatedAt    string       `json:"created_at"`
	UpdatedAt    string       `json:"updated_at"`
}

// AllInputs 返回模型配置的全部上游输入（有序去重）。
// 旧数据没有 inputs，用 provider_ids 与统一的 upstream_name 推导。
func (m Model) AllInputs() []ModelInput {
	out := make([]ModelInput, 0, len(m.Inputs))
	seen := map[int64]bool{}
	for _, in := range m.Inputs {
		if in.ProviderID <= 0 || seen[in.ProviderID] {
			continue
		}
		seen[in.ProviderID] = true
		name := strings.TrimSpace(in.UpstreamName)
		if name == "" {
			name = m.UpstreamName
		}
		out = append(out, ModelInput{ProviderID: in.ProviderID, UpstreamName: name})
	}
	if len(out) == 0 {
		for _, id := range m.AllProviderIDs() {
			out = append(out, ModelInput{ProviderID: id, UpstreamName: m.UpstreamName})
		}
	}
	return out
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

const modelSelect = `SELECT m.id,m.provider_id,COALESCE(p.name,''),COALESCE(p.kind,''),m.name,
	m.upstream_name,m.capability,m.config,m.enabled,m.remark,m.created_at,m.updated_at,m.provider_ids,m.inputs
	FROM models m LEFT JOIN providers p ON p.id=m.provider_id`

func (s *Store) ListModels() ([]Model, error) {
	rows, err := s.db.Query(modelSelect + ` ORDER BY m.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetModel(id int64) (Model, error) {
	m, err := scanModel(s.db.QueryRow(modelSelect+` WHERE m.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	return m, err
}

// ResolveModel 按对外模型名解析出可用模型（含渠道信息）。
func (s *Store) ResolveModel(name string) (Model, error) {
	m, err := scanModel(s.db.QueryRow(modelSelect+` WHERE m.name=? AND m.enabled=1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	return m, err
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

// cleanInputs 去掉无效项并按渠道去重，保持顺序。
func cleanInputs(in []ModelInput) []ModelInput {
	out := make([]ModelInput, 0, len(in))
	seen := map[int64]bool{}
	for _, v := range in {
		if v.ProviderID <= 0 || seen[v.ProviderID] {
			continue
		}
		seen[v.ProviderID] = true
		out = append(out, ModelInput{
			ProviderID:   v.ProviderID,
			UpstreamName: strings.TrimSpace(v.UpstreamName),
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
	case CapChat, CapImage, CapVideo, CapEmbedding:
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
	return s.scanAPIKey(s.db.QueryRow(`SELECT id,name,key,models,enabled,expires_at,remark,created_at,last_used_at,
		rpm,daily_quota,daily_tokens,0 FROM api_keys WHERE key=?`, key))
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
		return k, nil
	}
	_, err := s.db.Exec(`UPDATE api_keys SET name=?,models=?,enabled=?,expires_at=?,remark=?,
		rpm=?,daily_quota=?,daily_tokens=? WHERE id=?`,
		k.Name, marshal(cleanList(k.Models)), boolInt(k.Enabled), k.ExpiresAt, k.Remark,
		k.RPM, k.DailyQuota, k.DailyTokens, k.ID)
	if err != nil {
		return k, err
	}
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
	return nil
}

func (s *Store) TouchAPIKey(key string) {
	_, _ = s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE key=?`, now(), key)
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
