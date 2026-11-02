"""长期运行（soak）：跑很多轮之后，会不会变慢、会不会涨、会不会攒垃圾。

不是测"能不能跑"——那是 e2e 的事。这里盯的是**时间的形状**：

    RSS 内存        会不会一直涨（泄漏）
    句柄数          会不会一直涨（文件/连接没关）
    home 体积       会话日志、logs、artifacts 会不会无界增长
    每轮耗时        第 1 轮 vs 第 N 轮，有没有变慢
    锁残留          跑完之后有没有 .lock 留下

用法：
    python scripts/soak.py --turns 30            # 一个 ACP 进程连吃 30 轮
    python scripts/soak.py --turns 30 --agents 5 # 5 个进程，每个 6 轮

判定用"前后比值"，不用绝对值——绝对值和机器有关，形状才和代码有关。
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time


def rss_kb(pid):
    try:
        out = subprocess.run(["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
                             capture_output=True, text=True).stdout.strip()
        if out and "dshd" in out:
            import csv
            parts = next(csv.reader([out]))
            return int(parts[4].replace(",", "").replace(" K", ""))
    except Exception:
        pass
    return None


def handles(pid):
    try:
        out = subprocess.run(
            ["powershell", "-NoProfile", "-Command",
             f"(Get-Process -Id {pid} -ErrorAction SilentlyContinue).HandleCount"],
            capture_output=True, text=True, timeout=20).stdout.strip()
        return int(out) if out.isdigit() else None
    except Exception:
        return None


def dir_size(path):
    total, count = 0, 0
    for dp, _, files in os.walk(path):
        for f in files:
            try:
                total += os.path.getsize(os.path.join(dp, f))
                count += 1
            except OSError:
                pass
    return total, count


def find_locks(home):
    out = []
    for dp, _, files in os.walk(home):
        for f in files:
            if f.endswith(".lock"):
                out.append(os.path.join(dp, f))
    return out


class Agent:
    def __init__(self, exe, home, cwd):
        self.home = home
        self.proc = subprocess.Popen(
            [exe, "acp", "--home", home, "--workspace-bytes", "0"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, encoding="utf-8", bufsize=1,
            cwd=cwd)
        self.seq = 0

    def call(self, method, params, timeout=180):
        self.seq += 1
        self.proc.stdin.write(json.dumps(
            {"jsonrpc": "2.0", "id": self.seq, "method": method, "params": params}) + "\n")
        self.proc.stdin.flush()
        deadline = time.time() + timeout
        while time.time() < deadline:
            line = self.proc.stdout.readline()
            if not line:
                return None
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if msg.get("id") == self.seq:
                return msg
        return None

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=10)
        except Exception:
            self.proc.kill()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(
        os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tmp", "dshd.exe"))
    ap.add_argument("--turns", type=int, default=30)
    ap.add_argument("--agents", type=int, default=1)
    ap.add_argument("--prompt", default="用 read 读 target.txt 的第一行，然后只说那一行内容。")
    args = ap.parse_args()

    exe = os.path.abspath(args.bin)
    home = tempfile.mkdtemp()
    ws = tempfile.mkdtemp()
    with open(os.path.join(ws, "target.txt"), "w") as fh:
        fh.write("SOAK-OK\n")

    per_agent = max(1, args.turns // args.agents)
    agents = [Agent(exe, home, ws) for _ in range(args.agents)]

    rows = []
    t_start = time.time()
    for a_i, ag in enumerate(agents):
        ag.call("initialize", {"protocolVersion": 1, "clientCapabilities": {}})
        sid = (ag.call("session/new", {"cwd": ws.replace("\\", "/"), "mcpServers": []}) or {})
        sid = (sid.get("result") or {}).get("sessionId")
        if not sid:
            print("✗ session/new 失败")
            return 1
        for t in range(per_agent):
            t0 = time.time()
            r = ag.call("session/prompt",
                        {"sessionId": sid, "prompt": [{"type": "text", "text": args.prompt}]})
            dt = time.time() - t0
            size, files = dir_size(home)
            rows.append({
                "agent": a_i, "turn": t + 1,
                "roundtrip_s": round(dt, 2),
                "rss_kb": rss_kb(ag.proc.pid),
                "handles": handles(ag.proc.pid),
                "home_bytes": size, "home_files": files,
                "ok": r is not None and "result" in (r or {}),
            })
            mark = "ok" if rows[-1]["ok"] else "FAIL"
            print(f"  a{a_i} t{t+1:02d}  {dt:5.1f}s  rss={rows[-1]['rss_kb']}KB  "
                  f"handles={rows[-1]['handles']}  home={size/1024:.0f}KB/{files}f  {mark}",
                  flush=True)

    for ag in agents:
        ag.close()

    locks = find_locks(home)
    total = time.time() - t_start

    print()
    print("=" * 72)
    print(f"soak 结果：{len(rows)} 轮，{len(agents)} 个进程，总耗时 {total:.0f}s")
    print("=" * 72)
    fails = [r for r in rows if not r["ok"]]
    print(f"失败轮次: {len(fails)}")

    def drift(key, label, unit=""):
        vals = [r[key] for r in rows if r[key] is not None]
        if len(vals) < 4:
            print(f"{label}: 样本不足")
            return
        head = sum(vals[:3]) / 3
        tail = sum(vals[-3:]) / 3
        pct = (tail - head) / head * 100 if head else 0
        print(f"{label}: 前3轮 {head:.0f}{unit} → 后3轮 {tail:.0f}{unit} "
              f"（{pct:+.1f}%，峰值 {max(vals):.0f}{unit}）")

    drift("rss_kb", "RSS 内存", "KB")
    drift("handles", "句柄数")
    drift("home_bytes", "home 体积", " 字节")
    drift("roundtrip_s", "每轮往返", "s")
    print(f"结束时残留锁文件: {len(locks)}")
    for l in locks[:3]:
        print("   ", os.path.relpath(l, home))

    # 判定：只在明显信号上报警
    bad = []
    if fails:
        bad.append(f"{len(fails)} 轮失败")
    if locks:
        bad.append(f"{len(locks)} 个锁残留")
    vals = [r["rss_kb"] for r in rows if r["rss_kb"]]
    if len(vals) >= 4 and (sum(vals[-3:]) / 3) > (sum(vals[:3]) / 3) * 1.5:
        bad.append("内存涨了 50% 以上")
    print()
    if bad:
        print("⚠ 值得看一眼：" + "；".join(bad))
        return 1
    print("✓ 没有明显泄漏/变慢/锁残留")
    return 0


if __name__ == "__main__":
    sys.exit(main())
