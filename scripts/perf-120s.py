#!/usr/bin/env python3
"""HOST-PERF-120S：真实 dshd Transport 的五会话 120 秒进程级探针。

go test -c 只注入 cmd/dshd/perf_120s_probe_test.go 的合成子进程；父进程执行
产品自己的 cmdServe。真实 ready token→cookie、HTTP、RFC7692 WS、v4 follow、
Transport journal、approval broker 与磁盘持久化均参与，不用 mockd/模型/微基准。

默认证据目录是 tmp/perf-120s-final；目录必须不存在或为空，绝不清空旧证据。
"""
from __future__ import annotations

import argparse
import base64
import ctypes
from ctypes import wintypes
import datetime as dt
import hashlib
import http.cookiejar
import json
import math
import os
from pathlib import Path
import platform
import queue
import re
import secrets
import socket
import statistics
import struct
import subprocess
import sys
import threading
import time
import zlib
from collections import Counter

import psutil
import requests

ROOT = Path(__file__).resolve().parents[1]
DURATION_SECONDS = 120.0
SESSION_COUNT = 5
SAMPLE_INTERVAL_SECONDS = 0.25
PERCENTILE_ALGORITHM = "nearest-rank: sort ascending; rank=ceil(p/100*N), 1-based; no interpolation"
THRESHOLDS = {
    "min_wall_seconds": 120.0,
    "max_resource_sample_gap_seconds": 1.0,
    "min_resource_samples": 400,
    "min_rtt_samples_per_session": 60,
    "max_rtt_p95_ms": 250.0,
    "max_dispatch_p95_ms": 1500.0,
    "max_dispatch_p99_ms": 3000.0,
    "max_tree_cpu_p95_one_core_percent": 500.0,
    "max_tree_working_set_bytes": 512 * 1024 * 1024,
    "max_tree_private_bytes": 512 * 1024 * 1024,
    "max_tree_handles": 4096,
    "max_tree_threads": 512,
    "max_private_growth_bytes": 128 * 1024 * 1024,
    "max_handle_growth": 256,
    "max_thread_growth": 64,
    "max_frames_per_session": 1500,
}
WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
WS_DEFLATE_OFFER = "permessage-deflate; client_no_context_takeover; server_no_context_takeover"
SECRET_RE = re.compile(r"(?:KEY|TOKEN|COOKIE|SECRET|PASSWORD|CREDENTIAL)", re.I)


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def wall_ms() -> int:
    return time.time_ns() // 1_000_000


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def nearest_rank(values: list[float], percentile: float) -> float:
    if not values:
        raise ValueError("0 样本没有分位数")
    ordered = sorted(values)
    rank = math.ceil(percentile / 100.0 * len(ordered))
    return ordered[max(1, rank) - 1]


def distribution(values: list[float]) -> dict:
    return {
        "algorithm": PERCENTILE_ALGORITHM,
        "count": len(values),
        "p50": nearest_rank(values, 50) if values else None,
        "p95": nearest_rank(values, 95) if values else None,
        "p99": nearest_rank(values, 99) if values else None,
        "max": max(values) if values else None,
        "min": min(values) if values else None,
        "mean": statistics.fmean(values) if values else None,
    }


def json_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def read_jsonl(path: Path) -> list[dict]:
    rows = []
    if not path.is_file():
        return rows
    with path.open("r", encoding="utf-8") as f:
        for line_no, line in enumerate(f, 1):
            if line.strip():
                row = json.loads(line)
                row["_line"] = line_no
                rows.append(row)
    return rows


class Evidence:
    def __init__(self, run: Path):
        run.mkdir(parents=True, exist_ok=False)
        self.run = run
        self.files: dict[str, object] = {}
        self.locks: dict[str, threading.Lock] = {}
        self.init_lock = threading.Lock()
        self.checks: list[dict] = []
        self.ok = 0
        self.fail = 0

    def jsonl(self, name: str, row: dict) -> None:
        # 文件句柄与对应锁必须作为一个整体初始化；五会话首帧同时到达时，
        # 不能让两个线程各自打开句柄后互相覆盖 files/locks 登记。
        if not hasattr(self, "init_lock"):
            self.init_lock = threading.Lock()
        with self.init_lock:
            if name not in self.files:
                self.files[name] = (self.run / name).open("a", encoding="utf-8", newline="\n")
                self.locks[name] = threading.Lock()
        with self.locks[name]:
            self.files[name].write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
            self.files[name].flush()

    def save(self, name: str, value: object) -> None:
        (self.run / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    def check(self, name: str, condition: bool, detail: object = "") -> bool:
        row = {"timeUtc": utc_now(), "name": name, "pass": bool(condition), "detail": detail}
        self.checks.append(row)
        if condition:
            self.ok += 1
            print(f"PERF_OK {name} {detail}", flush=True)
        else:
            self.fail += 1
            print(f"PERF_FAIL {name} {detail}", flush=True)
        self.jsonl("checks.jsonl", row)
        return condition

    def close(self) -> None:
        for f in self.files.values():
            f.close()
        self.files.clear()


class APIError(RuntimeError):
    def __init__(self, status: int, payload: object):
        super().__init__(f"HTTP {status}: {payload}")
        self.status = status
        self.payload = payload


class API:
    def __init__(self, base: str, evidence: Evidence):
        self.base = base.rstrip("/")
        self.evidence = evidence
        self.cookie = ""

    def connect(self, token: str) -> None:
        started = time.perf_counter()
        response = requests.get(self.base + "/connect", params={"token": token}, allow_redirects=False, timeout=10)
        cookie = response.cookies.get("dshd_session", "")
        self.evidence.jsonl("http.jsonl", {"timeUtc": utc_now(), "method": "CONNECT", "status": response.status_code,
            "rttMs": (time.perf_counter() - started) * 1000, "cookie": "<redacted>" if cookie else "missing"})
        if response.status_code != 303 or not cookie:
            raise RuntimeError(f"ready token 换 cookie 失败：status={response.status_code} cookie={bool(cookie)}")
        self.cookie = cookie

    def call(self, method: str, body: dict, timeout: float = 20) -> dict:
        started = time.perf_counter()
        try:
            response = requests.post(self.base + "/api/" + method, data=json_bytes(body),
                headers={"Content-Type": "application/json", "Cookie": "dshd_session=" + self.cookie}, timeout=timeout)
            elapsed = (time.perf_counter() - started) * 1000
            try:
                payload = response.json()
            except Exception:
                payload = {"nonJSON": response.text[:500]}
            self.evidence.jsonl("http.jsonl", {"timeUtc": utc_now(), "method": method,
                "status": response.status_code, "rttMs": elapsed, "sessionId": body.get("sessionId", body.get("id"))})
            if response.status_code != 200:
                raise APIError(response.status_code, payload)
            if "result" not in payload:
                raise RuntimeError(f"{method} 成功响应缺 result")
            return payload["result"]
        except requests.RequestException as exc:
            self.evidence.jsonl("http.jsonl", {"timeUtc": utc_now(), "method": method,
                "status": 0, "rttMs": (time.perf_counter() - started) * 1000, "error": str(exc)})
            raise

    def debug_stats(self) -> dict:
        response = requests.get(self.base + "/debug/stats",
            headers={"Cookie": "dshd_session=" + self.cookie}, timeout=3)
        if response.status_code != 200:
            raise RuntimeError(f"debug stats HTTP {response.status_code}")
        payload = response.json()
        return payload.get("result", payload)


class RawWS:
    """小型 RFC6455/RFC7692 客户端；每消息独立解压，不保留上下文字典。"""

    def __init__(self, base: str, cookie: str, on_frame, on_close):
        self.base = base
        self.cookie = cookie
        self.on_frame = on_frame
        self.on_close = on_close
        self.sock: socket.socket | None = None
        self.buffer = b""
        self.send_lock = threading.Lock()
        self.state_lock = threading.Lock()
        self.closed = False
        self.intentional_close = False
        self.ping_started: float | None = None
        self.connection_id = 0
        self.note = ""
        self.thread: threading.Thread | None = None
        self.close_error = ""

    def connect(self) -> None:
        authority = self.base.split("://", 1)[1]
        host, port_s = authority.rsplit(":", 1)
        self.sock = socket.create_connection((host, int(port_s)), timeout=10)
        self.sock.settimeout(10)
        key = base64.b64encode(secrets.token_bytes(16)).decode("ascii")
        request = ("GET /ws HTTP/1.1\r\nHost: " + authority + "\r\nUpgrade: websocket\r\n"
            "Connection: Upgrade\r\nSec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n"
            "Sec-WebSocket-Extensions: " + WS_DEFLATE_OFFER + "\r\nCookie: dshd_session=" + self.cookie + "\r\n\r\n")
        self.sock.sendall(request.encode("ascii"))
        response = self._read_until(b"\r\n\r\n")
        header, self.buffer = response
        head = header.decode("iso-8859-1")
        lines = head.split("\r\n")
        if not lines[0].startswith("HTTP/1.1 101"):
            raise RuntimeError("WS 握手失败：" + lines[0])
        expected = base64.b64encode(hashlib.sha1((key + WS_GUID).encode("ascii")).digest()).decode("ascii")
        headers = {}
        for line in lines[1:]:
            if ":" in line:
                k, v = line.split(":", 1)
                headers[k.lower().strip()] = v.strip()
        if headers.get("sec-websocket-accept") != expected:
            raise RuntimeError("WS Sec-WebSocket-Accept 不符")
        if headers.get("sec-websocket-extensions") != WS_DEFLATE_OFFER:
            raise RuntimeError("WS 未协商预期双向 no-context-takeover deflate")
        self.sock.settimeout(None)

    def _read_until(self, marker: bytes):
        while marker not in self.buffer:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise EOFError("WS 握手提前关闭")
            self.buffer += chunk
        end = self.buffer.index(marker) + len(marker)
        return self.buffer[:end], self.buffer[end:]

    def start(self) -> None:
        self.thread = threading.Thread(target=self._run, name=f"perf-ws-{self.connection_id}", daemon=True)
        self.thread.start()

    def _recv_exact(self, count: int) -> bytes:
        while len(self.buffer) < count:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise EOFError("WS 对端关闭")
            self.buffer += chunk
        out, self.buffer = self.buffer[:count], self.buffer[count:]
        return out

    def _run(self) -> None:
        error = ""
        try:
            while True:
                with self.state_lock:
                    if self.closed:
                        break
                header = self._recv_exact(2)
                first, second = header[0], header[1]
                fin = bool(first & 0x80)
                rsv1 = bool(first & 0x40)
                opcode = first & 0x0F
                if not fin or first & 0x30 or second & 0x80:
                    raise RuntimeError("服务端帧 FIN/RSV/mask 非法")
                length = second & 0x7F
                if length == 126:
                    length = struct.unpack("!H", self._recv_exact(2))[0]
                elif length == 127:
                    length = struct.unpack("!Q", self._recv_exact(8))[0]
                if length > 1 << 20:
                    raise RuntimeError(f"WS 线上正文过大：{length}")
                payload = self._recv_exact(length)
                received_mono = time.perf_counter()
                received_ms = wall_ms()
                if opcode == 0x9:
                    self._send_frame(0xA, payload)
                    continue
                if opcode == 0xA:
                    continue
                if opcode == 0x8:
                    self._send_frame(0x8, payload)
                    break
                if opcode != 0x1:
                    raise RuntimeError(f"未知 WS opcode={opcode}")
                wire_bytes = len(payload)
                if rsv1:
                    inflater = zlib.decompressobj(-15)
                    payload = inflater.decompress(payload + b"\x00\x00\xff\xff\x01\x00\x00\xff\xff")
                    payload += inflater.flush()
                if len(payload) > 1 << 20:
                    raise RuntimeError("WS 解压后超过 1 MiB")
                frame = json.loads(payload.decode("utf-8"))
                self.on_frame(self, frame, received_mono, received_ms, wire_bytes, rsv1)
        except Exception as exc:
            with self.state_lock:
                intentional = self.intentional_close or self.closed
            if not intentional:
                error = f"{type(exc).__name__}: {exc}"
        self.close_error = error
        self.on_close(self, error)

    def _send_frame(self, opcode: int, payload: bytes) -> None:
        if len(payload) > 1 << 20:
            raise RuntimeError("客户端帧超过 1 MiB")
        mask = secrets.token_bytes(4)
        first = bytes([0x80 | opcode])
        n = len(payload)
        if n < 126:
            header = first + bytes([0x80 | n])
        elif n <= 0xFFFF:
            header = first + bytes([0x80 | 126]) + struct.pack("!H", n)
        else:
            header = first + bytes([0x80 | 127]) + struct.pack("!Q", n)
        masked = bytes(v ^ mask[i % 4] for i, v in enumerate(payload))
        with self.send_lock:
            with self.state_lock:
                if self.closed:
                    return
            self.sock.sendall(header + mask + masked)

    def send_json(self, value: dict) -> None:
        self._send_frame(0x1, json_bytes(value))

    def subscribe(self, session_id: str, epoch: int, since: int) -> None:
        self.send_json({"t": "sub", "topic": "session:" + session_id, "sinceSeq": since, "journalEpoch": epoch})

    def ping(self) -> bool:
        with self.state_lock:
            if self.closed or self.ping_started is not None:
                return False
            self.ping_started = time.perf_counter()
        try:
            self.send_json({"t": "ping"})
            return True
        except Exception:
            with self.state_lock:
                self.ping_started = None
            return False

    def pop_rtt(self, received_mono: float) -> float | None:
        with self.state_lock:
            if self.ping_started is None:
                return None
            started, self.ping_started = self.ping_started, None
        return (received_mono - started) * 1000

    def close(self, intentional: bool = True) -> None:
        with self.state_lock:
            if self.closed:
                return
            self.intentional_close = intentional
            self.closed = True
            sock = self.sock
        if sock is not None:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                sock.close()
            except OSError:
                pass

    def join(self, timeout: float = 5) -> None:
        if self.thread is not None:
            self.thread.join(timeout)


class SessionRuntime:
    def __init__(self, label: str, session_id: str, epoch: int, api: API, evidence: Evidence):
        self.label = label
        self.id = session_id
        self.epoch = epoch
        self.api = api
        self.evidence = evidence
        self.marker = f"P120-{label}-"
        self.cond = threading.Condition()
        self.current: RawWS | None = None
        self.connection_serial = 0
        self.last_seq = 0
        self.first_seq = None
        self.type_counts = Counter()
        self.seq_errors = []
        self.cross_talk = []
        self.unexpected_close = []
        self.done_turns = set()
        self.error_turns = set()
        self.approvals = []
        self.resets = []
        self.stop_heartbeat = threading.Event()
        self.heartbeat = threading.Thread(target=self._heartbeat, name=f"perf-heartbeat-{label}", daemon=True)
        self.heartbeat.start()

    def _heartbeat(self) -> None:
        while not self.stop_heartbeat.wait(0.5):
            with self.cond:
                ws = self.current
            if ws is not None:
                ws.ping()

    def connect(self, since: int | None = None, epoch: int | None = None, note: str = "live") -> int:
        self.close_ws("replace-" + note)
        with self.cond:
            self.connection_serial += 1
            serial = self.connection_serial
            if since is None:
                since = self.last_seq
            use_epoch = self.epoch if epoch is None else epoch
        ws = RawWS(self.api.base, self.api.cookie, self._on_frame, self._on_close)
        ws.connection_id = serial
        ws.note = note
        ws.connect()
        with self.cond:
            self.current = ws
        self.evidence.jsonl("connections.jsonl", {"timeUtc": utc_now(), "session": self.label,
            "sessionId": self.id, "connectionId": serial, "note": note, "sinceSeq": since,
            "submittedEpoch": use_epoch, "compression": WS_DEFLATE_OFFER})
        ws.start()
        ws.subscribe(self.id, use_epoch, since)
        return serial

    def close_ws(self, reason: str) -> int:
        with self.cond:
            ws = self.current
            self.current = None
        if ws is not None:
            ws.close(True)
            ws.join()
        with self.cond:
            # join 后取水位：关闭前已入 socket 缓冲并被读线程交付的帧也必须计入，
            # 否则下次 since 回退会制造重复正序帧。
            watermark = self.last_seq
        if ws is not None:
            self.evidence.jsonl("connections.jsonl", {"timeUtc": utc_now(), "session": self.label,
                "sessionId": self.id, "connectionId": ws.connection_id, "note": ws.note,
                "closed": True, "reason": reason, "watermark": watermark, "closeError": ws.close_error})
        return watermark

    def _on_close(self, ws: RawWS, error: str) -> None:
        if error:
            with self.cond:
                self.unexpected_close.append({"connectionId": ws.connection_id, "note": ws.note, "error": error})
                self.cond.notify_all()

    def _on_frame(self, ws: RawWS, frame: dict, received_mono: float, received_ms: int,
                  wire_bytes: int, compressed: bool) -> None:
        seq = int(frame.get("seq", 0) or 0)
        typ = frame.get("type", "")
        data = frame.get("data")
        frame_time = int(frame.get("time", 0) or 0)
        dispatch_ms = received_ms - frame_time if frame_time > 0 else None
        self.evidence.jsonl("frames.jsonl", {"timeUtc": utc_now(), "session": self.label,
            "sessionId": self.id, "connectionId": ws.connection_id, "connectionNote": ws.note,
            "seq": seq, "type": typ, "data": data, "time": frame_time, "receivedMs": received_ms,
            "dispatchMs": dispatch_ms, "wireBytes": wire_bytes, "jsonBytes": len(json_bytes(frame)),
            "compressed": compressed})
        if dispatch_ms is not None and typ != "pong":
            self.evidence.jsonl("dispatch.jsonl", {"session": self.label, "sessionId": self.id,
                "connectionId": ws.connection_id, "seq": seq, "type": typ, "dispatchMs": dispatch_ms,
                "time": frame_time, "receivedMs": received_ms})
        if typ == "pong":
            rtt = ws.pop_rtt(received_mono)
            if rtt is not None:
                self.evidence.jsonl("rtt.jsonl", {"session": self.label, "sessionId": self.id,
                    "connectionId": ws.connection_id, "rttMs": rtt, "timeUtc": utc_now()})
        with self.cond:
            if seq > 0:
                if self.first_seq is None:
                    self.first_seq = seq
                if seq != self.last_seq + 1:
                    self.seq_errors.append({"connectionId": ws.connection_id, "expected": self.last_seq + 1, "got": seq})
                self.last_seq = max(self.last_seq, seq)
                self.type_counts[typ] += 1
            raw_data = json.dumps(data, ensure_ascii=False)
            for other in "ABCDE":
                if other != self.label and f"P120-{other}-" in raw_data:
                    self.cross_talk.append({"seq": seq, "type": typ, "foreign": other})
            if typ == "approval/request" and isinstance(data, dict):
                self.approvals.append({"connectionId": ws.connection_id, "connectionNote": ws.note,
                    "seq": seq, "requestId": data.get("requestId", ""), "data": data})
            elif typ == "session/reset":
                self.resets.append({"connectionId": ws.connection_id, "connectionNote": ws.note,
                    "seq": seq, "data": data})
            elif typ == "turn/done" and isinstance(data, dict):
                self.done_turns.add(data.get("turnId", ""))
            elif typ == "turn/error" and isinstance(data, dict):
                self.error_turns.add(data.get("turnId", ""))
            self.cond.notify_all()

    def wait_turn(self, turn_id: str, timeout: float = 20) -> bool:
        deadline = time.monotonic() + timeout
        with self.cond:
            while turn_id not in self.done_turns and turn_id not in self.error_turns:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return False
                self.cond.wait(remaining)
            return turn_id in self.done_turns

    def wait_approval(self, baseline: int, timeout: float = 20) -> dict | None:
        deadline = time.monotonic() + timeout
        with self.cond:
            while True:
                candidates = [a for a in self.approvals if a["seq"] > baseline]
                if candidates:
                    return dict(candidates[-1])
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return None
                self.cond.wait(remaining)

    def wait_pending_replay(self, connection_id: int, request_id: str, timeout: float = 10) -> bool:
        deadline = time.monotonic() + timeout
        with self.cond:
            while True:
                if any(a["connectionId"] == connection_id and a["seq"] == 0 and a["requestId"] == request_id
                       for a in self.approvals):
                    return True
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return False
                self.cond.wait(remaining)

    def wait_reset(self, connection_id: int, timeout: float = 10) -> dict | None:
        deadline = time.monotonic() + timeout
        with self.cond:
            while True:
                rows = [r for r in self.resets if r["connectionId"] == connection_id]
                if rows:
                    return dict(rows[-1])
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return None
                self.cond.wait(remaining)

    def stop(self) -> None:
        self.stop_heartbeat.set()
        self.close_ws("final")
        self.heartbeat.join(timeout=3)


class LoadController:
    def __init__(self, sessions: list[SessionRuntime], evidence: Evidence):
        self.sessions = sessions
        self.evidence = evidence
        self.cond = threading.Condition()
        self.pause = False
        self.stop = False
        self.idle: set[str] = set()
        self.iterations = {s.label: 0 for s in sessions}
        self.failed_turns = []
        self.threads = [threading.Thread(target=self._worker, args=(s,), name=f"perf-load-{s.label}", daemon=True)
                        for s in sessions]

    def start(self) -> None:
        for thread in self.threads:
            thread.start()

    def _next_iteration(self, label: str) -> int:
        with self.cond:
            self.iterations[label] += 1
            return self.iterations[label]

    def execute_turn(self, session: SessionRuntime, purpose: str = "normal") -> tuple[bool, str, str]:
        iteration = self._next_iteration(session.label)
        marker = f"P120-{session.label}-{iteration:06d}"
        prompt = f"PERF120S marker={marker} iteration={iteration}"
        started_mono = time.perf_counter()
        row = {"session": session.label, "sessionId": session.id, "iteration": iteration,
               "marker": marker, "purpose": purpose, "startUtc": utc_now()}
        try:
            result = session.api.call("session.prompt", {"sessionId": session.id, "text": prompt})
            turn_id = result.get("turnId", "")
            row["turnId"] = turn_id
            done = session.wait_turn(turn_id, timeout=25)
            row["done"] = done
            row["error"] = turn_id in session.error_turns
        except Exception as exc:
            turn_id = row.get("turnId", "")
            done = False
            row["done"] = False
            row["exception"] = f"{type(exc).__name__}: {exc}"
        row["elapsedMs"] = (time.perf_counter() - started_mono) * 1000
        self.evidence.jsonl("turns.jsonl", row)
        if not done:
            with self.cond:
                self.failed_turns.append(row)
        return done, turn_id, marker

    def _worker(self, session: SessionRuntime) -> None:
        while True:
            with self.cond:
                while self.pause and not self.stop:
                    self.idle.add(session.label)
                    self.cond.notify_all()
                    self.cond.wait(0.2)
                self.idle.discard(session.label)
                if self.stop:
                    self.idle.add(session.label)
                    self.cond.notify_all()
                    return
            self.execute_turn(session)

    def pause_all(self, timeout: float = 35) -> bool:
        with self.cond:
            self.pause = True
            self.cond.notify_all()
            deadline = time.monotonic() + timeout
            while len(self.idle) < len(self.sessions):
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return False
                self.cond.wait(remaining)
            return True

    def resume(self) -> None:
        with self.cond:
            self.pause = False
            self.idle.clear()
            self.cond.notify_all()

    def finish(self) -> None:
        with self.cond:
            self.stop = True
            self.pause = True
            self.cond.notify_all()
        for thread in self.threads:
            thread.join(timeout=30)


def gui_resources(pid: int) -> dict:
    if os.name != "nt":
        return {"supported": False, "gdi": None, "user": None}
    kernel32 = ctypes.windll.kernel32
    user32 = ctypes.windll.user32
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    handle = kernel32.OpenProcess(0x1000, False, pid)
    if not handle:
        return {"supported": True, "gdi": None, "user": None, "error": "OpenProcess failed"}
    try:
        user32.GetGuiResources.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        user32.GetGuiResources.restype = wintypes.DWORD
        gdi = int(user32.GetGuiResources(handle, 0))
        user = int(user32.GetGuiResources(handle, 1))
        return {"supported": True, "gdi": gdi, "user": user}
    finally:
        kernel32.CloseHandle(handle)


class ResourceSampler:
    def __init__(self, root_pid: int, expected_image: Path, api: API, evidence: Evidence):
        self.root_pid = root_pid
        self.expected_image = expected_image
        self.api = api
        self.evidence = evidence
        self.stop_event = threading.Event()
        self.sample_lock = threading.Lock()
        self.previous_cpu: dict[tuple[int, float], tuple[float, float]] = {}
        self.previous_sample_mono = None
        self.count = 0
        self.started = False
        self.thread = threading.Thread(target=self._run, name="perf-resource-sampler", daemon=True)

    def start(self) -> None:
        self.sample_once("startup")
        self.thread.start()
        self.started = True

    def _run(self) -> None:
        next_at = time.monotonic()
        while not self.stop_event.wait(max(0, next_at - time.monotonic())):
            self.sample_once("steady")
            next_at += SAMPLE_INTERVAL_SECONDS
            if next_at < time.monotonic() - SAMPLE_INTERVAL_SECONDS:
                next_at = time.monotonic() + SAMPLE_INTERVAL_SECONDS

    def sample_once(self, phase: str) -> None:
        with self.sample_lock:
            now_mono = time.monotonic()
            elapsed = None if self.previous_sample_mono is None else now_mono - self.previous_sample_mono
            rows = []
            try:
                root = psutil.Process(self.root_pid)
                processes = [root] + root.children(recursive=True)
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                processes = []
            for proc in processes:
                try:
                    with proc.oneshot():
                        create = proc.create_time()
                        times = proc.cpu_times()
                        total_cpu = float(times.user + times.system)
                        mem = proc.memory_info()
                        key = (proc.pid, create)
                        previous = self.previous_cpu.get(key)
                        cpu = None
                        if previous is not None and elapsed is not None and elapsed > 0:
                            cpu = max(0.0, total_cpu - previous[1]) / elapsed * 100.0
                        self.previous_cpu[key] = (now_mono, total_cpu)
                        try:
                            cmdline = proc.cmdline()
                        except (psutil.AccessDenied, psutil.NoSuchProcess):
                            cmdline = []
                        try:
                            exe = proc.exe()
                        except (psutil.AccessDenied, psutil.NoSuchProcess):
                            exe = ""
                        same_image = bool(exe) and os.path.normcase(exe) == os.path.normcase(str(self.expected_image))
                        rows.append({"pid": proc.pid, "ppid": proc.ppid(), "name": proc.name(),
                            "createTime": create, "exe": exe, "ownedImage": same_image,
                            "ownedByProbeTree": True, "cmdline": cmdline,
                            "status": proc.status(), "cpuTimesSeconds": total_cpu,
                            "cpuPercentOneCore": cpu, "workingSetBytes": int(getattr(mem, "wset", mem.rss)),
                            "rssBytes": int(mem.rss), "privateBytes": int(getattr(mem, "private", 0)),
                            "handles": proc.num_handles() if hasattr(proc, "num_handles") else None,
                            "threads": proc.num_threads(), "fd": None, "fdSupported": False,
                            "fdNote": "Windows psutil 无 num_fds；不是 0", **gui_resources(proc.pid)})
                except (psutil.NoSuchProcess, psutil.AccessDenied, ProcessLookupError):
                    continue
            valid_cpu = [r["cpuPercentOneCore"] for r in rows if r["cpuPercentOneCore"] is not None]
            tree = {"processCount": len(rows), "cpuPercentOneCore": sum(valid_cpu) if valid_cpu else None,
                "cpuPercentAllCores": (sum(valid_cpu) / (psutil.cpu_count() or 1)) if valid_cpu else None,
                "cpuSamples": len(valid_cpu), "workingSetBytes": sum(r["workingSetBytes"] for r in rows),
                "privateBytes": sum(r["privateBytes"] for r in rows),
                "handles": sum(r["handles"] or 0 for r in rows), "threads": sum(r["threads"] for r in rows),
                "gdi": sum(r["gdi"] or 0 for r in rows), "userObjects": sum(r["user"] or 0 for r in rows)}
            try:
                debug = self.api.debug_stats()
            except Exception as exc:
                debug = {"error": f"{type(exc).__name__}: {exc}"}
            self.count += 1
            row = {"sample": self.count, "phase": phase, "timeUtc": utc_now(),
                "monotonic": now_mono, "intervalSeconds": elapsed, "rootPid": self.root_pid,
                "rootPresent": bool(rows),
                "allObservedProcessesInOwnedTree": bool(rows) and all(r["ownedByProbeTree"] for r in rows),
                "tree": tree, "debug": debug, "processes": rows}
            self.evidence.jsonl("resources.jsonl", row)
            if phase == "startup":
                self.evidence.save("process-tree-start.json", row)
            elif phase == "end":
                self.evidence.save("process-tree-end.json", row)
            self.previous_sample_mono = now_mono

    def finish(self) -> None:
        self.stop_event.set()
        if self.started:
            self.thread.join(timeout=5)
        self.sample_once("end")


def native_image(pid: int) -> str:
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
    found = shutil_which("go")
    if not found:
        raise RuntimeError("找不到 Go")
    return found


def shutil_which(name: str) -> str | None:
    from shutil import which
    return which(name)


def git_output(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=ROOT, text=True, encoding="utf-8", errors="replace",
                          capture_output=True, check=True).stdout


def source_manifest() -> list[dict]:
    rows = []
    for root_name in ("cmd", "internal", "modules", "codecs"):
        for path in sorted((ROOT / root_name).rglob("*.go")):
            rows.append({"path": str(path.relative_to(ROOT)).replace("\\", "/"),
                "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    return rows


def history_page(api: API, session: SessionRuntime, since: int, limit: int = 500) -> dict:
    return api.call("session.history", {"id": session.id, "journalEpoch": session.epoch,
                                      "sinceSeq": since, "limit": limit})


def wait_history_turn(api: API, session: SessionRuntime, since: int, turn_id: str,
                      timeout: float = 25) -> dict | None:
    deadline = time.monotonic() + timeout
    latest = None
    while time.monotonic() < deadline:
        latest = history_page(api, session, since)
        for frame in latest.get("frames", []):
            if frame.get("type") == "turn/done" and isinstance(frame.get("data"), dict) \
                    and frame["data"].get("turnId") == turn_id:
                return latest
        time.sleep(0.05)
    return latest


def full_history(api: API, session: SessionRuntime) -> dict:
    since = 0
    frames = []
    last_seq = 0
    while True:
        page = history_page(api, session, since, 500)
        if page.get("journalEpoch") != session.epoch:
            raise RuntimeError(f"{session.label} history epoch 改变")
        batch = page.get("frames", [])
        frames.extend(batch)
        last_seq = int(page.get("lastSeq", 0))
        if not batch:
            break
        next_since = int(batch[-1]["seq"])
        if next_since <= since:
            raise RuntimeError("history 游标没有前进")
        since = next_since
        if since >= last_seq:
            break
    return {"journalEpoch": session.epoch, "lastSeq": last_seq, "frames": frames}


def drill_session(controller: LoadController, session: SessionRuntime, evidence: Evidence) -> None:
    label = session.label
    evidence.check(f"drill/{label}/pause-five-load-workers", controller.pause_all(),
                   {"idle": sorted(controller.idle)})
    try:
        baseline = session.last_seq
        session.api.call("session.setPermission", {"id": session.id, "permission": "ask"})
        started = time.perf_counter()
        iteration = controller._next_iteration(label)
        marker = f"P120-{label}-{iteration:06d}"
        prompt = f"PERF120S marker={marker} iteration={iteration}"
        result = session.api.call("session.prompt", {"sessionId": session.id, "text": prompt})
        turn_id = result["turnId"]
        evidence.jsonl("turns.jsonl", {"session": label, "sessionId": session.id, "iteration": iteration,
            "marker": marker, "purpose": "approval-gap-reconnect", "turnId": turn_id,
            "startUtc": utc_now(), "manual": True})
        approval = session.wait_approval(baseline)
        evidence.check(f"drill/{label}/real-approval-request", approval is not None and approval.get("requestId") != "")
        if approval is None:
            return
        request_id = approval["requestId"]
        watermark = session.close_ws("pending-approval-disconnect")
        evidence.check(f"drill/{label}/disconnect-watermark", watermark >= approval["seq"],
                       {"watermark": watermark, "approvalSeq": approval["seq"]})
        try:
            history_page(session.api, session, watermark)
            session.api.call("session.history", {"id": session.id, "journalEpoch": session.epoch + 1,
                                               "sinceSeq": watermark, "limit": 500})
            evidence.check(f"drill/{label}/stale-history-rejected", False, "旧 epoch+1 意外成功")
        except APIError as exc:
            payload = exc.payload if isinstance(exc.payload, dict) else {}
            error = payload.get("error", {})
            evidence.check(f"drill/{label}/stale-history-rejected",
                exc.status == 409 and error.get("code") == "stale_epoch" and error.get("retryable") is True,
                {"status": exc.status, "error": error})
        stale_id = session.connect(watermark, session.epoch + 1, "stale-epoch")
        reset = session.wait_reset(stale_id)
        reset_data = reset.get("data", {}) if reset else {}
        evidence.check(f"drill/{label}/session-reset-real-epoch",
            reset is not None and reset.get("seq") == 0 and reset_data.get("journalEpoch") == session.epoch
            and reset_data.get("lastSeq", -1) >= watermark, reset or {})
        session.close_ws("stale-reset-complete")
        live_id = session.connect(watermark, note="pending-reconnect")
        evidence.check(f"drill/{label}/pending-approval-seq0-replay",
            session.wait_pending_replay(live_id, request_id), {"requestId": request_id, "connectionId": live_id})
        session.api.call("session.approve", {"requestId": request_id, "optionId": "allow_once"})
        # 第二次断线也可能已交付若干晚到帧；必须用这条连接 join 后的新水位，
        # 不能仍从审批前的旧 watermark 重放，否则客户端自己会制造重复帧。
        watermark = session.close_ws("approved-late-frames-offline")
        offline = wait_history_turn(session.api, session, watermark, turn_id)
        evidence.check(f"drill/{label}/offline-turn-done-in-raw-history", offline is not None)
        if offline is None:
            return
        evidence.save(f"drill-{label}-offline-history.json", offline)
        late = [f for f in offline.get("frames", []) if int(f.get("seq", 0)) > watermark]
        late_types = {f.get("type") for f in late}
        raw_reasoning = any(f.get("type") == "assistant/message" and "reasoning" in json.dumps(f.get("data"), ensure_ascii=False)
                            for f in late)
        evidence.check(f"drill/{label}/late-text-reasoning-tool-done",
            {"assistant/message", "tool/result", "turn/end", "turn/done"}.issubset(late_types) and raw_reasoning,
            {"lateFrames": len(late), "types": sorted(late_types), "reasoning": raw_reasoning})
        contiguous = bool(late) and all(int(f["seq"]) == watermark + i + 1 for i, f in enumerate(late))
        evidence.check(f"drill/{label}/history-fills-exact-gap", contiguous,
                       {"from": watermark + 1, "count": len(late), "lastSeq": offline.get("lastSeq")})
        session.connect(watermark, note="late-replay-reconnect")
        recovered = session.wait_turn(turn_id, timeout=15)
        evidence.check(f"drill/{label}/reconnect-replays-late-turn", recovered and session.last_seq >= offline["lastSeq"],
                       {"watermark": watermark, "lastSeq": session.last_seq, "offlineLastSeq": offline["lastSeq"]})
        evidence.jsonl("drills.jsonl", {"session": label, "sessionId": session.id, "epoch": session.epoch,
            "turnId": turn_id, "requestId": request_id, "baseline": baseline, "watermark": watermark,
            "lateSeqs": [f["seq"] for f in late], "lateTypes": sorted(late_types),
            "elapsedMs": (time.perf_counter() - started) * 1000, "recovered": recovered})
    except Exception as exc:
        evidence.check(f"drill/{label}/driver-exception", False, f"{type(exc).__name__}: {exc}")
    finally:
        try:
            session.api.call("session.setPermission", {"id": session.id, "permission": "allow"})
        except Exception as exc:
            evidence.check(f"drill/{label}/restore-allow", False, str(exc))
        controller.resume()


def build_summary(evidence: Evidence, sessions: list[SessionRuntime], timing: dict,
                  revisions: dict, cleanup: dict) -> dict:
    frames = read_jsonl(evidence.run / "frames.jsonl")
    dispatch_rows = read_jsonl(evidence.run / "dispatch.jsonl")
    rtt_rows = read_jsonl(evidence.run / "rtt.jsonl")
    resources = read_jsonl(evidence.run / "resources.jsonl")
    steady = [r for r in resources if r.get("phase") == "steady"]
    startup = next((r for r in resources if r.get("phase") == "startup"), None)
    end = next((r for r in reversed(resources) if r.get("phase") == "end"), None)
    per_session = {}
    for session in sessions:
        sf = [f for f in frames if f.get("session") == session.label and int(f.get("seq", 0)) > 0]
        sd = [float(r["dispatchMs"]) for r in dispatch_rows if r.get("session") == session.label]
        sr = [float(r["rttMs"]) for r in rtt_rows if r.get("session") == session.label]
        per_session[session.label] = {"sessionId": session.id, "journalEpoch": session.epoch,
            "livePositiveFrames": len(sf), "firstSeq": session.first_seq, "lastLiveSeq": session.last_seq,
            "types": dict(session.type_counts), "dispatchMs": distribution(sd), "rttMs": distribution(sr),
            "seqErrors": session.seq_errors, "crossTalk": session.cross_talk,
            "unexpectedClose": session.unexpected_close}
    tree_cpu = [float(r["tree"]["cpuPercentOneCore"]) for r in steady
                if r.get("tree", {}).get("cpuPercentOneCore") is not None]
    gaps = []
    previous = None
    for row in resources:
        if row.get("phase") == "steady":
            if previous is not None:
                gaps.append(float(row["monotonic"]) - float(previous["monotonic"]))
            previous = row
    first_steady = steady[0]["tree"] if steady else {}
    last_steady = steady[-1]["tree"] if steady else {}
    resource_summary = {
        "samples": len(steady), "sampleIntervalSeconds": SAMPLE_INTERVAL_SECONDS,
        "maxSampleGapSeconds": max(gaps) if gaps else None,
        "treeCpuPercentOneCore": distribution(tree_cpu),
        "maxWorkingSetBytes": max((r["tree"]["workingSetBytes"] for r in steady), default=0),
        "maxPrivateBytes": max((r["tree"]["privateBytes"] for r in steady), default=0),
        "maxHandles": max((r["tree"]["handles"] for r in steady), default=0),
        "maxThreads": max((r["tree"]["threads"] for r in steady), default=0),
        "maxGdiObjects": max((r["tree"].get("gdi", 0) for r in steady), default=0),
        "maxUserObjects": max((r["tree"].get("userObjects", 0) for r in steady), default=0),
        "privateGrowthSteadyBytes": last_steady.get("privateBytes", 0) - first_steady.get("privateBytes", 0),
        "handleGrowthSteady": last_steady.get("handles", 0) - first_steady.get("handles", 0),
        "threadGrowthSteady": last_steady.get("threads", 0) - first_steady.get("threads", 0),
        "startupTree": startup.get("tree") if startup else None,
        "endTree": end.get("tree") if end else None,
        "fd": {"supported": False, "value": None, "note": "Windows psutil 无 num_fds；null 不冒充 0"},
        "gdiUserSupported": os.name == "nt",
        "goroutines": {"startup": startup.get("debug", {}).get("goroutines") if startup else None,
                       "end": end.get("debug", {}).get("goroutines") if end else None},
    }
    pids = {}
    for row in resources:
        for proc in row.get("processes", []):
            key = f'{proc["pid"]}@{proc["createTime"]}'
            item = pids.setdefault(key, {"pid": proc["pid"], "firstSeenUtc": row["timeUtc"],
                "lastSeenUtc": row["timeUtc"], "createTime": proc["createTime"], "exe": proc["exe"],
                "ownedImage": proc["ownedImage"], "ownedByProbeTree": proc.get("ownedByProbeTree", False),
                "ppids": set(), "cmdlines": set()})
            item["lastSeenUtc"] = row["timeUtc"]
            item["ppids"].add(proc["ppid"])
            item["cmdlines"].add(json.dumps(proc.get("cmdline", []), ensure_ascii=False))
    pid_rows = []
    for item in pids.values():
        item["ppids"] = sorted(item["ppids"])
        item["cmdlines"] = sorted(item["cmdlines"])
        pid_rows.append(item)
    pid_rows.sort(key=lambda r: (r["firstSeenUtc"], r["pid"]))
    evidence.save("pid-attribution.json", pid_rows)
    summary = {"schema": "dsh.perf-120s@1", "dshdRev": revisions.get("dshdRev"),
        "binarySha256": revisions.get("binarySha256"), "timing": timing,
        "percentileAlgorithm": PERCENTILE_ALGORITHM, "thresholds": THRESHOLDS,
        "sessionCount": len(sessions), "sessions": per_session,
        "aggregate": {"dispatchMs": distribution([float(r["dispatchMs"]) for r in dispatch_rows]),
                      "rttMs": distribution([float(r["rttMs"]) for r in rtt_rows])},
        "resources": resource_summary, "cleanup": cleanup,
        "precheck": {"ok": evidence.ok, "fail": evidence.fail}}
    return summary


def add_metric_checks(summary: dict, evidence: Evidence) -> None:
    timing = summary.get("timing", {})
    wall = float(timing.get("wallSeconds", 0))
    evidence.check("metric/wall-at-least-120-seconds", wall >= THRESHOLDS["min_wall_seconds"], wall)
    evidence.check("metric/five-independent-sessions", summary.get("sessionCount") == SESSION_COUNT
                   and len({s.get("sessionId") for s in summary.get("sessions", {}).values()}) == SESSION_COUNT
                   and len({s.get("journalEpoch") for s in summary.get("sessions", {}).values()}) == SESSION_COUNT)
    resources = summary.get("resources", {})
    evidence.check("metric/resource-samples-present", resources.get("samples", 0) >= THRESHOLDS["min_resource_samples"],
                   resources.get("samples"))
    max_gap = resources.get("maxSampleGapSeconds")
    evidence.check("metric/resource-gap-at-most-1s", max_gap is not None and max_gap <= 1.0, max_gap)
    aggregate = summary.get("aggregate", {})
    rtt = aggregate.get("rttMs", {})
    dispatch = aggregate.get("dispatchMs", {})
    evidence.check("metric/rtt-nonzero-samples", rtt.get("count", 0) > 0, rtt.get("count"))
    evidence.check("metric/dispatch-nonzero-samples", dispatch.get("count", 0) > 0, dispatch.get("count"))
    evidence.check("metric/rtt-p95-threshold", rtt.get("p95") is not None and rtt["p95"] <= THRESHOLDS["max_rtt_p95_ms"], rtt)
    evidence.check("metric/dispatch-p95-threshold", dispatch.get("p95") is not None
                   and dispatch["p95"] <= THRESHOLDS["max_dispatch_p95_ms"], dispatch.get("p95"))
    evidence.check("metric/dispatch-p99-threshold", dispatch.get("p99") is not None
                   and dispatch["p99"] <= THRESHOLDS["max_dispatch_p99_ms"], dispatch.get("p99"))
    cpu = resources.get("treeCpuPercentOneCore", {})
    evidence.check("metric/tree-cpu-p95-threshold", cpu.get("count", 0) > 0 and cpu.get("p95") is not None
                   and cpu["p95"] <= THRESHOLDS["max_tree_cpu_p95_one_core_percent"], cpu.get("p95"))
    evidence.check("metric/tree-working-set-threshold", 0 < resources.get("maxWorkingSetBytes", 0)
                   <= THRESHOLDS["max_tree_working_set_bytes"], resources.get("maxWorkingSetBytes"))
    evidence.check("metric/tree-private-threshold", 0 < resources.get("maxPrivateBytes", 0)
                   <= THRESHOLDS["max_tree_private_bytes"], resources.get("maxPrivateBytes"))
    evidence.check("metric/tree-handles-threshold", 0 < resources.get("maxHandles", 0)
                   <= THRESHOLDS["max_tree_handles"], resources.get("maxHandles"))
    evidence.check("metric/tree-threads-threshold", 0 < resources.get("maxThreads", 0)
                   <= THRESHOLDS["max_tree_threads"], resources.get("maxThreads"))
    evidence.check("metric/private-growth-threshold", resources.get("privateGrowthSteadyBytes", 10**18)
                   <= THRESHOLDS["max_private_growth_bytes"], resources.get("privateGrowthSteadyBytes"))
    evidence.check("metric/handle-growth-threshold", resources.get("handleGrowthSteady", 10**9)
                   <= THRESHOLDS["max_handle_growth"], resources.get("handleGrowthSteady"))
    evidence.check("metric/thread-growth-threshold", resources.get("threadGrowthSteady", 10**9)
                   <= THRESHOLDS["max_thread_growth"], resources.get("threadGrowthSteady"))
    for label, session in summary.get("sessions", {}).items():
        evidence.check(f"metric/{label}/rtt-samples", session.get("rttMs", {}).get("count", 0)
                       >= THRESHOLDS["min_rtt_samples_per_session"], session.get("rttMs", {}).get("count"))
        evidence.check(f"metric/{label}/dispatch-samples", session.get("dispatchMs", {}).get("count", 0) > 0)
        evidence.check(f"metric/{label}/first-seq-one", session.get("firstSeq") == 1, session.get("firstSeq"))
        evidence.check(f"metric/{label}/seq-contiguous", not session.get("seqErrors"), session.get("seqErrors"))
        evidence.check(f"metric/{label}/no-cross-talk", not session.get("crossTalk"), session.get("crossTalk"))
        evidence.check(f"metric/{label}/no-unexpected-close", not session.get("unexpectedClose"), session.get("unexpectedClose"))
        types = session.get("types", {})
        coverage = types.get("assistant/message", 0) > 0 and types.get("tool/call", 0) > 0 \
            and types.get("tool/result", 0) > 0 and types.get("approval/request", 0) > 0 \
            and types.get("turn/done", 0) > 0
        evidence.check(f"metric/{label}/text-reasoning-tool-approval-coverage",
            coverage and any("reasoning" in json.dumps(f.get("data"), ensure_ascii=False)
                for f in read_jsonl(evidence.run / "frames.jsonl") if f.get("session") == label
                and f.get("type") == "assistant/message"), types)
        evidence.check(f"metric/{label}/no-frame-storm", 0 < session.get("livePositiveFrames", 0)
                       <= THRESHOLDS["max_frames_per_session"], session.get("livePositiveFrames"))


def validate_histories(evidence: Evidence, sessions: list[SessionRuntime], histories: dict) -> None:
    live_frames = read_jsonl(evidence.run / "frames.jsonl")
    for session in sessions:
        history = histories.get(session.label, {})
        frames = history.get("frames", [])
        contiguous = bool(frames) and all(int(f.get("seq", 0)) == i + 1 for i, f in enumerate(frames))
        evidence.check(f"history/{session.label}/raw-seq-one-to-last", contiguous
                       and history.get("lastSeq") == len(frames), {"frames": len(frames), "lastSeq": history.get("lastSeq")})
        live_by_seq = {int(f["seq"]): f for f in live_frames if f.get("session") == session.label and int(f.get("seq", 0)) > 0}
        history_by_seq = {int(f["seq"]): f for f in frames}
        missing = sorted(set(history_by_seq) - set(live_by_seq))
        evidence.check(f"history/{session.label}/live-plus-replay-covers-journal", not missing,
                       {"missing": missing[:20], "history": len(history_by_seq), "liveUnique": len(live_by_seq)})
        raw_shape = all(set(("seq", "type", "data", "time")).issubset(f) and f.get("time", 0) > 0 for f in frames)
        evidence.check(f"history/{session.label}/raw-frames-not-messages", raw_shape and "messages" not in history)


def evaluate_file(path: Path) -> int:
    summary = json.loads(path.read_text(encoding="utf-8"))
    out = path.parent / ("evaluate-" + path.stem)
    out.mkdir(exist_ok=False)
    evidence = Evidence(out)
    add_metric_checks(summary, evidence)
    total = evidence.ok + evidence.fail
    print(f"PERF_EVAL_SUMMARY OK={evidence.ok} FAIL={evidence.fail} TOTAL={total}", flush=True)
    evidence.save("evaluation.json", {"ok": evidence.ok, "fail": evidence.fail, "source": str(path)})
    evidence.close()
    return 0 if evidence.fail == 0 and evidence.ok > 0 else 1


def raw_ws_parser_self_test() -> tuple[bool, str]:
    """不经宿主，直接给读线程喂 extended-length 与 RSV1 两种真实线帧。"""
    if not hasattr(socket, "socketpair"):
        return False, "当前 Python 无 socketpair"
    received: queue.Queue[dict] = queue.Queue()
    ws = RawWS("", "", lambda _ws, frame, *_args: received.put(frame), lambda *_args: None)
    client, server = socket.socketpair()
    ws.sock = client
    ws.start()

    def emit(opcode: int, payload: bytes, compressed: bool = False) -> None:
        first = 0x80 | opcode | (0x40 if compressed else 0)
        if len(payload) < 126:
            header = bytes([first, len(payload)])
        elif len(payload) <= 0xFFFF:
            header = bytes([first, 126]) + struct.pack("!H", len(payload))
        else:
            header = bytes([first, 127]) + struct.pack("!Q", len(payload))
        server.sendall(header + payload)

    try:
        plain = json_bytes({"seq": 1, "type": "assistant/message", "data": {"text": secrets.token_hex(160)}, "time": 1})
        emit(0x1, plain)
        source = json_bytes({"seq": 2, "type": "assistant/message", "data": {"text": "压缩🙂" * 200}, "time": 2})
        compressor = zlib.compressobj(-1, zlib.DEFLATED, -15)
        packed = compressor.compress(source) + compressor.flush(zlib.Z_SYNC_FLUSH)
        if packed.endswith(b"\x00\x00\xff\xff"):
            packed = packed[:-4]
        emit(0x1, packed, True)
        first = received.get(timeout=3)
        second = received.get(timeout=3)
        valid = first.get("seq") == 1 and second.get("seq") == 2 \
            and second.get("data", {}).get("text") == "压缩🙂" * 200
        return valid, {"plainBytes": len(plain), "compressedWireBytes": len(packed), "jsonBytes": len(source)}
    except Exception as exc:
        return False, f"{type(exc).__name__}: {exc}"
    finally:
        ws.close(True)
        ws.join()
        server.close()


def self_test(run: Path) -> int:
    run.mkdir(parents=True, exist_ok=False)
    ok = fail = 0

    def check(name: str, condition: bool, detail: object = "") -> None:
        nonlocal ok, fail
        if condition:
            ok += 1
            print(f"SELFTEST_OK {name} {detail}", flush=True)
        else:
            fail += 1
            print(f"SELFTEST_FAIL {name} {detail}", flush=True)

    values = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]
    check("nearest-rank-p50", nearest_rank(values, 50) == 5)
    check("nearest-rank-p95", nearest_rank(values, 95) == 10)
    check("nearest-rank-single", nearest_rank([7], 99) == 7)
    ws_ok, ws_detail = raw_ws_parser_self_test()
    check("ws-byte-header-extended-length-and-deflate", ws_ok, ws_detail)
    race_ev = Evidence(run / "jsonl-init-race")
    race_start = threading.Event()

    def race_writer(worker: int) -> None:
        race_start.wait()
        for i in range(100):
            race_ev.jsonl("race.jsonl", {"worker": worker, "iteration": i})

    race_threads = [threading.Thread(target=race_writer, args=(worker,)) for worker in range(8)]
    for thread in race_threads:
        thread.start()
    race_start.set()
    for thread in race_threads:
        thread.join()
    race_ev.close()
    race_rows = read_jsonl(run / "jsonl-init-race" / "race.jsonl")
    race_identity = {(row["worker"], row["iteration"]) for row in race_rows}
    check("jsonl-concurrent-init-no-lost-rows", len(race_rows) == 800 and len(race_identity) == 800,
          {"rows": len(race_rows), "unique": len(race_identity)})
    try:
        nearest_rank([], 95)
        check("zero-sample-throws", False)
    except ValueError:
        check("zero-sample-throws", True)
    base = {"timing": {"wallSeconds": 120.0}, "sessionCount": 5,
        "sessions": {label: {"sessionId": "id-" + label, "journalEpoch": i + 1,
            "rttMs": {"count": 100}, "dispatchMs": {"count": 100}, "firstSeq": 1,
            "seqErrors": [], "crossTalk": [], "unexpectedClose": [],
            "types": {"assistant/message": 10, "tool/call": 5, "tool/result": 5, "approval/request": 1, "turn/done": 5},
            "livePositiveFrames": 100} for i, label in enumerate("ABCDE")},
        "aggregate": {"rttMs": {"count": 500, "p95": 5}, "dispatchMs": {"count": 500, "p95": 100, "p99": 200}},
        "resources": {"samples": 480, "maxSampleGapSeconds": 0.25,
            "treeCpuPercentOneCore": {"count": 480, "p95": 20}, "maxWorkingSetBytes": 1024,
            "maxPrivateBytes": 1024, "maxHandles": 100, "maxThreads": 20,
            "privateGrowthSteadyBytes": 0, "handleGrowthSteady": 0, "threadGrowthSteady": 0}}
    # add_metric_checks 还检查 reasoning 原始帧；自测给每个会话一条真实形状的合成证据。
    evidence_dir = run / "valid-input"
    evidence_dir.mkdir()
    with (evidence_dir / "frames.jsonl").open("w", encoding="utf-8") as f:
        for label in "ABCDE":
            f.write(json.dumps({"session": label, "type": "assistant/message",
                "data": {"message": {"content": [{"type": "reasoning", "text": "r"}]}}}) + "\n")
    # evaluate_file 需要源文件与独立输出目录；把 frames 证据复制到每次评估输出旁的固定位置不方便，
    # 因而红测直接调用同一个判定函数，并逐条确认真正增加 FAIL。
    cases = {
        "red-zero-rtt": lambda s: s["aggregate"]["rttMs"].update({"count": 0, "p95": None}),
        "red-short-wall": lambda s: s["timing"].update({"wallSeconds": 119.999}),
        "red-p95": lambda s: s["aggregate"]["dispatchMs"].update({"p95": THRESHOLDS["max_dispatch_p95_ms"] + 1}),
        "red-cross-talk": lambda s: s["sessions"]["A"]["crossTalk"].append({"foreign": "B"}),
    }
    for name, mutate in cases.items():
        case_dir = run / name
        case_dir.mkdir()
        (case_dir / "frames.jsonl").write_bytes((evidence_dir / "frames.jsonl").read_bytes())
        summary = json.loads(json.dumps(base))
        mutate(summary)
        ev = Evidence.__new__(Evidence)
        ev.run = case_dir
        ev.files, ev.locks, ev.checks, ev.ok, ev.fail = {}, {}, [], 0, 0
        add_metric_checks(summary, ev)
        expected = {"red-zero-rtt": "metric/rtt-nonzero-samples", "red-short-wall": "metric/wall-at-least-120-seconds",
                    "red-p95": "metric/dispatch-p95-threshold", "red-cross-talk": "metric/A/no-cross-talk"}[name]
        red_rows = [c for c in ev.checks if not c["pass"]]
        print(f"SELFTEST_RAW {name} OK={ev.ok} FAIL={ev.fail} expected_red={expected}", flush=True)
        check(name + "-really-red", ev.fail > 0 and any(c["name"] == expected for c in red_rows),
              {"ok": ev.ok, "fail": ev.fail})
    print(f"SELFTEST_SUMMARY OK={ok} FAIL={fail}", flush=True)
    (run / "summary.txt").write_text(f"SELFTEST_SUMMARY OK={ok} FAIL={fail}\n", encoding="utf-8")
    return 0 if fail == 0 and ok > 0 else 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", default=str(ROOT / "tmp" / "perf-120s-final"))
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--evaluate", help="只按已有 summary.json 重跑阈值判定")
    args = parser.parse_args()
    run = Path(args.evidence_dir).resolve()
    if args.evaluate:
        return evaluate_file(Path(args.evaluate).resolve())
    if args.self_test:
        return self_test(run)
    if run.exists() and any(run.iterdir()):
        print(f"PERF_FAIL evidence-dir 非空，拒绝覆盖：{run}", flush=True)
        return 1
    evidence = Evidence(run)
    home = run / "home"
    binary = run / "bin" / "dshd-perf.test.exe"
    revisions: dict = {}
    timing: dict = {}
    cleanup: dict = {}
    sessions: list[SessionRuntime] = []
    histories: dict = {}
    proc = None
    controller = None
    sampler = None
    host_log = None
    owned_pid_create = {}
    owned_pid_images = {}
    exit_code = 1
    try:
        binary.parent.mkdir(parents=True)
        build_cmd = [go_executable(), "test", "-c", "-o", str(binary), "./cmd/dshd"]
        build = subprocess.run(build_cmd, cwd=ROOT, text=True, encoding="utf-8", errors="replace", capture_output=True)
        (run / "build.log").write_text(build.stdout + build.stderr + f"\nexit={build.returncode}\n", encoding="utf-8")
        print(build.stdout + build.stderr, end="", flush=True)
        evidence.check("build-test-instrumented-real-dshd", build.returncode == 0 and binary.is_file(),
                       {"exit": build.returncode})
        if build.returncode != 0:
            raise RuntimeError("go test -c 失败")
        bin_check = subprocess.run([sys.executable, str(ROOT / "scripts" / "bin-check.py"), str(binary)],
            cwd=ROOT, text=True, encoding="utf-8", errors="replace", capture_output=True)
        (run / "bin-check.log").write_text(bin_check.stdout + bin_check.stderr + f"\nexit={bin_check.returncode}\n", encoding="utf-8")
        print(bin_check.stdout + bin_check.stderr, end="", flush=True)
        evidence.check("bin-check-current", bin_check.returncode == 0, bin_check.returncode)
        if bin_check.returncode != 0:
            raise RuntimeError("bin-check 非零")
        revisions = {"dshdRev": git_output("rev-parse", "HEAD").strip(),
            "dshdStatus": git_output("status", "--porcelain"),
            "dshdDiffStat": git_output("diff", "--stat"), "binary": str(binary),
            "binarySha256": sha256_file(binary), "sourceManifestSha256": ""}
        manifest = source_manifest()
        revisions["sourceManifestSha256"] = hashlib.sha256(json_bytes(manifest)).hexdigest()
        evidence.save("revisions.json", revisions)
        evidence.save("source-manifest.json", manifest)
        env = {k: ("<redacted>" if SECRET_RE.search(k) else v) for k, v in sorted(os.environ.items())}
        evidence.save("environment.json", {"utc": utc_now(), "platform": platform.platform(),
            "python": sys.version, "psutil": psutil.__version__, "logicalCpus": psutil.cpu_count(),
            "env": env, "guiStarted": False, "modelCalled": False, "mockd": False})
        home.mkdir()
        serve_cmd = [str(binary), "serve", "--home", str(home), "--addr", "127.0.0.1:0",
            "--policy", "allow", "--reasoning", "max", "--max-steps", "4",
            "--model", "synthetic/perf-120s", "--parent-pid", str(os.getpid())]
        config = {"serveArgv": serve_cmd, "durationSeconds": DURATION_SECONDS,
            "sessionCount": SESSION_COUNT, "sampleIntervalSeconds": SAMPLE_INTERVAL_SECONDS,
            "thresholds": THRESHOLDS, "percentileAlgorithm": PERCENTILE_ALGORITHM,
            "wsCompression": WS_DEFLATE_OFFER, "syntheticChildEnv": "DSHD_PERF_120S=1",
            "approvalDrillsUtcScheduleSeconds": [15, 35, 55, 75, 95],
            "load": "5 会话并发；每轮 9 个 v4 事件 + turn/done；90ms 事件间隔；text/reasoning/tool；每会话一次串行 approval/gap/reconnect",
            "historicalBaseline": "无；阈值是本次预先登记的工程告警线，不声称提升百分比"}
        evidence.save("commands-config.json", config)
        child_env = os.environ.copy()
        child_env["DSHD_PERF_120S"] = "1"
        child_env["PYTHONDONTWRITEBYTECODE"] = "1"
        host_log = (run / "dshd-stderr.log").open("w", encoding="utf-8", errors="replace")
        proc = subprocess.Popen(serve_cmd, cwd=ROOT, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=host_log, text=True, encoding="utf-8", errors="replace", env=child_env,
            creationflags=0x08000000 if os.name == "nt" else 0)
        owned_pid_create[proc.pid] = psutil.Process(proc.pid).create_time()
        owned_pid_images[proc.pid] = str(binary)
        lines: queue.Queue[str] = queue.Queue()
        threading.Thread(target=lambda: lines.put(proc.stdout.readline()), daemon=True).start()
        ready_line = lines.get(timeout=30).strip()
        match = re.fullmatch(r"ready (\{.*\})", ready_line)
        if not match:
            raise RuntimeError(f"非法 ready 行：{ready_line!r}")
        ready = json.loads(match.group(1))
        if not ready.get("url", "").startswith("http://127.0.0.1:") or not ready.get("token"):
            raise RuntimeError("ready 缺回环 url/token")
        evidence.save("ready-redacted.json", {"url": ready["url"], "token": "<redacted>",
            "pid": proc.pid, "image": native_image(proc.pid), "home": str(home)})
        print(f"DSHD pid={proc.pid} url={ready['url']} token=<redacted>", flush=True)
        api = API(ready["url"], evidence)
        api.connect(ready["token"])
        evidence.check("ready-token-cookie-connect", bool(api.cookie))
        for label in "ABCDE":
            created = api.call("session.create", {"mode": "default"})
            session_id = created.get("sessionId", "")
            loaded = api.call("session.load", {"id": session_id, "limit": 20})
            epoch = int(loaded.get("journalEpoch", 0))
            evidence.check(f"setup/{label}/create-load-epoch", bool(session_id) and epoch != 0,
                           {"sessionId": session_id, "epoch": epoch})
            session = SessionRuntime(label, session_id, epoch, api, evidence)
            sessions.append(session)
        for session in sessions:
            session.connect(0, note="initial")
        evidence.check("setup/five-independent-ws", len(sessions) == 5 and len({s.id for s in sessions}) == 5
                       and len({s.epoch for s in sessions}) == 5)
        sampler = ResourceSampler(proc.pid, binary, api, evidence)
        sampler.start()
        controller = LoadController(sessions, evidence)
        wall_start_mono = time.monotonic()
        wall_start_utc = utc_now()
        timing = {"wallStartUtc": wall_start_utc, "requestedSeconds": DURATION_SECONDS,
                  "wallStartMonotonic": wall_start_mono}
        print(f"PERF_WALL_START utc={wall_start_utc} monotonic={wall_start_mono:.6f} requested=120.000000", flush=True)
        controller.start()
        # 读线程/解压/ping 若坏了必须在数秒内真红，不能拿空指标再跑 120 秒。
        first_ready = False
        ready_deadline = time.monotonic() + 15
        while time.monotonic() < ready_deadline:
            rtt_counts = Counter(row.get("session") for row in read_jsonl(run / "rtt.jsonl"))
            first_ready = all(s.last_seq > 0 and rtt_counts[s.label] > 0 and not s.unexpected_close
                              for s in sessions)
            if first_ready or any(s.unexpected_close for s in sessions):
                break
            time.sleep(0.05)
        evidence.check("setup/first-frame-and-pong-every-session", first_ready,
            {"lastSeq": {s.label: s.last_seq for s in sessions},
             "rtt": dict(Counter(row.get("session") for row in read_jsonl(run / "rtt.jsonl"))),
             "errors": {s.label: s.unexpected_close for s in sessions}})
        if not first_ready:
            raise RuntimeError("首帧/ping 快速失败门未通过")
        schedule = list(zip([15, 35, 55, 75, 95], sessions))
        next_progress = 10
        while True:
            elapsed = time.monotonic() - wall_start_mono
            if elapsed >= DURATION_SECONDS:
                break
            if schedule and elapsed >= schedule[0][0]:
                _, target = schedule.pop(0)
                drill_session(controller, target, evidence)
                continue
            if elapsed >= next_progress:
                print(f"PERF_PROGRESS elapsed={elapsed:.3f} frames={sum(s.last_seq for s in sessions)}", flush=True)
                next_progress += 10
            time.sleep(0.02)
        wall_end_mono = time.monotonic()
        wall_end_utc = utc_now()
        wall_seconds = wall_end_mono - wall_start_mono
        timing.update({"wallEndUtc": wall_end_utc, "wallEndMonotonic": wall_end_mono,
                       "wallSeconds": wall_seconds})
        print(f"PERF_WALL_END utc={wall_end_utc} monotonic={wall_end_mono:.6f}", flush=True)
        print(f"PERF_WALL seconds={wall_seconds:.6f} start={wall_start_utc} end={wall_end_utc}", flush=True)
        controller.finish()
        evidence.check("load/no-failed-normal-turns", not controller.failed_turns, controller.failed_turns[:3])
        evidence.check("load/five-approval-gap-drills", len(read_jsonl(run / "drills.jsonl")) == 5,
                       len(read_jsonl(run / "drills.jsonl")))
        for session in sessions:
            histories[session.label] = full_history(api, session)
            evidence.save(f"history-{session.label}.json", histories[session.label])
        validate_histories(evidence, sessions, histories)
        for session in sessions:
            session.stop()
        evidence.check("connections/no-unexpected-ws-close", all(not s.unexpected_close for s in sessions),
                       {s.label: s.unexpected_close for s in sessions})
    except Exception as exc:
        evidence.check("driver-exception", False, f"{type(exc).__name__}: {exc}")
    finally:
        try:
            if controller is not None:
                controller.finish()
        except Exception as exc:
            evidence.check("cleanup-controller", False, str(exc))
        for session in sessions:
            try:
                session.stop()
            except Exception as exc:
                evidence.check(f"cleanup-ws-{session.label}", False, str(exc))
        if sampler is not None and proc is not None:
            try:
                sampler.finish()
            except Exception as exc:
                evidence.check("cleanup-sampler", False, str(exc))
        if proc is not None:
            cleanup = {"rootPid": proc.pid, "expectedImage": str(binary), "ownedPidCreate": owned_pid_create}
            try:
                image = native_image(proc.pid) if proc.poll() is None else ""
                owned = bool(image) and os.path.normcase(image) == os.path.normcase(str(binary))
                cleanup["rootImage"] = image
                cleanup["rootOwnerVerified"] = owned
                descendants = []
                if proc.poll() is None:
                    try:
                        descendants = psutil.Process(proc.pid).children(recursive=True)
                    except psutil.NoSuchProcess:
                        descendants = []
                descendant_rows = []
                # descendants 来自已核对根 PID 的 children(recursive=True)，因此 conhost.exe
                # 也属于这棵探针树；不要误要求每个子进程都与 dshd 同映像。
                all_owned = owned
                for child in descendants:
                    try:
                        child_image = child.exe()
                        same_image = os.path.normcase(child_image) == os.path.normcase(str(binary))
                        descendant_rows.append({"pid": child.pid, "ppid": child.ppid(), "image": child_image,
                            "createTime": child.create_time(), "ownedByRootTree": True,
                            "sameImageAsRoot": same_image})
                        owned_pid_create.setdefault(child.pid, child.create_time())
                        owned_pid_images.setdefault(child.pid, child_image)
                    except (psutil.NoSuchProcess, psutil.AccessDenied):
                        continue
                cleanup["descendants"] = descendant_rows
                cleanup["treeOwnerVerified"] = all_owned
                if proc.poll() is None and all_owned:
                    if os.name == "nt":
                        kill = subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"],
                            capture_output=True, text=True, encoding="mbcs", errors="replace")
                        cleanup["taskkillExit"] = kill.returncode
                        cleanup["taskkillOutput"] = (kill.stdout + kill.stderr).strip()
                    else:
                        proc.kill()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    cleanup["waitTimeout"] = True
                cleanup["rootExit"] = proc.poll()
                survivors = []
                for pid, create in owned_pid_create.items():
                    try:
                        p = psutil.Process(pid)
                        expected = owned_pid_images.get(pid, str(binary))
                        if p.create_time() == create and os.path.normcase(p.exe()) == os.path.normcase(expected):
                            survivors.append(pid)
                    except (psutil.NoSuchProcess, psutil.AccessDenied):
                        pass
                cleanup["survivingOwnedPids"] = survivors
                evidence.check("cleanup-owned-tree-only-and-gone", all_owned and proc.poll() is not None and not survivors,
                               {"owner": all_owned, "exit": proc.poll(), "survivors": survivors})
            except Exception as exc:
                evidence.check("cleanup-host-exception", False, f"{type(exc).__name__}: {exc}")
                cleanup["exception"] = str(exc)
        if host_log is not None:
            host_log.close()
        evidence.save("cleanup.json", cleanup)
        evidence.save("timing.json", timing)
        if not timing.get("wallSeconds"):
            timing["wallSeconds"] = 0
        summary = build_summary(evidence, sessions, timing, revisions, cleanup)
        add_metric_checks(summary, evidence)
        evidence.save("summary.json", summary)
        summary["checks"] = {"ok": evidence.ok, "fail": evidence.fail}
        evidence.save("summary.json", summary)
        summary_line = (f"PERF_120S_SUMMARY OK={evidence.ok} FAIL={evidence.fail} SESSIONS={len(sessions)} "
            f"WALL_SECONDS={timing.get('wallSeconds', 0):.6f} "
            f"RTT_N={summary['aggregate']['rttMs']['count']} DISPATCH_N={summary['aggregate']['dispatchMs']['count']} "
            f"RESOURCE_N={summary['resources']['samples']}")
        print(f"PERF_AGGREGATE {json.dumps(summary['aggregate'], ensure_ascii=False)}", flush=True)
        print(f"PERF_RESOURCES {json.dumps(summary['resources'], ensure_ascii=False)}", flush=True)
        print(summary_line, flush=True)
        (run / "summary.txt").write_text(summary_line + "\n", encoding="utf-8")
        evidence.close()
        exit_code = 0 if evidence.fail == 0 and evidence.ok > 0 else 1
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
