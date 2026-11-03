# 派活模板（2026-11-03 修订：第一行必须写自检）

## 任务书第一行，永远写这一句

> **交付前你必须自己跑一遍，并把输出原样贴出来。没贴输出 = 没完成。**

实测对照（同一个模型、同一个任务，只改这一句的位置）：

| 写法 | 结果 |
|---|---|
| 自检要求放在文末「要求」里 | 写完不跑，只交代码 |
| **放在第一行，并说明"没贴输出 = 没完成"** | **跑了，贴了输出，还主动指出一个假绿风险** |

具体案例见 `docs/dshd-shortboards.md` 的「自验」一节。

## 第二句：造一个失败分支

> **改坏一个值 → 应该红 → 改回来。**

只验"跑通了"是不够的：一个永远返回 0 的脚本也能跑通。
**交付里要有一次"它本该失败、确实失败了"的记录**，否则读者无法判断那条检查是不是废的。

实测：把这条写进任务书后，它会自己造失败分支（改文件造伪引用→看报错→还原→贴输出）。

## 第三条：失败分支只准动本任务自己的产物

> **不许移动、删除、改名仓外的任何路径。** 要构造「这个路径不存在」，用参数、临时目录，或改脚本里读的根路径。

事故记录（2026-11-03）：任务书里写了 `mv ../dsh-ui ../dsh-ui.bak` 来验"目录不存在"，
它照做了——而那个目录**当时正被另一个进程编辑**。

它最后完好（`ls` 确认、无 `.bak` 残留），但那是运气：
移动一个别人正在写的目录，轻则丢文件句柄，重则丢掉未保存的内容。

**它对手册里的每一条都会忠实执行，包括不该执行的那条。**
所以任务书的第三件事不是"要它做什么"，是**划清它能碰什么**。

## 为什么是"第一行"

不是我猜的：那一次任务书总共 30 行，内容和我之前给的几乎一样，
唯一的差别是自检要求从倒数第三段挪到了第一段。**位置变了，行为就变了。**

# DSH-GO 实现者手册

> 这份手册是给**写代码的那一方**看的（dshd 自己）。维护者手册在 `AGENTS.md`，
> 里面有里程碑、决策史、界面线规划——那些与你无关，别去读。
>
> 你只做一件事：**按任务书写一个工具模块**。下面全是这件事需要的规矩。

---

## 1. 你的产出长什么样

一个目录、一个文件、一个工具：

```
modules/tool/<名字>/<名字>.go      ← 你写这个
```

**除非任务书明确说，否则你不碰任何其他文件。** 接线（`cmd/dshd/main.go`、
`dsh.toml`、快照）由监工做。

---

## 2. 骨架（照抄，别自创结构）

```go
// Package foo 提供 `foo` 工具：<一句话说明何时用它>。
package foo

import (
	"context"
	"encoding/json"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

type Tool struct{}

func (t *Tool) Name() string { return "foo" }

// Description 必须 ≤120 字。超了有测试会红。
func (t *Tool) Description() string { return "……" }

func (t *Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{...},"required":[...]}`)
}

// Caps 必填：审批与沙箱全靠它，不许按工具名猜。
func (t *Tool) Caps() spi.Caps { return spi.Caps{} }

type args struct {
	// 字段用 snake_case 的 json tag
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"x": ...}`}, err
	}
	// ...
	return spi.Result{Summary: "一行给人看的结果", Text: "正文", Data: map[string]any{}}, nil
}

func Module() kernel.Module {
	return kernel.Module{
		ID:       "tool-foo",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			reg.Register(&Tool{})
			return nil
		},
	}
}
```

---

## 3. 八条硬约束（每条都有测试或评审在查）

| # | 约束 | 违反的后果 |
|---|---|---|
| 1 | **`Description` ≤120 字** | `TestEveryToolHasDescription` 直接红 |
| 2 | **每个 `error` 分支必须设 `Hint`** | AST 扫描会查；没有 Hint 等于让模型自己猜下一步 |
| 3 | **`Caps()` 必填且如实** | 写盘→`Writes`，起进程→`Executes`，联网→`Network`，删东西→`Destructive`。**宁可多声明**：任意程序都可能写盘，那就两个都标 |
| 4 | **输出有界**（建议 ≤200 KB），截断时 `Hint` 说清怎么办 | 无界输出会撑爆上下文 |
| 5 | **`Data` 是结构化 JSON，键名 snake_case**；`Summary` 只给人看 | 投影层靠 Data |
| 6 | **路径一律正斜杠**，绝不把 `os.Getwd()` 的原生反斜杠喂出去 | Windows 上必炸 |
| 7 | **失败返回 `(Result{}, err)`**，不要半截结果 + nil error | 上层会当成成功 |
| 8 | **不写测试、不改测试、不动别的模块** | 越界即返工 |

---

## 4. 写 `Hint` 的正确姿势

`Hint` 是**给模型看的下一步动作**，不是给人看的道歉。

```go
// ✗ 空话
Hint: "参数错误"

// ✗ 只描述现象
Hint: "5xx 错误，正文为空"

// ✓ 可执行
Hint: "5xx：这是对方的问题，不是你写错了。别连着重试——隔一会儿再来，或用 gh CLI 对照"
```

判断标准：模型读完这句，**下一句该发什么**是不是就确定了。

---

## 5. 自检（收工前必须自己跑）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
gofmt -w modules/tool/<名字>
go build ./modules/tool/<名字> && go vet ./modules/tool/<名字>
python scripts/desc-len.py modules/tool/<名字>/<名字>.go    # 必须 ≤120，这条最常翻车
echo OK
```

**`desc-len.py` 是给你用的，不是给我的。** 你数不准中文字数（实测连着四次超限），
跑一下它，红了就砍，直到绿。

三条全过才算交活。**`go build` 通过 ≠ 装配生效**——接线是监工的活，你不用管，
但要在收工时说明「已建文件、未接线」。

---

## 6. 你已经栽过的坑（先看这个，能省一轮返工）

1. **`Description` 写超 120 字——连续三次。** 写完自己数一遍：中文字符也算一个字。
2. **无限调研。** 拿到"实现一个功能"这种大任务时，你会不停读文件、写临时脚本、
   造夹具验证，最后步数用完、**一行正式代码都没有**。
   正确做法：**先写文件，再验证**。任务书写"只写一个文件"时，那个文件就是全部产出。
3. **越界改文件。** 任务书说"不许改别的文件"，就一个都不要改——即使你觉得
   `main.go` 少一行 import。报告即可，让监工加。
4. **`Description` 里塞了实现细节。** 它是"什么时候用它"，不是文档。
5. **自检只验"能不能编译"，不验"落到哪里"。** 实测：`go build` 过了、测试过了、
   真机也回 `[ok]`，但文件写进了 `state/mail/` 而不是 `state/team/`。
   凡是"写到某个目录"的任务，自检里必须有一次**真实路径**的确认
   （`find <home>/state -name <文件名>`），不能只靠编译通过。

---

## 6.5 任务书长什么样，你就怎么干——以及什么时候该反过来

实测过一次对照，同一个工具，两种任务书：

| 任务书 | 步数 | 工具调用 | 产出 |
|---|---:|---:|---|
| 「参考 team.go，加这个工具，契约如下」 | 24（用满） | **34** | **0 个文件** |
| 接口全贴进任务书 + 「禁止读任何已有文件」+ 「第一步就 write」 | 14 | **2** | 202 行，一次通过 |

差别不在你，在任务书。**但你要会识别坏任务书，并且自己补上缺失的那条**：

拿到任务后，先问自己三句话——

1. **"我需要的接口，任务书里都有吗？"** 缺接口 → 你才会去读文件。
   只读**最多 2 个**真正必要的文件，然后就动手写。
2. **"第一步是不是 write？"** 不是的话，把"先读一圈再说"这个冲动按下去。
   你的预算会被 read 和 bash 吃干净，而且**没有任何中间产物能交差**。
3. **"我怎么知道做对了？"** 任务书没给自检命令，就自己拼一条，
   并且**必须包含一次真实产物检查**（文件在哪、内容对不对），不只是 build。

**反过来**：如果任务书已经写了这三条，你又烧掉了 20 步还没 write，
那就是你自己在犯第 2 条坑——停下来，直接把脑子里已有的内容写出去。

---

## 7. 代码风格（与本仓一致）

- 注释写**为什么**，不写"这里做了什么"。
- 中文注释，句号结尾；代码标识符保持英文。
- 错误信息带上下文：`fmt.Errorf("读配置失败: %w", err)`。
- 不引新依赖。标准库够用——`go.mod` 加一行要写清「为什么标准库不够」。
- 不用 `log.Fatal`、不 `panic`；把错误返回上去。

---

## 8. 这个仓的目录（你只需要知道这些）

```
modules/tool/<名字>/    ← 你写的地方（一模块一目录）
modules/exec/bash/      起进程的参考实现
modules/fs/local/       文件读写的参考实现
internal/spi/           Tool 接口、Caps、Result——定义在这里，零实现
internal/kernel/        Scope / Module / Inject / Provide——装配用的
cmd/dshd/               入口；接线在这里，但**由监工改**
```

**先读 `internal/spi/tool.go` 的 Tool 接口**，再读一个最近的同类工具当模板
（`modules/tool/github/`、`modules/tool/pwsh/`）。别读整个仓库。
