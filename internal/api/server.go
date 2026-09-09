package api

import (
	"compress/gzip"
	"context"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"phonebook-server/internal/auth"
	"phonebook-server/internal/store"
)

// loginGuard 简易内存防爆破（按 用户名+IP）。
type loginGuard struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginGuard() *loginGuard {
	return &loginGuard{attempts: make(map[string][]time.Time)}
}

func (g *loginGuard) tooMany(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-loginWindow)
	list := g.attempts[key]
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	g.attempts[key] = kept
	return len(kept) >= loginMaxFails
}

func (g *loginGuard) recordFailure(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attempts[key] = append(g.attempts[key], time.Now())
}

func (g *loginGuard) clear(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.attempts, key)
}

// BuildVersion 是发布版本标识，构建期经 -ldflags 注入：
//
//	go build -ldflags "-X phonebook-server/internal/api.BuildVersion=$(git rev-parse --short HEAD)" .
//
// 未注入时为 "dev"（对应本地 go run）。经 GET /api/version 的 appVersion 字段暴露，
// 管理页顶栏展示（与 meta.data_version「数据版本」区分：那是每次写 +1 的缓存同步号）。
var BuildVersion = "dev"

// BuildTime 是构建时刻（UTC RFC3339），与 BuildVersion 一同在构建期注入（见上）；
// 未注入为空串，界面按未知展示。示例：
//
//	-X phonebook-server/internal/api.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var BuildTime = ""

// Server 持有依赖并提供全部 HTTP 处理器。
type Server struct {
	store *store.Store
	auth  *auth.Manager
	guard *loginGuard
	// WebFS 提供管理页静态资源；nil 时跳过静态服务。
	WebFS fs.FS
	// session cookie 名。
	sessionName string
}

// NewServer 组装服务。static 为 nil 时只提供 API。
func NewServer(st *store.Store, static fs.FS) *Server {
	return &Server{
		store:       st,
		auth:        auth.New(),
		guard:       newLoginGuard(),
		WebFS:       static,
		sessionName: "pb_session",
	}
}

// Handler 返回完整路由（含中间件）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- 公开读（仅健康检查与版本；联系人必须带令牌）----
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/version", s.handleGetVersion)
	mux.HandleFunc("GET /api/contacts", s.withAuth(s.handleListContacts))

	// ---- 设备激活（公开：用一次性激活码换取长期设备令牌）----
	mux.HandleFunc("POST /api/device/activate", s.handleDeviceActivate)
	mux.HandleFunc("GET /api/device/me", s.withAuth(s.handleDeviceMe))

	// ---- 身份 ----
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.withAuth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.withAuth(s.handleMe))
	mux.HandleFunc("POST /api/auth/password", s.withAuth(s.handleChangePassword))

	// ---- 单位（读：任一管理员；写：仅 super）----
	mux.HandleFunc("GET /api/units", s.withAuth(s.handleListUnits))
	mux.HandleFunc("POST /api/units", s.withSuper(s.handleCreateUnit))
	mux.HandleFunc("POST /api/units/bulk-public", s.withSuper(s.handleBulkPublic))
	mux.HandleFunc("PUT /api/units/{id}", s.withSuper(s.handleUpdateUnit))
	mux.HandleFunc("DELETE /api/units/{id}", s.withSuper(s.handleDeleteUnit))

	// ---- 联系人写/导入导出（super 或 unit，须已过强制改密）----
	mux.HandleFunc("POST /api/contacts", s.withStaff(s.handleCreateContact))
	mux.HandleFunc("PUT /api/contacts/{id}", s.withStaff(s.handleUpdateContact))
	mux.HandleFunc("DELETE /api/contacts/{id}", s.withStaff(s.handleDeleteContact))
	mux.HandleFunc("GET /api/contacts/export", s.withStaff(s.handleExportContacts))
	mux.HandleFunc("POST /api/contacts/import", s.withStaff(s.handleImportContacts))
	mux.HandleFunc("GET /api/duplicates", s.withStaff(s.handleDuplicates))

	// ---- 管理员账号管理（super/root）----
	mux.HandleFunc("GET /api/admin/users", s.withSuper(s.handleListAdminUsers))
	mux.HandleFunc("POST /api/admin/users", s.withSuper(s.handleCreateAdminUser))
	mux.HandleFunc("PUT /api/admin/users/{id}", s.withSuper(s.handleUpdateAdminUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}", s.withSuper(s.handleDeleteAdminUser))
	mux.HandleFunc("POST /api/admin/users/{id}/reset-password", s.withSuper(s.handleResetAdminPassword))

	// ---- 联系人 ↔ 账号关联（编辑/删除联系人时的联动提示）----
	mux.HandleFunc("GET /api/contacts/{id}/linked-user", s.withStaff(s.handleContactLinkedUser))

	// ---- 从通讯录导入发码（手机号→激活码；座机→账号密码账号）----
	mux.HandleFunc("POST /api/activation-codes/from-contacts", s.withStaff(s.handleImportCodesFromContacts))

	// ---- 可见性授权（super/root）：隐藏组/成员对某用户可见 ----
	mux.HandleFunc("GET /api/admin/users/{id}/visibility", s.withVisibility(s.handleGetVisibility))
	mux.HandleFunc("PUT /api/admin/users/{id}/visibility", s.withVisibility(s.handleUpdateVisibility))

	// ---- 激活码管理（super/root 全部；unit 仅本单位且不能设单点授权）----
	mux.HandleFunc("GET /api/activation-codes", s.withStaff(s.handleListActivationCodes))
	mux.HandleFunc("POST /api/activation-codes", s.withStaff(s.handleCreateActivationCodes))
	mux.HandleFunc("POST /api/activation-codes/{id}/revoke", s.withStaff(s.handleRevokeActivationCode))
	mux.HandleFunc("GET /api/activation-codes/export", s.withStaff(s.handleExportActivationCodes))
	mux.HandleFunc("POST /api/activation-codes/cleanup", s.withSuper(s.handleCleanupActivationCodes))

	// ---- 系统设置（仅 root）----
	mux.HandleFunc("GET /api/admin/settings", s.withRoot(s.handleGetSettings))
	mux.HandleFunc("PUT /api/admin/settings", s.withRoot(s.handleUpdateSettings))

	// ---- 目录数据运维（仅 super）：清空旧数据后整本导入 ----
	mux.HandleFunc("POST /api/admin/clear-directory", s.withSuper(s.handleClearDirectory))

	// ---- 系统日志（仅 super）----
	mux.HandleFunc("GET /api/logs", s.withSuper(s.handleQueryLogs))
	mux.HandleFunc("POST /api/logs/cleanup", s.withSuper(s.handleCleanupLogs))

	// ---- 管理页静态资源 ----
	if s.WebFS != nil {
		fileServer := http.FileServer(http.FS(s.WebFS))
		mux.Handle("GET /", fileServer)
	}

	var h http.Handler = mux
	h = s.gzip(h)
	h = s.cors(h)
	h = s.logRequests(h)
	h = s.recoverPanic(h)
	return h
}

// ---- 中间件 ----

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving %s: %v", r.URL.Path, rec)
				writeErr(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// cors 允许跨域（便于 Flutter web 调试），并处理预检请求。
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// 非 API 的 OPTIONS 直接放行给后续处理。
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// gzip 对声明支持 gzip 的客户端压缩响应（JSON、前端静态资源），显著减小传输字节。
func (s *Server) gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzip.NewWriter(w)
		gw := &gzipResponseWriter{ResponseWriter: w, gz: gz}
		defer func() {
			if gw.started && gw.compress {
				_ = gz.Close()
			}
		}()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter 懒初始化 gzip：在首个 WriteHeader/Write 时决定是否压缩。
type gzipResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	started  bool
	compress bool
}

// 已压缩过的内容类型不再套一层 gzip（无收益还费 CPU）。
var noGzipTypes = []string{"image/", "font/", "video/", "audio/", "application/octet-stream"}

func (g *gzipResponseWriter) start(code int) {
	g.started = true
	ct := g.Header().Get("Content-Type")
	g.compress = true
	for _, p := range noGzipTypes {
		if strings.HasPrefix(ct, p) {
			g.compress = false
			break
		}
	}
	if g.compress {
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Add("Vary", "Accept-Encoding")
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if !g.started {
		g.start(code)
	} else {
		g.ResponseWriter.WriteHeader(code)
	}
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.started {
		g.start(http.StatusOK)
	}
	if !g.compress {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

// ---- 认证辅助 ----

type ctxKey int

const userKey ctxKey = 0

// resolveToken 从 Cookie 或 Authorization: Bearer 头取原始令牌。
func (s *Server) resolveToken(r *http.Request) string {
	if c, err := r.Cookie(s.sessionName); err == nil && c.Value != "" {
		return c.Value
	}
	ah := r.Header.Get("Authorization")
	if len(ah) > 7 && strings.EqualFold(ah[:7], "Bearer ") {
		return strings.TrimSpace(ah[7:])
	}
	return ""
}

// withAuth 要求有效会话；通过后把 user 放进 context。
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next(w, r.WithContext(ctx))
	}
}

// roleOK 判断该角色能否执行管理操作（unit 单位管理员 / super / root）。
func roleOK(role string) bool {
	return role == store.RoleSuper || role == store.RoleRoot || role == store.RoleUnit
}

// withStaff 要求 super 或 unit 登录，并强制“首次登录改密”：
// 账号仍在用初始/重置密码（must_change_password=1）时，除改密外的管理操作一律拒绝。
func (s *Server) withStaff(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		if !roleOK(u.Role) {
			writeErr(w, http.StatusForbidden, "无权限执行该操作")
			return
		}
		if u.MustChangePassword {
			writeErr(w, http.StatusForbidden, "请先修改初始密码，再执行该操作")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next(w, r.WithContext(ctx))
	}
}

// withSuper 在 withAuth 基础上要求 super 或 root，并强制“首次登录改密”。
func (s *Server) withSuper(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		if !store.IsSuperRole(u.Role) {
			writeErr(w, http.StatusForbidden, "需要超级管理员权限")
			return
		}
		if u.MustChangePassword {
			writeErr(w, http.StatusForbidden, "请先修改初始密码，再执行该操作")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next(w, r.WithContext(ctx))
	}
}

// withRoot 仅 root（系统最高管理员）：系统设置、创建/修改 root 账号。
func (s *Server) withRoot(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		if u.Role != store.RoleRoot {
			writeErr(w, http.StatusForbidden, "需要系统最高管理员（root）权限")
			return
		}
		if u.MustChangePassword {
			writeErr(w, http.StatusForbidden, "请先修改初始密码，再执行该操作")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next(w, r.WithContext(ctx))
	}
}

// withVisibility 可见性配置（super 或 root）。
func (s *Server) withVisibility(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		if !store.IsSuperRole(u.Role) {
			writeErr(w, http.StatusForbidden, "需要可见性配置权限（super/root）")
			return
		}
		if u.MustChangePassword {
			writeErr(w, http.StatusForbidden, "请先修改初始密码，再执行该操作")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next(w, r.WithContext(ctx))
	}
}

// authenticate 解析请求令牌：先按账号会话（网页/脚本），再按设备令牌（手机端激活后）。
func (s *Server) authenticate(r *http.Request) (store.User, error) {
	raw := s.resolveToken(r)
	if raw == "" {
		return store.User{}, auth.ErrUnauthorized
	}
	hash := auth.TokenHash(raw)
	if u, err := s.store.UserForSession(r.Context(), hash); err == nil {
		return u, nil
	}
	u, err := s.store.UserForDeviceToken(r.Context(), hash)
	if err != nil {
		return store.User{}, auth.ErrUnauthorized
	}
	s.store.TouchDevice(r.Context(), hash, clientIP(r))
	return u, nil
}

// userFrom 从 context 取当前用户。
func userFrom(r *http.Request) store.User {
	u, _ := r.Context().Value(userKey).(store.User)
	return u
}

// staffUnitIDs 返回当前管理用户的数据范围：super/匿名 → nil（全部）；
// unit → 绑定单位 id 列表（没有绑定则为空切片，即“一条也看不到”）。
func (s *Server) staffUnitIDs(r *http.Request) ([]int64, error) {
	u := userFrom(r)
	if u.ID == 0 || store.IsSuperRole(u.Role) {
		return nil, nil
	}
	return s.store.UserUnitIDs(r.Context(), u.ID)
}

// authChannel 返回本次请求的认证通道：cookie=浏览器/网页会话，bearer=App/脚本（Bearer 头），
// 空=匿名。与 resolveToken 同源判断：Cookie 优先（网页会话），纯 Bearer 视为 App/手机端。
// 用于区分「网页后台 unit 只管绑定单位」与「手机端 unit/enduser 可见=绑定单位∪公开单位」。
func (s *Server) authChannel(r *http.Request) string {
	if c, err := r.Cookie(s.sessionName); err == nil && c.Value != "" {
		return "cookie"
	}
	ah := r.Header.Get("Authorization")
	if len(ah) > 7 && strings.EqualFold(ah[:7], "Bearer ") {
		return "bearer"
	}
	return ""
}

// accessScope 返回 GET /api/contacts 读接口的可见范围（设计 §3）：
//   super/root          → 全部
//   unit（cookie/网页）  → 绑定单位（网页列表不掺入公开单位，写范围=读范围一致）
//   其余（enduser、unit(bearer/app)、设备令牌）→ 绑定单位 ∪ 公开单位 ∪ 单点授权联系人
// 返回空范围 = “一条也看不到”。
func (s *Server) accessScope(r *http.Request) (store.ContactScope, error) {
	u := userFrom(r)
	if u.ID == 0 {
		return store.ContactScope{}, nil
	}
	if store.IsSuperRole(u.Role) {
		return store.ContactScope{All: true}, nil
	}
	if u.Role == store.RoleUnit && s.authChannel(r) == "cookie" {
		ids, err := s.store.UserUnitIDs(r.Context(), u.ID)
		if err != nil {
			return store.ContactScope{}, err
		}
		return store.ContactScope{UnitIDs: ids}, nil
	}
	bound, err := s.store.UserUnitIDs(r.Context(), u.ID)
	if err != nil {
		return store.ContactScope{}, err
	}
	pub, err := s.store.PublicUnitIDs(r.Context())
	if err != nil {
		return store.ContactScope{}, err
	}
	grants, err := s.store.UserContactGrantIDs(r.Context(), u.ID)
	if err != nil {
		return store.ContactScope{}, err
	}
	seen := make(map[int64]bool, len(bound)+len(pub))
	out := make([]int64, 0, len(bound)+len(pub))
	for _, id := range append(append([]int64{}, bound...), pub...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return store.ContactScope{UnitIDs: out, ContactIDs: grants}, nil
}

// managesUnit 判断当前登录管理员是否被允许管理该单位（super 恒 true）。
func (s *Server) managesUnit(r *http.Request, unitID int64) (bool, error) {
	u := userFrom(r)
	if u.ID == 0 || store.IsSuperRole(u.Role) {
		return true, nil
	}
	units, err := s.store.UserUnits(r.Context(), u.ID)
	if err != nil {
		return false, err
	}
	for _, un := range units {
		if un.ID == unitID {
			return true, nil
		}
	}
	return false, nil
}

// logAudit 记一条审计日志（当前登录用户 + 请求来源 IP）。
func (s *Server) logAudit(r *http.Request, category, action, detail string) {
	u := userFrom(r)
	_ = s.store.AppendLog(r.Context(), u.ID, u.Username, category, action, detail, clientIP(r))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
