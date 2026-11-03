#!/usr/bin/env python3
"""H14：两个会话同时跑，各自的流不能串。

为什么单独一条：单测里我用假的 Hub 验过隔离，但真机上"两条连接 + 两个子进程 +
几十条帧"同时跑，串不串只有实测知道。串了的表现很隐蔽——A 的界面里冒出 B 的内容，
用户会以为是模型说错了话。

判据三条：
  1. 两个会话各自从 seq=1 开始（每会话一条 journal）
  2. A 的连接里不出现 B 的暗号，反之亦然
  3. 两边的最后一帧都是 turn/done（谁也没把对方的结局吞掉）
"""
import base64
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import http.cookiejar

BIN = "tmp/serve-dir/dshd.exe"
HOME = "tmp/h14iso"
MARK_A = "ALPHA-ZQ7"
MARK_B = "BRAVO-K3X"


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

    proc = subprocess.Popen([BIN, "serve", "--home", HOME, "--policy", "allow"],
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

    def sub(s, session_id):
        pl = json.dumps({"t": "sub", "topic": "session:" + session_id, "sinceSeq": 0}).encode()
        mask = b"\x01\x02\x03\x04"
        s.sendall(bytes([0x81, 0x80 | len(pl)]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(pl)))

    def collect(s, out, stop):
        s.settimeout(3)
        try:
            while not stop.is_set():
                c = s.recv(65536)
                if not c:
                    break
                out.append(c)
        except Exception:
            pass

    try:
        sa = post("session.create", {})["sessionId"]
        sb = post("session.create", {})["sessionId"]

        ca = wsconn()
        sub(ca, sa)
        cb = wsconn()
        sub(cb, sb)
        time.sleep(0.3)

        bufa, bufb = [], []
        stop = threading.Event()
        ta = threading.Thread(target=collect, args=(ca, bufa, stop), daemon=True)
        tb = threading.Thread(target=collect, args=(cb, bufb, stop), daemon=True)
        ta.start()
        tb.start()

        # 同时发，制造交错
        post("session.prompt", {"sessionId": sa, "text": "只回答这一个词：%s" % MARK_A})
        post("session.prompt", {"sessionId": sb, "text": "只回答这一个词：%s" % MARK_B})

        deadline = time.time() + 120
        while time.time() < deadline:
            time.sleep(1)
            ta_txt = b"".join(bufa).decode(errors="replace")
            tb_txt = b"".join(bufb).decode(errors="replace")
            if "turn/done" in ta_txt and "turn/done" in tb_txt:
                break

        stop.set()
        ta.join(timeout=5)
        tb.join(timeout=5)
        a = b"".join(bufa).decode(errors="replace")
        b = b"".join(bufb).decode(errors="replace")

        seqs_a = [int(x) for x in re.findall(r'"seq":(\d+)', a) if int(x) > 0]
        seqs_b = [int(x) for x in re.findall(r'"seq":(\d+)', b) if int(x) > 0]

        chk("A 收到自己的帧", len(seqs_a) > 0, seqs_a)
        chk("B 收到自己的帧", len(seqs_b) > 0, seqs_b)
        chk("A 的 seq 从 1 开始（每会话一条 journal）", min(seqs_a) == 1 if seqs_a else False, seqs_a[:6])
        chk("B 的 seq 从 1 开始", min(seqs_b) == 1 if seqs_b else False, seqs_b[:6])
        chk("A 的流里没有 B 的暗号", MARK_B not in a)
        chk("B 的流里没有 A 的暗号", MARK_A not in b)
        chk("两边都收到 turn/done", "turn/done" in a and "turn/done" in b)
    finally:
        proc.kill()
        proc.wait(timeout=10)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
