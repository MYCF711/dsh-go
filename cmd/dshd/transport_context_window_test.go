package main

// session.load 的 contextWindow：界面靠它决定"画上下文占用"还是"只报数字"。
//
// 旧实现只认 DSHD_CONTEXT_WINDOW，没设就恒回 0——真实试玩里状态栏的上下文占用
// 于是永远不显示。这里钉住新的解析顺序，**按官方基线**：
// 模型目录 > DSHD_CONTEXT_WINDOW（兜底）> 内置表 > 0。
// 官方证据：llm-pi-ai `entry.contextWindow ?? base.contextWindow ??
// request.defaultContextWindow`（@dsa/dsh-llm-pi-ai/lib/index.js:694）；
// `DSH_CONTEXT_WINDOW` 只在 dsh-sdk-minimal 里作 defaultContextWindow 兜底
//（@dsa/dsh-sdk-minimal/cordis.patch.yml:30），见 tmp/GO3-PARAMS/official.md §4③。
// 并钉住网关实测值：/v1/models 对 group/deepseek-v4.1-flash-1017 返回
// context_length=1000000。

import (
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

const knownModelID = "group/deepseek-v4.1-flash-1017"

// writeModelCatalog 在 home 下写一份真实形状的模型目录（<home>/state/model-catalog.json）。
func writeModelCatalog(t *testing.T, home string, models string) {
	t.Helper()
	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"revision":0,"models":[` + models + `]}`
	if err := os.WriteFile(filepath.Join(dir, "model-catalog.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadContextWindow 走真实 session.load 投影（transport.LoadSession → WindowSourcer）。
func loadContextWindow(t *testing.T, home, cwd, sid string, configure func(*sessionSource)) int {
	t.Helper()
	src := newSessionSource(home, cwd)
	if configure != nil {
		configure(src)
	}
	res, err := transport.LoadSession(src, sid, 10)
	if err != nil {
		t.Fatalf("session.load: %v", err)
	}
	return res.ContextWindow
}

// 会话里跑过的模型（v4 request/context）在目录里声明了窗口 → 用目录值。
func TestLoadContextWindowUsesCatalogDeclaration(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-known"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("request/context", map[string]any{"provider": "magpie", "model": knownModelID})
	})
	// 故意写一个与内置兜底不同的值：证明目录声明优先于内置表。
	writeModelCatalog(t, home, `{"id":"`+knownModelID+`","name":"flash","contextWindow":2000000}`)

	got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) {
		src.catalog = llm.OpenModelCatalog(home)
	})
	if got != 2000000 {
		t.Fatalf("session.load contextWindow = %d，期望目录声明的 2000000", got)
	}
}

// 目录按网关形状（context_length）声明时也要认，且就是实测的 1000000。
func TestLoadContextWindowUsesGatewayContextLength(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-gateway"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("request/context", map[string]any{"provider": "magpie", "model": knownModelID})
	})
	writeModelCatalog(t, home, `{"id":"`+knownModelID+`","name":"flash","context_length":1000000}`)

	got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) { src.catalog = llm.OpenModelCatalog(home) })
	if got != 1000000 {
		t.Fatalf("session.load contextWindow = %d，期望网关实测 1000000", got)
	}
}

// 目录没声明这个模型 → 退到内置容量表（内置值已按网关实测改成 1000000）。
func TestLoadContextWindowFallsBackToBuiltInCapacity(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-builtin"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("request/context", map[string]any{"provider": "magpie", "model": knownModelID})
	})
	writeModelCatalog(t, home, `{"id":"other/model","name":"other","contextWindow":65536}`)

	got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) { src.catalog = llm.OpenModelCatalog(home) })
	if got != 1000000 {
		t.Fatalf("session.load contextWindow = %d，期望内置兜底 1000000", got)
	}
}

// 没有目录（老 home / 全新 home）时内置表兜底，不是 0。
func TestLoadContextWindowWithoutCatalogUsesBuiltInCapacity(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-no-catalog"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("request/context", map[string]any{"provider": "magpie", "model": knownModelID})
	})

	if got := loadContextWindow(t, home, cwd, sid, nil); got != 1000000 {
		t.Fatalf("session.load contextWindow = %d，期望内置兜底 1000000", got)
	}
}

// 官方主路径是**模型目录**：目录声明了就必须用它，env 不能覆盖。
// （旧实现把 env 放在第一位，等于让一个运维开关压过真实模型元数据。）
func TestLoadContextWindowCatalogBeatsEnv(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-catalog-vs-env"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("request/context", map[string]any{"provider": "magpie", "model": knownModelID})
	})
	writeModelCatalog(t, home, `{"id":"`+knownModelID+`","name":"flash","contextWindow":2000000}`)
	t.Setenv("DSHD_CONTEXT_WINDOW", "32000")

	if got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) {
		src.catalog = llm.OpenModelCatalog(home)
	}); got != 2000000 {
		t.Fatalf("session.load contextWindow = %d，期望目录声明的 2000000（env 32000 只能作兜底）", got)
	}
}

// 目录没给这个模型声明窗口时，env 才生效（官方 sdk-minimal 的 defaultContextWindow 语义）。
// 用 32000 而不是 1000000：内置表兜底也是 1000000，选它区分不出"env 生效"。
func TestLoadContextWindowEnvFallbackWhenCatalogMisses(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-env-fallback-2"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("request/context", map[string]any{"provider": "magpie", "model": knownModelID})
	})
	writeModelCatalog(t, home, `{"id":"other/model","name":"other","contextWindow":65536}`)
	t.Setenv("DSHD_CONTEXT_WINDOW", "32000")

	if got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) {
		src.catalog = llm.OpenModelCatalog(home)
	}); got != 32000 {
		t.Fatalf("session.load contextWindow = %d，期望 env 兜底 32000（目录没声明这个模型）", got)
	}
}

// 会话还没跑过（v4 里没有 request/context）时，用会话模型覆盖，再用宿主默认 --model。
func TestLoadContextWindowUsesSessionModelOverrideAndHostDefault(t *testing.T) {
	t.Run("会话模型覆盖", func(t *testing.T) {
		home := t.TempDir()
		cwd := t.TempDir()
		const sid = "session-context-window-override"
		seedTurnSession(t, home, cwd, sid, func(*session.Log) error { return nil })
		overrides := newModelOverrideStore(home)
		if err := overrides.Set(sid, knownModelID); err != nil {
			t.Fatal(err)
		}
		got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) {
			src.models = newModelOverrideStore(home)
			if err := src.models.Load(); err != nil {
				t.Fatal(err)
			}
		})
		if got != 1000000 {
			t.Fatalf("session.load contextWindow = %d，期望会话模型覆盖解析出的 1000000", got)
		}
	})
	t.Run("宿主默认模型", func(t *testing.T) {
		home := t.TempDir()
		cwd := t.TempDir()
		const sid = "session-context-window-default-model"
		seedTurnSession(t, home, cwd, sid, func(*session.Log) error { return nil })
		got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) { src.defaultModel = knownModelID })
		if got != 1000000 {
			t.Fatalf("session.load contextWindow = %d，期望宿主默认模型解析出的 1000000", got)
		}
	})
}

// 模型完全未知且没有环境变量 → 0（不知道就不画，不编一个数）。
func TestLoadContextWindowUnknownModelStaysZero(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-unknown"
	seedTurnSession(t, home, cwd, sid, func(*session.Log) error { return nil })
	t.Setenv("DSHD_CONTEXT_WINDOW", "")

	if got := loadContextWindow(t, home, cwd, sid, nil); got != 0 {
		t.Fatalf("session.load contextWindow = %d，期望 0（模型未知）", got)
	}
}

// 全新 home（catalog 文件物理缺失）+ 显式 --model 的真实路径：serve 启动时会把
// 当前 provider 发现到的**真实元数据**投影成 env 条目（不落盘），其中
// contextWindow 就是网关的 context_length=1000000。session.load 必须用上它。
func TestLoadContextWindowUsesEnvModelFallback(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-context-window-env-fallback"
	seedTurnSession(t, home, cwd, sid, func(*session.Log) error { return nil })

	catalog := llm.OpenModelCatalog(home)
	entry := `{"id":"` + knownModelID + `","name":"flash","owned_by":"magpie",` +
		`"api":"openai-completions","baseURL":"http://127.0.0.1:3425/v1","contextWindow":1000000}`
	applied, err := catalog.SetEnvModelFallback([]byte(entry))
	if err != nil {
		t.Fatalf("安装 env 模型投影: %v", err)
	}
	if !applied {
		t.Fatal("全新 home 上 env 模型投影必须生效（catalog 物理缺失）")
	}

	// 全新会话还没跑过：模型来自宿主默认 --model（生产的 src.defaultModel），
	// 目录里的 env 条目才是它的窗口真值。
	got := loadContextWindow(t, home, cwd, sid, func(src *sessionSource) {
		src.catalog = catalog
		src.defaultModel = knownModelID
	})
	if got != 1000000 {
		t.Fatalf("session.load contextWindow = %d，期望 env 模型投影的 1000000", got)
	}
}
