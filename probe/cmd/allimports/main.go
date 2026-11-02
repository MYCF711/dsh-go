// allimports: 每个候选依赖都真 import + 真跑一小段，用于「能否在 Windows/Go1.27.3 上编译并使用」的逐条证据。
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/coder/websocket"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/hashicorp/go-plugin"
	"github.com/klauspost/compress/zstd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tetratelabs/wazero"
	"golang.org/x/sync/errgroup"
	"mvdan.cc/sh/v3/syntax"
)

type cfg struct {
	Name  string   `toml:"name"`
	Ports []int    `toml:"ports"`
	Env   []string `toml:"env"`
}

type addArgs struct {
	A int `json:"a"`
	B int `json:"b"`
}
type addOut struct {
	Sum int `json:"sum"`
}

func check(name string, fn func() (string, error)) {
	out, err := fn()
	if err != nil {
		fmt.Printf("%-14s FAIL: %v\n", name, err)
		return
	}
	fmt.Printf("%-14s OK: %s\n", name, out)
}

func main() {
	ctx := context.Background()

	check("toml", func() (string, error) {
		var c cfg
		if err := toml.Unmarshal([]byte("name='dsh'\nports=[1,2]\nenv=['A=1']\n"), &c); err != nil {
			return "", err
		}
		return fmt.Sprintf("%+v", c), nil
	})

	check("zstd", func() (string, error) {
		var buf bytes.Buffer
		enc, err := zstd.NewWriter(&buf)
		if err != nil {
			return "", err
		}
		if _, err := enc.Write([]byte(strings.Repeat("dsh", 2000))); err != nil {
			return "", err
		}
		enc.Close()
		n := buf.Len()
		dec, err := zstd.NewReader(&buf)
		if err != nil {
			return "", err
		}
		defer dec.Close()
		raw, err := io.ReadAll(dec)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("6000B -> %dB, roundtrip=%d", n, len(raw)), nil
	})

	check("jsonschema", func() (string, error) {
		s, err := jsonschema.For[cfg](nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("type=%s props=%d", s.Type, len(s.Properties)), nil
	})

	check("lipgloss", func() (string, error) {
		return fmt.Sprintf("%q", lipgloss.NewStyle().Bold(true).Padding(0, 1).Render("dsh")), nil
	})

	check("bubbletea", func() (string, error) {
		p := tea.NewProgram(nil)
		return fmt.Sprintf("program_ctor_ok=%v", p != nil), nil
	})

	check("websocket", func() (string, error) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "")
			typ, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			_ = c.Write(r.Context(), typ, append([]byte("echo:"), data...))
		}))
		defer srv.Close()
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			return "", err
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		if err := c.Write(ctx, websocket.MessageText, []byte("acs")); err != nil {
			return "", err
		}
		_, data, err := c.Read(ctx)
		if err != nil {
			return "", err
		}
		return string(data), nil
	})

	check("mcp", func() (string, error) {
		s := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "add"}, func(_ context.Context, _ *mcp.CallToolRequest, in addArgs) (*mcp.CallToolResult, addOut, error) {
			return &mcp.CallToolResult{}, addOut{Sum: in.A + in.B}, nil
		})
		ct, st := mcp.NewInMemoryTransports()
		if _, err := s.Connect(ctx, st, nil); err != nil {
			return "", err
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "host", Version: "0"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			return "", err
		}
		defer cs.Close()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "add", Arguments: map[string]any{"a": 5, "b": 37}})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("content=%v isError=%v", res.Content[0], res.IsError), nil
	})

	check("x/sync", func() (string, error) {
		g, _ := errgroup.WithContext(ctx)
		sum := 0
		for i := 1; i <= 4; i++ {
			g.Go(func() error { sum += 1; return nil })
		}
		err := g.Wait()
		return fmt.Sprintf("errgroup_done=%v", err == nil), nil
	})

	check("mvdan/sh", func() (string, error) {
		f, err := syntax.NewParser().Parse(strings.NewReader("echo a | tr a b"), "x.sh")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("stmts=%d", len(f.Stmts)), nil
	})

	check("wazero", func() (string, error) {
		r := wazero.NewRuntime(ctx)
		defer r.Close(ctx)
		m, err := r.CompileModule(ctx, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("compiled_empty_module_ok=%v", m != nil), nil
	})

	check("go-plugin", func() (string, error) {
		return fmt.Sprintf("protocol_enum=%v", []plugin.Protocol{plugin.ProtocolNetRPC, plugin.ProtocolGRPC}), nil
	})

	_ = time.Now
}
