#!/usr/bin/env python3
"""开关守卫：定义了 `--flag` 但**没接到任何地方**的，要被抓出来。

为什么需要：定义 flag 只要一行，接上去要改调用链。漏接的表现是
"这个参数我传了，但没生效"，而且它**不报错、不冒烟**——用户只会觉得
"这个开关大概不好使"，然后不用它。

2026-11-03 就撞到过两回：`serve` 没有 `--model`（用户只能改环境变量），
`acp` 没有 `--reasoning`（同一个模型换个客户端就没推理）。

判据：`x := fs.String("name", …)` 之后，变量 x 必须在**别处**再出现至少一次。
只出现在定义那一行 = 死开关。

**这个检查的边界**：它只能证明"变量被引用了"，不能证明"引用到了正确的地方"。
真正到位的证据还是真机：传了这个开关，行为必须跟着变（比如 `--reasoning max`
之后流里要真的出现 reasoning）。
"""
import glob
import re
import sys

DEF = re.compile(r'(\w+)\s*:=\s*fs\.(String|Int|Bool|Duration)\("([\w-]+)"')


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

    dead = []
    total = 0
    for path in sorted(glob.glob("cmd/dshd/*.go")):
        if path.endswith("_test.go"):
            continue
        src = open(path, encoding="utf-8").read()
        for var, kind, name in DEF.findall(src):
            total += 1
            # 去掉定义那一行的匹配，再看剩下还有没有引用
            uses = len(re.findall(r"\b%s\b" % re.escape(var), src)) - 1
            if uses <= 0:
                dead.append("%s: --%s（变量 %s 定义后没再用）" % (path, name, var))

    if dead:
        for d in dead:
            print("  FAIL " + d)
        fail += len(dead)
    else:
        ok += 1
        print("  OK   %d 个开关都接到了调用链上" % total)

    # 反向：**同一个文件**里重复定义的开关名。
    # 跨文件同名是正常的（run/serve/acp 各有自己的 FlagSet，都有 --home / --model），
    # 但同一文件内撞名几乎一定是复制粘贴留下的。
    # 按**函数**分组：一个文件里可能有好几个子命令，各用各的 FlagSet，
    # 跨函数同名是正常的（`--manifest` 在 run/tools/doctor 里都有）。
    dup = []
    for path in sorted(glob.glob("cmd/dshd/*.go")):
        if path.endswith("_test.go"):
            continue
        src = open(path, encoding="utf-8").read()
        for chunk in re.split("\nfunc ", src):
            head = chunk.split("\n", 1)[0]
            names = [n for _, _, n in DEF.findall(chunk)]
            seen = set()
            for n in names:
                if n in seen:
                    dup.append("%s %s: --%s 定义了两次" % (path, head[:32], n))
                seen.add(n)
    chk("同一文件内没有重复定义的开关名", len(dup) == 0, dup[:5])

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
