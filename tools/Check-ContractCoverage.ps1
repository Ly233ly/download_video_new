# Contract-ID and acceptance-matrix integrity check.
#
# Three checks:
#   1. Coverage       - every contract defined in docs/02 (B-xxx), docs/07 (P-1xx)
#                       and docs/14 (A-1xx) appears in the coverage column of docs/11.
#   2. Type column    - every row of the docs/11 section-2 matrix carries A / A+M / M.
#   3. Back-reference - every contract ID mentioned in docs/, README or CONTEXT
#                       resolves to a defined ID (catches typos pointing nowhere).
#
# Why this is scripted: docs/11 uses both single (`A-103`) and grouped (`A-101/102`,
# where the suffix replaces the trailing digits) notation. Manual expansion was done
# wrong twice during the 2026-10-02 audit, producing both false positives and false
# negatives. Check 2 exists because `T-UI-08` was found missing its type cell, so 117
# matrix rows carried only 116 type values (`98+17+1 != 117`).
#
# Check 3 deliberately ignores bare `B-1`/`B-2` style tokens (the phase-0.5 baseline
# measurement IDs in docs/baseline.md are not contracts): it only matches `X-1` plus
# two or three digits, i.e. the B-1xx..B-10xx / P-1xx / A-1xx contract namespaces.
#
# ASCII-ONLY on purpose: Windows PowerShell 5.1 decodes BOM-less UTF-8 scripts as
# ANSI, which corrupts non-ASCII source and breaks parsing.
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File tools\Check-ContractCoverage.ps1
# Exit:  0 = all checks pass; 1 = at least one check failed.

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$d11 = Get-Content (Join-Path $root 'docs\11-ACCEPTANCE.md') -Raw -Encoding UTF8

function Expand-Coverage {
    param([string]$Text, [string]$Letter)
    $set = New-Object System.Collections.Generic.HashSet[string]
    $pattern = '\| (' + $Letter + '-\d+(?:/\d+)*) \|'
    foreach ($m in [regex]::Matches($Text, $pattern)) {
        $parts = $m.Groups[1].Value -split '/'
        $head = $parts[0]
        [void]$set.Add($head)
        $dash = $head.IndexOf('-')
        $digits = $head.Substring($dash + 1)
        if ($parts.Count -gt 1) {
            foreach ($suf in $parts[1..($parts.Count - 1)]) {
                if (-not $suf) { continue }
                $keep = $digits.Length - $suf.Length
                [void]$set.Add($Letter + '-' + $digits.Substring(0, $keep) + $suf)
            }
        }
    }
    return $set
}

function Get-Defined {
    param([string]$Path, [string]$Pattern)
    $text = Get-Content $Path -Raw -Encoding UTF8
    $set = New-Object System.Collections.Generic.HashSet[string]
    foreach ($m in [regex]::Matches($text, $Pattern)) { [void]$set.Add($m.Groups[1].Value) }
    return $set
}

$cases = @(
    @{ Letter = 'B'; DefFile = 'docs\02-BEHAVIOR.md';      DefPattern = '(?m)^\| (B-\d+) \|' },
    @{ Letter = 'P'; DefFile = 'docs\07-EXTENSION.md';     DefPattern = '(?m)^\| (P-\d+) \|' },
    # ASCII-only on purpose: the definitions look like "**\u5951\u7ea6 A-101**", but putting that
    # non-ASCII literal in this script breaks under PS 5.1's ANSI decoding (the exact
    # bug this checker was written to stop repeating). Matching the ID plus the
    # trailing bold marker selects the same lines without any non-ASCII literal.
    @{ Letter = 'A'; DefFile = 'docs\14-SITE-ADAPTERS.md'; DefPattern = '(A-1\d\d)\*\*' }
)

$failed = $false
$definedIds = New-Object System.Collections.Generic.HashSet[string]

Write-Host '=== 1. coverage: defined in docs/02, 07, 14 -> covered in docs/11 ==='
foreach ($c in $cases) {
    $defined = Get-Defined -Path (Join-Path $root $c.DefFile) -Pattern $c.DefPattern
    $covered = Expand-Coverage -Text $d11 -Letter $c.Letter
    foreach ($id in $defined) { [void]$definedIds.Add($id) }
    $missing = @($defined | Where-Object { -not $covered.Contains($_) } | Sort-Object)
    $extra = @($covered | Where-Object { -not $defined.Contains($_) } | Sort-Object)

    Write-Host ("{0}: defined {1}, covered {2}" -f $c.Letter, $defined.Count, $covered.Count)
    if ($missing.Count -gt 0) {
        Write-Host ("  MISSING ({0}): {1}" -f $missing.Count, ($missing -join ', '))
        $failed = $true
    } else {
        Write-Host '  missing: none'
    }
    if ($extra.Count -gt 0) {
        Write-Host ("  covered-but-not-defined ({0}): {1}" -f $extra.Count, ($extra -join ', '))
    }
}

Write-Host ''
Write-Host '=== 2. docs/11 section-2 rows: type column present ==='
$allRows = [regex]::Matches($d11, '(?m)^\| (T-[A-Z]+-\d+) \|')
$typedRows = [regex]::Matches($d11, '(?m)^\| (T-[A-Z]+-\d+) \|[^|]*\| (A\+M|A|M) \|')
$typedIds = New-Object System.Collections.Generic.HashSet[string]
foreach ($m in $typedRows) { [void]$typedIds.Add($m.Groups[1].Value) }
$untyped = @()
foreach ($m in $allRows) {
    if (-not $typedIds.Contains($m.Groups[1].Value)) { $untyped += $m.Groups[1].Value }
}
Write-Host ("rows {0}, typed {1}" -f $allRows.Count, $typedRows.Count)
if ($untyped.Count -gt 0) {
    Write-Host ("  MISSING TYPE ({0}): {1}" -f $untyped.Count, ($untyped -join ', '))
    $failed = $true
} else {
    Write-Host '  missing type: none'
}
foreach ($g in ($typedRows | ForEach-Object { $_.Groups[2].Value } | Group-Object | Sort-Object Name)) {
    Write-Host ("  {0} = {1}" -f $g.Name, $g.Count)
}

Write-Host ''
Write-Host '=== 3. back-references: every mentioned contract ID is defined ==='
$files = @()
$files += Get-Item (Join-Path $root 'README.md')
$files += Get-Item (Join-Path $root 'CONTEXT.md')
$files += Get-ChildItem (Join-Path $root 'docs') -Filter '*.md' -File
$files += Get-ChildItem (Join-Path $root 'docs\adr') -Filter '*.md' -File
$badRefs = @()
foreach ($f in $files) {
    $text = Get-Content $f.FullName -Raw -Encoding UTF8
    foreach ($m in [regex]::Matches($text, '\b([BPA]-1\d{2,3})\b')) {
        $id = $m.Groups[1].Value
        if (-not $definedIds.Contains($id)) { $badRefs += ("{0}:{1}" -f $f.Name, $id) }
    }
}
if ($badRefs.Count -gt 0) {
    $uniq = @($badRefs | Sort-Object -Unique)
    Write-Host ("  UNDEFINED REFERENCE ({0}): {1}" -f $uniq.Count, ($uniq -join '; '))
    $failed = $true
} else {
    Write-Host '  undefined references: none'
}

Write-Host ''
if ($failed) { Write-Host 'RESULT: FAIL'; exit 1 }
Write-Host 'RESULT: OK'
exit 0
