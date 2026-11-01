# Install the Toget local node on Windows.
#
#   powershell -c "irm https://toget.vu/install.ps1 | iex"
#
# Environment:
#   TOGET_VERSION       install a specific version instead of the latest
#   TOGET_INSTALL_DIR   directory for toget.exe
#   TOGET_BASE_URL      download origin (default: https://toget.vu)
#
# The archive is verified against its SHA-256 checksum before anything is
# installed. The install directory is added to the current user's PATH.

#Requires -Version 5.1
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$BaseUrl = if ($env:TOGET_BASE_URL) { $env:TOGET_BASE_URL } else { "https://toget.vu" }
$Version = $env:TOGET_VERSION
$InstallDir = $env:TOGET_INSTALL_DIR

function Fail($Message) {
  throw "toget install: $Message"
}

if ($BaseUrl -notmatch '^https://' -and $BaseUrl -notmatch '^http://(localhost|127\.0\.0\.1|\[::1\])(:|\/|$)') {
  Fail "TOGET_BASE_URL must use https (loopback http is allowed for testing)"
}
$BaseUrl = $BaseUrl.TrimEnd('/')

$Architecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } elseif ($env:PROCESSOR_ARCHITECTURE) { $env:PROCESSOR_ARCHITECTURE } else { "AMD64" }
switch ($Architecture.ToUpperInvariant()) {
  "AMD64" { $Arch = "amd64" }
  "ARM64" { $Arch = "arm64" }
  default { Fail "unsupported processor architecture: $Architecture (supported: x64, ARM64)" }
}

$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("toget-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
try {
  if (-not $Version) {
    $latestPath = Join-Path $Tmp "latest"
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/downloads/latest" -OutFile $latestPath
    $Version = (Get-Content -Raw -Path $latestPath).Trim()
    if (-not $Version) { Fail "the release server did not return a version" }
  }
  if ($Version -notmatch '^[0-9A-Za-z._-]+$') { Fail "invalid version: $Version" }

  $Artifact = "toget_${Version}_windows_${Arch}.zip"
  $Archive = Join-Path $Tmp $Artifact
  Write-Host "Downloading Toget $Version for windows/$Arch..."
  Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/downloads/$Artifact" -OutFile $Archive

  $ChecksumPath = Join-Path $Tmp "checksum"
  Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/downloads/$Artifact.sha256" -OutFile $ChecksumPath
  $Expected = ((Get-Content -Raw -Path $ChecksumPath).Trim() -split '\s+')[0].ToLowerInvariant()
  if ($Expected -notmatch '^[0-9a-f]{64}$') { Fail "the release server returned a malformed checksum" }
  $Actual = (Get-FileHash -Algorithm SHA256 -Path $Archive).Hash.ToLowerInvariant()
  if ($Expected -ne $Actual) { Fail "checksum mismatch for $Artifact; the download may be incomplete or altered" }

  $Unpack = Join-Path $Tmp "unpack"
  Expand-Archive -Path $Archive -DestinationPath $Unpack -Force
  $Source = Join-Path $Unpack "toget.exe"
  if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) { Fail "the archive does not contain toget.exe" }

  if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\Toget"
  }
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $Target = Join-Path $InstallDir "toget.exe"
  $Staged = Join-Path $InstallDir (".toget.new." + [Guid]::NewGuid().ToString("N") + ".exe")
  Copy-Item -LiteralPath $Source -Destination $Staged -Force
  if ($env:OS -eq "Windows_NT") {
    Unblock-File -LiteralPath $Staged -ErrorAction SilentlyContinue
  }
  Move-Item -LiteralPath $Staged -Destination $Target -Force

  $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
  $Parts = @()
  if ($UserPath) { $Parts = @($UserPath -split ';' | Where-Object { $_ }) }
  if ($Parts -notcontains $InstallDir) {
    [Environment]::SetEnvironmentVariable("Path", (($Parts + $InstallDir) -join ';'), "User")
  }
  if (($env:Path -split ';') -notcontains $InstallDir) {
    $env:Path = "$InstallDir;$env:Path"
  }

  & $Target version | Out-Null
  if ($LASTEXITCODE -ne 0) { Fail "the installed binary could not run" }

  Write-Host ""
  Write-Host "Toget $Version is installed at $Target"
  Write-Host "Open a new terminal if 'toget' is not recognized yet."
  Write-Host ""
  Write-Host "Start it:"
  Write-Host "  toget serve"
  Write-Host ""
  Write-Host "The local page opens at http://toget.localhost:7718"
}
finally {
  Remove-Item -LiteralPath $Tmp -Recurse -Force -ErrorAction SilentlyContinue
}
