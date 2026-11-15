package transport

// 官方 0.2.2-rc.2 `credentials.describe` 的 `masked` 字段（R3-G08）。
//
// 官方原件：
//
//	@deepseek-ai/dsh-credentials/src/types.ts:62-75          masked?: string
//	@deepseek-ai/dsh-credentials-local/src/index.ts:522-530  maskCredential()
//	@deepseek-ai/dsh-credentials-local/src/index.ts:648-657  describe() 只在 source:'file' 填
//
// 三条硬要求（本文件的全部意义，别为了"看起来更安全"改算法）：
//  1. **逐字符**复刻官方算法。掩码是界面给人看"这是哪一把旧密钥"的唯一线索，
//     差一个字符就会让 5 头 4 尾对不上，用户以为配错了密钥。
//  2. **只有本地存储层**有掩码：env/dotenv 层官方describe 不回，本仓也不回。
//  3. **完整旧密钥永不出现**：本文件只处理"已经算好的掩码串"，明文只在
//     `internal/llm` 包内作为 mask 函数的入参出现一次。

import (
	"strings"

	"dshgo/internal/llm"
)

// officialMaskedChar 是官方用的掩码字符：U+2022 BULLET（`'•'`），不是 '*'
// 也不是中文字符——它会把密钥中段替换成等长的一串圆点。
const officialMaskedChar = "•"

// officialMaskedPreview 逐字符复刻官方 `maskCredential()`。
//
// 官方 TS（dsh-credentials-local/src/index.ts:522-530）：
//
//	function maskCredential(value: string): string {
//	  const chars = Array.from(value)
//	  const providerEnd = chars.findIndex(char => char === '-' || char === '_') + 1
//	  const hidden = chars.length - providerEnd - 5 - 4
//	  return hidden < 8 ? '•'.repeat(chars.length)
//	    : `${chars.slice(0, providerEnd + 5).join('')}${'•'.repeat(hidden)}${chars.slice(-4).join('')}`
//	}
//
// 换算成 Go（逐条对应，别"顺手简化"）：
//   - `Array.from` ⇒ `[]rune`：按**码点**拆，不是按字节、也不是 UTF-16 码元；
//   - `findIndex(...)+1` ⇒ 找到第一个 '-'/'_' 之后 +1；**找不到时 findIndex 是 -1，
//     +1 之后正好是 0**（这是官方的一个边界，别写成"找不到就整串"）；
//   - `hidden = 长度 - providerEnd - 5 - 4`；
//   - `hidden < 8` ⇒ 整串 '•'（长度 = 码点数）；
//   - 否则 ⇒ 前 `providerEnd+5` 个码点 + `hidden` 个 '•' + 末 4 个码点。
func officialMaskedPreview(value string) string {
	chars := []rune(value)
	providerEnd := 0
	for i, char := range chars {
		if char == '-' || char == '_' {
			providerEnd = i + 1
			break
		}
	}
	hidden := len(chars) - providerEnd - 5 - 4
	if hidden < 8 {
		return strings.Repeat(officialMaskedChar, len(chars))
	}
	return string(chars[:providerEnd+5]) + strings.Repeat(officialMaskedChar, hidden) + string(chars[len(chars)-4:])
}

// OfficialCredentialMasker 是凭据后端的**可选**能力：给一批 ref，回本地存储层
// 的掩码预览。
//
// 做成可选接口而不是 OfficialCredentialStore 的必选方法，理由与官方一致：
// `masked` 是 provider 生成的可选字段（"Provider-generated masked preview"），
// 不是每个 CredentialProvider 都给得出；没有这个能力的后端照常 describe，
// 只是不带 masked。也正因如此，既有 fake 实现不会因为这次新增而编译不过。
type OfficialCredentialMasker interface {
	// CredentialMasks 回本地存储层的掩码；env/dotenv 层与未配置的 ref 不出现。
	CredentialMasks(refs []string) (map[string]string, error)
}

// officialCredentialMasks 从后端取掩码；后端不支持时返回 nil（不是错误）。
func officialCredentialMasks(store OfficialCredentialStore, refs []string) (map[string]string, error) {
	masker, ok := store.(OfficialCredentialMasker)
	if !ok {
		return nil, nil
	}
	return masker.CredentialMasks(refs)
}

// CredentialMasks 让真实后端（llm catalog）支持 masked。
//
// 明文不出 `internal/llm`：这里只把 `officialMaskedPreview` 作为函数传进去，
// 拿回来的是已经替换过的掩码串。
func (c *catalogCredentialStore) CredentialMasks(refs []string) (map[string]string, error) {
	if c == nil || c.store == nil {
		return nil, nil
	}
	return c.store.DescribeCredentialMasks(refs, officialMaskedPreview)
}

// mergeOfficialCredentialMasks 把掩码并进 describe 的投影。
//
// 三条过滤（缺一条就是泄漏或不一致）：
//  1. 只并 `source=='catalog'` 的条目——官方 describe 只在 source:'file' 填 masked；
//  2. 只并**本次请求过**且后端真的给了非空掩码的 ref；
//  3. 未配置的条目（Configured=false）一律不并，哪怕后端误回了一条。
func mergeOfficialCredentialMasks(out map[string]OfficialCredentialInfo, masks map[string]string) {
	if len(masks) == 0 {
		return
	}
	for ref, masked := range masks {
		info, ok := out[ref]
		if !ok || !info.Configured || masked == "" {
			continue
		}
		if info.Source != llm.ModelCredentialSourceCatalog {
			continue
		}
		info.Masked = masked
		out[ref] = info
	}
}
