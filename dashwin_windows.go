package main

import (
	"log"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// Windows 原生仪表盘窗口：用系统自带的 WebView2 显示 dashboard.html，
// 页面仍通过本机 HTTP 服务（dashboard.go）取数据。
// 外观是 Win11 风格的弹出面板：无系统标题栏（标题和关闭按钮在页面里）、DWM 圆角阴影，
// 停靠在任务栏右下角；点到别处自动隐藏，页面上的图钉可固定不隐藏。
// 窗口只创建一次，关闭只隐藏，再次打开直接显示，避免重复初始化 WebView2。
// WebView2 运行时缺失或创建失败时返回 false，调用方回退到浏览器。

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procSystemParametersInfoW    = user32.NewProc("SystemParametersInfoW")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procSetThreadDpiAwarenessCtx = user32.NewProc("SetThreadDpiAwarenessContext")

	dwmapi                           = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

const (
	swHide           = 0
	swShow           = 5
	wmClose          = 0x0010
	wmSetIcon        = 0x0080
	gwlpWndProc      = ^uintptr(3)  // -4
	gwlExStyle       = ^uintptr(19) // -20
	gwlStyle         = ^uintptr(15) // -16
	wsPopup          = 0x80000000
	wsClipChildren   = 0x02000000
	wsClipSiblings   = 0x04000000
	wmActivate       = 0x0006
	swpFrameChanged  = 0x0020
	swpNoMove        = 0x0002
	swpNoSize        = 0x0001
	dwmCornerPref    = 33 // DWMWA_WINDOW_CORNER_PREFERENCE（Win11）
	dwmCornerRound   = 2
	wsExToolWindow   = 0x00000080
	wsExAppWindow    = 0x00040000
	spiGetWorkArea   = 0x0030
	swpNoZOrder      = 0x0004
	dpiPerMonitorV2  = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	dashWidth        = 580         // 逻辑像素（96 DPI 下）
	dashCardHeight   = 128         // 首次显示前的估算；页面加载后按实际内容高度调整
	dashChromeHeight = 120
	dashMargin       = 12
)

type nativeDash struct {
	mu      sync.Mutex
	started bool
	failed  bool
	ready   chan struct{}
	w       webview2.WebView
	hwnd    uintptr
	oldProc uintptr
	// pinned 时失去焦点不自动隐藏；autoHidAt 用来识别"点托盘图标关闭"的那次失焦。
	pinned    bool
	autoHidAt time.Time
	contentH  int // 页面报告的内容高度（CSS 像素）
}

var dashWin nativeDash

// 子类化窗口过程：WM_CLOSE 改为隐藏；失去焦点时（未固定）自动隐藏，其余消息交回原过程。
var dashWndProc = syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
	switch {
	case msg == wmClose:
		procShowWindow.Call(hwnd, swHide)
		return 0
	case msg == wmActivate && wp&0xFFFF == 0 && !dashWin.pinned:
		if v, _, _ := procIsWindowVisible.Call(hwnd); v != 0 {
			procShowWindow.Call(hwnd, swHide)
			dashWin.autoHidAt = time.Now()
		}
	}
	r, _, _ := procCallWindowProcW.Call(dashWin.oldProc, hwnd, msg, wp, lp)
	return r
})

// toggleNativeDashboard 显示/隐藏原生窗口。accounts 用来估算窗口高度。
// 返回 false 表示 WebView2 不可用。
func toggleNativeDashboard(url, dataDir string, accounts int) bool {
	d := &dashWin
	d.mu.Lock()
	if d.failed {
		d.mu.Unlock()
		return false
	}
	if !d.started {
		d.started = true
		d.ready = make(chan struct{})
		go d.run(url, dataDir)
	}
	d.mu.Unlock()
	<-d.ready
	if d.failed {
		return false
	}
	d.w.Dispatch(func() {
		if v, _, _ := procIsWindowVisible.Call(d.hwnd); v != 0 {
			procShowWindow.Call(d.hwnd, swHide)
			return
		}
		// 点托盘图标时面板先因失焦自动隐藏，紧接着这次点击不应再把它打开。
		if time.Since(d.autoHidAt) < 400*time.Millisecond {
			return
		}
		d.place(accounts)
		procShowWindow.Call(d.hwnd, swShow)
		procSetForegroundWindow.Call(d.hwnd)
		d.w.Eval("typeof load === 'function' && load()") // 显示时立刻刷新一次数据
	})
	return true
}

// run 在独占的 OS 线程上创建窗口并跑消息循环；WebView2 要求创建与消息处理在同一线程。
func (d *nativeDash) run(url, dataDir string) {
	runtime.LockOSThread()
	// 只让这个线程按显示器 DPI 渲染，不影响托盘菜单。Win10 1607 以下没有此 API，忽略即可。
	if procSetThreadDpiAwarenessCtx.Find() == nil {
		procSetThreadDpiAwarenessCtx.Call(dpiPerMonitorV2)
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:      filepath.Join(dataDir, "webview2"),
		AutoFocus:     true,
		WindowOptions: webview2.WindowOptions{Title: "Claude Account Switcher", Width: dashWidth, Height: 600},
	})
	if w == nil {
		log.Print("dashboard window: WebView2 unavailable, falling back to the browser")
		d.failed = true
		close(d.ready)
		return
	}
	d.w = w
	d.hwnd = uintptr(w.Window())
	// 先隐藏，等 toggle 摆好位置再显示，避免闪一下默认位置。
	procShowWindow.Call(d.hwnd, swHide)
	// 工具窗口样式：不在任务栏显示按钮，只从托盘图标打开。须在窗口隐藏时设置。
	ex, _, _ := procGetWindowLongPtrW.Call(d.hwnd, gwlExStyle)
	procSetWindowLongPtrW.Call(d.hwnd, gwlExStyle, ex&^wsExAppWindow|wsExToolWindow)
	// 去掉系统标题栏和边框，改为弹出面板；Win11 上由 DWM 画圆角和阴影。
	procSetWindowLongPtrW.Call(d.hwnd, gwlStyle, wsPopup|wsClipChildren|wsClipSiblings)
	corner := uint32(dwmCornerRound)
	procDwmSetWindowAttribute.Call(d.hwnd, dwmCornerPref, uintptr(unsafe.Pointer(&corner)), 4)
	margins := [4]int32{1, 1, 1, 1} // 让 DWM 给无边框窗口画阴影
	procDwmExtendFrameIntoClientArea.Call(d.hwnd, uintptr(unsafe.Pointer(&margins)))
	procSetWindowPos.Call(d.hwnd, 0, 0, 0, 0, 0, swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder)
	d.oldProc, _, _ = procSetWindowLongPtrW.Call(d.hwnd, gwlpWndProc, dashWndProc)
	d.setIcon()
	// 页面调用的函数（WebView2 注入，在 UI 线程执行）：关闭、固定、按内容高度调整窗口。
	_ = w.Bind("hideDashboard", func() { procShowWindow.Call(d.hwnd, swHide) })
	_ = w.Bind("pinDashboard", func(on bool) { d.pinned = on })
	_ = w.Bind("fitDashboard", func(h int) {
		if h > 0 && h != d.contentH {
			d.contentH = h
			if v, _, _ := procIsWindowVisible.Call(d.hwnd); v != 0 {
				d.place(0)
			}
		}
	})
	w.Navigate(url)
	close(d.ready)
	w.Run()
}

// place 把窗口放到工作区右下角（托盘附近）。高度优先用页面报告的内容高度，
// 页面还没报告时按账户数估算；不超过工作区。
func (d *nativeDash) place(accounts int) {
	dpi, _, _ := procGetDpiForWindow.Call(d.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	scale := func(v int) int { return v * int(dpi) / 96 }
	var wa struct{ Left, Top, Right, Bottom int32 }
	procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	margin := scale(dashMargin)
	width := scale(dashWidth)
	height := scale(dashChromeHeight + dashCardHeight*max(accounts, 1))
	if d.contentH > 0 {
		height = scale(d.contentH)
	}
	if maxH := int(wa.Bottom-wa.Top) - 2*margin; height > maxH {
		height = maxH
	}
	x := int(wa.Right) - width - margin
	y := int(wa.Bottom) - height - margin
	procSetWindowPos.Call(d.hwnd, 0, uintptr(x), uintptr(y), uintptr(width), uintptr(height), swpNoZOrder)
}

// setIcon 用托盘同款圆环图标（灰色）作为窗口图标。
func (d *nativeDash) setIcon() {
	ico := trayIcon(-1, defaultThreshold)
	if len(ico) <= 22 {
		return
	}
	bits := ico[22:] // 跳过 ICONDIR + ICONDIRENTRY，剩下的是 BITMAPINFOHEADER 起的图像数据
	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&bits[0])), uintptr(len(bits)), 1, 0x00030000, 32, 32, 0)
	if h != 0 {
		procSendMessageW.Call(d.hwnd, wmSetIcon, 0, h)
		procSendMessageW.Call(d.hwnd, wmSetIcon, 1, h)
	}
}
