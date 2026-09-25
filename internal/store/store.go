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
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
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

CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// 老库升级：models 表补充 provider_ids 列，用于「一个模型聚合多个上游」。
	if err := s.addColumnIfMissing("models", "provider_ids", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	return s.seedBuiltinSkills()
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
