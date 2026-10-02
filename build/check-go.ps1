<#
  build/check-go.ps1 —— [12 §6.2] 门禁第 1 项：Go 格式化与静态检查

  工具链由 [12 §10] 的 Z1 定义：
    格式化      gofmt（标准库，无配置）
    静态检查    go vet + golangci-lint（linter 集见仓库根 .golangci.yml）

  本脚本**只覆盖门禁第 1 项**。第 2 项（全部单元与契约测试）直接跑：
    go test ./internal/... ./

  用法（可从任意目录调用）：
    powershell -ExecutionPolicy Bypass -File build/check-go.ps1

  退出码：
    0  全部通过
    1  有检查未通过
    2  有检查未执行（例如 golangci-lint 未安装）——**不谎报通过**

  ── 为什么包范围是「算出来的」而不是裸 ./... ──────────────────────────────
  frontend/ 是 npm 项目，但 node_modules 里夹带第三方 Go 源码（实测出现
  flatted/golang/pkg/flatted）。裸 `go vet ./...` / `go test ./...` 会下探进去
  编译它们：只要其中一个不能编译，全仓门禁就会以与本项目无关的理由失败。

  **不能**用「在 frontend/ 放嵌套 go.mod」来隔离——`//go:embed` 不允许跨模块
  嵌入，加了之后 main.go 的 `//go:embed all:frontend/dist` 会直接编译失败
  （实测：cannot embed directory frontend/dist: in different module）。
  若把 dist 产出移到别处，是改动架构（[01]），代价更大。

  所以范围只能显式给出：用 `git ls-files '*.go'` 反推本项目自己的包目录——
  以「仓库跟踪的文件」为准，天然排除 node_modules 与一切 gitignore 内容，
  且新增顶层包时**不需要**改这个脚本。gofmt 同理用文件列表而非 -l .。
#>

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$failed = @()
$skipped = @()

Write-Output '== Go 门禁（12 §6.2 第 1 项）=='

# --- 定位 go ---
$go = Get-Command go -ErrorAction SilentlyContinue
if ($null -eq $go) {
    Write-Output 'FAIL  找不到 go 命令'
    Write-Output '      本机 Go 1.27.1 装在 C:\Users\MSI\go-sdk\go，且只有新开的 shell 才在 PATH 里'
    Write-Output '      （CONTEXT.md §3.4）。本脚本刻意不做路径兜底——那会在别的机器上'
    Write-Output '      悄悄用上错误的工具链。'
    exit 2
}
Write-Output ('go    ' + (& go version 2>&1))

# --- 范围：仓库跟踪的 .go 文件 → 文件列表（gofmt）+ 包目录（vet / lint）---
$goFiles = @(& git ls-files '*.go' 2>$null)
if ($goFiles.Count -eq 0) {
    Write-Output 'FAIL  未取到任何 .go 文件（当前目录不是仓库，或 git 不可用）'
    exit 2
}
$pkgDirs = @(
    $goFiles |
        ForEach-Object { Split-Path -Parent $_ } |
        Sort-Object -Unique |
        ForEach-Object { if ([string]::IsNullOrEmpty($_) -or $_ -eq '.') { './' } else { './' + ($_ -replace '\\', '/') } }
)
Write-Output ('范围  ' + $goFiles.Count + ' 个文件 / ' + $pkgDirs.Count + ' 个包：' + ($pkgDirs -join ' '))

# --- 1/3 gofmt ---
$unformatted = @(& gofmt -l $goFiles 2>&1)
if ($unformatted.Count -gt 0) {
    Write-Output 'FAIL  gofmt：以下文件未格式化（用 gofmt -w 修正）'
    $unformatted | ForEach-Object { Write-Output ('        ' + $_) }
    $failed += 'gofmt'
}
else {
    Write-Output ('OK    gofmt（' + $goFiles.Count + ' 个文件）')
}

# --- 2/3 go vet ---
& go vet $pkgDirs 2>&1 | ForEach-Object { Write-Output ('      ' + $_) }
if ($LASTEXITCODE -ne 0) {
    Write-Output 'FAIL  go vet'
    $failed += 'go vet'
}
else {
    Write-Output 'OK    go vet'
}

# --- 3/3 golangci-lint ---
$gcl = Get-Command golangci-lint -ErrorAction SilentlyContinue
if ($null -eq $gcl) {
    Write-Output 'SKIP  golangci-lint：未安装（[12 §10] 的 Z1 要求它参与门禁）'
    Write-Output '      安装：go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest'
    $skipped += 'golangci-lint'
}
else {
    & golangci-lint run $pkgDirs 2>&1 | ForEach-Object { Write-Output ('      ' + $_) }
    if ($LASTEXITCODE -ne 0) {
        Write-Output 'FAIL  golangci-lint'
        $failed += 'golangci-lint'
    }
    else {
        Write-Output 'OK    golangci-lint'
    }
}

# --- 汇总 ---
Write-Output ''
if ($failed.Count -gt 0) {
    Write-Output ('RESULT: FAIL（' + ($failed -join ' · ') + '）')
    Write-Output '        若原因是模块下载超时，见 CONTEXT.md §3.4：本机 User 级 GOPROXY'
    Write-Output '        指向不可达的官方源，需先设 $env:GOPROXY=''https://goproxy.cn,direct'''
    exit 1
}
if ($skipped.Count -gt 0) {
    Write-Output ('RESULT: INCOMPLETE（未执行：' + ($skipped -join ' · ') + '）——不视为通过')
    exit 2
}
Write-Output 'RESULT: OK'
exit 0
