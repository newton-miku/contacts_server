package api

import (
	"fmt"
	"net/http"
	"strconv"

	"phonebook-server/internal/store"
)

// handleQueryLogs 分页查询审计日志（super），支持按 分类/用户名/时间 筛选。
func (s *Server) handleQueryLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	lq := store.LogQuery{
		Category: q.Get("category"),
		Username: q.Get("user"),
		From:     q.Get("from"),
		To:       q.Get("to"),
		Limit:    pageSize,
		Offset:   (page - 1) * pageSize,
	}
	total, items, err := s.store.QueryLogs(r.Context(), lq)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取日志失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
		"items":    items,
	})
}

// handleCleanupLogs 立即清理早于保留期的审计日志（super/root）。
func (s *Server) handleCleanupLogs(w http.ResponseWriter, r *http.Request) {
	days, err := s.store.LogRetentionDays(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取保留天数失败")
		return
	}
	var in struct {
		RetentionDays *int `json:"retentionDays"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
		if in.RetentionDays != nil {
			days = *in.RetentionDays
		}
	}
	if days <= 0 {
		writeErr(w, http.StatusBadRequest, "保留天数为 0（已关闭自动清理），请先设置保留天数再清理")
		return
	}
	n, err := s.store.CleanupLogs(r.Context(), days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "清理失败")
		return
	}
	if n > 0 {
		s.logAudit(r, store.LogCategoryAccount, "cleanup_logs",
			fmt.Sprintf("清理审计日志 %d 条（保留 %d 天）", n, days))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n, "retentionDays": days})
}
