package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ---- 激活码 ----

// activationAlphabet 去掉易混字符 0/O/1/I，共 32 个 → 每字符 5 bit，10 位 ≈ 50 bit 熵。
const activationAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ActivationCodeLen 激活码长度（10 位字母+数字，输入不区分大小写）。
const ActivationCodeLen = 10

// 激活码状态（计算得出，不入库）。
const (
	CodeStatusPending = "pending"
	CodeStatusUsed    = "used"
	CodeStatusExpired = "expired"
	CodeStatusRevoked = "revoked"
)

// ActivationCode 一次性激活码：绑定手机号，激活后应用其可见范围（单位 + 单点联系人）。
// 明文保存 code 是为了管理员能在后台查看/导出（一次性 + 有效期 + 可作废 + 审计作为补偿控制）。
type ActivationCode struct {
	ID          int64    `json:"id"`
	Code        string   `json:"code"`
	Phone       string   `json:"phone"`
	DisplayName string   `json:"displayName,omitempty"`
	Role        string   `json:"role"`
	UnitIDs     []int64  `json:"unitIds"`
	ContactIDs  []int64  `json:"contactIds"`
	CreatedBy   int64    `json:"createdBy"`
	CreatedAt   string   `json:"createdAt"`
	ExpiresAt   string   `json:"expiresAt"`
	UsedAt      string   `json:"usedAt,omitempty"`
	RevokedAt   string   `json:"revokedAt,omitempty"`
	Status      string   `json:"status"`
}

// NewActivationCode 创建激活码的入参。
type NewActivationCode struct {
	Phone       string
	DisplayName string
	Role        string
	UnitIDs     []int64
	ContactIDs  []int64
	ExpiresAt   string
	CreatedBy   int64
}

// GenerateActivationCode 生成一个 10 位激活码（32 字符表，均匀分布）。
func GenerateActivationCode() (string, error) {
	buf := make([]byte, ActivationCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, ActivationCodeLen)
	for i, b := range buf {
		out[i] = activationAlphabet[int(b)%len(activationAlphabet)]
	}
	return string(out), nil
}

// NormalizeActivationCode 归一化用户输入的激活码：去空格/连字符并转大写。
func NormalizeActivationCode(in string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(in)) {
		if r == ' ' || r == '-' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

const codeCols = `id, code, phone, display_name, role, unit_ids_json, contact_ids_json,
	created_by, created_at, expires_at, used_at, revoked_at`

func scanCode(row Scanner) (ActivationCode, error) {
	var c ActivationCode
	var name, usedAt, revokedAt sql.NullString
	var unitsJSON, contactsJSON string
	if err := row.Scan(&c.ID, &c.Code, &c.Phone, &name, &c.Role, &unitsJSON, &contactsJSON,
		&c.CreatedBy, &c.CreatedAt, &c.ExpiresAt, &usedAt, &revokedAt); err != nil {
		if err == sql.ErrNoRows {
			return ActivationCode{}, ErrNotFound
		}
		return ActivationCode{}, err
	}
	c.DisplayName = name.String
	c.UsedAt = usedAt.String
	c.RevokedAt = revokedAt.String
	_ = json.Unmarshal([]byte(unitsJSON), &c.UnitIDs)
	_ = json.Unmarshal([]byte(contactsJSON), &c.ContactIDs)
	c.Status = codeStatus(c, time.Now().UTC())
	return c, nil
}

func codeStatus(c ActivationCode, now time.Time) string {
	switch {
	case c.RevokedAt != "":
		return CodeStatusRevoked
	case c.UsedAt != "":
		return CodeStatusUsed
	default:
		if t, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil && now.After(t) {
			return CodeStatusExpired
		}
		return CodeStatusPending
	}
}

// CreateActivationCodes 批量写入激活码（同一事务）。
func (s *Store) CreateActivationCodes(ctx context.Context, items []NewActivationCode) ([]ActivationCode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]ActivationCode, 0, len(items))
	for _, it := range items {
		unitsJSON, _ := json.Marshal(it.UnitIDs)
		contactsJSON, _ := json.Marshal(it.ContactIDs)
		role := it.Role
		if role == "" {
			role = RoleEndUser
		}
		var id int64
		// 极小概率撞码：重试若干次。
		for attempt := 0; ; attempt++ {
			code, err := GenerateActivationCode()
			if err != nil {
				return nil, err
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO activation_codes(code, phone, display_name, role, unit_ids_json, contact_ids_json,
					created_by, created_at, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				code, it.Phone, it.DisplayName, role, string(unitsJSON), string(contactsJSON),
				it.CreatedBy, now, it.ExpiresAt)
			if err != nil {
				if strings.Contains(err.Error(), "UNIQUE") && attempt < 5 {
					continue
				}
				return nil, err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return nil, err
			}
			break
		}
		c, err := codeByIDTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func codeByIDTx(ctx context.Context, q querier, id int64) (ActivationCode, error) {
	return scanCode(q.QueryRowContext(ctx, `SELECT `+codeCols+` FROM activation_codes WHERE id = ?`, id))
}

// ActivationCodeByCode 按激活码查询（code 需已归一化）。
func (s *Store) ActivationCodeByCode(ctx context.Context, code string) (ActivationCode, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+codeCols+` FROM activation_codes WHERE code = ?`, code)
	return scanCode(row)
}

// CountActivationCodesSince 统计某时刻之后生成的激活码数量（用于每日上限）。
func (s *Store) CountActivationCodesSince(ctx context.Context, since string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM activation_codes WHERE created_at >= ?`, since).Scan(&n)
	return n, err
}

// ListActivationCodes 列出激活码；createdBy>0 时只看该管理员生成的；status 为空表示全部。
func (s *Store) ListActivationCodes(ctx context.Context, createdBy int64, status string, limit int) ([]ActivationCode, error) {
	where := []string{"1 = 1"}
	args := []any{}
	if createdBy > 0 {
		where = append(where, "created_by = ?")
		args = append(args, createdBy)
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT ` + codeCols + ` FROM activation_codes WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActivationCode{}
	for rows.Next() {
		c, err := scanCode(rows)
		if err != nil {
			return nil, err
		}
		if status != "" && c.Status != status {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCodeUsed 标记激活码已使用（一次性）。
func (s *Store) MarkCodeUsed(ctx context.Context, id, deviceID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE activation_codes SET used_at = ?, used_device_id = ? WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL`,
		now, deviceID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeActivationCode 作废未使用的激活码。
func (s *Store) RevokeActivationCode(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE activation_codes SET revoked_at = ? WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ActivationDailyLimit 每日可生成的激活码上限（meta 配置，默认 100）。
func (s *Store) ActivationDailyLimit(ctx context.Context) (int, error) {
	v, err := s.metaGet(ctx, "activation_daily_limit")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 100, nil
	}
	return n, nil
}

// SetActivationDailyLimit 设置每日上限（root）。
func (s *Store) SetActivationDailyLimit(ctx context.Context, n int) error {
	return s.metaSet(ctx, s.db, "activation_daily_limit", strconv.Itoa(n))
}

// ---- 设备与设备令牌 ----

// Device 一台已激活的设备。
type Device struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"userId"`
	Phone      string `json:"phone"`
	Label      string `json:"label,omitempty"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	LastIP     string `json:"lastIp,omitempty"`
}

// CreateDevice 新增设备（调用前应 RevokeUserDevices 实现“换机后旧设备失效”）。
func (s *Store) CreateDevice(ctx context.Context, userID int64, phone, label, ip string) (Device, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO devices(user_id, phone, label, status, created_at, last_seen_at, last_ip)
		 VALUES(?, ?, ?, 'active', ?, ?, ?)`, userID, phone, label, now, now, ip)
	if err != nil {
		return Device{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Device{}, err
	}
	return Device{ID: id, UserID: userID, Phone: phone, Label: label, Status: "active",
		CreatedAt: now, LastSeenAt: now, LastIP: ip}, nil
}

// RevokeUserDevices 撤销某用户全部在用设备及其令牌（换机语义）。
func (s *Store) RevokeUserDevices(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM device_tokens WHERE device_id IN (SELECT id FROM devices WHERE user_id = ?)`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE devices SET status = 'revoked' WHERE user_id = ? AND status = 'active'`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateDeviceToken 记录设备令牌哈希。
func (s *Store) CreateDeviceToken(ctx context.Context, deviceID int64, tokenHash, expiresAt string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO device_tokens(token_hash, device_id, created_at, expires_at) VALUES(?, ?, ?, ?)`,
		tokenHash, deviceID, now, expiresAt)
	return err
}

// UserForDeviceToken 用设备令牌换取用户（设备须在用、令牌未过期、账号有效）。
func (s *Store) UserForDeviceToken(ctx context.Context, tokenHash string) (User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.password_hash, u.display_name, u.role, u.is_active,
		        u.must_change_password, u.created_at, t.expires_at, d.id, d.status
		   FROM device_tokens t
		   JOIN devices d ON d.id = t.device_id
		   JOIN users u ON u.id = d.user_id
		  WHERE t.token_hash = ?`, tokenHash)
	var u User
	var displayName sql.NullString
	var isActive, mustChange int
	var expiresAt string
	var deviceID int64
	var deviceStatus string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &displayName, &u.Role, &isActive,
		&mustChange, &u.CreatedAt, &expiresAt, &deviceID, &deviceStatus); err != nil {
		if err == sql.ErrNoRows {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.DisplayName = displayName.String
	u.IsActive = isActive == 1
	u.MustChangePassword = mustChange == 1
	if !u.IsActive || deviceStatus != "active" {
		return User{}, ErrNotFound
	}
	if t, err := time.Parse(time.RFC3339, expiresAt); err != nil || time.Now().UTC().After(t) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM device_tokens WHERE token_hash = ?`, tokenHash)
		return User{}, ErrNotFound
	}
	return u, nil
}

// TouchDevice 刷新设备最近活跃时间/IP。
func (s *Store) TouchDevice(ctx context.Context, tokenHash, ip string) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ?, last_ip = ?
		  WHERE id = (SELECT device_id FROM device_tokens WHERE token_hash = ?)`,
		time.Now().UTC().Format(time.RFC3339), ip, tokenHash)
}

// DeviceByUser 返回某用户当前在用设备（无则 ErrNotFound）。
func (s *Store) DeviceByUser(ctx context.Context, userID int64) (Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, phone, COALESCE(label, ''), status, created_at,
		        COALESCE(last_seen_at, ''), COALESCE(last_ip, '')
		   FROM devices WHERE user_id = ? AND status = 'active' ORDER BY id DESC LIMIT 1`, userID)
	var d Device
	if err := row.Scan(&d.ID, &d.UserID, &d.Phone, &d.Label, &d.Status, &d.CreatedAt,
		&d.LastSeenAt, &d.LastIP); err != nil {
		if err == sql.ErrNoRows {
			return Device{}, ErrNotFound
		}
		return Device{}, err
	}
	return d, nil
}

// ErrCodeNotUsable 激活码不可用（已用/过期/作废/手机号不匹配由调用方区分）。
var ErrCodeNotUsable = errors.New("store: activation code not usable")
