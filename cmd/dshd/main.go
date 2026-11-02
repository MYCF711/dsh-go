// Command dshd 是宿主入口：只做装配，不含业务分支（A3）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"dshgo/internal/kernel"
	"dshgo/modules/tool/read"
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
		tools.Module(),
		read.Module(wd),
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
	reg, err := kernel.Inject(root, tools.Key)
	if err != nil {
		return err
	}
	t, ok := reg.Get(*toolName)
	if !ok {
		return fmt.Errorf("工具 %q 不存在；已注册: %v", *toolName, reg.(*tools.Registry).Sorted())
	}
	res, err := t.Exec(root.Context(), json.RawMessage(*rawArgs))
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
