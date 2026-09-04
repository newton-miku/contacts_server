package store

import (
	"context"
	"sort"
	"strings"
)

// DupRef 一条「号码被使用」的记录：哪个联系人、哪个字段列。
type DupRef struct {
	ContactID int64  `json:"contactId"`
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	Field     string `json:"field"` // 手机号 / 手机2 / 座机 / 传真
	Number    string `json:"number"`
}

// DupGroup 同一串数字被多个条目使用的一组。
type DupGroup struct {
	Number string   `json:"number"` // 展示用号码（首次出现）
	Count  int      `json:"count"`
	Refs   []DupRef `json:"refs"`
}

// FindDuplicateNumbers 全量号码重复扫描：把每个联系人的 手机/手机2/座机/传真 按纯数字归类，
// 出现次数 ≥2 的号码列出使用它的所有条目。范围同 ListContacts（unitIDs==nil 全部，
// 否则仅绑定单位）——单位管理员只能看到自己范围内的重复。
// 用途：管理端「号码查重」，供人工核对这些共享同一号码的条目是否合理
// （同一办公室线路多人共用是正常的，同一个人两个字段写了同号则属于录入重复）。
func (s *Store) FindDuplicateNumbers(ctx context.Context, unitIDs []int64) ([]DupGroup, error) {
	contacts, err := s.ListContacts(ctx, unitIDs)
	if err != nil {
		return nil, err
	}
	byDigits := make(map[string]*DupGroup)
	for _, c := range contacts {
		add := func(raw, field string) {
			digits := digitsOnly(raw)
			if len(digits) < 3 {
				return
			}
			g := byDigits[digits]
			if g == nil {
				g = &DupGroup{Number: raw}
				byDigits[digits] = g
			}
			g.Refs = append(g.Refs, DupRef{
				ContactID: c.ID,
				Name:      c.DisplayName,
				Unit:      c.Unit,
				Field:     field,
				Number:    raw,
			})
		}
		add(c.PhoneNumber, "手机号")
		if c.Mobile2 != nil {
			add(*c.Mobile2, "手机2")
		}
		if c.Landline != nil {
			add(*c.Landline, "座机")
		}
		if c.Fax != nil {
			add(*c.Fax, "传真")
		}
	}

	groups := make([]DupGroup, 0, len(byDigits))
	for _, g := range byDigits {
		if len(g.Refs) < 2 {
			continue
		}
		g.Count = len(g.Refs)
		sort.SliceStable(g.Refs, func(i, j int) bool {
			if g.Refs[i].Unit != g.Refs[j].Unit {
				return g.Refs[i].Unit < g.Refs[j].Unit
			}
			return g.Refs[i].Name < g.Refs[j].Name
		})
		groups = append(groups, *g)
	}
	// 重复越多越靠前；同次数按号码排。
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return digitsOnly(groups[i].Number) < digitsOnly(groups[j].Number)
	})
	return groups, nil
}

func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if ch := s[i]; ch >= '0' && ch <= '9' {
			b.WriteByte(ch)
		}
	}
	return b.String()
}
