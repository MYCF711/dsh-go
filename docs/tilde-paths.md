# 本地文件工具的 tilde 路径

> 范围：`modules/fs/local` 的路径解析。基线为 `af4a49a` + 共享导出；不改变 Transport、工具审批或其他权限模型。

## 解析规则

本地 FS 只展开两种前导形式：

| 输入形式 | 解析结果 |
| --- | --- |
| `~` | 宿主 home 根目录 |
| `~/...` | 宿主 home 下的子路径 |
| `~\...` | 与 `~/...` 相同；Windows 分隔符先归一化 |
| `~other`、`~other/...` | 不查询其他用户，仍按工作区相对路径处理 |
| `dir/~/file`、`file~name` | 路径中间的 `~` 不展开，仍按工作区相对路径处理 |
| 普通绝对路径 | 保持原规则：只允许工作区根或已配置的 extra 根 |

home 缺省由 `os.UserHomeDir` 提供，只有真正解析显式 tilde 路径时才查询。`local.WithHome(dir)` 可注入 home，供嵌入方和测试使用；返回路径统一清理为正斜杠绝对路径。

`Resolve` 只做路径归一化和边界检查，不要求目标已经存在。因此不存在的 `~/new.txt` 可以解析；`Read` 仍报告不存在，`Stat` 仍返回 `exists=false`。但 `Write`/`CheckWritable` 对工作区外的目标直接拒绝，不会创建文件。

## 安全边界

- **home 通道只由显式 tilde 触发，且只读。** 当 home 不在工作区根或 extra 根内时，`~/file` 可以经该受限通道解析并读取，但指向同一文件的普通绝对路径仍会被拒绝；`CheckWritable`/`Write` 只允许工作区根内的目标，`~` 和 extra 都不能成为写通道。
- **tilde 路径不能借 `..` 离开 home。** 清理后的目标必须是 home 本身或其子路径；即使目标落在另一个本来允许的 root/extra 中，也不能通过 `~/../...` 切换过去。
- **目录边界按完整路径段判断。** `home-evil` 不会因为与 `home` 有共同字符串前缀而被视为 home 内；root/extra 的原有判断不变。
- **读取与写入边界分开。** `Read`、`Stat` 共用 `Resolve`；`CheckWritable`、`Write` 追加 `withinWrite`，只认工作区根，不认 extra 或 home；读后写、过期重读和 Caps/审批仍按原契约执行。
- **测试不接触真实用户 home。** 同包测试通过 `WithHome` 注入 `t.TempDir()` 下的独立 home，覆盖 `/` 与 `\`、不存在目标、越界、root/extra、共同前缀和普通绝对路径行为。
