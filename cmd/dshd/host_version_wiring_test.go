package main

// cmd/dshd 的测试进程不走 `main()`，所以在这里做与 `main()` 第一条语句**同一件注入**
// （同一个 `wireHostVersion()`、同一个 `version` 变量）：
//
//   - 有的用例直接调 plugin API（`serve_plugins_official_test.go` 用
//     `pluginmgr.HostRuntimeVersion` 造请求），没有注入就会走到 fail-closed 分支；
//   - 更重要的是：测试里的宿主必须与真实二进制行为一致，否则"测试绿"证明不了产物对。
func init() { wireHostVersion() }
