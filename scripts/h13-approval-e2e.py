#!/usr/bin/env python3
"""H13 真机验收：--policy ask 下，允许时文件真的出现，拒绝时真的不存在。

判据**不是**"接口返回 200"，而是磁盘上那个文件在不在。
"点了允许但没执行"和"点了拒绝却执行了"这两种错，只有看文件才抓得到。

只回收自己启动的 PID；临时 home；不碰真实 home。
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


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def start(home):
    p = subprocess.Popen([BIN, "serve", "--home", home, "--policy", "ask"],
                         stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    info = json.loads(re.match(r"ready (\{.*\})", p.stdout.readline().strip()).group(1))
    return p, info


def client(info):
    jar = http.cookiejar.CookieJar()
    op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect())
    try:
        op.open(info["url"] + "/connect?token=" + info["token"])
    except urllib.error.HTTPError:
        pass
    return op


def post(op, info, method, body):
    rq = urllib.request.Request(info["url"] + "/api/" + method,
                                data=json.dumps(body).encode(),
                                headers={"Content-Type": "application/json"}, method="POST")
    try:
        return json.loads(op.open(rq).read().decode())
    except urllib.error.HTTPError as e:
        return {"_http": e.code, "body": json.loads(e.read().decode())}


def run_case(decision, fname):
    home = "tmp/h13-" + decision
    shutil.rmtree(home, ignore_errors=True)
    os.makedirs(home, exist_ok=True)
    work = os.path.abspath(home)
    target = os.path.join(work, fname)

    p, info = start(home)
    op = client(info)
    try:
        sid = post(op, info, "session.create", {})["result"]["sessionId"]
        post(op, info, "session.prompt", {
            "id": sid,
            # 用**绝对路径**：子进程的 cwd 是宿主目录，相对路径会落到意料之外的地方
            # （上一版就写进了仓库根，探针却去 home 里找，白白 FAIL 一次）。
            "text": "用 write 工具把 ok 写进文件 %s，只做这一步，不要解释。" % target,
        })

        answered = None
        deadline = time.time() + 120
        while time.time() < deadline:
            time.sleep(1)
            h = post(op, info, "session.history", {"id": sid, "sinceSeq": 0})
            if h.get("_http"):
                continue
            frames = h["result"]["frames"]
            types = [f["type"] for f in frames]
            if answered is None:
                for f in frames:
                    if f["type"] == "approval/request":
                        rid = json.loads(json.dumps(f["data"]))["requestId"]
                        r = post(op, info, "session.approve",
                                 {"requestId": rid, "optionId": decision})
                        answered = (rid, r)
                        break
            if "turn/done" in types or "turn/error" in types:
                break

        exists = os.path.exists(target)
        # 拒绝时还要证明"是被拒了"，不是"压根没试"——否则文件不存在就是白捡的通过。
        # 拒绝的证据藏在 data.message.isError 里（tool/result 的 v4 形状），
        # 所以直接在原始 JSON 上找，别假设层级。
        refused = any(f["type"] == "tool/result" and "\"isError\":true" in json.dumps(f["data"], separators=(",", ":"))
                      for f in frames)
        if decision == "reject_once":
            ok = (not exists) and refused
        else:
            ok = exists
        print("  %s  决策=%-12s 文件存在=%-5s 工具被拒=%-5s 应答=%s"
              % ("OK  " if ok else "FAIL", decision, exists, refused,
                 "已回" if answered else "未回"))
        return ok
    finally:
        p.kill()
        p.wait(timeout=10)


def main():
    ok = fail = 0
    for decision, fname in (("allow_once", "allow.txt"), ("reject_once", "reject.txt")):
        if run_case(decision, fname):
            ok += 1
        else:
            fail += 1
    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
