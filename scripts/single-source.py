#!/usr/bin/env python3
"""单点实现守卫：抓「同一条规则被写了第二份」。

## 为什么需要它

2026-11-04 一整天修的都是同一类事故：**同一条规则有多个实现，然后各自漂移**。

| # | 事故 | 提交 |
|---|---|---|
| 1 | 两份压表实现（`agent.CompactSchema` / `cmd/dshd.stripDescriptions`）都"见 `description` 键就删"，各删错一次，一共删掉 8 个名叫 `description` 的参数（5 个还在 `required` 里）。收敛到 `internal/schema` | 409879e / a882d5b |
| 2 | 三份铺块逻辑（`run` / `serve` / `acp`），ACP 那份不写思考块——那轮想过什么从日志里整轮消失，而 history / fork / load 三处读路径全是对的。收敛到 `assistantBlocksFrom` | 941460c / 3c9e457 |
| 3 | 两份思考块识别（serve 取第一条 / fork 全带），同一形状各解一遍 JSON。收敛到 `internal/session/reasoning.go` | b4809d1 |
| 4 | 七处"只认 text"的投影 → 129 条回答整条消失。已有 `scripts/text-only-guard.py` | 926ff4e 等 |

第 4 条已经被 `text-only-guard.py` 守住，本守卫**不重复实现它**（重复实现一条守卫，
本身就是这份守卫要抓的病）。本守卫管的是前三条的**共同形状**。

## 为什么判结构，不判行为

行为测试在两处实现"当前一致"时永远全绿。第 3 条的变异 C（把"取第一条"和"全带"
写成两份）就是靠结构判据才抓住的：两份都对、都过测试、结果不同只在某类事件上出现。

所以这里的判据只有两种，都是结构：

  * **同一个判据字面量**出现在登记位置之外（`b.Type == "reasoning"`、`k == "description"`）；
  * **同一种组装形状**出现在登记位置之外（一个函数里同时铺两种以上的内容块）。

## 怎么保证不吵（这是设计约束，不是附带好处）

守卫太吵的结局是"后来人加白名单而不是改代码"，那么它比没有更糟。三条约束：

1. **每条 FAIL 的修法都是"改成调用登记的唯一实现"**，不是"加一行白名单"。
   形状实现里带 `delegates`：同一函数里调了唯一实现的入口，这条命中就不算数
   （`--verbose` 里会标成"已委派"）。所以新写一个投影只要复用现成判据就是绿的，
   要红的是"自己重写一遍判据"。
2. **字面量必须是真的 Go 字面量**。词法器复用 `text-only-guard.py` 的 `strip_source`
   （不是复制一份——复制一份正是本守卫要抓的病）：字符串内容在 `blank` 里是下划线，
   引号只在代码态保留。所以工具 schema 里成千上万个 `"description":`、
   `json:"description"` 结构体标签，一个都不会误报（实测：0 处误报）。
3. **白名单当前 0 条**。真有第二处且收不了口的，写进登记表的 `second=` 列，
   只打 WARN 且每次门禁都打出来——不是挠一次就消失的橡皮图章。

## 登记表

`scripts/single-source.txt`：`概念 | 唯一实现 | 形状ID | 为什么这是同一件事`。
本文件只实现"形状"，"哪条形状归哪个唯一实现"在登记表里——加规则是改数据，
改形状判据才是改代码。

## 用法

    python scripts/single-source.py              # 全仓门禁用（默认本仓）
    python scripts/single-source.py --root /tmp/x  # 扫隔离副本（造红用）
    python scripts/single-source.py --verbose    # 连"已委派"与"登记位置内"的命中一起打印
    python scripts/single-source.py --list       # 只打印登记表，不扫
"""
import argparse
import importlib.util
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent


# ---------------------------------------------------------------- 词法器复用


def load_lexer():
    """把 text-only-guard.py 当模块加载，复用它的 Go 词法器。

    为什么不把 `strip_source` 复制过来：复制一份就是"同一条规则的第二份实现"，
    正是本守卫要抓的东西。两份词法器将来对 raw string / 块注释的处理一旦漂开，
    两个守卫会开始对同一行代码给出不同判断。

    加载失败就直接红：这个守卫离开了词法器就只剩"grep 字面量"，
    而 grep 会被工具 schema 里的 JSON 淹没（见文件头第 2 条约束）。
    """
    p = HERE / "text-only-guard.py"
    if not p.exists():
        raise SystemExit("找不到 scripts/text-only-guard.py —— 词法器在那儿，守卫没法判字面量")
    spec = importlib.util.spec_from_file_location("text_only_guard", p)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


LEX = load_lexer()
Source = LEX.Source
ROOTS = getattr(LEX, "ROOTS", ("cmd", "internal", "modules", "codecs"))
SKIP_PARTS = getattr(LEX, "SKIP_PARTS", {"refs", "tmp", "dist", "probe", "testdata", ".git", "vendor"})


# ---------------------------------------------------------------- 结构判据基元


def is_code_literal(blank_line, start, end):
    """keep 里 [start,end) 这段字面量，在 blank 上是不是一个**独立的 Go 双引号字面量**。

    词法器把字符串**内容**抹成下划线、把注释抹成空格，只保留代码态下的定界符。
    于是三种情况可以精确区分：

        if b.Type == "reasoning"     → blank 里是 `"_________"`（引号在）→ 是字面量
        json:"description"（tag）    → blank 里是 `_____________`（引号没了）→ 不是
        `` `{"type":"text"}` ``（raw）→ 同上，里面的引号全是内容 → 不是

    这一条是整个守卫"不误报"的地基：本仓 modules/ 下的工具 schema 全是 raw string，
    里面每个参数都有一个 `"description":`。
    """
    seg = blank_line[start:end]
    if len(seg) < 2 or seg[0] != '"' or seg[-1] != '"':
        return False
    return all(c == "_" for c in seg[1:-1])


def _pre(line, s, n=16):
    return line[max(0, s - n):s]


def _post(line, e, n=16):
    return line[e:e + n]


def _hit(src, idx, shape, concept, hint, code=None):
    return {
        "path": src.rel,
        "line": idx + 1,
        "scope": src.scope_at(idx),
        "shape": shape,
        "concept": concept,
        "hint": hint,
        "code": (code if code is not None else src.keep[idx]).strip(),
    }


# ---------------------------------------------------------------- 形状 1：思考块识别


REASONING_LIT = re.compile(r'"reasoning"')
# switch 的操作数得像是"块类型"，否则 `case "reasoning":` 也可能是命令行开关的分支。
TYPEISH = re.compile(r"(?i)type|kind|block")


def shape_reasoning_type_discriminator(src, delegates):
    """「一个内容块算不算思考」——只该在 internal/session/reasoning.go 里判一次。

    命中的形状：
        b.Type == "reasoning" / != "reasoning"
        b["type"] == "reasoning"
        case "reasoning":（switch 的操作数是块类型时）

    对应的历史事故（#3）：`reasoningBySeq` 与 `reasoningBlocksBySeq` 各写一遍
    「type 等于 reasoning 且正文不是空白」，一处认顶层 content、另一处不认，
    症状是"有时候思考块在、有时候不在"——而两份各自的单测都是绿的。
    """
    out = []
    for i, line in enumerate(src.keep):
        if '"reasoning"' not in line:
            continue
        blank = src.blank[i]
        case_line = bool(re.match(r"^\s*(case|default)\b", line))
        sw = src.switch_of_case(i) if case_line else (None, None)
        for m in REASONING_LIT.finditer(line):
            s, e = m.span()
            if not is_code_literal(blank, s, e):
                continue
            # (a) 比较：== / != 连着这个字面量
            if re.search(r"(?:==|!=)\s*$", _pre(line, s)) or re.match(r"\s*(?:==|!=)", _post(line, e)):
                out.append(_hit(src, i, "reasoning-type-discriminator",
                                "思考块识别", "改调 session.ReasoningText(kind, text)"))
                continue
            # (b) switch case：只有操作数是块类型时才算（否则可能是 --reasoning 开关）
            if case_line:
                if sw[0] is None:
                    continue
                operand = re.sub(r"^\s*switch\b", "", src.keep[sw[0]]).strip()
                if operand and not TYPEISH.search(operand):
                    continue
                out.append(_hit(src, i, "reasoning-type-discriminator",
                                "思考块识别", "改调 session.ReasoningText(kind, text)"))
    return out


# ---------------------------------------------------------------- 形状 2：思考块构造


BLOCK_KINDS = {
    "reasoning": (re.compile(r"\bReasoningBlock\s*\("), re.compile(r'"type"\s*:\s*"reasoning"'),
                  re.compile(r'\bType\s*:\s*"reasoning"')),
    "text": (re.compile(r"\bTextBlock\s*\("), re.compile(r'"type"\s*:\s*"text"'), None),
    "tool-call": (re.compile(r"\bToolCallBlock\s*\("), re.compile(r'"type"\s*:\s*"tool-call"'), None),
}


def _kind_present(src, i, kind):
    """这一行有没有出现某种内容块的构造（含手写 map/struct 字面量）。"""
    line, blank = src.keep[i], src.blank[i]
    call, maplit, structlit = BLOCK_KINDS[kind]
    if call.search(line):
        return True
    for rx in (maplit, structlit):
        if rx is None:
            continue
        m = rx.search(line)
        if not m:
            continue
        # 手写的 {"type": "..."} 可能是 raw string 里的 JSON 数据，得确认是真的字面量
        lit = line.find('"%s"' % kind, m.start())
        if lit >= 0 and is_code_literal(blank, lit, lit + len(kind) + 2):
            return True
    return False


def shape_reasoning_block_write(src, delegates):
    """「思考块长什么样」——只该是 internal/session.ReasoningBlock。

    命中的形状：手写 `{"type": "reasoning", "text": …}` 或 `spi.Block{Type: "reasoning"}`。

    为什么这条和"识别"分成两条：写入与识别是一对，识别认哪些字段、写入就必须写哪些。
    v4 往块里加字段时只有一处跟上，另一处写的块读回来就少字段，而两端都不报错。
    """
    out = []
    for i, line in enumerate(src.keep):
        if '"reasoning"' not in line:
            continue
        if not _kind_present(src, i, "reasoning"):
            continue
        if re.search(r"\bReasoningBlock\s*\(", line):
            continue  # 构造调用本身是合规用法（就是唯一实现）
        out.append(_hit(src, i, "reasoning-block-write",
                        "思考块构造", "改调 session.ReasoningBlock(text)"))
    return out


# ---------------------------------------------------------------- 形状 3：铺块顺序


def shape_assistant_block_layout(src, delegates):
    """「一次回答铺成哪些块、什么顺序、什么条件才铺」——只该有一处。

    命中的形状：**同一个函数里**同时构造两种以上的内容块。三个真实的重复实现
    （run / serve / acp）都是这个形状：先 make 一个 []map[string]any，再按
    思考 / 正文 / 调用逐段 append。

    为什么不逐个匹配"思考→正文→调用"的三段：那样只能抓住照抄的那一份，
    而漏掉思考块的 ACP 那份（正是出事故的那份）恰恰少了一段，反而不会红。
    这里判的是"这个函数在铺一次回答的块"这件事本身。

    委派：函数里调了 assistantBlocksFrom 就说明它在复用，不算第二份。
    """
    out = []
    for start, name in src.funcs:
        _, end = src.func_range(start)
        kinds = set()
        for j in range(start, end + 1):
            for kind in BLOCK_KINDS:
                if kind not in kinds and _kind_present(src, j, kind):
                    kinds.add(kind)
        if len(kinds) < 2:
            continue
        out.append(_hit(src, start, "assistant-block-layout",
                        "铺块顺序", "改调 assistantBlocksFrom(text, reasoning, toolCalls)", code=src.keep[start]))
    return out


# ---------------------------------------------------------------- 形状 4：schema 注解判据


DESC_LIT = re.compile(r'"description"')
BIND_DESC = re.compile(r'^\s*(?:const|var)?\s*[A-Za-z_]\w*\s*(?::=|=)\s*"description"\s*$')


def shape_schema_annotation_key(src, delegates):
    """「description 是注解还是参数名」——只该在 internal/schema 里判一次。

    命中的形状（三种都是"按字面量认这个键"）：
        k == "description" / "description" != k
        case "description":
        const description = "description"（自己造一份键名常量，等价于第二份判据）

    对应的历史事故（#1）：`CompactSchema` 与 `stripDescriptions` 都"见 description
    键就删"，各删错一次。判断必须同时看**键名**和**位置**（在 properties 映射里，
    它就是参数名，一个字节都不许动），所以只能有一处。

    刻意不抓的：`m["description"] = …` 这种往 schema 里**写**说明的地方——
    它不是在判断"这是不是注解"。
    """
    out = []
    for i, line in enumerate(src.keep):
        if '"description"' not in line:
            continue
        blank = src.blank[i]
        for m in DESC_LIT.finditer(line):
            s, e = m.span()
            if not is_code_literal(blank, s, e):
                continue
            cmp_ = re.search(r"(?:==|!=)\s*$", _pre(line, s)) or re.match(r"\s*(?:==|!=)", _post(line, e))
            case_ = re.match(r"^\s*case\b", line) and re.search(r":\s*$", line)
            bind_ = BIND_DESC.match(line)
            if cmp_ or case_ or bind_:
                out.append(_hit(src, i, "schema-annotation-key",
                                "schema 注解判据", "改调 schema.IsAnnotationKey(k, inPropertyMap)"))
                break
    return out


# 形状ID → 实现。加规则时：形状判据写在这里，归哪个唯一实现在 single-source.txt 里写。
SHAPES = {
    "reasoning-type-discriminator": shape_reasoning_type_discriminator,
    "reasoning-block-write": shape_reasoning_block_write,
    "assistant-block-layout": shape_assistant_block_layout,
    "schema-annotation-key": shape_schema_annotation_key,
}

# 形状ID → "同一函数里出现这个调用 = 它在委派，不算第二份实现"。
DELEGATES = {
    "reasoning-type-discriminator": re.compile(r"\bReasoningText\s*\("),
    "reasoning-block-write": re.compile(r"\bReasoningBlock\s*\("),
    "assistant-block-layout": re.compile(r"\bassistantBlocksFrom\s*\("),
    "schema-annotation-key": re.compile(r"\bIsAnnotationKey\s*\("),
}


# ---------------------------------------------------------------- 登记表


class Rule:
    def __init__(self, concept, owner_path, owner_symbol, shape, why, second, lineno):
        self.concept = concept
        self.owner_path = owner_path
        self.owner_symbol = owner_symbol
        self.shape = shape
        self.why = why
        self.second = second  # None 或 (path, symbol)
        self.lineno = lineno

    def owner_label(self):
        return self.owner_path + (":" + self.owner_symbol if self.owner_symbol else "")

    def is_owner(self, hit):
        # 目录形式（`internal/schema/`）表示「整个包就是唯一实现」
        if self.owner_path.endswith("/"):
            return hit["path"].startswith(self.owner_path)
        if hit["path"] != self.owner_path:
            return False
        if not self.owner_symbol:
            return True
        return hit["scope"] == self.owner_symbol

    def is_second(self, hit):
        if not self.second:
            return False
        path, symbol = self.second
        return hit["path"] == path and (not symbol or hit["scope"] == symbol)


def parse_owner(spec):
    """`internal/session/log.go:ReasoningBlock` → (路径, 符号)；`internal/schema/` → (目录, "")。"""
    if ":" in spec:
        p, _, sym = spec.rpartition(":")
        return p.strip(), sym.strip()
    return spec.strip(), ""


def load_registry(path):
    rules, allow = [], []
    section = "rule"
    for lineno, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("[") and line.endswith("]"):
            section = line[1:-1].strip()
            continue
        fields = [f.strip() for f in line.split("|")]
        if section == "allow":
            if len(fields) < 3 or not fields[2]:
                raise SystemExit("白名单第 %d 行要写「路径:行号 | 形状ID | 理由」：%s" % (lineno, line))
            if fields[1] not in SHAPES:
                # 不在这里拦住的话，形状ID 打错会变成"白名单条目没命中任何发现"，
                # 而那个提示会把人引去查行号，实际上错的是这一列。
                raise SystemExit("白名单第 %d 行的形状 ID 不认识：%s（已实现：%s）"
                                 % (lineno, fields[1], ", ".join(sorted(SHAPES))))
            allow.append({"spec": fields[0], "shape": fields[1], "reason": fields[2],
                          "lineno": lineno, "used": False})
            continue
        if len(fields) < 4:
            raise SystemExit("登记表第 %d 行少了「| 为什么这是同一件事」：%s" % (lineno, line))
        concept, owner, shape, why = fields[0], fields[1], fields[2], fields[3]
        if shape not in SHAPES:
            raise SystemExit("登记表第 %d 行的形状 ID 不认识：%s（已实现：%s）"
                             % (lineno, shape, ", ".join(sorted(SHAPES))))
        second = None
        for extra in fields[4:]:
            if not extra:
                continue
            if extra.startswith("second="):
                p, sym = parse_owner(extra[len("second="):])
                second = (p, sym)
            else:
                raise SystemExit("登记表第 %d 行有不认识的列：%s" % (lineno, extra))
        op, osym = parse_owner(owner)
        if not op:
            raise SystemExit("登记表第 %d 行没写唯一实现的位置：%s" % (lineno, line))
        rules.append(Rule(concept, op, osym, shape, why, second, lineno))
    return rules, allow


def verify_owner(root, rule):
    """唯一实现必须在，而且 :符号 得真的在这个文件里。登记表指空了 = 规则失效。"""
    target = root / rule.owner_path
    if rule.owner_path.endswith("/"):
        if not target.is_dir():
            return "唯一实现的目录不存在：%s" % rule.owner_path
        return None
    if not target.is_file():
        return "唯一实现的文件不存在：%s" % rule.owner_path
    if not rule.owner_symbol:
        return None
    src = target.read_text(encoding="utf-8", errors="replace")
    if not re.search(r"\b%s\b" % re.escape(rule.owner_symbol), src):
        return ("%s 里找不到符号 %s —— 唯一实现被改名/搬走了，"
                "登记表还指着老位置（那么所有同形状的代码都会被算成'在登记位置之外'）"
                % (rule.owner_path, rule.owner_symbol))
    return None


def verify_second(root, rule):
    if not rule.second:
        return None
    path, _sym = rule.second
    if not (root / path).is_file():
        return "登记的第二处文件不存在：%s" % path
    return None


# ---------------------------------------------------------------- 白名单


LINE_SPEC = re.compile(r"^(\d+)(?:-(\d+))?$")


def match_allow(entry, findings):
    """白名单条目匹配发现。行号条目 ±6 行内仍然认（打 WARN），超出就不认。"""
    cand = [f for f in findings if f["path"] == entry["path"]]
    if not cand:
        return [], False
    pat = entry["pattern"]
    if pat == "*":
        return cand, False
    m = LINE_SPEC.match(pat)
    if not m:
        # 函数作用域：与唯一实现同一套 `路径:符号` 写法
        return [f for f in cand if f["scope"] == pat], False
    lo = int(m.group(1))
    hi = int(m.group(2) or m.group(1))
    exact = [f for f in cand if lo <= f["line"] <= hi]
    if exact:
        return exact, False
    near = [f for f in cand if lo - 6 <= f["line"] <= hi + 6]
    if near:
        return near, True
    return [], False


# ---------------------------------------------------------------- 扫描


def iter_go_files(root):
    files = []
    for r in ROOTS:
        base = root / r
        if not base.is_dir():
            continue
        for p in sorted(base.rglob("*.go")):
            if set(p.relative_to(root).parts) & SKIP_PARTS:
                continue
            if p.name.endswith("_test.go"):
                continue
            files.append(p)
    return files


def scan(root, rules):
    files = iter_go_files(root)
    sources = []
    for p in files:
        rel = p.relative_to(root).as_posix()
        sources.append(Source(rel, p.read_text(encoding="utf-8", errors="replace")))

    # 只跑登记表里出现的形状（没登记的规则不该产生发现，否则输出会掺进"没有归属"的红）
    wanted = {}
    for rule in rules:
        wanted.setdefault(rule.shape, []).append(rule)

    delegated, in_owner, findings = [], [], []
    for shape_id, rule_list in wanted.items():
        fn = SHAPES[shape_id]
        delegate_rx = DELEGATES.get(shape_id)
        for src in sources:
            for h in fn(src, delegate_rx):
                # 归属判定四步，顺序就是这个守卫的"不吵"设计：
                #   1) 在唯一实现里        —— 本来就该在这儿
                #   2) 同一函数调了唯一实现 —— 它在复用判据，不算第二份（新写投影的常态）
                #   3) 登记过的第二处       —— WARN，每次门禁都打出来
                #   4) 其余                —— FAIL
                if any(r.is_owner(h) for r in rule_list):
                    in_owner.append(h)
                    continue
                if delegate_rx is not None and enclosing_calls(src, h, delegate_rx):
                    delegated.append(h)
                    continue
                second = next((r for r in rule_list if r.is_second(h)), None)
                if second is not None:
                    h["concept"] = second.concept
                    h["known_second"] = second
                    delegated.append(h)
                    continue
                h["concept"] = rule_list[0].concept
                h["known_second"] = None
                findings.append(h)
    return len(files), delegated, in_owner, findings


def enclosing_calls(src, hit, rx):
    """命中所在的那个函数里，有没有调用唯一实现的入口。"""
    if not hit["scope"]:
        return False
    start, end = src.func_range(hit["line"] - 1)
    return bool(rx.search("\n".join(src.keep[start:end + 1])))


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("--root", default=None, help="扫描根目录（默认本仓）")
    ap.add_argument("--rules", default=None, help="登记表（默认 scripts/single-source.txt）")
    ap.add_argument("--verbose", action="store_true", help="连委派/登记位置内的命中一起打印")
    ap.add_argument("--list", action="store_true", help="只打印登记表，不扫描")
    args = ap.parse_args()

    root = pathlib.Path(args.root).resolve() if args.root else HERE.parent
    rules_path = pathlib.Path(args.rules) if args.rules else HERE / "single-source.txt"

    if not rules_path.exists():
        print("FAIL 单点实现守卫：找不到登记表 %s —— 没有登记表就不知道该守什么" % rules_path)
        return 1
    rules, allow_entries = load_registry(rules_path)

    if args.list:
        print("== 单点实现登记表（%s） ==" % rules_path.name)
        for r in rules:
            print("  %s | %s | %s" % (r.concept, r.owner_label(), r.shape))
            if r.second:
                print("      已知第二处：%s:%s" % r.second)
        print("  白名单 %d 条" % len(allow_entries))
        return 0

    print("== 单点实现守卫 ==")
    if not rules:
        print("FAIL 登记表是空的（%s）：0 条规则等于什么也没守" % rules_path)
        return 1

    bad = []
    for r in rules:
        for msg in (verify_owner(root, r), verify_second(root, r)):
            if msg:
                bad.append((r.concept, msg))

    nfiles, delegated, in_owner, findings = scan(root, rules)
    if nfiles == 0:
        print("FAIL 扫描到 0 个 Go 文件（root=%s）—— 没扫东西不许报绿" % root)
        return 1
    print("登记 %d 条规则 / %d 条白名单，扫描 %d 个 Go 文件（%s 起）"
          % (len(rules), len(allow_entries), nfiles, ", ".join(ROOTS)))

    # 白名单
    kept, drift, stale = set(), [], []
    for e in allow_entries:
        fpath, _, pattern = e["spec"].rpartition(":")
        if not fpath or not pattern:
            raise SystemExit("白名单第 %d 行不是「路径:行号模式」：%s" % (e["lineno"], e["spec"]))
        e["path"], e["pattern"] = fpath.strip(), pattern.strip()
        hit, moved = match_allow(e, [f for f in findings if f["shape"] == e["shape"]])
        if not hit:
            stale.append(e)
            continue
        e["used"] = True
        kept.update(id(f) for f in hit)
        if moved:
            drift.append((e, hit[0]))

    for concept, msg in bad:
        print("  FAIL %s：%s" % (concept, msg))
    if bad:
        print("FAIL 单点实现守卫：%d 条登记项本身失效（登记表指的位置不在了）" % len(bad))
        return 1

    fail = 0
    for h in findings:
        if id(h) in kept:
            continue
        fail += 1
        rule = next((r for r in rules if r.concept == h["concept"]), None)
        print("  FAIL %s @ %s:%d [%s] %s" % (h["concept"], h["path"], h["line"], h["shape"],
                                             h["scope"] or "-"))
        print("       %s" % h["code"])
        print("       这条只有一份实现在 %s —— %s" % (rule.owner_label() if rule else "?", h["hint"]))
        if rule:
            print("       为什么：%s" % rule.why)

    for e, h in drift:
        print("  WARN 白名单行号漂移 %s → 命中 %s:%d（请更新行号）" % (e["spec"], h["path"], h["line"]))
    for e in stale:
        fail += 1
        print("  FAIL 白名单条目没命中任何发现：%s" % e["spec"])
        print("       理由写的是「%s」" % e["reason"])
        print("       问题修掉了、或者代码改名了——两种都要求改这一行，不许它悄悄烂着")

    warn_second = [h for h in delegated if h.get("known_second")]
    for h in warn_second:
        r = h["known_second"]
        pkg = h["path"].rsplit("/", 1)[0] + "/" if "/" in h["path"] else h["path"]
        print("  WARN %s：第二处仍在 %s:%s（登记表 second=，见 %s 第 %d 行）"
              % (r.concept, h["path"], h["scope"] or "-", rules_path.name, r.lineno))
        print("       收口方向：把这段下沉到 %s（第二处所在的包），让两边都调它。"
              "那要改 .go，所以先登记、每次门禁都打出来——"
              "目的是别让它悄悄变成第三处" % pkg)

    if args.verbose:
        for h in in_owner:
            print("  [登记位置内] %s:%d [%s] %s" % (h["path"], h["line"], h["shape"], h["scope"]))
        for h in delegated:
            if not h.get("known_second"):
                print("  [已委派] %s:%d [%s] %s" % (h["path"], h["line"], h["shape"], h["scope"]))

    if fail:
        print("FAIL 单点实现守卫：%d 处未登记（规则 %d 条，白名单 %d 条，WARN %d）"
              % (fail, len(rules), sum(1 for e in allow_entries if e["used"]), len(warn_second)))
        return 1
    print("PASS 单点实现守卫：%d 条规则 / %d 个文件 / 0 处未登记（白名单 %d，WARN %d，漂移 %d）"
          % (len(rules), nfiles, sum(1 for e in allow_entries if e["used"]), len(warn_second), len(drift)))
    return 0


if __name__ == "__main__":
    # Windows CI 控制台不是 UTF-8：中文 print 会 UnicodeEncodeError，
    # 那长得和"检查失败"一模一样，实际上是没跑完。
    for _s in (sys.stdout, sys.stderr):
        try:
            _s.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass
    sys.exit(main())
