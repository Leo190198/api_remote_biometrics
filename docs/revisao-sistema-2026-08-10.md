# 🔍 Revisão técnica do sistema — 2026-08-10

> ⚠️ **`main` continua em `26c9379`**, o mesmo HEAD de 2026-08-07. Os PRs **#10**,
> **#11** e **#12** seguem abertos e nenhum dos críticos foi tocado. Este é o
> quarto dia consecutivo.

Repetir a mesma lista pela quinta vez não ajuda ninguém. Esta revisão faz três
coisas diferentes:

1. **corrige um crítico anterior.** O `bioPort` (C3 do PR #12, aberto desde
   2026-07-30 e listado como *prioridade 1*) **não vaza template biométrico
   nenhum** — o `fetch()` recusa URL com credenciais desde 2017. O problema é
   real, mas é outro problema, com outra severidade e outra urgência. Ver
   **[R1](#r1-correção-o-bioport-não-vaza-biometria--o-fetch-recusa-url-com-credenciais)**;
2. **acrescenta dois críticos novos**, ambos consequência de uma mudança que as
   revisões anteriores analisaram peça por peça mas não como sistema: o
   comparador deixou de ser *um processo por sessão* e virou **um processo para o
   servidor inteiro**. Orçamentos dimensionados para um agente de um usuário
   foram herdados sem recontagem (**C1**), e o modelo de ameaça do anúncio foi
   escrito para um leitor, não para um autor (**C2**);
3. **mostra que a correção proposta ontem para o C2 não fecha o buraco** — ela
   tapa o caminho pelo `HKCU\Environment` e deixa aberto o caminho pela ACL
   herdada do `ProgramData`, que é o mais grave dos dois porque atinge *todos* os
   usuários do servidor, e não só quem o executou.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, incluindo os 7 de
teste), `integracao/integra-biometria.js`, `integracao/COMO-USAR.md`,
`instalador/instalar-servidor.ps1`, `instalador/msi/AgenteBiometria.wxs`,
`instalador/msi/build-msi.cmd`, `instalador/msi/AgenteBiometria.wixproj`,
`conferir-biometria.cmd`, `embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`,
`README.md` e os dois documentos em `docs/` — 40 arquivos rastreados.

**Verificações executadas:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | OK |
| `GOOS=windows GOARCH=386 go vet ./...` | limpo, inclusive nos arquivos de teste |
| `go test ./...` | **não executável aqui** — as *build tags* `windows && 386` exigem alvo real |
| Reprodução isolada de `leAnuncio` + validação de `configuraComparador` (cópia fiel, compilada e executada em Linux) | confirma **C2**: `endereco` fora do loopback é aceito |
| WHATWG Fetch, passo do construtor `Request` | confirma **R1**: URL com credenciais lança `TypeError` |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O comparador atende o servidor inteiro por uma fila de SDK de uma via — uma identificação 1:N para a biometria de todas as sessões *(novo)*

**Arquivos:** `comparador.go:73`, `main.go:100-105`, `main.go:63-64`,
`main.go:432`, `main.go:543`, `worker.go:339-346`

```go
// comparador.go:73 — uma goroutine, para o servidor todo
go sdkThreadMain()
```

```go
// main.go:100-105 — que executa uma tarefa por vez, para sempre
func sdkThreadMain() {
	runtime.LockOSThread()
	for fn := range sdkTasks {
		fn()
	}
}
```

**Por que é um problema.** No agente essa fila única é a decisão certa: um
processo, um usuário, um leitor. O comparador herdou o mesmo código e mudou o
denominador — ele é **um serviço `Start="auto"` que atende todas as sessões RDP
do servidor** (`AgenteBiometria.wxs:71-91`). A fila continua sendo uma.

A conta que o próprio código faz:

```go
// worker.go:339-346 — quanto tempo uma identificacao pode segurar a fila
func (c *clienteWorker) identifica(lida string, candidatos []candidatoJSON) (string, []string, error) {
	limite := 30*time.Second + time.Duration(len(candidatos))*25*time.Millisecond
	if limite > 3*time.Minute {
		limite = 3 * time.Minute
	}
	r, err := c.envia(pedidoWorker{Op: opIdentificar, A: lida, Candidatos: candidatos}, limite)
```

Com os `maxCandidatos = 5000` que o README anuncia, a fórmula pede 155 s e é
cortada em **180 s**. Durante esses três minutos, `sdkThreadMain` está ocupada.
Toda comparação 1:1 de **qualquer outra sessão** entra em `naThreadSDK` com um
contexto de 45 s (`main.go:432`), espera, estoura, e volta para o navegador como
`502`. O agente daquela sessão está saudável, o leitor está conectado, o
`/api/status` responde `ok`.

Três agravantes que fecham o quadro:

1. **`envia` não aceita contexto.** Ela dorme até `limite` (`worker.go:297-320`).
   O cliente desistir não libera a fila: a tarefa segue segurando a thread até o
   fim, mesmo sem ninguém para ler a resposta.
2. **`limiteIdentificar` de 2 vagas (`main.go:48`) não protege — enfileira.** As
   duas identificações admitidas serializam na mesma thread, então a segunda pode
   esperar 3 min de fila **mais** os seus próprios 3 min, contra um contexto de 4
   min (`main.go:543`). Ela estoura por construção.
3. **O comparador não tem o teto geral do agente.** `limiteHTTP` (32 vagas,
   `main.go:252-260`) vive dentro de `middleware`, e o comparador monta o seu
   servidor com `exigeSegredo(segredo, mux)` (`comparador.go:102`), que não passa
   por lá.

E o mesmo defeito tem uma leitura de segurança. O cabeçalho de `anuncio.go:18-23`
analisa com honestidade o que um usuário local ganha ao ler o segredo — *"o que
um leitor do arquivo ganha é poder confirmar um par de templates que ele já tenha
em mãos"*. A conclusão está certa para **confidencialidade** e não foi feita para
**disponibilidade**: com o segredo em mãos e sem nenhum limite por chamador,
qualquer usuário de qualquer sessão RDP manda dois `POST /identificar` com 5.000
candidatos em laço e **derruba a biometria do servidor inteiro** — sem
privilégio, sem exploit, usando a API como ela foi documentada.

**Como corrigir.** O caminho de fundo é permitir paralelismo onde ele já é
seguro. Cada instância de SDK no comparador é um **processo worker separado**
(`main.go:64` → `novoClienteWorker`), então N workers rodam em paralelo sem
disputar a DLL:

```go
// main.go — no modo comparador, uma fila com K executores em vez de uma thread.
// Continua LockOSThread por executor, que e o que a NBioBSP exige; o que muda e
// que passam a existir K instancias independentes, cada uma no seu processo.
func iniciaPoolSDK(k int) {
	for i := 0; i < k; i++ {
		go func() {
			runtime.LockOSThread()
			inst, err := criaSDK(caminhoDLL())
			if err != nil {
				registraErro("pool do SDK: %v", err)
				return
			}
			defer func() { _ = inst.encerra() }()
			for fn := range sdkTasks {
				fn()
			}
		}()
	}
}
```

Isso exige tirar `sdkInst` de global e passá-lo à tarefa — mudança contida, mas
não trivial. Enquanto ela não vem, **três medidas pequenas já removem o pior**:

```go
// 1. Fila separada para 1:1. Verificacao e o caminho interativo, com o dedo do
//    usuario encostado; 1:N e trabalho de lote e pode esperar.
var sdkTasks1a1 = make(chan func(), 16)

// 2. Teto de candidatos por chamada no modo comparador, para a fila nunca ser
//    segurada por minutos de uma vez so. O sistema web pagina.
const maxCandidatosComparador = 500

// 3. Teto de requisicoes simultaneas tambem no comparador.
Handler: exigeSegredo(segredo, limitaConcorrencia(mux)),
```

E `envia` precisa receber o contexto da requisição, para que um cliente que
desistiu libere a fila em vez de continuar ocupando-a.

---

### C2. `C:\ProgramData\AgenteBiometria` é gravável por `Users` por herança — o anúncio pode ser plantado sem tocar em variável de ambiente, e a correção proposta ontem não fecha esse caminho *(novo)*

**Arquivos:** `instalador/msi/AgenteBiometria.wxs:57-62`, `123-135`,
`anuncio.go:14-16`, `anuncio.go:59-80`, `anuncio.go:101-121`, `delegacao.go:74-85`

O comentário do WiX declara a premissa em que todo o desenho se apoia:

```xml
<!-- AgenteBiometria.wxs:57-59 -->
<!-- ProgramData: onde o servico publica porta e token. A ACL herdada daqui
     ja da leitura a Users e escrita so a SYSTEM e administradores, que e
     exatamente a divisao entre quem le e quem escreve. -->
```

```xml
<!-- AgenteBiometria.wxs:123-124 — a pasta e criada sem ACL propria -->
<Component Id="CompDados" Directory="PASTADADOS" Guid="*">
  <CreateFolder />
```

**Por que é um problema.** A premissa está errada. A ACL padrão de
`C:\ProgramData` é:

```
NT AUTHORITY\SYSTEM:(OI)(CI)(F)
BUILTIN\Administrators:(OI)(CI)(F)
CREATOR OWNER:(OI)(CI)(IO)(F)
BUILTIN\Users:(OI)(CI)(RX)
BUILTIN\Users:(CI)(WD,AD,WEA,WA)      <-- criar arquivos e subpastas
```

A última ACE é `(CI)` — propaga para subpastas. Um `<CreateFolder/>` sem
`util:PermissionEx` herda o conjunto inteiro, então em
`C:\ProgramData\AgenteBiometria` **qualquer usuário logado pode criar arquivos**.
E `CREATOR OWNER:(OI)(CI)(IO)(F)` diz que quem criar leva controle total do que
criou.

Isso abre dois caminhos que o PR #12 não cobre:

**(a) A janela que o próprio código abre a cada parada.** `removeAnuncio()` roda
por `defer` toda vez que o serviço para (`comparador.go:99`). Enquanto ele está
parado — reinício por política de falha, atualização por MSI, `net stop` —
`comparador.json` **não existe**, e criar arquivo é exatamente o direito que
`Users` tem. Na volta:

```go
// anuncio.go:113-121
func tokenDoComparador() (string, error) {
	if t := os.Getenv("COMPARADOR_TOKEN"); len(t) >= 32 {
		return t, nil
	}
	if a, err := leAnuncio(); err == nil {   // <- le o arquivo plantado
		return a.Token, nil
	}
	return geraToken()
}
```

O serviço **adota o segredo escolhido pelo atacante** e o republica como se fosse
seu. Numa instalação por MSI não existe `COMPARADOR_TOKEN` de máquina — é decisão
explícita do `AgenteBiometria.wxs:14-17` —, então esse é o caminho normal, não o
excepcional.

**(b) Quem cria a pasta primeiro é dono dela para sempre.** Se
`C:\ProgramData\AgenteBiometria` ainda não existe (máquina nova, ou após um
`msiexec /x` — que hoje deixa a pasta para trás por causa do
`comparador.log.1`, o A10 do PR #12), um usuário comum cria a pasta, vira
`CREATOR OWNER` com **controle total**, e passa a poder substituir
`comparador.json` a qualquer momento, com o serviço no ar.

Em ambos os casos o efeito é o mesmo, e é o do C2 de ontem — `leAnuncio` não
valida `endereco` e `configuraComparador` só exige que ele tenha *host* e
*esquema*:

```go
// delegacao.go:74-79
base = strings.TrimRight(base, "/")
endereco, err := url.Parse(base)
if err != nil || endereco.Host == "" || (endereco.Scheme != "http" && endereco.Scheme != "https") {
	registraErro("endereco do comparador invalido (%q): a comparacao continua local", base)
	return
}
// nada verifica que endereco.Host e loopback
```

Reproduzi a lógica dos dois trechos em um programa isolado, compilado e
executado:

```
leAnuncio ACEITOU -> base="https://coleta.atacante.example"  configuraComparador aceita=true
leAnuncio ACEITOU -> base="http://10.0.0.9:8080"             configuraComparador aceita=true
```

A partir daí, **todo template que passa por `/api/public/v1/captura` e
`/api/public/v1/identificar` sai da máquina** e o veredito volta de fora
(`main.go:436-460`) — um `true` forjado autentica qualquer beneficiário.

**A diferença para o PR #12 é o alcance, e é ela que faz este item ser novo.** O
caminho pelo `HKCU\Environment` descrito ontem afeta **a sessão de quem o
configurou**: o usuário engana o próprio agente. O caminho pela ACL afeta **todos
os agentes de todas as sessões do servidor**, porque o arquivo envenenado é o
mesmo que todo mundo lê. E a correção proposta ontem — trocar
`os.Getenv("ProgramData")` por `windows.KnownFolderPath` — **fecha só o primeiro**.
Ela é necessária e não é suficiente.

**Como corrigir.** Três camadas, das quais a primeira é a que realmente decide:

```go
// 1. delegacao.go — o comparador vive no MESMO host, sempre. (Mesma correcao
//    proposta no PR #12; e ela que anula os dois caminhos de uma vez.)
host := endereco.Hostname()
if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
	registraErro("comparador fora do loopback (%q) recusado: a comparacao continua local", base)
	return
}
```

```xml
<!-- 2. AgenteBiometria.wxs — parar de herdar a ACL do ProgramData.
     Inheritable="no" e o que remove a ACE (CI)(WD,AD) de Users. -->
<Component Id="CompDados" Directory="PASTADADOS" Guid="*">
  <CreateFolder>
    <util:PermissionEx User="[WIX_ACCOUNT_LOCALSYSTEM]"    GenericAll="yes"  Inheritable="no" />
    <util:PermissionEx User="[WIX_ACCOUNT_ADMINISTRATORS]" GenericAll="yes"  Inheritable="no" />
    <util:PermissionEx User="[WIX_ACCOUNT_USERS]"          GenericRead="yes" Inheritable="no" />
  </CreateFolder>
```

```go
// 3. anuncio.go — conferir o dono na leitura. Um MSI antigo ja instalado nao
//    ganha a ACL nova, entao a checagem em tempo de execucao e o que protege a
//    base ja implantada.
func donoConfiavel(caminho string) bool {
	sd, err := windows.GetNamedSecurityInfo(caminho, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	dono, _, err := sd.Owner()
	if err != nil {
		return false
	}
	return dono.IsWellKnown(windows.WinLocalSystemSid) ||
		dono.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}
```

E o `RemoveFile` do MSI precisa do curinga `comparador.*` (A10 do PR #12) para
que a desinstalação não deixe a pasta para trás — é ela que habilita o caminho
(b) na reinstalação seguinte.

---

### R1. Correção: o `bioPort` **não** vaza biometria — o `fetch()` recusa URL com credenciais

**Arquivo:** `integracao/integra-biometria.js:25-34`, `83-101`, `103-112`
**Status anterior:** 🔴 crítico em todas as revisões desde 2026-07-30; listado
como **prioridade 1** no PR #12
**Status correto:** 🟡 alerta

O trecho é o que as revisões anteriores descreveram:

```js
// integra-biometria.js:26-29
var h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
if (h.get('bioPort')) {
  localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + h.get('bioPort'))
}
```

E é verdade que `'http://localhost:' + '5000@evil.com'` produz uma URL cujo host
é `evil.com`. **O que não é verdade é o passo seguinte.** O construtor de
`Request` do WHATWG Fetch tem um passo explícito: *"If parsedURL includes
credentials, then throw a TypeError."* Todo navegador atual implementa isso —
está disponível de forma consistente desde **março de 2017**. Uma URL com
*userinfo* nunca chega a virar requisição:

```
TypeError: Failed to construct 'Request': Request cannot be constructed
from a URL that includes credentials.
```

Seguindo o caminho no código: o `TypeError` é lançado dentro do `try` de
`requisicao` (`js:95-100`), cai em `erroConexao(e)`, ganha `reconectavel = true`,
e `tentaComReconexao` (`js:103-112`) chama `Biometria.descobrir()`. A descoberta
**não usa `LS_ADDR`** — `hello()` monta a própria URL a partir do número da porta
do laço (`js:120`) — então ela varre 5000-5099, acha o agente e `conecta()`
**sobrescreve o endereço envenenado** (`js:144-151`).

Ou seja: nenhum `X-Bio-Token` sai da máquina, nenhum template sai da máquina, e
o estado se conserta sozinho na primeira reconexão.

**O que sobra de real, e por isso o item continua aberto:**

- **negação de serviço persistente** quando a descoberta *não* consegue se
  recuperar — agente fora do ar, ou origem ainda não autorizada na bandeja. O
  endereço quebrado fica em `localStorage`, e o usuário vê "não foi possível
  falar com o agente" em toda sessão nova, sem nada que indique a causa;
- **`Biometria.configurar({ endereco })` (`js:170-172`) aceita qualquer URL sem
  validação alguma.** Este é o ponto de entrada que *de fato* mandaria templates
  para fora, se um integrador ligar o valor a algo que o usuário controla. É um
  contrato de API, não um bug alcançável de fora — mas merece a validação.

**Como corrigir** (a mesma correção proposta antes; muda a urgência, não o
remédio):

```js
function portaValida(v) {
  if (!/^\d{1,5}$/.test(String(v))) return 0
  var n = Number(v)
  return n >= 1 && n <= 65535 ? n : 0
}

var p = portaValida(h.get('bioPort'))
if (p) localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + p)
```

E, em `configurar`, aceitar apenas `http(s)://localhost:<porta>` ou
`http(s)://127.0.0.1:<porta>`.

**Por que isso importa para o planejamento.** O PR #12 põe este item em primeiro
lugar na ordem de correção, justificando com *"é o único que expõe template
biométrico a um host externo sem nenhum acesso prévio à máquina"*. Essa premissa
não se sustenta. O item que **de fato** expõe template a host externo é o
**C2** — e ele exige acesso local, o que num servidor RDS com dezenas de sessões
é uma barreira bem mais baixa do que parece.

---

### Reincidentes do PR #12 — reconferidos hoje, linha a linha

Não repito o texto; todos continuam válidos nas mesmas linhas do mesmo `26c9379`.

| Achado | Onde | Situação hoje |
|---|---|---|
| **C1 #12** — parar o serviço apaga o anúncio e o token novo derruba os agentes de pé | `comparador.go:99`, `anuncio.go:113-121` | ❌ Aberto. Reconfirmado: `defer removeAnuncio()` e `tokenDoComparador()` inalterados |
| **C2 #12** — endereço do comparador sem checagem de loopback | `delegacao.go:74-85` | ❌ Aberto — e **a correção proposta é incompleta**, ver **C2** acima |
| **C3 #12** — `bioPort` sem validação | `integra-biometria.js:25-34` | 🟡 **Rebaixado**, ver **R1** acima |
| **C4 #12** — MSI e `.ps1` disputam nome e porta | `wxs:71-99`, `instalar-servidor.ps1` | ❌ Aberto. `instalar-servidor.ps1:178-183` ainda grava as três `COMPARADOR_*` de máquina |
| **A1..A11, S1..S10 #12** | — | ❌ Todos abertos; nenhum arquivo mudou |

---

## 🟡 Alertas (recomenda correção)

### A1. Não existe caminho de recuperação quando **nunca** houve anúncio no arranque *(novo)*

**Arquivos:** `main.go:890`, `delegacao.go:49-99`, `anuncio.go:47-57`

`configuraComparador()` roda **uma vez**, no arranque do agente. O PR #12 (C1)
propõe reler o anúncio ao tomar `401`, o que resolve a rotação de token. Mas
existe um estado em que **não há 401 para disparar nada**: se o anúncio não
existia quando o agente subiu, `comparadorRemoto` fica `nil`, e a partir daí o
agente compara localmente **para sempre**, sem nunca mais consultar o arquivo.

Dois cenários comuns levam exatamente a isso:

1. **Instalação com sessões abertas.** O MSI põe a chave `HKLM\...\Run`
   (`wxs:109-117`), que só age no *próximo* logon. Todos os agentes já em pé
   quando o MSI rodou continuam sem comparador — e é o caso normal de instalar
   num RDS em produção;
2. **Serviço fora do ar no logon.** Reinício pela política de falha, ou primeiro
   logon durante o boot antes de o `Start="auto"` concluir.

O comentário de `delegacao.go:55-57` decide o caminho: *"ausência do arquivo
significa que não há comparador nesta máquina, e aí comparar localmente é o
comportamento certo, não uma falha"*. Isso é verdade na estação e **falso no
servidor RDS**, onde comparar localmente significa `0x000B` ou o worker morrendo
por violação de acesso a cada tentativa. E o `/api/status` reporta
`"comparador": "local"` (`main.go:329-333`) como se fosse a configuração
pretendida.

**Como corrigir.** Reler quando a comparação local falhar, que é o único momento
em que a informação vale alguma coisa:

```go
// main.go, em handleComparar/handleIdentificar, antes de cair no caminho local
if comparadorRemoto == nil {
	// Barato: um os.Stat por comparacao. Um agente que subiu antes do
	// servico passa a delegar assim que o servico aparece, sem logoff.
	configuraComparador()
}
```

...combinado com o aviso de gancho proposto no A7 do PR #12, e expondo o estado
em `/api/status` de forma acionável:

```go
info["comparador"] = "local"
if temGanchoDeRedirecionamento() {
	info["comparador"] = "local (RISCO: gancho presente, servico ausente)"
	info["ok"] = false
}
```

### A2. O orçamento de memória de 16 MB foi copiado do agente para um serviço que atende o servidor inteiro *(novo)*

**Arquivos:** `main.go:41`, `main.go:69-72`, `comparador.go:102`

O comentário de `main.go:69-72` explica com precisão como `maxIdentificacoes = 2`
foi dimensionado: *"`/identificar` decodifica corpos de até 16 MB e o pico do
`encoding/json` chega a várias vezes isso… um punhado de requisições simultâneas
estourava a memória do processo"*. A conta foi feita para **um agente de um
usuário** num binário de 32 bits com ~2 GB de espaço de endereçamento.

O comparador reusa `handleIdentificar` inteiro, com os mesmos números, atendendo
agora **N sessões**. As duas vagas de `limiteIdentificar` mantêm o pico de
decodificação sob controle — essa parte continua correta —, mas nada mais do
orçamento foi recontado, e nada limita quantas requisições *no total* o
comparador aceita ao mesmo tempo, porque `limiteHTTP` não é aplicado lá
(`comparador.go:102` monta o servidor com `exigeSegredo(segredo, mux)` direto).
Somado ao A9 do PR #12 — a delegação materializa mais duas cópias completas do
corpo — o pico real por identificação delegada é maior do que o que motivou o
limite, e o limite não foi ajustado.

**Como corrigir.** Recontar explicitamente para o modo serviço e documentar a
conta ao lado do `const`, do jeito que o `main.go:37-48` já faz para os outros
limites. No mínimo, aplicar `limiteHTTP` também no comparador e reduzir
`maxCorpoIdentificar` quando o corpo vem de outro processo nosso, que já validou
a lista.

### A3. `conferir-biometria.cmd` resolve caminho relativo contra a pasta do script *(novo)*

**Arquivo:** `conferir-biometria.cmd:20-23`

```bat
cd /d "%~dp0"

set "ARQ=%~1"
if "%ARQ%"=="" set "ARQ=%~dp0.env.hash"
```

O `cd` acontece **antes** de `%~1` ser resolvido. Quem estiver em
`C:\suporte\caso-1234` e rodar
`C:\...\conferir-biometria.cmd cadastro.hash` recebe
`Nao achei o arquivo com o cadastro: cadastro.hash` — porque o script procurou em
`C:\Program Files (x86)\AgenteBiometria\`. A mensagem de erro mostra o nome que a
pessoa digitou, o que torna o engano difícil de enxergar.

É uma ferramenta de campo, usada por quem está com um problema em mãos. Errar o
caminho em silêncio é o defeito mais caro que ela pode ter.

**Como corrigir.** Resolver antes de trocar de diretório:

```bat
setlocal
rem Resolve o argumento contra o diretorio de quem chamou, nao contra o script.
set "ARQ=%~f1"
cd /d "%~dp0"
if "%ARQ%"=="" set "ARQ=%~dp0.env.hash"
```

### A4. `--conferir-contra` e `--teste-delegacao` chamam `configuraComparador()` com o log ainda em `io.Discard` *(complementa A8 do PR #12)*

**Arquivos:** `autoteste.go:428`, `autoteste.go:513`, `log.go:12`

O PR #12 já aponta que as duas funções não abrem log. Vale registrar a
consequência específica, porque ela toca justamente os dois críticos deste
documento: `configuraComparador()` é a função que **recusaria** um endereço fora
do loopback (**C2**) e que reportaria um anúncio ilegível (**A1**). Todos os
`registraErro` dela (`delegacao.go:62`, `77`, `83`) caem em `io.Discard`.

O operador que rodar `--conferir-contra` para investigar exatamente esses
sintomas vê `comparacao: local, neste processo` e **nenhuma pista** de que existia
um anúncio, de que ele foi lido, e de que foi recusado por um motivo específico.
A ferramenta de diagnóstico é cega justamente para o que ela deveria diagnosticar.

---

## 🟢 Sugestões (opcional)

- **S1.** `anuncio.go:36-43` — o anúncio grava `PID` e nunca o usa. Conferir se o
  processo está vivo (`OpenProcess` + `GetExitCodeProcess`) transformaria um
  anúncio órfão, deixado por uma parada abrupta, em "comparador morto" em vez de
  "comparador inacessível" na primeira comparação.
- **S2.** `main.go:643-653` — `urlSistema()` concatena o token na URL e a entrega
  a `rundll32` (`main.go:782`). O fragmento não vai para o servidor, mas a URL
  completa entra na linha de comando do processo e na restauração de sessão do
  navegador. Passar o token por outro canal (ou aceitar que ele viva só no
  `/api/hello`) evita um lugar a mais de onde ele não sai.
- **S3.** `comparador.go:168-181` — `comparadorStatus` responde `"ok": true`
  incondicionalmente, sem consultar o SDK. É o único ponto de observação externo
  do serviço, e hoje ele responde `ok` com a `NBioBSP.dll` ausente. Um
  `contaDispositivos()` não faz sentido lá (o comparador não tem leitor), mas
  "consegui abrir o SDK" faz.
- **S4.** `main.go:624-632` — `escreveConfig()` grava
  `agente-<sessao>.json` e nada o remove no encerramento. Sobra um arquivo com
  token morto e porta morta em `%LOCALAPPDATA%` a cada sessão.
- **S5.** `.gitignore:19-31` — o bloco que protege template biométrico está
  correto e bem comentado (`*.hash`, `.env.*`, `template*.txt` cobrem o
  `.env.hash` que o `conferir-biometria.cmd` usa por padrão, duas vezes). Vale
  acrescentar `autoteste.log` explicitamente: hoje ele é pego pelo `*.log`
  genérico, e um `*.log` é o tipo de linha que alguém restringe sem perceber o
  que mais estava dependendo dela.
- **S6.** `servico.go:87-94` — `souServico()` imprime em `fmt.Println` quando
  `svc.IsWindowsService()` falha. Sob o SCM, `stdout` não existe; a mensagem
  some. Deveria ir para o log, como o resto.

---

## 📋 Resumo

| | |
|---|---|
| **Arquivos alterados** | 1 neste PR (só documentação); **nenhum em `main`** desde 2026-08-07; 40 revisados |
| **Segurança** | 🚨 Risco |
| **Qualidade** | ⚠️ Atenção |
| **Risco de produção** | 🚨 Alto |
| **Testes** | ⚠️ Parcial |

**Contagem de hoje:** 2 críticos novos, 1 crítico anterior **rebaixado com
justificativa**, 4 alertas novos, 6 sugestões novas. Os 3 críticos restantes do
PR #12 seguem abertos e foram reconferidos linha a linha.

**Sobre os testes.** 47 funções de teste cobrem bem o que é difícil de acertar em
C — `sdk.go`, `worker.go`, `delegacao.go`, `anuncio.go`, `versaodll.go`. Seguem
sem nenhuma cobertura `middleware`, `origins.go`, `session.go`, `servico.go`,
`cert.go` e o cliente JS. Especificamente para os achados de hoje:

| Achado | Teste que o teria pegado |
|---|---|
| **C1** | Um teste que enfileira uma tarefa longa em `sdkTasks` e mede quanto uma curta espera |
| **C2** | Um caso a mais em `TestAnuncioRecusaConteudoInutil` com `"endereco":"https://fora.example"` — uma linha |
| **R1** | Qualquer teste do JS. Não existe nenhum, e é por isso que a severidade errada sobreviveu a quatro revisões |

---

## ✅ Pontos positivos

- **A separação entre "não é a pessoa" e "a comparação quebrou" é levada a sério
  em todo o sistema** — no código de saída `2` de `--conferir-contra`
  (`autoteste.go:411-423`), na lista de `ignorados` que atravessa o processo
  worker (`worker.go:48-51`), no `registraErro` de `main.go:567-572` e na tabela
  do `conferir-biometria.cmd`. Confundir os dois seria o pior defeito possível
  num sistema biométrico, e o código nunca confunde. Vale reforçar isto num dia
  em que o resto do documento é crítica: é a decisão mais importante do projeto e
  ela está certa.
- **A resposta do comparador é conferida contra si mesma antes de virar
  veredito** (`delegacao.go:166-171`): `confere` e `id` precisam contar a mesma
  história, e a divergência vira erro em vez de palpite. É exatamente o cuidado
  que falta em C2 aplicado a outro campo da mesma resposta — o instinto está lá,
  falta estendê-lo ao endereço.
- **`normalizaTemplate` é rigoroso pelo motivo certo** (`sdk.go:396-421`): a DLL
  lê fora da alocação quando os campos de tamanho não batem, e uma violação de
  acesso dentro dela não vira `panic` recuperável. Validar na entrada é a única
  defesa possível, e o comentário explica isso melhor do que a maioria das
  documentações de biblioteca.
- **O `.gitignore` trata template como dado irrevogável, com a justificativa
  escrita ao lado** — *"senha trocada vaza uma vez; digital vazada acompanha a
  pessoa pelo resto da vida"*. É a frase certa no arquivo certo.
- **`build` e `vet` limpos** para `windows/386`, inclusive nos arquivos de teste,
  em 4.965 linhas de código que mexem com memória nativa, `uintptr` e FFI. Não é
  pouco.
- **Os comentários citam a falha concreta que motivou cada decisão**, e é por isso
  que esta revisão conseguiu apontar C1 e C2: os dois foram encontrados
  *comparando o comentário com o que o código faz hoje*. Documentação que envelhece
  o suficiente para denunciar a própria defasagem é mais útil do que documentação
  vaga que nunca erra.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Os dois críticos novos têm a mesma origem, e ela não está em nenhuma linha
específica: **o comparador mudou de escala e os pressupostos não foram
recontados**. Enquanto era um processo por sessão, uma fila de SDK única era
certa e um arquivo de configuração compartilhado era inofensivo. Como serviço do
servidor inteiro, a fila virou um ponto único de parada (**C1**) e o arquivo virou
um ponto único de confiança (**C2**). Cada peça foi revisada com cuidado; o que
faltou foi revisar a composição.

**Ordem sugerida de correção**, atualizada com o que este documento apurou:

1. **C2 (loopback) + C2 #12 (`KnownFolderPath`)** — quatro linhas em
   `delegacao.go` e três em `anuncio.go`. Juntas fecham os dois caminhos de
   envenenamento do anúncio; separadas, cada uma deixa o outro aberto. É a única
   correção que impede template biométrico de sair da máquina.
2. **C1 #12 (`comparador.key`)** — um arquivo novo e três linhas em
   `tokenDoComparador`. Sem ela, todo `net stop` para a biometria do servidor até
   cada usuário fazer logoff.
3. **A1 (reler o anúncio quando `comparadorRemoto == nil`)** — cinco linhas, e é
   o que faz uma instalação por MSI num RDS em produção funcionar sem pedir
   logoff geral.
4. **C1 (fila do SDK)** — o mais trabalhoso e o que mais muda a arquitetura. As
   três medidas paliativas (fila separada para 1:1, teto de candidatos no modo
   comparador, `limiteHTTP` no comparador) cabem num commit e removem o pior
   antes de a mudança grande ficar pronta.
5. **R1 (`bioPort`)** — continua valendo, agora com a urgência certa: cinco linhas
   de JavaScript para uma negação de serviço persistente, não para um vazamento.

Este PR **não altera código**: acrescenta apenas este documento.

---

*Fontes consultadas para as afirmações verificáveis deste documento:*
*[WHATWG Fetch Standard](https://fetch.spec.whatwg.org/) · [MDN — Window: fetch()](https://developer.mozilla.org/en-US/docs/Web/API/Window/fetch) · [Bugzilla 1195820 — fetch() and new Request() should throw TypeError on URL with username/password](https://bugzilla.mozilla.org/show_bug.cgi?id=1195820) · [Microsoft Q&A — Default group permissions for ProgramData folder](https://learn.microsoft.com/en-us/answers/questions/8093156b-62fd-473b-93dd-7b0fad05ceac/default-group-permissions-for-programdata-folder) · [SS64 — icacls](https://ss64.com/nt/icacls.html)*
