package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestTrimUsageLogsBefore 验证调用日志按天清理：
// 早于 cutoff 的记录被删除，新记录保留；空 cutoff 不做任何事。
func TestTrimUsageLogsBefore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "trim.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer s.Close()

	add := func(createdAt, model string) {
		s.AddUsageLog(UsageLog{CreatedAt: createdAt, Capability: "chat", Model: model, Status: 200})
	}
	old := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)
	recent := time.Now().Add(-time.Hour).Format(time.RFC3339)
	add(old, "old-model-a")
	add(old, "old-model-b")
	add(recent, "new-model")

	// 清理 5 天前的记录。
	cutoff := time.Now().AddDate(0, 0, -5).Format(time.RFC3339)
	s.TrimUsageLogsBefore(cutoff)

	logs, err := s.ListUsageLogs(100, 0, "", "")
	if err != nil {
		t.Fatalf("读取日志失败：%v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("按天清理后应剩 1 条，实际 %d 条", len(logs))
	}
	if logs[0].Model != "new-model" {
		t.Fatalf("留下的应是新日志，实际模型 %s", logs[0].Model)
	}

	// 空 cutoff 是无害调用。
	s.TrimUsageLogsBefore("")
}
