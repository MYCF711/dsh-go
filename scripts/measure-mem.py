import json, os, subprocess, sys, threading, time

import _proc

BIN = os.environ.get("DSHD_BIN", "tmp/dshd.exe")
# `dshd acp` 由管理器托管：超时 SystemExit、断言失败、Ctrl+C 都会把进程收掉。
with _proc.managed(_proc.run_acp(bin=BIN)) as proc:
    peak = {"kb": 0, "samples": 0}
    stop = threading.Event()

    def sample():
        while not stop.is_set():
            try:
                out = subprocess.run(["tasklist", "/FI", f"PID eq {proc.pid}", "/FO", "CSV", "/NH"],
                                     capture_output=True, text=True).stdout.strip()
                if out and "dshd" in out:
                    # "dshd.exe","1234","Console","1","12,345 K"
                    parts = next(__import__("csv").reader([out]))
                    kb = int(parts[4].replace(",", "").replace(" K", ""))
                    peak["kb"] = max(peak["kb"], kb)
                    peak["samples"] += 1
            except Exception:
                pass
            time.sleep(0.15)

    threading.Thread(target=sample, daemon=True).start()

    seq = 0
    def send(method, params, want_reply=True):
        global seq
        msg = {"jsonrpc": "2.0", "method": method, "params": params}
        if want_reply:
            seq += 1
            msg["id"] = seq
        proc.stdin.write(json.dumps(msg) + "\n"); proc.stdin.flush()
        return seq if want_reply else None

    def read_until(rid, timeout=180):
        deadline = time.time() + timeout
        while time.time() < deadline:
            line = proc.stdout.readline()
            if not line: break
            m = json.loads(line)
            if m.get("method") == "session/update":
                continue
            if m.get("id") == rid:
                return m
        raise SystemExit("timeout")

    r = send("initialize", {"protocolVersion": 1}); read_until(r)
    r = send("session/new", {"cwd": os.getcwd(), "mcpServers": []})
    sid = read_until(r)["result"]["sessionId"]
    idle = peak["kb"]
    print(f"idle 峰值: {idle/1024:.1f} MB ({peak['samples']} 次采样)")

    r = send("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": "用 glob 数 .go 文件个数，一句话回答。"}]})
    t0 = time.time()
    res = read_until(r)
    print(f"prompt: stopReason={res['result']['stopReason']} 用时={time.time()-t0:.1f}s")
    peak_kb = peak["kb"]
    print(f"整轮峰值: {peak_kb/1024:.1f} MB (目标 <150 MB)")

    send("session/close", {"sessionId": sid}, False)
    stop.set()
    proc.stdin.close()
    if proc.wait(10) is None:      # 10 秒还不退：管理器收树（原来只 kill 父进程）
        proc.kill_tree("acp 关闭后没退出")
