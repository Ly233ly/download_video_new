# Contract-ID coverage check.
#
# Verifies that every contract ID defined in docs/02 (B-xxx), docs/07 (P-1xx) and
# docs/14 (A-1xx) actually appears in the "coverage" column of docs/11.
#
# Why this is scripted: docs/11 uses both single (`A-103`) and grouped
# (`A-101/102`, where the suffix replaces the trailing digits) notation. Manual
# expansion was done wrong twice during the 2026-10-02 audit, producing both
# false positives and false negatives.
#
# ASCII-ONLY on purpose: Windows PowerShell 5.1 decodes BOM-less UTF-8 scripts as
# ANSI, which corrupts non-ASCII source and breaks parsing.
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File tools\Check-ContractCoverage.ps1
# Exit:  0 = every contract is covered; 1 = something is missing.

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
foreach ($c in $cases) {
    $defined = Get-Defined -Path (Join-Path $root $c.DefFile) -Pattern $c.DefPattern
    $covered = Expand-Coverage -Text $d11 -Letter $c.Letter
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

if ($failed) { Write-Host ''; Write-Host 'RESULT: FAIL'; exit 1 }
Write-Host ''
Write-Host 'RESULT: OK'
exit 0
