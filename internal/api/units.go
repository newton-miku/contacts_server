package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"phonebook-server/internal/store"
)

// unitPayload 单位列表项：带联系人计数（在调用者数据范围内统计）。
// IsPublic（公开单位）仅 super 可编辑；unit 列表里为其绑定单位，字段恒为 false。
type unitPayload struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sortOrder"`
	IsPublic  bool   `json:"isPublic"`
	Count     int    `json:"count"`
}

// handleListUnits 返回单位列表。super → 全部；unit → 仅其绑定单位。
// count 为各单位在当前数据范围内的联系人数量（供概览页分布展示）。
func (s *Server) handleListUnits(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var (
		units   []store.Unit
		unitIDs []int64
		err     error
	)
	if store.IsSuperRole(u.Role) {
		units, err = s.store.ListUnits(r.Context())
		unitIDs = nil // 统计全部
	} else {
		units, err = s.store.UserUnits(r.Context(), u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取单位失败")
			return
		}
		unitIDs = make([]int64, 0, len(units))
		for _, un := range units {
			unitIDs = append(unitIDs, un.ID)
		}
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取单位失败")
		return
	}

	counts, err := s.store.CountContactsByUnit(r.Context(), unitIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "统计单位人数失败")
		return
	}
	out := make([]unitPayload, 0, len(units))
	for _, un := range units {
		out = append(out, unitPayload{ID: un.ID, Name: un.Name, SortOrder: un.SortOrder, IsPublic: un.IsPublic, Count: counts[un.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateUnit 新建单位（super）。
func (s *Server) handleCreateUnit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		SortOrder int    `json:"sortOrder"`
		IsPublic  bool   `json:"isPublic"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if err := validateUnitName(in.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.store.CreateUnit(r.Context(), in.Name, in.SortOrder, in.IsPublic)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateName) {
			writeErr(w, http.StatusBadRequest, "单位名称已存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "新建单位失败")
		return
	}
	s.logAudit(r, store.LogCategoryUnit, "create", "新建单位 "+u.Name)
	writeJSON(w, http.StatusCreated, u)
}

// handleUpdateUnit 修改单位名称/排序/公开标记（super）。
func (s *Server) handleUpdateUnit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的单位 id")
		return
	}
	var in struct {
		Name      string `json:"name"`
		SortOrder int    `json:"sortOrder"`
		IsPublic  bool   `json:"isPublic"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if err := validateUnitName(in.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.store.UpdateUnit(r.Context(), id, in.Name, in.SortOrder, in.IsPublic)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "单位不存在")
			return
		}
		if errors.Is(err, store.ErrDuplicateName) {
			writeErr(w, http.StatusBadRequest, "单位名称已存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "修改单位失败")
		return
	}
	s.logAudit(r, store.LogCategoryUnit, "update", "修改单位 #"+strconv.FormatInt(id, 10)+" 为 "+u.Name)
	writeJSON(w, http.StatusOK, u)
}

// handleDeleteUnit 删除单位（super）。其下仍有联系人时返回 400。
func (s *Server) handleDeleteUnit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效的单位 id")
		return
	}
	existing, err := s.store.UnitByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "单位不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "读取单位失败")
		return
	}
	if err := s.store.DeleteUnit(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrUnitInUse) {
			writeErr(w, http.StatusBadRequest, "该单位下仍有联系人，请先移走或删除其联系人")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "单位不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除单位失败")
		return
	}
	s.logAudit(r, store.LogCategoryUnit, "delete", "删除单位 "+existing.Name)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// handleBulkPublic 批量设单位公开/取消公开（super）。单位管理页支持多选（可全选）后一次“保存”。
// 配合“导入的单位默认全公开”：整本导入后所有单位已公开，如要调整可全选批量取消/改回。
func (s *Server) handleBulkPublic(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UnitIDs []int64 `json:"unitIds"`
		Public  bool    `json:"public"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if len(in.UnitIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "请至少选择一个单位")
		return
	}
	seen := make(map[int64]bool, len(in.UnitIDs))
	ids := make([]int64, 0, len(in.UnitIDs))
	for _, id := range in.UnitIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ok, err := s.store.UnitExists(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "校验单位失败")
			return
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "存在无效的单位 id")
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "请至少选择一个有效的单位")
		return
	}
	n, err := s.store.SetUnitsPublic(r.Context(), ids, in.Public)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "批量设置失败")
		return
	}
	flag := "公开"
	if !in.Public {
		flag = "取消公开"
	}
	s.logAudit(r, store.LogCategoryUnit, "bulk_public",
		fmt.Sprintf("批量%s %d 个单位", flag, n))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": n, "public": in.Public})
}

// validateUnitName 单位名校验（create/update 共用）。
func validateUnitName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("单位名称不能为空")
	}
	return nil
}
