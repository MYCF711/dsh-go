package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
)

// doctor 的每一项都必须给出**实测到的东西**，不是"正常/异常"。
// 出问题时人最需要知道"到底哪里、什么值"。
func TestDoctorReportsConcreteDetail(t *testing.T) {
	home := t.TempDir()
	rep := runDoctor(home, "http://127.0.0.1:1/v1")

	if len(rep.Results) < 5 {
		t.Fatalf("应该至少 5 项检查，实际 %d 项", len(rep.Results))
	}
	for _, c := range rep.Results {
		if strings.TrimSpace(c.Detail) == "" {
			t.Fatalf("%s 没给出实测细节", c.Name)
		}
		// 失败必须带下一步
		if !c.OK && !c.Warn && c.Next == "" {
			t.Fatalf("%s 失败了却没给下一步", c.Name)
		}
	}

	var buf bytes.Buffer
	rep.Render(&buf)
	out := buf.String()
	if !strings.Contains(out, "dshd doctor") {
		t.Fatalf("报告要有标题: %q", out)
	}
	// 网关不通时报告里要有 ✗ 和那条 base
	if !strings.Contains(out, "✗") || !strings.Contains(out, "127.0.0.1:1") {
		t.Fatalf("网关不通要在报告里可见: %q", out)
	}
	if !rep.Failed() {
		t.Fatal("网关不通是致命项")
	}
}

func TestDoctorHomeIsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := checkHomeWritable(f)
	if c.OK {
		t.Fatal("home 是文件时必须报错")
	}
	if !strings.Contains(c.Detail, "文件") || c.Next == "" {
		t.Fatalf("要说清是文件并给下一步: %+v", c)
	}
}

// 尾部半帧是**警告**不是错误：前面的内容照常可用。
func TestDoctorTornSessionIsWarning(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "--p--", "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 造一份真实会话再砍掉尾巴
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	w, err := v4.Create(path, v4.Header{Type: "session", Version: 4, ID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(v4.Event{Type: "user/message", Data: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:len(b)-6], 0o644); err != nil {
		t.Fatal(err)
	}

	c := checkSessionsReadable(home)
	if !c.OK || !c.Warn {
		t.Fatalf("尾部半帧应该是「可读但有警告」，实际 %+v", c)
	}
	if !strings.Contains(c.Detail, "半帧") {
		t.Fatalf("要说清是什么问题: %q", c.Detail)
	}
}

// 网关返回 500 时，报告里要出现状态码。
func TestDoctorGatewayStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := checkGateway(srv.URL + "/v1")
	if c.OK {
		t.Fatal("500 必须报错")
	}
	if !strings.Contains(c.Detail, "500") {
		t.Fatalf("要报出状态码: %q", c.Detail)
	}
}

// 网关正常时，报告里要出现模型数——那是"真的连上了"的证据。
func TestDoctorGatewayOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	defer srv.Close()

	c := checkGateway(srv.URL + "/v1")
	if !c.OK {
		t.Fatalf("应该连上: %+v", c)
	}
	if !strings.Contains(c.Detail, "2 个模型") {
		t.Fatalf("要报出模型数: %q", c.Detail)
	}
}
