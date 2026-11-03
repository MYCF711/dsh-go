#!/usr/bin/env python3
"""方法守卫：Transport 的 14 个 method，登记了就必须在分发里被处理。

为什么需要：method 列表是**对外声明的能力**，switch 才是真正的实现。
两者脱节时的表现是运行期 501「not implemented」——而界面拿到 501 只会
以为"这个版本还没做"，不会来报 bug。声明与实现必须一一对上。

两个方向都查：
  - 声明了但 switch 里没有 → 调用必然 501
  - switch 里有但没声明 → 快照/文档看不见它，等于私下多开一个口子
"""
import re
import sys

API = "internal/transport/api.go"
SRV = "internal/transport/server.go"
# 允许传两个路径，只为**反证**用：在临时副本里删掉一个 case，
# 守卫必须红。跑不红的守卫等于没有守卫。
if len(sys.argv) == 3:
    API, SRV = sys.argv[1], sys.argv[2]


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

    api = open(API, encoding="utf-8").read()
    srv = open(SRV, encoding="utf-8").read()

    # 声明：MethodXxx Method = "session.yyy"
    declared = dict(re.findall(r'(Method\w+)\s+Method\s*=\s*"([^"]+)"', api))
    # 分发：case MethodXxx:
    dispatched = set(re.findall(r"case (Method\w+):", srv))

    missing = sorted(set(declared) - dispatched)
    extra = sorted(dispatched - set(declared))

    chk("声明了 %d 个 method、分发了 %d 个" % (len(declared), len(dispatched)),
        len(declared) > 0 and len(dispatched) > 0)
    chk("每个 method 都在分发里", not missing,
        ["%s (%s)" % (m, declared.get(m, "?")) for m in missing])
    chk("分发里没有未声明的 method", not extra, extra)

    # 反向：快照里的方法名必须都来自声明的字符串
    try:
        snap = open("internal/transport/testdata/api.snapshot.json", encoding="utf-8").read()
    except OSError:
        snap = ""
    if snap:
        names = set(re.findall(r'"(session\.[\w.]+|workspace\.[\w.]+|fs\.[\w.]+|config\.[\w.]+)"', snap))
        unknown = sorted(n for n in names if n not in declared.values())
        chk("快照里的方法名都来自声明", not unknown, unknown)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
