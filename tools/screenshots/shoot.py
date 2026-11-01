"""README 截图：用假账户数据渲染 dashboard.html，生成中文浅色 / 深色 / 切换确认和英文浅色 / 切换确认 PNG。

做什么：用 Playwright 打开 dashboard.html，拦截 api/state 返回虚构数据（不读本机任何账户），
       注入原生窗口的三个函数让页面按原生浮窗样式渲染，按页面上报的高度截图。
用法：python -I tools/screenshots/shoot.py   （在仓库根目录运行，输出到 assets/）
注意：依赖 playwright 和本机 Edge（channel="msedge"）；数据里的名字和邮箱全是假的。
"""
import json
import pathlib
import sys
from datetime import datetime, timedelta, timezone

from playwright.sync_api import sync_playwright

ROOT = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
HTML = (ROOT / "dashboard.html").read_text(encoding="utf-8")
BASE = "http://demo.test/x/"
WIDTH = 520


def iso(dt):
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def fake_state(now, lang="zh"):
    """虚构 4 个账户：当前号用得快、一个周额度快到期（会被推荐）、一个 5 小时用满、一个仅网页登录。"""
    h = lambda x: now + timedelta(hours=x)
    return {
        "now": iso(now), "lang": lang, "threshold": 96, "warnAt": 85, "autoSwitch": False, "busy": False,
        "accounts": [
            {"uuid": "a", "label": "Work (Pro)", "email": "work@example.com", "active": True,
             "session": {"pct": 62, "reset": iso(h(2.2))}, "weekly": {"pct": 41, "reset": iso(h(75))},
             "checkedAt": iso(now - timedelta(minutes=1)), "loginExpires": iso(h(24 * 25)), "staleAfter": 300},
            {"uuid": "b", "label": "Personal (Pro)", "email": "me@example.com", "active": False,
             "session": {"pct": 0}, "weekly": {"pct": 18, "reset": iso(h(20))},
             "checkedAt": iso(now - timedelta(minutes=4)), "loginExpires": iso(h(24 * 21)), "staleAfter": 900},
            {"uuid": "c", "label": "Side (Pro)", "email": "side@example.com", "active": False,
             "session": {"pct": 97, "reset": iso(h(1.5))}, "weekly": {"pct": 55, "reset": iso(h(118))},
             "checkedAt": iso(now - timedelta(minutes=6)), "loginExpires": iso(h(24 * 4)), "staleAfter": 900},
            {"uuid": "d", "label": "Trial", "email": "trial@example.com", "active": False, "pending": True,
             "problem": "web sign-in only; usage shows after Pro + one use of the Code tab", "staleAfter": 900},
        ],
        "bigChats": [{"title": "重构数据管道", "context": 412000}, {"title": "回测框架", "context": 186000}] if lang == "zh"
        else [{"title": "Refactor data pipeline", "context": 412000}, {"title": "Backtest framework", "context": 186000}],
    }


# 让 NATIVE 为真（显示固定 / 关闭按钮），并记录页面上报的高度
STUBS = "window.hideDashboard=()=>{};window.pinDashboard=()=>{};window.fitDashboard=h=>{window.__h=h};"


def shoot(browser, scheme, out, confirm=None, lang="zh"):
    page = browser.new_page(viewport={"width": WIDTH, "height": 900}, device_scale_factor=2, color_scheme=scheme)
    page.add_init_script(STUBS)
    state = json.dumps(fake_state(datetime.now(timezone.utc), lang))

    def handle(route):
        url = route.request.url
        if url.endswith("api/state"):
            route.fulfill(status=200, content_type="application/json", body=state)
        elif url == BASE:
            route.fulfill(status=200, content_type="text/html; charset=utf-8", body=HTML)
        else:
            route.fulfill(status=404, body="")

    page.route("http://demo.test/**", handle)
    page.goto(BASE)
    page.wait_for_function("window.__h > 0")
    page.wait_for_timeout(1200)  # 等圆环动画播完
    page.set_viewport_size({"width": WIDTH, "height": page.evaluate("window.__h")})
    if confirm:
        page.click(f'.row[data-uuid="{confirm}"]')
        page.wait_for_timeout(500)
    page.screenshot(path=str(out))
    page.close()
    print("wrote", out)


with sync_playwright() as p:
    b = p.chromium.launch(channel="msedge")
    shoot(b, "light", ROOT / "assets" / "dashboard-light.png")
    shoot(b, "dark", ROOT / "assets" / "dashboard-dark.png")
    shoot(b, "light", ROOT / "assets" / "dashboard-confirm.png", confirm="b")
    shoot(b, "light", ROOT / "assets" / "dashboard-light-en.png", lang="en")
    shoot(b, "light", ROOT / "assets" / "dashboard-confirm-en.png", confirm="b", lang="en")
    b.close()
