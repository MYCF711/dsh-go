// Package storage 是官方 `storage` / `storage-json` / `storage-domain` 三个
// 插件在宿主侧的 Go 实现：具名后端注册表、可替换的 KV 介质（JSON 的
// `single` 与 `per-record` 两种布局）、以及带 schema 校验与变更通知的
// domain 数据表单。
//
// 分层与官方一致，三件事各归各的：
//   - **后端拥有介质**（一个文件树根、一份数据库文件），只做 LoadAll /
//     PutRecord / DeleteRecord / SetGlobal / Close 这一组原语；
//   - **domain 拥有语义**（record schema 校验、写入串行化、变更事件、
//     `invalid-record` 的备份并跳过），它先落盘再改内存，所以读到的值不会
//     领先于介质；
//   - **hub 只是一张名字表**（backend 名字 → 后端，form 名字 → 数据表单），
//     不做 IO，也不做"全局选一个后端"这种决定——哪个 domain 走哪个介质是
//     domain 的 route 配置。
//
// 稳定错误码（`Error.Code`）是给调用方 switch 的唯一契约；message 只用来
// 诊断。官方错误词表原样保留：`backend-not-found` / `form-not-mounted` /
// `duplicate-backend` / `duplicate-mount` / `facet-unsupported` /
// `malformed-medium` / `version-mismatch` / `unknown-state` / `closed` /
// `missing-key` / `invalid-record` / `already-open`。
package storage
