//go:build !windows

package main

import "log"

// 非 Windows 平台暂不弹系统通知，只记日志。
func notify(title, body string) { log.Printf("notify: %s: %s", title, body) }
