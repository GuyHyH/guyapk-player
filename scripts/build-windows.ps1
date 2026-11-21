# Builds dist\GuyAPK-<version>-Windows-x64-Setup.exe and a portable exe.
param([string]$Version = "1.1.0")
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
New-Item -ItemType Directory -Force dist | Out-Null

function Invoke-Checked { param([scriptblock]$Block) & $Block; if ($LASTEXITCODE -ne 0) { throw "Build step failed: $Block" } }

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

# Icon, version info and the DPI-aware manifest.
Invoke-Checked { go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --icon build/appicon.png --manifest gui `
  --product-name GuyAPK --file-description "GuyAPK" --product-version $Version --file-version $Version --original-filename GuyAPK.exe --out rsrc }

$flags = "-s -w -H windowsgui -buildid= -X main.Version=$Version"
Invoke-Checked { go build -trimpath -tags "desktop,production" -ldflags $flags -o dist/GuyAPK.exe . }

Invoke-Checked { go run ./installer/pack dist/GuyAPK.exe installer/payload.xz }
Set-Content -Path installer/version.txt -Value $Version -NoNewline -Encoding ascii
Copy-Item rsrc_windows_amd64.syso installer/rsrc_windows_amd64.syso -Force
$setup = "dist/GuyAPK-$Version-Windows-x64-Setup.exe"
Invoke-Checked { go build -trimpath -ldflags "-s -w -H windowsgui -buildid=" -o $setup ./installer }

Remove-Item installer/rsrc_windows_amd64.syso, rsrc_windows_amd64.syso -ErrorAction SilentlyContinue
Get-Item dist/GuyAPK.exe, $setup | ForEach-Object { "{0,-44} {1,7:N2} MB" -f $_.Name, ($_.Length / 1MB) }
