# Record the film to an mp4, on Windows.
#
#   powershell -File presentation/capture/record.ps1 [-Out freya.mp4]
#
# # Why it takes the screen
#
# The obvious thing is to grab the browser window by its title and leave the
# desktop alone. That was tried and it returns black frames: Chrome hands the
# window's contents to the compositor rather than to its device context, and
# turning the GPU off did not change it. So the window is put in front and the
# desktop is recorded, which means the screen belongs to this for the length of
# the film and one stray notification would land in the picture.
#
# # The slate
#
# ffmpeg cannot be started on the same tick the film starts, so the film flashes
# two frames of white before its first scene and this trims to that flash. The
# same reason a film set claps a board.
#
# The narration is not recorded off the sound card. narrate.py already laid every
# line on the film's own clock, so the track is simply attached, and the picture
# and the voice cannot drift apart.

param(
    [string]$Out = "$PSScriptRoot\freya.mp4",
    [int]$Port = 8712
)

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$film = Join-Path $root "film"
$work = Join-Path $PSScriptRoot "work"
New-Item -ItemType Directory -Force -Path $work | Out-Null

# How long the film runs: the scene durations, plus a dissolve wherever the next
# scene is not a continuation of the same shot. Both are read from the source, so
# this cannot disagree with what actually plays.
$scenes = Get-Content (Join-Path $film "scenes.js") -Raw
$js     = Get-Content (Join-Path $film "film.js") -Raw
$cover  = [int]([regex]::Match($js, 'COVER = (\d+)').Groups[1].Value)
$reveal = [int]([regex]::Match($js, 'REVEAL = (\d+)').Groups[1].Value)
$gap    = $cover + 20 + $reveal + 30

$beats = [regex]::Matches($scenes, 'dur:\s*(\d+)(,\s*keep:\s*true)?')
$total = 0
for ($i = 0; $i -lt $beats.Count; $i++) {
    $total += [int]$beats[$i].Groups[1].Value
    $nextKeeps = ($i + 1 -lt $beats.Count) -and $beats[$i + 1].Groups[2].Success
    if (-not $nextKeeps) { $total += $gap }
}
$dur = [math]::Round($total / 1000.0, 2)
Write-Host "film runs $dur seconds"

# the server the page is played from
$server = Start-Process -PassThru -WindowStyle Hidden python `
    -ArgumentList "-m", "http.server", $Port, "--directory", $root
Start-Sleep -Seconds 2

$chrome = @(
    "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
    "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe",
    "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $chrome) { throw "no Chrome found" }

$profileDir = Join-Path $work "profile"
Remove-Item -Recurse -Force $profileDir -ErrorAction SilentlyContinue

# The recorder starts first and runs long. Chrome comes up inside it, flashes the
# slate, and the lead-in is trimmed off afterwards.
$raw = Join-Path $work "raw.mkv"
$lead = 18        # the window has to finish growing before the film starts
$grab = Start-Process -PassThru -WindowStyle Hidden ffmpeg -ArgumentList @(
    "-hide_banner", "-loglevel", "error", "-y",
    "-f", "gdigrab", "-framerate", "30", "-draw_mouse", "0",
    "-offset_x", "0", "-offset_y", "0", "-video_size", "1920x1080", "-i", "desktop",
    "-t", ($dur + $lead + 3),
    "-c:v", "libx264", "-preset", "ultrafast", "-crf", "14", "-pix_fmt", "yuv420p",
    $raw)

Start-Sleep -Seconds 2
$browser = Start-Process -PassThru $chrome -ArgumentList @(
    "--user-data-dir=$profileDir", "--no-first-run", "--no-default-browser-check",
    "--mute-audio", "--kiosk", "--disable-session-crashed-bubble",
    "--disable-features=Translate", "--window-position=0,0", "--window-size=1920,1080",
    "--app=http://localhost:$Port/film/?mode=film")

# Pin the window above everything else. Without this, one click anywhere puts
# another window in front and the recorder faithfully captures that instead: a
# take was lost to exactly this, with the last minute of the film replaced by a
# browser tab.
Start-Sleep -Seconds 2
Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class Pin {
  [DllImport("user32.dll")] public static extern bool SetWindowPos(
      IntPtr h, IntPtr after, int x, int y, int w, int t, uint flags);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
}
"@
$browser.WaitForInputIdle(5000) | Out-Null
$h = $browser.MainWindowHandle
if ($h -ne [IntPtr]::Zero) {
    [Pin]::SetWindowPos($h, [IntPtr](-1), 0, 0, 1920, 1080, 0x0040) | Out-Null
    [Pin]::SetForegroundWindow($h) | Out-Null
}

Write-Host ""
Write-Host "RECORDING. Do not click, type or move a window for $dur seconds."
Write-Host "Anything that comes to the front lands in the film."
Write-Host ""
$grab.WaitForExit()
Stop-Process -Id $browser.Id -Force -ErrorAction SilentlyContinue
Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue

# Find the slate: the brightest frame in the lead-in is the two white frames.
# ffmpeg's movie= filter takes a filter argument, not a path, so a Windows drive
# letter has to be escaped inside it. Working from the file's own directory and
# naming it relatively avoids the question entirely.
Push-Location $work
$probe = & ffprobe -v error -f lavfi -i "movie=raw.mkv,signalstats" `
    -show_entries "frame_tags=lavfi.signalstats.YAVG" -of csv=p=0 `
    -read_intervals "%+$($lead + 3)"
Pop-Location
$best = $null; $bestY = -1; $frame = 0
foreach ($line in $probe) {
    $y = 0.0
    if (-not [double]::TryParse($line, [ref]$y)) { $frame++; continue }
    if ($y -gt $bestY) { $bestY = $y; $best = $frame / 30.0 }
    $frame++
}
if ($null -eq $best -or $bestY -lt 120) {
    Write-Warning "no slate found (brightest frame was $bestY); trimming to the lead instead"
    $best = $lead
}
$start = [math]::Round($best + 0.10, 3)
Write-Host "slate at $best, cutting from $start"

# Trim to the slate, attach the narration, and write something seekable: a
# keyframe every two seconds and the index at the front, so a player can scrub
# without reading the whole file first.
$voice = Join-Path $film "vo\voice.wav"
$args = @("-hide_banner", "-loglevel", "error", "-y", "-ss", $start, "-i", $raw)
if (Test-Path $voice) { $args += @("-i", $voice) }
$args += @("-t", $dur, "-c:v", "libx264", "-preset", "slow", "-crf", "18",
           "-pix_fmt", "yuv420p", "-g", "60", "-keyint_min", "60",
           "-movflags", "+faststart")
if (Test-Path $voice) { $args += @("-c:a", "aac", "-b:a", "192k", "-shortest") }
$args += $Out
& ffmpeg @args

Remove-Item -Recurse -Force $profileDir -ErrorAction SilentlyContinue
Write-Host "wrote $Out"
Write-Host "the raw grab is kept at $raw, so a re-mux costs nothing"
