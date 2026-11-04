"""doctor 的故障演练：四种真实故障各造一次，看它指不指得出来。

这不是冒烟测试——每一种都是**真把东西弄坏**，然后看诊断输出。
如果 doctor 指不出来，它就是装饰品。

用法：
    python scripts/doctor-drills.py [--bin tmp/DSH GO.exe]

Windows 上的只读目录用 icacls 造，跑完会还原（脚本里 try/finally）。
"""

import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


# 每项演练的验收判据：doctor 的输出里要出现这些词之一。
# 注意尾部半帧和僵尸锁是**警告**（!），不是错误（✗）——
# 它们能跑，只是值得看一眼。用 ✗ 当判据会误判成"没指出来"。
EXPECT = {
    "断电（尾部半帧）": ["半帧"],
    "僵尸锁": ["锁文件"],
    "网关 500": ["500"],
    "只读 home": ["写不进去", "Access is denied", "denied"],
    "日志写不了": ["是一个文件", "写不了"],
}


def run_doctor(binpath, home, base=None):
    cmd = [binpath, "doctor", "--home", home]
    if base:
        cmd += ["--base", base]
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
    return p.returncode, p.stdout + p.stderr


def drill_torn_tail(binpath, home):
    """断电模拟：跑到一半 kill -9，再砍掉尾部 6 字节。"""
    ws = tempfile.mkdtemp()
    proc = subprocess.Popen(
        [binpath, "run", "--home", home, "--policy", "allow",
         "--workspace-bytes", "0", "--max-steps", "2",
         "--prompt", "用 bash 执行 sleep 3，然后说完成。"],
        cwd=ws, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    # 等它写了几帧再杀
    target = None
    for _ in range(60):
        for dirpath, _, files in os.walk(os.path.join(home, "sessions")):
            for f in files:
                if f == "session.v4.jsonl.zstd":
                    target = os.path.join(dirpath, f)
        if target:
            break
        threading.Event().wait(0.1)
    threading.Event().wait(1.5)
    try:
        proc.kill()
    except Exception:
        pass
    if not target:
        shutil.rmtree(ws, ignore_errors=True)
        return "<没有写出会话>"
    size = os.path.getsize(target)
    with open(target, "r+b") as f:
        f.truncate(max(0, size - 6))
    shutil.rmtree(ws, ignore_errors=True)
    return target


def drill_stale_lock(binpath, home):
    """僵尸锁：别人崩溃留下的锁文件。"""
    locks = []
    for dirpath, _, files in os.walk(os.path.join(home, "sessions")):
        for f in files:
            if f == "session.v4.jsonl.zstd":
                lock = os.path.join(dirpath, f) + ".lock"
                with open(lock, "w") as fh:
                    fh.write("pid=999999\nat=2020-01-01T00:00:00Z\n")
                locks.append(lock)
    return locks


class Status500(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(500)
        self.end_headers()
        self.wfile.write(b"boom")

    def log_message(self, *a):
        pass


def drill_gateway_500():
    srv = HTTPServer(("127.0.0.1", 0), Status500)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_port}/v1"


def drill_readonly_home(home):
    """只读 home：Windows 用 icacls 拒写，POSIX 用 chmod。"""
    if os.name == "nt":
        user = os.environ.get("USERNAME", "")
        subprocess.run(["icacls", home, "/deny", f"{user}:(W)"],
                       capture_output=True)
        return lambda: subprocess.run(["icacls", home, "/remove:d", user],
                                      capture_output=True)
    mode = os.stat(home).st_mode
    os.chmod(home, 0o500)
    return lambda: os.chmod(home, mode)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(ROOT, "tmp", "DSH GO.exe"))
    args = ap.parse_args()
    binpath = os.path.abspath(args.bin)

    drills = []

    # 1. 尾部半帧
    home1 = tempfile.mkdtemp()
    try:
        path = drill_torn_tail(binpath, home1)
        rc, out = run_doctor(binpath, home1)
        drills.append(("断电（尾部半帧）", rc, out, path))
    finally:
        shutil.rmtree(home1, ignore_errors=True)

    # 2. 僵尸锁（先得有一份真实会话，锁才有地方挂）
    home2 = tempfile.mkdtemp()
    try:
        ws2 = tempfile.mkdtemp()
        subprocess.run([binpath, "run", "--home", home2, "--policy", "allow",
                        "--workspace-bytes", "0", "--max-steps", "1",
                        "--prompt", "说一个字"],
                       cwd=ws2, capture_output=True, timeout=180)
        shutil.rmtree(ws2, ignore_errors=True)
        locks = drill_stale_lock(binpath, home2)
        rc, out = run_doctor(binpath, home2)
        drills.append(("僵尸锁", rc, out, "; ".join(locks) or "<没有会话可加锁>"))
    finally:
        shutil.rmtree(home2, ignore_errors=True)

    # 3. 网关 500
    home3 = tempfile.mkdtemp()
    try:
        srv, base = drill_gateway_500()
        try:
            rc, out = run_doctor(binpath, home3, base)
            drills.append(("网关 500", rc, out, base))
        finally:
            srv.shutdown()
    finally:
        shutil.rmtree(home3, ignore_errors=True)

    # 5. 日志写不了（logs 被一个文件占了）
    home5 = tempfile.mkdtemp()
    try:
        with open(os.path.join(home5, "logs"), "w") as fh:
            fh.write("x")
        rc, out = run_doctor(binpath, home5)
        drills.append(("日志写不了", rc, out, os.path.join(home5, "logs") + "（是个文件）"))
    finally:
        shutil.rmtree(home5, ignore_errors=True)

    # 4. 只读 home
    home4 = tempfile.mkdtemp()
    try:
        restore = drill_readonly_home(home4)
        try:
            rc, out = run_doctor(binpath, home4)
            drills.append(("只读 home", rc, out, home4))
        finally:
            restore()
    finally:
        shutil.rmtree(home4, ignore_errors=True)

    print("=" * 70)
    print("doctor 故障演练")
    print("=" * 70)
    caught = 0
    for name, rc, out, what in drills:
        hit = next((w for w in EXPECT[name] if w in out), None)
        caught += 1 if hit else 0
        print()
        print(f"--- {name} ---")
        print(f"造了什么：{what}")
        print(f"退出码：{rc}")
        print(out.rstrip())
        if hit:
            print(f"结论：✓ 指出来了（输出里有「{hit}」）")
        else:
            print(f"结论：✗ 没指出来（期望出现 {EXPECT[name]} 之一）")
    print()
    print(f"{len(drills)} 项里 doctor 指出了 {caught} 项。")
    return 0 if caught == len(drills) else 1


if __name__ == "__main__":
    sys.exit(main())
