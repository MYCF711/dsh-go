#!/usr/bin/env python3
"""W83 独立真实 dshd 附件/引用/持久化审查探针。

只使用 Python 标准库与真实 `dshd serve`；模型端点是本脚本拥有的回环
OpenAI-compatible stub，用来观察真实 runner 发出的字节，不是 mockd，也不绕过
HTTP cookie、attachment store、run 子进程、v4 或 Transport journal。

用法（先在仓库根构建到 C: 证据目录）：
  go build -o tmp/attachments-review-bin/dshd.exe ./cmd/dshd
  python scripts/attachments-review.py --binary tmp/attachments-review-bin/dshd.exe \
    --out tmp/attachments-review-baseline

退出码：全部断言通过为 0；任何 FAIL、驱动异常、0 断言均为 1。
最终行固定 `ATTACHMENTS_REVIEW_SUMMARY OK=n FAIL=m SKIP=0`。
所有 home、fixture、日志均在指定 tmp/attachments-review-* 证据目录；token/cookie
与假 credential 不写 stdout。进程只经 scripts/_proc.py 回收本探针启动的树。
"""
from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import http.client
import http.cookies
import json
import os
from pathlib import Path
import re
import struct
import sys
import threading
import time
import urllib.parse
import zlib
from dataclasses import dataclass
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import _proc  # noqa: E402

SUMMARY = "ATTACHMENTS_REVIEW_SUMMARY OK={ok} FAIL={fail} SKIP=0"
ID_RE = re.compile(r"^att-[0-9a-f]{32}$")
REF_RE = re.compile(r"^ref-[0-9a-f]{32}$")
FAKE_CREDENTIAL = "W83_FAKE_CREDENTIAL_7f3c9a_NEVER_PERSIST"
MAX_ATTACHMENT = 8 << 20
JPEG_4X3 = "/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAAMABAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AOKgt4vKHyCpfs8X9wUkH+qFS1rKTvuVSk+Ran//2Q=="
GIF_5X4 = "R0lGODlhBQAEAIcAAAAAAAAARAAAiAAAzABEAABERABEiABEzACIAACIRACIiACIzADMAADMRADMiADMzADd3REREQAAVQAAmQAA3QBVAABVVQBMmQBJ3QCZAACZTACZmQCT3QDdAADdSQDdkwDungDu7iIiIgAAZgAAqgAA7gBmAABmZgBVqgBP7gCqAACqVQCqqgCe7gDuAADuTwD/VQD/qgD//zMzMwAAdwAAuwAA/wB3AAB3dwBduwBV/wC7AAC7XQC7uwCq/wD/AEQAREQAiEQAzEREAEREREREiEREzESIAESIRESIiESIzETMAETMRETMiETMzEQAAFUAAFUAVUwAmUkA3VVVAFVVVUxMmUlJ3UyZAEyZTEyZmUmT3UndAEndSUndk0nd3U/u7mYAAGYAZlUAqk8A7mZmAGZmZlVVqk9P7lWqAFWqVVWqqk+e7k/uAE/uT0/unlX/qlX//3cAAHcAd10Au1UA/3d3AHd3d11du1VV/127AF27XV27u1Wq/1X/AFX/VYgAiIgAzIhEAIhERIhEiIhEzIiIAIiIRIiIiIiIzIjMAIjMRIjMiIjMzIgAAIgARJkATJkAmZMA3ZlMAJlMTJlMmZNJ3ZmZAJmZTJmZmZOT3ZPdAJPdSZPdk5Pd3ZkAAKoAAKoAVaoAqp4A7qpVAKpVVapVqp5P7qqqAKqqVaqqqp6e7p7uAJ7uT57unp7u7qr//7sAALsAXbsAu6oA/7tdALtdXbtdu6pV/7u7ALu7Xbu7u6qq/6r/AKr/Var/qswAzMxEAMxERMxEiMxEzMyIAMyIRMyIiMyIzMzMAMzMRMzMiMzMzMwAAMwARMwAiN0Ak90A3d1JAN1JSd1Jk91J3d2TAN2TSd2Tk92T3d3dAN3dSd3dk93d3d0AAN0ASe4AT+4Anu4A7u5PAO5PT+5Pnu5P7u6eAO6eT+6enu6e7u7uAO7uT+7unu7u7u4AAP8AAP8AVf8Aqv8A//9VAP9VVf9Vqv9V//+qAP+qVf+qqv+q////AP//Vf//qv///ywAAAAABQAEAAAIFwBnFAFSRUwBIkWIDLKQsIoZC1UimgkIADs="
WEBP_1X1 = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def file_sha(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def png_chunk(kind: bytes, data: bytes) -> bytes:
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", binascii.crc32(kind + data) & 0xFFFFFFFF)


def make_png(width: int, height: int) -> bytes:
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    rows = []
    for y in range(height):
        row = bytearray([0])
        for x in range(width):
            row.extend(((30 + x * 40) & 255, (50 + y * 50) & 255, 120))
        rows.append(bytes(row))
    return b"\x89PNG\r\n\x1a\n" + png_chunk(b"IHDR", ihdr) + png_chunk(b"IDAT", zlib.compress(b"".join(rows))) + png_chunk(b"IEND", b"")


def safe_json(raw: bytes):
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None


@dataclass
class Response:
    status: int
    raw: bytes
    body: object

    @property
    def result(self):
        return self.body.get("result") if isinstance(self.body, dict) else None


class API:
    def __init__(self, base: str, runner: "Review", label: str):
        parsed = urllib.parse.urlsplit(base)
        self.host = parsed.hostname or "127.0.0.1"
        self.port = parsed.port or 80
        self.runner = runner
        self.label = label
        self.cookie = ""

    def request(self, method: str, path: str, payload=None, timeout=20) -> Response:
        conn = http.client.HTTPConnection(self.host, self.port, timeout=timeout)
        headers = {}
        data = None
        if payload is not None:
            data = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.cookie:
            headers["Cookie"] = self.cookie
        try:
            conn.request(method, path, body=data, headers=headers)
            res = conn.getresponse()
            raw = res.read()
            if res.status == 303:
                cookie = http.cookies.SimpleCookie()
                cookie.load(res.getheader("Set-Cookie") or "")
                if cookie:
                    self.cookie = "; ".join(f"{k}={v.value}" for k, v in cookie.items())
            response = Response(res.status, raw, safe_json(raw))
            self.runner.responses.append((self.label, method, path, response))
            return response
        except (OSError, http.client.HTTPException) as exc:
            raw = f"{type(exc).__name__}: {exc}".encode("utf-8", "replace")
            response = Response(0, raw, None)
            self.runner.responses.append((self.label, method, path, response))
            return response
        finally:
            conn.close()

    def connect(self, token: str) -> Response:
        return self.request("GET", "/connect?token=" + urllib.parse.quote(token))

    def post(self, method: str, payload: dict, timeout=20) -> Response:
        return self.request("POST", "/api/" + method, payload, timeout)


class Stub:
    """真实 runner 的回环 SSE 接收端；只在内存记录审查请求。"""

    def __init__(self):
        self.requests = []
        self.lock = threading.Lock()
        self.busy_gate = threading.Event()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                return

            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(min(length, 32 << 20))
                body = safe_json(raw)
                auth_ok = self.headers.get("Authorization") == "Bearer " + FAKE_CREDENTIAL
                with outer.lock:
                    index = len(outer.requests)
                    outer.requests.append({"path": self.path, "body": body, "raw": raw, "authOK": auth_ok})
                if not auth_ok or self.path != "/v1/chat/completions" or not isinstance(body, dict):
                    self.send_response(401 if not auth_ok else 404)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(b'{"error":"W83 stub rejected request"}')
                    return
                if b"W83_BUSY_GATE" in raw:
                    outer.busy_gate.wait(20)
                if b"W83_LEGACY" in raw:
                    marker = "W83_LEGACY_ASSISTANT"
                elif b"W83_BEFORE" in raw:
                    marker = "W83_BLOCKS_ASSISTANT"
                elif b"W83_BUSY_GATE" in raw:
                    marker = "W83_BUSY_ASSISTANT"
                else:
                    marker = f"W83_GENERIC_ASSISTANT_{index}"
                payload = {
                    "choices": [{"delta": {"content": marker}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
                }
                wire = b"data: " + json.dumps(payload, separators=(",", ":")).encode() + b"\n\ndata: [DONE]\n\n"
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(wire)))
                self.end_headers()
                self.wfile.write(wire)
                self.wfile.flush()

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_port}/v1"

    def matching(self, marker: str):
        with self.lock:
            return [dict(r) for r in self.requests if marker.encode() in r["raw"]]

    def close(self):
        self.busy_gate.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)


class Review:
    def __init__(self, out: Path, binary: Path):
        self.out = out
        self.binary = binary
        self.ok = 0
        self.fail = 0
        self.checks = []
        self.responses = []
        self.hosts = []

    def check(self, name: str, condition: bool, detail=""):
        condition = bool(condition)
        if condition:
            self.ok += 1
            prefix = "AR_OK"
        else:
            self.fail += 1
            prefix = "AR_FAIL"
        detail = str(detail).replace(FAKE_CREDENTIAL, "<redacted>")
        if len(detail) > 700:
            detail = detail[:700] + "…"
        self.checks.append({"name": name, "ok": condition, "detail": detail})
        print(f"{prefix} {name} {detail}".rstrip(), flush=True)

    def save(self, name: str, value):
        path = self.out / name
        path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(value, bytes):
            value = value.replace(FAKE_CREDENTIAL.encode(), b"<redacted>")
            path.write_bytes(value)
        else:
            text = json.dumps(value, ensure_ascii=False, indent=2, default=str).replace(FAKE_CREDENTIAL, "<redacted>")
            path.write_text(text + "\n", encoding="utf-8")

    def start_host(self, label: str, home: Path, env: dict) -> tuple[API, str]:
        log_path = self.out / f"host-{label}.stderr.log"
        log = log_path.open("ab")
        try:
            proc = _proc.run_serve(
                bin=str(self.binary), home=str(home), policy="allow", addr="127.0.0.1:0",
                cwd=str(ROOT), env=env, stderr=log, ready_timeout=35, name=f"W83 {label}")
        except Exception:
            log.close()
            raise
        host = {"label": label, "proc": proc, "log": log, "home": home}
        self.hosts.append(host)
        api = API(proc.url, self, label)
        print(f"AR_HOST label={label} pid={proc.pid} url={proc.url} token=<redacted>", flush=True)
        return api, proc.token

    def stop_host(self, label: str):
        for host in reversed(self.hosts):
            if host["label"] == label:
                host["proc"].kill_tree(reason=f"W83 {label} 阶段完成")
                host["log"].close()
                host["stopped"] = True

    def close(self):
        for host in reversed(self.hosts):
            if not host.get("stopped"):
                host["proc"].kill_tree(reason="W83 finally")
                host["log"].close()
                host["stopped"] = True


def expect_error(review: Review, name: str, response: Response, status: int, code: str):
    err = response.body.get("error") if isinstance(response.body, dict) else None
    shape = isinstance(err, dict) and set(err) == {"code", "message", "hint", "retryable"}
    values = shape and isinstance(err["message"], str) and bool(err["message"]) and isinstance(err["hint"], str) and bool(err["hint"]) and isinstance(err["retryable"], bool)
    good = response.status == status and shape and values and err.get("code") == code
    review.check(name, good, f"status={response.status} want={status} error={err!r} raw={response.raw[:240]!r}")
    return good


def result_ok(review: Review, name: str, response: Response):
    good = response.status == 200 and isinstance(response.body, dict) and "result" in response.body
    review.check(name, good, f"status={response.status} raw={response.raw[:300]!r}")
    return response.result if good else None


def create_session(review: Review, api: API, label: str) -> str:
    result = result_ok(review, f"{label}/create", api.post("session.create", {"mode": "default"}))
    sid = result.get("sessionId", "") if isinstance(result, dict) else ""
    review.check(f"{label}/real-session-id", isinstance(sid, str) and len(sid) >= 8 and not any(c.isspace() for c in sid), f"id={sid!r}")
    return sid if isinstance(sid, str) else ""


def load_session(api: API, sid: str):
    return api.post("session.load", {"sessionId": sid, "limit": 500})


def history_session(api: API, sid: str, epoch: int):
    return api.post("session.history", {"sessionId": sid, "sinceSeq": 0, "limit": 1000, "journalEpoch": epoch})


def wait_turn(review: Review, api: API, sid: str, turn_id: str, label: str, seconds=45):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        lr = load_session(api, sid)
        last = lr.result
        epoch = last.get("journalEpoch", 0) if isinstance(last, dict) else 0
        if epoch:
            hr = history_session(api, sid, epoch)
            frames = hr.result.get("frames", []) if isinstance(hr.result, dict) else []
            for frame in frames:
                if frame.get("type") not in ("turn/done", "turn/error"):
                    continue
                data = frame.get("data") or {}
                if data.get("turnId") == turn_id:
                    good = frame.get("type") == "turn/done"
                    review.check(f"{label}/turn-done", good, f"type={frame.get('type')} data={data!r}")
                    return last, hr.result, good
        time.sleep(0.12)
    review.check(f"{label}/turn-done", False, f"timeout={seconds}s turnId={turn_id} last={last!r}"[:700])
    return last, None, False


def prompt_wait(review: Review, api: API, sid: str, payload: dict, label: str):
    response = api.post("session.prompt", payload, timeout=25)
    result = result_ok(review, f"{label}/accepted", response)
    turn_id = result.get("turnId", "") if isinstance(result, dict) else ""
    if not result or not result.get("accepted") or not turn_id:
        return None, None, False
    return wait_turn(review, api, sid, turn_id, label)


def reject_prompt(review: Review, api: API, sid: str, blocks, label: str, status: int, code: str):
    """负例若被旧宿主误收，也等它真收工，避免下一轮 busy 掩盖原始失败。"""
    response = api.post("session.prompt", {"sessionId": sid, "content": blocks}, timeout=30)
    expect_error(review, label, response, status, code)
    result = response.result
    if isinstance(result, dict) and result.get("accepted") and result.get("turnId"):
        wait_turn(review, api, sid, result["turnId"], label + "/unexpected-accepted")
    return response


def user_messages(load):
    return [m for m in (load or {}).get("messages", []) if m.get("role") == "user"]


def find_user_with(load, marker: str):
    for message in user_messages(load):
        if marker in message.get("text", "") or marker in json.dumps(message, ensure_ascii=False):
            return message
    return None


def raw_user_with(history, marker: str):
    for frame in (history or {}).get("frames", []):
        if frame.get("type") != "user/message":
            continue
        data = frame.get("data") or {}
        if marker in json.dumps(data, ensure_ascii=False):
            return data
    return None


BLOCK_KEYS = ("type", "text", "attachmentId", "name", "byteSize", "mime", "sha256",
              "width", "height", "referenceId", "referenceType", "path", "sessionId")


def semantic_blocks(blocks):
    """去掉 v4 通用块固有的空 id/arguments 与 Transport omitempty 差异，只比契约字段。"""
    if not isinstance(blocks, list):
        return None
    out = []
    for block in blocks:
        if not isinstance(block, dict):
            return None
        item = {}
        for key in BLOCK_KEYS:
            if key not in block:
                continue
            value = block[key]
            if key == "type" or (key == "text" and block.get("type") == "text") or value not in ("", 0, None):
                item[key] = value
        out.append(item)
    return out


def session_file(home: Path, sid: str):
    matches = list(home.glob(f"sessions/*/{sid}/session.v4.jsonl.zstd"))
    return matches[0] if len(matches) == 1 else None


def object_ids(home: Path):
    root = home / "state" / "attachments" / "objects"
    if not root.exists():
        return set()
    return {p.name for p in root.iterdir() if p.is_dir()}


def staging_entries(home: Path):
    root = home / "state" / "attachments" / "tmp"
    if not root.exists():
        return []
    return list(root.iterdir())


def verify_put_get(review: Review, api: API, home: Path, path: Path, mime: str, width=0, height=0, get=True, label=""):
    label = label or path.name
    original = path.read_bytes()
    response = api.post("attachment.put", {"path": str(path)}, timeout=35)
    result = result_ok(review, f"put/{label}", response)
    attachment = result.get("attachment") if isinstance(result, dict) else None
    if not isinstance(attachment, dict):
        return "", None
    aid = attachment.get("id", "")
    expected = {
        "name": path.name, "byteSize": len(original), "mime": mime, "sha256": sha(original),
        "width": width, "height": height,
    }
    good = ID_RE.fullmatch(aid) is not None
    for key, value in expected.items():
        good = good and attachment.get(key, 0) == value
    review.check(f"put/{label}/real-metadata", good, f"attachment={attachment!r} expected={expected!r}")
    source_leaks = str(path) in response.raw.decode("utf-8", "replace") or path.as_posix() in response.raw.decode("utf-8", "replace")
    review.check(f"put/{label}/source-path-not-returned", not source_leaks and "path" not in attachment, f"keys={sorted(attachment)}")
    obj = home / "state" / "attachments" / "objects" / aid
    blob, meta = obj / "blob", obj / "meta.json"
    stored_ok = blob.is_file() and meta.is_file() and blob.read_bytes() == original
    if meta.is_file():
        metadata = safe_json(meta.read_bytes())
        stored_ok = stored_ok and isinstance(metadata, dict) and metadata.get("id") == aid and metadata.get("sha256") == sha(original)
        stored_ok = stored_ok and str(path) not in meta.read_text(encoding="utf-8") and "path" not in metadata
    else:
        metadata = None
    review.check(f"put/{label}/controlled-store-bytes-meta", stored_ok, f"object={obj.name} meta={metadata!r}")
    if get:
        got = api.post("attachment.get", {"attachmentId": aid}, timeout=35)
        got_result = result_ok(review, f"get/{label}", got)
        decoded = b""
        if isinstance(got_result, dict):
            try:
                decoded = base64.b64decode(got_result.get("dataBase64", ""), validate=True)
            except (ValueError, binascii.Error):
                decoded = b"<invalid-base64>"
        review.check(f"get/{label}/exact-bytes-and-identity", decoded == original and isinstance(got_result, dict) and got_result.get("attachment", {}).get("id") == aid,
                     f"decoded={len(decoded)} original={len(original)}")
        review.check(f"get/{label}/source-path-not-returned", str(path) not in got.raw.decode("utf-8", "replace") and path.as_posix() not in got.raw.decode("utf-8", "replace"))
    return aid, attachment


def run_probe(review: Review):
    out = review.out
    source = out / "source"
    source.mkdir(parents=True)
    home_a, home_b = out / "home-a", out / "home-b"
    fixtures = {
        "pixel.png": make_png(3, 2),
        "photo.jpg": base64.b64decode(JPEG_4X3),
        "anim.gif": base64.b64decode(GIF_5X4),
        "tiny.webp": base64.b64decode(WEBP_1X1),
        "note.txt": "W83_TEXT_ATTACHMENT_BODY\n第二行\n".encode("utf-8"),
        "text-with-png-extension.png": b"plain UTF-8, not an image\n",
        "png-with-text-extension.txt": make_png(6, 5),
        "empty.txt": b"",
        "binary.bin": bytes([0, 1, 2, 3, 4]),
        "invalid-utf8.txt": bytes([0xFF, 0xFE, 0x80]),
        "nul.txt": b"before\x00after",
        "bad-zero-width.png": make_png(3, 2),
        "bad-truncated.png": b"",
        "oversize.txt": b"",
        "exact-limit.txt": b"a" * MAX_ATTACHMENT,
    }
    # 零宽 PNG 保留完整 IDAT/IEND 与正确 CRC，隔离“尺寸非法”而非截断分支。
    bad = bytearray(fixtures["bad-zero-width.png"])
    bad[16:20] = struct.pack(">I", 0)
    bad[29:33] = struct.pack(">I", binascii.crc32(b"IHDR" + bytes(bad[16:29])) & 0xFFFFFFFF)
    fixtures["bad-zero-width.png"] = bytes(bad)
    # 只有合法 signature/IHDR，没有 IDAT/IEND：DecodeConfig 级校验不应冒充完整坏图检测。
    full = make_png(3, 2)
    fixtures["bad-truncated.png"] = full[:33]
    for name, data in fixtures.items():
        (source / name).write_bytes(data)
    with (source / "oversize.txt").open("r+b") as f:
        f.truncate(MAX_ATTACHMENT + 1)
    review.save("fixtures.sha256.json", {name: {"sha256": sha(data), "byteSize": len(data)} for name, data in fixtures.items() if name != "oversize.txt"})

    stub = Stub()
    env = os.environ.copy()
    env.update({
        "DSHD_LLM_BASE": stub.base, "DSHD_LLM_KEY": FAKE_CREDENTIAL,
        "DSHD_LLM_MODEL": "w83-independent-review", "DSHD_SERVE_MODEL_RUNNER": "0",
        "DSHD_SERVE_NO_FOLLOW": "0", "PYTHONDONTWRITEBYTECODE": "1",
    })
    try:
        api, token = review.start_host("A1", home_a, env)
        connect = api.connect(token)
        review.check("auth/ready-token-cookie", connect.status == 303 and bool(api.cookie), f"status={connect.status} cookie={bool(api.cookie)}")
        unauth = API(f"http://{api.host}:{api.port}", review, "A1-unauth")
        expect_error(review, "auth/put-without-cookie", unauth.post("attachment.put", {"path": str(source / 'pixel.png')}), 401, "unauthorized")
        expect_error(review, "auth/get-without-cookie", unauth.post("attachment.get", {"id": "att-00000000000000000000000000000000"}), 401, "unauthorized")

        legacy_sid = create_session(review, api, "legacy")
        target_sid = create_session(review, api, "reference-target")
        blocks_sid = create_session(review, api, "ordered-blocks")

        # 旧 text 请求必须走原 runner、原 durable 形状，不能因 additive content 改变。
        legacy_load, _, legacy_done = prompt_wait(review, api, legacy_sid, {"sessionId": legacy_sid, "text": "W83_LEGACY"}, "legacy")
        legacy_user = find_user_with(legacy_load, "W83_LEGACY") if legacy_done else None
        review.check("legacy/durable-text-no-additive-content", legacy_user is not None and legacy_user.get("text") == "W83_LEGACY" and "content" not in legacy_user,
                     f"user={legacy_user!r}")
        legacy_wire = stub.matching("W83_LEGACY")
        legacy_string_content = False
        if legacy_wire:
            messages = (legacy_wire[-1]["body"] or {}).get("messages", [])
            legacy_string_content = any(m.get("role") == "user" and isinstance(m.get("content"), str) and "W83_LEGACY" in m["content"] for m in messages)
        review.check("legacy/model-wire-string-content", bool(legacy_wire) and legacy_string_content and all(r["authOK"] for r in legacy_wire), f"requests={len(legacy_wire)}")

        ids = {}
        specs = [
            ("pixel.png", "image/png", 3, 2), ("photo.jpg", "image/jpeg", 4, 3),
            ("anim.gif", "image/gif", 5, 4), ("tiny.webp", "image/webp", 1, 1),
            ("note.txt", "text/plain", 0, 0),
            ("text-with-png-extension.png", "text/plain", 0, 0),
            ("png-with-text-extension.txt", "image/png", 6, 5),
            ("exact-limit.txt", "text/plain", 0, 0),
        ]
        for name, mime, width, height in specs:
            aid, _ = verify_put_get(review, api, home_a, source / name, mime, width, height, get=name != "exact-limit.txt")
            ids[name] = aid

        # 重复 put 是不同随机对象；重复 get 不改字节。另留一份 PNG 给持久完整性变异。
        duplicate_note, _ = verify_put_get(review, api, home_a, source / "note.txt", "text/plain", label="duplicate-note")
        review.check("duplicate/put-new-random-id", bool(ids["note.txt"]) and bool(duplicate_note) and ids["note.txt"] != duplicate_note,
                     f"first={ids['note.txt']} second={duplicate_note}")
        tamper_id, _ = verify_put_get(review, api, home_a, source / "pixel.png", "image/png", 3, 2, label="tamper-copy")

        before_bad = object_ids(home_a)
        symlink = source / "symlink-note.txt"
        try:
            symlink.symlink_to(source / "note.txt")
            review.check("fixture/symlink-created", symlink.is_symlink())
        except OSError as exc:
            review.check("fixture/symlink-created", False, f"{type(exc).__name__}: {exc}")
        bad_puts = [
            ("zero-byte", source / "empty.txt", 400, "bad_request"),
            ("oversize", source / "oversize.txt", 400, "bad_request"),
            ("relative-escape", Path("../outside.txt"), 400, "bad_request"),
            ("directory", source, 400, "bad_request"),
            ("symlink", symlink, 400, "bad_request"),
            ("unknown-binary", source / "binary.bin", 400, "bad_request"),
            ("invalid-utf8", source / "invalid-utf8.txt", 400, "bad_request"),
            ("nul-text", source / "nul.txt", 400, "bad_request"),
            ("bad-png-zero-dimension", source / "bad-zero-width.png", 400, "bad_request"),
            ("bad-png-missing-image-data", source / "bad-truncated.png", 400, "bad_request"),
        ]
        for label, path, status, code in bad_puts:
            expect_error(review, f"reject/{label}", api.post("attachment.put", {"path": str(path)}), status, code)
        expect_error(review, "reject/get-relative-traversal", api.post("attachment.get", {"id": "../../outside"}), 400, "bad_request")
        expect_error(review, "reject/get-windows-traversal", api.post("attachment.get", {"id": "..\\..\\outside"}), 400, "bad_request")
        expect_error(review, "reject/get-absolute-path", api.post("attachment.get", {"id": str(source / 'note.txt')}), 400, "bad_request")
        expect_error(review, "reject/get-unknown-valid-id", api.post("attachment.get", {"id": "att-00000000000000000000000000000000"}), 404, "not_found")
        review.check("reject/no-new-objects-or-staging", object_ids(home_a) == before_bad and not staging_entries(home_a),
                     f"added={sorted(object_ids(home_a) - before_bad)} staging={len(staging_entries(home_a))}")

        # 客户端伪造元数据/referenceId/text 旁路；宿主必须按 store 与真实目标重建。
        content = [
            {"type": "text", "text": "W83_BEFORE"},
            {"type": "attachment", "attachmentId": ids["pixel.png"], "name": "forged.png", "byteSize": 1,
             "mime": "text/plain", "sha256": "0" * 64, "width": 999, "height": 999, "path": str(source / 'note.txt')},
            {"type": "attachment", "attachmentId": ids["note.txt"], "name": "credential.txt"},
            {"type": "reference", "referenceType": "file", "path": str(source / "note.txt"),
             "referenceId": "ref-client-forged", "name": "forged", "byteSize": 1},
            {"type": "reference", "referenceType": "session", "sessionId": target_sid, "referenceId": "ref-client-forged"},
            {"type": "text", "text": "W83_AFTER"},
        ]
        blocks_load, blocks_history, blocks_done = prompt_wait(review, api, blocks_sid,
            {"sessionId": blocks_sid, "text": "W83_DECOY_MUST_NOT_PERSIST", "content": content}, "blocks")
        block_message = find_user_with(blocks_load, "W83_BEFORE") if blocks_done else None
        durable = block_message.get("content") if block_message else None
        review.check("blocks/load-exact-order", isinstance(durable, list) and [b.get("type") for b in durable] == ["text", "attachment", "attachment", "reference", "reference", "text"],
                     f"content={durable!r}")
        if isinstance(durable, list) and len(durable) == 6:
            png_block, text_block, file_ref, session_ref = durable[1], durable[2], durable[3], durable[4]
            original_png = (source / "pixel.png").read_bytes()
            review.check("blocks/attachment-metadata-not-client-forgery",
                         png_block.get("attachmentId") == ids["pixel.png"] and png_block.get("name") == "pixel.png" and
                         png_block.get("byteSize") == len(original_png) and png_block.get("mime") == "image/png" and
                         png_block.get("sha256") == sha(original_png) and png_block.get("width") == 3 and png_block.get("height") == 2 and
                         "path" not in png_block and text_block.get("name") == "note.txt", f"png={png_block!r} text={text_block!r}")
            review.check("blocks/references-host-generated-real-targets",
                         REF_RE.fullmatch(file_ref.get("referenceId", "")) is not None and REF_RE.fullmatch(session_ref.get("referenceId", "")) is not None and
                         file_ref.get("referenceId") != "ref-client-forged" and session_ref.get("referenceId") != "ref-client-forged" and
                         file_ref.get("referenceType") == "file" and file_ref.get("name") == "note.txt" and
                         file_ref.get("byteSize") == len((source / "note.txt").read_bytes()) and session_ref.get("sessionId") == target_sid,
                         f"file={file_ref!r} session={session_ref!r}")
            review.check("blocks/text-projection-and-decoy-excluded", block_message.get("text") == "W83_BEFORE\nW83_AFTER" and
                         "W83_DECOY_MUST_NOT_PERSIST" not in json.dumps(blocks_load, ensure_ascii=False), f"text={block_message.get('text')!r}")
        else:
            png_block = text_block = file_ref = session_ref = None
        raw_user = raw_user_with(blocks_history, "W83_BEFORE") if blocks_history else None
        review.check("blocks/history-same-ordered-content", raw_user is not None and semantic_blocks(raw_user.get("content")) == semantic_blocks(durable),
                     f"raw={raw_user!r}")

        # load/history/get 是读路径：v4 与对象均不得被读操作改写。
        v4 = session_file(home_a, blocks_sid)
        v4_before = file_sha(v4) if v4 else ""
        blob_before = file_sha(home_a / "state" / "attachments" / "objects" / ids["pixel.png"] / "blob") if ids["pixel.png"] else ""
        reread = load_session(api, blocks_sid)
        epoch = reread.result.get("journalEpoch", 0) if isinstance(reread.result, dict) else 0
        history_session(api, blocks_sid, epoch)
        if ids["pixel.png"]:
            api.post("attachment.get", {"id": ids["pixel.png"]})
        v4_after = file_sha(v4) if v4 else ""
        blob_after = file_sha(home_a / "state" / "attachments" / "objects" / ids["pixel.png"] / "blob") if ids["pixel.png"] else ""
        review.check("persistence/read-paths-do-not-write", bool(v4_before) and v4_before == v4_after and bool(blob_before) and blob_before == blob_after,
                     f"v4_same={v4_before == v4_after} blob_same={blob_before == blob_after}")

        # 真实 runner 模型线：附件字节真实、文本附件真实、credential 只在 Authorization 头。
        wire = stub.matching("W83_BEFORE")
        review.check("model/exactly-one-blocks-request", len(wire) == 1 and wire[0]["authOK"], f"requests={len(wire)}")
        if wire:
            body = wire[-1]["body"] or {}
            review.save("model-blocks-request.json", body)
            messages = body.get("messages", [])
            current = messages[-1] if messages else {}
            parts = current.get("content")
            raw_body = wire[-1]["raw"]
            png_url = "data:image/png;base64," + base64.b64encode((source / "pixel.png").read_bytes()).decode()
            review.check("model/real-image-and-text-bytes", isinstance(parts, list) and png_url in json.dumps(parts) and
                         b"W83_TEXT_ATTACHMENT_BODY" in raw_body and FAKE_CREDENTIAL.encode() not in raw_body,
                         f"part_types={[p.get('type') for p in parts] if isinstance(parts, list) else type(parts).__name__}")
            before_indices, image_indices, after_indices = [], [], []
            if isinstance(parts, list):
                for i, part in enumerate(parts):
                    text = part.get("text", "")
                    if "W83_BEFORE" in text:
                        before_indices.append(i)
                    if "W83_AFTER" in text:
                        after_indices.append(i)
                    if part.get("type") == "image_url" and part.get("image_url", {}).get("url") == png_url:
                        image_indices.append(i)
            order_ok = any(b < i < a for b in before_indices for i in image_indices for a in after_indices)
            review.check("model/image-preserves-content-block-position", order_ok,
                         f"before={before_indices} image={image_indices} after={after_indices}")
            # 文件引用是显式 path 授权，契约允许引用路径进入模型；attachment 自身不得带源路径。
            plain_part_text = "\n".join(p.get("text", "") for p in parts if isinstance(p, dict)) if isinstance(parts, list) else ""
            reference_text_ok = str(source / "note.txt") in plain_part_text and target_sid in plain_part_text
            review.check("model/explicit-reference-identities", reference_text_ok,
                         f"path_present={str(source / 'note.txt') in plain_part_text} session_present={target_sid in plain_part_text}")

        handoff = home_a / "state" / "prompt-blocks"
        remaining = list(handoff.iterdir()) if handoff.exists() else []
        review.check("persistence/prompt-handoff-cleaned", not remaining, f"remaining={[p.name for p in remaining]}")

        # 坏 content/reference 必须在任何 durable user 或模型调用之前失败。
        bad_content = [
            ("empty-content", [], 400, "bad_request"),
            ("too-many-blocks", [{"type": "text", "text": "x"}] * 65, 400, "bad_request"),
            ("text-over-1mib", [{"type": "text", "text": "x" * ((1 << 20) + 1)}], 400, "bad_request"),
            ("all-empty-text", [{"type": "text", "text": " \n\t"}], 400, "bad_request"),
            ("unknown-block", [{"type": "mystery"}], 400, "bad_request"),
            ("forged-missing-attachment", [{"type": "attachment", "attachmentId": "att-00000000000000000000000000000000"}], 404, "not_found"),
            ("relative-file-reference", [{"type": "reference", "referenceType": "file", "path": "../note.txt"}], 400, "bad_request"),
            ("directory-file-reference", [{"type": "reference", "referenceType": "file", "path": str(source)}], 400, "bad_request"),
            ("missing-file-reference", [{"type": "reference", "referenceType": "file", "path": str(source / 'missing.txt')}], 404, "not_found"),
            ("session-title-not-id", [{"type": "reference", "referenceType": "session", "sessionId": "W83_SESSION_TITLE_NOT_ID"}], 404, "not_found"),
        ]
        v4_invalid_before = file_sha(v4) if v4 else ""
        model_count_before = len(stub.requests)
        for label, blocks, status, code in bad_content:
            reject_prompt(review, api, blocks_sid, blocks, f"reject/prompt-{label}", status, code)
        time.sleep(0.3)
        v4_invalid_after = file_sha(v4) if v4 else ""
        review.check("reject/invalid-content-no-durable-or-model-side-effect", v4_invalid_before == v4_invalid_after and len(stub.requests) == model_count_before,
                     f"v4_same={v4_invalid_before == v4_invalid_after} model_delta={len(stub.requests) - model_count_before}")

        # 同一次请求重复提交：第一轮在飞时第二个请求必须 busy，不得写两份 durable user。
        busy_sid = create_session(review, api, "duplicate-prompt")
        busy_payload = {"sessionId": busy_sid, "content": [{"type": "text", "text": "W83_BUSY_GATE"},
                                                           {"type": "attachment", "attachmentId": ids["note.txt"]}]}
        first = api.post("session.prompt", busy_payload)
        first_result = result_ok(review, "duplicate/first-accepted", first)
        second = api.post("session.prompt", busy_payload)
        expect_error(review, "duplicate/inflight-rejected-busy", second, 409, "busy")
        stub.busy_gate.set()
        if isinstance(first_result, dict) and first_result.get("turnId"):
            busy_load, _, busy_done = wait_turn(review, api, busy_sid, first_result["turnId"], "duplicate")
        else:
            busy_load, busy_done = None, False
        busy_users = [m for m in user_messages(busy_load) if "W83_BUSY_GATE" in json.dumps(m, ensure_ascii=False)] if busy_load else []
        review.check("duplicate/one-durable-user-one-model-request", busy_done and len(busy_users) == 1 and len(stub.matching("W83_BUSY_GATE")) == 1,
                     f"users={len(busy_users)} model={len(stub.matching('W83_BUSY_GATE'))}")

        # A/B home 完全隔离：同 id、同源路径、同 session id 都不能跨 home 解析。
        api_b, token_b = review.start_host("B", home_b, env)
        connect_b = api_b.connect(token_b)
        review.check("ab/connect-B", connect_b.status == 303 and bool(api_b.cookie))
        expect_error(review, "ab/A-id-not-readable-in-B", api_b.post("attachment.get", {"id": ids["note.txt"]}), 404, "not_found")
        b_sid = create_session(review, api_b, "B-session")
        reject_prompt(review, api_b, b_sid,
                      [{"type": "reference", "referenceType": "session", "sessionId": target_sid}],
                      "ab/A-session-reference-not-valid-in-B", 404, "not_found")
        b_note, _ = verify_put_get(review, api_b, home_b, source / "note.txt", "text/plain", label="B-note")
        review.check("ab/same-bytes-independent-random-id", bool(b_note) and b_note != ids["note.txt"], f"A={ids['note.txt']} B={b_note}")
        review.stop_host("B")

        # 真重启：旧 cookie 失效，新 ready token 重连；load/history/get 保持同一块和引用身份。
        review.stop_host("A1")
        api_a2, token_a2 = review.start_host("A2", home_a, env)
        old_cookie = API(f"http://{api_a2.host}:{api_a2.port}", review, "A2-old-cookie")
        old_cookie.cookie = api.cookie
        expect_error(review, "restart/old-cookie-rejected", old_cookie.post("attachment.get", {"id": ids["note.txt"]}), 401, "unauthorized")
        connect_a2 = api_a2.connect(token_a2)
        review.check("restart/new-ready-token-cookie", connect_a2.status == 303 and bool(api_a2.cookie))
        got_restart = api_a2.post("attachment.get", {"id": ids["note.txt"]})
        restart_result = result_ok(review, "restart/get-original-attachment", got_restart)
        restart_bytes = b""
        if isinstance(restart_result, dict):
            try:
                restart_bytes = base64.b64decode(restart_result.get("dataBase64", ""), validate=True)
            except (ValueError, binascii.Error):
                pass
        review.check("restart/exact-bytes", restart_bytes == (source / "note.txt").read_bytes())
        restarted_load_response = load_session(api_a2, blocks_sid)
        restarted_load = result_ok(review, "restart/load", restarted_load_response)
        restarted_message = find_user_with(restarted_load, "W83_BEFORE") if restarted_load else None
        review.check("restart/load-content-byte-equivalent", restarted_message is not None and restarted_message.get("content") == durable,
                     f"content={restarted_message.get('content') if restarted_message else None!r}")
        restart_epoch = restarted_load.get("journalEpoch", 0) if isinstance(restarted_load, dict) else 0
        restarted_history_response = history_session(api_a2, blocks_sid, restart_epoch)
        restarted_history = result_ok(review, "restart/history", restarted_history_response)
        restarted_raw = raw_user_with(restarted_history, "W83_BEFORE") if restarted_history else None
        review.check("restart/history-content-byte-equivalent", restarted_raw is not None and semantic_blocks(restarted_raw.get("content")) == semantic_blocks(durable),
                     f"raw={restarted_raw!r}")
        review.check("restart/read-only-v4-unchanged", bool(v4_invalid_after) and file_sha(v4) == v4_invalid_after,
                     f"same={bool(v4) and file_sha(v4) == v4_invalid_after}")

        # 持久完整性：先证明 blob 篡改真红并被拦截，再改“同字节可重算”的 MIME/尺寸。
        # 只动本探针 home 中专门的 tamper-copy；原 meta/blob 备份在证据根，随后恢复。
        if tamper_id:
            obj = home_a / "state" / "attachments" / "objects" / tamper_id
            blob, meta = obj / "blob", obj / "meta.json"
            if blob.is_file() and meta.is_file():
                original_blob, original_meta = blob.read_bytes(), meta.read_bytes()
                review.save("tamper-backup-blob", original_blob)
                review.save("tamper-backup-meta.json", original_meta)
                mutated = bytearray(original_blob)
                mutated[-1] ^= 1
                blob.write_bytes(mutated)
                expect_error(review, "integrity/blob-tamper-rejected", api_a2.post("attachment.get", {"id": tamper_id}), 500, "internal")
                blob.write_bytes(original_blob)
                metadata = json.loads(original_meta)
                metadata["mime"] = "image/gif"
                metadata["width"] = int(metadata.get("width", 0)) + 10
                meta.write_bytes(json.dumps(metadata, separators=(",", ":")).encode())
                expect_error(review, "integrity/mime-dimension-tamper-rejected", api_a2.post("attachment.get", {"id": tamper_id}), 500, "internal")
                meta.write_bytes(original_meta)
                restored = api_a2.post("attachment.get", {"id": tamper_id})
                restored_result = result_ok(review, "integrity/backup-restored", restored)
                review.check("integrity/restored-original-readable", isinstance(restored_result, dict) and restored_result.get("attachment", {}).get("sha256") == sha(original_blob))
            else:
                review.check("integrity/tamper-fixture-present", False, f"object={obj}")
        else:
            review.check("integrity/tamper-fixture-present", False, "missing tamper id")

        # 汇总所有线上响应、宿主日志、home 文件与模型请求：假 credential 不得落任何一处。
        credential_hits = []
        for label, _, path, response in review.responses:
            if FAKE_CREDENTIAL.encode() in response.raw:
                credential_hits.append(f"response:{label}:{path}")
        for request in stub.requests:
            if FAKE_CREDENTIAL.encode() in request["raw"]:
                credential_hits.append("model-body")
        for root in (home_a, home_b, out):
            for path in root.rglob("*"):
                if path.is_file() and path.stat().st_size <= 16 * 1024 * 1024:
                    try:
                        if FAKE_CREDENTIAL.encode() in path.read_bytes():
                            credential_hits.append(str(path.relative_to(out)))
                    except OSError:
                        pass
        review.check("leak/credential-absent-from-api-model-disk-logs", not credential_hits, f"hits={sorted(set(credential_hits))}")
        review.check("leak/all-stub-authorization-headers-exact", bool(stub.requests) and all(r["authOK"] for r in stub.requests),
                     f"requests={len(stub.requests)}")
        review.save("checks.json", review.checks)
        review.save("restart-identities.json", {"attachmentId": ids["note.txt"], "orderedContent": durable,
                                                "references": [file_ref, session_ref], "targetSessionId": target_sid})
    finally:
        stub.busy_gate.set()
        stub.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default=str(ROOT / "tmp" / "attachments-review-bin" / "dshd.exe"))
    parser.add_argument("--out", default="")
    args = parser.parse_args()
    binary = Path(args.binary).resolve()
    if args.out:
        out = Path(args.out)
        if not out.is_absolute():
            out = ROOT / out
        out = out.resolve()
    else:
        stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
        out = ROOT / "tmp" / f"attachments-review-{stamp}-{os.getpid()}"
    if out.parent != (ROOT / "tmp").resolve() or not out.name.startswith("attachments-review-"):
        print(f"AR_FAIL driver/out-must-be-tmp-attachments-review out={out}", flush=True)
        print(SUMMARY.format(ok=0, fail=1), flush=True)
        return 1
    if not binary.is_file():
        print(f"AR_FAIL driver/binary-missing path={binary}", flush=True)
        print(SUMMARY.format(ok=0, fail=1), flush=True)
        return 1
    out.mkdir(parents=True, exist_ok=False)
    review = Review(out, binary)
    started = time.monotonic()
    review.save("run-manifest.json", {
        "startedUTC": datetime.now(timezone.utc).isoformat(), "binary": str(binary),
        "binarySHA256": file_sha(binary), "binaryBytes": binary.stat().st_size,
        "cwd": str(ROOT), "model": "owned loopback OpenAI-compatible SSE stub; real dshd run child",
        "tokenCookie": "memory only, stdout/files redacted", "fakeCredential": "redacted sentinel",
    })
    print(f"AR_BEGIN binary_sha256={file_sha(binary)} out={out}", flush=True)
    try:
        run_probe(review)
    except Exception as exc:
        review.check("driver/exception", False, f"{type(exc).__name__}: {exc}")
        import traceback
        review.save("driver-exception.txt", traceback.format_exc())
    finally:
        review.close()
        review.save("checks.json", review.checks)
        review.save("run-result.json", {"ok": review.ok, "fail": review.fail, "seconds": time.monotonic() - started})
    print(f"AR_EVIDENCE {out}", flush=True)
    print(SUMMARY.format(ok=review.ok, fail=review.fail), flush=True)
    return 0 if review.fail == 0 and review.ok > 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
