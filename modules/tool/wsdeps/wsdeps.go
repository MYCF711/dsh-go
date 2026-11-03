// Package wsdeps 提供 `load_workspace_dependencies` 工具：报告这台机器上跑
// Python/Office 活儿要用的运行时与库在哪、什么版本。
//
// 为什么需要它：模型要做 .xlsx/.docx 那类活儿时，先得知道有没有 openpyxl、
// python-docx，以及 python 在哪个路径。以前只能靠 bash 里试 `python -c "import ..."`，
// 一次一个库——既慢又容易在 PATH 上猜错。
//
// 这个工具**会起子进程**（一次 python 调用取全部版本），所以 Caps 里写了
// Executes: true。按 AGENTS §2，审批与沙箱不按工具名猜——起了进程就说起了进程。
// 代价是 deny 策略下它会被拒，那是正确行为。
package wsdeps

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// probeScript 一次取完所有库的版本。
//
// 关键点：**一个进程取全部**。七个库各起一个 python，在冷启动慢的机器上要好几秒，
// 而这里只需要一次导入。
const probeScript = `import json
libs = {}
for mod, name in [("numpy","numpy"),("pandas","pandas"),("openpyxl","openpyxl"),
                  ("docx","python-docx"),("pptx","python-pptx"),
                  ("PIL","Pillow"),("lxml","lxml")]:
    try:
        m = __import__(mod)
        libs[name] = getattr(m, "__version__", None) or "unknown"
    except Exception:
        pass
print(json.dumps({"version": "%d.%d.%d" % __import__("sys").version_info[:3], "libraries": libs}))`

type Tool struct{}

func (t *Tool) Name() string { return "load_workspace_dependencies" }
func (t *Tool) Caps() spi.Caps {
	// 起 python 子进程 = 执行。不因为"只是读版本"就把它藏起来。
	return spi.Caps{Executes: true}
}
func (t *Tool) Schema() json.RawMessage { return schema }
func (t *Tool) Description() string {
	return "查 Python/Node/pnpm 路径与 numpy/pandas/openpyxl/docx/pptx/Pillow/lxml 版本（会起子进程，deny 策略下会被拒）"
}

// deps 是返回给模型的结构化形状。
type deps struct {
	Python struct {
		Path    string `json:"path,omitempty"`
		Version string `json:"version,omitempty"`
		OK      bool   `json:"ok"`
	} `json:"python"`
	Libraries map[string]string `json:"libraries"`
	Node      struct {
		Path    string `json:"path,omitempty"`
		Version string `json:"version,omitempty"`
	} `json:"node"`
	Pnpm struct {
		Path    string `json:"path,omitempty"`
		Version string `json:"version,omitempty"`
	} `json:"pnpm"`
	Missing []string `json:"missing,omitempty"`
}

func (t *Tool) Exec(ctx context.Context, _ json.RawMessage) (spi.Result, error) {
	// 整件事 10 秒封顶：python 导入重库可能慢，但慢到 10 秒就不该再等。
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var d deps
	d.Libraries = map[string]string{}

	// Python：路径 + 版本 + 库，一次调用取完。
	if pyPath, err := exec.LookPath("python"); err == nil {
		d.Python.Path = pyPath
		if out, err := run(ctx, pyPath, "-c", probeScript); err == nil {
			var got struct {
				Version   string            `json:"version"`
				Libraries map[string]string `json:"libraries"`
			}
			if json.Unmarshal([]byte(out), &got) == nil {
				d.Python.Version = got.Version
				d.Python.OK = true
				for k, v := range got.Libraries {
					d.Libraries[k] = v
				}
			}
		}
	} else {
		d.Missing = append(d.Missing, "python")
	}
	if d.Python.OK {
		for _, name := range []string{"numpy", "pandas", "openpyxl", "python-docx", "python-pptx", "Pillow", "lxml"} {
			if _, ok := d.Libraries[name]; !ok {
				d.Missing = append(d.Missing, name)
			}
		}
	}

	// Node / pnpm 只报路径与版本；没有就说没有，不编。
	if p, err := exec.LookPath("node"); err == nil {
		d.Node.Path = p
		if out, err := run(ctx, p, "--version"); err == nil {
			d.Node.Version = trimNL(out)
		}
	} else {
		d.Missing = append(d.Missing, "node")
	}
	if p, err := exec.LookPath("pnpm"); err == nil {
		d.Pnpm.Path = p
		if out, err := run(ctx, p, "--version"); err == nil {
			d.Pnpm.Version = trimNL(out)
		}
	} else {
		d.Missing = append(d.Missing, "pnpm")
	}

	data, _ := json.MarshalIndent(d, "", "  ")

	// 一件事都没查到：这是**失败**，要带 Hint（R3：不许空 Result 丢 Hint）。
	if !d.Python.OK && d.Node.Path == "" && d.Pnpm.Path == "" {
		return spi.Result{
			Summary: "没找到 python/node/pnpm",
			Hint:    "这台机器上三样都没在 PATH 里；要么装一个，要么用 bash 手工确认它们的位置再告诉我",
		}, fmt.Errorf("wsdeps: python/node/pnpm 都不在 PATH 里")
	}

	return spi.Result{
		Summary: fmt.Sprintf("python %s｜库 %d 个｜node %s｜pnpm %s",
			orNA(d.Python.Version), len(d.Libraries), orNA(d.Node.Version), orNA(d.Pnpm.Version)),
		Text: string(data),
		Data: d,
	}, nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return string(out), err
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func orNA(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// Module 返回装配用的模块描述。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "tool-wsdeps",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&Tool{})
		},
	}
}
