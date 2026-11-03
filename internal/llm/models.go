package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ModelEntry 是 `/v1/models` 返回的一条。
//
// **只保留界面用得上的字段**：`id` 是模型标识（选它时传给 `--model`），
// `ownedBy` 是渠道（同一网关后面可能挂着多个 provider，用户想按渠道分组）。
// 其余字段（created/object）对界面没有意义，收进来只会增加"它们什么时候会变"的担心。
type ModelEntry struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// ListModels 问网关有哪些模型（OpenAI 兼容的 `GET /v1/models`）。
//
// # 为什么由宿主问、不让界面直接问
//
// 网关的地址与凭据只在宿主这一侧（R7：界面不直连模型出口）。
// 界面要一个模型列表，走 Transport，不自己发请求。
//
// # 失败就是失败，不回空列表
//
// 回空列表会让界面显示"没有可用模型"——而真实原因可能是网关没起、凭据过期。
// 两者在界面上长得一样，但用户该做的事完全不同（换模型 vs 修网关）。
func (c *Client) ListModels(ctx context.Context) ([]ModelEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"models", nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("问网关要模型列表失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("网关回 %d：%s", resp.StatusCode, string(body))
	}
	var doc struct {
		Data []ModelEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("模型列表不是合法 JSON: %w", err)
	}
	return doc.Data, nil
}
