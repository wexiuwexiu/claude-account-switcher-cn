package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// A profile is a saved sign-in: the account's config.json entries and the
// app's cookie store. Tokens stay encrypted by the app's own key, the
// switcher never writes them in plain text.

// saveProfile copies the current sign-in into dir. The app must be closed.
func saveProfile(d desktop, dir string) error {
	cfg, err := readObject(d.configPath())
	if err != nil {
		return err
	}
	saved := &object{vals: map[string]json.RawMessage{}}
	for _, k := range authKeys {
		if v, ok := cfg.get(k); ok {
			saved.set(k, v)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := saved.write(filepath.Join(dir, "config.json")); err != nil {
		return err
	}
	for _, f := range d.cookieFiles() {
		if err := copyFile(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			return err
		}
	}
	return nil
}

// 登录历史：每次覆盖账户的保存登录前，把旧的那份移到 history/<时间>/，保留最近 keepHistory 份。
// 对邮箱无法找回的账户，这是登录失效时唯一的退路。
const keepHistory = 5

// profileFiles 是一份保存的登录包含的文件：config.json 和 Cookie 文件。
func profileFiles(d desktop, dir string) []string {
	files := []string{filepath.Join(dir, "config.json")}
	for _, f := range d.cookieFiles() {
		files = append(files, filepath.Join(dir, filepath.Base(f)))
	}
	return files
}

// archiveProfile 把 dir 里现有的登录复制到 dir/history/<时间>/。内容和最近一份历史相同则跳过。
func archiveProfile(d desktop, dir string, now time.Time) error {
	cur, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // 第一次保存，没有旧登录
		}
		return err
	}
	histRoot := filepath.Join(dir, "history")
	names := historyNames(histRoot)
	if len(names) > 0 {
		last, err := os.ReadFile(filepath.Join(histRoot, names[len(names)-1], "config.json"))
		if err == nil && string(last) == string(cur) {
			return nil
		}
	}
	dst := filepath.Join(histRoot, now.Format("20060102-150405"))
	for _, f := range profileFiles(d, dir) {
		if _, err := os.Stat(f); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyFile(f, filepath.Join(dst, filepath.Base(f))); err != nil {
			return err
		}
	}
	names = historyNames(histRoot)
	for len(names) > keepHistory {
		_ = os.RemoveAll(filepath.Join(histRoot, names[0]))
		names = names[1:]
	}
	return nil
}

func historyNames(root string) []string {
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// profileExpiry 是 config.json 里该账户 token 的过期时间；没有 token 或读不出时为零值。
func profileExpiry(configPath, account string) time.Time {
	cache, err := tokenCacheOf(configPath)
	if err != nil {
		return time.Time{}
	}
	exp, _ := tokenExpiry(cache, account)
	return exp
}

// restoreProfile puts a saved sign-in back. The app must be closed.
func restoreProfile(d desktop, dir string) error {
	saved, err := readObject(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	cfg, err := readObject(d.configPath())
	if err != nil {
		return err
	}
	for _, k := range authKeys {
		if v, ok := saved.get(k); ok {
			cfg.set(k, v)
		} else {
			cfg.del(k)
		}
	}
	for _, f := range d.cookieFiles() {
		if err := copyFile(filepath.Join(dir, filepath.Base(f)), f); err != nil {
			return err
		}
	}
	return cfg.write(d.configPath())
}

// clearSignIn removes the current sign-in locally so the app shows its login
// screen. It does not sign out, which would revoke the saved tokens.
func clearSignIn(d desktop) error {
	cfg, err := readObject(d.configPath())
	if err != nil {
		return err
	}
	for _, k := range authKeys {
		cfg.del(k)
	}
	for _, f := range d.cookieFiles() {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return cfg.write(d.configPath())
}

const keepBackups = 10

// backup copies config.json and the cookie store before a switch touches them.
func backup(d desktop, root string) (string, error) {
	dir := filepath.Join(root, time.Now().Format("20060102-150405"))
	files := append([]string{d.configPath()}, d.cookieFiles()...)
	for _, f := range files {
		if err := copyFile(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			return "", err
		}
	}
	pruneBackups(root)
	return dir, nil
}

// restoreBackup undoes a switch that failed halfway.
func restoreBackup(d desktop, dir string) error {
	files := append([]string{d.configPath()}, d.cookieFiles()...)
	for _, f := range files {
		if err := copyFile(filepath.Join(dir, filepath.Base(f)), f); err != nil {
			return err
		}
	}
	return nil
}

func pruneBackups(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keepBackups {
		_ = os.RemoveAll(filepath.Join(root, names[0]))
		names = names[1:]
	}
}
