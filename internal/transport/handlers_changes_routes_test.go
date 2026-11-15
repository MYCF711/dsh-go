package transport

// 官方三条变更路由的**路由层**契约：裸 JSON、认证、方法、参数与错误分类。
//
// 真实 git 与真实 diff 在 cmd/dshd 的端到端里（`TestWorkspaceChangesRoutesServeRealGitDiff`），
// 这里只钉"路由自己"的行为：消费端（官方 deliverables 客户端）按状态码与裸字段
// 判断，任何一条错了都会退化成"卡片什么都没有"。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func changesTestServer(t *testing.T, api ChangesAPI) (string, *http.Client) {
	t.Helper()
	s, err := NewServer(Options{Changes: api})
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

func changesRequest(t *testing.T, client *http.Client, method, url string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestChangesRoutesReturnBareJSONAndClassifyErrors(t *testing.T) {
	api := ChangesAPI{
		Summary: func(sessionID string, seq int64) (ChangesSummary, bool, error) {
			if sessionID != "s1" || seq != 7 {
				return ChangesSummary{}, false, nil
			}
			return ChangesSummary{Turn: 2, Total: 1, Added: 3, Deleted: 1,
				Files: []ChangesSummaryFile{{Path: "a.txt", Display: "a.txt", Added: 3, Deleted: 1}}}, true, nil
		},
		Diff: func(sessionID string, seq int64, index int) (any, bool, error) {
			if sessionID != "s1" || seq != 7 || index != 0 {
				return nil, false, nil
			}
			return ChangesTextDiff{Kind: "text", Path: "a.txt", Display: "a.txt", Before: true, After: true,
				Hunks: []ChangesHunk{{OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 2, Lines: []string{" one", "+two"}}}}, true, nil
		},
		Open: func(sessionID string, seq int64, index int, action, application string) error {
			if sessionID == "no-desktop" {
				return ErrChangesOpenUnsupported
			}
			return nil
		},
	}
	base, client := changesTestServer(t, api)

	status, body := changesRequest(t, client, http.MethodGet, base+"/api/changes.summary?sessionId=s1&seq=7")
	if status != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", status, body)
	}
	var bare map[string]any
	if err := json.Unmarshal(body, &bare); err != nil {
		t.Fatalf("summary 不是 JSON: %s", body)
	}
	// 裸对象：不是 {"result": …} 信封（官方的消费端直接读顶层字段）。
	if _, wrapped := bare["result"]; wrapped {
		t.Fatalf("summary 被套了 result 信封：%s", body)
	}
	if bare["turn"] != float64(2) || bare["total"] != float64(1) {
		t.Fatalf("summary 字段 = %s", body)
	}

	status, body = changesRequest(t, client, http.MethodGet, base+"/api/changes.diff?sessionId=s1&seq=7&index=0")
	if status != http.StatusOK || !strings.Contains(string(body), `"kind":"text"`) || strings.Contains(string(body), `"result"`) {
		t.Fatalf("diff = %d %s", status, body)
	}

	// 没有记录 / 越界下标 → 404；缺参数 → 400；方法不对 → 404。
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.summary?sessionId=s1&seq=9"); status != http.StatusNotFound {
		t.Fatalf("未知 seq status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.diff?sessionId=s1&seq=7&index=4"); status != http.StatusNotFound {
		t.Fatalf("越界 index status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.summary?seq=7"); status != http.StatusBadRequest {
		t.Fatalf("缺 sessionId status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.summary?sessionId=s1"); status != http.StatusBadRequest {
		t.Fatalf("缺 seq status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodPost, base+"/api/changes.summary?sessionId=s1&seq=7"); status != http.StatusNotFound {
		t.Fatalf("summary 允许了 POST：status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.open?sessionId=s1&seq=7&index=0"); status != http.StatusNotFound {
		t.Fatalf("open 允许了 GET：status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodPost, base+"/api/changes.open?sessionId=s1&seq=7&index=0&action=run"); status != http.StatusBadRequest {
		t.Fatalf("非法 action status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodPost, base+"/api/changes.open?sessionId=s1&seq=7&index=0"); status != http.StatusOK {
		t.Fatalf("open status=%d", status)
	}
	// 没有桌面通道 → 422（官方客户端把它映射成 nativeUnavailable）。
	if status, _ := changesRequest(t, client, http.MethodPost, base+"/api/changes.open?sessionId=no-desktop&seq=7&index=0"); status != http.StatusUnprocessableEntity {
		t.Fatalf("无桌面通道 status=%d", status)
	}
}

func TestChangesRoutesRequireCookieAndReportUnwired(t *testing.T) {
	base, client := changesTestServer(t, ChangesAPI{})
	naked := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 没有 cookie：三条路由都 401（与 /ws、/api 同一道门）。
	for _, probe := range []struct{ method, url string }{
		{http.MethodGet, "/api/changes.summary?sessionId=s1&seq=1"},
		{http.MethodGet, "/api/changes.diff?sessionId=s1&seq=1&index=0"},
		{http.MethodPost, "/api/changes.open?sessionId=s1&seq=1&index=0"},
	} {
		req, err := http.NewRequest(probe.method, base+probe.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := naked.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s 无 cookie status=%d want=%d", probe.method, probe.url, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	// 有 cookie 但宿主没接线：501（不假装 200 空摘要）。
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.summary?sessionId=s1&seq=1"); status != http.StatusNotImplemented {
		t.Fatalf("未接线 summary status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodGet, base+"/api/changes.diff?sessionId=s1&seq=1&index=0"); status != http.StatusNotImplemented {
		t.Fatalf("未接线 diff status=%d", status)
	}
	if status, _ := changesRequest(t, client, http.MethodPost, base+"/api/changes.open?sessionId=s1&seq=1&index=0"); status != http.StatusNotImplemented {
		t.Fatalf("未接线 open status=%d", status)
	}
}
