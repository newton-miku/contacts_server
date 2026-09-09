package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// 日志分类（与前端筛选一致）。
const (
	LogCategoryContact = "contact" // 联系人数据变更
	LogCategoryAuth    = "auth"    // 登录/登出/改密
	LogCategoryAccount = "account" // 管理员账号管理
	LogCategoryUnit    = "unit"    // 单位维护
	LogCategoryDevice  = "device"  // 设备激活 / 激活码管理
)

// Log 一条审计日志。UserID==0 表示匿名/未成功识别的操作（如登录失败）。
type Log struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"createdAt"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	Category  string `json:"category"`
	Action    string `json:"action"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
}

// AppendLog 追加一条审计日志。username 为空则落空串，userID<=0 落 NULL。
func (s *Store) AppendLog(ctx context.Context, userID int64, username, category, action, detail, ip string) error {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO logs(created_at, user_id, username, category, action, detail, ip)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339), uid, username, category, action, detail, ip)
	return err
}

// LogQuery 日志筛选条件。time 前缀支持日期串或 RFC3339。
type LogQuery struct {
	Category string
	Username string // 模糊匹配
	From     string
	To       string // 含端点（闭区间）
	Limit    int
	Offset   int
}

// QueryLogs 按条件倒序分页返回日志，同时给出符合条件的总条数。
func (s *Store) QueryLogs(ctx context.Context, q LogQuery) (total int, items []Log, err error) {
	where, args := logsWhere(q)
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM logs`+where, args...).Scan(&total); err != nil {
		return 0, nil, err
	}

	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	query := `SELECT id, created_at, COALESCE(user_id,0), COALESCE(username,''), category, action, detail, ip
	            FROM logs` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, q.Limit, q.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items = []Log{}
	for rows.Next() {
		var l Log
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.UserID, &l.Username, &l.Category, &l.Action, &l.Detail, &l.IP); err != nil {
			return 0, nil, err
		}
		items = append(items, l)
	}
	return total, items, rows.Err()
}

func logsWhere(q LogQuery) (string, []any) {
	var conds []string
	var args []any
	if q.Category != "" {
		conds = append(conds, "category = ?")
		args = append(args, q.Category)
	}
	if q.Username != "" {
		conds = append(conds, "username LIKE ?")
		args = append(args, "%"+q.Username+"%")
	}
	if v := expandTimeRangeStart(q.From); v != "" {
		conds = append(conds, "created_at >= ?")
		args = append(args, v)
	}
	if v := expandTimeRangeEnd(q.To); v != "" {
		conds = append(conds, "created_at <= ?")
		args = append(args, v)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// expandTimeRangeStart/End 把“日期串”扩展成 RFC3339 边界；非日期串原样返回。
// created_at 一律存 UTC RFC3339 文本，字符串比较即时间序比较。
func expandTimeRangeStart(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) == 10 { // yyyy-mm-dd
		return v + "T00:00:00Z"
	}
	return v
}

func expandTimeRangeEnd(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) == 10 {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return v
		}
		return t.Add(24 * time.Hour).UTC().Format("2006-01-02") + "T00:00:00Z"
	}
	return v
}

// LogRetentionDays 返回审计日志保留天数（meta 配置，默认 90；0 = 不自动清理）。
func (s *Store) LogRetentionDays(ctx context.Context) (int, error) {
	v, err := s.metaGet(ctx, "log_retention_days")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 90, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 90, nil
	}
	return n, nil
}

// SetLogRetentionDays 设置日志保留天数（root；0 = 关闭自动清理）。
func (s *Store) SetLogRetentionDays(ctx context.Context, n int) error {
	return s.metaSet(ctx, s.db, "log_retention_days", strconv.Itoa(n))
}

// CleanupLogs 删除早于保留期的审计日志，返回删除行数；days<=0 时不做任何事。
func (s *Store) CleanupLogs(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `DELETE FROM logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
