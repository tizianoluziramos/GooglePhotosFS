param(
    [string]$OutputDirectory = (Join-Path $PSScriptRoot 'dist'),
    [switch]$SkipTests
)
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
    if (!$SkipTests) {
        & go test -buildvcs=false ./...
        if ($LASTEXITCODE -ne 0) { throw 'Fallaron las pruebas' }
    }
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    New-Item -ItemType Directory -Force $OutputDirectory | Out-Null
    $Binary = Join-Path $OutputDirectory 'googlephotosfs.exe'
    & go build -buildvcs=false -trimpath '-ldflags=-s -w' -o $Binary ./src
    if ($LASTEXITCODE -ne 0) { throw 'Falló la compilación' }
    Copy-Item README.md, VALIDACION.md, NOTICE, googlephotosfs.toml -Destination $OutputDirectory -Force
    Copy-Item licenses -Destination $OutputDirectory -Recurse -Force
} finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
    Pop-Location
}
