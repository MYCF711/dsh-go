"""验证一条顺序保证：**会话存不下来时，不要再去调模型。**

为什么重要：调一次模型是要花钱、要等的。如果先调模型再发现存不了，
用户白花钱、还拿到一个"答了但没记下"的结果——下次 resume 时它不在。

做法：把 `sessions` 做成一个文件（会话必然建不出来），
网关换成一个**只计数、永远回 500** 的桩，然后看它被打了几次。

    python scripts/probe-no-persist.py [--bin tmp/dshd.exe]

期望：退出码非 0、错误指向会话目录、**网关 0 次**。
"""

import argparse
import os
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class CountingGateway(BaseHTTPRequestHandler):
    hits = []

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        CountingGateway.hits.append("POST " + self.path)
        self.send_response(500)
        self.end_headers()

    def do_GET(self):
        CountingGateway.hits.append("GET " + self.path)
        self.send_response(500)
        self.end_headers()

    def log_message(self, *a):
        pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(ROOT, "tmp", "dshd.exe"))
    args = ap.parse_args()
    binpath = os.path.abspath(args.bin)

    srv = HTTPServer(("127.0.0.1", 0), CountingGateway)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    base = "http://127.0.0.1:%d/v1" % srv.server_port

    home = tempfile.mkdtemp()
    with open(os.path.join(home, "sessions"), "w") as fh:
        fh.write("x")  # 会话目录被一个文件占了

    try:
        p = subprocess.run(
            [binpath, "run", "--home", home, "--base", base, "--policy", "allow",
             "--workspace-bytes", "0", "--max-steps", "1", "--prompt", "x"],
            capture_output=True, timeout=180,
        )
        err_lines = p.stderr.decode("utf-8", "replace").strip().splitlines()
        last = err_lines[-1] if err_lines else "<空>"
        calls = len(CountingGateway.hits)

        print("=" * 70)
        print("会话存不下来时，会不会白调一次模型？")
        print("=" * 70)
        print("退出码:", p.returncode)
        print("最后一行:", last[:160])
        print("网关被打了:", calls, "次")
        for h in CountingGateway.hits:
            print("   ", h)
        print()

        ok = p.returncode != 0 and calls == 0 and "建会话目录失败" in last
        if ok:
            print("✓ 先确认会话能落盘，才去调模型——顺序是对的")
            return 0
        if calls > 0:
            print("✗ 会话都存不下来，还去调了 %d 次模型（白花钱）" % calls)
            return 1
        print("✗ 没有按预期失败，看看上面的输出")
        return 1
    finally:
        srv.shutdown()
        for name in os.listdir(home):
            p2 = os.path.join(home, name)
            if os.path.isfile(p2):
                os.remove(p2)


if __name__ == "__main__":
    sys.exit(main())
