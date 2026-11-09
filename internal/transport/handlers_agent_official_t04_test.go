package transport

// GO.2 T04：`userQuestions.attachWait` 的官方流面 + claim 释放语义。
//
// 官方（dsh-user-questions/lib/index.js 的 TimedQuestionWait.attach）：
//   - wait 活着 → yield 一帧 {remainingMs}，随后持有 claim 等结算；
//   - 关流只释放 claim，**不结算问题**（答案仍可投递、其它 UI 仍可 attach）；
//   - wait 结束 → 一帧都不发，流正常结束。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOfficialQuestionAttachWaitStreamYieldsOneFrameThenHolds 钉住官方流形状：
// 一帧剩余时长 + 持有 claim（关流/结算后释放），不是"每次轮询都发一帧"。
func TestOfficialQuestionAttachWaitStreamYieldsOneFrameThenHolds(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var claims []bool
	srv := officialTestServer(t, OfficialAgentAPI{
		UserQuestionsAttachWait: func(sessionID, callID string) (*OfficialQuestionWait, error) {
			mu.Lock()
			defer mu.Unlock()
			if sessionID != "s1" || callID != "call-1" {
				t.Fatalf("参数没透传：%s/%s", sessionID, callID)
			}
			calls++
			if calls == 1 {
				return &OfficialQuestionWait{RemainingMs: 4200}, nil
			}
			return nil, nil // wait 已结算/超时：官方不再发帧
		},
		UserQuestionsClaimed: func(sessionID, callID string, claimed bool) {
			mu.Lock()
			defer mu.Unlock()
			claims = append(claims, claimed)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/userQuestions.attachWait",
		strings.NewReader(`{"agentId":"s1","callId":"call-1"}`)).WithContext(ctx)
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.serveOfficialAgentAPI(rec, req, "userQuestions.attachWait")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("attachWait 流没在 wait 结束后退出")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("attachWait 流必须是 NDJSON：%q", ct)
	}
	scanner := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, strings.TrimSpace(scanner.Text()))
	}
	if len(lines) != 1 {
		t.Fatalf("官方 attach 只 yield 一帧，实际 %d 行：%v", len(lines), lines)
	}
	if lines[0] != `{"remainingMs":4200}` {
		t.Fatalf("帧形状必须是官方 {remainingMs}：%q", lines[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(claims) != 2 || claims[0] != true || claims[1] != false {
		t.Fatalf("claim 必须持有后释放（true→false）：%v", claims)
	}
	if calls < 2 {
		t.Fatalf("流必须真的探到 wait 结束（calls=%d）", calls)
	}
}

// TestOfficialQuestionAttachWaitStreamDeadWaitEndsEmpty：wait 已经结束 → 200 +
// 零帧（官方语义），不是 4xx，也不是一个 0 骗 UI 继续转。
func TestOfficialQuestionAttachWaitStreamDeadWaitEndsEmpty(t *testing.T) {
	var claimed bool
	srv := officialTestServer(t, OfficialAgentAPI{
		UserQuestionsAttachWait: func(string, string) (*OfficialQuestionWait, error) {
			return nil, nil
		},
		UserQuestionsClaimed: func(string, string, bool) { claimed = true },
	})
	req := httptest.NewRequest(http.MethodPost, "/api/userQuestions.attachWait",
		strings.NewReader(`{"agentId":"s1","callId":"call-1"}`))
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	srv.serveOfficialAgentAPI(rec, req, "userQuestions.attachWait")
	if rec.Code != http.StatusOK {
		t.Fatalf("已结束的 wait 应该 200 + 零帧：%d %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("已结束的 wait 不许发帧：%q", rec.Body.String())
	}
	if claimed {
		t.Fatal("没有 wait 时不该登记 claim")
	}
}

// TestOfficialQuestionAttachWaitStreamErrorsBeforeHeaders：参数/调用错误在写响应头
// 之前仍是普通四字段 JSON（不能先回 200 再报错）。
func TestOfficialQuestionAttachWaitStreamErrorsBeforeHeaders(t *testing.T) {
	srv := officialTestServer(t, OfficialAgentAPI{
		UserQuestionsAttachWait: func(string, string) (*OfficialQuestionWait, error) {
			return nil, fmt.Errorf("%w: 没有这个 callId", ErrOfficialBadInput)
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/userQuestions.attachWait",
		strings.NewReader(`{"agentId":"s1","callId":"missing"}`))
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	srv.serveOfficialAgentAPI(rec, req, "userQuestions.attachWait")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 callId 必须在写头之前 400：%d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("错误必须是 JSON：%q", rec.Body.String())
	}
	if _, ok := out["error"].(map[string]any); !ok {
		t.Fatalf("错误必须是四字段信封：%v", out)
	}
}

// TestOfficialAgentPresetsList：官方 `agentPresets.list` 是零参数方法；名册为空
// 时回 []（不是 null），缺宿主能力点名 501，多给字段 400。
