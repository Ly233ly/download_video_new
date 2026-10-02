<#
  tools/Check-BrandVersion.ps1 -- gate T-BRAND-03 (B-104; 12 s.8.1).

  Every place that carries the product version must agree exactly:
    1. Go constant         internal/app/version.go               const Version = "x.y.z"
    2. frontend manifest   frontend/package.json                 "version"
    3. extension manifest  extension/manifest.json               "version"
    4. Wails project       wails.json                            "productVersion"

  The extension ships ONE manifest in v1 (Chromium only, 07 s.10 E3), so there
  is no second manifest to compare.

  NOT checked separately: the installer resources. project.nsi carries only
  COMMENTED-OUT default defines (its own header says template replacement does
  not work there), and wails_tools.nsh fills them from ProjectInfo -- i.e. from
  wails.json above. So wails.json IS the installer's version source; comparing
  project.nsi here would compare a comment against the real value.

  Read-only: this script never writes. Exit codes:
    0 = all sources agree
    1 = versions differ
    2 = a source could not be read -- reported as UNVERIFIED, never as pass

  ASCII-only on purpose: Windows PowerShell 5.1 decodes BOM-less .ps1 as ANSI,
  so Chinese text in here would be mangled (see 12 s.1.1). All reads below
  specify UTF-8 explicitly for the same reason -- manifest files contain
  Chinese and would otherwise fail to parse.
#>

$ErrorActionPreference = 'Continue'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$sources = New-Object System.Collections.ArrayList

function Add-Source([string]$name, [string]$path, [string]$pattern) {
    $text = $null
    if (Test-Path $path) {
        try {
            $text = [System.IO.File]::ReadAllText((Resolve-Path $path), [System.Text.Encoding]::UTF8)
        }
        catch {
            $text = $null
        }
    }
    if ($null -eq $text) {
        [void]$sources.Add([pscustomobject]@{ Name = $name; Path = $path; Version = $null; Note = 'unreadable' })
        return
    }
    $m = [regex]::Match($text, $pattern)
    if (-not $m.Success) {
        [void]$sources.Add([pscustomobject]@{ Name = $name; Path = $path; Version = $null; Note = 'version not found' })
        return
    }
    [void]$sources.Add([pscustomobject]@{ Name = $name; Path = $path; Version = $m.Groups[1].Value; Note = '' })
}

Add-Source 'go constant'        'internal/app/version.go'              'const\s+Version\s*=\s*"([^"]+)"'
Add-Source 'frontend manifest'  'frontend/package.json'                '"version"\s*:\s*"([^"]+)"'
Add-Source 'extension manifest' 'extension/manifest.json'              '"version"\s*:\s*"([^"]+)"'
Add-Source 'wails project'      'wails.json'                           '"productVersion"\s*:\s*"([^"]+)"'

Write-Output '== Brand version gate (T-BRAND-03) =='
foreach ($s in $sources) {
    $shown = '<' + $s.Note + '>'
    if ($s.Version) { $shown = $s.Version }
    Write-Output ('  {0,-20} {1,-12} {2}' -f $s.Name, $shown, $s.Path)
}

$missing = @($sources | Where-Object { -not $_.Version })
if ($missing.Count -gt 0) {
    Write-Output ''
    Write-Output ('RESULT: UNVERIFIED - cannot read version from: ' + (($missing | ForEach-Object { $_.Name }) -join ', '))
    exit 2
}

$distinct = @($sources | ForEach-Object { $_.Version } | Sort-Object -Unique)
Write-Output ''
if ($distinct.Count -eq 1) {
    Write-Output ('RESULT: OK - all ' + $sources.Count + ' sources report ' + $distinct[0])
    exit 0
}
Write-Output ('RESULT: FAIL - versions differ: ' + ($distinct -join ' / '))
exit 1
