//go:build bindings

package main

// 见 bindings_mode_normal.go：Wails 生成绑定时会执行 main()，此时跳过一切真实初始化。
const bindingsMode = true
