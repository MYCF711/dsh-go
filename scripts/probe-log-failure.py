"""日志文件本身坏掉时，宿主还干不干活？

A 线的最后一条：日志**目录**可写、但当天那个**文件**只读（盘满、被改权限）时，
   ① 要明确说"这次没有事后可查的记录"
   ② 但工具照常执行、turn 照常完成

做法：先跑一次造出日志文件 → 把它设成只读 → 再跑一轮要求真调工具的对话。

    python scripts/probe-log-failure.py [--bin tmp/DSH GO.exe]

期望：退出码 0、有「没有事后可查的记录」警告、输出里出现 `← read`、有 stats 行。

注意：**先自证夹具生效**（自己写一次那个文件必须失败）。
这个仓库里已经有 7 次"夹具没造出来却以为造出来了"，别再添第 8 次。
"""

import argparse
import glob
import os
import stat
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(ROOT, "tmp", "DSH GO.exe"))
    args = ap.parse_args()
    exe = os.path.abspath(args.bin)

    home = tempfile.mkdtemp()
    ws = tempfile.mkdtemp()

    # 先跑一次，造出当天的日志文件
    subprocess.run(
        [exe, "run", "--home", home, "--policy", "allow", "--workspace-bytes", "0",
         "--max-steps", "1", "--prompt", "say one word"],
        capture_output=True, cwd=ws, timeout=180,
    )
    logs = glob.glob(os.path.join(home, "logs", "*.log"))
    if not logs:
        print("✗ 连日志文件都没造出来，夹具不成立")
        return 1
    logf = logs[0]

    os.chmod(logf, stat.S_IREAD)
    try:
        with open(logf, "ab") as fh:
            fh.write(b"x")
        print("✗ 夹具无效：python 仍然能写这个日志文件")
        os.chmod(logf, stat.S_IWRITE)
        return 1
    except Exception as e:
        print("✓ 夹具生效：日志文件只读（%s）" % type(e).__name__)

    # 再跑一轮，要求它真的执行一个工具
    with open(os.path.join(ws, "target.txt"), "w") as fh:
        fh.write("hello\n")
    r = subprocess.run(
        [exe, "run", "--home", home, "--policy", "allow", "--workspace-bytes", "0",
         "--max-steps", "3",
         "--prompt", "用 read 工具读 target.txt，然后告诉我第一行内容。"],
        capture_output=True, cwd=ws, timeout=240,
    )
    out = (r.stdout + r.stderr).decode("utf-8", "replace")

    warned = [l for l in out.splitlines() if "没有事后可查" in l]
    tool = [l for l in out.splitlines() if "← read" in l]
    stats = [l for l in out.splitlines() if l.startswith("stats:")]

    print()
    print("=" * 70)
    print("日志文件只读时，宿主还干不干活？")
    print("=" * 70)
    print("退出码:", r.returncode)
    print("警告:", warned[0].strip()[:120] if warned else "（没有）")
    print("工具:", tool[0].strip() if tool else "（没见到工具调用）")
    print("stats:", stats[0].strip() if stats else "（没有）")
    print()

    os.chmod(logf, stat.S_IWRITE)

    ok = r.returncode == 0 and warned and tool and stats
    if ok:
        print("✓ 说了「没有事后可查的记录」，且活照干（工具执行、stats 有账）")
        return 0
    print("✗ 不满足期望：要同时有警告、工具执行、stats")
    return 1


if __name__ == "__main__":
    sys.exit(main())
