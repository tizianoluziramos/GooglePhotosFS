param(
    [Parameter(Mandatory=$true)][string]$Media,
    [string]$Executable = (Join-Path $PSScriptRoot '..\dist\googlephotosfs.exe'),
    [ValidatePattern('^[A-Za-z]:$')][string]$Drive = 'P:',
    [switch]$TrashAfterTest
)
$ErrorActionPreference = 'Stop'
$Executable = (Resolve-Path $Executable).Path
$Media = (Resolve-Path $Media).Path
$Name = 'gphotosfs-validation-' + [guid]::NewGuid().ToString('N') + [IO.Path]::GetExtension($Media)
$MountedFile = Join-Path ($Drive + '\') $Name
$LocalCopy = Join-Path ([IO.Path]::GetTempPath()) $Name
function Invoke-FS {
    param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Arguments)
    $result = & $Executable @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Falló googlephotosfs $Arguments" }
    $result
}
Invoke-FS mount $Drive
try {
    Get-ChildItem -LiteralPath ($Drive + '\') | Out-Null
    Copy-Item -LiteralPath $Media -Destination $MountedFile
    $deadline = (Get-Date).AddMinutes(3)
    $metadata = $null
    while (!$metadata -and (Get-Date) -lt $deadline) {
        $result = & $Executable stat $Name 2>$null
        if ($LASTEXITCODE -eq 0) { $metadata = $result | ConvertFrom-Json }
        else { Start-Sleep -Seconds 3 }
    }
    if (!$metadata -or !$metadata.id) { throw "La nube no publicó $Name. Revisá el registro del montaje." }
    Copy-Item -LiteralPath $MountedFile -Destination $LocalCopy
    if ((Get-FileHash -Algorithm SHA256 $Media).Hash -ne (Get-FileHash -Algorithm SHA256 $LocalCopy).Hash) {
        throw 'Los bytes de la descarga difieren del origen'
    }
    $expected = (Get-FileHash -Algorithm SHA1 $Media).Hash
    if ($metadata.sha1 -and $metadata.sha1 -ne $expected) { throw 'El SHA-1 del índice difiere' }
    $stream = [IO.File]::OpenRead($MountedFile)
    try {
        $first = $stream.ReadByte()
        [void]$stream.Seek(-1, [IO.SeekOrigin]::End)
        [void]$stream.ReadByte()
        [void]$stream.Seek(0, [IO.SeekOrigin]::Begin)
        if ($stream.ReadByte() -ne $first) { throw 'Falló la lectura fuera de orden' }
    } finally { $stream.Dispose() }
    Invoke-FS set-description $Name 'GooglePhotosFS validation'
    $meta = Invoke-FS metadata $Name | ConvertFrom-Json
    if ($meta.description -ne 'GooglePhotosFS validation') { throw 'La descripción solicitada no aparece en el índice' }
    Invoke-FS about
    if ($TrashAfterTest) {
        Remove-Item -LiteralPath $MountedFile
        Invoke-FS sync
        if (Test-Path -LiteralPath $MountedFile) { throw 'El archivo todavía aparece en el montaje' }
        $result = & $Executable stat $Name 2>$null
        if ($LASTEXITCODE -eq 0) { throw "El archivo todavía aparece en el índice: $result" }
    }
    Write-Host "Validación completada para $Name"
} finally {
    Remove-Item -LiteralPath $LocalCopy -ErrorAction SilentlyContinue
    Invoke-FS unmount $Drive
}
