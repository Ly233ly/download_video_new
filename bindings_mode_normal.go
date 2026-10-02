//go:build !bindings

package main

// bindingsMode 表示本次运行由 Wails 在生成绑定（`wails build` 的 "Generating bindings" 阶段
// 会用 `-tags bindings` 编译并**真的执行 main()**）。
//
// 该模式下绝不能做真实初始化——否则会占用单实例互斥体，导致"程序开着时无法重新构建"。
// 实测依据：2026-10-02 首次 `wails build` 的输出里出现了 Bootstrap 的日志
// `启动完成 component=app event=bootstrapped`，且数据目录被创建。
const bindingsMode = false
