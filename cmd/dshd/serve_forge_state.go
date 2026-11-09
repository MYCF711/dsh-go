package main

// PARITY-W2-STATE：`forgePanel.{track,dismiss,refresh,follow}` 的宿主实现。
//
// # 数据源（一份账本）
//
// 全部读写 `internal/forge` 的绑定存储：`<home>/state/forge-sessions.json`。
// 没有第二个账本，也没有内存镜像——每次快照都从存储读，所以"另一个进程改了"
// 与"本进程改了"看到的完全一样。
//
// # 版本号与唤醒
//
// follow 要真流：本宿主用"写操作后递增版本号 + 等版本变化"唤醒帧机。
// 没有变化时帧机阻塞在 ctx 上（不空转轮询）；有变化立刻出帧。
// 版本号是**进程内**单调计数（跨进程靠帧机每轮重新读存储兜底，见 Snapshot
// 的实现：Version 取"存储内容 hash 的单调投影"不现实，因此这里用
// `storeFingerprint` 把绑定集合算成一个稳定值，进程间也能感知变化）。
//
// # Provider
//
// 生产：`DSHD_FORGE_PROVIDER_TOKEN` 存在时用 GitHub 真实拉取面（HTTPProvider）；
// 不存在时 **不静默回旧值**，refresh 直接为结构化 `ErrForgeNoProvider`。
// 测试：注入 httptest provider（同一接口，真实 HTTP）。

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"time"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
)

// forgePanelHost 把存储装成 Transport 认的那份函数表。
type forgePanelHost struct {
	store *forge.Store

	mu      sync.Mutex
	version int64
	waiters map[chan struct{}]struct{}
}

// newForgePanelHost 造一个宿主。home 为空时返回 nil（调用方据此不接线）。
func newForgePanelHost(home string) (*forgePanelHost, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, fmt.Errorf("forge: home 为空，不能开绑定存储")
	}
	store, err := forge.Open(home)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(os.Getenv("DSHD_FORGE_PROVIDER_TOKEN")); token != "" {
		store.SetProvider(&forge.HTTPProvider{Token: token})
	}
	return &forgePanelHost{
		store:   store,
		waiters: map[chan struct{}]struct{}{},
	}, nil
}

// api 把宿主动词装成 transport 的函数表。
func (h *forgePanelHost) api() transport.OfficialForgePanelAPI {
	return transport.OfficialForgePanelAPI{
		Track:    h.track,
		Dismiss:  h.dismiss,
		Refresh:  h.refresh,
		Snapshot: h.snapshot,
		Wait:     h.wait,
	}
}

func (h *forgePanelHost) track(sessionID, rawURL string) (transport.OfficialForgePanelBinding, error) {
	b, err := h.store.Track(sessionID, rawURL, "github")
	if err != nil {
		return transport.OfficialForgePanelBinding{}, forgePanelFault(err)
	}
	h.bump()
	return forgePanelBinding(b), nil
}

func (h *forgePanelHost) dismiss(sessionID, rawURL string) (transport.OfficialForgePanelBinding, error) {
	b, err := h.store.Dismiss(sessionID, rawURL)
	if err != nil {
		return transport.OfficialForgePanelBinding{}, forgePanelFault(err)
	}
	h.bump()
	return forgePanelBinding(b), nil
}

func (h *forgePanelHost) refresh(ctx context.Context, sessionID, rawURL string) ([]transport.OfficialForgePanelBinding, error) {
	// url 缺省：刷新该会话的**全部**绑定（官方 refresh 语义）。
	if strings.TrimSpace(rawURL) == "" {
		list, err := h.store.List()
		if err != nil {
			return nil, forgePanelFault(err)
		}
		out := make([]transport.OfficialForgePanelBinding, 0, len(list))
		var firstErr error
		for _, b := range list {
			if b.SessionID != sessionID {
				continue
			}
			refreshed, rerr := h.store.Refresh(ctx, b.SessionID, b.URL)
			if rerr != nil {
				if firstErr == nil {
					firstErr = rerr
				}
				continue
			}
			out = append(out, forgePanelBinding(refreshed))
		}
		if len(out) == 0 {
			if firstErr != nil {
				return nil, forgePanelFault(firstErr)
			}
			return nil, fmt.Errorf("%w：session=%q 没有可刷新的绑定", transport.ErrForgeBindingNotFound, sessionID)
		}
		h.bump()
		return out, nil
	}
	b, err := h.store.Refresh(ctx, sessionID, rawURL)
	if err != nil {
		return nil, forgePanelFault(err)
	}
	h.bump()
	return []transport.OfficialForgePanelBinding{forgePanelBinding(b)}, nil
}

// snapshot 读一次真实存储，版本号取"绑定集合指纹"与进程内计数的大者。
//
// 指纹让**跨进程**的写也能被 follow 感知（另一个 dshd 改了同一个 home），
// 进程内计数让"同内容但发生过写"也能推进版本（例如 dismiss 同一 url 两次）。
func (h *forgePanelHost) snapshot() (transport.OfficialForgePanelSnapshot, error) {
	list, err := h.store.List()
	if err != nil {
		return transport.OfficialForgePanelSnapshot{}, forgePanelFault(err)
	}
	out := make([]transport.OfficialForgePanelBinding, 0, len(list))
	h2 := fnv.New64a()
	for _, b := range list {
		out = append(out, forgePanelBinding(b))
		_, _ = fmt.Fprintf(h2, "%s|%s|%t|%s|%s|%d|%d|%d;",
			b.SessionID, b.URL, b.Tracked, b.State, b.HeadSHA,
			b.TrackedAt.UnixNano(), b.DismissedAt.UnixNano(), b.RefreshedAt.UnixNano())
	}
	h.mu.Lock()
	version := h.version
	h.mu.Unlock()
	fp := int64(h2.Sum64() & 0x7fffffffffffffff)
	if fp == 0 {
		fp = 1
	}
	// 版本 = 进程内计数 * 2^32 + 指纹低位，保证"内容变"和"计数变"都能推进。
	combined := (version << 20) | (fp & 0xfffff)
	return transport.OfficialForgePanelSnapshot{Version: combined, Bindings: out}, nil
}

// wait 阻塞到版本变化或 ctx 取消。
func (h *forgePanelHost) wait(ctx context.Context, since int64) error {
	h.mu.Lock()
	if cur, _ := h.currentVersionLocked(); cur != since {
		h.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	h.waiters[ch] = struct{}{}
	h.mu.Unlock()

	select {
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.waiters, ch)
		h.mu.Unlock()
		return ctx.Err()
	case <-time.After(2 * time.Second):
		// 兜底：跨进程写不会触发本进程的 bump，2s 后重新读存储。
		h.mu.Lock()
		delete(h.waiters, ch)
		h.mu.Unlock()
		return nil
	case <-ch:
		return nil
	}
}

func (h *forgePanelHost) currentVersionLocked() (int64, struct{}) {
	return h.version, struct{}{}
}

// bump 递增版本号并唤醒所有等待者（写操作后调用）。
func (h *forgePanelHost) bump() {
	h.mu.Lock()
	h.version++
	waiters := make([]chan struct{}, 0, len(h.waiters))
	for ch := range h.waiters {
		waiters = append(waiters, ch)
	}
	h.waiters = map[chan struct{}]struct{}{}
	h.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// forgePanelBinding 把领域绑定翻成 wire 形状（时间统一 RFC3339Nano）。
func forgePanelBinding(b forge.Binding) transport.OfficialForgePanelBinding {
	out := transport.OfficialForgePanelBinding{
		SessionID: b.SessionID,
		URL:       b.URL,
		Provider:  b.Provider,
		Tracked:   b.Tracked,
		State:     b.State,
		Title:     b.Title,
		HeadSHA:   b.HeadSHA,
	}
	if !b.TrackedAt.IsZero() {
		out.TrackedAt = b.TrackedAt.UTC().Format(time.RFC3339Nano)
	}
	if !b.DismissedAt.IsZero() {
		out.DismissedAt = b.DismissedAt.UTC().Format(time.RFC3339Nano)
	}
	if !b.RefreshedAt.IsZero() {
		out.RefreshedAt = b.RefreshedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// forgePanelFault 把领域错误翻成 Transport 的哨兵（四字段由 Transport 出）。
func forgePanelFault(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, forge.ErrNotFound):
		return fmt.Errorf("%w：%v", transport.ErrForgeBindingNotFound, err)
	case errors.Is(err, forge.ErrInvalidURL):
		return fmt.Errorf("%w：%v", transport.ErrForgeInvalidURL, err)
	case errors.Is(err, forge.ErrNoProvider):
		return fmt.Errorf("%w：%v", transport.ErrForgeNoProvider, err)
	}
	return err
}
