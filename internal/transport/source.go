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
func LoadSession(src Source, sessionID string, limit int) (LoadResult, error) {
	if src == nil {
		return LoadResult{}, errors.New("transport: LoadSession 需要 Source，传了 nil")
	}
	if sessionID == "" {
		return LoadResult{}, ErrNoSuchSession
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
func HistorySession(src Source, sessionID string, sinceSeq int, limit int) (Page, error) {
	if src == nil {
		return Page{}, errors.New("transport: HistorySession 需要 Source，传了 nil")
	}
	if sessionID == "" {
		return Page{}, ErrNoSuchSession
	}
	all, err := src.Messages(sessionID)
	if err != nil {
		return Page{}, err
	}
	return SelectPage(all, PageSince, sinceSeq, limit), nil
}
