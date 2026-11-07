# 另一会话 `/view` 现场指针（供 W127 核对）

来源：Lead 在 2026-11-05 读取同工作区 `session-191da5ac-bc07-489d-a219-4aa5c87379a1` 时，assistant seq≈926 的阶段性归因曾写：

- `/trace`：宿主没 trajectory.export / save 的真实写文件能力；
- `/view`：宿主没 view edit 能力，归因称“官方要求 view edit（`edit_file`）”。

但当前可读取的会话日志已经过后续追加/压缩，原始 `/view` 命令行、完整报错和截图未再出现；本地 dsh-TUI 0.18.0 与官方依赖中也未找到 `/view` slash 注册。`str_replace_editor` 的 `view` 是工具子命令，不等于 slash `/view`。

因此这条只能作为**待证实线索**，不能当官方命令规格或实现依据。W127 应在最终文档里明确：未找到官方 `/view` 注册；没有原始命令/报错/官方 file:line 前，不立项写新编辑器或新 Transport 方法。若另一会话后续提供现场，再重新核对。
