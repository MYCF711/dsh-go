#!/usr/bin/env bash
# scripts/fetch-refs.sh —— 拉第三方参考源码到 refs/（不进 git）。
# 参考实现：naamfung/dsc（Apache-2.0），只读参考；借鉴处须注明 refs/dsc/<file>:<line>。
set -euo pipefail
cd "$(dirname "$0")/.."

VER="${1:-v3.0.0}"
DEST="refs/dsc"

if [ -d "$DEST" ] && [ -f "$DEST/VERSION" ]; then
  echo "refs/dsc 已存在（$(cat "$DEST/VERSION")），跳过；要重拉先 rm -rf $DEST"
  exit 0
fi

mkdir -p refs
TMP="$(mktemp -d)"
echo "下载 dsc $VER ..."
curl -fsSL "https://codeload.github.com/naamfung/dsc/tar.gz/refs/tags/$VER" -o "$TMP/dsc.tgz"
tar -xzf "$TMP/dsc.tgz" -C "$TMP" --strip-components=1 --one-top-level=src 2>/dev/null \
  || { mkdir -p "$TMP/src" && tar -xzf "$TMP/dsc.tgz" -C "$TMP/src" --strip-components=1; }
rm -rf "$DEST"
mv "$TMP/src" "$DEST"
rm -rf "$TMP"
echo "完成：$DEST（$(find "$DEST" -type f | wc -l) 文件，$(cat "$DEST/VERSION" 2>/dev/null || echo '?')）"
