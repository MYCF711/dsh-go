#!/usr/bin/env python3
"""契约证据守卫：`docs/transport-contract.md` 里每个 ✅ 后面提到的东西，必须真的存在。

为什么需要：H 表里的 ✅ 是**断言**。断言后面写的测试名/脚本名如果不存在，
它就退化成一句"我们做过"，而这句话没人会去核——半年后谁也说不清它凭什么绿。

这里只核一件事：**被点名的证据是否还在仓库里**。
它不能证明证据有效（那是测试自己的事），但能挡住最常见的一种腐烂：
代码重构后测试改名/删掉，而表里的 ✅ 原封不动。
"""
import os
import re
import subprocess
import sys

CONTRACT = "docs/transport-contract.md"
PAT = re.compile(r"`([^`]+)`")


def go_test_names():
    """仓库里所有 Go 测试函数名（扫一遍比逐个文件找稳）。"""
    out = subprocess.run(["grep", "-rhoE", r"func (Test[A-Za-z0-9_]+)",
                          "--include=*_test.go", "internal", "cmd", "modules", "codecs"],
                         capture_output=True, text=True)
    return set(re.findall(r"func (Test[A-Za-z0-9_]+)", out.stdout))


def main():
    ok = fail = 0

    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1
            print("  OK   " + name)
        else:
            fail += 1
            print("  FAIL " + name + " " + str(detail))

    try:
        text = open(CONTRACT, encoding="utf-8").read()
    except OSError as e:
        print("读不到 %s：%s" % (CONTRACT, e))
        return 1

    tests = go_test_names()
    missing = []
    checked = 0

    for line in text.splitlines():
        if not line.startswith("|") or "✅" not in line:
            continue
        cells = line.split("|")
        row_name = cells[1].strip()[:24] if len(cells) > 1 else "?"
        for token in PAT.findall(line):
            token = token.strip()
            if not token:
                continue
            m = re.fullmatch(r"Test[A-Za-z0-9_]+", token)
            if m:
                checked += 1
                if token not in tests:
                    missing.append((row_name, token, "测试函数不存在"))
                continue
            if re.match(r"^[\w./-]+\.(go|py|md|json|jsonl)$", token) and "/" in token:
                checked += 1
                if not os.path.exists(token):
                    missing.append((row_name, token, "文件不存在"))

    if missing:
        for row, token, why in missing:
            print("  FAIL %s → %s（%s）" % (row, token, why))
        fail += len(missing)
    elif checked == 0:
        # 最关键的一条：**守卫自己不能空转**。
        # 解析不到任何证据却报"全部通过"，就是那种最坏的绿。
        fail += 1
        print("  FAIL 没解析到任何证据：要么 ✅ 行没点名测试/脚本，要么解析规则坏了")
    else:
        ok += 1
        print("  OK   %d 处 ✅ 点名的测试/文件都存在" % checked)

    # 反向要求：✅ 行必须点名至少一处证据。
    # 没有证据柄的 ✅ 就是"我们做过"——半年后没人说得清它凭什么绿。
    naked = []
    for ln in text.splitlines():
        if not ln.startswith("| **H") or "✅" not in ln:
            continue
        toks = [t for t in PAT.findall(ln)
                if re.fullmatch(r"Test[A-Za-z0-9_]+", t)
                or (re.match(r"^[\w./-]+\.(go|py|md|json|jsonl)$", t) and "/" in t)]
        if not toks:
            naked.append(ln.split("|")[1].strip()[:20])
    chk("每个 ✅ 都点名了证据", len(naked) == 0, naked)

    stale = [ln.split("|")[1].strip()[:20] for ln in text.splitlines()
             if ln.startswith("| **H") and "⏳" in ln]
    print("  --   仍为 ⏳ 的：%s" % (stale or "无"))

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
