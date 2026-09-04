package api

import (
	"net/http"
	"strconv"

	"phonebook-server/internal/store"
)

// handleClearDirectory 清空目录数据（super only 的一次性运维动作）：删除全部联系人，
// 再删除未被任何单位管理员绑定的单位。供“清空旧数据后整本导入”场景使用。
func (s *Server) handleClearDirectory(w http.ResponseWriter, r *http.Request) {
	contacts, units, kept, err := s.store.ResetDirectoryData(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "清空目录数据失败")
		return
	}
	s.logAudit(r, store.LogCategoryContact, "clear-directory",
		"清空目录数据：删除联系人 "+strconv.Itoa(contacts)+"，删除单位 "+strconv.Itoa(units)+"，保留被绑定单位 "+strconv.Itoa(kept))
	writeJSON(w, http.StatusOK, map[string]any{
		"contactsDeleted": contacts,
		"unitsDeleted":    units,
		"unitsKept":       kept,
	})
}
