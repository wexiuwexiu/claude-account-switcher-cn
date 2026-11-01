package main

import (
	"errors"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 预热 token 的加密、剪贴板读取和隐藏控制台窗口（Windows）。

// protect 用 DPAPI 加密：只有当前 Windows 用户能解密，文件被拷走也无法使用。
func protect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

var (
	kernel32dll      = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClip     = user32.NewProc("OpenClipboard")
	procCloseClip    = user32.NewProc("CloseClipboard")
	procGetClipData  = user32.NewProc("GetClipboardData")
	procEmptyClip    = user32.NewProc("EmptyClipboard")
	procGlobalLock   = kernel32dll.NewProc("GlobalLock")
	procGlobalUnlock = kernel32dll.NewProc("GlobalUnlock")
)

const cfUnicodeText = 13

// readClipboard 读剪贴板里的文本。
func readClipboard() (string, error) {
	if r, _, err := procOpenClip.Call(0); r == 0 {
		return "", err
	}
	defer procCloseClip.Call()
	h, _, err := procGetClipData.Call(cfUnicodeText)
	if h == 0 {
		return "", err
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		return "", err
	}
	defer procGlobalUnlock.Call(h)
	// GlobalLock 返回的地址在 GlobalUnlock 前一直有效；经 *unsafe.Pointer 转换避免 vet 误报
	return windows.UTF16PtrToString((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&p)))), nil
}

// clearClipboard 清空剪贴板，token 不留在里面。
func clearClipboard() {
	if r, _, _ := procOpenClip.Call(0); r == 0 {
		return
	}
	procEmptyClip.Call()
	procCloseClip.Call()
}

// hideWindow 让托盘程序启动的 claude 不弹出控制台窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
