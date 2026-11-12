package llm

import "strings"

// 官方 0.2.2-rc.2 新增的凭据掩码预览（`CredentialInfo.masked`）。
//
// 官方原件：
//
//	@deepseek-ai/dsh-credentials/src/types.ts:62-75           masked?: string（"never the full value"）
//	@deepseek-ai/dsh-credentials-local/src/index.ts:522-530   maskCredential()
//	@deepseek-ai/dsh-credentials-local/src/index.ts:648-657   describe() 只在 source:'file' 填 masked
//
// # 为什么 mask 函数从外面注入
//
// 算法是 **transport 层的 wire 契约**（`officialMaskedPreview` 逐字符复刻官方），
// 而明文只在这张表里有。把算法写两份迟早分叉；把明文送出去又违反"值不出包"。
// 所以由调用方给函数、本方法只回**函数返回的东西**：
//
//   - 明文只作为 mask 的入参在包内出现，不进返回值、不进错误串、不落盘；
//   - mask==nil 时返回空表（"这个后端给不出 masked"），绝不退化成回明文；
//   - 只有本地存储层（`doc.Credentials`，即 `describe` 的 source=='catalog'）
//     有这个值：环境变量层与未配置的引用一律不出现——与官方 describe 同一条
//     判据（env/dotenv 层没有 masked）。
//
// 参数/错误面与 `DescribeCredentials` 完全一致：ref 名字非法整批拒绝，错误串
// 只报位置与长度（名字是调用方可控字段，把密钥误填进 ref 时不许被错误串带出去）。
func (s *ModelCatalogStore) DescribeCredentialMasks(refs []string, mask func(string) string) (map[string]string, error) {
	for i, ref := range refs {
		if err := validateCredentialRef(ref, i); err != nil {
			return nil, err
		}
	}
	if mask == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	out := make(map[string]string, len(refs))
	for _, ref := range refs {
		stored, ok := s.doc.Credentials[ref]
		if !ok || strings.TrimSpace(stored) == "" {
			// 空值等于没配（与 credentialStatusLocked 的 presence 判据一致）：
			// 给它一个掩码会让界面把"未配置"显示成"配过"。
			continue
		}
		out[ref] = mask(stored)
	}
	return out, nil
}
