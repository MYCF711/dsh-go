package main

// 官方 rc.2 `PromptContentPart`（file/receiptId、image/mediaType+data）在
// `session.prompt` 上的真实绑定测试。
//
// 为什么必须有端到端：这两支形状的**唯一**作用是"让附件到得了模型"。
// 只测 NormalizeOfficialContent 的函数返回，证明不了 HTTP 入口解出了新字段、
// 也证明不了后续 ResolveBlocks/模型 parts 会把它当附件。所以这里真的：
//   1. 走一次 `fileUploads.upload` 拿回执（真实落盘）；
//   2. 真的 POST 一次带附件的 `session.prompt`（真 server + 真 cookie）；
//   3. 用产品自己的 model parts 构造点把解析结果翻成模型输入，断言图片段/文本段。

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/transport"
)

// officialPromptHarness 是"真 server + 真附件 store"的最小夹具。
type officialPromptHarness struct {
	base     string
	client   *http.Client
	store    *attachmentStore
	resolved []transport.ContentBlock
	parts    []llm.MessagePart
	lastBody string
}

func newOfficialPromptHarness(t *testing.T, home string) *officialPromptHarness {
	t.Helper()
	h := &officialPromptHarness{store: newAttachmentStore(home)}
	handle := func(sessionID string, blocks []transport.ContentBlock, _ string) (transport.PromptResult, error) {
		normalized, err := h.store.NormalizeOfficialContent(sessionID, blocks)
		if err != nil {
			return transport.PromptResult{}, err
		}
		resolved, err := h.store.ResolveBlocks(normalized)
		if err != nil {
			return transport.PromptResult{}, err
		}
		_, parts, err := promptPartsFromBlocks(home, resolved)
		if err != nil {
			return transport.PromptResult{}, err
		}
		h.resolved, h.parts = resolved, parts
		return transport.PromptResult{TurnID: "turn-official-attachment", Accepted: true}, nil
	}
	srv, err := transport.NewServer(transport.Options{
		// 两个入口都接：prompt 的 placement 决定走哪一个（生产 serve 同样两个都接）。
		PromptBlocks: func(sessionID string, blocks []transport.ContentBlock) (transport.PromptResult, error) {
			return handle(sessionID, blocks, string(transport.PlacementTurn))
		},
		PromptBlocksPlacement: handle,
	})
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		t.Fatalf("起 server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("connect status=%d", resp.StatusCode)
	}
	h.base, h.client = base, client
	return h
}

// promptBlocks 发一次官方形状的 session.prompt，返回 HTTP 状态码（响应体留在 lastBody）。
func (h *officialPromptHarness) promptBlocks(t *testing.T, sessionID string, content []map[string]any) int {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"requestId": "req-" + sessionID, "sessionId": sessionID, "mode": "queue", "content": content,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Post(h.base+"/api/session.prompt", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST session.prompt: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	h.lastBody = string(body)
	return resp.StatusCode
}

func TestSessionPromptBindsOfficialFileReceiptToAttachment(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	h := newOfficialPromptHarness(t, f.home)

	imagePath := filepath.Join(t.TempDir(), "shot.png")
	writeImageFixture(t, imagePath, "png", 3, 2)
	imageBytes := mustReadFile(t, imagePath)
	upload, err := f.api.FileUploadsUpload(sessionID, transport.OfficialFileUploadRequest{
		Data: base64.StdEncoding.EncodeToString(imageBytes), Name: "../shot.png",
	})
	if err != nil {
		t.Fatalf("fileUploads.upload: %v", err)
	}

	if code := h.promptBlocks(t, sessionID, []map[string]any{
		{"type": "text", "text": "看这张图"},
		{"type": "file", "receiptId": upload.ReceiptID},
	}); code != http.StatusOK {
		t.Fatalf("带回执的 prompt HTTP=%d body=%s（回执没被解析成附件）", code, h.lastBody)
	}
	if len(h.resolved) != 2 || h.resolved[1].Type != "attachment" {
		t.Fatalf("解析结果不是附件块：%+v", h.resolved)
	}
	if !strings.HasPrefix(h.resolved[1].AttachmentID, "att-") || h.resolved[1].MIME != "image/png" ||
		h.resolved[1].ByteSize != int64(len(imageBytes)) || h.resolved[1].Name != "shot.png" {
		t.Fatalf("附件真值不对：%+v", h.resolved[1])
	}
	// 真的到模型：第二段必须是图片段，data URL 里是这张图的真实字节。
	if len(h.parts) != 2 || h.parts[1].Type != llm.PartImageURL {
		t.Fatalf("模型 parts 里没有图片段：%+v", h.parts)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	if h.parts[1].URL != want {
		t.Fatalf("模型输入里的图片字节不是原图：%q", h.parts[1].URL)
	}
}

func TestSessionPromptBindsInlineImagePart(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	h := newOfficialPromptHarness(t, f.home)

	imagePath := filepath.Join(t.TempDir(), "inline.png")
	writeImageFixture(t, imagePath, "png", 2, 2)
	imageBytes := mustReadFile(t, imagePath)

	if code := h.promptBlocks(t, sessionID, []map[string]any{{
		"type": "image", "mediaType": "image/png",
		"data": base64.StdEncoding.EncodeToString(imageBytes), "name": "inline.png",
	}}); code != http.StatusOK {
		t.Fatalf("内联 image 块 HTTP=%d body=%s", code, h.lastBody)
	}
	if len(h.resolved) != 1 || h.resolved[0].MIME != "image/png" {
		t.Fatalf("内联图片没落成附件：%+v", h.resolved)
	}
	if len(h.parts) != 1 || h.parts[0].Type != llm.PartImageURL {
		t.Fatalf("内联图片没进模型 parts：%+v", h.parts)
	}
}

func TestSessionPromptBindsTextReceiptAsText(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	h := newOfficialPromptHarness(t, f.home)

	upload, err := f.api.FileUploadsUpload(sessionID, transport.OfficialFileUploadRequest{
		Data: base64.StdEncoding.EncodeToString([]byte("第一行\n第二行\n")), Name: "note.txt",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if code := h.promptBlocks(t, sessionID, []map[string]any{
		{"type": "file", "receiptId": upload.ReceiptID},
	}); code != http.StatusOK {
		t.Fatalf("文本回执 HTTP=%d body=%s", code, h.lastBody)
	}
	if len(h.parts) != 1 || h.parts[0].Type != llm.PartText {
		t.Fatalf("文本附件没落成文本段：%+v", h.parts)
	}
	if !strings.Contains(h.parts[0].Text, "第一行\n第二行\n") {
		t.Fatalf("文本附件正文没进模型输入：%q", h.parts[0].Text)
	}
}

func TestSessionPromptRejectsBadOfficialAttachmentParts(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	other := f.newSession("", nil)
	h := newOfficialPromptHarness(t, f.home)

	imagePath := filepath.Join(t.TempDir(), "shot.png")
	writeImageFixture(t, imagePath, "png", 2, 2)
	imageBytes := mustReadFile(t, imagePath)
	upload, err := f.api.FileUploadsUpload(sessionID, transport.OfficialFileUploadRequest{
		Data: base64.StdEncoding.EncodeToString(imageBytes), Name: "shot.png",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	cases := []struct {
		name    string
		target  string
		content []map[string]any
		want    int
	}{
		{"回执形状非法", sessionID, []map[string]any{{"type": "file", "receiptId": "../../etc/passwd"}}, http.StatusBadRequest},
		{"回执不存在", sessionID, []map[string]any{{"type": "file", "receiptId": "rcpt-00000000-0000-0000-0000-000000000000"}}, http.StatusNotFound},
		{"回执属于别的会话", other, []map[string]any{{"type": "file", "receiptId": upload.ReceiptID}}, http.StatusNotFound},
		{"mediaType 不在官方枚举", sessionID, []map[string]any{{"type": "image", "mediaType": "image/bmp",
			"data": base64.StdEncoding.EncodeToString(imageBytes)}}, http.StatusBadRequest},
		{"mediaType 与真实字节不符", sessionID, []map[string]any{{"type": "image", "mediaType": "image/jpeg",
			"data": base64.StdEncoding.EncodeToString(imageBytes)}}, http.StatusBadRequest},
		{"image 缺 data", sessionID, []map[string]any{{"type": "image", "mediaType": "image/png"}}, http.StatusBadRequest},
		{"image data 非 base64", sessionID, []map[string]any{{"type": "image", "mediaType": "image/png", "data": "!!!"}}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := h.promptBlocks(t, tc.target, tc.content); code != tc.want {
				t.Fatalf("HTTP=%d want=%d（%s）body=%s", code, tc.want, tc.name, h.lastBody)
			}
		})
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return data
}
