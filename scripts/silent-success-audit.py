#!/usr/bin/env python3
"""静默成功扫描器：找「有产出、但产出里没有有效载荷，而且没人报错」的路径。

这类 bug 的共同形状：

    退出码 0 / 返回了结果 / HTTP 200  ——  但结果里什么都没有，也不报错。

它比崩溃贵得多，因为不触发任何告警。2026-11-04 一天撞到两条真机案例：

  1. 网关上游坏了 → 200 + 空 SSE → 会话记成「assistant 空消息 + turn completed + 退出码 0」；
  2. `agent.CompactSchema` 见 description 键就删 → 连 8 个**名叫 description 的参数**
     一起删，而 required 里还点着它们 → 模型看到「必填但没类型」。

所以扫描从**接口边界**入手，而不是从文件名猜：

  A. LLM 层        空响应 / 只有 finish 没有内容 / 流在 finish 之前 EOF / usage 全 0
  B. 工具层        Result 全空 + nil err / Data 空数组 vs nil / required 点名了不存在的键 /
                   工具名空串
  C. 会话与日志层  写进去 0 字节 / append 了但 seq 没动 / 压缩了但摘要为空或 surface 没变
  D. Transport 层  200 + result:null / frames 为空但 lastSeq>0（补洞缺口）
  E. 模块装配      enabled=true 但 Register 什么也没注册

分两类检查：

  * **数据检查**（会 FAIL）：schema 里的 required/properties 对不上、返回空 Result 却 nil err、
    Append 空负载、writeJSON 传 nil……
  * **守卫检查**（会 FAIL）：每条已知的静默成功路径，源码里必须存在对应的校验/报错。
    守卫被删掉时这里立刻红——否则"修过"这件事只能靠记忆。

跑法：

    python scripts/silent-success-audit.py            # 默认仓库根
    python scripts/silent-success-audit.py --json     # 机器可读
    python scripts/silent-success-audit.py --root .   # 指定根（selftest 用）

退出码：有 FAIL 就是 1，全绿是 0。**没有结论不等于通过**：如果某条规则因为
文件不存在而无法检查，按 FAIL 算（"查不到"不是"没问题"）。
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import re
import sys

# ---------------------------------------------------------------- 基础工具


class Audit:
    def __init__(self, root: str):
        self.root = root
        self.fail: list[dict] = []
        self.ok: list[str] = []
        self.info: list[str] = []

    def exists(self, rel: str) -> bool:
        return os.path.isfile(os.path.join(self.root, rel))

    def read(self, rel: str) -> str | None:
        p = os.path.join(self.root, rel)
        if not os.path.isfile(p):
            return None
        with open(p, encoding="utf-8", errors="replace") as f:
            return f.read()

    def need(self, rule: str, rel: str, pattern: str, why: str) -> None:
        """守卫检查：源码里必须有这条校验/报错。"""
        src = self.read(rel)
        if src is None:
            self.fail.append({"rule": rule, "where": rel, "detail": "文件不存在，无法确认守卫"})
            return
        if re.search(pattern, src, re.S):
            self.ok.append(f"{rule}: {rel}")
        else:
            self.fail.append({"rule": rule, "where": rel,
                              "detail": f"缺少守卫 /{pattern}/ —— {why}"})

    def bad(self, rule: str, where: str, detail: str) -> None:
        self.fail.append({"rule": rule, "where": where, "detail": detail})

    def note(self, msg: str) -> None:
        self.info.append(msg)


# ---------------------------------------------------------------- schema 检查

# schema 字面量的**唯一**识别方式：`json.RawMessage(` + 反引号。
#
# 不能"把所有反引号串两两配对"：注释里到处是 `read` / `bash` 这种行内代码，
# 配对会从那里开始错位，于是整份文件的 schema 一个都解析不到——
# 扫描器会因此"看起来全绿"。team_task 的 schema 就是这么漏掉的。
RAW_SCHEMA = re.compile(r"json\.RawMessage\(\s*`([^`]*)`", re.S)

# 这些键下面挂的**不是 schema**，而是「名字 → schema」的映射。
# 在这一层里，键就是参数名：哪怕它叫 description / required，也只是个名字。
PROPERTY_MAPS = ("properties", "patternProperties", "$defs", "definitions",
                 "dependentSchemas")
# 这些键下面挂的是 schema（或 schema 数组）。
SCHEMA_KEYS = ("items", "additionalProperties", "not", "if", "then", "else",
               "contains", "propertyNames", "unevaluatedProperties",
               "additionalItems", "anyOf", "allOf", "oneOf", "prefixItems")


def walk_schema(node, path, out):
    """遍历 JSON Schema，收集 (path, 对象) 供 required/properties 检查。"""
    if isinstance(node, dict):
        out.append((path, node))
        for k, v in node.items():
            if k in PROPERTY_MAPS and isinstance(v, dict):
                for name, sub in v.items():
                    walk_schema(sub, f"{path}/properties/{name}", out)
            elif k in SCHEMA_KEYS:
                walk_schema(v, f"{path}/{k}", out)
            elif k in ("allOf", "anyOf", "oneOf", "prefixItems") and isinstance(v, list):
                for i, sub in enumerate(v):
                    walk_schema(sub, f"{path}/{k}[{i}]", out)
    elif isinstance(node, list):
        for i, sub in enumerate(node):
            walk_schema(sub, f"{path}[{i}]", out)


def check_required_props(doc) -> list[str]:
    """required 里的每个名字，properties 里必须真的有。

    这正是 2026-11-04 那条的形状：压表把参数删了，required 还点着它。
    模型看到的是「必填、但没类型也没说明」——凭空少一条契约，且不报错。
    """
    nodes: list[tuple[str, dict]] = []
    walk_schema(doc, "$", nodes)
    problems = []
    for path, obj in nodes:
        req = obj.get("required")
        if req is None:
            continue
        if not isinstance(req, list):
            problems.append(f"{path}: required 不是数组")
            continue
        props = obj.get("properties")
        if not isinstance(props, dict):
            problems.append(f"{path}: 有 required 但没有 properties :: {req}")
            continue
        for name in req:
            if name not in props:
                problems.append(f"{path}: required 点名了 {name!r}，properties 里没有")
    return problems


STRIP_DESC = object()


def strip_schema(node, in_property_map=False):
    """模拟 `agent.CompactSchema`（**修好之后**的语义）：只删注解，不删参数名。

    扫描器自己也实现一遍，是为了能对全仓 schema 做一次「压完还满足
    required ⊆ properties 吗」的反证——只查源码里有没有那句话不够。
    """
    if isinstance(node, dict):
        if in_property_map:
            return {k: strip_schema(v, False) for k, v in node.items()}
        out = {}
        for k, v in node.items():
            if k == "description":
                continue
            if k in PROPERTY_MAPS and isinstance(v, dict):
                out[k] = strip_schema(v, True)
            else:
                out[k] = strip_schema(v, False)
        return out
    if isinstance(node, list):
        return [strip_schema(v) for v in node]
    return node


def strip_schema_naive(node):
    """模拟**旧的** CompactSchema：见到 description 键就删，不管它在哪一层。

    只用来算「这个算法在全仓会删掉几个参数」——扫描器必须能反证旧写法是错的，
    否则修完之后"为什么不能这么写"就只剩一段注释。
    """
    if isinstance(node, dict):
        return {k: strip_schema_naive(v) for k, v in node.items() if k != "description"}
    if isinstance(node, list):
        return [strip_schema_naive(v) for v in node]
    return node


def go_files(root, sub="."):
    pat = os.path.join(root, sub, "**", "*.go")
    return sorted(p for p in glob.glob(pat, recursive=True) if not p.endswith("_test.go"))


def rel(root, p):
    return os.path.relpath(p, root).replace("\\", "/")


def scan_schemas(a: Audit) -> None:
    checked = 0
    unparsed = 0
    naive_broken = 0
    for p in go_files(a.root, "modules") + go_files(a.root, "cmd"):
        src = open(p, encoding="utf-8", errors="replace").read()
        if "json.RawMessage" not in src and "parameters" not in src:
            continue
        for m in RAW_SCHEMA.finditer(src):
            raw = m.group(1).strip()
            if not raw.startswith("{"):
                continue
            try:
                doc = json.loads(raw)
            except json.JSONDecodeError:
                if '"properties"' in raw or '"required"' in raw:
                    unparsed += 1  # 拼接出来的 schema：报告里单列，不算绿
                continue
            if not isinstance(doc, dict) or "properties" not in doc:
                continue
            checked += 1
            where = rel(a.root, p)
            for prob in check_required_props(doc):
                a.bad("schema-required-has-prop", where, prob)
            # 压表之后仍然不许出现悬空的 required。
            for prob in check_required_props(strip_schema(doc)):
                a.bad("schema-required-survives-compact", where, prob)
            # 反证：旧算法（见 description 就删）在这个 schema 上会打断几个必填参数。
            naive_broken += len(check_required_props(strip_schema_naive(doc)))
    a.note(f"schema 字面量解析成功 {checked} 个；解析不了（拼接/多段）{unparsed} 个")
    a.note(f"反证：旧 CompactSchema（见 description 就删）会打断 {naive_broken} 条 required——"
           f"这就是 2026-11-04 那条 bug 的全仓投影；现在代码里用的算法必须让这个数为 0")
    if checked == 0:
        a.bad("schema-required-has-prop", "modules/", "一个 schema 都没解析到——扫描器失效，别当绿")


# ---------------------------------------------------------------- 源码数据检查


def scan_empty_result(a: Audit) -> None:
    """工具返回空 Result 却 err == nil —— R3 明确禁止的形态。"""
    pat = re.compile(r"return\s+(?:spi\.)?(?:&\s*)?Result\s*\{\s*\}\s*,\s*nil")
    for p in go_files(a.root, "modules") + go_files(a.root, "internal") + go_files(a.root, "cmd"):
        src = open(p, encoding="utf-8", errors="replace").read()
        for m in pat.finditer(src):
            line = src[:m.start()].count("\n") + 1
            a.bad("empty-result-nil-err", f"{rel(a.root, p)}:{line}",
                  "成功（err=nil）但结果全空：模型只看到「（无输出）」，无法判断做没做")


def scan_append_nil(a: Audit) -> None:
    """会话日志写了 null / 空负载：文件里有字节，但投影读不出任何东西。"""
    pat = re.compile(r"\.Append\(\s*\"[^\"]*\"\s*,\s*nil\s*\)")
    for p in go_files(a.root, "cmd") + go_files(a.root, "internal"):
        src = open(p, encoding="utf-8", errors="replace").read()
        for m in pat.finditer(src):
            line = src[:m.start()].count("\n") + 1
            a.bad("append-nil-payload", f"{rel(a.root, p)}:{line}", "Append(事件, nil) 会落成 data:null")


def scan_writejson_nil(a: Audit) -> None:
    """HTTP 200 + result:null：客户端拿到「成功」，但载荷是空的。"""
    pat = re.compile(r"writeJSON\([^\n]*?,\s*nil\s*\)")
    for p in go_files(a.root, "internal/transport"):
        src = open(p, encoding="utf-8", errors="replace").read()
        for m in pat.finditer(src):
            line = src[:m.start()].count("\n") + 1
            a.bad("writejson-nil", f"{rel(a.root, p)}:{line}",
                  "成功响应传了 nil → {\"result\":null}")


def scan_desc_deletion(a: Audit) -> None:
    """谁在按「键名」删 description？D3 之后只许有一处判断函数。

    允许的唯一落点是 internal/schema.IsAnnotationKey（它同时看位置：这一层
    是不是 properties 映射）。其它 non-test Go 文件再出现
    `k == "description"` / `case "description":` 都按 FAIL 算——那说明又长出
    了第二份「见键就删」。旧版这里是 advisory，于是"修好了"之后第三份照样
    可以悄悄长出来；现在它是一条守卫。
    """
    judge = "internal/schema/schema.go"
    pat = re.compile(r"(?:if\s+k\s*==\s*\"description\"|case\s+\"description\"\s*:)")
    for p in go_files(a.root, "internal") + go_files(a.root, "cmd"):
        src = open(p, encoding="utf-8", errors="replace").read()
        for m in pat.finditer(src):
            line = src[:m.start()].count("\n") + 1
            where = rel(a.root, p)
            if where == judge:
                a.note(f"description 删除点 {where}:{line} — 唯一判断函数")
                continue
            a.bad(
                "description-deleted-without-position", f"{where}:{line}",
                "按 key 名删 description：properties 里的同名参数会被一起删掉且不报错；"
                "规则只能走 internal/schema.IsAnnotationKey",
            )


def scan_stream_callers(a: Audit) -> None:
    """列出所有直接调 Stream 的地方：每处都要自己判空（agent.Loop 之外的入口）。"""
    pat = re.compile(r"\.Stream\(\s*ctx")
    for p in go_files(a.root, "cmd") + go_files(a.root, "internal"):
        src = open(p, encoding="utf-8", errors="replace").read()
        for m in pat.finditer(src):
            line = src[:m.start()].count("\n") + 1
            a.note(f"Stream 调用点 {rel(a.root, p)}:{line}（要确认：空响应/半截流会不会被当成成功）")


# ---------------------------------------------------------------- 守卫检查


def scan_guards(a: Audit) -> None:
    a.need(
        "agent-empty-response", "internal/agent/loop.go", r"func emptyResponseErr\(",
        "空响应必须判成失败（200 + 空流不是「模型没说话」）",
    )
    a.need(
        "compact-schema-keeps-property-names", "internal/schema/schema.go", r"inPropertyMap",
        "压表必须区分「注解 description」与「名叫 description 的参数」",
    )
    # D3（2026-11-04）：规则只能有一份。判断函数在 internal/schema，
    # 两处调用方必须真的调它；谁退回自己按键名删，上面那条或这条就红。
    a.need(
        "agent-uses-shared-schema-rule", "internal/agent/loop.go", r"schema\.Compact\(",
        "CompactSchema 只是入口，实现必须在 internal/schema（不许再留第二份压表）",
    )
    a.need(
        "tighten-uses-shared-schema-rule", "cmd/dshd/tool_merged.go",
        r"schemarule\.IsAnnotationKey\(",
        "tightenSchema 删说明文字必须走同一判断函数，不许另写 `k == \"description\"`",
    )
    a.need(
        "sameshape-uses-shared-schema-rule", "cmd/dshd/tool_merged.go", r"schemarule\.Compact\(",
        "sameShape 的 shapeless 必须复用「只删注解」那一份实现",
    )
    a.need(
        "llm-stream-needs-finish", "internal/llm/client.go", r"sawDone",
        "流必须在 finish_reason 或 [DONE] 之一出现后才算完整；中途 EOF 要报错",
    )
    a.need(
        "session-compact-nonempty-summary", "internal/session/compact.go", r"摘要为空",
        "压缩摘要是空的就不许遮蔽历史（否则历史被换成空节点）",
    )
    a.need(
        "journal-covers-gap", "internal/transport/journal.go", r"func \(j \*Journal\) Window\(",
        "容量丢帧后必须能在一个原子快照里报告「这次补洞有缺口」",
    )
    a.need(
        "history-signals-gap", "internal/transport/server.go", r"win\.Covered",
        "session.history 遇到滑出内存的水位要回 stale_epoch，而不是静默给个短数组",
    )
    a.need(
        "ws-signals-gap", "internal/transport/wssession.go", r"win\.Covered",
        "WS 订阅遇到滑出内存的水位要发 session/reset，而不是静默少帧",
    )
    a.need(
        "log-append-validates", "internal/session/log.go", r"事件类型不能为空",
        "事件没有 type / 负载为 null 时落盘，投影读不出来且不报错",
    )
    a.need(
        "registry-rejects-empty-name", "modules/tools/registry.go", r"工具没有名字|name == \"\"",
        "空名字的工具进了表，模型看不到也调不到",
    )
    a.need(
        "pipeline-empty-result-hint", "modules/tools/pipeline.go", r"模型可见的输出",
        "工具成功但什么都没返回时，模型要拿到一句可执行的话（R3）",
    )
    a.need(
        "turn-end-kind-not-always-completed", "cmd/dshd/main.go", r"session\.TurnEndKind\(",
        "turn/end 恒写 completed：被 max_tokens 截断的回合也记成「完成」",
    )
    a.need(
        "writejson-rejects-nil", "internal/transport/server.go", r"响应是空值",
        "成功响应不许是 null",
    )
    # D4（2026-11-04）：投影只认 `type=="text"` 的病一天在七处各犯了一次
    # （session.load / history_search / 子代理转写 / fork / ACP load / Meter / TUI）。
    # 症状是"有产出、但产出里没有载荷、也不报错"，正是本扫描器管的那一类。
    # 判据本身是**形状**检查，写在 scripts/text-only-guard.py 里；
    # 这里钉的是"它还活着、而且真的接在门禁上"——守卫被删/被摘掉时这条立刻红。
    a.need(
        "text-only-projection-guard-wired", "scripts/check.sh",
        r"scripts/text-only-guard\.py",
        "投影只认 text 的守卫必须接在门禁上（否则「修过」只能靠记忆）",
    )
    a.need(
        "text-only-projection-guard-exists", "scripts/text-only-guard.py",
        r"def scan\(src\)",
        "守卫本体不见了：形状判据（只认 text 的投影）没有别的地方在查",
    )


# ---------------------------------------------------------------- 主流程


def main() -> int:
    ap = argparse.ArgumentParser(description="静默成功（有产出但无载荷）扫描器")
    ap.add_argument("--root", default=".", help="仓库根（默认当前目录）")
    ap.add_argument("--json", action="store_true", help="机器可读输出")
    args = ap.parse_args()
    a = Audit(os.path.abspath(args.root))

    scan_schemas(a)
    scan_empty_result(a)
    scan_append_nil(a)
    scan_writejson_nil(a)
    scan_desc_deletion(a)
    scan_stream_callers(a)
    scan_guards(a)

    if args.json:
        print(json.dumps({"ok": a.ok, "fail": a.fail, "info": a.info},
                         ensure_ascii=False, indent=2))
    else:
        print("静默成功扫描：有产出但没有任何有效载荷的路径")
        print("=" * 66)
        for msg in a.info:
            print("  · " + msg)
        print("-" * 66)
        for msg in a.ok:
            print("  OK   " + msg)
        for f in a.fail:
            print(f"  FAIL {f['rule']} @ {f['where']}\n         {f['detail']}")
        print("-" * 66)
        print(f"守卫/数据检查：{len(a.ok)} 项通过，{len(a.fail)} 项失败")
        if a.fail:
            print("判据提醒：空产出不报错是缺陷，不是「没输出」。先补红测，再修。")

    return 1 if a.fail else 0


if __name__ == "__main__":
    sys.exit(main())
