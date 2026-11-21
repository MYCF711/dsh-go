//go:build windows

package webfrontend

// 静态站围栏对目录 junction 的盲区（读边界）。
//
// dist 里的第二道围栏（`resolve`）语义是"解析后还必须落在 root 内"。只靠
// `filepath.EvalSymlinks` 时，Go ≥1.23 的 `winsymlink=1` 让这道围栏对 junction
// 完全失效：junction 本身原样返回（路径看起来还在根内），junction **下面**的路径
// 直接 ERROR_PATH_NOT_FOUND（errno=3）——而 `err != nil` 分支被当成"文件不存在 →
// 交给 serveAsset 回 404"，于是 serveAsset 的 `os.Stat` 跟着 junction 真读到了 dist
// 外面的文件，最后以 200 发出去。
//
// 另一半是 `New`：root 必须和 target 解析到同一个坐标系。dist 自己经 junction 部署
// 时，只解析 target、不解析 root，会把每个**合法**资产都判成越界（403）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

// makeJunction 建一个目录 junction；环境不支持时 SKIP。
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("本机建不了目录 junction，跳过这条围栏断言：%v（%s）",
			err, strings.TrimSpace(string(out)))
	}
	resolved, rerr := sandbox.ResolveExistingPath(link)
	if rerr != nil || strings.EqualFold(filepath.Clean(resolved), filepath.Clean(link)) {
		t.Skipf("junction 建了但解析不到目标（resolved=%q err=%v）", resolved, rerr)
	}
}

// get 发一次 GET 并返回状态码与正文。
func get(t *testing.T, h http.Handler, url string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+url, nil))
	return rec.Code, rec.Body.String()
}

// TestServeRefusesJunctionEscape 钉住围栏本身：dist 里的 junction 不许把内容发出去。
func TestServeRefusesJunctionEscape(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := writeDist(t, map[string]string{IndexFile: officialIndex, "app.js": "console.log(1)"})
	link := filepath.Join(dist, "leak")
	makeJunction(t, link, outside)

	front, err := New(Config{Dir: dist})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if code, body := get(t, front, "/leak/secret.txt"); code != http.StatusForbidden {
		t.Fatalf("经 junction 读到 dist 之外必须是 403，实际 %d（body=%q）"+
			"——served 出去就是任意文件读", code, body)
	}

	// 正向控制：dist 内的真资产照旧 200。
	if code, _ := get(t, front, "/app.js"); code != http.StatusOK {
		t.Fatalf("dist 内普通资产必须仍是 200，实际 %d", code)
	}
}

// TestNewResolvesDistThroughJunction dist 自己经 junction 部署时，围栏不许误杀。
//
// 只有 "root 也解析" 才能让 `/app.js` 通过：target 解析成真实路径、root 停在
// junction 拼法的话，前缀比较必然失败 → 每个资产 403（页面全白）。
func TestNewResolvesDistThroughJunction(t *testing.T) {
	realDist := writeDist(t, map[string]string{IndexFile: officialIndex, "app.js": "console.log(1)"})
	base := t.TempDir()
	link := filepath.Join(base, "dist-link")
	makeJunction(t, link, realDist)

	front, err := New(Config{Dir: link})
	if err != nil {
		t.Fatalf("经 junction 的 dist 必须能起来：%v", err)
	}
	if code, body := get(t, front, "/"); code != http.StatusOK || !strings.Contains(body, "id=\"root\"") {
		t.Fatalf("经 junction 的 dist 根必须正常发入口文档，实际 %d（body=%q）", code, body)
	}
	if code, body := get(t, front, "/app.js"); code != http.StatusOK || !strings.Contains(body, "console.log(1)") {
		t.Fatalf("经 junction 的 dist 资产必须正常发，实际 %d（body=%q）", code, body)
	}
}
