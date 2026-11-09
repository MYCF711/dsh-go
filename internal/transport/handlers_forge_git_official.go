package transport

// forgePanel.branches / localDiff / localText 的真实入口。
// 真值由宿主 Git 执行器提供；本文件只做官方参数形状与错误映射。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

type OfficialForgeGitAPI interface {
	HandleOfficialForgeGit(ctx context.Context, method string, params json.RawMessage) (any, error)
}

type ForgeGitError struct {
	Status  int
	Code    string
	Message string
}

func (e *ForgeGitError) Error() string { return e.Message }

var officialForgeGitMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialForgeGitAPI
}{m: map[*Server]OfficialForgeGitAPI{}}

func (s *Server) AttachOfficialForgeGitAPI(api OfficialForgeGitAPI) {
	officialForgeGitMount.mu.Lock()
	defer officialForgeGitMount.mu.Unlock()
	officialForgeGitMount.m[s] = api
}
func (s *Server) DetachOfficialForgeGitAPI() {
	officialForgeGitMount.mu.Lock()
	defer officialForgeGitMount.mu.Unlock()
	delete(officialForgeGitMount.m, s)
}
func (s *Server) officialForgeGitAPI() (OfficialForgeGitAPI, bool) {
	officialForgeGitMount.mu.RLock()
	defer officialForgeGitMount.mu.RUnlock()
	api, ok := officialForgeGitMount.m[s]
	return api, ok
}

func IsOfficialForgeGitMethod(name string) bool {
	switch Method(name) {
	case MethodForgePanelBranches, MethodForgePanelLocalDiff, MethodForgePanelLocalText:
		return true
	}
	return false
}

func (s *Server) ServeOfficialForgeGitAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialForgeGitMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 forgePanel git 方法"))
		return
	}
	api, ok := s.officialForgeGitAPI()
	if !ok {
		writeOfficialError(w, officialNotImplemented("%s 未接线：宿主没有 AttachOfficialForgeGitAPI", name))
		return
	}
	var raw json.RawMessage
	if r.Body != nil {
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeOfficialError(w, officialBadRequest("读参数失败: %v", err))
			return
		}
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	result, err := api.HandleOfficialForgeGit(r.Context(), name, raw)
	if err != nil {
		var fe *ForgeGitError
		if errors.As(err, &fe) {
			writeJSON(w, fe.Status, errorBodyFor(fe.Code, fe.Message))
			return
		}
		writeOfficialError(w, officialInternal("%v", err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}
