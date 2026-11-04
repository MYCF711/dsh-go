#!/usr/bin/env python3
"""守卫自测：把每个守卫**真的弄坏一次**，看它红不红。

规矩：新增任何守卫，都要在这里加一条"弄坏它、它必须红"。
理由很直接——跑不红的守卫等于没有守卫，而"它看起来在检查"这件事
没有任何外部信号能反驳。2026-11-03 的教训：`grep default "0"` 那条约等于恒真，
绿了很久才发现它从来没看过东西。

做法：在 tmp/ 里造一份被改坏的副本，用守卫自己的入参指过去，断言退出码非 0。
不改仓库里的任何文件。
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile

# 造红用的草稿目录**必须放在仓库外**。
#
# 以前是 `tmp/guard-selftest`：它建在仓库里，而门禁后面还会跑 `staticcheck ./...`——
# 两个进程撞在一起时，staticcheck 正好扫到这个正在建/删的目录，报
# `open tmp\guard-selftest: The system cannot find the file specified`，
# 于是"守卫自测"把静态检查搞红了（实测踩过一次，红的原因与代码无关）。
# 用系统临时目录：仓库树里的东西对 `./...` 不可见，也就不存在这个赛跑。
TMP = os.path.join(tempfile.mkdtemp(prefix="dshgo-guard-selftest-"), "root")


def run(args):
    p = subprocess.run([sys.executable, "-u"] + args, capture_output=True, text=True)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def main():
    ok = fail = 0

    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1
            print("  OK   " + name)
        else:
            fail += 1
            print("  FAIL " + name + " " + str(detail)[:300])

    shutil.rmtree(TMP, ignore_errors=True)
    os.makedirs(TMP, exist_ok=True)

    # ---- 1) contract-evidence：把 ✅ 换成点名一个不存在的测试 ----
    src = open("docs/transport-contract.md", encoding="utf-8").read()
    broken = src.replace("`TestLoadPassesViewThrough`", "`TestThisDoesNotExistAnywhere`", 1)
    p = os.path.join(TMP, "contract.md")
    open(p, "w", encoding="utf-8", newline="").write(broken)
    code, out = run(["scripts/contract-evidence.py", p])
    chk("contract-evidence 对不存在的证据会红", code != 0 and "不存在" in out, out.strip()[-160:])

    # ---- 2) flag-wired：造一个定义了却没用的开关 ----
    d = os.path.join(TMP, "cmd")
    os.makedirs(d, exist_ok=True)
    open(os.path.join(d, "x.go"), "w", encoding="utf-8", newline="").write(
        'package main\n\nfunc f(args []string) {\n\tfs := flag.NewFlagSet("x", flag.ExitOnError)\n'
        '\tdead := fs.String("dead", "", "没人用的开关")\n\t_ = fs.Parse(args)\n}\n')
    code, out = run(["scripts/flag-wired.py", d])
    chk("flag-wired 对没接线的开关会红", code != 0 and "--dead" in out, out.strip()[-160:])

    # ---- 3) method-wired：删掉一个分发 case ----
    d2 = os.path.join(TMP, "tr")
    os.makedirs(d2, exist_ok=True)
    shutil.copy("internal/transport/api.go", os.path.join(d2, "api.go"))
    srv = open("internal/transport/server.go", encoding="utf-8").read()
    srv = srv.replace("\tcase MethodSessionApprove:", "\tcase MethodSessionApproveGone:", 1)
    open(os.path.join(d2, "server.go"), "w", encoding="utf-8", newline="").write(srv)
    code, out = run(["scripts/method-wired.py",
                     os.path.join(d2, "api.go"), os.path.join(d2, "server.go")])
    chk("method-wired 对缺失的分发会红", code != 0 and "MethodSessionApprove" in out,
        out.strip()[-160:])

    # ---- 3.5) bin-check：把二进制摸旧，必须红 ----
    stale = os.path.join(TMP, "stale.exe")
    shutil.copy("tmp/dshd.exe", stale)
    os.utime(stale, (1577836800, 1577836800))  # 2020-01-01
    code, out = run(["scripts/bin-check.py", stale])
    chk("bin-check 对陈旧二进制会红", code != 0 and "旧" in out, out.strip()[-160:])

    # ---- 4) 空转保护：守卫在没有输入时不许报绿 ----
    empty = os.path.join(TMP, "empty")
    os.makedirs(empty, exist_ok=True)
    code, out = run(["scripts/flag-wired.py", empty])
    chk("flag-wired 对空目录不报绿", code == 0 and "0 个开关" in out, out.strip()[-120:])

    # ---- 5) doc-links：本仓缺失必红，跨仓引用必跳过 ----
    #
    # 这条守的是两个方向，缺一个都不算数：
    #   - 本仓真缺了文件 → 必须 MISS 且非零（否则门禁形同虚设）
    #   - 跨仓引用（../dsh-ui/...）→ 必须 SKIP 且不报红（否则永远误报）
    # 造一份坏手册而不是改真的 AGENTS.md：那会把别人的文档搞坏。
    dl = os.path.join(TMP, "doclinks")
    os.makedirs(dl, exist_ok=True)
    bad_manual = os.path.join(dl, "BAD.md")
    with open(bad_manual, "w", encoding="utf-8") as fh:
        fh.write("见 docs/missing-file.md 与 ../dsh-ui/docs/progress.md\n")
    code, out = run(["scripts/doc-links.py", bad_manual])
    chk("doc-links 对本仓缺失会红", code != 0 and "MISS docs/missing-file.md" in out, out.strip()[-200:])
    chk("doc-links 对跨仓引用跳过", "SKIP ../dsh-ui/docs/progress.md" in out, out.strip()[-200:])

    # 中文文件名：原来的字符类 `[A-Za-z0-9_.\-/]` 根本不匹配中文，
    # 于是「手册引用了不存在的中文文件名」这类腐烂**静默漏掉**。
    # 漏报比误报更糟：误报会有人去查，漏报没人知道。
    zh_manual = os.path.join(dl, "BAD_ZH.md")
    with open(zh_manual, "w", encoding="utf-8") as fh:
        fh.write("见 docs/不存在的中文手册.md\n")
    code, out = run(["scripts/doc-links.py", zh_manual])
    chk("doc-links 能发现中文文件名的缺失", code != 0 and "MISS docs/不存在的中文手册.md" in out, out.strip()[-200:])

    # ---- 6) text-only 守卫：把七处历史事故逐条改回去，必须逐条红 ----
    #
    # 这条守卫的判据不是"脚本能跑"，是"把真实事故改回去它认不认得"。
    # 七处都来自 2026-11-04 一天内的实际修复（见 docs/silent-success-audit.md §5.6）。
    # 全部在隔离副本里改，仓库里的 .go 文件一个都不碰。
    #
    # 副本放在系统临时目录，**不放在 tmp/ 下**：tmp/ 在本仓 Go module 里面，
    # 一份拷过去的树会变成 dshgo/tmp/... 包，`go build ./...` / `go test ./...`
    # 会连它一起编译（实测：一次全仓测试把副本里的包也跑了，还拖慢一倍）。
    to_root = tempfile.mkdtemp(prefix="dshgo-text-only-")
    to_tree = os.path.join(to_root, "tree")
    for d in ("cmd", "internal", "modules", "codecs"):
        shutil.copytree(d, os.path.join(to_tree, d))
    allow = os.path.abspath("scripts/text-only-allow.txt")

    def run_guard(root):
        return run(["scripts/text-only-guard.py", "--root", root, "--allow", allow])

    code, out = run_guard(to_tree)
    chk("text-only 守卫：未改动的副本是绿的（造红不是副本本身坏了）",
        code == 0 and "PASS text-only" in out, out.strip()[-200:])

    # (名字, 文件, 旧写法正则, 替换) —— 每一条都对应一次真实事故。
    text_only_reverts = [
        ("session.load 只凭 text 判空",
         "cmd/dshd/transport_source.go",
         r'if !assistantHasPayload\(text, think, countToolCalls\(blocks\)\) \{',
         'if text == "" {'),
        ("history_search 只拼 text 块",
         "cmd/dshd/history.go",
         r'(?s)\tparts := make\(\[\]string, 0, len\(blocks\)\)\n\tfor _, b := range blocks \{\n.*?\n\t\}\n\treturn strings\.Join\(parts, "\\n"\)',
         '\tparts := make([]string, 0, len(blocks))\n'
         '\tfor _, b := range blocks {\n'
         '\t\tif b.Text != "" {\n'
         '\t\t\tparts = append(parts, b.Text)\n'
         '\t\t}\n'
         '\t}\n'
         '\treturn strings.Join(parts, "\\n")'),
        ("子代理转写 m.Text 为空就丢",
         "cmd/dshd/subagent_inherit.go",
         r'if !assistantHasPayload\(n\.Message\.Content, reasoning, len\(n\.Message\.ToolCalls\)\) \{',
         'if n.Message.Content == "" {'),
        ("session.fork 不搬思考块",
         "internal/session/fork.go",
         r'(?s)\t\tblocks := make\(\[\]map\[string\]any, 0, len\(reasoning\)\+1\+len\(m\.ToolCalls\)\)\n'
         r'\t\tfor _, text := range reasoning \{\n'
         r'\t\t\tblocks = append\(blocks, ReasoningBlock\(text\)\)\n'
         r'\t\t\}\n',
         '\t\tblocks := make([]map[string]any, 0, 1+len(m.ToolCalls))\n'),
        ("ACP session/load 重放不看思考块",
         "cmd/dshd/acp.go",
         r'(?s)\treasoning := session\.ReasoningBlocksBySeq\(back\)\n\tfor _, node := range session\.Surface\(back\) \{\n\t\tm := node\.Message\n.*?\n\t\t\}\n\t\tif m\.Content != "" \{',
         '\tfor _, node := range session.Surface(back) {\n\t\tm := node.Message\n\t\tif m.Content != "" {'),
        ("Meter.Estimate 不算思考",
         "internal/session/tokens.go",
         r'(?s)\t\tfor _, text := range m\.Reasoning \{\n\t\t\ttotal \+= estimateString\(text\)\n\t\t\}\n',
         ""),
        ("serve 推帧用 resp.Content != \"\" 决定发不发",
         "cmd/dshd/serve_turn.go",
         r'if resp != nil && assistantHasPayload\(resp\.Content, resp\.Reasoning, len\(resp\.ToolCalls\)\) \{',
         'if resp != nil && resp.Content != "" {'),
    ]
    for name, rel, pattern, repl in text_only_reverts:
        target = os.path.join(to_tree, rel)
        orig = open(target, encoding="utf-8").read()
        new, n = re.subn(pattern, lambda _m: repl, orig, count=1)
        if n != 1 or new == orig:
            chk("text-only 守卫：%s → 造红片段能对上" % name, False,
                "旧写法片段没匹配上（%s），这条自测本身失效了" % rel)
            continue
        open(target, "w", encoding="utf-8", newline="").write(new)
        code, out = run_guard(to_tree)
        open(target, "w", encoding="utf-8", newline="").write(orig)
        chk("text-only 守卫：%s → 必须红" % name,
            code != 0 and "FAIL" in out, out.strip()[-220:])

    # 白名单自己也会烂：条目对不上任何发现时必须红。
    # "问题已经修掉了"也算对不上——那就该有人把这一行删掉，而不是让它继续当橡皮图章。
    stale_allow = os.path.join(TMP, "text-only-allow-stale.txt")
    with open(stale_allow, "w", encoding="utf-8", newline="") as fh:
        fh.write(open(allow, encoding="utf-8").read())
        fh.write("\ncmd/dshd/history.go:9999 | 自测用的坏条目：那个文件里没有这一行\n")
    code, out = run(["scripts/text-only-guard.py", "--allow", stale_allow])
    chk("text-only 守卫：白名单条目对不上任何发现 → 必须红（白名单不许烂着）",
        code != 0 and "白名单条目没命中任何发现" in out, out.strip()[-200:])

    # 第八次不是"改回旧的"，是"新写一个"：新文件里的 text-only 投影也必须红。
    brand_new = os.path.join(to_tree, "cmd", "dshd", "zz_text_only_selftest_probe.go")
    open(brand_new, "w", encoding="utf-8", newline="").write(
        "package main\n\n"
        "// 自测用的假投影：新写的代码，白名单里没有它。\n"
        "func selftestProbeJoin(blocks []block) string {\n"
        "\tout := \"\"\n"
        "\tfor _, b := range blocks {\n"
        "\t\tif b.Type == \"text\" {\n"
        "\t\t\tout += b.Text\n"
        "\t\t}\n"
        "\t}\n"
        "\treturn out\n"
        "}\n")
    code, out = run_guard(to_tree)
    os.remove(brand_new)
    chk("text-only 守卫：新写的 text-only 投影（不是改回旧代码）也红",
        code != 0 and "FAIL" in out, out.strip()[-220:])

    # map 形式的同形：`b["type"] == "text"`。
    #
    # 为什么单独一条：守卫一开始只认 `b.Type`（结构体字段），我在隔离副本里塞一个
    # map 形式的投影，它**没红**——而本仓的 fork/assistantBlocks 真是拿
    # `[]map[string]any` 在操作，所以这盲区是活的，不是理论问题。
    map_probe = os.path.join(to_tree, "cmd", "dshd", "zz_text_only_selftest_map_probe.go")
    map_src = "\n".join([
        "package main",
        "",
        "// 自测用的假投影（map 形式）。",
        "func selftestProbeMapJoin(blocks []map[string]any) string {",
        "\tout := \"\"",
        "\tfor _, b := range blocks {",
        "\t\tif b[\"type\"] == \"text\" {",
        "\t\t\tout += b[\"text\"].(string)",
        "\t\t}",
        "\t}",
        "\treturn out",
        "}",
        "",
    ])
    open(map_probe, "w", encoding="utf-8", newline="").write(map_src)
    code, out = run_guard(to_tree)
    os.remove(map_probe)
    chk("text-only 守卫：map 形式的 b[\"type\"]==\"text\" 也红（结构体形式之外的另一半）",
        code != 0 and "FAIL" in out, out.strip()[-220:])

    shutil.rmtree(to_root, ignore_errors=True)
    shutil.rmtree(TMP, ignore_errors=True)
    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
