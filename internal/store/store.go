// Package store 封装 SQLite 持久化：渠道、模型、对外秘钥、技能、视频任务与调用日志。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示目标记录不存在。
var ErrNotFound = errors.New("记录不存在")

type Store struct {
	db *sql.DB
}

// Open 打开（并在需要时创建）数据库，随后执行建表迁移。
func Open(path string) (*Store, error) {
	// synchronous(NORMAL) 在 WAL 模式下是官方推荐档位：不用每次提交都 fsync，
	// 写吞吐提升明显；代价是断电时可能丢最后一个事务，但不会损坏数据库。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 为纯 Go 实现，写并发需收敛连接数避免锁冲突。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS providers (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL UNIQUE,
  kind        TEXT    NOT NULL,
  base_urls   TEXT    NOT NULL DEFAULT '[]',
  api_keys    TEXT    NOT NULL DEFAULT '[]',
  headers     TEXT    NOT NULL DEFAULT '{}',
  strategy    TEXT    NOT NULL DEFAULT 'round_robin',
  timeout_sec INTEGER NOT NULL DEFAULT 600,
  enabled     INTEGER NOT NULL DEFAULT 1,
  remark      TEXT    NOT NULL DEFAULT '',
  created_at  TEXT    NOT NULL,
  updated_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS models (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id   INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  provider_ids  TEXT    NOT NULL DEFAULT '[]',
  inputs        TEXT    NOT NULL DEFAULT '[]',
  name          TEXT    NOT NULL UNIQUE,
  upstream_name TEXT    NOT NULL,
  capability    TEXT    NOT NULL,
  config        TEXT    NOT NULL DEFAULT '{}',
  enabled       INTEGER NOT NULL DEFAULT 1,
  remark        TEXT    NOT NULL DEFAULT '',
  created_at    TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL,
  key          TEXT    NOT NULL UNIQUE,
  models       TEXT    NOT NULL DEFAULT '[]',
  enabled      INTEGER NOT NULL DEFAULT 1,
  expires_at   TEXT    NOT NULL DEFAULT '',
  remark       TEXT    NOT NULL DEFAULT '',
  rpm          INTEGER NOT NULL DEFAULT 0,
  daily_quota  INTEGER NOT NULL DEFAULT 0,
  daily_tokens INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT    NOT NULL,
  last_used_at TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS skills (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  slug         TEXT    NOT NULL UNIQUE,
  name         TEXT    NOT NULL,
  description  TEXT    NOT NULL DEFAULT '',
  category     TEXT    NOT NULL DEFAULT '通用',
  icon         TEXT    NOT NULL DEFAULT '',
  capability   TEXT    NOT NULL DEFAULT 'chat',
  prompt       TEXT    NOT NULL DEFAULT '',
  params       TEXT    NOT NULL DEFAULT '[]',
  builtin      INTEGER NOT NULL DEFAULT 0,
  enabled      INTEGER NOT NULL DEFAULT 1,
  sort_order   INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS video_tasks (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id          TEXT    NOT NULL UNIQUE,
  model            TEXT    NOT NULL,
  provider_id      INTEGER NOT NULL,
  upstream_task_id TEXT    NOT NULL DEFAULT '',
  upstream_url     TEXT    NOT NULL DEFAULT '',
  status           TEXT    NOT NULL DEFAULT 'queued',
  progress         INTEGER NOT NULL DEFAULT 0,
  request          TEXT    NOT NULL DEFAULT '{}',
  result           TEXT    NOT NULL DEFAULT '{}',
  error            TEXT    NOT NULL DEFAULT '',
  api_key_id       INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT    NOT NULL,
  updated_at       TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS usage_logs (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at        TEXT    NOT NULL,
  capability        TEXT    NOT NULL,
  model             TEXT    NOT NULL,
  provider_name     TEXT    NOT NULL DEFAULT '',
  api_key_name      TEXT    NOT NULL DEFAULT '',
  status            INTEGER NOT NULL DEFAULT 0,
  stream            INTEGER NOT NULL DEFAULT 0,
  latency_ms        INTEGER NOT NULL DEFAULT 0,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  error             TEXT    NOT NULL DEFAULT '',
  request_body      TEXT    NOT NULL DEFAULT '',
  response_preview  TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_usage_logs_created ON usage_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_video_tasks_created ON video_tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_models_provider ON models(provider_id);

CREATE TABLE IF NOT EXISTS catalog_models (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id       INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  model_id          TEXT    NOT NULL,
  capability        TEXT    NOT NULL DEFAULT 'chat',
  source            TEXT    NOT NULL DEFAULT 'probe',
  enabled           INTEGER NOT NULL DEFAULT 1,
  context_length    INTEGER NOT NULL DEFAULT 0,
  input_modalities  TEXT    NOT NULL DEFAULT '[]',
  output_modalities TEXT    NOT NULL DEFAULT '[]',
  remark            TEXT    NOT NULL DEFAULT '',
  created_at        TEXT    NOT NULL,
  updated_at        TEXT    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_catalog_provider_model ON catalog_models(provider_id, model_id);
CREATE INDEX IF NOT EXISTS idx_catalog_provider ON catalog_models(provider_id);

CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);

-- health_checks 记录后台巡检的每次结果，用于算可用率与画趋势。
-- 只保留近期数据（由巡检循环按上限裁剪），不做长期归档。
CREATE TABLE IF NOT EXISTS health_checks (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id INTEGER NOT NULL,
  ok          INTEGER NOT NULL DEFAULT 0,
  status      INTEGER NOT NULL DEFAULT 0,
  latency_ms  INTEGER NOT NULL DEFAULT 0,
  error       TEXT    NOT NULL DEFAULT '',
  checked_at  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_health_checks_provider ON health_checks(provider_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_health_checks_time ON health_checks(checked_at DESC);

-- upstream_stats 是「渠道 × 上游模型」的健康画像：每次真实调用滚动累计，
-- 供路由自学习（连续失败的上游排到尝试队列尾部）与面板展示，重启不丢。
CREATE TABLE IF NOT EXISTS upstream_stats (
  provider_id   INTEGER NOT NULL,
  upstream_name TEXT    NOT NULL,
  ok_count      INTEGER NOT NULL DEFAULT 0,
  fail_count    INTEGER NOT NULL DEFAULT 0,
  consec_fails  INTEGER NOT NULL DEFAULT 0,
  avg_latency   REAL    NOT NULL DEFAULT 0,
  last_ok_at    TEXT    NOT NULL DEFAULT '',
  last_fail_at  TEXT    NOT NULL DEFAULT '',
  last_error    TEXT    NOT NULL DEFAULT '',
  updated_at    TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (provider_id, upstream_name)
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// 老库升级：models 表补充 provider_ids / inputs 列，用于「一个模型聚合多个上游」。
	if err := s.addColumnIfMissing("models", "provider_ids", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("models", "inputs", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	// catalog_models 表补充上下文长度列（探测上游模型列表时自动记录）。
	if err := s.addColumnIfMissing("catalog_models", "context_length", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// catalog_models 表补充输入 / 输出模态列，供按模态路由使用。
	if err := s.addColumnIfMissing("catalog_models", "input_modalities", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("catalog_models", "output_modalities", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	// api_keys 表补充配额列。
	for _, col := range []string{"rpm", "daily_quota", "daily_tokens"} {
		if err := s.addColumnIfMissing("api_keys", col, "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	// 渠道表补充加权负载均衡权重列。
	if err := s.addColumnIfMissing("providers", "weight", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	// 渠道表补充「无版本前缀」标记：部分 OpenAI 兼容端点（如 GitHub Models）
	// 接口直接挂在根路径，不需要自动补 /v1，否则会被拼成 404。
	if err := s.addColumnIfMissing("providers", "no_version_path", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// 调用日志补充成本列，供按模型计费统计。
	if err := s.addColumnIfMissing("usage_logs", "cost", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.seedBuiltinSkills(); err != nil {
		return err
	}
	// 首次启动就把内置的免费模型装好，部署完直接可用。
	return s.seedBuiltinPresets()
}

// addColumnIfMissing 为已存在的表补充新列，用于无版本号的轻量迁移。
// 由于连接池被限定为单连接，必须在关闭游标后再执行 ALTER。
func (s *Store) addColumnIfMissing(table, column, ddl string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + ddl)
	return err
}

func now() string { return time.Now().Format(time.RFC3339) }

// NowString 返回当前时间的 RFC3339 表示，供跨包写入时间戳时统一格式。
func NowString() string { return now() }

func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func unmarshal[T any](raw string, def T) T {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return def
	}
	return out
}
