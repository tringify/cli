# Install the Tringify CLI on Windows.
#
#   irm https://raw.githubusercontent.com/tringify/cli/main/install.ps1 | iex
#
# Environment:
#   TRINGIFY_CLI_VERSION  release tag to install (default: latest)
#   TRINGIFY_CLI_BIN      directory for tringify.exe (default: %LOCALAPPDATA%\Programs\tringify)
#
# Windows on Arm runs the x64 build.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'tringify/cli'
$version = if ($env:TRINGIFY_CLI_VERSION) { $env:TRINGIFY_CLI_VERSION } else { 'latest' }
$binDir = if ($env:TRINGIFY_CLI_BIN) { $env:TRINGIFY_CLI_BIN } else { Join-Path $env:LOCALAPPDATA 'Programs\tringify' }

if ($version -eq 'latest') {
  $base = "https://github.com/$repo/releases/latest/download"
} else {
  $base = "https://github.com/$repo/releases/download/$version"
}
$name = 'tringify-windows-amd64'
$archive = "$name.zip"

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("tringify-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
try {
  Write-Host "Downloading $archive ($version)"
  Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $work $archive) -UseBasicParsing
  Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS') -UseBasicParsing

  $expected = $null
  foreach ($line in Get-Content (Join-Path $work 'SHA256SUMS')) {
    $parts = $line -split '\s+', 2
    if ($parts.Count -eq 2 -and ($parts[1] -eq $archive -or $parts[1] -eq "*$archive")) { $expected = $parts[0].ToLower() }
  }
  if (-not $expected) { throw "no checksum published for $archive" }
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $work $archive)).Hash.ToLower()
  if ($expected -ne $actual) { throw "checksum mismatch for $archive" }

  Expand-Archive -Path (Join-Path $work $archive) -DestinationPath $work
  $exe = Join-Path $work "$name\tringify.exe"
  if (-not (Test-Path $exe)) { throw 'unexpected archive layout' }
  New-Item -ItemType Directory -Force -Path $binDir | Out-Null
  Copy-Item -Force $exe (Join-Path $binDir 'tringify.exe')
} finally {
  Remove-Item -Recurse -Force $work
}

$installed = & (Join-Path $binDir 'tringify.exe') version
Write-Host "Installed $installed to $binDir\tringify.exe"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
if (-not (($userPath -split ';') -contains $binDir)) {
  [Environment]::SetEnvironmentVariable('Path', (@($userPath.TrimEnd(';'), $binDir) | Where-Object { $_ }) -join ';', 'User')
  Write-Host "Added $binDir to your PATH. Open a new terminal to run tringify."
}
