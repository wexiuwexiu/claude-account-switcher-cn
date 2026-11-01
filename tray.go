package main

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
)

const maxAccounts = 8

var thresholds = []float64{90, 94, 96, 98}

type tray struct {
	s          *switcher
	status     *systray.MenuItem
	update     *systray.MenuItem
	accounts   [maxAccounts]*systray.MenuItem
	auto       *systray.MenuItem
	spend      *systray.MenuItem
	compact    *systray.MenuItem
	warm       *systray.MenuItem
	thresholds []*systray.MenuItem
	add        *systray.MenuItem
	importNow  *systray.MenuItem
	autostart  *systray.MenuItem
	lastIcon   string
	updates    chan struct{}
}

func (t *tray) onReady() {
	systray.SetTooltip("Claude Account Switcher")
	t.setIcon(-1, defaultThreshold)

	t.status = systray.AddMenuItem("", "")
	t.status.Disable()
	t.status.Hide()
	// 改版不自动安装官方更新（会覆盖本地修改），点击只打开发布页。
	t.update = systray.AddMenuItem("", "Open the release page; this modified build is not replaced")
	t.update.Hide()
	for i := range t.accounts {
		t.accounts[i] = systray.AddMenuItemCheckbox("", "Switch the Claude app to this account", false)
		t.accounts[i].Hide()
	}
	systray.AddSeparator()
	// 图形仪表盘：所有账户的 5h / 周用量进度条与重置倒计时，见 dashboard.go
	dash := systray.AddMenuItem("Dashboard…", "Show usage of every account as charts in the browser")
	t.auto = systray.AddMenuItemCheckbox("Auto-switch when nearly full", "Switch to the account with the most room left", false)
	limit := systray.AddMenuItem("Auto-switch at", "")
	for _, v := range thresholds {
		t.thresholds = append(t.thresholds, limit.AddSubMenuItemCheckbox(fmt.Sprintf("%.0f%%", v), "", false))
	}
	t.spend = systray.AddMenuItemCheckbox("Auto-switch to use up expiring weekly", "Switch to an account whose weekly limit resets within a day with half or more of it unused", false)
	t.compact = systray.AddMenuItemCheckbox("Remind to /compact before switching", "When the 5-hour limit will run out soon, remind to compact large chats before switching accounts", false)
	// 5 小时窗口预热，见 warmup.go；用各账户已保存的登录，不需要额外设置
	t.warm = systray.AddMenuItemCheckbox("Warm up 5-hour windows", "While you use Claude, keep the 5-hour window of the 2 most urgent accounts running with a tiny Haiku request (about 600 tokens)", false)
	t.add = systray.AddMenuItem("Add account…", "Restart Claude on its sign-in screen, your current account is kept")
	t.importNow = systray.AddMenuItem("Import chats from all accounts", "Restart Claude so every chat shows up in this account")
	systray.AddSeparator()
	// Rewriting an enabled entry keeps its path current and adds the Task
	// Manager record that entries from before v0.1.2 are missing.
	if autostartEnabled() {
		if err := setAutostart(true); err != nil {
			log.Printf("refresh start at login: %v", err)
		}
	}
	t.autostart = systray.AddMenuItemCheckbox("Start at login", "", autostartEnabled())
	folder := systray.AddMenuItem("Open data folder", "")
	quit := systray.AddMenuItem("Quit", "")

	for i, item := range t.accounts {
		go func() {
			for range item.ClickedCh {
				snap := t.s.snapshot()
				if i < len(snap.Accounts) && snap.Accounts[i].UUID != snap.Active {
					go t.s.run("Switch", func() error { return t.s.switchTo(snap.Accounts[i].UUID, byHand) })
				}
				t.s.onChange()
			}
		}()
	}
	for i, item := range t.thresholds {
		go func() {
			for range item.ClickedCh {
				t.s.setThreshold(thresholds[i])
			}
		}()
	}
	board := &dashboard{s: t.s}
	// 左键单击托盘图标：打开/隐藏仪表盘；右键仍是菜单。
	systray.SetOnTapped(func() { go board.open() })
	go func() {
		for {
			select {
			case <-dash.ClickedCh:
				go board.open()
			case <-t.auto.ClickedCh:
				t.s.setAuto(!t.auto.Checked())
			case <-t.warm.ClickedCh:
				t.s.setWarmup(!t.warm.Checked())
			case <-t.compact.ClickedCh:
				t.s.setCompactReminder(!t.compact.Checked())
			case <-t.spend.ClickedCh:
				t.s.setSpendWeekly(!t.spend.Checked())
			case <-t.add.ClickedCh:
				go t.s.run("Add account", t.s.addAccount)
			case <-t.importNow.ClickedCh:
				go t.s.run("Import", t.s.importChats)
			case <-t.autostart.ClickedCh:
				if err := setAutostart(!t.autostart.Checked()); err != nil {
					t.s.setStatus("Start at login: " + err.Error())
				}
				setChecked(t.autostart, autostartEnabled())
			case <-t.update.ClickedCh:
				if t.s.snapshot().Update != "" {
					shellOpen(releasesURL + "/latest")
				}
			case <-folder.ClickedCh:
				shellOpen(t.s.dir)
			case <-quit.ClickedCh:
				systray.Quit()
			}
		}
	}()

	// Menu updates arrive from worker goroutines; one goroutine applies them.
	t.s.onChange = func() {
		select {
		case t.updates <- struct{}{}:
		default:
		}
	}
	go func() {
		for range t.updates {
			t.render()
		}
	}()
	t.s.onChange()
	go t.loop()
	go t.updateLoop()
	go t.compactLoop()
	go t.s.warmLoop()
}

// loop runs refresh every few seconds while a new sign-in is expected,
// otherwise every minute; refresh decides which accounts are due.
func (t *tray) loop() {
	var last time.Time
	for ; ; time.Sleep(5 * time.Second) {
		if !t.s.adding() && time.Since(last) < hotEvery {
			continue
		}
		last = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		t.s.refresh(ctx)
		cancel()
	}
}

// updateLoop looks for a new release a minute after start, then every hour;
// checkUpdate decides whether a day has passed since the last answer.
func (t *tray) updateLoop() {
	time.Sleep(time.Minute)
	for {
		t.s.checkUpdate(context.Background())
		time.Sleep(time.Hour)
	}
}

func (t *tray) render() {
	snap := t.s.snapshot()
	now := time.Now()

	switch {
	case snap.Status != "":
		t.status.SetTitle(snap.Status)
		t.status.Show()
	case snap.Note != "":
		t.status.SetTitle(snap.Note)
		t.status.Show()
	default:
		t.status.Hide()
	}
	if snap.Update != "" {
		t.update.SetTitle("Official version " + snap.Update + " is out (click for release notes)")
		t.update.Show()
	} else {
		t.update.Hide()
	}
	if len(snap.Accounts) == 0 && snap.Status == "" {
		t.status.SetTitle("Open Claude and sign in")
		t.status.Show()
	}

	activePct := -1.0
	tip := "Claude Account Switcher"
	for i, item := range t.accounts {
		if i >= len(snap.Accounts) {
			item.Hide()
			continue
		}
		a := snap.Accounts[i]
		detail := usageText(a, snap.Threshold, now)
		item.SetTitle(a.label() + menuTab + detail)
		setChecked(item, a.UUID == snap.Active)
		if snap.Busy {
			item.Disable()
		} else {
			item.Enable()
		}
		item.Show()
		if a.UUID == snap.Active {
			tip = a.label() + "\n" + detail
			if a.Usage != nil {
				activePct = a.Usage.current(now).Session
			}
		}
	}
	systray.SetTooltip(tip)
	t.setIcon(activePct, snap.Threshold)

	setChecked(t.auto, snap.AutoSwitch)
	setChecked(t.spend, snap.SpendWeekly)
	setChecked(t.compact, snap.CompactReminder)
	setChecked(t.warm, snap.Warmup)
	for i, item := range t.thresholds {
		setChecked(item, thresholds[i] == snap.Threshold)
	}
	for _, item := range []*systray.MenuItem{t.add, t.importNow} {
		if snap.Busy {
			item.Disable()
		} else {
			item.Enable()
		}
	}
}

// menuTab right-aligns the usage on Windows, where a tab in a menu title
// starts a right-aligned column.
var menuTab = map[bool]string{true: "\t", false: "    "}[runtime.GOOS == "windows"]

func usageText(a Account, threshold float64, now time.Time) string {
	if a.Problem != "" {
		return a.Problem
	}
	if a.Usage == nil {
		return "usage unknown"
	}
	u := a.Usage.current(now)
	// 5h 和 week 的重置时间都始终显示（只要 API 返回了未来的 resets_at）；
	// 用量到阈值时写 "until"，表示要等到这个时间才能再用。
	parts := []string{
		"5h " + windowText(u.Session, u.SessionReset, threshold, now),
		"week " + windowText(u.Weekly, u.WeeklyReset, threshold, now),
	}
	return strings.Join(parts, " · ")
}

// windowText 格式化单个限额窗口：百分比 + 重置时间。
// reset 为零值或已过去时（窗口已重置 / 尚未开始计时）只显示百分比。
func windowText(pct float64, reset time.Time, threshold float64, now time.Time) string {
	s := fmt.Sprintf("%.0f%%", pct)
	switch {
	case reset.IsZero() || !reset.After(now):
	case pct >= threshold:
		s += " until " + resetText(reset, now)
	default:
		s += ", resets " + resetText(reset, now)
	}
	return s
}

func resetText(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return at.Format("15:04")
	case at.Sub(now) < 6*24*time.Hour:
		return at.Format("Mon 15:04")
	}
	// A weekday alone would read as today for a reset a week out.
	return at.Format("Mon 2 Jan 15:04")
}

func (t *tray) setIcon(pct, threshold float64) {
	key := fmt.Sprintf("%.0f/%.0f", pct, threshold)
	if key == t.lastIcon {
		return
	}
	t.lastIcon = key
	systray.SetIcon(trayIcon(pct, threshold))
}

func setChecked(item *systray.MenuItem, on bool) {
	if on != item.Checked() {
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}
