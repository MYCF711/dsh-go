package spi

import "context"

// SubagentRunner 是"派一个子代理去干一件独立的事"这一能力的抽象。
//
// 为什么是 SPI：子代理要跑一整个 turn（模型 + 工具 + 系统提示），这些东西
// 只有宿主装配层拿得到，工具本身够不着。做成服务之后，工具只管提问与收结果，
// "怎么跑"留给宿主——换一种跑法（同步/异步/换模型）不用改工具。
type SubagentRunner interface {
	RunSub(ctx context.Context, req SubagentRequest) (SubagentResult, error)
}

// SubagentRequest 是一次委派。
type SubagentRequest struct {
	Prompt      string // 完整、自包含的任务描述
	Description string // 3-5 个词的短描述，给人看
	Cwd         string // 子代理的初始工作目录；空表示继承
}

// SubagentResult 是子代理交回来的东西：**只要结果，不要中间步骤**。
type SubagentResult struct {
	Text   string // 子代理最后的正文
	Steps  int    // 用了几步
	Calls  int    // 调了几次工具
	Tokens struct {
		In  int
		Out int
	}
	// BorrowedApprovals 为真表示子代理**沿用了主会话的审批记忆**：
	// 主会话里被"永远允许"过的工具，子代理不会再问一次。
	//
	// 这是有意的（同一个人、同一件事，问两遍纯属打扰），但它是一次
	// **权限面的扩大**——模型自己派出去的子代理，用的是你刚才批准的授权。
	// 所以必须回传给调用方，让它出现在 Summary 里，而不是埋在行为里。
	BorrowedApprovals bool
}
