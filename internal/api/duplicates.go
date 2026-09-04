package api

import (
	"net/http"

	"phonebook-server/internal/store"
)

// handleDuplicates 号码重复扫描（super 全库；unit 仅其绑定单位）。
// 见 store.FindDuplicateNumbers：把 手机/手机2/座机/传真 按纯数字归类，列出 ≥2 处使用的号码。
func (s *Server) handleDuplicates(w http.ResponseWriter, r *http.Request) {
	unitIDs, err := s.staffUnitIDs(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取数据范围失败")
		return
	}
	groups, err := s.store.FindDuplicateNumbers(r.Context(), unitIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "号码查重失败")
		return
	}
	if groups == nil {
		groups = []store.DupGroup{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}
