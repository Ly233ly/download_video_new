package app

// Version 是产品版本号（B-104）。**这是 Go 侧的唯一出处。**
//
// 12 §8.1 规定版本出现在五处，它们必须完全一致，由
// `tools/Check-BrandVersion.ps1` 在提交前机械校验（门禁 T-BRAND-03）：
//
//	Go 常量            internal/app/version.go（本文件）
//	前端清单           frontend/package.json 的 version
//	扩展清单           extension/manifest.json 的 version（第一版只有这一份，07 §10 的 E3）
//	Wails 项目配置     wails.json 的 info.productVersion（注入给 exe 与安装器）
//	安装器资源         build/windows/installer/project.nsi 的 INFO_PRODUCTVERSION 默认值
//
// 改这里必须同步其余四处——脚本会挡住不一致，但它挡不住"只改一处"的企图。
const Version = "2.0.0"

// WindowTitle 返回窗口标题（B-103）。
//
// 测试构建带 "[测试版]" 后缀、正式版不带——差异由 buildLabel 用构建标签
// （`-tags testbuild`）分开，而不是靠运行期判断：标题是给人看的身份标识，
// 不该在运行时变来变去。
func WindowTitle() string {
	return ProductName + " v" + Version + buildLabel
}
