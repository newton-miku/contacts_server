package store

import (
	"context"
	"database/sql"
	"time"
)

// 账号角色。
const (
	RoleSuper   = "super"   // 超级管理员：管单位/管理员/日志 + 全部数据
	RoleUnit    = "unit"    // 单位管理员：只能管绑定单位的联系人
	RoleEndUser = "enduser" // 只读人员账号：仅手机端可登录，可见范围=绑定单位 ∪ 公开单位
)

// User 后台账号。role 取 RoleSuper/RoleUnit/RoleEndUser。
// MustChangePassword=1 表示该账号仍在使用初始/重置的密码，登录后必须先改密才能做其它操作。
// UnitIDs/UnitNames 为可选项，查询「管理端账号列表/me」时由 store 一并带出（super 无绑定）。
type User struct {
	ID                 int64    `json:"id"`
	Username           string   `json:"username"`
	PasswordHash       string   `json:"-"`
	DisplayName        string   `json:"displayName,omitempty"`
	Role               string   `json:"role"`
	IsActive           bool     `json:"isActive"`
	MustChangePassword bool     `json:"mustChangePassword"`
	CreatedAt          string   `json:"createdAt"`
	UnitIDs            []int64  `json:"unitIds,omitempty"`
	UnitNames          []string `json:"unitNames,omitempty"`
}

const userCols = `id, username, password_hash, display_name, role, is_active, must_change_password, created_at`

// CreateUser 创建账号（密码哈希由调用方算好传入）。
// mustChangePassword=true 时账号首次登录会被强制要求改密。
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, displayName, role string, mustChangePassword bool) (User, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	mcp := 0
	if mustChangePassword {
		mcp = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users(username, password_hash, display_name, role, is_active, must_change_password, created_at)
		 VALUES(?, ?, ?, ?, 1, ?, ?)`,
		username, passwordHash, displayName, role, mcp, now)
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return s.userByID(ctx, id)
}

// UserByUsername 按用户名查账号。
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) userByID(ctx context.Context, id int64) (User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// UserByID 返回账号（带绑定单位信息）；不存在返回 ErrNotFound。
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.userByID(ctx, id)
}

// UserWithUnits 返回账号及其绑定单位（id/名称）；超管无绑定返回空。
func (s *Store) UserWithUnits(ctx context.Context, id int64) (User, error) {
	u, err := s.userByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if u.Role == RoleSuper {
		return u, nil
	}
	units, err := s.UserUnits(ctx, id)
	if err != nil {
		return User{}, err
	}
	for _, un := range units {
		u.UnitIDs = append(u.UnitIDs, un.ID)
		u.UnitNames = append(u.UnitNames, un.Name)
	}
	return u, nil
}

// HasUsers 是否存在任意账号（用于“首次种子”判断）。
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListUsers 返回全部账号并按需带出绑定单位（角色 super 无单位）。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 一次查询取全部绑定关系，避免 N+1。
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	if len(users) == 0 {
		return users, nil
	}
	idMap, nameMap, err := s.unitsByUserIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range users {
		users[i].UnitIDs = idMap[users[i].ID]
		users[i].UnitNames = nameMap[users[i].ID]
	}
	return users, nil
}

// unitsByUserIDs 一次查出多个用户的绑定单位：返回 map[userID]→id 列表 与 map[userID]→名称列表。
func (s *Store) unitsByUserIDs(ctx context.Context, userIDs []int64) (map[int64][]int64, map[int64][]string, error) {
	idOut := map[int64][]int64{}
	nameOut := map[int64][]string{}
	if len(userIDs) == 0 {
		return idOut, nameOut, nil
	}
	phs := make([]string, len(userIDs))
	args := make([]any, len(userIDs))
	for i, id := range userIDs {
		phs[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT uu.user_id, u.id, u.name
		   FROM user_units uu JOIN units u ON u.id = uu.unit_id
		  WHERE uu.user_id IN (`+join(phs, ", ")+`)
		  ORDER BY u.sort_order, u.id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID, unitID int64
		var name string
		if err := rows.Scan(&userID, &unitID, &name); err != nil {
			return nil, nil, err
		}
		idOut[userID] = append(idOut[userID], unitID)
		nameOut[userID] = append(nameOut[userID], name)
	}
	return idOut, nameOut, rows.Err()
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func scanUser(row Scanner) (User, error) {
	var u User
	var displayName sql.NullString
	var isActive, mustChange int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &displayName, &u.Role, &isActive, &mustChange, &u.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.DisplayName = displayName.String
	u.IsActive = isActive == 1
	u.MustChangePassword = mustChange == 1
	return u, nil
}

// UserUnits 返回账号绑定的单位（含名称），超管返回空集。
func (s *Store) UserUnits(ctx context.Context, userID int64) ([]Unit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.name, u.sort_order, u.created_at
		   FROM user_units uu JOIN units u ON u.id = uu.unit_id
		  WHERE uu.user_id = ? ORDER BY u.sort_order, u.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	units := []Unit{}
	for rows.Next() {
		var u Unit
		if err := rows.Scan(&u.ID, &u.Name, &u.SortOrder, &u.CreatedAt); err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, rows.Err()
}

// UserUnitIDs 返回账号绑定的单位 id 列表（超管无绑定，返回空切片）。
func (s *Store) UserUnitIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT unit_id FROM user_units WHERE user_id = ? ORDER BY unit_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetUserUnits 整表替换账号的单位绑定（幂等；重复 id 去重）。
func (s *Store) SetUserUnits(ctx context.Context, userID int64, unitIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM user_units WHERE user_id = ?`, userID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range unitIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_units(user_id, unit_id) VALUES(?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateUserProfile 更新账号的角色/显示名/启用状态并整表替换绑定单位，一步事务完成。
// 不存在返回 ErrNotFound。
func (s *Store) UpdateUserProfile(ctx context.Context, id int64, displayName, role string, isActive bool, unitIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	act := 0
	if isActive {
		act = 1
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET display_name = ?, role = ?, is_active = ? WHERE id = ?`,
		displayName, role, act, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_units WHERE user_id = ?`, id); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, uid := range unitIDs {
		if uid <= 0 || seen[uid] {
			continue
		}
		seen[uid] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_units(user_id, unit_id) VALUES(?, ?)`, id, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResetUserPassword 重置某账号密码（哈希由调用方算好），并置 must_change_password=1，
// 使其下次登录被强制改密。会话失效由调用方另行处理（见 DeleteUserSessions）。
func (s *Store) ResetUserPassword(ctx context.Context, id int64, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, must_change_password = 1 WHERE id = ?`,
		passwordHash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- 会话 ----

// CreateSession 记录一个会话（tokenHash 由调用方传入；这里只存哈希）。
func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash, expiresAt string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	// 顺手清掉已过期的会话，避免 sessions 无界增长。
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)`,
		tokenHash, userID, now, expiresAt)
	return err
}

// UserForSession 根据 token 哈希返回对应用户（须账号有效且会话未过期）；过期会话会被删除。
func (s *Store) UserForSession(ctx context.Context, tokenHash string) (User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.password_hash, u.display_name, u.role, u.is_active,
		        u.must_change_password, u.created_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = ?`, tokenHash)
	u, err := scanUser(row)
	if err != nil {
		if err == ErrNotFound {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	if !u.IsActive {
		return User{}, ErrNotFound
	}
	// 校验是否过期
	var expiresAt string
	if err := s.db.QueryRowContext(ctx,
		`SELECT expires_at FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&expiresAt); err != nil {
		return User{}, ErrNotFound
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().UTC().After(t) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		return User{}, ErrNotFound
	}
	return u, nil
}

// DeleteSession 注销会话（幂等）。
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteUserSessions 使指定用户的全部会话失效（重置密码/停用后常用）。
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// UpdateUserPassword 更新用户密码哈希，并清除「必须改密」标记（改密即完成首次设置）。
func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?`,
		passwordHash, userID)
	return err
}

// DeleteUserSessionsExcept 使指定用户除当前会话外的其它会话全部失效
// （改密码后常用）；keepTokenHash 传空串则全部失效。
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID int64, keepTokenHash string) error {
	if keepTokenHash == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepTokenHash)
	return err
}
