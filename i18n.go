package main

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// 界面语言：中文 / English，托盘菜单"语言 / Language"切换，存在 state.json 的 lang。
// 没设置过时按系统界面语言决定（systemLang，平台文件实现）。
//
// 做法：代码里保留原版英文文案，显示前用 tr() 查中文表 zh；英文模式原样返回。
// 这样合并官方更新时文案行不必改动，中文集中在本文件维护。
// 日志不翻译，便于排查和对照原版。仪表盘页面和续聊对话框各自带中英文对照，按 lang 字段选择。

const (
	langZH = "zh"
	langEN = "en"
)

var curLang atomic.Value // string

// lang 返回当前界面语言，默认英文（测试里也是英文）。
func lang() string {
	if l, ok := curLang.Load().(string); ok && l != "" {
		return l
	}
	return langEN
}

// setLang 设置当前界面语言；不认识的值按英文。
func setLang(l string) {
	if l != langZH {
		l = langEN
	}
	curLang.Store(l)
}

// tr 把英文原文翻成当前语言；中文表里没有的原样返回。
func tr(en string) string {
	if lang() == langZH {
		if z, ok := zh[en]; ok {
			return z
		}
	}
	return en
}

// trf 翻译格式串再填参数，中英文的参数顺序必须一致。
func trf(en string, args ...any) string {
	return fmt.Sprintf(tr(en), args...)
}

// trPrefix 翻译"英文前缀 + 动态内容"形式的文本（例如保存在 state.json 里的错误），
// 只翻译认识的前缀。
func trPrefix(s string, prefixes ...string) string {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return tr(p) + s[len(p):]
		}
	}
	return tr(s)
}

// zh 是中文表，键是代码里的英文原文。
var zh = map[string]string{
	// 托盘菜单
	"Dashboard…": "仪表盘…",
	"Show usage of every account as charts (left-click the tray icon also opens it)": "图形显示所有账户的用量和重置时间（左键单击托盘图标也可打开）",
	"Auto-switch when nearly full":                  "快用满时自动切换",
	"Switch to the account with the most room left": "当前账户达到阈值时，切到剩余额度最多的账户",
	"Auto-switch at":                                "自动切换阈值",
	"Auto-switch to use up expiring weekly":         "自动切换去用完快到期的周额度",
	"Switch to an account whose weekly limit resets within a day with half or more of it unused":       "某账户的周额度一天内重置、且还剩一半以上时切过去，免得作废",
	"Remind to /compact before switching":                                                              "切号前提醒 /compact",
	"When the 5-hour limit will run out soon, remind to compact large chats before switching accounts": "5 小时额度快用满、要切号前，提醒先压缩大对话",
	"Warm up 5-hour windows": "预热 5 小时窗口",
	"While you use Claude, keep the 5-hour window of the 2 most urgent accounts running with a tiny Haiku request (about 600 tokens)": "最近用过 Claude 时，用一条 Haiku 极简请求（约 600 token）让最紧迫的 2 个账户的 5 小时窗口一直在计时",
	"Add account…": "添加账户…",
	"Restart Claude on its sign-in screen, your current account is kept": "重启 Claude 到登录界面，当前账户会保留",
	"Import chats from all accounts":                                     "导入所有账户的对话",
	"Restart Claude so every chat shows up in this account":              "重启 Claude，让所有对话都出现在当前账户里",
	"Start at login":                        "开机启动",
	"Open data folder":                      "打开数据文件夹",
	"Quit":                                  "退出",
	"Switch the Claude app to this account": "把 Claude 切换到这个账户",
	"Open the release page; this modified build is not replaced": "打开发布页；本改版不会被官方版本覆盖",
	"Official version %s is out (click for release notes)":       "官方新版本 %s 已发布（点击查看）",
	"Open Claude and sign in":                                    "请打开 Claude 并登录",

	// 用量文字（菜单、-status）
	"usage unknown": "用量未知",
	"5h ":           "5时 ",
	"week ":         "本周 ",
	"web sign-in only; usage shows after Pro + one use of the Code tab": "仅网页登录，开通 Pro 并用一次 Code 后显示用量",
	"signed out, add it again": "登录已失效，请重新添加",

	// 状态提示
	"%s failed: %v":            "%s失败：%v",
	"Switch":                   "切换",
	"Add account":              "添加账户",
	"Import":                   "导入对话",
	"Importing chats":          "导入对话",
	"Auto switch":              "自动切换",
	"Could not start Claude: ": "无法启动 Claude：",
	"Switching to %s…":         "正在切换到 %s…",
	"Sign in with the other account in Claude": "请在 Claude 中登录另一个账户",
	"Importing chats…":                         "正在导入对话…",
	"Start at login: ":                         "开机启动：",
	"Dashboard: ":                              "仪表盘：",
	"the last account":                         "上一个账户",
	"Auto-switched at %s, %s was nearly full":  "%s 自动切换：%s 快用满了",
	"Auto-switched at %s to use up this account's week before it resets %s": "%s 自动切换：在 %s 重置前用完这个账户的周额度",

	// 用量查询错误（仪表盘）
	"rate limited, retrying after %s":            "被限流，%s 后重试",
	"sign-in expired, add it again":              "登录已过期，需要重新添加",
	"connection timed out (check proxy/network)": "连接超时（检查代理/网络）",

	// 预热错误（仪表盘）
	"the 5-hour window did not start after the warmup": "预热后 5 小时窗口没有开始计时",
	"warmup failed: ": "预热失败：",

	// 切号前压缩提醒（系统通知）
	"5-hour usage %.0f%%, full around %s": "5 小时额度 %.0f%%，预计 %s 用满",
	"Before switching, run /compact in %s. Continuing a chat on another account rewrites its whole context there.": "准备切号前，先在 %s 里运行 /compact。切号后续聊要把整段上下文在新账号里重写。",
	"Untitled chat": "未命名对话",
	"Got it":        "知道了",

	// 续聊对话框（系统自带的简易版；网页版在 resume.html 里）
	"Continue chats?": "继续这些对话？",
	"These chats were working when Claude closed for the switch. Claude continues the checked ones when it starts again.":  "切换时 Claude 关闭，这些对话正在进行。Claude 重新启动后会继续勾选的对话。",
	"These chats were working when Claude closed for the switch. Claude continues the selected ones when it starts again.": "切换时 Claude 关闭，这些对话正在进行。Claude 重新启动后会继续选中的对话。",
	"Continue":       "继续",
	"Continue (%d)":  "继续（%d）",
	"Don't continue": "不继续",
}
