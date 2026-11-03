#!/usr/bin/env python3
"""H13 重放：审批问出去之后客户端断线，重连必须还能看到那个问题。

判据不是"没报错"，而是**第二个连接在什么都没发的情况下收到了 approval/request**。
不重放的话，子进程一直在等，界面上却什么都没有——看起来就是卡住了。
"""
import base64
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
import http.cookiejar

BIN = "tmp/serve-dir/dshd.exe"
HOME = "tmp/h13replay"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def main():
    ok = fail = 0

    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1
            print("  OK   " + name)
        else:
            fail += 1
            print("  FAIL " + name + " " + str(detail))

    shutil.rmtree(HOME, ignore_errors=True)
    os.makedirs(HOME, exist_ok=True)

    proc = subprocess.Popen([BIN, "serve", "--home", HOME, "--policy", "ask"],
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    info = json.loads(re.match(r"ready (\{.*\})", proc.stdout.readline().strip()).group(1))
    jar = http.cookiejar.CookieJar()
    op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect())
    try:
        op.open(info["url"] + "/connect?token=" + info["token"])
    except urllib.error.HTTPError:
        pass

    def post(method, body):
        rq = urllib.request.Request(info["url"] + "/api/" + method,
                                    data=json.dumps(body).encode(),
                                    headers={"Content-Type": "application/json"}, method="POST")
        env = json.loads(op.open(rq).read().decode())
        return env.get("result", env)

    host = info["url"].split("//")[1]
    hp, port = host.split(":")
    cookie = "; ".join("%s=%s" % (c.name, c.value) for c in jar)

    def wsconn():
        s = socket.create_connection((hp, int(port)))
        key = base64.b64encode(b"0123456789abcdef").decode()
        req = ("GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
               "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nCookie: %s\r\n\r\n"
               % (host, key, cookie))
        s.sendall(req.encode())
        s.recv(4096)
        return s

    def sub(s, session_id, since=0):
        pl = json.dumps({"t": "sub", "topic": "session:" + session_id, "sinceSeq": since}).encode()
        mask = b"\x01\x02\x03\x04"
        s.sendall(bytes([0x81, 0x80 | len(pl)]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(pl)))

    try:
        sid = post("session.create", {})["sessionId"]

        # 1) 触发一次审批，然后**不回答就断开**
        s1 = wsconn()
        sub(s1, sid)
        time.sleep(0.3)
        post("session.prompt", {"sessionId": sid, "text": "用 write 工具写 tmp/h13replay/x.txt，只做这一步"})
        s1.settimeout(90)
        buf = b""
        try:
            while b"approval/request" not in buf:
                c = s1.recv(65536)
                if not c:
                    break
                buf += c
        except Exception:
            pass
        first = buf.decode(errors="replace")
        chk("第一个连接收到 approval/request", "approval/request" in first)
        s1.close()  # 断线，且不回答

        # 2) 新连接：只订阅，不说话。看它会不会被重放
        time.sleep(1)
        s2 = wsconn()
        sub(s2, sid)
        s2.settimeout(8)
        buf2 = b""
        try:
            while True:
                c = s2.recv(65536)
                if not c:
                    break
                buf2 += c
        except Exception:
            pass
        txt2 = buf2.decode(errors="replace")
        chk("重连后被重放 approval/request", "approval/request" in txt2, txt2[:120])
        rids = re.findall(r'"requestId":"([^"]+)"', txt2)
        chk("重放里带 requestId", len(rids) > 0, str(rids[:1]))
        s2.close()
    finally:
        proc.kill()
        proc.wait(timeout=10)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
