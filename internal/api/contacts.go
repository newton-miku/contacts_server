package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"phonebook-server/internal/store"
)

// snapshot 是 GET /api/contacts 的返回结构。
type snapshot struct {
	Version   int64           `json:"version"`
	UpdatedAt string          `json:"updatedAt"`
	Contacts  []store.Contact `json:"contacts"`
}

// currentVersion 读取当前数据版本与更新时间（纯读，无副作用）。
// GET /api/contacts 用它给快照打版本号，必须在读取联系人之前调用（见 store.DataVersion）。
func (s *Server) currentVersion(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	v, ts, err := s.store.DataVersion(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取数据版本失败")
		return 0, "", false
	}
	return v, ts, true
}

// handleGetVersion 返回极小的版本信息，客户端据此判断是否需要拉全量。
//
// version/updatedAt 是「数据版本」：写操作只在自身事务里打脏标记、**不**递增版本号，
// 版本号由后台发布器在「静默窗口」（最后一条改动之后满 version_quiet_minutes 没有新改动）
// 到期后合并成一次 +1，管理员也可手动立即发布（POST /api/version/publish）。
// 本接口是**纯读**、无副作用，客户端可以放心频繁探测。
// pending/publishDueAt 表示「还有改动没发布、预计何时自动发布」，管理页据此提示并可手动发布；
// appVersion/buildTime/schemaVersion 是运行版本、构建时刻与库结构版本（管理页展示/运维排查用）。
func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.VersionState(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取数据版本失败")
		return
	}
	sv, err := s.store.SchemaVersion(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取结构版本失败")
		return
	}
	quiet, _ := s.store.VersionQuietMinutes(r.Context())
	out := map[string]any{
		"version":       st.Version,
		"updatedAt":     st.UpdatedAtRaw,
		"appVersion":    BuildVersion,
		"buildTime":     BuildTime,
		"schemaVersion": sv,
		"pending":       st.Pending,
		"quietMinutes":  quiet,
	}
	if st.Pending {
		if due := st.PublishDue(time.Duration(quiet) * time.Minute); !due.IsZero() {
			out["publishDueAt"] = due.UTC().Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePublishVersion 管理员手动发布数据版本（super/unit/root，须已过强制改密）：
// 把待发布的改动合并成一次版本递增，手机端下一次探测即拉全量（用于「改完号码想马上生效」）。
// 没有待发布改动时不递增版本（幂等，可放心连点）。
func (s *Server) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	v, published, err := s.store.PublishDataVersion(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "发布数据版本失败（数据库忙，请稍后重试）")
		return
	}
	if published {
		s.logAudit(r, store.LogCategoryContact, "publish_version",
			fmt.Sprintf("手动发布数据版本（数据版本 %d）", v))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": v, "published": published,
	})
}

// handleListContacts 返回联系人快照（含版本号），**必须携带有效令牌**（账号会话或设备令牌）。
// 可见范围由 accessScope 统一计算：super/root=全部；unit 网页=绑定单位；
// 手机端（enduser/unit bearer/设备令牌）=绑定单位 ∪ 公开单位 ∪ 单点授权联系人。
// 响应经 gzip 中间件压缩，减小传输体积。
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	scope, err := s.accessScope(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	// 先读版本号、再读联系人（顺序不可颠倒）：若两者之间恰好有写入提交或版本发布，客户端
	// 只会把「更新的数据」记在「更旧的版本号」下——下次探测版本必然不一致、触发重拉；
	// 反过来则会出现「旧数据配新版本号」，客户端会误判为已最新而永远不再更新。
	v, ts, ok := s.currentVersion(w, r)
	if !ok {
		return
	}
	contacts, err := s.store.ListContactsScoped(r.Context(), scope)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	if contacts == nil {
		contacts = []store.Contact{}
	}
	writeJSON(w, http.StatusOK, snapshot{Version: v, UpdatedAt: ts, Contacts: contacts})
}

// handleCreateContact 新增联系人（super 或 unit）。
func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	var c store.Contact
	if err := decodeJSON(w, r, &c); err != nil {
		return
	}
	normalized, err := store.NormalizeContact(c)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ok, err := s.managesUnit(r, normalized.UnitID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "校验单位权限失败")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "无权在该单位下新增联系人")
		return
	}
	created, err := s.store.CreateContact(r.Context(), normalized)
	if err != nil {
		if errors.Is(err, store.ErrUnknownUnit) {
			writeErr(w, http.StatusBadRequest, "单位不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "保存联系人失败")
		return
	}
	s.logAudit(r, store.LogCategoryContact, "create",
		"新增联系人 "+created.DisplayName+"（单位："+created.Unit+"）")
	writeJSON(w, http.StatusCreated, created)
}

// handleUpdateContact 更新联系人（super 或 unit；仅能改自己范围内的）。
func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的联系人 id")
		return
	}
	var c store.Contact
	if err := decodeJSON(w, r, &c); err != nil {
		return
	}
	normalized, err := store.NormalizeContact(c)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.store.ContactByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "联系人不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	// 目标联系人的当前单位 + 目标单位都必须在调用者权限内。
	for _, uid := range []int64{existing.UnitID, normalized.UnitID} {
		ok, err := s.managesUnit(r, uid)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验单位权限失败")
			return
		}
		if !ok {
			writeErr(w, http.StatusForbidden, "无权修改该联系人或目标单位")
			return
		}
	}
	updated, err := s.store.UpdateContact(r.Context(), id, normalized)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "联系人不存在")
			return
		}
		if errors.Is(err, store.ErrUnknownUnit) {
			writeErr(w, http.StatusBadRequest, "单位不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "更新联系人失败")
		return
	}
	s.logAudit(r, store.LogCategoryContact, "update",
		"更新联系人 "+updated.DisplayName+"（单位："+updated.Unit+"）")
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteContact 删除联系人（super 或 unit；仅能删自己范围内的）。
func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的联系人 id")
		return
	}
	existing, err := s.store.ContactByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "联系人不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	ok, err := s.managesUnit(r, existing.UnitID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "校验单位权限失败")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "无权删除该单位的联系人")
		return
	}
	if err := s.store.DeleteContact(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "联系人不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除联系人失败")
		return
	}
	s.logAudit(r, store.LogCategoryContact, "delete",
		"删除联系人 "+existing.DisplayName+"（单位："+existing.Unit+"）")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// handleListContactsPaged 管理页联系人分页查询（super/unit，按可见范围与搜索条件过滤）。
// GET /api/admin/contacts?page=1&pageSize=20&keyword=&unitId=
func (s *Server) handleListContactsPaged(w http.ResponseWriter, r *http.Request) {
	scope, err := s.accessScope(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	q := r.URL.Query()
	page := 1
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 0 {
		page = v
	}
	pageSize := 20
	if v, err := strconv.Atoi(q.Get("pageSize")); err == nil && v > 0 && v <= 200 {
		pageSize = v
	}
	var unitID int64
	if v := q.Get("unitId"); v != "" {
		unitID, _ = strconv.ParseInt(v, 10, 64)
	}
	// 单位过滤必须在可见范围内，否则直接返回空页。
	if unitID > 0 && !scope.All {
		ok := false
		for _, id := range scope.UnitIDs {
			if id == unitID {
				ok = true
				break
			}
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"items": []store.Contact{}, "total": 0, "page": page, "pageSize": pageSize,
			})
			return
		}
	}

	items, total, err := s.store.ListContactsPaged(r.Context(), scope, q.Get("keyword"), unitID,
		pageSize, (page-1)*pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	if items == nil {
		items = []store.Contact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "pageSize": pageSize,
	})
}
