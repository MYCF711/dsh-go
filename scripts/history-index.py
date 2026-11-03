#!/usr/bin/env python3
"""history-index：给 docs/history.md 生成按主题的目录。

为什么不是按时间：56 KB 的流水账按时间排是查不到东西的。
为什么不用真 NLP：这个话题分类靠关键词就够，可复现、可审查、改一次马上能看到覆盖率变化。

用法：
    python scripts/history-index.py            # 重新生成 docs/history-index.md
    python scripts/history-index.py --coverage # 只报覆盖率
"""
import sys

TOPICS = {
    "会话格式 / v4 编解码": ["v4", "session/", "session.", "header", "zstd", "帧", "surfaceOp", "断连", "取消", "Shutdown"],
    "压缩与上下文预算": ["压缩", "compact", "token", "上下文预算", "工具表", "usage_update", "context-window"],
    "并发 / 内存 / 长跑": ["goroutine", "内存", "soak", "并发", "泄漏", "Sys", "句柄"],
    "工具层（schema / Hint / 审批 / 对拍）": ["Caps", "Hint", "审批", "schema", "工具", "Tool", "对拍", "read", "edit", "bash", "glob", "grep"],
    "界面线 / transport": ["transport", "界面", "WS", "method", "U0", "快照", "HTTP"],
    "门禁 / 变异 / 自我验证": ["门禁", "变异", "check.sh", "守卫", "自测", "staticcheck", "回归"],
    "派活给 dshd": ["dshd", "派活", "任务书"],
    "探针与测量方法": ["探针", "实测", "量出来", "量了", "测量"],
    "ACP / 客户端": ["acp", "ACP", "Martty", "客户端"],
    "事故与教训": ["教训", "差点", "误报", "沉默", "骗", "假绿", "静默"],
}


def classify(lines):
    idx = {k: [] for k in TOPICS}
    other = []
    for n, line in enumerate(lines, 1):
        if not line.startswith("- "):
            continue
        best, hits = None, 0
        for topic, words in TOPICS.items():
            h = sum(1 for w in words if w in line)
            if h > hits:
                best, hits = topic, h
        (idx[best] if best else other).append(n)
    return idx, other


def main():
    lines = open("docs/history.md", encoding="utf-8").read().split("\n")
    idx, other = classify(lines)
    total = sum(len(v) for v in idx.values()) + len(other)
    covered = total - len(other)
    if "--coverage" in sys.argv:
        print(f"覆盖 {covered}/{total}（{100*covered//total}%），未归类 {len(other)}")
        return 0

    out = [
        "# 历史索引（按主题，不按时间）", "",
        "> `docs/history.md` 的目录。想知道「某件事最后怎么定的」，从这里进。", "",
        f"> 覆盖：**{covered}/{total} 条**（{100*covered//total}%）归进了主题，其余 {len(other)} 条在最后一行。",
        "> 归类规则：每条只进关键词命中最多**的一个**主题，所以各主题条数不重叠。",
        "> 这份文件由 `scripts/history-index.py` 生成，不要手改。", "",
        "| 主题 | 条数 | 起始行（前 10 个） |", "|---|---:|---|",
    ]
    for topic, v in sorted(idx.items(), key=lambda kv: -len(kv[1])):
        if v:
            out.append(f"| {topic} | {len(v)} | {', '.join(str(x) for x in v[:10])} |")
    out.append(f"| （未归类） | {len(other)} | {', '.join(str(x) for x in other[:10])} |")
    out += ["", "## 怎么用", "",
            "1. 先看主题 → 拿到行号 → 在 `docs/history.md` 跳到那一行附近",
            "2. 同一件事可能横跨两个主题（先出方案、后来改了主意），**两个都要看**",
            "3. 加新主题就改本脚本的 `TOPICS`，再跑一次；覆盖率会跟着动", ""]
    open("docs/history-index.md", "w", encoding="utf-8").write("\n".join(out))
    print(f"覆盖 {covered}/{total}（{100*covered//total}%），未归类 {len(other)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
