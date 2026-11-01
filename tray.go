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
	t.update = systray.AddMenuItem("", "打开发布页；本改版不会被官方版本覆盖")
	t.update.Hide()
	for i := range t.accounts {
		t.accounts[i] = systray.AddMenuItemCheckbox("", "把 Claude 切换到这个账户", false)
		t.accounts[i].Hide()
	}
	systray.AddSeparator()
	// 图形仪表盘：所有账户的 5h / 周用量进度条与重置倒计时，见 dashboard.go
	dash := systray.AddMenuItem("仪表盘…", "图形显示所有账户的用量和重置时间（左键单击托盘图标也可打开）")
	t.auto = systray.AddMenuItemCheckbox("快用满时自动切换", "当前账户达到阈值时，切到剩余额度最多的账户", false)
	limit := systray.AddMenuItem("自动切换阈值", "")
	for _, v := range thresholds {
		t.thresholds = append(t.thresholds, limit.AddSubMenuItemCheckbox(fmt.Sprintf("%.0f%%", v), "", false))
	}
	t.spend = systray.AddMenuItemCheckbox("自动切换去用完快到期的周额度", "某账户的周额度一天内重置、且还剩一半以上时切过去，免得作废", false)
	t.compact = systray.AddMenuItemCheckbox("切号前提醒 /compact", "5 小时额度快用满、要切号前，提醒先压缩大对话", false)
	// 5 小时窗口预热，见 warmup.go；用各账户已保存的登录，不需要额外设置
	t.warm = systray.AddMenuItemCheckbox("预热 5 小时窗口", "最近用过 Claude 时，用一条 Haiku 极简请求（约 600 token）让最紧迫的 2 个账户的 5 小时窗口一直在计时", false)
	t.add = systray.AddMenuItem("添加账户…", "重启 Claude 到登录界面，当前账户会保留")
	t.importNow = systray.AddMenuItem("导入所有账户的对话", "重启 Claude，让所有对话都出现在当前账户里")
	systray.AddSeparator()
	// Rewriting an enabled entry keeps its path current and adds the Task
	// Manager record that entries from before v0.1.2 are missing.
	if autostartEnabled() {
		if err := setAutostart(true); err != nil {
			log.Printf("refresh start at login: %v", err)
		}
	}
	t.autostart = systray.AddMenuItemCheckbox("开机启动", "", autostartEnabled())
	folder := systray.AddMenuItem("打开数据文件夹", "")
	quit := systray.AddMenuItem("退出", "")

	for i, item := range t.accounts {
		go func() {
			for range item.ClickedCh {
				snap := t.s.snapshot()
				if i < len(snap.Accounts) && snap.Accounts[i].UUID != snap.Active {
					go t.s.run("切换", func() error { return t.s.switchTo(snap.Accounts[i].UUID, byHand) })
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
				go t.s.run("添加账户", t.s.addAccount)
			case <-t.importNow.ClickedCh:
				go t.s.run("导入对话", t.s.importChats)
			case <-t.autostart.ClickedCh:
				if err := setAutostart(!t.autostart.Checked()); err != nil {
					t.s.setStatus("开机启动：" + err.Error())
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
		t.update.SetTitle("官方新版本 " + snap.Update + " 已发布（点击查看）")
		t.update.Show()
	} else {
		t.update.Hide()
	}
	if len(snap.Accounts) == 0 && snap.Status == "" {
		t.status.SetTitle("请打开 Claude 并登录")
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

// problemZH 把账户问题（Account.Problem 存英文原文）翻成菜单里的中文，与 dashboard.html 的 PROBLEM_ZH 一致。
var problemZH = map[string]string{
	"web sign-in only; usage shows after Pro + one use of the Code tab": "仅网页登录，开通 Pro 并用一次 Code 后显示用量",
	"signed out, add it again": "登录已失效，请重新添加",
}

// usageText 是菜单和 -status 里账户后面的用量说明，例如 "5时 20% · 本周 40%，周四 12:00 重置"。
func usageText(a Account, threshold float64, now time.Time) string {
	if a.Problem != "" {
		if zh, ok := problemZH[a.Problem]; ok {
			return zh
		}
		return a.Problem
	}
	if a.Usage == nil {
		return "用量未知"
	}
	u := a.Usage.current(now)
	// 5 小时和周的重置时间都始终显示（只要 API 返回了未来的 resets_at）；
	// 用量到阈值时写"恢复"，表示要等到这个时间才能再用。
	parts := []string{
		"5时 " + windowText(u.Session, u.SessionReset, threshold, now),
		"本周 " + windowText(u.Weekly, u.WeeklyReset, threshold, now),
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
		s += "，" + resetText(reset, now) + " 恢复"
	default:
		s += "，" + resetText(reset, now) + " 重置"
	}
	return s
}

// resetText 写重置时间：今天只写时分，6 天内加周几，更远再加月日（只写周几会被误读成本周）。
func resetText(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	wd := "周" + []string{"日", "一", "二", "三", "四", "五", "六"}[at.Weekday()]
	switch {
	case at.YearDay() == now.YearDay() && at.Year() == now.Year():
		return at.Format("15:04")
	case at.Sub(now) < 6*24*time.Hour:
		return wd + " " + at.Format("15:04")
	}
	return at.Format("01-02 ") + wd + " " + at.Format("15:04")
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
