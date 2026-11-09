package transport

// PARITY-W2-PROVIDER：forgePanel 四个 Change Request 读取方法的 L1 契约。
//
// 真实的 GitHub HTTP 读取在 internal/forge（宿主注入）；本文件只做官方参数
// 形状、结果投影与错误映射。没有绑定/未登录/网络失败都是**结构化错误**，
// 不返回假 PR、假 checks，也不落成 501。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const MethodForgePanelChangeRequestStates Method = "forgePanel.changeRequestStates"

type OfficialForgeChangeRequestRef struct {
	Forge  string `json:"forge"`
	Host   string `json:"host"`
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Number int    `json:"number"`
}

type OfficialForgeChangeRequestCheck struct {
	Name        string  `json:"name"`
	State       string  `json:"state"`
	URL         *string `json:"url"`
	Workflow    *string `json:"workflow"`
	StartedAt   *string `json:"startedAt"`
	CompletedAt *string `json:"completedAt"`
}

type OfficialForgeChangeRequestChecks struct {
	Ref               OfficialForgeChangeRequestRef     `json:"ref"`
	URL               string                            `json:"url"`
	Title             string                            `json:"title"`
	State             string                            `json:"state"`
	Draft             bool                              `json:"draft"`
	Author            *string                           `json:"author"`
	HeadBranch        string                            `json:"headBranch"`
	HeadSHA           string                            `json:"headSha"`
	BaseBranch        string                            `json:"baseBranch"`
	Additions         int                               `json:"additions"`
	Deletions         int                               `json:"deletions"`
	Mergeability      string                            `json:"mergeability"`
	MergeReady        bool                              `json:"mergeReady"`
	MergeMethods      []string                          `json:"mergeMethods"`
	ReviewDecision    string                            `json:"reviewDecision"`
	Checks            []OfficialForgeChangeRequestCheck `json:"checks"`
	ChecksRollup      string                            `json:"checksRollup"`
	ChecksTruncated   bool                              `json:"checksTruncated"`
	UnresolvedThreads int                               `json:"unresolvedThreads"`
	UpdatedAt         string                            `json:"updatedAt"`
	ClosedAt          *string                           `json:"closedAt"`
}

type OfficialForgeChangeRequestHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

type OfficialForgeChangeRequestFile struct {
	Path         string                           `json:"path"`
	PreviousPath *string                          `json:"previousPath"`
	Status       string                           `json:"status"`
	Binary       bool                             `json:"binary"`
	Oversized    bool                             `json:"oversized"`
	Untracked    bool                             `json:"untracked"`
	Additions    int                              `json:"additions"`
	Deletions    int                              `json:"deletions"`
	Hunks        []OfficialForgeChangeRequestHunk `json:"hunks"`
}

type OfficialForgeChangeRequestDiff struct {
	HeadSHA    string                           `json:"headSha"`
	Files      []OfficialForgeChangeRequestFile `json:"files"`
	LocalAhead *int                             `json:"localAhead"`
	FileURLs   map[string]string                `json:"fileUrls"`
}

type OfficialForgeChangeRequestState struct {
	Title          string `json:"title"`
	State          string `json:"state"`
	URL            string `json:"url"`
	Draft          bool   `json:"draft"`
	ChecksRollup   string `json:"checksRollup"`
	ReviewDecision string `json:"reviewDecision"`
	Mergeability   string `json:"mergeability"`
	Number         int    `json:"number"`
}

type OfficialForgeChangeRequestStatesFrame struct {
	Type   string                            `json:"type"`
	States []OfficialForgeChangeRequestState `json:"states"`
}

type OfficialForgeChangeRequestParams struct {
	SessionID string
	URL       string
	HeadSHA   string
	Commit    string
	Path      string
}

type OfficialForgeChangeRequestAPI interface {
	ChangeRequestChecks(context.Context, OfficialForgeChangeRequestParams) (OfficialForgeChangeRequestChecks, error)
	ChangeRequestDiff(context.Context, OfficialForgeChangeRequestParams) (OfficialForgeChangeRequestDiff, error)
	ChangeRequestText(context.Context, OfficialForgeChangeRequestParams) (string, error)
	ChangeRequestStates(context.Context, OfficialForgeChangeRequestParams) (OfficialForgeChangeRequestStatesFrame, error)
}

type ForgeChangeRequestError struct {
	Status  int
	Code    string
	Message string
}

func (e *ForgeChangeRequestError) Error() string { return e.Message }

func IsOfficialForgeChangeRequestMethod(name string) bool {
	switch Method(name) {
	case MethodForgePanelChangeRequestChecks, MethodForgePanelChangeRequestDiff,
		MethodForgePanelChangeRequestText, MethodForgePanelChangeRequestStates:
		return true
	}
	return false
}

var officialForgeChangeRequestMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialForgeChangeRequestAPI
}{m: map[*Server]OfficialForgeChangeRequestAPI{}}

func (s *Server) AttachOfficialForgeChangeRequestAPI(api OfficialForgeChangeRequestAPI) {
	officialForgeChangeRequestMount.mu.Lock()
	defer officialForgeChangeRequestMount.mu.Unlock()
	officialForgeChangeRequestMount.m[s] = api
}

func (s *Server) DetachOfficialForgeChangeRequestAPI() {
	officialForgeChangeRequestMount.mu.Lock()
	defer officialForgeChangeRequestMount.mu.Unlock()
	delete(officialForgeChangeRequestMount.m, s)
}

func (s *Server) officialForgeChangeRequestAPI() (OfficialForgeChangeRequestAPI, bool) {
	officialForgeChangeRequestMount.mu.RLock()
	defer officialForgeChangeRequestMount.mu.RUnlock()
	api, ok := officialForgeChangeRequestMount.m[s]
	return api, ok
}

func (s *Server) ServeOfficialForgeChangeRequestAPI(w http.ResponseWriter, r *http.Request, name string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "读参数失败："+err.Error()))
		return
	}
	params, perr := parseForgeChangeRequestParams(name, raw)
	if perr != nil {
		writeJSON(w, perr.Status, errorBodyFor(perr.Code, perr.Message))
		return
	}
	api, ok := s.officialForgeChangeRequestAPI()
	if !ok || api == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBodyFor("provider_unavailable",
			fmt.Sprintf("%s 未接线：宿主没有 AttachOfficialForgeChangeRequestAPI（GitHub provider 不可用）", name)))
		return
	}
	ctx := r.Context()
	var result any
	switch Method(name) {
	case MethodForgePanelChangeRequestChecks:
		result, err = api.ChangeRequestChecks(ctx, params)
	case MethodForgePanelChangeRequestDiff:
		result, err = api.ChangeRequestDiff(ctx, params)
	case MethodForgePanelChangeRequestText:
		result, err = api.ChangeRequestText(ctx, params)
	case MethodForgePanelChangeRequestStates:
		result, err = api.ChangeRequestStates(ctx, params)
	default:
		err = &ForgeChangeRequestError{Status: 400, Code: "bad_request", Message: "不是 Change Request 读取方法"}
	}
	if err != nil {
		var fe *ForgeChangeRequestError
		if errors.As(err, &fe) {
			writeJSON(w, fe.Status, errorBodyFor(fe.Code, fe.Message))
			return
		}
		writeJSON(w, http.StatusBadGateway, errorBodyFor("provider_failed", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func parseForgeChangeRequestParams(name string, raw []byte) (OfficialForgeChangeRequestParams, *ForgeChangeRequestError) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return OfficialForgeChangeRequestParams{}, &ForgeChangeRequestError{Status: 400, Code: "bad_request", Message: "请求体必须是 JSON 对象"}
	}
	str := func(field string, required bool) (string, *ForgeChangeRequestError) {
		value, ok := obj[field]
		if !ok {
			if required {
				return "", &ForgeChangeRequestError{Status: 400, Code: "bad_request", Message: "缺 " + field}
			}
			return "", nil
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil || strings.TrimSpace(s) == "" {
			return "", &ForgeChangeRequestError{Status: 400, Code: "bad_request", Message: field + " 必须是非空字符串"}
		}
		return s, nil
	}
	params := OfficialForgeChangeRequestParams{}
	var err *ForgeChangeRequestError
	if Method(name) != MethodForgePanelChangeRequestStates {
		if params.SessionID, err = str("sessionId", true); err != nil {
			return params, err
		}
		if params.URL, err = str("url", true); err != nil {
			return params, err
		}
	}
	switch Method(name) {
	case MethodForgePanelChangeRequestChecks:
		if params.HeadSHA, err = str("headSha", true); err != nil {
			return params, err
		}
	case MethodForgePanelChangeRequestText:
		if params.Commit, err = str("commit", true); err != nil {
			return params, err
		}
		if params.Path, err = str("path", true); err != nil {
			return params, err
		}
	}
	for field := range obj {
		switch field {
		case "sessionId", "url", "headSha", "commit", "path":
		default:
			return params, &ForgeChangeRequestError{Status: 400, Code: "bad_request", Message: "不认识的字段: " + field}
		}
	}
	return params, nil
}
