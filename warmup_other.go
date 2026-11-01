//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

// 预热目前只支持 Windows：token 的加密和剪贴板读取没有其它平台的实现。
var errWarmUnsupported = errors.New("warmup is only supported on Windows")

func protect([]byte) ([]byte, error)   { return nil, errWarmUnsupported }
func unprotect([]byte) ([]byte, error) { return nil, errWarmUnsupported }
func readClipboard() (string, error)   { return "", errWarmUnsupported }
func clearClipboard()                  {}
func hideWindow(*exec.Cmd)             {}
