package api

import (
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"phonebook-server/internal/store"
)

// CSV 列顺序固定，导入导出共用一套表头；数据为空时导出即得到一份可填写的空模板。
// v7：号码按类型分列——手机号=主叫（优先）；手机2=同一人第二个手机号；座机=另有座机时；
// 传真=办公室传真。
var csvHeader = []string{"姓名", "手机号", "手机2", "座机", "传真", "单位", "职务", "邮箱"}

func writeCSVRow(w *csv.Writer, c store.Contact) {
	w.Write([]string{
		c.DisplayName,
		c.PhoneNumber,
		deref(c.Mobile2),
		deref(c.Landline),
		deref(c.Fax),
		c.Unit,
		deref(c.JobTitle),
		deref(c.Email),
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// allowedUnits 返回当前登录者可用于导入的单位集合（与数据修改范围一致）：
// super → 全部已建单位；unit → 仅其绑定单位。用于把 CSV 单位名映射成 unit_id。
func (s *Server) allowedUnits(r *http.Request) ([]store.Unit, error) {
	u := userFrom(r)
	if store.IsSuperRole(u.Role) {
		return s.store.ListUnits(r.Context())
	}
	return s.store.UserUnits(r.Context(), u.ID)
}

// handleExportContacts 导出联系人 CSV。范围与登录者可见数据一致：super 导出全部；
// unit 仅导出其绑定单位。空库导出空表（仅表头），可直接当模板填写。
func (s *Server) handleExportContacts(w http.ResponseWriter, r *http.Request) {
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

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.csv"`)
	// UTF-8 BOM：让 Excel 正确识别中文。
	w.Write([]byte{0xEF, 0xBB, 0xBF})

	wr := csv.NewWriter(w)
	wr.Write(csvHeader)
	for _, c := range contacts {
		writeCSVRow(wr, c)
	}
	wr.Flush()
}

// handleImportContacts 导入 CSV（super 或 unit）：以「单位+姓名」为关键字批量 upsert——
// 命中唯一则覆盖（含 手机/手机2/座机/传真），否则新增。整批一个事务，全部成功或全部失败。
// 号码按类型分列（手机号/手机2/座机/传真，均可留空，至少一个）：主叫号=手机优先、无则座机、
// 仍无则传真；短固话自动补 0915 区号（store 侧统一归一）。
// 单位列按名称匹配：super 可以一次性导入整本通讯录——CSV 里尚未建好的单位会自动新建；
// unit 只能导入其绑定单位，未预建或不在其范围内的单位整行报错中止。
func (s *Server) handleImportContacts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8MB 上限
		writeErr(w, http.StatusBadRequest, "上传内容无效（需 multipart 表单）")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少文件字段 file")
		return
	}
	defer file.Close()

	// 兼容 Excel 导出的 UTF-8 BOM。
	br := newBOMReader(file)
	rd := csv.NewReader(br)
	rd.FieldsPerRecord = -1 // 允许列数不一，按表头对齐
	rd.LazyQuotes = true
	rd.TrimLeadingSpace = true

	rows, err := rd.ReadAll()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CSV 解析失败："+err.Error())
		return
	}
	if len(rows) < 1 {
		writeErr(w, http.StatusBadRequest, "CSV 为空")
		return
	}

	colIdx, err := csvColumnIndex(rows[0])
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	u := userFrom(r)

	// 已建可用单位（导入范围 = 数据修改范围）：super → 全部；unit → 仅绑定单位。
	allowed, err := s.allowedUnits(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取单位列表失败")
		return
	}
	unitByName := make(map[string]int64, len(allowed)+8)
	for _, un := range allowed {
		unitByName[un.Name] = un.ID
	}

	// 第一遍：收集各行；登记 super 需要自动新建的单位名（按首现顺序、去重）。
	type rawRow struct {
		line                                                           int
		name, phone, mobile2, landline, fax, unitName, jobTitle, email string
	}
	raws := make([]rawRow, 0, len(rows)-1)
	var missingNames []string
	missingSeen := make(map[string]bool)
	for i, row := range rows[1:] {
		if isBlankRow(row) {
			continue
		}
		rr := rawRow{
			line:     i + 2,
			name:     cell(row, colIdx.name),
			phone:    cell(row, colIdx.phone),
			mobile2:  cell(row, colIdx.mobile2),
			landline: cell(row, colIdx.landline),
			fax:      cell(row, colIdx.fax),
			unitName: cell(row, colIdx.unit),
			jobTitle: cell(row, colIdx.jobTitle),
			email:    cell(row, colIdx.email),
		}
		if rr.unitName == "" {
			writeErr(w, http.StatusBadRequest, "第 "+strconv.Itoa(rr.line)+" 行：单位不能为空")
			return
		}
		raws = append(raws, rr)
		if _, ok := unitByName[rr.unitName]; !ok && !missingSeen[rr.unitName] {
			missingSeen[rr.unitName] = true
			missingNames = append(missingNames, rr.unitName)
		}
	}

	// 第二遍（仅 super）：把缺失单位建出来。回滚函数仅在后续任一步失败时删除本批新建的
	// 空单位（联系人导入整体在事务内，失败不会残留半截数据），避免留下空单位。
	var newUnits []store.Unit
	rollback := func() {}
	if store.IsSuperRole(u.Role) && len(missingNames) > 0 {
		resolved, created, err := s.store.EnsureUnits(r.Context(), missingNames)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "自动创建单位失败："+err.Error())
			return
		}
		for name, id := range resolved {
			unitByName[name] = id
		}
		newUnits = created
		rollback = func() {
			for _, un := range newUnits {
				if derr := s.store.DeleteUnit(r.Context(), un.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
					// 尽力清理；删不掉（理论：已被并发占用）也不影响主流程报错。
					s.logAudit(r, store.LogCategoryUnit, "delete",
						"导入中止，未能清理自动新建单位 "+un.Name)
				}
			}
		}
	}

	// 第三遍：逐行规范化（单位须已解析；super 已补齐缺失单位，unit 保持原范围校验）。
	parsed := make([]store.Contact, 0, len(raws))
	for _, rr := range raws {
		unitID, ok := unitByName[rr.unitName]
		if !ok {
			rollback()
			writeErr(w, http.StatusBadRequest, "第 "+strconv.Itoa(rr.line)+" 行：单位“"+rr.unitName+"”不存在或不在你的管理范围，请先在管理端建好单位再导入")
			return
		}
		c := store.Contact{
			DisplayName: rr.name,
			PhoneNumber: rr.phone,
			UnitID:      unitID,
		}
		if colIdx.jobTitle >= 0 {
			c.JobTitle = strPtr(rr.jobTitle)
		}
		if colIdx.email >= 0 {
			c.Email = strPtr(rr.email)
		}
		if colIdx.mobile2 >= 0 {
			c.Mobile2 = strPtr(rr.mobile2)
		}
		if colIdx.landline >= 0 {
			c.Landline = strPtr(rr.landline)
		}
		if colIdx.fax >= 0 {
			c.Fax = strPtr(rr.fax)
		}
		normalized, err := store.NormalizeContact(c)
		if err != nil {
			rollback()
			writeErr(w, http.StatusBadRequest, "第 "+strconv.Itoa(rr.line)+" 行："+err.Error())
			return
		}
		parsed = append(parsed, normalized)
	}

	unitIDs, err := s.staffUnitIDs(r)
	if err != nil {
		rollback()
		writeErr(w, http.StatusInternalServerError, "读取数据范围失败")
		return
	}
	created, updated, err := s.store.ImportContacts(r.Context(), unitIDs, parsed)
	if err != nil {
		rollback()
		writeErr(w, http.StatusInternalServerError, "导入失败："+err.Error())
		return
	}

	for _, un := range newUnits {
		s.logAudit(r, store.LogCategoryUnit, "create", "CSV 导入自动新建单位 "+un.Name)
	}
	detail := "CSV 导入 " + strconv.Itoa(len(parsed)) + " 行（新增 " + strconv.Itoa(created) +
		"，更新 " + strconv.Itoa(updated) + "）"
	if len(newUnits) > 0 {
		detail += "，自动新建单位 " + strconv.Itoa(len(newUnits)) + " 个"
	}
	s.logAudit(r, store.LogCategoryContact, "import", detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"imported":     len(parsed),
		"created":      created,
		"updated":      updated,
		"unitsCreated": len(newUnits),
	})
}

type csvCols struct {
	name, phone, mobile2, landline, fax, unit, jobTitle, email int
}

// csvColumnIndex 按表头定位各列；姓名必填，号码列（手机/手机2/座机/传真）至少有一个，
// 其余列缺失时按不存在处理。兼容老模板：没有 手机2/座机/传真 等列的 CSV 仍可导入。
func csvColumnIndex(header []string) (csvCols, error) {
	idx := csvCols{name: -1, phone: -1, mobile2: -1, landline: -1, fax: -1, unit: -1, jobTitle: -1, email: -1}
	for i, h := range header {
		switch normalizeHeader(h) {
		case "姓名":
			idx.name = i
		case "手机号", "手机":
			idx.phone = i
		case "手机2", "手机2号", "第二手机", "备用手机":
			idx.mobile2 = i
		case "座机", "固话", "电话":
			idx.landline = i
		case "传真":
			idx.fax = i
		case "单位", "部门":
			idx.unit = i
		case "职务":
			idx.jobTitle = i
		case "邮箱", "email", "Email":
			idx.email = i
		}
	}
	if idx.name < 0 {
		return idx, errors.New("CSV 缺少必需列：姓名（表头需为：姓名,手机号,手机2,座机,传真,单位,职务,邮箱）")
	}
	if idx.phone < 0 && idx.landline < 0 && idx.fax < 0 && idx.mobile2 < 0 {
		return idx, errors.New("CSV 缺少号码列：手机号/手机2/座机/传真至少需有一列")
	}
	return idx, nil
}

func normalizeHeader(h string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(h), "\uFEFF"))
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func isBlankRow(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// bomReader 剥掉流开头的 UTF-8 BOM。
type bomReader struct {
	r       io.Reader
	skipped bool
}

func newBOMReader(r io.Reader) *bomReader { return &bomReader{r: r} }

func (b *bomReader) Read(p []byte) (int, error) {
	if !b.skipped {
		b.skipped = true
		var bom [3]byte
		n, err := io.ReadFull(b.r, bom[:])
		if err != nil || string(bom[:n]) != "\xEF\xBB\xBF" {
			// 不是 BOM：把已读字节还回去。
			return io.MultiReader(strings.NewReader(string(bom[:n])), b.r).Read(p)
		}
	}
	return b.r.Read(p)
}
