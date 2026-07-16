# Instala o agente para todas as sessoes de um servidor Windows/RDS.
# Cada versao fica em um diretorio proprio: sessoes existentes continuam na
# versao anterior, e recebem a nova no proximo logon sem parada global.
#Requires -RunAsAdministrator
param(
    [switch]$Desinstalar,
    [switch]$PermitirNaoAssinado,
    [string]$SistemaUrl,
    [string]$CorsOrigem
)

$ErrorActionPreference = 'Stop'

$baseArquivos = ${env:ProgramFiles(x86)}
if (-not $baseArquivos) { $baseArquivos = $env:ProgramFiles }
$destino = Join-Path $baseArquivos 'AgenteBiometria'
$runKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run'
$nomeRun = 'AgenteBiometria'

function Test-CaminhoInstalado([string]$Path) {
    if (-not $Path) { return $false }
    $base = [IO.Path]::GetFullPath($destino).TrimEnd('\') + '\'
    $alvo = [IO.Path]::GetFullPath($Path)
    return $alvo.StartsWith($base, [StringComparison]::OrdinalIgnoreCase)
}

function Get-AgentesInstalados {
    Get-CimInstance Win32_Process -Filter "Name = 'AgenteBiometria.exe'" `
        -ErrorAction SilentlyContinue | Where-Object {
            $_.ExecutablePath -and (Test-CaminhoInstalado $_.ExecutablePath)
        }
}

function Stop-AgentesDaSessao([uint32]$SessionId) {
    foreach ($processo in @(Get-AgentesInstalados)) {
        $p = Get-Process -Id $processo.ProcessId -ErrorAction SilentlyContinue
        if ($p -and $p.SessionId -eq $SessionId) {
            Stop-Process -Id $p.Id -Force -Confirm:$false -ErrorAction SilentlyContinue
        }
    }
}

function Test-PeX86([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    $reader = [IO.BinaryReader]::new($stream)
    try {
        if ($reader.ReadUInt16() -ne 0x5A4D) { return $false }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0x40 -or $peOffset -gt ($stream.Length - 6)) { return $false }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) { return $false }
        return $reader.ReadUInt16() -eq 0x014C
    } finally {
        $reader.Dispose()
        $stream.Dispose()
    }
}

function Remove-DiretorioInstalacao([string]$Path) {
    if (-not (Test-CaminhoInstalado (Join-Path $Path 'marcador'))) {
        throw "Recusa ao remover caminho fora da instalacao: $Path"
    }
    Remove-Item -LiteralPath $Path -Recurse -Force
}

if ($Desinstalar) {
    Remove-ItemProperty -Path $runKey -Name $nomeRun -ErrorAction SilentlyContinue
    foreach ($processo in @(Get-AgentesInstalados)) {
        Stop-Process -Id $processo.ProcessId -Force -Confirm:$false -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $destino) {
        Remove-DiretorioInstalacao $destino
    }
    Write-Host 'Agente desinstalado. Dados e certificado de cada usuario foram preservados.'
    exit 0
}

$candidatos = @(
    (Join-Path $PSScriptRoot 'AgenteBiometria.exe'),
    (Join-Path $PSScriptRoot '..\..\dist\AgenteBiometria.exe')
)
$origem = $candidatos | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $origem) {
    throw 'AgenteBiometria.exe nao encontrado. Execute Compilar-Go.ps1 primeiro.'
}
$origem = [IO.Path]::GetFullPath($origem)
if (-not (Test-PeX86 $origem)) {
    throw 'AgenteBiometria.exe nao e um executavel Windows x86 (386).'
}

$assinatura = Get-AuthenticodeSignature -LiteralPath $origem
if ($assinatura.Status -ne 'Valid' -and -not $PermitirNaoAssinado) {
    throw "Assinatura Authenticode invalida: $($assinatura.Status). Assine com Assinar.ps1 ou use -PermitirNaoAssinado somente em laboratorio."
}
if ($assinatura.Status -ne 'Valid') {
    Write-Warning "Instalando binario com assinatura $($assinatura.Status)."
}

$hash = (Get-FileHash -LiteralPath $origem -Algorithm SHA256).Hash.ToLowerInvariant()
$diretorioVersao = Join-Path $destino $hash.Substring(0, 16)
$exe = Join-Path $diretorioVersao 'AgenteBiometria.exe'
New-Item -ItemType Directory -Force -Path $diretorioVersao | Out-Null
if (-not (Test-Path -LiteralPath $exe) -or
    (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant() -ne $hash) {
    Copy-Item -LiteralPath $origem -Destination $exe -Force
}

$dllOrigem = Join-Path $PSScriptRoot 'NBioBSP.dll'
if (Test-Path -LiteralPath $dllOrigem) {
    if (-not (Test-PeX86 $dllOrigem)) { throw 'NBioBSP.dll nao e x86 (32 bits).' }
    Copy-Item -LiteralPath $dllOrigem -Destination (Join-Path $diretorioVersao 'NBioBSP.dll') -Force
}

if ($SistemaUrl) {
    [Environment]::SetEnvironmentVariable('SISTEMA_URL', $SistemaUrl, 'Machine')
    $env:SISTEMA_URL = $SistemaUrl
}
if ($CorsOrigem) {
    if ($CorsOrigem -eq '*') { throw 'CORS_ORIGEM nao aceita curinga. Informe origens exatas.' }
    [Environment]::SetEnvironmentVariable('CORS_ORIGEM', $CorsOrigem, 'Machine')
    $env:CORS_ORIGEM = $CorsOrigem
}

Set-ItemProperty -Path $runKey -Name $nomeRun -Value "`"$exe`""

$sessaoAtual = (Get-Process -Id $PID).SessionId
Stop-AgentesDaSessao $sessaoAtual
Start-Process -FilePath $exe -WorkingDirectory $diretorioVersao -WindowStyle Hidden

$ativos = @(Get-AgentesInstalados | ForEach-Object {
    [IO.Path]::GetFullPath($_.ExecutablePath)
})
foreach ($diretorio in @(Get-ChildItem -LiteralPath $destino -Directory -ErrorAction SilentlyContinue)) {
    if ($diretorio.FullName -eq $diretorioVersao) { continue }
    $exeAntigo = [IO.Path]::GetFullPath((Join-Path $diretorio.FullName 'AgenteBiometria.exe'))
    if ($ativos -notcontains $exeAntigo) {
        Remove-DiretorioInstalacao $diretorio.FullName
    }
}

Write-Host "Instalado em $diretorioVersao (windows/386)."
Write-Host 'As demais sessoes RDP serao atualizadas no proximo logon, sem interrupcao.'
if (-not $SistemaUrl) {
    Write-Host 'Como a URL ainda nao foi definida, cada nova origem sera autorizada pela bandeja.'
}
