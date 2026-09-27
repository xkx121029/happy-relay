package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"happy-relay/internal/store"
)

// StartBackground 启动后台任务：启动自检、负载再平衡初始化、
// 视频任务轮询、日志裁剪、健康巡检、日报推送、自动备份。
func (s *Server) StartBackground(ctx context.Context) {
	s.bootSelfCheck()
	// 先按上次落库的巡检数据建立再平衡基线，
	// 这样首轮真实请求就能用上「哪个渠道又快又稳」，不用等第一次巡检跑完。
	s.refreshBalance()
	go s.videoLoop(ctx)
	go s.logTrimLoop(ctx)
	go s.backupLoop(ctx)
	s.StartHealth(ctx)
	s.StartReport(ctx)
}

// bootSelfCheck 在启动时跑一次配置自省，把结论打进日志。
// 这一步的意义在于：容器化部署时用户看不到面板，日志是唯一的线索；
// 一条「发现 3 个问题」的启动日志能省掉大量「为什么调不通」的排查。
func (s *Server) bootSelfCheck() {
	res := s.RunSelfCheck()
	if res.Ready() && res.Warn == 0 {
		log.Printf("[自检] 配置正常（%d 项检查全部通过）", res.OK)
		return
	}
	log.Printf("[自检] 发现 %d 个问题、%d 个提醒：", res.Error, res.Warn)
	for _, it := range res.Items {
		if it.Level == CheckOK {
			continue
		}
		line := "  - " + string(it.Level) + "：" + it.Title
		if it.Detail != "" {
			line += " —— " + it.Detail
		}
		if it.Hint != "" {
			line += "（" + it.Hint + "）"
		}
		log.Print(line)
	}
}

// videoLoop 周期性刷新未完成的视频任务状态。
// 每轮重新读取间隔，这样在面板上调整轮询频率后无需重启即可生效。
func (s *Server) videoLoop(ctx context.Context) {
	for {
		interval := time.Duration(s.options().VideoPollSec) * time.Second
		if interval < time.Second {
			interval = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			s.pollPendingVideos(ctx)
		}
	}
}

func (s *Server) pollPendingVideos(ctx context.Context) {
	tasks, err := s.st.ListPendingVideoTasks()
	if err != nil {
		log.Printf("视频任务轮询失败：%v", err)
		return
	}
	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		updated, err := s.eng.PollVideo(pollCtx, t)
		cancel()
		if err != nil {
			// 单次查询失败不改变任务状态，下个周期继续尝试。
			continue
		}
		if updated.Status == store.TaskSucceeded || updated.Status == store.TaskFailed {
			log.Printf("视频任务 %s 结束，状态 %s", updated.TaskID, updated.Status)
		}
	}
}

// logTrimLoop 按配置上限裁剪调用日志，避免数据库无限增长。
// 条数上限（MaxLogRows）与天数上限（LogRetentionDays，0 关闭）同时生效。
func (s *Server) logTrimLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o := s.options()
			s.st.TrimUsageLogs(o.MaxLogRows)
			if o.LogRetentionDays > 0 {
				cutoff := time.Now().AddDate(0, 0, -o.LogRetentionDays).Format(time.RFC3339)
				s.st.TrimUsageLogsBefore(cutoff)
			}
		}
	}
}

// backupLoop 无人值守自动备份：每天到达设定时刻后，把全部配置导出成 JSON
// 存到数据目录 backups/ 下，并只保留最近 N 份。每 10 分钟醒一次看时间到了没；
// 服务中途重启也能补上当天的备份（小时数已过就立刻补跑）。
func (s *Server) backupLoop(ctx context.Context) {
	lastDate := ""
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o := s.options()
			if !o.BackupAuto {
				continue
			}
			now := time.Now()
			today := now.Format("2006-01-02")
			if lastDate == today || now.Hour() < o.BackupHour {
				continue
			}
			if err := s.runAutoBackup(o.BackupKeep, today); err != nil {
				// 失败不记录日期，下个周期自动重试；打日志 + 通知通道都提醒。
				log.Printf("[自动备份] 失败：%v", err)
				s.sendDirectAlert("自动备份失败", err.Error())
				continue
			}
			lastDate = today
			log.Printf("[自动备份] 已生成 %s 的配置备份", today)
		}
	}
}

// runAutoBackup 导出全部配置写入 backups/auto-backup-<date>.json，并裁掉旧文件。
func (s *Server) runAutoBackup(keep int, date string) error {
	backup, err := s.st.ExportAll()
	if err != nil {
		return fmt.Errorf("导出配置失败：%w", err)
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建备份目录失败：%w", err)
	}
	name := filepath.Join(dir, "auto-backup-"+date+".json")
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化备份失败：%w", err)
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		return fmt.Errorf("写入备份文件失败：%w", err)
	}
	pruneBackups(dir, "auto-backup-", ".json", keep)
	return nil
}

// pruneBackups 删除同前缀的旧备份文件，只保留最近 keep 份（按文件名排序，
// 文件名含日期所以字典序即时间序）。
func pruneBackups(dir, prefix, ext string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ext && len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}
