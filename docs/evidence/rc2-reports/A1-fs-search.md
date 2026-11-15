# A1 · workspaceFiles.search（官方 0.2.2-rc.2 新增方法）实现报告

- 任务板：`task-1`（owner `a1-fs-search`）
- 写作用域（实际落盘 8 个文件，全部为新建，未改任何既有文件）：
  - `internal/fssearch/doc.go`、`search.go`、`ignore.go`（实现）
  - `internal/fssearch/search_test.go`、`ignore_test.go`（测试）
  - `internal/transport/handlers_workspace_files_search_rc2.go`（传输层）
  - `internal/transport/handlers_workspace_files_search_rc2_test.go`（HTTP 层测试）
  - `tmp/GO8-RC2/reports/A1-fs-search.md`（本报告）
- 版本号：未动（仍 `0.2.2-rc.2-gobeta.1`）；未 commit / 未 push。
- **取证环境说明（重要）**：本 worktree 是多路 RC2 任务共用的，取证期间
  `internal/session/subagent_catalog.go`（B3 的 untracked 新文件）处于半写状态，
  `go build ./...` 全量红；因此**红/绿证据取自 HEAD 上的隔离副本**
  （`.agents/worktrees/a1-isolated`，`git worktree add --detach 9896f19`，只拷我
  这 7 个文件，未动别人的文件），并在共享 worktree 恢复可编译后**复跑复验**
  （§6.4）。我没有修改也没有等待任何他人文件的修复。

---

## 0. 一句话结论

`workspaceFiles.search` 已按官方 rc.2 契约实现：新包 `internal/fssearch` 用纯 Go
（不依赖外部 `rg` 二进制）复刻官方「打包 ripgrep」的可观察语义；传输层新增
`POST /api/workspaceFiles.search`（L1：裸参数 + `{"result":…}` / 四字段错误）。
权威契约与实现证据逐条见 §1/§5，与官方 rg 的差异见 §4（9 条 diff + 3 条 wire 差异
+ 4 条未覆盖项），**没有一条是"静默不同"**。

---

## 1. 官方证据（只读解包树 `tmp/GO8-RC2/official-rc2/dsh/`）

包：`node_modules/@deepseek-ai/dsh-api-workspace-files`（rc.2 **新增**
`dsh-util-ripgrep` 依赖，rc.1 树 `tmp/GO8-RC2/official-rc1/` 里没有这个方法）。

| 证据 | 位置 | 内容 |
|---|---|---|
| 方法描述符 | `lib/typert.host.js:432` | `'workspaceFiles/search': { service:'workspaceFiles', invocation:{kind:'direct'}, parameters:[…], cancellation:{parameter:'signal'}, result:{…}, sourceLocation:{file:'packages/api/workspace-files/src/index.ts',line:359,column:9} }` |
| 参数 [0] | `lib/typert.host.js:440-455` | `workspaceFileScope`：`wire:'workspaceFileScopeId'`、`source:'lookup'`、`lookup:'workspaceFileScope'`、`jsonSchema:{"type":"string"}` —— **不进 payload args**，由 Session header 派生（`index.js:520-534` 注册的 lookup：`sessions.get(id).header.cwd ?? sandboxPolicy.workspaceRoot`） |
| 参数 [1] | `lib/typert.host.js:457-470` | `request`：`jsonSchema` = `{query:string, excludedPaths:string[], includeHidden:bool, respectGitignore:bool}`，`required` 四项全要；`additionalProperties:true`（非 strict）/ `false`（strict 侧） |
| 结果 | `lib/typert.host.js:483-497` | `{matches:[{path,type:enum[file,directory,other]}], truncated:boolean, incomplete:boolean}`，`required` 三项；描述："matches capped by search.maxResults; incomplete marks excess rg files or exit 2." |
| 服务入口 | `lib/index.js:652-695` | `async search(workspaceFileScope, request, signal)`：`request.query.trim()` → 空则 `RemoteError("gateway/bad-request","query must not be blank")`；`fs.resolve`+`stat` root → 缺失 `workspace-file/not-found`、非目录 `workspace-file/not-directory`；`get("subprocess")` 缺失 → `workspace-file/search-failed`；把 `this.config.search` 交给 `searchWorkspace` |
| 实现主体 | `lib/index.js:189-232` | `searchWorkspace()`：`rg --files --sort=path --null --path-separator=/` + 可选 `--hidden` / `--no-ignore` + 一组 `--glob=!…` |
| 字面排除 | `lib/index.js:174-177` | `function exclusion(path){ const literal = path.replace(/[\\*?\[\]{}!]/g,"\\$&"); return `--glob=!${path.includes("/")?"/":""}${literal}`; }` |
| VCS 恒排除 | `lib/index.js:165-172` | `VCS_NAMES = [".git",".svn",".hg",".bzr",".jj",".sl"]`，与 excludedPaths 一起转成 `--glob=!…` |
| 匹配与祖先推导 | `lib/index.js:196-220` | `needle = query.toLowerCase()`；`byPath = query.includes("/")`；`admit(path,type)`：`name = 末段`，`(byPath?path:name).toLowerCase().includes(needle)` 才入 map；每个列出文件先 `admit(path,"file")`，再从右往左逐层 `admit(祖先,"directory")` |
| 枚举上限 | `lib/index.js:203-213` | callback 里 `if (listed === spec.maxEntries) return false;`（**先停再 admit**），`listed += 1` |
| 排序 | `lib/index.js:227-229` | `sort((a,b) => a.path.split("/").length - b.path.split("/").length \|\| (a.path < b.path ? -1 : 1))` |
| 截断/不完整 | `lib/index.js:230-233` | `matches.slice(0,maxResults)`、`truncated = matches.length > maxResults`、`incomplete = stopped \|\| outcome.exitCode === 2` |
| 配置默认值 | `lib/index.js:503-511` | `maxResults=512`、`maxEntries=1e5`、`graceMs=3000`、`timeoutMs=30000`、`stderrMaxBytes=65536`（外层 `maxEntrities` 等属于 list/read） |
| 超时 | `lib/index.js:190-196` | `setTimeout(..., spec.timeoutMs)` → `RemoteError("workspace-file/search-failed","Workspace file search timed out; narrow the workspace or increase search.timeoutMs")` |
| 打包 rg 解析 | `dsh-util-ripgrep/lib/index.js:12-22` | `resolveRgPath()`：`<exe-dir>/<exe-name>-rg.exe`（pkg sidecar）→ 否则 `@vscode/ripgrep` 的 `rgPath`；Electron 把 `.asar` 换成 `.asar.unpacked`；**不查系统 rg** |
| rg 启动 | `dsh-util-ripgrep/lib/index.js:31-45` | 每次 `spawnRipgrep` 都前置 `--no-config`（环境配置不能启用 preprocessor） |
| 读行 | `dsh-util-ripgrep/lib/index.js:54-…` | UTF-8 逐行（这里 `--null` → NUL 分隔），回调 false 即 terminate |
| 契约文档（本仓） | `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:2331-2400` | 与上面 descriptor 一致（新增方法段） |

---

## 2. 本仓落点（文件:行）

| 文件 | 关键符号（行） |
|---|---|
| `internal/fssearch/doc.go` | 包文档（73 行）：契约来源、9 条与官方 rg 的差异、被官方钉住必须照做的点 |
| `internal/fssearch/search.go` | `Config`(:70)、`DefaultConfig`(:84)、`Result`(:107)、`Search`(:131)、`SearchWithConfig`(:136)、`validateRoot`(:205)、`literalExclusions`(:239)、`vcsNames`(:269)、`walker.walkDir`(:308)、`walker.admit`(:390)、`walker.result`(:427)、`lessUTF16`(:466) |
| `internal/fssearch/ignore.go` | `newIgnoreMatcher`(:57)、`ignoreMatcher.ignored`(:102)、`findRepoRoot`(:146)、`parseIgnoreLine`(:239)、`trimTrailingSpaces`(:275)、`matchSegments`(:313)、`decideRules`(:344) |
| `internal/transport/handlers_workspace_files_search_rc2.go` | `MethodWorkspaceFilesSearch`(:92)、`IsWorkspaceFilesSearchMethod`(:95)、`OfficialWorkspaceFileSearchResult`(:108)、`WorkspaceFilesSearchAPI`(:125)、`Attach/DetachWorkspaceFilesSearchAPI`(:138/:152)、`serveWorkspaceFilesSearchIfMatch`(:169)、`ServeWorkspaceFilesSearch`(:179)、`workspaceFilesSearchParams`(:223)、`workspaceFilesSearchFault`(:350)、`workspaceFilesSearchWire`(:383)、`validateWorkspaceRelativePath`(:415) |

---

## 3. 参数 / 结果逐字段对照

### 3.1 参数（wire 形状）

| 官方 | 本仓 L1 | 处理 |
|---|---|---|
| `[0] workspaceFileScope`（wire `workspaceFileScopeId`，lookup，header 派生，**不进 payload**） | body 顶层 `workspaceFileScopeId`（兼容 `sessionId` / `agentId`，`officialFileScope`） | 必填非空；空串 400。**只作为 lookup 传给实现，不塞进 search 请求**（`fssearch.Request` 里没有这个字段，类型上就塞不进去） |
| `[1] request.query`（required, string） | 嵌套 `request.query` 或顶层 `query` | required；`null` 拒（见 §5.6）；trim 后为空 → 官方码 `gateway/bad-request` |
| `request.excludedPaths`（required, string[]） | 同上 | required；`null` 拒、非数组拒、元素非字符串拒；空数组合法 |
| `request.includeHidden`（required, bool） | 同上 | required；`null`/非布尔拒 |
| `request.respectGitignore`（required, bool） | 同上 | required；`null`/非布尔拒 |
| strict 侧 `additionalProperties:false` | 顶层与 `request` 内的未知字段 → 400 | 嵌套与平铺同时给 → 400（"不猜哪个是真的"，与官方族既有约定一致） |

### 3.2 结果

| 官方字段 | 本仓 | 备注 |
|---|---|---|
| `matches[].path` | `OfficialWorkspaceFileSearchMatch.Path` | 出口守卫：非空、`/` 连接、不以 `/` 开头、不含 `\`/NUL/`..`/空段（`validateWorkspaceRelativePath`）；违反 → 500 `internal`，**不静默改写** |
| `matches[].type` | `.Type` | 只允许 `file`/`directory`/`other`；这条能力实际只产出前两种（见 §4 diff 7） |
| `truncated` | `.Truncated` | `len(matches 全集) > maxResults`；`truncated && len(matches)==0` 视为宿主自相矛盾（500） |
| `incomplete` | `.Incomplete` | `撞 maxEntries 提前终止` 或 `枚举期读目录失败`（官方 rg exit 2 的等价物） |
| `matches` 为空 | JSON `[]`（不是 null） | `make([]…,0,n)` 保证 |

---

## 4. 与官方 ripgrep 的逐条差异（全部登记，未静默）

| # | 官方 | 本仓 | 影响 |
|---|---|---|---|
| 1 | 打包 rg 子进程（`resolveRgPath` + `spawnRipgrep` + `--no-config`） | 纯 Go：`os.ReadDir` 递归 + 自带 gitignore 匹配器（红线 R6：宿主不引入外部工具进程做核心能力） | 无外部二进制依赖；`graceMs` / `stderrMaxBytes` 这两个**子进程参数无对应物**（不适用，不是缺失） |
| 2 | `--sort=path`：rg 输出全序路径后再匹配 | 目录 DFS + **目录排序键补 `/`**（`a.txt` < `a/x` < `a0`），输出即全局路径字节序 | `maxEntries` 截断点与官方一致，且能提前退出不必扫全树 |
| 3 | 结果排序用 JS `<`（UTF-16 code unit 序） | `lessUTF16`（:466）逐 UTF-16 code unit 比较 | 与官方完全一致，含补充平面字符（emoji）；不用 Go 字节序冒充 |
| 4 | `--path-separator=/` 输出 `/`，排序比平台原生拼写 | 枚举用 `filepath`（Windows 为 `\`），对外一律 `/`；枚举顺序按 `/` 连接拼写 | **已知边界**：Windows 上 `\`=0x5C vs `/`=0x2F，当名字里出现 `0x2F..0x5C` 之间的字符（`0-9 A-Z : ; < = > ? @ [ ] ^ _`）时，**文件数超 maxEntries 的截断边界**可能与官方不同；未超上限时结果集/顺序不受影响 |
| 5 | rg 读 `core.excludesFile` / `~/.config/git/ignore` | 默认**不读**；`Config.GitGlobalExcludesFile` 显式给路径时才读（语义与 rg 全局排除一致） | 同一工作区在不同机器/CI 上结果确定；需要时由宿主显式配置 |
| 6 | rg 不读 `core.ignorecase`（descriptor 明说） | 同样不读；忽略模式匹配**大小写敏感** | 一致 |
| 7 | 结果 `type` 枚举含 `other` | 只产出 `file`/`directory`：`rg --files` 只列普通文件，目录由祖先推导；符号链接/设备等非普通文件跳过 | `other` 在这条能力上**官方也永不产出**，保留常量只为 wire 校验枚举 |
| 8 | rg 对 `a/**` 的匹配 | 尾随 `**` 至少消费一段（`a/**` 不命中目录 `a` 本身） | 与 git 语义一致；避免"整棵剪枝"吞掉目录内 `!` 反选的极端场景 |
| 9 | 超时 → `workspace-file/search-failed`；rg exit 2 → `incomplete:true` | `ErrTimeout` → 同一个官方码（↓ 400）；读目录失败 → `incomplete=true` | 一致（不含子进程 spawn 失败这一类，因为无子进程） |

### 4.1 与官方 **wire** 的差异（L1 层，与方法语义无关）

1. 官方 typert 是**位置参数**（scope + request）；本仓 L1 是一条
   `POST /api/workspaceFiles.search` + **裸 JSON 参数对象**：scope 用 wire 名
   `workspaceFileScopeId`，`request` 可嵌套也可平铺。
2. 成功信封 `{"result": …}`（`writeJSON` 统一加）、错误四字段
   `code/message/hint/retryable`（官方码写在 `message` 前缀里，HTTP 状态按
   `officialWorkspaceFaultStatus` 映射表）。
3. L2（`remote.mux` + typert Connection envelope）未实现，因此**不声称**与本方法
   官方 wire 兼容（与仓内其它 200+ 官方方法同一口径）。

### 4.2 未覆盖项与原因

| 项 | 原因 |
|---|---|
| 全局 git 排除的默认自动发现（`~/.gitconfig` 的 `core.excludesFile`、`~/.config/git/ignore`、`$GIT_CONFIG_GLOBAL`） | 见 diff 5：读用户级配置会让同一工作区跨机器结果不同；改为显式 `Config.GitGlobalExcludesFile`。**不是静默跳过**：宿主不配置时行为等价于"没有全局排除"，与 rg 在该文件不存在时一致 |
| `search.stderrMaxBytes` / `search.graceMs` | 它们只约束子进程的 stderr 缓冲与终止宽限期；无子进程时无对应物 |
| 不可读目录 → `incomplete` 的真机测试 | Windows 上 `chmod 0` 不阻止 `ReadDir`，构造真实不可读目录需要 ACL（`icacls`），属破坏性环境操作；代码路径存在（`walker.walkDir` 里 `os.ReadDir` 失败置 `incomplete`），本报告列为**未验证项**而不是"已通过" |
| 符号链接不被跟随的真机覆盖 | 有测试，但 Windows 建符号链接需要 Developer Mode/管理员，建不成时 `t.Skip`（本机当前跳过） |
| rg 的 `--sort=path` 平台原生排序边界（diff 4） | 需要官方打包 rg 二进制做对照（本解包树里 `@vscode/ripgrep` 未随附，`resolveRgPath` 也无 sidecar），无法在本任务内实验验证；已在 §4 写成**已知边界** |
| `OfficialWorkspaceMethods()` 快照里出现本方法 | 那个函数在 `internal/transport/handlers_workspace_official.go`（**别人的写作用域**）。集成时若要让快照/门禁看到 18 个方法，需要那位 owner 或 Lead 加一行 `MethodWorkspaceFilesSearch`——我这侧不改 |

---

## 5. 语义逐条对照（官方行 → 本仓行为 → 测试）

1. **trim 后空 query 拒绝**（`index.js:663`）→ `trimQuery` 复刻 JS `String.prototype.trim()`
   （含 `\u3000`、`\uFEFF`、`U+2000..U+200A`）→ 空则 `ErrBlankQuery` →
   transport 映射官方码 `gateway/bad-request` → 400。
   测试：`TestSearchBlankQueryRefused`（6 种空白）、`TestSearchQueryIsTrimmed`、
   `TestWorkspaceFilesSearchBlankQueryRefused`（HTTP 层，含 `\u3000`/`\uFEFF`）。
2. **大小写不敏感子串**（`:196,200-203`）→ `strings.ToLower` 后 `Contains`。
   测试：`TestSearchCaseInsensitiveSubstring`（`ALPHA_DEEP` / `Alpha.txt` / `sub/ALPHA.md`）。
3. **名称 vs 路径**（`:197`）→ `byPath = strings.Contains(query,"/")`；非 byPath 只取
   末段。测试：`TestSearchNameVersusPath`（`sub/foo` 只命中两个相对路径；
   `fsub.txt` 命中而 `deep/er/other/x.txt` 不命中）。
4. **祖先目录推导**（`:206-210`）→ 每个列出文件的每一层祖先都 `admit(dir)`；只有自身
   命中 query 的目录入结果，空目录永不出现。
   测试：`TestSearchAncestorDirectoriesMatchByName`、`TestSearchEmptyDirectoryAbsent`。
5. **excludedPaths 字面量**（`:174-177`）→ `literalExclusions`：含 `/` 精确路径、
   不含 `/` 任意深度同名；**不解释 glob 元字符**。
   测试：`TestSearchExcludedPathsAreLiteral`（5 组：任意深度、精确路径、`a[b].txt`
   不当字符类、`*` 不排除任何东西、`!bang.txt` 是真名、`/build/` 仍走精确路径分支）。
6. `null` 不能冒充默认值（官方 zod 不接受 null，而 Go 的 `json.Unmarshal(null,&s)` 不报错）
   → `takeNonNull` / `requiredString` / `requiredBool`（handler :244-300）。
   测试：`TestWorkspaceFilesSearchRequestValidation/query_是_null`。
7. **隐藏项**（`:209` `--hidden`）→ `.` 前缀且 `!IncludeHidden` 则跳过。
   测试：`TestSearchHiddenSwitch`。
8. **VCS 恒排除**（`:165-172`）→ `vcsNames` 在 hidden/ignore 之前判，两个开关都管不着。
   测试：`TestSearchVCSAlwaysExcluded`（6 个名字）与 `TestSearchHiddenSwitch` 的 `.git` 断言。
9. **respectGitignore**（`:209` `--no-ignore`）→ `.ignore` / `.gitignore`（需仓库）/
   `.git/info/exclude` / 父级 `.gitignore`；false 时全部失效但 hidden 仍单独判。
   测试：`TestSearchRespectGitignoreSwitch`、`TestSearchGitignoreRequiresGitRepo`、
   `TestSearchGitInfoExclude`、`TestSearchParentGitignore`、`TestSearchGitdirFileRepo`、
   `TestSkipIgnoredDirectoryPrunesChildren`、`TestSearchGlobalGitExcludesFile`、
   `TestIgnoreRuleMatching`（24 条规则表：锚定/基名/`**` 三种/`?`/字符类/大小写/注释/转义 `#`/尾空格）、
   `TestIgnoreLastMatchWins`。
10. **maxEntries 先停再 admit**（`:204`）→ `walker.walkDir` 里 `listed == MaxEntries`
    时**先** `stopped=true` 再返回，不 admit 第 N+1 个。
    测试：`TestSearchIncompleteFromEntryCap`（3 文件 / 上限 2 → incomplete 且只入 2 条；
    上限 3 → 不 incomplete）。
11. **结果排序深→路径**（`:227-229`）→ `walker.result` 按 `pathDepth` 再 `lessUTF16`。
    测试：`TestSearchSortDepthThenPath`（`a.txt`、`alpha/q.txt`、`b/z2.txt`、`z/y.txt`、`b/a/z.txt`）。
12. **截断**（`:230-231`）→ `truncated = 全集 > MaxResults` 后切片。
    测试：`TestSearchTruncated`（2/3 两档边界）。
13. **incomplete 口径**（`:232`）→ `stopped || 枚举读失败`。
    测试：`TestSearchIncompleteFromEntryCap` + `TestSearchTruncated` 的反向断言
    （不超限时 incomplete=false）。
14. **root 校验**（`:670-679`）→ 缺失 `ErrRootNotFound`（官方 `workspace-file/not-found`
    → 404）、非目录 `ErrRootNotDirectory`（`workspace-file/not-directory` → 400）；
    另外本包对路径形状收紧：必须绝对、不含 `..` 元素（`ErrInvalidRoot` →
    `workspace-file/outside-workspace` → 400）。
    测试：`TestSearchRootValidation`（相对/`..`/空/不存在/是文件/尾部分隔符可接受）、
    `TestWorkspaceFilesSearchRootFaults`（HTTP 层四条）。
15. **取消**（`:232` 前一次 `throwIfAborted`）→ ctx 取消后即使实现返回了结果也不当成功。
    测试：`TestSearchCancellation`（fssearch）、`TestWorkspaceFilesSearchCancellation`（499 + `canceled`）。
16. **超时**（`:190-196`）→ `Config.Timeout`（默认 30s）→ `ErrTimeout` →
    `workspace-file/search-failed` → 400。
    测试：`TestSearchTimeout`。

---

## 6. 测试红 → 绿记录

命令统一带 `export PATH="/c/Program Files/Go/bin:$PATH"`，全部用 `timeout`。

### 6.1 先红（实现文件不存在时）

```
$ go test ./internal/fssearch/...
# dshgo/internal/fssearch [dshgo/internal/fssearch.test]
internal\fssearch\ignore_test.go:44:47: undefined: Request
internal\fssearch\ignore_test.go:44:56: undefined: Result
internal\fssearch\ignore_test.go:46:14: undefined: Search
...
internal\fssearch\search_test.go:43:20: undefined: Result
FAIL	dshgo/internal/fssearch [build failed]
EXIT=1

$ go test ./internal/transport/ -run 'WorkspaceFilesSearch'
dshgo/internal/fssearch: no non-test Go files in ...\internal\fssearch
FAIL	dshgo/internal/transport [build failed]
EXIT=1
```

### 6.2 实现过程中的红点（都是实现 bug，被测试抓住后修掉）

| 红点 | 根因 | 修复 |
|---|---|---|
| `ignore_test` 9 条失败（"应当命中（decided=false）"） | 测试表断言写死在"必须命中"上，未区分"不命中"行 | 断言改为 `decided != c.matched` 比较 |
| `TestSearchSortDepthThenPath` 多出 `alpha/q.txt` | 我算错了期望（`q.txt` 含 `t`） | 期望补上并按 depth2 正确排序 |
| `TestSearchAncestorDirectoriesMatchByName` 少 2 条 | 期望里混入了"文件名不含 query"的文件 | 期望改为 4 条，并注释说明"只比条目名" |
| `TestSearchRootValidation` 未报 `ErrInvalidRoot` | 测试用 `filepath.Join(ws,"..","ws")`，Join **清理掉了** `..` | 学手工拼未清理路径（代码不改：代码本身的校验是对的） |
| `TestSearchTimeout` 未超时 | Windows 时钟 tick 粗于 1ns，首检落在同一 tick | 测试先建 360 个文件再跑 1ns 超时（代码不改） |
| `TestWorkspaceFilesSearchRequestValidation/query_是_null` 变 404 | `json.Unmarshal("null",&s)` 不报错 → 空串被当合法 | handler 加 `takeNonNull`/`requiredString`/`requiredBool`，`null` 一律 400 |
| `TestWorkspaceFilesSearchBlankQueryRefused` 报"非法 JSON" | 测试把 TAB 直接拼进 JSON 字符串 | 测试改用 `json.Marshal` 生成 body |
| `TestWorkspaceFilesSearchRootFaults` 变 404 | 同 `filepath.Join` 清理 `..` | 测试手工拼未清理路径 |
| `TestWorkspaceFilesSearchRejectsHostContractViolation` `C:/abs/x.txt` 通过 | 出口守卫只看前导 `/` 与 `\` | 加 `filepath.IsAbs` + `looksLikeWindowsAbsPath`（`C:` / `\\` / `//`），跨平台都拒 |

### 6.3 绿（隔离副本 HEAD `9896f19` + 我的 7 个文件）

```
$ go build ./...            → BUILD_EXIT=0
$ go vet ./...              → VET_EXIT=0
$ go test ./internal/fssearch/... -count=1 -v
ok  dshgo/internal/fssearch   6.590s
  === RUN  51   --- PASS 51   --- FAIL 0        （27 个顶层 + 24 条 ignore 规则表子测试）

$ go test ./internal/transport/ -run 'WorkspaceFilesSearch' -count=1 -v
ok  dshgo/internal/transport  0.354s
  === RUN  33   --- PASS 33   --- FAIL 0        （14 个顶层 + 19 条校验表子测试）

$ go test ./internal/transport/ -count=1        # 整个 transport 包回归
ok  dshgo/internal/transport  5.162s            （没有既有 handler 被带红）
```

### 6.4 共享 worktree 复验（B3 修好 `internal/session` 之后）

```
$ go vet ./internal/fssearch ./internal/transport     → EXIT=0
$ go test ./internal/fssearch/... -count=1            → ok  dshgo/internal/fssearch  3.712s
$ go test ./internal/transport/ -run 'WorkspaceFilesSearch' -count=1
                                                      → ok  dshgo/internal/transport 0.314s

# 任务书验收命令（共享 worktree，全仓口径）
$ go build ./...                                      → SHARED_BUILD_EXIT=0
$ go vet ./...                                        → SHARED_VET_EXIT=0
$ go test ./internal/fssearch/... ./internal/transport/ -run 'Search' -count=1
ok   dshgo/internal/fssearch   4.356s
ok   dshgo/internal/transport  0.341s                  → ACCEPT_EXIT=0
```

### 6.5 接线片段编译验证（隔离副本，已在验证后删除）

把 §7.1 的三行路由钩子 + §7.2 的挂接片段（含 `context` / `dshgo/internal/fssearch`
两个 import）实际打进隔离副本的 `internal/transport/server.go` 与
`cmd/dshd/serve_workspace_api.go`：

```
$ go build ./...                  → WIRE_BUILD_EXIT=0
$ go vet ./cmd/dshd/ ./internal/transport/
                                  → WIRE_VET_EXIT=0
```

也就是说 §7 的两段可以**原样粘贴**，不需要你再改签名或补别的东西。

---

## 7. 接线行（Lead 可直接粘贴）

### 7.1 传输层路由（`internal/transport/server.go`，当前 `:505` agent、`:511` native、`:515` workspace 那一段）

在 `if IsOfficialWorkspaceMethod(name) {`（`:515`）**之前**插三行：

```go
	if s.serveWorkspaceFilesSearchIfMatch(w, r, name) {
		return
	}
```

（`IsOfficialWorkspaceMethod` 本身认领不到本方法，所以必须在它之前或之后单独分派。）

### 7.2 宿主挂接（推荐：0 处改 `serve.go`）

`cmd/dshd/serve_workspace_api.go:1563` 的 `attachOfficialWorkspace` 里，紧跟
`serve_workspace_api.go:1568` 的 `srv.AttachOfficialWorkspaceAPI(host.workspaceAPI())`
之后加：

```go
	srv.AttachWorkspaceFilesSearchAPI(transport.WorkspaceFilesSearchAPI{
		Search: func(ctx context.Context, sessionID string, req fssearch.Request) (fssearch.Result, error) {
			root, err := host.workspaceRootForSession(sessionID) // serve_workspace_api.go:1062
			if err != nil {
				return fssearch.Result{}, err
			}
			return fssearch.Search(ctx, root, req)
		},
	})
```

`cmd/dshd/serve_workspace_api.go` 的 import 块需要补两个（当前该文件 **没有** `context`，
也没有 `fssearch`；`transport` 已有）：

```go
	"context"

	"dshgo/internal/fssearch"
```

**所需依赖（一句话）**：`transport.WorkspaceFilesSearchAPI{ Search: func(ctx context.Context,
workspaceFileScopeID string, req fssearch.Request) (fssearch.Result, error) }`；
宿主侧唯一依赖是"会话 → 工作区根"，直接复用 `workspaceRootForSession`
（与 workspaceFiles.list/read/stat 同一条判据，不新增路径语义）。

### 7.3 选择性项（你的写作用域）

若要 `OfficialWorkspaceMethods()` 快照/门禁里出现第 18 个方法，在
`internal/transport/handlers_workspace_official.go` 的 `OfficialWorkspaceMethods()` 与
`IsOfficialWorkspaceMethod` 里各加 `MethodWorkspaceFilesSearch`（**我没动那个文件**）。

---

## 8. 自查

1. **挂接形式**：`(*transport.Server).AttachWorkspaceFilesSearchAPI(WorkspaceFilesSearchAPI)` +
   `(*transport.Server).serveWorkspaceFilesSearchIfMatch(w, r, name) bool`（§7.1/§7.2 可直接照抄）。
   纯函数入口 `transport.ServeWorkspaceFilesSearch(w, r, name, api)` 供测试直接打。
2. **他人文件改动 = 0**：`git status` 里属于我的只有 `internal/fssearch/`（5 个新文件）与
   `internal/transport/handlers_workspace_files_search_rc2.go(_test.go)`（2 个新文件），
   外加本报告；`cmd/dshd/serve.go`、`AGENTS.md`、`CHANGELOG.md`、`docs/**`、
   `internal/transport/handlers_workspace_official.go`、`internal/session/**` **一行未动**。
3. **接线片段已编译验证**（§6.5）；`cmd/dshd/serve.go` 本身**零改动**（挂接点在
   `serve_workspace_api.go:1563` 的 `attachOfficialWorkspace` 里）。
4. **并发写入背景**：本 worktree 同时有 10 路 RC2 任务在写（`internal/transport/handlers_{agent_team,forge,
   plugin_update,web_providers}_rc2*.go`、`internal/session/subagent_catalog.go` 等 untracked 新文件）。
   取证期间 `internal/session/subagent_catalog.go:112` 半写导致 `go build ./...` 红，
   我**没有**修改/回退/等待任何他人文件，改用隔离副本取证（§开头的说明），B3 修好后已在本
   worktree 复验通过（§6.4）。
5. **未验证项**：见 §4.2（不可读目录、符号链接真机、rg 原生排序边界、全局排除自动发现）。
   这些不是"通过"，按仓规登记为 known gap，没有静默放过。

---

## 9. 验收命令（可直接跑）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./...
go vet ./...
go test ./internal/fssearch/... -count=1 -v
go test ./internal/transport/ -run 'WorkspaceFilesSearch' -count=1 -v
# 接完线之后的 HTTP 快速确认（需先 /connect 换 cookie）：
# curl -s -X POST http://127.0.0.1:<port>/api/workspaceFiles.search \
#   -H 'Content-Type: application/json' \
#   -d '{"workspaceFileScopeId":"<sessionId>","request":{"query":"main","excludedPaths":[],"includeHidden":false,"respectGitignore":true}}'
```

预期：`matches` 为 `{path,type}` 数组、`truncated`/`incomplete` 布尔；空 query 400 +
`gateway/bad-request`；未接线时 501 + `not_implemented`（点名 `workspaceFiles.search`）。
