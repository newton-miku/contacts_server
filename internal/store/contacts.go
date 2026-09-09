package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound 表示按 id 找不到对应资源。
var ErrNotFound = errors.New("store: not found")

// ErrUnknownUnit 表示引用的单位不存在（写入联系人前校验用）。
var ErrUnknownUnit = errors.New("store: unknown unit")

// Contact 对应通讯录里一条联系人记录。可选字段用 *string 便于 JSON 输出 null。
// UnitID 是联系人所属单位（外键），Unit 为该单位名称（查询时 JOIN 得到，写时可留空）。
// 号码模型（v7）：phone_number 为主叫号（手机优先，无手机则座机；仍无则传真兜底）；
// mobile2 为第二个手机号（同一人登记了多个手机时用，可选）；
// landline 仅当「另有座机 ≠ 主叫号」时填（办公室联系人的座机就放 phone_number，landline 留空）；
// fax 填传真。
type Contact struct {
	ID          int64   `json:"id"`
	DisplayName string  `json:"displayName"`
	PhoneNumber string  `json:"phoneNumber"`
	Mobile2     *string `json:"mobile2"`
	Landline    *string `json:"landline"`
	Fax         *string `json:"fax"`
	UnitID      int64   `json:"unitId"`
	Unit        string  `json:"unit"`
	Email       *string `json:"email"`
	JobTitle    *string `json:"jobTitle"`
}

// contactSel / contactFrom 供所有联系人读取使用：一律 JOIN units 带出单位名。
const contactSel = `c.id, c.display_name, c.phone_number, c.mobile2, c.landline, c.fax, c.unit_id, u.name, c.email, c.job_title`
const contactFrom = ` FROM contacts c JOIN units u ON u.id = c.unit_id`

// contactScope 构造按单位集合过滤的 SQL。
// unitIDs == nil 表示不限（超管/匿名客户端全部可见）；空切片表示「查不到任何单位」。
func contactScope(unitIDs []int64) (string, []any) {
	if unitIDs == nil {
		return "", nil
	}
	if len(unitIDs) == 0 {
		return " WHERE 1 = 0", nil
	}
	phs := make([]string, len(unitIDs))
	args := make([]any, len(unitIDs))
	for i, id := range unitIDs {
		phs[i] = "?"
		args[i] = id
	}
	return " WHERE c.unit_id IN (" + strings.Join(phs, ", ") + ")", args
}

// CreateContact 插入一条联系人，并在同一事务内递增数据版本（脏标记，客户端据此重新拉全量）。
func (s *Store) CreateContact(ctx context.Context, c Contact) (Contact, error) {
	if ok, err := s.UnitExists(ctx, c.UnitID); err != nil {
		return Contact{}, err
	} else if !ok {
		return Contact{}, ErrUnknownUnit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Contact{}, err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO contacts(display_name, phone_number, mobile2, landline, fax, unit_id, email, job_title, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.DisplayName, c.PhoneNumber, c.Mobile2, c.Landline, c.Fax, c.UnitID, c.Email, c.JobTitle, now, now)
	if err != nil {
		return Contact{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Contact{}, err
	}
	c.ID = id
	if err := s.bumpVersion(ctx, tx); err != nil {
		return Contact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Contact{}, err
	}
	return s.contactByID(ctx, id)
}

// UpdateContact 整体更新某条联系人；不存在则返回 ErrNotFound。
// 若内容与现值完全相同则不写入、也不递增版本，避免无谓的客户端刷新。
func (s *Store) UpdateContact(ctx context.Context, id int64, c Contact) (Contact, error) {
	if ok, err := s.UnitExists(ctx, c.UnitID); err != nil {
		return Contact{}, err
	} else if !ok {
		return Contact{}, ErrUnknownUnit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Contact{}, err
	}
	defer tx.Rollback()

	var existing Contact
	{
		row := tx.QueryRowContext(ctx, `SELECT `+contactSel+contactFrom+` WHERE c.id = ?`, id)
		existing, err = scanContact(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Contact{}, ErrNotFound
			}
			return Contact{}, err
		}
	}

	if contactEquals(existing, c) {
		return existing, tx.Commit() // 内容未变，直接返回
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE contacts SET display_name = ?, phone_number = ?, mobile2 = ?, landline = ?, fax = ?, unit_id = ?,
		        email = ?, job_title = ?, updated_at = ?
		  WHERE id = ?`,
		c.DisplayName, c.PhoneNumber, c.Mobile2, c.Landline, c.Fax, c.UnitID, c.Email, c.JobTitle,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return Contact{}, err
	}
	c.ID = id
	if err := s.bumpVersion(ctx, tx); err != nil {
		return Contact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Contact{}, err
	}
	return s.contactByID(ctx, id)
}

// contactEquals 比较除 id/时间戳外的可编辑字段是否一致。
func contactEquals(a, b Contact) bool {
	return a.DisplayName == b.DisplayName &&
		a.PhoneNumber == b.PhoneNumber &&
		strPtrEq(a.Mobile2, b.Mobile2) &&
		strPtrEq(a.Landline, b.Landline) &&
		strPtrEq(a.Fax, b.Fax) &&
		a.UnitID == b.UnitID &&
		strPtrEq(a.Email, b.Email) &&
		strPtrEq(a.JobTitle, b.JobTitle)
}

func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// DeleteContact 删除联系人并在同一事务内递增版本。不存在返回 ErrNotFound。
func (s *Store) DeleteContact(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM contacts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := s.bumpVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ListContacts 返回联系人（按单位排序，再按姓名排序，稳定）。
// unitIDs == nil 返回全部；否则只返回这些单位的联系人。
func (s *Store) ListContacts(ctx context.Context, unitIDs []int64) ([]Contact, error) {
	where, args := contactScope(unitIDs)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSel+contactFrom+where+
			` ORDER BY u.sort_order, u.id, c.display_name COLLATE NOCASE, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := []Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// ListContactsScoped 按可见范围返回联系人（单位命中 或 单点授权命中）。
// 这是所有对外读取（手机端/网页）的统一入口；范围为空则返回空集。
func (s *Store) ListContactsScoped(ctx context.Context, sc ContactScope) ([]Contact, error) {
	where, args := contactScopeOf(sc)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSel+contactFrom+where+
			` ORDER BY u.sort_order, u.id, c.display_name COLLATE NOCASE, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := []Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// ListContactsPaged 分页查询联系人（管理页用）：返回当前页数据与总条数。
// keyword 非空时对 姓名/手机/手机2/座机/传真/职务/邮箱/单位名 做模糊匹配；
// unitID>0 时只查该单位。scope 为调用者的可见范围。
func (s *Store) ListContactsPaged(ctx context.Context, sc ContactScope, keyword string, unitID int64, limit, offset int) ([]Contact, int, error) {
	where, args := contactScopeOf(sc)
	if where == "" {
		where = " WHERE 1 = 1"
	}
	if unitID > 0 {
		where += " AND c.unit_id = ?"
		args = append(args, unitID)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + strings.ToLower(kw) + "%"
		where += ` AND (lower(c.display_name) LIKE ? OR c.phone_number LIKE ?
			OR COALESCE(c.mobile2, '') LIKE ? OR COALESCE(c.landline, '') LIKE ?
			OR COALESCE(c.fax, '') LIKE ? OR lower(COALESCE(c.job_title, '')) LIKE ?
			OR lower(COALESCE(c.email, '')) LIKE ? OR lower(u.name) LIKE ?)`
		for i := 0; i < 8; i++ {
			args = append(args, like)
		}
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+contactFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSel+contactFrom+where+
			` ORDER BY u.sort_order, u.id, c.display_name COLLATE NOCASE, c.id LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	contacts := []Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, 0, err
		}
		contacts = append(contacts, c)
	}
	return contacts, total, rows.Err()
}

// CountContacts 返回联系人总数（unitIDs==nil 时统计全部）。
func (s *Store) CountContacts(ctx context.Context, unitIDs []int64) (int, error) {
	where, args := contactScope(unitIDs)
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)`+contactFrom+where, args...).Scan(&n)
	return n, err
}

// CountContactsByUnit 返回每个单位的人数（用于概览统计），仅统计 unitIDs 范围内的单位。
func (s *Store) CountContactsByUnit(ctx context.Context, unitIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int)
	if unitIDs != nil && len(unitIDs) == 0 {
		return out, nil
	}
	where, args := contactScope(unitIDs)
	// 直接以 units 为主表，保证「有单位没人」时也返回 0 行数据项。
	q := `SELECT u.id, COUNT(c.id)` + contactFrom + where +
		` GROUP BY u.id ORDER BY u.sort_order, u.id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Store) contactByID(ctx context.Context, id int64) (Contact, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+contactSel+contactFrom+` WHERE c.id = ?`, id)
	c, err := scanContact(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Contact{}, ErrNotFound
		}
		return Contact{}, err
	}
	return c, nil
}

// ContactByID 返回指定联系人（含单位名）；不存在返回 ErrNotFound。
func (s *Store) ContactByID(ctx context.Context, id int64) (Contact, error) {
	return s.contactByID(ctx, id)
}

// Scanner 兼容 *sql.Row 与 *sql.Rows 的 Scan。
type Scanner interface{ Scan(dest ...any) error }

func scanContact(row Scanner) (Contact, error) {
	var c Contact
	var mobile2, landline, fax, email, jobTitle sql.NullString
	err := row.Scan(&c.ID, &c.DisplayName, &c.PhoneNumber, &mobile2, &landline, &fax,
		&c.UnitID, &c.Unit, &email, &jobTitle)
	if err != nil {
		return Contact{}, err
	}
	c.Mobile2 = nullToPtr(mobile2)
	c.Landline = nullToPtr(landline)
	c.Fax = nullToPtr(fax)
	c.Email = nullToPtr(email)
	c.JobTitle = nullToPtr(jobTitle)
	return c, nil
}

func nullToPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

// NormalizeContact 清理输入：号码归一（去分隔符 + 短固话补 0915 区号），主叫号兜底，
// 去掉空字符串可选字段并 trim。返回规范化后的 Contact 与校验错误。
// 单位存在性校验在 Store 写路径（Create/Update/Import）内完成，保证不在存储层漏判。
func NormalizeContact(c Contact) (Contact, error) {
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	c.PhoneNumber = normalizePhone(c.PhoneNumber)
	c.Mobile2 = normalizePhonePtr(c.Mobile2)
	c.Landline = normalizePhonePtr(c.Landline)
	c.Fax = normalizePhonePtr(c.Fax)

	// 主叫号兜底：手机/座机都没有时，依次用 手机2、座机、传真 兜底
	// （手机2 也是手机，优先于座机；办公室传真兜底仅限「只有传真的办公室」罕见情况）。
	if c.PhoneNumber == "" && c.Mobile2 != nil {
		c.PhoneNumber = *c.Mobile2
		c.Mobile2 = nil
	}
	if c.PhoneNumber == "" && c.Landline != nil {
		c.PhoneNumber = *c.Landline
		c.Landline = nil
	}
	if c.PhoneNumber == "" && c.Fax != nil {
		c.PhoneNumber = *c.Fax
		c.Fax = nil
	}
	if c.DisplayName == "" {
		return c, fmt.Errorf("姓名不能为空")
	}
	if c.PhoneNumber == "" {
		return c, fmt.Errorf("手机号/座机/传真至少填一个")
	}
	if n := len(c.PhoneNumber); n < 3 || n > 20 {
		return c, fmt.Errorf("号码格式不正确（应为 3~20 位）：%s", c.PhoneNumber)
	}
	// 冗余清空：手机2/座机 与主叫号或彼此相同则清空，保持「每列号码互不相同」约束。
	if c.Mobile2 != nil && (*c.Mobile2 == c.PhoneNumber || (c.Landline != nil && *c.Mobile2 == *c.Landline)) {
		c.Mobile2 = nil
	}
	if c.Mobile2 != nil {
		if n := len(*c.Mobile2); n < 3 || n > 20 {
			return c, fmt.Errorf("号码格式不正确（应为 3~20 位）：%s", *c.Mobile2)
		}
	}
	// landline 与主叫号相同的冗余清空（保持「landline 仅在不等于主叫号时填充」约束）。
	if c.Landline != nil && *c.Landline == c.PhoneNumber {
		c.Landline = nil
	}
	if c.UnitID <= 0 {
		return c, fmt.Errorf("单位不能为空")
	}
	c.Email = normStrPtr(c.Email)
	c.JobTitle = normStrPtr(c.JobTitle)
	return c, nil
}

func normStrPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

// normalizePhone 号码归一的单点权威：只保留 0-9，去掉空格/横杠/括号/加号/换行等一切分隔；
// 纯 6~8 位且不以 0/1 开头的数字视为本县（安康 0915）短固话，自动补 0915 区号。
// 11 位以 1 开头的手机号、已带区号（0 开头）的座机均原样保留。
func normalizePhone(raw string) string {
	digits := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch >= '0' && ch <= '9' {
			digits = append(digits, ch)
		}
	}
	s := string(digits)
	if n := len(s); n >= 6 && n <= 8 && s[0] != '0' && s[0] != '1' {
		s = "0915" + s
	}
	return s
}

func normalizePhonePtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := normalizePhone(*p)
	if v == "" {
		return nil
	}
	return &v
}

// validPhone 粗校验：允许数字、+、-、空格、括号，长度 3~20（用于兼容非纯数字的宽容输入）。
func validPhone(p string) bool {
	digits := 0
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == '-' || r == ' ' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return digits >= 3 && digits <= 20
}

// ResetDirectoryData 清空目录数据：删除全部联系人，再删除未被任何单位管理员绑定（user_units）
// 的单位；被绑定的单位保留，避免破坏单位管理员的账号数据范围。
// 不删 users/sessions/logs/meta。整个过程一个事务，且递增 data_version 让客户端重新拉全量。
// 返回 (删除联系人, 删除单位, 保留单位)。调用方必须确保当前是 super 的一次性运维动作。
func (s *Store) ResetDirectoryData(ctx context.Context) (contactsDeleted, unitsDeleted, unitsKept int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM contacts`)
	if err != nil {
		return 0, 0, 0, err
	}
	cd, _ := res.RowsAffected()

	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM units WHERE id IN (SELECT unit_id FROM user_units)`).Scan(&unitsKept); err != nil {
		return 0, 0, 0, err
	}

	res, err = tx.ExecContext(ctx,
		`DELETE FROM units WHERE id NOT IN (SELECT unit_id FROM user_units)`)
	if err != nil {
		return 0, 0, 0, err
	}
	ud, _ := res.RowsAffected()

	if cd > 0 || ud > 0 {
		if err := s.bumpVersion(ctx, tx); err != nil {
			return 0, 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return int(cd), int(ud), unitsKept, nil
}
