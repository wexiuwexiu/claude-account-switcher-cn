package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// removeAccount：从列表去掉、清理相关记录，登录文件夹移到 removed/ 保留；在用的账户不能移除。
func TestRemoveAccount(t *testing.T) {
	dir := t.TempDir()
	s := newSwitcher(dir)
	withLang(t, langEN) // newSwitcher 按系统语言设了界面语言，测试结束恢复英文
	s.st.Accounts = []*Account{{UUID: "aaaa1111", Email: "a@x"}, {UUID: "bbbb2222", Email: "b@x"}}
	s.st.Warm = map[string]*warmRec{"bbbb2222": {}}
	s.st.Spending, s.st.SpendUntil = "bbbb2222", time.Now().Add(time.Hour)
	s.errs["bbbb2222"] = "x"
	s.active = "aaaa1111"
	write(t, filepath.Join(s.profileDir("bbbb2222"), "config.json"), "{}")
	write(t, filepath.Join(s.profileDir("bbbb2222"), "history", "20261101-000000", "config.json"), "{}")

	if err := s.removeAccount("aaaa1111"); err == nil {
		t.Fatal("the active account must not be removed")
	}
	if err := s.removeAccount("bbbb2222"); err != nil {
		t.Fatal(err)
	}
	if len(s.st.Accounts) != 1 || s.st.Accounts[0].UUID != "aaaa1111" {
		t.Fatalf("accounts = %+v", s.st.Accounts)
	}
	if _, ok := s.st.Warm["bbbb2222"]; ok || s.st.Spending != "" || s.errs["bbbb2222"] != "" {
		t.Fatal("records of the removed account remain")
	}
	if _, err := os.Stat(s.profileDir("bbbb2222")); !os.IsNotExist(err) {
		t.Fatal("profile folder still in accounts/")
	}
	if _, err := os.Stat(filepath.Join(s.removedDir("bbbb2222"), "history", "20261101-000000", "config.json")); err != nil {
		t.Fatalf("sign-in not kept: %v", err)
	}
	// 重新加回再移除一次：已有同名文件夹时另存，不覆盖
	s.st.Accounts = append(s.st.Accounts, &Account{UUID: "bbbb2222"})
	write(t, filepath.Join(s.profileDir("bbbb2222"), "config.json"), "{}")
	if err := s.removeAccount("bbbb2222"); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "removed", "bbbb2222*")); len(m) != 2 {
		t.Fatalf("removed folders = %v", m)
	}
	// state.json 已保存
	s2 := newSwitcher(dir)
	setLang(langEN)
	if len(s2.st.Accounts) != 1 {
		t.Fatalf("saved accounts = %d", len(s2.st.Accounts))
	}
}
