package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 逐条验证触发条件：用量、预计用满、离重置的时间、大对话。
func TestCompactReminder(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	big := []chatInfo{{Title: "光学实验台重构", LastRequest: now.Add(-5 * time.Minute), Context: 452_000}}
	// 窗口已过 2 小时、用了 88%，3 小时后重置：按速度约 11 分钟后到 96%
	u := Usage{Session: 88, SessionReset: now.Add(3 * time.Hour), CheckedAt: now}
	title, body, ok := compactReminder(u, 96, big, now)
	if !ok || !strings.Contains(title, "88%") || !strings.Contains(body, "光学实验台重构") || !strings.Contains(body, "45.2万") {
		t.Fatalf("should remind: %v %q %q", ok, title, body)
	}
	cases := []struct {
		name  string
		u     Usage
		chats []chatInfo
	}{
		{"below 85%", Usage{Session: 80, SessionReset: now.Add(3 * time.Hour)}, big},
		{"already at threshold", Usage{Session: 97, SessionReset: now.Add(3 * time.Hour)}, big},
		{"resets within 30 min", Usage{Session: 90, SessionReset: now.Add(20 * time.Minute)}, big},
		{"slow pace lasts until reset", Usage{Session: 86, SessionReset: now.Add(10 * time.Minute)}, big},
		{"no large chat", u, []chatInfo{{Title: "x", LastRequest: now, Context: 90_000}}},
		{"large chat idle over an hour", u, []chatInfo{{Title: "x", LastRequest: now.Add(-2 * time.Hour), Context: 500_000}}},
	}
	for _, c := range cases {
		if _, _, ok := compactReminder(c.u, 96, c.chats, now); ok {
			t.Errorf("%s: should not remind", c.name)
		}
	}
	// 慢速：窗口过了 4 小时才用到 86%，剩 1 小时，按速度到 96% 需要 28 分钟以上 → 会用满；改成 4.5 小时用到 86% 就用不满
	slow := Usage{Session: 86, SessionReset: now.Add(31 * time.Minute)}
	if _, _, ok := compactReminder(slow, 96, big, now); ok {
		t.Error("86% after 4.5h with 31m left should last until the reset")
	}
}

// recentChats：同一对话在多个账户目录里只算一次，取最后一次主线程请求的上下文。
func TestRecentChats(t *testing.T) {
	root := t.TempDir()
	sessions, projects := filepath.Join(root, "s"), filepath.Join(root, "p")
	now := time.Now()
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry := `{"title":"大任务","cliSessionId":"cli1","lastActivityAt":%d}`
	write(filepath.Join(sessions, "a", "o", "local_x.json"), fmt.Sprintf(entry, now.UnixMilli()))
	write(filepath.Join(sessions, "b", "o", "local_y.json"), fmt.Sprintf(entry, now.Add(-time.Minute).UnixMilli()))
	req := func(ts time.Time, side bool, read int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"isSidechain":%v,"message":{"usage":{"input_tokens":5,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0}}}`,
			ts.UTC().Format(time.RFC3339Nano), side, read)
	}
	write(filepath.Join(projects, "proj", "cli1.jsonl"),
		req(now.Add(-10*time.Minute), false, 200_000)+"\n"+req(now.Add(-5*time.Minute), false, 300_000)+"\n"+req(now.Add(-4*time.Minute), true, 9)+"\n")
	chats := recentChats(sessions, projects, now, time.Hour)
	if len(chats) != 1 || chats[0].Context != 300_005 || chats[0].Title != "大任务" {
		t.Fatalf("chats = %+v", chats)
	}
	// 刚 /compact 完、还没新请求：按 compact_boundary 的 postTokens 算
	f, _ := os.OpenFile(filepath.Join(projects, "proj", "cli1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	fmt.Fprintf(f, `{"type":"system","subtype":"compact_boundary","timestamp":%q,"isSidechain":false,"compactMetadata":{"trigger":"manual","preTokens":300000,"postTokens":15000}}`+"\n",
		now.Add(-time.Minute).UTC().Format(time.RFC3339Nano))
	f.Close()
	if chats := recentChats(sessions, projects, now, time.Hour); len(chats) != 1 || chats[0].Context != 15_000 {
		t.Fatalf("after compact = %+v", chats)
	}
	if got := recentChats(sessions, projects, now.Add(2*time.Hour), time.Hour); len(got) != 0 {
		t.Fatalf("old chats should be skipped: %+v", got)
	}
}
