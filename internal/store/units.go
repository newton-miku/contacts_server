package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrUnitInUse 表示单位下仍有联系人（外键 RESTRICT），不能删除。
var ErrUnitInUse = errors.New("store: unit in use")

// Unit 对应预定义的单位列表；联系人挂在其下，单位管理员按绑定单位管数据。
// IsPublic=1 表示「公开单位」：其联系人任何登录用户（enduser/unit）可见（“全局可见”）。
type Unit struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sortOrder"`
	CreatedAt string `json:"createdAt"`
	IsPublic  bool   `json:"isPublic"`
}

const unitCols = `id, name, sort_order, created_at, is_public`

func scanUnit(row Scanner) (Unit, error) {
	var u Unit
	var isPublic int
	if err := row.Scan(&u.ID, &u.Name, &u.SortOrder, &u.CreatedAt, &isPublic); err != nil {
		if err == sql.ErrNoRows {
			return Unit{}, ErrNotFound
		}
		return Unit{}, err
	}
	u.IsPublic = isPublic == 1
	return u, nil
}

// ListUnits 返回全部单位（按 sort_order、id 排序）。
func (s *Store) ListUnits(ctx context.Context) ([]Unit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+unitCols+` FROM units ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	units := []Unit{}
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, rows.Err()
}

// PublicUnitIDs 返回全部公开单位 id（“全局可见”集合）。手机/普通账号的读范围 = 绑定单位 ∪ 公开单位。
func (s *Store) PublicUnitIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM units WHERE is_public = 1 ORDER BY id`)
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

// SetUnitsPublic 批量设置若干单位的公开标记（super 在单位管理页“全选→设公开/取消公开”用）。
// 返回实际受影响行数；ids 为空时直接返回 0。
func (s *Store) SetUnitsPublic(ctx context.Context, ids []int64, public bool) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	pub := 0
	if public {
		pub = 1
	}
	phs := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, pub)
	for _, id := range ids {
		phs = append(phs, "?")
		args = append(args, id)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE units SET is_public = ? WHERE id IN (`+strings.Join(phs, ", ")+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// UnitExists 判断单位是否存在（写联系人前校验用）。
func (s *Store) UnitExists(ctx context.Context, id int64) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM units WHERE id = ?`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UnitByID 按 id 查单位；不存在返回 ErrNotFound。
func (s *Store) UnitByID(ctx context.Context, id int64) (Unit, error) {
	var u Unit
	var isPublic int
	err := s.db.QueryRowContext(ctx,
		`SELECT `+unitCols+` FROM units WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.SortOrder, &u.CreatedAt, &isPublic)
	if err == sql.ErrNoRows {
		return Unit{}, ErrNotFound
	}
	if err != nil {
		return Unit{}, err
	}
	u.IsPublic = isPublic == 1
	return u, nil
}

// CreateUnit 新建单位。同名单位已存在时返回 ErrDuplicateName。
func (s *Store) CreateUnit(ctx context.Context, name string, sortOrder int, isPublic bool) (Unit, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Unit{}, errors.New("单位名称不能为空")
	}
	isP := 0
	if isPublic {
		isP = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO units(name, sort_order, is_public, created_at) VALUES(?, ?, ?, ?)`,
		name, sortOrder, isP, now)
	if err != nil {
		if isUniqueViolation(err) {
			return Unit{}, ErrDuplicateName
		}
		return Unit{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Unit{}, err
	}
	return s.UnitByID(ctx, id)
}

// EnsureUnits 为批量导入解析单位：返回「名称→id」映射，CSV 里出现但尚不存在的单位
// （名称 trim 后非空）会在同一事务内自动创建，排序排在现有单位之后。返回值 created 是
// 本批新建的单位（按首现顺序），供出错时回滚删除/记日志。新建空单位不递增 data_version
// （不改变任何联系人数据，客户端无需重拉）。**导入时自动创建的单位默认设为公开单位
// （is_public=1）**——即「导入的单位默认全公开」，这样手机只读/普通账号可见整本目录。
func (s *Store) EnsureUnits(ctx context.Context, names []string) (byName map[string]int64, created []Unit, err error) {
	byName = make(map[string]int64)
	want := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		want = append(want, n)
	}
	if len(want) == 0 {
		return byName, nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var maxSort int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) FROM units`).Scan(&maxSort); err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, name := range want {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM units WHERE name = ?`, name).Scan(&id)
		if err == nil {
			byName[name] = id
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		maxSort++
		res, err := tx.ExecContext(ctx,
			`INSERT INTO units(name, sort_order, is_public, created_at) VALUES(?, ?, 1, ?)`,
			name, maxSort, now)
		if err != nil {
			if isUniqueViolation(err) {
				// 并发下已被别的事务建好：再查一次即可。
				if err2 := tx.QueryRowContext(ctx, `SELECT id FROM units WHERE name = ?`, name).Scan(&id); err2 == nil {
					byName[name] = id
					continue
				}
			}
			return nil, nil, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return nil, nil, err
		}
		byName[name] = id
		created = append(created, Unit{ID: id, Name: name, SortOrder: maxSort, IsPublic: true, CreatedAt: now})
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return byName, created, nil
}

// UpdateUnit 修改单位名称/排序/公开标记；不存在返回 ErrNotFound，重名返回 ErrDuplicateName。
func (s *Store) UpdateUnit(ctx context.Context, id int64, name string, sortOrder int, isPublic bool) (Unit, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Unit{}, errors.New("单位名称不能为空")
	}
	isP := 0
	if isPublic {
		isP = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE units SET name = ?, sort_order = ?, is_public = ? WHERE id = ?`,
		name, sortOrder, isP, id)
	if err != nil {
		if isUniqueViolation(err) {
			return Unit{}, ErrDuplicateName
		}
		return Unit{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Unit{}, ErrNotFound
	}
	return s.UnitByID(ctx, id)
}

// DeleteUnit 删除单位。其下仍有联系人时返回 ErrUnitInUse（FK RESTRICT 兜底）。
// 单位管理员绑定关系随 user_units 的 ON DELETE CASCADE 一并清理。
func (s *Store) DeleteUnit(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM contacts WHERE unit_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrUnitInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM units WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if cnt, _ := res.RowsAffected(); cnt == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrDuplicateName 单位/账号重名。
var ErrDuplicateName = errors.New("store: duplicate name")

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "unique")
}
