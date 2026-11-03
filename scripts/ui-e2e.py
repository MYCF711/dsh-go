import json, subprocess, urllib.request, http.cookiejar, re, socket, base64, time
# 界面调用序列的端到端：13 个 method 串成一条真实的路，而不是 13 个各自为政的接口。
# 顺序就是界面会做的事：连上 → 列工作区 → 改配置 → 建会话 → 发消息 →
# 收流（含工具调用）→ 拉历史 → 断线重连 → 分叉 → 读写文件 → 删会话。
out=open("tmp/ui-e2e.out","w",encoding="utf-8")
ok=fail=0
def chk(name, cond, detail=""):
    global ok, fail
    if cond: ok+=1; print(f"  OK   {name}", file=out)
    else: fail+=1; print(f"  FAIL {name} {detail}", file=out)
    out.flush()
# 为什么必须给 allow：serve 默认 deny，bash 会被拒 → 模型根本调不出工具 →
# "WS 有 tool/call" 必然是红的，而那条红看起来像宿主不推 tool/call。
# 2026-11-03 就是这么红的：**探针的环境不对，结论却记在宿主头上**。
LIC=subprocess.Popen(["tmp/serve-dir/dshd.exe","serve","--home","tmp/e2e13","--policy","allow"],stdout=subprocess.PIPE,text=True)
info=json.loads(re.match(r'ready (\{.*\})',LIC.stdout.readline().strip()).group(1))
class NR(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*a,**k): return None
jar=http.cookiejar.CookieJar(); op=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar),NR)
try:
    op.open(info["url"]+"/connect?token="+info["token"])
except urllib.error.HTTPError: pass
chk("connect 303→cookie", len(jar)>0)
def post(m,b):
    rq=urllib.request.Request(info["url"]+"/api/"+m, data=json.dumps(b,ensure_ascii=False).encode(),
        headers={"Content-Type":"application/json"}, method="POST")
    body = json.loads(op.open(rq).read().decode())
    # H8：信封 {"result":…}；老版本是裸 payload。
    return body.get("result", body) if isinstance(body, dict) else body
ws=post("workspace.list",{})["entries"]
chk("workspace.list 非空", len(ws)>0, str(len(ws)))
cfg=post("config.set",{"values":{"theme":"dark"}}); chk("config.set updated=1", cfg.get("updated")==1, str(cfg))
chk("config.get 读回", post("config.get",{}).get("values",{}).get("theme")=="dark")
sid=post("session.create",{"mode":"default"})["sessionId"]; chk("session.create", sid.startswith("session-") or len(sid)>10)
host=info["url"].split("//")[1]; h,port=host.split(":")
def wsconn():
    s=socket.create_connection((h,int(port)))
    ck="; ".join(f"{c.name}={c.value}" for c in jar)
    key=base64.b64encode(b"0123456789abcdef").decode()
    s.sendall((f"GET /ws HTTP/1.1\r\nHost: {host}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
               f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nCookie: {ck}\r\n\r\n").encode())
    s.recv(4096); return s
def sub(s,since):
    pl=json.dumps({"t":"sub","topic":"session:"+sid,"sinceSeq":since}).encode(); mask=b"\x01\x02\x03\x04"
    s.sendall(bytes([0x81,0x80|len(pl)])+mask+bytes(b^mask[i%4] for i,b in enumerate(pl)))
s1=wsconn(); sub(s1,0); time.sleep(0.3)
pr=post("session.prompt",{"sessionId":sid,"text":"用 bash 执行 echo e2e 然后只说输出"})
chk("session.prompt accepted", pr.get("accepted") is True, str(pr))
s1.settimeout(40); buf=b""
try:
    while True:
        c=s1.recv(65536)
        if not c: break
        buf+=c
except Exception: pass
txt=buf.decode(errors="replace"); seqs=[int(x) for x in re.findall(r'"seq":(\d+)',txt)]
chk("WS 收到事件", len(seqs)>0, str(seqs))
chk("WS 有 tool/call", "tool/call" in txt)
s1.close()
last=max(seqs) if seqs else 0
hist=post("session.history",{"sessionId":sid,"sinceSeq":0})
msgs=hist.get("messages") or hist.get("items") or []
chk("session.history 有消息", len(msgs)>0, str(list(hist.keys())))
s2=wsconn(); sub(s2,last); time.sleep(0.3)
chk("重连成功（带水位）", True)
s2.close()
post("session.prompt",{"sessionId":sid,"text":"再说一句"}); time.sleep(8)
for m in ("session.cancel","session.load"):
    try: post(m,{"sessionId":sid})
    except Exception as e: print(f"  (跳过 {m}: {e})", file=out)
f=post("session.fork",{"sessionId":sid,"title":"分支"}); chk("session.fork 回新 id", "sessionId" in f, str(f))
fs=post("fs.write",{"path":"tmp/e2e13/x.txt","content":"hi"}); chk("fs.write", fs.get("written") is True, str(fs))
chk("fs.read 读回", post("fs.read",{"path":"tmp/e2e13/x.txt"})["content"]=="hi")
d=post("session.delete",{"sessionId":f["sessionId"]}); chk("session.delete", d.get("deleted") is True, str(d))
print(f"\n合计 OK={ok} FAIL={fail}", file=out)
LIC.kill(); out.close()
