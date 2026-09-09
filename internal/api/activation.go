package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"phonebook-server/internal/auth"
	"phonebook-server/internal/store"
)

// deviceTokenTTL 设备令牌有效期（长期；换机即失效）。
const deviceTokenTTL = 180 * 24 * time.Hour

// normalizePhone 归一化手机号：只留数字，去掉 86 国家码前缀。
func normalizePhone(in string) string {
	digits := make([]rune, 0, len(in))
	for _, r := range in {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	s := string(digits)
	if len(s) > 11 && strings.HasPrefix(s, "86") {
		s = s[2:]
	}
	return s
}

// startOfTodayUTC 返回服务器本地“今天 00:00”对应的 UTC RFC3339 字符串（用于每日限额统计）。
func startOfTodayUTC() string {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start.UTC().Format(time.RFC3339)
}

// ---- 设备激活（公开）----

// handleDeviceActivate 用一次性激活码换取长期设备令牌。
// 码绑定手机号，激活时号码必须一致；激活会撤销该用户名下所有旧设备（换机语义）。
func (s *Server) handleDeviceActivate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code  string `json:"code"`
		Phone string `json:"phone"`
		Label string `json:"label"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	code := store.NormalizeActivationCode(in.Code)
	phone := normalizePhone(in.Phone)
	if code == "" || phone == "" {
		writeErr(w, http.StatusBadRequest, "请输入激活码和手机号")
		return
	}

	ip := clientIP(r)
	guardKey := "activate|" + ip
	if s.guard.tooMany(guardKey) {
		writeErr(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}

	c, err := s.store.ActivationCodeByCode(r.Context(), code)
	if err != nil {
		s.guard.recordFailure(guardKey)
		_ = s.store.AppendLog(r.Context(), 0, phone, store.LogCategoryDevice, "activate_fail", "激活失败：激活码不存在", ip)
		writeErr(w, http.StatusBadRequest, "激活码无效，请核对后重试")
		return
	}
	switch c.Status {
	case store.CodeStatusUsed:
		writeErr(w, http.StatusBadRequest, "该激活码已被使用（一个码只能用一次）")
		return
	case store.CodeStatusExpired:
		writeErr(w, http.StatusBadRequest, "该激活码已过期，请联系管理员重新生成")
		return
	case store.CodeStatusRevoked:
		writeErr(w, http.StatusBadRequest, "该激活码已被作废")
		return
	}
	if normalizePhone(c.Phone) != phone {
		s.guard.recordFailure(guardKey)
		writeErr(w, http.StatusBadRequest, "手机号与该激活码不匹配，请核对")
		return
	}

	// 按手机号找/建只读账号（未设置密码，见下）。
	u, err := s.store.UserByUsername(r.Context(), phone)
	if errors.Is(err, store.ErrNotFound) {
		// 密码存哨兵值：激活码账号默认不可用密码登录；将来要支持手机号+密码登录时，
		// 由管理员重置密码或客户端激活后自行设置。
		u, err = s.store.CreateUser(r.Context(), phone, auth.NoPasswordHash, c.DisplayName, c.Role, false)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "创建账号失败")
			return
		}
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "激活失败")
		return
	} else if u.Role != store.RoleEndUser {
		writeErr(w, http.StatusBadRequest, "该手机号已被管理员账号占用，请联系管理员处理")
		return
	}
	// 账号已被管理员停用 → 不允许激活（否则会拿到一个立刻失效的令牌）。
	if !u.IsActive {
		writeErr(w, http.StatusBadRequest, "该账号已停用，请联系管理员")
		return
	}

	// 换机：撤销该用户名下所有旧设备与令牌。
	if err := s.store.RevokeUserDevices(r.Context(), u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "激活失败")
		return
	}
	// 应用激活码携带的可见范围（单位 + 单点联系人）。
	if err := s.store.SetUserUnits(r.Context(), u.ID, c.UnitIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, "应用可见范围失败")
		return
	}
	if err := s.store.SetUserContactGrants(r.Context(), u.ID, c.ContactIDs, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "应用可见范围失败")
		return
	}

	dev, err := s.store.CreateDevice(r.Context(), u.ID, phone, strings.TrimSpace(in.Label), ip)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "创建设备失败")
		return
	}
	token, err := s.auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "签发令牌失败")
		return
	}
	expiresAt := time.Now().UTC().Add(deviceTokenTTL).Format(time.RFC3339)
	if err := s.store.CreateDeviceToken(r.Context(), dev.ID, auth.TokenHash(token), expiresAt); err != nil {
		writeErr(w, http.StatusInternalServerError, "签发令牌失败")
		return
	}
	if err := s.store.MarkCodeUsed(r.Context(), c.ID, dev.ID); err != nil {
		writeErr(w, http.StatusBadRequest, "该激活码已被使用")
		return
	}
	s.guard.clear(guardKey)

	_ = s.store.AppendLog(r.Context(), u.ID, phone, store.LogCategoryDevice, "activate",
		"设备激活成功（"+maskCode(code)+"）", ip)

	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"expiresAt": expiresAt,
		"user": map[string]any{
			"id":          u.ID,
			"phone":       phone,
			"displayName": u.DisplayName,
			"role":        u.Role,
		},
		"unitIds":    c.UnitIDs,
		"contactIds": c.ContactIDs,
	})
}

// handleDeviceMe 返回当前设备/账号信息与可见范围（客户端展示“已激活”）。
func (s *Server) handleDeviceMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	dev, _ := s.store.DeviceByUser(r.Context(), u.ID)
	units, err := s.store.UserUnitIDs(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取可见范围失败")
		return
	}
	grants, err := s.store.UserContactGrantIDs(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取可见范围失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":          u.ID,
			"phone":       u.Username,
			"displayName": u.DisplayName,
			"role":        u.Role,
		},
		"device":     dev,
		"unitIds":    units,
		"contactIds": grants,
	})
}

// ---- 激活码管理（super/root 全部；unit 仅本单位且不能设单点授权）----

type codeInput struct {
	Phone       string  `json:"phone"`
	DisplayName string  `json:"displayName"`
	UnitIDs     []int64 `json:"unitIds"`
	ContactIDs  []int64 `json:"contactIds"`
}

// handleCreateActivationCodes 批量生成激活码（受每日上限约束）。
func (s *Server) handleCreateActivationCodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items         []codeInput `json:"items"`
		ExpiresInDays int         `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if len(in.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "请至少提供一条激活码信息")
		return
	}
	if len(in.Items) > 100 {
		writeErr(w, http.StatusBadRequest, "一次最多生成 100 个激活码")
		return
	}
	me := userFrom(r)
	boundIDs, err := s.store.UserUnitIDs(r.Context(), me.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取单位失败")
		return
	}
	boundSet := map[int64]bool{}
	for _, id := range boundIDs {
		boundSet[id] = true
	}
	isUnitAdmin := me.Role == store.RoleUnit

	days := in.ExpiresInDays
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	expiresAt := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)

	items := make([]store.NewActivationCode, 0, len(in.Items))
	for i, it := range in.Items {
		phone := normalizePhone(it.Phone)
		if len(phone) < 6 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("第 %d 条手机号无效", i+1))
			return
		}
		if isUnitAdmin {
			if len(it.ContactIDs) > 0 {
				writeErr(w, http.StatusForbidden, "单位管理员不能设置单点可见联系人")
				return
			}
			for _, uid := range it.UnitIDs {
				if !boundSet[uid] {
					writeErr(w, http.StatusForbidden, "单位管理员只能选择自己绑定的单位")
					return
				}
			}
		}
		for _, uid := range it.UnitIDs {
			ok, err := s.store.UnitExists(r.Context(), uid)
			if err != nil || !ok {
				writeErr(w, http.StatusBadRequest, "存在无效的单位 id")
				return
			}
		}
		items = append(items, store.NewActivationCode{
			Phone:       phone,
			DisplayName: strings.TrimSpace(it.DisplayName),
			Role:        store.RoleEndUser,
			UnitIDs:     it.UnitIDs,
			ContactIDs:  it.ContactIDs,
			ExpiresAt:   expiresAt,
			CreatedBy:   me.ID,
		})
	}

	// 每日上限（全局，root 可配置）。
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
	if created+len(items) > limit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("超出每日上限：今日已生成 %d 个，上限 %d 个（可由系统管理员调整）", created, limit))
		return
	}

	codes, err := s.store.CreateActivationCodes(r.Context(), items)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成激活码失败")
		return
	}
	_ = s.store.AddActivationQuota(r.Context(), len(codes))
	s.logAudit(r, store.LogCategoryDevice, "code_create",
		fmt.Sprintf("生成 %d 个激活码（有效期 %d 天）", len(codes), days))
	writeJSON(w, http.StatusCreated, map[string]any{"codes": codes, "dailyLimit": limit, "todayCreated": created + len(codes)})
}

// handleListActivationCodes 列出激活码（unit 只能看自己生成的）。
func (s *Server) handleListActivationCodes(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r)
	createdBy := int64(0)
	if me.Role == store.RoleUnit {
		createdBy = me.ID
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	status := r.URL.Query().Get("status")
	codes, err := s.store.ListActivationCodes(r.Context(), createdBy, status, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取激活码失败")
		return
	}
	limitToday, _ := s.store.ActivationDailyLimit(r.Context())
	createdToday, _ := s.store.ActivationQuotaToday(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"codes":        codes,
		"dailyLimit":   limitToday,
		"todayCreated": createdToday,
	})
}

// handleRevokeActivationCode 作废未使用的激活码（unit 仅能作废自己生成的）。
func (s *Server) handleRevokeActivationCode(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的 id")
		return
	}
	me := userFrom(r)
	if me.Role == store.RoleUnit {
		codes, err := s.store.ListActivationCodes(r.Context(), me.ID, "", 500)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取激活码失败")
			return
		}
		found := false
		for _, c := range codes {
			if c.ID == id {
				found = true
				break
			}
		}
		if !found {
			writeErr(w, http.StatusForbidden, "只能作废自己生成的激活码")
			return
		}
	}
	if err := s.store.RevokeActivationCode(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "该激活码已使用或已作废")
			return
		}
		writeErr(w, http.StatusInternalServerError, "作废失败")
		return
	}
	s.logAudit(r, store.LogCategoryDevice, "code_revoke", fmt.Sprintf("作废激活码 #%d", id))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleExportActivationCodes 导出激活码 CSV（UTF-8 BOM，Excel 直接打开不乱码）。
func (s *Server) handleExportActivationCodes(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r)
	createdBy := int64(0)
	if me.Role == store.RoleUnit {
		createdBy = me.ID
	}
	codes, err := s.store.ListActivationCodes(r.Context(), createdBy, "", 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取激活码失败")
		return
	}
	units, _ := s.store.ListUnits(r.Context())
	unitName := map[int64]string{}
	for _, u := range units {
		unitName[u.ID] = u.Name
	}

	var b strings.Builder
	b.WriteString("\xEF\xBB\xBF") // BOM
	b.WriteString("激活码,手机号,姓名,角色,可见单位,单点联系人,状态,有效期,创建时间\n")
	for _, c := range codes {
		names := make([]string, 0, len(c.UnitIDs))
		for _, id := range c.UnitIDs {
			if n, ok := unitName[id]; ok {
				names = append(names, n)
			} else {
				names = append(names, strconv.FormatInt(id, 10))
			}
		}
		role := "只读用户"
		if c.Role == store.RoleUnit {
			role = "单位管理员"
		}
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%d,%s,%s,%s\n",
			c.Code, c.Phone, c.DisplayName, role,
			strings.Join(names, " / "), len(c.ContactIDs),
			codeStatusLabel(c.Status), c.ExpiresAt, c.CreatedAt)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="activation_codes.csv"`)
	_, _ = w.Write([]byte(b.String()))
}

func codeStatusLabel(s string) string {
	switch s {
	case store.CodeStatusPending:
		return "未使用"
	case store.CodeStatusUsed:
		return "已使用"
	case store.CodeStatusExpired:
		return "已过期"
	case store.CodeStatusRevoked:
		return "已作废"
	default:
		return s
	}
}

func maskCode(code string) string {
	if len(code) <= 4 {
		return code
	}
	return "****" + code[len(code)-4:]
}

// ---- 系统设置（root）----

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	limit, err := s.store.ActivationDailyLimit(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取设置失败")
		return
	}
	created, _ := s.store.ActivationQuotaToday(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"activationDailyLimit": limit,
		"activationToday":      created,
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ActivationDailyLimit int `json:"activationDailyLimit"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.ActivationDailyLimit < 1 || in.ActivationDailyLimit > 10000 {
		writeErr(w, http.StatusBadRequest, "每日上限需在 1~10000 之间")
		return
	}
	if err := s.store.SetActivationDailyLimit(r.Context(), in.ActivationDailyLimit); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存设置失败")
		return
	}
	s.logAudit(r, store.LogCategoryAccount, "update_settings",
		fmt.Sprintf("调整每日激活码上限为 %d", in.ActivationDailyLimit))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "activationDailyLimit": in.ActivationDailyLimit})
}

// ---- 可见性授权（super/root）----

func (s *Server) handleGetVisibility(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的用户 id")
		return
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	units, err := s.store.UserUnitIDs(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取可见范围失败")
		return
	}
	contacts, err := s.store.ListUserContactGrants(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取可见范围失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"userId":   u.ID,
		"username": u.Username,
		"role":     u.Role,
		"unitIds":  units,
		"contacts": contacts,
	})
}

func (s *Server) handleUpdateVisibility(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的用户 id")
		return
	}
	var in struct {
		UnitIDs    []int64 `json:"unitIds"`
		ContactIDs []int64 `json:"contactIds"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if store.IsSuperRole(u.Role) {
		writeErr(w, http.StatusBadRequest, "全局管理员默认可看全部，无需配置可见范围")
		return
	}
	for _, uid := range in.UnitIDs {
		ok, err := s.store.UnitExists(r.Context(), uid)
		if err != nil || !ok {
			writeErr(w, http.StatusBadRequest, "存在无效的单位 id")
			return
		}
	}
	if err := s.store.SetUserUnits(r.Context(), id, in.UnitIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存可见单位失败")
		return
	}
	me := userFrom(r)
	if err := s.store.SetUserContactGrants(r.Context(), id, in.ContactIDs, me.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存单点授权失败")
		return
	}
	s.logAudit(r, store.LogCategoryAccount, "update_visibility",
		fmt.Sprintf("调整 %s 的可见范围（单位 %d 个，单点联系人 %d 个）",
			u.Username, len(in.UnitIDs), len(in.ContactIDs)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
