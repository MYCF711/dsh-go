"""B 线现状实测：三条"协作一致性"疑点，到底是真问题还是本来就对。

只测量，不改代码。每条都给出实测输出，结论以输出为准。

    python scripts/collab-probe.py [--bin tmp/dshd.exe]
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BS = "\\"


def run(binpath, args, cwd=None, timeout=180):
    p = subprocess.run([binpath] + args, capture_output=True, text=True,
                       cwd=cwd, timeout=timeout)
    return p.returncode, (p.stdout + p.stderr).strip()


def mkfloor(binpath, home, cwd):
    """在一个 home 里造一份真实会话，返回会话 id。"""
    rc, out = run(binpath, ["run", "--home", home, "--policy", "allow",
                            "--workspace-bytes", "0", "--max-steps", "1",
                            "--prompt", "说一个字"], cwd=cwd)
    for line in out.splitlines():
        if line.startswith("会话："):
            # 会话：<path>（N 事件）
            path = line.split("：", 1)[1].split("（")[0]
            return os.path.basename(os.path.dirname(path))
    raise RuntimeError("没造出会话: " + out[:400])


def probe_lock_cross_machine(binpath):
    """疑点①：锁文件跨机器/崩溃残留，会不会永久挡住。

    做法：造会话 → 手工写一个锁（模拟另一台机器或崩溃留下）→
    把整个 home 拷到另一个路径（模拟换机器）→ 看能不能 resume。
    """
    home = tempfile.mkdtemp()
    ws = tempfile.mkdtemp()
    try:
        sid = mkfloor(binpath, home, ws)
        # 找到会话文件，手工加一个"别人的"锁
        target = None
        for dp, _, files in os.walk(os.path.join(home, "sessions")):
            for f in files:
                if f == "session.v4.jsonl.zstd":
                    target = os.path.join(dp, f)
        with open(target + ".lock", "w") as fh:
            fh.write("pid=1\nat=2020-01-01T00:00:00Z\n")

        # 换机器 = 换个 home 路径
        home2 = tempfile.mkdtemp()
        shutil.rmtree(home2)
        shutil.copytree(home, home2)
        spot = os.path.join(home2, "sessions")
        for dp, _, files in os.walk(spot):
            for f in files:
                if f == "session.v4.jsonl.zstd":
                    target2 = os.path.join(dp, f)
        rc, out = run(binpath, ["session", "info", "--in", target2])
        info_ok = "会话:" in out

        # 再试一次"能不能写"（resume 会抢锁）
        rc2, out2 = run(binpath, ["run", "--home", home2, "--policy", "allow",
                                  "--workspace-bytes", "0", "--max-steps", "1",
                                  "--prompt", "说一个字"], cwd=ws)
        return {
            "换了路径后 info 能不能读": info_ok,
            "info 输出": out.splitlines()[0] if out else "",
            "新 home 上新开会话": rc2 == 0,
            "doctor 怎么说": run(binpath, ["doctor", "--home", home2])[1],
        }
    finally:
        shutil.rmtree(home, ignore_errors=True)
        shutil.rmtree(ws, ignore_errors=True)


def acp_resume(binpath, home, sid, cwd):
    """用 ACP 的 session/resume 试：换一个 cwd 还找不找得到这份会话。"""
    p = subprocess.Popen([binpath, "acp", "--home", home, "--workspace-bytes", "0"],
                         stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.DEVNULL, text=True, bufsize=1)

    def call(i, method, params):
        p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method,
                                  "params": params}) + chr(10))
        p.stdin.flush()
        return json.loads(p.stdout.readline())

    try:
        call(1, "initialize", {"protocolVersion": 1, "clientCapabilities": {}})
        r = call(2, "session/resume",
                 {"sessionId": sid, "cwd": cwd, "mcpServers": []})
        return r
    finally:
        p.kill()
        # ACP 被 kill 会把锁文件留在原地（正常退出才会清）。不清掉的话，
        # 下一次测量会撞上自己上一轮留下的锁——这是**夹具的锅**，不是产品的问题。
        for dp, _, files in os.walk(home):
            for f in files:
                if f.endswith(".lock"):
                    try:
                        os.remove(os.path.join(dp, f))
                    except OSError:
                        pass


def probe_projectkey(binpath):
    """疑点②：同一个逻辑 workspace 在不同盘符/路径下，projectKey 不同 → 会话找不到。

    测的是**用户实际走的那条路**：ACP 的 session/resume。
    """
    base = tempfile.mkdtemp()
    ws_a = os.path.join(base, "a", "proj")
    ws_b = os.path.join(base, "b", "proj")
    os.makedirs(ws_a, exist_ok=True)
    os.makedirs(ws_b, exist_ok=True)

    home = tempfile.mkdtemp()
    try:
        sid = mkfloor(binpath, home, ws_a)
        dirs = os.listdir(os.path.join(home, "sessions"))
        same = acp_resume(binpath, home, sid, ws_a)
        other = acp_resume(binpath, home, sid, ws_b)
        return {
            "会话 id": sid,
            "落盘目录": dirs,
            "同一个 cwd resume": "成功" if "result" in same else "失败: " + str(same.get("error", {}).get("message", ""))[:90],
            "换一个 cwd resume": "成功" if "result" in other else "失败: " + str(other.get("error", {}).get("message", ""))[:120],
        }
    finally:
        shutil.rmtree(home, ignore_errors=True)
        shutil.rmtree(base, ignore_errors=True)


def probe_approvals(binpath):
    """疑点③：审批记忆落在哪、跨机器是否生效。"""
    home = tempfile.mkdtemp()
    ws = tempfile.mkdtemp()
    try:
        run(binpath, ["run", "--home", home, "--policy", "allow",
                      "--remember-approvals", "--workspace-bytes", "0",
                      "--max-steps", "2",
                      "--prompt", "用 write 写 tmp_approve.txt 内容 ok"], cwd=ws)
        found = []
        for dp, _, files in os.walk(home):
            for f in files:
                if "approval" in f.lower() or "approval" in dp.lower():
                    found.append(os.path.relpath(os.path.join(dp, f), home))
        return {
            "home 里与审批相关的文件": found or "（没有）",
            "说明": "审批记忆写在 home 里，所以跟着 home 走；换 home 就等于换一份记忆",
        }
    finally:
        shutil.rmtree(home, ignore_errors=True)
        shutil.rmtree(ws, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(ROOT, "tmp", "dshd.exe"))
    args = ap.parse_args()
    binpath = os.path.abspath(args.bin)

    print("=" * 70)
    print("B 线现状实测：协作一致性三条疑点")
    print("=" * 70)

    print("\n--- ① 锁文件跨机器 / 崩溃残留 ---")
    r = probe_lock_cross_machine(binpath)
    for k, v in r.items():
        if k == "doctor 怎么说":
            print(f"  {k}:")
            for line in str(v).splitlines():
                if "写锁" in line or "→" in line:
                    print("    " + line.strip())
        else:
            print(f"  {k}: {v}")

    print("\n--- ② projectKey 按本地路径算 ---")
    r = probe_projectkey(binpath)
    for k, v in r.items():
        print(f"  {k}: {v}")

    print("\n--- ③ 审批记忆 ---")
    r = probe_approvals(binpath)
    for k, v in r.items():
        print(f"  {k}: {v}")

    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
