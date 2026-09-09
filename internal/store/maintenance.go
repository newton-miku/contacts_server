package store

import (
	"context"
	"os"
	"strconv"
	"time"
)

// DBFileSize 返回数据库文件字节数（不存在返回 0）。
func (s *Store) DBFileSize() int64 {
	fi, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// DBPath 返回数据库文件路径。
func (s *Store) DBPath() string { return s.path }

// Vacuum 整理数据库（VACUUM）：把删除行释放的页归还给文件，文件才会真正缩小。
// 会短暂锁库；需要约 2 倍临时磁盘空间。
// WAL 模式下 VACUUM 重写的页面会先写入 WAL，主库文件要等检查点才缩小——
// 这里紧接着做一次 TRUNCATE 检查点，让磁盘空间立刻归还。
func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	var busy, logPages, checkpointed int
	// PRAGMA 会返回一行三列；失败（如被并发读者占用）不视为致命错误。
	_ = s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed)
	return nil
}

// MaintenanceTime 返回每日维护时刻（meta 配置，默认 "03:30"，本地时间 HH:MM）。
func (s *Store) MaintenanceTime(ctx context.Context) (string, error) {
	v, err := s.metaGet(ctx, "maintenance_time")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "03:30", nil
	}
	return v, nil
}

// SetMaintenanceTime 设置每日维护时刻（root）。
func (s *Store) SetMaintenanceTime(ctx context.Context, hhmm string) error {
	return s.metaSet(ctx, s.db, "maintenance_time", hhmm)
}

// AutoVacuumEnabled 是否在清理有删除时自动整理数据库（默认开）。
func (s *Store) AutoVacuumEnabled(ctx context.Context) (bool, error) {
	v, err := s.metaGet(ctx, "auto_vacuum")
	if err != nil {
		return false, err
	}
	return v != "0", nil
}

// SetAutoVacuum 设置自动整理开关（root）。
func (s *Store) SetAutoVacuum(ctx context.Context, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.metaSet(ctx, s.db, "auto_vacuum", v)
}

// NextMaintenanceAt 解析 HH:MM，返回下一个该时刻（本地时区）；解析失败回退 03:30。
func NextMaintenanceAt(hhmm string, now time.Time) time.Time {
	h, m := 3, 30
	if idx := indexByte(hhmm, ':'); idx > 0 {
		if v, err := strconv.Atoi(hhmm[:idx]); err == nil && v >= 0 && v < 24 {
			h = v
		}
		if v, err := strconv.Atoi(hhmm[idx+1:]); err == nil && v >= 0 && v < 60 {
			m = v
		}
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
