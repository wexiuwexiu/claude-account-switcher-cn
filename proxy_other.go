//go:build !windows

package main

import "net/http"

// 非 Windows 平台只认 HTTP_PROXY / HTTPS_PROXY 环境变量。
var proxyFunc = http.ProxyFromEnvironment
