package main

// 官方插件 80 的 `request-error → retry` 编排端到端。
//
// 这一份必须证明三件事，缺一都是没接线：
//  1. 超预算的请求**自动降级**并重试一次，第二次请求真的少了图，而且成功了；
//  2. 只降级一次：一直超预算时只发两次请求，然后把失败原样报出去（防抖）；
//  3. 降级决策落成一条真实 `image/offload` 事件，指向本轮 user/message 的 seq，
//     而 durable 块**一个字节都没改**（v4 append-only）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// imageWireProbe 记录每次模型请求里的图片张数（按线上 JSON 数 image_url 段）。
type imageWireProbe struct {
	mu     sync.Mutex
	shots  []int
	bodies []string
}

func (p *imageWireProbe) record(n int, body string) {
	p.mu.Lock()
	p.shots = append(p.shots, n)
	p.bodies = append(p.bodies, body)
	p.mu.Unlock()
}

func (p *imageWireProbe) images() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.shots...)
}

// countImageParts 数一次请求里有多少张图（content 数组里 type=image_url 的段）。
func countImageParts(t *testing.T, body []byte) int {
	t.Helper()
	var wire struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("解码请求体: %v", err)
	}
	total := 0
	for _, message := range wire.Messages {
		if len(message.Content) == 0 {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "image_url" {
				total++
			}
		}
	}
	return total
}

// offloadGateway 造一个"第一次超预算、之后正常"的网关。
//
// firstBody 是第一次请求回的 400 失败体；attempts 是总请求次数上限（超过就报错，
// 用来看"只降级一次"有没有变成无限重试）。
func offloadGateway(t *testing.T, firstBody string, alwaysFail bool) (base string, probe *imageWireProbe, calls *atomic.Int32) {
	t.Helper()
	probe = &imageWireProbe{}
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw strings.Builder
		data := make([]byte, 0, 1<<16)
		buf := make([]byte, 1<<16)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
		_ = raw
		n := count.Add(1)
		probe.record(countImageParts(t, data), string(data))
		if alwaysFail || n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, firstBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"降级后成功\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", probe, &count
}

// threeImagePrompt 造一条带三张真图片的 prompt（附件 store 里是真 PNG 字节）。
func threeImagePrompt(t *testing.T, home string) []transport.ContentBlock {
	t.Helper()
	root := t.TempDir()
	store := newAttachmentStore(home)
	blocks := []transport.ContentBlock{{Type: "text", Text: "看这三张图"}}
	for i := 0; i < 3; i++ {
		path := filepath.Join(root, fmt.Sprintf("shot-%d.png", i))
		writeImageFixture(t, path, "png", 3+i, 2+i)
		put, err := store.Put(path)
		if err != nil {
			t.Fatalf("Put(%s): %v", path, err)
		}
		if !strings.HasPrefix(put.ID, "att-") {
			t.Fatalf("附件 id 形状不对：%q", put.ID)
		}
		blocks = append(blocks, transport.ContentBlock{Type: "attachment", AttachmentID: put.ID})
	}
	return blocks
}

// 超预算 → 自动降级一次 → 第二次请求真的少两张图 → 本轮成功。
func TestImageOffloadRetryDegradesAndSucceeds(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	path := createModelWireSession(t, home, cwd, "s-offload")
	blocks := threeImagePrompt(t, home)

	base, probe, calls := offloadGateway(t,
		`{"error":{"code":"IMAGE_OFFLOAD_REQUIRED","message":"request has too many images","offloadImages":2}}`, false)
	runner := &modelRunner{home: home, base: base, model: "default-model"}
	if err := runner.RunContent(context.Background(), "s-offload", "turn-1", blocks); err != nil {
		t.Fatalf("降级之后本轮必须成功，实际 %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("必须只重试一次：请求次数 = %d", got)
	}
	images := probe.images()
	if len(images) != 2 {
		t.Fatalf("请求次数 = %d", len(images))
	}
	if images[0] != 3 {
		t.Fatalf("第一次请求必须带 3 张图，实际 %d", images[0])
	}
	if images[1] != 1 {
		t.Fatalf("第二次请求必须少掉降级的那 2 张（3-2=1），实际 %d", images[1])
	}

	// 决策落盘：image/offload 指向本轮 user/message 的 seq，序号是深度优先图片序。
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话日志：%v", err)
	}
	var userSeq int64 = -1
	var offloads []session.ImageOffloadTarget
	for _, ev := range log.Events {
		switch ev.Type {
		case "user/message":
			if userSeq < 0 {
				userSeq = ev.Seq
			}
		case session.ImageOffloadEventType:
			targets, err := session.ParseImageOffloadTargets(ev.Data)
			if err != nil {
				t.Fatalf("image/offload 事件不合法：%v", err)
			}
			offloads = append(offloads, targets...)
		}
	}
	if userSeq < 0 {
		t.Fatal("日志里没有 user/message")
	}
	if len(offloads) != 1 {
		t.Fatalf("必须恰好一条 image/offload 目标（只降级一次）：%+v", offloads)
	}
	if offloads[0].Seq != userSeq {
		t.Fatalf("image/offload 必须指向本轮 user/message 的 seq=%d，实际 %d", userSeq, offloads[0].Seq)
	}
	if len(offloads[0].ImageIndexes) != 2 || offloads[0].ImageIndexes[0] != 0 || offloads[0].ImageIndexes[1] != 1 {
		t.Fatalf("降级的必须是最旧的两张（序号 0,1）：%v", offloads[0].ImageIndexes)
	}

	// durable 块一个字节都没改：user/message 仍是 4 个块（文本 + 3 张附件）。
	var durableBlocks int
	for _, ev := range log.Events {
		if ev.Type != "user/message" || ev.Seq != userSeq {
			continue
		}
		var data struct {
			Content []map[string]any `json:"content"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("解码 user/message：%v", err)
		}
		durableBlocks = len(data.Content)
	}
	if durableBlocks != 4 {
		t.Fatalf("durable 块必须原样保留 4 块（v4 append-only），实际 %d", durableBlocks)
	}

	// 重新 Surface：最旧两张已是占位文本，第三张还在，且占位里带附件 id。
	surface := session.Surface(log)
	node := -1
	for i := range surface {
		if surface[i].Seq == userSeq {
			node = i
			break
		}
	}
	if node < 0 {
		t.Fatal("surface 里找不到本轮用户消息")
	}
	if len(surface[node].Images) != 3 {
		t.Fatalf("surface 应该仍有 3 个图片出现位置：%d", len(surface[node].Images))
	}
	if !surface[node].Images[0].Offloaded || !surface[node].Images[1].Offloaded {
		t.Fatalf("最旧两张必须被标记为已退役：%+v", surface[node].Images)
	}
	if surface[node].Images[2].Offloaded {
		t.Fatalf("第三张不该被退役：%+v", surface[node].Images[2])
	}
	if id := surface[node].Images[0].Block.AttachmentID; id == "" || !strings.Contains(surface[node].Message.Content, id) {
		t.Fatalf("占位文本必须带附件 id 供模型取回原件：content=%q", surface[node].Message.Content)
	}
}

// 一直超预算：只降级一次、只发两次请求，失败如实上报（不静默成功）。
func TestImageOffloadRetryOnlyDegradesOnce(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	createModelWireSession(t, home, cwd, "s-debounce")
	blocks := threeImagePrompt(t, home)

	base, _, calls := offloadGateway(t,
		`{"error":{"code":"IMAGE_OFFLOAD_REQUIRED","message":"still too many images","offloadImages":1}}`, true)
	runner := &modelRunner{home: home, base: base, model: "default-model"}
	err := runner.RunContent(context.Background(), "s-debounce", "turn-1", blocks)
	if err == nil {
		t.Fatal("第二次仍超预算时必须如实报错，不许静默成功")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("防抖：只允许降级一次（共 2 次请求），实际 %d 次", got)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("错误必须保留上游 400 的真实信息：%v", err)
	}
}

// 判据表：只有"4xx + 图片预算"才触发降级；坏图/鉴权/5xx 一律不碰。
func TestOffloadImagesForErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		ok   bool
	}{
		{"official-code-with-count", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
			Body: `{"error":{"code":"IMAGE_OFFLOAD_REQUIRED","offloadImages":3}}`}, 3, true},
		{"official-code-no-count", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 413,
			Body: `{"error":{"code":"IMAGE_OFFLOAD_REQUIRED"}}`}, 0, true},
		{"plain-text-code", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
			Body: "IMAGE_OFFLOAD_REQUIRED: shrink the request"}, 0, true},
		{"image-budget-wording", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
			Body: "request too large: too many images in messages"}, 0, true},
		{"payload-wording", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 413,
			Body: "Request Entity Too Large (image payload)"}, 0, true},
		{"bad-image-format", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
			Body: "invalid image format for image_url"}, 0, false},
		{"server-error", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 500,
			Body: "images too large"}, 0, false},
		{"unauthorized", &llm.GatewayError{Kind: llm.GatewayStatus, Code: 401, Body: "bad key"}, 0, false},
		{"connect", &llm.GatewayError{Kind: llm.GatewayConnect, Body: "images too large"}, 0, false},
		{"plain-error", fmt.Errorf("boom"), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := offloadImagesForError(tc.err)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("offloadImagesForError = (%d,%v)，要 (%d,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// 降级函数本身：最旧 N 张换占位文本、durable 块不动、张数被夹紧。
func TestDegradePromptImagesReplacesOldestOnly(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	store := newAttachmentStore(home)
	transportBlocks := []transport.ContentBlock{{Type: "text", Text: "before"}}
	for i := 0; i < 3; i++ {
		path := filepath.Join(root, fmt.Sprintf("d-%d.png", i))
		writeImageFixture(t, path, "png", 3, 2)
		put, err := store.Put(path)
		if err != nil {
			t.Fatal(err)
		}
		transportBlocks = append(transportBlocks, transport.ContentBlock{Type: "attachment", AttachmentID: put.ID})
	}
	initial, err := modelInputFromBlocks(home, transportBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(initial.durable); got != 4 {
		t.Fatalf("durable 块数 = %d", got)
	}

	// 张数超过图片数：夹紧到 3，不许越界 panic。
	degraded, indexes := degradePromptImages(initial, 99, nil)
	if len(indexes) != 3 {
		t.Fatalf("必须降级全部 3 张（夹紧），实际 %v", indexes)
	}
	imageParts, textParts := 0, 0
	for _, part := range degraded.message.Parts {
		switch part.Type {
		case llm.PartImageURL:
			imageParts++
		case llm.PartText:
			textParts++
		}
	}
	if imageParts != 0 {
		t.Fatalf("全部降级后不该再有 image 段：%d", imageParts)
	}
	if textParts != 4 { // before + 三条占位
		t.Fatalf("占位文本段数 = %d，要 4（原文本 + 三条占位）", textParts)
	}
	for i, block := range initial.durable {
		if !session.IsImageBlock(block) {
			continue
		}
		want := session.OffloadedImageText(block, nil)
		found := false
		for _, part := range degraded.message.Parts {
			if part.Type == llm.PartText && part.Text == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("第 %d 张的占位文本必须是官方文案（带附件 id）：%q", i, want)
		}
	}
	// 传入的 durable 不被就地改（读路径不写盘、也不该被写路径偷改）。
	if strings.Contains(initial.message.Parts[1].URL, "offload") {
		t.Fatal("原始 input 不许被就地改")
	}
}

// 防抖开关只用一个 turn：第二个 turn 仍可再降级一次（不是永久关闭）。
func TestOffloadRetrierIsPerTurn(t *testing.T) {
	var retrier offloadRetrier
	if retrier.used {
		t.Fatal("新 retrier 不该已用掉")
	}
	retrier.used = true
	if _, _, ok := retrier.tryOffloadRetry(&llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
		Body: `{"error":{"code":"IMAGE_OFFLOAD_REQUIRED","offloadImages":1}}`}, modelTurnInput{}, nil, -1); ok {
		t.Fatal("用掉的 retrier 不许再降级")
	}
}

// TestImageOffloadPlaceholderPointsAtReadableOriginal 是"重放能找到原图"这一条。
//
// 官方 `resolveImageAttachmentAccess`（dsh-llm/lib/types/content.js:13-19）与
// `offloadedImageText`（同文件 :73-79）的契约：退役后的占位**必须**带上归一化副本的
// 只读路径，模型靠它 `read` 原件、再拷到可写路径继续编辑。只给附件 id 不够——
// 模型没有"按 id 取附件"的工具，那句 "ask the user to attach it again" 等于
// 告诉用户"你的图没了，重发一次"。
//
// 本宿主的原件落在 `<home>/state/attachments/objects/<id>/blob`，所以这条测试
// 不看文案长得像不像，而是**真的按占位里的路径把原始字节读回来**，并核对
// durable 块记的 sha256——重放（Surface）内容与 durable 块必须指到同一份字节。
func TestImageOffloadPlaceholderPointsAtReadableOriginal(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	path := createModelWireSession(t, home, cwd, "s-offload-access")
	blocks := threeImagePrompt(t, home)

	base, _, calls := offloadGateway(t,
		`{"error":{"code":"IMAGE_OFFLOAD_REQUIRED","message":"request has too many images","offloadImages":2}}`, false)
	runner := &modelRunner{home: home, base: base, model: "default-model"}
	if err := runner.RunContent(context.Background(), "s-offload-access", "turn-1", blocks); err != nil {
		t.Fatalf("降级之后本轮必须成功，实际 %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("必须只重试一次：请求次数 = %d", got)
	}

	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话日志：%v", err)
	}
	surface := session.Surface(log)
	node := -1
	for i := range surface {
		if surface[i].Type == "user/message" {
			node = i
			break
		}
	}
	if node < 0 {
		t.Fatal("surface 里没有本轮 user/message")
	}
	if len(surface[node].Images) != 3 {
		t.Fatalf("图片出现位置数 = %d，期望 3", len(surface[node].Images))
	}
	offloaded := surface[node].Images[0]
	if !offloaded.Offloaded {
		t.Fatalf("最旧的一张必须已退役：%+v", surface[node].Images)
	}

	// 占位文案里必须出现 JSON 引号形式的只读路径（官方 quoted()：JSON.stringify）。
	blobPath := filepath.Join(home, "state", "attachments", "objects", offloaded.Block.AttachmentID, "blob")
	quotedPath, err := json.Marshal(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	content := surface[node].Message.Content
	if strings.Contains(content, "No local normalized image path is available") {
		t.Fatalf("重放拿不到原图路径（官方有 resolveImageAttachmentAccess）：%q", content)
	}
	if !strings.Contains(content, string(quotedPath)) {
		t.Fatalf("占位里没有附件只读路径 %s：%q", blobPath, content)
	}

	// 按占位里的路径真的把原始字节读回来，并与 durable 块的 sha256 对齐。
	original, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("按占位路径读不到原图 %s：%v", blobPath, err)
	}
	digest := sha256.Sum256(original)
	if got := hex.EncodeToString(digest[:]); got != offloaded.Block.SHA256 {
		t.Fatalf("路径指向的字节不是原图：sha256=%s，durable 块=%s", got, offloaded.Block.SHA256)
	}
	if offloaded.Block.SHA256 == "" || len(original) == 0 {
		t.Fatal("原图字节为空或 durable 块没有 sha256")
	}
}

// TestOffloadRetrierRequestSideCarriesReadonlyPath：请求侧（降级重试那一次）
// 与重放侧（`session.Surface`）必须用同一份只读副本解析器，否则同一张图在
// "这一次重试"与"下一轮历史"里是两段互相矛盾的说明（一处说没有路径、请用户重发，
// 一处给了能 read 的路径）。
func TestOffloadRetrierRequestSideCarriesReadonlyPath(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	path := createModelWireSession(t, home, cwd, "s-offload-request")
	blocks := threeImagePrompt(t, home)

	initial, err := modelInputFromBlocks(home, blocks)
	if err != nil {
		t.Fatal(err)
	}
	retrier := &offloadRetrier{access: session.SessionImageAccessAt(path)}
	history := []llm.Message{initial.message}
	degraded, indexes, ok := retrier.tryOffloadRetry(&llm.GatewayError{
		Kind: llm.GatewayStatus, Code: 400,
		Body: `{"error":{"code":"IMAGE_OFFLOAD_REQUIRED","offloadImages":2}}`,
	}, initial, history, 0)
	if !ok {
		t.Fatal("官方 code + 张数必须触发一次降级重试")
	}
	if len(indexes) != 2 {
		t.Fatalf("降级张数 = %d，期望 2", len(indexes))
	}
	// 历史里的那条也必须被换成降级后的消息（重试请求真的少了图）。
	if len(history[0].Parts) != len(degraded.message.Parts) {
		t.Fatal("history 没有被替换成降级后的消息")
	}

	durable := make([]session.ContentBlock, 0, 3)
	for _, block := range initial.durable {
		if session.IsImageBlock(block) {
			durable = append(durable, block)
		}
	}
	var placeholders []string
	for _, part := range degraded.message.Parts {
		if part.Type == llm.PartText && strings.Contains(part.Text, "image omitted to fit request image limits") {
			placeholders = append(placeholders, part.Text)
		}
	}
	if len(placeholders) != 2 {
		t.Fatalf("请求侧占位数 = %d，期望 2", len(placeholders))
	}
	for i, text := range placeholders {
		blobPath := filepath.Join(home, "state", "attachments", "objects", durable[i].AttachmentID, "blob")
		quotedPath, err := json.Marshal(blobPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "No local normalized image path is available") {
			t.Fatalf("第 %d 张的占位丢了只读路径：%q", i, text)
		}
		if !strings.Contains(text, string(quotedPath)) {
			t.Fatalf("第 %d 张的占位没有只读路径 %s：%q", i, blobPath, text)
		}
		// 逐字节等于重放侧会产出的文案（同一个 OffloadedImageText + 同一个解析器）。
		want := session.OffloadedImageText(durable[i], &session.ImageAccess{ReadonlyPath: blobPath})
		if text != want {
			t.Fatalf("第 %d 张请求侧文案 = %q / 重放侧文案 = %q", i, text, want)
		}
	}
	if len(indexes) != len(placeholders) {
		t.Fatalf("降级序号数 = %d，占位数 = %d", len(indexes), len(placeholders))
	}
}
