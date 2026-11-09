package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

func changeRequestFixture(t *testing.T) (*ChangeRequestProvider, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1") && strings.Contains(r.Header.Get("Accept"), "diff"):
			_, _ = w.Write([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-old\n+new\n context\n"))
		case strings.HasSuffix(r.URL.Path, "/pulls/1") && strings.Contains(r.URL.Path, "/repos/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 1, "title": "Fix it", "state": "open", "draft": false,
				"html_url": "https://github.com/o/r/pull/1", "additions": 1, "deletions": 1,
				"mergeable": true, "updated_at": "2026-11-08T00:00:00Z",
				"user":                map[string]any{"login": "alice"},
				"head":                map[string]any{"ref": "feature", "sha": "abc123"},
				"base":                map[string]any{"ref": "main"},
				"requested_reviewers": []any{},
			})
		case strings.Contains(r.URL.Path, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "check_runs": []map[string]any{{
				"name": "ci", "status": "completed", "conclusion": "success", "details_url": "https://ci.example/1",
				"started_at": "2026-11-08T00:00:00Z", "completed_at": "2026-11-08T00:01:00Z",
				"check_suite": map[string]any{"name": "build"},
			}}})
		case strings.HasSuffix(r.URL.Path, "/repos/o/r"):
			_, _ = w.Write([]byte(`{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":false}`))
		case strings.Contains(r.URL.Path, "/contents/a.go"):
			content := base64.StdEncoding.EncodeToString([]byte("package main\n"))
			_ = json.NewEncoder(w).Encode(map[string]any{"content": content, "encoding": "base64"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &ChangeRequestProvider{BaseURL: srv.URL, Token: "test-token", HTTP: srv.Client()}, srv.URL + "/o/r/pull/1"
}

func TestChangeRequestProviderChecksDiffTextStatesRealHTTP(t *testing.T) {
	p, rawURL := changeRequestFixture(t)
	ctx := context.Background()
	checks, err := p.ChangeRequestChecks(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if checks.HeadSHA != "abc123" || checks.Title != "Fix it" || checks.ChecksRollup != "success" || len(checks.Checks) != 1 {
		t.Fatalf("checks 结果不对：%+v", checks)
	}
	if checks.Checks[0].State != "success" || checks.Checks[0].Workflow == nil || *checks.Checks[0].Workflow != "build" {
		t.Fatalf("check 投影不对：%+v", checks.Checks[0])
	}
	if len(checks.MergeMethods) != 2 || checks.MergeMethods[0] != "merge" || checks.MergeMethods[1] != "squash" {
		t.Fatalf("mergeMethods 不对：%v", checks.MergeMethods)
	}
	if _, err := p.ChangeRequestChecks(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: "other"}); err == nil {
		t.Fatal("stale head 必须报错")
	}

	diff, err := p.ChangeRequestDiff(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL})
	if err != nil {
		t.Fatal(err)
	}
	if diff.HeadSHA != "abc123" || len(diff.Files) != 1 || diff.Files[0].Path != "a.go" || len(diff.Files[0].Hunks) != 1 {
		t.Fatalf("diff 结果不对：%+v", diff)
	}
	if diff.Files[0].Additions != 1 || diff.Files[0].Deletions != 1 || diff.Files[0].Status != "modified" {
		t.Fatalf("diff 统计不对：%+v", diff.Files[0])
	}

	text, err := p.ChangeRequestText(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, Commit: "abc123", Path: "a.go"})
	if err != nil || text != "package main\n" {
		t.Fatalf("text = %q, err=%v", text, err)
	}

	t.Setenv("DSHD_FORGE_CHANGE_REQUEST_URLS", rawURL)
	states, err := p.ChangeRequestStates(ctx, transport.OfficialForgeChangeRequestParams{})
	if err != nil || states.Type != "baseline" || len(states.States) != 1 || states.States[0].Number != 1 {
		t.Fatalf("states = %+v, err=%v", states, err)
	}

	// 未登录：结构化 auth_required，不是空结果。
	unauthed := &ChangeRequestProvider{BaseURL: p.BaseURL, HTTP: p.HTTP}
	if _, err := unauthed.ChangeRequestChecks(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}); err == nil {
		t.Fatal("未登录必须报错")
	} else if e, ok := err.(*ForgeHTTPError); !ok || e.Code != "auth_required" {
		t.Fatalf("未登录错误不对：%T %v", err, err)
	}
}
