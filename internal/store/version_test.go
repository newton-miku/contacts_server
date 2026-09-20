package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestUnit(t *testing.T, st *Store) Unit {
	t.Helper()
	u, err := st.CreateUnit(context.Background(), "测试单位", 0, true)
	if err != nil {
		t.Fatalf("create unit: %v", err)
	}
	return u
}

// addContact 新增一条联系人（写路径只打脏标记，不递增版本）。
func addContact(t *testing.T, st *Store, unitID int64, name string) Contact {
	t.Helper()
	c, err := st.CreateContact(context.Background(), Contact{
		DisplayName: name,
		PhoneNumber: "13800000000",
		UnitID:      unitID,
	})
	if err != nil {
		t.Fatalf("create contact %q: %v", name, err)
	}
	return c
}

func mustVersion(t *testing.T, st *Store) int64 {
	t.Helper()
	v, _, err := st.DataVersion(context.Background())
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	return v
}

// 一批连续改动必须合并成「一次」版本递增：写路径只打脏标记（版本号原地不动），
// 发布时才 +1。这是「改一条记录就多一个版本、短时间多条改动狂涨版本把服务器压垮」的回归测试。
func TestWritesOnlyMarkDirtyUntilPublished(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	unit := newTestUnit(t, st)
	v0 := mustVersion(t, st)

	// 模拟短时间连续变动多条记录：9 次新增 + 1 次删除（各自一个事务，最坏情况）。
	ids := make([]int64, 0, 9)
	for i := 0; i < 9; i++ {
		ids = append(ids, addContact(t, st, unit.ID, fmt.Sprintf("张三%d", i)).ID)
	}
	if err := st.DeleteContact(ctx, ids[0]); err != nil {
		t.Fatalf("delete contact: %v", err)
	}

	// 写路径不得递增版本号，否则客户端会被每条记录牵着做一次全量拉取。
	if v := mustVersion(t, st); v != v0 {
		t.Fatalf("写路径不应递增版本：got %d, want %d", v, v0)
	}
	state, err := st.VersionState(ctx)
	if err != nil {
		t.Fatalf("version state: %v", err)
	}
	if !state.Pending {
		t.Fatal("有改动时 Pending 应为 true")
	}
	if due := state.PublishDue(15 * time.Minute); !due.After(time.Now()) {
		t.Fatalf("静默窗口未到期，发布时间应在将来：%s", due)
	}

	// 发布：10 条改动合并为一次 +1。
	v1, published, err := st.PublishDataVersion(ctx)
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	if !published || v1 != v0+1 {
		t.Fatalf("10 条改动应只发布 1 次递增：published=%v got %d, want %d", published, v1, v0+1)
	}

	// 没有新改动时反复发布不得继续递增（幂等：管理员连点「立即发布」也安全）。
	for i := 0; i < 3; i++ {
		v, published, err := st.PublishDataVersion(ctx)
		if err != nil {
			t.Fatalf("publish version: %v", err)
		}
		if published || v != v1 {
			t.Fatalf("无新改动时不应发布：published=%v got %d, want %d", published, v, v1)
		}
	}

	// 新一批改动 → 再发布一次 +1。
	addContact(t, st, unit.ID, "李四")
	v2, published, err := st.PublishDataVersion(ctx)
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	if !published || v2 != v1+1 {
		t.Fatalf("新一批改动应再发布 1 次：published=%v got %d, want %d", published, v2, v1+1)
	}
}

// 内容未变的更新不算改动：不应弄脏数据、也不应产生新版本（客户端无需重拉）。
func TestUnchangedUpdateDoesNotDirty(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	unit := newTestUnit(t, st)
	c := addContact(t, st, unit.ID, "王五")

	if _, _, err := st.PublishDataVersion(ctx); err != nil {
		t.Fatalf("publish version: %v", err)
	}
	v0 := mustVersion(t, st)

	// 原样再存一次（管理页点开就保存也会走到这里）。
	if _, err := st.UpdateContact(ctx, c.ID, c); err != nil {
		t.Fatalf("update contact: %v", err)
	}
	if state, err := st.VersionState(ctx); err != nil {
		t.Fatalf("version state: %v", err)
	} else if state.Pending {
		t.Fatal("内容未变的更新不应弄脏数据")
	}
	if v, published, err := st.PublishDataVersion(ctx); err != nil {
		t.Fatalf("publish version: %v", err)
	} else if published || v != v0 {
		t.Fatalf("内容未变的更新不应产生新版本：published=%v got %d, want %d", published, v, v0)
	}
}

// 并发发布同一个待发布改动时只允许递增一次：其余看到脏标记已清零、版本号不变。
func TestPublishConcurrentClaimsOnce(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	unit := newTestUnit(t, st)
	before := mustVersion(t, st)
	addContact(t, st, unit.ID, "赵六")

	const n = 8
	type result struct {
		v         int64
		published bool
		err       error
	}
	got := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, published, err := st.PublishDataVersion(ctx)
			got[i] = result{v, published, err}
		}(i)
	}
	wg.Wait()

	bumps := 0
	for i, r := range got {
		if r.err != nil {
			t.Fatalf("第 %d 个发布失败：%v", i, r.err)
		}
		if r.v != before+1 {
			t.Fatalf("并发发布应只递增一次：第 %d 个拿到 %d，期望 %d", i, r.v, before+1)
		}
		if r.published {
			bumps++
		}
	}
	if bumps != 1 {
		t.Fatalf("并发发布只应有 1 个真正递增：got %d", bumps)
	}
}

// 静默窗口语义：窗口内又有改动要重新计时（最后一条改动满窗口后才发布）。
func TestPublisherWaitsForQuietWindow(t *testing.T) {
	const quiet = 250 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newTestStore(t)
	unit := newTestUnit(t, st)
	v0 := mustVersion(t, st)

	published := make(chan int64, 8)
	go st.RunVersionPublisher(ctx, PublisherOptions{
		Quiet:    func(context.Context) time.Duration { return quiet },
		MaxSleep: 20 * time.Millisecond,
		OnPublish: func(version int64, ok bool, err error) {
			if err == nil && ok {
				published <- version
			}
		},
	})

	addContact(t, st, unit.ID, "窗口1")
	time.Sleep(150 * time.Millisecond) // 距第一条改动 150ms（不足 quiet）
	addContact(t, st, unit.ID, "窗口2")
	time.Sleep(150 * time.Millisecond) // 距第一条 300ms（> quiet），距最后一条仅 150ms

	if v := mustVersion(t, st); v != v0 {
		t.Fatalf("窗口内又有改动时应重新计时、暂不发布：got %d, want %d", v, v0)
	}

	select {
	case v := <-published:
		if v != v0+1 {
			t.Fatalf("发布版本应为 %d，got %d", v0+1, v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("最后一条改动满静默窗口后应自动发布")
	}

	// 发布后不应重复发布。
	select {
	case v := <-published:
		t.Fatalf("不应重复发布：got %d", v)
	case <-time.After(300 * time.Millisecond):
	}
}

// 静默窗口 = 0（不延迟）时，有改动就尽快发布。
// MaxSleep 故意用生产值（30s）：发布必须靠写提交后的唤醒立刻发生，而不是等下一轮轮询。
func TestPublisherImmediateWhenQuietZero(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newTestStore(t)
	unit := newTestUnit(t, st)
	v0 := mustVersion(t, st)

	go st.RunVersionPublisher(ctx, PublisherOptions{
		Quiet:    func(context.Context) time.Duration { return 0 },
		MaxSleep: publisherMaxSleep,
	})

	addContact(t, st, unit.ID, "立即发布")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v := mustVersion(t, st); v == v0+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("静默窗口为 0 时应尽快发布：版本仍为 %d", mustVersion(t, st))
}

// 静默窗口设置：默认 15 分钟，可改、越界拒绝。
func TestVersionQuietMinutesSetting(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if got, err := st.VersionQuietMinutes(ctx); err != nil || got != DefaultVersionQuietMinutes {
		t.Fatalf("默认静默窗口应为 %d：got %d (err=%v)", DefaultVersionQuietMinutes, got, err)
	}
	if err := st.SetVersionQuietMinutes(ctx, 30); err != nil {
		t.Fatalf("设置静默窗口失败：%v", err)
	}
	if got, _ := st.VersionQuietMinutes(ctx); got != 30 {
		t.Fatalf("静默窗口应为 30，got %d", got)
	}
	if err := st.SetVersionQuietMinutes(ctx, 0); err != nil {
		t.Fatalf("窗口 0（不延迟）应允许：%v", err)
	}
	if err := st.SetVersionQuietMinutes(ctx, -1); err == nil {
		t.Fatal("负数窗口应被拒绝")
	}
	if err := st.SetVersionQuietMinutes(ctx, MaxVersionQuietMinutes+1); err == nil {
		t.Fatal("超过上限的窗口应被拒绝")
	}
}
