package main

// 官方 0.2.2 L2 wire 的 settings 域投影适配器。
//
// 背景（T02 裁决，2026-11-08）：既有 L1 `POST /api/settings.describe` 的
// `NamespaceView.Secrets` 是 `[]string`，属于本仓 58 方法快照，**冻结不改**。
// 官方 zod 的 `settings/describe` 要求 `secrets: [{path: string[], set: boolean}]`。
// 两者不是同一层，因此走两条 wire：
//
//	L1  POST /api/settings.describe   —— additive 面，字节不变（sections/errors/[]string）
//	L2  POST /api/settings/describe   —— 官方 SettingsDescribeValue（本文件投影）
//
// 业务读取仍只有一份真相：L2 通过 officialwire.LegacyClient 调 L1 已有的
// settings.describe（同一个 settingsform.Service / 同一份设置存储），只在
// 返回值上做官方投影，不另建第二套 describe。
//
// 契约来源（官方 0.2.2-rc.1 app.asar 原件）：
//
//	@deepseek-ai/dsh-api-settings-controller/lib/typert.host.js:24-42
//	  settings/describe => z.object({
//	    writable: boolean,
//	    hasDocument: boolean,
//	    namespaces: z.array(z.object({
//	      autoGenerate: boolean,
//	      ns: string,
//	      schema: json,
//	      value: json,
//	      base?: json,
//	      user?: json,
//	      applies: z.literal("live"),
//	      secrets: z.array(z.object({
//	        path: z.array(z.string()),
//	        set: boolean,
//	      })),
//	      revision: number,
//	    })),
//	  })
//	  parameters: []（无参数）

import (
	"encoding/json"

	"dshgo/internal/officialwire"
	"dshgo/internal/settingsform"
	"dshgo/internal/transport"
)

// officialSettingsDescribeBinding 把官方 L2 `settings/describe` 绑定到既有 L1
// `settings.describe`。
//
// 官方 settings/describe 没有参数（typert.host.js 的 `parameters: []`），
// 所以 descriptor 是空字段集：客户端多传任何键都会被 gateway/arguments-invalid
// 拒绝（例如把官方 Host 内部的 `redactSecrets` 当成 wire 参数）。
//
// 接线点（由 integrator 串行接入，不在本任务写作用域）：
//
//	cmd/dshd/serve_official_wire.go 的 newOfficialWireServer 里、
//	session/events 注册之后：
//
//	  if err := server.Handle("settings/describe", officialSettingsDescribeBinding(client)); err != nil {
//	      return nil, err
//	  }
func officialSettingsDescribeBinding(client *officialwire.LegacyClient) officialwire.Binding {
	return client.Binding("settings.describe", &officialwire.Descriptor{}, decodeSettingsDescribeResult)
}

// decodeSettingsDescribeResult 是 L1 `settings.describe` 结果 → 官方
// `SettingsDescribeValue` 的投影。
//
// L1 的 sections/errors 不上官方 wire；namespaces[].secrets 从 []string
// 转成 []{path,set}，同时把 secret 值从 value/base/user 剥离（见
// transport.OfficialSettingsDescribeResultOf）。解析失败必须响亮：
// 返回空对象会让官方界面把"宿主坏了"显示成"没有设置"。
func decodeSettingsDescribeResult(result json.RawMessage) (any, *officialwire.Error) {
	var described settingsform.DescribeResult
	if err := json.Unmarshal(result, &described); err != nil {
		return nil, officialwire.Errorf(officialwire.CodeInternal,
			"settings.describe 的 L1 结果不是 DescribeResult：%v", err)
	}
	return transport.OfficialSettingsDescribeResultOf(described), nil
}
