#!/usr/bin/env python3
"""对照 PERF-120S 的磁盘 Transport journal、原始 history 与 WS 正帧。"""
from __future__ import annotations

import argparse
import base64
import json
from pathlib import Path
import re
import sys


def compact(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def jsonl(path: Path):
    with path.open("r", encoding="utf-8") as f:
        for line in f:
            if line.strip():
                yield json.loads(line)


def validate_processes(run: Path, summary: dict) -> tuple[int, int]:
    """spawn 行覆盖每次 turn；资源行按 PID+createTime 区分 Windows PID 复用。"""
    resources = list(jsonl(run / "resources.jsonl"))
    generations = {}
    for sample in resources:
        for proc in sample.get("processes", []):
            key = (int(proc["pid"]), float(proc["createTime"]))
            row = generations.setdefault(key, {"pid": key[0], "createTime": key[1],
                "firstSeenUtc": sample["timeUtc"], "lastSeenUtc": sample["timeUtc"],
                "exe": proc["exe"], "ppid": proc["ppid"],
                "ownedByProbeTree": proc.get("ownedByProbeTree", False)})
            row["lastSeenUtc"] = sample["timeUtc"]
    generation_rows = sorted(generations.values(), key=lambda r: (r["firstSeenUtc"], r["pid"], r["createTime"]))
    (run / "pid-generations.json").write_text(json.dumps(generation_rows, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8")
    by_pid = {}
    for row in generation_rows:
        by_pid.setdefault(row["pid"], []).append(row)
    owner_by_session = {meta["sessionId"]: label for label, meta in summary["sessions"].items()}
    spawn_rows = []
    for line in (run / "dshd-stderr.log").read_text(encoding="utf-8", errors="replace").splitlines():
        match = re.search(r"spawn pid=(\d+) argv=\[(.*)\]", line)
        if not match:
            continue
        pid = int(match.group(1))
        argv = match.group(2)
        session = re.search(r"--session\s+(\S+)", argv)
        marker = re.search(r"marker=(P120-([A-E])-(\d+))", argv)
        sid = session.group(1) if session else ""
        mark = marker.group(1) if marker else ""
        label = marker.group(2) if marker else ""
        observed = by_pid.get(pid, [])
        spawn_rows.append({"ordinal": len(spawn_rows) + 1, "pid": pid, "argv": argv,
            "sessionId": sid, "marker": mark, "markerSession": label,
            "expectedSession": owner_by_session.get(sid, ""),
            "homeArgvPresent": str(run / "home") in argv,
            "observedCreateTimes": [g["createTime"] for g in observed],
            "ownedByProbeTree": bool(observed) and all(g["ownedByProbeTree"] for g in observed)})
    with (run / "spawn-attribution.jsonl").open("w", encoding="utf-8", newline="\n") as f:
        for row in spawn_rows:
            f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
    turns = list(jsonl(run / "turns.jsonl"))
    unique_pids = {row["pid"] for row in spawn_rows}
    checks = []

    def check(name: str, condition: bool, detail: object) -> None:
        checks.append({"name": name, "pass": bool(condition), "detail": detail})
        print(f"PROCESS_{'OK' if condition else 'FAIL'} {name} {detail}", flush=True)

    check("spawn-count-equals-turns", len(spawn_rows) == len(turns) and len(spawn_rows) > 0,
          {"spawn": len(spawn_rows), "turns": len(turns)})
    check("every-spawn-pid-was-sampled", all(row["observedCreateTimes"] for row in spawn_rows),
          {"spawn": len(spawn_rows), "uniquePids": len(unique_pids)})
    bad_markers = [row["ordinal"] for row in spawn_rows if not row["homeArgvPresent"]
                   or not row["markerSession"] or row["markerSession"] != row["expectedSession"]]
    check("marker-home-session-isolated", not bad_markers, {"badOrdinals": bad_markers[:20]})
    check("all-process-generations-tree-owned", bool(generation_rows)
          and all(row["ownedByProbeTree"] for row in generation_rows), len(generation_rows))
    check("pid-reuse-explicitly-attributed", len(spawn_rows) - len(unique_pids) >= 0
          and all(row["ownedByProbeTree"] for row in spawn_rows),
          {"spawnRecords": len(spawn_rows), "uniquePidValues": len(unique_pids),
           "pidReuseRecords": len(spawn_rows) - len(unique_pids),
           "note": "PID 不是唯一世代；pid-generations.json 使用 PID+createTime"})
    ok = sum(1 for row in checks if row["pass"])
    fail = len(checks) - ok
    result = {"ok": ok, "fail": fail, "checks": checks, "spawnRecords": len(spawn_rows),
              "uniquePidValues": len(unique_pids), "pidReuseRecords": len(spawn_rows) - len(unique_pids),
              "processGenerations": len(generation_rows)}
    (run / "process-validation.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8")
    print(f"PROCESS_CHECK OK={ok} FAIL={fail} SPAWN={len(spawn_rows)} UNIQUE_PID={len(unique_pids)} REUSE={len(spawn_rows)-len(unique_pids)}",
        flush=True)
    return ok, fail


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("evidence", help="包含 summary.json/history-*.json/home 的 run 目录")
    args = ap.parse_args()
    run = Path(args.evidence).resolve()
    summary = json.loads((run / "summary.json").read_text(encoding="utf-8"))
    live = list(jsonl(run / "frames.jsonl"))
    records = {}
    journal_files = sorted((run / "home").glob("sessions/*/*/transport-journal.v1.jsonl"))
    for path in journal_files:
        for line_no, rec in enumerate(jsonl(path), 1):
            if rec.get("kind") != "frame":
                continue
            sid = rec.get("sessionId", "")
            records.setdefault(sid, []).append({"path": str(path.relative_to(run)), "line": line_no,
                "seq": rec.get("seq"), "type": rec.get("type"), "time": rec.get("time"),
                "data": base64.b64decode(rec.get("data", ""))})
    checks = []

    def check(session: str, name: str, condition: bool, detail: object = "") -> None:
        row = {"session": session, "name": name, "pass": bool(condition), "detail": detail}
        checks.append(row)
        print(f"JOURNAL_{'OK' if condition else 'FAIL'} {session}/{name} {detail}", flush=True)

    for label, meta in sorted(summary.get("sessions", {}).items()):
        sid = meta["sessionId"]
        disk = records.get(sid, [])
        history = json.loads((run / f"history-{label}.json").read_text(encoding="utf-8"))["frames"]
        ws = {}
        duplicates = []
        for frame in live:
            if frame.get("session") != label or int(frame.get("seq", 0)) <= 0:
                continue
            seq = int(frame["seq"])
            if seq in ws:
                duplicates.append(seq)
            ws[seq] = frame
        check(label, "disk-journal-present-contiguous", bool(disk)
              and all(int(f["seq"]) == i + 1 for i, f in enumerate(disk)), len(disk))
        check(label, "history-count-equals-disk", len(history) == len(disk),
              {"history": len(history), "disk": len(disk)})
        mismatches = []
        for i, (h, d) in enumerate(zip(history, disk), 1):
            if int(h.get("seq", 0)) != int(d["seq"]) or h.get("type") != d["type"] \
                    or int(h.get("time", 0)) != int(d["time"]) or compact(h.get("data")) != d["data"]:
                mismatches.append(i)
        check(label, "history-raw-data-byte-equal-disk", not mismatches,
              {"mismatchSeqs": mismatches[:20], "compared": min(len(history), len(disk))})
        ws_mismatch = []
        missing = []
        for d in disk:
            seq = int(d["seq"])
            f = ws.get(seq)
            if f is None:
                missing.append(seq)
            elif f.get("type") != d["type"] or int(f.get("time", 0)) != int(d["time"]) \
                    or compact(f.get("data")) != d["data"]:
                ws_mismatch.append(seq)
        check(label, "ws-covers-and-byte-equals-disk", not missing and not ws_mismatch and not duplicates,
              {"missing": missing[:20], "mismatch": ws_mismatch[:20], "duplicatePositiveSeq": duplicates[:20]})
    extra = sorted(set(records) - {m["sessionId"] for m in summary.get("sessions", {}).values()})
    check("ALL", "exact-five-journal-files", len(journal_files) == 5 and not extra,
          {"files": len(journal_files), "extraSessionIds": extra})
    ok = sum(1 for c in checks if c["pass"])
    fail = len(checks) - ok
    result = {"ok": ok, "fail": fail, "checks": checks,
              "comparison": "base64-decoded disk data == compact UTF-8 JSON data in history and WS; seq/type/time also exact"}
    (run / "journal-validation.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"JOURNAL_CHECK OK={ok} FAIL={fail}", flush=True)
    process_ok, process_fail = validate_processes(run, summary)
    return 0 if fail == 0 and process_fail == 0 and ok > 0 and process_ok > 0 else 1


if __name__ == "__main__":
    sys.exit(main())
