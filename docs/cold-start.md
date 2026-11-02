# 冷启动抖动：定位结论（2026-11-02）

问题：ACP 就绪时间在 **55.8–61.4 ms** 之间跨运行波动，最大值能到 85 ms。谁在抖？

## 方法：拿"同体积的空程序"做对照

只测我们自己的代码是没用的——进程启动本身有成本，而它和**二进制大小**强相关。
所以编了几个对照件，用同样的方式计时（25 次，取 p50/p90/max）：

| 二进制 | 大小 | min | p50 | p90 | max |
|---|---:|---:|---:|---:|---:|
| noop（空 main） | 1.2 MB | 10.4 | **11.6** | 12.9 | 14.6 |
| noop-big（空 main + 11MB embed） | 12.8 MB | 40.2 | **46.5** | 66.1 | 76.7 |
| dshd（未裁剪） | 12.6 MB | 39.3 | **41.8** | 44.8 | 63.8 |
| dshd（`-s -w`） | 8.8 MB | 28.9 | **31.7** | 36.5 | 65.7 |
| 发布件 go.17 | 8.8 MB | 28.7 | **30.5** | 60.0 | 66.0 |

## 结论三条

1. **抖动不是我们的代码。**
   12.8 MB 的空程序（`main(){}` 里只有一个 `len(blob)`）p50 是 46.5 ms，
   而 12.6 MB 的 dshd 是 41.8 ms——**同一个量级，我们甚至更快**。
   方差也来自同一处：空程序 max 76.7，dshd max 63.8。

2. **成本 ≈ 2.5 ms/MB。**
   1.2 MB → 11.6 ms；8.8 MB → 30.5 ms；12.8 MB → 46.5 ms。
   这是 Windows 的二进制加载（page-in + 杀软扫描），不是 Go 运行时初始化
   （`GODEBUG=inittrace=1` 里所有包的 init 合计 ≈ 4 ms，且每个都是 0 ms clock）。

3. **`-s -w` 值 11 ms。**
   同一份代码 12.6 MB → 8.8 MB，p50 41.8 → 31.7 ms。

## 所以"ACP 就绪 57–61 ms"里是什么

```
30 ms  二进制加载（和体积成正比，与代码无关）
~27 ms 我们自己的活：装配 11 个模块 + initialize 应答
```

要压，只有两条路：

- **减体积**（每 MB ≈ 2.5 ms）——下一切片就做这个；
- 减装配期的工作（但 `main 开始 → initialize 应答` 里 `timingf` 显示到装配前是 0 ms，
  说明时间在装配与 ACP 组装之间，需要更细的打点）。

## 复现

```bash
go build -ldflags "-s -w" -o tmp/dshd-strip.exe ./cmd/dshd
# 对照件：tmp/base/noop.go（空 main）、tmp/base/big.go（embed 11MB）
python - <<'PY'
import subprocess,time,statistics
for label,cmd in [("noop",['tmp/base/noop.exe']),("dshd",['tmp/dshd-strip.exe','version'])]:
    xs=[]
    for _ in range(25):
        t=time.perf_counter(); subprocess.run(cmd,capture_output=True); xs.append((time.perf_counter()-t)*1000)
    xs.sort(); print(label, "p50", round(statistics.median(xs),1), "max", round(xs[-1],1))
PY
```

## 那么体积能不能减？先看它都装了什么

`go tool nm -size` 按符号归桶（8.7 MB 发布件）：

| 桶 | 大小 | 能减吗 |
|---|---:|---|
| `runtime`（pclntab 2.37 MB + 类型元数据） | 3.07 MB | **不能**，去了没法 panic/反射 |
| 标准库散件（json / regexp / syscall / reflect…） | 3.01 MB | 大部分不能 |
| `crypto/tls` + `crypto/x509` | **0.88 MB** | 只在"永远不说 https"时能去掉 |
| `net/http` 栈（含 http2） | 0.32 MB | 同上 |
| **我们自己的代码** | **0.29 MB** | — |
| `x/net` + `x/text`（vendor） | 0.27 MB | 部分（idna/norm 是 tls 带进来的） |
| `klauspost/compress`（zstd） | 0.25 MB | 不能，会话格式要它 |
| `pelletier/go-toml` | 0.15 MB | 不能，配置要它 |

**我们自己的代码只占 0.29 MB / 8.7 MB。**

### 决定：不减

能减的只有 TLS 那一串，合计 **~1.2 MB ≈ 3 ms**，代价是**不支持 https**——
那意味着只能连本机 magpie，接不了任何远程 provider。

**用 3 ms 换掉"能不能接远程模型"，不做。**

顺带确认另一件事：ACP 启动时我们自己的活有多少——

```
[timing] main 开始          0.00 ms
[timing] acp: 装配前         0.50 ms
[timing] acp: 装配后         0.50 ms
```

**装配 11 个模块花 0.5 ms。** initialize 往返 33.1 ms 里，
30 ms 是加载可执行文件，1 ms 是我们的活。

### 这一条到此为止

冷启动的账已经算到底了：
**60 ms ≈ 30 ms（8.7 MB 加载，不可减）+ ~1 ms（我们的代码）+ 客户端与协议开销。**

再往下只有在"二进制更小"这一条路上走，而那条路的收费站是放弃 HTTPS。
