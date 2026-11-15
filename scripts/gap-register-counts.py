#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gap-register-counts.py —— 让缺口登记表的口径**不能说谎**。

# 为什么要有它

`docs/host-stable-gap-register.md` 是全仓唯一一份"我们离官方还差多少"的底账。
它长期写着「官方方法总数 **193**」「官方 base 插件 id **97**」——**那是 rc.1 口径**。
2026-11-14 全量盘点查出 **149 条陈旧登记**，其中 6 条是反向降级（记成"已做"其实没做）。

根因不是有人手滑，是**这份文件从来没有东西盯着**：数字是人手填的，改完代码没人回头改它。
GUI 仓那份曾经一模一样（分母写死 55 而官方是 66，`--check` 却报 PASS ——
**陈旧口径拿到了绿**），修法是在那边加了 `parity-count.py` 对官方清单逐 id 断言。
这里做同一件事，对象换成宿主仓。

# 它守住四件事

1. **分母必须等于官方清单的真实行数** —— 不是文档自称多少，是数出来的。
   `docs/evidence/official-*-0.2.2-rc.2.txt` 是取证文件，它们有几行官方就有几项。
2. **每个分面的状态之和必须等于该面总数** —— 不然就是漏了或重复计了。
3. **「当前口径」里不许残留 rc.1 数字** —— 历史界线之后是审计记录，不受此限。
4. **base 插件逐项表与结论行逐数相等** —— 见下。

# 为什么还要第 4 条（三面总和守住的东西它守不住）

上面的 1–3 只看**总数**：99 = 75 + 17 + 7 对得上，就绿了。但**每一条**的状态
没人管——某一条从 IMPLEMENTED 悄悄改成 PARTIAL（或反过来）总和照样自洽，
守卫看不见。2026-11-14 盘点查出的 **149 条陈旧登记**（其中 6 条是反向降级：
记成"已做"其实没做）就是这一类漂移。

所以 base 插件面另有一张**机器可读的逐项表**
`docs/evidence/base-plugins-per-item-0.2.2-rc.2.json`（99 行，顺序与官方 txt 一致，
每条带 id / 状态 / 落点证据，可选 `changes` 记"旧登记 → 现在"、`drift` 记证据行号漂移）。
这里对它做三件断言：

  a. **官方清单里每个 id 都要有交代** —— 少一个就红（分母不是抄的，是数的）；
  b. **表里不许有官方清单里没有的 id** —— 编造一个就红；
  c. **表里的状态计数必须与 `docs/host-stable-gap-register.md` 结论表那一行逐数相等**
     —— 把「文档说的」和「逐项表说的」钉在一起，两边都不许单独漂；
     只改一边、或者改了代码没改计数，都会在这里红。

另外两条结构性断言（防止把历史抹掉）：§4.2 的 6 条反向降级必须留着 `changes` 记录；
`changes` 每一步都要真的"变了"，且最后一步必须落到当前状态。

# 用法

    python scripts/gap-register-counts.py            # 打印
    python scripts/gap-register-counts.py --check    # CI 用：对不上就 exit 1
"""

import argparse
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOC = os.path.join(ROOT, "docs", "host-stable-gap-register.md")
EVIDENCE = os.path.join(ROOT, "docs", "evidence")
METHODS_FILE = os.path.join(EVIDENCE, "official-desktop-api-methods-0.2.2-rc.2.txt")
PLUGINS_FILE = os.path.join(EVIDENCE, "official-base-plugins-0.2.2-rc.2.txt")
PER_ITEM_FILE = os.path.join(EVIDENCE, "base-plugins-per-item-0.2.2-rc.2.json")

# base 插件面只认这三档（与结论行同一套词）。DEFERRED / DO_NOT_IMPLEMENT 之类
# 是 rc.1 时代的分档，已经不用了——写进逐项表就是旧口径复活。
PLUGIN_STATUSES = ("IMPLEMENTED", "PARTIAL", "MISSING")

# `changes` 的三种来源：
#   audit-4.1  = 审计报告 §4.1「已不再成立的条目」（40 行旧登记 → 现在的档）
#   audit-4.2  = 审计报告 §4.2「反向变化」（旧的 IMPLEMENTED 要降级）
#   post-audit-pr = 审计之后已合并 PR 的收口结论（本表 11 条：#24/#25/#26/#28）
CHANGE_SOURCES = ("audit-4.1", "audit-4.2", "post-audit-pr")

# §4.2 的 6 条反向降级。**只钉"历史必须留着"，不钉当前状态**：
# 将来真把它补上了，就该追加一条 post-audit-pr 的 change，而不是把这条历史删掉。
REVERSE_DOWNGRADES = {
    "agent": "PARTIAL",
    "command-compact": "MISSING",
    "llm-deepseek-account": "MISSING",
    "session-log-deepseek": "MISSING",
    "session-telemetry-otel": "MISSING",
    "shell-env": "MISSING",
}

# §4.1 的 40 行按 id 计是 43（尾行一行覆盖 4 个 id：tool-result-pruner /
# spill-local / spill-policy / session-checkpoint-policy）。改这个数就等于改审计。
FORWARD_STALE_IDS = 43

try:
    sys.stdout.reconfigure(encoding="utf-8")
except Exception:
    pass

# 结论表的三行。**形状固定**，改形状就得同步改这里。
ROW = {
    "methods": re.compile(
        r"\|\s*\*\*宿主方法\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|\s*"
        r"\*\*(\d+)\*\*\s*IMPLEMENTED\s*/\s*\*\*(\d+)\*\*\s*STRUCTURED_UNIMPLEMENTED\s*/\s*\*\*(\d+)\*\*\s*MISSING"
    ),
    "plugins": re.compile(
        r"\|\s*\*\*base 插件\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|\s*"
        r"\*\*(\d+)\*\*\s*IMPLEMENTED\s*/\s*\*\*(\d+)\*\*\s*PARTIAL\s*/\s*\*\*(\d+)\*\*\s*MISSING"
    ),
    "ui": re.compile(
        r"\|\s*\*\*UI 插件\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|\s*"
        r"\*\*(\d+)\*\*\s*/\s*\*\*(\d+)\*\*\s*/\s*\*\*(\d+)\*\*"
    ),
}

# rc.1 口径的老数字。出现在「当前口径」的表格行里即红。
STALE = ["193", "97"]

# 历史界线。它之后的内容是 rc.1 时代的审计记录——作为历史**是准确的**，不该被反咬。
HISTORY_MARKER = "以下各表是"


def evidence_lines(path):
    """官方清单有几行就是几项。读不到就抛——**不许静默退化成 0**。"""
    with io.open(path, encoding="utf-8") as fh:
        return sum(1 for line in fh if line.strip())


def official_plugin_ids():
    """官方 base 插件清单：一行一个 id，**顺序也是契约**（逐项表必须同序）。"""
    with io.open(PLUGINS_FILE, encoding="utf-8") as fh:
        return [line.strip() for line in fh if line.strip()]


def check_per_item(problems, row_counts):
    """第 4 条：99 个插件逐项有交代，状态计数与结论行逐数相等。

    row_counts = 结论行 base 插件那一格的 (总数, IMPLEMENTED, PARTIAL, MISSING)；
    没有那一行时传 None，只做 id 一一对应与表自身计数自洽。
    """
    official = official_plugin_ids()
    if not os.path.exists(PER_ITEM_FILE):
        problems.append("找不到逐项表 %s —— 99 个插件各自的状态没人守了" % PER_ITEM_FILE)
        return
    try:
        with io.open(PER_ITEM_FILE, encoding="utf-8") as fh:
            doc = json.load(fh)
    except Exception as exc:  # noqa: BLE001 —— 读不出来就是红，不许吞
        problems.append("逐项表解析失败（%s）：%r" % (PER_ITEM_FILE, exc))
        return

    if doc.get("schema") != "dsh.base-plugins-per-item/1":
        problems.append("逐项表 schema 不是 `dsh.base-plugins-per-item/1`：%r" % (doc.get("schema"),))
    if doc.get("baseline") != "0.2.2-rc.2":
        problems.append("逐项表 baseline 不是 `0.2.2-rc.2`：%r" % (doc.get("baseline"),))

    items = doc.get("items")
    if not isinstance(items, list):
        problems.append("逐项表 items 不是数组")
        return

    by_id = {}
    for it in items:
        if not isinstance(it, dict):
            problems.append("逐项表 items 里有非对象条目：%r" % (it,))
            continue
        pid = it.get("id")
        st = it.get("status")
        if not isinstance(pid, str) or not pid:
            problems.append("逐项表有条目没有 id：%r" % (it,))
            continue
        if pid in by_id:
            problems.append("逐项表 id 重复：%s" % pid)
            continue
        if st not in PLUGIN_STATUSES:
            problems.append(
                "逐项表 %s 的状态 %r 不在 %s 里 —— 只认这三档，"
                "DEFERRED / DO_NOT_IMPLEMENT 是 rc.1 旧口径" % (pid, st, "/".join(PLUGIN_STATUSES))
            )
            continue
        if it.get("audit_status") not in PLUGIN_STATUSES:
            problems.append("逐项表 %s 的 audit_status %r 不合法"
                            % (pid, it.get("audit_status")))
        if not isinstance(it.get("evidence"), str):
            problems.append("逐项表 %s 的 evidence 必须是字符串（可空，但字段要在）" % pid)
        by_id[pid] = it

    ids = [it.get("id") for it in items if isinstance(it, dict)]
    # a. 官方清单里每个 id 都要有交代 —— 少一个就是分母漏了
    missing = [pid for pid in official if pid not in by_id]
    if missing:
        problems.append(
            "官方清单里有 %d 个 id 在逐项表里没有交代：%s%s"
            % (len(missing), " ".join(missing[:8]), " …" if len(missing) > 8 else "")
        )
    # b. 表里不许有官方清单里没有的 id —— 编造一个就红
    official_set = set(official)
    extra = [pid for pid in ids if isinstance(pid, str) and pid not in official_set]
    if extra:
        problems.append(
            "逐项表里有 %d 个官方清单不存在的 id（编造）：%s%s"
            % (len(extra), " ".join(extra[:8]), " …" if len(extra) > 8 else "")
        )
    if len(items) != len(official):
        problems.append("逐项表 %d 行，官方清单 %d 行 —— 行数不等" % (len(items), len(official)))
    elif not missing and not extra and ids != official:
        problems.append("逐项表 id 顺序与官方清单不一致 —— 表的顺序就该是官方清单的顺序")

    # 表自称的计数必须等于数出来的
    counts = dict((st, sum(1 for it in by_id.values() if it.get("status") == st))
                  for st in PLUGIN_STATUSES)
    declared = doc.get("counts") or {}
    for st in PLUGIN_STATUSES:
        if declared.get(st) != counts[st]:
            problems.append("逐项表自称 %s=%r，实际数出来 %d —— 表自己的计数就不对"
                            % (st, declared.get(st), counts[st]))
    if declared.get("total") != len(official):
        problems.append("逐项表自称 total=%r，官方清单 %d 行"
                        % (declared.get("total"), len(official)))

    # 历史：§4.1 的陈旧登记与 §4.2 的反向降级都必须是 `changes` 里的真实一步
    forward_ids, reverse_ids = set(), set()
    for pid, it in by_id.items():
        drift = it.get("drift")
        if drift is not None:
            if not isinstance(drift, dict) or drift.get("source") != "audit-4.1":
                problems.append("逐项表 %s 的 drift 来源必须是 audit-4.1：%r" % (pid, drift))
            else:
                forward_ids.add(pid)
        chg = it.get("changes")
        if chg is None:
            continue
        if not isinstance(chg, list) or not chg:
            problems.append("逐项表 %s 的 changes 必须是非空数组" % pid)
            continue
        for step in chg:
            if not isinstance(step, dict):
                problems.append("逐项表 %s 的 changes 里有非对象步骤" % pid)
                continue
            src = step.get("source")
            if src not in CHANGE_SOURCES:
                problems.append("逐项表 %s 的 changes 来源 %r 不认识（只认 %s）"
                                % (pid, src, " / ".join(CHANGE_SOURCES)))
                continue
            if step.get("to") not in PLUGIN_STATUSES:
                problems.append("逐项表 %s 的 changes.to %r 不合法" % (pid, step.get("to")))
            if step.get("from") == step.get("to"):
                problems.append("逐项表 %s 有一处 changes 前后状态相同（%r）—— 那不是变化"
                                % (pid, step.get("from")))
            if src in ("audit-4.1", "audit-4.2") and step.get("to") != it.get("audit_status"):
                problems.append("逐项表 %s 的 %s 步骤落点 %r 与 audit_status %r 不一致"
                                % (pid, src, step.get("to"), it.get("audit_status")))
            if src == "post-audit-pr" and step.get("from") != it.get("audit_status"):
                problems.append("逐项表 %s 的 post-audit-pr 步骤必须从 audit_status %r 出发，实际从 %r"
                                % (pid, it.get("audit_status"), step.get("from")))
            if src == "audit-4.1":
                forward_ids.add(pid)
            elif src == "audit-4.2":
                reverse_ids.add(pid)
                if REVERSE_DOWNGRADES.get(pid) != step.get("to"):
                    problems.append(
                        "逐项表 %s 的 §4.2 反向降级落点 %r 与审计的 %r 不一致"
                        % (pid, step.get("to"), REVERSE_DOWNGRADES.get(pid))
                    )
        if isinstance(chg[-1], dict) and chg[-1].get("to") != it.get("status"):
            problems.append("逐项表 %s 的 changes 最后一步落到 %r，但 status 是 %r"
                            % (pid, chg[-1].get("to"), it.get("status")))

    if len(forward_ids) != FORWARD_STALE_IDS:
        problems.append(
            "§4.1 的 40 行陈旧登记按 id 计是 %d 个，逐项表只标了 %d 个 —— 旧登记被纠正过这件事"
            "必须留在表里，删掉它就等于让陈旧口径复活" % (FORWARD_STALE_IDS, len(forward_ids))
        )
    for pid in REVERSE_DOWNGRADES:
        if pid not in reverse_ids:
            problems.append("§4.2 的反向降级 %s 在逐项表里没有 audit-4.2 记录 —— 历史被抹掉了" % pid)
    for pid in sorted(reverse_ids - set(REVERSE_DOWNGRADES)):
        problems.append("逐项表把 %s 标成了 audit-4.2 反向降级，但 §4.2 只有 %d 条"
                        % (pid, len(REVERSE_DOWNGRADES)))

    # c. 与结论行逐数相等 —— 这条是"两边都不许单独漂"的钉子
    equal = True
    if row_counts:
        total, impl, part, miss = row_counts
        if total != len(official):
            equal = False
            problems.append("结论行 base 插件总数 %d != 官方清单 %d 行" % (total, len(official)))
        if (counts["IMPLEMENTED"], counts["PARTIAL"], counts["MISSING"]) != (impl, part, miss):
            equal = False
            problems.append(
                "逐项表说 %d IMPLEMENTED / %d PARTIAL / %d MISSING，结论行说 %d / %d / %d"
                " —— 两边对不上。改完代码要同步计数，两边都不许单独漂"
                % (counts["IMPLEMENTED"], counts["PARTIAL"], counts["MISSING"], impl, part, miss)
            )

    # known_drift：表里如实登记的"文档说已收口、结论行没计入"的条目。
    # 不是错误（不许因为它在就报红），但 id 必须是官方清单里的，且每次跑都要露出来。
    # 清空之后用 known_drift_note 交代"那些条目去哪儿了"，不许无声消失。
    note = doc.get("known_drift_note")
    if note is not None and not isinstance(note, str):
        problems.append("逐项表 known_drift_note 必须是字符串")
    known = doc.get("known_drift") or []
    if not isinstance(known, list):
        problems.append("逐项表 known_drift 不是数组")
        known = []
    for ent in known:
        if not isinstance(ent, dict) or ent.get("id") not in official_set:
            problems.append("逐项表 known_drift 里有官方清单不存在的 id：%r" % (ent,))

    print(
        "  %-8s 逐项 %d/%d = %d IMPLEMENTED / %d PARTIAL / %d MISSING   %s"
        % (
            "base插件",
            len(by_id),
            len(official),
            counts["IMPLEMENTED"],
            counts["PARTIAL"],
            counts["MISSING"],
            ("结论行逐数相等" if equal else "!! 与结论行对不上")
            + "（陈旧登记 %d 条 + 反向降级 %d 条已入表）" % (len(forward_ids), len(reverse_ids)),
        )
    )
    if known:
        print(
            "  NOTE     known_drift %d 条（文档说已收口、结论行计数没算进去，待 Lead 裁决）：%s"
            % (len(known), " ".join(str(ent.get("id")) for ent in known))
        )
        print("           见逐项表 known_drift 字段；要改就同时改结论行与逐项表（守卫会把两边钉死）。")
    elif isinstance(note, str) and note:
        print("  NOTE     known_drift 已清空：%s" % note[:110])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="对不上就 exit 1")
    a = ap.parse_args()

    with io.open(DOC, encoding="utf-8") as fh:
        text = fh.read()

    problems = []
    want = {"methods": evidence_lines(METHODS_FILE), "plugins": evidence_lines(PLUGINS_FILE)}
    row_nums = {}

    # ── 1. 三个分面：总数 == 状态之和，且总数 == 官方清单行数 ──
    for name in ("methods", "plugins", "ui"):
        m = ROW[name].search(text)
        if not m:
            problems.append(
                "找不到「%s」那一行。形状必须是：\n"
                "    | **宿主方法** | **223** | **220** IMPLEMENTED / **3** STRUCTURED_UNIMPLEMENTED / **0** MISSING |\n"
                "    | **base 插件** | **99** | **84** IMPLEMENTED / **11** PARTIAL / **4** MISSING |\n"
                "    | **UI 插件** | **66** | **30** / **19** / **17** |" % name
            )
            continue
        nums = [int(x) for x in m.groups()]
        row_nums[name] = nums
        total, parts = nums[0], nums[1:]
        if sum(parts) != total:
            problems.append(
                "%s：总数 %d，状态之和却是 %d（%s）—— 漏了或重复计了"
                % (name, total, sum(parts), " + ".join(str(p) for p in parts))
            )
        if name in want and total != want[name]:
            problems.append(
                "%s：文档说总数 %d，官方清单实际 **%d** 行 —— 分母对不上"
                % (name, total, want[name])
            )
        print(
            "  %-8s 总数 %d = %s   %s"
            % (
                name,
                total,
                " + ".join(str(p) for p in parts),
                ("官方清单 %d 行 OK" % want[name])
                if name in want
                else "(分母在 GUI 仓，那边 parity-count.py 守着)",
            )
        )

    # ── 1.5 base 插件逐项表：99 条 id 一一对应，状态计数与上面那行逐数相等 ──
    check_per_item(problems, row_nums.get("plugins"))

    # ── 2. 「当前口径」里不许残留 rc.1 数字 ──
    marker_at = text.find(HISTORY_MARKER)
    if marker_at < 0:
        problems.append(
            "找不到历史界线 `以下各表是...` —— 没有界线就分不清「当前口径」与「历史记录」，"
            "守卫只能一律当错"
        )
    current = text if marker_at < 0 else text[:marker_at]
    for line in current.splitlines():
        if not line.lstrip().startswith("|"):
            continue
        for stale in STALE:
            if re.search(r"\*\*%s\*\*" % re.escape(stale), line):
                problems.append(
                    "当前口径的表格里还有 rc.1 的 **%s**：%s" % (stale, line.strip()[:90])
                )

    if not a.check:
        return 0
    if problems:
        print("FAIL  缺口登记表口径对不上：")
        for p in problems:
            print("      - %s" % p)
        print("      这张表是**数出来的**，不是手填的——改完代码要同步计数。")
        return 1
    print("PASS  缺口登记表口径：三面与官方清单一致，状态之和自洽，当前口径无陈旧数字，"
          "base 插件逐项表与结论行逐数相等")
    return 0


if __name__ == "__main__":
    sys.exit(main())
