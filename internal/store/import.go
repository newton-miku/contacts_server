package store

import (
	"context"
	"strings"
	"time"
)

// ImportContacts 以「单位+姓名」为关键字批量导入：
// 同单位同名的现存记录命中唯一 → 覆盖全部可编辑字段（含 手机/手机2/座机/传真）；
// 命中多条（同单位确有多个同名）或不存在 → 新增，避免误合并。
// 整批一个事务，全部成功或全部失败。
// unitIDs 用于限定可影响的单位：nil=全部（超管/导入端已逐行放行），否则只在该范围内查重。
// 返回 (新增数, 更新数)。有任何写入时打一次数据变动脏标记（整批只算一次改动，
// 静默窗口到期或手动发布时合并 +1，只触发一次全量拉取）。
func (s *Store) ImportContacts(ctx context.Context, unitIDs []int64, items []Contact) (created, updated int, err error) {
	if len(items) == 0 {
		return 0, 0, nil
	}
	scopeWhere, scopeArgs := contactScope(unitIDs) // 形如 " WHERE c.unit_id IN (...)"（nil=全部）
	if scopeWhere != "" {
		scopeWhere = strings.TrimPrefix(scopeWhere, " WHERE ")
	} else {
		scopeWhere = ""
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := ""
	for _, c := range items {
		// 关键字查询：单位 + 姓名（姓名已校验非空，UnitID 已映射）。
		where := ` WHERE c.unit_id = ? AND c.display_name = ?`
		args := []any{c.UnitID, c.DisplayName}
		if scopeWhere != "" {
			where += " AND " + scopeWhere
			args = append(args, scopeArgs...)
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT c.id FROM contacts c`+where+` LIMIT 2`, args...)
		if err != nil {
			return 0, 0, err
		}
		ids := make([]int64, 0, 2)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, 0, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, 0, err
		}

		if len(ids) == 1 {
			// 唯一命中：覆盖全部可编辑字段（号码变了也落到同一行），内容相同则跳过。
			var existing Contact
			row := tx.QueryRowContext(ctx, `SELECT `+contactSel+contactFrom+` WHERE c.id = ?`, ids[0])
			existing, err = scanContact(row)
			if err != nil {
				return 0, 0, err
			}
			if contactEquals(existing, c) {
				continue
			}
			if now == "" {
				now = nowUTC()
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE contacts SET display_name = ?, phone_number = ?, mobile2 = ?, landline = ?, fax = ?,
				        email = ?, job_title = ?, updated_at = ?
				  WHERE id = ?`,
				c.DisplayName, c.PhoneNumber, c.Mobile2, c.Landline, c.Fax, c.Email, c.JobTitle, now, ids[0]); err != nil {
				return 0, 0, err
			}
			updated++
			continue
		}

		// 0 条或 >=2 条：插入新增。
		if now == "" {
			now = nowUTC()
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO contacts(display_name, phone_number, mobile2, landline, fax, unit_id, email, job_title, created_at, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.DisplayName, c.PhoneNumber, c.Mobile2, c.Landline, c.Fax, c.UnitID, c.Email, c.JobTitle, now, now); err != nil {
			return 0, 0, err
		}
		created++
	}

	if created+updated > 0 {
		if err := s.markDataChanged(ctx, tx); err != nil {
			return 0, 0, err
		}
	}
	if err := s.commitWrite(tx); err != nil {
		return 0, 0, err
	}
	return created, updated, nil
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
