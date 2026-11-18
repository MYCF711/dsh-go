package session

// 导入历史的写入端：与真实会话**同一条** v4 写路径，只多两个参数——
// header.createdAt 由调用方给（第一条事件的时间），以及一批事件一次成帧。
//
// 为什么不能复用 `CreateSession` + `Append`：
//   - `CreateSession` 把 createdAt 写成 `time.Now()`，导入的会话于是全部挤在
//     导入那一刻，侧栏排序与"这份历史到底是什么时候发生的"都会说谎；
//   - `Append` 一事件一帧 + 每帧一次 fsync。导入一条几百条事件的对话就是几百次
//     fsync，而官方写的是"一次 create + 一次 append + 一次 flush"。
//
// 两者都不改既有路径的语义：`CreateSession` / `Append` 一个字节都不变。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dshgo/codecs/v4"
)

// CreateImportedSession 在 <home>/sessions/<projectKey>/<sessionId>/ 下新建一个
// 导入会话。createdAtMS 取导出里第一条事件的时间（官方
// `dsh-deepseek-chat-import/src/importer.ts:562-566`：header 是
// `{version, id, createdAt: conversion.createdAt, cwd, isSeeded: false}`）。
//
// 与 `CreateSession` 的差异只有两点：创建时间由调用方给；`agentPreset` 留空
// （官方导入的 header 里没有 preset——它不是在某个 preset 下产生的历史）。
func CreateImportedSession(home, cwd, id string, createdAtMS int64) (*Log, error) {
	if createdAtMS <= 0 {
		return nil, fmt.Errorf("导入会话的 createdAt 必须是正数（拿到 %d）", createdAtMS)
	}
	return createSession(home, cwd, id, "", createdAtMS)
}

// ImportedEvent 是一条待写入的导入事件：类型、data、可选的信封字段与时间。
//
// seq 由 AppendImportedEvents 统一分配（调用方给的一律忽略）：v4 的 seq 必须
// 从 0 起连续，让调用方决定它等于把"文件能不能读回来"交给了一个外部计数。
type ImportedEvent struct {
	// Type 是 v4 事件类型（必须在词汇表里）。
	Type string
	// Data 是事件负载，按 JSON 编码。
	Data any
	// Time 是事件时间（epoch 毫秒）。
	Time int64
	// SurfaceOp 是信封上的 surfaceOp（`append` 或 replace 形状）；空表示不写。
	SurfaceOp string
	// SourceEventSeqs 是信封上的来源事件 seq 列表。
	SourceEventSeqs []int64
}

// AppendImportedEvents 把一批导入事件写成**一帧**，并按现有 seq 连续编号。
//
// 与 `Append` 同一套写前拦截：没有 type、负载是 null 的事件一律拒绝——导入写的是
// 一份要能 resume 的历史，写进去一条读不回来的记录比不写更糟。
func (l *Log) AppendImportedEvents(events []ImportedEvent) error {
	if len(events) == 0 {
		return nil
	}
	batch := make([]v4.Event, 0, len(events))
	for index, event := range events {
		if strings.TrimSpace(event.Type) == "" {
			return fmt.Errorf("第 %d 条导入事件没有类型：写进去也没有任何投影能认出它", index)
		}
		record := v4.Event{
			Type: event.Type,
			Seq:  l.seq + int64(index),
			Time: event.Time,
			Data: nil,
		}
		if event.Time <= 0 {
			// 时间是排序与显示的真相：0 会让这一条排到 1970。
			return fmt.Errorf("第 %d 条导入事件（%s）的时间必须为正数（拿到 %d）", index, event.Type, event.Time)
		}
		raw, err := marshalImportedData(event.Data)
		if err != nil {
			return fmt.Errorf("第 %d 条导入事件（%s）序列化失败: %w", index, event.Type, err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			return fmt.Errorf("第 %d 条导入事件（%s）的负载是 null：落盘后投影读不到任何字段", index, event.Type)
		}
		// 与真实会话同一份 seed 校验：官方导入会先跑 `Session.create` 的种子校验才
		// 落盘（importer.ts:563-564）。本仓对应的就是事件词汇表准入（未知类型会被
		// 读取路径拒收），加上已登记类型的负载校验。
		if err := ValidateKnownEventPayload(event.Type, raw); err != nil {
			return fmt.Errorf("第 %d 条导入事件（%s）没过 seed 校验: %w", index, event.Type, err)
		}
		record.Data = raw
		if event.SurfaceOp != "" {
			record.SurfaceOp = mustQuoteJSON(event.SurfaceOp)
		}
		if len(event.SourceEventSeqs) > 0 {
			record.SourceEventSeqs = event.SourceEventSeqs
		}
		batch = append(batch, record)
	}
	if err := l.w.Append(batch...); err != nil {
		return err
	}
	l.seq += int64(len(batch))
	// v4 事件是权威记录，但 `session.list` 读的是**同目录小索引**
	// （`session-title.json`，见 metadata.go 的 readExplicitTitle）：不提交索引，
	// 导入的会话在侧栏里就没有原标题，只有一句摘要。这里复用真实改名路径的
	// 同一段索引代码（`metadataIndexFromTransaction` + `writeSessionMetadataIndex`），
	// 不是另写一份索引格式。
	return l.commitImportedIndexes(batch)
}

// commitImportedIndexes 把导入日志里的 metadata 事件（title/color）提交成
// 同目录读索引。最后一条同类事件胜出（latest-wins，与 projection 一致）。
func (l *Log) commitImportedIndexes(events []v4.Event) error {
	for _, event := range events {
		switch event.Type {
		case "session/title", "session/color":
		default:
			continue
		}
		tx := sessionMetadataTransaction{Version: 1, EventSeq: event.Seq, Type: event.Type, Data: event.Data}
		index, err := metadataIndexFromTransaction(tx)
		if err != nil {
			return fmt.Errorf("导入会话的 %s 索引构造失败: %w", event.Type, err)
		}
		raw, err := json.Marshal(index)
		if err != nil {
			return fmt.Errorf("导入会话的 %s 索引序列化失败: %w", event.Type, err)
		}
		indexPath, _, err := metadataPaths(l.Path, event.Type)
		if err != nil {
			return err
		}
		if err := writeSessionMetadataIndex(indexPath, raw); err != nil {
			return fmt.Errorf("导入会话的 %s 索引写盘失败: %w", event.Type, err)
		}
	}
	return nil
}

// CloseImported 关闭导入会话的写入端（关闭后才算 durable）。
func (l *Log) CloseImported() error {
	return l.Close()
}

// NowMillis 是导入路径给"没有导出时间的事件"兜底的时钟（极少用到：对话文件的
// 每条消息都带 inserted_at）。
func NowMillis() int64 { return time.Now().UnixMilli() }

// marshalImportedData 把导入事件的负载编码成 JSON。
func marshalImportedData(data any) ([]byte, error) {
	if data == nil {
		return nil, nil
	}
	if raw, ok := data.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(data)
}

// mustQuoteJSON 把一个字符串编码成 JSON 字符串字面量（surfaceOp 只有 "append"
// 这一种字符串形状；replace 形状由调用方直接给 v4.Event）。
func mustQuoteJSON(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`"append"`)
	}
	return encoded
}
