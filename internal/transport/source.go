package transport

import "errors"

// Source 是 transport 唯一的取数入口。
//
// 实现放在宿主那边（它才被允许读 v4 日志）；transport 自己**不 import 任何
// 会话日志的编解码包**（红线 R7），也不碰文件系统——这条线只做协议翻译。
type Source interface {
	// Messages 返回一个会话的全部消息，**必须按 Seq 升序**。
	Messages(sessionID string) ([]Message, error)
}

var ErrNoSuchSession = errors.New("没有这个会话")

// ErrNoSuchFile 由宿主在读文件时返回，协议层据此回 404。
// "文件不存在"和"读失败"在界面上长得一样，但前者该换路径、后者该重试。
var ErrNoSuchFile = errors.New("文件不存在")

// LoadSession 是 session.load 的实现：首屏，取最新一屏。
// TailSourcer 是 Source 的可选增强：只取最后 limit 条消息，同时给出总数。
//
// 与 SeqSourcer 同一个理由：`session.load` 只要首屏 50 条，
// 但"先投影全部再截尾"在 24k 事件的会话上要 480 ms。
type TailSourcer interface {
	MessagesTail(sessionID string, limit int) ([]Message, int, error)
}

func LoadSession(src Source, sessionID string, limit int) (LoadResult, error) {
	if src == nil {
		return LoadResult{}, errors.New("transport: LoadSession 需要 Source，传了 nil")
	}
	if sessionID == "" {
		return LoadResult{}, ErrNoSuchSession
	}
	if ts, ok := src.(TailSourcer); ok {
		msgs, total, err := ts.MessagesTail(sessionID, limit)
		if err != nil {
			return LoadResult{}, err
		}
		return LoadResult{Messages: msgs, Total: total, Truncated: total > len(msgs)}, nil
	}

	all, err := src.Messages(sessionID)
	if err != nil {
		return LoadResult{}, err
	}
	page := SelectPage(all, PageTail, 0, limit)
	return LoadResult{
		Messages:  page.Messages,
		Total:     page.Total,
		Truncated: page.Truncated,
	}, nil
}

// HistorySession 是 session.history 的实现：按 seq 增量补洞。
// SeqSourcer 是 Source 的可选增强：只取 seq > sinceSeq 的消息。
//
// 为什么需要它：`session.history` 是界面断线补洞时反复调用的 method。
// 只实现 Messages 的源会**先从全量事件投影出所有消息、再按 seq 过滤**——
// 实测最大的那份会话（24k 事件）即使结果为空也要 425 ms。
// 实现这个接口就能在投影前先掐掉不需要的事件。
type SeqSourcer interface {
	MessagesSince(sessionID string, sinceSeq int) ([]Message, error)
}

func HistorySession(src Source, sessionID string, sinceSeq int, limit int) (Page, error) {
	if src == nil {
		return Page{}, errors.New("transport: HistorySession 需要 Source，传了 nil")
	}
	if sessionID == "" {
		return Page{}, ErrNoSuchSession
	}
	// 能增量取就增量取：这是界面轮询最频繁的一条路。
	if ss, ok := src.(SeqSourcer); ok {
		newer, err := ss.MessagesSince(sessionID, sinceSeq)
		if err != nil {
			return Page{}, err
		}
		return SelectPage(newer, PageSince, sinceSeq, limit), nil
	}

	all, err := src.Messages(sessionID)
	if err != nil {
		return Page{}, err
	}
	return SelectPage(all, PageSince, sinceSeq, limit), nil
}
