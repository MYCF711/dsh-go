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
    shutil.copy("tmp/DSH GO.exe", stale)
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

    # ---- 7) 单点实现守卫：把三条历史事故逐条改回"双实现"，必须逐条红 ----
    #
    # 这条守卫的判据是**结构**（同一判据字面量/同一种组装形状出现在登记位置之外），
    # 所以造红不能靠"让它行为不同"——只能真的把第二份实现放回去。
    # 三条事故的旧代码逐字取自 git（提交号见每条注释），不是想象的形状。
    ss_root = tempfile.mkdtemp(prefix="dshgo-single-source-")
    ss_tree = os.path.join(ss_root, "tree")
    for d in ("cmd", "internal", "modules", "codecs"):
        shutil.copytree(d, os.path.join(ss_tree, d))
    ss_rules = os.path.abspath("scripts/single-source.txt")

    def run_ss(root, rules=None):
        return run(["scripts/single-source.py", "--root", root, "--rules", rules or ss_rules])

    code, out = run_ss(ss_tree)
    chk("单点实现守卫：未改动的副本是绿的（造红不是副本本身坏了）",
        code == 0 and "PASS 单点实现守卫" in out, out.strip()[-220:])
    chk("单点实现守卫：登记的第二处（fork 铺块）以 WARN 出现而不是被吞掉",
        "WARN 铺块顺序" in out and "internal/session/fork.go:copyNode" in out, out.strip()[-220:])

    def ss_revert(name, rel, pattern, repl, expect_shape):
        """在副本上把某一处改回"第二份实现"，跑守卫，恢复。"""
        target = os.path.join(ss_tree, rel)
        orig = open(target, encoding="utf-8").read()
        new, n = re.subn(pattern, lambda _m: repl, orig, count=1)
        if n != 1 or new == orig:
            chk("单点实现守卫：%s → 造红片段能对上" % name, False,
                "旧写法片段没匹配上（%s），这条自测本身失效了" % rel)
            return
        open(target, "w", encoding="utf-8", newline="").write(new)
        code, out = run_ss(ss_tree)
        open(target, "w", encoding="utf-8", newline="").write(orig)
        chk("单点实现守卫：%s → 必须红（%s）" % (name, expect_shape),
            code != 0 and "FAIL" in out and expect_shape in out, out.strip()[-260:])

    # 事故 2（941460c / 3c9e457）：ACP 写盘自己铺一份块，只铺 text + 工具调用。
    ss_revert(
        "ACP 写盘重新自己铺块（941460c 之前）",
        "cmd/dshd/acp.go",
        r"\t\t\tblocks := assistantBlocksFrom\(a\.Content, a\.Reasoning, a\.ToolCalls\)\n",
        "\t\t\tblocks := make([]map[string]any, 0, 1+len(a.ToolCalls))\n"
        "\t\t\tif a.Content != \"\" {\n"
        "\t\t\t\tblocks = append(blocks, session.TextBlock(a.Content))\n"
        "\t\t\t}\n"
        "\t\t\tfor _, tc := range a.ToolCalls {\n"
        "\t\t\t\tblocks = append(blocks, session.ToolCallBlock(tc.ID, tc.Function.Name, tc.Function.Arguments))\n"
        "\t\t\t}\n",
        "assistant-block-layout")

    # 事故 3（b4809d1）：serve 自己解一遍事件、自己写一遍思考块判据。
    ss_revert(
        "serve 的 reasoningBySeq 重新自己解析（b4809d1 之前）",
        "cmd/dshd/serve_turn.go",
        r"\tall := session\.ReasoningBlocksBySeq\(log\)\n",
        "\tout2 := make(map[int64]string, len(log.Events))\n"
        "\tfor _, ev := range log.Events {\n"
        "\t\tif ev.Type != \"assistant/message\" {\n"
        "\t\t\tcontinue\n"
        "\t\t}\n"
        "\t\tvar d struct {\n"
        "\t\t\tMessage struct {\n"
        "\t\t\t\tContent []block `json:\"content\"`\n"
        "\t\t\t} `json:\"message\"`\n"
        "\t\t\tContent []block `json:\"content\"`\n"
        "\t\t}\n"
        "\t\tif err := json.Unmarshal(ev.Data, &d); err != nil {\n"
        "\t\t\tcontinue\n"
        "\t\t}\n"
        "\t\tblocks := d.Message.Content\n"
        "\t\tif len(blocks) == 0 {\n"
        "\t\t\tblocks = d.Content\n"
        "\t\t}\n"
        "\t\tfor _, b := range blocks {\n"
        "\t\t\tif b.Type == \"reasoning\" && strings.TrimSpace(b.Text) != \"\" {\n"
        "\t\t\t\tout2[ev.Seq] = b.Text\n"
        "\t\t\t\tbreak\n"
        "\t\t\t}\n"
        "\t\t}\n"
        "\t}\n"
        "\t_ = out2\n"
        "\tall := session.ReasoningBlocksBySeq(log)\n",
        "reasoning-type-discriminator")

    # 事故 1（409879e / a882d5b）：tightenSchema 重新按字面量删 description。
    ss_revert(
        "tightenSchema 重新 k == \"description\"（a882d5b 之前）",
        "cmd/dshd/tool_merged.go",
        r"if schemarule\.IsAnnotationKey\(k, false\) \{",
        'if k == "description" {',
        "schema-annotation-key")

    # 事故 1 的另一半：CompactSchema 重新自己压一遍。
    ss_revert(
        "agent.CompactSchema 重新自己删键（409879e 之前）",
        "internal/agent/loop.go",
        r"\treturn schema\.Compact\(raw\)\n",
        "\tvar v any\n"
        "\tif err := json.Unmarshal(raw, &v); err != nil {\n"
        "\t\treturn raw\n"
        "\t}\n"
        "\tif m, ok := v.(map[string]any); ok {\n"
        "\t\tfor k := range m {\n"
        "\t\t\tif k == \"description\" {\n"
        "\t\t\t\tdelete(m, k)\n"
        "\t\t\t}\n"
        "\t\t}\n"
        "\t}\n"
        "\tb, _ := json.Marshal(v)\n"
        "\treturn b\n",
        "schema-annotation-key")

    # 本轮（§5.7「第五刀」）：取块规则「message.content 优先 / 顶层 content 兜底」。
    #
    # 这一条为什么必须靠结构判据：把 eventText 改回自己解一遍，**行为完全一样**，
    # 它自己的用例（TestTextOfReadsBothContentShapes）改前改后都是绿的——
    # 要等四处漂开才发作，而那时看到的是"这个会话加载出来一条消息都没有"。
    ss_revert(
        "eventText 重新自己解一遍 message.content / content（本轮之前）",
        "cmd/dshd/history.go",
        r"\tblocks, ok := session\.ContentBlocks\(ev\.Data, session\.PreferNested\)\n"
        r"\tif !ok \{\n"
        r"\t\treturn \"\"\n"
        r"\t\}\n",
        "\tvar d struct {\n"
        "\t\tMessage struct {\n"
        "\t\t\tContent []block `json:\"content\"`\n"
        "\t\t} `json:\"message\"`\n"
        "\t\tContent []block `json:\"content\"`\n"
        "\t}\n"
        "\tif err := json.Unmarshal(ev.Data, &d); err != nil {\n"
        "\t\treturn \"\"\n"
        "\t}\n"
        "\tblocks := d.Message.Content\n"
        "\tif len(blocks) == 0 {\n"
        "\t\tblocks = d.Content\n"
        "\t}\n",
        "content-block-locus")

    # 第九次不是"改回旧的"，是"新写一个"：新文件里的第二份判据也必须红。
    new_probe = os.path.join(ss_tree, "cmd", "dshd", "zz_single_source_selftest_probe.go")
    open(new_probe, "w", encoding="utf-8", newline="").write(
        "package main\n\n"
        "// 自测用的第二份实现：新写的代码，登记表里没有它。\n"
        "func selftestProbeIsThinking(kind string) bool {\n"
        "\treturn kind == \"reasoning\"\n"
        "}\n")
    code, out = run_ss(ss_tree)
    os.remove(new_probe)
    chk("单点实现守卫：新写的第二份判据（不是改回旧代码）也红",
        code != 0 and "reasoning-type-discriminator" in out, out.strip()[-260:])

    # 取块规则的另一半：**新写**一份（不是改回旧代码）也必须红——否则这条规则只管历史，
    # 拦不住下一个"我自己解一遍算了"的人。
    new_locus = os.path.join(ss_tree, "cmd", "dshd", "zz_single_source_selftest_locus.go")
    open(new_locus, "w", encoding="utf-8", newline="").write(
        "package main\n\n"
        "// 自测用的第二份取块规则：新写的代码，登记表里没有它。\n"
        "func selftestProbeBlocks(raw []byte) []block {\n"
        "\tvar d struct {\n"
        "\t\tMessage struct {\n"
        "\t\t\tContent []block `json:\"content\"`\n"
        "\t\t} `json:\"message\"`\n"
        "\t\tContent []block `json:\"content\"`\n"
        "\t}\n"
        "\t_ = json.Unmarshal(raw, &d)\n"
        "\tif len(d.Message.Content) > 0 {\n"
        "\t\treturn d.Message.Content\n"
        "\t}\n"
        "\treturn d.Content\n"
        "}\n")
    code, out = run_ss(ss_tree)
    os.remove(new_locus)
    chk("单点实现守卫：新写的第二份取块规则（不是改回旧代码）也红",
        code != 0 and "content-block-locus" in out, out.strip()[-260:])

    # 反面：**正常写一个新投影**不许红——它就是该绿的形状。
    #
    # 这一条是这条守卫"会不会太吵"的实测判据：新投影要看思考块，正确的写法是
    # 在 case 里调唯一实现；这种写法必须绿，否则守卫第一天就会逼人去加白名单。
    ok_probe = os.path.join(ss_tree, "cmd", "dshd", "zz_single_source_selftest_ok.go")
    open(ok_probe, "w", encoding="utf-8", newline="").write(
        "package main\n\n"
        "// 自测用的新投影：分块走 switch，判据调唯一实现。\n"
        "func selftestProbeThinkingText(b block) string {\n"
        "\tswitch b.Type {\n"
        "\tcase \"reasoning\":\n"
        "\t\tif text, ok := session.ReasoningText(b.Type, b.Text); ok {\n"
        "\t\t\treturn text\n"
        "\t\t}\n"
        "\t}\n"
        "\treturn \"\"\n"
        "}\n\n"
        "// 往 schema 里写说明文字不是「判断注解」，不该算第二份。\n"
        "func selftestProbeSetDoc(m map[string]any, doc string) {\n"
        "\tm[\"description\"] = doc\n"
        "}\n")
    code, out = run_ss(ss_tree)
    os.remove(ok_probe)
    chk("单点实现守卫：委派给唯一实现的新投影是绿的（不会逼人加白名单）",
        code == 0 and "PASS 单点实现守卫" in out, out.strip()[-260:])

    # 登记表自己兜底：指的位置不在了 → 红（否则所有同形状代码都会被算成"在登记位置之外"）。
    tmp_rules = os.path.join(TMP, "single-source-owner-gone.txt")
    with open(tmp_rules, "w", encoding="utf-8", newline="") as fh:
        fh.write("[rule]\n思考块识别 | internal/session/gone.go:ReasoningText | "
                 "reasoning-type-discriminator | 自测：唯一实现被搬走了\n")
    code, out = run_ss(ss_tree, tmp_rules)
    chk("单点实现守卫：唯一实现的文件不存在 → 必须红",
        code != 0 and "唯一实现的文件不存在" in out, out.strip()[-200:])

    tmp_rules2 = os.path.join(TMP, "single-source-symbol-gone.txt")
    with open(tmp_rules2, "w", encoding="utf-8", newline="") as fh:
        fh.write("[rule]\n思考块识别 | internal/session/reasoning.go:NoSuchFunc | "
                 "reasoning-type-discriminator | 自测：唯一实现被改名了\n")
    code, out = run_ss(ss_tree, tmp_rules2)
    chk("单点实现守卫：唯一实现的符号被改名 → 必须红",
        code != 0 and "找不到符号" in out, out.strip()[-200:])

    tmp_rules3 = os.path.join(TMP, "single-source-stale-allow.txt")
    with open(tmp_rules3, "w", encoding="utf-8", newline="") as fh:
        fh.write("[rule]\n思考块识别 | internal/session/reasoning.go:ReasoningText | "
                 "reasoning-type-discriminator | 自测：白名单腐烂\n"
                 "[allow]\n"
                 "cmd/dshd/history.go:9999 | reasoning-type-discriminator | 自测：那个文件里没有这一行\n")
    code, out = run_ss(ss_tree, tmp_rules3)
    chk("单点实现守卫：白名单条目对不上任何发现 → 必须红（白名单不许烂着）",
        code != 0 and "白名单条目没命中任何发现" in out, out.strip()[-200:])

    tmp_rules4 = os.path.join(TMP, "single-source-bad-shape.txt")
    with open(tmp_rules4, "w", encoding="utf-8", newline="") as fh:
        fh.write("[rule]\n猜的规则 | internal/session/reasoning.go | no-such-shape | 自测：形状ID不存在\n")
    code, out = run_ss(ss_tree, tmp_rules4)
    chk("单点实现守卫：登记表写了不认识的形状ID → 必须红（不许静默忽略一条规则）",
        code != 0 and "形状 ID 不认识" in out, out.strip()[-200:])

    empty_root = os.path.join(TMP, "single-source-empty")
    os.makedirs(empty_root, exist_ok=True)
    code, out = run_ss(empty_root)
    chk("单点实现守卫：空目录不报绿（没扫到东西 ≠ 通过）",
        code != 0 and "0 个 Go 文件" in out, out.strip()[-200:])

    shutil.rmtree(ss_root, ignore_errors=True)

    shutil.rmtree(TMP, ignore_errors=True)
    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
