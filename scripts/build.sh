#!/usr/bin/env bash
# 打发布件：两个程序 × 三个目标平台，-s -w + 注入版本/commit/buildTime，产出 dist/ 与 SHA256SUMS。
#
#   bash scripts/build.sh
#
# 两个程序，**产品名就是产物文件名**（用户口径：exe 面向用户，名字必须是产品名，
# 像官方的 `DeepSeek Harness.exe` 那样全名、带空格、分大小写）：
#
#   - `DSH GO`      = 宿主（Transport 服务端），包 ./cmd/dshd
#   - `DSH TUI GO`  = 终端界面，包 ./cmd/dshtui
#
# 目录形状：**产品名在文件名里，平台放在目录里**。三个平台的交叉编译产物同名不同目录，
# 也就不存在"给文件名塞版本号/平台串"这种把产品名挤掉的做法。
#
#   dist/dsh-<版本号>.exe          本机 windows/amd64 的**宿主**（用户要求宿主产物带版本号；
#                                  `--version` 首词仍是产品名 DSH GO）：真机、任务管理器、脚本用这一个
#   dist/DSH TUI GO.exe            本机 windows/amd64 的 TUI（名字就是产品名）
#   dist/<os>-<arch>/dsh-<版本号>[.exe]     三个平台的宿主（windows 加 .exe，linux/darwin 不加后缀）
#   dist/<os>-<arch>/DSH TUI GO[.exe]       三个平台的 TUI
#   dist/SHA256SUMS                路径相对 dist/，在 dist/ 里跑 `sha256sum -c SHA256SUMS`
#
# **名字里有空格**：本脚本每一处用到产物路径都整段加引号（`"${out}"`、`"dist/${name}.exe"`）。
# 少一个引号就会被拆成两个词，而报错信息看起来会像"文件不存在"，不像"引号漏了"。
#
# 版本号只有一个出处：宿主的 `cmd/dshd/main.go`。这里把它同时注入两边
# （`-X main.version`），所以 dist 里的两个程序永远是同一个版本号。
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

# Windows 版本资源（.syso）在每次打包前重新生成一次。
#
# 为什么必须在这里重生成：`.syso` 是**编译期嵌进去的**，而版本号是 `-ldflags -X` 注入的。
# 两者来源不同就会出现"文件属性里是 0.2.1-go.30、程序 --version 却是别的"这种
# 对不上的情况——版本资源本来就只服务"人看"，对不上等于没有。
# 生成器从 main.go / brand.go 读产品名与产物名，这里只显式钉版本号。
for row in "DSH GO|cmd/dshd" "DSH TUI GO|cmd/dshtui"; do
  IFS='|' read -r name dir <<<"${row}"
  go run ./cmd/dshtui/internal/vsinfo -product "${name}" -version "${VER}" -o "${dir}/versioninfo_windows_amd64.syso" >/dev/null || {
    echo "生成 ${name} 的版本资源失败" >&2
    exit 1
  }
done

mkdir -p dist
: > dist/SHA256SUMS

# 名字只在这里写一遍：下面两段循环都从这张表取。
#   <产品名>|<包路径>|<产物基名>
# 宿主产物按用户要求使用 dsh-<版本号>；`--version` 首词仍是产品名 DSH GO。
PRODUCTS=(
  "DSH GO|./cmd/dshd|dsh-${VER}"
  "DSH TUI GO|./cmd/dshtui|DSH TUI GO"
)

TARGETS=("windows/amd64" "linux/amd64" "darwin/arm64")

echo "构建 ${VER} @ ${COMMIT} (${DATE})"
echo

for row in "${PRODUCTS[@]}"; do
  IFS='|' read -r name pkg artifact <<<"${row}"
  echo "${name} ${VER} → ${artifact}"

  for target in "${TARGETS[@]}"; do
    os=${target%%/*}
    arch=${target##*/}
    ext=""
    [ "${os}" = "windows" ] && ext=".exe"
    dir="dist/${os}-${arch}"
    out="${dir}/${artifact}${ext}"

    mkdir -p "${dir}"
    GOOS=${os} GOARCH=${arch} CGO_ENABLED=0 \
      go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" "${pkg}"

    size=$(du -h "${out}" | cut -f1)
    printf '  %-44s %s\n' "${os}-${arch}/${artifact}${ext}" "${size}"
    ( cd dist && sha256sum "${os}-${arch}/${artifact}${ext}" >> SHA256SUMS )
  done

  # 本机（windows/amd64）再留一份**不带平台目录**的 `dist/<产品名>.exe`：
  # 手工跑、任务管理器里看名字、脚本直接调，用的都是这一个文件。
  # 顺手拿它 `--version` 自检：产物名必须等于版本行首词，对不上就是
  # PRODUCTS 表和代码里的 productName 分叉了（那正是"两个名字"的来源）。
  if [ -f "dist/windows-amd64/${artifact}.exe" ]; then
    cp "dist/windows-amd64/${artifact}.exe" "dist/${artifact}.exe"
    got=$("./dist/${artifact}.exe" --version 2>/dev/null | head -1 || true)
    case "${got}" in
      "${name} ${VER}"*)
        printf '  %-44s %s\n' "${artifact}.exe" "${got}"
        ;;
      *)
        echo "  !! ${artifact}.exe --version 输出不对：${got:-（空）}" >&2
        exit 1
        ;;
    esac
    ( cd dist && sha256sum "${artifact}.exe" >> SHA256SUMS )
  fi
  echo
done

echo "产物在 dist/（宿主为 dsh-<版本号>，TUI保留产品名），校验和 dist/SHA256SUMS"
