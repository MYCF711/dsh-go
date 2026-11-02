// Command dshd 是宿主入口：只做装配，不含业务分支（A3）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/exec/bash"
	"dshgo/modules/fs/local"
	"dshgo/modules/tool/edit"
	"dshgo/modules/tool/glob"
	"dshgo/modules/tool/grep"
	"dshgo/modules/tool/read"
	"dshgo/modules/tool/write"
	"dshgo/modules/tools"
)

const version = "0.2.1-go.5"

// builtinModules 是编译进二进制的默认实现列表。
// 将来这里由 blank import + 注册表生成，形状不变。
func builtinModules() []kernel.Module {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return []kernel.Module{
		// 默认实现也是插件（A3）：它们与第三方插件走同一个 Register(scope)。
		tools.Module(tools.Options{}),
		bash.Module(wd, bash.Options{}),
		local.Module(wd),
		read.Module(),
		write.Module(),
		edit.Module(),
		glob.Module(),
		grep.Module(),
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "modules":
		err = cmdModules(os.Args[2:])
	case "exec":
		err = cmdExec(os.Args[2:])
	case "tools":
		err = cmdTools(os.Args[2:])
	case "version":
		fmt.Printf("dshd %s\n", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dshd: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dshd —— dsh-go 宿主

用法:
  dshd modules graph [--manifest dsh.toml]   打印装配图（按能力排序后的激活顺序）
  dshd modules list  [--manifest dsh.toml]   列出可装配模块
  dshd exec --tool <name> --args <json>      装配后执行一个工具
  dshd tools [--manifest dsh.toml]           打印模型看到的工具表（JSON）
  dshd version                               版本
`)
}

func loadMods(manifestPath string) ([]kernel.Module, error) {
	mods := builtinModules()
	m, err := kernel.LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	return m.Apply(mods)
}

func cmdModules(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("用法: dshd modules graph|list")
	}
	fs := flag.NewFlagSet("modules", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	switch argv[0] {
	case "list":
		fmt.Printf("%-22s %-9s %-26s %s\n", "MODULE", "BACKEND", "PROVIDES", "REQUIRES")
		for _, m := range mods {
			fmt.Printf("%-22s %-9s %-26s %s\n", m.ID, backendOf(m), join(m.Provides), join(m.Requires))
		}
		return nil
	case "graph":
		order, err := kernel.TopoSort(mods)
		if err != nil {
			return err
		}
		plan := &kernel.Plan{Order: order}
		fmt.Printf("# 激活顺序（由能力依赖决定，不是 manifest 行顺序）\n")
		plan.Write(os.Stdout)
		return nil
	default:
		return fmt.Errorf("未知子命令 modules %s", argv[0])
	}
}

// ToolEntry 是模型看到的工具表里的一行（R4：这张表属于稳定性契约）。
type ToolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildToolTable 按名字排序，保证同一装配产生同样的字节。
func buildToolTable(reg spi.ToolRegistry) ([]ToolEntry, error) {
	tools := reg.Tools()
	entries := make([]ToolEntry, 0, len(tools))
	for _, t := range tools {
		if !json.Valid(t.Schema()) {
			return nil, fmt.Errorf("工具 %q 的 schema 不是合法 JSON", t.Name())
		}
		entries = append(entries, ToolEntry{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func cmdTools(argv []string) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		return err
	}
	table, err := buildToolTable(reg)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "工具 %d 个，%d 字节\n", len(table), len(out))
	return nil
}

func cmdExec(argv []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	toolName := fs.String("tool", "", "工具名")
	rawArgs := fs.String("args", "{}", "工具参数 JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *toolName == "" {
		return fmt.Errorf("缺 --tool")
	}
	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	runner, err := kernel.Inject(root, tools.RunKey)
	if err != nil {
		return err
	}
	res, err := runner.Run(root.Context(), *toolName, json.RawMessage(*rawArgs))
	if err != nil {
		fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
		if res.Hint != "" {
			fmt.Fprintf(os.Stderr, "提示: %s\n", res.Hint)
		}
		return err
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	return nil
}

func backendOf(m kernel.Module) string {
	if m.Backend == "" {
		return string(kernel.BackendStatic)
	}
	return string(m.Backend)
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
