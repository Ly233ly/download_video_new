//go:build testbuild

package app

// buildLabel 是正式版与测试构建的唯一差异（B-103）。
//
// 用构建标签而不是运行期开关：`wails build -tags testbuild` 出来的测试包
// 与正式包在**任何**环境下都不该互相伪装。
const buildLabel = " [测试版] by阿毅i"
