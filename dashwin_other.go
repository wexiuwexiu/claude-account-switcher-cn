//go:build !windows

package main

// 非 Windows 平台没有原生窗口，仪表盘在浏览器中打开。
func toggleNativeDashboard(url, dataDir string, accounts int) bool { return false }
