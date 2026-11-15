#!/usr/bin/env bash
# pack-release.sh —— 把 dist/ 里的产物整理成**能直接上传 GitHub Release** 的名字。
#
# # 为什么需要这一步（2026-11-14 发 0.2.2-go.4 时踩的）
#
# `scripts/build.sh` 的产物名字是**给人看的产品名**：
#
#     dist/DSH TUI GO.exe                 (windows/amd64)
#     dist/linux-amd64/DSH TUI GO
#     dist/darwin-arm64/DSH TUI GO
#
# 本地这样没问题——它们在**不同的目录**里。但 GitHub Release 的附件是**平铺的命名空间**，
# 上传时 `DSH TUI GO.exe` 与 `DSH TUI GO` 会**归一成同一个 name**，第二个必然：
#
#     HTTP 422: Validation Failed ... ReleaseAsset.name already exists
#
# 这不是"偶尔失败"，是**每次发版都会撞**——只是第一次发版才暴露。
#
# # 它做什么
#
# 把 6 个产物复制到 `dist/release/`，文件名带**平台后缀**，并对这批新名字重算
# `SHA256SUMS`。发布时直接：
#
#     gh release create <tag> dist/release/*
#
# 产物本体**一个字节都不改**（不 strip、不重编），只改文件名——所以校验和是
# 对**实际发布的那份字节**算的，不是对复制前的。
#
# # 用法
#
#     bash scripts/pack-release.sh          # 读 dist/，产出 dist/release/
#     bash scripts/pack-release.sh --check  # 只核对，不写
set -euo pipefail

VER="${VER:-$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' cmd/dshd/main.go | head -1)}"
[ -n "$VER" ] || { echo "拿不到版本号（cmd/dshd/main.go 的 version）" >&2; exit 1; }

OUT=dist/release
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

# 源 -> 发布名。**每个发布名都必须唯一**，否则又回到 422。
declare -a PAIRS=(
  "dist/windows-amd64/dsh-${VER}.exe|dsh-${VER}-windows-amd64.exe"
  "dist/linux-amd64/dsh-${VER}|dsh-${VER}-linux-amd64"
  "dist/darwin-arm64/dsh-${VER}|dsh-${VER}-darwin-arm64"
  "dist/windows-amd64/DSH TUI GO.exe|DSH-TUI-GO-${VER}-windows-amd64.exe"
  "dist/linux-amd64/DSH TUI GO|DSH-TUI-GO-${VER}-linux-amd64"
  "dist/darwin-arm64/DSH TUI GO|DSH-TUI-GO-${VER}-darwin-arm64"
)

# 先自证：发布名不许重复。这条断言就是防止 422 再发生。
names=()
for p in "${PAIRS[@]}"; do names+=("${p##*|}"); done
dupes="$(printf '%s\n' "${names[@]}" | sort | uniq -d)"
[ -z "$dupes" ] || { echo "发布名重复（上传必 422）: $dupes" >&2; exit 1; }

missing=""
for p in "${PAIRS[@]}"; do
  src="${p%%|*}"
  [ -f "$src" ] || missing="$missing $src"
done
if [ -n "$missing" ]; then
  echo "产物不全，先跑 scripts/build.sh:$missing" >&2
  exit 1
fi

if [ "$CHECK" -eq 1 ]; then
  echo "命名自检 PASS：6 个发布名互不重复，源产物齐全（VER=$VER）"
  exit 0
fi

rm -rf "$OUT" && mkdir -p "$OUT"
for p in "${PAIRS[@]}"; do
  src="${p%%|*}"; dst="${p##*|}"
  cp "$src" "$OUT/$dst"
done
( cd "$OUT" && sha256sum * > SHA256SUMS )
echo "已整理到 $OUT/（6 件 + SHA256SUMS）"
( cd "$OUT" && ls -la | awk 'NR>3 {printf "  %10s  %s\n", $5, $9}' )
