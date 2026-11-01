package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 5 小时窗口预热：用户最近在用 Claude 时，让周额度最紧迫的 2 个账户的 5 小时窗口一直在计时
// （一个窗口结束就发一次极小的请求续上），这样不管几点开工，这些账户最多 5 小时、平均 2.5 小时
// 就会重置，少切号。默认关闭。
//
// 请求方式：官方 claude CLI 的无头模式，token 用 switcher 已保存的该账户桌面端登录
// （和查用量是同一个，经 CLAUDE_CODE_OAUTH_TOKEN 传入，CLI 不会刷新或改写它）。
// Haiku、无工具、一句话系统提示，实测约 600 token。不冒充客户端直接调 API。
// 保存的 token 约 3～4 周到期，切到该账户用一下 Claude 就会续期；到期的账户跳过。
//
// 限制（减少请求量，见 warmPick）：
//   - 最近 warmActiveFor 内有对话活动才预热；睡觉、几天不用时不发；
//   - 只预热按周额度紧迫度排前 warmTargets 名、且保存的 token 未到期的账户；
//   - 任意两次预热至少间隔 warmGap（顺带把两个账户的窗口错开）；
//   - 每个账户每天最多 warmPerAccount 次，全部每天最多 warmPerDay 次；
//   - 预热后若用量里没出现 5 小时重置时间，记一次失败；连续 warmMaxFails 次就停用该账户并提示。
const (
	warmTargets    = 2
	warmActiveFor  = 6 * time.Hour
	warmGap        = 150 * time.Minute
	warmPerAccount = 4
	warmPerDay     = 8
	warmMaxFails   = 2
	// 预热后等这么久再看用量（用量接口有延迟）；超过 warmVerifyBy 还没新数据就按失败算。
	warmVerifyAfter = 30 * time.Second
	warmVerifyBy    = 20 * time.Minute
	warmTimeout     = 90 * time.Second
)

// warmRec 是一个账户的预热记录，存在 state.json 里。
type warmRec struct {
	Last   time.Time `json:"last,omitempty"`
	Day    string    `json:"day,omitempty"` // Count 所属的本地日期 2006-01-02
	Count  int       `json:"count,omitempty"`
	Verify time.Time `json:"verify,omitempty"` // 等待确认的那次预热时间；确认后清零
	Fails  int       `json:"fails,omitempty"`
	// Err 不为空时该账户停止预热；ErrLogin 是出错时保存的登录到期时间，
	// 切到该账户后登录续期（到期时间变了）就自动恢复。
	Err      string    `json:"err,omitempty"`
	ErrLogin time.Time `json:"errLogin,omitempty"`
	Tokens   int       `json:"tokens,omitempty"` // 最近一次预热消耗的 token 总数
}

// stopped 表示该账户因出错停止预热，且登录还没续期过。
func (r *warmRec) stopped(a Account) bool {
	return r != nil && r.Err != "" && r.ErrLogin.Equal(a.LoginExpires)
}

// loginValid 表示保存的登录 token 还没到期（不知道到期时间时也尝试）。
func loginValid(a Account, now time.Time) bool {
	return a.LoginExpires.IsZero() || a.LoginExpires.After(now)
}

func (r *warmRec) countOn(day string) int {
	if r == nil || r.Day != day {
		return 0
	}
	return r.Count
}

// urgency 是周额度紧迫度：周剩余 % ÷ 距周重置的小时数，即"要按时用完，平均每小时得用多少"。
// 与 dashboard.html 的 urgency() 一致；不可用（有问题、无用量、周已到阈值）时为 0。
func urgency(a Account, threshold float64, now time.Time) float64 {
	if a.Problem != "" || a.Usage == nil {
		return 0
	}
	u := a.Usage.current(now)
	left := 100 - u.Weekly
	if left <= 100-threshold {
		return 0
	}
	hours := 7 * 24.0
	if !u.WeeklyReset.IsZero() {
		hours = max(u.WeeklyReset.Sub(now).Hours(), 0.5)
	}
	return left / hours
}

// warmPick 选出现在该预热的账户，没有则返回空。纯函数，便于测试。
// lastChat 是最近一次对话活动时间。
func warmPick(accts []Account, recs map[string]*warmRec, threshold float64, lastChat, now time.Time) string {
	if lastChat.IsZero() || now.Sub(lastChat) > warmActiveFor {
		return ""
	}
	day := now.Local().Format("2006-01-02")
	total := 0
	for _, r := range recs {
		total += r.countOn(day)
		if r != nil && now.Sub(r.Last) < warmGap {
			return "" // 离上一次预热太近（也让各账户的窗口错开）
		}
	}
	if total >= warmPerDay {
		return ""
	}
	// 只在登录未到期、未停用的账户里按紧迫度取前 warmTargets 名
	var cands []Account
	for _, a := range accts {
		if loginValid(a, now) && !recs[a.UUID].stopped(a) && urgency(a, threshold, now) > 0 {
			cands = append(cands, a)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return urgency(cands[i], threshold, now) > urgency(cands[j], threshold, now) })
	if len(cands) > warmTargets {
		cands = cands[:warmTargets]
	}
	for _, a := range cands {
		r := recs[a.UUID]
		u := a.Usage.current(now)
		running := !u.SessionReset.IsZero() && u.SessionReset.After(now)
		// 用量数据要新：至少在上次预热之后、且在 idleFresh 内查过，避免对着旧数据重复发
		fresh := now.Sub(a.Usage.CheckedAt) <= idleFresh && (r == nil || a.Usage.CheckedAt.After(r.Last))
		if !running && fresh && r.countOn(day) < warmPerAccount && (r == nil || r.Verify.IsZero()) {
			return a.UUID
		}
	}
	return ""
}

// warmVerdict 判断一次待确认的预热：done 表示有结论，ok 表示窗口已开始计时。
// 用量必须是预热之后 warmVerifyAfter 以上查到的；超过 warmVerifyBy 仍无新数据算失败。
func warmVerdict(r *warmRec, u *Usage, now time.Time) (done, ok bool) {
	if r == nil || r.Verify.IsZero() {
		return false, false
	}
	if u != nil && u.CheckedAt.Sub(r.Verify) >= warmVerifyAfter {
		return true, u.SessionReset.After(r.Verify)
	}
	return now.Sub(r.Verify) > warmVerifyBy, false
}

// warmToken 取该账户已保存的桌面端登录 token（当前账户取 Claude 里正在用的），与查用量相同。
func (s *switcher) warmToken(uuid string) (string, error) {
	active, _ := s.app().activeAccount()
	return s.tokenFor(uuid, active)
}

// ---- 发送预热 ----

// claudeExe 找 claude CLI 的可执行文件。npm 安装的 claude.cmd 经 cmd.exe 转发时空参数（--tools ""）
// 不可靠，所以直接用它背后的 claude.exe。
func claudeExe() (string, error) {
	p, err := exec.LookPath("claude")
	if err != nil {
		home, _ := os.UserHomeDir()
		for _, c := range []string{filepath.Join(home, ".local", "bin", "claude.exe"), filepath.Join(home, ".local", "bin", "claude")} {
			if _, e := os.Stat(c); e == nil {
				return c, nil
			}
		}
		return "", errors.New("claude CLI not found; install Claude Code")
	}
	if strings.EqualFold(filepath.Ext(p), ".cmd") {
		real := filepath.Join(filepath.Dir(p), "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
		if _, err := os.Stat(real); err == nil {
			return real, nil
		}
	}
	return p, nil
}

// warmArgs 是精简的一次性请求：Haiku、无工具、一句话系统提示、不加载设置/MCP/斜杠命令、不留会话。
var warmArgs = []string{"-p", "hi", "--model", "haiku", "--tools", "", "--system-prompt", "Reply with one word.",
	"--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands", "--no-session-persistence",
	"--output-format", "json"}

// sendWarmup 用 token 发一次预热，返回消耗的 token 数。
func sendWarmup(ctx context.Context, token string) (int, error) {
	exe, err := claudeExe()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, warmArgs...)
	cmd.Dir = os.TempDir() // 避免读到某个项目的 CLAUDE.md
	env := []string{"CLAUDE_CODE_OAUTH_TOKEN=" + token}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL":
			continue // 只用这个账户的 token，不能被别的认证方式顶替
		}
		env = append(env, kv)
	}
	cmd.Env = env
	hideWindow(cmd)
	out, err := cmd.Output()
	var res struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Usage   struct {
			Input         int `json:"input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			Output        int `json:"output_tokens"`
		} `json:"usage"`
	}
	if jerr := json.Unmarshal(out, &res); jerr != nil {
		if err == nil {
			err = jerr
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return 0, err
	}
	if res.IsError {
		return 0, errors.New(res.Result)
	}
	u := res.Usage
	return u.Input + u.CacheCreation + u.CacheRead + u.Output, nil
}

// warmLoop 每分钟看一次：先确认上次预热的结果，再按 warmPick 决定是否预热。
func (s *switcher) warmLoop() {
	for ; ; time.Sleep(time.Minute) {
		s.warmStep(context.Background(), time.Now())
	}
}

func (s *switcher) warmStep(ctx context.Context, now time.Time) {
	s.mu.Lock()
	if !s.st.Warmup || s.busy.Load() {
		s.mu.Unlock()
		return
	}
	if s.st.Warm == nil {
		s.st.Warm = map[string]*warmRec{}
	}
	changed := false
	// 1. 确认待定的预热
	for _, a := range s.st.Accounts {
		r := s.st.Warm[a.UUID]
		done, ok := warmVerdict(r, a.Usage, now)
		if !done {
			continue
		}
		r.Verify, changed = time.Time{}, true
		if ok {
			r.Fails, r.Err = 0, ""
			log.Printf("warmup %s: 5-hour window started", a.label())
			continue
		}
		r.Fails++
		log.Printf("warmup %s: no 5-hour window after the request (%d/%d)", a.label(), r.Fails, warmMaxFails)
		if r.Fails >= warmMaxFails {
			r.Err, r.ErrLogin = "预热后 5 小时窗口没有开始计时", a.LoginExpires
		}
	}
	accts := make([]Account, 0, len(s.st.Accounts))
	for _, a := range s.st.Accounts {
		accts = append(accts, *a)
	}
	recs := map[string]*warmRec{}
	for k, v := range s.st.Warm {
		c := *v
		recs[k] = &c
	}
	threshold := s.st.Threshold
	if changed {
		s.saveState()
	}
	s.mu.Unlock()
	if changed {
		s.onChange()
	}

	// 2. 选账户并发送
	uuid := warmPick(accts, recs, threshold, lastChatWrite(s.app().sessionsDir()), now)
	if uuid == "" {
		return
	}
	token, err := s.warmToken(uuid)
	var n int
	if err == nil {
		n, err = sendWarmup(ctx, token)
	}
	s.mu.Lock()
	r := s.st.Warm[uuid]
	if r == nil {
		r = &warmRec{}
		s.st.Warm[uuid] = r
	}
	day := now.Local().Format("2006-01-02")
	if r.Day != day {
		r.Day, r.Count = day, 0
	}
	r.Last = now // 失败也算一次，避免出错时每分钟重试
	r.Count++
	a := s.account(uuid)
	if err != nil {
		msg := err.Error()
		if len(msg) > 120 {
			msg = msg[:120] + "…"
		}
		r.Fails++
		if r.Fails >= warmMaxFails || strings.Contains(msg, "401") || strings.Contains(strings.ToLower(msg), "auth") {
			r.Err = "预热失败：" + msg
			if a != nil {
				r.ErrLogin = a.LoginExpires
			}
		}
		log.Printf("warmup %s failed: %v", uuid[:8], err)
	} else {
		r.Verify, r.Tokens = now, n
		if a != nil && a.Usage != nil {
			a.Usage.CheckedAt = time.Time{} // 下一轮 refresh 立即重查这个账户的用量
		}
		label := uuid[:8]
		if a != nil {
			label = a.label()
		}
		log.Printf("warmup %s: sent, %d tokens", label, n)
	}
	s.saveState()
	s.mu.Unlock()
	s.onChange()
}

// setWarmup 开关预热。
func (s *switcher) setWarmup(on bool) {
	s.mu.Lock()
	s.st.Warmup = on
	s.saveState()
	s.mu.Unlock()
	s.onChange()
}
