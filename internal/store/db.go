package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store 封装 SQLite 访问。所有联系人写操作都通过 bumpVersion 在同一事务内
// 递增数据版本号，从而让客户端只需比较版本即可决定是否重新拉取。
type Store struct {
	db *sql.DB
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

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate 幂等执行 schema 迁移。
func (s *Store) migrate(ctx context.Context) error {
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
	defaults := map[string]string{
		"schema_version":         "9",
		"data_version":           "0",
		"data_updated_at":        time.Now().UTC().Format(time.RFC3339),
		"activation_daily_limit":    "100",       // 每日可生成的激活码上限（root 可改）
		"default_account_password":     "Pb@123456", // 单位/办公室账号统一初始密码（root 可改）
		"activation_code_retention_days": "30",       // 失效激活码保留天数（0=不自动清理）
	}
	for k, v := range defaults {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, k, v); err != nil {
			return err
		}
	}
	// 已跑过的老库 meta 里仍是旧版本号：幂等推进到 v9（defaults 只 INSERT 不会改旧值；
	// 兼容各历史版本号 4~8——旧代码默认值曾停在 4，且递增链并非全覆盖）。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE meta SET value = '9' WHERE key = 'schema_version' AND value IN ('4', '5', '6', '7', '8')`); err != nil {
		return err
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

// bumpVersion 在给定事务/执行器内递增数据版本并刷新更新时间。
func (s *Store) bumpVersion(ctx context.Context, q querier) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'data_version'`); err != nil {
		return err
	}
	return s.metaSet(ctx, q, "data_updated_at", time.Now().UTC().Format(time.RFC3339))
}

// DataVersion 返回当前数据版本与更新时间。
func (s *Store) DataVersion(ctx context.Context) (int64, string, error) {
	vs, err := s.metaGet(ctx, "data_version")
	if err != nil {
		return 0, "", err
	}
	ts, err := s.metaGet(ctx, "data_updated_at")
	if err != nil {
		return 0, "", err
	}
	var v int64
	if _, err := fmt.Sscanf(vs, "%d", &v); err != nil {
		return 0, "", fmt.Errorf("parse data_version %q: %w", vs, err)
	}
	return v, ts, nil
}

// SchemaVersion 返回数据库 schema_version（迁移后为当前结构版本，如 8）。
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
