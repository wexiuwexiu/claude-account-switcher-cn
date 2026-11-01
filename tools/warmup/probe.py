"""预热验证：用某账号的 setup-token 发一次最小 Haiku 请求，看它的 5 小时窗口是否开始计时。

做什么：隐藏输入 token（不显示、不保存），确认 token 对应的邮箱，读 switcher 的用量，
       发一次精简 claude -p，然后每 30 秒再读一次用量，直到出现 5 小时重置时间（最多 3 分钟）。
用法：先 `claude setup-token` 拿到 token，再运行 `python -I tools/warmup/probe.py`。
注意：token 只放在本进程的环境变量里传给 claude；switcher 读用量用的是它自己保存的登录。
"""
import getpass
import json
import os
import pathlib
import re
import subprocess
import time

REPO = pathlib.Path(__file__).resolve().parents[2]
EXE = REPO / "claude-account-switcher.exe"
STATE = pathlib.Path.home() / ".claude-account-switcher" / "state.json"
PING = ["claude", "-p", "hi", "--model", "haiku", "--tools", "", "--system-prompt", "Reply with one word.",
        "--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands",
        "--no-session-persistence", "--output-format", "json"]


def run(cmd, env=None):
    return subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8", env=env, timeout=120, shell=False)


def usage_line(label):
    """switcher -status 里这个账户的一行，例如 'wexiuk (Pro)  5h 0% · week 14%, resets Sat 04:59'。"""
    for line in run([str(EXE), "-status"]).stdout.splitlines():
        if re.match(r"^[* ] " + re.escape(label) + r" \(", line):
            return line.strip()
    return "(switcher 里没有这个账户)"


token = getpass.getpass("粘贴 setup-token（输入不显示）: ").strip()
env = dict(os.environ, CLAUDE_CODE_OAUTH_TOKEN=token)
who = json.loads(run(["claude", "auth", "status"], env).stdout or "{}")
email = who.get("email", "")
labels = {a.get("email"): a.get("name") for a in json.load(open(STATE, encoding="utf-8")).get("accounts", [])}
label = labels.get(email)
print("token 对应账户:", email or "(未知)", "->", label or "(不在 switcher 里)")
if not label:
    raise SystemExit("这个邮箱不在 switcher 的账户里，无法对比用量")

before = usage_line(label)
print(time.strftime("%H:%M:%S"), "预热前:", before)
if ", resets" in before.split("·")[0] or " until " in before.split("·")[0]:
    print("注意：这个账户的 5 小时窗口已经在计时，看不出预热是否开窗。最好换一个 5h 0% 的账户。")

out = run(PING, env)
try:
    d = json.loads(out.stdout)
    u = d.get("usage", {})
    print(time.strftime("%H:%M:%S"), "预热回复:", d.get("result"), "| 输入", u.get("input_tokens"),
          "缓存写入", u.get("cache_creation_input_tokens"), "输出", u.get("output_tokens"), "| 标价 $", d.get("total_cost_usd"))
except json.JSONDecodeError:
    raise SystemExit("预热失败: " + (out.stdout + out.stderr)[-500:])

for _ in range(6):
    time.sleep(30)
    after = usage_line(label)
    print(time.strftime("%H:%M:%S"), "预热后:", after)
    if ", resets" in after.split("·")[0]:
        print("结论：5 小时窗口已开始计时。")
        break
else:
    print("结论：3 分钟内没看到 5 小时重置时间（可能是 API 延迟，稍后再看 switcher）。")
