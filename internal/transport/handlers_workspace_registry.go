package transport

// PLAY-WORKSPACE-HOST：workspace registry 的**宿主扩展**动词。
//
// 与官方 193 个方法的关系：这 5 个名字**不在**官方方法表里，是宿主为了把
// TUI2Go 的 workspace provider（listWorkspaces / listWorkspaceRegistry /
// resolveWorkspace / switchWorkspace / removeWorkspace / workspaceCommands）
// 接上真实 registry 而新增的能力。它们走的是**同一套**官方 workspace dispatch
// （`IsOfficialWorkspaceMethod` → `serveOfficialWorkspaceAPI`），因此四字段错误、
// 参数校验、认证与官方族完全一致；**只多不少**，不改任何官方方法的形状。
//
//	workspace.registry  列出 registry（Order 是唯一顺序真相）
//	workspace.resolve   uri / path / id / title → registry 条目
//	workspace.switch    切换当前工作区（持久化 defaultWorkspaceId）
//	workspace.remove    只摘注册、不删文件（区别于 workspace.delete）
//	workspace.commands  本宿主真实支持的 workspace 命令及可用性

// 宿主扩展方法名。用 Method 强制转换而不是在 api.go 里加常量：api.go 是
// 冻结路由表（本任务的写作用域之外），扩展名在这里定义即可被 dispatch 认到。
const (
	MethodWorkspaceRegistry Method = "workspace.registry"
	MethodWorkspaceResolve  Method = "workspace.resolve"
	MethodWorkspaceSwitch   Method = "workspace.switch"
	MethodWorkspaceRemove   Method = "workspace.remove"
	MethodWorkspaceCommands Method = "workspace.commands"
)

// OfficialWorkspaceRegistry 是 `workspace.registry` 的结果。
//
// `Order` 与 `Workspaces` 一起给出：前者是唯一顺序真相，后者按 order 排好，
// 客户端不需要自己再排一次（也不允许自己造顺序）。
type OfficialWorkspaceRegistry struct {
	Version            int                 `json:"version"`
	DefaultWorkspaceID string              `json:"defaultWorkspaceId,omitempty"`
	Order              []string            `json:"order"`
	Workspaces         []OfficialWorkspace `json:"workspaces"`
	ArchivedSessionIDs []string            `json:"archivedSessionIds"`
	PinnedSessionIDs   []string            `json:"pinnedSessionIds"`
	RegistryPath       string              `json:"registryPath,omitempty"`
}

// OfficialWorkspaceSwitchValue 是 `workspace.switch` 的结果。
type OfficialWorkspaceSwitchValue struct {
	Workspace           OfficialWorkspace `json:"workspace"`
	PreviousWorkspaceID string            `json:"previousWorkspaceId,omitempty"`
}

// OfficialWorkspaceRemoveValue 是 `workspace.remove` 的结果。
//
// `Removed` 表示这次真的从 registry 摘掉了；文件**没有**被删（要删文件走
// 官方的 `workspace.delete`，两者语义不同）。
type OfficialWorkspaceRemoveValue struct {
	Removed            bool   `json:"removed"`
	WorkspaceID        string `json:"workspaceId"`
	DefaultWorkspaceID string `json:"defaultWorkspaceId,omitempty"`
}

// OfficialWorkspaceCommand 是一条 workspace 命令的真实可用性。
type OfficialWorkspaceCommand struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// OfficialWorkspaceCommandList 是 `workspace.commands` 的结果。
type OfficialWorkspaceCommandList struct {
	Commands []OfficialWorkspaceCommand `json:"commands"`
}

// workspaceRefAliases 是 resolve/switch/remove 共同接受的引用名。
//
// `uri` 既有官方词汇（workspace://<id> / file:///…），也接受裸 id、绝对路径
// 与标题——TUI 的 provider 就是这么传的，不该逼调用方先猜类型。
var workspaceRefAliases = []string{"uri", "path", "workspaceId", "title", "ref"}

func officialWorkspaceRegistryMethod(api OfficialWorkspaceAPI, p *officialParams) (any, error) {
	// 参数表为空：出现任何字段都是违约（与 initializeDefault 一致）。
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Registry == nil {
		return nil, officialNotImplemented("workspace.registry 未接线：宿主没有工作区注册表")
	}
	reg, err := api.Registry()
	if err != nil {
		return nil, err
	}
	// 出口不接受 null：空 registry 给空数组。
	if reg.Order == nil {
		reg.Order = []string{}
	}
	if reg.Workspaces == nil {
		reg.Workspaces = []OfficialWorkspace{}
	}
	if reg.ArchivedSessionIDs == nil {
		reg.ArchivedSessionIDs = []string{}
	}
	if reg.PinnedSessionIDs == nil {
		reg.PinnedSessionIDs = []string{}
	}
	return reg, nil
}

func officialWorkspaceResolveMethod(api OfficialWorkspaceAPI, p *officialParams) (any, error) {
	ref, which, err := p.strAlias(workspaceRefAliases...)
	if err != nil {
		return nil, err
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Resolve == nil {
		return nil, officialNotImplemented("workspace.resolve 未接线：宿主没有工作区注册表")
	}
	ws, err := api.Resolve(ref)
	if err != nil {
		return nil, err
	}
	if ws.WorkspaceID == "" {
		return nil, OfficialWorkspaceError("workspace/not-found", "%s=%q 没有对应的工作区", which, ref)
	}
	return OfficialWorkspaceValue{Workspace: officialNormalizeWorkspace(ws)}, nil
}

func officialWorkspaceSwitchMethod(api OfficialWorkspaceAPI, p *officialParams) (any, error) {
	ref, _, err := p.strAlias(workspaceRefAliases...)
	if err != nil {
		return nil, err
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Switch == nil {
		return nil, officialNotImplemented("workspace.switch 未接线：宿主没有工作区注册表")
	}
	value, err := api.Switch(ref)
	if err != nil {
		return nil, err
	}
	return OfficialWorkspaceSwitchValue{
		Workspace:           officialNormalizeWorkspace(value.Workspace),
		PreviousWorkspaceID: value.PreviousWorkspaceID,
	}, nil
}

func officialWorkspaceRemoveMethod(api OfficialWorkspaceAPI, p *officialParams) (any, error) {
	ref, _, err := p.strAlias(workspaceRefAliases...)
	if err != nil {
		return nil, err
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Remove == nil {
		return nil, officialNotImplemented("workspace.remove 未接线：宿主没有工作区注册表")
	}
	value, err := api.Remove(ref)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func officialWorkspaceCommandsMethod(api OfficialWorkspaceAPI, p *officialParams) (any, error) {
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Commands == nil {
		return nil, officialNotImplemented("workspace.commands 未接线：宿主没有工作区命令行")
	}
	list, err := api.Commands()
	if err != nil {
		return nil, err
	}
	if list.Commands == nil {
		list.Commands = []OfficialWorkspaceCommand{}
	}
	for i := range list.Commands {
		if list.Commands[i].ID == "" {
			return nil, OfficialWorkspaceError("workspace/internal",
				"workspace.commands 第 %d 条缺 id（宿主 bug，不能回给客户端）", i)
		}
	}
	return list, nil
}
