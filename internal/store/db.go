package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store 封装 SQLite 访问。
//
// 目录数据的版本号采用「脏标记 + 静默窗口合并发布」：写操作只在自身事务内打脏标记
// （markDataChanged，data_dirty=1），**不**递增 data_version；版本号的 +1 由发布器
// （RunVersionPublisher）在「最后一条改动之后满静默窗口没有新改动」时合并执行一次，
// 管理员也可手动立即发布（PublishDataVersion）。于是一段时间内 N 条记录的连续变动
// 只产生 1 次版本递增、客户端只全量拉取 1 次，避免「改一条记录就多一个版本」把服务器压垮。
// 最终一致性不变：脏标记持久化在库里，重启后发布器照常补发。
// 详见 version.go。
type Store struct {
	db   *sql.DB
	path string // 数据库文件路径（用于统计文件大小）

	// verMu 串行化「查脏标记 → 版本号 +1 → 清脏标记」的发布动作，
	// 保证同一进程内并发发布只有一个真正递增版本。
	verMu sync.Mutex
	// versionKick 有新写入时唤醒发布器（容量 1，非阻塞发送）；只是「尽快重算」的优化，
	// 发布器自身有最大睡眠兜底，丢一次唤醒不影响正确性。
	versionKick chan struct{}
}

// Open 打开（必要时创建）SQLite 数据库并执行迁移。
func Open(path string) (*Store, error) {
	// modernc.org/sqlite 通过 URL query 传 pragma；WAL + busy_timeout 提高并发读与锁友好度。
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// 单连接写路径足够；允许多读。
	db.SetMaxOpenConns(8)

	s := &Store{db: db, path: path, versionKick: make(chan struct{}, 1)}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate 幂等执行 schema 迁移。
func (s *Store) migrate(ctx context.Context) error {
	// schema_version 10：
	//  - 姓名不允许含空格：历史联系人姓名一次性去掉全部空白（normalizeName），
	//    与新增/修改/导入时的姓名归一保持一致——否则「张 三」与「张三」在导入时
	//    会被当成两个人，建出重复记录。
	// schema_version 9：
	//  - 设备激活（devices/device_tokens/activation_codes）+ 单点可见授权（user_contact_grants）；
	//    users 新增 can_manage_visibility；新增 root 角色（系统最高管理员，初始 admin 账号升为 root）。
	// schema_version 8：
	//  - 单位新增「公开单位」标记 is_public：公开单位的联系人对任何登录用户可见
	//    （手机端 enduser/unit 的“全局可见”，见 internal/store/units.go PublicUnitIDs）。
	// schema_version 7：
	//  - 联系人新增「手机2」可选列（mobile2）：同一人可登记第二个手机号
	//    （源表存在“手机 / 手机”同格多号时也从此不再丢失）。
	//  - schema_version 6：联系人支持「座机/传真」可选列（landline/fax）；
	//    单位块的办公室作为一条 display_name="办公室" 的联系人，座机存主叫号、传真存 fax。
	//  - v5：联系人改为挂在「单位」下（unit_id 外键），去掉 company/department 两列；
	//    users.role 语义改为 super / unit；新增 logs 审计日志表。
	statements := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS units (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL UNIQUE,
			sort_order INTEGER NOT NULL DEFAULT 0,
			is_public  INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS contacts (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			display_name TEXT NOT NULL,
			phone_number TEXT NOT NULL,
			unit_id      INTEGER NOT NULL REFERENCES units(id) ON DELETE RESTRICT,
			email        TEXT,
			job_title    TEXT,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			username            TEXT NOT NULL UNIQUE,
			password_hash       TEXT NOT NULL,
			display_name        TEXT,
			role                TEXT NOT NULL DEFAULT 'unit', -- 'super' 超级管理员 | 'unit' 单位管理员 | 'enduser' 只读手机用户
			is_active           INTEGER NOT NULL DEFAULT 1,
			must_change_password INTEGER NOT NULL DEFAULT 0,
			created_at          TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_units (
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
			PRIMARY KEY (user_id, unit_id)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS logs (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL,
			user_id    INTEGER,
			username   TEXT,
			category   TEXT NOT NULL, -- contact / auth / account / password / unit
			action     TEXT NOT NULL, -- create/update/delete/login/logout/reset/...
			detail     TEXT NOT NULL DEFAULT '',
			ip         TEXT NOT NULL DEFAULT ''
		)`,
		// v9：设备激活 + 隐藏组可见性
		`CREATE TABLE IF NOT EXISTS devices (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			phone         TEXT NOT NULL,
			label         TEXT,
			status        TEXT NOT NULL DEFAULT 'active', -- active | revoked
			created_at    TEXT NOT NULL,
			last_seen_at  TEXT,
			last_ip       TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS device_tokens (
			token_hash TEXT PRIMARY KEY,
			device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS activation_codes (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			code             TEXT NOT NULL UNIQUE,      -- 10 位大写字母+数字（输入不区分大小写）
			phone            TEXT NOT NULL,             -- 激活时手机号必须与之一致
			display_name     TEXT,
			role             TEXT NOT NULL DEFAULT 'enduser',
			unit_ids_json    TEXT NOT NULL DEFAULT '[]', -- 激活后绑定/可见的单位（含隐藏组）
			contact_ids_json TEXT NOT NULL DEFAULT '[]', -- 单点可见的联系人
			created_by       INTEGER,
			created_at       TEXT NOT NULL,
			expires_at       TEXT NOT NULL,
			used_at          TEXT,
			used_device_id   INTEGER,
			revoked_at       TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS user_contact_grants (
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
			created_by INTEGER,
			created_at TEXT NOT NULL,
			PRIMARY KEY (user_id, contact_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_device_tokens_device ON device_tokens(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_activation_codes_created ON activation_codes(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_activation_codes_phone ON activation_codes(phone)`,
		`CREATE INDEX IF NOT EXISTS idx_user_contact_grants_user ON user_contact_grants(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_created ON logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_category ON logs(category)`,
		`CREATE INDEX IF NOT EXISTS idx_user_units_unit ON user_units(unit_id)`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	// 记录迁移前的结构版本：用于只在升级时跑一次的数据迁移（见文末 v10 姓名归一）。
	prevVersion := 0
	if vs, err := s.metaGet(ctx, "schema_version"); err == nil && vs != "" {
		_, _ = fmt.Sscanf(vs, "%d", &prevVersion)
	}

	// 老库迁移：contacts 若还是旧结构（没有 unit_id），直接重建——旧数据无保留价值，
	// 但为兼容“早期部署”仍把账号、会话原样保留。
	hasUnitID, err := s.columnExists(ctx, "contacts", "unit_id")
	if err != nil {
		return err
	}
	if !hasUnitID {
		if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS contacts`); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `CREATE TABLE contacts (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			display_name TEXT NOT NULL,
			phone_number TEXT NOT NULL,
			unit_id      INTEGER NOT NULL REFERENCES units(id) ON DELETE RESTRICT,
			email        TEXT,
			job_title    TEXT,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx,
			`CREATE INDEX IF NOT EXISTS idx_contacts_unit ON contacts(unit_id)`); err != nil {
			return err
		}
	}

	// 无论全新库还是升级库，保证 contacts.unit_id 索引存在。
	if _, err := s.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_contacts_unit ON contacts(unit_id)`); err != nil {
		return err
	}

	// 老账号（role=admin）语义升级为超级管理员。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET role = 'super' WHERE role = 'admin'`); err != nil {
		return err
	}
	// 老库补列：must_change_password。
	if err := s.addColumnIfMissing(ctx, "users", "must_change_password",
		"INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}

	// v9：可见性配置权限标志（预留；本期 super/root 直接允许，暂不开放勾选）。
	if err := s.addColumnIfMissing(ctx, "users", "can_manage_visibility",
		"INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	// 老库补列：contacts.landline（座机）/ contacts.fax（传真）v6、contacts.mobile2（手机2）v7，
	// 均可选。
	if err := s.addColumnIfMissing(ctx, "contacts", "landline", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "contacts", "fax", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "contacts", "mobile2", "TEXT"); err != nil {
		return err
	}

	// v8：单位「公开单位」标记（is_public=1 → 任何登录用户都可见其联系人，手机 enduser 的“全局可见”）。
	if err := s.addColumnIfMissing(ctx, "units", "is_public", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	// 初始化 meta 键（若缺失）。
	// data_dirty 是「数据已变动但版本号尚未发布」的脏标记：写操作置 1，
	// 静默窗口到期（或管理员手动发布）时合并成 data_version +1 并清零（见 version.go）。
	defaults := map[string]string{
		"schema_version":                 "10",
		"data_version":                   "0",
		"data_dirty":                     "0",
		"version_quiet_minutes":          "15",
		"data_updated_at":                time.Now().UTC().Format(time.RFC3339),
		"activation_daily_limit":         "100",       // 每日可生成的激活码上限（root 可改）
		"default_account_password":       "Pb@123456", // 单位/办公室账号统一初始密码（root 可改）
		"activation_code_retention_days": "30",        // 失效激活码保留天数（0=不自动清理）
		"log_retention_days":             "90",        // 审计日志保留天数（0=不自动清理）
		"maintenance_time":               "03:30",     // 每日维护（清理+整理）时刻，本地时间 HH:MM
		"auto_vacuum":                    "1",         // 清理后自动 VACUUM（1/0）
	}
	for k, v := range defaults {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, k, v); err != nil {
			return err
		}
	}
	// 已跑过的老库 meta 里仍是旧版本号：幂等推进到 v10（defaults 只 INSERT 不会改旧值；
	// 兼容各历史版本号 4~9——旧代码默认值曾停在 4，且递增链并非全覆盖）。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE meta SET value = '10' WHERE key = 'schema_version' AND value IN ('4', '5', '6', '7', '8', '9')`); err != nil {
		return err
	}

	// v10：姓名不允许含空格——升级时把历史联系人姓名里的空白一次性去掉（幂等，只跑一次）。
	// 与新增/修改/导入时的姓名归一（normalizeName）保持一致，避免同一人被建成多条记录。
	if prevVersion < 10 {
		if _, err := s.normalizeContactNames(ctx); err != nil {
			return err
		}
	}

	// v9：确保存在 root（系统最高管理员）——没有 root 时把最早创建的启用 super 提升为 root。
	var rootCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'root'`).Scan(&rootCount); err != nil {
		return err
	}
	if rootCount == 0 {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE users SET role = 'root'
			  WHERE id = (SELECT id FROM users WHERE role = 'super' AND is_active = 1 ORDER BY id LIMIT 1)`); err != nil {
			return err
		}
	}
	return nil
}

// ---- meta 帮助 ----

// columnExists 判断表是否存在指定列。
func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// normalizeContactNames 一次性规整历史联系人姓名：去掉姓名中的全部空白（见 normalizeName）。
// 幂等：姓名本来就没有空白的行不写库；返回被改动的行数。
// 说明：若去掉空格后同一单位内出现同名记录，这里不做合并（不猜测哪条为准），
// 由管理页「号码查重」去处理；关键是此后导入不会再因为空格而误建重复记录。
func (s *Store) normalizeContactNames(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, display_name FROM contacts`)
	if err != nil {
		return 0, err
	}
	type fix struct {
		id   int64
		name string
	}
	var fixes []fix
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return 0, err
		}
		if cleaned := normalizeName(name); cleaned != name {
			fixes = append(fixes, fix{id: id, name: cleaned})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(fixes) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, f := range fixes {
		if _, err := tx.ExecContext(ctx,
			`UPDATE contacts SET display_name = ? WHERE id = ?`, f.name, f.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(fixes), nil
}

// addColumnIfMissing 轻量列迁移：表缺该列时用 ALTER TABLE 补上（幂等）。
func (s *Store) addColumnIfMissing(ctx context.Context, table, column, def string) error {
	ok, err := s.columnExists(ctx, table, column)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+def)
	return err
}

func (s *Store) metaGet(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) metaSet(ctx context.Context, q querier, key, value string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SchemaVersion 返回数据库 schema_version（迁移后为当前结构版本，如 10）。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	vs, err := s.metaGet(ctx, "schema_version")
	if err != nil {
		return 0, err
	}
	var v int
	if _, err := fmt.Sscanf(vs, "%d", &v); err != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", vs, err)
	}
	return v, nil
}
