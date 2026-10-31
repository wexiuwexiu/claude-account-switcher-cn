package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 切号前压缩提醒用：找出最近在用的大对话。
// 提示缓存按账号隔离，切号后续聊旧对话要把整段上下文在新账号里重写一遍
// （实测切号后第一条命中率 7%，每个对话平均重写约 26 万 token）。
// 数据只读本机两处文件，不联网：
//   - Claude 的对话索引 claude-code-sessions/<账户>/<组织>/local_<id>.json：标题、cliSessionId
//   - Claude Code 对话记录 ~/.claude/projects/*/<cliSessionId>.jsonl：每次请求的时间和 token 用量

// 只读对话记录末尾这么多字节找最后一次请求，避免整读几十 MB 的文件。
const tailBytes = 1 << 20

// chatInfo 是一个对话最后一次请求的情况。
type chatInfo struct {
	Title       string
	LastRequest time.Time
	Context     int // 最后一次请求的上下文 token 数
}

// claudeProjectsDir 是 Claude Code 对话记录目录，尊重 CLAUDE_CONFIG_DIR。
func claudeProjectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// transcriptPaths 缓存 cliSessionId -> 对话记录路径，避免每次全盘 glob。
var transcriptPaths sync.Map

func findTranscript(projectsRoot, cliID string) string {
	if p, ok := transcriptPaths.Load(cliID); ok {
		if _, err := os.Stat(p.(string)); err == nil {
			return p.(string)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(projectsRoot, "*", cliID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	transcriptPaths.Store(cliID, matches[0])
	return matches[0]
}

// recentChats 返回 within 时间内有过请求的对话，上下文从大到小排列。
func recentChats(sessionsRoot, projectsRoot string, now time.Time, within time.Duration) []chatInfo {
	type entry struct {
		Title          string `json:"title"`
		CliSessionID   string `json:"cliSessionId"`
		LastActivityAt int64  `json:"lastActivityAt"`
		IsArchived     bool   `json:"isArchived"`
	}
	// 同一个对话会被复制到每个账户目录：按 cliSessionId 去重，标题取最新的那份。
	latest := map[string]entry{}
	paths, _ := filepath.Glob(filepath.Join(sessionsRoot, "*", "*", "local_*.json"))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || now.Sub(info.ModTime()) > within+time.Hour {
			continue // 先按文件修改时间粗筛
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var e entry
		if json.Unmarshal(data, &e) != nil || e.CliSessionID == "" || e.IsArchived {
			continue
		}
		if cur, ok := latest[e.CliSessionID]; !ok || e.LastActivityAt > cur.LastActivityAt {
			latest[e.CliSessionID] = e
		}
	}
	var out []chatInfo
	for cli, e := range latest {
		tr := findTranscript(projectsRoot, cli)
		if tr == "" {
			continue
		}
		at, ctx, ok := lastRequest(tr)
		if !ok || now.Sub(at) > within {
			continue
		}
		title := e.Title
		if title == "" {
			title = "未命名对话"
		}
		out = append(out, chatInfo{Title: title, LastRequest: at, Context: ctx})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Context > out[j].Context })
	return out
}

// lastRequest 从对话记录末尾找最后一次主线程请求（跳过子代理 sidechain），
// 返回时间和上下文大小 = input + cache_read + cache_creation。
func lastRequest(path string) (time.Time, int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, 0, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}, 0, false
	}
	start := max(info.Size()-tailBytes, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return time.Time{}, 0, false
	}
	type line struct {
		Type        string `json:"type"`
		Subtype     string `json:"subtype"`
		Timestamp   string `json:"timestamp"`
		IsSidechain bool   `json:"isSidechain"`
		// /compact 写入的 compact_boundary 记录，postTokens 是压缩后的消息部分大小
		Compact *struct {
			PostTokens int `json:"postTokens"`
		} `json:"compactMetadata"`
		Message struct {
			Usage *struct {
				Input         int `json:"input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	var best time.Time
	bestCtx := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	skipFirst := start > 0 // 从文件中间开始读，第一行可能不完整
	for sc.Scan() {
		b := sc.Bytes()
		if skipFirst {
			skipFirst = false
			continue
		}
		isCompact := bytes.Contains(b, []byte(`"compact_boundary"`))
		if !isCompact && !bytes.Contains(b, []byte(`"usage"`)) {
			continue
		}
		var l line
		if json.Unmarshal(b, &l) != nil || l.IsSidechain {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil || t.Before(best) {
			continue
		}
		switch {
		case l.Type == "assistant" && l.Message.Usage != nil:
			u := l.Message.Usage
			best, bestCtx = t, u.Input+u.CacheRead+u.CacheCreation
		case l.Type == "system" && l.Subtype == "compact_boundary" && l.Compact != nil:
			// 刚压缩完、还没发新消息时，没有新的 usage；用压缩后大小，
			// 否则切换确认面板会一直按压缩前的几十万显示代价。
			// 注意：postTokens 不含系统提示和工具（约 3～5 万），只用于判断是否还算大对话。
			best, bestCtx = t, l.Compact.PostTokens
		}
	}
	return best, bestCtx, !best.IsZero()
}

// tokensText 把 token 数写成"45.2万"这样的中文习惯格式。
func tokensText(n int) string {
	if n >= 10_000 {
		return strconv.FormatFloat(float64(n)/10_000, 'f', 1, 64) + "万"
	}
	return strconv.Itoa(n)
}

// shortTitle 截断过长的对话标题，通知里放得下。
func shortTitle(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 18 {
		return string(r[:18]) + "…"
	}
	return string(r)
}
