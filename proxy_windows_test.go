package main

import "testing"

func TestParseProxyServer(t *testing.T) {
	h, s, b := parseProxyServer("127.0.0.1:7890", "localhost;127.*;<local>")
	if h.String() != "http://127.0.0.1:7890" || s.String() != "http://127.0.0.1:7890" || len(b) != 3 {
		t.Fatalf("single: %v %v %v", h, s, b)
	}
	if !proxyBypassed("127.0.0.1", b) || !proxyBypassed("intranet", b) || proxyBypassed("api.anthropic.com", b) {
		t.Fatal("bypass rules")
	}
	h, s, _ = parseProxyServer("http=127.0.0.1:7890;https=127.0.0.1:7891", "")
	if h.Host != "127.0.0.1:7890" || s.Host != "127.0.0.1:7891" {
		t.Fatalf("per scheme: %v %v", h, s)
	}
	h, s, _ = parseProxyServer("socks=127.0.0.1:7891", "")
	if s.String() != "socks5://127.0.0.1:7891" || h != s {
		t.Fatalf("socks only: %v %v", h, s)
	}
	if h, s, _ := parseProxyServer("", ""); h != nil || s != nil {
		t.Fatal("empty")
	}
}
