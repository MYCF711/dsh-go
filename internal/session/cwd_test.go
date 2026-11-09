package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

func TestWorkingDirectoryDurableReplay(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "workspace", "sub")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	log, err := CreateSession(home, filepath.Join(home, "workspace"), "s-1", "")
	if err != nil {
		t.Fatal(err)
	}
	change, err := RecordWorkingDirectory(log, target)
	if err != nil {
		t.Fatalf("记录 cwd 变更失败: %v", err)
	}
	if !change.Recorded {
		t.Fatal("目录变了必须落一条事件")
	}
	if change.Previous != filepath.Join(home, "workspace") || change.Cwd != target {
		t.Fatalf("change = %+v", change)
	}
	// 同一个目录再记一次：没有变化就不落事件（官方 previous === directory 直接返回）。
	again, err := RecordWorkingDirectory(log, target)
	if err != nil {
		t.Fatal(err)
	}
	if again.Recorded {
		t.Fatal("目录没变不该落事件")
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// 重开 = 进程重启：cwd 必须从 durable 事件重放回来。
	reopened, err := ReopenLog(log.Path)
	if err != nil {
		t.Fatalf("重开会话失败: %v", err)
	}
	defer reopened.Close()
	state, err := reopened.WorkingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if state.Cwd != target || !state.Changed || state.Events != 1 {
		t.Fatalf("重放状态 = %+v", state)
	}
	if state.Seq != change.Seq {
		t.Fatalf("重放 seq = %d，期望 %d", state.Seq, change.Seq)
	}

	// 事件形状是官方契约：type=working-directory/change、data={cwd}。
	parsed, err := v4.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range parsed.Events {
		if ev.Type != EventWorkingDirectoryChange {
			continue
		}
		found = true
		var data struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("cwd 事件 data 解析失败: %v", err)
		}
		if data.Cwd != target {
			t.Fatalf("data.cwd = %q", data.Cwd)
		}
	}
	if !found {
		t.Fatal("日志里没有 working-directory/change 事件")
	}
}

func TestWorkingDirectoryDoesNotChdirProcess(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	log, err := CreateSession(home, home, "s-1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	follower := NewCwdFollower(log)
	if err := follower.SetCwd(target); err != nil {
		t.Fatalf("SetCwd 失败: %v", err)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("进程 cwd 被改了：%q -> %q（多会话会互相串味）", before, after)
	}
	if follower.Current() != target {
		t.Fatalf("Current = %q", follower.Current())
	}
	// 相对路径按当前会话目录解析。
	child := filepath.Join(target, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := follower.SetCwd("child"); err != nil {
		t.Fatalf("相对路径 SetCwd 失败: %v", err)
	}
	if follower.Current() != child {
		t.Fatalf("相对路径解析 = %q，期望 %q", follower.Current(), child)
	}
	var _ spi.CwdFollower = follower
}

func TestWorkingDirectoryRejectsBadPaths(t *testing.T) {
	home := t.TempDir()
	log, err := CreateSession(home, home, "s-1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	follower := NewCwdFollower(log)
	if err := follower.SetCwd(""); err == nil {
		t.Fatal("空路径必须拒绝")
	}
	if err := follower.SetCwd(filepath.Join(home, "missing")); err == nil {
		t.Fatal("不存在的目录必须拒绝")
	}
	file := filepath.Join(home, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := follower.SetCwd(file); err == nil {
		t.Fatal("文件不是目录，必须拒绝")
	}
	if _, err := ResolveWorkingDirectory(home, "\x00bad"); err == nil {
		t.Fatal("含 NUL 的路径必须拒绝")
	}
}

func TestFoldWorkingDirectoryIgnoresBadEvents(t *testing.T) {
	original := filepath.Join(t.TempDir(), "original")
	next := filepath.Join(t.TempDir(), "next")
	header := v4.Header{Version: 4, Cwd: original}
	events := []v4.Event{
		{Type: EventWorkingDirectoryChange, Seq: 0, Data: []byte(`{"cwd":""}`)},
		{Type: EventWorkingDirectoryChange, Seq: 1, Data: []byte(`{"cwd":"relative/path"}`)},
		{Type: EventWorkingDirectoryChange, Seq: 2, Data: []byte(`not json`)},
		{Type: "user/message", Seq: 3, Data: []byte(`{"content":[]}`)},
	}
	state, err := FoldWorkingDirectory(header, events)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cwd != original || state.Changed {
		t.Fatalf("坏事件不许把 cwd 弄坏: %+v", state)
	}

	encoded, err := json.Marshal(map[string]any{"cwd": next})
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, v4.Event{Type: EventWorkingDirectoryChange, Seq: 4, Data: encoded})
	state, err = FoldWorkingDirectory(header, events)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cwd != next || !state.Changed || state.Events != 1 || state.Seq != 4 {
		t.Fatalf("state = %+v", state)
	}
}

func TestFoldWorkingDirectoryFallsBackToHeader(t *testing.T) {
	fromHeader := filepath.Join(t.TempDir(), "from-header")
	state, err := FoldWorkingDirectory(v4.Header{Version: 4, Cwd: fromHeader}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cwd != fromHeader || state.Changed {
		t.Fatalf("state = %+v（header.cwd 是原始项目目录，不是变更）", state)
	}
}

func TestReopenLogPreservesSeq(t *testing.T) {
	home := t.TempDir()
	log, err := CreateSession(home, home, "s-1", "")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := log.Append("turn/start", map[string]any{"turn": index + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := ReopenLog(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Seq() != 3 {
		t.Fatalf("重开 Seq = %d，期望 3（seq 必须接着历史走，不能从 0 重来）", reopened.Seq())
	}
	if err := reopened.Append("turn/start", map[string]any{"turn": 4}); err != nil {
		t.Fatal(err)
	}
	parsed, err := v4.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Events[len(parsed.Events)-1].Seq; got != 3 {
		t.Fatalf("最后一条 seq = %d，期望 3", got)
	}
}
