#!/usr/bin/env python3
"""text-only 守卫：拦住"投影只认 type==\"text\""的第八次。

## 这个病是什么

2026-11-04 一天里，同一个病在七处各犯了一次：
`session.load`、`history_search`、子代理转写、`session.fork`、ACP `session/load`、
`Meter.Estimate`、TUI 的 `extractText`。形状都是同一个：

    把"这条消息有没有内容"判成"它的 text 块是不是非空"，
    于是只有 tool_calls 的轮次、只有思考块的回答被整条丢掉（或读回空气泡）。

七处都修了，但没有任何东西拦住第八次。所以这里把判据变成可执行的：
**决定"有没有内容"的地方，不许只看 text。**

## 五类形状（每条都对应一次真实事故）

1. `block-type-filter`：`if x.Type == "text"` / `!= "text"` / `case "text":`
   用来拼正文或过滤块，而这段代码不知道"reasoning"/"tool-call"这两个兄弟类型。
   对应 history_search 旧实现（只拼 text 块）与 fork 旧 `copyNode`（只铺 text+tool_call）。
2. `text-presence-drop`：`x.Text == ""` / `x.Content != ""` / `TrimSpace(x) == ""`
   直接 `continue` / `return 空值`——把 text 当"有没有内容"的唯一判据。
   对应子代理转写旧实现（`if m.Text == "" { continue }`）。
3. `surface-without-reasoning`：遍历 `session.Surface(...)`（或 `[]SurfaceMsg`）读
   `.Message` 做投影，但整个函数没提过 `Reasoning`。Surface 只装 text + tool_calls，
   思考正文是**另带**的（SurfaceMsg.Reasoning），不看就等于丢。
   对应 `Meter.Estimate` 旧实现（少算思考 ⇒ 压缩提前触发）与 ACP `session/load` 旧实现。
4. `text-only-block-join`：`for _, b := range blocks { ... b.Text ... }` 只把 text
   拼进结果，函数里既没有 reasoning 也没有 tool-call。对应 `eventText` 旧实现。
5. `assistant-case-text-only` / `text-presence-emits-message`：assistant 分支只凭 text
   决定要不要产出这条消息（不含 reasoning 判据），以及"text 非空"直接决定发不发一帧。
   对应 `session.load` 旧 `messagesOf` 与 serve 旧推帧条件（`resp.Content != ""`）。

## 白名单

`scripts/text-only-allow.txt` 一行一条：`路径:行号模式 | 理由`。
理由必须能查证（指明"为什么这里只该看 text"），不许写"历史原因"这种没法核对的话。
行号模式支持四种：

    cmd/dshd/transport_source.go:533     精确行
    cmd/dshd/transport_source.go:530-540 行区间
    cmd/dshd/transport_source.go:joinText 函数作用域（改名即失效，比行号稳）
    cmd/dshd/transport_source.go:*       整个文件（慎用）

行号漂了（代码在前面插了几行）不会把门禁搞红：守卫在 ±6 行内重新对上，并打一条
`WARN 白名单行号漂移`。但**对不上任何发现**就是 FAIL——问题修掉了、或者白名单在
护的东西已经不存在，两种都要求有人去改白名单，而不是让它悄悄烂在那儿。
（为什么只容忍 ±6 行、不按函数兜底：那会把同一函数里新写进去的回归一起吞掉，实测踩过。）

## 用法

    python scripts/text-only-guard.py                 # 全仓门禁用
    python scripts/text-only-guard.py --root /tmp/x   # 扫隔离副本（造红用）
    python scripts/text-only-guard.py --verbose       # 连白名单命中也打印
"""
import argparse
import pathlib
import re
import sys

# 只扫本仓的宿主/模块代码。refs 是上游源码镜像（自带 text-only 的天真实现，
# 不是我们的代码）；tmp/dist/evidence/probe/research 是产物与实验目录。
ROOTS = ("cmd", "internal", "modules", "codecs")
SKIP_PARTS = {
    "refs", "tmp", "dist", "probe", "research", "play", "evidence",
    "testdata", "node_modules", ".git", "vendor",
}

# 允许出现的"非 text"兄弟块类型：代码里提到任意一个，说明它知道消息不止有正文。
SIBLING_MARKERS = re.compile(r'"reasoning"|"tool-call"|\\bReasoning\\b|ToolCalls')

FUNC_LINE = re.compile(r"^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\(\[]")
IF_LINE = re.compile(r"^\s*(?:\}\s*else\s+)?if\b")
CASE_LINE = re.compile(r"^\s*case\b")
FOR_LINE = re.compile(r"^\s*for\s+_,\s*([A-Za-z_]\w*)\s*:=\s*range\s+([A-Za-z_][\w.]*)")

# "这条消息有没有内容"的 text 判据。
PRESENCE_PATTERNS = (
    re.compile(r'\.(?:Text|Content)\s*(?:==|!=)\s*""'),
    re.compile(r'\btext\b\s*(?:==|!=)\s*""'),
    re.compile(r'TrimSpace\([^)]*(?:Text|Content|\btext\b)[^)]*\)\s*(?:==|!=)\s*""'),
    re.compile(r'len\([^)]*\.(?:Text|Content)\)\s*==\s*0'),
)

# "丢掉这条消息"的控制流。注意两点：
#   - 只认丢弃：`parts = append(...)` 那种"这一段为空就不拼"是正常局部选择；
#   - 不认 `break`：`if b.Type == "reasoning" && TrimSpace(b.Text) != "" { ...; break }`
#     是"找到第一条就停"的正向筛选，不是丢弃（误报过一次，记在这里）。
DROP_BODY = re.compile(
    r'\bcontinue\b|return\s+"",|return\s+""\b|return\s+nil,\s*false'
    r'|return\s+false\b|return\s+nil\b|return\s+0,\s*nil'
)

# 产出"整条消息/整帧"的动作（区别于往 blocks/parts 里塞一个片段）。
EMIT_BODY = re.compile(
    r'emitFrame\(|(?<![.\w])emit\(|\.push\(|(?<![.\w])push\(|transport\.Message\{'
    r'|AssistantMessageEvent\('
)

# 白名单里"这一行是函数作用域"与"这一行是整数"的判别。
LINE_SPEC = re.compile(r"^(\d+)(?:-(\d+))?$")


def strip_source(text):
    """把注释抹成空格、保留字符串字形；另出一份把字符串内容也抹掉的版本。

    为什么要两份：判断形状要看 `"text"` 这个**字面量**，所以字符串内容得留着；
    数花括号找语句块时字符串里的 `{` 会干扰，所以要一份抹掉内容的。
    抹注释是必须的：这个仓库的注释里到处写着 `if b.Type != "text"` 这种举例，
    不先抹掉，守卫第一天就会被自己的文档搞红。
    """
    keep, blank = [], []
    kbuf, bbuf = [], []
    i, n = 0, len(text)
    state = "code"

    def flush():
        keep.append("".join(kbuf))
        blank.append("".join(bbuf))
        kbuf.clear()
        bbuf.clear()

    while i < n:
        c = text[i]
        nxt = text[i + 1] if i + 1 < n else ""
        # 行尾统一在这里收口：一条源码行对应 keep/blank 里的一项，
        # 行号因此与文件行号一一对上（下面所有规则都直接拿下标当今行号）。
        if c == "\n":
            flush()
            if state == "line_comment":
                state = "code"
            i += 1
            continue
        if state == "code":
            if c == "/" and nxt == "/":
                state = "line_comment"
                kbuf.append("  ")
                bbuf.append("  ")
                i += 2
                continue
            if c == "/" and nxt == "*":
                state = "block_comment"
                kbuf.append("  ")
                bbuf.append("  ")
                i += 2
                continue
            if c == '"':
                state = "quote"
            elif c == "`":
                state = "raw"
            elif c == "'":
                state = "rune"
            kbuf.append(c)
            bbuf.append(c)
            i += 1
            continue
        if state == "line_comment":
            kbuf.append(" ")
            bbuf.append(" ")
            i += 1
            continue
        if state == "block_comment":
            if c == "*" and nxt == "/":
                state = "code"
                kbuf.append("  ")
                bbuf.append("  ")
                i += 2
                continue
            kbuf.append(" ")
            bbuf.append(" ")
            i += 1
            continue
        # 字符串/字符字面量内部：字形留着，空白化版本抹成下划线（等长，列号不变）
        if c == "\\" and nxt and state in ("quote", "rune"):
            kbuf.append(c)
            kbuf.append(nxt)
            bbuf.append("__")
            i += 2
            continue
        if (state == "quote" and c == '"') or (state == "raw" and c == "`") or (
            state == "rune" and c == "'"
        ):
            state = "code"
            kbuf.append(c)
            bbuf.append(c)
            i += 1
            continue
        kbuf.append(c)
        bbuf.append("_")
        i += 1
    flush()
    return keep, blank


class Source:
    def __init__(self, rel, text):
        self.rel = rel
        self.keep, self.blank = strip_source(text)
        self.funcs = []  # [(起始行下标, 名字)]
        for i, line in enumerate(self.keep):
            m = FUNC_LINE.match(line)
            if m:
                self.funcs.append((i, m.group(1)))

    def scope_at(self, idx):
        name = ""
        for start, fn in self.funcs:
            if start > idx:
                break
            name = fn
        return name

    def func_range(self, idx):
        """(起始行, 结束行)，闭区间；找不到函数就退化成整个文件。"""
        start = 0
        for s, _ in self.funcs:
            if s > idx:
                break
            start = s
        end = len(self.keep) - 1
        for s, _ in self.funcs:
            if s > idx:
                end = s - 1
                break
        return start, end

    def block_after(self, idx):
        """从 idx 行开始找到第一个 `{`，返回与它配对的 `}` 所在行；找不到返回 None。

        if/case 的花括号可能在下一行（多行条件），所以往后看；但**不猜**：
        一直没见到 `{`、或者先撞上另一个 `func`，就返回 None，由调用方退化成
        "只看这一行"。宁可少判，也不要把后面无关的语句算进这个分支的身体里。

        上限给 400 行而不是十几行：一个分支的身体常常比看起来长得多
        （真事：serve 推帧那段 if 从条件到 emitFrame 有 14 行，早先的 12 行上限
        让它整段被当成"空身体"，于是造红时守卫没红）。
        """
        depth = 0
        opened = False
        for j in range(idx, min(idx + 400, len(self.blank))):
            if j > idx and FUNC_LINE.match(self.blank[j]):
                return None
            for ch in self.blank[j]:
                if ch == "{":
                    depth += 1
                    opened = True
                elif ch == "}":
                    depth -= 1
                    if opened and depth == 0:
                        return j
            if opened and depth == 0:
                # 花括号在同一行就闭合了
                return j
        return None

    def body_text(self, start, end):
        """语句块的内容（含首尾两行的花括号）。

        含首行是有意的：`if b.Type != "text" { continue }` 这种一行写法，
        身体就在首行上；只取"中间"会把唯一那句话漏掉。
        """
        if start is None:
            return ""
        if end is None:
            return self.keep[start]
        return "\n".join(self.keep[start:end + 1])

    def case_body(self, idx):
        """case 分支的身体：到下一个同级 case/default 或 switch 结尾为止。"""
        indent = len(self.blank[idx]) - len(self.blank[idx].lstrip())
        end = len(self.keep) - 1
        for j in range(idx + 1, len(self.keep)):
            line = self.blank[j]
            if not line.strip():
                continue
            cur = len(line) - len(line.lstrip())
            if cur <= indent and re.match(r"\s*(case\b|default\s*:)", line):
                end = j - 1
                break
            if cur < indent and line.strip().startswith("}"):
                end = j
                break
        return "\n".join(self.keep[idx:end + 1])

    def switch_of_case(self, idx):
        """case 所在 switch 的 (起始行, 结束行)。"""
        for j in range(idx, max(-1, idx - 60), -1):
            if re.match(r"^\s*switch\b", self.blank[j]):
                end = self.block_after(j)
                return j, (end if end is not None else len(self.keep) - 1)
        return None, None


def text_presence(cond):
    return any(p.search(cond) for p in PRESENCE_PATTERNS)


def scan(src):
    rel = src.rel
    out = []

    def add(idx, kind, why):
        out.append({
            "path": rel,
            "line": idx + 1,
            "scope": src.scope_at(idx),
            "kind": kind,
            "why": why,
            "code": src.keep[idx].strip(),
        })

    for i, line in enumerate(src.keep):
        # ---- 1) block-type-filter ----
        m = re.search(r'\.Type\s*(?:==|!=)\s*"text"', line)
        if m:
            end = src.block_after(i)
            body = src.body_text(i, end)
            if not SIBLING_MARKERS.search(body):
                add(i, "block-type-filter",
                    "只按 text 判块；同一段代码里没有 reasoning/tool-call——"
                    "只有调用或只有思考的轮次会被这一句滤掉")
        if re.match(r"^\s*case\s+\"text\"\s*:", line):
            sw_start, sw_end = src.switch_of_case(i)
            if sw_start is not None:
                sw = "\n".join(src.keep[sw_start:sw_end + 1])
                if not re.search(r'case\s+"(?:reasoning|tool-call)"', sw):
                    add(i, "block-type-filter",
                        "switch 只处理 text 块；同一个 switch 里没有 reasoning/tool-call 分支")

        # ---- 2) text-presence-drop ----
        if IF_LINE.match(line) and text_presence(line):
            end = src.block_after(i)
            body = src.body_text(i, end)
            if DROP_BODY.search(body):
                add(i, "text-presence-drop",
                    "把 text 当'有没有内容'的唯一判据，并据此丢消息；"
                    "判据应是 assistantHasPayload（正文/思考/调用三样占一样）")

        # ---- 5a) assistant-case-text-only ----
        if CASE_LINE.match(line) and '"assistant/message"' in line:
            body = src.case_body(i)
            if re.search(r"append\(|TextBlock\(|Text:|Message\{|Event\{|emit", body) and not re.search(
                r"[Rr]easoning", body
            ):
                add(i, "assistant-case-text-only",
                    "assistant 分支只铺 text（/工具调用），没有思考块这一路——"
                    "只有思考的回答在这里就没了")

        # ---- 5b) text-presence-emits-message ----
        if IF_LINE.match(line) and text_presence(line):
            if not re.search(r"[Rr]easoning|ToolCalls|assistantHasPayload", line):
                end = src.block_after(i)
                body = src.body_text(i, end)
                if EMIT_BODY.search(body):
                    add(i, "text-presence-emits-message",
                        "用'text 非空'决定发不发这条消息/这一帧；"
                        "只有思考的回答整条不发（旧 serve 推帧就是这个形状）")

        # ---- 4) text-only-block-join ----
        fm = FOR_LINE.match(line)
        if fm:
            var, coll = fm.group(1), fm.group(2)
            # 只认"遍历内容块"的循环：集合名或循环变量得像是块。
            # 不加这一层，`for _, m := range ms { rows = append(..., m.Text) }`
            # （grep 工具画表格）也会被算进来——那是匹配结果的 Text，不是内容块。
            looks_like_blocks = re.search(
                r"(?i)(blocks?|contents?|summary|parts)$", coll.split(".")[-1]
            ) or re.match(r"^(b|blk|block|cb)$", var)
            if looks_like_blocks:
                end = src.block_after(i)
                body = src.body_text(i, end)
                # `{"type": b.Type, "text": b.Text}` 是把块原样再编码一遍，不是拼接正文。
                if (
                    re.search(r"\b%s\.Text\b" % re.escape(var), body)
                    and re.search(r"append\(|\+=|WriteString\(", body)
                    and '"type"' not in body
                ):
                    fstart, fend = src.func_range(i)
                    fbody = "\n".join(src.keep[fstart:fend + 1])
                    if not SIBLING_MARKERS.search(fbody):
                        add(i, "text-only-block-join",
                            "把块拼成正文时只拼 text（函数里没有 reasoning/tool-call）——"
                            "正是 history_search 旧实现那条：只有调用的轮次搜不到")

    # ---- 3) surface-without-reasoning（函数作用域）----
    for start, name in src.funcs:
        _, end = src.func_range(start)
        body = "\n".join(src.keep[start:end + 1])
        if not re.search(r"range\s+(?:session\.)?Surface\(|range\s+surface\b|range\s+nodes\b", body):
            continue
        if not re.search(r"\.Message\b", body):
            continue  # 只数节点/摘要，不做消息投影
        if re.search(r"[Rr]easoning", body):
            continue
        loop = start
        for j in range(start, end + 1):
            if re.search(r"range\s+(?:session\.)?Surface\(|range\s+surface\b|range\s+nodes\b",
                         src.keep[j]):
                loop = j
                break
        out.append({
            "path": rel,
            "line": loop + 1,
            "scope": name,
            "kind": "surface-without-reasoning",
            "why": "遍历 Surface 投影消息但整个函数没取过思考正文"
                   "（SurfaceMsg.Reasoning 是另带的；不看就丢）",
            "code": src.keep[loop].strip(),
        })
    return out


# ---------------------------------------------------------------- 白名单

def load_allow(path):
    entries = []
    if not path.exists():
        return entries
    for lineno, raw in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "|" not in line:
            raise SystemExit("白名单第 %d 行少了 '| 理由'：%s" % (lineno, line))
        spec, reason = line.split("|", 1)
        spec, reason = spec.strip(), reason.strip()
        if not reason:
            raise SystemExit("白名单第 %d 行没写理由（理由必须能查证）：%s" % (lineno, line))
        fpath, _, pattern = spec.rpartition(":")
        if not fpath or not pattern:
            raise SystemExit("白名单第 %d 行不是 '路径:行号模式'：%s" % (lineno, line))
        entries.append({
            "raw": spec,
            "path": fpath.strip(),
            "pattern": pattern.strip(),
            "reason": reason,
            "lineno": lineno,
            "used": False,
        })
    return entries


def match_entry(entry, findings):
    """返回 (被这条白名单压住的发现列表, 是否需要提示行号漂移)。

    行号条目在"精确行没命中"时会退一小步：±6 行内还有发现就认，
    并打一条 WARN。为什么要有这一小步——代码在上面插几行就让门禁红，
    最后一定是有人把白名单改成 `*` 或者干脆把守卫删掉。
    为什么只退 6 行而不是"整个函数"——那会把同一函数里新写进去的回归一起吞掉，
    实测造红时真吞过一次（ACP Load 那条）。
    """
    cand = [f for f in findings if f["path"] == entry["path"]]
    if not cand:
        return [], False
    pat = entry["pattern"]
    if pat == "*":
        return cand, False
    m = LINE_SPEC.match(pat)
    if m:
        lo = int(m.group(1))
        hi = int(m.group(2) or m.group(1))
        exact = [f for f in cand if lo <= f["line"] <= hi]
        if exact:
            return exact, False
        # 行号漂了（上面插了几行）：只容忍**邻近**的同类发现。
        # 早期版本退到"整个函数"，结果造红时把同一函数里新写进去的回归也吞了——
        # 白名单是按行的，就不能按函数兜底。
        near = [f for f in cand if lo - 6 <= f["line"] <= hi + 6]
        if near:
            return near, True
        return [], False
    # 函数作用域
    return [f for f in cand if f["scope"] == pat], False


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("--root", default=None, help="扫描根目录（默认本仓）")
    ap.add_argument("--allow", default=None, help="白名单文件（默认 scripts/text-only-allow.txt）")
    ap.add_argument("--verbose", action="store_true", help="白名单命中也逐条打印")
    ap.add_argument("--max-print", type=int, default=40, help="最多打印多少条未登记发现")
    args = ap.parse_args()

    here = pathlib.Path(__file__).resolve().parent
    root = pathlib.Path(args.root).resolve() if args.root else here.parent
    allow_path = pathlib.Path(args.allow) if args.allow else here / "text-only-allow.txt"

    files = []
    for r in ROOTS:
        base = root / r
        if not base.is_dir():
            continue
        for p in sorted(base.rglob("*.go")):
            rel_parts = set(p.relative_to(root).parts)
            if rel_parts & SKIP_PARTS:
                continue
            if p.name.endswith("_test.go"):
                continue
            files.append(p)

    if not files:
        print("text-only 守卫：扫描到 0 个 Go 文件（root=%s）——没扫东西不许报绿" % root)
        return 1

    findings = []
    for p in files:
        rel = p.relative_to(root).as_posix()
        findings.extend(scan(Source(rel, p.read_text(encoding="utf-8", errors="replace"))))

    allow = load_allow(allow_path)
    kept, drift, stale = [], [], []
    for entry in allow:
        hit, moved = match_entry(entry, findings)
        if not hit:
            stale.append(entry)
            continue
        entry["used"] = True
        kept.extend(id(f) for f in hit)
        if moved:
            drift.append((entry, hit))

    kept_ids = set(kept)
    left = [f for f in findings if id(f) not in kept_ids]

    print("== text-only 守卫 ==")
    print("扫描 %d 个 Go 文件（%s 起）" % (len(files), ", ".join(ROOTS)))
    if args.verbose:
        for f in findings:
            tag = "登记" if id(f) in kept_ids else "未登记"
            print("  [%s] %s:%d %s\t%s" % (tag, f["path"], f["line"], f["kind"], f["code"]))
    fail = 0
    for f in left[: args.max_print]:
        fail += 1
        print("  FAIL %s:%d [%s] %s" % (f["path"], f["line"], f["kind"], f["scope"] or "-"))
        print("       %s" % f["code"])
        print("       %s" % f["why"])
    if len(left) > args.max_print:
        fail += 1
        print("  FAIL 还有 %d 处未打印（--max-print 限制）" % (len(left) - args.max_print))
    for entry, hit in drift:
        print("  WARN 白名单行号漂移 %s → 命中 %s:%d（请更新行号）"
              % (entry["raw"], hit[0]["path"], hit[0]["line"]))
    for entry in stale:
        fail += 1
        print("  FAIL 白名单条目没命中任何发现：%s" % entry["raw"])
        print("       理由写的是「%s」" % entry["reason"])
        print("       问题修掉了、或者代码改名了——两种都要求改白名单，不许它悄悄烂着")
    if not allow:
        print("  WARN 白名单文件不存在（%s）：任何命中都会红" % allow_path)

    used = sum(1 for e in allow if e["used"])
    if fail:
        print("FAIL text-only 守卫：%d 处未登记（白名单 %d 条命中 %d）" % (len(left), len(allow), used))
        return 1
    print("PASS text-only 守卫：%d 个文件 0 处未登记（白名单 %d 条命中 %d，漂移 %d）"
          % (len(files), len(allow), used, len(drift)))
    return 0


if __name__ == "__main__":
    # CI 的 Windows 控制台不是 UTF-8，中文 print 会 UnicodeEncodeError。
    # 那是"打印失败"，不是"检查失败"——两者在门禁输出里不能长得一样。
    for _s in (sys.stdout, sys.stderr):
        try:
            _s.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass
    sys.exit(main())
