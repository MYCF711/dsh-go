# 定时方向复核 · 2026-11-07 10:08 Asia/Shanghai

## 审计结论（必须显式告知用户）

只读审计 `GO1-20261107-0410/review/AUDIT-TUI-KNOWN-ISSUES.md` 给出 2 BLOCKER + 7 P1 + 7 P2，揭示矩阵绿与真实问题并存。

### BLOCKER

1. W167 reasoning零帧会记UNVERIFIED，但full门要求UNVERIFIED=0；必须先裁口径或使用帧回放夹具，不能靠重试刷绿。
2. W167 80列model picker目标kimi在catalog但位于视口外，驱动必须↓滚动；当前直接includes造成不可满足红。selection还误比INITIAL_MODEL，steer首轮红为模型答LONG_DONE，均先归驱动/模型行为，不归Go宿主产品。

### P1 / 可见缺陷

- W170空白带在80与120均复现，W171 120关/jobs也有同类空白；继续阻塞GO.1。
- W175真实80/120与两个post-hoc audit未跑，静态frames131/0、row10/0不可替代。
- 最新`bin/dshd-fresh.exe`=`b78cfd15…`已实测含`DSHD_TRACE_API`，08:44 W167已使用；W170/W171此前矩阵仍用旧binary，冻结前需重跑。
- W167最新80=73/3/0，120未跑，仍未清。

### P2 已登记

- Bash工具卡参数渲染多余`)`；
- composer多行粘贴第2行曾见`%`/`·`残留，W171 80四连4/4出现，瞬态/持久未定；
- `/permission`每项双行已登记TUI-MODE-DESC-DUP。

## 本轮动作

PLAN新增W175真实矩阵与新binary重跑；LEAD登记2 BLOCKER、P1/P2可见缺陷；UIPLAN同步。未派新成员、未跑第二PTY、未改产品、未push。GO.1仍NO-GO，GUI暂停新增任务。
