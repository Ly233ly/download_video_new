# Stage 0.5 idle-resident sampling for the OLD app -- frozen 1.6.3 release build.
#
# Lessons baked in from the first two attempts:
#   1) The old app guards itself with a named Win32 mutex
#      ("Local_IdmEagleAutoImport", see single_instance.py). A leftover instance
#      silently makes every later launch exit immediately, so a stale orphan
#      turns the whole measurement into zeros. We therefore assert the mutex is
#      free before starting, and fail loudly if it is not.
#   2) Under --external-tray the real GUI is an ORPHAN (parent bootloader exits
#      after re-exec), so walking down from the launched pid never finds it.
#      We locate the process by its executable path: snapshot before launch,
#      snapshot after, and diff.
#   3) Every sample asserts liveness. A dead app must look like a failed
#      measurement, never like "0% CPU".
#
# ASCII-only on purpose (Windows PowerShell 5.1 mangles BOM-less UTF-8 scripts).

param(
    [int]$Minutes = 10,
    [int]$IntervalSeconds = 10,
    [int]$SettleSeconds = 60,
    [string]$OutDir = (Join-Path $PSScriptRoot 'raw')
)

$ErrorActionPreference = 'Stop'
$cfg = Get-Content (Join-Path $PSScriptRoot 'targets.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$FrozenExe = Join-Path $cfg.frozenRoot $cfg.frozenBackendRel
$ExeDir = Split-Path $FrozenExe -Parent
$DataDir = Join-Path $OutDir 'idle-data'
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class MutexProbe {
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    public static extern IntPtr CreateMutexW(IntPtr attrs, bool initialOwner, string name);
    [DllImport("kernel32.dll", SetLastError=true)]
    public static extern bool CloseHandle(IntPtr h);
    // True when the named mutex already exists (i.e. an instance holds it).
    public static bool Exists(string name) {
        IntPtr h = CreateMutexW(IntPtr.Zero, false, name);
        int err = Marshal.GetLastWin32Error();
        if (h != IntPtr.Zero) CloseHandle(h);
        return err == 183; // ERROR_ALREADY_EXISTS
    }
}
'@

function Get-AppProcesses {
    # Locate by executable path -- immune to orphaned / re-exec'd processes.
    return @(Get-Process -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and $_.Path.StartsWith($cfg.frozenRoot, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    })
}

function Get-Metrics {
    param($Procs)
    if (-not $Procs -or $Procs.Count -eq 0) {
        return [pscustomobject]@{ ProcessCount = 0; LiveIds = ''; CpuSeconds = 0.0; WorkingSetMB = 0.0; PrivateMB = 0.0; Handles = 0; Threads = 0 }
    }
    $cpu = 0.0; $ws = 0L; $priv = 0L; $handles = 0; $threads = 0; $ids = @()
    foreach ($p in $Procs) {
        $cpu += $p.CPU
        $ws += $p.WorkingSet64
        $priv += $p.PrivateMemorySize64
        $handles += $p.HandleCount
        $threads += $p.Threads.Count
        $ids += $p.Id
    }
    return [pscustomobject]@{
        ProcessCount = $Procs.Count
        LiveIds      = ($ids -join ',')
        CpuSeconds   = [math]::Round($cpu, 3)
        WorkingSetMB = [math]::Round($ws / 1MB, 2)
        PrivateMB    = [math]::Round($priv / 1MB, 2)
        Handles      = $handles
        Threads      = $threads
    }
}

# --- preconditions -----------------------------------------------------------
$stale = Get-AppProcesses
if ($stale.Count -gt 0) {
    Write-Host ("!! found {0} pre-existing instance(s); killing them so the mutex is free" -f $stale.Count)
    $stale | ForEach-Object { try { $_.Kill() } catch {} }
    Start-Sleep -Seconds 3
}
if ([MutexProbe]::Exists('Local_IdmEagleAutoImport')) {
    throw 'single-instance mutex is still held -- measurement would be invalid'
}
Write-Host '  precondition OK: no leftover instance, mutex free'

if (Test-Path $DataDir) { Remove-Item $DataDir -Recurse -Force }
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

$before = @(Get-AppProcesses | ForEach-Object { $_.Id })
Write-Host ("=== idle sampling: frozen 1.6.3, {0} min @ {1}s ===" -f $Minutes, $IntervalSeconds)
Write-Host ("  data dir {0}" -f $DataDir)

$env:IDM_EAGLE_DATA_DIR = $DataDir
Start-Process -FilePath $FrozenExe -ArgumentList '--external-tray' -WorkingDirectory $ExeDir | Out-Null

# Resolve the real backend by snapshot diff, but ONLY accept a candidate that is
# still alive after a second poll. The frozen app is a PyInstaller onefile: the
# first process is a bootloader that re-execs and then exits, so a single "new
# pid" observation happily latches onto a corpse. Requiring survival across two
# polls one second apart is what actually identifies the long-lived GUI.
$backend = $null
$deadline = (Get-Date).AddSeconds(120)
$candidateId = -1
$candidateSeenAt = $null
while ((Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 500
    $new = @(Get-AppProcesses | Where-Object { $before -notcontains $_.Id })
    if ($new.Count -eq 0) { continue }
    $top = $new | Sort-Object StartTime | Select-Object -First 1
    if ($candidateId -ne $top.Id) {
        $candidateId = $top.Id
        $candidateSeenAt = Get-Date
        continue
    }
    # same pid still present ~1 s later -> it is the surviving process
    if (((Get-Date) - $candidateSeenAt).TotalSeconds -ge 1) {
        $m = Get-Metrics -Procs @($top)
        Write-Host ("  resolved backend pid {0} (WS {1} MB, handles {2}, alive across 2 polls)" -f $m.LiveIds, $m.WorkingSetMB, $m.Handles)
        $backend = @($top)
        break
    }
}
if (-not $backend) { throw 'could not resolve a surviving backend process' }

Write-Host ("  settling {0} s before the measured window..." -f $SettleSeconds)
Start-Sleep -Seconds $SettleSeconds

$samples = @()
$prev = Get-Metrics -Procs (Get-AppProcesses)
$cpuPrev = $prev.CpuSeconds
$tPrev = Get-Date
$lostAt = -1
$total = [int]($Minutes * 60 / $IntervalSeconds)

for ($i = 1; $i -le $total; $i++) {
    Start-Sleep -Seconds $IntervalSeconds
    $m = Get-Metrics -Procs (Get-AppProcesses)
    $tNow = Get-Date
    $dt = ($tNow - $tPrev).TotalSeconds
    $cpuPct = if ($dt -gt 0) { [math]::Round(100.0 * ($m.CpuSeconds - $cpuPrev) / $dt, 3) } else { 0 }
    if ($m.ProcessCount -eq 0 -and $lostAt -lt 0) {
        $lostAt = $i
        Write-Host '  !! APP DISAPPEARED -- this measurement is INVALID'
    }
    $samples += [pscustomobject]@{
        ElapsedSec   = $i * $IntervalSeconds
        CpuPercent   = $cpuPct
        WorkingSetMB = $m.WorkingSetMB
        PrivateMB    = $m.PrivateMB
        Handles      = $m.Handles
        Threads      = $m.Threads
        ProcessCount = $m.ProcessCount
    }
    $cpuPrev = $m.CpuSeconds; $tPrev = $tNow
    if ($i % 6 -eq 0) {
        Write-Host ("    t={0,5}s CPU={1,6}% WS={2,8} MB priv={3,8} MB handles={4,5} procs={5}" -f `
            ($i * $IntervalSeconds), $cpuPct, $m.WorkingSetMB, $m.PrivateMB, $m.Handles, $m.ProcessCount)
    }
}

$samples | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $OutDir 'idle-samples.frozen.json') -Encoding UTF8

$valid = @($samples | Where-Object { $_.ProcessCount -gt 0 })
$cpuVals = @($valid | ForEach-Object { [double]$_.CpuPercent } | Sort-Object)
$wsVals = @($valid | ForEach-Object { [double]$_.WorkingSetMB })
$hVals = @($valid | ForEach-Object { [int]$_.Handles })
$stats = [pscustomobject]@{
    Target         = 'frozen-1.6.3'
    RequestedMin   = $Minutes
    TotalSamples   = $samples.Count
    ValidSamples   = $valid.Count
    LostAtSample   = $lostAt
    CpuMeanPercent = if ($valid.Count) { [math]::Round(($valid | Measure-Object CpuPercent -Average).Average, 3) } else { $null }
    CpuMaxPercent  = if ($valid.Count) { ($valid | Measure-Object CpuPercent -Maximum).Maximum } else { $null }
    CpuP95Percent  = if ($valid.Count) { $cpuVals[[int][math]::Floor(($cpuVals.Count - 1) * 0.95)] } else { $null }
    WsFirstMB      = if ($valid.Count) { $wsVals[0] } else { $null }
    WsLastMB       = if ($valid.Count) { $wsVals[-1] } else { $null }
    WsMinMB        = if ($valid.Count) { ($wsVals | Measure-Object -Minimum).Minimum } else { $null }
    WsMaxMB        = if ($valid.Count) { ($wsVals | Measure-Object -Maximum).Maximum } else { $null }
    WsGrowthMB     = if ($valid.Count) { [math]::Round($wsVals[-1] - $wsVals[0], 2) } else { $null }
    HandlesFirst   = if ($valid.Count) { $hVals[0] } else { $null }
    HandlesLast    = if ($valid.Count) { $hVals[-1] } else { $null }
    HandlesGrowth  = if ($valid.Count) { $hVals[-1] - $hVals[0] } else { $null }
}
$stats | ConvertTo-Json | Set-Content (Join-Path $OutDir 'idle-stats.frozen.json') -Encoding UTF8

Write-Host ''
Write-Host '=== idle summary ==='
$stats | Format-List

Get-AppProcesses | ForEach-Object { try { $_.Kill() } catch {} }
Start-Sleep -Seconds 2
Write-Host ("leftover instances: {0}" -f (Get-AppProcesses).Count)
