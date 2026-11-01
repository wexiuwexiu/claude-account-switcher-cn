package main

import (
	"fmt"
	"strings"
	"time"
)

// 切号前压缩提醒：当前账户的 5 小时额度快用满、马上要切号时，提醒先在旧账号里 /compact
// 大对话——旧账号缓存还在，压缩便宜；切过去后只需重写摘要，而不是几十万 token 的全文。
// 以下条件全部满足才提醒，每个 5 小时窗口最多一次：
//   - 5 小时用量 ≥ remindAt；
//   - 按本窗口至今的平均速度，会在重置前用到阈值（真的需要切号）；
//   - 离重置还有 waitInstead 以上（快重置了就等，不用切）；
//   - 最近 recentWithin 内有上下文 ≥ bigContext 的对话。
const (
	remindAt     = 85.0
	waitInstead  = 30 * time.Minute
	recentWithin = time.Hour
	bigContext   = 150_000
	minElapsed   = 15 * time.Minute // 窗口刚开始时样本太少，不预测
	sessionSpan  = 5 * time.Hour
)

// compactReminder 判断是否该提醒；该提醒时返回通知标题和正文。
func compactReminder(u Usage, threshold float64, chats []chatInfo, now time.Time) (title, body string, ok bool) {
	s := u.current(now)
	if s.Session < remindAt || s.Session >= threshold || s.SessionReset.IsZero() {
		return "", "", false
	}
	left := s.SessionReset.Sub(now)
	elapsed := sessionSpan - left
	if left < waitInstead || elapsed < minElapsed {
		return "", "", false
	}
	// 平均速度推算到达阈值的时间
	toFull := time.Duration(float64(elapsed) * (threshold - s.Session) / s.Session)
	if toFull >= left {
		return "", "", false
	}
	var big []chatInfo
	for _, c := range chats {
		if c.Context >= bigContext && now.Sub(c.LastRequest) <= recentWithin {
			big = append(big, c)
		}
	}
	if len(big) == 0 {
		return "", "", false
	}
	names := make([]string, 0, 3)
	for i, c := range big {
		if i == 3 {
			break
		}
		if lang() == langZH {
			names = append(names, fmt.Sprintf("「%s」(%s)", shortTitle(c.Title), tokensText(c.Context)))
		} else {
			names = append(names, fmt.Sprintf("\"%s\" (%s)", shortTitle(c.Title), tokensText(c.Context)))
		}
	}
	at := now.Add(toFull).Local().Format("15:04")
	sep := ", "
	if lang() == langZH {
		sep = "、"
	}
	title = trf("5-hour usage %.0f%%, full around %s", s.Session, at)
	body = trf("Before switching, run /compact in %s. Continuing a chat on another account rewrites its whole context there.", strings.Join(names, sep))
	return title, body, true
}

// compactLoop 每分钟检查一次当前账户；同一账户的同一个 5 小时窗口只提醒一次。
func (t *tray) compactLoop() {
	notified := map[string]bool{}
	for ; ; time.Sleep(time.Minute) {
		snap := t.s.snapshot()
		if !snap.CompactReminder {
			continue
		}
		var cur *Account
		for i := range snap.Accounts {
			if snap.Accounts[i].UUID == snap.Active {
				cur = &snap.Accounts[i]
			}
		}
		if cur == nil || cur.Usage == nil {
			continue
		}
		key := cur.UUID + "@" + cur.Usage.SessionReset.Format(time.RFC3339)
		if notified[key] {
			continue
		}
		now := time.Now()
		chats := recentChats(t.s.app().sessionsDir(), claudeProjectsDir(), now, recentWithin)
		if title, body, ok := compactReminder(*cur.Usage, snap.Threshold, chats, now); ok {
			notified[key] = true
			go notify(title, body)
		}
	}
}
