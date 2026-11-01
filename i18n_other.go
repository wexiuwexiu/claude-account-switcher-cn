//go:build !windows

package main

import (
	"os"
	"strings"
)

// systemLang 按 LANG / LC_ALL 选默认语言：zh 开头用中文，其它用英文。
func systemLang() string {
	for _, k := range []string{"LC_ALL", "LANG"} {
		if v := os.Getenv(k); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "zh") {
				return langZH
			}
			return langEN
		}
	}
	return langEN
}
