package api

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// GB2312/GBK 字节样本（中文 Windows 上 Excel/WPS 另存 CSV 的默认编码）：
//
//	姓名 = D0 D5 C3 FB    单位 = B5 A5 CE BB
//	张三 = D5 C5 C8 FD    测试单位 = B2 E2 CA D4 B5 A5 CE BB
var gb2312Sample = []byte{
	0xD0, 0xD5, 0xC3, 0xFB, ',', 0xB5, 0xA5, 0xCE, 0xBB, '\n', // 姓名,单位
	0xD5, 0xC5, 0xC8, 0xFD, ',', 0xB2, 0xE2, 0xCA, 0xD4, 0xB5, 0xA5, 0xCE, 0xBB, '\n', // 张三,测试单位
}

const decodedSample = "姓名,单位\n张三,测试单位\n"

func utf16Bytes(s string, order binary.ByteOrder, bom []byte) []byte {
	u := utf16.Encode([]rune(s))
	out := append([]byte{}, bom...)
	buf := make([]byte, 2)
	for _, r := range u {
		order.PutUint16(buf, r)
		out = append(out, buf...)
	}
	return out
}

// 编码识别：UTF-8 / UTF-8 BOM / GB2312(GBK) / UTF-16 都要能解码成同一份文本。
func TestDecodeCSVEncodings(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
		enc  string
	}{
		{"utf-8", []byte(decodedSample), decodedSample, encUTF8},
		{"utf-8 bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte(decodedSample)...), decodedSample, encUTF8BOM},
		{"gb2312", gb2312Sample, decodedSample, encGB18030},
		{"utf-16le", utf16Bytes(decodedSample, binary.LittleEndian, []byte{0xFF, 0xFE}), decodedSample, encUTF16LE},
		{"utf-16be", utf16Bytes(decodedSample, binary.BigEndian, []byte{0xFE, 0xFF}), decodedSample, encUTF16BE},
		{"utf-16le 无 BOM", utf16Bytes(decodedSample, binary.LittleEndian, nil), decodedSample, encUTF16LE},
		{"utf-16be 无 BOM", utf16Bytes(decodedSample, binary.BigEndian, nil), decodedSample, encUTF16BE},
		{"ascii", []byte("a,b\n1,2\n"), "a,b\n1,2\n", encUTF8},
	}
	for _, tc := range cases {
		got, enc, err := decodeCSV(tc.raw)
		if err != nil {
			t.Errorf("%s: 解码失败 %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: 文本不符\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
		if enc != tc.enc {
			t.Errorf("%s: 编码名 = %q, want %q", tc.name, enc, tc.enc)
		}
	}
}

// 非法字节不能静默导入成乱码：必须报「无法识别文件编码」。
// （注意 0x80 在 CP936/GBK 里是合法的欧元符号，所以不能用它当垃圾样本。）
func TestDecodeCSVRejectsGarbage(t *testing.T) {
	for _, raw := range [][]byte{
		{0xFF},                   // GB18030 里不是合法首字节
		{0x81},                   // 双字节序列只有首字节，截断
		{0xD0, 0xD5, 0xFF, 0xFF}, // 前两字节像 GBK，后面是垃圾
	} {
		if got, enc, err := decodeCSV(raw); err == nil {
			t.Errorf("垃圾字节 % X 应报错：got %q（编码 %s）", raw, got, enc)
		}
	}
}

// 中文 GB2312 载荷解出来必须是干净文本（不含替换符）。
func TestDecodeCSVGB2312HasNoReplacementChar(t *testing.T) {
	got, _, err := decodeCSV(gb2312Sample)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if bytes.ContainsRune([]byte(got), '\uFFFD') {
		t.Fatalf("解码结果含替换符，说明编码判断错误：%q", got)
	}
}
