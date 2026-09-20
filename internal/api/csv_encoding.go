package api

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 上传 CSV 的编码识别与解码。
//
// 国内用户用 Excel/WPS 另存 CSV 时，中文 Windows 默认写出 GB2312/GBK（不是 UTF-8），
// 直接按 UTF-8 读会得到乱码、表头匹配失败、整批导入报错。这里统一先解码成 UTF-8，
// 再交给 encoding/csv 解析，用户不必手动改编码。支持的编码：
//   - UTF-8（带/不带 BOM）——本系统导出的 CSV、「CSV UTF-8」另存；
//   - UTF-16 LE/BE（带 BOM）——Excel「Unicode 文本」；
//   - GB2312 / GBK / GB18030——按 GB18030（GB2312/GBK 的超集）解码，一套表全覆盖。
//
// 判定顺序：BOM 优先 → 出现 NUL 字节则按无 BOM 的 UTF-16 处理（正常文本 CSV 不会有 NUL）→
// 通过 UTF-8 校验（纯 ASCII 与正常 UTF-8 都命中）→ 否则按 GB18030 解码；
// 仍解不出（非法字节）则明确报错，不静默导入乱码。
const (
	encUTF8    = "utf-8"
	encUTF8BOM = "utf-8-bom"
	encUTF16LE = "utf-16le"
	encUTF16BE = "utf-16be"
	encGB18030 = "gb18030" // 覆盖 GB2312 / GBK / GB18030
)

// errUnknownEncoding 文件既不是 UTF-8/UTF-16，也不是合法的 GB2312/GBK/GB18030。
var errUnknownEncoding = errors.New(
	"无法识别文件编码（支持 UTF-8 / GB2312 / GBK / GB18030 / UTF-16）：请在 Excel/WPS 里另存为「CSV UTF-8」，或确认文件没有被改坏")

// decodeCSV 把上传的文件字节解码成 UTF-8 文本，并返回识别到的编码名（用于界面提示与审计日志）。
func decodeCSV(raw []byte) (string, string, error) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(raw[3:]), encUTF8BOM, nil
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return decodeUTF16(raw[2:], binary.LittleEndian), encUTF16LE, nil
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return decodeUTF16(raw[2:], binary.BigEndian), encUTF16BE, nil
	}
	// 无 BOM 的 UTF-16：正常文本 CSV（UTF-8/GBK）绝不会出现 NUL 字节，
	// 而 UTF-16 的 ASCII 部分每隔一字节就有一个 NUL。按 NUL 的位置判断字节序。
	if bytes.IndexByte(raw, 0) >= 0 {
		if nulsAtOdd(raw) {
			return decodeUTF16(raw, binary.LittleEndian), encUTF16LE, nil
		}
		return decodeUTF16(raw, binary.BigEndian), encUTF16BE, nil
	}
	if utf8.Valid(raw) {
		return string(raw), encUTF8, nil
	}
	// 非 UTF-8：按 GB18030 解码（GB2312/GBK 均是其子集）。
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
		// 解不出或出现替换符（U+FFFD）→ 不是合法的国标编码，宁可报错也不导入乱码。
		return "", "", errUnknownEncoding
	}
	return string(out), encGB18030, nil
}

// nulsAtOdd 判断 NUL 字节是否主要落在奇数下标（UTF-16LE 的 ASCII 字符形态：61 00 2C 00 …）。
func nulsAtOdd(raw []byte) bool {
	odd, even := 0, 0
	for i, b := range raw {
		if b != 0 {
			continue
		}
		if i%2 == 1 {
			odd++
		} else {
			even++
		}
	}
	return odd >= even
}

// decodeUTF16 把已去掉 BOM 的 UTF-16 字节按给定字节序解码成 UTF-8 文本。
func decodeUTF16(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, order.Uint16(b[i:i+2]))
	}
	return string(utf16.Decode(u))
}
