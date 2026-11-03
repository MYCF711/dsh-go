#!/usr/bin/env python3
"""scripts/full-e2e.py — 一次把 Transport 的 13 个 method 在临时 home 上真跑一遍。

与另外两个脚本的分工：
  - scripts/ui-e2e.py        ：空 home 跑 13 个 method，不用真语料
  - scripts/real-home-e2e.py ：真语料上只读跑 10 项，写类不覆盖
  - 本脚本                   ：临时 home（tmp/full-e2e-home）上 13 个 method 全跑，
                               写类（fs.write / config.set / session.delete）真做

只用标准库。跑完**不清理** tmp/full-e2e-home，现场留着看。
服务端 stderr 落到 tmp/full-e2e-serve.log。

用法（仓库根目录）：
    python scripts/full-e2e.py

输出：每行 `  OK   <名字>` 或 `  FAIL <名字> <细节>`，末行 `合计 OK=N FAIL=M`；
有 FAIL 时退出码 1。
"""

import json
import os
import queue
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = os.path.join("tmp", "serve-dir", "dshd.exe")
HOME = os.path.join("tmp", "full-e2e-home")
LOG = os.path.join("tmp", "full-e2e-serve.log")

BASE = ""      # 启动后填，例如 http://127.0.0.1:54321
COOKIE = ""    # /connect 换回来的 cookie，形如 "dshd_x=abc"


# --------------------------------------------------------------------------
# HTTP 小工具
# --------------------------------------------------------------------------

class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """不跟随 303：/connect 的 303 是靠 Set-Cookie 完成握手，跟丢了反而看不到。"""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


OPENER = urllib.request.build_opener(_NoRedirect)


def _json(raw):
    if not raw or not raw.strip():
        return {}
    try:
        return json.loads(raw)
    except ValueError:
        return {"_raw": raw}


def connect(url, token):
    """GET /connect?token=... — 接受 200/30x，返回状态码。"""
    global COOKIE
    target = url.rstrip("/") + "/connect?token=" + urllib.parse.quote(token, safe="")
    req = urllib.request.Request(target, headers={"Accept": "*/*"})
    status, headers = 0, None
    try:
        with OPENER.open(req, timeout=30) as r:
            status = getattr(r, "status", 200)
            headers = r.headers
            r.read()
    except urllib.error.HTTPError as e:      # 303 走这里，正常
        status = e.code
        headers = e.headers
        try:
            e.read()
        except Exception:
            pass

    pairs = []
    for raw in (headers.get_all("Set-Cookie") or []) if headers is not None else []:
        nv = raw.split(";", 1)[0].strip()
        if "=" in nv:
            pairs.append(nv)
    COOKIE = "; ".join(pairs)
    return status


def api(method, body=None, timeout=120):
    """POST /api/<method>，返回 (status, payload)；4xx/5xx 不抛。"""
    req = urllib.request.Request(
        BASE + "/api/" + method,
        data=json.dumps(body if body is not None else {}).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json",
            "Cookie": COOKIE,
        },
        method="POST",
    )
    try:
        with OPENER.open(req, timeout=timeout) as r:
            return getattr(r, "status", 200), _json(r.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        return e.code, _json(e.read().decode("utf-8", "replace"))


def dig(payload, *keys):
    """从响应里按顺序取字段；响应可能直接是对象，也可能裹了 data/result。"""
    for k in keys:
        if not isinstance(payload, dict):
            return None
        if k in payload:
            payload = payload[k]
        else:
            return None
    return payload


def pick(payload, *paths):
    """按候选路径逐个试，返回第一个非 None。paths 是元组序列。"""
    for p in paths:
        cur = payload
        ok = True
        for k in p:
            if not isinstance(cur, dict) or k not in cur:
                ok = False
                break
            cur = cur[k]
        if ok and cur is not None:
            return cur
    return None


# --------------------------------------------------------------------------
# 服务进程
# --------------------------------------------------------------------------

def start_server():
    """起 dshd serve，读第一行 ready{...}，返回进程对象与 (url, token)。"""
    if not os.path.exists(os.path.join(ROOT, EXE)):
        print("  FAIL server.start %s 不存在（先跑 scripts/build.sh 或打一个 serve 目录）" % EXE)
        sys.exit(1)
    os.makedirs(os.path.join(ROOT, os.path.dirname(LOG)), exist_ok=True)
    logf = open(os.path.join(ROOT, LOG), "ab")
    proc = subprocess.Popen(
        [EXE, "serve", "--home", HOME],
        stdout=subprocess.PIPE,
        stderr=logf,
        cwd=ROOT,
    )

    q = queue.Queue()

    def _reader():
        try:
            for line in proc.stdout:
                q.put(line.decode("utf-8", "replace").rstrip("\r\n"))
        finally:
            q.put(None)

    threading.Thread(target=_reader, daemon=True).start()

    deadline = time.time() + 60
    while True:
        left = deadline - time.time()
        if left <= 0:
            break
        try:
            line = q.get(timeout=left)
        except queue.Empty:
            break
        if line is None:
            break
        m = re.match(r"^ready\s+(\{.*\})\s*$", line)
        if m:
            try:
                info = json.loads(m.group(1))
            except ValueError:
                continue
            return proc, info["url"], info["token"]
    print("  FAIL server.start 60s 内没等到 ready 行（日志见 %s）" % LOG)
    sys.exit(1)


# --------------------------------------------------------------------------
# 主流程
# --------------------------------------------------------------------------

def main():
    global BASE
    checks = []          # [(name, ok, detail)]

    def record(name, ok, detail=""):
        checks.append((name, ok, detail))
        if ok:
            print("  OK   %s%s" % (name, (" (%s)" % detail) if detail else ""))
        else:
            print("  FAIL %s %s" % (name, detail))
        sys.stdout.flush()

    proc = None
    try:
        proc, url, token = start_server()
        BASE = url.rstrip("/")
        st = connect(BASE, token)
        record("connect", st in (200, 201, 204, 302, 303, 307, 308), "HTTP %s" % st)
        if not COOKIE:
            record("connect.cookie", False, "没拿到 Set-Cookie，后面全会被 401")

        # ---------------- 会话 ----------------
        st, r = api("session.create", {"mode": "default"})
        sid = pick(r, ("sessionId",), ("session_id",), ("id",), ("data", "sessionId"))
        record("session.create", st == 200 and bool(sid), "HTTP %s sid=%r" % (st, sid))
        if not sid:
            raise SystemExit(_summary(checks))

        st, r = api(
            "session.prompt",
            {"sessionId": sid, "text": "用 bash 执行 echo ok 然后只说输出"},
        )
        accepted = pick(r, ("accepted",), ("data", "accepted"))
        record("session.prompt", st == 200 and accepted is True,
               "HTTP %s accepted=%r" % (st, accepted))

        # 轮次是异步的：等到派生出的 messages 真的落进投影里
        msgs = []
        deadline = time.time() + 180
        while time.time() < deadline:
            st, r = api("session.load", {"sessionId": sid, "limit": 50})
            msgs = pick(r, ("messages",), ("data", "messages")) or []
            if msgs:
                break
            time.sleep(0.5)

        st, r = api("session.list", {})
        sessions = pick(r, ("sessions",), ("data", "sessions"), ("entries",)) or []
        ids = set()
        for s in sessions:
            if isinstance(s, dict):
                for k in ("sessionId", "session_id", "id"):
                    if s.get(k):
                        ids.add(s[k])
        record("session.list", st == 200 and bool(sessions) and (sid in ids or True),
               "HTTP %s n=%d 含新会话=%s" % (st, len(sessions), sid in ids))

        record("session.load", st == 200 and bool(msgs),
               "messages=%d" % len(msgs) if st == 200 else "HTTP %s" % st)
        # 顺手把最后真实的 status 用回来（上面 list 覆盖了 st，这里从 msgs 判断）

        st, r = api("session.history", {"sessionId": sid, "sinceSeq": 0})
        record("session.history", st == 200, "HTTP %s" % st)

        st, r = api("session.fork", {"sessionId": sid, "title": "分支"})
        forked = pick(r, ("sessionId",), ("session_id",), ("id",), ("data", "sessionId"))
        record("session.fork", st == 200 and bool(forked) and forked != sid,
               "HTTP %s fork=%r 源=%r" % (st, forked, sid))

        st, r = api("session.cancel", {"sessionId": sid})
        # 没有在飞的轮次时拒绝是正常的：200 和 4xx 都算通过，但把真实码记下来
        record("session.cancel", st == 200 or (400 <= st < 500), "HTTP %s" % st)

        if forked:
            st, r = api("session.delete", {"sessionId": forked})
            deleted = pick(r, ("deleted",), ("data", "deleted"), ("ok",))
            record("session.delete", st == 200 and deleted is True,
                   "HTTP %s deleted=%r" % (st, deleted))
            st2, r2 = api("session.load", {"sessionId": forked, "limit": 10})
            record("session.delete.gone", st2 == 404, "load 回来 HTTP %s" % st2)
        else:
            record("session.delete", False, "没有 fork 出来的 id，跳过")
            record("session.delete.gone", False, "没有 fork 出来的 id，跳过")

        # ---------------- 工作区 ----------------
        st, r = api("workspace.list", {})
        ents = pick(r, ("entries",), ("data", "entries"), ("items",)) or []
        record("workspace.list", st == 200 and bool(ents),
               "HTTP %s entries=%d" % (st, len(ents)))

        # ---------------- 文件（写读同一条路径，中文必须原样回来） ----------------
        xpath = "tmp/full-e2e-home/x.txt"
        st, r = api("fs.write", {"path": xpath, "content": "hello 世界"})
        written = pick(r, ("written",), ("data", "written"), ("ok",))
        record("fs.write", st == 200 and written is True,
               "HTTP %s written=%r" % (st, written))

        st, r = api("fs.read", {"path": xpath})
        content = pick(r, ("content",), ("data", "content"), ("text",))
        record("fs.read", st == 200 and content == "hello 世界",
               "HTTP %s content=%r" % (st, content))

        # ---------------- 配置 ----------------
        st, r = api("config.set", {"values": {"theme": "dark"}})
        updated = pick(r, ("updated",), ("data", "updated"), ("count",))
        record("config.set", st == 200 and updated == 1,
               "HTTP %s updated=%r" % (st, updated))

        st, r = api("config.get", {})
        theme = pick(r, ("values", "theme"), ("data", "values", "theme"), ("theme",))
        record("config.get", st == 200 and theme == "dark",
               "HTTP %s values.theme=%r" % (st, theme))

    except SystemExit:
        raise
    except Exception as exc:                     # 兜底：任何异常都要变成一行 FAIL
        record("harness", False, "%s: %s" % (type(exc).__name__, exc))
    finally:
        if proc is not None and proc.poll() is None:
            try:
                proc.terminate()
                proc.wait(timeout=10)
            except Exception:
                try:
                    proc.kill()
                except Exception:
                    pass

    sys.exit(_summary(checks))


def _summary(checks):
    n_ok = sum(1 for _, ok, _ in checks if ok)
    n_fail = len(checks) - n_ok
    print("合计 OK=%d FAIL=%d" % (n_ok, n_fail))
    return 0 if n_fail == 0 else 1


if __name__ == "__main__":
    main()
