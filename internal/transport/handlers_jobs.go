package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// JobInfo 是 `jobs.list` 返回的一行。
//
// 它和 `spi.JobInfo` 字段一致但**是这个包自己的类型**：协议层不 import 内核包
// （和 Message/LoadResult 的处理方式一样）。两边的对应关系由接线处那一层负责，
// 字段名一旦分叉，`jobs.list` 的快照测试会立刻红。
type JobInfo struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Done     bool   `json:"done"`
	ExitCode int    `json:"exit_code"`
	Bytes    int    `json:"bytes"`
}

// JobOutput 是 `jobs.output` 的返回。
type JobOutput struct {
	ID       string `json:"id"`
	Output   string `json:"output"`
	Done     bool   `json:"done"`
	ExitCode int    `json:"exit_code"`
	Cwd      string `json:"cwd,omitempty"`
}

// handleJobsList 是 /api/jobs.list。
//
// 没接线 → 501（这个宿主没有 exec 模块，不是我们的 bug）。
func (s *Server) handleJobsList(w http.ResponseWriter, r *http.Request) {
	if s.opt.JobsList == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("jobs.list 未接线"))
		return
	}
	jobs := s.opt.JobsList()
	if jobs == nil {
		// nil 会被编成 null，界面就得同时处理 null 和 []。归一成空数组。
		jobs = []JobInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"jobs": jobs}})
}

// handleJobsOutput 是 /api/jobs.output。
//
// 参数：`id`（必填）+ `waitMs`（可选，等多久；默认 0 = 只取现有的）。
// **不阻塞太久**：界面上是轮询，等 0 就够了；给 waitMs 是为了第一次打开时
// 能等一下正在跑的任务，别让用户看到空。
func (s *Server) handleJobsOutput(w http.ResponseWriter, r *http.Request) {
	if s.opt.JobsOutput == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("jobs.output 未接线"))
		return
	}
	var params struct {
		ID     string `json:"id"`
		WaitMs int    `json:"waitMs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体不是 JSON 对象"))
		return
	}
	if params.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 id：要取哪个任务的输出"))
		return
	}
	wait := time.Duration(params.WaitMs) * time.Millisecond
	if wait < 0 {
		wait = 0
	}
	// 上限 10 秒：再长就是在让 HTTP 请求替我干等，界面上该用轮询。
	if wait > 10*time.Second {
		wait = 10 * time.Second
	}
	out, err := s.opt.JobsOutput(params.ID, wait)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}

// ScheduleItem 是 `schedule.list` 的一行。
//
// 和 JobInfo 同理：协议层自己的类型，不 import internal/schedule。
type ScheduleItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	NextAt   string `json:"next_at"`
	EverySec int    `json:"every_seconds,omitempty"`
	DailyAt  string `json:"daily_at,omitempty"`
	WeeklyAt string `json:"weekly_at,omitempty"`
}

// handleScheduleList 是 /api/schedule.list。
func (s *Server) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	if s.opt.ScheduleList == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("schedule.list 未接线"))
		return
	}
	items := s.opt.ScheduleList()
	if items == nil {
		items = []ScheduleItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"schedules": items}})
}

// handleSessionSetModel 是 /api/session.setModel。
//
// 参数：`id`（必填）+ `model`（必填，空串表示"清除覆盖、回到默认"）。
func (s *Server) handleSessionSetModel(w http.ResponseWriter, r *http.Request) {
	if s.opt.SetSessionModel == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("session.setModel 未接线"))
		return
	}
	var params struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体不是 JSON 对象"))
		return
	}
	if params.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 id：要改哪个会话"))
		return
	}
	if err := s.opt.SetSessionModel(params.ID, params.Model); err != nil {
		// 400 是既有约定：改不动通常是 model/id 认不出来（客户端该改请求）。
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

// handleSessionSetPermission 是 /api/session.setPermission。
//
// 参数：`id`（必填）+ `permission`（deny/ask/allow/auto；空串 = 清除覆盖）。
// **值要白名单校验**：写进 store 之后会被 serve 拿去当审批策略用，
// 收一个 `"block"` 这种没定义的词，后果是那一轮的行为没人说得清。
func (s *Server) handleSessionSetPermission(w http.ResponseWriter, r *http.Request) {
	if s.opt.SetSessionPermission == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("session.setPermission 未接线"))
		return
	}
	var params struct {
		ID         string `json:"id"`
		Permission string `json:"permission"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体不是 JSON 对象"))
		return
	}
	if params.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 id：要改哪个会话"))
		return
	}
	switch params.Permission {
	case "", "deny", "ask", "allow", "auto":
	default:
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
			"permission 只能是 deny/ask/allow/auto（或空串清除），收到 "+params.Permission))
		return
	}
	if err := s.opt.SetSessionPermission(params.ID, params.Permission); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

// ModelEntry 是 `models.list` 的一行。
type ModelEntry struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// handleModelsList 是 /api/models.list。
//
// **失败不回空列表**：空列表在界面上等于"没有可用模型"，而真实原因可能是
// 网关没起、凭据过期。两者长得一样，但用户该做的事完全不同。
func (s *Server) handleModelsList(w http.ResponseWriter, r *http.Request) {
	if s.opt.ModelsList == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("models.list 未接线"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	list, err := s.opt.ModelsList(ctx)
	if err != nil {
		// 502：网关是上游，不是我们的代码；按 internal 判为可重试（网关抖完就好）。
		writeJSON(w, http.StatusBadGateway, errorBodyFor("internal", "列模型失败："+err.Error()))
		return
	}
	if list == nil {
		list = []ModelEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"models": list}})
}
