# Instala o agente para todas as sessoes de um servidor Windows/RDS.
# Cada versao fica em um diretorio proprio: sessoes existentes continuam na
# versao anterior, e recebem a nova no proximo logon sem parada global.
#Requires -RunAsAdministrator
param(
    [switch]$Desinstalar,
    [switch]$PermitirNaoAssinado,
    [string]$SistemaUrl,
    [string]$CorsOrigem,
    # Num servidor RDP com o redirecionamento da FabulaTech, a comparacao nao
    # pode acontecer na sessao: o driver ftsjail.sys injeta o ftapihook32 em todo
    # processo dela e a NBioBSP.dll passa a corromper memoria. A sessao 0 nao e
    # alcancada, entao a comparacao vira um servico aqui mesmo.
    # Ver docs/diagnostico-verifymatch-rdp-2026-07-30.md.
    [switch]$InstalarComparador,
    [string]$ComparadorToken,
    [string]$ComparadorUrl = 'http://127.0.0.1:5150',
    [int]$ComparadorPorta = 5150
)

$ErrorActionPreference = 'Stop'

$baseArquivos = ${env:ProgramFiles(x86)}
if (-not $baseArquivos) { $baseArquivos = $env:ProgramFiles }
$destino = Join-Path $baseArquivos 'AgenteBiometria'
$runKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run'
$nomeRun = 'AgenteBiometria'
$nomeTarefa = 'AgenteBiometriaComparador'

function New-TokenComparador {
    # 32 bytes em hexadecimal: 64 caracteres, bem acima do minimo de 32 que os
    # dois lados exigem, e sem caractere que precise de escape em .cmd ou XML.
    $rng = New-Object System.Security.Cryptography.RNGCryptoServiceProvider
    try {
        $bytes = New-Object byte[] 32
        $rng.GetBytes($bytes)
        return (($bytes | ForEach-Object { $_.ToString('x2') }) -join '')
    } finally {
        $rng.Dispose()
    }
}

function Stop-TarefaComparador {
    $tarefa = Get-ScheduledTask -TaskName $nomeTarefa -ErrorAction SilentlyContinue
    if (-not $tarefa) { return }
    Stop-ScheduledTask -TaskName $nomeTarefa -ErrorAction SilentlyContinue
    # Stop-ScheduledTask encerra a tarefa, nao necessariamente o processo que
    # ela deixou para tras - e um comparador vivo continua segurando a porta.
    foreach ($processo in @(Get-AgentesInstalados)) {
        $p = Get-Process -Id $processo.ProcessId -ErrorAction SilentlyContinue
        if ($p -and $p.SessionId -eq 0) {
            Stop-Process -Id $p.Id -Force -Confirm:$false -ErrorAction SilentlyContinue
        }
    }
}

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
    Stop-TarefaComparador
    Unregister-ScheduledTask -TaskName $nomeTarefa -Confirm:$false -ErrorAction SilentlyContinue
    foreach ($nome in @('COMPARADOR_URL', 'COMPARADOR_TOKEN', 'COMPARADOR_PORTA')) {
        [Environment]::SetEnvironmentVariable($nome, $null, 'Machine')
    }
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

if ($InstalarComparador) {
    if (-not $ComparadorToken) { $ComparadorToken = New-TokenComparador }
    if ($ComparadorToken.Length -lt 32) {
        throw 'ComparadorToken precisa de pelo menos 32 caracteres: e a unica barreira do servico.'
    }

    # No ambiente da maquina, e nao no da tarefa: o comparador em sessao 0 e os
    # agentes de cada sessao RDP precisam do MESMO token, e este e o unico lugar
    # que os dois leem. Em troca, qualquer usuario logado consegue ler o token e
    # usar o comparador como oraculo de comparacao - ele nao guarda digital
    # nenhuma, mas confirma um par que o chamador ja tenha em maos.
    [Environment]::SetEnvironmentVariable('COMPARADOR_TOKEN', $ComparadorToken, 'Machine')
    [Environment]::SetEnvironmentVariable('COMPARADOR_URL', $ComparadorUrl, 'Machine')
    [Environment]::SetEnvironmentVariable('COMPARADOR_PORTA', "$ComparadorPorta", 'Machine')
    $env:COMPARADOR_TOKEN = $ComparadorToken
    $env:COMPARADOR_URL = $ComparadorUrl
    $env:COMPARADOR_PORTA = "$ComparadorPorta"

    Stop-TarefaComparador
    $acao = New-ScheduledTaskAction -Execute $exe -Argument '--comparador' -WorkingDirectory $diretorioVersao
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $ajustes = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
    Register-ScheduledTask -TaskName $nomeTarefa -Action $acao -Trigger (New-ScheduledTaskTrigger -AtStartup) `
        -Principal $principal -Settings $ajustes -Force | Out-Null
    Start-ScheduledTask -TaskName $nomeTarefa
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
if ($InstalarComparador) {
    Write-Host "Comparador registrado como tarefa $nomeTarefa (SYSTEM, sessao 0), ouvindo em $ComparadorUrl."
    Write-Host 'A sessao RDP captura; a comparacao roda fora do alcance do gancho da FabulaTech.'
    Write-Host 'Os agentes so passam a delegar no proximo logon, quando leem o ambiente da maquina.'
    Write-Host 'Confira de dentro de uma sessao RDP com: AgenteBiometria.exe --teste-delegacao'
}
if (-not $SistemaUrl) {
    Write-Host 'Como a URL ainda nao foi definida, cada nova origem sera autorizada pela bandeja.'
}
