package store

import (
	"context"
	"strings"
	"time"
)

// ContactScope 描述一次联系人读取的可见范围（服务端唯一实现，所有读接口共用）。
//
//	All        = true  → 全部（super/root，或内部调用）
//	UnitIDs    → 可见单位（绑定单位 ∪ 公开单位 ∪ 被授权的隐藏单位）
//	ContactIDs → 单点授权的联系人（隐藏单位里的个别人）
//
// 两者皆空且非 All = 一条也看不到（注意：绝不能退化成“全部”）。
type ContactScope struct {
	All        bool
	UnitIDs    []int64
	ContactIDs []int64
}

// UnitScope 用单位集合构造范围（nil = 全部，保留历史语义）。
func UnitScope(unitIDs []int64) ContactScope {
	if unitIDs == nil {
		return ContactScope{All: true}
	}
	return ContactScope{UnitIDs: unitIDs}
}

// Empty 判断是否“一条也看不到”。
func (sc ContactScope) Empty() bool {
	return !sc.All && len(sc.UnitIDs) == 0 && len(sc.ContactIDs) == 0
}

// contactScopeOf 生成 WHERE 片段：单位命中 或 单点授权命中。
func contactScopeOf(sc ContactScope) (string, []any) {
	if sc.All {
		return "", nil
	}
	parts := make([]string, 0, 2)
	args := make([]any, 0, len(sc.UnitIDs)+len(sc.ContactIDs))
	if len(sc.UnitIDs) > 0 {
		phs := make([]string, len(sc.UnitIDs))
		for i, id := range sc.UnitIDs {
			phs[i] = "?"
			args = append(args, id)
		}
		parts = append(parts, "c.unit_id IN ("+strings.Join(phs, ", ")+")")
	}
	if len(sc.ContactIDs) > 0 {
		phs := make([]string, len(sc.ContactIDs))
		for i, id := range sc.ContactIDs {
			phs[i] = "?"
			args = append(args, id)
		}
		parts = append(parts, "c.id IN ("+strings.Join(phs, ", ")+")")
	}
	if len(parts) == 0 {
		return " WHERE 1 = 0", nil
	}
	return " WHERE (" + strings.Join(parts, " OR ") + ")", args
}

// ---- 单点可见授权（user_contact_grants） ----

// UserContactGrantIDs 返回某用户被单点授权的联系人 id 列表。
func (s *Store) UserContactGrantIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT contact_id FROM user_contact_grants WHERE user_id = ? ORDER BY contact_id`, userID)
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

// SetUserContactGrants 整表替换某用户的单点授权联系人（幂等；重复 id 去重）。
func (s *Store) SetUserContactGrants(ctx context.Context, userID int64, contactIDs []int64, createdBy int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM user_contact_grants WHERE user_id = ?`, userID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	seen := map[int64]bool{}
	for _, id := range contactIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_contact_grants(user_id, contact_id, created_by, created_at)
			 VALUES(?, ?, ?, ?)`, userID, id, createdBy, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GrantedContact 单点授权的联系人（带姓名/单位，供管理页展示）。
type GrantedContact struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
	UnitID      int64  `json:"unitId"`
	Unit        string `json:"unit"`
}

// ListUserContactGrants 返回某用户单点授权的联系人明细。
func (s *Store) ListUserContactGrants(ctx context.Context, userID int64) ([]GrantedContact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.display_name, c.unit_id, u.name
		   FROM user_contact_grants g
		   JOIN contacts c ON c.id = g.contact_id
		   JOIN units u ON u.id = c.unit_id
		  WHERE g.user_id = ?
		  ORDER BY u.sort_order, u.id, c.display_name COLLATE NOCASE, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GrantedContact{}
	for rows.Next() {
		var g GrantedContact
		if err := rows.Scan(&g.ID, &g.DisplayName, &g.UnitID, &g.Unit); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
