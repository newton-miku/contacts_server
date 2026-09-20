package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 数据版本（客户端缓存同步号）的发布策略：
//
//	写操作（增删改/导入/清空）在自身事务里只打脏标记 data_dirty=1 并刷新 data_updated_at，
//	**不**递增 data_version；版本号的 +1 由发布器在「静默窗口」到期后合并执行一次：
//	  · 最后一条改动之后满 version_quiet_minutes（默认 15 分钟）没有新改动 → 发布一次；
//	  · 窗口内又有改动 → 重新计时（即「最后一条数据更新结束 15 分钟后」才发布）；
//	  · 配置为 0 = 不延迟：一有改动就发布。
//	管理员可在管理页手动「立即发布」（PublishDataVersion），让改动马上同步到手机端。
//
// 这样一段时间内连续改 N 条记录（连续编辑、批量导入）只产生 1 个版本、客户端只全量
// 拉取 1 次；而最终一致性不受影响：脏标记持久化在库里，进程重启后照常补发。

// 数据版本相关 meta 键。
const (
	metaDataVersion   = "data_version"
	metaDataDirty     = "data_dirty"
	metaDataUpdatedAt = "data_updated_at" // 客户端可见的「最后更新于」（RFC3339，秒级）
	metaDataDirtyAt   = "data_dirty_at"   // 静默窗口计时起点：最后一次改动（RFC3339Nano，纳秒精度）
	metaVersionQuiet  = "version_quiet_minutes"
)

// 静默窗口默认值与取值范围（分钟）。
const (
	// DefaultVersionQuietMinutes 默认静默窗口：最后一条改动后 15 分钟没有新改动才发布。
	DefaultVersionQuietMinutes = 15
	// MaxVersionQuietMinutes 静默窗口上限（24 小时）；0 表示不延迟、有改动即发布。
	MaxVersionQuietMinutes = 24 * 60
)

// publishTimeout 发布版本时等待写锁的上限：大批量导入会长时间持有写锁，
// 发布动作不该无限等待；超时即失败，稍后重试（脏标记仍在，不会丢改动）。
const publishTimeout = 5 * time.Second

// publishRetryDelay 发布失败后（多为写锁忙）的退避时间。
const publishRetryDelay = 2 * time.Second

// publisherMaxSleep 发布器的默认最大睡眠时间：空闲时定期醒来复查，并为唤醒丢失兜底。
const publisherMaxSleep = 30 * time.Second

// VersionState 是数据版本的当前状态。
type VersionState struct {
	Version      int64     // 当前对外版本号（data_version）
	UpdatedAt    time.Time // 最后一次数据变动时间（客户端展示用）
	UpdatedAtRaw string    // 最后一次数据变动时间原文（RFC3339，直接回给客户端）
	LastChangeAt time.Time // 最后一次改动时间（纳秒精度，静默窗口计时起点）
	Pending      bool      // 是否有改动尚未发布（data_dirty=1）
}

// PublishDue 返回按给定静默窗口计算的自动发布时间（最后一次改动 + quiet）。
// 时间戳缺失/不可解析时返回零值时间，调用方可据此立即发布（宁可早发，不可漏发）。
func (st VersionState) PublishDue(quiet time.Duration) time.Time {
	if st.LastChangeAt.IsZero() {
		return time.Time{}
	}
	return st.LastChangeAt.Add(quiet)
}

// markDataChanged 在给定事务/执行器内把目录数据标记为「已变动」，并刷新更新时间。
//
// 这里**不**递增 data_version：版本号的 +1 交给发布器按静默窗口合并执行（见文件头注释）。
// 必须在数据变更的同一事务内调用——写入回滚时脏标记一并回滚。
// 事务提交后请调用 commitWrite（或 kickVersionPublisher）唤醒发布器。
func (s *Store) markDataChanged(ctx context.Context, q querier) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES('`+metaDataDirty+`', '1')
		 ON CONFLICT(key) DO UPDATE SET value = '1'`); err != nil {
		return err
	}
	now := time.Now().UTC()
	// 两个时间戳各司其职：客户端看到的 updatedAt 保持秒级 RFC3339（对外契约不变），
	// 静默窗口用纳秒精度的 data_dirty_at（否则同秒内的多次改动会让窗口提前到期）。
	if err := s.metaSet(ctx, q, metaDataUpdatedAt, now.Format(time.RFC3339)); err != nil {
		return err
	}
	return s.metaSet(ctx, q, metaDataDirtyAt, now.Format(time.RFC3339Nano))
}

// commitWrite 提交打脏标记的写事务，并在提交成功后唤醒发布器。
// 唤醒必须发生在提交之后：否则发布器可能读到尚未提交的状态（看不到脏标记），
// 只能等下一次最大睡眠才复查——静默窗口为 0（有改动即发布）时这就是明显的延迟。
func (s *Store) commitWrite(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	s.kickVersionPublisher()
	return nil
}

// kickVersionPublisher 非阻塞地唤醒发布器（已有待处理的唤醒时直接跳过）。
func (s *Store) kickVersionPublisher() {
	if s.versionKick == nil {
		return
	}
	select {
	case s.versionKick <- struct{}{}:
	default:
	}
}

// VersionKick 返回发布器的唤醒通道（供 RunVersionPublisher select 使用）。
func (s *Store) VersionKick() <-chan struct{} { return s.versionKick }

// DataVersion 返回当前数据版本与更新时间（**纯读**，不递增版本、不清脏标记）。
// GET /api/contacts 用它给联系人快照打版本号：必须在读取联系人**之前**调用，
// 这样即使两者之间有写入提交，客户端也只会把「更新的数据」记在「更旧的版本」下
// （下次探测必然触发重拉），绝不会出现「旧数据配新版本」而永远不再更新。
func (s *Store) DataVersion(ctx context.Context) (int64, string, error) {
	vs, err := s.metaGet(ctx, metaDataVersion)
	if err != nil {
		return 0, "", err
	}
	ts, err := s.metaGet(ctx, metaDataUpdatedAt)
	if err != nil {
		return 0, "", err
	}
	var v int64
	if _, err := fmt.Sscanf(vs, "%d", &v); err != nil {
		return 0, "", fmt.Errorf("parse data_version %q: %w", vs, err)
	}
	return v, ts, nil
}

// VersionState 返回版本号、最后改动时间与「是否有待发布改动」（供 API 与发布器使用）。
func (s *Store) VersionState(ctx context.Context) (VersionState, error) {
	v, ts, err := s.DataVersion(ctx)
	if err != nil {
		return VersionState{}, err
	}
	dirty, err := s.metaGet(ctx, metaDataDirty)
	if err != nil {
		return VersionState{}, err
	}
	st := VersionState{Version: v, UpdatedAtRaw: ts, Pending: dirty == "1"}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		st.UpdatedAt = t
	}
	// 静默窗口的计时起点用纳秒精度的 data_dirty_at；老库/异常缺失时退回秒级 updatedAt。
	dirtyAt, err := s.metaGet(ctx, metaDataDirtyAt)
	if err != nil {
		return VersionState{}, err
	}
	if t, err := time.Parse(time.RFC3339, dirtyAt); err == nil {
		st.LastChangeAt = t
	} else {
		st.LastChangeAt = st.UpdatedAt
	}
	return st, nil
}

// PublishDataVersion 立即发布：若存在待发布改动，则把 data_version +1 并清除脏标记；
// 没有待发布改动时版本号保持不变（幂等）。
// 返回 (发布后的版本号, 本次是否真的递增了版本, error)。
func (s *Store) PublishDataVersion(ctx context.Context) (int64, bool, error) {
	// 同一进程内串行化，避免并发发布各自 +1 造成版本号无谓跳变。
	s.verMu.Lock()
	defer s.verMu.Unlock()

	// 极限等待时间只作用于「发布」这一步，读取仍用调用方的 ctx。
	claimCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	published, err := s.bumpVersionIfDirty(claimCtx)
	if err != nil {
		return 0, false, err
	}
	v, _, err := s.DataVersion(ctx)
	if err != nil {
		return 0, published, err
	}
	return v, published, nil
}

// bumpVersionIfDirty 若存在未发布的改动（data_dirty=1），则把 data_version +1 并清除脏标记。
// 判断与递增写在同一条 UPDATE 里（同一事务持写锁），因此并发发布只会有一个真正递增，
// 其余的看到脏标记已清零、版本号不变。
func (s *Store) bumpVersionIfDirty(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// 本条 UPDATE 是事务里的第一条语句（先拿写锁再读脏标记）：
	// 保证「读到脏 → +1 → 清脏」对外是一个原子动作，不会漏掉任何一次改动。
	res, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = CAST(value AS INTEGER) + 1
		  WHERE key = '`+metaDataVersion+`'
		    AND EXISTS (SELECT 1 FROM meta WHERE key = '`+metaDataDirty+`' AND value = '1')`)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, tx.Commit() // 没有待发布的改动：版本号保持不变
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = '0' WHERE key = '`+metaDataDirty+`'`); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// PublisherOptions 是版本发布器的可选参数。
type PublisherOptions struct {
	// Quiet 返回当前静默窗口；nil = 读系统设置 version_quiet_minutes（分钟）。
	// 测试可注入毫秒级窗口。
	Quiet func(context.Context) time.Duration
	// MaxSleep 空闲时的最长睡眠时间（<=0 用默认 30s）：定期醒来复查，防止漏唤醒。
	MaxSleep time.Duration
	// OnPublish 每次尝试发布后回调（可为 nil）；err 非空表示本次发布失败、稍后重试。
	OnPublish func(version int64, published bool, err error)
}

// RunVersionPublisher 数据版本发布器：阻塞运行到 ctx 结束，通常以 go 调用。
//
// 每轮：算出「静默窗口是否到期」→ 没到期就睡到到期时间（最长 MaxSleep）→
// 到期且有待发布改动就发布一次。窗口内又有改动时 LastChangeAt 会被刷新，
// 于是自然变成「最后一条改动之后再等满窗口」。
func (s *Store) RunVersionPublisher(ctx context.Context, opts PublisherOptions) {
	maxSleep := opts.MaxSleep
	if maxSleep <= 0 {
		maxSleep = publisherMaxSleep
	}
	quiet := opts.Quiet
	if quiet == nil {
		quiet = s.quietWindow
	}

	for {
		wait := maxSleep
		if st, err := s.VersionState(ctx); err == nil && st.Pending {
			if due := st.PublishDue(quiet(ctx)); due.IsZero() || !time.Now().Before(due) {
				wait = 0 // 窗口已到期（或时间戳缺失）→ 立即发布
			} else if d := time.Until(due); d < maxSleep {
				wait = d
			}
		}
		if ctx.Err() != nil {
			return
		}

		if wait <= 0 {
			v, published, err := s.PublishDataVersion(ctx)
			if opts.OnPublish != nil {
				opts.OnPublish(v, published, err)
			}
			if err != nil {
				// 发布失败（多为写锁忙）：稍后重试，避免忙等；脏标记仍在，改动不会丢。
				select {
				case <-ctx.Done():
					return
				case <-time.After(publishRetryDelay):
				}
			}
			continue
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.VersionKick():
			timer.Stop() // 有新写入 → 立即重算窗口
		case <-timer.C:
		}
	}
}

// quietWindow 返回系统设置里的静默窗口；读不到或非法时用默认值。
func (s *Store) quietWindow(ctx context.Context) time.Duration {
	mins, err := s.VersionQuietMinutes(ctx)
	if err != nil || mins < 0 {
		mins = DefaultVersionQuietMinutes
	}
	return time.Duration(mins) * time.Minute
}

// ---- 静默窗口设置（root）----

// VersionQuietMinutes 返回静默窗口分钟数（默认 15；0 = 有改动即发布）。
func (s *Store) VersionQuietMinutes(ctx context.Context) (int, error) {
	vs, err := s.metaGet(ctx, metaVersionQuiet)
	if err != nil {
		return 0, err
	}
	if vs == "" {
		return DefaultVersionQuietMinutes, nil
	}
	var v int
	if _, err := fmt.Sscanf(vs, "%d", &v); err != nil {
		return DefaultVersionQuietMinutes, nil
	}
	if v < 0 {
		v = DefaultVersionQuietMinutes
	}
	if v > MaxVersionQuietMinutes {
		v = MaxVersionQuietMinutes
	}
	return v, nil
}

// SetVersionQuietMinutes 设置静默窗口分钟数（0 = 有改动即发布，上限 24 小时）。
func (s *Store) SetVersionQuietMinutes(ctx context.Context, minutes int) error {
	if minutes < 0 || minutes > MaxVersionQuietMinutes {
		return fmt.Errorf("静默窗口需在 0~%d 分钟之间", MaxVersionQuietMinutes)
	}
	return s.metaSet(ctx, s.db, metaVersionQuiet, fmt.Sprintf("%d", minutes))
}
