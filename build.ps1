param(
    [string]$VersionOverride
)

$ErrorActionPreference = "Stop"

# ir para a raiz do repo (mesmo se correres o script de outra pasta)
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

Write-Host "== sharepoint-client build =="

# descobrir versão
$mainPath = Join-Path $repoRoot "cmd\sharepoint-client\main.go"
$mainSrc  = Get-Content $mainPath -Raw

$version = $VersionOverride
if (-not $version) {
    if ($mainSrc -match 'const\s+Version\s*=\s*"([^"]+)"') {
        $version = $matches[1]
    } else {
        $version = "dev"
    }
}

Write-Host "Version detected: $version"

# sanity check Go toolchain
Write-Host "Go toolchain:"
go version

Write-Host "`nRunning go vet..."
go vet ./...

# preparar pasta dist
$distDir = Join-Path $repoRoot "dist"
if (!(Test-Path $distDir)) {
    New-Item -ItemType Directory -Path $distDir | Out-Null
}

# build do binário
$binPath = Join-Path $distDir "sharepoint-client.exe"
Write-Host "`nBuilding -> $binPath"
go build -o $binPath ./cmd/sharepoint-client

# info do binário
$fileInfo  = Get-Item $binPath
$sizeBytes = $fileInfo.Length
$sizeMB    = [math]::Round($sizeBytes / 1MB, 2)

$sha256 = (Get-FileHash $binPath -Algorithm SHA256).Hash

Write-Host ""
Write-Host "Build OK!"
Write-Host "  Path      : $binPath"
Write-Host "  Version   : $version"
Write-Host "  Size      : $sizeMB MB ($sizeBytes bytes)"
Write-Host "  SHA256    : $sha256"
Write-Host ""
Write-Host "Dá este binário a outra pessoa + o teu private.json (deles), e já conseguem usar."

# Como usar:
# powershell -ExecutionPolicy Bypass -File .\build.ps1
#
# Se um dia quiseres forçar uma versão custom sem editar o código:
# powershell -ExecutionPolicy Bypass -File .\build.ps1 -VersionOverride "v1.0.1-beta"

