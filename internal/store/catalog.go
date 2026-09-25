package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---------- 模型库 ----------

// CatalogModel 是从渠道探测到（或手工登记）的上游模型。
// 它只是「可供配置引用」的候选清单，本身不直接对外提供服务。
type CatalogModel struct {
	ID           int64  `json:"id"`
	ProviderID   int64  `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
	ProviderKind string `json:"provider_kind,omitempty"`
	ModelID      string `json:"model_id"`
	Capability   string `json:"capability"`
	Source       string `json:"source"` // probe | manual
	Enabled      bool   `json:"enabled"`
	Remark       string `json:"remark"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`

	// UsedBy 表示有多少个对外模型引用了它。
	UsedBy int `json:"used_by,omitempty"`
}

const catalogSelect = `SELECT c.id,c.provider_id,COALESCE(p.name,''),COALESCE(p.kind,''),c.model_id,
	c.capability,c.source,c.enabled,c.remark,c.created_at,c.updated_at FROM catalog_models c
	LEFT JOIN providers p ON p.id=c.provider_id`

// ListCatalogModels 列出模型库条目，可按渠道与能力过滤。
func (s *Store) ListCatalogModels(providerID int64, capability string) ([]CatalogModel, error) {
	q := catalogSelect + ` WHERE 1=1`
	args := []any{}
	if providerID > 0 {
		q += ` AND c.provider_id=?`
		args = append(args, providerID)
	}
	if capability != "" {
		q += ` AND c.capability=?`
		args = append(args, capability)
	}
	q += ` ORDER BY c.provider_id ASC, c.model_id ASC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	out := []CatalogModel{}
	for rows.Next() {
		item, err := scanCatalog(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	usage, err := s.catalogUsage()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].UsedBy = usage[catalogKey(out[i].ProviderID, out[i].ModelID)]
	}
	return out, nil
}

func (s *Store) GetCatalogModel(id int64) (CatalogModel, error) {
	item, err := scanCatalog(s.db.QueryRow(catalogSelect+` WHERE c.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CatalogModel{}, ErrNotFound
	}
	return item, err
}

func (s *Store) getCatalogByKey(providerID int64, modelID string) (CatalogModel, error) {
	item, err := scanCatalog(s.db.QueryRow(catalogSelect+` WHERE c.provider_id=? AND c.model_id=?`,
		providerID, modelID))
	if errors.Is(err, sql.ErrNoRows) {
		return CatalogModel{}, ErrNotFound
	}
	return item, err
}

func scanCatalog(sc scanner) (CatalogModel, error) {
	var m CatalogModel
	var enabled int
	if err := sc.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderKind, &m.ModelID,
		&m.Capability, &m.Source, &enabled, &m.Remark, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return CatalogModel{}, err
	}
	m.Enabled = enabled == 1
	return m, nil
}

// SaveCatalogModel 登记（或更新）一个模型库条目，按「渠道 + 上游模型名」去重。
func (s *Store) SaveCatalogModel(m CatalogModel) (CatalogModel, error) {
	m.ModelID = strings.TrimSpace(m.ModelID)
	if m.ModelID == "" {
		return m, errors.New("上游模型名不能为空")
	}
	if _, err := s.GetProvider(m.ProviderID); err != nil {
		return m, errors.New("请选择有效的渠道")
	}
	switch m.Capability {
	case CapChat, CapImage, CapVideo, CapEmbedding:
	default:
		m.Capability = CapChat
	}
	if m.Source != "probe" {
		m.Source = "manual"
	}
	ts := now()
	if m.ID == 0 {
		if _, err := s.db.Exec(`INSERT INTO catalog_models
			(provider_id,model_id,capability,source,enabled,remark,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(provider_id,model_id) DO UPDATE SET
			  capability=excluded.capability, enabled=excluded.enabled,
			  remark=excluded.remark, updated_at=excluded.updated_at`,
			m.ProviderID, m.ModelID, m.Capability, m.Source, boolInt(m.Enabled), m.Remark, ts, ts); err != nil {
			return m, err
		}
		return s.getCatalogByKey(m.ProviderID, m.ModelID)
	}
	if _, err := s.db.Exec(`UPDATE catalog_models SET provider_id=?,model_id=?,capability=?,
		enabled=?,remark=?,updated_at=? WHERE id=?`,
		m.ProviderID, m.ModelID, m.Capability, boolInt(m.Enabled), m.Remark, ts, m.ID); err != nil {
		return m, err
	}
	return s.GetCatalogModel(m.ID)
}

func (s *Store) DeleteCatalogModel(id int64) error {
	res, err := s.db.Exec(`DELETE FROM catalog_models WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ImportCatalogModels 批量把探测结果写入模型库，返回新建与更新的条数。
func (s *Store) ImportCatalogModels(providerID int64, items []CatalogModel) (int, int, error) {
	if _, err := s.GetProvider(providerID); err != nil {
		return 0, 0, errors.New("请选择有效的渠道")
	}
	existing, err := s.ListCatalogModels(providerID, "")
	if err != nil {
		return 0, 0, err
	}
	known := map[string]bool{}
	for _, it := range existing {
		known[it.ModelID] = true
	}

	created, updated := 0, 0
	for _, item := range items {
		item.ProviderID = providerID
		item.Source = "probe"
		// 从探测结果导入的模型默认标记为可用，需要的话可在模型库里停用。
		item.Enabled = true
		if _, err := s.SaveCatalogModel(item); err != nil {
			return created, updated, err
		}
		if known[item.ModelID] {
			updated++
		} else {
			created++
			known[item.ModelID] = true
		}
	}
	return created, updated, nil
}

// catalogUsage 统计每个「渠道 + 上游模型名」被多少个对外模型引用。
func (s *Store) catalogUsage() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT provider_id,provider_ids,inputs,upstream_name FROM models`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := map[string]int{}
	for rows.Next() {
		var primary int64
		var providerIDs, inputs, upstream string
		if err := rows.Scan(&primary, &providerIDs, &inputs, &upstream); err != nil {
			return nil, err
		}
		m := Model{
			ProviderID:   primary,
			ProviderIDs:  unmarshal(providerIDs, []int64{}),
			Inputs:       unmarshal(inputs, []ModelInput{}),
			UpstreamName: upstream,
		}
		for _, in := range m.AllInputs() {
			usage[catalogKey(in.ProviderID, in.UpstreamName)]++
		}
	}
	return usage, rows.Err()
}

func catalogKey(providerID int64, modelID string) string {
	return fmt.Sprint(providerID) + "\x00" + modelID
}
