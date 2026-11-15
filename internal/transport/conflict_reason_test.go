package transport

// GP 报告 §5 第 3 条：**409 的来源必须可结构化区分**。
//
// 背景：GUI 原来靠 `strings.Contains(err.Error(), "滑出内存")` 区分 409 的两种
// 来源（journal 换代 vs 更早的历史已经滑出内存）。文案一改就失效，而且判错的
// 代价是两个方向都难看：当成换代 → 重建基线、同一次点击又能再点（"点了永远在
// 转圈"）；当成到头了 → 少一页（界面安静地缺内容）。
//
// 本文件钉住三件事：
//
//  1. **取值表**（`ConflictReasons` / `conflictReasonFor`）：code → reason 的映射
//     是契约，加新 409 来源必须先在表里有一行；
//  2. **出口不变量**：任何 409 响应都带 `details.reason`，且 `error` 对象仍是
//     恰好四字段（纯 additive）。全出口那条在 `error_contract_test.go` 的
//     `scanErrorResponse` 里扫；本文件补每个产出点的**取值**；
//  3. **两种 stale_epoch 真的分得开**：journal 换代 → `stale_epoch`，
//     历史滑出内存 → `history_evicted`（同一个 code、不同 reason）。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/config"
	"dshgo/internal/cua"
	"dshgo/internal/jobs"
	"dshgo/internal/mcp"
)

// conflictReasonSet 是允许出现的 reason 取值集合（从 `ConflictReasons` 建）。
//
// 用集合而不是在断言里写 `||` 串：加新取值时**必须**同时进 `ConflictReasons`，
// 否则出口扫描会对新值报"不认识"——这正是我们要的提醒。
func conflictReasonSet() map[string]bool {
	out := map[string]bool{}
	for _, reason := range ConflictReasons() {
		out[reason] = true
	}
	return out
}

// TestConflictReasonsTable 钉住**取值表本身**：非空、不重名、三个必需语义都在。
func TestConflictReasonsTable(t *testing.T) {
	reasons := ConflictReasons()
	if len(reasons) == 0 {
		t.Fatal("ConflictReasons() 是空的：409 的 details.reason 没有可取值")
	}
	seen := map[string]bool{}
	for _, reason := range reasons {
		if reason == "" {
			t.Error("ConflictReasons() 里有空值：客户端解出来是空串，等于没有 reason")
			continue
		}
		if seen[reason] {
			t.Errorf("reason %q 重复：取值表必须一一对应", reason)
		}
		seen[reason] = true
	}

	// 任务书点名的三个语义：少一个就是没做完。
	for _, want := range []string{
		ConflictReasonStaleEpoch,
		ConflictReasonRevisionConflict,
		ConflictReasonAgentBusy,
	} {
		if !seen[want] {
			t.Errorf("取值表里没有 %q——这是（至少）必须覆盖的 409 语义之一", want)
		}
	}
	// 两种 stale_epoch 必须各有一个取值：这是本字段存在的**唯一理由**。
	if !seen[ConflictReasonHistoryEvicted] {
		t.Error("取值表里没有 history_evicted——两种 stale_epoch 就还是分不开")
	}
	// 官方 `session/agent-busy` 的 reason 是官方逐字值，不能改名成 agent_busy。
	if !seen[OfficialPromptContinuationReason] {
		t.Errorf("取值表里没有官方逐字值 %q——官方客户端按它分支", OfficialPromptContinuationReason)
	}
}

// TestConflictReasonForCode 是 code → reason 的**唯一**默认推导。
//
// 每一行都点名产出点：加一条 409 分支时先在这里补行，再写 handler——
// 否则那个 409 会落到兜底的 `conflict`，界面拿不到更细的语义。
func TestConflictReasonForCode(t *testing.T) {
	cases := []struct {
		code, want, where string
	}{
		{"stale_epoch", ConflictReasonStaleEpoch, "server.go handleSessionHistory（journal 换代/水位超前）"},
		{"revision_conflict", ConflictReasonRevisionConflict, "handlers_agent_official.go / handlers_config_patch.go / handlers_jobs.go / 官方 workspace+settings 面"},
		{"job_finished", ConflictReasonJobFinished, "handlers_jobs.go / handlers_job_terminal_official.go"},
		{"job_owner_not_live", ConflictReasonJobOwnerNotLive, "handlers_jobs.go officialJobErrorSurface（JOB_OWNER_NOT_LIVE）"},
		{"stale_head", ConflictReasonStaleHead, "handlers_forge_write_official.go / 官方 forge 写面"},
		{"busy", ConflictReasonAgentBusy, "context_mcp_sidequery.go / handlers_mcp_manager_rc2.go / server.go writePromptError"},
		{"session/agent-busy", OfficialPromptContinuationReason, "server.go serveEmptyPromptContinuation（官方逐字 reason）"},
		{"conflict", ConflictReasonConflict, "handlers_session_official.go ErrSteerUnavailable"},
		// 兜底：叫不出名字的 409 也必须有一个稳定取值，不能是空串。
		{"error", ConflictReasonConflict, "server.go writePromptError 的 default 分支"},
		{"unsupported", ConflictReasonConflict, "handlers_cua_rc2.go（cua 的 conflict 码）"},
		{"", ConflictReasonConflict, "空 code（手写信封）：兜底"},
	}
	for _, c := range cases {
		if got := conflictReasonFor(c.code); got != c.want {
			t.Errorf("conflictReasonFor(%q) = %q，want %q（%s）", c.code, got, c.want, c.where)
		}
	}
}

// TestWithConflictReason 钉住信封合并规则（纯 additive）。
func TestWithConflictReason(t *testing.T) {
	t.Run("没有 details 时补上推导值", func(t *testing.T) {
		body := errorBodyFor("revision_conflict", "revision 过期")
		got := withConflictReason(body).(map[string]any)
		details, ok := got["details"].(map[string]any)
		if !ok {
			t.Fatalf("details 不是对象：%#v", got["details"])
		}
		if details["reason"] != ConflictReasonRevisionConflict {
			t.Fatalf("details.reason = %v，want %s", details["reason"], ConflictReasonRevisionConflict)
		}
	})

	t.Run("调用点的 reason 优先（更了解语义的一层决定）", func(t *testing.T) {
		body := errorBodyFor("stale_epoch", "滑出内存", map[string]any{
			"details": map[string]any{"reason": ConflictReasonHistoryEvicted},
		})
		got := withConflictReason(body).(map[string]any)
		if reason := got["details"].(map[string]any)["reason"]; reason != ConflictReasonHistoryEvicted {
			t.Fatalf("原因被覆盖成 %v：history_evicted 必须保留", reason)
		}
	})

	t.Run("调用点的其它 details 字段不丢", func(t *testing.T) {
		body := errorBodyFor("revision_conflict", "冲突", map[string]any{
			"details": map[string]any{"expectedRevision": "7"},
		})
		details := withConflictReason(body).(map[string]any)["details"].(map[string]any)
		if details["expectedRevision"] != "7" {
			t.Errorf("details.expectedRevision 丢了：%#v", details)
		}
		if details["reason"] != ConflictReasonRevisionConflict {
			t.Errorf("details.reason = %v，want %s", details["reason"], ConflictReasonRevisionConflict)
		}
	})

	t.Run("error 对象仍是恰好四字段（纯 additive）", func(t *testing.T) {
		body := withConflictReason(errorBodyFor("job_finished", "已结束")).(map[string]any)
		obj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatalf("error 不是对象：%#v", body["error"])
		}
		if len(obj) != len(errorEnvelopeKeys) {
			t.Fatalf("error 有 %d 个键，契约要求恰好 %d 个：%#v", len(obj), len(errorEnvelopeKeys), obj)
		}
		for _, k := range errorEnvelopeKeys {
			if _, ok := obj[k]; !ok {
				t.Errorf("error 丢了 %q", k)
			}
		}
	})

	t.Run("不是 map 时原样返回", func(t *testing.T) {
		type result struct{ OK bool }
		in := result{OK: true}
		if got := withConflictReason(in); got != any(in) {
			t.Fatalf("非信封值被改写了：%#v", got)
		}
	})
}

// conflictWriter 是"直接调用一个 409 产出点"的探针。
type conflictWriter func(w http.ResponseWriter)

// TestConflictProducersCarryReason 给**每个 409 产出点**一条断言。
//
// 为什么直接调产出点而不是走 HTTP：这些分支要造出真实宿主状态（job 已结束、
// config revision 冲突、forge head 变了…），走 HTTP 的夹具成本高且容易只测到
// 前面那道 400。产出点是包内的**唯一**错误出口，直接喂错误最接近它们的真实用法。
func TestConflictProducersCarryReason(t *testing.T) {
	fakeForgeFailure := &fakeForgeWriteFailure{status: http.StatusConflict, code: "stale_head", msg: "moved"}
	forgeRC2Stale := &ForgeRC2Error{Status: http.StatusConflict, Code: "stale_head", Message: "head 已经前进"}

	cases := []struct {
		where      string
		write      conflictWriter
		wantStatus int
		wantCode   string
		wantReason string
		// extras 是必须仍然平铺在信封顶层的兄弟字段（additive 不许踩掉它们）。
		extras []string
	}{
		{
			where:      "handlers_agent_official.go 官方 agent 冲突",
			write:      func(w http.ResponseWriter) { writeOfficialError(w, ErrOfficialConflict) },
			wantStatus: http.StatusConflict, wantCode: "revision_conflict",
			wantReason: ConflictReasonRevisionConflict,
		},
		{
			where:      "handlers_agent_official.go goal 状态迁移",
			write:      func(w http.ResponseWriter) { writeOfficialError(w, ErrGoalTransition) },
			wantStatus: http.StatusConflict, wantCode: "revision_conflict",
			wantReason: ConflictReasonRevisionConflict,
		},
		{
			where: "handlers_config_patch.go revision 冲突",
			write: func(w http.ResponseWriter) {
				(&ConfigPatchHandler{}).writeApplyError(w, &config.RevisionConflict{Expected: 3, Actual: 5})
			},
			wantStatus: http.StatusConflict, wantCode: "revision_conflict",
			wantReason: ConflictReasonRevisionConflict,
			extras:     []string{"revision", "expectedRevision"},
		},
		{
			where:      "handlers_jobs.go schedule revision 冲突",
			write:      func(w http.ResponseWriter) { writeTaskActionError(w, ErrRevisionConflict) },
			wantStatus: http.StatusConflict, wantCode: "revision_conflict",
			wantReason: ConflictReasonRevisionConflict,
		},
		{
			where:      "handlers_jobs.go 任务已结束",
			write:      func(w http.ResponseWriter) { writeTaskActionError(w, ErrJobFinished) },
			wantStatus: http.StatusConflict, wantCode: "job_finished",
			wantReason: ConflictReasonJobFinished,
		},
		{
			where: "handlers_jobs.go 任务 owner 已不在（官方 JOB_OWNER_NOT_LIVE）",
			write: func(w http.ResponseWriter) {
				writeTaskActionError(w, jobs.New(jobs.CodeOwnerNotLive, "owner 已经不是活的", nil))
			},
			wantStatus: http.StatusConflict, wantCode: "job_owner_not_live",
			wantReason: ConflictReasonJobOwnerNotLive,
		},
		{
			where:      "handlers_job_terminal_official.go 任务已结束",
			write:      func(w http.ResponseWriter) { writeJTOfficialError(w, "jobs.stop", ErrJobFinished) },
			wantStatus: http.StatusConflict, wantCode: "job_finished",
			wantReason: ConflictReasonJobFinished,
		},
		{
			where:      "handlers_forge_write_official.go head 已经前进",
			write:      func(w http.ResponseWriter) { writeForgeWriteFailure(w, fakeForgeFailure) },
			wantStatus: http.StatusConflict, wantCode: "stale_head",
			wantReason: ConflictReasonStaleHead,
		},
		{
			where: "handlers_forge_rc2.go 官方 forge 写面 head 已经前进",
			write: func(w http.ResponseWriter) {
				writeForgeRC2Error(w, forgeRC2Stale, http.StatusBadGateway, "forgePanel/action-failed")
			},
			wantStatus: http.StatusConflict, wantCode: "stale_head",
			wantReason: ConflictReasonStaleHead,
		},
		{
			where:      "context_mcp_sidequery.go side query 已存在",
			write:      func(w http.ResponseWriter) { writeW149Error(w, ErrSideQueryExists) },
			wantStatus: http.StatusConflict, wantCode: "busy",
			wantReason: ConflictReasonAgentBusy,
		},
		{
			where:      "handlers_mcp_manager_rc2.go 配置正在变",
			write:      func(w http.ResponseWriter) { writeMCPManagerError(w, mcp.ErrConfigChanging) },
			wantStatus: http.StatusConflict, wantCode: "busy",
			wantReason: ConflictReasonAgentBusy,
		},
		{
			where:      "handlers_session_official.go steer 不再接受",
			write:      func(w http.ResponseWriter) { writeSessionOfficialError(w, ErrSteerUnavailable, "") },
			wantStatus: http.StatusConflict, wantCode: "conflict",
			wantReason: ConflictReasonConflict,
		},
		{
			where:      "server.go writePromptError 上一轮还没结束",
			write:      func(w http.ResponseWriter) { writePromptError(w, errors.New("上一轮还没结束")) },
			wantStatus: http.StatusConflict, wantCode: "busy",
			wantReason: ConflictReasonAgentBusy,
		},
		{
			where:      "server.go writePromptError 兜底（叫不出名字的 409）",
			write:      func(w http.ResponseWriter) { writePromptError(w, errors.New("没有在飞的一轮")) },
			wantStatus: http.StatusConflict, wantCode: "error",
			wantReason: ConflictReasonConflict,
		},
		{
			where: "handlers_cua_rc2.go cua 冲突（码不在 reason 表里 → 兜底）",
			write: func(w http.ResponseWriter) {
				writeCUARc2Error(w, &cua.Error{
					Status: http.StatusConflict, Code: "unsupported",
					Message: "这个组合没有持久化状态目录，不能保存「以后都允许」",
				})
			},
			wantStatus: http.StatusConflict, wantCode: "unsupported",
			wantReason: ConflictReasonConflict,
		},
	}

	allowed := conflictReasonSet()
	seenReason := map[string]bool{}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.write(rec)
		if rec.Code != c.wantStatus {
			t.Errorf("%s：状态码 %d，want %d", c.where, rec.Code, c.wantStatus)
			continue
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
			t.Errorf("%s：响应不是 JSON：%v（%q）", c.where, err, rec.Body.String())
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(top["error"], &obj); err != nil {
			t.Errorf("%s：error 不是对象：%v", c.where, err)
			continue
		}
		if len(obj) != len(errorEnvelopeKeys) {
			t.Errorf("%s：error 有 %d 个键，契约要求恰好 %d 个", c.where, len(obj), len(errorEnvelopeKeys))
		}
		var code string
		_ = json.Unmarshal(obj["code"], &code)
		if code != c.wantCode {
			t.Errorf("%s：code = %q，want %q（纯 additive：code 不许改）", c.where, code, c.wantCode)
		}
		var details map[string]any
		if err := json.Unmarshal(top["details"], &details); err != nil {
			t.Errorf("%s：details 不是对象：%v（%q）", c.where, err, rec.Body.String())
			continue
		}
		reason, _ := details["reason"].(string)
		if reason == "" {
			t.Errorf("%s：409 没有 details.reason：%q", c.where, rec.Body.String())
			continue
		}
		if !allowed[reason] {
			t.Errorf("%s：details.reason = %q 不在 ConflictReasons() 里——加新取值要同时进取值表", c.where, reason)
		}
		if reason != c.wantReason {
			t.Errorf("%s：details.reason = %q，want %q", c.where, reason, c.wantReason)
		}
		seenReason[reason] = true
		for _, extra := range c.extras {
			if _, ok := top[extra]; !ok {
				t.Errorf("%s：顶层兄弟字段 %q 丢了（details 是 additive，不能踩掉既有附加信息）", c.where, extra)
			}
		}
	}

	// 覆盖度：这两类语义各至少要有一个产出点被驱动到，否则"覆盖了"是空的。
	// `stale_epoch` / `history_evicted` 走 session.history 的 journal 分支
	// （见 TestHistoryConflictReasons），不在上面这张产出点表里。
	for _, want := range []string{ConflictReasonRevisionConflict, ConflictReasonAgentBusy} {
		if !seenReason[want] {
			t.Errorf("没有任何产出点断言出 reason=%q——这一类 409 的取值没人钉", want)
		}
	}
}

// TestHistoryConflictReasons 钉住**两种 stale_epoch 分得开**（GP §5 第 3 条的原始诉求）。
//
// 契约：两者共用四字段 code `stale_epoch`（既有客户端按码分支，不能改），
// 靠 `details.reason` 区分；journalEpoch/lastSeq 仍然平铺在顶层（客户端靠它们
// 重建基线，不是 details 的一部分）。
func TestHistoryConflictReasons(t *testing.T) {
	// 容量 2 + 3 帧：最老一帧被挤掉，于是 sinceSeq=0 的补洞"缺口看不出来"。
	journal := NewJournal(7, 2)
	for i := 0; i < 3; i++ {
		journal.Append("turn/done", json.RawMessage(`{}`), int64(i+1))
	}
	js := &Journals{m: map[string]*Journal{"evict-session": journal}}

	s, err := NewServer(Options{Journals: js})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cookies["reason-cookie"] = struct{}{}
	s.mu.Unlock()

	probe := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/session.history", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "reason-cookie"})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	readConflict := func(t *testing.T, rec *httptest.ResponseRecorder) (code, reason string, extras map[string]json.RawMessage) {
		t.Helper()
		var top map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
			t.Fatalf("响应不是 JSON：%v（%q）", err, rec.Body.String())
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(top["error"], &obj); err != nil {
			t.Fatalf("error 不是对象：%v", err)
		}
		if len(obj) != len(errorEnvelopeKeys) {
			t.Fatalf("error 有 %d 个键，契约要求恰好 %d 个：%s", len(obj), len(errorEnvelopeKeys), rec.Body.String())
		}
		_ = json.Unmarshal(obj["code"], &code)
		var details map[string]any
		if err := json.Unmarshal(top["details"], &details); err != nil {
			t.Fatalf("409 没有 details 对象：%s", rec.Body.String())
		}
		reason, _ = details["reason"].(string)
		return code, reason, top
	}

	t.Run("历史滑出内存 → history_evicted", func(t *testing.T) {
		rec := probe(t, `{"sessionId":"evict-session","sinceSeq":0,"limit":10}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("状态码 %d，want 409：%s", rec.Code, rec.Body.String())
		}
		code, reason, extras := readConflict(t, rec)
		if code != "stale_epoch" {
			t.Errorf("code = %q，want stale_epoch（既有客户端按码分支，不许改）", code)
		}
		if reason != ConflictReasonHistoryEvicted {
			t.Fatalf("details.reason = %q，want %q", reason, ConflictReasonHistoryEvicted)
		}
		// 客户端靠这两个兄弟字段重建基线：它们必须还在顶层。
		for _, k := range []string{"journalEpoch", "lastSeq"} {
			if _, ok := extras[k]; !ok {
				t.Errorf("顶层兄弟字段 %q 丢了", k)
			}
		}
	})

	t.Run("journal 换代 → stale_epoch", func(t *testing.T) {
		rec := probe(t, `{"sessionId":"evict-session","sinceSeq":2,"journalEpoch":999,"limit":10}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("状态码 %d，want 409：%s", rec.Code, rec.Body.String())
		}
		code, reason, _ := readConflict(t, rec)
		if code != "stale_epoch" || reason != ConflictReasonStaleEpoch {
			t.Fatalf("code=%q reason=%q，want stale_epoch/stale_epoch", code, reason)
		}
	})

	t.Run("水位比当前还大 → stale_epoch", func(t *testing.T) {
		rec := probe(t, `{"sessionId":"evict-session","sinceSeq":99,"journalEpoch":7,"limit":10}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("状态码 %d，want 409：%s", rec.Code, rec.Body.String())
		}
		code, reason, _ := readConflict(t, rec)
		if code != "stale_epoch" || reason != ConflictReasonStaleEpoch {
			t.Fatalf("code=%q reason=%q，want stale_epoch/stale_epoch", code, reason)
		}
	})

	t.Run("补洞能覆盖时仍然是 200（reason 不该凭空出现）", func(t *testing.T) {
		rec := probe(t, `{"sessionId":"evict-session","sinceSeq":2,"journalEpoch":7,"limit":10}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 %d，want 200：%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"details"`) {
			t.Errorf("成功响应里出现了 details（reason 只属于错误信封）：%s", rec.Body.String())
		}
	})
}
