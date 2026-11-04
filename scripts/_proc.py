#!/usr/bin/env python3
"""统一的子进程管理器：起 `dshd serve` / `dshd acp`，退出时**连进程树一起收干净**。

为什么有这个文件
================
2026-11-04 现场：任务管理器里 34 个 `dshd*` 残留，每个占一个端口。其中一批来自
本仓的测试脚本——它们 `subprocess.Popen` 起 `dshd serve`，然后**没有 finally、
没有 atexit、也没有信号钩子**。脚本一旦抛异常、被 Ctrl+C、或断言失败提前
`return`，那个 serve 就活下来，端口跟着一起活下来。

四条退出路径，必须都是"死"：

| 路径 | 谁兜的 |
|---|---|
| 正常走完 | `with managed(...)` 的 finally / 结尾显式 `kill_tree()` |
| 抛异常、断言失败 | 同上；就算脚本忘了写，`atexit` 也兜得住 |
| Ctrl+C | SIGINT 钩子**先收子进程，再把 KeyboardInterrupt 抛回去** |
| 我们自己被强杀 | Windows 作业对象（KILL_ON_JOB_CLOSE）：句柄一关，整棵树灭 |

进程树
======
`serve` 会再起 `dshd run` 子进程（`cmd/dshd/serve_tools.go` 的 loopRunner，
`exec.CommandContext`，没有 CREATE_BREAKAWAY_FROM_JOB）。**只杀父会留下孙子**，
所以：

- Windows：每个句柄一个**作业对象**，设 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`。
  作业成员的后代会自动进同一个作业，于是 `TerminateJobObject` 一次收完整棵
  树；连我们被 TerminateProcess 时也一样（句柄关闭 → 作业销毁 → 全灭）。
- 作业没建起来（个别环境不让 assign）时兜底 `taskkill /F /T /PID`。
- 非 Windows：`start_new_session=True` + `killpg`。

只回收自己起的
==============
每个句柄启动时记下 `(pid, 创建时间)`。用 `taskkill` 这种**按 PID** 动手的路径
之前，先比对创建时间——Windows 上 PID 会复用，不比对就会杀错人。作业对象
天生只含我们自己放进去的进程；`popen.kill()` 走的是进程句柄，与 PID 复用无关。
被回收的 PID 记进 `KILL_LOG`，自测用它断言"没碰过别人的进程"。

用法
====
    import _proc

    # 1) 起 serve：等 ready 行，拿 url/token；出作用域自动收树
    with _proc.managed(_proc.run_serve(home="tmp/x", policy="allow")) as srv:
        print(srv.url, srv.token)

    # 2) 起 acp（stdio 对话）；脚本自己读 stdout，所以不挂 drain
    p = _proc.run_acp(bin="tmp/DSH GO.exe", home="tmp/x")
    p.stdin.write(...)          # p.stdin / p.stdout / p.pid / p.popen 都有

    # 3) 兜底自测：故意在起完子进程后抛异常，断言 0 残留
    python scripts/_proc.py --selftest
"""
from __future__ import annotations

import atexit
import contextlib
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time

_WINDOWS = os.name == "nt"
_LOCK = threading.RLock()

#: 被本模块回收过的 PID（只记账，用来证明"没碰过别人的进程"）。
KILL_LOG: list = []

#: 登记表：还没彻底收尾的句柄。atexit / SIGINT 从这里取。
_LIVE: dict = {}
_HOOKS_INSTALLED = False

# ---- Windows 作业对象常量（winnt.h） ----
_JOB_OBJECT_BASIC_ACCOUNTING = 1
_JOB_OBJECT_EXTENDED_LIMIT = 9
_JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000
_PROCESS_TERMINATE = 0x0001
_PROCESS_SET_QUOTA = 0x0100
_PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
_PROCESS_QUERY_INFORMATION = 0x0400
_STILL_ACTIVE = 259


class ProcError(RuntimeError):
    """子进程没起来 / 没给 ready / 起来就死。"""


# --------------------------------------------------------------------------
# Windows 原生：作业对象 + 进程创建时间
# --------------------------------------------------------------------------


def _k32():
    import ctypes

    k = ctypes.WinDLL("kernel32", use_last_error=True)
    k.CreateJobObjectW.restype = ctypes.c_void_p
    k.CreateJobObjectW.argtypes = [ctypes.c_void_p, ctypes.c_wchar_p]
    k.SetInformationJobObject.restype = ctypes.c_int
    k.SetInformationJobObject.argtypes = [
        ctypes.c_void_p, ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
    k.AssignProcessToJobObject.restype = ctypes.c_int
    k.AssignProcessToJobObject.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
    k.TerminateJobObject.restype = ctypes.c_int
    k.TerminateJobObject.argtypes = [ctypes.c_void_p, ctypes.c_uint32]
    k.OpenProcess.restype = ctypes.c_void_p
    k.OpenProcess.argtypes = [ctypes.c_uint32, ctypes.c_int, ctypes.c_uint32]
    k.CloseHandle.restype = ctypes.c_int
    k.CloseHandle.argtypes = [ctypes.c_void_p]
    k.GetProcessTimes.restype = ctypes.c_int
    k.GetProcessTimes.argtypes = [
        ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p]
    k.QueryInformationJobObject.restype = ctypes.c_int
    k.QueryInformationJobObject.argtypes = [
        ctypes.c_void_p, ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32, ctypes.c_void_p]
    return k


class _WinJob:
    """一个受管子进程一个作业对象：`TerminateJobObject` 收整棵树。

    为什么不是"记下所有后代 PID 再逐个杀"：进程会在我们扫描的间隙里再生孩子，
    而作业对象是内核记的账，没有这个竞态。`KILL_ON_JOB_CLOSE` 还顺手覆盖了
    "python 自己被打死、来不及跑任何清理"的那条路。
    """

    def __init__(self):
        self._h = None
        self._assigned = False
        if not _WINDOWS:
            return
        import ctypes

        k = _k32()
        h = k.CreateJobObjectW(None, None)
        if not h:
            raise OSError(ctypes.get_last_error(), "CreateJobObjectW 失败")
        self._h = h
        # 设不上限制也要留着作业：显式 Terminate 照样有效，只是少了"句柄关闭即灭"。
        self._set_limits()

    def _set_limits(self):
        import ctypes

        class _Basic(ctypes.Structure):
            _fields_ = [
                ("PerProcessUserTimeLimit", ctypes.c_longlong),
                ("PerJobUserTimeLimit", ctypes.c_longlong),
                ("LimitFlags", ctypes.c_uint32),
                ("MinimumWorkingSetSize", ctypes.c_size_t),
                ("MaximumWorkingSetSize", ctypes.c_size_t),
                ("ActiveProcessLimit", ctypes.c_uint32),
                ("Affinity", ctypes.c_size_t),
                ("PriorityClass", ctypes.c_uint32),
                ("SchedulingClass", ctypes.c_uint32),
            ]

        class _Io(ctypes.Structure):
            _fields_ = [
                ("ReadOperationCount", ctypes.c_ulonglong),
                ("WriteOperationCount", ctypes.c_ulonglong),
                ("OtherOperationCount", ctypes.c_ulonglong),
                ("ReadTransferCount", ctypes.c_ulonglong),
                ("WriteTransferCount", ctypes.c_ulonglong),
                ("OtherTransferCount", ctypes.c_ulonglong),
            ]

        class _Ext(ctypes.Structure):
            _fields_ = [
                ("BasicLimitInformation", _Basic),
                ("IoInfo", _Io),
                ("ProcessMemoryLimit", ctypes.c_size_t),
                ("JobMemoryLimit", ctypes.c_size_t),
                ("PeakProcessMemoryUsed", ctypes.c_size_t),
                ("PeakJobMemoryUsed", ctypes.c_size_t),
            ]

        info = _Ext()
        info.BasicLimitInformation.LimitFlags = _JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        k = _k32()
        ok = k.SetInformationJobObject(
            self._h, _JOB_OBJECT_EXTENDED_LIMIT, ctypes.byref(info), ctypes.sizeof(info))
        return bool(ok)

    def assign(self, pid):
        """把刚起的进程放进作业；成功返回 True（失败就只剩 taskkill 兜底）。"""
        import ctypes

        if not _WINDOWS or not self._h:
            return False
        k = _k32()
        h = k.OpenProcess(_PROCESS_SET_QUOTA | _PROCESS_TERMINATE, False, int(pid))
        if not h:
            return False
        try:
            return bool(k.AssignProcessToJobObject(self._h, h))
        finally:
            k.CloseHandle(h)

    def active_processes(self):
        """作业里还活着几个进程（收尾后应为 0）。"""
        import ctypes

        if not _WINDOWS or not self._h:
            return None

        class _Acct(ctypes.Structure):
            _fields_ = [
                ("TotalUserTime", ctypes.c_longlong),
                ("TotalKernelTime", ctypes.c_longlong),
                ("ThisPeriodTotalUserTime", ctypes.c_longlong),
                ("ThisPeriodTotalKernelTime", ctypes.c_longlong),
                ("TotalPageFaultCount", ctypes.c_uint32),
                ("TotalProcesses", ctypes.c_uint32),
                ("ActiveProcesses", ctypes.c_uint32),
                ("TotalTerminatedProcesses", ctypes.c_uint32),
            ]

        acct = _Acct()
        k = _k32()
        ok = k.QueryInformationJobObject(
            self._h, _JOB_OBJECT_BASIC_ACCOUNTING, ctypes.byref(acct),
            ctypes.sizeof(acct), None)
        return int(acct.ActiveProcesses) if ok else None

    def terminate(self):
        if not _WINDOWS or not self._h:
            return False
        k = _k32()
        return bool(k.TerminateJobObject(self._h, 1))

    def close(self):
        # 关句柄 = 触发 KILL_ON_JOB_CLOSE：还赖在作业里的孙进程跟着一起走。
        if not _WINDOWS or not self._h:
            return
        import ctypes

        h, self._h = self._h, None
        try:
            _k32().CloseHandle(h)
        except Exception:
            pass


def _win_creation_time(pid):
    """进程创建时间（FILETIME）；拿不到（进程不在/没权限）返回 None。

    这是"这个 PID 还是不是当初那个进程"的判据——PID 会被系统复用。
    """
    import ctypes

    k = _k32()
    h = k.OpenProcess(_PROCESS_QUERY_LIMITED_INFORMATION, False, int(pid))
    if not h:
        h = k.OpenProcess(_PROCESS_QUERY_INFORMATION, False, int(pid))
    if not h:
        return None
    try:
        created = ctypes.c_ulonglong()
        exit_t = ctypes.c_ulonglong()
        kern = ctypes.c_ulonglong()
        user = ctypes.c_ulonglong()
        if not k.GetProcessTimes(h, ctypes.byref(created), ctypes.byref(exit_t),
                                 ctypes.byref(kern), ctypes.byref(user)):
            return None
        return int(created.value)
    finally:
        k.CloseHandle(h)


def _creation_time(pid):
    if _WINDOWS:
        return _win_creation_time(pid)
    try:  # Linux：/proc/<pid>/stat 第 22 个字段是 starttime
        with open("/proc/%d/stat" % int(pid), "rb") as fh:
            rest = fh.read().rsplit(b")", 1)[1].split()
        return int(rest[19])
    except (OSError, IndexError, ValueError):
        return None


# --------------------------------------------------------------------------
# 只读的 dshd 名单（给自测和证据用；这些进程可能是别人的，一根手指都不碰）
# --------------------------------------------------------------------------


def list_dshd(image="DSH GO.exe"):
    """当前活着的 `DSH GO.exe` PID 集合。只读。"""
    try:
        out = subprocess.run(
            ["tasklist", "/FI", "IMAGENAME eq %s" % image, "/FO", "CSV", "/NH"],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
            timeout=60).stdout
    except (OSError, subprocess.SubprocessError):
        return set()
    pat = re.compile(r'^"' + re.escape(image) + r'","(\d+)"')
    pids = set()
    for line in out.splitlines():
        m = pat.match(line.strip())
        if m:
            pids.add(int(m.group(1)))
    return pids


def dshd_cmdlines(image="DSH GO.exe"):
    """{pid: 命令行}。只读；拿不到就返回 {}（自测据此认领"自己起的那些"）。"""
    if not _WINDOWS:
        return {}
    script = (
        "[Console]::OutputEncoding=[Text.Encoding]::UTF8;"
        "Get-CimInstance Win32_Process -Filter \"Name='%s'\" | "
        "ForEach-Object { \"$($_.ProcessId)`t$($_.CommandLine)\" }" % image)
    try:
        out = subprocess.run(
            ["powershell", "-NoProfile", "-NonInteractive", "-Command", script],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
            timeout=90).stdout
    except (OSError, subprocess.SubprocessError):
        return {}
    res = {}
    for line in out.splitlines():
        pid, sep, cmd = line.partition("\t")
        if sep and pid.strip().isdigit():
            res[int(pid.strip())] = cmd.strip()
    return res


def dshd_under(marker):
    """命令行里带 `marker` 的 dshd PID —— 也就是"这些是我起的"。"""
    return {pid for pid, cmd in dshd_cmdlines().items() if marker in cmd}


# --------------------------------------------------------------------------
# 小工具
# --------------------------------------------------------------------------


def _stdio(value):
    """把 "pipe"/"devnull"/"inherit" 简写翻成 subprocess 常量。"""
    if not isinstance(value, str):
        return value
    table = {"pipe": subprocess.PIPE, "devnull": subprocess.DEVNULL, "inherit": None}
    if value not in table:
        raise ValueError("不认识的 stdio 简写：%r（只认 pipe/devnull/inherit）" % (value,))
    return table[value]


def _wait_until(pred, timeout, interval=0.02):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pred():
            return True
        time.sleep(interval)
    return bool(pred())


def _taskkill_tree(pid):
    """兜底：连树一起杀（`serve` 起的 `dshd run` 也在里面）。"""
    try:
        subprocess.run(["taskkill", "/F", "/T", "/PID", str(int(pid))],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
    except (OSError, subprocess.SubprocessError):
        pass


def _parse_ready(line):
    s = line.strip()
    if not s.startswith("ready "):
        return None
    try:
        obj = json.loads(s[len("ready "):])
    except ValueError:
        return None
    return obj if isinstance(obj, dict) else None


# --------------------------------------------------------------------------
# Proc：一个受管子进程（含它的整棵进程树）
# --------------------------------------------------------------------------


class Proc:
    """受管子进程。`kill_tree()` 幂等；登记表 + atexit + 信号钩子兜住所有退出路径。"""

    def __init__(self, argv, *, cwd=None, env=None, stdin="pipe", stdout="pipe",
                 stderr="devnull", text=True, encoding="utf-8", errors="replace",
                 bufsize=1, drain=False, name=None):
        self.argv = [str(a) for a in argv]
        self.name = name or " ".join(self.argv[:2])
        self.lines = []          # drain=True 时：stdout 的每一行（原样，带换行）
        self.info = None         # serve 的 ready 内容
        self.ready = threading.Event()
        self.eof = threading.Event()
        self._finished = False
        self._job = None
        self._reader = None
        self._lock = threading.RLock()

        if drain and _stdio(stdout) != subprocess.PIPE:
            raise ValueError("drain=True 要求 stdout=pipe")

        _install_hooks()
        kw = {}
        if not _WINDOWS:
            kw["start_new_session"] = True   # 自成进程组，收尾时 killpg
        self._popen = subprocess.Popen(
            self.argv, cwd=cwd, env=env,
            stdin=_stdio(stdin), stdout=_stdio(stdout), stderr=_stdio(stderr),
            text=text, encoding=encoding, errors=errors, bufsize=bufsize, **kw)
        self._created = _creation_time(self._popen.pid)
        self._attach_job()
        if drain:
            self._reader = threading.Thread(
                target=self._drain, name="proc-drain:%d" % self._popen.pid, daemon=True)
            self._reader.start()
        with _LOCK:
            _LIVE[id(self)] = self

    # ---- 暴露给脚本的属性 ----

    @property
    def popen(self):
        return self._popen

    @property
    def pid(self):
        return self._popen.pid

    @property
    def stdin(self):
        return self._popen.stdin

    @property
    def stdout(self):
        return self._popen.stdout

    @property
    def stderr(self):
        return self._popen.stderr

    @property
    def url(self):
        return (self.info or {}).get("url")

    @property
    def token(self):
        return (self.info or {}).get("token")

    @property
    def returncode(self):
        return self._popen.poll()

    # ---- 内部 ----

    def _attach_job(self):
        if not _WINDOWS:
            return
        job = None
        try:
            job = _WinJob()
            if job.assign(self._popen.pid):
                self._job = job
                return
        except Exception:
            pass
        if job is not None:
            job.close()   # 分不进去就别留着（KILL_ON_JOB_CLOSE 会误伤别的东西）

    def _drain(self):
        stream = self._popen.stdout
        try:
            for line in stream:
                self.lines.append(line)
                if self.info is None:
                    info = _parse_ready(line)
                    if info is not None:
                        self.info = info
                        self.ready.set()
        except (ValueError, OSError):
            pass
        finally:
            self.eof.set()
            self.ready.set()   # 进程没了也别让 wait_ready 死等

    def _owned(self):
        """这个 PID 现在还是当初那个进程吗（防 PID 复用杀错人）。"""
        if self._created is None:
            return False
        return _creation_time(self._popen.pid) == self._created

    def tail(self, n=3):
        """最近几行输出，报错时贴给人看。"""
        chunks = []
        if self._popen.stderr is not None and not self._popen.stderr.closed:
            try:
                chunks.append(self._popen.stderr.read() or "")
            except (ValueError, OSError):
                pass
        if not chunks:
            chunks = list(self.lines[-n:])
        return "".join(chunks).strip()[-800:]

    def poll(self):
        return self._popen.poll()

    def wait_ready(self, timeout=60.0):
        """等 stdout 上那一行 `ready {…}`；超时/提前退出 → 先收树再抛 ProcError。"""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.ready.is_set():
                break
            if self._popen.poll() is not None and self.eof.is_set():
                break
            time.sleep(0.02)
        if self.info is not None:
            return self.info
        rc = self._popen.poll()
        why = ("超时 %.0fs 没等到 ready" % timeout) if rc is None else (
            "ready 之前就退出了（exit=%s）" % rc)
        tail = self.tail()
        self.kill_tree(reason="ready 失败")
        raise ProcError("%s：%s%s" % (self.name, why, ("\n  " + tail) if tail else ""))

    def wait(self, timeout=None):
        """等它退出；超时返回 None（不抛，和原来的 try/except 写法等价）。"""
        try:
            rc = self._popen.wait(timeout)
        except subprocess.TimeoutExpired:
            return None
        self._release()
        return rc

    def close_stdin(self):
        try:
            if self._popen.stdin is not None and not self._popen.stdin.closed:
                self._popen.stdin.close()
        except (OSError, ValueError):
            pass

    def join_reader(self, timeout=2.0):
        if self._reader is not None and self._reader.is_alive():
            self._reader.join(timeout)

    def kill_tree(self, reason="", timeout=6.0):
        """收掉这个句柄起的**整棵**进程树。幂等；只碰自己起的。"""
        with self._lock:
            if self._finished:
                return
        alive = self._popen.poll() is None
        if alive:
            KILL_LOG.append((self._popen.pid, reason or "kill_tree"))
            try:
                self._kill_tree_inner(timeout)
            except Exception:
                pass
        self._release()
        with _LOCK:
            self._finished = True
            _LIVE.pop(id(self), None)

    def _kill_tree_inner(self, timeout):
        # 1) 作业对象：内核记的账，一次收完整棵树（含 serve 起的 dshd run）。
        if self._job is not None:
            self._job.terminate()
            _wait_until(lambda: self._popen.poll() is not None, min(timeout, 3.0))
        # 2) 作业没建起来 / 还有活口：按 PID 兜底——但先核对归属，防 PID 复用。
        if self._popen.poll() is None and self._owned():
            _taskkill_tree(self._popen.pid)
            _wait_until(lambda: self._popen.poll() is not None, 2.0)
        # 3) 最后一步走进程句柄：和 PID 复用无关，永远是"我们起的那个"。
        if self._popen.poll() is None:
            try:
                self._popen.kill()
            except OSError:
                pass

    def _release(self):
        """进程已收（或已自己退出）：关作业句柄、摘登记、收 reader 线程。"""
        try:
            self._popen.wait(timeout=3)
        except (subprocess.TimeoutExpired, OSError):
            pass
        job, self._job = self._job, None
        if job is not None:
            job.close()
        self.join_reader(2.0)
        with _LOCK:
            _LIVE.pop(id(self), None)

    # ---- with 语句 ----

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        self.kill_tree(reason="with 退出" if exc_type is None else "with 异常退出")
        return False

    def __repr__(self):
        return "<Proc %s pid=%s%s>" % (
            self.name, self._popen.pid, "" if self._popen.poll() is None else " exited")


# --------------------------------------------------------------------------
# 登记表 + 钩子
# --------------------------------------------------------------------------


def kill_all(reason="退出"):
    """收掉所有登记过的子进程（atexit / 信号钩子 / 手动兜底都走这里）。"""
    with _LOCK:
        procs = list(_LIVE.values())
    for p in procs:
        try:
            p.kill_tree(reason=reason)
        except Exception:
            pass


def _make_signal_handler(signum, prev):
    def handler(sig, frame):
        # 先把子进程收掉，再把信号交回原来的处理器——
        # KeyboardInterrupt 仍然按原样抛给脚本，try/finally 的语义不变。
        kill_all("signal %s" % sig)
        if prev is None or prev is signal.SIG_IGN:
            return
        if prev is signal.SIG_DFL:
            if sig in (getattr(signal, "SIGINT", None), getattr(signal, "SIGBREAK", None)):
                raise KeyboardInterrupt()
            os._exit(128 + int(sig))
        if callable(prev):
            return prev(sig, frame)

    return handler


def _install_hooks():
    """幂等：atexit + SIGINT/SIGTERM/SIGBREAK。第一次起子进程时装上。"""
    global _HOOKS_INSTALLED
    with _LOCK:
        if _HOOKS_INSTALLED:
            return
        _HOOKS_INSTALLED = True
    atexit.register(kill_all, "atexit")
    for name in ("SIGINT", "SIGTERM", "SIGBREAK"):
        sig = getattr(signal, name, None)
        if sig is None:
            continue
        try:
            prev = signal.getsignal(sig)
        except (OSError, ValueError):
            continue
        try:
            signal.signal(sig, _make_signal_handler(sig, prev))
        except (OSError, ValueError):
            pass   # 非主线程 / 平台不支持：还有 atexit 兜底


@contextlib.contextmanager
def managed(proc):
    """`with managed(run_serve(...)) as srv:` —— 正常/异常/KeyboardInterrupt 都收。"""
    try:
        yield proc
    finally:
        if proc is not None:
            proc.kill_tree(reason="managed 退出")


# --------------------------------------------------------------------------
# 起进程的三个口子
# --------------------------------------------------------------------------


def default_bin():
    """默认二进制：$DSHD_BIN，否则仓库根的 tmp/DSH GO.exe。"""
    env = os.environ.get("DSHD_BIN")
    if env:
        return env
    here = os.path.dirname(os.path.abspath(__file__))
    return os.path.join(os.path.dirname(here), "tmp", "DSH GO.exe")


def spawn(argv, **kw):
    """通用受管子进程（作业对象照样挂上）。"""
    return Proc(argv, **kw)


def run_serve(home=None, policy=None, extra=None, *, bin=None, cwd=None, env=None,
              addr=None, max_steps=None, stderr="devnull", ready_timeout=60.0,
              require_ready=True, drain=True, name="dshd serve"):
    """起 `dshd serve`，等它打出那一行 `ready {…}`，返回句柄（带 .url/.token）。

    `require_ready=False` 时只起不等（H11 那种"我要看完整 stdout"的用途）。
    ready 之前就退出、或超时 → 先收树再抛 `ProcError`，绝不留半个进程。
    """
    argv = [bin or default_bin(), "serve"]
    if home is not None:
        argv += ["--home", str(home)]
    if policy is not None:
        argv += ["--policy", str(policy)]
    if addr is not None:
        argv += ["--addr", str(addr)]
    if max_steps is not None:
        argv += ["--max-steps", str(max_steps)]
    argv += [str(a) for a in (extra or [])]

    p = Proc(argv, cwd=cwd, env=env, stdin="devnull", stdout="pipe",
             stderr=stderr, drain=drain, name=name)
    if require_ready:
        p.wait_ready(ready_timeout)
    return p


def run_acp(home=None, extra=None, *, bin=None, cwd=None, env=None,
            stdin="pipe", stdout="pipe", stderr="devnull", bufsize=1, drain=False,
            name="dshd acp"):
    """起 `dshd acp`（stdio 对话）。脚本自己读写管道，所以默认不挂 drain。"""
    argv = [bin or default_bin(), "acp"]
    if home is not None:
        argv += ["--home", str(home)]
    argv += [str(a) for a in (extra or [])]
    return Proc(argv, cwd=cwd, env=env, stdin=stdin, stdout=stdout, stderr=stderr,
                bufsize=bufsize, drain=drain, name=name)


# --------------------------------------------------------------------------
# 自检：故意在起完子进程之后出事，断言**一个都不剩**
# --------------------------------------------------------------------------
#
# 判据不是"模块能 import"，是"出事了以后任务管理器里看不见它"：
#   - 起真的 `dshd serve`，然后分别用 异常 / KeyboardInterrupt / 真的 SIGINT 钩子 /
#     sys.exit 四条路提前退出；
#   - 每条路跑完都断言：那个 PID 死了、命令行里带我这个临时 home 的 dshd 一个不剩、
#     端口不再 LISTENING；
#   - 再单独造一棵**三层**进程树（python → python → python），断言孙子也没了
#     （这就是 serve 起 dshd run 的形状）；
#   - 全程只记账、不碰基线进程：KILL_LOG 里出现基线 PID 就算红。

_SELFTEST_CHILD = r'''
import atexit, os, signal, sys
sys.path.insert(0, os.environ["PROC_SCRIPTS"])


def _report():
    # 先注册 → 最后跑（atexit 后进先出），这时 kill_all 已经跑完了。
    try:
        import _proc
        killed = ",".join(str(p) for p, _ in _proc.KILL_LOG)
    except Exception:
        killed = ""
    print("KILLED=" + killed, flush=True)


atexit.register(_report)
import _proc

mode = sys.argv[1]
p = _proc.run_serve(bin=os.environ["PROC_BIN"], home=os.environ["PROC_HOME"],
                    policy="deny", ready_timeout=90)
print("SERVE_PID=%d" % p.pid, flush=True)
print("URL=%s" % (p.url or ""), flush=True)

if mode == "raise":
    raise RuntimeError("模拟断言失败：起完 serve 直接抛异常")
if mode == "kir":
    raise KeyboardInterrupt()
if mode == "sigint":
    # 真的走信号处理器：它必须**先收子进程，再把 KeyboardInterrupt 抛回来**。
    signal.getsignal(signal.SIGINT)(signal.SIGINT, None)
if mode == "exit":
    sys.exit(3)
if mode == "ok":
    p.kill_tree("正常路径")
'''

_SELFTEST_TREE = r'''
import os, subprocess, sys, time
sys.path.insert(0, os.environ["PROC_SCRIPTS"])
import _proc

out = os.environ["PROC_TREE_OUT"]
mid_code = (
    "import os, subprocess, sys, time\n"
    "out = os.environ['PROC_TREE_OUT']\n"
    "leaf = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(180)'])\n"
    "open(out, 'a').write('LEAF=' + str(leaf.pid) + ' ')\n"
    "time.sleep(180)\n"
)
top = _proc.spawn([sys.executable, "-u", "-c", mid_code],
                  stdin="devnull", stdout="devnull", stderr="devnull")
print("MID=%d" % top.pid, flush=True)
time.sleep(1.5)
raise RuntimeError("模拟：起了两层子进程之后抛异常")
'''


def _pid_alive(pid):
    return _creation_time(pid) is not None


def _port_listening(port):
    """端口上还有人 LISTENING 吗；netstat 不可用返回 None。"""
    if not port:
        return None
    try:
        out = subprocess.run(["netstat", "-ano"], capture_output=True, text=True,
                             encoding="utf-8", errors="replace", timeout=90).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    pat = re.compile(r":%d\s" % int(port))
    return any(pat.search(line) and "LISTENING" in line for line in out.splitlines())


def _parse_kv(text, key):
    for line in text.splitlines():
        if line.startswith(key + "="):
            return line.split("=", 1)[1].strip()
    return ""


def _selftest():
    ok = fail = 0

    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1
            print("  OK   " + name)
        else:
            fail += 1
            print("  FAIL " + name + " " + str(detail)[:400])

    here = os.path.dirname(os.path.abspath(__file__))
    binary = os.environ.get("DSHD_BIN") or os.path.join(
        os.path.dirname(here), "tmp", "DSH GO.exe")
    if not os.path.exists(binary):
        print('找不到 %s —— 先 go build -o "tmp/DSH GO.exe" ./cmd/dshd' % binary)
        return 1

    baseline = list_dshd()
    print("基线：%d 个 DSH GO.exe 活着 %s（这些不是我的，一根手指都不碰）"
          % (len(baseline), sorted(baseline)))

    homes = []
    killed_by_children = set()

    def run_child(mode, home):
        env = dict(os.environ)
        env.update(PROC_SCRIPTS=here, PROC_BIN=binary, PROC_HOME=home)
        r = subprocess.run([sys.executable, "-u", "-c", _SELFTEST_CHILD, mode],
                           capture_output=True, text=True, encoding="utf-8",
                           errors="replace", env=env, timeout=180)
        pid = _parse_kv(r.stdout, "SERVE_PID")
        url = _parse_kv(r.stdout, "URL")
        for item in _parse_kv(r.stdout, "KILLED").split(","):
            if item.strip().isdigit():
                killed_by_children.add(int(item))
        port = 0
        m = re.search(r":(\d+)$", url or "")
        if m:
            port = int(m.group(1))
        return r, (int(pid) if pid.isdigit() else 0), port

    # ---- 四条提前退出的路，每条都要 0 残留 ----
    for mode, label in [
        ("raise", "抛异常（断言失败那种）"),
        ("kir", "KeyboardInterrupt（Ctrl+C 在脚本里看到的样子）"),
        ("sigint", "真的 SIGINT 处理器"),
        ("exit", "sys.exit(3)"),
        ("ok", "正常路径（显式 kill_tree）"),
    ]:
        home = os.path.join(os.path.dirname(os.path.abspath(binary)),
                            "proc-selftest-%s-%d" % (mode, os.getpid()))
        homes.append(home)
        r, pid, port = run_child(mode, home)
        gone = _wait_until(lambda: not _pid_alive(pid), 15.0) if pid else False
        left = dshd_under(home)
        port_free = _port_listening(port)
        chk("[%s] 起了 serve（pid=%s）" % (label, pid or "?"), pid > 0,
            (r.stderr or r.stdout)[-300:])
        chk("[%s] 退出后 serve 的 PID 没了" % label, gone, "pid=%s 还活着" % pid)
        chk("[%s] 命令行带我这个 home 的 dshd 一个不剩" % label, not left,
            "还在：%s" % sorted(left))
        chk("[%s] 端口没人再 LISTENING" % label, port_free is not True,
            "port=%s 仍被占" % port)

    # ---- 三层进程树：孙进程也得死 ----
    tree_home = os.path.join(os.path.dirname(os.path.abspath(binary)),
                             "proc-selftest-tree-%d" % os.getpid())
    homes.append(tree_home)
    tree_out = tree_home + ".pids"
    env = dict(os.environ)
    env.update(PROC_SCRIPTS=here, PROC_TREE_OUT=tree_out)
    r = subprocess.run([sys.executable, "-u", "-c", _SELFTEST_TREE],
                       capture_output=True, text=True, encoding="utf-8",
                       errors="replace", env=env, timeout=180)
    mid = int(_parse_kv(r.stdout, "MID") or 0)
    leaf = 0
    try:
        with open(tree_out, encoding="utf-8") as fh:
            m = re.search(r"LEAF=(\d+)", fh.read())
        leaf = int(m.group(1)) if m else 0
    except OSError:
        pass
    finally:
        if os.path.exists(tree_out):
            os.remove(tree_out)
    chk("[进程树] 三层都起来了（mid=%s leaf=%s）" % (mid or "?", leaf or "?"),
        mid > 0 and leaf > 0, (r.stderr or "")[-300:])
    mid_gone = _wait_until(lambda: not _pid_alive(mid), 15.0) if mid else False
    leaf_gone = _wait_until(lambda: not _pid_alive(leaf), 15.0) if leaf else False
    chk("[进程树] 父进程死了", mid_gone, "mid=%s 还活着" % mid)
    chk("[进程树] **孙进程也死了**（只杀父会漏的就是它）", leaf_gone, "leaf=%s 还活着" % leaf)

    # ---- 只收自己的 ----
    chk("[只收自己的] 没有碰过基线进程", not (killed_by_children & baseline),
        "碰了：%s" % sorted(killed_by_children & baseline))
    still_there = baseline & list_dshd()
    print("  INFO 基线里 %d/%d 个还在（少掉的多半是别人自己退的）"
          % (len(still_there), len(baseline)))
    for home in homes:
        shutil.rmtree(home, ignore_errors=True)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


def main(argv):
    if "--selftest" in argv:
        return _selftest()
    print("用法：python scripts/_proc.py --selftest", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
