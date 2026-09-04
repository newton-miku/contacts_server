package api

import (
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
