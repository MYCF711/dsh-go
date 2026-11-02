#!/usr/bin/env bash
# 打发布件：三个目标平台，-s -w + 注入 commit/buildTime，产出 dist/ 与 SHA256SUMS。
#
#   bash scripts/build.sh
#
# 不进 npm（用户要求），只出静态二进制。
set -euo pipefail

export PATH="/c/Program Files/Go/bin:$PATH"
cd "$(dirname "$0")/.."

VER=$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' cmd/dshd/main.go | head -1)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-s -w -X main.commit=${COMMIT} -X main.buildTime=${DATE}"

if [ -z "${VER}" ]; then
  echo "读不到版本号（cmd/dshd/main.go 的 version 变量）" >&2
  exit 1
fi

mkdir -p dist
: > dist/SHA256SUMS

echo "dshd ${VER} @ ${COMMIT} (${DATE})"

for target in windows/amd64 linux/amd64 darwin/arm64; do
  os=${target%%/*}
  arch=${target##*/}
  ext=""
  [ "${os}" = "windows" ] && ext=".exe"
  out="dist/dshd-${VER}-${os}-${arch}${ext}"

  GOOS=${os} GOARCH=${arch} CGO_ENABLED=0 \
    go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" ./cmd/dshd

  size=$(du -h "${out}" | cut -f1)
  printf '  %-52s %s\n' "$(basename "${out}")" "${size}"
  ( cd dist && sha256sum "$(basename "${out}")" >> SHA256SUMS )
done

echo
echo "产物在 dist/，校验和 dist/SHA256SUMS"
