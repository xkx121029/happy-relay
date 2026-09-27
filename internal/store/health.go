package store

import (
	"fmt"
	"strings"
	"time"
)

// HealthCheck 是一条渠道巡检记录。
type HealthCheck struct {
	ID         int64  `json:"id"`
	ProviderID int64  `json:"provider_id"`
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	LatencyMS  int    `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	CheckedAt  string `json:"checked_at"`
}

// ProviderUptime 是某渠道在统计窗口内的可用率摘要。
type ProviderUptime struct {
	ProviderID int64 `json:"provider_id"`
	Total      int64 `json:"total"`
	OKCount    int64 `json:"ok_count"`
	// Uptime 是可用率百分比（0-100）；没有巡检样本时为 -1。
	Uptime     float64 `json:"uptime"`
	AvgLatency float64 `json:"avg_latency_ms"`
	// LastOK 是最近一次巡检是否成功。
	LastOK bool `json:"last_ok"`
	// LastAt 是最近一次巡检时间。
	LastAt string `json:"last_at,omitempty"`
	// LastError 是最近一次巡检的错误信息。
	LastError string `json:"last_error,omitempty"`
	// ConsecutiveFails 是自最近一次成功以来连续失败的次数，用于判断「持续挂」。
	ConsecutiveFails int `json:"consecutive_fails"`
}

// SaveHealthCheck 写入一条巡检记录。
func (s *Store) SaveHealthCheck(h HealthCheck) error {
	_, err := s.db.Exec(`INSERT INTO health_checks
		(provider_id,ok,status,latency_ms,error,checked_at) VALUES (?,?,?,?,?,?)`,
		h.ProviderID, boolInt(h.OK), h.Status, h.LatencyMS, h.Error, now())
	return err
}

// TrimHealthChecks 只保留每个渠道最近 keep 条巡检记录，避免表无限增长。
func (s *Store) TrimHealthChecks(keep int) error {
	if keep <= 0 {
		keep = 500
	}
	// 用窗口函数无法在 DELETE 里直接用，这里按「超过 keep 条的最老记录」删。
	_, err := s.db.Exec(`DELETE FROM health_checks WHERE id NOT IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY provider_id ORDER BY id DESC) AS rn
			FROM health_checks
		) WHERE rn <= ?
	)`, keep)
	return err
}

// ProviderUptime 返回各渠道在最近 hours 小时内的可用率摘要。
// 返回 map 便于调用方按渠道 ID 取用；无巡检数据的渠道不会出现在结果里。
func (s *Store) ProviderUptime(hours int) (map[int64]ProviderUptime, error) {
	if hours <= 0 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)

	rows, err := s.db.Query(`SELECT provider_id,
		COUNT(*),
		COALESCE(SUM(ok),0),
		COALESCE(AVG(latency_ms),0),
		MAX(checked_at)
		FROM health_checks WHERE checked_at>=? GROUP BY provider_id`, since)
	if err != nil {
		return nil, err
	}
	out := map[int64]ProviderUptime{}
	for rows.Next() {
		var u ProviderUptime
		if err := rows.Scan(&u.ProviderID, &u.Total, &u.OKCount, &u.AvgLatency, &u.LastAt); err != nil {
			rows.Close()
			return nil, err
		}
		if u.Total > 0 {
			u.Uptime = float64(u.OKCount) / float64(u.Total) * 100
		} else {
			u.Uptime = -1
		}
		out[u.ProviderID] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 补上「最近一次结果」与「连续失败次数」——两者都比平均值更能反映当下状态。
	for id, u := range out {
		var ok int
		var status int
		var errText, at string
		row := s.db.QueryRow(`SELECT ok,status,error,checked_at FROM health_checks
			WHERE provider_id=? ORDER BY id DESC LIMIT 1`, id)
		if err := row.Scan(&ok, &status, &errText, &at); err == nil {
			u.LastOK = ok == 1
			u.LastError = errText
			u.LastAt = at
		}
		// 连续失败：从最新往前数，遇到第一条成功就停。
		row = s.db.QueryRow(`SELECT COUNT(*) FROM health_checks
			WHERE provider_id=? AND id > COALESCE((
				SELECT MAX(id) FROM health_checks WHERE provider_id=? AND ok=1
			), -1)`, id, id)
		if err := row.Scan(&u.ConsecutiveFails); err != nil {
			u.ConsecutiveFails = 0
		}
		out[id] = u
	}
	return out, nil
}

// LastHealthOKMap 返回每个渠道最近一次巡检是否通过。
// 从未巡检过的渠道不出现在结果里。
// 供「渠道可用性」判定使用：无密钥但最近连通正常的自建渠道应视为可用。
func (s *Store) LastHealthOKMap() (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT provider_id, ok FROM health_checks
		WHERE id IN (SELECT MAX(id) FROM health_checks GROUP BY provider_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		var ok bool
		if err := rows.Scan(&id, &ok); err != nil {
			return nil, err
		}
		out[id] = ok
	}
	return out, rows.Err()
}

// RecentHealthChecks 返回某渠道最近的巡检记录，供面板画趋势。
// providerID <= 0 时返回全部渠道的最近记录。
func (s *Store) RecentHealthChecks(providerID int64, limit int) ([]HealthCheck, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id,provider_id,ok,status,latency_ms,error,checked_at FROM health_checks`
	args := []any{}
	if providerID > 0 {
		q += ` WHERE provider_id=?`
		args = append(args, providerID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HealthCheck{}
	for rows.Next() {
		var h HealthCheck
		var ok int
		if err := rows.Scan(&h.ID, &h.ProviderID, &ok, &h.Status, &h.LatencyMS, &h.Error, &h.CheckedAt); err != nil {
			return nil, err
		}
		h.OK = ok == 1
		out = append(out, h)
	}
	return out, rows.Err()
}

// HealthCheckCount 返回巡检记录总条数，供面板展示。
func (s *Store) HealthCheckCount() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM health_checks`).Scan(&n)
	return n, err
}

// ExpiringAPIKeys 返回在 withinDays 天内到期的启用秘钥。
// ExpiresAt 为空表示永不过期，直接跳过；日期解析失败的条目也跳过，
// 不让一条脏数据破坏整份日报。
func (s *Store) ExpiringAPIKeys(withinDays int) ([]APIKey, error) {
	if withinDays <= 0 {
		return nil, nil
	}
	keys, err := s.ListAPIKeys()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().AddDate(0, 0, withinDays)
	out := []APIKey{}
	for _, k := range keys {
		raw := strings.TrimSpace(k.ExpiresAt)
		if raw == "" {
			continue
		}
		t, err := parseExpiry(raw)
		if err != nil {
			continue
		}
		if !t.After(deadline) {
			out = append(out, k)
		}
	}
	return out, nil
}

// parseExpiry 兼容 "2006-01-02" 与 RFC3339 两种日期写法。
func parseExpiry(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析日期：%s", raw)
}
