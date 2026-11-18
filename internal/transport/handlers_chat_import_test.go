package transport

// 官方 rc.5 `dsh-deepseek-chat-import` 的**路由层**契约：
// `POST /api/deepseek-chat-import/archive`（裸字节 + NDJSON）与
// `deepseekChatImport/limits`。
//
// 这里只钉路由自己的行为（状态码、媒体类型、行形状、单槽位、接线缺失时的 501）；
// 真实 ZIP → v4 会话的端到端在 `cmd/dshd` 的
// `TestChatImportEndToEndWritesResumableSessions`。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
)

func chatImportTestServer(t *testing.T, api *ChatImportAPI) (string, *http.Client) {
	t.Helper()
	s, err := NewServer(Options{ChatImport: api})
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return base, client
}

// fakeChatImportAPI 是一个可编排的假宿主：只记录调用、按闭包返回。
func fakeChatImportAPI(request *ChatImportRequest, calls *int, result ChatImportResult, failure error) *ChatImportAPI {
	var mu sync.Mutex
	return &ChatImportAPI{
		Limits: func() ChatImportLimits { return ChatImportLimits{MaxUploadBytes: 1024} },
		ResolveWorkspace: func(workspaceID string) (ChatImportWorkspace, bool) {
			if workspaceID != "ws-1" {
				return ChatImportWorkspace{}, false
			}
			return ChatImportWorkspace{ID: "ws-1", Path: "/w"}, true
		},
		Import: func(ctx context.Context, req ChatImportRequest) (ChatImportResult, error) {
			mu.Lock()
			*calls++
			mu.Unlock()
			if request != nil {
				*request = req
			}
			if req.OnProgress != nil {
				req.OnProgress(1, 2)
			}
			return result, failure
		},
	}
}

func postChatImport(t *testing.T, client *http.Client, url, contentType string, body []byte) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// TestChatImportRouteRequiresMediaTypeAndWorkspace 钉住官方的校验顺序
// （src/upload.ts:67-72：415 → 400，且都是纯文本）。
func TestChatImportRouteRequiresMediaTypeAndWorkspace(t *testing.T) {
	var calls int
	base, client := chatImportTestServer(t, fakeChatImportAPI(nil, &calls, ChatImportResult{}, nil))
	url := base + ChatImportUploadRoute + "?workspaceId=ws-1"

	resp, body := postChatImport(t, client, url, "application/json", []byte("{}"))
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("非 octet-stream 必须 415，拿到 %d", resp.StatusCode)
	}
	// 状态码照官方；正文按本仓四字段出口编码（官方客户端把正文当不透明文本）。
	if !strings.Contains(body, "content type must be application/octet-stream") {
		t.Fatalf("415 的 message 必须是官方原文，拿到 %q", body)
	}
	assertChatImportFourFieldError(t, body, "bad_request")

	resp, body = postChatImport(t, client, base+ChatImportUploadRoute, chatImportMediaType, []byte("x"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 workspaceId 必须 400，拿到 %d", resp.StatusCode)
	}
	if !strings.Contains(body, "workspaceId is required") {
		t.Fatalf("400 的 message 必须是官方原文，拿到 %q", body)
	}
	assertChatImportFourFieldError(t, body, "bad_request")
	if calls != 0 {
		t.Fatalf("校验失败的请求不许进入导入，实际调用了 %d 次", calls)
	}
}

// TestChatImportRouteRequiresCookie 钉住 owner-only（官方
// `deepseekChatImport/owner-only`；本仓的非 owner 就是没有 cookie）。
func TestChatImportRouteRequiresCookie(t *testing.T) {
	var calls int
	base, _ := chatImportTestServer(t, fakeChatImportAPI(nil, &calls, ChatImportResult{}, nil))
	// 换一个不带 cookie 的客户端。
	client := &http.Client{}
	resp, _ := postChatImport(t, client, base+ChatImportUploadRoute+"?workspaceId=ws-1", chatImportMediaType, []byte("x"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("没有 cookie 必须 401，拿到 %d", resp.StatusCode)
	}
	if calls != 0 {
		t.Fatal("未认证请求不许进入导入")
	}
}

// TestChatImportRouteStreamsProgressThenOutcome 钉住 NDJSON 行形状
// （官方 src/types.ts:65-73：进度行若干 + 恰好一行 outcome）。
func TestChatImportRouteStreamsProgressThenOutcome(t *testing.T) {
	var captured ChatImportRequest
	var calls int
	api := fakeChatImportAPI(&captured, &calls, ChatImportResult{
		WorkspaceID:       "ws-1",
		WorkspacePath:     "/w",
		SessionIDs:        []string{"s1", "s2"},
		ConversationCount: 2,
		OmittedByLimit:    0,
	}, nil)
	base, client := chatImportTestServer(t, api)
	resp, body := postChatImport(t, client, base+ChatImportUploadRoute+"?workspaceId=ws-1",
		chatImportMediaType+"; charset=binary", []byte("ZIPBYTES"))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导入成功必须 200，拿到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("响应必须是 application/x-ndjson，拿到 %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("响应必须带 nosniff，拿到 %q", got)
	}
	lines := parseNDJSON(t, body)
	if len(lines) != 2 {
		t.Fatalf("应有 1 条进度 + 1 条 outcome，拿到 %d 行: %s", len(lines), body)
	}
	if lines[0]["type"] != "progress" {
		t.Fatalf("第一行必须是 progress，拿到 %v", lines[0])
	}
	if lines[0]["done"].(float64) != 1 || lines[0]["total"].(float64) != 2 {
		t.Fatalf("进度值不对: %v", lines[0])
	}
	outcome := lines[1]["outcome"].(map[string]any)
	if outcome["ok"] != true {
		t.Fatalf("outcome 必须 ok=true，拿到 %v", outcome)
	}
	value := outcome["value"].(map[string]any)
	if len(value["sessionIds"].([]any)) != 2 {
		t.Fatalf("value.sessionIds 形状不对: %v", value)
	}
	// 裸字节必须原样交给宿主（不 base64、不 JSON 包一层）。
	if string(captured.Archive) != "ZIPBYTES" {
		t.Fatalf("宿主收到的归档字节不对: %q", captured.Archive)
	}
	if captured.Workspace.ID != "ws-1" || captured.Workspace.Path != "/w" {
		t.Fatalf("宿主收到的工作区不对: %+v", captured.Workspace)
	}
}

// TestChatImportRouteOutcomeForKnownFailures 钉住"归档级失败也是 200 + outcome 行"
// （官方 `failure()`，src/upload.ts:194-196）。
func TestChatImportRouteOutcomeForKnownFailures(t *testing.T) {
	cases := []struct {
		name   string
		api    func() *ChatImportAPI
		reason string
	}{
		{
			name: "工作区不存在",
			api: func() *ChatImportAPI {
				return &ChatImportAPI{
					Limits:           func() ChatImportLimits { return ChatImportLimits{MaxUploadBytes: 1024} },
					ResolveWorkspace: func(string) (ChatImportWorkspace, bool) { return ChatImportWorkspace{}, false },
					Import:           func(context.Context, ChatImportRequest) (ChatImportResult, error) { return ChatImportResult{}, nil },
				}
			},
			reason: "workspace-not-found",
		},
		{
			name: "归档太大",
			api: func() *ChatImportAPI {
				return &ChatImportAPI{
					Limits: func() ChatImportLimits { return ChatImportLimits{MaxUploadBytes: 4} },
					ResolveWorkspace: func(string) (ChatImportWorkspace, bool) {
						return ChatImportWorkspace{ID: "ws-1", Path: "/w"}, true
					},
					Import: func(context.Context, ChatImportRequest) (ChatImportResult, error) { return ChatImportResult{}, nil },
				}
			},
			reason: "too-large",
		},
		{
			name: "宿主报 unreadable-archive",
			api: func() *ChatImportAPI {
				return &ChatImportAPI{
					Limits: func() ChatImportLimits { return ChatImportLimits{MaxUploadBytes: 1024} },
					ResolveWorkspace: func(string) (ChatImportWorkspace, bool) {
						return ChatImportWorkspace{ID: "ws-1", Path: "/w"}, true
					},
					Import: func(context.Context, ChatImportRequest) (ChatImportResult, error) {
						return ChatImportResult{}, &ChatImportError{
							Reason:  ChatImportUnreadableArchive,
							Message: "the uploaded file is not a readable ZIP archive",
						}
					},
				}
			},
			reason: "unreadable-archive",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			base, client := chatImportTestServer(t, testCase.api())
			resp, body := postChatImport(t, client, base+ChatImportUploadRoute+"?workspaceId=ws-1",
				chatImportMediaType, []byte("0123456789"))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("失败也必须是 200（官方 failure()），拿到 %d", resp.StatusCode)
			}
			lines := parseNDJSON(t, body)
			if len(lines) != 1 || lines[0]["type"] != "outcome" {
				t.Fatalf("应只有一行 outcome，拿到 %s", body)
			}
			outcome := lines[0]["outcome"].(map[string]any)
			if outcome["ok"] != false {
				t.Fatalf("outcome 必须 ok=false，拿到 %v", outcome)
			}
			failure := outcome["error"].(map[string]any)
			if failure["reason"] != testCase.reason {
				t.Fatalf("原因码=%v，应为 %s", failure["reason"], testCase.reason)
			}
			if strings.TrimSpace(failure["message"].(string)) == "" {
				t.Fatalf("失败必须带诊断，拿到 %v", failure)
			}
		})
	}
}

// TestChatImportRouteIsBusyWhileOneImportRuns 钉住单槽位
// （官方 `claim()`，src/index.ts:120-131）。
func TestChatImportRouteIsBusyWhileOneImportRuns(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	api := &ChatImportAPI{
		Limits: func() ChatImportLimits { return ChatImportLimits{MaxUploadBytes: 1024} },
		ResolveWorkspace: func(string) (ChatImportWorkspace, bool) {
			return ChatImportWorkspace{ID: "ws-1", Path: "/w"}, true
		},
		Import: func(context.Context, ChatImportRequest) (ChatImportResult, error) {
			once.Do(func() { close(entered) })
			<-release
			return ChatImportResult{WorkspaceID: "ws-1", WorkspacePath: "/w"}, nil
		},
	}
	base, client := chatImportTestServer(t, api)
	url := base + ChatImportUploadRoute + "?workspaceId=ws-1"

	var firstBody string
	var firstStatus int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, body := postChatImport(t, client, url, chatImportMediaType, []byte("a"))
		firstStatus, firstBody = resp.StatusCode, body
	}()
	<-entered

	// 第一个还挂在导入里，第二个必须拿到 busy 的 outcome 行。
	resp, body := postChatImport(t, client, url, chatImportMediaType, []byte("b"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("busy 也是 200 + outcome 行，拿到 %d", resp.StatusCode)
	}
	lines := parseNDJSON(t, body)
	if len(lines) != 1 || lines[0]["type"] != "outcome" {
		t.Fatalf("busy 只写一行 outcome，拿到 %s", body)
	}
	outcome := lines[0]["outcome"].(map[string]any)
	if outcome["ok"] != false || outcome["error"].(map[string]any)["reason"] != "busy" {
		t.Fatalf("第二个请求必须拿 busy，拿到 %v", outcome)
	}
	close(release)
	wg.Wait()
	if firstStatus != http.StatusOK {
		t.Fatalf("第一个请求应成功，拿到 %d (%s)", firstStatus, firstBody)
	}
	// 槽位释放后再来一次必须能进（不是永久占死）。
	resp2, body2 := postChatImport(t, client, url, chatImportMediaType, []byte("c"))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("槽位释放后必须能再导入，拿到 %d", resp2.StatusCode)
	}
	if !strings.Contains(body2, `"ok":true`) {
		t.Fatalf("槽位释放后的导入应成功，拿到 %s", body2)
	}
}

// TestChatImportLimitsMethod 钉住 `deepseekChatImport/limits` 的结果形状
// （官方 lib/typert.host.js:14-28：`{maxUploadBytes}`）。
func TestChatImportLimitsMethod(t *testing.T) {
	var calls int
	base, client := chatImportTestServer(t, fakeChatImportAPI(nil, &calls, ChatImportResult{}, nil))
	resp, err := client.Post(base+"/api/"+string(MethodChatImportLimits), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("limits 应 200，拿到 %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			MaxUploadBytes float64 `json:"maxUploadBytes"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("limits 结果不是 JSON: %v", err)
	}
	if envelope.Result.MaxUploadBytes != 1024 {
		t.Fatalf("maxUploadBytes=%v，应为宿主给的 1024", envelope.Result.MaxUploadBytes)
	}
}

// TestChatImportNotWiredReportsStructured501 钉住"没接线不伪造"
// （不允许返回假上限，也不允许假装导入成功）。
func TestChatImportNotWiredReportsStructured501(t *testing.T) {
	base, client := chatImportTestServer(t, nil)

	resp, err := client.Post(base+"/api/"+string(MethodChatImportLimits), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未接线时 limits 必须 501，拿到 %d", resp.StatusCode)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatalf("501 必须是四字段错误体: %v", err)
	}
	if failure.Error.Code != "not_implemented" {
		t.Fatalf("错误码=%q，应为 not_implemented", failure.Error.Code)
	}

	resp2, _ := postChatImport(t, client, base+ChatImportUploadRoute+"?workspaceId=ws-1", chatImportMediaType, []byte("x"))
	if resp2.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未接线时上传路由必须 501，拿到 %d", resp2.StatusCode)
	}
}

// TestChatImportRouteRejectsNonPost 钉住非 POST 走本仓 /api 的 404 用法。
func TestChatImportRouteRejectsNonPost(t *testing.T) {
	var calls int
	base, client := chatImportTestServer(t, fakeChatImportAPI(nil, &calls, ChatImportResult{}, nil))
	resp, err := client.Get(base + ChatImportUploadRoute + "?workspaceId=ws-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET 上传路由必须 404（路由只注册 POST），拿到 %d", resp.StatusCode)
	}
	if calls != 0 {
		t.Fatal("非 POST 不许进入导入")
	}
}

// assertChatImportFourFieldError 检查正文是 /api 的四字段错误体（code/message/hint/retryable）。
func assertChatImportFourFieldError(t *testing.T, body, wantCode string) {
	t.Helper()
	var failure struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Hint      string `json:"hint"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &failure); err != nil {
		t.Fatalf("错误正文不是四字段 JSON: %q (%v)", body, err)
	}
	if failure.Error.Code != wantCode {
		t.Fatalf("错误码=%q，应为 %q", failure.Error.Code, wantCode)
	}
	if failure.Error.Hint == "" {
		t.Fatalf("四字段错误必须带 hint，拿到 %q", body)
	}
}

// parseNDJSON 把响应正文按行解成对象。
func parseNDJSON(t *testing.T, body string) []map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	lines := make([]map[string]any, 0, 2)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("NDJSON 行不是对象: %q (%v)", line, err)
		}
		lines = append(lines, decoded)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("读 NDJSON 失败: %v", err)
	}
	return lines
}
