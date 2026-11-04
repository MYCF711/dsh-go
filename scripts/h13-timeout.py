#!/usr/bin/env python3
"""H13 超时：问了没人答，到期必须**按拒绝**执行，并且让界面知道是超时。

为什么单独一条：超时这条路径默认永远跑不到（120 秒没人答才算数）。
不给它一个能调小的开关，它就只是"看起来实现了"——而审批的超时是安全边界：
实现错了（比如默认放行），写操作会在没人看着的时候自己执行。
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

BIN = "tmp/serve-dir/DSH GO.exe"
HOME = "tmp/h13timeout"
WAIT_SEC = 3


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
    target = os.path.abspath(os.path.join(HOME, "timeout.txt"))

    env = dict(os.environ)
    env["DSHD_APPROVAL_TIMEOUT_SEC"] = str(WAIT_SEC)
    proc = subprocess.Popen([BIN, "serve", "--home", HOME, "--policy", "ask"],
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, env=env)
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
            env2 = json.loads(op.open(rq).read().decode())
            return env2.get("result", env2)
        except urllib.error.HTTPError as e:
            return {"_http": e.code, "body": json.loads(e.read().decode())}

    try:
        sid = post("session.create", {})["sessionId"]
        post("session.prompt", {"sessionId": sid,
                                "text": "用 write 工具把 ok 写进文件 %s，只做这一步。" % target})

        frames = []
        deadline = time.time() + 150
        while time.time() < deadline:
            time.sleep(1)
            h = post("session.history", {"id": sid, "sinceSeq": 0})
            if isinstance(h, dict) and h.get("_http"):
                continue
            frames = h["frames"]
            types = [f["type"] for f in frames]
            if "turn/done" in types or "turn/error" in types:
                break

        types = [f["type"] for f in frames]
        chk("出现 approval/request", "approval/request" in types, types)
        chk("出现 approval/timeout", "approval/timeout" in types, types)
        chk("超时后文件不存在（按拒绝执行）", not os.path.exists(target))
        # 拒绝的证据（tool/result 里的 isError），防止"文件不存在"只是没试
        refused = any("isError" in json.dumps(f.get("data", {})) and "true" in json.dumps(f.get("data", {}))
                      for f in frames if f["type"] == "tool/result")
        chk("工具执行被判为拒绝", refused)
    finally:
        proc.kill()
        proc.wait(timeout=10)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
