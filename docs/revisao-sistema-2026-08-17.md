# 🔍 Revisão técnica do sistema — 2026-08-17

> ⚠️ **`main` continua em `26c9379`, de 2026-08-06.** Os PRs **#10** a **#19**
> seguem abertos e nenhum crítico foi tocado. É o **décimo primeiro dia
> consecutivo** sem um commit de correção.

As revisões anteriores foram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos
caminhos de exceção, pelo resíduo em disco, pela cadeia de entrega e pela
convivência de mais de uma instância. Esta foi por uma região vizinha e ainda
não aberta: **o que o sistema faz depois que algo já quebrou** — os caminhos de
recuperação.

O sistema tem quatro mecanismos de recuperação, e todos são reais: o supervisor
reinicia o agente (`supervisor.go:29-55`), o SCM reinicia o comparador três
vezes com 60 s de intervalo (`AgenteBiometria.wxs:85-90`), o worker é descartado
e recriado quando o SDK devolve um erro que exige reinício (`worker.go:120-129`),
e o cliente JS refaz a descoberta quando o token expira
(`integra-biometria.js:103-112`). Cada um foi escrito com cuidado e resolve o
problema que motivou sua existência.

A pergunta desta revisão foi outra: **quando um deles roda, quem fica sabendo?**
A resposta é o que produz **dois críticos novos**: o agente decide uma única vez,
no arranque, se vai delegar a comparação, e nenhuma recuperação do comparador
alcança um agente que já está de pé; e a parada do comparador desiste depois de
10 s de encerrar o worker, deixando órfão exatamente o processo que o comentário
do código diz que ela existe para matar — o que quebra a atualização seguinte.

Além deles, **cinco alertas** e **quatro sugestões**. Os 20 críticos abertos
foram reconferidos contra `26c9379` e **todos continuam válidos**.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, das quais 1.055 em 7
arquivos de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/AgenteBiometria.wixproj`,
`instalador/msi/build-msi.cmd`, `conferir-biometria.cmd`, `embutir-icone.py`,
`go.mod`/`go.sum`, `.gitignore`, `README.md` e `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `gofmt -l .` | **nada a formatar** |
| `go test ./...` (alvo do host) | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo |
| `GOOS=windows GOARCH=386 go test ./...` | **`exec format error`** — compila e não roda. As 48 funções `Test*` continuam sem executar em lugar nenhum. Segue valendo o **A8 de 30/07** |
| `git log -1 origin/main` | `26c9379`, de **2026-08-06** (11 dias) |
| `grep -n "iniciaLog" *.go` | base do A5 — três dos quatro modos de linha de comando nunca ligam o log |
| `grep -n "configuraComparador" *.go` | base do C1 — uma única chamada em todo o ciclo de vida do agente |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O agente decide **uma única vez**, no arranque, se delega a comparação — e nenhuma das recuperações do comparador alcança um agente já de pé *(novo)*

**Arquivos:** `main.go:885-894`, `delegacao.go:47`, `delegacao.go:49-99`,
`main.go:436-448`, `main.go:547-558`, `anuncio.go:113-121`,
`instalador/msi/AgenteBiometria.wxs:85-90`

`configuraComparador()` roda exatamente uma vez, na inicialização do agente:

```go
// main.go:889-894 — a única chamada em todo o ciclo de vida do processo
origens = novoGerenciadorOrigens()
configuraComparador()
defineDLL(achaDLL())
```

E o resultado dela vira um ponteiro de pacote que nada mais escreve:

```go
// delegacao.go:47
var comparadorRemoto *clienteComparador
```

Todo pedido de comparação, para sempre, lê essa decisão congelada:

```go
// main.go:436-448
if comparadorRemoto != nil {
	ok, err = comparadorRemoto.compara(ctx, body.BiometriaBenef, body.BiometriaLida)
} else {
	ok, err = naThreadSDK(ctx, func() (bool, error) { ... })
}
```

Não há releitura do anúncio, nem ao falhar uma comparação, nem por tempo, nem
quando o arquivo muda. `leAnuncio()` (`anuncio.go:59-80`) é chamado **uma vez**,
de dentro de `configuraComparador`.

**Por que é um problema.** Toda a engenharia de recuperação do sistema está do
lado do comparador, e ela não tem para quem falar.

*O caminho da queda.* O MSI configura o SCM para reiniciar o serviço três vezes,
com um minuto de espera entre elas:

```xml
<!-- AgenteBiometria.wxs:85-90 -->
<util:ServiceConfig
    FirstFailureActionType="restart"
    ...
    RestartServiceDelayInSeconds="60"
    ResetPeriodInDays="1" />
```

Existe, portanto, uma janela **projetada** de pelo menos 60 segundos sem
comparador. Se um agente for iniciado dentro dessa janela — e é o que acontece
com qualquer usuário que faça logon no minuto seguinte à queda, num RDS com
dezenas de sessões —, `leAnuncio()` falha, `configuraComparador()` retorna, e
esse agente compara **localmente pelo resto da vida do processo**. Dentro da
sessão RDP isso é o caminho documentado em
[docs/diagnostico-verifymatch-rdp-2026-07-30.md](diagnostico-verifymatch-rdp-2026-07-30.md)
como fatal: o `ftapihook32` está injetado, e a comparação falha ou derruba o
worker. O serviço volta 60 segundos depois, saudável, e esse agente **nunca fica
sabendo**.

*O caminho do token.* Pior ainda quando o anúncio some antes do reinício (o
**C5 do #10**, aberto): o serviço volta, `tokenDoComparador()`
(`anuncio.go:113-121`) não acha anúncio, gera um token novo e o publica. Os
agentes que já estavam de pé seguem com o token velho em memória e passam a
receber **401 em toda comparação**, indefinidamente. O **C5 do #10** descreve
esse defeito pelo lado do serviço; este crítico é a outra metade, e é a que
determina o tempo de recuperação: mesmo com o serviço perfeito, o agente só
volta a funcionar quando o **usuário fizer logoff e logon**.

*O que o operador vê.* Nada que aponte para cá. `/api/status` responde
`comparador: "local"` (`main.go:329-333`) como se fosse configuração, o ícone da
bandeja continua verde (ele só olha o leitor, `main.go:797-826`), e o
`agente.log` daquela sessão não tem uma linha sobre o assunto — porque o único
`registraErro` desse caminho está atrás de uma condição que, numa instalação por
MSI, é sempre falsa (`delegacao.go:58-65`, o **C2 do #19**).

Vale dizer o que **não** conserta: reiniciar o serviço não conserta (o agente não
relê), reiniciar o agente conserta mas ninguém tem motivo para tentar, e o
supervisor — que reinicia o agente em caso de falha — não é acionado, porque do
ponto de vista do processo nada falhou.

**Como corrigir.** A decisão precisa deixar de ser um estado de arranque e virar
uma leitura barata. O arquivo tem menos de 300 bytes e está em disco local:

```go
// delegacao.go — releitura com cache curto. O anuncio e a verdade; o ponteiro
// em memoria e so um cache dela, e um cache de biometria nao pode durar horas.
var (
	comparadorMu     sync.RWMutex
	comparadorRemoto *clienteComparador
	comparadorLido   time.Time
)

// comparador devolve o cliente valido agora, reconferindo o anuncio no maximo
// uma vez a cada 5 segundos. Custa um os.Stat por comparacao no pior caso.
func comparador() *clienteComparador {
	comparadorMu.RLock()
	c, lido := comparadorRemoto, comparadorLido
	comparadorMu.RUnlock()
	if time.Since(lido) < 5*time.Second {
		return c
	}
	return reconfiguraComparador()
}
```

E a mudança de estado precisa aparecer no log, porque é ela que explica o
atendimento que falhou:

```go
// delegacao.go, dentro de reconfiguraComparador
if antes == nil && agora != nil {
	registraInfo("comparador apareceu em %s: a comparacao deixa de ser local", agora.base)
}
if antes != nil && agora == nil {
	registraErro("o comparador em %s sumiu do anuncio: a comparacao volta a ser local nesta sessao", antes.base)
}
```

Um degrau abaixo, e ainda útil se a releitura periódica for considerada cara:
reconfigurar **sob falha**, que é o momento em que a informação velha se
denuncia sozinha:

```go
// main.go, em handleComparar — 401 ou "inacessivel" significam anuncio velho
if err != nil && comparadorRemoto != nil {
	if reconfiguraComparador() != nil {
		ok, err = comparadorRemoto.compara(ctx, body.BiometriaBenef, body.BiometriaLida)
	}
}
```

Note que a correção também fecha o buraco de arranque do **C2 do #19** por outro
lado: um agente que nasceu sem anúncio passa a se recuperar sozinho quando o
serviço volta.

---

### C2. Parar ou atualizar o comparador durante uma identificação deixa o worker órfão segurando o executável — exatamente o que o código diz que essa parada existe para evitar *(novo)*

**Arquivos:** `comparador.go:131-141`, `servico.go:54-62`, `main.go:828-840`,
`main.go:107-138`, `worker.go:339-345`, `worker.go:243-262`,
`instalador/msi/AgenteBiometria.wxs:93-99`

O comentário é explícito sobre o que essa sequência protege:

```go
// comparador.go:132-141
// Encerra o worker antes de sair. Sem isto ele sobrevive ao servico: fica
// orfao segurando o proprio executavel, e a atualizacao seguinte falha com
// "arquivo em uso" depois de o servico ja ter parado - ou seja, com o
// comparador fora do ar. Cada reinicio ainda deixaria mais um para tras.
ctxSaida, cancelaSaida := context.WithTimeout(context.Background(), 10*time.Second)
encerraSDK(ctxSaida)
cancelaSaida()
```

Só que `encerraSDK` não fala com o worker: ele **entra na fila da thread do
SDK**, atrás de tudo o que já estiver lá.

```go
// main.go:828-836
func encerraSDK(ctx context.Context) {
	_, err := naThreadSDK(ctx, func() (struct{}, error) {
		...
		err := sdkInst.encerra()
```

```go
// main.go:132-137 — e naThreadSDK desiste quando o contexto vence, sem cancelar
// nada: a tarefa em execucao segue rodando na thread do SDK.
select {
case res := <-resultado:
	return res.valor, res.err
case <-ctx.Done():
	return zero, ctx.Err()
}
```

E a tarefa que pode estar na frente dura muito mais que 10 segundos:

```go
// worker.go:339-345 — identificacao: 30s + 25ms por candidato, ate 3 minutos
func (c *clienteWorker) identifica(lida string, candidatos []candidatoJSON) (string, []string, error) {
	limite := 30*time.Second + time.Duration(len(candidatos))*25*time.Millisecond
	if limite > 3*time.Minute {
		limite = 3 * time.Minute
	}
```

**Por que é um problema.** Faça a conta de uma parada durante uma identificação
de 5.000 candidatos (o teto que `maxCandidatos` autoriza, `main.go:46`):

| t | o que acontece |
|---|---|
| 0 s | o SCM pede Stop; `Execute` chama `cancelaApp()` (`servico.go:56`) |
| 0 s | `servidor.Shutdown` começa, com 10 s de prazo (`comparador.go:118-122`) |
| 10 s | `Shutdown` vence o prazo com a requisição ainda em curso, e `Serve` retorna |
| 10 s | `encerraSDK` enfileira a tarefa de encerramento — atrás da identificação |
| 20 s | `encerraSDK` vence os 10 s dele, registra `encerrar SDK: context deadline exceeded` e **retorna** |
| 20 s | `rodaComparadorCom` retorna; `Execute` recebe `<-saida` **antes** dos 12 s dele (`servico.go:59`) e reporta `Stopped` |
| 20 s | `os.Exit` — **com o worker ainda dentro da DLL, comparando o candidato 2.000** |
| até 155 s | o worker só percebe o EOF do stdin quando termina a operação, e só então sai |

Durante esses ~135 segundos existe um processo `AgenteBiometria.exe` rodando,
que **não é serviço nenhum**, mantendo aberto o executável em
`Program Files (x86)`. O `ServiceControl` do MSI (`AgenteBiometria.wxs:93-99`)
usa `Stop="both"` e `Wait="yes"`, então a atualização espera o **serviço** parar
— e ele para, corretamente, aos 20 s. O que ela não espera é o worker, que o
Windows Installer vai encontrar segurando o arquivo que ele precisa substituir.
O resultado é o que o comentário descreve: falha de atualização **com o
comparador já fora do ar**.

Três agravantes:

1. **Reinício automático piora.** A política de `restart` do SCM
   (`AgenteBiometria.wxs:85-90`) sobe uma instância nova 60 s depois. Se o
   worker antigo ainda estiver vivo — e o cálculo acima mostra que pode estar —
   há dois processos falando com a mesma DLL, um deles sem dono.
2. **A parada por desligamento é pior.** `svc.AcceptShutdown` (`servico.go:27`)
   dá ao serviço um orçamento apertado do Windows; o worker órfão não recebe
   nada e é morto pelo desligamento no meio de uma chamada nativa.
3. **O prazo do SCM foi anunciado e não é usado.** `Execute` promete
   `WaitHint: 15000` (`servico.go:55`), mas só espera 12 s (`servico.go:59`), e
   `encerraSDK` só usa 10 dos 12. A folga declarada ao Windows não chega a quem
   precisa dela.

**Como corrigir.** A correção robusta não depende de prazo nenhum: **prender o
worker ao pai com um Job Object**, para o Windows matá-lo junto, aconteça o que
acontecer — inclusive num `os.Exit` ou num `TerminateProcess` vindo do SCM.

```go
// worker.go, em sobe() — o filho morre com o pai por construcao, e nao por
// cortesia. E o unico jeito que sobrevive a uma parada abrupta do servico.
job, err := windows.CreateJobObject(nil, nil)
if err == nil {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	_ = windows.AssignProcessToJobObject(job, windows.Handle(cmd.Process.Pid))
	c.job = job
}
```

E, independente disso, a saída limpa deve matar o worker **fora da fila do SDK**,
porque a fila é justamente o que pode estar entupido:

```go
// comparador.go — derruba direto, sem passar por naThreadSDK. Perder a chamada
// de NBioAPI_Terminate de um processo que vai morrer nao custa nada; deixar o
// processo vivo custa a atualizacao.
if c, ok := sdkInst.(*clienteWorker); ok {
	registraInfo("comparador: derrubando o worker (saida: %s)", c.derruba())
} else {
	encerraSDK(ctxSaida)
}
```

Por fim, os três prazos precisam parar de se contradizer: se o `WaitHint` diz
15 s, `Execute` deve esperar 15 s, e o encerramento do SDK deve caber dentro
disso com folga — ou, melhor, o `Execute` deve reenviar `StopPending` com
`WaitHint` renovado enquanto o worker não morre, que é para isso que o protocolo
do SCM tem esse campo.

---

## 🟡 Alertas (recomenda correção)

### A1. Um `certutil` que falha desliga o HTTPS que já estava funcionando — e o passo só é refeito porque ninguém pergunta se ele já foi dado *(novo; explica a causa do A1 do #13)*

**Arquivos:** `cert.go:125-141`, `cert.go:115-123`, `main.go:908-912`

```go
// cert.go:125-141
func carregaTLS() (*tls.Config, error) {
	if err := gerarCert(); err != nil {
		return nil, err
	}
	certPath, keyPath := caminhosCert()
	if err := instalaCertificadoUsuario(certPath); err != nil {
		return nil, err          // <- TLS inteiro cai por causa deste passo
	}
	...
}
```

`gerarCert()` é idempotente e sabe disso: ele só regenera quando o par é
inválido ou está perto de expirar (`cert.go:92-97`). Já
`instalaCertificadoUsuario` roda `certutil.exe -user -addstore -f Root` **em todo
arranque** (`cert.go:115-123`), sem perguntar se aquele certificado já está no
repositório do usuário — e um erro seu derruba o `carregaTLS` inteiro:

```go
// main.go:908-912 — a consequencia e silenciosa e permanente para aquele processo
tlsCfg, err := carregaTLS()
if err != nil {
	registraErro("TLS desabilitado: %v", err)
}
usaTLS = tlsCfg != nil
```

O certificado continua válido no disco, o repositório do usuário continua com
ele, e mesmo assim o agente sobe em **HTTP puro** porque um comando externo —
`certutil.exe` — falhou. Num RDS isso não é hipótese: AppLocker, política de
software restrito e antivírus corporativo bloqueiam `certutil.exe` com
frequência, justamente porque ele é uma ferramenta de download conhecida. O
efeito é o **A1 do #13** (o mesmo servidor se comportando de dois jeitos); o que
faltava era a causa, e ela é esta: um passo idempotente que falha fechado.

**Como corrigir.** Só instalar quando faltar, e nunca deixar esse passo derrubar
o resto:

```go
// cert.go — a chave e o certificado ja carregaram; o certutil e conveniencia
// para o navegador confiar, nao pre-requisito do servidor.
cert, err := tls.LoadX509KeyPair(certPath, keyPath)
if err != nil {
	return nil, err
}
if err := instalaCertificadoUsuario(certPath); err != nil {
	// Sem isso o navegador vai reclamar do certificado, mas o agente atende
	// em HTTPS e o cliente ja sabe cair para HTTP se precisar.
	registraErro("nao consegui confiar no certificado (%v): o HTTPS continua de pe", err)
}
return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
```

Vale registrar o rastro que ninguém limpa: cada regeneração acrescenta **mais um**
certificado autoassinado ao repositório `Root` do usuário, e nem o MSI
(`AgenteBiometria.wxs:123-135` remove só `comparador.json` e `comparador.log`)
nem o agente removem qualquer um deles na desinstalação.

### A2. A janela de indisponibilidade que o próprio desenho cria não tem retentativa em camada nenhuma, e o operador recebe a mensagem de rede em cru *(novo)*

**Arquivos:** `main.go:449-452`, `main.go:559-562`, `delegacao.go:114-117`,
`integra-biometria.js:73-79`, `integra-biometria.js:95-112`

O agente converte qualquer falha do comparador em 502 com o texto interno:

```go
// main.go:449-452
if err != nil {
	registraErro("comparacao: %v", err)
	escreveErro(w, http.StatusBadGateway, err.Error())
	return
}
```

```go
// delegacao.go:114-117 — e o texto e o do net/http, com endereco e porta dentro
resp, err := c.http.Do(req)
if err != nil {
	return fmt.Errorf("comparador inacessivel: %w", err)
}
```

E o cliente JS trata tudo que não seja 401 como erro terminal:

```js
// integra-biometria.js:73-79
if (r.status === 401) throw erroToken()
if (!r.ok) {
  var mensagem = dados && dados.erro ? dados.erro : 'Agente retornou HTTP ' + r.status
  var err = new Error(mensagem)
  err.status = r.status        // <- marca .status, e nao .reconectavel
  throw err
}
```

```js
// integra-biometria.js:103-112 — so o reconectavel e tentado de novo
async function tentaComReconexao(exec) {
  try { return await exec() } catch (e) {
    if (!e.reconectavel) throw e
    ...
```

Junte com a política de reinício do SCM (60 s, `AgenteBiometria.wxs:89`): a cada
queda do comparador existe **pelo menos um minuto** em que toda verificação
biométrica de todas as sessões do servidor falha na primeira tentativa e não é
repetida por ninguém. O que chega à tela do atendente é algo como
`comparador inacessivel: Post "http://127.0.0.1:5150/comparar": dial tcp ...` —
com endereço e porta do serviço interno, para um usuário que não pode fazer nada
com essa informação (e que, pelo **C10 do #13**, é uma informação que também
interessa a quem pode plantar um anúncio em `ProgramData`).

**Como corrigir.** Uma retentativa curta no cliente, para as falhas que são
tipicamente transitórias, e uma mensagem própria para elas:

```js
// integra-biometria.js — 502/503 sao "tente de novo em um instante", nao "deu erro"
if (r.status === 502 || r.status === 503) {
  var err = new Error('O servico de comparacao esta reiniciando. Tente novamente em alguns segundos.')
  err.status = r.status
  err.temporario = true
  throw err
}
```

```js
// e tentaComReconexao passa a cobrir os dois casos
if (e.temporario) { await espera(3000); return exec() }
```

E o 502 do agente deve levar uma mensagem de operador, guardando o texto técnico
para o log — que é onde ele serve:

```go
// main.go
registraErro("comparacao: %v", err)
escreveErro(w, http.StatusBadGateway, "o servico de comparacao nao respondeu; tente novamente")
```

### A3. O supervisor não distingue "o usuário clicou em Sair" de "a bandeja não subiu" — nos dois casos ele desiste, calado *(novo)*

**Arquivos:** `main.go:950-960`, `supervisor.go:29-55`

```go
// main.go:950-960 — qualquer retorno de systray.Run leva ao mesmo codigo 0
systray.Run(onReady, cancelaApp)
cancelaApp()
...
logger.Printf("encerrado")
return 0
```

```go
// supervisor.go:43-45 — e saida 0 significa "foi pedido", entao o supervisor sai
if cmd.Wait() == nil {
	os.Exit(0)
}
```

`systray.Run` retorna tanto quando alguém escolhe **Sair** quanto quando a
bandeja não pôde ser criada. Num RDS com shell restrito, com política de área de
trabalho, ou numa sessão cujo `explorer.exe` ainda não subiu no momento do
`HKLM\...\Run`, o segundo caso é o provável — e o agente **desaparece no logon**,
deixando no log a mesma linha `encerrado` que uma saída pedida deixaria. O
supervisor, que existe para reiniciar o agente após falhas
(`README.md:34`), entende isso como "trabalho concluído".

**Como corrigir.** Separar as duas saídas, que é barato porque o clique já é
conhecido:

```go
// main.go — quem clicou em Sair marca; o resto e falha e merece reinicio
var saidaPedida atomic.Bool
...
case _, ok := <-sair.ClickedCh:
	if ok {
		saidaPedida.Store(true)
		systray.Quit()
	}
...
systray.Run(onReady, cancelaApp)
if !saidaPedida.Load() {
	registraErro("a bandeja encerrou sem que ninguem pedisse: o supervisor vai reiniciar")
	return 2
}
```

### A4. O único laço que reabre o SDK depois de uma falha não tem prazo, e nunca registra que o leitor sumiu *(novo)*

**Arquivos:** `main.go:797-826`, `main.go:63`, `main.go:107-138`

```go
// main.go:805-812 — ctx aqui e o ctxApp: so termina quando o agente termina
conectado, err := naThreadSDK(ctx, func() (bool, error) {
	s, err := ensureSDK()
	...
})
if err == nil && conectado {
	systray.SetIcon(iconeVerde)
	...
} else {
	systray.SetIcon(iconeVermelho)
```

Dois problemas no mesmo trecho, e os dois são do tema desta revisão.

O primeiro: este é o laço que chama `ensureSDK()` de tempos em tempos e, com
isso, é o que percebe o leitor voltando depois de uma desconexão — a recuperação
mais comum do sistema. Ele passa `ctxApp`, **sem prazo**. Se a fila do SDK (16
posições, `main.go:63`) estiver ocupada por uma identificação de até 3 minutos —
o **C4 de 30/07**, aberto —, ele fica preso lá, e o ícone continua **mostrando o
último estado conhecido** o tempo todo. Um ícone verde durante três minutos em
que nada funciona é pior que nenhum ícone.

O segundo: a transição não vira log. O ícone muda de cor e nada mais acontece.
"Desde quando parou?" é a primeira pergunta de qualquer chamado de suporte, e o
`agente.log` — que registra cada captura e cada comparação — não tem uma linha
sobre o leitor ter sumido às 14h02 e voltado às 14h09.

**Como corrigir.** Prazo próprio e registro só nas mudanças:

```go
// main.go — sondagem e pergunta de tela: 10s bastam, e o que passa disso ja e
// resposta ("ocupado"), nao espera.
ctxSonda, cancela := context.WithTimeout(ctx, 10*time.Second)
conectado, err := naThreadSDK(ctxSonda, func() (bool, error) { ... })
cancela()

if conectado != ultimoEstado {
	if conectado {
		registraInfo("leitor detectado")
	} else {
		registraErro("leitor deixou de responder: %v", err)
	}
	ultimoEstado = conectado
}
```

### A5. As três ferramentas de diagnóstico de linha de comando nunca ligam o log — e a mais usada esconde justamente o motivo de não estar delegando *(novo)*

**Arquivos:** `autoteste.go:424-456`, `autoteste.go:508-518`, `log.go:12`,
`log.go:14`, `delegacao.go:58-85`, `main.go:850-858`, `conferir-biometria.cmd:39`

O logger nasce apontando para o vazio, e só três lugares o religam:

```go
// log.go:12
var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)
```

| quem religa | onde |
|---|---|
| agente | `main.go:885` (`iniciaLog`) |
| worker | `worker.go:68` |
| comparador | `comparador.go:40` |
| **`--conferir-contra`** | **ninguém** |
| **`--teste-delegacao`** | **ninguém** |
| **`--autoteste`** | ninguém (tem relatório próprio, `autoteste.go:82-94`) |

O `--autoteste` se salva com o relatório dele. Os outros dois, não — e o
`--conferir-contra` é a ferramenta que o `conferir-biometria.cmd` põe na mão do
suporte. Ele chama `configuraComparador()` na primeira linha útil:

```go
// autoteste.go:424-428
func confereContra(caminho string) int {
	...
	configuraComparador()
```

E `configuraComparador` explica o que está errado **só pelo log**:

```go
// delegacao.go:76-85 — as tres explicacoes possiveis, todas para io.Discard
registraErro("endereco do comparador invalido (%q): a comparacao continua local", base)
...
registraErro("token do comparador ausente ou curto demais: a comparacao continua local")
```

O que o operador vê, então, é isto — e só isto:

```go
// autoteste.go:449-455
fmt.Println("comparacao: local, neste processo")
if temGanchoDeRedirecionamento() {
	fmt.Println("  ATENCAO: ha gancho de redirecionamento neste processo e nenhum")
```

"A comparação vai ser local" sem **por quê**: se não há anúncio, se o anúncio tem
token curto, se o endereço é inválido, ou se as variáveis de ambiente estão pela
metade. É a diferença entre "instale o serviço" e "conserte o token", e a
ferramenta de diagnóstico não diz qual.

**Como corrigir.** Uma linha em cada modo, e o log passa a existir:

```go
// autoteste.go, no comeco de confereContra e de testeDelegacao
iniciaLogArquivo("diagnostico.log")
```

E, como o operador está olhando o terminal e não o arquivo, vale ecoar a decisão
com o motivo — o que `configuraComparador` já sabe e hoje joga fora:

```go
// delegacao.go — devolver o motivo em vez de so registra-lo
func configuraComparador() string   // "" quando delegou; o motivo quando nao
```

```go
// autoteste.go
if motivo := configuraComparador(); motivo != "" {
	fmt.Println("comparacao local porque:", motivo)
}
```

---

## 🟢 Sugestões (opcional)

### S1. O comparador não tem o limitador de concorrência que o agente tem

`limiteHTTP` (32 vagas, `main.go:68`) e o `503 agente ocupado`
(`main.go:252-260`) vivem dentro do `middleware`, que é do modo agente. O
comparador monta a cadeia com `exigeSegredo(segredo, mux)` (`comparador.go:102`)
e nada mais — então o único freio dele é o `limiteIdentificar` (2 vagas), que
está dentro do próprio `handleIdentificar` (`main.go:484-492`). O `/comparar` do
comparador é ilimitado: cada requisição decodifica até 256 KB e enfileira uma
tarefa na fila de 16 posições, e o excedente vira goroutine bloqueada em
`naThreadSDK` até o contexto de 45 s vencer. É o processo que atende **o servidor
inteiro**, num binário de 32 bits, e ele é o que tem menos proteção. Extrair o
limitador do `middleware` para uma função e aplicá-la nos dois é um diff de
poucas linhas.

### S2. `/api/status` já é o lugar certo para responder "de onde veio essa decisão"

O campo `comparador` hoje devolve `"local"` ou a URL (`main.go:329-333`), sem
dizer se isso veio de variável de ambiente, do anúncio, ou de um anúncio que não
existia no arranque. Com o C1 corrigido, quatro campos baratos fecham o
diagnóstico de fora da máquina, sem ninguém precisar abrir sessão no servidor:

```json
"comparador": { "base": "http://127.0.0.1:5150", "origem": "anuncio",
                "anuncio_pid": 4184, "lido_em": "2026-08-17T08:31:02Z" }
```

### S3. O campo `PID` do anúncio segue gravado e nunca lido — e ele responde "esse comparador ainda existe?"

`publicaAnuncio` grava `PID` desde o começo (`anuncio.go:82-96`) e nenhum leitor
o usa (o **A5 do #18**). Além do uso proposto no **C2 do #19** (não apagar o
anúncio de outro processo), ele resolve o caso que o `defer removeAnuncio()` não
cobre: quando o serviço **cai** em vez de parar, o anúncio fica para trás
apontando para uma porta morta. Um `OpenProcess` no PID anunciado, feito na
releitura do C1, distingue "o comparador está no ar" de "isto é o rastro de um
que morreu" antes da primeira comparação falhar.

### S4. A descoberta do cliente pode ter o pior caso cortado pela metade

`descobrir()` varre 5000→5099 e, para cada porta, `hello()` tenta os **dois**
protocolos com 700 ms cada (`integra-biometria.js:114-138`, `179-189`). Portas
fechadas recusam na hora e não custam nada, mas qualquer porta dessa faixa
ocupada por outro programa que aceite e não responda custa 1,4 s. Varrer todas as
portas no protocolo preferido primeiro, e só depois repetir no outro, tem o mesmo
resultado com metade do pior caso — e o protocolo preferido acerta quase sempre,
porque `conecta()` já grava o endereço completo em `localStorage`
(`integra-biometria.js:144-151`).

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 33 (22 `.go`, 1 `.js`, 1 `.ps1`, 3 do MSI, 2 `.cmd`, 1 `.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e 2 docs)
- **Segurança**: 🚨 **Risco** — sem críticos de segurança novos hoje, mas nenhum dos abertos foi tocado: o dado pessoal em log legível (**C14 do #17**), o `ProgramData` gravável (**C10 do #13**), o agente impostor na 5000 (**C13 do #15**) e a revogação de origens que se desfaz sozinha (**C19 do #19**) seguem todos válidos contra `26c9379`
- **Qualidade**: ⚠️ Atenção — `build`, `vet` e `gofmt` limpos; o ponto fraco desta revisão é que **nenhum caminho de recuperação tem teste, log de transição ou prazo próprio**
- **Risco de produção**: 🚨 **Alto** — o C1 faz uma sessão comparar dentro da jaula do RDP para sempre, em silêncio, depois de uma queda de 60 s do comparador; o C2 quebra a atualização, que é o único canal por onde qualquer correção chegaria
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que não rodam em alvo nenhum (`matched no packages` no host, `exec format error` em `windows/386`) e nenhum CI. **A8 de 30/07, aberto há 18 dias**

### Inventário dos críticos abertos contra `26c9379`

| # | Origem | Crítico | Estado |
|---|---|---|---|
| 1 | main (07-30) | `bioPort` do fragmento não validado | **ABERTO** |
| 2 | main (07-30) | Cliente JS descarta `ignorados` | **ABERTO** |
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
| 21 | **hoje** | **Agente decide delegar uma vez e nunca reavalia** | **NOVO** |
| 22 | **hoje** | **Parada do comparador deixa worker órfão e quebra a atualização** | **NOVO** |

---

## ✅ Pontos positivos

**A recuperação do worker é o melhor pedaço do sistema, e por isso serviu de
régua.** `worker.go:120-129` descarta a instância do SDK só quando o código de
erro justifica (`sdk.go:89-95`), `worker.go:198-200` segura a subida quando as
falhas se repetem para não trocar um crash por uma tempestade de processos, e
`worker.go:300-309` reconhece que "o SDK devolveu erro" é sinal de **saúde** do
worker e zera o contador. É um mecanismo de recuperação que sabe distinguir os
três estados que importam — sadio, doente e morto —, e os dois críticos de hoje
são, no fundo, a observação de que as outras camadas não têm nada equivalente.

**O `--conferir-contra` acerta a parte mais difícil.** Os códigos de saída
separam "confere", "não confere" e "falhou" (`autoteste.go:418-424`), e o
`conferir-biometria.cmd:44-50` traduz os três em português com a ressalva certa
("não é veredito biométrico: nada foi comparado"). É a distinção que um script de
integração erra por padrão, e aqui ela foi decidida antes de alguém precisar
dela. O A5 é sobre o que falta ao redor dessa ferramenta, não sobre ela.

**O autoteste foi construído para sobreviver ao próprio fracasso.**
`autoteste.go:96-106` grava e faz `Sync` linha a linha porque o passo seguinte
pode matar o processo, e `autoteste.go:75-81` explica que essa decisão nasceu de
um relatório perdido. É a mesma disciplina de recuperação que falta ao arranque
do agente, aplicada onde alguém já se queimou.

**O `ligaConsole` não rouba a saída de quem redirecionou** (`autoteste.go:41-51`),
por um motivo registrado no comentário: dois diagnósticos se perderam numa janela
de console que fechou. É um detalhe de três linhas que só existe porque alguém
tratou "o diagnóstico sumiu" como defeito de software, e não como azar.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O sistema chega hoje a **22 críticos abertos** e **onze dias** sem um commit de
correção. Os dois de hoje têm em comum uma coisa que as revisões anteriores não
tinham tocado: **as recuperações existem, funcionam, e não conversam entre si**.
O SCM reinicia o comparador para um agente que não vai reler o anúncio; a parada
limpa desiste de matar o worker e entrega ao instalador o arquivo travado que ela
existia para evitar. Nos dois casos o componente que se recupera fica são, e quem
paga é o vizinho — que não tem como saber que algo aconteceu.

A sequência recomendada muda pouco em relação à do #19, e ganha um item na frente
porque ele é pré-requisito de todo o resto:

1. **C2 (parte do log) do #19** — as três linhas de `delegacao.go:58-65` que
   fazem o agente dizer "não há comparador anunciado, vou comparar localmente".
   Continua sendo o menor diff do repositório e o que ilumina mais coisas ao
   mesmo tempo, e agora também o **C1** de hoje.
2. **C1 de hoje** — releitura do anúncio com cache curto. Sem ela, nenhuma
   correção do lado do comparador chega a um agente já de pé, o que torna todas
   as outras mais lentas de validar em campo.
3. **C2 de hoje** — o Job Object no worker. É a correção que não depende de
   nenhum prazo estar certo, e sem ela a atualização — o canal por onde tudo isto
   seria entregue — não é confiável.
4. **C3 do #18** — versão derivada do repositório e `AllowSameVersionUpgrades`,
   com a assinatura do **A1 do #19**. Sem os dois não há como afirmar que uma
   máquina recebeu uma correção nem que o que ela recebeu é o que foi publicado.
5. **C19 do #19** — a união na gravação de `origens-autorizadas.json`. Segue
   sendo o único aberto em que o sistema desfaz sozinho uma decisão de segurança
   tomada pelo usuário.
