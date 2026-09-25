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
	rows, err := s.db.Query(`SELECT p.id,p.name,p.kind,p.base_urls,p.api_keys,p.headers,p.strategy,
		p.timeout_sec,p.enabled,p.remark,p.created_at,p.updated_at,
		(SELECT COUNT(*) FROM models m WHERE m.provider_id=p.id) FROM providers p ORDER BY p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	res, err := s.db.Exec(`DELETE FROM providers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
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
	// 以下仅在渠道类型为 template，或能力为 video 时生效
	Chat       *TemplateSpec `json:"chat,omitempty"`
	Image      *TemplateSpec `json:"image,omitempty"`
	Video      *TemplateSpec `json:"video,omitempty"`
	VideoQuery *TemplateSpec `json:"video_query,omitempty"`
}

// Model 是对外暴露的一个模型名，映射到某个渠道下的上游模型。
type Model struct {
	ID           int64       `json:"id"`
	ProviderID   int64       `json:"provider_id"`
	ProviderName string      `json:"provider_name,omitempty"`
	ProviderKind string      `json:"provider_kind,omitempty"`
	Name         string      `json:"name"`
	UpstreamName string      `json:"upstream_name"`
	Capability   string      `json:"capability"`
	Config       ModelConfig `json:"config"`
	Enabled      bool        `json:"enabled"`
	Remark       string      `json:"remark"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
}

const modelSelect = `SELECT m.id,m.provider_id,COALESCE(p.name,''),COALESCE(p.kind,''),m.name,
	m.upstream_name,m.capability,m.config,m.enabled,m.remark,m.created_at,m.updated_at
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
	var cfg string
	var enabled int
	if err := sc.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderKind, &m.Name,
		&m.UpstreamName, &m.Capability, &cfg, &enabled, &m.Remark, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return Model{}, err
	}
	m.Config = unmarshal(cfg, ModelConfig{})
	m.Enabled = enabled == 1
	return m, nil
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
	if _, err := s.GetProvider(m.ProviderID); err != nil {
		return m, errors.New("请选择有效的渠道")
	}
	ts := now()
	if m.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO models
			(provider_id,name,upstream_name,capability,config,enabled,remark,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			m.ProviderID, m.Name, m.UpstreamName, m.Capability, marshal(m.Config),
			boolInt(m.Enabled), m.Remark, ts, ts)
		if err != nil {
			return m, err
		}
		m.ID, _ = res.LastInsertId()
		m.CreatedAt, m.UpdatedAt = ts, ts
	} else {
		if _, err := s.db.Exec(`UPDATE models SET provider_id=?,name=?,upstream_name=?,
			capability=?,config=?,enabled=?,remark=?,updated_at=? WHERE id=?`,
			m.ProviderID, m.Name, m.UpstreamName, m.Capability, marshal(m.Config),
			boolInt(m.Enabled), m.Remark, ts, m.ID); err != nil {
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

	Calls int64 `json:"calls,omitempty"`
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.db.Query(`SELECT k.id,k.name,k.key,k.models,k.enabled,k.expires_at,k.remark,
		k.created_at,k.last_used_at,
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
			&k.Remark, &k.CreatedAt, &k.LastUsedAt, &k.Calls); err != nil {
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
	return s.scanAPIKey(s.db.QueryRow(`SELECT id,name,key,models,enabled,expires_at,remark,created_at,last_used_at,0
		FROM api_keys WHERE key=?`, key))
}

// FindAPIKeyByID 按主键查询秘钥。
func (s *Store) FindAPIKeyByID(id int64) (APIKey, error) {
	return s.scanAPIKey(s.db.QueryRow(`SELECT id,name,key,models,enabled,expires_at,remark,created_at,last_used_at,0
		FROM api_keys WHERE id=?`, id))
}

func (s *Store) scanAPIKey(row *sql.Row) (APIKey, error) {
	var k APIKey
	var models string
	var enabled int
	err := row.Scan(&k.ID, &k.Name, &k.Key, &models, &enabled, &k.ExpiresAt, &k.Remark,
		&k.CreatedAt, &k.LastUsedAt, &k.Calls)
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
			(name,key,models,enabled,expires_at,remark,created_at) VALUES (?,?,?,?,?,?,?)`,
			k.Name, k.Key, marshal(cleanList(k.Models)), boolInt(k.Enabled), k.ExpiresAt, k.Remark, ts)
		if err != nil {
			return k, err
		}
		k.ID, _ = res.LastInsertId()
		k.CreatedAt = ts
		return k, nil
	}
	_, err := s.db.Exec(`UPDATE api_keys SET name=?,models=?,enabled=?,expires_at=?,remark=? WHERE id=?`,
		k.Name, marshal(cleanList(k.Models)), boolInt(k.Enabled), k.ExpiresAt, k.Remark, k.ID)
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