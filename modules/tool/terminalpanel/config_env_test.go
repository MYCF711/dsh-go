package terminalpanel

// 装配配置的来源判据：`readUserTerminals` 等三个键必须能被宿主显式关掉/覆盖，
// 而且**非法值必须响亮失败**（拼错的布尔值如果被当成"没设置"，用户会以为工具
// 已经关掉了，实际上它们还在表里）。

import (
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tools"
)

// envMap 把一张表变成 getenv 函数（模拟宿主环境）。
func envMap(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestConfigFromEnvDefaultsWhenUnset(t *testing.T) {
	cfg, err := ConfigFromEnv(envMap(nil))
	if err != nil {
		t.Fatalf("没有环境变量时不该报错: %v", err)
	}
	if cfg != DefaultConfig() {
		t.Fatalf("没设置时必须与官方默认值一致: %+v", cfg)
	}
	if _, err := ConfigFromEnv(nil); err != nil {
		t.Fatalf("getenv=nil（测试/旧装配）也必须能用: %v", err)
	}
}

func TestConfigFromEnvReadsOfficialKeys(t *testing.T) {
	cfg, err := ConfigFromEnv(envMap(map[string]string{
		EnvReadUserTerminals: "false",
		EnvMaxReadBytes:      "4096",
		EnvTimeoutMs:         "1500",
	}))
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	if cfg.ReadUserTerminals {
		t.Fatal("readUserTerminals=false 必须真的关掉")
	}
	if cfg.MaxReadBytes != 4096 || cfg.TimeoutMs != 1500 {
		t.Fatalf("两个数值项没读到: %+v", cfg)
	}

	// 官方写法也认（"0"/"1" 与 true/false 同义）。
	cfg, err = ConfigFromEnv(envMap(map[string]string{EnvReadUserTerminals: "0"}))
	if err != nil || cfg.ReadUserTerminals {
		t.Fatalf(`"0" 必须解析成 false: %+v / %v`, cfg, err)
	}
}

func TestConfigFromEnvRejectsGarbage(t *testing.T) {
	for _, kv := range []map[string]string{
		{EnvReadUserTerminals: "flase"},
		{EnvMaxReadBytes: "many"},
		{EnvTimeoutMs: "30s"},
	} {
		if _, err := ConfigFromEnv(envMap(kv)); err == nil {
			t.Fatalf("非法值必须报错，而不是静默回退: %v", kv)
		}
	}
}

// TestModuleFromEnvRemovesBothTools：`readUserTerminals:false` 时两个工具都**不存在**
// （官方 "removes both"），而不是注册成"永远报错"。
func TestModuleFromEnvRemovesBothTools(t *testing.T) {
	for _, tt := range []struct {
		name    string
		env     map[string]string
		present bool
	}{
		{name: "默认（官方默认部署）", env: nil, present: true},
		{name: "readUserTerminals=true", env: map[string]string{EnvReadUserTerminals: "true"}, present: true},
		{name: "readUserTerminals=false", env: map[string]string{EnvReadUserTerminals: "false"}, present: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			k := kernel.New("terminalpanel-env")
			t.Cleanup(k.Root().Dispose)
			mods := []kernel.Module{
				tools.Module(tools.Options{SpillDir: t.TempDir()}),
				ModuleFromEnv(envMap(tt.env)),
			}
			if _, err := kernel.Assemble(k.Root(), mods); err != nil {
				t.Fatalf("装配失败: %v", err)
			}
			reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"user_terminal_list", "user_terminal_read"} {
				_, got := reg.Get(name)
				if got != tt.present {
					t.Fatalf("%s 的注册状态是 %v，期望 %v", name, got, tt.present)
				}
			}
		})
	}
}

// TestModuleFromEnvFailsLoudOnGarbage：坏配置让**装配**失败（不是静默不注册）。
func TestModuleFromEnvFailsLoudOnGarbage(t *testing.T) {
	k := kernel.New("terminalpanel-bad-env")
	t.Cleanup(k.Root().Dispose)
	mods := []kernel.Module{
		tools.Module(tools.Options{SpillDir: t.TempDir()}),
		ModuleFromEnv(envMap(map[string]string{EnvReadUserTerminals: "flase"})),
	}
	_, err := kernel.Assemble(k.Root(), mods)
	if err == nil {
		t.Fatal("非法环境变量必须让装配失败")
	}
	if !strings.Contains(err.Error(), EnvReadUserTerminals) {
		t.Fatalf("错误里要说清是哪个环境变量: %v", err)
	}
}
