package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"phonebook-server/internal/store"
)

// snapshot 是 GET /api/contacts 的返回结构。
type snapshot struct {
	Version   int64           `json:"version"`
	UpdatedAt string          `json:"updatedAt"`
	Contacts  []store.Contact `json:"contacts"`
}

func (s *Server) currentVersion(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	v, ts, err := s.store.DataVersion(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取数据版本失败")
		return 0, "", false
	}
	return v, ts, true
}

// handleGetVersion 返回极小的版本信息，客户端据此判断是否需要拉全量。
func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	v, ts, ok := s.currentVersion(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":   v,
		"updatedAt": ts,
	})
}

// handleListContacts 返回联系人快照（含版本号）。匿名客户端/超管 = 全部；
// 单位管理员 = 仅绑定单位（登录态由可选认证识别；无 cookie 即匿名公开读）。
// 响应经 gzip 中间件压缩，减小传输体积。
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	// 可选认证：携带有效会话（管理端）则按角色过滤；否则按匿名公开读返回全部（客户端）。
	if u, err := s.authenticate(r); err == nil {
		ctx := context.WithValue(r.Context(), userKey, u)
		r = r.WithContext(ctx)
	}
	unitIDs, err := s.staffUnitIDs(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	contacts, err := s.store.ListContacts(r.Context(), unitIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取联系人失败")
		return
	}
	v, ts, ok := s.currentVersion(w, r)
	if !ok {
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
