package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---------- 上游健康画像 ----------

// UpstreamStat 是「渠道 × 上游模型」的健康画像：由每次真实调用滚动累计。
// 它是路由自学习的数据基础——连续失败的上游会被排到尝试队列尾部，
// 平均延迟用 EWMA 平滑，重启不丢。
type UpstreamStat struct {
	ProviderID   int64   `json:"provider_id"`
	ProviderName string  `json:"provider_name,omitempty"`
	UpstreamName string  `json:"upstream_name"`
	OKCount      int64   `json:"ok_count"`
	FailCount    int64   `json:"fail_count"`
	ConsecFails  int     `json:"consec_fails"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	LastOKAt     string  `json:"last_ok_at,omitempty"`
	LastFailAt   string  `json:"last_fail_at,omitempty"`
	LastError    string  `json:"last_error,omitempty"`
	UpdatedAt    string  `json:"updated_at,omitempty"`
}

// SuccessRate 返回历史成功率（0-100），无记录时返回 -1 表示「未知」。
func (s UpstreamStat) SuccessRate() float64 {
	total := s.OKCount + s.FailCount
	if total == 0 {
		return -1
	}
	return float64(s.OKCount) / float64(total) * 100
}

const upstreamStatSelect = `SELECT u.provider_id,COALESCE(p.name,''),u.upstream_name,
	u.ok_count,u.fail_count,u.consec_fails,u.avg_latency,u.last_ok_at,u.last_fail_at,u.last_error,u.updated_at
	FROM upstream_stats u LEFT JOIN providers p ON p.id=u.provider_id`

// ListUpstreamStats 返回全部上游画像，按失败风险从高到低排（方便面板一眼看到问题上游）。
func (s *Store) ListUpstreamStats() ([]UpstreamStat, error) {
	rows, err := s.db.Query(upstreamStatSelect + ` ORDER BY u.consec_fails DESC, u.fail_count DESC, u.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpstreamStat{}
	for rows.Next() {
		var st UpstreamStat
		if err := rows.Scan(&st.ProviderID, &st.ProviderName, &st.UpstreamName,
			&st.OKCount, &st.FailCount, &st.ConsecFails, &st.AvgLatencyMS,
			&st.LastOKAt, &st.LastFailAt, &st.LastError, &st.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SaveUpstreamStat 以「渠道 + 上游名」为主键整行覆盖（调用方维护全量字段）。
func (s *Store) SaveUpstreamStat(st UpstreamStat) error {
	st.UpstreamName = strings.TrimSpace(st.UpstreamName)
	if st.ProviderID <= 0 || st.UpstreamName == "" {
		return errors.New("上游画像缺少渠道或上游名")
	}
	if st.UpdatedAt == "" {
		st.UpdatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT INTO upstream_stats
		(provider_id,upstream_name,ok_count,fail_count,consec_fails,avg_latency,last_ok_at,last_fail_at,last_error,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(provider_id,upstream_name) DO UPDATE SET
		ok_count=excluded.ok_count, fail_count=excluded.fail_count, consec_fails=excluded.consec_fails,
		avg_latency=excluded.avg_latency, last_ok_at=excluded.last_ok_at, last_fail_at=excluded.last_fail_at,
		last_error=excluded.last_error, updated_at=excluded.updated_at`,
		st.ProviderID, st.UpstreamName, st.OKCount, st.FailCount, st.ConsecFails,
		st.AvgLatencyMS, st.LastOKAt, st.LastFailAt, st.LastError, st.UpdatedAt)
	return err
}

// GetUpstreamStat 读取单个上游画像，不存在时返回零值与 false。
func (s *Store) GetUpstreamStat(providerID int64, upstream string) (UpstreamStat, bool, error) {
	var st UpstreamStat
	var pname string
	err := s.db.QueryRow(upstreamStatSelect+` WHERE u.provider_id=? AND u.upstream_name=?`,
		providerID, strings.TrimSpace(upstream)).Scan(&st.ProviderID, &pname, &st.UpstreamName,
		&st.OKCount, &st.FailCount, &st.ConsecFails, &st.AvgLatencyMS,
		&st.LastOKAt, &st.LastFailAt, &st.LastError, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UpstreamStat{}, false, nil
	}
	if err != nil {
		return UpstreamStat{}, false, err
	}
	st.ProviderName = pname
	return st, true, nil
}
