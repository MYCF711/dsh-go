#!/usr/bin/env python3
"""H11：`dshd serve` 的 **stdout 必须恰好一行** `ready {…}`。

旧检法是 `serve … | head -1`——它**根本看不到第二行**，所以永远"通过"。
这里改成捕获完整 stdout 再判断。
"""
import json, os, subprocess, sys, threading, time

HOME = "tmp/h11out"
BIN = "tmp/serve-dir/dshd.exe"

def main():
    os.system("rm -rf " + HOME)
    p = subprocess.Popen([BIN, "serve", "--home", HOME],
                         stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    buf = []
    def drain():
        for line in p.stdout:
            buf.append(line)
    t = threading.Thread(target=drain, daemon=True)
    t.start()
    time.sleep(3)          # 留够时间让任何"第二行"出现
    p.kill()               # 只回收自己启动的进程
    t.join(timeout=2)

    ok = fail = 0
    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1; print(f"  OK   {name}")
        else:
            fail += 1; print(f"  FAIL {name} {detail}")

    lines = buf
    chk("stdout 恰好一行", len(lines) == 1, f"实际 {len(lines)} 行：{lines!r}")
    if lines:
        first = lines[0].rstrip("\n")
        chk("以 'ready ' 开头", first.startswith("ready "), first[:40])
        try:
            json.loads(first[len("ready "):])
            parsed = True
        except Exception as e:
            parsed = False
            err = str(e)
        chk("'ready ' 之后是合法 JSON", parsed, "" if parsed else err)
        if parsed:
            d = json.loads(first[len("ready "):])
            chk("ready 里有 url 与 token", bool(d.get("url")) and bool(d.get("token")), str(d)[:80])
    print(f"合计 OK={ok} FAIL={fail}")
    return 0 if fail == 0 else 1

if __name__ == "__main__":
    sys.exit(main())
