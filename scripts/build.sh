#!/usr/bin/env bash
# 打发布件：两个程序 × 三个目标平台，-s -w + 注入版本/commit/buildTime，产出 dist/ 与 SHA256SUMS。
#
#   bash scripts/build.sh
#
# 两个程序，**名字不混**：
#   - `dshd`      = 宿主（Transport 服务端），产物 `dshd-<ver>-<os>-<arch>`
#   - `dshtui`    = 终端界面 DSH TUI GO，产物 `dshtui-<ver>-<os>-<arch>`
#
# 版本号只有一个出处：宿主的 `cmd/dshd/main.go`。这里把它同时注入两边
# （`-X main.version`），所以 dist 里的 dshd 和 dshtui 永远是同一个版本号。
#
# 不进 npm（用户要求），只出静态二进制。
set -euo pipefail

export PATH="/c/Program Files/Go/bin:$PATH"
cd "$(dirname "$0")/.."

VER=$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' cmd/dshd/main.go | head -1)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)

if [ -z "${VER}" ]; then
  echo "读不到版本号（cmd/dshd/main.go 的 version 变量）" >&2
  exit 1
fi

# 两个程序各自的 main 包，同一个注入串（变量名两边都叫 version/commit/buildTime）。
LDFLAGS="-s -w -X main.version=${VER} -X main.commit=${COMMIT} -X main.buildTime=${DATE}"

mkdir -p dist
: > dist/SHA256SUMS

# 名字只在这里写一遍：下面两段循环都从这张表取。
#   <产品名>|<包路径>|<产物前缀>
PRODUCTS=(
  "dshd|./cmd/dshd|dshd"
  "DSH TUI GO|./cmd/dshtui|dshtui"
)

echo "宿主 dshd ${VER} @ ${COMMIT} (${DATE})"
echo

for row in "${PRODUCTS[@]}"; do
  IFS='|' read -r name pkg prefix <<<"${row}"
  # 清单头一行就是「产品名 + 版本」：dshd 那种是产品名，TUI 这种是 DSH TUI GO，
  # 与 `dshtui --version` 的第一行逐字一致。
  echo "${name} ${VER}"

  for target in windows/amd64 linux/amd64 darwin/arm64; do
    os=${target%%/*}
    arch=${target##*/}
    ext=""
    [ "${os}" = "windows" ] && ext=".exe"
    out="dist/${prefix}-${VER}-${os}-${arch}${ext}"

    GOOS=${os} GOARCH=${arch} CGO_ENABLED=0 \
      go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" "${pkg}"

    size=$(du -h "${out}" | cut -f1)
    printf '  %-52s %s\n' "$(basename "${out}")" "${size}"
    ( cd dist && sha256sum "$(basename "${out}")" >> SHA256SUMS )
  done

  # 本机（windows/amd64）再留一份**不带版本号**的 `dist/dshtui.exe`：
  # 手工跑、任务管理器里看名字、脚本直接调，用的都是这一个文件。
  if [ "${prefix}" = "dshtui" ] && [ -f "dist/${prefix}-${VER}-windows-amd64.exe" ]; then
    cp "dist/${prefix}-${VER}-windows-amd64.exe" "dist/${prefix}.exe"
    got=$("./dist/${prefix}.exe" --version 2>/dev/null | head -1 || true)
    case "${got}" in
      "${name} ${VER}"*)
        printf '  %-52s %s\n' "${prefix}.exe" "${got}"
        ;;
      *)
        echo "  !! ${prefix}.exe --version 输出不对：${got:-（空）}" >&2
        exit 1
        ;;
    esac
    ( cd dist && sha256sum "${prefix}.exe" >> SHA256SUMS )
  fi
  echo
done

echo "产物在 dist/，校验和 dist/SHA256SUMS"
