package main

import (
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Windows 系统代理：Go 默认只认 HTTP_PROXY 等环境变量，不读"设置 > 网络 > 代理"
// 里的手动代理（Clash、v2rayN 等开"系统代理"时写的就是这里），导致 api.anthropic.com 直连超时。
// 这里优先用环境变量，其次读 HKCU 的 Internet Settings；PAC 自动配置脚本不支持，只记日志。

const inetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

type sysProxy struct {
	mu      sync.Mutex
	readAt  time.Time
	http    *url.URL // 用于 http:// 请求
	https   *url.URL // 用于 https:// 请求
	bypass  []string
	pacSeen bool
}

var winProxy sysProxy

// proxyFunc 给 http.Transport.Proxy 用。系统设置每 30 秒重读一次，切换代理开关后无需重启。
func proxyFunc(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
		return u, err
	}
	return winProxy.forRequest(req)
}

func (p *sysProxy) forRequest(req *http.Request) (*url.URL, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.readAt) > 30*time.Second {
		p.reload()
	}
	if p.bypassed(req.URL.Hostname()) {
		return nil, nil
	}
	if req.URL.Scheme == "https" {
		return p.https, nil
	}
	return p.http, nil
}

// reload 读注册表。ProxyServer 有两种写法：
// "127.0.0.1:7890"（所有协议同一个）或 "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891"。
func (p *sysProxy) reload() {
	p.readAt = time.Now()
	p.http, p.https, p.bypass = nil, nil, nil
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettings, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if pac, _, _ := k.GetStringValue("AutoConfigURL"); pac != "" && !p.pacSeen {
		p.pacSeen = true
		log.Printf("system proxy uses a PAC script (%s), which is not supported; set a manual proxy or HTTPS_PROXY", pac)
	}
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	p.http, p.https, p.bypass = parseProxyServer(server, override)
}

// parseProxyServer 解析 ProxyServer / ProxyOverride，独立出来便于测试。
func parseProxyServer(server, override string) (httpProxy, httpsProxy *url.URL, bypass []string) {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, nil, nil
	}
	mk := func(scheme, hostport string) *url.URL {
		hostport = strings.TrimSpace(hostport)
		if hostport == "" {
			return nil
		}
		if strings.Contains(hostport, "://") {
			u, err := url.Parse(hostport)
			if err != nil {
				return nil
			}
			return u
		}
		return &url.URL{Scheme: scheme, Host: hostport}
	}
	if !strings.Contains(server, "=") {
		u := mk("http", server)
		httpProxy, httpsProxy = u, u
	} else {
		var socks *url.URL
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "http":
				httpProxy = mk("http", v)
			case "https":
				httpsProxy = mk("http", v)
			case "socks":
				socks = mk("socks5", v)
			}
		}
		if httpsProxy == nil {
			httpsProxy = httpProxy
		}
		if httpsProxy == nil {
			httpsProxy = socks
		}
		if httpProxy == nil {
			httpProxy = httpsProxy
		}
	}
	for _, b := range strings.Split(override, ";") {
		if b = strings.ToLower(strings.TrimSpace(b)); b != "" {
			bypass = append(bypass, b)
		}
	}
	return httpProxy, httpsProxy, bypass
}

// bypassed 按 ProxyOverride 判断是否直连：<local> 表示不带点的主机名，其余支持 * 通配。
func (p *sysProxy) bypassed(host string) bool {
	return proxyBypassed(strings.ToLower(host), p.bypass)
}

func proxyBypassed(host string, bypass []string) bool {
	for _, b := range bypass {
		if b == "<local>" {
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(b, host); ok {
			return true
		}
	}
	return false
}
