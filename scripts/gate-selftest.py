#!/usr/bin/env python3
"""门禁自测：把门禁要防的东西**真的弄坏一次**，看它红不红。

`scripts/check.sh` 现在二十多项 PASS。PASS 得多不代表验得多——
一条检查可能早就因为代码改动而永远为真（"跑了但什么也没验"），
而输出上它和真正在工作时一模一样。

这个脚本对每一处做四件事：
  1. 把被守护的东西临时改坏
  2. 跑一次门禁，**必须红**（而且最好红在预期那条上）
  3. 无论结果如何都恢复原文件
  4. 再跑一次门禁确认恢复干净，并核对 `git status` 里没留下改动

破坏方式由条目的 `op` 决定：
  op 缺省 / "replace" —— 把 `old` 逐字替换成 `new`（`new` 里的占位符会展开）
  op = "delete"       —— 把整个文件删掉（测"文档存在性"这类检查）

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

# 恢复干净的标准：git 里除了本脚本自己，不许有别的改动。
# 只看"已跟踪文件的改动"会漏掉"删了又建回来但内容不同"；
# 反之把未跟踪文件也算进来又会因为别人正在开发而误报——
# 所以只允许本脚本这两个文件，其余一律算没恢复干净。
ALLOWED_CHANGED = {"scripts/gate-selftest.py", "scripts/gate-selftest.json"}

# op=delete 时禁止删的东西：这些文件被删会让整条门禁失去意义（甚至让人找不到路）。
# 约束写在代码里，不能只写在 json 的说明里（json 也没有注释）。
PROTECTED_DELETE = {"README.md", "PLAN.md", "AGENTS.md", "go.mod", "go.sum", "scripts/check.sh"}


def run_gate():
    t0 = time.time()
    p = subprocess.run(["bash", "scripts/check.sh"], cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", timeout=600)
    return p.returncode, p.stdout + p.stderr, time.time() - t0


def git_changed():
    """返回 git 里当前的改动路径；git 跑不起来返回 None（那种情况不许当成干净）。"""
    p = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    if p.returncode != 0:
        return None
    return [l[3:].strip().strip('"') for l in p.stdout.splitlines() if l.strip()]


def leftover():
    """恢复之后还剩下的、不该出现的改动；干净返回空串。"""
    changed = git_changed()
    if changed is None:
        return "git status 跑不起来"
    return ", ".join(p for p in changed if p not in ALLOWED_CHANGED)


def decode(raw):
    """匹配用的文本：CRLF 统一成 LF。

    工作区里的行尾不统一（.gitattributes 管的是入库形态，checkout 出来的
    某个文件可能还是 CRLF），而 json 里的 old 片段是照 LF 写的。
    这里统一一次，**判断"片段在不在"和"真的去替换"用的是同一份文本**——
    否则会出现"检查说找不到、实际能改"这种自相矛盾的 BAD。
    """
    return raw.decode("utf-8").replace("\r\n", "\n")


def apply_breakage(s, text):
    """按 op 把文件弄坏。调用前已确认文件存在且 old 片段命中。"""
    path = ROOT / s["file"]
    op = s.get("op", "replace")
    if op == "delete":
        path.unlink()
        return
    if op != "replace":
        raise ValueError(f"未知 op: {op}")
    # 占位符在这里展开：仓库里**不能**真的存一个像凭据的字符串，
    # 否则门禁的凭据检查会先把它抓出来。
    filled = re.sub(r"\{(\d+)x([A-Za-z])\}", lambda m: m.group(2) * int(m.group(1)), s["new"])
    path.write_text(text.replace(s["old"], filled, 1), encoding="utf-8", newline="")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only")
    ap.add_argument("--list", action="store_true")
    args = ap.parse_args()

    spec = json.loads(SPEC.read_text(encoding="utf-8"))["breakages"]
    items = [s for s in spec if not args.only or args.only in s["name"]]
    if args.list:
        for s in items:
            how = "删文件" if s.get("op") == "delete" else "改内容"
            print(f"{s['name']:28s} {how}  应当红在: {s['expect']}")
        print(f"\n共 {len(items)} 条")
        return 0

    print(f"门禁自测：{len(items)} 条（每条要跑一次完整门禁）\n")
    failures = []
    for s in items:
        path = ROOT / s["file"]
        op = s.get("op", "replace")
        if op == "delete" and path.name in PROTECTED_DELETE:
            print(f"  BAD     {s['name']}：不许删 {path.name}（这条自测本身写错了）")
            failures.append(s["name"])
            continue
        if not path.exists():
            print(f"  BAD     {s['name']}：被测文件不存在（{s['file']}）")
            failures.append(s["name"])
            continue
        # 字节级备份：恢复必须一模一样，行尾都不许变。
        raw = path.read_bytes()
        text = decode(raw)
        if op == "replace" and s["old"] not in text:
            print(f"  BAD     {s['name']}：文件里找不到要改的片段")
            failures.append(s["name"])
            continue
        try:
            apply_breakage(s, text)
            rc, out, secs = run_gate()
            if rc == 0:
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
            # 删掉的也要建回来：内容在内存里，路径先补上。
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(raw)
        # 恢复后必须回到绿
        rc2, out2, _ = run_gate()
        if rc2 != 0:
            print(f"  ERROR   恢复后门禁仍是红的——源码没恢复干净")
            failures.append(s["name"] + "/restore")
        # 而且 git 里不许留痕（"改回了绿"不等于"改回了原样"）。
        left = leftover()
        if left:
            print(f"  LEFTOVER {s['name']}：git 里还有改动 {left}")
            failures.append(s["name"] + "/leftover")

    print()
    if failures:
        print(f"有 {len(failures)} 条没过：{', '.join(failures)}")
        return 1
    print(f"全部 {len(items)} 条都被门禁抓住，且每次都恢复干净")
    return 0


if __name__ == "__main__":
    sys.exit(main())
