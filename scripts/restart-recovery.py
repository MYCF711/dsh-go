#!/usr/bin/env python3
"""RESTART-RECOVERY：同一临时 home 跨两个真实 dshd 进程的恢复证据。

Go 探针通过 `go test -overlay` 进入真实 UI `internal/apiclient` 包，UI 工作树不改。
阶段 A 完成 create/list/load/prompt/text/reasoning/tool/approval；Python 只终止自己启动
且核对映像路径的宿主 A，再在同一 home 启动宿主 B。阶段 B 实测旧 cookie/token，
处理 401 后重新 Connect，并验证 reset/stale_epoch、重新 load/subscribe、新 prompt、
A/B 隔离与 v4 append-only。全程不启动 GUI，不用 mockd/ACP/截图代替 Transport 证据。

用法：
  go build -o tmp/restart-recovery-bin/dshd.exe ./cmd/dshd
  python scripts/restart-recovery.py --binary tmp/restart-recovery-bin/dshd.exe --ui <UI副本>

证据默认写入 tmp/restart-recovery-<随机>/。最终一行固定为 OK=n FAIL=m SKIP=k；
没有阶段汇总、0 断言、非零 go test 或驱动异常都不会报绿。cookie 仅经一个临时
0600 文件跨阶段传递，驱动收尾核对路径后删除；token/cookie 不写日志与证据。
"""
from __future__ import annotations

import argparse
import ctypes
from ctypes import wintypes
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import queue
import re
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_UI = Path(r"C:\Users\Administrator\AppData\Local\Temp\dsh-ui-restart-recovery-isolated")
SUMMARY_RE = re.compile(r"RR_SUMMARY phase=(A|B) OK=(\d+) FAIL=(\d+) SKIP=(\d+)")
SECRET_NAME = ".restart-cookies.json"

GO_PROBE = r'''package apiclient

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
    "time"
)

const (
    rrAlphaTextBefore = "RR_ALPHA_TEXT_BEFORE_OK"
    rrAlphaToolBefore = "RR_ALPHA_TOOL_BEFORE_OK"
    rrBetaTextBefore  = "RR_BETA_TEXT_BEFORE_OK"
    rrAlphaTextAfter  = "RR_ALPHA_TEXT_AFTER_OK"
)

type rrState struct {
    AlphaID      string `json:"alphaId"`
    BetaID       string `json:"betaId"`
    AlphaEpoch   int64  `json:"alphaJournalEpoch"`
    BetaEpoch    int64  `json:"betaJournalEpoch"`
    AlphaLastSeq int    `json:"alphaLastSeq"`
    BetaLastSeq  int    `json:"betaLastSeq"`
}

type rrChunk struct { kind string; chunk Chunk }
type rrRecorder struct {
    mu sync.Mutex
    events []Event
    chunks []rrChunk
    closed bool
    notify chan struct{}
}
func newRRRecorder() *rrRecorder { return &rrRecorder{notify: make(chan struct{}, 1)} }
func (r *rrRecorder) signal() { select { case r.notify <- struct{}{}: default: } }
func (r *rrRecorder) add(ev Event) {
    r.mu.Lock()
    r.events = append(r.events, ev)
    for _, c := range ev.Chunks() { r.chunks = append(r.chunks, rrChunk{ev.Kind, c}) }
    r.mu.Unlock(); r.signal()
}
func (r *rrRecorder) run(ch <-chan Event) {
    for ev := range ch { r.add(ev) }
    r.mu.Lock(); r.closed = true; r.mu.Unlock(); r.signal()
}
func (r *rrRecorder) snap() ([]Event, []rrChunk, bool) {
    r.mu.Lock(); defer r.mu.Unlock()
    return append([]Event(nil), r.events...), append([]rrChunk(nil), r.chunks...), r.closed
}
func (r *rrRecorder) wait(d time.Duration, pred func([]Event, []rrChunk, bool) bool) bool {
    deadline := time.Now().Add(d)
    for {
        ev, ch, closed := r.snap()
        if pred(ev, ch, closed) { return true }
        if time.Now().After(deadline) { return false }
        select { case <-r.notify: case <-time.After(100 * time.Millisecond): }
    }
}
func rrEndAfter(events []Event, since int) (Event, bool) {
    for _, e := range events {
        if e.Seq > since && (e.Kind == "turn/done" || e.Kind == "turn/error") { return e, true }
    }
    return Event{}, false
}
func rrTypes(events []Event, typ string) []Event {
    out := []Event{}
    for _, e := range events { if e.Kind == typ { out = append(out, e) } }
    return out
}
func rrText(chunks []rrChunk, marker string) bool {
    for _, c := range chunks { if c.chunk.Block == BlockText && strings.Contains(c.chunk.Text, marker) { return true } }
    return false
}
func rrReasoningBytes(chunks []rrChunk) int {
    n := 0
    for _, c := range chunks { if c.chunk.Block == BlockReasoning { n += len(c.chunk.Text) } }
    return n
}
func rrToolChunks(chunks []rrChunk) int {
    n := 0
    for _, c := range chunks { if c.chunk.Block == BlockTool { n++ } }
    return n
}
func rrEventsContain(events []Event, marker string) bool {
    for _, e := range events { if strings.Contains(string(e.Data), marker) { return true } }
    return false
}
func rrFrameContains(h *HistoryResult, typ, marker string) bool {
    if h == nil { return false }
    for _, f := range h.Frames {
        if f.Type == typ && (marker == "" || strings.Contains(string(f.Data), marker)) { return true }
    }
    return false
}
func rrHistoryReasoning(h *HistoryResult) bool {
    if h == nil { return false }
    for _, f := range h.Frames {
        if f.Type == "assistant/reasoning" || strings.Contains(string(f.Data), `"type":"reasoning"`) { return true }
    }
    return false
}
func rrLoadContains(load *LoadResult, role, marker string) bool {
    if load == nil { return false }
    for _, m := range load.Messages {
        if (role == "" || m.Role == role) && strings.Contains(m.Text, marker) { return true }
    }
    return false
}
func rrLoadTool(load *LoadResult) bool {
    if load == nil { return false }
    for _, m := range load.Messages { if m.Role == "tool" && m.Tool != "" { return true } }
    return false
}

// rrStatusCapture 只观察真实 RoundTripper 的状态码，不改请求/响应，也不是 mock。
// 这样 apiclient 的 typed APIError 与线上 HTTP 401/409 能在同一次调用里核对。
type rrStatusCapture struct {
    base http.RoundTripper
    mu sync.Mutex
    status int
}
func (t *rrStatusCapture) RoundTrip(req *http.Request) (*http.Response, error) {
    base := t.base; if base == nil { base = http.DefaultTransport }
    resp, err := base.RoundTrip(req)
    if resp != nil { t.mu.Lock(); t.status = resp.StatusCode; t.mu.Unlock() }
    return resp, err
}
func (t *rrStatusCapture) last() int { t.mu.Lock(); defer t.mu.Unlock(); return t.status }

type rrHarness struct {
    t *testing.T
    phase string
    base string
    home string
    out string
    statePath string
    secretPath string
    cli *Client
    ok, fail int
}
func (h *rrHarness) check(name string, cond bool, detail ...any) {
    msg := fmt.Sprint(detail...)
    if cond { h.ok++; fmt.Printf("RR_OK phase=%s %s %s\n", h.phase, name, msg)
    } else { h.fail++; fmt.Printf("RR_FAIL phase=%s %s %s\n", h.phase, name, msg) }
}
func (h *rrHarness) save(name string, value any) {
    b, err := json.MarshalIndent(value, "", "  ")
    if err == nil { err = os.WriteFile(filepath.Join(h.out, name+".json"), b, 0644) }
    h.check("save/"+name, err == nil, "err=", err)
}
func (h *rrHarness) mux(epoch int64, cli *Client) *Mux {
    m := NewMux(h.base)
    m.Cookies = cli.Cookies()
    m.EpochOf = func(string) int64 { return epoch }
    m.PingEvery = 250 * time.Millisecond
    return m
}
func (h *rrHarness) subscribe(cli *Client, epoch int64, id string, since int) (*rrRecorder, context.CancelFunc, error) {
    ctx, cancel := context.WithCancel(context.Background())
    ch, err := h.mux(epoch, cli).Subscribe(ctx, "session:"+id, since)
    if err != nil { cancel(); return nil, func(){}, err }
    rec := newRRRecorder(); go rec.run(ch)
    return rec, cancel, nil
}
func (h *rrHarness) create(title string) (string, *LoadResult) {
    id, err := h.cli.Create(context.Background(), "", "default", title)
    h.check("create/"+title, err == nil && id != "", "id=", id, " err=", err)
    if err != nil || id == "" { return "", nil }
    load, err := h.cli.Load(context.Background(), id, 200)
    h.check("initial-load/"+title, err == nil && load != nil && load.JournalEpoch != 0 && load.Permission == "ask",
        "epoch/permission err=", err)
    return id, load
}
func (h *rrHarness) prompt(rec *rrRecorder, id, text string, since int, approve bool) (Event, int) {
    turn, err := h.cli.Prompt(context.Background(), id, text)
    h.check("prompt-accepted", err == nil && turn != "", "turn=", turn, " err=", err)
    if err != nil { return Event{}, 0 }
    seen := map[string]bool{}
    answered := 0
    deadline := time.Now().Add(240 * time.Second)
    for time.Now().Before(deadline) {
        events, _, closed := rec.snap()
        if approve {
            for _, ev := range rrTypes(events, "approval/request") {
                req, parseErr := ParseApprovalRequest(ev.Data)
                if parseErr != nil { h.check("approval-parse", false, "err=", parseErr); continue }
                if seen[req.RequestID] { continue }
                seen[req.RequestID] = true
                option := req.OptionID("allow_once")
                h.check("approval-option", option != "", "request=", req.RequestID)
                if option == "" { continue }
                answerErr := h.cli.Approve(context.Background(), req.RequestID, option)
                h.check("approval-allow-once", answerErr == nil, "err=", answerErr)
                if answerErr == nil { answered++ }
            }
        }
        if end, ok := rrEndAfter(events, since); ok { return end, answered }
        if closed { h.check("subscription-open-until-turn-end", false, "closed early"); return Event{}, answered }
        time.Sleep(100 * time.Millisecond)
    }
    h.check("prompt-turn-end-timeout", false, "since=", since)
    return Event{}, answered
}
func (h *rrHarness) checkOriginalHistory(prefix string, hist *HistoryResult) {
    h.check(prefix+"-original-assistant-text", rrFrameContains(hist, "assistant/message", rrAlphaTextBefore),
        "frames=", func() int { if hist == nil { return 0 }; return len(hist.Frames) }())
    h.check(prefix+"-original-reasoning", rrHistoryReasoning(hist))
    h.check(prefix+"-original-tool-call-result", rrFrameContains(hist, "tool/call", "") && rrFrameContains(hist, "tool/result", ""))
    h.check(prefix+"-original-approval", rrFrameContains(hist, "approval/request", ""))
}

func TestRestartRecovery(t *testing.T) {
    h := &rrHarness{
        t: t, phase: os.Getenv("RR_PHASE"), base: os.Getenv("RR_BASE"),
        home: os.Getenv("RR_HOME"), out: os.Getenv("RR_OUT"),
        statePath: os.Getenv("RR_STATE"), secretPath: os.Getenv("RR_SECRET"),
    }
    defer func() {
        fmt.Printf("RR_SUMMARY phase=%s OK=%d FAIL=%d SKIP=0\n", h.phase, h.ok, h.fail)
        if h.fail > 0 { t.Errorf("restart recovery phase %s 有 %d 条 FAIL", h.phase, h.fail) }
    }()
    if h.base == "" || h.home == "" || h.out == "" || h.statePath == "" || h.secretPath == "" {
        h.check("required-env", false); return
    }
    switch h.phase {
    case "A": h.runA()
    case "B": h.runB()
    default: h.check("phase-must-be-A-or-B", false, "phase=", h.phase)
    }
}

func (h *rrHarness) runA() {
    var state rrState
    defer func() {
        if state.AlphaID != "" || state.BetaID != "" {
            b, err := json.MarshalIndent(state, "", "  ")
            if err == nil { err = os.WriteFile(h.statePath, b, 0644) }
            h.check("save-restart-state", err == nil && state.AlphaID != "" && state.BetaID != "" &&
                state.AlphaEpoch != 0 && state.BetaEpoch != 0 && state.AlphaLastSeq > 0 && state.BetaLastSeq > 0, "err=", err)
        } else { h.check("save-restart-state", false, "no session ids") }
    }()

    h.cli = New(h.base); h.cli.HTTP.Timeout = 30 * time.Second
    err := h.cli.Connect(context.Background(), os.Getenv("RR_TOKEN"))
    h.check("connect-ready-token", err == nil && len(h.cli.Cookies()) > 0, "err=", err)
    if err != nil { return }
    cookieBytes, err := json.Marshal(h.cli.Cookies())
    if err == nil { err = os.WriteFile(h.secretPath, cookieBytes, 0600) }
    h.check("stage-old-cookie-for-phase-B", err == nil, "err=", err)
    if err != nil { return }

    alphaID, alphaInitial := h.create("restart-alpha")
    betaID, betaInitial := h.create("restart-beta")
    state.AlphaID, state.BetaID = alphaID, betaID
    if alphaID == "" || betaID == "" || alphaInitial == nil || betaInitial == nil { return }
    listed, err := h.cli.List(context.Background())
    foundA, foundB := false, false
    for _, s := range listed { if s.ID == alphaID { foundA = true }; if s.ID == betaID { foundB = true } }
    h.check("list-contains-A-B", err == nil && foundA && foundB, "sessions=", len(listed), " err=", err)

    alphaRec, alphaCancel, err := h.subscribe(h.cli, alphaInitial.JournalEpoch, alphaID, 0)
    h.check("subscribe-alpha-A", err == nil, "err=", err)
    if err != nil { return }
    defer alphaCancel()
    textEnd, _ := h.prompt(alphaRec, alphaID, "Reply with exactly "+rrAlphaTextBefore+" and nothing else. Do not call any tool.", 0, false)
    alphaEvents, alphaChunks, _ := alphaRec.snap()
    h.check("alpha-text-turn-done", textEnd.Kind == "turn/done" && rrText(alphaChunks, rrAlphaTextBefore), "end=", textEnd.Kind)
    if textEnd.Kind != "turn/done" { return }

    target := filepath.Join(h.out, "alpha-tool-before.txt")
    toolEnd, answered := h.prompt(alphaRec, alphaID,
        "用 write 工具把 "+rrAlphaToolBefore+" 写进文件 "+target+"，只做这一步，不要解释。", textEnd.Seq, true)
    alphaEvents, alphaChunks, _ = alphaRec.snap()
    content, readErr := os.ReadFile(target)
    h.check("alpha-tool-turn-done", toolEnd.Kind == "turn/done" && answered > 0, "end=", toolEnd.Kind, " approvals=", answered)
    h.check("alpha-tool-real-side-effect", readErr == nil && strings.Contains(string(content), rrAlphaToolBefore), "err=", readErr)
    h.check("alpha-tool-through-ui-chunks", rrToolChunks(alphaChunks) > 0, "tool_chunks=", rrToolChunks(alphaChunks))
    h.check("alpha-reasoning-through-ui-chunks", rrReasoningBytes(alphaChunks) > 0, "reasoning_bytes=", rrReasoningBytes(alphaChunks))
    h.save("phase-a-alpha-events", alphaEvents)

    alphaLoad, alphaLoadErr := h.cli.Load(context.Background(), alphaID, 200)
    h.check("alpha-load-before-restart", alphaLoadErr == nil && alphaLoad != nil && alphaLoad.LastSeq > 0,
        "err=", alphaLoadErr)
    if alphaLoad == nil { return }
    state.AlphaEpoch, state.AlphaLastSeq = alphaLoad.JournalEpoch, alphaLoad.LastSeq
    h.save("phase-a-alpha-load", alphaLoad)
    alphaHist, alphaHistErr := h.cli.History(context.Background(), alphaID, 0, 500, int(alphaLoad.JournalEpoch))
    h.check("alpha-history-before-restart", alphaHistErr == nil && alphaHist != nil && alphaHist.LastSeq == alphaLoad.LastSeq,
        "err=", alphaHistErr)
    if alphaHist != nil {
        h.save("phase-a-alpha-history", alphaHist)
        h.checkOriginalHistory("phase-a-history", alphaHist)
        continuous := len(alphaHist.Frames) > 0
        for i, f := range alphaHist.Frames { if f.Seq != i+1 || f.Type == "" || len(f.Data) == 0 { continuous = false } }
        h.check("phase-a-history-continuous-raw-seq", continuous && alphaHist.Frames[0].Seq == 1, "frames=", len(alphaHist.Frames))
    }
    h.check("alpha-load-text-and-tool", rrLoadContains(alphaLoad, "assistant", rrAlphaTextBefore) && rrLoadTool(alphaLoad))

    betaRec, betaCancel, err := h.subscribe(h.cli, betaInitial.JournalEpoch, betaID, 0)
    h.check("subscribe-beta-A", err == nil, "err=", err)
    if err != nil { return }
    defer betaCancel()
    betaEnd, _ := h.prompt(betaRec, betaID, "Reply with exactly "+rrBetaTextBefore+" and nothing else. Do not call any tool.", 0, false)
    betaEvents, betaChunks, _ := betaRec.snap()
    h.check("beta-text-turn-done", betaEnd.Kind == "turn/done" && rrText(betaChunks, rrBetaTextBefore), "end=", betaEnd.Kind)
    betaLoad, betaLoadErr := h.cli.Load(context.Background(), betaID, 200)
    h.check("beta-load-before-restart", betaLoadErr == nil && betaLoad != nil && betaLoad.LastSeq > 0, "err=", betaLoadErr)
    if betaLoad == nil { return }
    state.BetaEpoch, state.BetaLastSeq = betaLoad.JournalEpoch, betaLoad.LastSeq
    h.save("phase-a-beta-load", betaLoad)
    betaHist, betaHistErr := h.cli.History(context.Background(), betaID, 0, 500, int(betaLoad.JournalEpoch))
    h.check("beta-history-before-restart", betaHistErr == nil && betaHist != nil && betaHist.LastSeq == betaLoad.LastSeq, "err=", betaHistErr)
    if betaHist != nil {
        h.save("phase-a-beta-history", betaHist)
        h.check("phase-a-beta-own-assistant", rrFrameContains(betaHist, "assistant/message", rrBetaTextBefore))
        h.check("phase-a-beta-independent-seq", len(betaHist.Frames) > 0 && betaHist.Frames[0].Seq == 1)
    }
    h.check("phase-A-no-cross-talk-alpha", !rrEventsContain(alphaEvents, rrBetaTextBefore) && !rrLoadContains(alphaLoad, "", rrBetaTextBefore))
    h.check("phase-A-no-cross-talk-beta", !rrEventsContain(betaEvents, rrAlphaTextBefore) && !rrEventsContain(betaEvents, rrAlphaToolBefore) &&
        !rrLoadContains(betaLoad, "", rrAlphaTextBefore) && !rrLoadContains(betaLoad, "", rrAlphaToolBefore))
}

func (h *rrHarness) runB() {
    stateBytes, err := os.ReadFile(h.statePath)
    var state rrState
    if err == nil { err = json.Unmarshal(stateBytes, &state) }
    h.check("read-restart-state", err == nil && state.AlphaID != "" && state.BetaID != "", "err=", err)
    cookieBytes, cookieErr := os.ReadFile(h.secretPath)
    var oldCookies []*http.Cookie
    if cookieErr == nil { cookieErr = json.Unmarshal(cookieBytes, &oldCookies) }
    h.check("read-old-cookie", cookieErr == nil && len(oldCookies) > 0, "err=", cookieErr)
    if err != nil || cookieErr != nil { return }

    // 不猜旧 cookie 语义：同一次真实 apiclient List 同时记录 typed error 与 HTTP 状态。
    old := New(h.base); old.HTTP.Timeout = 30 * time.Second
    oldStatus := &rrStatusCapture{}; old.HTTP.Transport = oldStatus; old.SetCookies(oldCookies)
    oldList, oldListErr := old.List(context.Background())
    var oldAPIErr *APIError
    cookieReused := oldListErr == nil && oldStatus.last() == http.StatusOK
    cookieExpired := errors.As(oldListErr, &oldAPIErr) && oldAPIErr.Code == "unauthorized" && oldStatus.last() == http.StatusUnauthorized
    h.check("old-cookie-contract-branch", cookieReused || cookieExpired,
        "reuse=", cookieReused, " explicit401=", cookieExpired, " status=", oldStatus.last(), " err=", oldListErr)
    if cookieReused {
        foundA, foundB := false, false
        for _, s := range oldList { if s.ID == state.AlphaID { foundA = true }; if s.ID == state.BetaID { foundB = true } }
        h.check("old-cookie-reuse-list-is-real", foundA && foundB, "sessions=", len(oldList))
    }

    oldTokenClient := New(h.base); oldTokenClient.HTTP.Timeout = 30 * time.Second
    oldTokenStatus := &rrStatusCapture{}; oldTokenClient.HTTP.Transport = oldTokenStatus
    oldTokenErr := oldTokenClient.Connect(context.Background(), os.Getenv("RR_OLD_TOKEN"))
    oldTokenReused := oldTokenErr == nil && oldTokenStatus.last() == http.StatusSeeOther && len(oldTokenClient.Cookies()) > 0
    oldTokenRejected := oldTokenErr != nil && oldTokenStatus.last() == http.StatusUnauthorized && strings.Contains(oldTokenErr.Error(), "401")
    h.check("old-token-contract-branch", oldTokenReused || oldTokenRejected,
        "reuse=", oldTokenReused, " explicit401=", oldTokenRejected, " status=", oldTokenStatus.last(), " err=", oldTokenErr)

    // 无论旧凭证哪条分支成立，都用 B 的 ready token 经真实 apiclient 重新 Connect。
    h.cli = New(h.base); h.cli.HTTP.Timeout = 30 * time.Second
    connectStatus := &rrStatusCapture{}; h.cli.HTTP.Transport = connectStatus
    err = h.cli.Connect(context.Background(), os.Getenv("RR_TOKEN"))
    h.check("reconnect-new-ready-token", err == nil && connectStatus.last() == http.StatusSeeOther && len(h.cli.Cookies()) > 0,
        "status=", connectStatus.last(), " err=", err)
    if err != nil { return }
    listed, err := h.cli.List(context.Background())
    foundA, foundB := false, false
    for _, s := range listed { if s.ID == state.AlphaID { foundA = true }; if s.ID == state.BetaID { foundB = true } }
    h.check("post-restart-list-A-B", err == nil && foundA && foundB, "sessions=", len(listed), " err=", err)

    historyStatus := &rrStatusCapture{}; h.cli.HTTP.Transport = historyStatus
    _, staleErr := h.cli.History(context.Background(), state.AlphaID, state.AlphaLastSeq, 500, int(state.AlphaEpoch))
    var staleAPIErr *APIError
    h.check("old-history-watermark-stale-epoch-409", errors.As(staleErr, &staleAPIErr) && staleAPIErr.Code == "stale_epoch" &&
        staleAPIErr.Retryable && historyStatus.last() == http.StatusConflict,
        "status=", historyStatus.last(), " err=", staleErr)

    staleRec, staleCancel, staleSubErr := h.subscribe(h.cli, state.AlphaEpoch, state.AlphaID, state.AlphaLastSeq)
    h.check("old-watermark-subscribe", staleSubErr == nil, "err=", staleSubErr)
    if staleSubErr != nil { return }
    gotReset := staleRec.wait(5*time.Second, func(events []Event, _ []rrChunk, _ bool) bool { return len(rrTypes(events, "session/reset")) > 0 })
    staleEvents, _, _ := staleRec.snap(); resets := rrTypes(staleEvents, "session/reset")
    var resetData struct { JournalEpoch int64 `json:"journalEpoch"`; LastSeq int `json:"lastSeq"` }
    resetValid := gotReset && len(resets) == 1 && resets[0].Seq == 0 && json.Unmarshal(resets[0].Data, &resetData) == nil && resetData.JournalEpoch != 0
    h.check("old-watermark-session-reset", resetValid, "resets=", len(resets))
    h.save("phase-b-old-watermark-events", staleEvents)
    staleCancel()

    // reset/stale_epoch 之后才重新 load，不能用提前 load 掩盖恢复顺序。
    alphaLoad, alphaLoadErr := h.cli.Load(context.Background(), state.AlphaID, 200)
    betaLoad, betaLoadErr := h.cli.Load(context.Background(), state.BetaID, 200)
    h.check("post-reset-reload-A-B", alphaLoadErr == nil && betaLoadErr == nil && alphaLoad != nil && betaLoad != nil,
        "alphaErr=", alphaLoadErr, " betaErr=", betaLoadErr)
    if alphaLoad == nil || betaLoad == nil { return }
    h.save("phase-b-alpha-reload", alphaLoad); h.save("phase-b-beta-reload", betaLoad)
    h.check("alpha-new-journal-epoch", alphaLoad.JournalEpoch != 0 && alphaLoad.JournalEpoch != state.AlphaEpoch,
        "old=", state.AlphaEpoch, " new=", alphaLoad.JournalEpoch)
    h.check("beta-new-journal-epoch", betaLoad.JournalEpoch != 0 && betaLoad.JournalEpoch != state.BetaEpoch,
        "old=", state.BetaEpoch, " new=", betaLoad.JournalEpoch)
    h.check("reset-matches-reloaded-baseline", resetValid && resetData.JournalEpoch == alphaLoad.JournalEpoch && resetData.LastSeq == alphaLoad.LastSeq,
        "resetEpoch=", resetData.JournalEpoch, " loadEpoch=", alphaLoad.JournalEpoch,
        " resetLastSeq=", resetData.LastSeq, " loadLastSeq=", alphaLoad.LastSeq)
    h.check("reload-preserves-v4-text-tool", rrLoadContains(alphaLoad, "assistant", rrAlphaTextBefore) && rrLoadTool(alphaLoad) &&
        rrLoadContains(betaLoad, "assistant", rrBetaTextBefore))

    // 精确验任务要求：新进程 history 是否还保留重启前那四类原始帧。
    // 若为空，四条都会红；随后仍继续新 prompt，留下最小复现而不是提前收工。
    recovered, recoveredErr := h.cli.History(context.Background(), state.AlphaID, 0, 500, int(alphaLoad.JournalEpoch))
    h.check("post-restart-history-readable", recoveredErr == nil && recovered != nil && recovered.JournalEpoch == int(alphaLoad.JournalEpoch),
        "err=", recoveredErr)
    if recovered != nil {
        h.save("phase-b-alpha-history-before-new-turn", recovered)
        h.checkOriginalHistory("post-restart-history", recovered)
    } else { h.checkOriginalHistory("post-restart-history", nil) }

    liveRec, liveCancel, liveErr := h.subscribe(h.cli, alphaLoad.JournalEpoch, state.AlphaID, alphaLoad.LastSeq)
    h.check("post-reset-correct-subscribe", liveErr == nil, "err=", liveErr)
    if liveErr != nil { return }
    defer liveCancel()
    newEnd, _ := h.prompt(liveRec, state.AlphaID, "Reply with exactly "+rrAlphaTextAfter+" and nothing else. Do not call any tool.", alphaLoad.LastSeq, false)
    liveEvents, liveChunks, _ := liveRec.snap()
    h.check("post-restart-new-prompt-done-text", newEnd.Kind == "turn/done" && rrText(liveChunks, rrAlphaTextAfter), "end=", newEnd.Kind)
    oldReplay := false
    for _, ev := range liveEvents { if ev.Seq > 0 && ev.Seq <= alphaLoad.LastSeq { oldReplay = true } }
    h.check("post-reset-no-stale-replay", !oldReplay, "baseline=", alphaLoad.LastSeq)
    h.save("phase-b-alpha-new-events", liveEvents)

    finalAlphaLoad, finalAlphaErr := h.cli.Load(context.Background(), state.AlphaID, 200)
    finalBetaLoad, finalBetaErr := h.cli.Load(context.Background(), state.BetaID, 200)
    h.check("post-new-turn-load", finalAlphaErr == nil && finalBetaErr == nil && finalAlphaLoad != nil && finalBetaLoad != nil &&
        rrLoadContains(finalAlphaLoad, "assistant", rrAlphaTextAfter), "alphaErr=", finalAlphaErr, " betaErr=", finalBetaErr)
    finalHist, finalHistErr := h.cli.History(context.Background(), state.AlphaID, 0, 500, int(alphaLoad.JournalEpoch))
    h.check("post-new-turn-history-raw-text-done", finalHistErr == nil && finalHist != nil &&
        rrFrameContains(finalHist, "assistant/message", rrAlphaTextAfter) && rrFrameContains(finalHist, "turn/done", "") &&
        rrFrameContains(finalHist, "turn/end", ""), "err=", finalHistErr)
    if finalHist != nil { h.save("phase-b-alpha-history-after-new-turn", finalHist) }
    h.check("post-restart-A-B-no-cross-talk", finalBetaLoad != nil &&
        !rrLoadContains(finalBetaLoad, "", rrAlphaTextBefore) && !rrLoadContains(finalBetaLoad, "", rrAlphaToolBefore) &&
        !rrLoadContains(finalBetaLoad, "", rrAlphaTextAfter) && rrLoadContains(finalBetaLoad, "assistant", rrBetaTextBefore))
    h.save("phase-b-final-summary-state", map[string]any{
        "alphaId": state.AlphaID, "betaId": state.BetaID,
        "alphaOldEpoch": state.AlphaEpoch, "alphaNewEpoch": alphaLoad.JournalEpoch,
        "alphaOldLastSeq": state.AlphaLastSeq, "alphaBaselineLastSeq": alphaLoad.LastSeq,
        "alphaFinalLastSeq": func() int { if finalAlphaLoad == nil { return -1 }; return finalAlphaLoad.LastSeq }(),
        "betaOldEpoch": state.BetaEpoch, "betaNewEpoch": betaLoad.JournalEpoch,
        "betaOldLastSeq": state.BetaLastSeq, "betaBaselineLastSeq": betaLoad.LastSeq,
        "oldCookieReuse": cookieReused, "oldCookieExplicit401": cookieExpired,
        "oldTokenReuse": oldTokenReused, "oldTokenExplicit401": oldTokenRejected,
    })
}
'''


def native_image(pid: int) -> str:
    """返回 PID 完整映像路径，只用于核对本脚本启动的 dshd。"""
    if os.name != "nt":
        return ""
    kernel32 = ctypes.windll.kernel32
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    handle = kernel32.OpenProcess(0x1000, False, pid)
    if not handle:
        return ""
    try:
        size = wintypes.DWORD(32768)
        buf = ctypes.create_unicode_buffer(size.value)
        ok = kernel32.QueryFullProcessImageNameW(handle, 0, buf, ctypes.byref(size))
        return buf.value if ok else ""
    finally:
        kernel32.CloseHandle(handle)


def go_executable() -> str:
    standard = Path(r"C:\Program Files\Go\bin\go.exe")
    if standard.is_file():
        return str(standard)
    from shutil import which
    found = which("go")
    if not found:
        raise RuntimeError("找不到 go.exe")
    return found


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def git_output(cwd: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=cwd, text=True, encoding="utf-8",
                          errors="replace", capture_output=True, check=True).stdout.strip()


def home_shape(home: Path) -> list[dict]:
    out: list[dict] = []
    if not home.exists():
        return out
    for base, dirs, files in os.walk(home):
        dirs.sort(); files.sort()
        rel = Path(base).relative_to(home)
        for name in files:
            p = Path(base) / name
            try:
                size = p.stat().st_size
            except OSError:
                size = -1
            out.append({"path": str(rel / name), "bytes": size})
    return out


def v4_snapshot(home: Path) -> dict[str, bytes]:
    result: dict[str, bytes] = {}
    if not home.exists():
        return result
    for path in sorted(home.rglob("*.v4.jsonl.zstd")):
        result[str(path.relative_to(home))] = path.read_bytes()
    return result


def v4_manifest(home: Path, snapshot: dict[str, bytes]) -> list[dict]:
    return [{"path": rel, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}
            for rel, data in sorted(snapshot.items())]


class Driver:
    def __init__(self, run: Path):
        self.run = run
        self.ok = 0
        self.fail = 0
        self.commands: dict[str, object] = {}
        self.processes: dict[str, object] = {}

    def check(self, name: str, cond: bool, detail: object = "") -> None:
        if cond:
            self.ok += 1
            print(f"DRIVER_OK {name} {detail}", flush=True)
        else:
            self.fail += 1
            print(f"DRIVER_FAIL {name} {detail}", flush=True)


class SkipRun(Exception):
    pass


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--binary", default=str(ROOT / "tmp" / "restart-recovery-bin" / "dshd.exe"))
    ap.add_argument("--ui", default=str(DEFAULT_UI))
    ap.add_argument("--evidence-dir", help="已存在的空证据目录；默认新建 tmp/restart-recovery-*")
    ap.add_argument("--model", default=os.environ.get("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"))
    ap.add_argument("--base", default=os.environ.get("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1"))
    ap.add_argument("--timeout", type=int, default=600, help="每个 go test 阶段的硬超时（秒）")
    args = ap.parse_args()

    binary = Path(args.binary).resolve()
    ui = Path(args.ui).resolve()
    if args.evidence_dir:
        run = Path(args.evidence_dir).resolve()
        run.mkdir(parents=True, exist_ok=True)
    else:
        run = Path(tempfile.mkdtemp(prefix="restart-recovery-", dir=str(ROOT / "tmp"))).resolve()
    home = run / "home"
    state = run / "restart-state.json"
    secret = run / SECRET_NAME
    driver = Driver(run)
    phase_counts: list[tuple[int, int, int]] = []
    hosts: dict[str, dict] = {}
    go_procs: dict[str, subprocess.Popen] = {}
    skip = 0
    started = dt.datetime.now(dt.timezone.utc).isoformat()
    print(f"EVIDENCE {run}", flush=True)

    def start_host(label: str) -> dict:
        argv = [str(binary), "serve", "--home", str(home), "--addr", "127.0.0.1:0",
                "--policy", "ask", "--reasoning", "max", "--max-steps", "4",
                "--model", args.model, "--parent-pid", str(os.getpid())]
        driver.commands[f"dshd_{label}_argv"] = argv
        log_path = run / f"dshd-{label}-stderr.log"
        log = log_path.open("w", encoding="utf-8", errors="replace")
        env = os.environ.copy()
        env.update({"DSHD_LLM_BASE": args.base, "DSHD_LLM_MODEL": args.model,
                    "PYTHONDONTWRITEBYTECODE": "1"})
        proc = subprocess.Popen(argv, cwd=ROOT, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=log, text=True,
                                encoding="utf-8", errors="replace", env=env,
                                creationflags=0x08000000 if os.name == "nt" else 0)
        host = {"proc": proc, "log": log, "log_path": log_path, "stopped": False,
                "pid": proc.pid, "argv": argv}
        hosts[label] = host
        lines: queue.Queue[str] = queue.Queue()
        threading.Thread(target=lambda: lines.put(proc.stdout.readline()), daemon=True).start()
        try:
            ready_line = lines.get(timeout=25).strip()
        except queue.Empty as exc:
            raise RuntimeError(f"dshd {label} 25 秒内未输出 ready 行") from exc
        match = re.fullmatch(r"ready (\{.*\})", ready_line)
        if not match:
            raise RuntimeError(f"dshd {label} ready 行非法：{ready_line!r}")
        ready = json.loads(match.group(1))
        if not ready.get("url", "").startswith("http://127.0.0.1:") or not ready.get("token"):
            raise RuntimeError(f"dshd {label} ready 缺回环 url/token")
        host["ready"] = ready
        host["image"] = native_image(proc.pid)
        (run / f"ready-{label}-redacted.json").write_text(json.dumps({
            "url": ready["url"], "token": "<redacted>", "pid": proc.pid,
            "image": host["image"], "home": str(home),
        }, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"DSHD_{label} pid={proc.pid} url={ready['url']} token=<redacted>", flush=True)
        return host

    def stop_host(label: str, reason: str) -> None:
        host = hosts.get(label)
        if not host or host["stopped"]:
            return
        proc: subprocess.Popen = host["proc"]
        record: dict[str, object] = {"pid": proc.pid, "reason": reason,
                                     "expected_image": str(binary)}
        image = native_image(proc.pid) if proc.poll() is None else ""
        owned = bool(image) and os.path.normcase(image) == os.path.normcase(str(binary))
        record["image"] = image
        record["owner_verified"] = owned
        if proc.poll() is None and owned and os.name == "nt":
            cmd = ["taskkill", "/PID", str(proc.pid), "/T", "/F"]
            record["command"] = cmd
            killed = subprocess.run(cmd, capture_output=True, text=True,
                                    encoding="mbcs", errors="replace")
            record["taskkill_exit"] = killed.returncode
            record["taskkill_output"] = (killed.stdout + killed.stderr).strip()
        elif proc.poll() is None and owned:
            proc.kill()
        if proc.poll() is None and owned:
            try:
                proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                record["wait_timeout"] = True
        record["exit"] = proc.poll()
        host["stopped"] = True
        host["log"].close()
        driver.processes[f"dshd_{label}"] = record
        driver.check(f"{label}-owned-host-terminated", owned and proc.poll() is not None and
                     (proc.poll() is not None and record.get("taskkill_exit", 0) == 0), record)
        print(f"DSHD_{label}_CLEANUP " + json.dumps(record, ensure_ascii=False), flush=True)

    def run_phase(phase: str, host: dict, old_token: str = "") -> None:
        go_cmd = [go_executable(), "test", "-overlay=" + str(run / "overlay.json"),
                  "./internal/apiclient", "-run", "^TestRestartRecovery$", "-count=1",
                  "-v", "-timeout", "10m"]
        driver.commands[f"go_test_{phase}_argv"] = go_cmd
        env = os.environ.copy()
        env.update({"RR_PHASE": phase, "RR_BASE": host["ready"]["url"],
                    "RR_TOKEN": host["ready"]["token"], "RR_OLD_TOKEN": old_token,
                    "RR_HOME": str(home), "RR_OUT": str(run), "RR_STATE": str(state),
                    "RR_SECRET": str(secret), "DSHD_LLM_BASE": args.base,
                    "DSHD_LLM_MODEL": args.model, "PYTHONDONTWRITEBYTECODE": "1"})
        proc = subprocess.Popen(go_cmd, cwd=ui, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                text=True, encoding="utf-8", errors="replace", env=env,
                                creationflags=0x08000000 if os.name == "nt" else 0)
        go_procs[phase] = proc
        timed_out = False
        try:
            output, _ = proc.communicate(timeout=args.timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            proc.kill()
            output, _ = proc.communicate(timeout=10)
        (run / f"phase-{phase}.log").write_text(output, encoding="utf-8")
        (run / f"phase-{phase}-exit.txt").write_text(str(proc.returncode) + "\n", encoding="utf-8")
        print(output, end="", flush=True)
        summaries = SUMMARY_RE.findall(output)
        if not summaries:
            driver.check(f"phase-{phase}-machine-summary-present", False,
                         f"go_test_exit={proc.returncode} timeout={timed_out}")
            return
        _, ok_s, fail_s, skip_s = summaries[-1]
        ok_n, fail_n, skip_n = int(ok_s), int(fail_s), int(skip_s)
        phase_counts.append((ok_n, fail_n, skip_n))
        driver.check(f"phase-{phase}-summary-nonzero", ok_n > 0, summaries[-1])
        driver.check(f"phase-{phase}-go-test-exit", proc.returncode == 0 and not timed_out,
                     f"exit={proc.returncode} timeout={timed_out}")
        driver.processes[f"go_test_{phase}"] = {"pid": proc.pid, "exit": proc.returncode,
                                               "timeout": timed_out}

    exit_code = 1
    try:
        if not binary.is_file() or not ui.is_dir():
            raise RuntimeError(f"binary/ui 不存在：binary={binary} ui={ui}")
        (run / "host-status-at-start.txt").write_text(git_output(ROOT, "status", "--porcelain") + "\n", encoding="utf-8")
        (run / "ui-status-at-start.txt").write_text(git_output(ui, "status", "--porcelain") + "\n", encoding="utf-8")
        (run / "revisions.json").write_text(json.dumps({
            "dshd_rev": git_output(ROOT, "rev-parse", "HEAD"),
            "ui_rev": git_output(ui, "rev-parse", "HEAD"),
            "binary": str(binary), "binary_sha256": sha256(binary), "ui": str(ui),
            "started_utc": started,
        }, ensure_ascii=False, indent=2), encoding="utf-8")

        gate = subprocess.run([sys.executable, str(ROOT / "scripts" / "bin-check.py"), str(binary)],
                              cwd=ROOT, text=True, encoding="utf-8", errors="replace",
                              capture_output=True)
        (run / "driver-bin-check.log").write_text(gate.stdout + gate.stderr, encoding="utf-8")
        print(gate.stdout, end="", flush=True)
        driver.check("bin-check-current-binary", gate.returncode == 0, f"exit={gate.returncode}")
        if gate.returncode != 0:
            raise RuntimeError("bin-check 非零，立即中止")

        try:
            with urllib.request.urlopen(args.base.rstrip("/") + "/models", timeout=8) as resp:
                catalog = json.load(resp)
            models = [x.get("id", "") for x in catalog.get("data", [])]
        except Exception as exc:
            print(f"SKIP magpie 不可达：{exc}", flush=True)
            skip = 1
            raise SkipRun()
        (run / "models.json").write_text(json.dumps({"base": args.base, "models": models},
                                                   ensure_ascii=False, indent=2), encoding="utf-8")
        if args.model not in models:
            print(f"SKIP magpie 模型目录缺少 {args.model}；实际={models}", flush=True)
            skip = 1
            raise SkipRun()

        # 证据留在宿主 tmp/；用 .go.txt 避免宿主 `go test ./...`
        # 把 UI overlay 探针误收进本仓 module。
        probe_path = run / "restart_recovery_test.go.txt"
        probe_path.write_text(GO_PROBE, encoding="utf-8")
        overlay = run / "overlay.json"
        target = ui / "internal" / "apiclient" / "restart_recovery_test.go"
        overlay.write_text(json.dumps({"Replace": {str(target): str(probe_path)}}, ensure_ascii=False),
                           encoding="utf-8")
        home.mkdir(exist_ok=False)
        driver.commands["shape"] = {"home": str(home), "addr": "127.0.0.1:0",
                                    "policy": "ask", "reasoning": "max", "max_steps": 4,
                                    "gui": False, "mockd": False, "acp": False}

        host_a = start_host("A")
        run_phase("A", host_a)
        before = v4_snapshot(home)
        (run / "v4-before.json").write_text(json.dumps(v4_manifest(home, before),
                                                   ensure_ascii=False, indent=2), encoding="utf-8")
        driver.check("phase-A-two-v4-logs", len(before) == 2, sorted(before))
        state_valid = False
        try:
            state_data = json.loads(state.read_text(encoding="utf-8"))
            state_valid = bool(state_data.get("alphaId") and state_data.get("betaId")) and secret.is_file()
        except Exception:
            state_valid = False
        if not state_valid:
            driver.check("restart-prerequisites", False, "缺 state/cookie，仍清理 A，不伪造 B 结果")
        else:
            stop_host("A", "intentional cross-process restart")
            host_b = start_host("B")
            run_phase("B", host_b, old_token=host_a["ready"]["token"])
            after = v4_snapshot(home)
            (run / "v4-after.json").write_text(json.dumps(v4_manifest(home, after),
                                                     ensure_ascii=False, indent=2), encoding="utf-8")
            prefix_rows = []
            all_prefix = len(before) == 2 and len(after) == 2
            for rel, old in before.items():
                new = after.get(rel)
                row = {"path": rel, "beforeBytes": len(old),
                       "afterBytes": len(new) if new is not None else -1,
                       "oldPrefixUnchanged": new is not None and new[:len(old)] == old}
                all_prefix = all_prefix and row["oldPrefixUnchanged"]
                prefix_rows.append(row)
            driver.check("v4-append-only-old-prefix-unchanged", all_prefix, prefix_rows)
            alpha_rel = next((rel for rel in before if state_data["alphaId"] in rel), "")
            beta_rel = next((rel for rel in before if state_data["betaId"] in rel), "")
            alpha_appended = bool(alpha_rel) and len(after.get(alpha_rel, b"")) > len(before[alpha_rel])
            beta_unchanged = bool(beta_rel) and after.get(beta_rel) == before[beta_rel]
            driver.check("v4-only-alpha-appended-beta-not-rewritten", alpha_appended and beta_unchanged,
                         {"alpha": alpha_rel, "alphaAppended": alpha_appended,
                          "beta": beta_rel, "betaUnchanged": beta_unchanged})
            (run / "v4-prefix-check.json").write_text(json.dumps({
                "rows": prefix_rows, "alphaAppended": alpha_appended,
                "betaUnchanged": beta_unchanged,
            }, ensure_ascii=False, indent=2), encoding="utf-8")
    except SkipRun:
        pass
    except Exception as exc:
        driver.check("driver-exception", False, f"{type(exc).__name__}: {exc}")
    finally:
        for label in ("B", "A"):
            try:
                stop_host(label, "final cleanup")
            except Exception as exc:
                driver.check(f"cleanup-{label}-exception", False, f"{type(exc).__name__}: {exc}")
        for phase, proc in go_procs.items():
            if proc.poll() is None:
                proc.kill()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    pass
        # 删除前核对唯一、固定的本证据目录路径，绝不对未核路径做删除。
        secret_removed = False
        try:
            if secret.name == SECRET_NAME and secret.parent.resolve() == run.resolve():
                secret.unlink(missing_ok=True)
                secret_removed = not secret.exists()
        except Exception as exc:
            driver.check("secret-removal-exception", False, f"{type(exc).__name__}: {exc}")
        driver.check("transient-cookie-artifact-removed", secret_removed, str(secret))
        (run / "home-shape.json").write_text(json.dumps(home_shape(home), ensure_ascii=False, indent=2),
                                            encoding="utf-8")
        try:
            (run / "ui-status-at-end.txt").write_text(git_output(ui, "status", "--porcelain") + "\n", encoding="utf-8")
        except Exception as exc:
            driver.check("ui-final-status", False, f"{type(exc).__name__}: {exc}")
        (run / "commands.json").write_text(json.dumps(driver.commands, ensure_ascii=False, indent=2),
                                           encoding="utf-8")
        (run / "processes.json").write_text(json.dumps(driver.processes, ensure_ascii=False, indent=2),
                                            encoding="utf-8")
        ok = driver.ok + sum(x[0] for x in phase_counts)
        fail = driver.fail + sum(x[1] for x in phase_counts)
        skip += sum(x[2] for x in phase_counts)
        if ok == 0 and skip == 0 and fail == 0:
            fail = 1
        if fail == 0 and skip == 0:
            exit_code = 0
        elif skip > 0 and fail == 0:
            exit_code = 2
        summary = f"OK={ok} FAIL={fail} SKIP={skip}"
        (run / "summary.txt").write_text(summary + f"\nexit={exit_code}\n", encoding="utf-8")
        (run / "result.json").write_text(json.dumps({
            "ok": ok, "fail": fail, "skip": skip, "exit": exit_code,
            "phaseCount": len(phase_counts), "home": str(home),
            "finishedUtc": dt.datetime.now(dt.timezone.utc).isoformat(),
        }, ensure_ascii=False, indent=2), encoding="utf-8")
        print(summary, flush=True)
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
