package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"

	"phonebook-server/internal/auth"
	"phonebook-server/internal/store"
)

// newTestAPI 起一个用临时库的 Server（无静态资源），返回 handler / store / 可用的 Bearer 令牌。
func newTestAPI(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(filepath.Join(t.TempDir(), "api-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hash, err := auth.HashPassword("Test@123456")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u, err := st.CreateUser(ctx, "tester", hash, "测试管理员", store.RoleRoot, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	am := auth.New()
	token, err := am.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if err := st.CreateSession(ctx, u.ID, auth.TokenHash(token), am.ExpiresAt()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return NewServer(st, nil).Handler(), st, token
}

// postCSV 以 multipart 上传一份 CSV，返回状态码与响应 JSON。
func postCSV(t *testing.T, h http.Handler, token, filename string, data []byte) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/contacts/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func num(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("响应缺少字段 %s：%v", key, m)
	}
	return int(v)
}

// GB2312（Excel/WPS 中文 Windows 另存 CSV 的默认编码）导入 + 姓名去空格防重复。
func TestImportGB2312WithSpacedNamesDedups(t *testing.T) {
	ctx := context.Background()
	h, st, token := newTestAPI(t)
	if _, err := st.CreateUnit(ctx, "测试单位", 0, true); err != nil {
		t.Fatalf("create unit: %v", err)
	}

	// 中文 Windows 的 Excel 另存：GB2312/GBK 字节，姓名里带半角/全角空格。
	csvText := "姓名,手机号,单位,职务\n" +
		"张 三,13800003001,测试单位,科员\n" +
		"李\u3000四,13800003002,测试单位,科员\n"
	gb, err := simplifiedchinese.GBK.NewEncoder().String(csvText)
	if err != nil {
		t.Fatalf("encode gb2312: %v", err)
	}
	// 先钉住样本确实是 GB2312 字节（“姓名” = D0 D5 C3 FB），避免编码库行为变化让测试失去意义。
	if !bytes.HasPrefix([]byte(gb), []byte{0xD0, 0xD5, 0xC3, 0xFB}) {
		t.Fatalf("样本不是预期的 GB2312 字节：% X", []byte(gb)[:8])
	}

	code, out := postCSV(t, h, token, "通讯录.csv", []byte(gb))
	if code != http.StatusOK {
		t.Fatalf("GB2312 导入应成功：HTTP %d, %v", code, out)
	}
	if out["encoding"] != encGB18030 {
		t.Fatalf("应识别为 GB2312/GBK：got %v", out["encoding"])
	}
	if got := num(t, out, "created"); got != 2 {
		t.Fatalf("应新增 2 条：got %d (%v)", got, out)
	}

	contacts, err := st.ListContacts(ctx, nil)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	names := make([]string, 0, len(contacts))
	for _, c := range contacts {
		names = append(names, c.DisplayName)
	}
	if strings.Join(names, ",") != "张三,李四" {
		t.Fatalf("姓名应去掉全部空格后入库：got %v", names)
	}

	// 再导入一份「不带空格」的同一姓名（UTF-8，无 BOM）：必须覆盖更新，不能建出重复记录。
	csv2 := "姓名,手机号,单位,职务\n张三,13800003009,测试单位,科长\n"
	code, out = postCSV(t, h, token, "again.csv", []byte(csv2))
	if code != http.StatusOK {
		t.Fatalf("第二次导入应成功：HTTP %d, %v", code, out)
	}
	if got := num(t, out, "created"); got != 0 {
		t.Fatalf("带空格的姓名去空格后应命中已有记录（新增 0）：got %d (%v)", got, out)
	}
	if got := num(t, out, "updated"); got != 1 {
		t.Fatalf("应覆盖更新 1 条：got %d (%v)", got, out)
	}

	contacts, err = st.ListContacts(ctx, nil)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("不应产生重复记录：共 %d 条", len(contacts))
	}
	for _, c := range contacts {
		if c.DisplayName == "张三" && c.PhoneNumber != "13800003009" {
			t.Fatalf("覆盖更新应写入新号码：got %s", c.PhoneNumber)
		}
	}
}

// UTF-16LE（Excel「Unicode 文本」）与 UTF-8 BOM 都要能导入。
func TestImportUTF16AndUTF8BOM(t *testing.T) {
	ctx := context.Background()
	h, st, token := newTestAPI(t)
	if _, err := st.CreateUnit(ctx, "测试单位", 0, true); err != nil {
		t.Fatalf("create unit: %v", err)
	}

	csvText := "姓名,手机号,单位\n王五,13800004001,测试单位\n"
	// UTF-16LE + BOM
	u := utf16.Encode([]rune(csvText))
	raw := []byte{0xFF, 0xFE}
	for _, r := range u {
		raw = append(raw, byte(r), byte(r>>8))
	}
	code, out := postCSV(t, h, token, "utf16.csv", raw)
	if code != http.StatusOK {
		t.Fatalf("UTF-16 导入应成功：HTTP %d, %v", code, out)
	}
	if out["encoding"] != encUTF16LE {
		t.Fatalf("应识别为 utf-16le：got %v", out["encoding"])
	}

	// UTF-8 + BOM（本系统导出的 CSV 形态）
	code, out = postCSV(t, h, token, "bom.csv", append([]byte{0xEF, 0xBB, 0xBF}, []byte("姓名,手机号,单位\n赵六,13800004002,测试单位\n")...))
	if code != http.StatusOK {
		t.Fatalf("UTF-8 BOM 导入应成功：HTTP %d, %v", code, out)
	}
	if out["encoding"] != encUTF8BOM {
		t.Fatalf("应识别为 utf-8-bom：got %v", out["encoding"])
	}

	contacts, err := st.ListContacts(ctx, nil)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("应导入 2 条：got %d", len(contacts))
	}
}

// 无法识别的编码要明确报错，不能静默导入乱码。
func TestImportRejectsUnknownEncoding(t *testing.T) {
	h, st, token := newTestAPI(t)
	if _, err := st.CreateUnit(context.Background(), "测试单位", 0, true); err != nil {
		t.Fatalf("create unit: %v", err)
	}
	code, out := postCSV(t, h, token, "broken.csv", []byte{0xFF, 0x81, 0xFE})
	if code != http.StatusBadRequest {
		t.Fatalf("垃圾字节应 400：HTTP %d, %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "无法识别文件编码") {
		t.Fatalf("应提示编码无法识别：%v", out)
	}
}

// 空模板：只有表头、带 BOM、文件名可读（供管理页「下载导入模板」）。
func TestContactsTemplateIsEmpty(t *testing.T) {
	h, _, token := newTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/contacts/template", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("模板下载应 200：got %d", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "contacts-template.csv") {
		t.Fatalf("Content-Disposition 应带模板文件名：%q", cd)
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("模板应带 UTF-8 BOM（Excel 打开不乱码）")
	}
	text := strings.TrimRight(string(body[3:]), "\r\n")
	want := strings.Join(csvHeader, ",")
	if text != want {
		t.Fatalf("模板应只有一行表头：\ngot  %q\nwant %q", text, want)
	}
}
