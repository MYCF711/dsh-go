package main

// 官方 rc.2 `PromptContentPart` 的两支非 attachment 形状 → 宿主 attachment 块。
//
// 官方契约（随包 src 原件，`@deepseek-ai/dsh-api-session-controller/src/types.ts:80-92`）：
//
//	export type PromptContentPart =
//	  | { type: 'text'; text: string }
//	  | { type: 'image'; mediaType: ImageMediaType; data: string; name?: string }
//	  | { type: 'file'; receiptId: Branded<'file-upload-receipt-id'> }
//
// 也就是说：官方客户端**不认识**宿主的 `att-…` id。它先 `fileUploads.upload`
// 拿到回执（`{receiptId, file:{attachmentId,name,bytes}}`），然后把这些 part
// 塞进 `session.prompt` 的 `content` 里；宿主负责在准入时把回执/内联图片解析成
// 真实附件（官方是 `ctx.attachments.admitPromptContent` + `fileUploads.resolve`）。
//
// 本宿主此前的 `session.prompt` 只认 `{type:'attachment', attachmentId}`，
// 于是官方形状的附件**到不了模型**（400 未知块类型）。这个文件补的就是这条绑定：
// 解析出的每个块都是宿主自己的 attachment 块，后续 ResolveBlocks / 模型 parts
// 与旧路径完全同一条，不产生第二套内容形状。
//
// 边界（故意不做的）：
//   - 不信任回执里的名称/哈希/大小：一律重新读 blob、重新嗅探、重新算哈希；
//   - 回执是**会话作用域**的（官方回执只在接收它的 Agent 里有效），
//     跨会话使用按 not_found 拒绝，不静默当成本会话的；
//   - mediaType 只是声明：与真实字节不一致就 400，不让模型看到"标着 png 的 exe"。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dshgo/internal/transport"
)

// uploadReceiptIDPattern 是回执 id 的形状：`rcpt-` + 宿主 serveID() 的 uuid 形状
// （8-4-4-4-12 的十六进制，见 cmd/dshd/serve_create.go）。
//
// 它同时是**路径安全**判据：回执 id 会拼进文件路径，不校验形状就能用 `../` 逃出
// `<home>/state/uploads`。形状放宽等于把目录穿越口留给调用方，所以这里按生成器
// 的真实形状精确匹配；将来生成器改形状时这个测试会红，而不是静默放行。
var uploadReceiptIDPattern = regexp.MustCompile(`^rcpt-[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// officialImageMediaTypes 是官方 `ImageMediaType` 的四个枚举值（逐字）。
var officialImageMediaTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
}

// uploadReceiptRecord 是 `<home>/state/uploads/<receiptId>.json` 的形状。
//
// 它由 `fileUploads.upload` 写、这里读；字段名与那份记录逐字一致，不另造。
type uploadReceiptRecord struct {
	Version    int    `json:"version"`
	ReceiptID  string `json:"receiptId"`
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	Attachment string `json:"attachmentId"`
	CreatedAt  int64  `json:"createdAt"`
}

// uploadStateDir 是上传暂存区的唯一根（serve 的 fileUploads.upload 也用它）。
func uploadStateDir(home string) string { return filepath.Join(home, "state", "uploads") }

// uploadBlobPath 是内容寻址 blob 的落点。
func uploadBlobPath(home, digest string) string {
	return filepath.Join(uploadStateDir(home), "blobs", digest)
}

// NormalizeOfficialContent 把官方 `PromptContentPart` 的 file/image 两支解析成
// 宿主的 attachment 块，其余块原样透传（text/reference/attachment 的形状不变）。
//
// 返回值与输入**等长同序**：块顺序是语义（BEFORE/图/AFTER），少一块或多一块
// 都会让模型看到不一样的东西。
func (s *attachmentStore) NormalizeOfficialContent(sessionID string, input []transport.ContentBlock) ([]transport.ContentBlock, error) {
	if len(input) == 0 {
		return input, nil
	}
	out := make([]transport.ContentBlock, len(input))
	for i, block := range input {
		switch block.Type {
		case "file":
			attachment, err := s.attachmentFromUploadReceipt(sessionID, block.ReceiptID)
			if err != nil {
				return nil, err
			}
			out[i] = attachmentBlockOf(attachment)
		case "image":
			attachment, err := s.attachmentFromInlineImage(block)
			if err != nil {
				return nil, err
			}
			out[i] = attachmentBlockOf(attachment)
		default:
			out[i] = block
		}
	}
	return out, nil
}

// attachmentBlockOf 是"附件 → 线上块"的唯一投影：只带宿主自己核过的真值。
func attachmentBlockOf(attachment transport.Attachment) transport.ContentBlock {
	return transport.ContentBlock{
		Type: "attachment", AttachmentID: attachment.ID, Name: attachment.Name,
		ByteSize: attachment.ByteSize, MIME: attachment.MIME, SHA256: attachment.SHA256,
		Width: attachment.Width, Height: attachment.Height,
	}
}

// attachmentFromUploadReceipt 解析一个官方上传回执，返回真实的附件真值。
//
// 每个字段都重新核：回执里的 sessionId 必须就是本次 prompt 的会话；
// blob 必须存在、大小与 sha256 必须与回执一致；解析结果重新落一份受控附件对象
// （回执的 blob 目录是上传暂存区，不是附件 store 的 id 空间）。
func (s *attachmentStore) attachmentFromUploadReceipt(sessionID, receiptID string) (transport.Attachment, error) {
	if !uploadReceiptIDPattern.MatchString(receiptID) {
		return transport.Attachment{}, attachmentBadRequest("file 块的 receiptId 形状非法（期望 rcpt-<hex>）", nil)
	}
	if strings.TrimSpace(sessionID) == "" {
		return transport.Attachment{}, attachmentBadRequest("file 块需要会话身份才能解析回执", nil)
	}
	raw, err := readBoundedFile(filepath.Join(uploadStateDir(s.home), receiptID+".json"), 1<<20)
	if err != nil {
		if os.IsNotExist(err) {
			return transport.Attachment{}, attachmentNotFound("上传回执不存在于当前 home："+receiptID, err)
		}
		return transport.Attachment{}, attachmentBadRequest("读取上传回执失败", err)
	}
	var record uploadReceiptRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return transport.Attachment{}, attachmentBadRequest("上传回执 JSON 损坏", err)
	}
	if record.ReceiptID != receiptID {
		return transport.Attachment{}, attachmentBadRequest(
			"上传回执声明 id 为 "+record.ReceiptID+"，与请求的 "+receiptID+" 不一致", nil)
	}
	// 官方的回执只在接收它的 Agent 作用域里有效：跨会话使用是 not_found，
	// 不是"顺手借一下"。
	if record.SessionID != sessionID {
		return transport.Attachment{}, attachmentNotFound(
			fmt.Sprintf("上传回执 %s 属于会话 %s，不属于 %s", receiptID, record.SessionID, sessionID), nil)
	}
	if record.Bytes <= 0 || record.Bytes > attachmentMaxBytes {
		return transport.Attachment{}, attachmentBadRequest(
			fmt.Sprintf("上传回执声明的字节数 %d 不在 1..%d", record.Bytes, attachmentMaxBytes), nil)
	}
	data, err := readBoundedFile(uploadBlobPath(s.home, record.SHA256), attachmentMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return transport.Attachment{}, attachmentNotFound("上传回执指向的 blob 不存在："+record.SHA256, err)
		}
		return transport.Attachment{}, attachmentBadRequest("读取上传 blob 失败", err)
	}
	if int64(len(data)) != record.Bytes {
		return transport.Attachment{}, attachmentBadRequest(
			fmt.Sprintf("上传 blob 实际 %d 字节，回执声明 %d", len(data), record.Bytes), nil)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != record.SHA256 {
		return transport.Attachment{}, attachmentBadRequest("上传 blob 的 sha256 与回执不一致", nil)
	}
	name := agentUploadName(record.Name)
	return s.putBytes(name, data)
}

// attachmentFromInlineImage 解析官方 image 块的内联 base64 图片。
//
// mediaType 是客户端的**声明**：只接受官方四个枚举值，且必须与真实字节嗅探出的
// MIME 一致。不一致就拒绝，不让模型收到伪装类型。
func (s *attachmentStore) attachmentFromInlineImage(block transport.ContentBlock) (transport.Attachment, error) {
	mediaType := strings.ToLower(strings.TrimSpace(block.MediaType))
	if !officialImageMediaTypes[mediaType] {
		return transport.Attachment{}, attachmentBadRequest(
			"image 块的 mediaType 必须是官方四个图片类型之一，收到 "+block.MediaType, nil)
	}
	encoded := strings.TrimSpace(block.Data)
	if encoded == "" {
		return transport.Attachment{}, attachmentBadRequest("image 块的 data 是空串", nil)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return transport.Attachment{}, attachmentBadRequest("image 块的 data 不是合法 base64", err)
		}
	}
	if len(data) == 0 {
		return transport.Attachment{}, attachmentBadRequest("image 块解码后是 0 字节", nil)
	}
	name := agentUploadName(block.Name)
	if name == "upload" || name == "" {
		// 名字只是展示：没给就按类型给一个稳定叶子名，不编造原文件名。
		name = "image" + strings.TrimPrefix(mediaType, "image/")
	}
	attachment, err := s.putBytes(name, data)
	if err != nil {
		return transport.Attachment{}, err
	}
	if attachment.MIME != mediaType {
		return transport.Attachment{}, attachmentBadRequest(
			fmt.Sprintf("image 块声明 %s，真实字节是 %s", mediaType, attachment.MIME), nil)
	}
	return attachment, nil
}

// readBoundedFile 读一个有上限的文件；超过上限即报错（不截断后当成功）。
func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文件 %s 超过 %d 字节上限", path, limit)
	}
	return data, nil
}
