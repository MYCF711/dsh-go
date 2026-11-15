// Package fssearch 实现官方 0.2.2-rc.2 新增方法 `workspaceFiles.search` 的
// 文件发现语义：在工作区内按「大小写不敏感子串」找文件，并把命中文件的祖先
// 目录一起作为可命中的条目，结果按「先深度、再工作区相对路径」排序，受
// `search.maxResults` 截断；枚举受 `search.maxEntries` 限制，超限时报告
// `incomplete`。
//
// # 契约来源（官方 0.2.2-rc.2 解包树，只读取证）
//
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/typert.host.js:443`
//     —— `'workspaceFiles/search'` 描述符：参数 [1] request 的 jsonSchema
//     `{query, excludedPaths, includeHidden, respectGitignore}` 四项 required；
//     结果 `{matches:[{path,type}], truncated, incomplete}`。
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:189-232`
//     —— `searchWorkspace()`：`rg --files --sort=path --null --path-separator=/`，
//     仅列出文件；匹配与祖先目录推导都在 JS 侧（`admit`，:201-220）；
//     `entries` 排序 :227-229；`matches.slice(0, maxResults)` 与
//     `truncated: matches.length > maxResults`、`incomplete: stopped || exitCode===2`。
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:185-188`
//     —— `exclusion()`：把 excludedPaths 逐字符转义成 rg 字面 glob；
//     `VCS_NAMES`（:171-173）恒排除。
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:652-695`
//     —— `search()`：`request.query.trim()` 后为空 → `gateway/bad-request`；
//     root 不存在 → `workspace-file/not-found`；不是目录 → `workspace-file/not-directory`。
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:498-511`
//     —— 配置默认值：`search.maxResults=512`、`search.maxEntries=1e5`、
//     `search.graceMs=3000`、`search.timeoutMs=30000`。
//   - `node_modules/@deepseek-ai/dsh-util-ripgrep/lib/index.js` —— 官方用打包的
//     rg 子进程（`spawnRipgrep` 固定带 `--no-config`），不读系统 rg。
//
// # 本包与官方 ripgrep 的逐条差异（不静默，全部登记）
//
//  1. **不依赖外部 rg 二进制**（本仓红线 R6：宿主不引入外部工具进程做核心能力）。
//     枚举改用 Go 的目录遍历 + 自带 gitignore 匹配器，语义按 rg 的可观察行为对齐。
//  2. **枚举顺序**：官方 `--sort=path` 让 rg 输出全序路径；本包用「目录 DFS +
//     目录键补一个 `/`」复现同一个全序（`a.txt` 在 `a/x` 之前、`a0` 在 `a/x` 之后），
//     因此 `maxEntries` 的截断点与官方一致，且能提前退出不必扫全树。
//  3. **排序比较器**：官方结果排序用 JS 的 `<`（UTF-16 code unit 序）；本包按
//     UTF-16 code unit 逐单元比较（`lessUTF16`），对 ASCII 与 BMP 路径完全一致，
//     对补充平面字符也与 JS 一致，不用 Go 的字节序冒充。
//  4. **跨平台路径分隔**：官方用 `--path-separator=/` 输出 `/` 连接路径；本包在
//     枚举时用 `filepath`（Windows 上是 `\`）但对外一律输出 `/` 连接路径。
//     **已知差异**：rg 的 `--sort=path` 排的是平台原生拼写，Windows 上 `\` 的
//     字节值是 0x5C 而 `/` 是 0x2F，所以当名字里出现 `0x2F..0x5C` 之间的字符
//     （`0-9 A-Z : ; < = > ? @ [ ] ^ _`）时，**文件数超过 maxEntries 的截断边界**
//     可能与官方不同。未超上限时结果集与顺序不受影响。
//  5. **全局 git 排除默认不读**：官方 rg 会读 `core.excludesFile` /
//     `~/.config/git/ignore`。本包默认不读（避免同一工作区在不同机器/CI 上结果
//     不同，也避免读用户级配置），需要时由 `Config.GitGlobalExcludesFile`
//     显式给文件路径，行为与 rg 的全局排除一致。
//  6. **`core.ignorecase` 不读**：与官方一致（descriptor 明说 "The service does
//     not read `core.ignorecase`"），忽略模式匹配始终大小写敏感。
//  7. **`matches[].type` 只会是 `file` / `directory`**：官方枚举里的 `other`
//     在这条能力上永不产出——`rg --files` 只列普通文件，目录由祖先推导。本包对
//     非普通文件（符号链接、设备等）同样跳过，与官方 "symlinks ... absent" 一致。
//  8. **`a/**` 的边界**：git 里 `a/**` 只匹配 `a` 内部、不匹配目录 `a` 自身；
//     本包按同一语义实现（尾随 `**` 至少消费一段），因此不会因为忽略整棵
//     子树而丢掉"目录内用 `!` 反选"的场景。
//  9. **超时与崩溃口径**：官方超时抛 `workspace-file/search-failed`（不是
//     incomplete）；本包超时返回 `ErrTimeout`，由 transport 层翻成同一个官方码。
//     枚举中遇到不可读目录时官方是 rg 退出码 2（部分发现）→ `incomplete:true`；
//     本包同样标 `incomplete`，不静默当完整结果。
//
// # 已被官方源码钉住、本包照做的点
//
//   - query 去空白后为空一律拒绝（`ErrBlankQuery`），绝不返回空结果冒充成功。
//   - 不含 `/` 的 query 只匹配**条目名**；含 `/` 的匹配 `/` 连接的相对路径。
//   - excludedPaths 是**字面量**：含 `/` 精确路径、不含 `/` 任意深度同名；
//     名字里的 glob 元字符不当模式（`a[b].txt` 只排除那个真名文件）。
//   - 隐藏项（`.` 前缀）默认不列；`includeHidden` 打开后列出。
//   - VCS 元数据（`.git .svn .hg .bzr .jj .sl`）恒排除，与两个开关无关。
//   - 空目录不出现（目录只由已列出的文件推导）。
//   - 祖先目录不随命中文件一起返回：只有自身命中 query 的目录才入结果。
package fssearch
