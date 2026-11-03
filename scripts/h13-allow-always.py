#!/usr/bin/env python3
"""H13 `allow_always`：点过"以后都允许"之后，同一会话的同一工具不该再问。

判据：**第二次调用不再出现新的 approval/request**。
只验"文件写出来了"是不够的——文件照样会写出来，只要第二次又问了并又答了允许，
那也是绿的，但用户明明点过"以后都允许"。
"""
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request
import http.cookiejar

BIN = "tmp/serve-dir/dshd.exe"
HOME = "tmp/h13always"


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
    a = os.path.abspath(os.path.join(HOME, "a.txt"))
    b = os.path.abspath(os.path.join(HOME, "b.txt"))

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
        try:
            env = json.loads(op.open(rq).read().decode())
            return env.get("result", env)
        except urllib.error.HTTPError as e:
            return {"_http": e.code, "body": json.loads(e.read().decode())}

    def frames_of(sid):
        h = post("session.history", {"id": sid, "sinceSeq": 0})
        if isinstance(h, dict) and h.get("_http"):
            return []
        return h["frames"]

    try:
        sid = post("session.create", {})["sessionId"]
        # 一句话让它写两个文件：按顺序，第一个批 allow_always，第二个不该再问
        post("session.prompt", {"sessionId": sid,
                                "text": "依次用 write 工具写两个文件：先 %s 内容 ok，再 %s 内容 ok。按顺序做。" % (a, b)})

        answered = set()
        deadline = time.time() + 180
        while time.time() < deadline:
            time.sleep(1)
            fr = frames_of(sid)
            types = [f["type"] for f in fr]
            for f in fr:
                if f["type"] != "approval/request":
                    continue
                rid = json.loads(json.dumps(f["data"]))["requestId"]
                if rid in answered:
                    continue
                answered.add(rid)
                post("session.approve", {"requestId": rid, "optionId": "allow_always"})
            if os.path.exists(a) and os.path.exists(b):
                break
            if "turn/done" in types or "turn/error" in types:
                break

        fr = frames_of(sid)
        reqs = [f for f in fr if f["type"] == "approval/request"]
        chk("两个文件都写出来了", os.path.exists(a) and os.path.exists(b))
        chk("只问了一次审批（allow_always 生效）", len(reqs) == 1, "问了 %d 次" % len(reqs))
    finally:
        proc.kill()
        proc.wait(timeout=10)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
