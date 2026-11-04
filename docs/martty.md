# Martty 实测：真客户端连上 dsh-go

> 2026-11-02 · `martty@0.3.3`（`martty-cli`）· `dshd 0.2.1-go.14`

最初的目标里有一条：**「ACP 能被 Martty 连上并流式收到 reasoning」**。
之前只用官方 ACP SDK 写脚本验过——那是同款协议实现，但不是真客户端。
这一份是拿 Martty 本体跑出来的。

---

## 装

```bash
npm i -g martty          # 装的是 martty-cli
```

## 接

Martty 用 `--agent` 指定 ACP agent 命令，参数一个个用 `--agent-arg` 传：

```bash
martty \
  --workspace  "D:/…/DSH-GO" \
  --agent      "D:/…/tmp/DSH GO.exe" \
  --agent-arg  acp \
  --agent-arg  --home --agent-arg "D:/…/tmp/martty-home"
```

`--check-runtime` 只握手不跑对话，用来快速确认能不能连：

```
$ martty --agent "…/DSH GO.exe" --agent-arg acp --check-runtime
agent    "D:/…/tmp/DSH GO.exe" acp --home D:/…/tmp/martty-home
initialize ok in 0.1s → dsh-go-acp
```

**`initialize ok in 0.1s → dsh-go-acp`** —— 握手成功，agent 名对得上。

## 真跑一轮

问题：*一个池塘里的睡莲每天翻倍，48 天盖满。盖满一半是第几天？先想再答。*

Martty 屏幕上出现的（去掉控制字符后）：

```
 推理：睡莲每天翻倍，所以"满池塘"前一天，必然正好盖住一半；因为第 48
 天的面积 = 第 47 天面积 × 2 既然第 48 天是全部，那么第 47 天就是 1/2。
 易错点是直觉上想"48 一半 = 24 天"但那是把"线性增长"当成了"指数增长"
 ⠙ working 1s
  1 turn · 0 step | TTFT avg 1.1s
 ↑4.2K · ↓105 | 3.4%/128K | 1 turn · 0 steps | Cache hit 0%
                             ╰· standard · Workspace Write        dsh-go-acp ·
```

逐项对上：

| 屏幕上看到的 | 来自我们的哪个 update |
|---|---|
| `推理：…` 的流式文字 | `agent_thought_chunk`（**判据 ① 的核心**） |
| `TTFT avg 1.1s` | 首字延迟由 Martty 自己按 chunk 计时 |
| `↑4.2K · ↓105 · 3.4%/128K` | `usage_update`（used / size） |
| `standard · Workspace Write` | 模式名 + `spi.Caps` 推出的权限标签 |
| `dsh-go-acp` | `initialize` 的 `agentInfo.name` |
| `1 turn · 0 steps` | `tool_call` 计数（这轮没调工具，所以 0） |

## 顺带抓到的一个真 bug

第一轮跑完，会话落到了：

```
--~003F-D-DeepSeek~0020Harness-default-workspace-DSH-GO--
```

`~003F` 是 `?` 的编码——说明 Martty 传过来的 workspace 带着 Windows 扩展长度前缀
`\\?\D:\…`，我们原样拿去算 `projectKey` 了。

后果不是"目录名难看"，而是**两边互相看不见对方的会话**：真实 DSH 永远不会产生
这种目录名（116 个真实会话目录里**零个**带 `~003F`）。

修法：`v4.NormalizeCwd` 去掉 `\\?\` / `\\.\` 前缀，`session/new` 收到 cwd 先归一化。
修完再跑，目录变成：

```
--D-DeepSeek~0020Harness-default-workspace-DSH-GO--
```

**这是真客户端才暴露的问题** —— 自己写的脚本永远传干净的路径。

修的过程中还踩了一次：测试里用反引号写 `` `\?\` ``，看着像四个字符，实际只有三个
（Go 的 raw string 不处理转义）。改用双引号 `"\\\\?\\"` 才写对。
**"看着像"是这类 bug 唯一的伪装**，所以测试里现在留了注释。

## 复跑命令

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o "tmp/DSH GO.exe" ./cmd/dshd
rm -rf tmp/martty-home && mkdir -p tmp/martty-home

martty --workspace "D:/DeepSeek Harness/default-workspace/DSH-GO" \
       --agent "$PWD/tmp/DSH GO.exe" \
       --agent-arg acp --agent-arg --home --agent-arg "$PWD/tmp/martty-home"
```

（在真终端里跑；用 `winpty` 驱动时退出阶段 `libwinpty` 可能自己断言失败，与本仓无关。）
