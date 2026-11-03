#!/usr/bin/env python3
"""放宽守卫：每一处"有意放宽"都必须仍然带着它的可见性。

`docs/deliberate-relaxations.md` 记的是"我们主动放宽了什么、为什么、怎么让人看见"。
问题是那份文档**靠人记得去看**：代码里把"怎么让人看见"那行删掉，文档照样躺着，
没人会发现。这条守卫把那份文档变成可执行的。

每一处登记三样东西（`scripts/relaxations.json`）：

  file          放宽发生在哪个文件
  must_contain  那句**给人看的**话（删掉它 = 放宽变成隐形的）
  doc_anchor    文档里必须有这个片段（防止文档和代码走散）

三种失败：
  ✗ 文件不见了 / 那句话不见了 / 文档里找不到对应段落

注意：这条守卫检查的是"可见性还在不在"，**不是**"这个放宽还该不该存在"。
后者是人做的判断，写进文档的"为什么"里。
"""
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
REG = ROOT / "scripts" / "relaxations.json"
DOC = ROOT / "docs" / "deliberate-relaxations.md"


def main() -> int:
    if not REG.exists():
        print("放宽守卫：没有 scripts/relaxations.json（跳过）")
        return 0
    reg = json.loads(REG.read_text(encoding="utf-8"))
    doc = DOC.read_text(encoding="utf-8") if DOC.exists() else ""
    problems = []

    for name, spec in reg.get("relaxations", {}).items():
        f = ROOT / spec["file"]
        if not f.exists():
            problems.append(f"[{name}] 文件不见了：{spec['file']}")
            continue
        src = f.read_text(encoding="utf-8", errors="replace")
        if spec["must_contain"] not in src:
            problems.append(
                f"[{name}] 「怎么让人看见」那句话不见了：{spec['must_contain']!r}"
                f"（在 {spec['file']} 里）——放宽变成了隐形的"
            )
        if spec["doc_anchor"] not in doc:
            problems.append(
                f"[{name}] 文档里找不到 {spec['doc_anchor']!r}——代码改了文档没跟"
            )

    if problems:
        print("放宽守卫：")
        for x in problems:
            print("  ✗", x)
        return 1
    n = len(reg.get("relaxations", {}))
    print(f"放宽守卫：{n} 处有意放宽，可见性都还在，文档也跟得上")
    return 0


if __name__ == "__main__":
    # CI 的 Windows 控制台不是 UTF-8（cp1252），中文 print 会 UnicodeEncodeError。
    # 那是"打印失败"，不是"检查失败"——两者在门禁输出里不能长得一样，所以强制 UTF-8。
    for _s in (sys.stdout, sys.stderr):
        try:
            _s.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass
    sys.exit(main())
