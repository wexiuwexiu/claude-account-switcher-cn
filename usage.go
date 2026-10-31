package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const apiBase = "https://api.anthropic.com"

// Usage is an account's plan usage in percent, as the app shows it.
type Usage struct {
	Session      float64   `json:"session"`
	SessionReset time.Time `json:"sessionReset"`
	Weekly       float64   `json:"weekly"`
	WeeklyReset  time.Time `json:"weeklyReset"`
	CheckedAt    time.Time `json:"checkedAt"`
}

// peak is the fuller of the two limits.
func (u Usage) peak() float64 { return max(u.Session, u.Weekly) }

// current returns usage with windows that have reset since the last check set to zero.
func (u Usage) current(now time.Time) Usage {
	c := u
	if !c.SessionReset.IsZero() && now.After(c.SessionReset) {
		c.Session = 0
	}
	if !c.WeeklyReset.IsZero() && now.After(c.WeeklyReset) {
		c.Weekly = 0
	}
	return c
}

type profile struct {
	Account struct {
		UUID        string `json:"uuid"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		HasMax      bool   `json:"has_claude_max"`
		HasPro      bool   `json:"has_claude_pro"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
		Type string `json:"organization_type"`
	} `json:"organization"`
}

// plan names the subscription. The organization type is current; the
// account flags are only a fallback.
func (p profile) plan() string {
	switch t := strings.TrimPrefix(p.Organization.Type, "claude_"); {
	case t == "max" || t == "pro" || t == "team" || t == "enterprise":
		return strings.ToUpper(t[:1]) + t[1:]
	case p.Account.HasMax:
		return "Max"
	case p.Account.HasPro:
		return "Pro"
	}
	return ""
}

// proxyTransport 让所有请求（用量、更新检查、下载）走系统代理，见 proxy_windows.go。
var proxyTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = proxyFunc
	return t
}()

var client = &http.Client{Timeout: 15 * time.Second, Transport: proxyTransport}

func apiGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-account-switcher/"+version)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(res.Body).Decode(out)
	case http.StatusUnauthorized:
		return errSignedOut
	case http.StatusTooManyRequests:
		wait, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		return &rateLimited{after: time.Duration(wait) * time.Second}
	}
	return fmt.Errorf("%s: %s", path, res.Status)
}

var errSignedOut = errors.New("sign-in expired")

// rateLimited is a 429. after is the server's Retry-After, zero when it gave none.
type rateLimited struct{ after time.Duration }

func (e *rateLimited) Error() string { return "rate limited" }

func fetchProfile(ctx context.Context, token string) (profile, error) {
	var p profile
	err := apiGet(ctx, token, "/api/oauth/profile", &p)
	return p, err
}

func fetchUsage(ctx context.Context, token string) (*Usage, error) {
	type window struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	}
	var body struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
	}
	if err := apiGet(ctx, token, "/api/oauth/usage", &body); err != nil {
		return nil, err
	}
	u := &Usage{CheckedAt: time.Now()}
	if w := body.FiveHour; w != nil {
		u.Session = w.Utilization
		u.SessionReset, _ = time.Parse(time.RFC3339Nano, w.ResetsAt)
	}
	if w := body.SevenDay; w != nil {
		u.Weekly = w.Utilization
		u.WeeklyReset, _ = time.Parse(time.RFC3339Nano, w.ResetsAt)
	}
	return u, nil
}

// accessToken decrypts a saved oauth:tokenCacheV2 value and returns an
// unexpired token of the account that may read its profile and usage.
// Cache keys look like "acct:<account>|<client>:<org>:<audience>:<scopes>".
func accessToken(cacheValue, account string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cacheValue)
	if err != nil {
		return "", err
	}
	plain, err := decryptValue(data)
	if err != nil {
		return "", err
	}
	var cache map[string]struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(plain, &cache); err != nil {
		return "", err
	}
	best, bestExp := "", int64(0)
	now := time.Now().UnixMilli()
	for k, v := range cache {
		if !strings.HasPrefix(k, "acct:"+account+"|") || !strings.Contains(k, "user:profile") {
			continue
		}
		if v.ExpiresAt > now && v.ExpiresAt > bestExp {
			best, bestExp = v.Token, v.ExpiresAt
		}
	}
	if best == "" {
		return "", errSignedOut
	}
	return best, nil
}

// tokenExpiry 返回保存的登录里该账户最早到期的 token 时间。
// 每个 token 都带 refreshToken：到期后 Claude 下次以该账户运行时会自动刷新，所以这不是
// "登录失效"的时间，而是"最好在此之前用一次"的时间；refreshToken 本身的有效期无法得知。
func tokenExpiry(cacheValue, account string) (time.Time, error) {
	data, err := base64.StdEncoding.DecodeString(cacheValue)
	if err != nil {
		return time.Time{}, err
	}
	plain, err := decryptValue(data)
	if err != nil {
		return time.Time{}, err
	}
	var cache map[string]struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	if err := json.Unmarshal(plain, &cache); err != nil {
		return time.Time{}, err
	}
	var earliest int64
	for k, v := range cache {
		if strings.HasPrefix(k, "acct:"+account+"|") && v.ExpiresAt > 0 && (earliest == 0 || v.ExpiresAt < earliest) {
			earliest = v.ExpiresAt
		}
	}
	if earliest == 0 {
		return time.Time{}, errSignedOut
	}
	return time.UnixMilli(earliest), nil
}
