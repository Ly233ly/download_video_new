# ADR-002 · 桌面端采用 Go + Wails + WebView2

- **状态**：已接受
- **相关**：[01 架构](../01-ARCHITECTURE.md)、[09 界面](../09-UI.md)

## 背景

重写需同时满足：流畅不卡顿、界面美观、用户无负面影响（体积与内存可接受）。其中最重的负载是**本机 HTTPS 中间人代理**与**大量并发下载**。

## 决策

| 层 | 选型 |
| --- | --- |
| 后端 | Go |
| 桌面壳 | Wails |
| 渲染 | 系统 WebView2 |
| 前端 | TypeScript + Vite + Tailwind |
| 数据库 | SQLite（`modernc.org/sqlite`，纯 Go） |

## 理由

**Go**

- MITM 代理是标准库强项（`crypto/tls` + `net/http` + `httputil`）
- goroutine + channel 天然适配并发下载，无需线程池
- 单文件静态编译，**不要求用户安装任何运行时**
- 同领域（视频号下载）已有成熟实现，降低最难部分的风险

**Wails + WebView2**

- 使用系统 WebView2，安装包与内存远低于 Electron
- 前端可用完整 Web 技术栈，UI 天花板远高于 Tkinter 手绘
- 相比 WinUI 3，**不依赖 Windows App SDK**——后者安装失败会导致程序无法启动

## 后果

**正面**：体积、内存、启动均在 Electron 之下；界面现代化成本转为纯前端问题。

**负面**：Win10 需部署 WebView2；前后端两套语言，绑定层是新复杂度（见 [04](../04-INTERFACES.md)）；主题需在首帧前确定否则闪烁。

## 替代方案

| 方案 | 否决理由 |
| --- | --- |
| Electron | 包 150 MB+、内存 200 MB+ |
| Rust + Tauri | 性能不劣，但本场景共享可变状态多，开发成本显著更高 |
| C# + WinUI 3 | 依赖 Windows App SDK，装不上即无法启动 |
| C# + WPF | 可行，但 UI 现代化成本高于 Web |
| Python + PySide6 | 只解决根因 A |
