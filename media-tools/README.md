# 桌面媒体工具

运行 `powershell -ExecutionPolicy Bypass -File packaging/Fetch-FFmpeg.ps1` 下载并校验固定的 FFmpeg Windows essentials 构建。

`ffmpeg.exe` 与 `ffprobe.exe` 是生成的本地/发行资产，受根目录 `.gitignore` 的 `*.exe` 规则排除；版本、来源和哈希写入 `FFMPEG-VERSION.json`。构建发行包前必须存在这两个文件，安装器会把整个 `media-tools` 目录复制到安装根目录。

FFmpeg 官方下载页把 gyan.dev 列为 Windows 构建来源；本项目固定使用 8.1.2 essentials build，并在解压前验证发布方提供的 SHA-256。

YouTube 当前的 SABR 播放响应只提供画质目录而不提供可复用直链。运行 `powershell -ExecutionPolicy Bypass -File packaging/Fetch-YouTube-Resolver.ps1` 可下载并校验固定的 yt-dlp 2026.06.09 与 Deno 2.8.1；它们只在桌面端把用户选择的画质解析为瞬时音视频地址，实际下载与合并仍由 FFmpeg 完成。
