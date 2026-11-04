#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把工具表**参数区**按"结构词"拆开统计：钱到底花在骨架、参数名，还是可省的东西上。

为什么要有这个脚本：docs/tool-table-budget.md 量到参数区 9,391 字节（compact，默认表），
但"9,391 是骨架贵"这句话还太粗——骨架里既有 JSON 语法必须付的标点，也有
`"type":"object"` 这种**顶层恒真、删了语义不变**的词。不把这两类分开，
就不知道"还能不能压"，只会重复"再写短点描述"这种无效动作。

口径（重要）：
  * 只量 `parameters`，不量工具名/描述/caps（那是另一本账）。
  * 字节 = 与 Go `json.Marshal` 等价的最紧凑序列化（无空格、中文不转义），
    即真正进模型上下文的那串字符的长度。
  * 所有分类**相加等于参数区总字节**（脚本自己校验，不等就非零退出）——
    宁可报错，也不要打一张对不上账的表。

用法：
    python scripts/tool-table-anatomy.py                      # 量默认表（compact）
    python scripts/tool-table-anatomy.py --pre-tighten        # 量"收紧前"的 compact（9,391 那张账）
    python scripts/tool-table-anatomy.py --schema full        # 量完整表（对照）
    python scripts/tool-table-anatomy.py --from-json tmp/x.json
    python scripts/tool-table-anatomy.py --json tmp/x.json    # 另存机器可读结果

退出码：0 = 正常；2 = 拿不到工具表 / 账对不上（绝不把空表当成功）。
"""
import argparse
import collections
import json
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE_CANDIDATES = [os.path.join(ROOT, "tmp", "DSH GO.exe"), os.path.join(ROOT, "DSH GO.exe")]

# 结构词：JSON Schema 里"不是这个工具的参数名、也不是用户数据"的那些键。
# 漏一个就会被算进"其它"，所以宁可多列，最后靠账平校验兜住。
STRUCT_KEYS = (
    "type", "properties", "required", "enum", "items", "additionalProperties",
    "default", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
    "minLength", "maxLength", "pattern", "format", "minItems", "maxItems",
    "uniqueItems", "oneOf", "anyOf", "allOf", "not", "$ref", "definitions",
    "$defs", "title", "deprecated", "readOnly", "writeOnly", "examples",
)


def q(s):
    """JSON 字符串字面量的字节长度（不转义中文，与 Go 一致）。"""
    return len(json.dumps(s, ensure_ascii=False))


def naive_strip(v):
    """重现"收紧之前"的 compact：见到 description 键就删（agent.CompactSchema 的做法）。

    注意它连**名叫 description 的参数**一起删——那是旧实现的信息丢失，
    这里照原样重现，好让 9,391 那张账在新版本上也能对上。
    """
    if isinstance(v, dict):
        return {k: naive_strip(x) for k, x in v.items() if k != "description"}
    if isinstance(v, list):
        return [naive_strip(x) for x in v]
    return v


def dumps(v):
    return json.dumps(v, ensure_ascii=False, separators=(",", ":"))


def size(v):
    return len(dumps(v))


def load_table(schema, from_json):
    """返回 (entries, 来源说明)。entries 是 dshd tools 打出来的那张表。"""
    if from_json:
        with open(from_json, "r", encoding="utf-8") as f:
            return json.load(f), from_json
    exe = next((p for p in EXE_CANDIDATES if os.path.exists(p)), None)
    if exe:
        cmd = [exe, "tools", "--schema", schema]
    else:
        cmd = ["go", "run", "./cmd/dshd", "tools", "--schema", schema]
    p = subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8",
                       errors="replace", cwd=ROOT, timeout=180)
    if p.returncode != 0:
        raise SystemExit("取工具表失败（%s，退出码 %d）：\n%s" % (
            " ".join(cmd), p.returncode, (p.stderr or p.stdout)[-800:]))
    s = (p.stdout or "").strip()
    i = s.find("[")
    if i < 0:
        raise SystemExit("dshd tools 的输出里没有 JSON 数组：\n" + s[:400])
    return json.loads(s[i:]), " ".join(cmd)


class Ledger:
    """按类别记账，保证每一字节都有归属。"""

    def __init__(self):
        self.buckets = collections.Counter()
        self.words = collections.Counter()      # 骨架词的"整段"字节
        self.word_occ = collections.Counter()
        self.fragments = collections.Counter()  # 参数级 schema 片段（用于去重分析）
        self.per_tool = []

    def add(self, bucket, n, word=None, word_bytes=None, word_occ=1):
        """记一笔：n 字节进 bucket；如果给了 word，再把这笔（或 word_bytes）记到词表。

        word_bytes 用于"一个字节段同时属于多个桶"的场合：例如 `"type":"string"`
        这 15 字节里 7 字节是键、8 字节是值，桶要分开记，词表只记整段一次。
        """
        self.buckets[bucket] += n
        if word:
            self.words[word] += n if word_bytes is None else word_bytes
            self.word_occ[word] += word_occ

    def total(self):
        return sum(self.buckets.values())


# 值本身就是"结构声明"的键：它们的 `"key":value` 整段算骨架词。
SCALAR_KEYS = ("type", "additionalProperties", "minimum", "maximum",
               "exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength",
               "minItems", "maxItems", "uniqueItems", "pattern", "format", "default")


def walk(ledger, tool, v, parent_key, depth):
    """遍历一个 schema 值，把它的序列化字节拆进桶里。

    parent_key 是"这个值挂在哪个键下面"，用来判断：
      properties 的子对象键 = 参数名；required/enum 数组元素 = 名字/枚举值。

    记账粒度 = 每个键值对（`"key":` 与其直接值）+ 每个标点；
    words 表里每一字节只出现一次，所以 words 之和必须等于总字节。
    """
    if isinstance(v, dict):
        n = len(v)
        # 花括号 + 键值对之间的逗号（冒号算在键那一段里）
        ledger.add("标点", 2 + max(0, n - 1), "标点（花括号/逗号/方括号）")
        is_props = parent_key == "properties"
        for k, val in v.items():
            keypart = q(k) + 1  # `"key":`
            if is_props:
                ledger.add("参数名", keypart, "参数名（properties 的键）")
                walk(ledger, tool, val, k, depth + 1)
                continue
            if k in SCALAR_KEYS and not isinstance(val, (dict, list)):
                frag = keypart + size(val)
                if k == "type":
                    ledger.add("类型值", size(val), "type:" + str(val).lower(), frag)
                    ledger.add("结构键", keypart, None)
                else:
                    ledger.add("约束值", size(val), "%s:%s" % (k, dumps(val)), frag)
                    ledger.add("结构键", keypart, None)
                continue
            if k in STRUCT_KEYS:
                ledger.add("结构键", keypart, '"%s":' % k, keypart)
            else:
                ledger.add("其它键", keypart, "其它键:" + k, keypart)
            walk(ledger, tool, val, k, depth + 1)
    elif isinstance(v, list):
        n = len(v)
        ledger.add("标点", 2 + max(0, n - 1), "标点（花括号/逗号/方括号）")
        for item in v:
            if parent_key == "required" and isinstance(item, str):
                ledger.add("required 引用", q(item), "required 数组元素（重复的参数名）")
            elif parent_key == "enum":
                ledger.add("枚举值", size(item), "enum 值")
            else:
                walk(ledger, tool, item, parent_key, depth + 1)
    else:
        ledger.add("其它值", size(v), "其它值")


def analyse(entries):
    ledger = Ledger()
    for e in entries:
        params = e.get("parameters")
        if not isinstance(params, dict):
            raise SystemExit("%s 的 parameters 不是对象：%r" % (e.get("name"), params))
        before = ledger.total()
        walk(ledger, e.get("name"), params, None, 0)
        used = ledger.total() - before
        if used != size(params):
            raise SystemExit("账对不上：%s 实占 %d 字节，拆出来 %d" % (
                e.get("name"), size(params), used))
        # 参数级片段：每个 property 的完整 schema，用来找重复定义
        for name, raw in (params.get("properties") or {}).items():
            ledger.fragments[dumps(raw)] += 1
        ledger.per_tool.append((e.get("name"), used, size(params)))
    return ledger


def removables(entries):
    """可去且**语义不变**的字节：逐条重建 schema 实测差值，不靠估算。

    必须递归到嵌套 schema——`additionalProperties:true` 这种默认值在 items /
    properties 里也会出现（本仓有 2 处嵌套的），只查顶层会漏掉 56 字节。
    """
    out = collections.Counter()
    detail = collections.Counter()

    def walk(obj, root, where):
        if not isinstance(obj, dict):
            return
        # 逐条候选：重建一份不含该键的对象，差多少就是省多少（逗号也算进去了）。
        for key, cond, bucket in (
            ("type", root and obj.get("type") == "object", '顶层 type:"object"'),
            ("properties", obj.get("properties") == {}, "空 properties"),
            ("required", obj.get("required") == [], "空 required"),
            ("additionalProperties", obj.get("additionalProperties") is True,
             "additionalProperties:true"),
        ):
            if cond:
                out[bucket] += size(obj) - size({k: v for k, v in obj.items() if k != key})
                detail[bucket] += 1
        for sub in (obj.get("properties") or {}).values():
            walk(sub, False, where)
        if isinstance(obj.get("items"), dict):
            walk(obj["items"], False, where)

    for e in entries:
        walk(e.get("parameters"), True, e.get("name"))
    return out, detail


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--schema", default="compact",
                    help="dshd tools --schema 的取值（compact|full|merged）")
    ap.add_argument("--from-json", default=None, help="直接读一份 dshd tools 的输出")
    ap.add_argument("--pre-tighten", action="store_true",
                    help="重现收紧前的 compact（只删 description 键）：量 9,391 那张账时用")
    ap.add_argument("--json", default=None, help="把结果另存为 JSON")
    a = ap.parse_args()

    schema = "full" if a.pre_tighten else a.schema
    entries, source = load_table(schema, a.from_json)
    if not entries:
        raise SystemExit("工具表是空的——这不算通过")
    if a.pre_tighten:
        for e in entries:
            e["parameters"] = naive_strip(e.get("parameters"))
        source += "（--pre-tighten：只删 description 键，即收紧前的 compact）"
    ledger = analyse(entries)
    total = ledger.total()
    n_tools = len(entries)
    sizes = sorted(t[1] for t in ledger.per_tool)
    median = sizes[len(sizes) // 2]

    print("工具表分析：%d 个工具，参数区 %d 字节（来源 %s）" % (n_tools, total, source))
    print("每工具参数区：均值 %.0f，中位数 %d，最大 %d" % (total / n_tools, median, sizes[-1]))
    print()

    print("== 1. 参数区按类别拆账（相加 = 总字节）==")
    print("%-14s %10s %7s" % ("类别", "字节", "占比"))
    order = ["结构键", "类型值", "参数名", "required 引用", "枚举值",
             "约束值", "其它键", "其它值", "标点"]
    for k in order:
        if ledger.buckets[k]:
            print("%-14s %10d %6.1f%%" % (k, ledger.buckets[k],
                                          100.0 * ledger.buckets[k] / total))
    assert ledger.total() == total
    print("%-14s %10d %6.1f%%" % ("合计", total, 100.0))
    print()

    print("== 2. 骨架词账本（\"整段\"= 键+冒号+它直接带的值）==")
    print("%-28s %6s %10s %7s" % ("骨架词", "出现", "字节", "占比"))
    for w, b in ledger.words.most_common():
        print("%-28s %6d %10d %6.1f%%" % (w, ledger.word_occ[w], b, 100.0 * b / total))
    print()

    rem, rem_n = removables(entries)
    rem_total = sum(rem.values())
    print("== 3. 可去且语义不变的字节（逐条重建 schema 实测）==")
    if rem:
        for k, b in rem.most_common():
            print("%-28s %3d 个 %6d 字节" % (k, rem_n[k], b))
    else:
        print("（无）")
    print("可去合计 %d 字节 = 参数区的 %.1f%%，压完剩 %d 字节" % (
        rem_total, 100.0 * rem_total / total, total - rem_total))
    print()

    print("== 4. 重复的 schema 片段（候选 B：$ref 去重）==")
    cross = collections.Counter()
    for e in entries:
        props = (e.get("parameters") or {}).get("properties") or {}
        for raw in props.values():
            cross[dumps(raw)] += 1
    print("%-52s %6s %6s" % ("片段", "出现", "总字节"))
    for frag, c in cross.most_common(10):
        print("%-52s %6d %6d" % (frag[:52], c, c * len(frag)))
    # $ref 只能在**同一个工具的 schema 内部**起作用：OpenAI 兼容接口的每个
    # function 各带一份 parameters，没有跨工具的 definitions 可引用。
    intra = 0
    intra_occ = 0
    for e in entries:
        props = (e.get("parameters") or {}).get("properties") or {}
        seen = collections.Counter(dumps(v) for v in props.values())
        for frag, c in seen.items():
            if c > 1:
                intra += (c - 1) * len(frag)
                intra_occ += c - 1
    cross_dup = sum((c - 1) * len(f) for f, c in cross.items() if c > 1)
    print()
    print("同一工具内重复（$ref 唯一可能生效的地方）：%d 处，%d 字节" % (intra_occ, intra))
    print("跨工具重复（$ref 用不上：每个 function 各带一份 parameters）：%d 字节" % cross_dup)
    print("→ $ref 的净收益上限 = %d 字节 - 每个工具新增的定义块；跨工具的 %d 字节省不掉。" % (
        intra, cross_dup))
    print()

    print("== 5. 按需装载的上限（候选 C：常驻 30 个 + 其余只留目录）==")
    # 一个工具在表里占的字节 = 名字 + 描述 + 参数 + 信封（约 40：type/function 那层）。
    # 信封是估的，只为量级；真正要问的是"藏起来的那部分值多少"。
    def entry_bytes(e):
        return size(e.get("parameters")) + q(e.get("name", "")) + q(e.get("description", "")) + 40

    ranked = sorted(((entry_bytes(e), e.get("name", "")) for e in entries))
    grand = sum(r[0] for r in ranked)
    for label, resident in (
        ("藏起最**小**的 27 个（对按需装载最不利）", set(r[1] for r in ranked[-30:])),
        ("藏起最**大**的 27 个（最有利）", set(r[1] for r in ranked[:30])),
    ):
        hidden = [e for e in entries if e.get("name") not in resident]
        hb = sum(entry_bytes(e) for e in hidden)
        names_only = sum(q(e.get("name", "")) + 1 for e in hidden)
        with_desc = sum(q(e.get("name", "")) + q(e.get("description", "")) + 2 for e in hidden)
        print("%s：藏 %d 个 %d 字节；目录只列名字 %d 字节，带一句话 %d 字节" % (
            label, len(hidden), hb, names_only, with_desc))
        print("   净省 %d 字节（%.0f%%），目录带一句话则净省 %d 字节（%.0f%%）" % (
            hb - names_only, 100.0 * (hb - names_only) / grand,
            hb - with_desc, 100.0 * (hb - with_desc) / grand))
    print("   这是**上限**：一次 tool_load = 一次调用 + 一份 schema 回填，")
    print("   省下的字节会被「拉一次」付回去一部分；真实收益取决于这些工具多久被用到一次。")
    print()

    print("== 6. 结论数字 ==")
    if rem_total == 0:
        print("这张表已经没有可去的冗余了：%d 字节里 %.0f%% 是语义（参数名/类型/enum/"
              "required/约束），其余是 JSON 语法必须付的标点。" % (
                  total, 100.0 * (total - ledger.buckets["标点"]) / total))
    else:
        print("去掉描述之后，参数区 %.1f%% 是语义（参数名/类型/enum/required/约束），"
              "%.1f%% 是可去的冗余。" % (100.0 * (total - rem_total) / total,
                                         100.0 * rem_total / total))
        print("压完 %.0f 字节，省 %.1f%%。" % (total - rem_total,
                                              100.0 * rem_total / total))

    if a.json:
        with open(a.json, "w", encoding="utf-8") as f:
            json.dump({
                "tools": n_tools, "total": total, "source": source,
                "buckets": dict(ledger.buckets),
                "words": {k: {"occ": ledger.word_occ[k], "bytes": v}
                          for k, v in ledger.words.items()},
                "removable": dict(rem), "removable_total": rem_total,
                "ref_intra": intra, "ref_cross": cross_dup,
            }, f, ensure_ascii=False, indent=2)
        print("明细：%s" % a.json)


if __name__ == "__main__":
    main()
