# Stage 0.5 baseline measurement of the OLD app -- external process-tree metrics.
#
# Why external: the old app's PerformanceMonitor is an in-memory ring buffer
# (lost on exit), while docs/11 S3 requires the FULL PROCESS TREE for CPU /
# memory / handles. So we measure from outside.
#
# Read-only w.r.t. the old project; writes only under this directory.
# IDM_EAGLE_DATA_DIR redirects the app data dir to a throwaway folder, so the
# user's installed instance data is never touched.
#
# NOTE: ASCII-only on purpose. Windows PowerShell 5.1 decodes BOM-less UTF-8 as
# ANSI, which corrupts non-ASCII source and breaks parsing.
#
# Targets:
#   frozen   = official released 1.6.3 frozen backend  (AUTHORITATIVE baseline)
#   launcher = the C# tray launcher 留底下载器.exe, which spawns the frozen backend
#              (this is the full user double-click path)
#   source   = 1.6.3 sources under pythonw (secondary; lets us reuse the repo's
#              own UI timing fixture which runs in-process)
#
# Usage:
#   ... -File Measure-ColdStart.ps1 -Target frozen   -Count 7
#   ... -File Measure-ColdStart.ps1 -Target launcher -Count 5
#   ... -File Measure-ColdStart.ps1 -Target frozen   -IdleMinutes 10

param(
    [ValidateSet('frozen', 'launcher', 'source')]
    [string]$Target = 'frozen',
    [int]$Count = 7,
    [int]$IdleMinutes = 0,
    [int]$TimeoutSeconds = 120,
    [string]$OutDir = (Join-Path $PSScriptRoot 'raw')
)

$ErrorActionPreference = 'Stop'

# Paths come from a UTF-8 JSON file: Windows PowerShell 5.1 decodes BOM-less
# UTF-8 *scripts* as ANSI, which would corrupt the Chinese component names.
$cfg = Get-Content (Join-Path $PSScriptRoot 'targets.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$OldSrc = $cfg.oldProjectSrc
$FrozenRoot = $cfg.frozenRoot
$FrozenExe = Join-Path $FrozenRoot $cfg.frozenBackendRel
$LauncherExe = Join-Path $FrozenRoot $cfg.frozenLauncherRel
$Py = (Get-Command python).Source
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

switch ($Target) {
    'frozen'   { if (-not (Test-Path $FrozenExe))   { throw "missing frozen backend: $FrozenExe" } }
    'launcher' { if (-not (Test-Path $LauncherExe)) { throw "missing launcher: $LauncherExe" } }
}
Write-Host ("=== target = {0} ===" -f $Target)

Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class Win32 {
    public delegate bool EnumProc(IntPtr hWnd, IntPtr lParam);
    [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc cb, IntPtr p);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowTextW(IntPtr hWnd, StringBuilder s, int n);
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
    [DllImport("user32.dll")] public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint msg, IntPtr wp, IntPtr lp, uint flags, uint timeout, out IntPtr result);

    public static int VisibleTitledWindows(HashSet<uint> pids, out string titles) {
        var sb = new StringBuilder();
        int count = 0;
        EnumWindows(delegate(IntPtr h, IntPtr l) {
            uint pid;
            GetWindowThreadProcessId(h, out pid);
            if (!pids.Contains(pid)) return true;
            if (!IsWindowVisible(h)) return true;
            var t = new StringBuilder(512);
            GetWindowTextW(h, t, t.Capacity);
            if (t.Length == 0) return true;
            count++;
            sb.Append(t.ToString()).Append(" | ");
            return true;
        }, IntPtr.Zero);
        titles = sb.ToString();
        return count;
    }

    public static bool AnyWindowResponding(HashSet<uint> pids) {
        bool ok = false;
        EnumWindows(delegate(IntPtr h, IntPtr l) {
            uint pid;
            GetWindowThreadProcessId(h, out pid);
            if (!pids.Contains(pid)) return true;
            if (!IsWindowVisible(h)) return true;
            var t = new StringBuilder(512);
            GetWindowTextW(h, t, t.Capacity);
            if (t.Length == 0) return true;
            IntPtr res;
            SendMessageTimeout(h, 0, IntPtr.Zero, IntPtr.Zero, 2, 1000, out res);
            ok = true;
            return false;
        }, IntPtr.Zero);
        return ok;
    }
}
'@

function Get-TreeProcesses {
    param([int[]]$RootIds)
    $all = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Select-Object ProcessId, ParentProcessId
    $set = New-Object System.Collections.Generic.HashSet[int]
    foreach ($r in $RootIds) { [void]$set.Add($r) }
    $changed = $true
    while ($changed) {
        $changed = $false
        foreach ($p in $all) {
            if ($set.Contains([int]$p.ParentProcessId) -and -not $set.Contains([int]$p.ProcessId)) {
                [void]$set.Add([int]$p.ProcessId); $changed = $true
            }
        }
    }
    return $set
}

# Locate app processes by executable path. Under --external-tray the frozen app
# re-execs and the real GUI becomes an ORPHAN (its parent bootloader exits), so
# tree walking from the launched pid never reaches it. Path matching is immune
# to that, and to leftover instances from a previous run.
function Get-AppProcesses {
    $prefix = if ($Target -eq 'source') { $null } else { $cfg.frozenRoot }
    return @(Get-Process -ErrorAction SilentlyContinue | Where-Object {
        try {
            if (-not $_.Path) { return $false }
            if ($prefix) { return $_.Path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) }
            return ($_.Path -like '*pythonw.exe')
        } catch { return $false }
    })
}

function Assert-NoLeftovers {
    $stale = Get-AppProcesses
    if ($stale.Count -gt 0) {
        Write-Host ("!! killing {0} leftover instance(s) so the single-instance mutex is free" -f $stale.Count)
        $stale | ForEach-Object { try { $_.Kill() } catch {} }
        Start-Sleep -Seconds 3
    }
}

function Get-TreeMetrics {
    param([System.Collections.Generic.HashSet[int]]$Pids)
    $cpu = 0.0; $ws = 0L; $priv = 0L; $handles = 0; $threads = 0; $alive = 0; $names = @()
    foreach ($id in $Pids) {
        $p = Get-Process -Id $id -ErrorAction SilentlyContinue
        if ($p) {
            $alive++
            $cpu += $p.CPU
            $ws += $p.WorkingSet64
            $priv += $p.PrivateMemorySize64
            $handles += $p.HandleCount
            $threads += $p.Threads.Count
            $names += ("{0}({1})" -f $p.ProcessName, $id)
        }
    }
    [pscustomobject]@{
        ProcessCount = $alive
        CpuSeconds   = [math]::Round($cpu, 3)
        WorkingSetMB = [math]::Round($ws / 1MB, 2)
        PrivateMB    = [math]::Round($priv / 1MB, 2)
        Handles      = $handles
        Threads      = $threads
        ProcessNames = ($names -join ', ')
    }
}

function Stop-OldApp {
    param([int[]]$RootIds)
    $set = Get-TreeProcesses -RootIds $RootIds
    foreach ($id in $set) {
        $p = Get-Process -Id $id -ErrorAction SilentlyContinue
        if ($p) { try { if ($p.MainWindowHandle -ne 0) { [void]$p.CloseMainWindow() } } catch {} }
    }
    Start-Sleep -Milliseconds 1500
    foreach ($id in $set) {
        $p = Get-Process -Id $id -ErrorAction SilentlyContinue
        if ($p) { try { $p.Kill() } catch {} }
    }
    Start-Sleep -Milliseconds 800
}

$dataDir = Join-Path $OutDir 'data'
if (Test-Path $dataDir) { Remove-Item $dataDir -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

$results = @()
$totalRuns = if ($IdleMinutes -gt 0) { 1 } else { $Count }
Write-Host ("=== measuring: {0} run(s); data dir {1} ===" -f $totalRuns, $dataDir)

for ($i = 1; $i -le $totalRuns; $i++) {
    $env:PYTHONPATH = $OldSrc
    $env:IDM_EAGLE_DATA_DIR = $dataDir
    $env:PYTHONUTF8 = '1'

    if ($i -gt 1) {
        Remove-Item $dataDir -Recurse -Force -ErrorAction SilentlyContinue
        New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    }

    $proxyBefore = (Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings').ProxyServer

    $sw = [Diagnostics.Stopwatch]::StartNew()
    $psi = New-Object Diagnostics.ProcessStartInfo
    switch ($Target) {
        'frozen' {
            # Same arguments the shipped launcher uses (BuildBackendStartInfo).
            $psi.FileName = $FrozenExe
            $psi.Arguments = '--external-tray'
            $psi.WorkingDirectory = Split-Path $FrozenExe -Parent
        }
        'launcher' {
            # Full user path: double-click 留底下载器.exe (C# tray host) which
            # spawns the frozen backend. Root pid = launcher, backend is a child.
            $psi.FileName = $LauncherExe
            $psi.Arguments = ''
            $psi.WorkingDirectory = $FrozenRoot
        }
        'source' {
            $pyw = Join-Path (Split-Path $Py -Parent) 'pythonw.exe'
            $psi.FileName = $pyw
            $psi.Arguments = '-m idm_eagle_bridge.main --external-tray'
            $psi.WorkingDirectory = $OldSrc
        }
    }
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $proc = [Diagnostics.Process]::Start($psi)
    $rootPid = $proc.Id

    $firstWindowMs = -1
    $interactiveMs = -1
    $titles = ''
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)

    # Window detection must watch the pid set that actually owns the app. We keep
    # a live path-based snapshot (immune to orphaned / re-exec'd processes) and
    # union it with the launched pid's descendants, so both frozen and source
    # targets are covered.
    $rootPid = $proc.Id
    $seen = New-Object System.Collections.Generic.HashSet[int]
    [void]$seen.Add($rootPid)

    while ((Get-Date) -lt $deadline) {
        foreach ($id in (Get-TreeProcesses -RootIds @($rootPid))) { [void]$seen.Add([int]$id) }
        foreach ($p in (Get-AppProcesses)) { [void]$seen.Add([int]$p.Id) }
        $pidSet = New-Object System.Collections.Generic.HashSet[uint32]
        foreach ($id in $seen) { [void]$pidSet.Add([uint32]$id) }
        $t = ''
        $n = [Win32]::VisibleTitledWindows($pidSet, [ref]$t)
        if ($n -gt 0) {
            if ($firstWindowMs -lt 0) { $firstWindowMs = $sw.Elapsed.TotalMilliseconds; $titles = $t }
            if ([Win32]::AnyWindowResponding($pidSet)) {
                $interactiveMs = $sw.Elapsed.TotalMilliseconds
                $titles = $t
                break
            }
        }
        Start-Sleep -Milliseconds 20
    }
    $sw.Stop()

    foreach ($id in (Get-TreeProcesses -RootIds @($rootPid))) { [void]$seen.Add([int]$id) }
    foreach ($p in (Get-AppProcesses)) { [void]$seen.Add([int]$p.Id) }
    $m = Get-TreeMetrics -Pids $seen
    $proxyAfter = (Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings').ProxyServer

    $row = [pscustomobject]@{
        Run           = $i
        FirstWindowMs = if ($firstWindowMs -ge 0) { [math]::Round($firstWindowMs, 1) } else { $null }
        InteractiveMs = if ($interactiveMs -ge 0) { [math]::Round($interactiveMs, 1) } else { $null }
        Exited        = $proc.HasExited
        ProcessCount  = $m.ProcessCount
        WorkingSetMB  = $m.WorkingSetMB
        PrivateMB     = $m.PrivateMB
        Handles       = $m.Handles
        Threads       = $m.Threads
        WindowTitles  = $titles.Trim()
        ProcessNames  = $m.ProcessNames
        ProxyBefore   = $proxyBefore
        ProxyAfter    = $proxyAfter
        ProxyChanged  = ($proxyBefore -ne $proxyAfter)
    }
    $results += $row
    Write-Host ("  run {0}: firstWindow={1} ms | interactive={2} ms | procs={3} | WS={4} MB | handles={5}" -f `
        $i, $row.FirstWindowMs, $row.InteractiveMs, $row.ProcessCount, $row.WorkingSetMB, $row.Handles)

    if ($IdleMinutes -gt 0) {
        throw 'idle sampling moved to Measure-Idle.ps1 (the inline version re-walked a dead pid and produced all-zero samples). Run that script instead: -File Measure-Idle.ps1 -Minutes N'
    }

    Stop-OldApp -RootIds @($seen)
    Start-Sleep -Seconds 2
}

$results | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $OutDir ("coldstart.{0}.json" -f $Target)) -Encoding UTF8
Write-Host ''
Write-Host ("=== cold start summary [{0}] (process create -> main window visible AND responding) ===" -f $Target)
$ok = $results | Where-Object { $_.InteractiveMs }
if ($ok) {
    $vals = @($ok | ForEach-Object { [double]$_.InteractiveMs } | Sort-Object)
    $idx = { param($a, $q) [int][math]::Floor(($a.Count - 1) * $q) }
    Write-Host ("  n={0}  min={1}  p50={2}  p95={3}  max={4}" -f `
        $vals.Count, $vals[0], $vals[(& $idx $vals 0.50)], $vals[(& $idx $vals 0.95)], $vals[-1])
} else {
    Write-Host '  no successful sample -- inspect the run rows below'
}
$results | Format-Table Run, FirstWindowMs, InteractiveMs, ProcessCount, WorkingSetMB, Handles, ProxyChanged -AutoSize
