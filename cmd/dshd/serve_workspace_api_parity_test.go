package main

// HS-16B 先红（workspace 域，宿主侧）：三处与官方契约 / 本包自身注释不一致的真实缺陷。
//
// 契约原件（只读镜像）：
//
//	dsh-api-workspace-files/lib/index.js:234  cutPage()
//	  `if (lineNumber > last) return {…, eof:false}` —— 页满即停，**文件尾部
//	  不再读**；`admit()` 只在页面已累计超过 maxBytes 时才 workspace-file/too-large。
//	  所以"offset 落在末行之后"时 lines=0、eof=false（官方 result 注释：`0` when
//	  `offset` lies past the file's last line；eof: includes the file's LAST LINE）。
//	dsh-api-workspace-files/lib/index.js:430  read()
//	  页面文本含 NUL → `workspace-file/not-text`（不做"替客户端吞掉二进制"）。
//	dsh-api-workspace-files/lib/index.js:591  locateFile()
//	  只认 `entry.type === "file"`；symlink 从 lstat 起就不是 file →
//	  `workspace-file/not-regular-file`（**没有**专门的 symlink 码）。
//	dsh-api-workspace-files/lib/index.js:505  list()
//	  `entry.type === "symlink"` 时 stat 跟随：目标不是目录 →
//	  `workspace-file/not-directory`（工作区内的目录符号链接是**合法**的；
//	  只有逃出工作区才 outside-workspace）。
//	dsh-api-workspace-files/lib/index.js:524  relativePath()
//	  `baseFile` 给出时 path 必须是相对路径：空串 / 绝对路径 / `scheme:` 前缀 /
//	  含 NUL → `gateway/bad-request`。
//	dsh-api-workspace-controller/lib/index.js:881  initializeDefault()
//	  首次使用时建/复用默认工作区；不可用时回 undefined（不是错误，
//	  也不许假造一个工作区）。本仓还没有这个宿主动作：它属于**外壳**层
//	  （`cmd/dshd/serve.go` 的装配与启动），本轮写作用域禁止改外壳，
//	  所以它只在 WIRING.md 里作为"需要 Lead 接的一条"登记，不在这里造红。

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOfficialWorkspaceFilesReadPastLastLineIsNotEOF 官方 cutPage：页满即停，
// 后面还有内容就不算读到末行。
//
// 缺陷：filesRead 用 `EOF: end >= total` 判 eof。`offset` 落在末行之后时
// `start == total`、`end == total`、`len(page) == 0`，于是报出
// **lines=0 且 eof=true**——同一个文件，客户端据此认为"文件只有这么多"，
// 与官方 `0` when `offset` lies past the file's last line 相反。
func TestOfficialWorkspaceFilesReadPastLastLineIsNotEOF(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	id := "ffffffff-0000-0000-0000-00000000r001"
	newRealSession(t, h.home, root, id)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("l1\nl2\nl3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 菜单：第 3 行的页 → eof=true（包含末行）。
	res := h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "notes.txt", "range": map[string]any{"offset": 3, "limit": 5},
	})
	if res["text"] != "l3" || res["lines"] != float64(1) || res["eof"] != true {
		t.Fatalf("末行页语义不对：%v", res)
	}

	// 越过末行：lines=0、text 空 —— 但**不是** eof。
	res = h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "notes.txt", "range": map[string]any{"offset": 99},
	})
	if res["text"] != "" || res["lines"] != float64(0) {
		t.Fatalf("越过末行的页应是空页：%v", res)
	}
	if eof, _ := res["eof"].(bool); eof {
		t.Fatalf("越过末行的页 eof 必须是 false（官方 cutPage：页满即停、没读到末行）；got %v", res)
	}
}

// TestOfficialWorkspaceFilesReadRefusesNULBytes 官方 read()：页面含 NUL →
// `workspace-file/not-text`，不是把二进制当文本发出去。
//
// 缺陷：filesRead 直接 `string(content)`，NUL 原样进 `text`；客户端拿到的
// "文本"里藏着 0x00，JSON 能编码（`\u0000`），于是调用方以为拿到了文本。
func TestOfficialWorkspaceFilesReadRefusesNULBytes(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	id := "ffffffff-0000-0000-0000-00000000r002"
	newRealSession(t, h.home, root, id)
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte("abc\x00def\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := h.call(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "bin.dat", "range": map[string]any{},
	})
	if code == http.StatusOK {
		t.Fatalf("含 NUL 的文件不许当文本返回：%v", out)
	}
	msg := h.fail(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "bin.dat", "range": map[string]any{},
	}, code)
	if !strings.Contains(msg, "not-text") {
		t.Fatalf("官方码是 workspace-file/not-text，实际：%q", msg)
	}
}

// TestOfficialWorkspaceFilesReadRefusesSymlinkWithOfficialCode 官方 locateFile：
// symlink 从 lstat 起就不是 file → `workspace-file/not-regular-file`。
//
// 缺陷：本仓造了一个私有的 `workspaceFiles/symlink-refused`，客户端按官方码
// 分支时匹配不到（同一个"不是普通文件"被说成两个码）。
func TestOfficialWorkspaceFilesReadRefusesSymlinkWithOfficialCode(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	id := "ffffffff-0000-0000-0000-00000000r003"
	newRealSession(t, h.home, root, id)
	target := filepath.Join(root, "real.txt")
	if err := os.WriteFile(target, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("本机不允许建符号链接：%v", err)
	}

	msg := h.fail(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "link.txt", "range": map[string]any{},
	}, http.StatusBadRequest)
	if !strings.Contains(msg, "workspace-file/not-regular-file") {
		t.Fatalf("symlink 必须是官方 workspace-file/not-regular-file，实际 %q", msg)
	}
	if strings.Contains(msg, "symlink-refused") {
		t.Fatalf("不许再用本仓私有的 symlink-refused 码：%q", msg)
	}
}

// TestOfficialWorkspaceFilesListFollowsDirectorySymlinkInsideWorkspace 官方 list()：
// 工作区内的目录符号链接是合法的（stat 跟随），只有逃出工作区才拒。
//
// 缺陷：官方ResolveWithin 在目标存在时用 EvalSymlinks 复核边界，任何逃不出
// 工作区的链接都能过——但 `filesList` 之后的 `os.Stat` 才判类型；这条其实是
// 绿行为，先用测试钉住它**不许**在修 symlink 语义时被顺手拒掉。
func TestOfficialWorkspaceFilesListFollowsDirectorySymlinkInsideWorkspace(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	id := "ffffffff-0000-0000-0000-00000000r004"
	newRealSession(t, h.home, root, id)
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link-in")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("本机不允许建符号链接：%v", err)
	}

	// 直接列链接（工作区内）→ 官方跟随目录，列真实子项。
	res := h.ok(t, "workspaceFiles.list", map[string]any{
		"workspaceFileScopeId": id, "path": "link-in",
	})
	entries, _ := res["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("工作区内的目录符号链接必须能列（官方 list 跟随 symlink）：%v", res)
	}
	if entry, _ := entries[0].(map[string]any); entry["name"] != "a.txt" {
		t.Fatalf("子项不对：%v", res)
	}
}

// TestOfficialWorkspaceFilesReadBytesBaseFileRequiresRelativePath 官方 relativePath：
// baseFile 给出时 path 必须是相对路径（空串 / 绝对路径都 400 gateway/bad-request）。
//
// 缺陷：本仓只在 `base == ""` 时才用 root，空串 path 被官方ResolveWithin 拒成
// not-a-file；绝对路径 path 反而被接受并直接覆盖 baseFile 的语义——两处都与官方相反。
func TestOfficialWorkspaceFilesReadBytesBaseFileRequiresRelativePath(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	id := "ffffffff-0000-0000-0000-00000000r005"
	newRealSession(t, h.home, root, id)
	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 空 path + baseFile → 官方 gateway/bad-request。
	msg := h.fail(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": "",
		"options": map[string]any{"baseFile": "a.bin"},
	}, http.StatusBadRequest)
	if !strings.Contains(msg, "gateway/bad-request") {
		t.Fatalf("空 path + baseFile 必须报官方 gateway/bad-request，实际 %q", msg)
	}

	// 绝对 path + baseFile → 同样 gateway/bad-request（baseFile 只解析相对路径）。
	absolute := filepath.ToSlash(filepath.Join(root, "a.bin"))
	msg = h.fail(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": absolute,
		"options": map[string]any{"baseFile": "a.bin"},
	}, http.StatusBadRequest)
	if !strings.Contains(msg, "gateway/bad-request") {
		t.Fatalf("绝对 path + baseFile 必须报官方 gateway/bad-request，实际 %q", msg)
	}
}
