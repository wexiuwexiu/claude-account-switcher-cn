package main

import (
	"testing"
	"time"
)

// 预热选择：活跃、紧迫度前 2、窗口没在计时、数据新、未超次数和间隔才选。
func TestWarmPick(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
	fresh := now.Add(-time.Minute)
	acct := func(id string, weekly float64, weekLeft time.Duration, sessionReset time.Time) Account {
		return Account{UUID: id, Usage: &Usage{Weekly: weekly, WeeklyReset: now.Add(weekLeft), SessionReset: sessionReset, CheckedAt: fresh}}
	}
	// a 最紧迫（剩 80%、1 天后重置），b 次之，c 最不紧迫
	accts := []Account{
		acct("c", 10, 6*24*time.Hour, time.Time{}),
		acct("a", 20, 24*time.Hour, time.Time{}),
		acct("b", 30, 2*24*time.Hour, time.Time{}),
	}
	active := now.Add(-time.Hour)

	if got := warmPick(accts, nil, 96, active, now); got != "a" {
		t.Fatalf("most urgent first: got %q", got)
	}
	if got := warmPick(accts, nil, 96, now.Add(-7*time.Hour), now); got != "" {
		t.Fatalf("idle user: got %q", got)
	}
	// a 的窗口在计时 -> 选 b；c 不在前 2 名，永远不选
	running := append([]Account(nil), accts...)
	running[1] = acct("a", 20, 24*time.Hour, now.Add(2*time.Hour))
	if got := warmPick(running, nil, 96, active, now); got != "b" {
		t.Fatalf("a running: got %q", got)
	}
	running[2] = acct("b", 30, 2*24*time.Hour, now.Add(3*time.Hour))
	if got := warmPick(running, nil, 96, active, now); got != "" {
		t.Fatalf("top 2 running, c must not be picked: got %q", got)
	}
	// 保存的登录已到期的账户不算进前 2 名
	expired := append([]Account(nil), accts...)
	expired[1].LoginExpires = now.Add(-time.Hour)
	if got := warmPick(expired, nil, 96, active, now); got != "b" {
		t.Fatalf("a login expired: got %q", got)
	}
	// 间隔：任何账户 150 分钟内预热过就不发
	recent := map[string]*warmRec{"b": {Last: now.Add(-time.Hour)}}
	if got := warmPick(accts, recent, 96, active, now); got != "" {
		t.Fatalf("gap: got %q", got)
	}
	// 当天次数上限
	day := now.Format("2006-01-02")
	capped := map[string]*warmRec{"a": {Last: now.Add(-3 * time.Hour), Day: day, Count: warmPerAccount}}
	if got := warmPick(accts, capped, 96, active, now); got != "b" {
		t.Fatalf("a capped: got %q", got)
	}
	// 停用的账户跳过，登录续期（到期时间变了）后恢复；待确认的账户不重复发
	if got := warmPick(accts, map[string]*warmRec{"a": {Err: "x"}}, 96, active, now); got != "b" {
		t.Fatalf("a stopped: got %q", got)
	}
	renewed := append([]Account(nil), accts...)
	renewed[1].LoginExpires = now.Add(20 * 24 * time.Hour)
	if got := warmPick(renewed, map[string]*warmRec{"a": {Err: "x"}}, 96, active, now); got != "a" {
		t.Fatalf("a renewed: got %q", got)
	}
	if got := warmPick(accts, map[string]*warmRec{"a": {Last: now.Add(-3 * time.Hour), Verify: now.Add(-3 * time.Hour)}}, 96, active, now); got != "b" {
		t.Fatalf("a verifying: got %q", got)
	}
	// 用量数据比上次预热旧：不发
	stale := map[string]*warmRec{"a": {Last: now.Add(-3 * time.Hour)}}
	old := append([]Account(nil), accts...)
	old[1].Usage = &Usage{Weekly: 20, WeeklyReset: now.Add(24 * time.Hour), CheckedAt: now.Add(-4 * time.Hour)}
	if got := warmPick(old, stale, 96, active, now); got != "b" {
		t.Fatalf("stale usage for a: got %q", got)
	}
}

// 确认：预热后 30 秒以上查到的用量里出现了晚于预热的 5 小时重置时间才算成功。
func TestWarmVerdict(t *testing.T) {
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
	r := &warmRec{Verify: at}
	if done, _ := warmVerdict(r, &Usage{CheckedAt: at.Add(10 * time.Second)}, at.Add(time.Minute)); done {
		t.Fatal("too early to judge")
	}
	if done, ok := warmVerdict(r, &Usage{CheckedAt: at.Add(time.Minute), SessionReset: at.Add(5 * time.Hour)}, at.Add(time.Minute)); !done || !ok {
		t.Fatal("window started should be ok")
	}
	if done, ok := warmVerdict(r, &Usage{CheckedAt: at.Add(time.Minute)}, at.Add(time.Minute)); !done || ok {
		t.Fatal("no window should fail")
	}
	if done, ok := warmVerdict(r, nil, at.Add(warmVerifyBy+time.Minute)); !done || ok {
		t.Fatal("no data in time should fail")
	}
	if done, _ := warmVerdict(&warmRec{}, nil, at); done {
		t.Fatal("nothing pending")
	}
}

// 紧迫度与 dashboard.html 一致：周剩余 % ÷ 距重置小时数；到阈值或有问题为 0。
func TestUrgency(t *testing.T) {
	now := time.Now()
	a := Account{Usage: &Usage{Weekly: 20, WeeklyReset: now.Add(40 * time.Hour)}}
	if u := urgency(a, 96, now); u < 1.99 || u > 2.01 {
		t.Fatalf("urgency = %v", u)
	}
	a.Usage.Weekly = 97
	if urgency(a, 96, now) != 0 {
		t.Fatal("full week should be 0")
	}
	if urgency(Account{Problem: "x", Usage: &Usage{}}, 96, now) != 0 {
		t.Fatal("problem should be 0")
	}
}
