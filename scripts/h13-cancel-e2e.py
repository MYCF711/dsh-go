#!/usr/bin/env python3
"""H13 pending cancel 真机验收：审批还挂着的时候客户端发 `session.cancel`。

判据四件事（缺一条都不算收口）：

  ① 那个挂着的审批按**拒绝**结算，界面收到明确的一帧 `approval/cancel`
     （契约 `docs/transport-contract.md` §2 的 type 白名单里就叫这个词）；
  ② 对应工具**没有执行**——写类工具的判据是磁盘上那个文件在不在；
  ③ 取消之后**第二轮 prompt 还能跑**（拿到 assistant/message）；
  ④ 第二轮没有撞上"会话正被另一个进程写入"——子进程被 TerminateProcess 杀掉时
     不会删自己的会话写锁，那份锁没人清就会把整个会话锁死。

判据**不是**"接口返回 200"。

只回收自己启动的 PID；临时 home 在 tmp/ 下；不碰真实 home、不碰别人的端口。
"""
import base64
import http.cookiejar
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

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(ROOT, "tmp", "serve-dir", "DSH GO.exe")
RUN = os.path.join(ROOT, "tmp", "h13-cancel")
HOME = os.path.join(RUN, "home")
SERVE_LOG = os.path.join(RUN, "serve.log")

# 工具看的路径一律**相对仓库根**：仓库路径里有空格（"DeepSeek Harness"），
# 绝对路径一旦被模型拼进命令就会在空格处断掉，而那种红看起来像"工具没执行"。
REL_TARGET = "tmp/h13-cancel/home/canceled.txt"
TARGET = os.path.join(ROOT, REL_TARGET)

APPROVAL_WAIT = 120   # 等模型决定调工具（它得先想清楚要用 write）
TURN_WAIT = 180       # 取消之后等这一轮收尾
SECOND_WAIT = 240     # 第二轮：真模型，慢是常态，但慢不等于没有上限


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def chk(ok, fail, name, cond, detail=""):
    if cond:
        print("  OK   " + name)
        return ok + 1, fail
    print("  FAIL " + name + "  " + str(detail))
    return ok, fail + 1


def main():
    ok = fail = 0
    if not os.path.exists(BIN):
        print("缺二进制 %s（先 go build -o \"tmp/DSH GO.exe\" ./cmd/dshd）" % BIN)
        return 1
    shutil.rmtree(RUN, ignore_errors=True)
    os.makedirs(HOME, exist_ok=True)
    if os.path.exists(TARGET):
        os.remove(TARGET)

    logf = open(SERVE_LOG, "w", encoding="utf-8", errors="replace")
    proc = subprocess.Popen([BIN, "serve", "--home", HOME, "--policy", "ask"],
                            stdout=subprocess.PIPE, stderr=logf, text=True)
    info = None
    try:
        line = proc.stdout.readline().strip()
        m = re.match(r"ready (\{.*\})", line)
        if not m:
            print("宿主没打出 ready 行：%r" % line)
            return 1
        info = json.loads(m.group(1))

        jar = http.cookiejar.CookieJar()
        op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect())
        try:
            op.open(info["url"] + "/connect?token=" + info["token"])
        except urllib.error.HTTPError:
            pass

        def post(method, body):
            rq = urllib.request.Request(info["url"] + "/api/" + method,
                                        data=json.dumps(body).encode(),
                                        headers={"Content-Type": "application/json"},
                                        method="POST")
            try:
                return json.loads(op.open(rq).read().decode())
            except urllib.error.HTTPError as e:
                return {"_http": e.code, "body": json.loads(e.read().decode())}

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
            s.sendall(bytes([0x81, 0x80 | len(pl)]) + mask +
                      bytes(b ^ mask[i % 4] for i, b in enumerate(pl)))

        # WS 服务端帧是**未掩码**的文本帧，payload 原样躺在字节流里，
        # 所以直接在累积缓冲上找 JSON 子串就够（Go 的 marshal 不带空格）。
        state = {"buf": b""}

        def pump(s, deadline):
            """把 socket 上的东西读进缓冲，直到 deadline 或对端关闭。"""
            s.settimeout(1.0)
            while time.time() < deadline:
                try:
                    c = s.recv(65536)
                except socket.timeout:
                    continue
                except OSError:
                    return
                if not c:
                    return
                state["buf"] += c

        def wait_for(needle, timeout, since=0):
            """等到缓冲（since 之后）出现 needle，返回出现位置（找不到返回 -1）。"""
            end = time.time() + timeout
            while time.time() < end:
                i = state["buf"].find(needle.encode(), since)
                if i >= 0:
                    return i
                pump(s, min(time.time() + 1.0, end))
            return -1

        sid = post("session.create", {})["result"]["sessionId"]
        s = wsconn()
        sub(s, sid)
        time.sleep(0.3)

        # ── 1. 触发一次审批，然后**不回答**
        post("session.prompt", {"id": sid, "text":
              "用 write 工具把 ok 写进文件 %s，只做这一步，不要解释。" % REL_TARGET})
        i_req = wait_for('"type":"approval/request"', APPROVAL_WAIT)
        ok, fail = chk(ok, fail, "出现 approval/request", i_req >= 0,
                       "模型/审批链路没起来（后续判据无意义）")
        if i_req < 0:
            return finish(ok, fail)

        # 抓到挂起的那条 requestId：approval/cancel 必须点名同一条。
        head = state["buf"][:i_req + 4000].decode("utf-8", "replace")
        m = re.findall(r'"requestId":"([^"]+)"', head)
        rid = m[-1] if m else ""
        mark = len(state["buf"])

        # ── 2. cancel：没人应答的那条审批还挂着
        resp = post("session.cancel", {"id": sid})
        cancelled = bool(resp.get("result", {}).get("cancelled"))
        ok, fail = chk(ok, fail, "session.cancel 返回 cancelled", cancelled, resp)

        # ── 3. 界面上要看得到明确的一帧 approval/cancel
        i_cancel = wait_for('"type":"approval/cancel"', 30, since=mark)
        detail = state["buf"][mark:mark + 400].decode("utf-8", "replace")
        ok, fail = chk(ok, fail, "出现 approval/cancel", i_cancel >= 0, detail[:200])
        if i_cancel >= 0:
            seg = state["buf"][i_cancel:i_cancel + 4000].decode("utf-8", "replace")
            ok, fail = chk(ok, fail, "approval/cancel 点名了那条 requestId",
                           bool(rid) and rid in seg, "想找 %s，帧里没有" % rid)

        # ── 4. 等第一轮收尾，再看磁盘：工具**不许执行**
        end = time.time() + TURN_WAIT
        while time.time() < end:
            pump(s, min(time.time() + 2.0, end))
            tail = state["buf"][mark:].decode("utf-8", "replace")
            if ('"type":"turn/error"' in tail or '"type":"turn/done"' in tail
                    or '"type":"turn/end"' in tail):
                break
        time.sleep(1.0)  # 落盘那一拍
        ok, fail = chk(ok, fail, "取消后文件不存在（工具没执行）", not os.path.exists(TARGET),
                       TARGET)

        # 被杀掉的子进程不会删自己的会话写锁（TerminateProcess 连 defer 都不跑）。
        # 残留一把锁，下一轮的子进程就会报"会话正被另一个进程写入"——
        # 界面上是"取消一次，这个会话再也发不出去了"。
        locks = [os.path.join(d, f) for d, _, fs in os.walk(HOME) for f in fs if f.endswith(".lock")]
        ok, fail = chk(ok, fail, "第一轮收工后没有残留的会话写锁", not locks, locks)

        # ── 5. 第二轮：取消不能把会话搞死
        mark2 = len(state["buf"])
        r2 = post("session.prompt", {"id": sid, "text": "只用一句话回答：1+1 等于几？不要调用任何工具。"})
        accepted = bool(r2.get("result", {}).get("accepted"))
        ok, fail = chk(ok, fail, "取消之后第二轮 prompt 被接受", accepted, r2)

        i_msg = wait_for('"type":"assistant/message"', SECOND_WAIT, since=mark2)
        tail2 = state["buf"][mark2:].decode("utf-8", "replace")
        ok, fail = chk(ok, fail, "第二轮拿到 assistant 消息", i_msg >= 0,
                       tail2[:300] if i_msg < 0 else "")
        ok, fail = chk(ok, fail, "第二轮没撞上会话写锁（没被搞死）",
                       "会话正被另一个进程写入" not in tail2,
                       "第二轮报：%s" % tail2[:300])
        return finish(ok, fail)
    finally:
        try:
            if info:
                pass
        finally:
            proc.kill()
            proc.wait(timeout=10)
            logf.close()


def finish(ok, fail):
    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
