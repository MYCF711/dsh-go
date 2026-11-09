package main

// 官方每请求采样面的两个宿主可配项：`llm.temperature` 与 `llm.stop`。
//
// 官方依据（`docs/parameter-register.md` #13/#20/#21）：每请求采样面恰 4 个字段
// ——reasoningEffort / temperature / maxTokens / stop，**明确没有 top_p**。
// 宿主原来只能在 reasoning 与 maxTokens 上配，temperature 与 stop 全树 0 命中。
//
// # 为什么走环境变量而不是命令开关
//
// 这两项是"进程级默认采样面"，不是每条会话的选择：会话级要在 Transport 上加字段，
// 那是另一条线（R7 边界）。环境变量与 DSHD_LLM_BASE / DSHD_LLM_MODEL /
// DSHD_REASONING_EFFORT 的既有做法同一层，四个入口（run/acp/serve/子代理）共用
// 同一份解析，不会出现"某个入口偷偷用服务端默认温度"。
//
// # 三条硬约束
//
//  1. **不设就不发**：两个变量都空时 llmSampling 是零值，wire 报文逐字节不变
//     （严格网关不接受无谓漂移，见 compat.go 的默认形状测试）；
//  2. **非法 fail-loud**：温度写错不该被静默忽略——那会让人以为"我设了 0.2"，
//     实际跑的是服务端默认值；
//  3. **temperature=0 要真的发出去**：0 是确定性复现的唯一手段，不能被
//     omitempty 当成"没设置"（llm.Request 用指针表达这件事）。

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// llmTemperatureEnv 是采样温度（十进制度数）。
	llmTemperatureEnv = "DSHD_LLM_TEMPERATURE"
	// llmStopEnv 是停用词序列（逗号分隔，按给定顺序原样发出）。
	llmStopEnv = "DSHD_LLM_STOP"
)

// llmSampling 是解析后的采样面：零值 = 两个都不发。
type llmSampling struct {
	Temperature *float64
	Stop        []string
}

// samplingFromEnv 解析采样面。env 为 nil 时返回零值（测试友好，不读进程环境）。
//
// 温度只做"能不能成为一条合法请求"的校验（必须是有限浮点数）；范围不在这里
// 拍板——各网关的上界不同（0..1 / 0..2），宿主替它们定一条上限只会造出第二种
// 事实。NaN/Inf 例外：它们进了 JSON 就是非法报文，必须在发之前拒掉。
func samplingFromEnv(env func(string) string) (llmSampling, error) {
	var sampling llmSampling
	if env == nil {
		return sampling, nil
	}
	if raw := strings.TrimSpace(env(llmTemperatureEnv)); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return llmSampling{}, fmt.Errorf("%s=%q 不是浮点数：%w", llmTemperatureEnv, raw, err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return llmSampling{}, fmt.Errorf("%s=%q 不是有限的浮点数（NaN/Inf 会变成非法 JSON 报文）",
				llmTemperatureEnv, raw)
		}
		sampling.Temperature = &value
	}
	if raw := strings.TrimSpace(env(llmStopEnv)); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			// 裁剪发生在**转义还原之前**：分隔用的空格（"a, b"）会被丢掉，而
			// `\n\n` 这类转义在还原前只是"反斜杠+字母"，不会被裁掉——所以换行
			// 停用词照样表达得出来。顺序原样保留（stop 是硬闸，重排等于换闸）。
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if decoded := decodeStopSequence(part); decoded != "" {
				sampling.Stop = append(sampling.Stop, decoded)
			}
		}
		// 只写了逗号 = "没有停用词"，不是"发一个空数组"。
		if len(sampling.Stop) == 0 {
			sampling.Stop = nil
		}
	}
	return sampling, nil
}

// decodeStopSequence 把环境变量里的转义还原成真实停用词。
//
// 为什么要转义：环境变量是**单行**的，而最常用的两个停用词恰恰是换行
// （"\n\n" 截断空行、"\n" 截断行尾）。不还原，在 Windows 上根本没法输入换行，
// 这个参数就成了摆设。只认四种：\\ / \n / \r / \t；别的反斜杠组合**原样保留**
// （不猜、不吞），所以字面量 \q 还是 \q。
func decodeStopSequence(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var decoded strings.Builder
	decoded.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			decoded.WriteByte(raw[i])
			continue
		}
		switch raw[i+1] {
		case '\\':
			decoded.WriteByte('\\')
			i++
		case 'n':
			decoded.WriteByte('\n')
			i++
		case 'r':
			decoded.WriteByte('\r')
			i++
		case 't':
			decoded.WriteByte('\t')
			i++
		default:
			decoded.WriteByte(raw[i])
		}
	}
	return decoded.String()
}
