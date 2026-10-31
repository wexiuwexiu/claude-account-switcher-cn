package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 仪表盘：托盘菜单 "Dashboard…" 打开的图形页面。
// 程序内启动一个只监听 127.0.0.1 随机端口的 HTTP 服务，用默认浏览器打开。
// 数据来源是 switcher.snapshot()，与托盘菜单完全一致，不额外请求 API。
// 安全：所有路径都带随机 token，且校验 Host，防止其他网页跨站触发切换 / DNS rebinding。

//go:embed dashboard.html
var dashboardHTML []byte

type dashboard struct {
	s    *switcher
	once sync.Once
	url  string
	host string
	err  error
}

// open 第一次调用时启动服务；Windows 上切换原生窗口的显示/隐藏，
// WebView2 不可用或其它平台则用浏览器打开页面。
func (d *dashboard) open() {
	d.once.Do(d.start)
	if d.err != nil {
		d.s.setStatus("Dashboard: " + d.err.Error())
		return
	}
	if toggleNativeDashboard(d.url, d.s.dir, len(d.s.snapshot().Accounts)) {
		return
	}
	shellOpen(d.url)
}

func (d *dashboard) start() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		d.err = err
		return
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		d.err = err
		l.Close()
		return
	}
	prefix := "/" + hex.EncodeToString(b)
	d.host = l.Addr().String()
	d.url = "http://" + d.host + prefix + "/"

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(dashboardHTML)
	})
	mux.HandleFunc("GET "+prefix+"/api/state", d.state)
	mux.HandleFunc("POST "+prefix+"/api/switch", d.switchTo)
	srv := &http.Server{Handler: d.checkHost(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(l); err != nil {
			log.Printf("dashboard: %v", err)
		}
	}()
	log.Printf("dashboard on %s", d.host)
}

// checkHost 只接受 Host 正好是监听地址的请求（挡 DNS rebinding）。
func (d *dashboard) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != d.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// dashWindow 是单个限额窗口（5h / 周）。Reset 为空表示没有未来的重置时间。
type dashWindow struct {
	Pct   float64    `json:"pct"`
	Reset *time.Time `json:"reset,omitempty"`
}

type dashAccount struct {
	UUID      string      `json:"uuid"`
	Label     string      `json:"label"`
	Email     string      `json:"email"`
	Active    bool        `json:"active"`
	Problem   string      `json:"problem,omitempty"`
	Session   *dashWindow `json:"session,omitempty"`
	Weekly    *dashWindow `json:"weekly,omitempty"`
	CheckedAt *time.Time  `json:"checkedAt,omitempty"`
	// Error 是最近一次用量查询失败的原因；StaleAfter 秒后数据算过期（当前账户刷新更勤）。
	Error string `json:"error,omitempty"`
	// LoginExpires 是保存的登录 token 过期时间，用于提醒长期不用的账户。
	LoginExpires *time.Time `json:"loginExpires,omitempty"`
	StaleAfter   int        `json:"staleAfter"`
}

type dashState struct {
	Now        time.Time     `json:"now"`
	Accounts   []dashAccount `json:"accounts"`
	Threshold  float64       `json:"threshold"`
	WarnAt     float64       `json:"warnAt"`
	AutoSwitch bool          `json:"autoSwitch"`
	Busy       bool          `json:"busy"`
	Status     string        `json:"status,omitempty"`
	Note       string        `json:"note,omitempty"`
}

// buildDashState 把 snapshot 转成页面用的 JSON；用量先经 current() 处理，
// 已过重置时间的窗口归零，和菜单文字保持一致。
func buildDashState(snap snapshot, now time.Time) dashState {
	st := dashState{Now: now, Threshold: snap.Threshold, WarnAt: warnAt, AutoSwitch: snap.AutoSwitch,
		Busy: snap.Busy, Status: snap.Status, Note: snap.Note, Accounts: []dashAccount{}}
	window := func(pct float64, reset time.Time) *dashWindow {
		w := &dashWindow{Pct: pct}
		if !reset.IsZero() && reset.After(now) {
			w.Reset = &reset
		}
		return w
	}
	for _, a := range snap.Accounts {
		da := dashAccount{UUID: a.UUID, Label: a.label(), Email: a.Email, Active: a.UUID == snap.Active, Problem: a.Problem,
			Error: snap.Errs[a.UUID], StaleAfter: int(idleFresh.Seconds())}
		if da.Active {
			da.StaleAfter = int(activeFresh.Seconds())
		}
		if !a.LoginExpires.IsZero() {
			exp := a.LoginExpires
			da.LoginExpires = &exp
		}
		if a.Usage != nil {
			u := a.Usage.current(now)
			da.Session = window(u.Session, u.SessionReset)
			da.Weekly = window(u.Weekly, u.WeeklyReset)
			if !u.CheckedAt.IsZero() {
				da.CheckedAt = &u.CheckedAt
			}
		}
		st.Accounts = append(st.Accounts, da)
	}
	return st
}

func (d *dashboard) state(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	now := time.Now()
	json.NewEncoder(w).Encode(buildDashState(d.s.snapshot(), now))
}

// switchTo 与菜单点击账户相同：目标不是当前账户且不忙时，后台执行切换。
func (d *dashboard) switchTo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UUID string `json:"uuid"`
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "json only", http.StatusUnsupportedMediaType)
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	snap := d.s.snapshot()
	found := false
	for _, a := range snap.Accounts {
		found = found || a.UUID == body.UUID
	}
	switch {
	case !found:
		http.Error(w, "unknown account", http.StatusNotFound)
		return
	case snap.Busy:
		http.Error(w, "busy", http.StatusConflict)
		return
	case body.UUID != snap.Active:
		go d.s.run("Switch", func() error { return d.s.switchTo(body.UUID, byHand) })
	}
	w.WriteHeader(http.StatusAccepted)
}
