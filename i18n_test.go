package main

import (
	"testing"
	"time"
)

// withLang 在测试里临时切换语言，结束后恢复英文（其它测试按英文写）。
func withLang(t *testing.T, l string) {
	setLang(l)
	t.Cleanup(func() { setLang(langEN) })
}

// 中文模式下用量文字、重置时间、自动切换说明都是中文；没有翻译的原样返回。
func TestChineseTexts(t *testing.T) {
	withLang(t, langZH)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	a := Account{Usage: &Usage{Session: 1, SessionReset: now.Add(3 * time.Hour), Weekly: 40, WeeklyReset: now.Add(2 * 24 * time.Hour)}}
	if got, want := usageText(a, 96, now), "5时 1%，15:00 重置 · 本周 40%，周四 12:00 重置"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	a.Usage.Session = 100
	if got, want := usageText(a, 96, now), "5时 100%，15:00 恢复 · 本周 40%，周四 12:00 重置"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := resetText(now.Add(6*24*time.Hour+time.Hour), now), "10-12 周一 13:00"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := usageText(Account{Problem: "signed out, add it again"}, 96, now); got != "登录已失效，请重新添加" {
		t.Fatalf("problem: %q", got)
	}
	if got := tr("no such text"); got != "no such text" {
		t.Fatalf("untranslated: %q", got)
	}
	if got := trPrefix("warmup failed: 401", "warmup failed: "); got != "预热失败：401" {
		t.Fatalf("prefix: %q", got)
	}
	if got := tokensText(452_000); got != "45.2万" {
		t.Fatalf("tokens: %q", got)
	}
}

// 英文是默认值；不认识的语言按英文。
func TestLangDefaults(t *testing.T) {
	withLang(t, "fr")
	if lang() != langEN || tr("Quit") != "Quit" || tokensText(452_000) != "452k" {
		t.Fatalf("lang=%q tr=%q tokens=%q", lang(), tr("Quit"), tokensText(452_000))
	}
	if l := systemLang(); l != langZH && l != langEN {
		t.Fatalf("systemLang = %q", l)
	}
}
