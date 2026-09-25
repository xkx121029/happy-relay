package store

import (
	"fmt"
	"time"
)

// ---------- 用量统计 ----------

func todayStart() string {
	return time.Now().Format("2006-01-02") + "T00:00:00"
}

func daysAgoStart(days int) string {
	if days <= 0 {
		days = 1
	}
	return time.Now().AddDate(0, 0, -(days-1)).Format("2006-01-02") + "T00:00:00"
}

// APIKeyDailyUsage 返回某把秘钥当天的调用次数与 token 用量，供配额校验使用。
func (s *Store) APIKeyDailyUsage(name string) (int64, int64, error) {
	var calls, tokens int64
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(prompt_tokens+completion_tokens),0)
		FROM usage_logs WHERE api_key_name=? AND created_at>=?`, name, todayStart()).Scan(&calls, &tokens)
	return calls, tokens, err
}

// ModelStat 是按对外模型维度聚合的用量。
type ModelStat struct {
	Model      string  `json:"model"`
	Calls      int64   `json:"calls"`
	Failed     int64   `json:"failed"`
	Tokens     int64   `json:"tokens"`
	AvgLatency float64 `json:"avg_latency_ms"`
}

// ProviderStat 是按渠道维度聚合的用量。
type ProviderStat struct {
	Provider   string  `json:"provider"`
	Calls      int64   `json:"calls"`
	Failed     int64   `json:"failed"`
	AvgLatency float64 `json:"avg_latency_ms"`
}

// ModelStats 返回最近 days 天里按模型聚合的用量排行。
func (s *Store) ModelStats(days int) ([]ModelStat, error) {
	rows, err := s.db.Query(`SELECT model,COUNT(*),
		COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(prompt_tokens+completion_tokens),0),COALESCE(AVG(latency_ms),0)
		FROM usage_logs WHERE created_at>=? GROUP BY model ORDER BY COUNT(*) DESC LIMIT 20`,
		daysAgoStart(days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelStat{}
	for rows.Next() {
		var it ModelStat
		if err := rows.Scan(&it.Model, &it.Calls, &it.Failed, &it.Tokens, &it.AvgLatency); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ProviderStats 返回最近 days 天里按渠道聚合的用量排行。
func (s *Store) ProviderStats(days int) ([]ProviderStat, error) {
	rows, err := s.db.Query(`SELECT provider_name,COUNT(*),
		COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0),COALESCE(AVG(latency_ms),0)
		FROM usage_logs WHERE created_at>=? AND provider_name<>'' GROUP BY provider_name
		ORDER BY COUNT(*) DESC LIMIT 20`, daysAgoStart(days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderStat{}
	for rows.Next() {
		var it ProviderStat
		if err := rows.Scan(&it.Provider, &it.Calls, &it.Failed, &it.AvgLatency); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MetricRow 是 /metrics 导出所需的聚合行。
type MetricRow struct {
	Capability string
	Model      string
	Status     string
	Calls      int64
	AvgLatency float64
}

// MetricRows 汇总全量调用日志，供 Prometheus 指标导出。
func (s *Store) MetricRows() ([]MetricRow, error) {
	rows, err := s.db.Query(`SELECT capability,model,
		CASE WHEN status>=400 OR error<>'' THEN 'error' ELSE 'ok' END,
		COUNT(*),COALESCE(AVG(latency_ms),0)
		FROM usage_logs GROUP BY capability,model ORDER BY capability,model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetricRow{}
	for rows.Next() {
		var it MetricRow
		if err := rows.Scan(&it.Capability, &it.Model, &it.Status, &it.Calls, &it.AvgLatency); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ---------- 配置备份 ----------

// Backup 是一份完整的配置快照（不含调用日志与任务记录）。
type Backup struct {
	Version    int               `json:"version"`
	ExportedAt string            `json:"exported_at"`
	Providers  []Provider        `json:"providers"`
	Models     []Model           `json:"models"`
	APIKeys    []APIKey          `json:"api_keys"`
	Skills     []Skill           `json:"skills"`
	Catalog    []CatalogModel    `json:"catalog"`
	Settings   map[string]string `json:"settings"`
}

// ImportSummary 描述一次合并导入的结果。
type ImportSummary struct {
	Providers int `json:"providers"`
	Models    int `json:"models"`
	Keys      int `json:"keys"`
	Skills    int `json:"skills"`
	Catalog   int `json:"catalog"`
	Settings  int `json:"settings"`
}

// ExportAll 导出全部配置。
func (s *Store) ExportAll() (*Backup, error) {
	providers, err := s.ListProviders()
	if err != nil {
		return nil, err
	}
	models, err := s.ListModels()
	if err != nil {
		return nil, err
	}
	keys, err := s.ListAPIKeys()
	if err != nil {
		return nil, err
	}
	skills, err := s.ListSkills()
	if err != nil {
		return nil, err
	}
	catalog, err := s.ListCatalogModels(0, "")
	if err != nil {
		return nil, err
	}
	settings, err := s.AllSettings()
	if err != nil {
		return nil, err
	}
	return &Backup{
		Version:    1,
		ExportedAt: now(),
		Providers:  providers,
		Models:     models,
		APIKeys:    keys,
		Skills:     skills,
		Catalog:    catalog,
		Settings:   settings,
	}, nil
}

// ImportAll 按名称合并导入配置：同名更新、不删除已有数据。
// 模型与模型库里的渠道引用会按渠道名称重新映射。
func (s *Store) ImportAll(b *Backup) (*ImportSummary, error) {
	summary := &ImportSummary{}

	// 渠道：按名称匹配。
	nameToID := map[string]int64{}
	existingProviders, err := s.ListProviders()
	if err != nil {
		return nil, err
	}
	for _, p := range existingProviders {
		nameToID[p.Name] = p.ID
	}
	oldIDToName := map[int64]string{}
	for _, p := range b.Providers {
		oldIDToName[p.ID] = p.Name
	}
	for _, p := range b.Providers {
		p.ID = nameToID[p.Name] // 不存在时为 0，走新建
		saved, err := s.SaveProvider(p)
		if err != nil {
			return nil, fmt.Errorf("导入渠道「%s」失败：%w", p.Name, err)
		}
		nameToID[saved.Name] = saved.ID
		summary.Providers++
	}

	// 模型：把输入里的旧渠道 ID 换成新 ID。
	for _, m := range b.Models {
		raw := m.Inputs
		if len(raw) == 0 {
			for _, id := range m.ProviderIDs {
				raw = append(raw, ModelInput{ProviderID: id, UpstreamName: m.UpstreamName})
			}
		}
		mapped := make([]ModelInput, 0, len(raw))
		for _, in := range raw {
			id := in.ProviderID
			if name, ok := oldIDToName[id]; ok {
				if newID, ok := nameToID[name]; ok {
					id = newID
				}
			}
			mapped = append(mapped, ModelInput{ProviderID: id, UpstreamName: in.UpstreamName})
		}
		m.ID = 0
		m.ProviderIDs = nil
		m.ProviderID = 0
		m.Inputs = mapped
		if _, err := s.SaveModel(m); err != nil {
			return nil, fmt.Errorf("导入模型「%s」失败：%w", m.Name, err)
		}
		summary.Models++
	}

	// 模型库：按「渠道 + 模型名」去重。
	for _, c := range b.Catalog {
		newID, ok := nameToID[oldIDToName[c.ProviderID]]
		if !ok {
			continue
		}
		if _, err := s.SaveCatalogModel(CatalogModel{
			ProviderID: newID,
			ModelID:    c.ModelID,
			Capability: c.Capability,
			Enabled:    c.Enabled,
			Remark:     c.Remark,
		}); err != nil {
			return nil, fmt.Errorf("导入模型库条目「%s」失败：%w", c.ModelID, err)
		}
		summary.Catalog++
	}

	// 秘钥：按 key 匹配。
	existingKeys, err := s.ListAPIKeys()
	if err != nil {
		return nil, err
	}
	keyToID := map[string]int64{}
	for _, k := range existingKeys {
		keyToID[k.Key] = k.ID
	}
	for _, k := range b.APIKeys {
		k.ID = keyToID[k.Key]
		if _, err := s.SaveAPIKey(k); err != nil {
			return nil, fmt.Errorf("导入秘钥「%s」失败：%w", k.Name, err)
		}
		summary.Keys++
	}

	// 技能：按 slug 匹配。
	existingSkills, err := s.ListSkills()
	if err != nil {
		return nil, err
	}
	slugToID := map[string]int64{}
	for _, sk := range existingSkills {
		slugToID[sk.Slug] = sk.ID
	}
	for _, sk := range b.Skills {
		sk.ID = slugToID[sk.Slug]
		if _, err := s.SaveSkill(sk); err != nil {
			return nil, fmt.Errorf("导入技能「%s」失败：%w", sk.Name, err)
		}
		summary.Skills++
	}

	for k, v := range b.Settings {
		if err := s.SetSetting(k, v); err != nil {
			return nil, fmt.Errorf("导入设置「%s」失败：%w", k, err)
		}
		summary.Settings++
	}

	return summary, nil
}
