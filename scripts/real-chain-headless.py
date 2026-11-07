#!/usr/bin/env python3
"""REAL-CHAIN-HEADLESS：同一新 dshd + 真实 UI internal/apiclient 的无窗整链。

Go 探针通过 `go test -overlay` 映射进 UI 的 internal/apiclient 包；UI 工作树不被修改。
宿主使用临时 home、OS 分配端口（--addr 127.0.0.1:0）和一次性 ready token。
只回收本脚本启动且已核对映像路径的 dshd PID 树；不启动任何 GUI。

用法：
  python scripts/real-chain-headless.py

证据默认写入 tmp/real-chain-current-<随机>/。token/cookie/credential 不写日志。
同一 dshd 先跑 W36 核心（原72项+MSGID），再跑当前能力闭环。
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
import secrets
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_UI = Path(r"C:\Users\Administrator\AppData\Local\Temp\dsh-ui-current-chain")
SUMMARY_RE = re.compile(r"RCH_SUMMARY OK=(\d+) FAIL=(\d+) SKIP=(\d+)")
CURRENT_SUMMARY_RE = re.compile(r"RCC_SUMMARY OK=(\d+) FAIL=(\d+) SKIP=(\d+)")

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

    "github.com/coder/websocket"
)

type rchChunk struct { kind string; chunk Chunk }
type rchRecorder struct {
    mu sync.Mutex
    events []Event
    chunks []rchChunk
    closed bool
    notify chan struct{}
}
func newRCHRecorder() *rchRecorder { return &rchRecorder{notify: make(chan struct{}, 1)} }
func (r *rchRecorder) signal() { select { case r.notify <- struct{}{}: default: } }
func (r *rchRecorder) add(ev Event) {
    r.mu.Lock()
    r.events = append(r.events, ev)
    for _, c := range ev.Chunks() { r.chunks = append(r.chunks, rchChunk{ev.Kind, c}) }
    r.mu.Unlock()
    r.signal()
}
func (r *rchRecorder) run(ch <-chan Event) {
    for ev := range ch { r.add(ev) }
    r.mu.Lock(); r.closed = true; r.mu.Unlock(); r.signal()
}
func (r *rchRecorder) snap() ([]Event, []rchChunk, bool) {
    r.mu.Lock(); defer r.mu.Unlock()
    ev := append([]Event(nil), r.events...)
    ch := append([]rchChunk(nil), r.chunks...)
    return ev, ch, r.closed
}
func (r *rchRecorder) wait(d time.Duration, pred func([]Event, []rchChunk, bool) bool) bool {
    deadline := time.Now().Add(d)
    for {
        ev, ch, closed := r.snap()
        if pred(ev, ch, closed) { return true }
        if time.Now().After(deadline) { return false }
        select { case <-r.notify: case <-time.After(100 * time.Millisecond): }
    }
}
func rchEndAfter(ev []Event, since int) (Event, bool) {
    // v4 turn/end 是日志结局；Transport turn/done|turn/error 才是 runner 已收工。
    // 这里等后者，另在 history 断言中检查前者，不能把两种帧混为一个水位。
    for _, e := range ev { if e.Seq > since && (e.Kind == "turn/done" || e.Kind == "turn/error") { return e, true } }
    return Event{}, false
}
func rchHasTypeAfter(ev []Event, typ string, since int) bool {
    for _, e := range ev { if e.Seq > since && e.Kind == typ { return true } }
    return false
}
func rchLastSeq(ev []Event) int { n := 0; for _, e := range ev { if e.Seq > n { n = e.Seq } }; return n }
func rchText(ch []rchChunk, marker string) bool {
    for _, c := range ch { if c.chunk.Block == BlockText && strings.Contains(c.chunk.Text, marker) { return true } }
    return false
}
func rchReasoning(ch []rchChunk) int {
    n := 0; for _, c := range ch { if c.chunk.Block == BlockReasoning { n += len(c.chunk.Text) } }; return n
}
func rchTools(ch []rchChunk) int {
    n := 0; for _, c := range ch { if c.chunk.Block == BlockTool { n++ } }; return n
}
func rchRaw(ev []Event, marker string) bool {
    for _, e := range ev { if strings.Contains(string(e.Data), marker) { return true } }
    return false
}
func rchType(ev []Event, typ string) []Event {
    out := []Event{}; for _, e := range ev { if e.Kind == typ { out = append(out, e) } }; return out
}
type rchIdentity struct { role string; v4 int64; messageID string }
func rchHistoryIdentities(hist *HistoryResult) ([]rchIdentity, bool) {
    out := []rchIdentity{}; valid := hist != nil
    if hist == nil { return nil, false }
    for _, f := range hist.Frames {
        if f.Type != "user/message" && f.Type != "assistant/message" { continue }
        var d struct {
            Role string `json:"role"`; V4Seq *int64 `json:"v4Seq"`; MessageID string `json:"messageId"`
            Message struct{ Role string `json:"role"` } `json:"message"`
        }
        if json.Unmarshal(f.Data, &d) != nil || d.V4Seq == nil { valid = false; continue }
        role := d.Role; if role == "" { role = d.Message.Role }
        if role != f.Type[:len(f.Type)-len("/message")] { valid = false }
        out = append(out, rchIdentity{role, *d.V4Seq, d.MessageID})
    }
    return out, valid && len(out) > 0
}
func rchLoadIdentities(load *LoadResult) ([]rchIdentity, bool) {
    out := []rchIdentity{}; valid := load != nil
    if load == nil { return nil, false }
    for _, m := range load.Messages {
        if m.Role != "user" && m.Role != "assistant" { continue }
        if m.V4Seq == nil { valid = false; continue }
        out = append(out, rchIdentity{m.Role, *m.V4Seq, m.MessageID})
    }
    return out, valid && len(out) > 0
}
func rchSameIdentities(a, b []rchIdentity) bool {
    if len(a) != len(b) { return false }
    for i := range a { if a[i] != b[i] { return false } }
    return true
}

var rchSharedCookies []*http.Cookie

type rchHarness struct {
    t *testing.T
    cli *Client
    base string
    home string
    out string
    ok, fail int
}
func (h *rchHarness) check(name string, cond bool, detail ...any) {
    msg := fmt.Sprint(detail...)
    if cond { h.ok++; fmt.Printf("RCH_OK %s %s\n", name, msg) } else { h.fail++; fmt.Printf("RCH_FAIL %s %s\n", name, msg) }
}
func (h *rchHarness) saveHistory(name string, r *HistoryResult) {
    b, err := json.MarshalIndent(r, "", "  ")
    if err == nil { _ = os.WriteFile(filepath.Join(h.out, name+".json"), b, 0644) }
}
func (h *rchHarness) mux(epoch int64, capture **websocket.Conn) *Mux {
    m := NewMux(h.base)
    m.Cookies = h.cli.Cookies()
    m.EpochOf = func(string) int64 { return epoch }
    m.PingEvery = 250 * time.Millisecond
    if capture != nil {
        m.Dial = func(ctx context.Context, urlStr string) (*websocket.Conn, error) {
            hdr := http.Header{}
            for _, ck := range h.cli.Cookies() { hdr.Add("Cookie", ck.Name+"="+ck.Value) }
            c, _, err := websocket.Dial(ctx, urlStr, &websocket.DialOptions{HTTPHeader: hdr})
            if err == nil { *capture = c }
            return c, err
        }
    }
    return m
}
func (h *rchHarness) subscribe(m *Mux, id string, since int) (*rchRecorder, context.CancelFunc, error) {
    ctx, cancel := context.WithCancel(context.Background())
    ch, err := m.Subscribe(ctx, "session:"+id, since)
    if err != nil { cancel(); return nil, func(){}, err }
    r := newRCHRecorder(); go r.run(ch)
    return r, cancel, nil
}
func (h *rchHarness) create(title string) (string, *LoadResult) {
    id, err := h.cli.Create(context.Background(), "", "default", title)
    h.check("create/"+title, err == nil && id != "", "id=", id, " err=", err)
    if err != nil || id == "" { return "", nil }
    ld, err := h.cli.Load(context.Background(), id, 100)
    h.check("initial-load/"+title, err == nil && ld != nil && ld.JournalEpoch != 0 && ld.Permission == "ask",
        "epoch/permission err=", err)
    return id, ld
}
func (h *rchHarness) waitHistoryEnd(id string, since, epoch int, d time.Duration) (*HistoryResult, bool) {
    deadline := time.Now().Add(d)
    var last *HistoryResult
    for time.Now().Before(deadline) {
        r, err := h.cli.History(context.Background(), id, since, 200, epoch)
        if err == nil {
            last = r
            ev := make([]Event, 0, len(r.Frames))
            for _, f := range r.Frames { ev = append(ev, f.Event("session:"+id)) }
            if _, ok := rchEndAfter(ev, since); ok { return r, true }
        }
        time.Sleep(250 * time.Millisecond)
    }
    return last, false
}
func (h *rchHarness) approvals(r *rchRecorder, id, kind string, timeout time.Duration) int {
    seen := map[string]bool{}
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        ev, _, closed := r.snap()
        for _, e := range rchType(ev, "approval/request") {
            req, err := ParseApprovalRequest(e.Data)
            if err != nil { h.check("approval-parse/"+id, false, err); continue }
            if seen[req.RequestID] { continue }
            seen[req.RequestID] = true
            opt := req.OptionID(kind)
            h.check("approval-option/"+kind, opt != "", " request=", req.RequestID)
            if opt == "" { continue }
            err = h.cli.Approve(context.Background(), req.RequestID, opt)
            h.check("approval-answer/"+kind, err == nil, " err=", err)
        }
        if _, done := rchEndAfter(ev, 0); done { return len(seen) }
        if closed { return len(seen) }
        time.Sleep(100 * time.Millisecond)
    }
    return len(seen)
}

func TestRealChainHeadless(t *testing.T) {
    h := &rchHarness{t: t, base: os.Getenv("RCH_BASE"), home: os.Getenv("RCH_HOME"), out: os.Getenv("RCH_OUT")}
    defer func() {
        fmt.Printf("RCH_SUMMARY OK=%d FAIL=%d SKIP=0\n", h.ok, h.fail)
        if h.fail > 0 { t.Errorf("真实无窗整链有 %d 条 FAIL", h.fail) }
    }()
    if h.base == "" || h.home == "" || h.out == "" {
        h.check("required-env", false, "RCH_BASE/RCH_HOME/RCH_OUT"); return
    }
    h.cli = New(h.base)
    h.cli.HTTP.Timeout = 30 * time.Second
    err := h.cli.Connect(context.Background(), os.Getenv("RCH_TOKEN"))
    h.check("connect-ready-token", err == nil && len(h.cli.Cookies()) > 0, "err=", err)
    if err != nil { return }
    rchSharedCookies = h.cli.Cookies()
    second := New(h.base); second.HTTP.Timeout = 5 * time.Second
    err = second.Connect(context.Background(), os.Getenv("RCH_TOKEN"))
    h.check("ready-token-single-use", err != nil, "err=", err)

    // 主链：create/list/load/prompt/text/reasoning/history。
    mainID, mainLoad := h.create("main")
    if mainID == "" || mainLoad == nil { return }
    epoch := int(mainLoad.JournalEpoch)
    listed, err := h.cli.List(context.Background())
    found := false; for _, s := range listed { if s.ID == mainID { found = true } }
    h.check("list-contains-main", err == nil && found, "sessions=", len(listed), " err=", err)
    var mainWS *websocket.Conn
    mainRec, mainCancel, err := h.subscribe(h.mux(int64(epoch), &mainWS), mainID, 0)
    h.check("subscribe-main", err == nil, "err=", err)
    if err != nil { return }
    defer mainCancel()
    textMarker := "RC_HEADLESS_TEXT_OK"
    turn1, err := h.cli.Prompt(context.Background(), mainID, "Reply with exactly "+textMarker+" and nothing else. Do not call any tool.")
    h.check("prompt-main", err == nil && turn1 != "", "turn=", turn1, " err=", err)
    got := mainRec.wait(180*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { _, ok := rchEndAfter(ev, 0); return ok })
    ev, ch, _ := mainRec.snap()
    end, ended := rchEndAfter(ev, 0)
    h.check("main-turn-ended", got && ended && end.Kind == "turn/done" && rchHasTypeAfter(ev, "turn/end", 0), "end=", end.Kind)
    h.check("real-text-through-ui-chunks", rchText(ch, textMarker), "chunks=", len(ch))
    mainLoaded, err := h.cli.Load(context.Background(), mainID, 100)
    h.check("load-after-prompt", err == nil && mainLoaded != nil && mainLoaded.LastSeq > 0 && int(mainLoaded.JournalEpoch) == epoch,
        "lastSeq/epoch err=", err)
    if mainLoaded != nil {
        userOK, assistantOK := false, false
        for _, m := range mainLoaded.Messages {
            if m.Role == "user" && strings.Contains(m.Text, textMarker) { userOK = true }
            if m.Role == "assistant" && strings.Contains(m.Text, textMarker) { assistantOK = true }
        }
        h.check("load-user-and-assistant", userOK && assistantOK, "messages=", len(mainLoaded.Messages))
        h.check("load-policy", mainLoaded.Permission == "ask", "permission=", mainLoaded.Permission)
        loadIDs, loadIDsOK := rchLoadIdentities(mainLoaded)
        userID, assistantInventedID := false, false
        for _, m := range mainLoaded.Messages {
            if m.Role == "user" && m.MessageID != "" { userID = true }
            if m.Role == "assistant" && m.MessageID != "" { assistantInventedID = true }
        }
        h.check("msgid-load-v4seq-present-including-zero", loadIDsOK, "identities=", loadIDs)
        h.check("msgid-load-message-id-only-passthrough", userID && !assistantInventedID, "userID=", userID, " assistantInventedID=", assistantInventedID)
        // 当前 run 日志没有 request/context，model 投影为空；如实记录但不把 H4 混入本链门禁。
        fmt.Printf("RCH_NOTE load-model-field model=%q permission=%q（model 未纳入本探针判据）\n", mainLoaded.Model, mainLoaded.Permission)
    }
    hist, err := h.cli.History(context.Background(), mainID, 0, 200, epoch)
    h.check("history-raw-frames", err == nil && hist != nil && len(hist.Frames) > 0, "err=", err)
    if hist != nil {
        h.saveHistory("history-main-initial", hist)
        continuous, hasDone, hasV4End, hasAssistant := len(hist.Frames) > 0, false, false, false
        for i, f := range hist.Frames {
            if f.Seq != i+1 || f.Type == "" || f.Time <= 0 || len(f.Data) == 0 { continuous = false }
            if f.Type == "turn/done" { hasDone = true }
            if f.Type == "turn/end" { hasV4End = true }
            if f.Type == "assistant/message" { hasAssistant = true }
        }
        h.check("history-seq-time-data", continuous && hasDone, "frames=", len(hist.Frames))
        h.check("history-preserves-assistant-and-turn-end", hasAssistant && hasV4End, "frames=", len(hist.Frames))
        historyIDs, historyIDsOK := rchHistoryIdentities(hist)
        loadIDs, loadIDsOK := rchLoadIdentities(mainLoaded)
        separate := false
        for _, f := range hist.Frames {
            if f.Type != "user/message" && f.Type != "assistant/message" { continue }
            var d struct{ V4Seq *int64 `json:"v4Seq"` }
            if json.Unmarshal(f.Data, &d) == nil && d.V4Seq != nil && f.Seq != int(*d.V4Seq) { separate = true }
        }
        h.check("msgid-history-real-v4seq-not-transport-seq", historyIDsOK && separate, "identities=", historyIDs)
        h.check("msgid-load-history-identities-exact", loadIDsOK && rchSameIdentities(loadIDs, historyIDs), "load=", loadIDs, " history=", historyIDs)
        merged := append([]Message(nil), mainLoaded.Messages...)
        for i := len(hist.Frames)-1; i >= 0; i-- {
            f := hist.Frames[i]
            if f.Type != "user/message" && f.Type != "assistant/message" { continue }
            var stream Stream
            stream.Apply(f.Event("session:"+mainID))
            merged = Merge(merged, &stream)
        }
        mergedIDs, mergedIDsOK := rchLoadIdentities(&LoadResult{Messages: merged})
        h.check("msgid-real-load-ws-late-replay-idempotent", mergedIDsOK && rchSameIdentities(loadIDs, mergedIDs), "merged=", mergedIDs)
    }
    _, staleErr := h.cli.History(context.Background(), mainID, 0, 1, epoch+1)
    var apiErr *APIError
    h.check("stale-epoch-typed-error", errors.As(staleErr, &apiErr) && apiErr.Code == "stale_epoch" && apiErr.Retryable,
        "err=", staleErr)

    // 强制关闭真实 WS，断线期间跑一轮；只用 history/FillGap 补缺失 seq。
    if mainWS == nil { h.check("capture-real-ws", false); return }
    _ = mainWS.CloseNow()
    closed := mainRec.wait(5*time.Second, func(_ []Event, _ []rchChunk, closed bool) bool { return closed })
    h.check("real-ws-drop-closes-channel", closed)
    ev, _, _ = mainRec.snap(); watermark := rchLastSeq(ev)
    reconnectMarker := "RC_HEADLESS_GAP_OK"
    _, err = h.cli.Prompt(context.Background(), mainID, "Reply with exactly "+reconnectMarker+" and nothing else. Do not call any tool.")
    h.check("prompt-while-disconnected", err == nil, "err=", err)
    disconnectedHist, done := h.waitHistoryEnd(mainID, watermark, epoch, 180*time.Second)
    h.check("disconnected-turn-finished", done && disconnectedHist != nil && disconnectedHist.LastSeq > watermark,
        "watermark=", watermark)
    if !done || disconnectedHist == nil { return }
    lastSeq := disconnectedHist.LastSeq
    gapRec := newRCHRecorder()
    var filled []int
    n, fillErr := FillGap(context.Background(), h.cli, mainID, watermark+1, lastSeq, epoch, func(f Frame) {
        filled = append(filled, f.Seq); gapRec.add(f.Event("session:"+mainID))
    })
    exact := n == lastSeq-watermark && len(filled) == n
    for i, seq := range filled { if seq != watermark+1+i { exact = false } }
    _, gapChunks, _ := gapRec.snap()
    h.check("fill-gap-exact-real-seq", fillErr == nil && exact, "from=", watermark+1, " to=", lastSeq, " n=", n, " err=", fillErr)
    h.check("fill-gap-restores-text-and-end", rchText(gapChunks, reconnectMarker) && func() bool { e,_,_ := gapRec.snap(); _, ok := rchEndAfter(e, watermark); return ok }())
    h.saveHistory("history-main-disconnected", disconnectedHist)

    // 错 epoch 订阅必须收到无 seq 的 session/reset，再按正确水位重连。
    badRec, badCancel, badErr := h.subscribe(h.mux(int64(epoch+1), nil), mainID, lastSeq)
    h.check("subscribe-stale-epoch", badErr == nil, "err=", badErr)
    if badErr == nil {
        reset := badRec.wait(5*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { return len(rchType(ev, "session/reset")) > 0 })
        rev, _, _ := badRec.snap(); resets := rchType(rev, "session/reset")
        valid := reset && len(resets) == 1 && resets[0].Seq == 0
        if valid {
            var data struct{ JournalEpoch int `json:"journalEpoch"`; LastSeq int `json:"lastSeq"` }
            valid = json.Unmarshal(resets[0].Data, &data) == nil && data.JournalEpoch == epoch && data.LastSeq >= lastSeq
        }
        h.check("session-reset-carries-real-epoch", valid, "resets=", len(resets))
        badCancel()
    }
    liveRec, liveCancel, err := h.subscribe(h.mux(int64(epoch), nil), mainID, lastSeq)
    h.check("reconnect-correct-epoch", err == nil, "err=", err)
    if err != nil { return }
    defer liveCancel()
    afterMarker := "RC_HEADLESS_RECONNECTED_OK"
    _, err = h.cli.Prompt(context.Background(), mainID, "Reply with exactly "+afterMarker+" and nothing else. Do not call any tool.")
    h.check("prompt-after-reconnect", err == nil, "err=", err)
    liveRec.wait(180*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { _, ok := rchEndAfter(ev, lastSeq); return ok })
    liveEv, liveCh, _ := liveRec.snap()
    _, liveDone := rchEndAfter(liveEv, lastSeq)
    h.check("reconnected-new-turn-and-text", liveDone && rchText(liveCh, afterMarker), "events=", len(liveEv))
    oldReplay := false; for _, e := range liveEv { if e.Seq > 0 && e.Seq <= lastSeq { oldReplay = true } }
    h.check("reconnect-no-old-replay", !oldReplay, "watermark=", lastSeq)

    // allow：真实 write 副作用必须出现。
    runTag := fmt.Sprintf("%d", time.Now().UnixNano())
    allowID, allowLoad := h.create("allow")
    if allowID != "" && allowLoad != nil {
        rec, cancel, err := h.subscribe(h.mux(allowLoad.JournalEpoch, nil), allowID, 0)
        h.check("subscribe-allow", err == nil, "err=", err)
        if err == nil {
            defer cancel()
            target := filepath.Join(h.out, "allow-"+runTag+".txt")
            marker := "RC_HEADLESS_ALLOW_WRITE_OK"
            _, err = h.cli.Prompt(context.Background(), allowID, "用 write 工具把 "+marker+" 写进文件 "+target+"，只做这一步，不要解释。")
            h.check("prompt-allow-tool", err == nil, "err=", err)
            count := h.approvals(rec, allowID, "allow_once", 240*time.Second)
            ev, ch, _ := rec.snap(); _, ended := rchEndAfter(ev, 0)
            content, readErr := os.ReadFile(target)
            h.check("allow-request-and-turn", count > 0 && ended, "approvals=", count)
            h.check("allow-real-side-effect", readErr == nil && strings.Contains(string(content), marker), "err=", readErr)
            h.check("allow-tool-through-ui-chunks", rchTools(ch) > 0, "tool_chunks=", rchTools(ch))
            h.check("real-reasoning-through-ui-chunks", rchReasoning(ch) > 0, "reasoning_bytes=", rchReasoning(ch))
            ah, ahErr := h.cli.History(context.Background(), allowID, 0, 200, int(allowLoad.JournalEpoch))
            h.check("allow-history-tool-frames", ahErr == nil && ah != nil && len(rchType(func() []Event { out:=[]Event{}; for _,f:=range ah.Frames { out=append(out,f.Event("session:"+allowID)) }; return out }(), "tool/result")) > 0, "err=", ahErr)
            if ah != nil {
                h.saveHistory("history-allow", ah)
                reasoningRaw := false
                for _, f := range ah.Frames { if f.Type == "assistant/reasoning" || strings.Contains(string(f.Data), `"type":"reasoning"`) { reasoningRaw = true } }
                h.check("history-preserves-reasoning", reasoningRaw, "frames=", len(ah.Frames))
            }
        }
    }

    // deny：请求确实指向 marker，拒绝后文件绝不能出现，tool/result 要保留拒绝真值。
    denyID, denyLoad := h.create("deny")
    if denyID != "" && denyLoad != nil {
        rec, cancel, err := h.subscribe(h.mux(denyLoad.JournalEpoch, nil), denyID, 0)
        h.check("subscribe-deny", err == nil, "err=", err)
        if err == nil {
            defer cancel()
            target := filepath.Join(h.out, "deny-"+runTag+".txt")
            marker := "RC_HEADLESS_DENY_NEVER_WRITE"
            _, err = h.cli.Prompt(context.Background(), denyID, "用 write 工具把 "+marker+" 写进文件 "+target+"，只做这一步，不要解释。")
            h.check("prompt-deny-tool", err == nil, "err=", err)
            count := h.approvals(rec, denyID, "reject_once", 240*time.Second)
            ev, _, _ := rec.snap(); _, ended := rchEndAfter(ev, 0)
            _, statErr := os.Stat(target)
            h.check("deny-request-and-turn", count > 0 && ended && rchRaw(ev, marker), "approvals=", count)
            h.check("deny-real-side-effect-absent", os.IsNotExist(statErr), "stat=", statErr)
            refused := false
            for _, e := range rchType(ev, "tool/result") { if strings.Contains(string(e.Data), `"isError":true`) { refused = true } }
            h.check("deny-tool-result-is-error", refused)
            dh, dhErr := h.cli.History(context.Background(), denyID, 0, 200, int(denyLoad.JournalEpoch))
            if dh != nil { h.saveHistory("history-deny", dh) }
            h.check("deny-history-readable", dhErr == nil && dh != nil, "err=", dhErr)
        }
    }

    // pending approval cancel：工具不得执行，随后第二轮必须真实完成。
    cancelID, cancelLoad := h.create("cancel")
    if cancelID != "" && cancelLoad != nil {
        rec, cancelSub, err := h.subscribe(h.mux(cancelLoad.JournalEpoch, nil), cancelID, 0)
        h.check("subscribe-cancel", err == nil, "err=", err)
        if err == nil {
            defer cancelSub()
            target := filepath.Join(h.out, "cancel-"+runTag+".txt")
            marker := "RC_HEADLESS_CANCEL_NEVER_WRITE"
            _, err = h.cli.Prompt(context.Background(), cancelID, "用 write 工具把 "+marker+" 写进文件 "+target+"，只做这一步，不要解释。")
            h.check("prompt-cancel-pending", err == nil, "err=", err)
            pending := rec.wait(180*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { return len(rchType(ev, "approval/request")) > 0 })
            ev, _, _ := rec.snap(); reqs := rchType(ev, "approval/request")
            requestID := ""
            if pending && len(reqs) > 0 { req, e := ParseApprovalRequest(reqs[0].Data); if e == nil { requestID = req.RequestID } }
            h.check("cancel-has-real-pending-approval", requestID != "", "request=", requestID)
            var cancelOut struct{ Cancelled bool `json:"cancelled"` }
            cancelErr := h.cli.Call(context.Background(), "session.cancel", map[string]string{"id":cancelID}, &cancelOut)
            h.check("cancel-accepted", cancelErr == nil && cancelOut.Cancelled, "err=", cancelErr)
            ev, _, _ = rec.snap(); firstEnd, firstEnded := rchEndAfter(ev, 0)
            cancelFrame := false
            for _, e := range rchType(ev, "approval/cancel") { if strings.Contains(string(e.Data), requestID) { cancelFrame = true } }
            _, statErr := os.Stat(target)
            h.check("cancel-settles-approval-and-turn", firstEnded && cancelFrame, "end=", firstEnd.Kind)
            h.check("cancel-tool-never-executed", os.IsNotExist(statErr), "stat=", statErr)
            secondMarker := "RC_HEADLESS_SECOND_TURN_OK"
            _, secondErr := h.cli.Prompt(context.Background(), cancelID, "Reply with exactly "+secondMarker+" and nothing else. Do not call any tool.")
            h.check("second-prompt-after-cancel", secondErr == nil, "err=", secondErr)
            since := 0; if firstEnded { since = firstEnd.Seq }
            rec.wait(180*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { _, ok := rchEndAfter(ev, since); return ok })
            ev, ch, _ = rec.snap(); secondEnd, secondEnded := rchEndAfter(ev, since)
            h.check("second-turn-real-text", secondEnded && secondEnd.Kind == "turn/done" && rchText(ch, secondMarker), "end=", secondEnd.Kind)
            locks := []string{}
            _ = filepath.Walk(h.home, func(path string, info os.FileInfo, err error) error { if err == nil && info != nil && strings.HasSuffix(info.Name(), ".lock") { locks = append(locks, path) }; return nil })
            h.check("cancel-no-stale-session-lock", len(locks) == 0, "locks=", locks)
        }
    }

    // A/B 真并发：两个子进程、两条 WS、两套 seq，暗号不得互串。
    aID, aLoad := h.create("A")
    bID, bLoad := h.create("B")
    if aID != "" && bID != "" && aLoad != nil && bLoad != nil {
        aRec, aCancel, aErr := h.subscribe(h.mux(aLoad.JournalEpoch, nil), aID, 0)
        bRec, bCancel, bErr := h.subscribe(h.mux(bLoad.JournalEpoch, nil), bID, 0)
        h.check("subscribe-A-B", aErr == nil && bErr == nil, "a=", aErr, " b=", bErr)
        if aErr == nil && bErr == nil {
            defer aCancel(); defer bCancel()
            markA, markB := "RC_HEADLESS_ALPHA_ONLY", "RC_HEADLESS_BRAVO_ONLY"
            var wg sync.WaitGroup; wg.Add(2)
            var paErr, pbErr error
            go func(){ defer wg.Done(); _, paErr = h.cli.Prompt(context.Background(), aID, "Reply with exactly "+markA+" and nothing else. Do not call any tool.") }()
            go func(){ defer wg.Done(); _, pbErr = h.cli.Prompt(context.Background(), bID, "Reply with exactly "+markB+" and nothing else. Do not call any tool.") }()
            wg.Wait()
            h.check("prompt-A-B-accepted", paErr == nil && pbErr == nil, "a=", paErr, " b=", pbErr)
            aRec.wait(240*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { _, ok := rchEndAfter(ev, 0); return ok })
            bRec.wait(240*time.Second, func(ev []Event, _ []rchChunk, _ bool) bool { _, ok := rchEndAfter(ev, 0); return ok })
            aEv, aCh, _ := aRec.snap(); bEv, bCh, _ := bRec.snap()
            _, aDone := rchEndAfter(aEv, 0); _, bDone := rchEndAfter(bEv, 0)
            h.check("A-B-both-done-own-text", aDone && bDone && rchText(aCh, markA) && rchText(bCh, markB), "aEvents=",len(aEv)," bEvents=",len(bEv))
            h.check("A-no-B-cross-talk", !rchRaw(aEv, markB) && !rchText(aCh, markB))
            h.check("B-no-A-cross-talk", !rchRaw(bEv, markA) && !rchText(bCh, markA))
            aFirst, bFirst := 0, 0
            for _, e := range aEv { if e.Seq > 0 { aFirst = e.Seq; break } }
            for _, e := range bEv { if e.Seq > 0 { bFirst = e.Seq; break } }
            h.check("A-B-independent-seq-from-one", aFirst == 1 && bFirst == 1, "a=",aFirst," b=",bFirst)
            al, alErr := h.cli.Load(context.Background(), aID, 100); bl, blErr := h.cli.Load(context.Background(), bID, 100)
            loadOK := alErr == nil && blErr == nil && al != nil && bl != nil
            if loadOK {
                aOwn, bOwn := false, false
                for _, m := range al.Messages { if strings.Contains(m.Text, markB) { loadOK = false }; if m.Role == "assistant" && strings.Contains(m.Text, markA) { aOwn = true } }
                for _, m := range bl.Messages { if strings.Contains(m.Text, markA) { loadOK = false }; if m.Role == "assistant" && strings.Contains(m.Text, markB) { bOwn = true } }
                loadOK = loadOK && aOwn && bOwn
            }
            h.check("A-B-load-isolated", loadOK, "a=",alErr," b=",blErr)
        }
    }

    finalList, listErr := h.cli.List(context.Background())
    h.check("final-list-six-real-sessions", listErr == nil && len(finalList) >= 6, "sessions=", len(finalList), " err=", listErr)
}
'''

GO_CURRENT_PROBE = r'''package apiclient

import (
    "bytes"
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "sync"
    "sync/atomic"
    "syscall"
    "testing"
    "time"
    "unsafe"
)

type rccWireRecorder struct {
    base http.RoundTripper
    out string
    secrets []string
    mu sync.Mutex
    n int
}
func (w *rccWireRecorder) redact(raw []byte) []byte {
    for _, secret := range w.secrets {
        if secret != "" { raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[redacted]")) }
    }
    return raw
}
func (w *rccWireRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
    var requestBody []byte
    if req.Body != nil {
        requestBody, _ = io.ReadAll(io.LimitReader(req.Body, 2<<20))
        req.Body.Close()
        req.Body = io.NopCloser(bytes.NewReader(requestBody))
    }
    resp, err := w.base.RoundTrip(req)
    w.mu.Lock(); defer w.mu.Unlock()
    w.n++
    entry := map[string]any{"sequence": w.n, "method": req.Method, "path": req.URL.Path,
        "request": json.RawMessage(w.redact(requestBody)), "error": ""}
    if len(requestBody) == 0 { entry["request"] = nil }
    if err != nil { entry["error"] = err.Error(); return resp, err }
    responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
    resp.Body.Close()
    resp.Body = io.NopCloser(bytes.NewReader(responseBody))
    entry["status"] = resp.StatusCode
    entry["response"] = json.RawMessage(w.redact(responseBody))
    if readErr != nil { entry["error"] = readErr.Error() }
    raw, _ := json.MarshalIndent(entry, "", "  ")
    name := filepath.Join(w.out, fmt.Sprintf("wire-%04d.json", w.n))
    if writeErr := os.WriteFile(name, raw, 0644); writeErr != nil { entry["error"] = writeErr.Error() }
    return resp, err
}

type rccHarness struct {
    t *testing.T
    cli *Client
    home, out string
    ok, fail int
    secrets []string
}
func (h *rccHarness) check(name string, cond bool, detail ...any) {
    msg := fmt.Sprint(detail...)
    if cond { h.ok++; fmt.Printf("RCC_OK %s %s\n", name, msg) } else { h.fail++; fmt.Printf("RCC_FAIL %s %s\n", name, msg) }
}
func (h *rccHarness) apiError(name string, err error, code string) {
    var apiErr *APIError
    ok := errors.As(err, &apiErr) && apiErr.Code == code && apiErr.Message != "" && apiErr.Hint != ""
    h.check(name, ok, "err=", err)
}
func (h *rccHarness) create(title string) (string, *LoadResult) {
    id, err := h.cli.Create(context.Background(), "", "default", title)
    h.check("create/"+title, err == nil && id != "", "id=", id, " err=", err)
    if err != nil || id == "" { return "", nil }
    ld, err := h.cli.Load(context.Background(), id, 100)
    h.check("load/"+title, err == nil && ld != nil && ld.JournalEpoch != 0, "err=", err)
    return id, ld
}
func (h *rccHarness) waitDone(id string, epoch int64, since int, d time.Duration) (*HistoryResult, bool) {
    deadline := time.Now().Add(d)
    var last *HistoryResult
    for time.Now().Before(deadline) {
        hist, err := h.cli.History(context.Background(), id, since, 500, int(epoch))
        if err == nil {
            last = hist
            for _, f := range hist.Frames {
                if f.Seq > since && f.Type == "turn/done" { return hist, true }
                if f.Seq > since && f.Type == "turn/error" { return hist, false }
            }
        }
        time.Sleep(100 * time.Millisecond)
    }
    return last, false
}
func rccHistoryContains(hist *HistoryResult, marker string) bool {
    if hist == nil { return false }
    for _, f := range hist.Frames { if bytes.Contains(f.Data, []byte(marker)) { return true } }
    return false
}
func (h *rccHarness) promptDone(id string, epoch int64, prompt, marker string) bool {
    turn, err := h.cli.Prompt(context.Background(), id, prompt)
    h.check("prompt/"+marker, err == nil && turn != "", "turn=", turn, " err=", err)
    hist, done := h.waitDone(id, epoch, 0, 90*time.Second)
    h.check("turn-and-text/"+marker, done && rccHistoryContains(hist, marker), "done=", done)
    return done && rccHistoryContains(hist, marker)
}

type rccWireRequest struct {
    Path string `json:"path"`
    Model string `json:"model"`
    AuthorizationOK bool `json:"authorizationOK"`
    Stream bool `json:"stream"`
    MaxTokens int `json:"maxTokens"`
    Status int `json:"status"`
}
type rccEndpoint struct {
    label, secret, helper, pidDir string
    listener net.Listener
    server *http.Server
    requests chan rccWireRequest
    jobCalls atomic.Int32
    finish chan struct{}
    finishOnce sync.Once
}
func rccJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
func newRCCEndpoint(label, secret string) *rccEndpoint {
    ep := &rccEndpoint{label: label, secret: secret, requests: make(chan rccWireRequest, 32), finish:make(chan struct{})}
    ep.listener, _ = net.Listen("tcp", "127.0.0.1:0")
    ep.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var body struct { Model string `json:"model"`; Stream bool `json:"stream"`; MaxTokens int `json:"max_tokens"` }
        _ = json.NewDecoder(r.Body).Decode(&body)
        authOK := r.Header.Get("Authorization") == "Bearer "+ep.secret
        rec := rccWireRequest{Path: r.URL.Path, Model: body.Model, AuthorizationOK: authOK, Stream: body.Stream, MaxTokens: body.MaxTokens, Status: 200}
        if !authOK {
            rec.Status = 401
            ep.requests <- rec
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(401)
            fmt.Fprintf(w, "{\"error\":%s}", rccJSON("received "+r.Header.Get("Authorization")))
            return
        }
        ep.requests <- rec
        if r.URL.Path != "/v1/chat/completions" { http.NotFound(w, r); return }
        if !body.Stream {
            w.Header().Set("Content-Type", "application/json")
            fmt.Fprint(w, rccJSON(map[string]any{"model": body.Model, "choices": []any{map[string]any{"message": map[string]any{"role":"assistant", "content":"OK"}}}}))
            return
        }
        w.Header().Set("Content-Type", "text/event-stream")
        if body.Model == "model-job" && ep.jobCalls.Add(1) == 1 {
            command := "printf 'A甲🙂乙B'; "+rccShellQuote(filepath.ToSlash(ep.helper))+" parent "+rccShellQuote(filepath.ToSlash(ep.pidDir))
            args, _ := json.Marshal(map[string]any{"command": command, "description": "owned current process tree", "run_in_background": true})
            payload := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
                "index":0, "id":"call-current-job", "type":"function", "function":map[string]any{"name":"bash", "arguments":string(args)},
            }}}, "finish_reason":"tool_calls"}}}
            fmt.Fprintf(w, "data: %s\n\n", rccJSON(payload))
        } else {
            marker := "RCC_PROVIDER_"+ep.label+"_OK"
            if body.Model == "model-job" {
                marker = "RCC_JOB_TURN_DONE"
                select { case <-ep.finish: case <-time.After(90*time.Second): }
            }
            payload := map[string]any{"choices": []any{map[string]any{"delta":map[string]any{"content":marker}, "finish_reason":"stop"}}}
            fmt.Fprintf(w, "data: %s\n\n", rccJSON(payload))
        }
        fmt.Fprint(w, "data: [DONE]\n\n")
        if f, ok := w.(http.Flusher); ok { f.Flush() }
    })}
    go ep.server.Serve(ep.listener)
    return ep
}
func (e *rccEndpoint) base() string { return "http://"+e.listener.Addr().String()+"/v1" }
func (e *rccEndpoint) release() { e.finishOnce.Do(func(){ close(e.finish) }) }
func (e *rccEndpoint) close() { e.release(); _ = e.server.Close() }
func (e *rccEndpoint) next(d time.Duration) (rccWireRequest, bool) {
    select { case req := <-e.requests: return req, true; case <-time.After(d): return rccWireRequest{}, false }
}
func rccShellQuote(s string) string { return "'"+strings.ReplaceAll(s, "'", "'\\''")+"'" }

func rccWritePlugin(dir, id, version, marker string) error {
    if err := os.MkdirAll(dir, 0755); err != nil { return err }
    payload := []byte("#!/bin/sh\necho executed > "+filepath.ToSlash(marker)+"\n# "+id+" "+version+"\n")
    sum := sha256.Sum256(payload)
    if err := os.WriteFile(filepath.Join(dir, "entry.sh"), payload, 0644); err != nil { return err }
    manifest := map[string]any{"schema":"dsh.local-plugin@1", "id":id, "version":version, "kind":"process", "entry":"entry.sh",
        "files":map[string]string{"entry.sh":hex.EncodeToString(sum[:])}}
    raw, _ := json.MarshalIndent(manifest, "", "  ")
    return os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0644)
}

var rccKernel32 = syscall.NewLazyDLL("kernel32.dll")
var rccOpenProcess = rccKernel32.NewProc("OpenProcess")
var rccCloseHandle = rccKernel32.NewProc("CloseHandle")
var rccQueryImage = rccKernel32.NewProc("QueryFullProcessImageNameW")
func rccProcessImage(pid int) (string, error) {
    handle, _, callErr := rccOpenProcess.Call(0x1000, 0, uintptr(pid))
    if handle == 0 { return "", callErr }
    defer rccCloseHandle.Call(handle)
    buf := make([]uint16, 32768); n := uint32(len(buf))
    ok, _, callErr := rccQueryImage.Call(handle, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
    if ok == 0 { return "", callErr }
    return syscall.UTF16ToString(buf[:n]), nil
}
func rccImageMatches(pid int, expected string) bool {
    actual, err := rccProcessImage(pid); if err != nil { return false }
    a, errA := os.Stat(actual); b, errB := os.Stat(expected)
    return errA == nil && errB == nil && os.SameFile(a, b)
}
func rccReadPID(path string) int {
    raw, err := os.ReadFile(path); if err != nil { return 0 }
    pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); if err != nil || pid <= 0 { return 0 }
    return pid
}
func rccWaitHelper(dir, name, image string, wantAlive bool) int {
    deadline := time.Now().Add(8*time.Second); pid := 0
    for time.Now().Before(deadline) {
        pid = rccReadPID(filepath.Join(dir, name+".pid"))
        alive := pid > 0 && rccImageMatches(pid, image)
        if alive == wantAlive && (pid > 0 || !wantAlive) { return pid }
        time.Sleep(30*time.Millisecond)
    }
    return 0
}
func rccCleanupHelpers(dir, image string) {
    for _, name := range []string{"parent", "child", "grandchild"} {
        pid := rccReadPID(filepath.Join(dir, name+".pid"))
        if pid > 0 && rccImageMatches(pid, image) {
            out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
            fmt.Printf("RCC_CLEANUP owned-helper=%s pid=%d err=%v output=%s\n", name, pid, err, out)
        }
    }
}

func TestRealChainCombined(t *testing.T) {
    t.Run("W36-core", TestRealChainHeadless)
    t.Run("current", TestRealChainCurrent)
}

func TestRealChainCurrent(t *testing.T) {
    h := &rccHarness{t:t, home:os.Getenv("RCH_HOME"), out:os.Getenv("RCH_OUT"),
        secrets:[]string{os.Getenv("RCC_SECRET_A"), os.Getenv("RCC_SECRET_B"), os.Getenv("RCC_SECRET_WRONG")}}
    defer func() {
        fmt.Printf("RCC_SUMMARY OK=%d FAIL=%d SKIP=0\n", h.ok, h.fail)
        if h.fail > 0 { t.Errorf("当前真实整链有 %d 条 FAIL", h.fail) }
    }()
    h.cli = New(os.Getenv("RCH_BASE")); h.cli.HTTP.Timeout = 30*time.Second
    if len(rchSharedCookies) > 0 {
        h.cli.SetCookies(rchSharedCookies)
    } else if err := h.cli.Connect(context.Background(), os.Getenv("RCH_TOKEN")); err != nil {
        h.check("connect", false, err); return
    }
    h.check("connect-shared-ui-cookie", len(h.cli.Cookies()) > 0)
    h.cli.HTTP.Transport = &rccWireRecorder{base:http.DefaultTransport, out:h.out, secrets:h.secrets}
    ctx := context.Background()

    // W74：真实 v4 配置记录经同一 dshd 的 session.load 到真实 UI Client.Call。
    for _, tc := range []struct{ id, want string }{{"session-effort-A","high"},{"session-effort-B","xhigh"},{"session-effort-old",""},{"session-effort-unknown",""}} {
        var wire struct { Effort json.RawMessage `json:"effort"`; Model string `json:"model"` }
        err := h.cli.Call(ctx, "session.load", map[string]any{"id":tc.id,"sessionId":tc.id,"limit":100,"tailMessages":100}, &wire)
        got := ""
        if len(wire.Effort) > 0 { _ = json.Unmarshal(wire.Effort, &got) }
        h.check("effort/"+tc.id, err == nil && got == tc.want && (tc.want != "" || len(wire.Effort) == 0), "effort=",got," raw=",string(wire.Effort)," err=",err)
    }
    effortA, errA := h.cli.Load(ctx, "session-effort-A", 100)
    effortB, errB := h.cli.Load(ctx, "session-effort-B", 100)
    h.check("effort-A-B-load-isolated", errA == nil && errB == nil && effortA.Model == "model-A" && effortB.Model == "model-B", "a=",errA," b=",errB)

    // settings.describe/mutate：真实 revision、后续新会话与失败冲突。
    desc, err := h.cli.SettingsDescribe(ctx)
    h.check("settings-describe", err == nil && desc.Writable && len(desc.Namespaces) >= 2 && len(desc.Sections) >= 2, "err=",err)
    var permissionNS SettingsNamespace
    for _, ns := range desc.Namespaces { if ns.NS == "permission" { permissionNS = ns } }
    mutated, err := h.cli.SettingsMutate(ctx, "permission", []SettingsPathOp{{Op:"set",Path:[]string{"defaultPreset"},Value:"deny"}}, permissionNS.Revision)
    h.check("settings-mutate-deny", err == nil && mutated.Value["defaultPreset"] == "deny" && mutated.Revision > permissionNS.Revision, "revision=",mutated.Revision," err=",err)
    settingsID, settingsLoad := h.create("settings-deny")
    h.check("settings-new-session-permission", settingsID != "" && settingsLoad != nil && settingsLoad.Permission == "deny")
    _, staleErr := h.cli.SettingsMutate(ctx, "permission", []SettingsPathOp{{Op:"set",Path:[]string{"defaultPreset"},Value:"allow"}}, permissionNS.Revision)
    h.apiError("settings-stale-revision-real-conflict", staleErr, "revision_conflict")
    restored, err := h.cli.SettingsMutate(ctx, "permission", []SettingsPathOp{{Op:"unset",Path:[]string{"defaultPreset"}}}, mutated.Revision)
    h.check("settings-mutate-unset", err == nil && restored.Value["defaultPreset"] == "ask", "value=",restored.Value," err=",err)

    // 两个独立本地 OpenAI listener 只承接模型请求；dshd/Transport/UI 都是真实产品路径。
    endpointA := newRCCEndpoint("A", h.secrets[0]); defer endpointA.close()
    endpointB := newRCCEndpoint("B", h.secrets[1]); defer endpointB.close()
    catalog, err := h.cli.ModelCatalogList(ctx)
    h.check("catalog-list-initial", err == nil && catalog.Active.ID == os.Getenv("DSHD_LLM_MODEL"), "active=",catalog.Active," err=",err)
    modelA := json.RawMessage(rccJSON(map[string]any{"id":"model-a","name":"Current A","api":"openai-completions","baseURL":endpointA.base(),"apiKeyEnv":"CURRENT_A_KEY","future":map[string]any{"keep":true}}))
    view, err := h.cli.ModelCatalogUpsert(ctx, modelA, &h.secrets[0], &catalog.Revision)
    h.check("catalog-upsert-A", err == nil && view.Revision == catalog.Revision+1 && view.Credentials["CURRENT_A_KEY"].Configured, "revision=",view.Revision," err=",err)
    modelB := json.RawMessage(rccJSON(map[string]any{"id":"model-b","name":"Current B","api":"openai-completions","baseURL":endpointB.base(),"apiKeyEnv":"CURRENT_B_KEY"}))
    view, err = h.cli.ModelCatalogUpsert(ctx, modelB, &h.secrets[1], &view.Revision)
    h.check("catalog-upsert-B", err == nil && len(view.Models) == 2 && view.Credentials["CURRENT_B_KEY"].Configured, "revision=",view.Revision," err=",err)
    legacyID, legacyLoad := h.create("legacy")
    testA, err := h.cli.ModelCatalogTest(ctx, "model-a", ModelCatalogTestOverride{})
    reqA, gotA := endpointA.next(10*time.Second)
    h.check("catalog-test-A", err == nil && testA.OK && testA.Model == "model-a", "result=",testA," err=",err)
    h.check("catalog-test-A-wire", gotA && reqA.Path == "/v1/chat/completions" && reqA.Model == "model-a" && reqA.AuthorizationOK && !reqA.Stream && reqA.MaxTokens == 1, "request=",reqA)
    testB, err := h.cli.ModelCatalogTest(ctx, "model-b", ModelCatalogTestOverride{})
    reqB, gotB := endpointB.next(10*time.Second)
    h.check("catalog-test-B", err == nil && testB.OK && testB.Model == "model-b", "result=",testB," err=",err)
    h.check("catalog-test-B-wire", gotB && reqB.Path == "/v1/chat/completions" && reqB.Model == "model-b" && reqB.AuthorizationOK && !reqB.Stream, "request=",reqB)
    wrong := h.secrets[2]
    _, wrongErr := h.cli.ModelCatalogTest(ctx, "model-a", ModelCatalogTestOverride{Credential:&wrong})
    wrongReq, wrongGot := endpointA.next(10*time.Second)
    h.apiError("catalog-wrong-credential-real-401", wrongErr, "upstream_error")
    h.check("catalog-wrong-credential-endpoint-and-redaction", wrongGot && wrongReq.Status == 401 && !wrongReq.AuthorizationOK && wrongErr != nil && !strings.Contains(wrongErr.Error(), wrong), "request=",wrongReq)

    activeA, err := h.cli.ModelCatalogSetActive(ctx, "model-a", &catalog.Active.Revision)
    h.check("catalog-set-active-A", err == nil && activeA.ActiveModel == "model-a", "result=",activeA," err=",err)
    sessionA, loadA := h.create("provider-A")
    activeB, err := h.cli.ModelCatalogSetActive(ctx, "model-b", &activeA.ActiveRevision)
    h.check("catalog-set-active-B", err == nil && activeB.ActiveModel == "model-b" && activeB.ActiveRevision > activeA.ActiveRevision, "result=",activeB," err=",err)
    sessionB, loadB := h.create("provider-B")
    if sessionA != "" && loadA != nil { h.promptDone(sessionA, loadA.JournalEpoch, "prove provider A", "RCC_PROVIDER_A_OK") }
    reqA, gotA = endpointA.next(15*time.Second)
    h.check("provider-A-real-runner-base-path-model-auth", gotA && reqA.Path == "/v1/chat/completions" && reqA.Model == "model-a" && reqA.AuthorizationOK && reqA.Stream, "request=",reqA)
    if sessionB != "" && loadB != nil { h.promptDone(sessionB, loadB.JournalEpoch, "prove provider B", "RCC_PROVIDER_B_OK") }
    reqB, gotB = endpointB.next(15*time.Second)
    h.check("provider-B-real-runner-base-path-model-auth", gotB && reqB.Path == "/v1/chat/completions" && reqB.Model == "model-b" && reqB.AuthorizationOK && reqB.Stream, "request=",reqB)
    if legacyID != "" && legacyLoad != nil { h.promptDone(legacyID, legacyLoad.JournalEpoch, "Reply with exactly RCC_LEGACY_OK and nothing else. Do not call any tool.", "RCC_LEGACY_OK") }
    models, modelsErr := h.cli.ModelsList(ctx)
    legacyCatalog := false; for _, m := range models { if m.ID == os.Getenv("DSHD_LLM_MODEL") { legacyCatalog = true } }
    h.check("canonical-models-list-legacy-preserved", modelsErr == nil && legacyCatalog, "models=",len(models)," err=",modelsErr)
    if raw, readErr := os.ReadFile(os.Getenv("RCH_DSHD_STDERR")); readErr == nil {
        text := string(raw)
        h.check("provider-actual-argv-base-and-no-credential", strings.Contains(text, "--base "+endpointA.base()) && strings.Contains(text, "--base "+endpointB.base()) && strings.Contains(text, "--model model-a") && strings.Contains(text, "--model model-b") && !strings.Contains(text, h.secrets[0]) && !strings.Contains(text, h.secrets[1]))
    } else { h.check("provider-actual-argv-base-and-no-credential", false, readErr) }

    // 本地包只复制校验，绝不执行 entry；所有生命周期动作均经 UI apiclient。
    pluginID := "current.probe"
    pluginMarker := filepath.Join(h.out, "PLUGIN_ENTRY_EXECUTED")
    pkgV1, pkgV2 := filepath.Join(h.out,"plugin-v1"), filepath.Join(h.out,"plugin-v2")
    _ = rccWritePlugin(pkgV1, pluginID, "1.0.0", pluginMarker)
    _ = rccWritePlugin(pkgV2, pluginID, "1.1.0", pluginMarker)
    installed, err := h.cli.PluginInstall(ctx, pkgV1)
    h.check("plugin-install-disabled", err == nil && installed.ID == pluginID && installed.Version == "1.0.0" && !installed.Enabled, "plugin=",installed," err=",err)
    enabled, err := h.cli.PluginEnable(ctx, pluginID)
    h.check("plugin-enable", err == nil && enabled.Enabled, "err=",err)
    updated, err := h.cli.PluginUpdate(ctx, pluginID, pkgV2)
    h.check("plugin-update", err == nil && updated.Version == "1.1.0" && updated.PreviousVersion == "1.0.0", "plugin=",updated," err=",err)
    _, versionErr := h.cli.PluginUpdate(ctx, pluginID, pkgV1)
    h.apiError("plugin-version-conflict-real-409", versionErr, "version_conflict")
    disabled, err := h.cli.PluginDisable(ctx, pluginID)
    h.check("plugin-disable", err == nil && !disabled.Enabled, "err=",err)
    restoredPlugin, err := h.cli.PluginRestore(ctx, pluginID, "")
    h.check("plugin-restore", err == nil && restoredPlugin.Version == "1.0.0" && restoredPlugin.PreviousVersion == "1.1.0", "plugin=",restoredPlugin," err=",err)
    reconciled, err := h.cli.PluginReconcile(ctx)
    h.check("plugin-reconcile", err == nil && reconciled.Recovered == 0 && len(reconciled.Actions) == 0, "result=",reconciled," err=",err)
    uninstalled, err := h.cli.PluginUninstall(ctx, pluginID)
    h.check("plugin-uninstall", err == nil && uninstalled, "err=",err)
    _, missingErr := h.cli.PluginEnable(ctx, pluginID)
    h.apiError("plugin-enable-after-uninstall-real-404", missingErr, "not_found")
    plugins, err := h.cli.PluginList(ctx)
    absent := true; for _, p := range plugins { if p.ID == pluginID { absent = false } }
    _, markerErr := os.Stat(pluginMarker)
    h.check("plugin-list-and-entry-never-executed", err == nil && absent && os.IsNotExist(markerErr), "plugins=",plugins," marker=",markerErr)

    // schedule delete/clear-ended：预置临时 home 的真实持久文件，包含活跃与周期保留项。
    schedules, err := h.cli.ScheduleList(ctx)
    revision := ""; found := 0
    for _, item := range schedules { if item.SessionID == "session-schedule-current" { found++; if item.ID == "schedule-delete" { revision = item.Revision } } }
    h.check("schedule-list-rich", err == nil && found == 4 && revision != "", "items=",schedules," err=",err)
    deleted, err := h.cli.ScheduleDelete(ctx, "session-schedule-current", "schedule-delete", revision)
    h.check("schedule-delete", err == nil && deleted.Deleted && deleted.Remaining == 3, "result=",deleted," err=",err)
    _, repeatDeleteErr := h.cli.ScheduleDelete(ctx, "session-schedule-current", "schedule-delete", "")
    h.apiError("schedule-repeat-delete-real-404", repeatDeleteErr, "not_found")
    _, staleScheduleErr := h.cli.ScheduleDelete(ctx, "session-schedule-current", "schedule-ended", "stale")
    h.apiError("schedule-stale-delete-real-409", staleScheduleErr, "revision_conflict")
    cleared, err := h.cli.ScheduleClear(ctx)
    keepRunning, keepRecurring, endedAbsent := false, false, true
    for _, item := range cleared.Schedules {
        if item.ID == "schedule-running" { keepRunning = true }; if item.ID == "schedule-recurring" { keepRecurring = true }; if item.ID == "schedule-ended" { endedAbsent = false }
    }
    h.check("schedule-clear-ended-readback", err == nil && cleared.Deleted == 1 && keepRunning && keepRecurring && endedAbsent && len(cleared.Schedules) == 2, "result=",cleared," err=",err)
    clearedAgain, err := h.cli.ScheduleClear(ctx)
    h.check("schedule-clear-empty", err == nil && clearedAgain.Deleted == 0 && len(clearedAgain.Schedules) == 2, "result=",clearedAgain," err=",err)

    // jobs.output/stop：真实 dshd→run→bash→helper 三级进程树，读回 CJK/emoji rune 页。
    modelJob := json.RawMessage(rccJSON(map[string]any{"id":"model-job","api":"openai-completions","baseURL":endpointA.base(),"apiKeyEnv":"CURRENT_A_KEY"}))
    view, err = h.cli.ModelCatalogUpsert(ctx, modelJob, nil, &view.Revision)
    h.check("catalog-upsert-job", err == nil && len(view.Models) == 3, "revision=",view.Revision," err=",err)
    activeJob, err := h.cli.ModelCatalogSetActive(ctx, "model-job", &activeB.ActiveRevision)
    h.check("catalog-set-active-job", err == nil && activeJob.ActiveModel == "model-job", "result=",activeJob," err=",err)
    jobSession, jobLoad := h.create("jobs")
    if jobSession == "" || jobLoad == nil { return }
    h.check("jobs-session-allow", h.cli.SetSessionPermission(ctx, jobSession, "allow") == nil)
    helperDir := filepath.Join(h.out, "job-helper")
    pidDir := filepath.Join(h.out, "job-pids")
    _ = os.MkdirAll(helperDir,0755); _ = os.MkdirAll(pidDir,0755)
    helperExe := filepath.Join(helperDir, "current-jobs-tree.exe")
    helperSource := filepath.Join(helperDir, "main.go")
    const helperGo = `package main
import("os";"os/exec";"path/filepath";"strconv";"time")
func main(){mode,dir:=os.Args[1],os.Args[2];next:="";if mode=="parent"{next="child"}else if mode=="child"{next="grandchild"};if next!=""{c:=exec.Command(os.Args[0],next,dir);if err:=c.Start();err!=nil{panic(err)};_ = c.Process.Release()};if err:=os.WriteFile(filepath.Join(dir,mode+".pid"),[]byte(strconv.Itoa(os.Getpid())),0600);err!=nil{panic(err)};deadline:=time.Now().Add(120*time.Second);for time.Now().Before(deadline){time.Sleep(20*time.Millisecond)}}`
    if err := os.WriteFile(helperSource, []byte(helperGo),0644); err != nil { h.check("job-helper-source",false,err); return }
    build := exec.Command(os.Getenv("RCC_GO"), "build", "-o", helperExe, helperSource); build.Dir = helperDir; build.Env = append(os.Environ(),"CGO_ENABLED=0")
    buildOut, buildErr := build.CombinedOutput()
    _ = os.Rename(helperSource, helperSource+".txt")
    h.check("job-helper-build", buildErr == nil, "output=",string(buildOut)," err=",buildErr)
    if buildErr != nil { return }
    defer rccCleanupHelpers(pidDir, helperExe)
    endpointA.helper, endpointA.pidDir = helperExe, pidDir
    _, err = h.cli.Prompt(ctx, jobSession, "start one owned background job")
    h.check("jobs-prompt", err == nil, "err=",err)
    var jobID string
    deadline := time.Now().Add(10*time.Second)
    for jobID == "" && time.Now().Before(deadline) {
        jobs, listErr := h.cli.JobsList(ctx)
        for _, job := range jobs { if job.SessionID == jobSession && !job.Done { jobID = job.ID } }
        if jobID == "" && listErr == nil { time.Sleep(50*time.Millisecond) }
    }
    h.check("jobs-list-real-running", jobID != "", "id=",jobID)
    if jobID == "" { return }
    var first JobOutput
    deadline = time.Now().Add(8*time.Second)
    for time.Now().Before(deadline) {
        first, err = h.cli.JobOutput(ctx, jobSession, jobID, 0, 5, 0)
        if err == nil && first.Total == 5 { break }; time.Sleep(50*time.Millisecond)
    }
    h.check("jobs-output-first-rune-page", err == nil && first.Output == "A甲🙂乙B" && first.NextCursor == 5 && !first.HasMore, "page=",first," err=",err)
    second, err := h.cli.JobOutput(ctx, jobSession, jobID, 1, 2, 0)
    h.check("jobs-output-cjk-emoji-runes", err == nil && second.Output == "甲🙂" && second.NextCursor == 3 && second.HasMore, "page=",second," err=",err)
    _, crossErr := h.cli.JobStop(ctx, "other-session", jobID)
    h.apiError("jobs-cross-session-real-403", crossErr, "cross_session")
    _, unknownErr := h.cli.JobStop(ctx, jobSession, "unknown-job")
    h.apiError("jobs-unknown-real-404", unknownErr, "not_found")
    ownedPIDs := map[string]int{}
    alive := true
    for _, name := range []string{"parent","child","grandchild"} {
        pid := rccWaitHelper(pidDir,name,helperExe,true); ownedPIDs[name] = pid; if pid == 0 { alive = false }
    }
    rawPIDs,_ := json.MarshalIndent(ownedPIDs,"","  "); _ = os.WriteFile(filepath.Join(h.out,"job-owned-pids.json"),rawPIDs,0644)
    h.check("jobs-real-process-tree-pids-images", alive, "pids=",ownedPIDs)
    stopped, err := h.cli.JobStop(ctx, jobSession, jobID)
    h.check("jobs-stop-real-tree", err == nil && stopped.Stopped && stopped.Done, "result=",stopped," err=",err)
    _, secondStopErr := h.cli.JobStop(ctx, jobSession, jobID)
    h.apiError("jobs-second-stop-real-409", secondStopErr, "job_finished")
    gone := true
    for _, name := range []string{"parent","child","grandchild"} {
        pid := ownedPIDs[name]
        deadline = time.Now().Add(6*time.Second)
        for pid > 0 && rccImageMatches(pid,helperExe) && time.Now().Before(deadline) { time.Sleep(30*time.Millisecond) }
        if pid > 0 && rccImageMatches(pid,helperExe) { gone = false }
    }
    h.check("jobs-process-tree-actually-gone", gone, "pids=",ownedPIDs)
    retained, err := h.cli.JobOutput(ctx, jobSession, jobID, 3, 2, 0)
    h.check("jobs-stop-readback", err == nil && retained.Output == "乙B" && retained.Done, "page=",retained," err=",err)
    endpointA.release()
    jobHist, jobDone := h.waitDone(jobSession, jobLoad.JournalEpoch, 0, 90*time.Second)
    h.check("jobs-turn-done", jobDone && rccHistoryContains(jobHist,"RCC_JOB_TURN_DONE"), "done=",jobDone)
    // jobs.output 的失败页可能只剩空白；W77 最终快照才保留 MSYS 原始 stderr。
    // 这里只打印环境标注，不增加 SKIP，也不豁免上面的任何真实 FAIL。
    var persistedJob struct { Output string `json:"output"`; ExitCode int `json:"exit_code"` }
    persistedPath := filepath.Join(h.home,"state","jobs",jobSession,jobID+".json")
    if raw, readErr := os.ReadFile(persistedPath); readErr == nil && json.Unmarshal(raw,&persistedJob) == nil &&
        (persistedJob.ExitCode == 3221225794 || strings.Contains(persistedJob.Output,"NtCreateDirectoryObject")) {
        fmt.Printf("RCC_ENV_MSYS_STARTUP_ACL exit=%d output=%q source=%s note=MSYS 命名对象启动失败；环境标注，不跳过也不豁免真实 FAIL\n",persistedJob.ExitCode,persistedJob.Output,persistedPath)
    }
    again, againErr := h.cli.JobOutput(ctx, jobSession, jobID, 3, 2, 0)
    h.check("jobs-ended-readback-idempotent", againErr == nil && again == retained, "page=",again," err=",againErr)

    // 原始请求/响应、dshd argv/stderr 与 UI 日志不得出现三个 credential literal。
    leaked := []string{}
    files, _ := filepath.Glob(filepath.Join(h.out,"wire-*.json"))
    files = append(files, os.Getenv("RCH_DSHD_STDERR"))
    for _, path := range files {
        raw, _ := os.ReadFile(path)
        for _, secret := range h.secrets { if secret != "" && bytes.Contains(raw,[]byte(secret)) { leaked = append(leaked,filepath.Base(path)) } }
    }
    h.check("credentials-never-echoed-in-wire-or-logs", len(leaked) == 0, "files=",leaked)
}
'''

FIXTURE_SEED = r'''package main
import (
    "encoding/json"
    "flag"
    "os"
    "dshgo/internal/session"
)
func main() {
    home := flag.String("home", "", ""); cwd := flag.String("cwd", "", ""); flag.Parse()
    fixtures := map[string]string{}
    seed := func(id string, events []struct{ typ string; data any }) {
        lg, err := session.CreateSession(*home, *cwd, id, "default"); if err != nil { panic(err) }
        for _, ev := range events { if err := lg.Append(ev.typ, ev.data); err != nil { panic(err) } }
        fixtures[id] = lg.Path; if err := lg.Close(); err != nil { panic(err) }
    }
    seed("session-effort-A", []struct{typ string;data any}{
        {"model/selection",map[string]any{"provider":"magpie","model":"model-A","reasoningEffort":"high"}},
        {"request/context",map[string]any{"provider":"magpie","model":"model-A"}},
        {"user/message",session.UserMessage(nil)},
    })
    seed("session-effort-B", []struct{typ string;data any}{
        {"request/header",map[string]any{"header":map[string]any{"config":map[string]any{"provider":"magpie","model":"model-B","reasoningEffort":"xhigh"}}}},
        {"request/context",map[string]any{"provider":"magpie","model":"model-B"}},
        {"user/message",session.UserMessage(nil)},
    })
    seed("session-effort-old", []struct{typ string;data any}{{"request/context",map[string]any{"provider":"magpie","model":"model-old"}}})
    seed("session-effort-unknown", []struct{typ string;data any}{
        {"request/header",map[string]any{"header":map[string]any{"config":map[string]any{"reasoningEffort":"max"}}}},
        {"model/selection",map[string]any{"provider":"magpie","model":"model-unknown","reasoningEffort":"medium"}},
    })
    raw,_ := json.MarshalIndent(fixtures,"","  "); _, _ = os.Stdout.Write(raw)
}
'''


def native_image(pid: int) -> str:
    """返回 PID 的完整映像路径；只用于核对我们自己的 dshd。"""
    if os.name != "nt":
        return ""
    kernel32 = ctypes.windll.kernel32
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    handle = kernel32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
    if not handle:
        return ""
    try:
        size = wintypes.DWORD(32768)
        buf = ctypes.create_unicode_buffer(size.value)
        ok = kernel32.QueryFullProcessImageNameW(handle, 0, buf, ctypes.byref(size))
        return buf.value if ok else ""
    finally:
        kernel32.CloseHandle(handle)


def native_token_facts(pid: int) -> dict:
    """只读进程 token 诊断；不记录 credential，也不改变进程权限。"""
    if os.name != "nt":
        return {"available": False}
    kernel32 = ctypes.windll.kernel32
    advapi32 = ctypes.windll.advapi32
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.IsProcessInJob.argtypes = [wintypes.HANDLE, wintypes.HANDLE, ctypes.POINTER(wintypes.BOOL)]
    kernel32.IsProcessInJob.restype = wintypes.BOOL
    advapi32.OpenProcessToken.argtypes = [wintypes.HANDLE, wintypes.DWORD, ctypes.POINTER(wintypes.HANDLE)]
    advapi32.OpenProcessToken.restype = wintypes.BOOL
    advapi32.GetTokenInformation.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.LPVOID,
                                             wintypes.DWORD, ctypes.POINTER(wintypes.DWORD)]
    advapi32.GetTokenInformation.restype = wintypes.BOOL
    advapi32.ConvertSidToStringSidW.argtypes = [wintypes.LPVOID, ctypes.POINTER(wintypes.LPWSTR)]
    advapi32.ConvertSidToStringSidW.restype = wintypes.BOOL
    process = token = None
    stage = "OpenProcess"
    try:
        process = kernel32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
        if not process:
            raise ctypes.WinError()
        token = wintypes.HANDLE()
        stage = "OpenProcessToken"
        if not advapi32.OpenProcessToken(process, 0x0008, ctypes.byref(token)):  # TOKEN_QUERY
            raise ctypes.WinError()

        def information(cls: int, size: int = 4096) -> ctypes.Array:
            nonlocal stage
            stage = f"GetTokenInformation({cls})"
            buf = ctypes.create_string_buffer(size)
            needed = wintypes.DWORD()
            if not advapi32.GetTokenInformation(token, cls, buf, len(buf), ctypes.byref(needed)):
                raise ctypes.WinError()
            return buf

        def dword(cls: int) -> int:
            return ctypes.cast(information(cls, 4), ctypes.POINTER(wintypes.DWORD)).contents.value

        integrity = information(25)  # TokenIntegrityLevel -> SID_AND_ATTRIBUTES
        sid = ctypes.cast(integrity, ctypes.POINTER(ctypes.c_void_p)).contents.value
        text = wintypes.LPWSTR()
        if not advapi32.ConvertSidToStringSidW(sid, ctypes.byref(text)):
            raise ctypes.WinError()
        try:
            integrity_sid = text.value
        finally:
            kernel32.LocalFree(text)
        in_job = wintypes.BOOL()
        if not kernel32.IsProcessInJob(process, None, ctypes.byref(in_job)):
            raise ctypes.WinError()
        return {"available": True, "integrity_sid": integrity_sid,
                "session_id": dword(12), "elevated": bool(dword(20)),
                "has_restrictions": bool(dword(21)), "in_job": bool(in_job.value)}
    except OSError as exc:
        return {"available": False, "error": f"{stage}: {exc}"}
    finally:
        if token:
            kernel32.CloseHandle(token)
        if process:
            kernel32.CloseHandle(process)


def final_summary(ok: int, fail: int, skip: int = 0) -> None:
    print(f"OK={ok} FAIL={fail} SKIP={skip}", flush=True)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--binary", default=str(ROOT / "tmp" / "real-chain-current-bin" / "dshd.exe"))
    ap.add_argument("--ui", default=str(DEFAULT_UI))
    ap.add_argument("--model", default=os.environ.get("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"))
    ap.add_argument("--base", default=os.environ.get("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1"))
    ap.add_argument("--timeout", type=int, default=1200, help="每个 go test 阶段硬超时（秒）")
    args = ap.parse_args()
    binary = Path(args.binary).resolve()
    ui = Path(args.ui).resolve()
    run = Path(tempfile.mkdtemp(prefix="real-chain-current-", dir=str(ROOT / "tmp"))).resolve()
    home = run / "home"
    home.mkdir()
    print(f"EVIDENCE {run}", flush=True)
    host_rev = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    ui_rev = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ui, text=True).strip()
    commands: list[dict] = []
    phases: dict[str, dict] = {}
    go_procs: list[subprocess.Popen] = []
    proc = None
    go_proc = None
    total_ok = total_fail = total_skip = 0
    secret_values = [secrets.token_hex(16) for _ in range(3)]
    process_environment = {"probe": native_token_facts(os.getpid())}

    def record(name: str, argv: list[str], result: subprocess.CompletedProcess, log: Path) -> int:
        output = result.stdout + result.stderr
        log.write_text("$ " + json.dumps(argv, ensure_ascii=False) + "\n" + output +
                       f"\nEXIT_CODE={result.returncode}\n", encoding="utf-8")
        commands.append({"name": name, "argv": argv, "exit_code": result.returncode, "log": log.name})
        return result.returncode

    gate_cmd = [sys.executable, str(ROOT / "scripts" / "bin-check.py"), str(binary)]
    gate = subprocess.run(gate_cmd, cwd=ROOT, text=True, encoding="utf-8", errors="replace",
                          capture_output=True)
    record("bin-check", gate_cmd, gate, run / "bin-check.log")
    print(gate.stdout, end="", flush=True)
    if gate.returncode != 0 or not binary.is_file():
        print(f"FAIL bin-check exit={gate.returncode} binary={binary}")
        final_summary(0, 1)
        return 1

    try:
        with urllib.request.urlopen(args.base.rstrip("/") + "/models", timeout=8) as resp:
            catalog = json.load(resp)
        models = [x.get("id", "") for x in catalog.get("data", [])]
    except Exception as exc:
        print(f"SKIP magpie 不可达：{exc}")
        final_summary(0, 0, 1)
        return 2
    (run / "models.json").write_text(json.dumps({"base": args.base, "models": models}, ensure_ascii=False, indent=2), encoding="utf-8")
    if args.model not in models:
        print(f"SKIP magpie 模型目录缺少 {args.model}；实际={models}")
        final_summary(0, 0, 1)
        return 2

    go_exe = shutil_which_go()
    seed_dir = run / "fixture-seed"
    seed_dir.mkdir()
    seed_source = seed_dir / "main.go"
    seed_source.write_text(FIXTURE_SEED, encoding="utf-8")
    seed_cmd = [go_exe, "run", str(seed_source), "--home", str(home), "--cwd", str(ROOT)]
    seed = subprocess.run(seed_cmd, cwd=ROOT, text=True, encoding="utf-8", errors="replace", capture_output=True)
    os.replace(seed_source, seed_source.with_suffix(".go.txt"))
    record("real-v4-fixture-seed", seed_cmd, seed, run / "fixture-seed.log")
    if seed.returncode != 0:
        print(seed.stdout + seed.stderr, flush=True)
        final_summary(0, 1)
        return 1
    (run / "fixtures.json").write_text(seed.stdout, encoding="utf-8")
    schedule_dir = home / "state" / "schedules"
    schedule_dir.mkdir(parents=True)
    schedule_items = [
        {"id":"schedule-delete","title":"delete","prompt":"p","kind":"daily","daily_at":"09:00","next_at":"2099-01-01T09:00:00+08:00","status":"running"},
        {"id":"schedule-ended","title":"ended","prompt":"p","kind":"daily","daily_at":"09:00","next_at":"2020-01-01T09:00:00+08:00","status":"ended","ended_at":"2020-01-01T09:01:00+08:00"},
        {"id":"schedule-running","title":"running","prompt":"p","kind":"daily","daily_at":"09:00","next_at":"2099-01-01T09:00:00+08:00","status":"running"},
        {"id":"schedule-recurring","title":"recurring","prompt":"p","kind":"every","every_seconds":3600,"next_at":"2099-01-01T09:00:00+08:00"},
    ]
    (schedule_dir / "session-schedule-current.json").write_text(json.dumps({"version":1,"schedules":schedule_items}, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")

    serve_log_path = run / "dshd-stderr.log"
    serve_cmd = [str(binary), "serve", "--home", str(home), "--addr", "127.0.0.1:0",
                 "--policy", "ask", "--reasoning", "max", "--max-steps", "4",
                 "--model", args.model, "--parent-pid", str(os.getpid())]
    (run / "command.json").write_text(json.dumps({
        "dshd_rev": host_rev, "ui_rev": ui_rev,
        "binary": str(binary), "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "dshd_argv": serve_cmd, "temporary_home": str(home), "probe_python_pid": os.getpid(),
        "port": "OS assigned via --addr 127.0.0.1:0", "ui": str(ui),
        "model": args.model, "legacy_base": args.base,
        "process_environment": process_environment,
        "token": "ready stdout only; redacted", "cookie": "UI memory only; never logged",
        "commands": commands,
    }, ensure_ascii=False, indent=2), encoding="utf-8")
    creationflags = 0x08000000 if os.name == "nt" else 0
    exit_code = 1
    try:
        with serve_log_path.open("w", encoding="utf-8", errors="replace") as serve_log:
            proc = subprocess.Popen(serve_cmd, cwd=ROOT, stdin=subprocess.DEVNULL,
                                    stdout=subprocess.PIPE, stderr=serve_log,
                                    text=True, encoding="utf-8", errors="replace", creationflags=creationflags)
            commands.append({"name":"dshd serve", "argv":serve_cmd, "pid":proc.pid, "exit_code":None})
            lines: queue.Queue[str] = queue.Queue()
            threading.Thread(target=lambda: lines.put(proc.stdout.readline()), daemon=True).start()
            try:
                ready_line = lines.get(timeout=25).strip()
            except queue.Empty:
                raise RuntimeError("dshd 25 秒内未输出 ready 行")
            match = re.fullmatch(r"ready (\{.*\})", ready_line)
            if not match:
                raise RuntimeError(f"dshd ready 行非法：{ready_line!r}")
            ready = json.loads(match.group(1))
            if not ready.get("url", "").startswith("http://127.0.0.1:") or not ready.get("token"):
                raise RuntimeError("ready 行缺回环 url/token")
            process_environment["dshd"] = native_token_facts(proc.pid)
            (run / "ready-redacted.json").write_text(json.dumps({
                "url": ready["url"], "token": "<redacted>", "cookie": "<redacted>", "pid": proc.pid,
                "image": native_image(proc.pid), "home": str(home), "argv": serve_cmd,
                "process": process_environment["dshd"],
            }, ensure_ascii=False, indent=2), encoding="utf-8")
            print(f"DSHD pid={proc.pid} url={ready['url']} token=<redacted> cookie=<redacted>", flush=True)
            print("DSHD_ENV " + json.dumps(process_environment, ensure_ascii=False), flush=True)

            core_path = run / "real_chain_headless_test.go.txt"
            current_path = run / "real_chain_current_test.go.txt"
            core_path.write_text(GO_PROBE, encoding="utf-8")
            current_path.write_text(GO_CURRENT_PROBE, encoding="utf-8")
            core_target = ui / "internal" / "apiclient" / "real_chain_headless_test.go"
            current_target = ui / "internal" / "apiclient" / "real_chain_current_test.go"
            env = os.environ.copy()
            env.update({"RCH_BASE":ready["url"], "RCH_TOKEN":ready["token"], "RCH_HOME":str(home),
                        "RCH_OUT":str(run), "RCH_DSHD_STDERR":str(serve_log_path), "RCC_GO":go_exe,
                        "RCC_SECRET_A":secret_values[0], "RCC_SECRET_B":secret_values[1], "RCC_SECRET_WRONG":secret_values[2],
                        "DSHD_LLM_MODEL":args.model, "DSHD_LLM_BASE":args.base})

            overlay = run / "overlay-combined.json"
            overlay.write_text(json.dumps({"Replace":{str(core_target):str(core_path),str(current_target):str(current_path)}},ensure_ascii=False),encoding="utf-8")
            go_cmd = [go_exe,"test","-overlay="+str(overlay),"./internal/apiclient","-run","^TestRealChainCombined$",
                      "-count=1","-v","-timeout","18m"]
            go_proc = subprocess.Popen(go_cmd,cwd=ui,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT,text=True,encoding="utf-8",errors="replace",
                                       env=env,creationflags=creationflags)
            go_procs.append(go_proc)
            log = run / "probe.log"
            try:
                output,_ = go_proc.communicate(timeout=args.timeout)
            except subprocess.TimeoutExpired:
                go_proc.kill(); output,_ = go_proc.communicate(timeout=10)
                output += f"\nFAIL combined 超过 {args.timeout}s\n"
            log.write_text(output,encoding="utf-8")
            (run / "current-probe.log").write_text(output,encoding="utf-8")
            print(output,end="",flush=True)
            commands.append({"name":"core-then-current-same-process","argv":go_cmd,"pid":go_proc.pid,
                             "exit_code":go_proc.returncode,"log":log.name})
            for name,pattern in (("core",SUMMARY_RE),("current",CURRENT_SUMMARY_RE)):
                summaries = pattern.findall(output)
                if summaries:
                    ok,fail,skip = map(int,summaries[-1])
                else:
                    ok,fail,skip = 0,1,0
                    print(f"FAIL {name} 无机器汇总")
                if ok == 0:
                    fail += 1
                    print(f"FAIL {name} 0 条有效断言")
                phases[name] = {"ok":ok,"fail":fail,"skip":skip,"go_test_exit":go_proc.returncode,"log":log.name}
                total_ok += ok; total_fail += fail; total_skip += skip
                print(f"{name.upper()}_RESULT OK={ok} FAIL={fail} SKIP={skip} combined_go_test_exit={go_proc.returncode}",flush=True)
            if go_proc.returncode != 0 and all(p["fail"] == 0 for p in phases.values()):
                total_fail += 1
                print("FAIL combined go test 非零但两个阶段没有归属 FAIL")

            leaked: list[str] = []
            for base, _, files in os.walk(run):
                for name in files:
                    path = Path(base) / name
                    relative = path.relative_to(run)
                    # 唯一合法含 literal 的是明确 write-only 的持久秘密存储；Transport、日志和其余证据仍全扫。
                    if relative.as_posix() == "home/state/model-catalog.json":
                        continue
                    if path.suffix.lower() not in {".json",".log",".txt",".md"} or path.stat().st_size > 16<<20:
                        continue
                    raw = path.read_bytes()
                    if any(value.encode() in raw for value in secret_values):
                        leaked.append(str(path.relative_to(run)))
            (run / "secret-scan.json").write_text(json.dumps({"leaked_files":leaked,"literal_count":len(secret_values),
                "excluded_write_only_store":"home/state/model-catalog.json"},indent=2),encoding="utf-8")
            if leaked:
                total_fail += 1
                print("FAIL credential literal 泄漏文件=" + json.dumps(leaked,ensure_ascii=False))
            else:
                print("SECRET_SCAN_OK literals=3 leaked_files=0")
            print(f"CORE_OK={phases['core']['ok']} CORE_FAIL={phases['core']['fail']} CURRENT_OK={phases['current']['ok']} CURRENT_FAIL={phases['current']['fail']}")
            final_summary(total_ok,total_fail,total_skip)
            exit_code = 0 if total_fail == 0 and total_skip == 0 and phases["core"]["ok"] >= 72 and all(p["go_test_exit"] == 0 for p in phases.values()) else 1
            return exit_code
    except Exception as exc:
        print(f"FAIL 探针执行异常：{type(exc).__name__}: {exc}")
        total_fail += 1
        final_summary(total_ok,total_fail,total_skip)
        return 1
    finally:
        cleanup = {"dshd_pid":proc.pid if proc else None, "go_test_pids":[p.pid for p in go_procs]}
        for item in go_procs:
            if item.poll() is None:
                item.kill()
                try: item.wait(timeout=10)
                except subprocess.TimeoutExpired: pass
        cleanup["go_test_exits"] = [p.returncode for p in go_procs]
        if proc is not None:
            image = native_image(proc.pid) if proc.poll() is None else ""
            expected = os.path.normcase(str(binary))
            owned = bool(image) and os.path.normcase(image) == expected
            cleanup.update({"dshd_image":image,"dshd_expected":str(binary),"dshd_owner_verified":owned})
            if proc.poll() is None and owned and os.name == "nt":
                killed = subprocess.run(["taskkill","/PID",str(proc.pid),"/T","/F"],
                                        capture_output=True,text=True,encoding="mbcs",errors="replace")
                cleanup["taskkill_exit"] = killed.returncode
                cleanup["taskkill_output"] = (killed.stdout+killed.stderr).strip()
            if proc.poll() is None and owned: proc.kill()
            try: proc.wait(timeout=15)
            except subprocess.TimeoutExpired: pass
            cleanup["dshd_exit"] = proc.returncode
            for command in commands:
                if command["name"] == "dshd serve": command["exit_code"] = proc.returncode
        cleanup["home_shape"] = home_shape(home)
        (run / "cleanup.json").write_text(json.dumps(cleanup,ensure_ascii=False,indent=2),encoding="utf-8")
        machine = {"dshd_rev":host_rev,"ui_rev":ui_rev,"binary":str(binary),
                   "binary_sha256":hashlib.sha256(binary.read_bytes()).hexdigest(),"evidence":str(run),
                   "home":str(home),"commands":commands,"phases":phases,"process_environment":process_environment,
                   "ok":total_ok,"fail":total_fail,"skip":total_skip,"script_exit":exit_code,"cleanup":cleanup}
        (run / "machine-summary.json").write_text(json.dumps(machine,ensure_ascii=False,indent=2),encoding="utf-8")
        (run / "command.json").write_text(json.dumps({"dshd_rev":host_rev,"ui_rev":ui_rev,"dshd_argv":serve_cmd,
            "temporary_home":str(home),"process_environment":process_environment,"commands":commands},ensure_ascii=False,indent=2),encoding="utf-8")
        print("CLEANUP "+json.dumps({"dshd_exit":cleanup.get("dshd_exit"),"owner_verified":cleanup.get("dshd_owner_verified"),
              "taskkill_exit":cleanup.get("taskkill_exit"),"home_entries":len(cleanup["home_shape"])},ensure_ascii=False),flush=True)


def home_shape(home: Path) -> list[dict]:
    out = []
    for base, dirs, files in os.walk(home):
        dirs.sort()
        files.sort()
        rel = Path(base).relative_to(home)
        for name in files:
            p = Path(base) / name
            # 只记形状与大小，不读会话内容，不收集 token/cookie。
            try:
                size = p.stat().st_size
            except OSError:
                size = -1
            out.append({"path": str(rel / name), "bytes": size})
    return out


def shutil_which_go() -> str:
    # 不依赖调用者 PATH；本机标准安装位置优先，仍允许 PATH 中的 Go。
    standard = Path(r"C:\Program Files\Go\bin\go.exe")
    if standard.is_file():
        return str(standard)
    from shutil import which
    found = which("go")
    if not found:
        raise RuntimeError("找不到 go.exe")
    return found


if __name__ == "__main__":
    sys.exit(main())
