package official_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/pluginmgr"
)

// tarPackage 把 packageFixture 的目录打成 npm tarball（根 package/）。
func tarPackage(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := "package/" + filepath.ToSlash(rel)
		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o755, Typeflag: tar.TypeDir})
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRegistryInspectInstallUpdateRollbackRealHTTP(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	f := newFixture(t, "")
	v1 := packageFixture(t, filepath.Join(f.home, "private", "v1"), "acme.registry", "1.0.0", nil)
	v2 := packageFixture(t, filepath.Join(f.home, "private", "v2"), "acme.registry", "2.0.0", nil)
	tar1, tar2 := tarPackage(t, v1), tarPackage(t, v2)
	sum1, sum2 := sha1.Sum(tar1), sha1.Sum(tar2)

	mux := http.NewServeMux()
	mux.HandleFunc("/acme.registry", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":      "acme.registry",
			"dist-tags": map[string]string{"latest": "2.0.0"},
			"versions": map[string]any{
				"1.0.0": map[string]any{"version": "1.0.0", "dist": map[string]any{
					"tarball": "http://" + r.Host + "/acme.registry/-/acme.registry-1.0.0.tgz",
					"shasum":  hex.EncodeToString(sum1[:]),
				}},
				"2.0.0": map[string]any{"version": "2.0.0", "dist": map[string]any{
					"tarball": "http://" + r.Host + "/acme.registry/-/acme.registry-2.0.0.tgz",
					"shasum":  hex.EncodeToString(sum2[:]),
				}},
			},
		})
	})
	mux.HandleFunc("/acme.registry/-/acme.registry-1.0.0.tgz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tar1) })
	mux.HandleFunc("/acme.registry/-/acme.registry-2.0.0.tgz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tar2) })
	registry := httptest.NewServer(mux)
	defer registry.Close()

	inspect := f.result(t, "pluginManager.inspect", map[string]any{
		"spec": "acme.registry@1.0.0", "options": map[string]any{"registry": registry.URL},
	})
	if inspect["status"] != "accepted" || inspect["name"] != "acme.registry" || inspect["version"] != "1.0.0" || inspect["kind"] != "registry" {
		t.Fatalf("registry inspect 结果不对：%v", inspect)
	}

	installed := f.result(t, "pluginManager.installBundle", map[string]any{
		"spec": "acme.registry@1.0.0", "options": map[string]any{"registry": registry.URL},
	})
	if installed["application"] != "applied" || installed["bundle"] != "acme.registry" || installed["version"] != "1.0.0" {
		t.Fatalf("registry install 结果不对：%v", installed)
	}
	if info := installedIDs(t, f.manager)["acme.registry"]; info.Version != "1.0.0" {
		t.Fatalf("安装没落到真实状态：%+v", info)
	}

	updated := f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.registry", "version": "2.0.0", "options": map[string]any{"registry": registry.URL},
	})
	if updated["from"] != "1.0.0" || updated["to"] != "2.0.0" || updated["step"] != "install" {
		t.Fatalf("registry update 三段式不对：%v", updated)
	}
	if info := installedIDs(t, f.manager)["acme.registry"]; info.Version != "2.0.0" {
		t.Fatalf("更新没落到真实状态：%+v", info)
	}
	// 回滚到 v1：真实 manager.restore，不是假版本。
	if _, err := f.manager.Restore("acme.registry", "1.0.0"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if info := installedIDs(t, f.manager)["acme.registry"]; info.Version != "1.0.0" {
		t.Fatalf("回滚没落到真实状态：%+v", info)
	}

	// 不存在的版本：inspect refused / install failed，状态不变。
	missing := f.result(t, "pluginManager.inspect", map[string]any{
		"spec": "acme.registry@9.9.9", "options": map[string]any{"registry": registry.URL},
	})
	if missing["status"] != "refused" || missing["problem"] != "not-a-package" {
		t.Fatalf("缺失版本 inspect 必须 refused：%v", missing)
	}
	failed := f.result(t, "pluginManager.installBundle", map[string]any{
		"spec": "acme.registry@9.9.9", "options": map[string]any{"registry": registry.URL},
	})
	if failed["application"] != "failed" {
		t.Fatalf("缺失版本 install 必须 failed：%v", failed)
	}
	if info := installedIDs(t, f.manager)["acme.registry"]; info.Version != "1.0.0" {
		t.Fatalf("失败的安装改了状态：%+v", info)
	}
}

func TestTarballInspectAndInstallRealHTTP(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	f := newFixture(t, "")
	pkg := packageFixture(t, filepath.Join(f.home, "private", "tar"), "acme.tarball", "1.2.3", nil)
	raw := tarPackage(t, pkg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer srv.Close()

	inspect := f.result(t, "pluginManager.inspect", map[string]any{"spec": srv.URL + "/pkg.tgz"})
	if inspect["status"] != "accepted" || inspect["name"] != "acme.tarball" || inspect["kind"] != "tarball" {
		t.Fatalf("tarball inspect 结果不对：%v", inspect)
	}
	installed := f.result(t, "pluginManager.installBundle", map[string]any{"spec": srv.URL + "/pkg.tgz"})
	if installed["application"] != "applied" || installed["bundle"] != "acme.tarball" {
		t.Fatalf("tarball install 结果不对：%v", installed)
	}
	if info := installedIDs(t, f.manager)["acme.tarball"]; info.Version != "1.2.3" {
		t.Fatalf("tarball 安装没落到真实状态：%+v", info)
	}
	_ = pluginmgr.SourceKind // keep import if assertions change
}
