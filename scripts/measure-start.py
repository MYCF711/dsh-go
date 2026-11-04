import json, os, statistics, time
import _proc

BIN = os.environ.get("DSHD_BIN", "tmp/dshd.exe")
xs = []
for _ in range(5):
    t0 = time.time()
    # 起 acp 用统一管理器：中途抛异常/被 Ctrl+C 也会把子进程收掉（以前这里漏）。
    with _proc.managed(_proc.run_acp(bin=BIN)) as p:
        p.stdin.write(json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}})+"\n"); p.stdin.flush()
        while True:
            line = p.stdout.readline()
            if not line: break
            m = json.loads(line)
            if m.get("id") == 1: break
        xs.append((time.time()-t0)*1000)
print(f"acp 就绪: median {statistics.median(xs):.1f} ms, min {min(xs):.1f} ms")
