import json, os, subprocess, time, statistics
BIN=__import__("os").environ.get("DSHD_BIN", "tmp/dshd.exe")
xs=[]
for _ in range(5):
    t0=time.time()
    p=subprocess.Popen([BIN,"acp"],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,bufsize=1)
    p.stdin.write(json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}})+"\n"); p.stdin.flush()
    while True:
        line=p.stdout.readline()
        if not line: break
        m=json.loads(line)
        if m.get("id")==1: break
    xs.append((time.time()-t0)*1000)
    p.kill(); p.wait()
print(f"acp 就绪: median {statistics.median(xs):.1f} ms, min {min(xs):.1f} ms")
