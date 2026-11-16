#!/usr/bin/env python3
"""方法守卫：Transport 声明的 method 必须有真实路由，路由也必须已声明。

覆盖 switch case、Method(name)==MethodX 的 if 分派与 HasPrefix 族处理器。
声明与路由脱节会在运行期变成 501「not implemented」或快照外的私有入口。
"""
import re
import sys
from pathlib import Path

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

    if len(sys.argv) == 3:
        sources = [(Path(API), Path(SRV))]
    else:
        # Transport 已按领域拆到 handlers_*.go；只读 api.go/server.go 会把真实
        # 声明误报缺失、把真实分发误报私有方法。默认必须覆盖整棵生产源码树。
        sources = [(path, path) for path in Path("internal/transport").rglob("*.go")
                   if not path.name.endswith("_test.go")]
    api = "\n".join(path.read_text(encoding="utf-8") for pair in sources for path in pair[:1])
    srv = "\n".join(path.read_text(encoding="utf-8") for pair in sources for path in pair[1:])

    # 声明：MethodXxx Method = "session.yyy"
    declared = dict(re.findall(r'(Method\w+)\s+Method\s*=\s*"([^"]+)"', api))
    # 分发：单个与分组 case 都算（case MethodA, MethodB:）。旧正则只认单个，
    # 会把分组里第二个以后的方法误报成未分发。
    dispatched = set()
    for case in re.findall(r"^\s*case\s+([^:]+):", srv, re.MULTILINE):
        dispatched.update(re.findall(r"Method\w+", case))

    wired = set(dispatched)
    if len(sys.argv) != 3:
        # 新代码还有两类真实入口：Method(name)==MethodX 的 if 分派，以及
        # strings.HasPrefix(name,"models.catalog.") 的族处理器。它们不是 switch case，
        # 但同样证明请求被路由，而不是落到未知方法。
        handlers = "\n".join(path.read_text(encoding="utf-8")
                             for path in Path("internal/transport").rglob("*.go")
                             if not path.name.endswith("_test.go") and path.as_posix() != API)
        referenced = set(re.findall(r"Method\w+", handlers))
        wired.update(referenced & set(declared))
        prefixes = set(re.findall(r"strings\.HasPrefix\(name,\s*\"([\w.]+)\"\)", handlers))
        for method, wire_name in declared.items():
            parts = wire_name.split(".")
            # 任意已接线的祖先族都算，例如 settings.form.* 由 settings. 的
            # SettingsFormHandler 分派；不能要求每个叶子都再写一条 HasPrefix。
            families = [".".join(parts[:i]) + "." for i in range(1, len(parts))]
            if any(family in prefixes for family in families):
                wired.add(method)

    missing = sorted(set(declared) - wired)
    extra = sorted(dispatched - set(declared))

    chk("声明了 %d 个 method、分发了 %d 个" % (len(declared), len(wired)),
        len(declared) > 0 and len(wired) > 0)
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
