# 🔍 Revisão técnica do sistema — 2026-08-21

> ⚠️ **`main` continua em `26c9379`, de 2026-08-06.** Os PRs **#10** a **#23**
> seguem abertos e nenhum crítico foi tocado. É o **décimo quinto dia
> consecutivo** sem um commit de correção. `git diff origin/main...HEAD` é vazio:
> o código revisado hoje é **byte a byte** o mesmo das revisões anteriores, então
> os 28 críticos abertos continuam válidos por construção.

As revisões anteriores entraram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos caminhos
de exceção, pelo resíduo em disco, pela cadeia de entrega, pela convivência de
mais de uma instância, pelos caminhos de recuperação, pelos números do sistema,
pela evidência que sobra depois do atendimento e — ontem — pelo **despacho**, o
estágio que decide qual dos seis papéis o executável vai assumir.

Esta foi pelo eixo simétrico ao de ontem: **a parada**. Não o arranque, mas o
desligamento — o que cada um dos seis papéis faz no caminho de saída, e o que ele
**deixa para trás** quando some. O eixo se justifica porque este sistema tem
estado fora do processo em três lugares (o anúncio em `ProgramData`, o arquivo de
descoberta em `%LOCALAPPDATA%` e a loja Raiz do usuário) e **nenhum deles tem
dono na hora de parar**: quem grava é o processo, e quem apaga deveria ser ele
também — só que quase nunca é ele que decide quando morre.

O resultado são **dois críticos novos**, e os dois são do mesmo tipo: um artefato
que sobrevive à desinstalação e reativa, sozinho, um caminho que já não existe
mais. O primeiro derruba a biometria da máquina inteira; o segundo deixa um
certificado confiável de `localhost` — e a chave privada dele — em pé por até
dois anos depois de o produto ter sido removido.

Além deles, **quatro alertas** e **quatro sugestões**.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, das quais 1.055 em 7
arquivos de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/build-msi.cmd`,
`instalador/msi/AgenteBiometria.wixproj`, `conferir-biometria.cmd`,
`embutir-icone.py`, `.gitignore`, `go.mod`/`go.sum`, `README.md` e `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `gofmt -l .` | **nada a formatar** |
| `go test ./...` (alvo do host) | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo |
| `GOOS=windows GOARCH=386 go test ./...` | **`exec format error`** — compila e não roda. As **48** funções `Test*` continuam sem executar em lugar nenhum. Segue valendo o **A8 de 30/07** |
| `git log -1 origin/main` | `26c9379`, de **2026-08-06** (15 dias) |
| `git diff origin/main...HEAD` | **vazio** — nenhuma mudança de código nesta revisão |
| `grep -rn "\.PID\|\.Desde" *.go` | **nenhuma leitura** — o anúncio publica PID e horário e ninguém os consulta. Base do **C1** |
| `grep -rn "delstore\|ProgramData" instalador/instalar-servidor.ps1` | **zero ocorrências** das duas. Base do **C1** e do **C2** |
| Leitura de `svc.Run` em `x/sys@v0.47.0/windows/svc/service.go:181-188, 276` | confirma o **A1**: `Execute` devolvendo `(false, 0)` reporta `Stopped` com `Win32ExitCode = NO_ERROR` — parada limpa aos olhos do SCM |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. `-Desinstalar` remove tudo **menos** o anúncio do comparador — e o anúncio é o único gatilho da delegação *(novo)*

**Arquivos:** `instalador/instalar-servidor.ps1:104-119`, `anuncio.go:82-105`,
`delegacao.go:49-99`, `comparador.go:99`, `instalador/msi/AgenteBiometria.wxs:122-127`,
`README.md` (seção *Desinstalação*)

A desinstalação faz seis coisas e nenhuma delas toca em `ProgramData`:

```powershell
# instalar-servidor.ps1:104-119
if ($Desinstalar) {
    Remove-ItemProperty -Path $runKey -Name $nomeRun -ErrorAction SilentlyContinue
    Stop-TarefaComparador                       # <- Stop-Process -Force nos agentes da sessao 0
    Unregister-ScheduledTask -TaskName $nomeTarefa -Confirm:$false -ErrorAction SilentlyContinue
    foreach ($nome in @('COMPARADOR_URL', 'COMPARADOR_TOKEN', 'COMPARADOR_PORTA')) {
        [Environment]::SetEnvironmentVariable($nome, $null, 'Machine')
    }
    foreach ($processo in @(Get-AgentesInstalados)) {
        Stop-Process -Id $processo.ProcessId -Force -Confirm:$false -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $destino) { Remove-DiretorioInstalacao $destino }
    Write-Host 'Agente desinstalado. Dados e certificado de cada usuario foram preservados.'
    exit 0
}
```

O comparador tem uma limpeza própria, e ela é boa — `defer removeAnuncio()`
(`comparador.go:99`), com o motivo escrito ao lado:

```go
// anuncio.go:98-100
// removeAnuncio apaga o anuncio ao parar. Sem isso, um agente aberto depois de
// o servico cair tentaria delegar para uma porta morta e so descobriria o
// problema na primeira comparacao, com o usuario e o dedo ja esperando.
```

Só que `Stop-Process -Force` é `TerminateProcess`: **nenhum `defer` do Go roda**.
A desinstalação, por construção, é o único caminho de parada em que a limpeza que
o próprio arquivo declara obrigatória **não pode** acontecer — e nada assume o
lugar dela.

**Por que é um problema.** `C:\ProgramData\AgenteBiometria\comparador.json` sobra
com porta, token e endereço válidos. A partir daí o gatilho está armado, e ele
dispara sozinho no próximo agente que subir naquela máquina:

```go
// delegacao.go:58-72 — sem as variaveis (que a desinstalacao acabou de apagar),
// cai direto no anuncio
if base == "" || len(token) < 32 {
	a, err := leAnuncio()
	if err != nil { ...; return }        // <- so aqui a comparacao continuaria local
	if base == "" { base = a.Endereco }
	if len(token) < 32 { token = a.Token }
}
```

E `leAnuncio()` não tem como recusar um anúncio morto, porque ele só olha a forma:

```go
// anuncio.go:59-80 — as unicas checagens
if a.Porta < 1 || a.Porta > 65535 { return nil, errors.New("...porta invalida") }
if len(a.Token) < 32              { return nil, errors.New("...token curto demais") }
```

O detalhe que fecha o argumento: `publicaAnuncio` **grava o PID e o horário**
(`anuncio.go:83-90`) — exatamente os dois campos que responderiam "o publicador
ainda está vivo?". `grep -rn "\.PID\|\.Desde" *.go` devolve **zero leituras**. A
evidência necessária para detectar o anúncio órfão é produzida e jogada fora.

O sintoma em campo é o pior possível, porque é uma inversão: numa estação de
trabalho **sem gancho nenhum**, onde a comparação local funcionaria perfeitamente,
o agente lê o anúncio de um serviço que foi desinstalado, decide delegar, e
responde **502 Bad Gateway** com `comparador inacessivel: dial tcp
127.0.0.1:5150: connectex...` em **toda** comparação. O comentário do próprio
`configuraComparador` diz o que deveria acontecer:

> *"Ausencia do arquivo significa que nao ha comparador nesta maquina, e ai
> comparar localmente e o comportamento certo, nao uma falha."*

A ausência do arquivo é o sinal — e a desinstalação é justamente a operação que
deveria produzi-lo e não produz. Somado ao **C21 do #20** (o agente decide delegar
uma vez e nunca reavalia), a máquina fica assim até alguém descobrir um arquivo
JSON que nenhum documento menciona. O README, na seção *Desinstalação*, diz
apenas *"Os dados e certificados de cada usuário são preservados"* — fala do
perfil do usuário e não dá ao operador nenhum motivo para suspeitar de um resíduo
de máquina.

A assimetria dentro do próprio repositório é o que torna isto um crítico e não um
esquecimento: **o MSI faz certo.**

```xml
<!-- AgenteBiometria.wxs:122-127 -->
<!-- O anuncio e criado em tempo de execucao, entao o MSI nao o conhece:
     sem esta limpeza explicita ele sobreviveria a desinstalacao, e um
     agente reinstalado depois tentaria falar com um servico que nao existe
     mais. -->
<RemoveFile Id="RemoveAnuncio" Name="comparador.json" On="uninstall" />
```

O requisito foi entendido, escrito e implementado numa metade da cadeia de
entrega. A outra metade — a que o README manda usar — não o recebeu.

**Como corrigir.** Duas linhas no desinstalador, e uma verificação de vida do
lado de quem lê. Primeiro o desinstalador:

```powershell
# instalar-servidor.ps1, dentro de if ($Desinstalar), antes do exit 0
# O anuncio e a UNICA coisa que faz um agente delegar. Deixa-lo para tras
# transforma uma maquina sem comparador numa maquina que responde 502 em toda
# comparacao. Stop-Process -Force nao roda o defer removeAnuncio() do Go.
$pastaDados = Join-Path $env:ProgramData 'AgenteBiometria'
Remove-Item -LiteralPath (Join-Path $pastaDados 'comparador.json') `
    -Force -ErrorAction SilentlyContinue
```

E, do lado do agente, usar o PID que já está gravado — assim o anúncio órfão
deixa de ser um problema mesmo quando a limpeza falha (queda de energia, matar
pelo Gerenciador de Tarefas, um `-Desinstalar` de uma versão antiga):

```go
// anuncio.go — o campo PID existe desde o primeiro dia; falta le-lo.
func leAnuncio() (*anuncioComparador, error) {
	...
	// Anuncio de publicador morto e pior que anuncio ausente: ausente faz o
	// agente comparar localmente, morto faz ele responder 502 para sempre.
	if a.PID > 0 && !processoVivo(a.PID) {
		return nil, fmt.Errorf("anuncio do comparador e de um processo morto (pid %d)", a.PID)
	}
	return &a, nil
}

func processoVivo(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var codigo uint32
	// STILL_ACTIVE (259) e o unico estado que interessa aqui.
	return windows.GetExitCodeProcess(h, &codigo) == nil && codigo == 259
}
```

Vale notar que a segunda metade também é a base de uma correção barata para o
**C21 do #20**: com `leAnuncio()` sabendo dizer "morto", releitura periódica passa
a ser possível sem inventar um novo canal de saúde.

---

### C2. Nada, em lugar nenhum, remove o certificado da loja Raiz do usuário — a desinstalação deixa um `localhost` confiável por até dois anos, com a chave privada preservada de propósito *(novo)*

**Arquivos:** `cert.go:56-141`, `instalador/instalar-servidor.ps1:104-119`,
`instalador/msi/AgenteBiometria.wxs:104-140`, `README.md:135`,
`integracao/COMO-USAR.md:91-96`

O agente gera um certificado autoassinado de `localhost` e **o instala na loja
Raiz do usuário**:

```go
// cert.go:115-123
func instalaCertificadoUsuario(certPath string) error {
	cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
	...
}
```

```go
// cert.go:71-72 — dois anos de validade
NotBefore: agora.Add(-time.Hour),
NotAfter:  agora.AddDate(2, 0, 0),
```

`grep -rn "delstore" .` no repositório inteiro devolve **nada**. Não há remoção
no desinstalador PowerShell, não há `<RemoveRegistryKey>`/ação customizada
equivalente no WiX, não há um `--remover-cert` no binário. O que existe é a frase
final da desinstalação, que trata isso como cortesia:

```powershell
# instalar-servidor.ps1:117
Write-Host 'Agente desinstalado. Dados e certificado de cada usuario foram preservados.'
```

E o README repete: *"Os dados e certificados de cada usuário são preservados
durante a desinstalação."*

**Por que é um problema.** O que está sendo "preservado" não é um dado do
usuário: é uma **âncora de confiança** mais a **chave privada** correspondente.
Depois da desinstalação, em cada perfil que algum dia rodou o agente, sobram:

1. um certificado para `localhost`, `127.0.0.1` e `::1` na loja **Raiz** do
   usuário, válido por até dois anos a partir da última renovação;
2. `%LOCALAPPDATA%\BiometriaAgente\key.pem`, a chave privada dele, gravada com
   `0o600` — que no Windows não é ACL, é atributo: o arquivo herda a ACL de
   `%LOCALAPPDATA%` e é **legível pelo próprio usuário** e por administradores.

Qualquer processo rodando como esse usuário — e não é preciso privilégio nenhum
para isso — lê `key.pem`, sobe um servidor em `127.0.0.1:5000` e apresenta um
`https://localhost` que **o navegador daquele usuário aceita sem um único aviso**.

Isso não é um risco teórico: é exatamente o **C13 do #15** (o cliente web não
distingue o agente de um impostor na 5000) com a última barreira removida. O
`descobrir()` do cliente varre as portas a partir da 5000 e conecta no primeiro
que responder `/api/hello` com um token (`integra-biometria.js:179-189`); e ele
tenta **`https` primeiro** quando a página é HTTPS (`integra-biometria.js:20`).
Um impostor que fale HTTPS confiável ganha o par completo: recebe os templates
que o sistema web manda comparar e devolve `true` em qualquer comparação. A
diferença que o C2 acrescenta é o **tempo**: isso continua verdade em máquinas de
onde o produto **já foi removido**, onde ninguém mais está olhando, por até dois
anos.

Há ainda o acúmulo. `carregaTLS()` chama `gerarCert()` e depois
`instalaCertificadoUsuario()` **a cada arranque do agente** (`cert.go:125-132`),
e `gerarCert` regenera quando falta menos de 30 dias para vencer
(`cert.go:47-49`). Cada renovação gera um serial novo e aleatório
(`cert.go:61-64`), então `certutil -addstore -f` **acrescenta** uma entrada em vez
de substituir a anterior. Nenhuma é removida. A loja Raiz do usuário vai
juntando certificados de `localhost`, e cada chave antiga que tenha vazado
continua utilizável até o `NotAfter` dela.

**Como corrigir.** Três frentes, todas baratas.

Primeiro, dar ao binário o comando que falta — ele é quem sabe qual certificado
é dele:

```go
// cert.go — o inverso de instalaCertificadoUsuario, pelo mesmo caminho.
func removeCertificadoUsuario() error {
	certPath, keyPath := caminhosCert()
	// Remove pelo Common Name: pega tambem as renovacoes antigas, que tem
	// serial diferente e que -addstore foi acumulando na loja a cada 2 anos.
	cmd := exec.Command("certutil.exe", "-user", "-delstore", "Root",
		"Agente de Biometria (localhost)")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	saida, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil -delstore: %w: %s", err, string(saida))
	}
	// A chave privada e o que transforma o certificado confiavel em impostor
	// utilizavel: sai junto.
	_ = os.Remove(keyPath)
	_ = os.Remove(certPath)
	return nil
}
```

ligado a um `--remover-cert` no despacho (que o **C1 do #23** já propõe
reorganizar em `switch`).

Segundo, chamá-lo na desinstalação, para cada perfil alcançável, e corrigir a
mensagem:

```powershell
# instalar-servidor.ps1, dentro de if ($Desinstalar), ANTES de remover o binario
foreach ($processo in @(Get-AgentesInstalados)) { ... }   # como hoje
# Um certificado de localhost na loja Raiz do usuario, com a chave privada ao
# lado, e o que permite a um impostor na 5000 falar HTTPS que o navegador aceita.
# Preservar isso depois de o produto sair nao e cortesia.
& $exe --remover-cert 2>$null
...
Write-Host 'Agente desinstalado. Origens autorizadas e logs de cada usuario foram preservados.'
Write-Host 'O certificado de localhost e a chave privada foram removidos.'
```

Terceiro, e independente da desinstalação: parar de acumular. `carregaTLS` só
precisa chamar `instalaCertificadoUsuario` quando o certificado **mudou** —
guardar o *fingerprint* instalado ao lado do `cert.pem` resolve, e de quebra tira
um `certutil.exe` de cada arranque (ver **S3**).

---

## 🟡 Alertas (recomenda correção)

### A1. O serviço reporta parada limpa ao SCM com 2 segundos de folga — e quem termina o processo é um `os.Exit`, que não roda nenhum `defer` do comparador *(novo)*

**Arquivos:** `servico.go:26-71`, `comparador.go:99, 113-141`, `main.go:963-965`

O caminho de parada do serviço tem dois prazos, escolhidos em arquivos
diferentes, e eles se encaixam por 2 segundos:

```go
// servico.go:54-62 — o teto
case svc.Stop, svc.Shutdown:
	estado <- svc.Status{State: svc.StopPending, WaitHint: 15000}
	cancelaApp()
	select {
	case <-saida:
	case <-time.After(12 * time.Second):                 // <- 12 s
		registraErro("servico: o comparador nao encerrou a tempo")
	}
	return false, 0
```

```go
// comparador.go:139-141 — o que roda dentro desse teto
ctxSaida, cancelaSaida := context.WithTimeout(context.Background(), 10*time.Second)
encerraSDK(ctxSaida)                                     // <- 10 s
cancelaSaida()
```

Os 10 segundos são atingíveis de verdade: `encerraSDK` entra na fila de
`sdkTasks`, e a thread do SDK pode estar ocupada com uma identificação 1:N que o
worker segura por até 3 minutos (`worker.go:340-343`). `cancelaApp()` libera quem
*pediu*, não quem está *executando*. Então uma parada durante uma busca 1:N —
que é exatamente o que acontece numa atualização pelo MSI num horário de
movimento — consome os 10 segundos inteiros, e sobram 2 para tudo o mais.

Estourado o teto, três coisas acontecem, nesta ordem:

1. `return false, 0`. Li o `svc.Run` em `x/sys@v0.47.0`
   (`windows/svc/service.go:181-188, 276`): `svcSpecificEC=false` com
   `exitCode=0` reporta `Stopped` com `Win32ExitCode = NO_ERROR`. **Para o SCM, a
   parada foi limpa** — e para o `<ServiceControl Stop="both" Wait="yes" />` do
   WiX também.
2. `rodaComoServico` devolve 0, `executa()` devolve 0, e `main` faz
   `os.Exit(0)` (`main.go:964`). **`os.Exit` não roda função adiada nenhuma**, e
   `rodaComparadorCom` ainda está viva na sua goroutine.
3. Portanto o `defer removeAnuncio()` (`comparador.go:99`) e o
   `defer cancelaApp()` (`comparador.go:64`) **não rodam**. O anúncio fica — e o
   resto da história é o **C1** desta revisão.

O agravante é de ordenação, e é gratuito: `removeAnuncio()` é o **primeiro**
`defer` registrado, logo o **último** a executar. A única ação da parada de que
*outros processos* dependem foi colocada atrás da mais lenta.

**Como corrigir.** Tirar o anúncio da fila dos `defer` e fazer dele o primeiro
passo da parada, antes de qualquer dreno:

```go
// comparador.go — o anuncio sai na hora em que a porta para de valer,
// nao depois de drenar o SDK.
go func() {
	<-ctxApp.Done()
	// Primeiro o que os outros processos leem: a partir daqui esta porta nao
	// vale mais, e um agente que subir agora deve comparar localmente.
	removeAnuncio()
	ctx, cancela := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancela()
	if err := servidor.Shutdown(ctx); err != nil {
		registraErro("comparador: desligar: %v", err)
	}
}()
```

(`removeAnuncio` já é idempotente — ignora `os.IsNotExist` —, então o `defer`
pode continuar existindo como rede.)

E, no serviço, parar de mentir quando o prazo estoura:

```go
// servico.go
case <-time.After(12 * time.Second):
	registraErro("servico: o comparador nao encerrou a tempo")
	// Codigo != 0 para o SCM registrar a parada como anormal: hoje o
	// services.msc, o MSI e o log de eventos dizem que deu tudo certo.
	return false, uint32(windows.ERROR_SERVICE_REQUEST_TIMEOUT)
```

### A2. `encerraSDK` do agente reaproveita o contexto que o `Shutdown` já pode ter gasto — o comparador corrigiu isso e deixou o comentário explicando *(novo)*

**Arquivos:** `main.go:952-955`, `comparador.go:137-141`, `main.go:107-138`

Os dois caminhos de saída fazem a mesma sequência. O do comparador tem um
contexto novo, e diz por quê:

```go
// comparador.go:137-141
// Contexto novo de proposito: ctxApp ja foi cancelado neste ponto, e
// naThreadSDK desiste de imediato com um contexto morto.
ctxSaida, cancelaSaida := context.WithTimeout(context.Background(), 10*time.Second)
encerraSDK(ctxSaida)
cancelaSaida()
```

O do agente reaproveita o mesmo contexto nas duas chamadas:

```go
// main.go:952-955
ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = servidor.Shutdown(ctxShutdown)
encerraSDK(ctxShutdown)          // <- recebe o que sobrou dos 10 s, se sobrou
```

**Por que é um problema.** `naThreadSDK` verifica o contexto **duas vezes**: uma
antes de enfileirar e outra dentro da tarefa, já na thread do SDK
(`main.go:122-124`). Com o contexto vencido, a tarefa retorna sem executar
`fn()`, e `sdkInst.encerra()` — o `opEncerrar` que dá ao worker a chance de
chamar `NBioAPI_Terminate` e fechar o leitor de forma ordenada — simplesmente não
acontece. O worker só descobre que acabou pelo EOF no `stdin`, depois que o pai
já morreu.

Não é o mesmo dano do A1 (aqui não há anúncio para sobrar, e o sistema
operacional recolhe o processo de qualquer jeito), e por isso é alerta e não
crítico. O que o torna digno de nota é o padrão: **a mesma correção, com o mesmo
comentário, já existe a 800 linhas de distância.** É o segundo achado de hoje em
que uma metade do binário aprendeu uma lição que a outra não recebeu — o **C1** é
o mesmo entre o MSI e o PowerShell.

**Como corrigir.** Copiar o que o comparador já faz:

```go
// main.go
ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = servidor.Shutdown(ctxShutdown)

// Contexto proprio: o Shutdown pode ter consumido o prazo inteiro esperando
// requisicao em voo, e naThreadSDK desiste de imediato com contexto morto.
ctxSDK, cancelaSDK := context.WithTimeout(context.Background(), 10*time.Second)
defer cancelaSDK()
encerraSDK(ctxSDK)
```

### A3. O supervisor transforma qualquer encerramento externo em um reinício em 2 segundos — e o README manda "feche o agente" sem dizer que só o *Sair* da bandeja encerra *(novo)*

**Arquivos:** `supervisor.go:29-56`, `main.go:761, 785-789, 950-960`,
`README.md:266-267`

O supervisor só desiste com código de saída **zero**:

```go
// supervisor.go:35-54
for {
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "BIO_FILHO=1")
	inicio := time.Now()
	if err := cmd.Start(); err != nil { os.Exit(1) }
	if cmd.Wait() == nil { os.Exit(0) }      // <- unica saida do laco
	time.Sleep(espera)                        // <- 2 s no primeiro reinicio
	if time.Since(inicio) > time.Minute { espera = 2 * time.Second } else { ... }
}
```

E código zero só sai por um caminho: `systray.Quit()` a partir do item **Sair**
(`main.go:761, 785-789`), que faz `systray.Run` retornar e `executa()` devolver 0
(`main.go:950-960`). Qualquer outro fim do processo filho — Gerenciador de
Tarefas, `taskkill`, `Stop-Process`, violação de acesso na DLL, o próprio
`os.Exit(1)` de um erro de listener — devolve erro no `Wait()` e o agente **volta
em 2 segundos**, com um ícone novo na bandeja, num processo novo, disputando o
mesmo leitor.

O `espera` ainda é reiniciado para 2 segundos sempre que o filho tiver vivido
mais de um minuto (`supervisor.go:47-48`), que é o caso normal: um agente aberto
desde o logon e morto agora reaparece no menor prazo possível.

**Por que é um problema.** O README abre a seção de diagnóstico assim:

> *"O executável aceita comandos que rodam fora do modo normal. **Feche o agente
> antes de usá-los**: dois processos disputando o mesmo leitor derrubam a
> captura."* (`README.md:266-267`)

O documento declara a precondição e não diz como cumpri-la. O reflexo de quem dá
suporte num servidor RDS é o Gerenciador de Tarefas — e ali "encerrar tarefa" no
processo do agente **não encerra o agente**: encerra um filho que o pai repõe
antes de a pessoa terminar de digitar o comando. O `--autoteste` seguinte roda
exatamente na condição que o README mandou evitar, e o relatório acusa leitor com
problema. Vale lembrar que os dois processos têm o **mesmo nome** e o mesmo
caminho, então a lista de tarefas não ajuda a distinguir quem é o supervisor.

O mesmo se aplica ao `instalar-servidor.ps1`, com uma diferença de sorte:
`Stop-AgentesDaSessao` (`:71-78`) mata supervisor **e** filho no mesmo laço, em
milissegundos, e a janela de 2 segundos do `time.Sleep` cobre a corrida. Funciona
— mas por margem de tempo, não por desenho.

**Como corrigir.** Duas linhas no README e um caminho de parada explícito. No
README:

```markdown
Feche o agente antes de usá-los: dois processos disputando o mesmo leitor
derrubam a captura. **Feche pelo item *Sair* do ícone na bandeja** — encerrar o
processo pelo Gerenciador de Tarefas não resolve: o supervisor reabre o agente
em dois segundos. Para conferir que fechou, o ícone tem de sumir da bandeja.
```

E, no código, dar ao supervisor um motivo além do código de saída — um evento
nomeado que o próprio executável possa sinalizar:

```go
// supervisor.go — "pare de verdade" deixa de depender de o filho conseguir
// sair com 0, que e justamente o que nao acontece quando ele e morto.
func supervisor() {
	parar, _ := windows.CreateEvent(nil, 1, 0, syscall.StringToUTF16Ptr(`Local\AgenteBiometriaParar`))
	...
	if cmd.Wait() == nil { os.Exit(0) }
	if w, _ := windows.WaitForSingleObject(parar, 0); w == windows.WAIT_OBJECT_0 {
		registraInfo("supervisor: parada pedida; nao vou reabrir")
		os.Exit(0)
	}
	...
}
```

com um `--parar` que sinaliza o evento e mata os processos da sessão. Isso dá ao
instalador, ao suporte e ao próprio README um verbo único para "feche o agente".

### A4. A desinstalação mata os processos e apaga o diretório na linha seguinte, sem esperar — e com `$ErrorActionPreference = 'Stop'` a falha vira uma desinstalação pela metade *(novo)*

**Arquivos:** `instalador/instalar-servidor.ps1:21, 97-102, 111-116`,
`worker.go:191-239`

```powershell
# instalar-servidor.ps1:111-116
foreach ($processo in @(Get-AgentesInstalados)) {
    Stop-Process -Id $processo.ProcessId -Force -Confirm:$false -ErrorAction SilentlyContinue
}
if (Test-Path -LiteralPath $destino) {
    Remove-DiretorioInstalacao $destino        # Remove-Item -Recurse -Force
}
```

`Stop-Process -Force` devolve assim que o `TerminateProcess` é aceito, não quando
o objeto de processo é destruído e os handles de arquivo são liberados. Enquanto
houver um handle aberto para `AgenteBiometria.exe`, o `Remove-Item` falha com
*"está sendo usado por outro processo"*.

E há mais de um processo por sessão para fechar: o supervisor, o filho e — quando
alguma comparação ou captura estava em curso — o **worker**, que é o mesmo
executável (`worker.go:215-216`) e portanto entra na lista de
`Get-AgentesInstalados` (que filtra por `Name = 'AgenteBiometria.exe'`). Num RDS
com dez sessões conectadas isso é da ordem de 20 a 30 terminações disparadas em
sequência, e a última linha do laço não espera nenhuma delas.

**Por que é um problema.** Não é a falha em si: é o **estado em que ela deixa a
máquina**. `$ErrorActionPreference = 'Stop'` (`:21`) transforma o erro do
`Remove-Item` em exceção, e nesse ponto o script **já** removeu a chave `Run`, já
desregistrou a tarefa e já apagou as três variáveis de máquina. A desinstalação
para no último passo tendo desligado tudo o que fazia o sistema funcionar e
deixado os binários no disco — e, junto com o **C1**, o anúncio. Quem lê a tela vê
um erro vermelho de PowerShell e nenhuma das mensagens finais.

**Como corrigir.** Esperar o que se acabou de matar, e não deixar o último passo
derrubar o resto:

```powershell
# instalar-servidor.ps1
$mortos = @(Get-AgentesInstalados | ForEach-Object { $_.ProcessId })
foreach ($pid in $mortos) {
    Stop-Process -Id $pid -Force -Confirm:$false -ErrorAction SilentlyContinue
}
# TerminateProcess devolve antes de o Windows liberar os handles de arquivo,
# e o worker segura o proprio .exe. Sem esta espera o Remove-Item abaixo falha.
foreach ($pid in $mortos) {
    $p = Get-Process -Id $pid -ErrorAction SilentlyContinue
    if ($p) { $null = $p.WaitForExit(5000) }
}

if (Test-Path -LiteralPath $destino) {
    try { Remove-DiretorioInstalacao $destino }
    catch {
        Write-Warning "Nao consegui remover $destino agora: $_"
        Write-Warning 'Reexecute -Desinstalar apos o proximo logoff das sessoes.'
    }
}
```

---

## 🟢 Sugestões (opcional)

### S1. `Stop-TarefaComparador` sai calada quando o comparador veio do MSI

```powershell
# instalar-servidor.ps1:43-46
function Stop-TarefaComparador {
    $tarefa = Get-ScheduledTask -TaskName $nomeTarefa -ErrorAction SilentlyContinue
    if (-not $tarefa) { return }        # <- num servidor com MSI, sai aqui
    ...
}
```

Os dois instaladores usam o **mesmo nome** (`AgenteBiometriaComparador`), mas em
espaços diferentes: o MSI cria um **serviço**, o PS1 uma **tarefa agendada**.
Numa máquina onde o MSI passou primeiro, `Get-ScheduledTask` não acha nada, a
função retorna, e o script segue registrando a tarefa e chamando
`Start-ScheduledTask` sobre uma porta que o serviço já ocupa — o comparador da
tarefa morre em `net.Listen` (`comparador.go:85-90`) e a política de reinício
tenta mais três vezes. É a face operacional do **C7 do #10**; o conserto barato é
a função enxergar os dois:

```powershell
$servico = Get-Service -Name $nomeTarefa -ErrorAction SilentlyContinue
if ($servico) {
    throw "O comparador ja esta instalado como servico Windows (provavelmente pelo MSI). Desinstale por la antes de usar este script."
}
```

### S2. `listenerMista.Close()` não fecha as conexões que ficaram no buffer

```go
// cert.go:242-261 — Accept tira de m.conns; Close nao esvazia
conns: make(chan net.Conn, 32),
...
func (m *listenerMista) Close() error {
	err := m.bruto.Close()
	m.falha(net.ErrClosed)
	return err
}
```

Uma conexão que o `espia` já entregou ao canal e que o `Accept` ainda não retirou
fica com o socket aberto até o processo morrer — o `Shutdown` do `http.Server`
não a conhece, porque ela nunca chegou a ser uma conexão dele. São até 32
sockets, e no caminho normal o processo termina logo em seguida, o que torna isto
uma sugestão e não um alerta. Fecha-se drenando no `Close`:

```go
func (m *listenerMista) Close() error {
	err := m.bruto.Close()
	m.falha(net.ErrClosed)
	for {
		select {
		case c := <-m.conns:
			_ = c.Close()
		default:
			return err
		}
	}
}
```

### S3. `certutil.exe` roda a cada arranque do agente, inclusive a cada reinício do supervisor

`carregaTLS()` chama `instalaCertificadoUsuario()` incondicionalmente
(`cert.go:130`), mesmo quando o certificado em disco é o mesmo que já está
instalado — o caso normal. É um processo externo por logon, mais um por cada
reinício do supervisor (**A3**), e num servidor RDS com muitos logons
simultâneos isso aparece. Guardar o *fingerprint* do que foi instalado ao lado do
`cert.pem` e comparar antes de chamar resolve, e é o mesmo gancho que o **C2**
precisa para parar de acumular entradas na loja Raiz.

### S4. O anúncio publica PID, versão e horário que ninguém lê

```go
// anuncio.go:83-90
a := anuncioComparador{
	Porta: porta, Token: token,
	PID:    os.Getpid(),                        // nunca lido
	Versao: versao,                             // nunca lido
	Desde:  time.Now().Format(time.RFC3339),    // nunca lido
	Endereco: "http://127.0.0.1:" + strconv.Itoa(porta),
}
```

Três campos gravados e zero leituras (`grep -rn "\.PID\|\.Desde\|a\.Versao" *.go`).
O `PID` é a correção do **C1** e o `Versao` é metade da resposta do **C16 do #18**
("nada confere versão"): um agente que leia a versão do anúncio pode recusar
delegar para um comparador mais antigo que ele em vez de descobrir a
incompatibilidade como `0x000B` no meio de um atendimento. Os dados já estão no
arquivo; falta usá-los.

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 33 (22 `.go`, 1 `.js`, 1 `.ps1`, 3 do MSI, 2 `.cmd`, 1 `.py`, `.gitignore`, `go.mod`/`go.sum`, `README.md` e 4 docs)
- **Segurança**: 🚨 **Risco** — o **C2** de hoje é de segurança e é o primeiro em muitas revisões a *estender no tempo* um crítico já aberto: ele mantém o **C13 do #15** (impostor na 5000) explorável, agora sobre HTTPS confiável, em máquinas de onde o produto já foi desinstalado. Somam-se os seis anteriores intocados: impressão de template como identificador estável (**C25 do #22**), parser nativo sob `LocalSystem` (**C23 do #21**), dado pessoal em log legível (**C14 do #17**), `ProgramData` gravável (**C10 do #13**) e revogação que se desfaz sozinha (**C19 do #19**)
- **Qualidade**: ⚠️ Atenção — `build`, `vet` e `gofmt` limpos. O ponto fraco de hoje é de **simetria**: três achados (**C1**, **A1**, **A2**) são a mesma lição aprendida numa metade do sistema e não aplicada na outra — MSI × PowerShell, comparador × agente
- **Risco de produção**: 🚨 **Alto** — o **C1** desliga a biometria de uma máquina inteira a partir de uma desinstalação bem-sucedida, sem erro em lugar nenhum, e o **C21 do #20** garante que ela não se recupere sozinha
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que não rodam em alvo nenhum (`matched no packages` no host, `exec format error` em `windows/386`) e nenhum CI. **A8 de 30/07, aberto há 22 dias**. O **C1** de hoje é testável sem Windows e sem leitor: `leAnuncio()` já é exercitado por `anuncio_test.go`, e a verificação de vida do PID cabe na mesma tabela

### Inventário dos críticos abertos contra `26c9379`

| # | Origem | Crítico | Estado |
|---|---|---|---|
| 1 | main (07-30) | `bioPort` do fragmento não validado (`integra-biometria.js:26-34`) | **ABERTO** |
| 2 | main (07-30) | Cliente JS descarta `ignorados` (`integra-biometria.js:222-230`) | **ABERTO** |
| 3 | main (07-30) | 1:N é laço de 1:1 — falsa aceitação acumula | **ABERTO** |
| 4 | main (07-30) | 1:N congela as capturas da sessão por até 3 min | **ABERTO** |
| 5 | PR #10 | Parar o serviço apaga o anúncio; token novo derruba os agentes | **ABERTO** |
| 6 | PR #10 | Endereço do comparador não validado como loopback | **ABERTO** |
| 7 | PR #10 | MSI × `instalar-servidor.ps1` disputam serviço e porta | **ABERTO** |
| 8 | PR #10 | MSI não para os agentes das sessões (arquivo em uso) | **ABERTO** |
| 9 | PR #13 | Fila de SDK de uma via atende o servidor inteiro | **ABERTO** |
| 10 | PR #13 | `ProgramData` gravável por `Users` — anúncio plantável | **ABERTO** |
| 11 | PR #14 | Diálogo da bandeja forjável (`tituloOrigem` corta o domínio) | **ABERTO** |
| 12 | PR #14 | Pendência expirada nunca sai da bandeja | **ABERTO** |
| 13 | PR #15 | Cliente web não distingue o agente de um impostor na 5000 | **ABERTO** |
| 14 | PR #17 | `comparador.log` legível por qualquer usuário, com id e veredito | **ABERTO** |
| 15 | PR #17 | 1:N obriga o navegador a receber até 5.000 templates | **ABERTO** |
| 16 | PR #18 | Nada confere versão; agente velho volta a comparar na jaula | **ABERTO** |
| 17 | PR #18 | `/api/status` diz OK sem sondar o comparador | **ABERTO** |
| 18 | PR #18 | `VERSAO` fixa: `MajorUpgrade` não dispara | **ABERTO** |
| 19 | PR #19 | Estado por usuário compartilhado entre sessões: revogação desfeita | **ABERTO** |
| 20 | PR #19 | `--comparador` de diagnóstico apaga o anúncio de produção | **ABERTO** |
| 21 | PR #20 | Agente decide delegar uma vez e nunca reavalia | **ABERTO** |
| 22 | PR #20 | Parada do comparador deixa worker órfão e quebra a atualização | **ABERTO** |
| 23 | PR #21 | Usuário comum alcança o parser nativo dentro de um processo `LocalSystem` | **ABERTO** |
| 24 | PR #21 | Tetos de `/identificar` contradizem a API e dobram no caminho delegado | **ABERTO** |
| 25 | PR #22 | Impressão do template é identificador estável de pessoa, gravado em log | **ABERTO** |
| 26 | PR #22 | Comparador sem `middleware`: sem teto de concorrência e sem `recover` | **ABERTO** |
| 27 | PR #23 | Argumento desconhecido inicia um agente completo, em silêncio | **ABERTO** |
| 28 | PR #23 | O despacho não grava log e resolve os dois erros de detecção para o lado errado | **ABERTO** |
| 29 | **hoje** | **`-Desinstalar` deixa o anúncio do comparador; a máquina passa a responder 502 em toda comparação** | **NOVO** |
| 30 | **hoje** | **Nada remove o certificado da loja Raiz do usuário: `localhost` confiável e chave privada sobrevivem à desinstalação** | **NOVO** |

---

## ✅ Pontos positivos

**`Remove-DiretorioInstalacao` recusa apagar o que não é dele, e a checagem é a
certa.** O instalador apaga diretórios de versões antigas num laço
(`instalar-servidor.ps1:204-210`), que é o tipo de código que já destruiu perfil
de usuário em muita empresa. Aqui todo caminho passa por
`Test-CaminhoInstalado`, que normaliza com `[IO.Path]::GetFullPath` **dos dois
lados** e compara com o separador final incluído (`:57-62`) — então
`...\AgenteBiometria-antigo` não passa por prefixo de `...\AgenteBiometria`, e
`..\..\` não engana. E o `Remove-DiretorioInstalacao` ainda repete a checagem por
conta própria antes de agir (`:97-102`), em vez de confiar em quem o chamou. É
defesa em profundidade num lugar onde quase ninguém coloca.

**`Test-PeX86` confere a arquitetura lendo o cabeçalho PE, e não o nome do
arquivo** (`instalar-servidor.ps1:80-95`): valida `MZ`, valida o deslocamento do
cabeçalho contra o tamanho real do arquivo antes de posicionar o stream, valida a
assinatura `PE\0\0` e só então lê a máquina. Um binário x64 renomeado é recusado
na instalação, e não descoberto quando a `NBioBSP.dll` de 32 bits se recusar a
carregar dentro de uma sessão RDP em produção. O mesmo teste é reaplicado à
própria DLL (`:153`).

**O serviço só reporta `Running` depois que a porta aceita conexões**
(`servico.go:38-46`). Reconferido hoje pelo eixo da parada, e a decisão continua
certa: o canal `pronto` atravessa a fronteira entre `rodaComparadorCom` e
`Execute` só para isso. O **A1** não contesta o arranque — contesta o caminho de
volta, onde o mesmo cuidado com "o que o SCM acredita" não foi aplicado.

**`removeAnuncio` é idempotente e diz por que existe** (`anuncio.go:98-105`):
ignora `os.IsNotExist` e registra qualquer outro erro. Duas chamadas não brigam,
o que é exatamente o que permite a correção do **A1** — mover a remoção para o
início da parada — sem tirar o `defer` que já existe. É raro uma função de limpeza
estar pronta para ser chamada de dois lugares antes de alguém precisar disso.

**A separação entre "erro daquele registro" e "erro da operação" continua sendo a
melhor decisão do repositório** (`sdk.go:512-522`, `worker.go:130-134`,
`main.go:567-572`). Vista hoje pelo eixo da parada, ela reaparece invertida no
`leAnuncio()`: lá, "o comparador não existe" e "o comparador existe e está morto"
viraram a mesma coisa — um anúncio que passa na validação. É o mesmo princípio
que falta, e é por isso que o **C1** custa poucas linhas.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O sistema chega a **30 críticos abertos** e **quinze dias** sem um commit de
correção. Os dois de hoje têm uma origem comum, e ela é o espelho da de ontem: o
projeto sabe muito bem **o que gravar** e não decidiu **quem apaga**. O anúncio, o
arquivo de descoberta e o certificado na loja Raiz são estado que vive fora do
processo; os três são criados por código cuidadoso, com comentário explicando o
porquê; e os três dependem, para sumir, de um `defer` que só roda quando o
processo tem a sorte de morrer do jeito previsto. `Stop-Process -Force`,
`os.Exit`, o Gerenciador de Tarefas e a desinstalação — os quatro jeitos mais
comuns de o sistema parar em campo — não são esse jeito.

A sequência recomendada muda pouco em relação à do #23, e ganha dois itens
baratos na frente:

1. **C1 de hoje** — `Remove-Item` do `comparador.json` no `-Desinstalar` (2
   linhas) e leitura do `PID` no `leAnuncio()` (~12 linhas). Nenhum handler muda,
   nenhum caminho de dados muda, e a segunda metade destrava a correção do
   **C21 do #20**.
2. **C2 de hoje** — `--remover-cert` com `certutil -user -delstore Root` e a
   chamada dele na desinstalação (~20 linhas). É o que impede um crítico já
   aberto de continuar explorável em máquinas onde o produto não existe mais.
3. **A1 + A2** — mover `removeAnuncio()` para o início da parada do comparador e
   dar ao `encerraSDK` do agente um contexto próprio. São 6 linhas somadas, e as
   duas correções já existem escritas no repositório, no arquivo vizinho.
4. **C1 + C2 do #23** — o `switch` com caso padrão e o
   `iniciaLogDeArranque()` na primeira linha de `executa()`. Continuam sendo o
   que torna diagnosticável tudo o que vier depois.
5. **C26 do #22 (o `protege`)**, **C23 do #21 (a troca de conta)** e
   **C25 do #22 (o HMAC na impressão)**, na ordem do #23.

O **A3** não custa código nenhum para começar a valer: são duas frases no README
dizendo que "feche o agente" significa o *Sair* da bandeja. Hoje o documento
declara uma precondição que o Gerenciador de Tarefas não consegue cumprir, e o
diagnóstico que depende dela começa errado — pelo segundo dia seguido, agora pela
outra ponta do mesmo problema.

Este PR **não altera código**: acrescenta apenas este documento.
