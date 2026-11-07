#!/bin/sh
# 给「支持 dsh-TUI」这件事打一个**市场标记 tag**，不是版本号。
#
# 版本串只有一种形状（AGENTS §6）：稳定 `<上游>-go.<N>`、测试 `<上游>-beta.<N>`。
# TUI 支持是**兼容性标注**，不参与版本排序，所以 tag 名不带版本语义。
#
# 用法：sh scripts/tag-dsh-tui.sh            # 打/覆盖 dsh-tui 标记
#       sh scripts/tag-dsh-tui.sh --check    # 只检查当前是否已标注一致
set -eu

TAG="dsh-tui"
MARKER_FILE="docs/dsh-tui-compat.md"

if [ "${1:-}" = "--check" ]; then
    if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
        echo "✓ 已打标记 $TAG → $(git rev-parse --short "$TAG")"
        exit 0
    fi
    echo "✗ 还没有 $TAG 标记" >&2
    exit 1
fi

[ -f "$MARKER_FILE" ] || { echo "✗ 缺 $MARKER_FILE：先写清兼容到哪个 dsh-TUI 版本" >&2; exit 1; }
if [ -n "$(git status --porcelain -- "$MARKER_FILE")" ]; then
    echo "✗ $MARKER_FILE 有未提交改动：先提交再打标记" >&2
    exit 1
fi

git tag -f -a "$TAG" -m "支持官方 dsh-TUI（\`@deepseek-harness-tui/dsh-tui\`）

这是**兼容性标记**，不是版本号：
- 本仓版本仍是稳定的 \`<上游>-go.<N>\` / 测试 \`<上游>-beta.<N>\`；
- 标记表示这一提交起，dsh-TUI 可以用 \`--backend dsh-go\` 直连本宿主；
- 兼容范围、已知限制见 $MARKER_FILE。

上游基线 DSH 0.2.2-rc.1。"
echo "✓ 已打标记 $TAG"
git show --stat --oneline "$TAG" | head -3
