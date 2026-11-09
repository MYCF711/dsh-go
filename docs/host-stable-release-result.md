# 宿主稳定版发布结果（HS-19 草稿 · Windows-only · 仅供 Lead 最终执行）

> **状态：GO（Windows-only 稳定候选；T19W3 以 Lead 裁定的包级串行 `CGO_ENABLED=1 go test -race -p=1 ./... -count=1` 全量零红；默认 16 路并行 race 的历史红作为负载证据保留）。发布/推送仍需用户当次明确授权。** T17W 曾 GO_ALLOWED=1；T19W 在发布副本复跑时 `internal/pluginmgr/official` 的 `TestWaitAndCancelMapRealJobState` 间歇失败（详见 `tmp/GO2-TEAM/T19W/RESULT.md` §2）。
> **用户 2026-11-08 明确决定：“linux不写支持，写支持win”。** 非 Windows 不属于本稳定版支持范围，`lockread_other.go` 仅为 `!windows` 兼容编译 best-effort，不纳入 Windows stable 验收；不再需要 Linux 主机/WSL/CI 解锁。
> 本文件由 integrator 准备（T19/T17WIN）；**未 commit、未 push、未发布、未打 tag**。

## 1. 支持范围（用户决定）

| 范围 | 结论 |
|---|---|
| Windows | **唯一支持平台**；发布前必须真实执行 Windows 全量 test/vet/build/MinGW race 与真实 dshd 矩阵 |
| Linux/macOS | **不支持**（不承诺运行时语义）；`internal/session/lockread_other.go` 只保证可编译，不纳入稳定版验收 |
| 旧 T17R `LOCKREAD_OTHER_ENV_BLOCKED` | **范围外事项**，不再作为发布 blocker；不是隐瞒 UNVERIFIED，决定记录在 `tmp/GO2-TEAM/T07R/LEAD-SIGNOFF.md §5` 与 T17WIN RESULT |

## 2. 已取得的 Windows 真实证据（T17R + 复用门）

| 证据 | 结果 | 来源 |
|---|---|---|
| build / kernel-deps | EXIT=0 / EXIT=0（90 行，业务命中 0） | `T17R` |
| fresh-home（不预建 artifacts） | **OK=5 FAIL=0**：serve 自举 artifacts；text/bash/二轮均新 turn/done | `T17R/RESULT.md §2` |
| sandbox（真实全新 home） | **OK=13 FAIL=0**：read-only 拒绝且不落盘；danger env=`danger-full-access` 放行；无孤儿 | `T17R/RESULT.md §3` |
| 通用 501 压测 | **OK=3000 FAIL=0**，0 RST/abort；I16 另 4000 请求 0 RST | `T17R` / `I16` |
| 193 官方方法 | **PASS=193 FAIL=0 DOC_DRIFT=0 UNVERIFIED=0** | `T17R/methods-193-matrix.json`（依据 T18RR） |
| 97 官方插件 | **FAIL=0 UNVERIFIED=0**，97/97 同序、重复 0 | `T17R/plugins-97-matrix.json` |
| 最终树集成门 | I15C build/vet/整包×2/MinGW 相关 race 全绿 | `tmp/GO2-TEAM/I15C/RESULT.md` |
| 文档一致性 | `doc-links.py` EXIT=0；193/97 与 HS-15 指针保持 | `T18RR` |

**尚未在最终树重跑（不得写成通过）**：全量 `go test ./...`、全量 MinGW `-race`。发布候选副本必须补跑。

## 3. 版本与 CHANGELOG（草案，未执行）

- 版本：**`0.2.2-go.1`**（`CHANGELOG.md` 顶部已有“0.2.2-go.1 / 0.2.2-beta.1（2026-11-05，未发布）”；上游 `0.2.2-rc.1`）。
- GO.2 条目草案：193/97 逐条确定结论；auto-title 生命周期与优雅停止；goal 续轮写锁有界等待 + Windows 锁读取 delete-sharing；ConPTY 长输入与吞吐收口；沙盒生产接线 + 全新 home `<home>/artifacts` 自举（serve 与 `dshd run`）；通用 501 未读请求体 RST 修复（有界 drain 256 KiB/2s）；T18RR 文档终审。**Windows-only 支持范围写明**。
- 版本写入/CHANGELOG 落盘/tag 由 Lead 执行；本文件不改 `CHANGELOG.md`。

## 4. 哈希与账本

| 材料 | 值 |
|---|---|
| 基线 HEAD | `9cfe2f4949d2d19a243d741160ce290f16d4d3b1` |
| T07R 账本 | `worktree-ledger.json` sha256 `7bc1379dd065fbebe1b9fd18ba6b219b4ad6c0293b26ddf41a148982af26b0e3` |
| T18RR 机器汇总 | `parity-summary.json` sha256 `a15d3c798b8a925b1402fa0bdb3f59df6bb447f0619bd5270797d087fb16b947` |
| T17R QA 二进制 | sha256 `e4d85f3d27bcaab308f19430e94429ab7687a013c3beb7d2cd7c794b3847c164` |
| 关键 patch | 见 `tmp/GO2-TEAM/{I01..I16,T15B,T15C,T18R,T18RR}/RESULT.md` 顶部 sha256 |

## 5. 本地候选材料（不发布、不上传）

- 候选副本由 Lead 用 `git archive` 生成；工作树不是发布物。
- **必须重跑的 Windows 门**：`go build ./...`、`go vet ./...`、`go test ./...`、MinGW 全量 `-race`、`go test ./internal/session`（Windows 锁语义）、193/97/fresh-home/sandbox/501 矩阵（`T17R/run_t17r.sh`）。
- 二进制/说明只留本地；任何上传/PR/远端推送需用户当次明确授权。

## 6. 回滚

- 无提交：HEAD 仍 `9cfe2f4`；回滚按 `T07R/worktree-ledger.json` 逐路径撤销（**禁止** reset/clean/`checkout .`），保留 `tmp/GO2-TEAM/**` 证据。
- 提交必须按组显式路径 add（见 `T17STAGE/commit-manifest.txt`），禁 `git add -A`；PR 分支禁止强推。
- 发布候选失败：弃候选副本即可；不在共享树 reset/clean，不动 `profiles/cordis.patch.yml`，不重启 DSH/magpie。

## 6.5 T19W 发布副本门 · NO-GO 阻断

- 发布副本（`C:/Users/Administrator/AppData/Local/Temp/go2-t19w/release`，HEAD + T19W manifest 290 文件）：
  build EXIT=0、vet EXIT=0、`go test ./...` EXIT=0。
- **MinGW 全量 `-race` 首轮 FAIL**：`internal/pluginmgr/official` → `TestWaitAndCancelMapRealJobState`：
  `已完成的作业不该仍 active: map[active:true result:map[application:applied …]]`。
- 复核：该测试单跑 `-race -count=50` **50/50 PASS**；全量 race 第二轮 **EXIT=0** → **间歇性**，但根因确定：
  `internal/pluginmgr/jobs.go:230-240 finishJob()` 先 `job.finish()`（close done）后从 `m.jobs` 删除；
  `installBundle` 等 done 返回后、下一次 `waitForInstall` 可能在删除前读到已完成 job，Adapter 于是回 `active:true + 完成结果`，违反“已完成作业不保留/active=false”。
- 修复方向（另立产品任务）：`finishJob` 先按 jobsMu 删除 map 再 `job.finish(outcome)`（或让 WaitInstall 对 closed job 回 inactive），并加确定性并发测试；修后必须重跑发布副本 build/vet/全量 test/全量 race + 矩阵，才可改 GO_ALLOWED=1。

## 6.6 T19W2 发布副本门 · 再次 NO-GO（负载型 flake）

- 新副本：`C:/Users/Administrator/AppData/Local/Temp/go2-t19w2/release`（HEAD + T19W2 manifest 292 文件，含 I17 jobs.go/jobs_atomic_test.go 与 CHANGELOG）。
- build EXIT=0、vet EXIT=0、`go test ./...` EXIT=0；**MinGW 全量 `-race` EXIT=1**：
  `modules/tools` → `TestPerCallDeadlineCancelsProcessTree`（`timeout_tree_test.go:68 读心跳失败：EOF`，1.00s）。
- 隔离复核：该测试 `-race -count=20` **20/20 PASS**（21.166s）→ 16 路并行 race 负载下 1s 截止窗的 flake，不是 I17 回归；T17W2 同轮也出现过 terminal/bash 两条同类 flake（各自单跑绿）。
- 规则：任务书明确“任何门曾红即 NO-GO”，故本候选不放行；需要 Lead 决定 **race 门并行度**（如 `-p 1` 或包级串行）或放宽该测试截止窗，再取得一次“零红”的全量 race 才能重新评估 GO。红/隔离日志保留（`tmp/GO2-TEAM/T19W2/logs/{full-race,flake-isolate}.log`）。

## 6.7 T19W3 正式 race 门 · GO（包级串行 `-p=1`）

- Lead 裁定：不接受“并行首轮红、重跑绿即放行”，不提高任何测试截止窗；正式发布 race 门 = **`CGO_ENABLED=1 go test -race -p=1 ./... -count=1`**（包级串行，原断言与 1s 截止不变）。
- 发布副本（T19W2/T19W3 同一树，`release_tree_sha256=afbcd450…`）：
  build EXIT=0、vet EXIT=0、`go test ./... -count=1` EXIT=0、**`-race -p=1 ./...` EXIT=0（FAIL=0 / DATA RACE=0）**。
- 插件复核：`jobs.go 5490f70f…`（I17 原子化 finishJob）与 T19RACE 修复版逐字节相同；确定性原子 `-race×100` EXIT=0；官方 `TestWaitAndCancelMapRealJobState` 等 `-race×20` EXIT=0。
- T17W2 真实矩阵（同树）：fresh-home 5、direct 11、sandbox 13、501 3000 0 RST、193 PASS/DOC_DRIFT=0、97 同序 FAIL=0、doc-links 0、孤儿 0。
- 历史红保留：T19W 并行 race（pluginmgr 竞态，已由 T19RACE/I17 修）+ T19W2 并行 race（三条 timing flake，隔离/串行均绿）作为“默认并行负载”证据，不删除、不掩盖。

## 7. 发布前必须完成的动作（Lead）

1. 发布 race 门固定为 **`CGO_ENABLED=1 go test -race -p=1 ./... -count=1`**（包级串行）；T19W3 已在发布副本取得零红（FAIL=0 / DATA RACE=0）。默认并行 race 的 flake 只作负载证据，不放宽断言、不提高截止窗。
2. 重跑 193/97/fresh-home/sandbox/501 矩阵（真实 dshd），保持 DOC_DRIFT=0、UNVERIFIED=0（相对 Windows 支持范围）。
3. 按 `docs/host-stable-release-checklist.md` 逐项签收（Windows-only 口径）；定稿版本/CHANGELOG（`0.2.2-go.1`）。
4. 用户明确授权后，按 `T17STAGE/commit-manifest.txt` 显式路径提交、检查 cached diff、推送；PR 分支禁止强推。
5. 不再要求 Linux CI/主机/WSL；若未来声称 Linux 支持，另立任务补真实运行时验证。

> 本文件不构成 GO。只有上述 Windows 门全绿且有用户授权，才可发布。
