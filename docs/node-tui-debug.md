# Node/TS TUI 诊断工具与检查单

> 适用：`dsh-TUI` / `rc/` / W170 / W171 / W167 这类 Node + TypeScript + ConPTY TUI。
> Go 的 `gopls/dlv/gotestsum` 不适用于这一层。

## 1. 已装工具

| 工具 | 用途 | 调用 |
|---|---|---|
| `node` 24.20 | 运行时 | `node --version` |
| `tsx` v4.23 | 直接跑 TS/ESM | `tsx script.ts` |
| `wtfnode` | 进程不退出时列 active handles/requests | `wtfnode <script>` |
| `why-is-node-running` | 同上，输出更短 | `why-is-node-running <script>` |
| `freeze` v0.2.2 | ANSI/终端输出转 SVG/PNG/WebP | `freeze --execute "..." -o out.png` |

全局包在 `C:\Users\Administrator\AppData\Roaming\npm\node_modules`。  
在别的目录 `require('wtfnode')` 时加：

```bash
NODE_PATH="$(npm root -g)" node -e "require('wtfnode')"
```

## 2. 先分三类

| 现象 | 先看 |
|---|---|
| 进程 crash | `--trace-uncaught`、raw log 的 stack、React error #300 |
| 进程 hang / 不退出 | `wtfnode` / `why-is-node-running`、active handles、child process |
| 退出码错但没 crash | `--trace-exit`、`quit-exit-zero`、`finally` 清理、watchdog |

## 3. 常用命令

```bash
# 未捕获异常 / 警告 / 退出
node --trace-uncaught --trace-warnings --trace-exit script.mjs

# 进程不退出：看谁还活着
wtfnode script.mjs
why-is-node-running script.mjs

# TS 入口
tsx script.ts

# 断点 / DevTools
node --inspect-brk script.mjs
# 然后 Chrome/Edge 打开 chrome://inspect

# 终端输出转图（给 Lead/用户看）
freeze --execute "cat snapshots/120x36.txt" -o /tmp/120x36.png
freeze snapshots/120x36.txt -l text -o /tmp/120x36.png
```

## 4. W171 这类回归的固定顺序

1. **不要先跑 full**。
2. 读 raw log / `result.json`：
   - 先找 crash / React error / exitCode
   - 再找 `driver-not-timed-out`；它可能只是 watchdog 兜底，不是原发
3. 判断是：
   - crash（React #300、未捕获异常）
   - hang（active handle、子进程未退）
   - 驱动判据（等待窗、marker 选错）
4. 单文件回退/二分：
   ```bash
   cp file file.bak
   git apply -R --include=path/to/file.patch
   # 只跑一个尺寸的最小 restart 子序列，timeout 120
   ```
5. 同一编译 hash 复验；不要用历史绿替代当前 hash。

## 5. React #300 的已知形态

- 报错：`Minified React error #300`
- 含义：本次 render 的 hooks 数量/顺序与上次不一致
- 常见原因：hooks 放在条件 `return` 之后，某个分支（如 `/jobs`）提前 return
- 修法：把新增 hooks 统一移到所有条件 return 之前
- 验证：同一 W171 80 的 restart 子序列，`23/0`，再同 hash 复验 W170/W175

## 6. 证据格式

```text
run:
src hash:
patch hash:
first failure:
raw offset:
result.json exitCode:
active handles:
conclusion: crash / hang / driver / product
```

## 7. 禁止

- 不要用 Go 工具硬套 Node TUI
- 不要用“多跑几次到绿”代替隔离
- 不要跑 full 当调试器
- 不要改 `log-update.ts` 来修 overlay/React 问题
