package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 只有 lastKnownAccountUuid、没有 token 缓存时视为"仅网页登录"；有 token 时不算。
func TestPendingAccount(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(d.dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"lastKnownAccountUuid":"2ba07d3c-4dbb-4f6c-ad5d-b4dce94cdc17","locale":"zh-CN"}`)
	if got := d.pendingAccount(); got != "2ba07d3c-4dbb-4f6c-ad5d-b4dce94cdc17" {
		t.Fatalf("web-only sign-in: got %q", got)
	}
	if id, _ := d.activeAccount(); id != "" {
		t.Fatalf("activeAccount should still need a token, got %q", id)
	}
	write(`{"lastKnownAccountUuid":"2ba07d3c-4dbb-4f6c-ad5d-b4dce94cdc17","oauth:tokenCacheV2":"x"}`)
	if got := d.pendingAccount(); got != "" {
		t.Fatalf("with a token it is not pending, got %q", got)
	}
	write(`{"locale":"zh-CN"}`)
	if got := d.pendingAccount(); got != "" {
		t.Fatalf("signed out: got %q", got)
	}
}
