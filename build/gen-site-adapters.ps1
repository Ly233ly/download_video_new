# 从唯一事实源 adapters/ 生成扩展侧副本 extension/site-adapters.json。
#
# 依据：[14 §4] 契约 A-104（adapters/ 是唯一事实源）、A-105（产物禁止手工编辑）、
# [14 §4.1]（本脚本与 -Check 两用的时机与理由）。
#
# 用法：
#   pwsh -File build/gen-site-adapters.ps1           # 生成（写盘）
#   pwsh -File build/gen-site-adapters.ps1 -Check    # 校验（不写盘，提交门禁第 6 项）
#
# 退出码：0 = 一致 / 已生成；1 = -Check 发现不一致；2 = 环境或声明有问题（跑不起来）。

[CmdletBinding()]
param(
    [switch]$Check
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$sourceDir = Join-Path $repoRoot 'adapters'
$targetPath = Join-Path $repoRoot 'extension/site-adapters.json'
$targetRel = 'extension/site-adapters.json'

function Write-Result([string]$text) { Write-Host "RESULT: $text" }

if (-not (Test-Path $sourceDir)) {
    Write-Result "FAIL - 找不到适配器目录 $sourceDir"
    exit 2
}

# 只扫描一级子目录，与桌面端加载器（internal/adapter 的 Load）保持一致。
$dirs = Get-ChildItem -Path $sourceDir -Directory | Sort-Object Name
if ($dirs.Count -eq 0) {
    Write-Result "FAIL - $sourceDir 下没有任何适配器目录"
    exit 2
}

$entries = New-Object System.Collections.ArrayList

foreach ($dir in $dirs) {
    $file = Join-Path $dir.FullName 'adapter.json'
    if (-not (Test-Path $file)) {
        Write-Result "FAIL - 适配器目录 $($dir.Name) 里没有 adapter.json"
        exit 2
    }

    try {
        $raw = [System.IO.File]::ReadAllText($file, [System.Text.UTF8Encoding]::new($false))
        $adapter = $raw | ConvertFrom-Json
    }
    catch {
        Write-Result "FAIL - 解析 $file 失败：$($_.Exception.Message)"
        exit 2
    }

    if ([string]::IsNullOrWhiteSpace($adapter.id)) {
        Write-Result "FAIL - $file 缺少 id"
        exit 2
    }
    if ($adapter.id -ne $dir.Name) {
        Write-Result "FAIL - $file 的 id 是 '$($adapter.id)'，目录名是 '$($dir.Name)'（契约 A-114 要求两者一致）"
        exit 2
    }

    # 只抽取扩展需要的四组字段（[14 §4] 的字段分配表）：
    # match / identity / capture / title。resolve 与 errors 只有桌面端用。
    $entry = [ordered]@{
        id         = $adapter.id
        name       = $adapter.name
        version    = $adapter.version
        updatedFor = $adapter.updatedFor
        match      = $adapter.match
        identity   = $adapter.identity
        capture    = $adapter.capture
        title      = $adapter.title
    }
    [void]$entries.Add([pscustomobject]$entry)
}

$document = [ordered]@{
    generated = $true
    source    = 'adapters/'
    note      = '本文件由 build/gen-site-adapters.ps1 生成，禁止手工编辑（[14 §4] 契约 A-105）。唯一事实源是 adapters/<id>/adapter.json。'
    adapters  = @($entries)
}

# 手写缩进与换行：ConvertTo-Json 在不同 PowerShell 版本上的缩进与转义不完全一致，
# 而 -Check 要求产物与生成结果**逐字**可比。
function ConvertTo-JsonText($value, [int]$indent) {
    $pad = ' ' * $indent
    $inner = ' ' * ($indent + 2)
    if ($null -eq $value) { return 'null' }
    if ($value -is [bool]) { if ($value) { return 'true' } else { return 'false' } }
    if ($value -is [int] -or $value -is [long] -or $value -is [double] -or $value -is [decimal]) {
        return $value.ToString([System.Globalization.CultureInfo]::InvariantCulture)
    }
    if ($value -is [string]) { return (ConvertTo-JsonString $value) }
    if ($value -is [System.Collections.IDictionary]) {
        $lines = @()
        foreach ($key in $value.Keys) {
            $lines += "$inner$(ConvertTo-JsonString ([string]$key)): $(ConvertTo-JsonText $value[$key] ($indent + 2))"
        }
        if ($lines.Count -eq 0) { return '{}' }
        return "{`n" + ($lines -join ",`n") + "`n$pad}"
    }
    if ($value -is [System.Management.Automation.PSCustomObject]) {
        $map = [ordered]@{}
        foreach ($property in $value.PSObject.Properties) { $map[$property.Name] = $property.Value }
        return (ConvertTo-JsonText $map $indent)
    }
    if ($value -is [System.Collections.IEnumerable]) {
        $items = @()
        foreach ($item in $value) {
            $items += "$inner$(ConvertTo-JsonText $item ($indent + 2))"
        }
        if ($items.Count -eq 0) { return '[]' }
        return "[`n" + ($items -join ",`n") + "`n$pad]"
    }
    return (ConvertTo-JsonString ([string]$value))
}

function ConvertTo-JsonString([string]$text) {
    $builder = New-Object System.Text.StringBuilder
    [void]$builder.Append('"')
    foreach ($ch in $text.ToCharArray()) {
        switch ($ch) {
            '"' { [void]$builder.Append('\"') }
            '\' { [void]$builder.Append('\\') }
            "`n" { [void]$builder.Append('\n') }
            "`r" { [void]$builder.Append('\r') }
            "`t" { [void]$builder.Append('\t') }
            default {
                if ([int]$ch -lt 32) {
                    [void]$builder.Append('\u' + ([int]$ch).ToString('x4'))
                }
                else {
                    [void]$builder.Append($ch)
                }
            }
        }
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

$text = (ConvertTo-JsonText $document 0) + "`n"
$utf8 = [System.Text.UTF8Encoding]::new($false)

if ($Check) {
    if (-not (Test-Path $targetPath)) {
        Write-Result "FAIL - $targetRel 不存在，请先运行 pwsh -File build/gen-site-adapters.ps1"
        exit 1
    }
    $current = [System.IO.File]::ReadAllText($targetPath, [System.Text.UTF8Encoding]::new($false))
    if ($current -ne $text) {
        Write-Result "FAIL - $targetRel 与 adapters/ 不一致（可能被手工改过）。请重新生成后提交"
        exit 1
    }
    Write-Result "OK - $targetRel is up to date with adapters/ ($($entries.Count) adapters)"
    exit 0
}

[System.IO.File]::WriteAllText($targetPath, $text, $utf8)
Write-Result "OK - wrote $targetRel ($($entries.Count) adapters)"
exit 0
