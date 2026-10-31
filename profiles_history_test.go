package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 每次覆盖前存一份历史，内容相同不重复存，最多保留 keepHistory 份。
func TestArchiveProfile(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	dir := filepath.Join(t.TempDir(), "acct")
	if err := archiveProfile(d, dir, time.Now()); err != nil {
		t.Fatalf("first save has nothing to archive: %v", err)
	}
	os.MkdirAll(dir, 0o700)
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	for i := 0; i < keepHistory+3; i++ {
		os.WriteFile(filepath.Join(dir, "config.json"), []byte{byte('a' + i)}, 0o600)
		if err := archiveProfile(d, dir, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		// 同样的内容再存一次：不应新增
		if err := archiveProfile(d, dir, base.Add(time.Duration(i)*time.Minute+time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	names := historyNames(filepath.Join(dir, "history"))
	if len(names) != keepHistory {
		t.Fatalf("kept %d copies, want %d: %v", len(names), keepHistory, names)
	}
	newest, _ := os.ReadFile(filepath.Join(dir, "history", names[len(names)-1], "config.json"))
	if string(newest) != string(rune('a'+keepHistory+2)) {
		t.Fatalf("newest copy = %q", newest)
	}
}
