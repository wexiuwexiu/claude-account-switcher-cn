//go:build !windows

package main

import "os/exec"

// hideWindow 只在 Windows 上需要（托盘程序启动控制台程序会弹窗）。
func hideWindow(*exec.Cmd) {}
