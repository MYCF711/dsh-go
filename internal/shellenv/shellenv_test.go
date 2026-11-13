package shellenv

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// contrib 造一个只声明一个键的贡献者；order 非 nil 时记录被 resolve 的顺序。
func contrib(name string, key string, value string, order *[]string) Contributor {
	return Contributor{
		Name:      name,
		Variables: map[string]Variable{key: {Description: "测试用变量"}},
		Resolve: func(context.Context) (map[string]any, error) {
			if order != nil {
				*order = append(*order, name)
			}
			return map[string]any{key: value}, nil
		},
	}
}

func mustRegister(t *testing.T, r *Registry, c Contributor) func() {
	t.Helper()
	dispose, err := r.Register(c)
	if err != nil {
		t.Fatalf("注册 %q 失败: %v", c.Name, err)
	}
	return dispose
}

func TestCollectBuiltins(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got[HomeEnv] != "/tmp/dsh-home" {
		t.Fatalf("%s = %q，期望 /tmp/dsh-home", HomeEnv, got[HomeEnv])
	}
	if got[ShellKey] != "1" {
		t.Fatalf("%s = %q，期望 \"1\"", ShellKey, got[ShellKey])
	}
	// 没有会话上下文 → 不许编一个 DSH_SESSION_ID 出来。
	if _, ok := got[SessionIDKey]; ok {
		t.Fatalf("无会话上下文时不该有 %s", SessionIDKey)
	}
	// 本仓没有 profileContext、也没有 Node 宿主：这几条事实不存在，就不注入
	// （既不是空串，也不是占位）。
	for _, key := range []string{ProfileKey, ProfileDirKey, InstallDirKey, NodeExecutableKey} {
		if _, ok := got[key]; ok {
			t.Fatalf("本仓没有这条事实，不该注入 %s", key)
		}
	}
	if len(got) != 2 {
		t.Fatalf("内置快照应只有 2 个键，实际 %d: %v", len(got), got)
	}
}

func TestCollectSessionIDFromContext(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	got, err := r.Collect(spi.WithSession(context.Background(), "sess-42"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got[SessionIDKey] != "sess-42" {
		t.Fatalf("%s = %q，期望 sess-42", SessionIDKey, got[SessionIDKey])
	}
}

func TestCollectMergesContributorsInNameOrder(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	var order []string
	// 故意乱序注册：collect 必须按贡献者**名字**排序后逐个 resolve（官方 localeCompare）。
	mustRegister(t, r, contrib("zeta", "DSH_ZETA", "z", &order))
	mustRegister(t, r, contrib("alpha", "DSH_ALPHA", "a", &order))
	mustRegister(t, r, contrib("mid", "DSH_MID", "m", &order))

	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if want := []string{"alpha", "mid", "zeta"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("resolve 顺序 = %v，期望 %v", order, want)
	}
	if got["DSH_ALPHA"] != "a" || got["DSH_MID"] != "m" || got["DSH_ZETA"] != "z" {
		t.Fatalf("贡献值没合入: %v", got)
	}
	// 键有序：消费者（bash 拼 export、pwsh 拼 cmd.Env）拿到的序列必须确定。
	want := []string{"DSH_ALPHA", "DSH_HOME", "DSH_MID", "DSH_SHELL", "DSH_ZETA"}
	if keys := SortedKeys(got); !reflect.DeepEqual(keys, want) {
		t.Fatalf("键序列 = %v，期望 %v", keys, want)
	}
}

func TestCollectReturnsIndependentCopy(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	first, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	first[HomeEnv] = "被调用方改了"
	first["DSH_INJECTED_BY_CALLER"] = "x"

	second, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if second[HomeEnv] != "/tmp/dsh-home" {
		t.Fatalf("改返回值污染了注册表: %q", second[HomeEnv])
	}
	if _, ok := second["DSH_INJECTED_BY_CALLER"]; ok {
		t.Fatal("调用方塞进返回值的键不该出现在下一次快照里")
	}
}

func TestRegisterRejectsBadContributors(t *testing.T) {
	type testCase struct {
		name string
		// pre 先建立前置状态（重名、键被占这两条需要）。
		pre func(t *testing.T, r *Registry)
		// call 是要断言失败的这一次注册。
		call func(r *Registry) error
		want string
	}
	cases := []testCase{
		{
			name: "空名字",
			call: func(r *Registry) error {
				_, err := r.Register(Contributor{Name: "   ", Variables: map[string]Variable{}, Resolve: nopResolve})
				return err
			},
			want: "bash env contributor name must be non-empty",
		},
		{
			name: "缺 resolver",
			call: func(r *Registry) error {
				_, err := r.Register(Contributor{Name: "c", Variables: map[string]Variable{}})
				return err
			},
			want: `bash env contributor "c" has no resolver`,
		},
		{
			name: "重名",
			pre: func(t *testing.T, r *Registry) {
				mustRegister(t, r, contrib("dup", "DSH_DUP", "1", nil))
			},
			call: func(r *Registry) error {
				_, err := r.Register(contrib("dup", "DSH_DUP2", "2", nil))
				return err
			},
			want: `bash env contributor "dup" is already registered`,
		},
		{
			name: "非法键_没有前缀",
			call: func(r *Registry) error {
				_, err := r.Register(Contributor{Name: "c", Variables: map[string]Variable{"NOPE": {Description: "d"}}, Resolve: nopResolve})
				return err
			},
			want: `bash env contributor "c" declared invalid key "NOPE"`,
		},
		{
			name: "非法键_后缀小写",
			call: func(r *Registry) error {
				_, err := r.Register(Contributor{Name: "c", Variables: map[string]Variable{"DSH_lower": {Description: "d"}}, Resolve: nopResolve})
				return err
			},
			want: `bash env contributor "c" declared invalid key "DSH_lower"`,
		},
		{
			name: "非法键_后缀数字开头",
			call: func(r *Registry) error {
				_, err := r.Register(Contributor{Name: "c", Variables: map[string]Variable{"DSH_1BAD": {Description: "d"}}, Resolve: nopResolve})
				return err
			},
			want: `bash env contributor "c" declared invalid key "DSH_1BAD"`,
		},
		{
			name: "空 description",
			call: func(r *Registry) error {
				_, err := r.Register(Contributor{Name: "c", Variables: map[string]Variable{"DSH_OK": {Description: "  "}}, Resolve: nopResolve})
				return err
			},
			want: `bash env contributor "c" must describe "DSH_OK"`,
		},
		{
			name: "键已被别人拥有",
			pre: func(t *testing.T, r *Registry) {
				mustRegister(t, r, contrib("first", "DSH_SHARED", "1", nil))
			},
			call: func(r *Registry) error {
				_, err := r.Register(contrib("second", "DSH_SHARED", "2", nil))
				return err
			},
			want: `bash env key "DSH_SHARED" is already owned by contributor "first"; contributor "second" cannot also own it`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry("/tmp/dsh-home")
			if tc.pre != nil {
				tc.pre(t, r)
			}
			err := tc.call(r)
			if err == nil {
				t.Fatal("期望失败，实际注册成功")
			}
			if err.Error() != tc.want {
				t.Fatalf("错误文案 = %q，期望 %q", err.Error(), tc.want)
			}
			// 注册期失败是普通 error，不带码（对齐官方：只有 collect 期两条带码）。
			var coded interface{ ShellEnvCode() string }
			if errors.As(err, &coded) {
				t.Fatalf("注册期失败不该带码，实际 %q", coded.ShellEnvCode())
			}
		})
	}
}

func TestRegisterRejectsEveryReservedKey(t *testing.T) {
	// 7 个预留键一个都不许被贡献者声明（官方 RESERVED_BASH_ENV_KEYS）。
	for _, key := range []string{HomeEnv, ShellKey, SessionIDKey, ProfileKey, ProfileDirKey, InstallDirKey, NodeExecutableKey} {
		t.Run(key, func(t *testing.T) {
			r := NewRegistry("/tmp/dsh-home")
			_, err := r.Register(Contributor{
				Name:      "c",
				Variables: map[string]Variable{key: {Description: "d"}},
				Resolve:   nopResolve,
			})
			want := `bash env contributor "c" cannot own reserved key "` + key + `"`
			if err == nil || err.Error() != want {
				t.Fatalf("错误 = %v，期望 %q", err, want)
			}
		})
	}
}

func TestCollectRejectsUndeclaredKey(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	mustRegister(t, r, Contributor{
		Name:      "sneaky",
		Variables: map[string]Variable{"DSH_OK": {Description: "d"}},
		Resolve: func(context.Context) (map[string]any, error) {
			return map[string]any{"DSH_EXTRA": "x"}, nil
		},
	})
	_, err := r.Collect(context.Background())
	assertShellEnvError(t, err, `bash env contributor "sneaky" returned undeclared key "DSH_EXTRA"`)
}

func TestCollectRejectsNonStringValue(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	mustRegister(t, r, Contributor{
		Name:      "typed",
		Variables: map[string]Variable{"DSH_OK": {Description: "d"}},
		Resolve: func(context.Context) (map[string]any, error) {
			// 官方 `typeof value !== 'string'`（index.ts:190-195）在 Go 侧的等价物：
			// 返回类型是 any，所以这条运行期检查真的存在、也真的能触发。
			return map[string]any{"DSH_OK": 42}, nil
		},
	})
	_, err := r.Collect(context.Background())
	assertShellEnvError(t, err, `bash env contributor "typed" returned a non-string value for "DSH_OK"`)
}

func TestCollectPropagatesResolverError(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	boom := errors.New("贡献者自己炸了")
	mustRegister(t, r, Contributor{
		Name:      "boom",
		Variables: map[string]Variable{"DSH_OK": {Description: "d"}},
		Resolve:   func(context.Context) (map[string]any, error) { return nil, boom },
	})
	_, err := r.Collect(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("resolver 的错误应原样透传，实际 %v", err)
	}
}

func TestListEnumeratesWithoutResolving(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	calls := 0
	mustRegister(t, r, Contributor{
		Name:      "zeta",
		Variables: map[string]Variable{"DSH_Z": {Description: "z 的说明"}},
		Resolve: func(context.Context) (map[string]any, error) {
			calls++
			return nil, nil
		},
	})
	mustRegister(t, r, Contributor{
		Name:      "alpha",
		Variables: map[string]Variable{"DSH_A": {Description: "a 的说明"}},
		Resolve: func(context.Context) (map[string]any, error) {
			calls++
			return nil, nil
		},
	})

	got := r.List()
	if calls != 0 {
		t.Fatalf("List 不该执行 resolver，实际调用 %d 次", calls)
	}
	want := []VariableInfo{
		{Contributor: "alpha", Key: "DSH_A", Description: "a 的说明"},
		{Contributor: "zeta", Key: "DSH_Z", Description: "z 的说明"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %+v，期望 %+v", got, want)
	}
	// 内置事实不进 List（官方 TODO(bash-env-list-builtins) 的同一条已知边界）。
	for _, info := range got {
		if info.Key == HomeEnv || info.Key == ShellKey {
			t.Fatalf("内置事实不该出现在 List 里: %+v", info)
		}
	}
}

func TestDisposerReleasesContributorAndKeys(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	dispose := mustRegister(t, r, contrib("first", "DSH_SHARED", "1", nil))
	if _, err := r.Register(contrib("second", "DSH_SHARED", "2", nil)); err == nil {
		t.Fatal("键被别人拥有时应拒绝")
	}

	dispose()
	dispose() // 幂等

	if got := r.List(); len(got) != 0 {
		t.Fatalf("撤销后不该还有声明: %+v", got)
	}
	values, err := r.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, ok := values["DSH_SHARED"]; ok {
		t.Fatalf("撤销后键还在快照里: %v", values)
	}
	// 撤销释放了名字与键：同名、同键都能重新注册。
	mustRegister(t, r, contrib("first", "DSH_SHARED", "3", nil))
}

func TestRegisterScopedDisposesWithScope(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	k := kernel.New("shellenv-test")
	scope := k.Root().Child("plugin-x")
	defer scope.Dispose()

	if err := r.RegisterScoped(scope, contrib("scoped", "DSH_SCOPED", "1", nil)); err != nil {
		t.Fatalf("RegisterScoped: %v", err)
	}
	if got := r.List(); len(got) != 1 {
		t.Fatalf("注册后应有 1 条声明，实际 %+v", got)
	}
	// 作用域撤销 = 官方 effect 撤销：贡献者与它声明的键一起释放。
	scope.Dispose()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("作用域撤销后不该还有声明: %+v", got)
	}
	mustRegister(t, r, contrib("scoped", "DSH_SCOPED", "2", nil))
}

func TestRegisterScopedRejectsNilScope(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	if err := r.RegisterScoped(nil, contrib("x", "DSH_X", "1", nil)); err == nil {
		t.Fatal("nil 作用域应被拒绝：撤不掉的贡献者比不注册更坏")
	}
}

func TestRegistryConcurrentUse(t *testing.T) {
	r := NewRegistry("/tmp/dsh-home")
	ctx := spi.WithSession(context.Background(), "sess-race")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				name := "c" + string(rune('a'+j%3))
				dispose, err := r.Register(contrib(name, "DSH_"+strings.ToUpper(name), "v", nil))
				if err == nil {
					dispose()
				}
				_, _ = r.Collect(ctx)
				_ = r.List()
			}
		}()
	}
	wg.Wait()
}

// nopResolve 给"不该走到 resolve"的用例用。
func nopResolve(context.Context) (map[string]any, error) { return nil, nil }

// assertShellEnvError 断言是带正确码与文案的 *ShellEnvError。
func assertShellEnvError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("期望失败，实际成功")
	}
	var se *ShellEnvError
	if !errors.As(err, &se) {
		t.Fatalf("错误类型 = %T，期望 *ShellEnvError（%v）", err, err)
	}
	if se.Code != CodeContributionInvalid {
		t.Fatalf("错误码 = %q，期望 %q", se.Code, CodeContributionInvalid)
	}
	if se.Error() != want {
		t.Fatalf("错误文案 = %q，期望 %q", se.Error(), want)
	}
	if got := se.ShellEnvCode(); got != CodeContributionInvalid {
		t.Fatalf("ShellEnvCode() = %q，期望 %q", got, CodeContributionInvalid)
	}
}
