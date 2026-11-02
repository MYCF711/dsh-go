#!/usr/bin/env python3
"""门禁自测：把门禁要防的东西**真的弄坏一次**，看它红不红。

`scripts/check.sh` 现在二十多项 PASS。PASS 得多不代表验得多——
一条检查可能早就因为代码改动而永远为真（"跑了但什么也没验"），
而输出上它和真正在工作时一模一样。

这个脚本对每一处做三件事：
  1. 把被守护的东西临时改坏
  2. 跑一次门禁，**必须红**（而且最好红在预期那条上）
  3. 无论结果如何都恢复原文件，再跑一次门禁确认恢复干净

用法：
    python scripts/gate-selftest.py            # 跑全部
    python scripts/gate-selftest.py --only policy

比 `mutate.py` 慢（每次要跑整条门禁 ~13 秒），所以默认不进日常流程，
改了门禁之后手动跑一次。
"""
import argparse
import json
import pathlib
import re
import subprocess
import sys
import time

ROOT = pathlib.Path(__file__).resolve().parent.parent
SPEC = ROOT / "scripts" / "gate-selftest.json"


def run_gate():
    t0 = time.time()
    p = subprocess.run(["bash", "scripts/check.sh"], cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", timeout=600)
    return p.returncode, p.stdout + p.stderr, time.time() - t0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only")
    ap.add_argument("--list", action="store_true")
    args = ap.parse_args()

    spec = json.loads(SPEC.read_text(encoding="utf-8"))["breakages"]
    items = [s for s in spec if not args.only or args.only in s["name"]]
    if args.list:
        for s in items:
            print(f"{s['name']:28s} 应当红在: {s['expect']}")
        print(f"\n共 {len(items)} 条")
        return 0

    print(f"门禁自测：{len(items)} 条（每条要跑一次完整门禁）\n")
    failures = []
    for s in items:
        path = ROOT / s["file"]
        original = path.read_text(encoding="utf-8")
        status = "?"
        try:
            if s["old"] not in original:
                print(f"  BAD     {s['name']}：文件里找不到要改的片段")
                failures.append(s["name"])
                continue
            # 占位符在这里展开：仓库里**不能**真的存一个像凭据的字符串，
            # 否则门禁的凭据检查会先把它抓出来。
            filled = re.sub(r"\{(\d+)x([A-Za-z])\}", lambda m: m.group(2) * int(m.group(1)), s["new"])
            path.write_text(original.replace(s["old"], filled, 1), encoding="utf-8", newline="")
            rc, out, secs = run_gate()
            if rc == 0:
                status = "MISSED"
                print(f"  MISSED  {s['name']}：门禁仍然是绿的（这条检查是废的）")
                failures.append(s["name"])
            elif s.get("expect") and s["expect"] not in out:
                caught = [l.strip() for l in out.splitlines() if l.strip().startswith("FAIL")]
                got = caught[0] if caught else "（没找到 FAIL 行）"
                print(f"  WRONG   {s['name']}：红了但不是预期那条（想要 {s['expect']!r}，实际 {got}）")
                failures.append(s["name"])
            else:
                caught = [l.strip()[6:] for l in out.splitlines() if l.strip().startswith("FAIL")]
                who = caught[0] if caught else "?"
                print(f"  CAUGHT  {s['name']}  {secs:.1f}s  抓它的是: {who[:60]}")
        finally:
            path.write_text(original, encoding="utf-8", newline="")
        # 恢复后必须回到绿
        rc2, out2, _ = run_gate()
        if rc2 != 0:
            print(f"  ERROR   恢复后门禁仍是红的——源码没恢复干净")
            failures.append(s["name"] + "/restore")

    print()
    if failures:
        print(f"有 {len(failures)} 条没过：{', '.join(failures)}")
        return 1
    print(f"全部 {len(items)} 条都被门禁抓住，且每次都恢复干净")
    return 0


if __name__ == "__main__":
    sys.exit(main())
