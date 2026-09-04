package api

import (
	"log"
	"net/http"
	"strings"
	"time"

	"phonebook-server/internal/auth"
	"phonebook-server/internal/store"
)

const (
	loginWindow   = 60 * time.Second
	loginMaxFails = 5
)

// loginGuard 简易内存防爆破（按 用户名+IP）。
// 以便“以后客户端直接用同一接口做身份校验”。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Client   string `json:"client"` // 'web'（默认，Cookie 会话）| 'app'（手机端，返回 Bearer token）
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "请输入用户名和密码")
		return
	}
	client := in.Client
	if client == "" {
		client = "web"
	}
	if client != "web" && client != "app" {
		writeErr(w, http.StatusBadRequest, "client 只能为 web 或 app")
		return
	}

	ip := clientIP(r)
	key := in.Username + "|" + ip
	if s.guard.tooMany(key) {
		writeErr(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}

	u, err := s.store.UserByUsername(r.Context(), in.Username)
	if err != nil {
		s.guard.recordFailure(key)
		s.logAudit(r, store.LogCategoryAuth, "login_fail", "登录失败："+in.Username)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !u.IsActive || !auth.CheckPassword(u.PasswordHash, in.Password) {
		s.guard.recordFailure(key)
		s.logAudit(r, store.LogCategoryAuth, "login_fail", "登录失败："+in.Username)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	s.guard.clear(key)

	// 只读账号（enduser）仅允许手机端（client=app）登录，网页后台一律拒绝。
	if u.Role == store.RoleEndUser && client != "app" {
		_ = s.store.AppendLog(r.Context(), u.ID, u.Username,
			store.LogCategoryAuth, "login_blocked", "只读账号尝试网页登录被拒", clientIP(r))
		writeErr(w, http.StatusForbidden, "该账号仅限手机端登录")
		return
	}

	// 签发会话
	token, err := s.auth.NewToken()
	if err != nil {
		log.Printf("issue token: %v", err)
		writeErr(w, http.StatusInternalServerError, "无法签发登录凭证")
		return
	}
	expiresAt := s.auth.ExpiresAt()
	if err := s.store.CreateSession(r.Context(), u.ID, auth.TokenHash(token), expiresAt); err != nil {
		log.Printf("create session: %v", err)
		writeErr(w, http.StatusInternalServerError, "无法创建会话")
		return
	}

	// 仅网页端（client=web）种 Cookie 会话；App 端只返回 Bearer token，不种 Cookie——
	// 这样服务端能可靠区分“网页 unit 只管绑定单位”与“手机端 unit/enduser 可见=绑定∪公开”。
	// （见 server.go authChannel / accessUnitIDs。）
	if client != "app" {
		expireT, _ := time.Parse(time.RFC3339, expiresAt)
		http.SetCookie(w, &http.Cookie{
			Name:     s.sessionName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  expireT,
			MaxAge:   int(time.Until(expireT).Seconds()),
		})
	}

	// 登录成功日志（含来源端）。
	_ = s.store.AppendLog(r.Context(), u.ID, u.Username,
		store.LogCategoryAuth, "login", "登录成功（"+client+" 端）", clientIP(r))

	payload, err := s.userPayload(r, u)
	if err != nil {
		log.Printf("build user payload: %v", err)
		writeErr(w, http.StatusInternalServerError, "读取账号信息失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  payload,
	})
}

// handleMe 返回当前登录用户（供管理页判断登录态）。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	payload, err := s.userPayload(r, u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取账号信息失败")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// userPayload 返回对前端可见的用户信息；unit/enduser 额外带出其绑定单位
// （手机端据此提示可见范围；绑定单位 ∪ 公开单位）。
func (s *Server) userPayload(r *http.Request, u store.User) (map[string]any, error) {
	m := map[string]any{
		"id":                 u.ID,
		"username":           u.Username,
		"displayName":        u.DisplayName,
		"role":               u.Role,
		"mustChangePassword": u.MustChangePassword,
	}
	if u.Role == store.RoleUnit || u.Role == store.RoleEndUser {
		units, err := s.store.UserUnits(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		m["units"] = units
	}
	return m, nil
}

// handleLogout 注销当前会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	raw := s.resolveToken(r)
	if raw != "" {
		_ = s.store.DeleteSession(r.Context(), auth.TokenHash(raw))
	}
	if u.ID > 0 {
		_ = s.store.AppendLog(r.Context(), u.ID, u.Username,
			store.LogCategoryAuth, "logout", "退出登录", clientIP(r))
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.sessionName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChangePassword 修改当前用户密码；改完使其它会话失效（保留当前会话）。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var in struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if len(in.NewPassword) < 6 {
		writeErr(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, in.OldPassword) {
		writeErr(w, http.StatusBadRequest, "原密码不正确")
		return
	}

	newHash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法加密新密码")
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), u.ID, newHash); err != nil {
		log.Printf("update password: %v", err)
		writeErr(w, http.StatusInternalServerError, "修改密码失败")
		return
	}

	// 只保留当前会话，其它设备强制重新登录。
	keep := ""
	if raw := s.resolveToken(r); raw != "" {
		keep = auth.TokenHash(raw)
	}
	if err := s.store.DeleteUserSessionsExcept(r.Context(), u.ID, keep); err != nil {
		log.Printf("invalidate sessions: %v", err)
	}
	_ = s.store.AppendLog(r.Context(), u.ID, u.Username,
		store.LogCategoryAuth, "change_password", "修改密码", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}
