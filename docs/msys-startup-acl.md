# W78 · MSYS 启动失败：二进制位置关联的完整性/ACL 环境

> 日期：2026-11-05。基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树（含 W76/W77）。
> 结论：**不是 D: cwd、session cwd、临时 home 所在盘或产品 bash/Job Object 启动缺陷。决定性变量是 dshd 二进制从哪个位置启动。** 同一二进制内容从本机 C: 证据目录启动得到 High token；从 D: 共享工作区启动得到 Medium token，后者稳定触发 MSYS `NtCreateDirectoryObject ... 0xC0000022` / `0xC0000142`。这是路径关联的 Windows 完整性/ACL 启动环境差异。W78 不改产品启动语义，不改 ACL，不让该失败跳过或变绿。

## 1. 固定变量与证据根

证据根：`C:/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/`。完整标准输出逐轮保存，未用后续轮次覆盖历史红。

| 项 | 固定值 |
|---|---|
| 用户 / Windows session | `Wei\WeiCong` / `1` |
| 父探针 token | High `S-1-16-12288`，`has_restrictions=false`，`in_job=true` |
| 环境 | `MSYSTEM=MINGW64`，`MSYS=`，`CYGWIN=`，`TEMP=/tmp`，`TMP=/tmp`，`HOME=/c/Users/Administrator` |
| Go / Python | `go1.27.3 windows/amd64` / `Python 3.13.15` |
| Git bash | `C:\Program Files\Git\usr\bin\bash.exe`，`5.3.15(2)-release (x86_64-pc-cygwin)` |
| 原始探针 SHA-256 | `174e4fd6661d6c31bf306725f8366ffa59ddf771641f682156d2413175fc9c65` |
| W78 最终探针 SHA-256 | `055bdba93020649d1c8a5d16e9d98b163fed2606a4b8cd2bffaedec066c72f35` |
| 对照二进制 SHA-256 | `118941db774efb69b1cc1d43d2d5027ad05c55bba64dff63545cb4ecc661dc11` |
| 历史红二进制 | `ea312b6861f44459d58cf506a3437b7fdae98ab4f48286e6aab95eaeeb158e0e`；更早一轮为 `8bc55351597d9e7513915a4b923fe026c151d8a0468caf02245c165c1f71a4e8` |

所有对照都用同一 UI：`C:/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain`，同一默认模型与 magpie base。日志只记录 `<redacted>` token/cookie；credential literal 扫描继续执行。

共享根目录原有的 `dshd.exe` 没有用于结论：`python scripts/bin-check.py dshd.exe` 在 C:/D: 均原样返回：

```text
  FAIL 1 个二进制都不比源码旧 ['dshd.exe（比 cmd\dshd\jobs_out.go 旧）']
合计 OK=0 FAIL=1
```

对照二进制是从 C: 当前源码新构建并先通过 `bin-check` 的 `118941…`。历史红用的 `ea312…` 也另行做位置交换，避免把“构建不同”误当“路径不同”。

## 2. C:/D: 完整 W76 对照

### 2.1 真实 jobs 基线

C: 先跑：

```bash
go test ./cmd/dshd -run '^(TestJobsRealDSHDNaturalExitReadback|TestJobsRealDSHDOutputStopAndOwnedProcessTree)$' -count=1 -v
```

原样结尾（`c-baseline-jobs.txt`）：

```text
--- PASS: TestJobsRealDSHDNaturalExitReadback (4.13s)
    --- PASS: TestJobsRealDSHDNaturalExitReadback/success (0.56s)
    --- PASS: TestJobsRealDSHDNaturalExitReadback/failure (0.56s)
--- PASS: TestJobsRealDSHDOutputStopAndOwnedProcessTree (4.86s)
PASS
ok  	dshgo/cmd/dshd	9.064s
C_BASELINE_EXIT=0
```

### 2.2 同一 C: 二进制，在 D: 完整跑两次

命令形状（两轮只改变 Python 新建的证据目录）：

```bash
cd '/d/DeepSeek Harness/default-workspace/DSH-GO'
python scripts/real-chain-headless.py \
  --binary C:/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/bin/dshd.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain
```

两轮原始输出分别为 `d-full-1.txt`、`d-full-2.txt`；两轮结尾均为：

```text
RCH_SUMMARY OK=77 FAIL=0 SKIP=0
RCC_SUMMARY OK=76 FAIL=0 SKIP=0
CORE_RESULT OK=77 FAIL=0 SKIP=0 combined_go_test_exit=0
CURRENT_RESULT OK=76 FAIL=0 SKIP=0 combined_go_test_exit=0
SECRET_SCAN_OK literals=3 leaked_files=0
CORE_OK=77 CORE_FAIL=0 CURRENT_OK=76 CURRENT_FAIL=0
OK=153 FAIL=0 SKIP=0
CLEANUP {"dshd_exit": 1, "owner_verified": true, "taskkill_exit": 0, "home_entries": 32}
```

对应退出标记：`D_FULL_1_EXIT=0`、`D_FULL_2_EXIT=0`。这证明 **D: repo、cwd、home 本身不足以制造红灯**。

### 2.3 同一二进制内容移到 D:，恢复真红

把同一 `118941…` 复制到 D: 任务目录，只改变二进制位置：

```bash
python scripts/real-chain-headless.py \
  --binary D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/w78-msys-startup-acl-token/dshd-118941-on-d.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain
```

原始完整输出 `d-same-118941-on-d.txt`：

```text
RCC_FAIL jobs-output-first-rune-page page={job-ab7a4b2beb3b6cde-1 3af79ef3-72e3-c21e-a7ea-c6276e8de0c8       true 3221225794  0 5 true 167 false} err=<nil>
RCC_FAIL jobs-output-cjk-emoji-runes page={job-ab7a4b2beb3b6cde-1 3af79ef3-72e3-c21e-a7ea-c6276e8de0c8    true 3221225794  1 3 true 167 false} err=<nil>
RCC_FAIL jobs-real-process-tree-pids-images pids=map[child:0 grandchild:0 parent:0]
RCC_FAIL jobs-stop-real-tree result={  false false 0} err=job_finished: job already finished: 任务 job-ab7a4b2beb3b6cde-1 已经结束，不能重复停止（任务已经结束，只能 jobs.output 读取保留输出，不能重复停止）
RCC_FAIL jobs-stop-readback page={job-ab7a4b2beb3b6cde-1 3af79ef3-72e3-c21e-a7ea-c6276e8de0c8    true 3221225794  3 5 true 167 false} err=<nil>
RCC_SUMMARY OK=71 FAIL=5 SKIP=0
OK=148 FAIL=5 SKIP=0
D_SAME_118941_ON_D_EXIT=1
```

最终快照原样保存（`tmp/real-chain-current-rf0_djiu/.../job-ab7a4b2beb3b6cde-1.json`）：

```text
      0 [main] bash (36716) C:\Program Files\Git\usr\bin\bash.exe: *** fatal error - NtCreateDirectoryObject(\BaseNamedObjects\msys-2.0S5-1888ae32e00d56aa): 0xC0000022
```

历史 Lead 两轮红也未删除：

- `tmp/real-chain-current-7agf65zq/`：`binary_sha256=ea312…`，`OK=148 FAIL=5`，`script_exit=1`；
- `tmp/real-chain-current-wjxbvezg/`：`binary_sha256=8bc553…`，`OK=147 FAIL=6`，`script_exit=1`。

`7agf65zq` 的原始快照与本次新红是同一对象名和同一 NTSTATUS：

```text
      0 [main] bash (33936) C:\Program Files\Git\usr\bin\bash.exe: *** fatal error - NtCreateDirectoryObject(\BaseNamedObjects\msys-2.0S5-1888ae32e00d56aa): 0xC0000022
```

### 2.4 最终 patched 探针：C: 绿、D: 红都保留

最终 C: 原始输出 `c-patched-final-full.txt`：

```text
DSHD_ENV {"probe": {"available": true, "integrity_sid": "S-1-16-12288", "session_id": 1, "elevated": true, "has_restrictions": false, "in_job": true}, "dshd": {"available": true, "integrity_sid": "S-1-16-12288", "session_id": 1, "elevated": true, "has_restrictions": false, "in_job": true}}
RCH_SUMMARY OK=77 FAIL=0 SKIP=0
RCC_SUMMARY OK=76 FAIL=0 SKIP=0
CORE_RESULT OK=77 FAIL=0 SKIP=0 combined_go_test_exit=0
CURRENT_RESULT OK=76 FAIL=0 SKIP=0 combined_go_test_exit=0
SECRET_SCAN_OK literals=3 leaked_files=0
CORE_OK=77 CORE_FAIL=0 CURRENT_OK=76 CURRENT_FAIL=0
OK=153 FAIL=0 SKIP=0
CLEANUP {"dshd_exit": 1, "owner_verified": true, "taskkill_exit": 0, "home_entries": 32}
C_PATCHED_FINAL_FULL_EXIT=0
```

最终 D: 原始输出 `d-patched-final-red.txt`：

```text
DSHD_ENV {"probe": {"available": true, "integrity_sid": "S-1-16-12288", "session_id": 1, "elevated": true, "has_restrictions": false, "in_job": true}, "dshd": {"available": true, "integrity_sid": "S-1-16-4096", "session_id": 1, "elevated": true, "has_restrictions": false, "in_job": true}}
RCC_ENV_MSYS_STARTUP_ACL exit=3221225794 output="      1 [main] bash (31252) C:\\Program Files\\Git\\usr\\bin\\bash.exe: *** fatal error - NtCreateDirectoryObject(\\BaseNamedObjects\\msys-2.0S5-1888ae32e00d56aa): 0xC0000022" source=D:\DeepSeek Harness\default-workspace\DSH-GO\tmp\real-chain-current-41n5fp8l\home\state\jobs\6a5008ec-ec05-fe68-a681-ea50615a3894\job-9d65523d2a9bf473-1.json note=MSYS 命名对象启动失败；环境标注，不跳过也不豁免真实 FAIL
RCC_SUMMARY OK=71 FAIL=5 SKIP=0
OK=148 FAIL=5 SKIP=0
CLEANUP {"dshd_exit": 1, "owner_verified": true, "taskkill_exit": 0, "home_entries": 32}
D_PATCHED_FINAL_RED_EXIT=1
```

环境标注不增加 `SKIP`，不减少既有 5 条 `FAIL`，脚本退出仍是 1。失败分支是真红。

首轮 C: 完整链另有真实模型未产生 reasoning 的 `OK=151 FAIL=2`，jobs 当时仍 `76/0`；该轮保留在 `c-full-1-after-tmp.txt`，没有为变绿删除或放宽 reasoning 判据。最终两轮 patched C: 均达到 `153/0`。

## 3. 最小化：双向复制与四格 cwd 矩阵

### 3.1 二进制位置交换，token 随位置改变

直接对 `serve` PID 调 `OpenProcessToken`，同一父 Python、同一用户、同一 Session：

```text
CASE=ea312-on-c
PROCESS_FACTS {"pid": 36428, "integrity_sid": "S-1-16-12288", "session_id": 1, "binary": "C:\\Users\\Administrator\\AppData\\Local\\Temp\\msys-startup-acl-w78-evidence\\bin\\dshd-ea312-on-c.exe"}
CASE=118941-on-d
PROCESS_FACTS {"pid": 31956, "integrity_sid": "S-1-16-4096", "session_id": 1, "binary": "D:\\DeepSeek Harness\\default-workspace\\DSH-GO\\tmp\\w78-msys-startup-acl-token\\dshd-118941-on-d.exe"}
LOCATION_SWAP_EXIT=0
```

补充查询（`process-token-restrictions.txt`）显示 C:/D: 两者均：

```text
"elevated": true, "elevation_type": 1, "has_restrictions": false, "restricted_sids": [], "in_job": true
```

所以不是凭文件名猜权限，也不是 restricted-SID 列表不同。C: 的 `ea312…` 与 D: 的历史 `ea312…` 字节一致；C:/D: 的 `118941…` 也字节一致。位置交换排除了编译内容差异。

### 3.2 同一 dshd run、同一 bash 命令，cwd 不是变量

最小驱动 `minimal_dshd_run_matrix.py` 用本地真实 SSE 让 `dshd run` 执行同一个后台命令：

```bash
printf 'MINIMAL_MSYS_OK'; sleep 0.2
```

四个 case 的 home 都放在 D: 任务目录，避免把 C: home 的写入权限混进 MSYS 判断。最终 v3 等任务自然结束并读取最终 job 快照，原样汇总（`minimal-dshd-run-matrix-v3.txt`）：

```text
CASE_OBSERVED label=c_binary_c_cwd exit=0 timeout=False model_calls=2 msys_fatal=False marker=True
CASE_OBSERVED label=c_binary_d_cwd exit=0 timeout=False model_calls=2 msys_fatal=False marker=True
CASE_OBSERVED label=d_binary_c_cwd exit=0 timeout=False model_calls=2 msys_fatal=True marker=False
CASE_OBSERVED label=d_binary_d_cwd exit=0 timeout=False model_calls=2 msys_fatal=True marker=False
DIAG_MATRIX_EXPECTATION_MATCH=true (not a product PASS; D observations remain startup failures)
MINIMAL_MATRIX_V3_EXIT=0
```

两个 D: 二进制的 job 都是：

```text
"output": "      0 [main] bash (...) C:\\Program Files\\Git\\usr\\bin\\bash.exe: *** fatal error - NtCreateDirectoryObject(\\BaseNamedObjects\\msys-2.0S5-1888ae32e00d56aa): 0xC0000022",
"exit_code": 3221225794,
"done": true
```

两个 C: 二进制的 job 都是 `"output":"MINIMAL_MSYS_OK"` / `exit_code=0`。因此 C: cwd 不能救 D: 二进制，D: cwd 也不能破坏 C: 二进制；无需把 W76 工作目录改到 C: home。

前两版最小驱动保留在 `minimal-dshd-run-matrix.txt` / `-v2.txt`：v1 错把 argv 里的 marker 当输出，v2 又发现 `Dispose` 可把未放行的后台任务收编为 `exit=1`。两版都报告 `DIAG_MATRIX_EXPECTATION_MATCH=false`，没有拿来冒充产品绿。v3 修正为等待自然结束并只读 job 真值。

## 4. MSYS 对象与 Job Object 边界

只读 `NtOpenDirectoryObject(READ_CONTROL)` + `NtQuerySecurityObject` 的原样输出（`msys-object-acl.txt`）：

```text
OBJECT=\BaseNamedObjects\msys-2.0S5-1888ae32e00d56aa
NTOPEN_STATUS=0x00000000
NTQUERY_SIZE_STATUS=0xC0000023 BYTES=104
NTQUERY_STATUS=0x00000000
SDDL=O:LAG:S-1-5-21-2453284766-1072787770-2054666001-513D:(A;;CCDCLCSWRC;;;WD)
NTCLOSE_STATUS=0x00000000
OBJECT_ACL_QUERY_EXIT=0
```

父对象（`base-named-objects-acl.txt`）：

```text
OBJECT=\BaseNamedObjects
NTOPEN_STATUS=0x00000000
NTQUERY_STATUS=0x00000000
SDDL=O:BAG:SYD:(A;;CCDCLCSWRC;;;WD)(A;;DC;;;RC)(A;;CCDCLCSWSDRCWDWO;;;S-1-5-90-0-0)(A;;CCDCLCSWSDRCWDWO;;;SY)S:AI(ML;;NW;;;LW)
```

没有修改这些对象或文件 ACL。D: 文件带 `Mandatory Label\Low Mandatory Level:(I)(NW)` 及 DSH 工作区 ACE，C: 对照文件没有该标签；它与 token 差异一致，但本任务不进一步声称某一个 ACE 单独构成充分根因。直接可验收的事实是“位置改变 → token 完整性改变 → 同一 MSYS 对象名启动红/绿”。

Job Object 也不是区分变量：

- 外部查询：C:/D: `serve` 都 `in_job=true`；
- 产品日志：四格最小复现全部带 `"process_tree":"windows-job-object"`；
- 红绿都在 `attachProcessTree` 之后登记真实 bash PID/映像；红灯是 bash 初始化时退出，helper 从未启动。

因此没有证据支持修改 `modules/exec/bash` 的 Job Object、继承句柄或进程树实现。该结论不外推到其他 Windows 会话、其他 MSYS 安装或所有 ACL 组合。

## 5. W78 探针变更

只改 `scripts/real-chain-headless.py` 的诊断，不改 Transport/UI/产品语义：

1. `native_token_facts(pid)` 只读记录 probe/dshd 的 `integrity_sid`、`session_id`、`elevated`、`has_restrictions`、`in_job`；结果进 stdout、`ready-redacted.json`、`command.json`、`machine-summary.json`。
2. jobs 回合完成后，从 W77 最终快照读取原始 `output/exit_code`；命中 `3221225794` 或 `NtCreateDirectoryObject` 时打印 `RCC_ENV_MSYS_STARTUP_ACL`。
3. 不新增 `SKIP`，不吞 `FAIL`，不改既有断言数。`jobs.output` 失败页可能只有空白，所以标注不拿空白冒充原始 MSYS stderr。

`cmd/dshd` 与 `modules/exec/bash` 没有 W78 改动。共享树原有 W76/W77 内容不是本任务补丁的一部分。

## 6. Lead 在 C: 重跑 153 的明确命令

可以。最终脚本已在当前 C: 副本实测两次 `OK=153 FAIL=0 SKIP=0`，其中最终一轮为 §2.4。Lead 直接执行：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
cd /c/Users/Administrator/AppData/Local/Temp/DSH-GO-msys-startup
mkdir -p tmp /c/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/lead-bin
go build -o /c/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/lead-bin/dshd.exe ./cmd/dshd
python scripts/bin-check.py /c/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/lead-bin/dshd.exe
set -o pipefail
python scripts/real-chain-headless.py \
  --binary C:/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/lead-bin/dshd.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain \
  2>&1 | tee /c/Users/Administrator/AppData/Local/Temp/msys-startup-acl-w78-evidence/lead-c-rerun.txt
```

判据：`bin-check` 先 `OK=1 FAIL=0`；`DSHD_ENV` 的 probe/dshd 都是 `S-1-16-12288`；最终 `OK=153 FAIL=0 SKIP=0` 且脚本退出 0。不要使用 D: `tmp/*bin/dshd.exe`，也不要使用未过 `bin-check` 的共享根 `dshd.exe`。

若要重新保留 D: 红，只需把同一新二进制构建/复制到 D: 任务目录，并把 `--binary` 指到该 D: 路径。预期 `DSHD_ENV dshd.integrity_sid=S-1-16-4096`、jobs 真红，而不是把 D: 改判为环境 PASS。

## 7. 全仓门禁

全仓命令均在 C: 副本执行：`go test ./... -count=1`、`go vet ./...`、`go build ./...`。原样汇总：

```text
ok  	dshgo/cmd/dshd	83.548s
ok  	dshgo/internal/transport	1.126s
ok  	dshgo/modules/exec/bash	26.630s
GO_TEST_EXIT=0
GO_VET_EXIT=0
GO_BUILD_EXIT=0
```

`full-test.txt` 保存所有包原样输出；`full-vet.txt` / `full-build.txt` 为空，真实退出标记分别为 `full-vet-exit.txt` / `full-build-exit.txt`。`kernel-deps.txt` 的 `go list -deps ./internal/kernel` 不含 `dshgo/modules/...` 或业务包。

相关 race 使用 `C:/toolchains/mingw64/bin/gcc.exe`、`CGO_ENABLED=1`：

```bash
go test -race ./cmd/dshd ./modules/exec/bash ./internal/transport -count=1
```

`related-race.txt` 原样输出：

```text
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0
ok  	dshgo/cmd/dshd	267.565s
ok  	dshgo/modules/exec/bash	21.636s
ok  	dshgo/internal/transport	2.421s
RELATED_RACE_EXIT=0
```

## 8. 产物与清理边界

- 最终补丁：`C:/Users/Administrator/AppData/Local/Temp/msys-startup-acl-final.patch`；SHA-256 在同名 `.sha256` 文件。
- 证据根：§1；关键原始输出：`c-patched-final-full.txt`、`d-patched-final-red.txt`、`process-token-location-swap.txt`、`minimal-dshd-run-matrix-v3.txt`、`msys-object-acl.txt`、`base-named-objects-acl.txt`。
- 未 commit/push，未启动 GUI，未重启或关闭 DSH/magpie，未改 Transport/UI，未改 ACL。
- 只清理由测试/探针自己启动并核对归属的 PID；没有按镜像名批杀，没有移动/删除/改名非本任务产物。
