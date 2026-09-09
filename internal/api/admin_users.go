package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"phonebook-server/internal/auth"
	"phonebook-server/internal/store"
)

// usernameRe 管理员用户名规则：字母/数字/下划线/点/短横线，3~32 位。
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// handleListAdminUsers 列出全部管理员账号（super）。
func (s *Server) handleListAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// handleCreateAdminUser 新建管理员账号（super）。新账号一律置 must_change_password=1。
func (s *Server) handleCreateAdminUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string  `json:"username"`
		Password    string  `json:"password"`
		DisplayName string  `json:"displayName"`
		Role        string  `json:"role"`
		UnitIDs     []int64 `json:"unitIds"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	in.DisplayName = strings.TrimSpace(in.DisplayName)

	if !usernameRe.MatchString(in.Username) {
		writeErr(w, http.StatusBadRequest, "用户名需为 3~32 位字母/数字/_/./-")
		return
	}
	if len(in.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "初始密码至少 6 位")
		return
	}
	switch in.Role {
	case store.RoleRoot:
		// 系统最高管理员：不绑单位；只有 root 能创建 root。
		if userFrom(r).Role != store.RoleRoot {
			writeErr(w, http.StatusForbidden, "只有系统最高管理员（root）可创建 root 账号")
			return
		}
	case store.RoleSuper:
		// 超管：不绑单位。
	case store.RoleUnit:
		if len(in.UnitIDs) == 0 {
			writeErr(w, http.StatusBadRequest, "单位管理员至少需绑定一个单位")
			return
		}
	case store.RoleEndUser:
		// 只读用户（手机端）：可绑 0..N 个单位，0 个单位时也能看公开单位。
	default:
		writeErr(w, http.StatusBadRequest, "角色只能是 root、super、unit 或 enduser")
		return
	}
	// 校验所有 unit id 均存在（超管可任意指派）。
	for _, uid := range in.UnitIDs {
		ok, err := s.store.UnitExists(r.Context(), uid)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验单位失败")
			return
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "存在无效的单位 id")
			return
		}
	}
	// 用户名唯一性预检（友好报错）。
	if _, err := s.store.UserByUsername(r.Context(), in.Username); err == nil {
		writeErr(w, http.StatusBadRequest, "用户名已存在")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, "校验用户名失败")
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法加密密码")
		return
	}
	u, err := s.store.CreateUser(r.Context(), in.Username, hash, in.DisplayName, in.Role, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	if in.Role == store.RoleUnit || in.Role == store.RoleEndUser {
		if err := s.store.SetUserUnits(r.Context(), u.ID, in.UnitIDs); err != nil {
			writeErr(w, http.StatusInternalServerError, "绑定单位失败")
			return
		}
		u, err = s.store.UserWithUnits(r.Context(), u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取账号失败")
			return
		}
	}
	s.logAudit(r, store.LogCategoryAccount, "create_user", "新建账号 "+in.Username+"（角色 "+roleLabel(in.Role)+"）")
	writeJSON(w, http.StatusCreated, u)
}

// handleUpdateAdminUser 修改管理员账号（角色/绑定单位/启用状态，super）。
func (s *Server) handleUpdateAdminUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的用户 id")
		return
	}
	var in struct {
		DisplayName string  `json:"displayName"`
		Role        string  `json:"role"`
		IsActive    bool    `json:"isActive"`
		UnitIDs     []int64 `json:"unitIds"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)

	me := userFrom(r)
	if id == me.ID {
		writeErr(w, http.StatusBadRequest, "不能在这里修改自己的账号，请在「修改密码」页操作")
		return
	}
	target, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return
	}
	// 只有 root 能改动 root 账号（防止 super 提权/降权）。
	if target.Role == store.RoleRoot && userFrom(r).Role != store.RoleRoot {
		writeErr(w, http.StatusForbidden, "只有系统最高管理员（root）可修改 root 账号")
		return
	}
	switch in.Role {
	case store.RoleRoot:
		// 系统最高管理员：不绑单位；只有 root 能设置 root 角色。
		if userFrom(r).Role != store.RoleRoot {
			writeErr(w, http.StatusForbidden, "只有系统最高管理员（root）可设置 root 角色")
			return
		}
	case store.RoleSuper:
		// 超管：不绑单位。
	case store.RoleUnit:
		if len(in.UnitIDs) == 0 {
			writeErr(w, http.StatusBadRequest, "单位管理员至少需绑定一个单位")
			return
		}
	case store.RoleEndUser:
		// 只读用户（手机端）：可绑 0..N 个单位，0 个单位时也能看公开单位。
	default:
		writeErr(w, http.StatusBadRequest, "角色只能是 root、super、unit 或 enduser")
		return
	}
	for _, uid := range in.UnitIDs {
		ok, err := s.store.UnitExists(r.Context(), uid)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验单位失败")
			return
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "存在无效的单位 id")
			return
		}
	}

	// 防止移除最后一个可用的全局管理员（super/root）。
	if store.IsSuperRole(target.Role) && target.IsActive && !store.IsSuperRole(in.Role) {
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
	// root 另需单独保证至少保留一个：降级/停用 root 时校验。
	if target.Role == store.RoleRoot && target.IsActive &&
		(in.Role != store.RoleRoot || !in.IsActive) {
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

	// 全局管理员不保留单位绑定；unit/enduser 按 payload 整表替换绑定（enduser 可为 0..N）。
	if store.IsSuperRole(in.Role) {
		in.UnitIDs = nil
	}
	if err := s.store.UpdateUserProfile(r.Context(), id, in.DisplayName, in.Role, in.IsActive, in.UnitIDs); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "修改账号失败")
		return
	}
	// 停用账号 → 立即使其全部会话失效。
	if !in.IsActive {
		_ = s.store.DeleteUserSessions(r.Context(), id)
	}
	u, err := s.store.UserWithUnits(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return
	}
	s.logAudit(r, store.LogCategoryAccount, "update_user",
		"修改账号 "+u.Username+"（角色 "+roleLabel(u.Role)+"，启用 "+boolText(u.IsActive)+"）")
	writeJSON(w, http.StatusOK, u)
}

// handleResetAdminPassword 重置他人密码（super）。重置后账号必须改密，且旧会话全部失效。
func (s *Server) handleResetAdminPassword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的用户 id")
		return
	}
	var in struct {
		NewPassword string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	me := userFrom(r)
	if id == me.ID {
		writeErr(w, http.StatusBadRequest, "请通过「修改密码」页改自己的密码")
		return
	}
	target, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return
	}
	if len(in.NewPassword) < 6 {
		writeErr(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法加密密码")
		return
	}
	if err := s.store.ResetUserPassword(r.Context(), id, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "重置密码失败")
		return
	}
	_ = s.store.DeleteUserSessions(r.Context(), id)
	s.logAudit(r, store.LogCategoryAccount, "reset_password", "重置管理员 "+target.Username+" 的密码")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// otherActiveSuperExists 是否存在除 excludeID 外的启用全局管理员（super/root）。
func (s *Server) otherActiveSuperExists(ctx context.Context, excludeID int64) (bool, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.ID != excludeID && u.IsActive && store.IsSuperRole(u.Role) {
			return true, nil
		}
	}
	return false, nil
}

// otherActiveRootExists 是否存在除 excludeID 外的启用 root。
func (s *Server) otherActiveRootExists(ctx context.Context, excludeID int64) (bool, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.ID != excludeID && u.IsActive && u.Role == store.RoleRoot {
			return true, nil
		}
	}
	return false, nil
}

func roleLabel(role string) string {
	switch role {
	case store.RoleRoot:
		return "系统管理员"
	case store.RoleSuper:
		return "超级管理员"
	case store.RoleEndUser:
		return "只读用户"
	default:
		return "单位管理员"
	}
}

func boolText(b bool) string {
	if b {
		return "是"
	}
	return "否"
}
