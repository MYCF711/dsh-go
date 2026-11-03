# race-guard：为什么按「序号」而不是「行号」配对（2026-11-03）

## 症状

2026-11-03 一天里红了三次，每次原因都一样：

```
FAIL  新 goroutine 没登记：internal/transport/server.go:142
FAIL  新 goroutine 没登记：internal/transport/server.go:225
WARN  清单里的位置已经不存在：internal/transport/server.go:136
WARN  清单里的位置已经不存在：internal/transport/server.go:219
```

而代码里**一个 goroutine 都没多**——只是上面插了两行 import 和一个字段，行号平移了 6 行。

## 为什么这不是小事

每次红都要人手动去改 JSON 里的数字。改第三次的时候我开始想「要不直接删掉这条检查」——
**这就是噪声真正的代价：它会训练人去关掉检查。**

## 改法

配对键从「文件:绝对行号」改成「文件 + 该文件里第几个 goroutine」：

- 插行/删行 → 序号不变 → **PASS**，同时把清单里的行号自动刷成实际行号
- 真新增一个 goroutine → 该文件序号数变多 → **FAIL**（自测：插一个独占一行的 `go func(){}()` → `FAIL ... server.go:534`）
- 真删掉一个 → 数量变少 → **FAIL**

## 自测（当场做的，不是推演）

| 场景 | 结果 |
|---|---|
| 插入一行注释 | `PASS（13 处）`，清单行号自动刷新 |
| 插入一个真 goroutine | `FAIL 新 goroutine 没登记：server.go:534` |
| 恢复 | `PASS（13 处）` |

## 留下的一条规矩

> **检查红了，先问「红的是风险还是噪声」。**
> 如果人在为了让它变绿而改数据而不是改代码，那这条检查已经在消耗信用了。
