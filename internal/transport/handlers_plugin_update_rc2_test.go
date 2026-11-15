package transport

// 官方 0.2.2-rc.2 插件管理面契约增量的协议测试（红先写）。
//
// 权威源（随包 0.2.2-rc.2，file:line 见报告）：
//   - guide：`docs/upgrade-guide/v0.2.2-rc.1/plugin-update-method/guide.md`
//     `BundleInfo.updatable` → `BundleInfo.update: 'registry' | 'git'`（缺省=换不了）；
//     `ManagementError.code` 新增 `unexpected-package`。
//   - `dsh-plugin-manager/lib/typert.host.js`：
//     `installBundle` 的 `parameters[1].jsonSchema.properties.expected`（name/version 必填，
//     `additionalProperties: true`）；`updateBundle` 的
//     `parameters[2].jsonSchema.properties.spec`（string）。
//   - `dsh-plugin-manager/src/types.ts:39`（ManagementError code 联合）、
//     `src/types.ts:94-112`（BundleInfo.update 三态）、
//     `src/types.ts:218-222`（InstallBundleOptions.expected）、
//     `src/types.ts:328-338`（UpdateBundleOptions.spec）。
//
// 这些测试只打本包的 `ServeOfficialPluginManagerAPI`（路由与四字段错误已由
// handlers_pluginmanager_official_test.go 钉住），宿主状态由真实适配器在
// `internal/pluginmgr/official` 的测试里覆盖。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// resultRows 取出 {"result":[ … ]} 里的数组（listBundles 的结果是数组）。
func resultRows(t *testing.T, code int, out map[string]any) []any {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（body=%v）", code, out)
	}
	rows, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("成功信封缺 result 数组：%v", out)
	}
	return rows
}

// rowByName 取一行 bundle 的 JSON 对象。
func rowByName(t *testing.T, rows []any, name string) map[string]any {
	t.Helper()
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("bundle 行不是对象：%v", raw)
		}
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("listBundles 结果里没有 %q：%v", name, rows)
	return nil
}

// newFakePluginManagerAPI 造一个可用的假宿主：seen 必须初始化，否则 record 会 panic。
func newFakePluginManagerAPI() *fakePluginManagerAPI {
	return &fakePluginManagerAPI{seen: map[string]any{}}
}

// updateKindHook 是一个只按来源字符串回答的假宿主钩子。
func updateKindHook(kinds map[string]string) func(source, name, version string) string {
	return func(source, name, version string) string { return kinds[source] }
}

func TestListBundlesUpdateTriState(t *testing.T) {
	fake := newFakePluginManagerAPI()
	fake.listBundlesRet = []OfficialBundle{
		{Name: "acme.registry", Availability: "profile", Version: "1.0.0", Source: "acme.registry@1.0.0",
			Installed: true, Removable: true},
		{Name: "acme.git", Availability: "profile", Version: "1.0.0",
			Source: "github:acme/widget#" + gitSHA40, Installed: true, Removable: true},
		{Name: "acme.local", Availability: "profile", Version: "1.0.0", Source: "./private/v1",
			Installed: true, Removable: true},
	}
	api := fake.api()
	api.BundleUpdateKind = updateKindHook(map[string]string{
		"acme.registry@1.0.0":            OfficialUpdateRegistry,
		"github:acme/widget#" + gitSHA40: OfficialUpdateGit,
		"./private/v1":                   "",
	})
	srv := officialPluginTestServer(t, api)

	code, out := callPluginManager(t, srv, string(MethodPluginManagerListBundles), map[string]any{})
	rows := resultRows(t, code, out)

	registry := rowByName(t, rows, "acme.registry")
	if registry["update"] != OfficialUpdateRegistry {
		t.Fatalf("registry 来源的包 update 应为 %q，实际 %v", OfficialUpdateRegistry, registry["update"])
	}
	// 旧字段必须仍在，且与官方 guide 的映射一致：updatable:true ⇔ update:'registry'。
	if registry["updatable"] != true {
		t.Fatalf("update=registry 的行必须同时保留 updatable=true，实际 %v", registry["updatable"])
	}

	gitRow := rowByName(t, rows, "acme.git")
	if gitRow["update"] != OfficialUpdateGit {
		t.Fatalf("完整 commit 钉住的 git 来源 update 应为 %q，实际 %v", OfficialUpdateGit, gitRow["update"])
	}
	// git 行在 rc.1 里 updatable 是 false（guide：updatable:true 只对应 registry）。
	if gitRow["updatable"] != false {
		t.Fatalf("update=git 的行 updatable 必须保持 false，实际 %v", gitRow["updatable"])
	}

	local := rowByName(t, rows, "acme.local")
	if _, present := local["update"]; present {
		t.Fatalf("换不了的包必须**缺省** update（不能给空串）：%v", local["update"])
	}
	if local["updatable"] != false {
		t.Fatalf("换不了的包 updatable 必须保持 false，实际 %v", local["updatable"])
	}
}

// TestListBundlesUpdateGates 钉住官方 updateRefusal 的四条前置门槛：
// 未安装 / official / availability≠profile / 带插件管理 / 没有 SemVer 版本，
// 任何一条成立都必须缺省 update——即使宿主钩子说这个来源可以换。
func TestListBundlesUpdateGates(t *testing.T) {
	fake := newFakePluginManagerAPI()
	fake.listBundlesRet = []OfficialBundle{
		{Name: "acme.absent", Availability: "profile", Version: "1.0.0", Source: "acme.absent@1.0.0", Installed: false},
		{Name: "acme.offered", Availability: "missing", Version: "", Source: "acme.offered@1.0.0",
			Installed: true, Official: true},
		{Name: "acme.shipped", Availability: "installation", Version: "1.0.0", Source: "acme.shipped@1.0.0",
			Installed: true},
		{Name: "acme.managed", Availability: "profile", Version: "1.0.0", Source: "acme.managed@1.0.0",
			Installed: true, ReadOnlyReason: "management-required"},
		{Name: "acme.nonsemver", Availability: "profile", Version: "not-a-version",
			Source: "acme.nonsemver@not-a-version", Installed: true},
	}
	api := fake.api()
	api.BundleUpdateKind = func(source, name, version string) string { return OfficialUpdateRegistry }
	srv := officialPluginTestServer(t, api)

	code, out := callPluginManager(t, srv, string(MethodPluginManagerListBundles), map[string]any{})
	rows := resultRows(t, code, out)
	for _, name := range []string{"acme.absent", "acme.offered", "acme.shipped", "acme.managed", "acme.nonsemver"} {
		row := rowByName(t, rows, name)
		if _, present := row["update"]; present {
			t.Fatalf("%s 不满足官方 update 门槛却带了 update=%v", name, row["update"])
		}
		if row["updatable"] != false {
			t.Fatalf("%s 的兼容字段 updatable 应为 false，实际 %v", name, row["updatable"])
		}
	}
}

// TestListBundlesUpdateWithoutHostHook 钉住"宿主说不出就不许编"：
// 没有 BundleUpdateKind 钩子时一律缺省 update，且旧 updatable 原样保留
// （能力下限：只能更多不能更少）。
func TestListBundlesUpdateWithoutHostHook(t *testing.T) {
	fake := newFakePluginManagerAPI()
	fake.listBundlesRet = []OfficialBundle{
		{Name: "acme.audit", Availability: "profile", Version: "1.0.0",
			Source: "acme.audit@1.0.0", Installed: true, Updatable: true},
	}
	srv := officialPluginTestServer(t, fake.api())

	code, out := callPluginManager(t, srv, string(MethodPluginManagerListBundles), map[string]any{})
	row := rowByName(t, resultRows(t, code, out), "acme.audit")
	if _, present := row["update"]; present {
		t.Fatalf("宿主没给判据时不许编 update，实际 %v", row["update"])
	}
	if row["updatable"] != true {
		t.Fatalf("没有新判据时必须原样保留 rc.1 的 updatable，实际 %v", row["updatable"])
	}
}

// TestInstallBundleExpectedParam 钉住 rc.2 新增的 `options.expected`。
func TestInstallBundleExpectedParam(t *testing.T) {
	fake := newFakePluginManagerAPI()
	srv := officialPluginTestServer(t, fake.api())

	code, _ := callPluginManager(t, srv, string(MethodPluginManagerInstallBundle), map[string]any{
		"spec":    "./private/v1",
		"options": map[string]any{"expected": map[string]any{"name": "acme.audit", "version": "1.0.0"}},
	})
	if code != http.StatusOK {
		t.Fatalf("expected 合法时状态码 %d，期望 200", code)
	}
	if fake.install.Expected == nil {
		t.Fatal("options.expected 没有被解析进宿主参数")
	}
	if fake.install.Expected.Name != "acme.audit" || fake.install.Expected.Version != "1.0.0" {
		t.Fatalf("expected 解析错了：%+v", *fake.install.Expected)
	}

	// 官方 zod 里 expected 是 `additionalProperties: true`：多余的键必须容忍。
	fake2 := newFakePluginManagerAPI()
	srv2 := officialPluginTestServer(t, fake2.api())
	code, out := callPluginManager(t, srv2, string(MethodPluginManagerInstallBundle), map[string]any{
		"spec": "./private/v1",
		"expected": map[string]any{
			"name": "acme.audit", "version": "1.0.0", "reason": "update target",
		},
	})
	if code != http.StatusOK {
		t.Fatalf("expected 的多余键应被容忍（官方 additionalProperties:true），实际 %d %v", code, out)
	}

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"expected 缺 version", map[string]any{"spec": "./private/v1",
			"expected": map[string]any{"name": "acme.audit"}}},
		{"expected 缺 name", map[string]any{"spec": "./private/v1",
			"expected": map[string]any{"version": "1.0.0"}}},
		{"expected.name 是空串", map[string]any{"spec": "./private/v1",
			"expected": map[string]any{"name": "  ", "version": "1.0.0"}}},
		{"expected 不是对象", map[string]any{"spec": "./private/v1",
			"expected": []any{"acme.audit", "1.0.0"}}},
		{"expected 与顶层 expected 重复", map[string]any{"spec": "./private/v1",
			"expected": map[string]any{"name": "acme.audit", "version": "1.0.0"},
			"options":  map[string]any{"expected": map[string]any{"name": "acme.audit", "version": "1.0.0"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakePluginManagerAPI()
			srv := officialPluginTestServer(t, fake.api())
			code, out := callPluginManager(t, srv, string(MethodPluginManagerInstallBundle), tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("状态码 %d，期望 400（body=%v）", code, out)
			}
			envelope, ok := out["error"].(map[string]any)
			if !ok {
				t.Fatalf("错误信封应是 {error:{code,message,hint,retryable}}：%v", out)
			}
			for _, key := range []string{"code", "message", "hint", "retryable"} {
				if _, has := envelope[key]; !has {
					t.Fatalf("四字段缺 %q：%v", key, envelope)
				}
			}
			if envelope["code"] != "bad_request" {
				t.Fatalf("四字段错误码应为 bad_request，实际 %v", envelope["code"])
			}
		})
	}
}

// TestUpdateBundleSpecParam 钉住 rc.2 新增的 `options.spec`（git 更新目标）。
func TestUpdateBundleSpecParam(t *testing.T) {
	gitSpec := "github:acme/widget#" + gitSHA40
	fake := newFakePluginManagerAPI()
	srv := officialPluginTestServer(t, fake.api())

	code, out := callPluginManager(t, srv, string(MethodPluginManagerUpdateBundle), map[string]any{
		"name": "acme.git", "version": "2.0.0",
		"options": map[string]any{"spec": gitSpec},
	})
	if code != http.StatusOK {
		t.Fatalf("options.spec 合法时状态码 %d，期望 200（%v）", code, out)
	}
	if fake.update.Spec == nil || *fake.update.Spec != gitSpec {
		t.Fatalf("options.spec 没有被解析进宿主参数：%+v", fake.update.Spec)
	}
	// 官方 wire 是位置参数：spec 也可以与 registry/requestId 一起给。
	fake2 := newFakePluginManagerAPI()
	srv2 := officialPluginTestServer(t, fake2.api())
	code, out = callPluginManager(t, srv2, string(MethodPluginManagerUpdateBundle), map[string]any{
		"name": "acme.git", "version": "2.0.0",
		"spec": gitSpec, "requestId": "req-7", "registry": nil,
	})
	if code != http.StatusOK {
		t.Fatalf("顶层 spec 也应被接受：%d %v", code, out)
	}
	if fake2.update.Spec == nil || *fake2.update.Spec != gitSpec {
		t.Fatalf("顶层 spec 没有解析进宿主参数：%+v", fake2.update.Spec)
	}
	if fake2.update.RequestID == nil || *fake2.update.RequestID != "req-7" {
		t.Fatalf("requestId 丢了：%+v", fake2.update.RequestID)
	}
	if fake2.update.Registry != nil {
		t.Fatalf("registry:null 应解析成 nil（用配置源），实际 %v", *fake2.update.Registry)
	}

	// 没给 spec 时必须是 nil：registry 行的更新不接受 spec（由宿主按官方口径拒绝）。
	fake3 := newFakePluginManagerAPI()
	srv3 := officialPluginTestServer(t, fake3.api())
	if code, out := callPluginManager(t, srv3, string(MethodPluginManagerUpdateBundle),
		map[string]any{"name": "acme.audit", "version": "2.0.0"}); code != http.StatusOK {
		t.Fatalf("不带 spec 的更新应正常走到宿主：%d %v", code, out)
	}
	if fake3.update.Spec != nil {
		t.Fatalf("没给 spec 时宿主参数必须是 nil，实际 %q", *fake3.update.Spec)
	}

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"spec 是空串", map[string]any{"name": "acme.git", "version": "2.0.0", "spec": ""}},
		{"spec 只有空白", map[string]any{"name": "acme.git", "version": "2.0.0", "spec": "   "}},
		{"spec 不是字符串", map[string]any{"name": "acme.git", "version": "2.0.0", "spec": 42}},
		{"spec 与 options.spec 重复", map[string]any{"name": "acme.git", "version": "2.0.0",
			"spec": gitSpec, "options": map[string]any{"spec": gitSpec}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakePluginManagerAPI()
			srv := officialPluginTestServer(t, fake.api())
			code, out := callPluginManager(t, srv, string(MethodPluginManagerUpdateBundle), tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("状态码 %d，期望 400（body=%v）", code, out)
			}
		})
	}
}

// TestUnexpectedPackageCodeIsCarried 钉住 rc.2 新增错误码在结果里的可表达性：
// 官方把它放在 `ManagementError.code`（ChangeResult.error / BundleInfo.error），
// 不是 HTTP 错误码，所以它必须能原样穿过 result 信封。
func TestUnexpectedPackageCodeIsCarried(t *testing.T) {
	if OfficialCodeUnexpectedPackage != "unexpected-package" {
		t.Fatalf("错误码字面量必须与官方 vitest 一致：%q", OfficialCodeUnexpectedPackage)
	}
	fake := newFakePluginManagerAPI()
	api := fake.api()
	api.InstallBundle = func(p OfficialInstallBundleParams) (OfficialChangeResult, error) {
		fake.install = p
		return OfficialFailedChange("install", p.Spec, OfficialCodeUnexpectedPackage,
			"acme.other@9.9.9 装到了 acme.audit@1.0.0 该在的位置"), nil
	}
	srv := officialPluginTestServer(t, api)
	code, out := callPluginManager(t, srv, string(MethodPluginManagerInstallBundle), map[string]any{
		"spec": "./private/v1",
		"expected": map[string]any{
			"name": "acme.audit", "version": "1.0.0",
		},
	})
	result := resultOf(t, code, out)
	if result["application"] != "failed" {
		t.Fatalf("application 应为 failed，实际 %v", result["application"])
	}
	errObj, ok := result["error"].(map[string]any)
	if !ok {
		t.Fatalf("failed 结果必须带 error 对象：%v", result)
	}
	if errObj["code"] != OfficialCodeUnexpectedPackage {
		t.Fatalf("error.code 应为 %q，实际 %v", OfficialCodeUnexpectedPackage, errObj["code"])
	}
	if errObj["diagnostic"] == nil {
		t.Fatal("error.diagnostic 必须出现：客户端要靠它显示到底装了什么")
	}
}

// TestFourMethodsRc2StructuralAlignment 是 rc.2 四个方法的结构契约对齐表：
// 参数 wire 名、结果字段、错误码逐条钉住（字段名来自官方 typert.host.js）。
func TestFourMethodsRc2StructuralAlignment(t *testing.T) {
	t.Run("cancelInstall 结果 status 三值", func(t *testing.T) {
		for _, status := range []string{"not-running", "cancelled", "too-late"} {
			fake := newFakePluginManagerAPI()
			api := fake.api()
			api.CancelInstall = func(p OfficialRequestIDParams) (OfficialInstallCancellation, error) {
				return OfficialInstallCancellation{Status: status}, nil
			}
			srv := officialPluginTestServer(t, api)
			code, out := callPluginManager(t, srv, string(MethodPluginManagerCancelInstall),
				map[string]any{"requestId": "req-1"})
			result := resultOf(t, code, out)
			if result["status"] != status {
				t.Fatalf("status 应原样穿过：期望 %q，实际 %v", status, result["status"])
			}
		}
	})

	t.Run("inspect 的 refused.problem 取官方 rc.2 枚举", func(t *testing.T) {
		want := []string{
			"unknown", "network", "not-found", "invalid-spec",
			"already-installed", "not-a-package", "not-a-bundle",
		}
		for _, problem := range want {
			fake := newFakePluginManagerAPI()
			api := fake.api()
			api.Inspect = func(p OfficialInspectParams) (OfficialPluginSpecInspection, error) {
				return OfficialInspectionRefused(problem, "理由"), nil
			}
			srv := officialPluginTestServer(t, api)
			code, out := callPluginManager(t, srv, string(MethodPluginManagerInspect),
				map[string]any{"spec": "acme.audit"})
			result := resultOf(t, code, out)
			if result["status"] != "refused" || result["problem"] != problem {
				t.Fatalf("problem 应原样穿过：期望 %q，实际 %v/%v", problem, result["status"], result["problem"])
			}
			if result["reason"] != "理由" {
				t.Fatalf("refused 必须带 reason，实际 %v", result["reason"])
			}
		}
	})

	t.Run("listBundles 行的官方必填字段与兼容字段并存", func(t *testing.T) {
		fake := newFakePluginManagerAPI()
		fake.listBundlesRet = []OfficialBundle{{
			Name: "acme.audit", Availability: "profile", Version: "1.0.0",
			Source: "acme.audit@1.0.0", Installed: true, Removable: true,
		}}
		api := fake.api()
		api.BundleUpdateKind = func(source, name, version string) string { return OfficialUpdateRegistry }
		srv := officialPluginTestServer(t, api)
		code, out := callPluginManager(t, srv, string(MethodPluginManagerListBundles), map[string]any{})
		row := rowByName(t, resultRows(t, code, out), "acme.audit")
		for _, key := range []string{
			"name", "official", "availability", "enabled", "installed", "optional",
			"removable", "rows", "overrides",
			// rc.2 新增 + rc.1 兼容字段
			"update", "updatable",
		} {
			if _, present := row[key]; !present {
				t.Fatalf("listBundles 行缺字段 %q：%v", key, row)
			}
		}
	})
}

// gitSHA40 是一个合法的完整 commit（官方 FULL_COMMIT = 40 位十六进制）。
const gitSHA40 = "0123456789abcdef0123456789abcdef01234567"

// jsonValid 断言一段结果能被 JSON 往返（防止手工拼接出来的非法 JSON）。
func jsonValid(t *testing.T, value any) {
	t.Helper()
	if _, err := json.Marshal(value); err != nil {
		t.Fatalf("结果不可 JSON 序列化：%v", err)
	}
}
