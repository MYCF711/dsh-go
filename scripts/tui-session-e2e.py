#!/usr/bin/env python3
"""scripts/tui-session-e2e.py — TUI 客户端对着**真宿主**连跑四轮：多轮 + 工具 + 审批交错。

为什么单开一层：`cmd/dshtui` 已经有 57 条单测（键解码 / view / 审批倒计时 / 折叠 / 搜索 /
resume），`--send` 也验过单轮链路。但这两样都证明不了**连跑**的性质：

  ① 第二轮还要带着第一轮的上下文（serve 是一轮一个子进程，上下文断了就是断了）；
  ② 审批那一下答完，工具才真的执行 —— 帧上多一条 tool/result 不算数，文件得真的在；
  ③ 工具结果进了 lines（带了折叠标记）之后，折叠/展开仍然对，而且可逆。

这些错各自都能在单测里"看起来对"，串起来才露出来。所以这里做的是：
起一个真 `dshd serve`（临时 home、端口 0、--policy ask），用 `dshtui --script` 回放
四轮，把每一轮的**帧**和**屏幕快照**一起交出来，逐条断言，任何一项没有结论就是 FAIL。

驱动方式：`dshtui --script plan.json --out report.json`
  - 键走的就是界面那条路（handleRune → submit / answerApproval），
    所以"按 y 批准"和人在终端里按 y 是同一个函数；
  - 屏幕快照是 `view()` 的输出 —— 断言的是**屏幕上有什么**，不是内部变量。

判据（任何一条不成立都 exit 1）：
  - 四轮都以 turn/done 收尾；
  - 第二轮有 tool/call + tool/result，且结果默认折叠、Ctrl+T 展开后再折回去；
  - 第三轮 approval/request 出现在屏幕上，按 y 应答后出现 tool/result，
    **并且文件真的被写出来、内容是本轮 nonce**（不是"点了按钮"）；
  - 第四轮的回答里带着第一轮的暗号（上下文跨轮）。

只回收自己起的进程；临时 home 在 tmp/ 下；不碰真实 home、不碰 57476。
"""

import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import uuid

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RUN = os.path.join(ROOT, "tmp", "tui-session-e2e")
HOME = os.path.join(RUN, "home")
DSHD = os.path.join(ROOT, "tmp", "serve-dir", "dshd.exe")
TUI = os.path.join(RUN, "dshtui.exe")
PLAN = os.path.join(RUN, "plan.json")
REPORT = os.path.join(RUN, "report.json")
PLAN_TXT = os.path.join(RUN, "plan.txt")
SERVE_LOG = os.path.join(RUN, "serve.log")
TUI_LOG = os.path.join(RUN, "dshtui.log")

# 工具看的路径一律**相对工作区根**：仓库路径里有空格（"DeepSeek Harness"），
# 绝对路径一旦被模型拼进 bash 就会在空格处断掉，而那种红看起来像"工具没执行"。
REL_NOTE = "tmp/tui-session-e2e/home/note.txt"
REL_TARGET = "tmp/tui-session-e2e/home/wrote.txt"

CODEX = "BLUE-WHALE-17"   # 第一轮的暗号：第四轮要能背出来
NOTE = "kumquat-42"       # 第二轮要读出来的文件内容
TURN_TIMEOUT_MS = 240000  # 单轮上限（模型慢是常态，但慢不等于没有上限）

ok = 0
fail = 0
warned = 0


def chk(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print("  OK   " + name)
    else:
        fail += 1
        print("  FAIL " + name + ("  " + str(detail) if detail != "" else ""))
    sys.stdout.flush()


def warn(name, cond, detail="", why=""):
    """观察项：**不计入 OK/FAIL**。

    只给"已确认是真缺陷、但修它要改本次边界外的文件"的那一条用（见调用点）。
    它现在是 WARN，修好之后会自己变成 OK 并开始算数——不会永远是个摆设。
    """
    global ok, warned
    if cond:
        ok += 1
        print("  OK   " + name)
    else:
        warned += 1
        print("  WARN " + name + ("  " + str(detail) if detail != "" else "")
              + ("（" + why + "）" if why else ""))
    sys.stdout.flush()


# ------------------------------------------------------------------ 起宿主

def newest_source():
    """dshd 二进制的新鲜度：扫**会影响它**的源码。

    不能照抄 scripts/bin-check.py 的 `cmd`：那会把 `cmd/dshtui` 算进来，
    而 dshtui 是**另一个二进制**——改了它，dshd.exe 一点也不旧。
    照抄的后果是这条断言在每次改 TUI 后都假红，而假红会把整条检查废掉。
    """
    newest, path = 0.0, ""
    for root in ("cmd/dshd", "internal", "modules", "codecs"):
        for dirpath, _, files in os.walk(os.path.join(ROOT, root)):
            for f in files:
                if not f.endswith(".go") or f.endswith("_test.go"):
                    continue
                p = os.path.join(dirpath, f)
                try:
                    m = os.path.getmtime(p)
                except OSError:
                    continue
                if m > newest:
                    newest, path = m, p
    return newest, path


def build_tui():
    """按**当前源码**编 dshtui。旧二进制的后果是"结论记在已经不在的代码上"，
    bin-check 那条规矩同样适用这里。"""
    go = shutil.which("go")
    if not go:
        chk("按当前源码编译 dshtui", False, "PATH 里没有 go（先 export PATH=\"/c/Program Files/Go/bin:$PATH\"）")
        return False
    r = subprocess.run([go, "build", "-o", TUI, "./cmd/dshtui"], cwd=ROOT,
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    chk("按当前源码编译 dshtui", r.returncode == 0, (r.stderr or "").strip()[:400])
    return r.returncode == 0


def start_serve():
    """起真宿主：临时 home、端口交 OS、--policy ask（要真的触发审批）。"""
    p = subprocess.Popen(
        [DSHD, "serve", "--home", HOME, "--policy", "ask", "--addr", "127.0.0.1:0"],
        cwd=ROOT, stdout=subprocess.PIPE,
        stderr=open(SERVE_LOG, "w", encoding="utf-8"), text=True,
        encoding="utf-8", errors="replace")
    # readline 会一直等：宿主起不来时必须能自己停，而不是把探针挂在那儿。
    box = {}

    def reader():
        try:
            box["line"] = p.stdout.readline()
        except Exception as e:  # noqa: BLE001 - 只是把失败原因带出去
            box["err"] = str(e)

    th = threading.Thread(target=reader, daemon=True)
    th.start()
    th.join(30)
    line = (box.get("line") or "").strip()
    m = re.match(r"ready (\{.*\})", line)
    if not m:
        p.kill()
        return None, None, "没读到 ready 行（stderr 见 %s）：%r" % (os.path.relpath(SERVE_LOG, ROOT), line)
    return p, json.loads(m.group(1)), ""


# ------------------------------------------------------------------ 断言用的取值

def frames_of(rep, turn=None, typ=None):
    out = []
    for f in rep.get("frames") or []:
        if turn is not None and f.get("turn") != turn:
            continue
        if typ is not None and f.get("type") != typ:
            continue
        out.append(f)
    return out


def screen_of(rep, label):
    for s in rep.get("screens") or []:
        if s.get("label") == label:
            return s
    return None


def screen_text(scr):
    return "\n".join(scr.get("lines") or [])


def join_text(data):
    """assistant/message 的正文：顶层 text / 顶层 content 块 / message.content 块，三种都认。"""
    if not isinstance(data, dict):
        return ""
    for key in ("text",):
        if isinstance(data.get(key), str) and data[key]:
            return data[key]
    for blk in content_blocks(data):
        if isinstance(blk, dict) and blk.get("type") == "text" and blk.get("text"):
            return blk["text"]
    msg = data.get("message")
    if isinstance(msg, dict):
        if isinstance(msg.get("text"), str) and msg["text"]:
            return msg["text"]
        for blk in content_blocks(msg):
            if isinstance(blk, dict) and blk.get("type") == "text" and blk.get("text"):
                return blk["text"]
    return ""


def content_blocks(obj):
    for key in ("content",):
        v = obj.get(key)
        if isinstance(v, list):
            return v
        if isinstance(v, str) and v:
            return [{"type": "text", "text": v}]
    return []


def tool_name(data):
    if not isinstance(data, dict):
        return ""
    for k in ("name", "tool", "toolName"):
        if isinstance(data.get(k), str) and data[k]:
            return data[k]
    return ""


def is_error(data):
    if not isinstance(data, dict):
        return False
    if data.get("isError"):
        return True
    msg = data.get("message")
    return bool(isinstance(msg, dict) and msg.get("isError"))


def text_on_screen(scr, text, keep_tail=24):
    """正文是不是真的画在屏幕上。

    逐行比**前 60 个字**：终端会把超宽的行裁成 `…`，比全句会让"这行很长"变成假红。
    keep_tail：屏幕只显示尾部，长回答只能验最后几行。
    """
    joined = screen_text(scr)
    lines = [ln.strip() for ln in (text or "").splitlines() if ln.strip()]
    if not lines:
        return False
    return all(ln[:60] in joined for ln in lines[-keep_tail:])


# ------------------------------------------------------------------ 跑

def main():
    print("== 准备 ==")
    shutil.rmtree(RUN, ignore_errors=True)
    os.makedirs(HOME, exist_ok=True)

    if not os.path.exists(DSHD):
        chk("宿主二进制存在（tmp/serve-dir/dshd.exe）", False,
            "先 go build -o tmp/dshd.exe ./cmd/dshd && cp tmp/dshd.exe tmp/serve-dir/dshd.exe")
        return finish()
    stale_t, stale_p = newest_source()
    chk("宿主二进制不比源码旧", os.path.getmtime(DSHD) >= stale_t,
        "先跑 python scripts/bin-check.py（比 %s 旧）" % os.path.relpath(stale_p, ROOT))

    if not build_tui() and not os.path.exists(TUI):
        return finish()  # 编不出来又没得复用：后面每一步都会是同一句"文件不存在"

    # 现场：读得到的一行（工具真的读了它）+ 一个**不存在**的写目标。
    with open(os.path.join(HOME, "note.txt"), "w", encoding="utf-8", newline="\n") as f:
        f.write(NOTE + "\n")
    target = os.path.join(HOME, "wrote.txt")
    if os.path.exists(target):
        os.remove(target)
    chk("写目标在开跑前不存在（否则「文件存在」是白捡的）", not os.path.exists(target))

    nonce = "OK-" + uuid.uuid4().hex[:10]
    plan = {
        "size": [100, 40],
        "turnTimeoutMs": TURN_TIMEOUT_MS,
        "steps": [
            # ① 普通问答 + 埋暗号
            {"kind": "send", "label": "t1",
             "text": "请原样记住这个暗号：%s。只回复一句「记住了」，不要做别的。" % CODEX},
            # ② 触发工具调用（读一个文件，答案只有读了才可能知道）
            {"kind": "send", "label": "t2",
             "text": "调用 read 工具读文件 %s（相对工作区根），然后只回答文件里的那一行内容，不要解释。" % REL_NOTE},
            # 屏上验折叠：先展开，再折回去
            {"kind": "key", "key": "ctrl+t", "label": "t2-expanded"},
            {"kind": "key", "key": "ctrl+t", "label": "t2-refolded"},
            # ③ 触发写操作 → 审批 → 允许这一次（文件必须真的出现）
            {"kind": "send", "label": "t3", "approve": "allow_once",
             "text": "只做一步：调用 write 工具，file_path 填 %s，content 填 %s。不要用 bash，不要解释。"
                     % (REL_TARGET, nonce)},
            # ④ 再问一句：上下文还在不在
            {"kind": "send", "label": "t4",
             "text": "我刚才让你记住的暗号是什么？只回答暗号本身，不要别的字。"},
            {"kind": "snapshot", "label": "final"},
        ],
    }
    with open(PLAN, "w", encoding="utf-8") as f:
        json.dump(plan, f, ensure_ascii=False, indent=2)
        f.write("\n")
    with open(PLAN_TXT, "w", encoding="utf-8") as f:
        for st in plan["steps"]:
            f.write(json.dumps(st, ensure_ascii=False) + "\n")

    started = time.time()
    print("== 起宿主（临时 home + 端口 0 + --policy ask）==")
    serve, info, err = start_serve()
    if serve is None:
        chk("dshd serve 打出 ready 行", False, err)
        return finish()
    chk("dshd serve 打出 ready 行", True)
    p = None
    try:
        print("== 回放四轮（dshtui --script）==")
        budget = int(TURN_TIMEOUT_MS / 1000 * 4 + 120)
        p = subprocess.Popen(
            [TUI, "--addr", info["url"], "--token", info["token"],
             "--script", PLAN, "--out", REPORT],
            cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, encoding="utf-8", errors="replace")
        try:
            out, errout = p.communicate(timeout=budget)
        except subprocess.TimeoutExpired:
            p.kill()
            out, errout = p.communicate()
            errout += "\n（驱动器超过 %ds 被强杀）" % budget
        with open(TUI_LOG, "w", encoding="utf-8") as f:
            f.write("$ dshtui --script ... (rc=%s)\n--- stdout ---\n%s\n--- stderr ---\n%s\n"
                    % (p.returncode, out, errout))
        chk("驱动器退出码 0（四轮跑完）", p.returncode == 0,
            "rc=%s；stderr 尾部：%s" % (p.returncode, (errout or "").strip()[-500:]))

        rep = {}
        if os.path.exists(REPORT):
            with open(REPORT, encoding="utf-8") as f:
                rep = json.load(f)
        chk("报告落盘且是合法 JSON（%s）" % os.path.relpath(REPORT, ROOT),
            bool(rep) and isinstance(rep.get("frames"), list), "报告缺失或坏了；见 %s" % os.path.relpath(TUI_LOG, ROOT))
        if not rep:
            return finish()

        check_turns(rep, target, nonce, started)
    finally:
        if p is not None and p.poll() is None:
            p.kill()
        serve.kill()
        serve.wait(timeout=10)
    return finish()


def check_turns(rep, target, nonce, started):
    turns = rep.get("turns") or []
    chk("四轮都跑到了（turns=4）", len(turns) == 4,
        "实际 %d 轮；驱动错误=%r" % (len(turns), rep.get("error", "")))
    chk("同一会话连跑（sessionId 非空）", bool(rep.get("sessionId")), rep.get("sessionId"))
    chk("四轮都以 turn/done 收尾（没有 turn/error）",
        len(turns) == 4 and all(t.get("status") == "done" for t in turns)
        and not frames_of(rep, typ="turn/error"),
        [(t.get("label"), t.get("status")) for t in turns])

    seqs = [f.get("seq") for f in rep.get("frames") or [] if f.get("type") != "session/reset"]
    chk("帧序号严格递增（同一会话 journal 没有乱序/重号）",
        bool(seqs) and all(isinstance(s, int) and s > 0 for s in seqs)
        and seqs == sorted(seqs) and len(set(seqs)) == len(seqs),
        "前 12 个：%r" % seqs[:12])

    # 已知缺陷的真机证据（不在本次可改范围）：serve 的 follow 每轮从 **offset 0**
    # 重读整份会话日志，于是第二轮之后 WS 会把前几轮的事件**再推一遍**（新 seq）。
    # 屏幕上的表现就是前几轮的消息与工具卡片重复出现（t4 那一屏能直接数出来）。
    # 文档里记着这件事（docs/host-side-closeout.md:51「follow 零水位重复」），
    # 但一直没有多轮真机证据 —— 这条补的就是那个证据。
    # 修法是 follow 起跑时把 offset 定位到文件末尾（本任务不许动 cmd/dshd/serve*.go）。
    per_turn_users = [len(frames_of(rep, n, "user/message")) for n in (1, 2, 3, 4)]
    # 先钉住"只有这一种重复"：第 k 轮最多把前 k-1 轮的再推一遍（= k 条）。
    # 比这更多就是**另一个**错（推两遍、或把别的会话混进来），那必须红。
    chk("每轮最多重推本会话已发生的历史（没有重复推送 / 串会话）",
        not any(per_turn_users[i] > i + 1 for i in range(len(per_turn_users))),
        per_turn_users)
    warn("每一轮只推本轮自己的 user/message（历史不重推）",
         per_turn_users == [1, 1, 1, 1],
         "每轮 user/message 条数=%r（每轮帧数=%r）"
         % (per_turn_users, [t.get("frames") for t in turns]),
         why="HEAD 已知缺陷，见 docs/host-side-closeout.md:51")

    # ---------------- 第一轮：普通问答 ----------------
    t1_msgs = [join_text(f.get("data")) for f in frames_of(rep, 1, "assistant/message")]
    t1_text = "\n".join(t for t in t1_msgs if t)
    chk("第一轮：assistant 正文非空", bool(t1_text.strip()), t1_msgs[:2])
    s1 = screen_of(rep, "t1")
    chk("第一轮：回答真的画在屏幕上（帧 → 屏幕一致）",
        bool(s1) and text_on_screen(s1, t1_text), (s1 or {}).get("lines", [])[-6:])

    # ---------------- 第二轮：工具调用 + 折叠 ----------------
    calls = frames_of(rep, 2, "tool/call")
    results = frames_of(rep, 2, "tool/result")
    chk("第二轮：出现 tool/call", bool(calls), [f.get("type") for f in frames_of(rep, 2)])
    chk("第二轮：出现 tool/result", bool(results))
    if calls and results:
        first_call = rep["frames"].index(calls[0])
        first_result = rep["frames"].index(results[0])
        chk("第二轮：tool/result 在 tool/call 之后（不是空跑）", first_call < first_result,
            "call@%d result@%d" % (first_call, first_result))
    chk("第二轮：tool/result 不是错误", bool(results) and not any(is_error(f.get("data")) for f in results),
        [f.get("data") for f in results[:1]])
    t2_text = "\n".join(t for t in [join_text(f.get("data")) for f in frames_of(rep, 2, "assistant/message")] if t)
    chk("第二轮：回答里带出被读文件的内容（工具结果真的回到了模型手里）",
        NOTE in t2_text, t2_text[:200])

    name = tool_name((calls[0].get("data") if calls else None) or {})
    s2 = screen_of(rep, "t2")
    chk("第二轮：屏幕上画出工具卡片（· %s）" % (name or "?"),
        bool(s2) and bool(name) and ("· " + name) in screen_text(s2),
        (s2 or {}).get("lines", [])[-8:])
    chk("第二轮：状态行报出工具次数（工具 1 起）",
        bool(s2) and re.search(r"工具 [1-9]", screen_text(s2)) is not None,
        (s2 or {}).get("lines", [])[-3:])

    folded = bool(s2) and "结果已折叠" in screen_text(s2) and "  ← " not in screen_text(s2)
    chk("第二轮：工具结果默认折叠（屏幕没有 ← 行，状态行说已折叠）", folded,
        (s2 or {}).get("lines", [])[-6:])

    s2e = screen_of(rep, "t2-expanded")
    chk("第二轮：Ctrl+T 展开后 ← 结果行出现在屏幕上",
        bool(s2e) and "  ← " in screen_text(s2e) and "结果已折叠" not in screen_text(s2e),
        (s2e or {}).get("lines", [])[-8:])

    s2r = screen_of(rep, "t2-refolded")
    chk("第二轮：再按 Ctrl+T 折回去（折叠可逆、行没丢）",
        bool(s2r) and "结果已折叠" in screen_text(s2r) and "  ← " not in screen_text(s2r),
        (s2r or {}).get("lines", [])[-6:])

    # ---------------- 第三轮：审批 + 真的执行 ----------------
    reqs = frames_of(rep, 3, "approval/request")
    chk("第三轮：出现 approval/request", bool(reqs), [f.get("type") for f in frames_of(rep, 3)])
    req = (reqs[0].get("data") if reqs else None) or {}
    chk("第三轮：审批带 requestId 与 allow_once 选项",
        bool(req.get("requestId")) and any(o.get("optionId") == "allow_once"
                                            for o in (req.get("options") or []) if isinstance(o, dict)),
        {"requestId": req.get("requestId"), "options": req.get("options")})

    s3a = screen_of(rep, "t3-approval")
    chk("第三轮：审批那一问画在屏幕上（需要你批准 + 按键提示）",
        bool(s3a) and "需要你批准" in screen_text(s3a) and "[y]允许" in screen_text(s3a),
        (s3a or {}).get("lines", [])[-8:])
    chk("第三轮：审批时状态行是「等你批准」（不是普通状态行）",
        bool(s3a) and "等你批准" in screen_text(s3a),
        (s3a or {}).get("lines", [])[-2:])

    keys = [k for k in (rep.get("keys") or []) if k.get("when") == "approval"]
    chk("第三轮：驱动器按了 y（allow_once）应答",
        len(keys) >= 1 and all(k.get("key") == "y" for k in keys), keys)

    s3b = screen_of(rep, "t3-answered")
    chk("第三轮：应答后屏幕上收起倒计时、留下 → allow_once",
        bool(s3b) and "→ allow_once" in screen_text(s3b) and "等你批准" not in screen_text(s3b),
        (s3b or {}).get("lines", [])[-4:])

    if reqs and results:
        idx_req = rep["frames"].index(reqs[0])
        after = [f for f in rep["frames"][idx_req + 1:] if f.get("turn") == 3 and f.get("type") == "tool/result"]
        chk("第三轮：审批之后出现 tool/result（答完这一轮才继续）",
            bool(after) and not any(is_error(f.get("data")) for f in after),
            [f.get("type") for f in rep["frames"][idx_req + 1:]])
    else:
        chk("第三轮：审批之后出现 tool/result（答完这一轮才继续）", False, "没有 approval/request 或 tool/result")

    # 这一条才是"审批真的被宿主接受并执行"：**文件在不在**。
    # 答 200 只能证明接口通；文件写出来才证明子进程真的拿到了 allow 并执行了工具。
    exists = os.path.exists(target)
    body = ""
    if exists:
        with open(target, encoding="utf-8", errors="replace") as f:
            body = f.read()
    chk("第三轮：允许之后文件真的被写出来，且内容是本轮 nonce",
        exists and nonce in body and os.path.getmtime(target) >= started,
        {"存在": exists, "内容": body[:80], "mtime_新于开跑": exists and os.path.getmtime(target) >= started})

    # ---------------- 第四轮：上下文跨轮 ----------------
    t4_text = "\n".join(t for t in [join_text(f.get("data")) for f in frames_of(rep, 4, "assistant/message")] if t)
    chk("第四轮：回答里带着第一轮的暗号（上下文跨轮保留）",
        CODEX.lower() in t4_text.lower(), t4_text[:300])
    s4 = screen_of(rep, "t4")
    chk("第四轮：暗号也出现在屏幕上", bool(s4) and CODEX.lower() in screen_text(s4).lower(),
        (s4 or {}).get("lines", [])[-6:])

    # 最后一张屏：这一轮的正文应当完整地在屏幕上（尾部锚定，不能少行）
    sf = screen_of(rep, "final")
    chk("收尾屏与第四轮一致（没有多画/少画一行）",
        bool(sf) and bool(s4) and sf.get("lines") == s4.get("lines"),
        {"final": (sf or {}).get("lines", [])[-3:], "t4": (s4 or {}).get("lines", [])[-3:]})


def finish():
    if warned:
        print("备注：%d 条 WARN 未计入 OK/FAIL —— 它是真缺陷，但修它要改本次边界外的文件（见上）" % warned)
    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
