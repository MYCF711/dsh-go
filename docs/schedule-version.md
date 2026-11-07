# Schedule 整单元 JSON 版本兼容

本页只定义 `<home>/state/schedules/<session>.json` 的版本规则。实现位于
`internal/schedule` 与 `modules/tool/schedule`；没有引入通用存储框架，也不改变会话 v4、
Transport、goal 或 SQLite 的任何语义。

## 当前形状

```json
{
  "version": 1,
  "schedules": [
    {
      "id": "sch-0001-3f9a",
      "title": "每天早报",
      "prompt": "汇总昨天的提交",
      "kind": "daily",
      "daily_at": "09:30",
      "next_at": "2026-11-05T09:30:00+08:00"
    }
  ]
}
```

`currentScheduleVersion = 1`。到期推进层持有当前值和兼容版本表，登记工具通过
`CurrentScheduleVersion` 与 `ValidateScheduleVersion` 复用它们，避免两份白名单漂移。
当前读取白名单只有 `{1}`；历史 legacy 是**字段缺失**，不是 `version: 0`。

## 兼容版本表

| 文件形态 | 读取规则 | 写回规则 |
|---|---|---|
| 文件不存在 | 返回空任务表 | 下一次真实 `save` 写 `version: 1` |
| 无 `version`、含历史 `schedules` 形状 | 按 **legacy** 读取 | 读取本身不改文件；下一次真实 `save` / `Advance` 写 `version: 1` |
| `version: 1` | 当前版本，可读 | 永远写 `version: 1` |
| `version: 0`、负数、`999` 等未列入白名单的整数 | **fail-loud**，返回错误 | 不进入写回，不覆盖原文件 |
| `version: null`、字符串、浮点数等 | **fail-loud**；只有字段缺失才是 legacy | 不进入写回，不覆盖原文件 |
| 空白、截断或其他非法 JSON | 返回解析错误，不当作空任务表 | 不进入写回，不覆盖原文件 |

新增版本时，必须先实现该版本的明确读取规则并增加 fixture，才能把它加入
`compatibleScheduleVersions`。仅让 `json.Unmarshal` 能忽略未知字段，不等于声明兼容。

## Legacy 字段与权限默认值

无版本文件按现有 `schedules` 字段直接解码，不伪造一次额外磁盘迁移。旧条目缺少
`allow_writes` 时，Go 布尔零值仍为 `false`，即只读；升级为版本 1 不会把它改成 `true`。
显式登记的 `allow_writes: true` 仍按原值保留。

只有文件不存在的“第一次使用”会返回空表。空白文件不是 legacy：把它当成空表会让随后的
create/update/delete 静默覆盖一份需要人工检查的状态文件。

## 写路径

- `modules/tool/schedule` 的 create/update/delete 都先 `load`，成功后才能 `save`；`save`
  固定序列化 `version: 1`，继续沿用 `.tmp` + `rename`。
- `internal/schedule.Advance` 同样先 `Load`，成功后才能推进并写回；写回固定携带
  `version: 1`。
- 因此，未知/未来版本或损坏 JSON 在读阶段就失败，不会被后续写路径重置。
- 版本校验只针对 schedule 整单元 JSON；不得把 `codecs/v4` 的 header 白名单或 SQLite
  迁移规则套到这里。

## 测试与未验项

新增 fixture 覆盖：无版本 legacy、当前版本 1、未知版本 0、未来版本 999、损坏/空白 JSON、
legacy 写回当前版本、旧数据缺少 `allow_writes` 仍为 `false`，以及 create/Advance 拒绝后
原文件逐字节不变。登记工具已有的 `allow_writes: true` 持久化测试继续保留。

定向验收命令：

```bash
go test ./internal/schedule ./modules/tool/schedule ./modules/tool/goal
```

本次上述定向命令 exit 0；全仓 `go test ./...`、`go vet ./...`、`go build ./...` 也均 exit 0。

仍未验证：真实用户 home 中既有 schedule 文件的人工备份与迁移、真实宿主长进程中的并发写、
`-race`、跨仓 UI/Transport 消费，以及任何 v4/SQLite 行为。本改动只证明上述 JSON 文件在
单测中的版本读取与写回契约，不外推为这些链路已经完成产品验收。
