#!/usr/bin/env python3
"""帮助文本守卫：`--help` 里的每句话，现在是否还成立。

为什么需要：帮助文本过期**自己不会暴露**——没人会因为一句说明错了去报 bug，
他们只会不去用那个功能。2026-11-03 就踩到一次：`serve -policy` 写着
"ask 等于永远挂起"，而 H13 之后 ask 已经能把审批转给界面了。

这里钉两件事：
  1. 三个入口（run/serve/acp）对同一个概念必须用**同一组措辞**
     （同一个开关换个客户端就消失/改义，是最难解释的差异）
  2. 已知过期的说法不许再出现
"""
import re
import subprocess
import sys

BIN = ["go", "run", "./cmd/dshd"]

# 三个入口共有的概念：名字 → 必须都有的 flag
SHARED = {
    "policy": ["run"],
    "reasoning": ["run", "serve", "acp"],
    "model": ["run", "serve", "acp"],
    "home": ["run", "serve", "acp"],
}

# 已经确认过期、不许再出现的说法（左：入口，右：正则）
BANNED = [
    ("serve", r"ask 等于永远挂起"),          # H13 之后不成立
    ("serve", r"没有终端.*永远挂起"),
]


def help_text(cmd):
    p = subprocess.run(BIN + [cmd, "--help"], capture_output=True, text=True)
    return (p.stdout or "") + (p.stderr or "")


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

    texts = {c: help_text(c) for c in ("run", "serve", "acp")}

    for flag, cmds in SHARED.items():
        for c in cmds:
            chk("%s 有 --%s" % (c, flag), ("-" + flag) in texts[c], texts[c][:80])

    for cmd, pat in BANNED:
        chk("%s 不再出现过期说法 /%s/" % (cmd, pat), not re.search(pat, texts[cmd]))

    # 每个 flag 都必须有一句说明：只有名字没有说明 = 等于没写。
    #
    # 判断方式是"flag 行之后、下一个 flag 行之前，有没有非空文本"。
    # 一开始我只匹配"行尾没有别的内容"，于是把布尔 flag（本来就没有 value）
    # 全判成了缺说明——**守卫自己的判据也要经得起看**。
    for cmd, t in texts.items():
        lines = t.splitlines()
        missing = []
        for i, ln in enumerate(lines):
            m = re.match(r"^\s+-([A-Za-z][\w-]*)", ln)
            if not m:
                continue
            described = False
            for nxt in lines[i + 1:]:
                if re.match(r"^\s+-[A-Za-z]", nxt):
                    break
                if nxt.strip():
                    described = True
                    break
            if not described:
                missing.append(m.group(1))
        chk("%s 每个 flag 都有说明" % cmd, len(missing) == 0, missing[:5])

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
