package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"phonebook-server/internal/auth"
	"phonebook-server/internal/store"
)

// mobileRe 中国大陆手机号（用于区分「手机号」与「座机号」）。
var mobileRe = regexp.MustCompile(`^1[3-9]\d{9}$`)

// contactPhones 从联系人记录里取「手机号」与「座机号」：
//   - 手机号：phone_number 是手机 → 用它；否则 mobile2 是手机 → 用它；否则无。
//   - 座机号：landline 优先；否则 phone_number 非手机时用它。
func contactPhones(c store.Contact) (mobile, landline string) {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return normalizePhone(*p)
	}
	p := normalizePhone(c.PhoneNumber)
	m2 := deref(c.Mobile2)
	ll := deref(c.Landline)
	switch {
	case mobileRe.MatchString(p):
		mobile = p
	case mobileRe.MatchString(m2):
		mobile = m2
	}
	if ll != "" {
		landline = ll
	} else if p != "" && !mobileRe.MatchString(p) {
		landline = p
	}
	return
}

// handleContactLinkedUser 查询联系人（手机号/座机号）是否已关联账号，供编辑/删除时的联动提示。
func (s *Server) handleContactLinkedUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的联系人 id")
		return
	}
	c, err := s.store.ContactByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "联系人不存在")
		return
	}
	allowed, err := s.staffUnitIDs(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取权限失败")
		return
	}
	if allowed != nil {
		ok := false
		for _, uid := range allowed {
			if uid == c.UnitID {
				ok = true
				break
			}
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "联系人不存在")
			return
		}
	}

	mobile, landline := contactPhones(c)
	for _, cand := range []string{mobile, landline} {
		if cand == "" {
			continue
		}
		u, err := s.store.UserByUsername(r.Context(), cand)
		if err != nil {
			continue
		}
		dev, _ := s.store.DeviceByUser(r.Context(), u.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"linked":       true,
			"userId":       u.ID,
			"username":     u.Username,
			"displayName":  u.DisplayName,
			"role":         u.Role,
			"isActive":     u.IsActive,
			"hasPassword":  u.HasPassword,
			"deviceActive": dev.ID > 0,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": false})
}

// handleDeleteAdminUser 删除账号（super/root）：级联删除其设备、令牌、会话、单位绑定与单点授权。
func (s *Server) handleDeleteAdminUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的用户 id")
		return
	}
	me := userFrom(r)
	if id == me.ID {
		writeErr(w, http.StatusBadRequest, "不能删除自己的账号")
		return
	}
	target, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if target.Role == store.RoleRoot && me.Role != store.RoleRoot {
		writeErr(w, http.StatusForbidden, "只有系统最高管理员（root）可删除 root 账号")
		return
	}
	// 保护最后一个启用的 root / 全局管理员。
	if target.Role == store.RoleRoot {
		ok, err := s.otherActiveRootExists(r.Context(), target.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验账号失败")
			return
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "系统至少需保留一个启用的 root 管理员")
			return
		}
	}
	if store.IsSuperRole(target.Role) {
		ok, err := s.otherActiveSuperExists(r.Context(), target.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验账号失败")
			return
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "系统至少需保留一个启用的超级管理员")
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除账号失败")
		return
	}
	s.logAudit(r, store.LogCategoryAccount, "delete_user",
		"删除账号 "+target.Username+"（角色 "+roleLabel(target.Role)+"）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleImportCodesFromContacts 从通讯录导入：手机号联系人 → 一次性激活码；
// 只有座机号的联系人 → 账号密码登录账号（初始密码由调用方指定或随机生成并返回一次）。
func (s *Server) handleImportCodesFromContacts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ContactIDs      []int64 `json:"contactIds"`
		UnitIDs         []int64 `json:"unitIds"`         // 按单位整组导入
		GrantOwnUnit    *bool   `json:"grantOwnUnit"`    // 自动把联系人所在单位加入可见范围（默认 true）
		ExpiresInDays   int     `json:"expiresInDays"`   // 激活码有效期，默认 7 天
		InitialPassword string  `json:"initialPassword"` // 座机账号初始密码；留空则随机生成
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	grantOwn := true
	if in.GrantOwnUnit != nil {
		grantOwn = *in.GrantOwnUnit
	}

	me := userFrom(r)
	allowed, err := s.staffUnitIDs(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取权限失败")
		return
	}
	allowedSet := map[int64]bool{}
	for _, uid := range allowed {
		allowedSet[uid] = true
	}
	inScope := func(unitID int64) bool { return allowed == nil || allowedSet[unitID] }

	skipped := []map[string]any{}
	addSkip := func(c store.Contact, phone, reason string) {
		skipped = append(skipped, map[string]any{
			"contactId": c.ID, "name": c.DisplayName, "phone": phone, "reason": reason,
		})
	}

	// 1) 收集联系人（去重；unit 管理员只能取自己单位的）
	seen := map[int64]bool{}
	contacts := []store.Contact{}
	for _, id := range in.ContactIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		c, err := s.store.ContactByID(r.Context(), id)
		if err != nil {
			skipped = append(skipped, map[string]any{"contactId": id, "reason": "联系人不存在"})
			continue
		}
		if !inScope(c.UnitID) {
			addSkip(c, "", "无权访问该联系人所在单位")
			continue
		}
		contacts = append(contacts, c)
	}
	if len(in.UnitIDs) > 0 {
		for _, uid := range in.UnitIDs {
			if !inScope(uid) {
				writeErr(w, http.StatusForbidden, "无权访问所选单位")
				return
			}
		}
		list, err := s.store.ListContacts(r.Context(), in.UnitIDs)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取联系人失败")
			return
		}
		for _, c := range list {
			if !seen[c.ID] {
				seen[c.ID] = true
				contacts = append(contacts, c)
			}
		}
	}
	if len(contacts) == 0 {
		// 全部被跳过（越权/不存在）时，把原因一并返回，便于前端展示。
		if len(skipped) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"codes": []any{}, "accounts": []any{}, "skipped": skipped,
				"message": "没有可导入的联系人",
			})
			return
		}
		writeErr(w, http.StatusBadRequest, "没有可导入的联系人")
		return
	}

	days := in.ExpiresInDays
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	expiresAt := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)

	pendingPhones, err := s.store.PendingActivationPhones(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取激活码失败")
		return
	}

	// 2) 分类：手机号 → 激活码；只有座机 → 账号密码账号
	type landlineAccount struct {
		Username string
		Name     string
		UnitIDs  []int64
		Password string
	}
	usedPhone := map[string]bool{}
	codeItems := []store.NewActivationCode{}
	accounts := []landlineAccount{}
	for _, c := range contacts {
		mobile, landline := contactPhones(c)
		units := []int64{}
		if grantOwn {
			units = []int64{c.UnitID}
		}
		switch {
		case mobile != "":
			if usedPhone[mobile] {
				addSkip(c, mobile, "号码重复（同批其他联系人已使用）")
				continue
			}
			if pendingPhones[mobile] {
				addSkip(c, mobile, "该号码已有未使用的激活码")
				continue
			}
			usedPhone[mobile] = true
			codeItems = append(codeItems, store.NewActivationCode{
				Phone:       mobile,
				DisplayName: c.DisplayName,
				Role:        store.RoleEndUser,
				UnitIDs:     units,
				ExpiresAt:   expiresAt,
				CreatedBy:   me.ID,
			})
		case landline != "":
			// 座机 = 桌面端（账号密码）使用者。这里不再按姓名关键词（原「办公室/值班室」）
			// 预筛：那会连带挡掉「搬迁后扶股」「喜河财政所」这类同样属于单位办公室的命名，
			// 且姓名常是个人（如「李康（财政所长）」）而非机构名。是否建号由管理员在导入时选谁决定。
			if usedPhone[landline] {
				addSkip(c, landline, "号码重复（同批其他联系人已使用）")
				continue
			}
			if _, err := s.store.UserByUsername(r.Context(), landline); err == nil {
				addSkip(c, landline, "该号码已存在账号")
				continue
			}
			usedPhone[landline] = true
			accounts = append(accounts, landlineAccount{
				Username: landline, Name: c.DisplayName, UnitIDs: units,
			})
		default:
			addSkip(c, "", "既无手机号也无座机号")
		}
	}

	need := len(codeItems) + len(accounts)
	if need == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"codes": []any{}, "accounts": []any{}, "skipped": skipped,
			"message": "没有可导入的对象",
		})
		return
	}

	// 3) 每日上限（激活码 + 座机账号合计占用额度）
	limit, err := s.store.ActivationDailyLimit(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取限额失败")
		return
	}
	created, err := s.store.ActivationQuotaToday(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "统计今日数量失败")
		return
	}
	if created+need > limit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("本次需 %d 个额度，今日已用 %d/%d；请先由 root 调高每日上限，或分批导入", need, created, limit))
		return
	}

	initPw := strings.TrimSpace(in.InitialPassword)
	if initPw != "" && len(initPw) < 6 {
		writeErr(w, http.StatusBadRequest, "初始密码至少 6 位")
		return
	}
	if initPw == "" {
		// 未指定 → 用系统统一默认密码（root 可在「系统设置」修改）
		initPw, err = s.store.DefaultAccountPassword(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取默认密码失败")
			return
		}
	}

	// 4) 座机账号（密码登录；首次登录强制改密）
	createdAccounts := []map[string]any{}
	for i := range accounts {
		pw := initPw
		hash, err := auth.HashPassword(pw)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "无法加密密码")
			return
		}
		u, err := s.store.CreateUser(r.Context(), accounts[i].Username, hash,
			accounts[i].Name, store.RoleEndUser, true)
		if err != nil {
			skipped = append(skipped, map[string]any{
				"name": accounts[i].Name, "phone": accounts[i].Username, "reason": "创建账号失败",
			})
			continue
		}
		if err := s.store.SetUserUnits(r.Context(), u.ID, accounts[i].UnitIDs); err != nil {
			writeErr(w, http.StatusInternalServerError, "绑定单位失败")
			return
		}
		createdAccounts = append(createdAccounts, map[string]any{
			"userId": u.ID, "username": accounts[i].Username,
			"name": accounts[i].Name, "password": pw, "unitIds": accounts[i].UnitIDs,
		})
	}

	// 5) 激活码
	codes := []store.ActivationCode{}
	if len(codeItems) > 0 {
		codes, err = s.store.CreateActivationCodes(r.Context(), codeItems)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "生成激活码失败")
			return
		}
	}

	_ = s.store.AddActivationQuota(r.Context(), len(codes)+len(createdAccounts))
	s.logAudit(r, store.LogCategoryDevice, "import_contacts",
		fmt.Sprintf("从通讯录导入：激活码 %d 个、座机账号 %d 个、跳过 %d 条",
			len(codes), len(createdAccounts), len(skipped)))

	writeJSON(w, http.StatusCreated, map[string]any{
		"codes":        codes,
		"accounts":     createdAccounts,
		"skipped":      skipped,
		"dailyLimit":   limit,
		"todayCreated": created + need,
	})
}
