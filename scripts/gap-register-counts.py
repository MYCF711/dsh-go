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

# 它守住三件事

1. **分母必须等于官方清单的真实行数** —— 不是文档自称多少，是数出来的。
   `docs/evidence/official-*-0.2.2-rc.2.txt` 是取证文件，它们有几行官方就有几项。
2. **每个分面的状态之和必须等于该面总数** —— 不然就是漏了或重复计了。
3. **「当前口径」里不许残留 rc.1 数字** —— 历史界线之后是审计记录，不受此限。

# 用法

    python scripts/gap-register-counts.py            # 打印
    python scripts/gap-register-counts.py --check    # CI 用：对不上就 exit 1
"""

import argparse
import io
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOC = os.path.join(ROOT, "docs", "host-stable-gap-register.md")
EVIDENCE = os.path.join(ROOT, "docs", "evidence")
METHODS_FILE = os.path.join(EVIDENCE, "official-desktop-api-methods-0.2.2-rc.2.txt")
PLUGINS_FILE = os.path.join(EVIDENCE, "official-base-plugins-0.2.2-rc.2.txt")

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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="对不上就 exit 1")
    a = ap.parse_args()

    with io.open(DOC, encoding="utf-8") as fh:
        text = fh.read()

    problems = []
    want = {"methods": evidence_lines(METHODS_FILE), "plugins": evidence_lines(PLUGINS_FILE)}

    # ── 1. 三个分面：总数 == 状态之和，且总数 == 官方清单行数 ──
    for name in ("methods", "plugins", "ui"):
        m = ROW[name].search(text)
        if not m:
            problems.append(
                "找不到「%s」那一行。形状必须是：\n"
                "    | **宿主方法** | **223** | **220** IMPLEMENTED / **3** STRUCTURED_UNIMPLEMENTED / **0** MISSING |\n"
                "    | **base 插件** | **99** | **75** IMPLEMENTED / **17** PARTIAL / **7** MISSING |\n"
                "    | **UI 插件** | **66** | **30** / **19** / **17** |" % name
            )
            continue
        nums = [int(x) for x in m.groups()]
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
    print("PASS  三面口径与官方清单一致，状态之和自洽，当前口径无陈旧数字")
    return 0


if __name__ == "__main__":
    sys.exit(main())
