package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 已过期的重置时间不输出，用量按 current() 归零；未来的重置时间原样输出。
func TestBuildDashState(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	snap := snapshot{Active: "a", Threshold: 96, Accounts: []Account{
		{UUID: "a", Name: "A", Usage: &Usage{Session: 50, SessionReset: now.Add(time.Hour), Weekly: 30, WeeklyReset: now.Add(-time.Minute)}},
		{UUID: "b", Name: "B", Problem: "sign-in expired"},
	}}
	st := buildDashState(snap, now)
	a := st.Accounts[0]
	if !a.Active || a.Session.Pct != 50 || a.Session.Reset == nil || !a.Session.Reset.Equal(now.Add(time.Hour)) {
		t.Fatalf("session = %+v", a.Session)
	}
	if a.Weekly.Pct != 0 || a.Weekly.Reset != nil {
		t.Fatalf("weekly should be reset, got %+v", a.Weekly)
	}
	if b := st.Accounts[1]; b.Active || b.Session != nil || b.Problem == "" {
		t.Fatalf("b = %+v", b)
	}
}

// 服务只在带 token 的路径和正确 Host 下响应；切换接口只收 JSON。
func TestDashboardServer(t *testing.T) {
	s := &switcher{st: State{Threshold: 96, Accounts: []*Account{{UUID: "a", Name: "A"}}}, active: "a", onChange: func() {}}
	d := &dashboard{s: s}
	d.once.Do(d.start)
	if d.err != nil {
		t.Fatal(d.err)
	}
	res, err := http.Get(d.url + "api/state")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("state: %v %v", err, res)
	}
	var st dashState
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if len(st.Accounts) != 1 || !st.Accounts[0].Active {
		t.Fatalf("state = %+v", st)
	}
	if res, _ := http.Get("http://" + d.host + "/api/state"); res.StatusCode != 404 {
		t.Fatalf("no token: got %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", d.url+"api/state", nil)
	req.Host = "evil.example:80"
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("bad host: got %d", res.StatusCode)
	}
	if res, _ := http.Post(d.url+"api/switch", "text/plain", strings.NewReader(`{"uuid":"a"}`)); res.StatusCode != 415 {
		t.Fatalf("form post: got %d", res.StatusCode)
	}
	// 切到当前账户：接受但不做任何事
	if res, _ := http.Post(d.url+"api/switch", "application/json", strings.NewReader(`{"uuid":"a"}`)); res.StatusCode != 202 {
		t.Fatalf("same account: got %d", res.StatusCode)
	}
	if res, _ := http.Post(d.url+"api/switch", "application/json", strings.NewReader(`{"uuid":"zz"}`)); res.StatusCode != 404 {
		t.Fatalf("unknown: got %d", res.StatusCode)
	}
}

// 切换代价只列上下文 ≥ 15 万的对话，保持从大到小的顺序；没有时是空数组而不是 null。
func TestBigChats(t *testing.T) {
	got := bigChats([]chatInfo{{Title: "a", Context: 500_000}, {Title: "b", Context: 200_000}, {Title: "c", Context: 90_000}})
	if len(got) != 2 || got[0].Title != "a" || got[1].Title != "b" {
		t.Fatalf("got %+v", got)
	}
	if got := bigChats(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty should be [], got %#v", got)
	}
}
