#!/usr/bin/env python3
"""H10：同一新二进制 + 同一临时 home 的整条链。

create → list → load（每个样本 id）→ prompt → 再 load。
**只回收自己启动的那个 PID**（不按镜像名杀）。
"""
import json, os, re, subprocess, sys, time, http.cookiejar, urllib.request, urllib.error

HOME = "tmp/h10chain"
BIN = "tmp/serve-dir/DSH GO.exe"

# 空会话上第一次 prompt 可能要等模型先做一轮工具调用才落下 assistant/message。
# 这个等待窗口必须**明显短于套件的超时**，否则超时后套件拿到的是"没有汇总行"，
# 看起来像探针崩了——而不是"这条断言没等到"。
WARMUP_SEC = 120

def main():
    os.system("rm -rf " + HOME)
    p = subprocess.Popen([BIN, "serve", "--home", HOME, "--policy", "allow"],
                         stdout=subprocess.PIPE, text=True, stderr=subprocess.DEVNULL)
    try:
        info = json.loads(re.match(r"ready (\{.*\})", p.stdout.readline().strip()).group(1))
        jar = http.cookiejar.CookieJar()

        class NR(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *a, **k):
                return None

        op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NR)
        try:
            op.open(info["url"] + "/connect?token=" + info["token"])
        except urllib.error.HTTPError:
            pass

        def post(m, body):
            rq = urllib.request.Request(info["url"] + "/api/" + m,
                                        data=json.dumps(body).encode(),
                                        headers={"Content-Type": "application/json"},
                                        method="POST")
            raw = op.open(rq).read().decode()
            env = json.loads(raw)
            if "error" in env:
                raise RuntimeError(f"{m} 返回错误：{raw}")
            return env.get("result", env), raw

        ok = fail = 0
        def chk(name, cond, detail=""):
            nonlocal ok, fail
            if cond:
                ok += 1
                print(f"  OK   {name}")
            else:
                fail += 1
                print(f"  FAIL {name} {detail}")

        sid, _ = post("session.create", {})
        new_id = sid.get("sessionId")
        chk("create 给出 id", bool(new_id), str(sid))

        lst, _ = post("session.list", {})
        ids = [s["id"] for s in (lst.get("sessions") or [])]
        chk("list 里包含刚建的会话", new_id in ids, str(ids[:3]))

        # 每个样本 id 都要能 load（两种入参名都试）
        loaded = 0
        for i in ids:
            for payload in ({"sessionId": i}, {"id": i, "tailMessages": 5}):
                _, _ = post("session.load", payload)
                loaded += 1
        chk(f"list 的 {len(ids)} 个 id 全部可 load（两种入参名）", loaded == len(ids) * 2, f"{loaded}")

        # prompt → 等回答落盘 → 再 load 必须看得到
        # 把 prompt 的应答记下来：如果它根本没被接受（409 有轮次在跑 / 400 参数错），
        # 后面 load 不到 assistant 就是**必然**，而不是"宿主丢了消息"。
        pr, _ = post("session.prompt", {"id": new_id, "text": "回答：H10 通过", "mode": "default"})
        chk("prompt 被接受", (pr or {}).get("accepted") is True, str(pr)[:120])
        # 60 秒在网关忙的时候不够：那红的是**探针的耐心**，不是宿主。
        # （和 h14 同一条教训：先看是哪一条红，再决定改探针还是改宿主。）
        got = 0
        for _ in range(WARMUP_SEC):
            time.sleep(1)
            d, _ = post("session.load", {"id": new_id, "tailMessages": 20})
            roles = [m.get("role") for m in (d.get("messages") or [])]
            got = roles.count("assistant")
            if got:
                break
        # 失败时把"到底 load 到了什么"写进断言：只报 0 等于没说。
        chk("prompt 之后能 load 到 assistant 消息", got > 0, f"assistant={got} roles={roles[:8]}")

        # 形状：成功必须带 result 信封（raw 里看得见）
        _, raw = post("session.load", {"id": new_id})
        chk("响应是 {result:…} 信封", '"result"' in raw, raw[:80])

        print(f"合计 OK={ok} FAIL={fail}")
        return 0 if fail == 0 else 1
    finally:
        p.kill()          # 只回收自己启动的进程

if __name__ == "__main__":
    sys.exit(main())
