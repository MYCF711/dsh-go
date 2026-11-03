# PLAN §15 的 CI 七条：现在的覆盖情况（2026-11-03）

| # | 规则 | 怎么管的 | 状态 |
|---|---|---|---|
| 1 | kernel 不依赖业务包 | `check.sh` 的 R1 检查 | ✅ |
| 2 | 删 `modules/` 仍能 `go build ./internal/...` | `check.sh` 的 A3 | ✅ |
| 3 | 每个 `Exec` 的 error 有 Hint | `TestEveryToolErrorHasHint`（扫 32 文件 / 297 处） | ✅ |
| 4 | `go vet` + `staticcheck` + `-race` | vet ✅ / **staticcheck 未装** / **-race 缺 C 编译器** | ⚠️ 1/3 |
| 5 | 工具 schema 快照 | `TestToolTableSnapshot` + `-update` | ✅ |
| 6 | 工具表 ≤ 6000 token（分词实测） | 体积守卫 32 KB + 实测 4,149 token | ⚠️ 用字节代理 |
| 7 | 二进制 ≤ 25 MB | **本轮新加**：`check.sh` 直接量产物 | ✅ 13 MB |

## 这轮补的

第 7 条之前**完全没人管**——PLAN 写了，代码里没有对应检查。
现在 `check.sh` 会真的构建一次并量字节数：`PASS 二进制体积 13 MB（上限 25 MB）`。

顺带说明为什么是 13 MB 而不是文档里的 8.6 MB：那条数是在 `-s -w` 下的，
门禁量的是**不定裁剪**的产物——它更大，所以是更严的下界。

## 三条还不能算过关的

**第 4 条的 staticcheck**：没装。它在 Windows 上要 `go install honnef.co/go/tools/cmd/staticcheck`，
而本机装依赖要看网络。**没装就是没跑**，不写成"应该没问题"。

**第 4 条的 `-race`**：结构性补偿见 `race-guard.py`（13 处并发点逐个登记并读过），
但那是"有人想过"，不是"机器验过"。

**第 6 条**：PLAN 要求分词实测 ≤6000。我们现在的做法是**体积守卫（32 KB）+ 一次人工实测（4,149 token）**，
不是每次门禁都分词。要真做到得在门禁里跑一次分词器——那需要一个本地 tokenizer，暂时没有。

## 一句话

七条里 **4 条严格过关、3 条是补偿或代理**。这张表的价值在于：
**下次有人说"CI 七条都满足"，他能看到哪三条不是。**
