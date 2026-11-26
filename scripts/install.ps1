# rchrome installer (Windows). https://github.com/ceeyang/rchrome
#
#   irm https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.ps1 | iex
#   $env:RCHROME_RUN='serve'; irm https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.ps1 | iex
#
# Env: RCHROME_VERSION=v1.2.3 (default latest), RCHROME_INSTALL_DIR=C:\some\dir, RCHROME_RUN='serve --headless'
$ErrorActionPreference = 'Stop'
$repo = 'ceeyang/rchrome'
$version = if ($env:RCHROME_VERSION) { $env:RCHROME_VERSION } else { 'latest' }
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "不支持的架构 $($env:PROCESSOR_ARCHITECTURE) / unsupported arch" }
}
$file = "rchrome_windows_$arch.zip"
$base = if ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/$version" }
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("rchrome-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}
  Write-Host "↓ $base/$file"
  Invoke-WebRequest -Uri "$base/$file" -OutFile (Join-Path $tmp $file) -UseBasicParsing
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing
  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match ("\s" + [regex]::Escape($file) + "$") } | Select-Object -First 1
  $want = if ($line) { ($line -split '\s+')[0].ToLower() } else { '' }
  $got = (Get-FileHash (Join-Path $tmp $file) -Algorithm SHA256).Hash.ToLower()
  if (-not $want -or $want -ne $got) { throw '校验失败 / checksum mismatch' }
  Expand-Archive -Path (Join-Path $tmp $file) -DestinationPath $tmp -Force
  $dir = if ($env:RCHROME_INSTALL_DIR) { $env:RCHROME_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'rchrome' }
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $exe = Join-Path $dir 'rchrome.exe'
  if (Test-Path $exe) { Remove-Item $exe -Force -ErrorAction SilentlyContinue }
  Copy-Item (Join-Path $tmp 'rchrome.exe') $exe -Force
  Unblock-File $exe -ErrorAction SilentlyContinue
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not (($userPath -split ';') -contains $dir)) {
    [Environment]::SetEnvironmentVariable('Path', ($dir + ';' + $userPath).TrimEnd(';'), 'User')
  }
  if (-not (($env:Path -split ';') -contains $dir)) { $env:Path = "$dir;$env:Path" }
  $ver = (& $exe version 2>$null | Select-Object -First 1) -split ' ' | Select-Object -First 1
  Write-Host "rchrome $ver  windows/$arch  → $exe   ✓ 校验通过"
  if ($env:RCHROME_RUN) {
    Write-Host ''
    & $exe @($env:RCHROME_RUN -split ' ')
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
