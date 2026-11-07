# TUI 上游视觉资产：鲸鱼动画与 Gentle Mist Blue

本次只交付数据资产，不改 Go 渲染代码。上游为 MIT 项目 [`ccch1mneyyy/dsh-TUI`](https://github.com/ccch1mneyyy/dsh-TUI)，固定提交 **`1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`**；许可证原文在 [`UPSTREAM_LICENSE.txt`](../cmd/dshtui/assets/UPSTREAM_LICENSE.txt)。

## 1. 交付清单

- [`assets/whale/`](../cmd/dshtui/assets/whale/)：22 个原始帧，文件名即上游帧名。每帧 25 行 × 40 ASCII 列，LF，末尾一个 LF，每文件 1025 字节；字符表为 `.DBLWHZ`。
- [`frames.json`](../cmd/dshtui/assets/frames.json)：22 帧的源顺序、上游行号、逐帧 SHA-256；`OPENING_SEQUENCES` 的逐步帧名/索引/毫秒/循环；`whaleIdle.ts` 与 `whaleLayers.ts` 的独立层映射、节奏、循环及合成顺序。
- [`theme-mist.json`](../cmd/dshtui/assets/theme-mist.json)：Gentle Mist Blue（雾蓝）`dark` / `light` 两套完整 `Theme`，每套 81 键，含上游 hex、运行时 `rgb(R,G,B)` 或空字符串、来源表达式/行号及逐色 SHA-256。
- [`verify_assets.py`](../cmd/dshtui/assets/verify_assets.py)：只用 Python 标准库的离线校验，不访问网络，不修改文件。
- 既有 `assets.go`、`whale_standard.txt` 不在本次改动中。

### 上游源码指纹

| 上游路径 | 原始文件 SHA-256 |
|---|---|
| `src/components/whaleFrames.ts` | `579ff09b132864b9a9a7c7c3e38ef445b77850d9e3f7674eeabf34444c4c3b3c` |
| `src/components/whaleLayers.ts` | `be2dda03c6fea8f85a1bb824e1022380b76758987010fe9ce7ab3f6802afddb6` |
| `src/components/whaleIdle.ts` | `9310e69833d2abb8c18cd6ba6319d29d60835abe9f0616a4ee355725a8f72368` |
| `src/theme.ts` | `318349fed19dd8ed673ea3c7520de43bf7777bbcec3ddb1c79bba2da3d32f377` |

`src/theme.ts` 与本地参考 `dsh-TUI-upstream-key/src_theme.ts` 逐字节一致。帧及颜色的详细哈希不重复抄入本文，以两个 JSON 清单为准，避免第二份事实源漂移。

## 2. 帧顺序与开屏节奏

22 帧严格按 `WHALE_FRAMES` / `WHALE_FRAME_INDEX`：

```text
0 standard       1 blink          2 fin1           3 fin2
4 spout1         5 spout2         6 spout3         7 spout4
8 spout5         9 spout6        10 tail1         11 tail2
12 tail3        13 tail4         14 heart1        15 heart2
16 heart3       17 sleep1        18 sleep2        19 sleep3
20 sleep4       21 sleep5
```

每次 logo header mount 及 `/deepseek` replay 在 `classic`、`heart`、`sleep` 中均匀随机选一段；三段均不循环，结束回到 `standard`。

| 序列 | 上游帧与停留时间（ms） | 步数 | 总时长 |
|---|---|---:|---:|
| `classic` | `standard(400) → blink(250) → standard(300) → spout1…spout6(各150) → standard(250) → tail1/2/3(各150) → tail4(180) → tail3/2/1(各150) → standard(300)` | 18 | 3480 ms |
| `heart` | `standard(400) → heart1(350) → heart2(350) → heart3(450) → heart2(350) → heart1(350) → standard(500)` | 7 | 2750 ms |
| `sleep` | `standard(400) → sleep1/2/3/4(各250) → sleep5(300) → sleep4/3/2/1(各250) → standard(400)` | 11 | 3100 ms |

## 3. settled header / idle 节奏

`idle` 不是把整条鲸鱼当成一个不可拆的帧播放器：tail、fin、heart、sleep、blink 是独立层。`composeWhaleGrid` 的绘制顺序为 **eyes → tail → fin → spout → heart → sleep**，调度最小延迟 16 ms；idle 时 spout 固定为层 0。

| 层 | 帧顺序与间隔 | 循环 / 调度 |
|---|---|---|
| tail | `tail1 → tail2 → tail3 → tail4 → tail3 → tail2 → tail1`；working 每步 240 ms，idle/asleep 每步 360 ms | working 连续循环；idle/asleep 单趟后回层 0，间隔 3000 ms 后再触发 |
| fin | `fin1 → fin2 → fin1`；working 与 idle/asleep 均每步 240 ms | working 连续循环；idle/asleep 单趟后回层 0，间隔 1920 ms 后再触发 |
| heart | 点击/heart 输入：`heart1 → heart2 → heart3`，每步 360 ms | 单趟不循环，回层 0；可叠在其他层上；再次触发从 `heart1` 重启并唤醒睡眠 |
| sleep | 无活动 10000 ms 后先显示层 0（`standard`）360 ms，再 `sleep1 → sleep2 → sleep3 → sleep4 → sleep5`，每步 360 ms | `sleep5 → sleep1` 循环，绝不绕回 `standard`；idle 时保持睡眠，working 或点击唤醒 |
| blink | `blink` 保持 120 ms | 独立叠加循环；working 间隔 1680 ms，idle 间隔 5040 ms |

层 0 与各数字层对应的完整帧名见 `frames.json` 的 `idle.layer_frame_maps`，因此 `fin1`、`fin2` 虽未出现在开屏序列中，也没有遗漏。

## 4. Gentle Mist Blue dark / light

`theme-mist.json` 按上游 `Theme` 声明顺序保存 81 个语义键。`darkTheme` 与 `lightTheme` 展开共享的 `MIST_BAR_RAMP` 后均为完整 81 色；不包含 `dark-ansi`、Sakura 或 Claude/Codex 品牌派生主题。

- `value`：上游运行时精确值，形如 `rgb(125,161,222)`；上游空字符串仍为空字符串。
- `hex`：上游 `rgb('#RRGGBB')` 的原始 hex；空色为 `null`。
- 每色 `sha256`：对 `value` 的 UTF-8 字节计算 SHA-256；空色即空字节串的 SHA-256。
- 每色另存 `source`、`source_expression`、`source_line`，共享 context bar 五色明确指向 `MIST_BAR_RAMP`，其余指向对应主题对象。
- `palette_sha256`：按 81 键声明顺序，将 `{key: value}` 编成 compact JSON 后计算 SHA-256。dark 为 `5aa866d5b3657a83e01b185d98312d787dc01be02cd5906a9996730b05e35721`，light 为 `0f3061d79b36085ce64156c592f51afd9a5bc2b144bee98156ab80aa5f7f3bcc`。

## 5. 共享首屏资产对照（只记录，未覆盖）

既有 [`whale_standard.txt`](../cmd/dshtui/assets/whale_standard.txt) 与本次上游 `standard` **逐字节一致**，没有差异格：

```text
legacy sha256   = 4b5c9ca7c886a331160d4b5bd584b55fe5c49ea294799132db1610961a6e5b4c
upstream sha256 = 4b5c9ca7c886a331160d4b5bd584b55fe5c49ea294799132db1610961a6e5b4c
matches_upstream = true
difference_coordinates = []
```

同一结果也记录在 `frames.json` 的 `legacy_whale_standard`。离线脚本会重新比较两个文件并核对差异坐标；即使未来出现差异，也应只更新记录，不能由资产提取覆盖共享首屏文件。

## 6. 离线验收与本次证据

在仓库根目录执行：

```bash
python cmd/dshtui/assets/verify_assets.py
```

脚本先钉住两个清单本身的 SHA-256，再独立检查：22 帧名称/顺序/尺寸/字符表/逐帧哈希、三段开屏每步与总时长、idle 各层节奏与循环、dark/light 各 81 色及逐色/整版哈希、共享首屏对照记录。成功输出：

```text
frames=22, size=25x40, opening=classic:18/3480ms heart:7/2750ms sleep:11/3100ms
idle=tail/fin/heart/sleep/blink cadence and loops pinned; theme=Gentle Mist Blue dark/light 81 colors each
OK=1288 FAIL=0
```

2026-11-04 本次实跑结果即 `OK=1288 FAIL=0`。另在系统临时目录的三个资产副本中分别破坏一个尾帧、一个开屏停留时间和一个 dark 颜色，均按预期退出 1：

```text
frame-mutation:   FAIL: tail1: frame sha256
cadence-mutation: FAIL: opening classic: frame order/dwell
color-mutation:   FAIL: dark: pinned palette sha256
MUTATIONS_CAUGHT=3 MUTATIONS_MISSED=0
```

两个交付清单自身的 SHA-256：

```text
frames.json     2c9b9e2a5f35a7d05596be68f41937059963ec2a76436c462f4e66de42426ca8
theme-mist.json 0f50d294641ebe7ec495a68d8e915bb0c1628798484261d40f46f4292b957f75
```
