package notify

import (
	"encoding/base64"
	"strings"
)

// base64Std 对正文做标准 Base64 编码，并去掉换行（折行由调用方控制）。
func base64Std(s string) string {
	return strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(s)), "\n")
}

// encodeSubject 把主题编码成 RFC 2047 的 B 形式。
// 纯 ASCII 主题保持原样，避免无意义的编码。
func encodeSubject(s string) string {
	if isASCII(s) {
		return s
	}
	return "=?UTF-8?B?" + base64Std(s) + "?="
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
