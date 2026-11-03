# 真语料只读端到端：13 个 method 里所有**不写盘**的在 183 个真实会话上跑一遍。
# 写类（create/prompt/delete 真删/fork 真建/config.set/fs.write）不在这里跑——不能污染语料。
import json, subprocess, urllib.request, http.cookiejar, re, time
out=open("tmp/real-e2e.out","w",encoding="utf-8")
ok=fail=0
def chk(n,c,d=""):
    global ok,fail
    if c: ok+=1; print(f"  OK   {n}",file=out)
    else: fail+=1; print(f"  FAIL {n} {d}",file=out)
    out.flush()
LIC=subprocess.Popen(["tmp/serve-dir/dshd.exe","serve","--home","D:/DeepSeek Harness/home"],stdout=subprocess.PIPE,text=True)
info=json.loads(re.match(r'ready (\{.*\})',LIC.stdout.readline().strip()).group(1))
class NR(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*a,**k): return None
jar=http.cookiejar.CookieJar(); op=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar),NR)
try: op.open(info["url"]+"/connect?token="+info["token"])
except urllib.error.HTTPError: pass
def post(m,b,expect=200):
    rq=urllib.request.Request(info["url"]+"/api/"+m, data=json.dumps(b).encode(),
        headers={"Content-Type":"application/json"}, method="POST")
    try:
        r=op.open(rq); body=json.loads(r.read().decode())
        return r.code, (body.get("result", body) if isinstance(body, dict) else body)
    except urllib.error.HTTPError as e:
        return e.code, {}
c,v=post("workspace.list",{}); chk("workspace.list", c==200 and len(v.get("entries",[]))>0, f"{c} {len(v.get('entries',[]))}")
c,v=post("config.get",{}); chk("config.get", c==200 and isinstance(v.get("values"),dict), f"{c}")
c,v=post("session.list",{}); items=v.get("sessions") or []
chk("session.list 183 条", c==200 and len(items)>=100, f"{c} {len(items)}")
sid=items[0]["id"] if items else ""
c,v=post("session.load",{"sessionId":sid,"limit":50})
msgs=v.get("messages") or []
chk("session.load 有消息", c==200 and len(msgs)>0, f"{c} {len(msgs)}")
chk("load 的 total 可能为 -1（契约允许）", v.get("total") is not None, str(v.get("total")))
chk("total=-1 时 truncated 必须 true", not (v.get("total",0)<0 and not v.get("truncated")))
c,v=post("session.history",{"sessionId":sid,"sinceSeq":0}); chk("session.history", c==200, str(c))
c,v=post("fs.read",{"path":"AGENTS.md"}); chk("fs.read 本仓文件", c==200 and len(v.get("content",""))>100, str(c))
c,v=post("session.delete",{"sessionId":"nope-not-exist"}); chk("delete 不存在→404", c==404, str(c))
c,v=post("session.fork",{"sessionId":"nope-not-exist"}); chk("fork 不存在→404", c==404, str(c))
print(f"\n合计 OK={ok} FAIL={fail}",file=out)
LIC.kill(); out.close()
