package main

// directoryPicker 的**互斥组合声明**：本宿主这次启动到底是 native 那套
// （OS 原生目录框）还是 browse 那套（应用内浏览）。
//
// # 官方原件（rc.2 解包树 `node_modules/@deepseek-ai/`）
//
//   - `dsh-host-directory-picker/src/index.ts:19-28` native（`pick`）、
//     `:35-59` browse（`list`+`createDirectory`）、`:96-113` 抽象服务
//     （"one implementation per context; loading a second throws"）；
//   - `dsh-api-workspace-controller/src/directory-picker.ts:105-119` 动词按 kind
//     门控，不匹配就 `directory-picker/unavailable` + `{capability}`；
//   - `dsh-host-directory-picker-auto/src/resolve.ts:55-61` 官方**唯一**的组合
//     决策函数：远程权威 / 非回环绑定 / SSH 启动 / 平台 / Linux 选择器二进制
//     + DISPLAY 任一不利就 browse，含糊一律 browse（`:50-51`）。
//
// # 本宿主的移植与两处显式差异（都写清楚，不静默）
//
//  1. **平台集合收窄到 windows**。官方 native 后端驱动 darwin/win32/linux 三种
//     OS 选择器；本仓的原生目录框只有 Windows 实现（`internal/native`），
//     darwin/linux 没有真实支撑，所以那些平台一律 browse——**不假装**能弹框。
//     这是本仓的平台范围（AGENTS.md：只做 Windows 稳定版）带来的，不是判断。
//  2. **"有没有交互桌面"是外部事实，不是能自证的事实**。官方那条 `win32 ⇒ 有桌面`
//     的假设来自 Electron 主进程（它一定坐在某个桌面会话里）；`dshd serve` 是控制台
//     二进制，可能被服务、计划任务、无人值守脚本、SSH 会话拉起。所以本宿主**不猜**：
//     未声明时取官方自己的兜底答案 browse，要 native 就由知道桌面存在的启动方
//     **显式声明**（`--directory-picker=native` / `DSH_DIRECTORY_PICKER=native`）。
//     声明是操作员的判断（与"用户显式给出的 path 就是本次读取授权"同一条判据）。
//
// # 为什么"默认 browse"不算能力缩水
//
// 两套是**互斥替代**，不是叠加（官方同时挂第二套直接抛）。默认 browse 时
// `list`/`createDirectory` 是真落盘实现；显式声明 native 时 `pick` 是真弹框。
// 任一时刻都只有一套可服务，这与官方一致；差别只在"未声明时选哪一套"，而官方
// 对含糊情形的答案本来就是 browse。

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"dshgo/internal/transport"
)

// directoryPickerDesktopEnv 是"当前进程是否坐在交互桌面会话里"的 Windows 事实。
//
// `SESSIONNAME` 由 Winlogon/Terminal Services 在登录会话里设置：控制台会话是
// `Console`，RDP 是 `RDP-Tcp#N`；**服务会话是 `Services`**，而没有会话的进程
// 拿不到这个变量。判据只用它，不猜别的东西。
const directoryPickerDesktopEnv = "SESSIONNAME"

// directoryPickerServicesSession 是"有会话名但没有交互桌面"的那一个取值。
const directoryPickerServicesSession = "Services"

// directoryPickerSSHEnv 是官方 `launchedThroughSsh` 读的那一族环境变量。
var directoryPickerSSHEnv = []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"}

// directoryPickerFacts 是组合决策的全部输入（一次启动采样一次）。
//
// 每个字段都对应官方 `DirectoryPickerHostFacts`（resolve.ts:19-36）里同样的一项，
// 只有 hasDesktop 是本宿主新增的（见文件头第 2 条差异）。
type directoryPickerFacts struct {
	// bindHost 是 webserver 实际绑定地址（决定"是不是只有本机能连"）。
	bindHost string
	// allowsRemoteAuthorities 是连接层是否信任非回环权威（本宿主没有可信主机表，恒 false，
	// 保留这一项是为了让决策函数与官方逐项对得上、并且可测）。
	allowsRemoteAuthorities bool
	// platform 是 runtime.GOOS。
	platform string
	// ssh 表示本次进程是从 SSH 会话里起来的（转发的连接弹不出服务端的框）。
	ssh bool
	// hasDesktop 表示当前进程坐在一个交互桌面会话里（Windows 的 SESSIONNAME 事实）。
	hasDesktop bool
}

// resolveDirectoryPickerKind 把"声明 + 事实"解成本次组合。
//
// 声明的取值只有三个字面量：空 = 自动，`native` / `browse` = 显式。别的一律报错
// ——拼错的声明不许静默退回某一档，那会让 `--directory-picker` 变成假开关
// （与 `--tools-mode` 同一条判据）。
//
// 自动（空）分支 = browse。官方 resolve.ts 选 native 的条件是"回环绑定 + 无远程
// 权威 + 非 SSH + 平台是 darwin/win32（或 linux 且 DISPLAY + 选择器二进制）"全部
// 成立；本宿主把这些事实（`nativeBlocker`）全部实现了，但**多了一条官方没有的
// 前提**：官方能做这个判断，是因为 Electron 主进程一定坐在某个桌面会话里；
// `dshd serve` 不是（见文件头第 2 条差异），而它手里的事实全是继承来的环境变量，
// 不构成"我能弹框"的证明。官方对含糊情形的兜底就是 browse，所以未声明 = browse。
// 事实那一半没有被浪费：显式声明 native 时它负责把"这条声明与事实冲突"说出来。
func resolveDirectoryPickerKind(declaration string) (transport.DirectoryPickerKind, error) {
	switch strings.ToLower(strings.TrimSpace(declaration)) {
	case string(transport.DirectoryPickerKindNative):
		return transport.DirectoryPickerKindNative, nil
	case string(transport.DirectoryPickerKindBrowse):
		return transport.DirectoryPickerKindBrowse, nil
	case "":
		// 未声明 = 官方对**含糊**情形的答案：browse（resolve.ts:50-51 "Anything
		// ambiguous resolves to browse"）。这里的事实通常"齐全"（本地回环 +
		// Windows + Console 会话），但齐全不等于**证明**：dshd 的桌面事实是从父
		// 进程继承来的环境变量，一个继承到 SESSIONNAME 却没有可用桌面的拉起方式
		// 照样弹不出框。所以未声明时不赌，取官方兜底；要 native 就显式声明。
		return transport.DirectoryPickerKindBrowse, nil
	default:
		return "", fmt.Errorf(
			"directoryPicker: %q 不是官方组合（只认 %q / %q；空 = 自动）",
			declaration, transport.DirectoryPickerKindNative, transport.DirectoryPickerKindBrowse)
	}
}

// nativeBlocker 返回"这次启动绝对服务不了 native"的第一条理由；空串 = 没有阻碍。
//
// 它就是官方 resolve.ts 那条事实判据的逐条移植，用在一处：显式声明 native 时的
// 启动告警。声明是操作员的判断，本宿主不推翻它，但必须把"这条声明和事实冲突"
// 说出来，否则原生框弹不出来会变成一次静默失败（pick 会 fail-loud 回 500，
// 而 500 不会告诉任何人"你其实声明错了组合"）。
func nativeBlocker(facts directoryPickerFacts) string {
	if facts.allowsRemoteAuthorities {
		return "连接层信任了非回环权威（远程浏览器用不了宿主屏幕上的框）"
	}
	if !isLoopbackBindHost(facts.bindHost) {
		return fmt.Sprintf("webserver 绑的不是回环地址（%q）", facts.bindHost)
	}
	if facts.ssh {
		return "进程是 SSH 会话里起的（转发连接弹不出服务端的框）"
	}
	if facts.platform != "windows" {
		return fmt.Sprintf("原生目录框只实现了 Windows（当前 GOOS=%q）", facts.platform)
	}
	if !facts.hasDesktop {
		return "当前进程没有交互桌面会话（" + directoryPickerDesktopEnv + " 不含会话名）"
	}
	return ""
}

// isLoopbackBindHost 判一个监听地址是不是只在本机可达。
//
// 空串 = transport 的默认（127.0.0.1:0），算回环；`localhost` 算回环；
// 别的按 IP 判（含 `::1`）。解析不了的主机名一律**不算**回环——宁可判 browse，
// 也不许把一个外部可达的绑定当成"只有本机能连"。
func isLoopbackBindHost(bindHost string) bool {
	host := strings.TrimSpace(bindHost)
	if host == "" {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	switch strings.ToLower(host) {
	case "", "localhost":
		return true
	case "0.0.0.0", "::", "[::]":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// directoryPickerFactsFromEnv 采样一次启动事实。
//
// lookup 注入是为了可测（生产传 os.Getenv；测试不用改进程环境）。addr 是
// `serve --addr` 的原始取值（空 = transport 默认 127.0.0.1:0）。
func directoryPickerFactsFromEnv(addr, goos string, lookup func(string) string) directoryPickerFacts {
	facts := directoryPickerFacts{bindHost: addr, platform: goos}
	for _, key := range directoryPickerSSHEnv {
		if strings.TrimSpace(lookup(key)) != "" {
			facts.ssh = true
			break
		}
	}
	if goos == "windows" {
		session := strings.TrimSpace(lookup(directoryPickerDesktopEnv))
		facts.hasDesktop = session != "" && !strings.EqualFold(session, directoryPickerServicesSession)
	}
	return facts
}

// attachOfficialDirectoryPickerSeam 把这次启动声明的组合挂到 srv 上。
//
// 这是**唯一**的挂载点：native 声明只带 kind（pick 由 OfficialNativeAPI 供），
// browse 声明带三个字段里 browse 那两个原语。同时挂两套会在
// transport.AttachOfficialDirectoryPickerSeam 的校验里报错并被当成启动失败。
func attachOfficialDirectoryPickerSeam(srv *transport.Server, host *officialWorkspaceHost, kind transport.DirectoryPickerKind) error {
	if srv == nil {
		return errors.New("directoryPicker: srv 是 nil，挂不上去")
	}
	if host == nil {
		return errors.New("directoryPicker: workspace host 是 nil，挂不上去")
	}
	if err := srv.AttachOfficialDirectoryPickerSeam(host.directoryPickerSeam(kind)); err != nil {
		return fmt.Errorf("directoryPicker 组合挂载失败（声明 %q）：%w", string(kind), err)
	}
	return nil
}
