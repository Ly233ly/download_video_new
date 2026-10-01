# Stage 0.5 UI timing for the OLD app, reusing the old project's OWN fixtures.
#
# Why reuse instead of writing our own:
#   tests/visual_ui_fixture.py already drives MainWindow and emits repeatable
#   UI timings via --metrics-json (startup, per-page build, refresh warm/forced,
#   page switch, wheel dispatch, resize, widget counts). Rebuilding that harness
#   would be exactly the "reinvent the wheel" the project forbids.
#
# Read-only w.r.t. the old project: it only *runs* the fixture from that path.
#
# IMPORTANT CAVEAT -- recorded so nobody mistakes these numbers for process
# metrics: the fixture's memory fields come from tracemalloc, which measures the
# PYTHON HEAP, not the process working set. The acceptance spec (docs/11 S3)
# requires the FULL PROCESS TREE, which is why memory/handles come from
# Measure-ColdStart.ps1 / Measure-Idle.ps1 instead. From this fixture we take
# only the TIMING fields.
#
# ASCII-only on purpose (Windows PowerShell 5.1 mangles BOM-less UTF-8 scripts).

param(
    [ValidateSet('standard', 'stress')]
    [string]$Scenario = 'stress',
    [int]$Iterations = 20,
    [string]$Page = 'media',
    [string]$OutDir = (Join-Path $PSScriptRoot 'raw-ui')
)

$ErrorActionPreference = 'Stop'
$cfg = Get-Content (Join-Path $PSScriptRoot 'targets.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$OldProject = Split-Path $cfg.oldProjectSrc -Parent
$Fixture = Join-Path $OldProject 'tests\visual_ui_fixture.py'
if (-not (Test-Path $Fixture)) { throw "missing fixture: $Fixture" }

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$metricsPath = Join-Path $OutDir ("ui-metrics.{0}.{1}.json" -f $Scenario, $Page)
if (Test-Path $metricsPath) { Remove-Item $metricsPath -Force }

# Keep the fixture away from the real user data dir as well.
$env:PYTHONPATH = $cfg.oldProjectSrc
$env:PYTHONUTF8 = '1'

$py = (Get-Command python).Source
$args = @(
    $Fixture,
    '--page', $Page,
    '--scenario', $Scenario,
    '--metrics-json', $metricsPath,
    '--metrics-iterations', $Iterations
)

Write-Host ("=== UI timing: scenario={0} page={1} iterations={2} ===" -f $Scenario, $Page, $Iterations)
Write-Host ("  fixture: {0}" -f $Fixture)
Write-Host ("  output : {0}" -f $metricsPath)

$sw = [Diagnostics.Stopwatch]::StartNew()
& $py @args 2>&1 | ForEach-Object { Write-Host ("  | " + $_) }
$code = $LASTEXITCODE
$sw.Stop()
Write-Host ("  fixture exit={0} in {1:N1}s" -f $code, $sw.Elapsed.TotalSeconds)

if (-not (Test-Path $metricsPath)) {
    throw "fixture produced no metrics file (exit=$code). It may need an interactive desktop session."
}

Write-Host ''
Write-Host '=== metrics (timing fields) ==='
$m = Get-Content $metricsPath -Raw -Encoding UTF8 | ConvertFrom-Json
$m | ConvertTo-Json -Depth 6
