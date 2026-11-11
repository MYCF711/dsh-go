package transport

// rc.2 `user_terminal_list` / `user_terminal_read` 数据源适配器的判据测试。
//
// 钉三件事：
//  1. `control` 逐条来自 `WebTerminalInfo`（同一份 roster），本层不重算；
//  2. `running` 的投影是**诚实**的（running + 无探针 = unknown，不是 yes）；
//  3. 会话来源缺失时**响亮失败**，不会静默把子代理当主会话。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dshgo/internal/terminal"
)

type fakeRoster struct {
	infos    []OfficialWebTerminalInfo
	snapshot OfficialTerminalSnapshot
	listErr  error
	attErr   error
	lastID   string
}

func (f *fakeRoster) TerminalList(string) ([]OfficialWebTerminalInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.infos, nil
}

func (f *fakeRoster) TerminalAttach(_ context.Context, sessionID, id string) (OfficialTerminalSnapshot, error) {
	f.lastID = id
	if f.attErr != nil {
		return OfficialTerminalSnapshot{}, f.attErr
	}
	return f.snapshot, nil
}

func TestUserTerminalSourceKeepsControlFromRoster(t *testing.T) {
	roster := &fakeRoster{infos: []OfficialWebTerminalInfo{
		{ID: "u1", Title: "user shell", Cwd: "/w", State: "running", Origin: "user", Control: "user"},
		{ID: "a1", Title: "agent shell", Cwd: "/w", State: "running", Origin: "agent", Control: "agent"},
	}}
	src := NewUserTerminalSource(roster, func(context.Context, string) (string, error) { return "user", nil })
	got, err := src.ListUserTerminals(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("roster 有 2 条就该回 2 条（筛选是工具层的判据，不是数据源的）：%+v", got)
	}
	if got[0].Control != terminal.PanelControlUser || got[1].Control != terminal.PanelControlAgent {
		t.Fatalf("control 必须逐条来自 WebTerminalInfo: %+v", got)
	}
	if got[0].ID != "u1" || got[0].Title != "user shell" || got[0].Cwd != "/w" {
		t.Fatalf("id/title/cwd 应原样透传: %+v", got[0])
	}
}

// rc.2 之前的构造点不填 origin/control，wire 边界靠 aligned() 补齐；
// 数据源也必须用同一套补齐规则，否则"侧栏有 origin=user、工具里是空串"。
func TestUserTerminalSourceAlignsMissingControl(t *testing.T) {
	roster := &fakeRoster{infos: []OfficialWebTerminalInfo{{ID: "t1", State: "running"}}}
	src := NewUserTerminalSource(roster, nil)
	got, err := src.ListUserTerminals(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Control != terminal.PanelControlUser {
		t.Fatalf("空 control 应补齐成官方默认 user，实际 %q", got[0].Control)
	}
}

// running：非 running 状态 = no；running 但本宿主没有前台探针 = unknown（不能编 yes）。
func TestUserTerminalSourceRunningIsHonest(t *testing.T) {
	roster := &fakeRoster{infos: []OfficialWebTerminalInfo{
		{ID: "a", State: "running"},
		{ID: "b", State: "exited"},
		{ID: "c", State: "failed"},
	}}
	src := NewUserTerminalSource(roster, nil)
	got, err := src.ListUserTerminals(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Running != terminal.PanelRunningUnknown {
		t.Fatalf("运行中但无探针必须 unknown，实际 %q", got[0].Running)
	}
	if got[1].Running != terminal.PanelRunningNo || got[2].Running != terminal.PanelRunningNo {
		t.Fatalf("非 running 状态必须 no: %+v", got)
	}
}

// 退出码要真实透传（"已退出"的说明靠它，不能编 0）。
func TestUserTerminalSourceKeepsExitCode(t *testing.T) {
	code := 127
	roster := &fakeRoster{infos: []OfficialWebTerminalInfo{{ID: "t1", State: "exited", ExitCode: &code}}}
	src := NewUserTerminalSource(roster, nil)
	got, err := src.ListUserTerminals(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ExitCode == nil || *got[0].ExitCode != 127 {
		t.Fatalf("退出码要原样透传: %+v", got[0].ExitCode)
	}
}

// 读屏走 attach 的 snapshot（与 wire 同一条真值），并按 lines 分页。
func TestUserTerminalSourceReadsScreenTail(t *testing.T) {
	roster := &fakeRoster{snapshot: OfficialTerminalSnapshot{Screen: "one\ntwo\nthree\nfour"}}
	src := NewUserTerminalSource(roster, nil)
	page, err := src.ReadUserScreen(context.Background(), "s1", "t1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if roster.lastID != "t1" {
		t.Fatalf("id 没透传: %q", roster.lastID)
	}
	if page.Text != "three\nfour" || page.LineBegin != 3 || page.LineEnd != 4 || page.TotalLines != 4 {
		t.Fatalf("分页不对: %+v", page)
	}
}

func TestUserTerminalSourcePropagatesRosterErrors(t *testing.T) {
	src := NewUserTerminalSource(&fakeRoster{listErr: errors.New("控制器挂了")},
		func(context.Context, string) (string, error) { return "user", nil })
	if _, err := src.ListUserTerminals(context.Background(), "s1"); err == nil ||
		!strings.Contains(err.Error(), "控制器挂了") {
		t.Fatalf("错误要上抛: %v", err)
	}
}

func TestUserTerminalSourcePropagatesAttachErrors(t *testing.T) {
	src := NewUserTerminalSource(&fakeRoster{attErr: errors.New("终端已关闭")}, nil)
	if _, err := src.ReadUserScreen(context.Background(), "s1", "t1", 5); err == nil ||
		!strings.Contains(err.Error(), "终端已关闭") {
		t.Fatalf("attach 失败要上抛: %v", err)
	}
}

// 会话来源由宿主提供，逐字透传（工具层按 'subagent' 判拒绝）。
func TestUserTerminalSourceSessionOrigin(t *testing.T) {
	src := NewUserTerminalSource(&fakeRoster{}, func(_ context.Context, sessionID string) (string, error) {
		if sessionID == "child" {
			return terminal.PanelOriginSubagent, nil
		}
		return terminal.PanelOriginUser, nil
	})
	if got, err := src.SessionOrigin(context.Background(), "child"); err != nil || got != terminal.PanelOriginSubagent {
		t.Fatalf("子会话来源不对: %q %v", got, err)
	}
	if got, err := src.SessionOrigin(context.Background(), "root"); err != nil || got != terminal.PanelOriginUser {
		t.Fatalf("主会话来源不对: %q %v", got, err)
	}
}

// 没接线 origin：响亮失败，不静默当成主会话（那等于让子代理读用户的终端）。
func TestUserTerminalSourceMissingOriginFailsLoud(t *testing.T) {
	src := NewUserTerminalSource(&fakeRoster{}, nil)
	_, err := src.SessionOrigin(context.Background(), "s1")
	if err == nil {
		t.Fatal("没有 origin 判定必须报错")
	}
	if !strings.Contains(err.Error(), "子代理") {
		t.Fatalf("错误要说清缺的是什么能力: %v", err)
	}
}
