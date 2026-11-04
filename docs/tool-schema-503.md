# job_list 无参 Schema 导致的 WPS 503：根因与修复

> 日期：2026-11-04。范围：`cmd/dshd/tool_merged.go`、`cmd/dshd/tool_schema_test.go`、`internal/schema/schema.go`。未改 `modules/exec/bash/tools.go` 的原始 Schema，也未改 magpie/插件安装产物。

## 结论

WPS 灵犀的 503 不是账号、额度或上游抖动。触发形态是：

```json
{"additionalProperties": false}
```

`job_list` 的原始 Schema 本身是完整的：

```json
{"type":"object","properties":{},"additionalProperties":false}
```

但默认 compact 投影把**顶层 `type:"object"`** 与**空 `properties`** 都当成“可省骨架”删掉，最后只剩 `additionalProperties:false`。WPS 的 schema 转换器在开流前拒绝这种形态，插件归一成 `503 upstream_unavailable`。

同一个坏请求依次发给多个账号，所以多个账号“一起 503”；换号、冷却、提并发都不会改变结果，只有换 provider 才能继续。

## 修复

`tightenObject` 不再对无参/空对象删除：

- 顶层 `type:"object"`；
- 空 `properties`。

如果对象 schema 缺失 `properties`，补成 `{}`。非空对象的顶层 `type:"object"` 仍可省，原有压缩收益不变；`additionalProperties:false` 继续保留，它是有内容的约束。

修复后真实 compact 工具表输出：

```text
JOB_LIST_PARAMETERS={"additionalProperties":false,"properties":{},"type":"object"}
JOB_LIST_OK True
```

原始工具定义无需改动；问题只在“模型实际看到的那张表”。

## 证据与命令

新增回归 `TestTightSchemaNoArgKeepsObjectSkeleton`：

- 单点钉住 `job_list` 的 compact 结果必须是 `{"additionalProperties":false,"properties":{},"type":"object"}`；
- 全表遍历 `assembleRegistry(t)` 的 57 个工具，所有无参/空对象都必须保留 `type:"object"` 与 `properties`。

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
export CGO_ENABLED=0
go test ./cmd/dshd -run 'TestTightSchemaNoArgKeepsObjectSkeleton|TestCompactTableKeepsContract|TestTightTableIsAThinWrapper|TestRequiredParamsAreDeclared' -count=1 -v
# PASS

go test ./... -count=1
go vet ./...
go build ./...
# 全部 exit 0
```

实测 compact 表：

```bash
go run ./cmd/dshd tools --tools-mode compact > /tmp/dshd-tools-compact.json
python -c 'import sys,json;t=json.load(sys.stdin);e=next(x for x in t if x["name"]=="job_list");p=e["parameters"];print(p);assert p.get("type")=="object" and p.get("properties")=={}'
```

## 插件侧仍有一个防御缺口（未改安装产物）

当前安装的 `@kevinfx/magpie-wps-lingxi/index.mjs` 的 `sanitizeSchema` 对：

```js
sanitizeSchema({ additionalProperties: false })
```

实际输出：

```json
{"additionalProperties":false,"type":"string"}
```

原因在 `inferSchemaType`：

```js
if (node.properties || node.required || node.additionalProperties) return "object"
```

`additionalProperties:false` 是 falsy，于是落到最后的 `"string"`。这说明“插件会补 type”并不意味着“补对了类型”；即使已补 `type`，缺 `properties` 仍可能被上游拒绝。

建议在社区插件源码 `packages/wps-lingxi` 修：

- 对象判定检查键是否存在，而不是值是否 truthy：
  `if (node.properties || node.required || "additionalProperties" in node) return "object"`；
- 当推断为 `object` 且没有 `properties` 时，补 `properties: {}` 或至少不删原空对象。

本次**没有**直接改 `node_modules`：它不是可维护源码，且改动生效需要重启 magpie；本机铁律禁止会话内重启 magpie。当前 DSH-GO 根修后，我们发出的请求已是完整形态，插件缺口只作为第三方/未来工具的防线。

## 未验与部署

- 没有用真实 WPS 账号重新发请求验证 200；本次验证到“实际 compact 表字节正确”为止。用户提供的路由与重放证据已证明完整形态上游返回 200。
- 当前运行中的 `DSH GO.exe` 不会自动更新。需要由部署流程重新构建/替换二进制；未在会话内杀进程或重启 DSH/magpie。