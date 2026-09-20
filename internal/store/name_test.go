package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 姓名归一：删除全部空白（半角/全角/制表符/不换行/零宽），避免「张 三」与「张三」被当成两个人。
func TestNormalizeNameStripsAllBlanks(t *testing.T) {
	cases := map[string]string{
		"张三":       "张三",      // 正常姓名原样返回
		"  张三  ":   "张三",      // 首尾半角空格
		"张 三":      "张三",      // 中间半角空格
		"张　三":      "张三",      // 全角空格（U+3000）
		"张\u00A0三": "张三",      // 不换行空格
		"张\u200B三": "张三",      // 零宽空格
		"张\t三\n":   "张三",      // 制表符/换行
		"阿卜杜拉 买买提": "阿卜杜拉买买提", // 少数民族姓名里的空格也去掉
		"":         "",        // 空串
	}
	for in, want := range cases {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// NormalizeContact 必须吃掉姓名里的空格；全空白姓名视为未填。
func TestNormalizeContactStripsNameSpaces(t *testing.T) {
	c, err := NormalizeContact(Contact{DisplayName: " 张 三 ", PhoneNumber: "138 0000 0000", UnitID: 1})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.DisplayName != "张三" {
		t.Fatalf("姓名应去掉全部空格：got %q", c.DisplayName)
	}
	if c.PhoneNumber != "13800000000" {
		t.Fatalf("号码归一：got %q", c.PhoneNumber)
	}

	if _, err := NormalizeContact(Contact{DisplayName: "   ", PhoneNumber: "13800000000", UnitID: 1}); err == nil {
		t.Fatal("全空白姓名应报「姓名不能为空」")
	}
}

// 升级迁移（schema v10）：历史数据里带空格的姓名一次性去空格，且只跑一次。
func TestMigrationStripsExistingNameSpaces(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migrate.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	unit, err := st.CreateUnit(ctx, "测试单位", 0, true)
	if err != nil {
		t.Fatalf("create unit: %v", err)
	}
	// 直接写库（模拟历史数据）：姓名带全角空格。
	raw, err := st.CreateContact(ctx, Contact{
		DisplayName: "李　四", PhoneNumber: "13800000001", UnitID: unit.ID,
	})
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	if raw.DisplayName != "李　四" {
		t.Fatalf("前置条件：库里应存着带空格的姓名，got %q", raw.DisplayName)
	}
	// 把结构版本退回 v9，模拟「升级前的老库」。
	if err := st.metaSet(ctx, st.db, "schema_version", "9"); err != nil {
		t.Fatalf("set schema_version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 重新打开 → 触发 v10 迁移。
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	got, err := st2.ContactByID(ctx, raw.ID)
	if err != nil {
		t.Fatalf("read contact: %v", err)
	}
	if got.DisplayName != "李四" {
		t.Fatalf("迁移应去掉历史姓名里的空格：got %q", got.DisplayName)
	}
	if v, err := st2.SchemaVersion(ctx); err != nil || v != 10 {
		t.Fatalf("迁移后结构版本应为 10：got %d (err=%v)", v, err)
	}
	// 再开一次：幂等，不报错、内容不变。
	if err := st2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st3, err := Open(path)
	if err != nil {
		t.Fatalf("reopen again: %v", err)
	}
	defer st3.Close()
	if again, err := st3.ContactByID(ctx, raw.ID); err != nil || again.DisplayName != "李四" {
		t.Fatalf("重复迁移应幂等：got %q (err=%v)", again.DisplayName, err)
	}
}
