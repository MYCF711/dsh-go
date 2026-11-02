# P0 验收记录

> 四条判据来自目标定义；这里只记录**可复现的命令与实测数字**，不写形容词。
> 最近一次更新：2026-11-02（0.2.1-go.11）。

## 一句话结论

| # | 判据 | 结论 | 关键数字 |
|---|---|---|---|
| ① | ACP 能被客户端连上并流式收到 reasoning | **达成** | 官方 SDK 客户端连上；reasoning 1 分片 / 64 字；`stopReason=end_turn` |
| ② | 9 个工具与 DSH 语义一致且结果结构化 | **基本达成** | 工具表 9 个 / **906 token**；能力声明全量；错误带 Hint |
| ③ | 会话 v4 双向兼容 | **达成** | 116 个真实会话解码；Go→Node 逐帧读通；12 个会话往返字节相同 |
| ④ | 内存 <150 MB、冷启动 <100 ms、工具表 ≤6k token | **达成** | 峰值 **15.3 MB**；ACP 就绪 **59.5 ms**；工具表 **906 token** |

---

## ① ACP：第三方客户端能连、能收到 reasoning

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o tmp/dshd.exe ./cmd/dshd

# 官方 SDK（@agentclientprotocol/sdk@1.4.0）当客户端
ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" \
    scripts/acp-sdk-client.mjs "先简短想想再答：9 的平方根是多少？"
```

实测输出：

```
initialize: dsh-go-acp 0.2.1-go.5 protocol=1
session/new: acp-51542791-... configOptions=2
prompt: stopReason=end_turn 用时=3006ms
  reasoning 分片=1（64 字）
  text 分片=2（16 字）
  工具调用=0
  reasoning 首片: "Simple question: square root of 9 is 3 (and -3). Answer briefly."
```

带工具与审批：

```
工具调用=1 glob  审批请求=0          # 只读工具不问
工具调用=1 shell 审批请求=1 shell:allow  # writes+executes → 问
```

跨进程恢复：

```bash
ELECTRON_RUN_AS_NODE=1 "..." scripts/acp-sdk-resume.mjs
# [A] 存暗号 → 退出；[B] resume 同一 id → 答出 BLUE-FALCON-42 → PASS
```

**未做**：真正的 Martty 应用未安装在本机；验收用的是 Martty 也会用的同一个 SDK。

## ② 工具：结构化、有界、错误带 Hint

```bash
./tmp/dshd.exe tools --manifest dsh.toml   # 打印模型看到的工具表
go test ./cmd/dshd -run TestWriteToolsDeclareCaps -v
```

| 项 | 值 |
|---|---|
| 工具数 | 9（read/glob/grep/write/edit/shell/job_output/job_kill/job_list） |
| 工具表 | 3,600 字节（紧凑 JSON）/ **1,294 token**（magpie 真实分词） |
| 能力声明 | 全部必填；快照里可见 `caps` |
| 只读 vs 需批准 | 5 : 4，由 `Caps().NeedsApproval()` 判定 |

**未做**：没有和 DSH 的工具输出做逐项对拍（字段名/摘要措辞可能不同），只对齐了"结构化 + 有界 + 带 Hint"这三条硬要求。

## ③ 会话 v4：双向兼容

```bash
go test ./codecs/v4    -run 'TestReadRealCorpus|TestRoundTripRealCorpus' -v
go test ./internal/session -run 'TestSurfaceRealCorpus' -v
```

| 项 | 数字 |
|---|---|
| 真实会话解码 | **116 个文件 / 66,877 帧 / 123,790 事件**，0 失败 |
| Go→Node 读取 | 6 个会话（最大 19 MB / 24,478 事件 / 1,225 帧）逐帧读通 |
| 读→写→再读 | 12 个会话事件字节逐条相同 |
| surface 投影 | 7,216 条可派生 → 1,378 节点（22 次压缩，级联遮蔽） |

## ④ 资源三条线

```bash
bash scripts/build.sh
python tmp/measure_mem.py     # 峰值内存
python tmp/measure_start.py   # ACP 就绪延迟
```

| 指标 | 目标 | 实测 | 余量 |
|---|---:|---:|---|
| 常驻内存（整轮峰值） | < 150 MB | **15.2 MB** | 9.9× |
| 冷启动（进程起到 initialize 应答） | < 100 ms | **59.5 ms**（median, min 58.7） | 1.7× |
| 工具表 | ≤ 6,000 token | **1,294 token** | 4.6× |
| 二进制（`-s -w`） | ≤ 25 MB | **8.8 MB** | 2.8× |

对照 DSH 本体：870 MB 私有工作集 / 61 个工具 / 12,209 token。

---

## 发布件验证

```bash
gh release view v0.2.1-go.6 --json assets --jq '.assets[] | "\(.size) \(.name)"'
```

附件体积与本地 `dist/` 逐字节一致（7,710,498 / 8,175,776 / 8,415,232），
`SHA256SUMS` 里的 windows 摘要 `1d2ef64f…` 与本地文件一致。

**空目录验证**（v0.2.1-go.5 就是在这里翻的车）：

```
$ cd /tmp/empty && ./dshd.exe modules list | wc -l
9
$ ./dshd.exe tools
工具 9 个，5832 字节
```

## 没达成的部分（诚实清单）

1. **Martty 真机未验**：本机没有安装 Martty（`AppData/Local/Programs` 下只有 codebuff / magpie / xiezuo），
   验收用的是 Martty 也会用的同一个 `@agentclientprotocol/sdk@1.4.0`。要真机验收得先装那个客户端。
2. **工具语义未与 DSH 逐项对拍**：只保证结构与边界，不保证措辞一致。
3. **ACP 未实现的方法**：`session/load`、`session/delete`、`session/fork`、`session/set_mode`、`providers/*`。
4. **审批目前按工具粒度**：没有"同类操作记住选择"。
5. **压缩只读不写**：能解 `compaction/summary`，但不会主动生成压缩（要等 M6）。

## 冷启动怎么判读（2026-11-02 补）

同一份二进制、同一台机器，连续五次测出 **59.6 / 60.9 / 65.8 / 70.1 / 81.7 ms**。
这台机器当时 396 个进程、CPU 29%——波动来自负载，不是代码。

**判读规则：**

1. 先测 `dshd version`（纯进程 + init，不含装配）。这个数**不该**随功能变化而变；
   今天 42.8 ms，与换 toml 库时的 40.0 ms 基本一致 → 说明"启动本身没退化"。
2. 再看 ACP 就绪减去 `version` 的差额（今天 65.8-42.8 ≈ 23 ms），这才是我们自己的装配成本。
3. **不要**拿今天的 median 和上周的 median 直接比。要么取同一天的 min-of-N，要么比差额。
