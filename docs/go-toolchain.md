# Go 诊断工具链与验收检查单

> 目的：把“改完等 PTY”改成“先用 Go 工具定位，再用确定性 replay，最后才上 PTY”。
> 适用：DSH-GO `DSH-GO/` 与 TUI `src/ink/**` 的 Go/TS 混合排障。

## 1. 工具与用途

| 工具 | 用途 | 命令 |
|---|---|---|
| `go` | 编译/测试/vet | `go test -count=1 ./pkg` |
| `gopls` | 类型/引用/诊断，不跑测试 | `gopls check <file>` |
| `staticcheck` | 静态缺陷 | `staticcheck ./...` |
| `dlv` | 断点看账本状态 | `dlv test ./pkg -- -test.run TestX` |
| `gotestsum` | 结构化红点 | `gotestsum --format testname ./...` |
| `goimports` | 导入整理 | `goimports -w <file>` |
| `gofumpt` | 更严格格式 | `gofumpt -w <file>` |

## 2. 先做确定性复现，再上 PTY

顺序固定：

1. `gopls check` / `go test -run` 确认编译与目标行为
2. 纯函数/headless replay 复现红点（xterm/无 PTY）
3. `go test -count=3` 判 flaky
4. 只有以上都稳定，才消耗真实 PTY

**禁止**：一上来跑 full PTY；把模型时序抖动当产品 bug；用 full 绿替代目标包证据。

## 3. 测试预设

```bash
# 目标包
go test -count=1 ./internal/agent ./modules/tools ./modules/tool/workflow

# 全仓
go test -count=1 ./...

# flake 判定
go test -count=3 -run 'TestX' ./pkg

# race（有 C 工具链时）
CGO_ENABLED=1 go test -race ./pkg

# 结构化输出
gotestsum --format testname ./...
```

## 4. 红点分类

| 类型 | 判据 | 处理 |
|---|---|---|
| 产品 | 同一输入稳定红，目标包可复现 | 产品 owner 修 |
| 契约 | 断言与 0.2.2/官方行为冲突 | Lead 批契约迁移 |
| 驱动 | raw/sequence 与产品不一致 | 驱动 owner |
| 模型 flake | 同代码红点漂移、reasoning 不稳定 | 先改判据/等待，不派产品 |
| 环境 | ACL、端口、home、node_modules | 环境 owner，不碰产品 |

## 5. 证据格式

```text
task / owner / model:
base hash:
changed files:
patch sha256:
pre-fix  command + exit + FAIL:
post-fix command + exit + OK=n FAIL=m:
src/lib hash before/after:
evidence dir:
unverified:
```

## 6. 当前 GO.1 用法

- W170 overlay：先 `gopls check` / 目标测试，再做 xterm replay，最后 W170 80/120 PTY
- W167 120：先 Host `session.load` 日志/单测，不先改 TUI
- W167 80：先判 reasoning flake，不先改渲染

## 7. 安装命令（缺失时）

```bash
go install golang.org/x/tools/gopls@latest
go install golang.org/x/tools/cmd/goimports@latest
go install mvdan.cc/gofumpt@latest
go install github.com/go-delve/delve/cmd/dlv@latest
go install gotest.tools/gotestsum@latest
```

安装后 `command -v` + 版本输出；不要改 `go.mod`，不要改全局代理。
