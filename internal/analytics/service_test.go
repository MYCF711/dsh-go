package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testEvent(name string, attributes map[string]any) Event {
	raw, _ := json.Marshal(attributes)
	return Event{EventName: name, Timestamp: 1730000000000, Attributes: raw}
}

func TestReportStoresHashChainAndRedacts(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	attributes := map[string]any{
		"model_name":  "deepseek-chat",
		"input_value": `C:\Users\alice\private\secret.txt`,
		"prompt":      "top secret prompt body",
		"apiKey":      "sk-live-0ac1-should-not-store",
		"nested":      map[string]any{"path": "/etc/passwd"},
	}
	for index := 0; index < 100; index++ {
		result, err := service.Report(context.Background(), testEvent("send_button_click", attributes))
		if err != nil {
			t.Fatalf("Report #%d: %v", index, err)
		}
		if result.Stored != 1 || result.Dropped != 0 {
			t.Fatalf("Report #%d = %+v", index, result)
		}
	}
	count, err := service.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if count != 100 {
		t.Fatalf("Verify count = %d", count)
	}
	files, err := listEventFiles(service.Dir())
	if err != nil || len(files) != 1 {
		t.Fatalf("event files = %v, %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 100 {
		t.Fatalf("JSONL 行数 = %d", len(lines))
	}
	for _, forbidden := range []string{"alice", "top secret prompt body", "sk-live-0ac1-should-not-store", "/etc/passwd", `nested`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("本地 JSONL 含禁止内容 %q：%s", forbidden, raw)
		}
	}
	aggregate, err := service.Aggregate()
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if aggregate.Total != 100 || aggregate.ByEvent["send_button_click"] != 100 {
		t.Fatalf("Aggregate = %+v", aggregate)
	}
	records, err := service.Dump(time.Time{})
	if err != nil || len(records) != 100 {
		t.Fatalf("Dump = %d, %v", len(records), err)
	}
	if got := records[0].Attributes["input_value"]; got != "<path>" {
		t.Fatalf("路径未替换为 <path>：%v", got)
	}
	if records[0].PolicyVersion != service.Policy().PolicyVersion {
		t.Fatalf("record policyVersion = %d，policy = %d", records[0].PolicyVersion, service.Policy().PolicyVersion)
	}
}

func TestDisabledAndSamplingDropWithoutWriting(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	policy := service.Policy()
	policy.Enabled = false
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy disabled: %v", err)
	}
	result, err := service.Report(context.Background(), testEvent("desktop_app_launch", nil))
	if err != nil {
		t.Fatalf("Report disabled: %v", err)
	}
	if result.Dropped != 1 || result.Stored != 0 {
		t.Fatalf("disabled result = %+v", result)
	}
	if count, err := service.Verify(); err != nil || count != 0 {
		t.Fatalf("disabled 仍写盘：%d %v", count, err)
	}

	policy = service.Policy()
	policy.Enabled = true
	policy.SamplingRate = 0
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy sampling=0: %v", err)
	}
	result, err = service.Report(context.Background(), testEvent("desktop_app_launch", nil))
	if err != nil {
		t.Fatalf("Report sampling=0: %v", err)
	}
	if result.Dropped != 1 || result.Stored != 0 {
		t.Fatalf("sampling=0 result = %+v", result)
	}
}

func TestWatchPolicyInitialAndUpdates(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := service.WatchPolicy(ctx)
	if err != nil {
		t.Fatalf("WatchPolicy: %v", err)
	}
	if initial := <-ch; initial != true {
		t.Fatalf("initial policy = %v", initial)
	}
	policy := service.Policy()
	policy.Enabled = false
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	select {
	case got := <-ch:
		if got != false {
			t.Fatalf("更新后 policy = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchPolicy 没有推更新帧")
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("取消后 channel 应关闭")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 channel 未关闭")
	}
}

func TestExternalPolicyFileRefresh(t *testing.T) {
	home := t.TempDir()
	service, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	policy := DefaultPolicy()
	policy.Enabled = false
	policy.PolicyVersion = 7
	raw, _ := json.Marshal(policy)
	if err := os.WriteFile(filepath.Join(service.Dir(), policyFileName), raw, 0o644); err != nil {
		t.Fatalf("写 policy: %v", err)
	}
	if service.Enabled() {
		t.Fatal("外部 policy.json 的 enabled=false 没有生效")
	}
	if got := service.Policy().PolicyVersion; got != 7 {
		t.Fatalf("policyVersion = %d", got)
	}
}

func TestLocalModeNeverCallsCollector(t *testing.T) {
	var hits atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	policy := service.Policy()
	policy.Mode = ModeLocal
	policy.CollectorURL = collector.URL // 即使填了，local 也绝不能用
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	for index := 0; index < 5; index++ {
		if _, err := service.Report(context.Background(), testEvent("desktop_app_launch", nil)); err != nil {
			t.Fatalf("Report: %v", err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("local 模式出网了：collector hits=%d", hits.Load())
	}
	if service.OutboundRequests() != 0 {
		t.Fatalf("outbound = %d", service.OutboundRequests())
	}
}

func TestExportConfiguredCollectorReceivesSanitizedAndOfflineQueues(t *testing.T) {
	type captured struct {
		body []byte
	}
	capturedCh := make(chan captured, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		capturedCh <- captured{body: raw}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	policy := service.Policy()
	policy.Mode = ModeLocalExport
	policy.CollectorURL = collector.URL
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	event := testEvent("plugin_install_click", map[string]any{
		"input_value": `C:\Users\alice\plugin.tgz`,
		"apiKey":      "sk-export-leak-0ac1",
	})
	result, err := service.Report(context.Background(), event)
	if err != nil {
		t.Fatalf("Report export: %v", err)
	}
	if result.Stored != 1 || result.Exported != 1 || result.ExportError != "" {
		t.Fatalf("export result = %+v", result)
	}
	select {
	case got := <-capturedCh:
		if strings.Contains(string(got.body), "alice") || strings.Contains(string(got.body), "sk-export-leak-0ac1") {
			t.Fatalf("导出 body 泄漏：%s", got.body)
		}
		if !strings.Contains(string(got.body), `"policyVersion"`) {
			t.Fatalf("导出 body 缺 policyVersion：%s", got.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector 没收到 POST")
	}

	// 断网：collector 指向必然拒绝的端口，记录必须落盘 + 进待发队列。
	offline := service.Policy()
	offline.CollectorURL = "http://127.0.0.1:1/collect"
	if _, err := service.UpdatePolicy(offline); err != nil {
		t.Fatalf("UpdatePolicy offline: %v", err)
	}
	result, err = service.Report(context.Background(), testEvent("desktop_app_launch", nil))
	if err != nil {
		t.Fatalf("Report offline: %v", err)
	}
	if result.Stored != 1 || result.Exported != 0 || result.ExportError == "" {
		t.Fatalf("offline result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(service.Dir(), pendingFileName)); err != nil {
		t.Fatalf("待发队列不存在：%v", err)
	}

	// collector 恢复：下一次 report 会把待发队列一起补发。
	back := service.Policy()
	back.CollectorURL = collector.URL
	if _, err := service.UpdatePolicy(back); err != nil {
		t.Fatalf("UpdatePolicy back: %v", err)
	}
	result, err = service.Report(context.Background(), testEvent("desktop_app_launch", nil))
	if err != nil {
		t.Fatalf("Report back: %v", err)
	}
	if result.Stored != 1 || result.Exported != 1 {
		t.Fatalf("恢复后 result = %+v", result)
	}
	select {
	case got := <-capturedCh:
		// 载荷是 OTLP/HTTP JSON（logs）而不是宿主自有的 ExportBatch：
		// 形状判据从旧的 `"events"` 改成 OTLP 信封，断言更严不更松——
		// 补发的两条记录必须真的在 resourceLogs[0].scopeLogs[0].logRecords 里。
		if !strings.Contains(string(got.body), `"resourceLogs"`) ||
			!strings.Contains(string(got.body), `"scopeLogs"`) ||
			strings.Count(string(got.body), `"eventName":"desktop_app_launch"`) != 2 {
			t.Fatalf("补发 body 形状不对：%s", got.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("补发时 collector 没收到 POST")
	}
	if _, err := os.Stat(filepath.Join(service.Dir(), pendingFileName)); !os.IsNotExist(err) {
		t.Fatalf("补发成功后待发队列应删除：%v", err)
	}
}

func TestVerifyDetectsTamperedChain(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	if _, err := service.Report(context.Background(), testEvent("desktop_app_launch", nil)); err != nil {
		t.Fatalf("Report: %v", err)
	}
	files, err := listEventFiles(service.Dir())
	if err != nil || len(files) != 1 {
		t.Fatalf("event files = %v, %v", files, err)
	}
	file, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := file.Write(append([]byte(`{"eventName":"tampered","timestamp":1,"attributes":{},"policyVersion":1,"receivedAt":"2026-01-01T00:00:00Z","prevHash":"","hash":""}`), byte(10))); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	file.Close()
	if _, err := service.Verify(); err == nil {
		t.Fatal("被篡改的 hash 链没有被发现")
	}
}

func TestSanitizeExportBatchReRedacts(t *testing.T) {
	batch := ExportBatch{
		PolicyVersion: 3,
		Events: []Record{{
			EventName:  "plugin_install_click",
			Timestamp:  1730000000000,
			Attributes: map[string]any{"input_value": "/home/alice/plugin.tgz", "prompt": "body"},
		}},
	}
	sanitized, err := sanitizeExportBatch(batch)
	if err != nil {
		t.Fatalf("sanitizeExportBatch: %v", err)
	}
	raw, _ := json.Marshal(sanitized)
	for _, forbidden := range []string{"alice", "body"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("导出前二次脱敏失败，含 %q：%s", forbidden, raw)
		}
	}
}

func TestPurgeAndVerify(t *testing.T) {
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	for index := 0; index < 3; index++ {
		if _, err := service.Report(context.Background(), testEvent("desktop_app_launch", nil)); err != nil {
			t.Fatalf("Report: %v", err)
		}
	}
	if count, err := service.Verify(); err != nil || count != 3 {
		t.Fatalf("Verify before purge = %d, %v", count, err)
	}
	purged, err := service.Purge()
	if err != nil || purged != 3 {
		t.Fatalf("Purge = %d, %v", purged, err)
	}
	entries, err := os.ReadDir(service.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("purge 后目录不空：%v", entries)
	}
	if count, err := service.Verify(); err != nil || count != 0 {
		t.Fatalf("Verify after purge = %d, %v", count, err)
	}
	if !service.Enabled() {
		t.Fatal("purge 后策略应回到 enabled=true")
	}
}
