#!/usr/bin/env python3
"""ACP 冒烟客户端：按真实客户端的方式和 `dshd acp` 对话。

用法：
    python scripts/acp-smoke.py "<要问的话>"

它做的事：
  1. 拉起 `dshd acp`（stdio）；
  2. initialize → session/new → session/prompt；
  3. 边收边打印 session/update（reasoning / text / tool），直到 prompt 应答；
  4. 打印 stopReason。
"""
import json
import os
import subprocess
import sys
import threading
import time

BIN = os.environ.get("DSHD_BIN", "tmp/dshd.exe")
ARGS = [BIN, "acp"] if os.path.exists(BIN) else ["go", "run", "./cmd/dshd", "acp"]


def main() -> int:
    prompt = sys.argv[1] if len(sys.argv) > 1 else "用一句话说明你在干什么。"
    proc = subprocess.Popen(
        ARGS,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        bufsize=1,
    )

    def pump_stderr():
        for line in proc.stderr:
            print(f"[stderr] {line.rstrip()}", file=sys.stderr)

    threading.Thread(target=pump_stderr, daemon=True).start()

    seq = 0

    def send(method, params, expect_reply=True):
        nonlocal seq
        msg = {"jsonrpc": "2.0", "method": method, "params": params}
        if expect_reply:
            seq += 1
            msg["id"] = seq
        proc.stdin.write(json.dumps(msg) + "\n")
        proc.stdin.flush()
        return seq if expect_reply else None

    def read_until(reply_id, timeout=180):
        deadline = time.time() + timeout
        updates = 0
        while time.time() < deadline:
            line = proc.stdout.readline()
            if not line:
                break
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            if msg.get("method") == "session/update":
                updates += 1
                u = msg["params"]["update"]
                kind = u.get("sessionUpdate")
                if kind in ("agent_thought_chunk", "agent_message_chunk"):
                    text = u.get("content", {}).get("text", "")
                    tag = "think" if kind == "agent_thought_chunk" else "text"
                    print(f"  [{tag}] {text}", end="", flush=True)
                elif kind == "tool_call":
                    print(f"\n  [tool] {u.get('title')} {json.dumps(u.get('rawInput'), ensure_ascii=False)}")
                elif kind == "tool_call_update":
                    print(f"  [tool] {u.get('status')}")
                continue
            if msg.get("id") == reply_id:
                print()
                return msg, updates
        raise SystemExit(f"等待应答 {reply_id} 超时")

    rid = send("initialize", {"protocolVersion": 1})
    res, _ = read_until(rid)
    info = res.get("result", {}).get("agentInfo", {})
    print(f"initialize: {info.get('name')} {info.get('version')} protocol={res['result'].get('protocolVersion')}")

    rid = send("session/new", {"cwd": os.getcwd(), "mcpServers": []})
    res, _ = read_until(rid)
    sid = res["result"]["sessionId"]
    print(f"session/new: {sid}")

    started = time.time()
    rid = send("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": prompt}]})
    res, updates = read_until(rid)
    elapsed = time.time() - started
    if "error" in res:
        print(f"prompt 失败: {res['error']}")
        return 1
    print(f"prompt: stopReason={res['result']['stopReason']} updates={updates} 用时={elapsed:.2f}s usage={res['result'].get('usage')}")

    send("session/close", {"sessionId": sid})
    proc.stdin.close()
    proc.wait(timeout=10)
    return 0


if __name__ == "__main__":
    sys.exit(main())
