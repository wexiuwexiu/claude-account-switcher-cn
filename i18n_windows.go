package main

import (
	"strings"

	"golang.org/x/sys/windows"
)

// systemLang 按 Windows 界面语言选默认语言：中文系统用中文，其它用英文。
func systemLang() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err == nil && len(langs) > 0 && strings.HasPrefix(strings.ToLower(langs[0]), "zh") {
		return langZH
	}
	return langEN
}
