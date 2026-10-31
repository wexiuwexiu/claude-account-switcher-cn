package main

import (
	"encoding/base64"
	"encoding/binary"
	"log"
	"os"
	"os/exec"
	"unicode/utf16"
)

// Windows 系统通知：用 PowerShell 调 WinRT ToastNotificationManager，不引入新依赖。
// 标题和正文通过环境变量传入并在脚本里做 XML 转义，避免拼接注入。
// 通知来源会显示为 "Windows PowerShell"（借用它的 AppUserModelID，否则需要注册快捷方式）。
const toastScript = `$ErrorActionPreference='Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$t = [Security.SecurityElement]::Escape($env:CAS_TOAST_TITLE)
$b = [Security.SecurityElement]::Escape($env:CAS_TOAST_BODY)
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml("<toast scenario='reminder'><visual><binding template='ToastGeneric'><text>$t</text><text>$b</text></binding></visual><actions><action content='知道了' arguments='dismiss' activationType='system'/></actions></toast>")
$n = [Windows.UI.Notifications.ToastNotification]::new($x)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($n)
`

// notify 弹出系统通知，失败只记日志。会阻塞到 PowerShell 退出（约 1 秒），调用方应放到 goroutine。
func notify(title, body string) {
	u := utf16.Encode([]rune(toastScript))
	buf := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[2*i:], c)
	}
	cmd := hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(buf)))
	cmd.Env = append(os.Environ(), "CAS_TOAST_TITLE="+title, "CAS_TOAST_BODY="+body)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("notify: %v %s", err, out)
	}
}
