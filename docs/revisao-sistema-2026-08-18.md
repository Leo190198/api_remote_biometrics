# 🔍 Revisão técnica do sistema — 2026-08-18

> ⚠️ **`main` continua em `26c9379`, de 2026-08-06.** Os PRs **#10** a **#20**
> seguem abertos e nenhum crítico foi tocado. É o **décimo segundo dia
> consecutivo** sem um commit de correção. O `git diff origin/main...` deste
> branch é vazio: o código revisado hoje é **byte a byte** o mesmo que as
> revisões anteriores leram, então os 22 críticos abertos continuam válidos por
> construção — três deles foram reconferidos linha a linha (ver o inventário).

As revisões anteriores foram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos
caminhos de exceção, pelo resíduo em disco, pela cadeia de entrega, pela
convivência de mais de uma instância e pelos caminhos de recuperação. Esta foi
por um eixo ainda não aberto: **os números do sistema** — os tetos, as quotas e
os privilégios que decidem o que é grande demais, quem é poderoso demais e
quanto tempo é tempo demais.

O sistema tem muitos números, e quase todos vieram com uma justificativa escrita
ao lado: `maxCorpoComparar` caiu de 2 MB para 256 KB por causa do pico do
decodificador num binário de 32 bits (`main.go:37-40`), `maxTemplate` caiu de
1 MB para 64 KB porque o limite antigo "não protegia contra nada"
(`main.go:42-44`), `limiteIdentificar` nasceu com 2 vagas porque 32 corpos de
16 MB estouravam a memória do processo (`main.go:69-72`), e a sondagem do leitor
passou de 5 s para 15 s por causa de um vazamento do `EnumerateDevice`
(`main.go:798-801`). É raro ver um repositório em que cada constante sabe
explicar por que tem o valor que tem.

A pergunta desta revisão foi a seguinte: **esses números foram recalculados
quando o desenho mudou?** A resposta produz **dois críticos novos**: o serviço
comparador roda como `LocalSystem` e a única barreira dele é um segredo que, por
desenho documentado, qualquer usuário logado consegue ler — o que põe um parser
nativo frágil ao alcance de qualquer sessão do servidor; e os tetos de
`/identificar` não fecham nem com o que a API promete por escrito nem com o
caminho de delegação, que dobra o custo do mesmo corpo e foi acrescentado depois
que os tetos já estavam decididos.

Além deles, **três alertas** e **três sugestões**.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, das quais 1.055 em 7
arquivos de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/build-msi.cmd`,
`conferir-biometria.cmd`, `embutir-icone.py`, `go.mod`/`go.sum`, `README.md` e
`docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `gofmt -l .` | **nada a formatar** |
| `go test ./...` (alvo do host) | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo |
| `GOOS=windows GOARCH=386 go test ./...` | **`exec format error`** — compila e não roda. As **48** funções `Test*` continuam sem executar em lugar nenhum. Segue valendo o **A8 de 30/07** |
| `git log -1 origin/main` | `26c9379`, de **2026-08-06** (12 dias) |
| `git diff origin/main...HEAD` | **vazio** — nenhuma mudança de código nesta revisão |
| `grep -n "Account=" instalador/msi/AgenteBiometria.wxs` | base do C1 — `LocalSystem` |
| `grep -n "iniciaLogArquivo\|iniciaLogEm" *.go` | base do A1 e do A2 |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Qualquer usuário logado no servidor entrega bytes arbitrários ao parser da `NBioBSP.dll` **dentro de um processo `LocalSystem`** *(novo)*

**Arquivos:** `instalador/msi/AgenteBiometria.wxs:71-91`,
`instalador/instalar-servidor.ps1:187`, `anuncio.go:18-23`, `anuncio.go:82-96`,
`comparador.go:156-166`, `main.go:405-431`, `sdk.go:396-421`, `sdk.go:448-471`,
`worker.go:18-23`

O MSI instala o comparador com a conta mais poderosa que o Windows tem:

```xml
<!-- instalador/msi/AgenteBiometria.wxs:71-91 -->
<ServiceInstall
    Id="ServicoComparador"
    Name="AgenteBiometriaComparador"
    ...
    Account="LocalSystem"
    Arguments="--comparador"
    Vital="yes">
```

(o `instalar-servidor.ps1:187` faz o mesmo pelo outro caminho:
`New-ScheduledTaskPrincipal -UserId 'SYSTEM' ... -RunLevel Highest`).

A única barreira desse serviço é um *bearer token*:

```go
// comparador.go:156-166
func exigeSegredo(segredo string, proximo http.Handler) http.Handler {
	esperado := []byte("Bearer " + segredo)
	...
	if subtle.ConstantTimeCompare(recebido, esperado) != 1 {
		escreveErro(w, http.StatusUnauthorized, "credencial invalida")
```

E esse segredo é, **por desenho documentado**, legível por todo mundo:

```go
// anuncio.go:18-23
// Sobre o segredo: qualquer usuario logado consegue le-lo. Isso e inerente ao
// desenho, nao um descuido - o agente roda como o usuario e precisa se
// autenticar. O comparador nao guarda digital nenhuma; o que um leitor do
// arquivo ganha e poder confirmar um par de templates que ele ja tenha em
// maos.
```

**Por que é um problema.** A avaliação de risco desse comentário — "o que um
leitor do arquivo ganha é poder confirmar um par de templates que ele já tenha
em mãos" — está correta para o *significado* da resposta e incorreta para o
*alcance* da chamada. Quem tem o token não ganha só um oráculo de comparação:
ganha um caminho direto até um parser nativo, e o processo do outro lado desse
parser é `SYSTEM`.

O caminho é curto e não tem nenhuma outra checagem:

```
POST /comparar  (Authorization: Bearer <token lido do ProgramData>)
  → handleComparar                   main.go:405
  → normalizaTemplate                sdk.go:407  (ASCII 0x21..0x7E, 32..65536)
  → clienteWorker → processo worker  worker.go   (filho do servico ⇒ SYSTEM)
  → novaInputFIRNativa               sdk.go:376  (monta a struct nativa)
  → NBioAPI_VerifyMatch              sdk.go:458
```

A validação que separa o atacante da DLL é esta, inteira:

```go
// sdk.go:407-421
func normalizaTemplate(t string) string {
	limpo := strings.TrimSpace(t)
	if len(limpo) < minTemplate || len(limpo) > maxTemplate {
		return ""
	}
	for i := 0; i < len(limpo); i++ {
		if c := limpo[i]; c < 0x21 || c > 0x7E {
			return ""
		}
	}
	return limpo
}
```

Ou seja: **qualquer** cadeia de 32 a 65.536 caracteres ASCII imprimíveis passa.
Não há conferência de cabeçalho, de tamanho declarado, de versão de FIR nem de
checksum antes da DLL. E o próprio código diz, no comentário logo acima, o que
está do outro lado:

```go
// sdk.go:396-403
// A checagem precisa ser rigorosa: NBioAPI_VerifyMatch confia nos campos de
// tamanho embutidos no template e le fora da alocacao quando eles nao batem
// com o conteudo. Uma violacao de acesso dentro da DLL nao vira panic do Go —
// o Windows derruba o processo e nenhum recover() roda.
```

O repositório inteiro é construído em cima dessa premissa — o processo worker
(`worker.go:18-23`) existe justamente porque se assume que a DLL **pode** ser
derrubada por bytes malformados. A conclusão é inevitável: um usuário sem
privilégio nenhum, numa sessão RDP qualquer, alcança um parser que assumimos ser
frágil dentro de um processo que roda como `SYSTEM`.

O que isso rende hoje, com certeza:

- **Negação de serviço em todo o servidor.** Uma requisição derruba o worker
  do comparador. Como é o comparador que atende **todas** as sessões, cada
  derrubada tira a biometria do prédio inteiro pelo tempo de recriar o SDK — e
  três seguidas ativam o esfriamento de 5 s (`worker.go:198-200`). Repetir a
  chamada em laço mantém o serviço inútil indefinidamente, sem nenhum privilégio
  especial e sem tocar em disco.
- **Uso do serviço como oráculo**, que o comentário já reconhece.

O que isso pode render, e não dá para descartar: corrupção de memória
controlada dentro de um processo `SYSTEM` é o começo clássico de uma elevação
local de privilégio. Não afirmo que exista um exploit — afirmo que a premissa de
fragilidade é do próprio código, e que a distância entre "usuário comum" e
"parser dentro do SYSTEM" deveria ser maior que um arquivo de texto legível por
`Users`.

**Como corrigir.** O conserto que vale mais e custa menos não é validação: é
**tirar o privilégio**. O comparador não precisa de nada que `LocalSystem` dá —
ele carrega uma DLL, escuta em `127.0.0.1` e escreve dois arquivos em
`ProgramData`:

```xml
<!-- instalador/msi/AgenteBiometria.wxs — conta de servico sem privilegio.
     A sessao 0, que e o motivo de tudo, continua sendo sessao 0: ela vem de
     ser servico, e nao de ser SYSTEM. -->
<ServiceInstall
    Id="ServicoComparador"
    Name="AgenteBiometriaComparador"
    ...
    Account="NT AUTHORITY\LocalService"
    Arguments="--comparador"
    Vital="yes">
```

Isso exige duas coisas do pacote, ambas mecânicas: dar a `LOCAL SERVICE`
permissão de escrita em `C:\ProgramData\AgenteBiometria` (um `util:PermissionEx`
no componente `CompDados`, que hoje só cria a pasta) e conferir uma vez que a
`NBioBSP.dll` carrega sob essa conta. O `instalar-servidor.ps1:187` precisa da
mesma troca (`-UserId 'LOCAL SERVICE'`).

Em segundo lugar, e independente disso, a validação deveria olhar a **estrutura**
do FIR texto antes de entregá-lo, e não só a faixa de caracteres:

```go
// sdk.go — o FIR texto do NBioBSP tem cabecalho com tamanhos declarados.
// Conferir que o declarado bate com o recebido e o que transforma "parece
// texto" em "e um FIR", e e a diferenca entre entregar dado e entregar entrada
// arbitraria ao parser.
func firTextoPlausivel(t string) bool {
	// cabecalho fixo + tamanho declarado == len(t); rejeita o resto.
}
```

E, por fim, o segredo pode deixar de ser legível por todos sem quebrar o
desenho: uma ACL explícita no `comparador.json` restringindo a leitura ao grupo
que de fato usa o sistema (em vez do `Users` herdado) reduz a superfície sem
exigir canal privilegiado nenhum. Isso **não** substitui a troca de conta — é
defesa em profundidade sobre ela.

---

### C2. Os tetos de `/identificar` não fecham nem com o que a API promete por escrito, nem com o caminho de delegação que foi acrescentado depois deles *(novo)*

**Arquivos:** `main.go:40-49`, `main.go:69-72`, `main.go:475-510`,
`main.go:389-403`, `delegacao.go:102-113`, `README.md:193`,
`integracao/COMO-USAR.md:44-53`

Os três números que governam a identificação 1:N foram decididos em momentos
diferentes e nunca foram postos lado a lado:

```go
// main.go:40-49
maxCorpoIdentificar = 16 << 20   // 16 MiB de corpo
maxTemplate         = 64 << 10   // 64 KiB por template
maxCandidatos       = 5000       // 5.000 candidatos por chamada
```

A documentação promete o terceiro em dois lugares:

> `README.md:193` — "Os IDs precisam ser strings únicas e não vazias. Cada
> chamada aceita entre 1 e 5.000 candidatos."

**Por que é um problema.** Divida: 16 MiB ÷ 5.000 = **3.355 bytes por
candidato**, dos quais ~27 vão no envelope JSON (`{"id":"…","template":"…"},`)
e mais alguns no `id`. Sobram ~3,3 KB para o template — **um vigésimo** do que
`maxTemplate` autoriza. Um FIR texto de cadastro (`purposeEnroll`, duas
amostras) passa dos 3 KB com facilidade; a partir daí, a chamada de 5.000
candidatos que a documentação promete **não cabe no corpo que o servidor
aceita**.

Pior que falhar é *como* falha. O teto do corpo é aplicado na decodificação:

```go
// main.go:389-403
func decodificaJSON(w http.ResponseWriter, r *http.Request, limite int64, destino any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limite)
	dec := json.NewDecoder(r.Body)
```

e a checagem de quantidade só existe **depois**, no corpo já decodificado
(`main.go:507-510`). Então o operador nunca vê "a lista é grande demais"; ele vê:

```go
// main.go:498-501
escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
// => 400 {"erro":"JSON invalido: http: request body too large"}
```

A mesma mensagem que o sistema usa para dado corrompido. Num atendimento, "JSON
inválido" manda o suporte investigar o cadastro do beneficiário — que está
perfeito. A causa é o tamanho da busca, e nada na resposta diz isso.

**A segunda metade do problema é a memória, e é o que torna isto crítico.** O
teto de 2 identificações simultâneas foi calculado explicitamente para o caminho
**local**:

```go
// main.go:69-72
// /identificar decodifica corpos de ate 16 MB e o pico do encoding/json
// chega a varias vezes isso. Com os 32 slots gerais de limiteHTTP, um
// punhado de requisicoes simultaneas estourava a memoria do processo.
limiteIdentificar = make(chan struct{}, maxIdentificacoes)
```

Depois disso veio a delegação, e ela **remonta o corpo inteiro em memória** antes
de enviar:

```go
// delegacao.go:102-113
func (c *clienteComparador) chama(ctx context.Context, rota string, corpo, destino any) error {
	dados, err := json.Marshal(corpo)          // <- outra copia integral de ate 16 MB
	...
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+rota, bytes.NewReader(dados))
```

No caminho delegado, uma única identificação de 16 MB custa, no agente: o corpo
lido, as *strings* decodificadas, **mais** o slice remontado pelo `Marshal`,
**mais** o buffer do `http.Transport` — e o mesmo corpo é decodificado outra vez
do lado do comparador. Com 2 vagas, o pico previsto para "corpo + pico do
decodificador" vira algo perto do dobro disso, num processo `windows/386` que
divide um espaço de endereçamento de ~2 GB (o comentário de `main.go:37-40`
mostra que essa restrição é conhecida e levada a sério em outro ponto do
arquivo). Uma falha de alocação aqui não devolve 500: derruba o processo — e
quando o processo é o comparador, derruba a biometria do servidor inteiro.

**Como corrigir.** Três mudanças pequenas, nenhuma delas arquitetural.

Primeiro, dizer a verdade sobre o limite, com o código errado certo:

```go
// main.go, em handleIdentificar
if err := decodificaJSON(w, r, maxCorpoIdentificar, &body); err != nil {
	var excedeu *http.MaxBytesError
	if errors.As(err, &excedeu) {
		escreveErro(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"a lista de candidatos passou de %d MB; divida a busca em lotes menores",
			maxCorpoIdentificar>>20))
		return
	}
	escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
	return
}
```

Segundo, não remontar o corpo no caminho delegado — o `Encoder` escreve direto
no cano da requisição:

```go
// delegacao.go — sem a copia integral: o corpo atravessa em fluxo.
leitura, escrita := io.Pipe()
go func() { escrita.CloseWithError(json.NewEncoder(escrita).Encode(corpo)) }()
req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+rota, leitura)
```

Terceiro, alinhar o número prometido ao número que cabe. Ou o teto do corpo passa
a ser derivado do contrato…

```go
// main.go — um teto que nao pode contradizer a documentacao porque nasce dela
maxCorpoIdentificar = maxCandidatos * (maxTemplateCandidato + 128)
```

…ou a documentação passa a anunciar o lote que de fato funciona (1.000 candidatos
por chamada é um número seguro para templates de até 12 KB), com `README.md:193`
e `integracao/COMO-USAR.md:44-53` atualizados juntos. O que não pode continuar é
o sistema prometer 5.000 num lugar e recusar em outro, com a mensagem de erro
apontando para o lado errado.

---

## 🟡 Alertas (recomenda correção)

### A1. O `worker.log` do comparador vai parar no perfil do `SYSTEM` — exatamente o lugar que o `iniciaLogEm` foi criado para evitar *(novo)*

**Arquivos:** `worker.go:68`, `log.go:14-44`, `comparador.go:40`

O comentário do `log.go` é explícito sobre o problema e sobre a solução:

```go
// log.go:24-31
// iniciaLogEm grava o log num diretorio escolhido.
//
// Existe para o comparador: rodando como servico, o diretorio de dados do
// usuario e o perfil do SYSTEM, e o log ficaria enterrado em
// C:\Windows\SysWOW64\config\systemprofile - um lugar que ninguem procura e que
// o instalador nao consegue limpar.
```

O comparador foi corrigido (`comparador.go:40` usa
`iniciaLogEm(diretorioCompartilhado(), "comparador.log")`). O **worker dele**,
não:

```go
// worker.go:68 — cai em diretorioDados(), isto e, %LOCALAPPDATA%
iniciaLogArquivo("worker.log")
```

**Por que é um problema.** O worker do comparador é filho do serviço, logo roda
como `SYSTEM`, logo `%LOCALAPPDATA%` é o perfil do sistema (e, se a variável não
estiver definida no ambiente do serviço, `diretorioDados()` cai para
`os.TempDir()`, ou seja, `C:\Windows\Temp` — `storage.go:13-19`). O worker é,
por definição, o processo que se espera que morra: ele existe porque a DLL pode
derrubá-lo. Quando isso acontece, o diagnóstico — inclusive a impressão dos
templates envolvidos, `worker.go:151-152` — fica exatamente no lugar que o
comentário acima classifica como "ninguém procura e o instalador não consegue
limpar". O MSI remove só `comparador.json` e `comparador.log`
(`AgenteBiometria.wxs:123-135`), então esse arquivo também sobrevive à
desinstalação.

**Como corrigir.** O pai já sabe onde o log deve ficar; basta contar ao filho:

```go
// worker.go, em sobe() — o pai escolhe o diretorio de log do filho
cmd.Env = append(os.Environ(),
	"BIO_WORKER=1", "BIO_WORKER_DLL="+c.dll, "BIO_WORKER_LOG="+dirLogAtual())
```

```go
// worker.go, em workerMain()
if dir := os.Getenv("BIO_WORKER_LOG"); dir != "" {
	iniciaLogEm(dir, "worker.log")
} else {
	iniciaLogArquivo("worker.log")
}
```

E o MSI passa a remover `worker.log` junto com os outros dois.

### A2. Nenhum log rotaciona depois do arranque — e o comparador é um serviço que roda por meses *(novo)*

**Arquivos:** `log.go:31-44`, `comparador.go:40`, `main.go:430-459`,
`instalador/msi/AgenteBiometria.wxs:71-91`

A rotação existe e está correta, mas acontece **uma vez por processo**, no
momento de abrir o arquivo:

```go
// log.go:31-44
func iniciaLogEm(dir, nome string) {
	...
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
```

**Por que é um problema.** O agente de bandeja reinicia a cada logon, então para
ele o teto de 5 MB é real. O comparador não: é um serviço `Start="auto"` com
política de reinício (`AgenteBiometria.wxs:71-91`), feito para subir com a
máquina e não parar mais. Ele grava duas a três linhas por comparação
(`main.go:430-431` e `main.go:458-459`) e uma por identificação
(`main.go:565-566`) — para o servidor inteiro. Num RDS com algumas dezenas de
sessões, o `comparador.log` cresce sem teto nenhum em
`C:\ProgramData\AgenteBiometria`, que é a **mesma pasta** onde o anúncio é
regravado atomicamente (`anuncio.go:82-96`): quando o disco encher, o serviço não
só perde o log, perde a capacidade de republicar o próprio endereço. E é o mesmo
arquivo do **C14 do #17** (id do beneficiário e veredito legíveis por qualquer
usuário), agora sem limite de quantos meses de atendimento ele acumula.

**Como corrigir.** Conferir o tamanho na escrita, e não só na abertura — um
`io.Writer` de dez linhas resolve, sem dependência nova:

```go
// log.go — o logger passa a saber rotacionar sozinho
type arquivoRotativo struct {
	mu   sync.Mutex
	f    *os.File
	path string
	max  int64
	n    int64
}

func (a *arquivoRotativo) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.n+int64(len(p)) > a.max {
		a.rotaciona() // fecha, renomeia para .1, reabre e zera n
	}
	escritos, err := a.f.Write(p)
	a.n += int64(escritos)
	return escritos, err
}
```

Vale notar que `iniciaLogEm` também deixa o arquivo anterior aberto quando é
chamado duas vezes no mesmo processo (`logger.SetOutput(f)` sem fechar o
antigo) — a mesma correção elimina isso de passagem.

### A3. O recuo do supervisor mede o tempo errado e nunca estabiliza: um agente em laço de falha reinicia muito mais do que o desenho previa *(novo)*

**Arquivo:** `supervisor.go:29-55`

```go
// supervisor.go:34-54
espera := 2 * time.Second
for {
	cmd := exec.Command(exe)
	...
	inicio := time.Now()
	if err := cmd.Start(); err != nil { os.Exit(1) }
	if cmd.Wait() == nil { os.Exit(0) }
	time.Sleep(espera)                       // <- o sono entra na conta
	if time.Since(inicio) > time.Minute {    // <- medido DEPOIS de dormir
		espera = 2 * time.Second
	} else if espera < time.Minute {
		espera *= 2
		...
	}
}
```

**Por que é um problema.** A regra pretendida é clara: "se o filho viveu mais de
um minuto, o problema passou; volte ao recuo mínimo". Só que `time.Since(inicio)`
é avaliado **depois** do `time.Sleep(espera)`, então mede *vida do filho + tempo
de espera*. Siga um agente que morre instantaneamente (bandeja que não sobe, DLL
que derruba o processo no arranque):

| ciclo | espera antes | `time.Since(inicio)` | espera depois |
|---|---|---|---|
| 1 | 2 s | ~2 s | 4 s |
| 2 | 4 s | ~4 s | 8 s |
| 3 | 8 s | ~8 s | 16 s |
| 4 | 16 s | ~16 s | 32 s |
| 5 | 32 s | ~32 s | 60 s |
| 6 | 60 s | **~60 s → passou de 1 min** | **2 s (reinicia o ciclo)** |

O recuo nunca chega a segurar em 1/min: ele oscila entre 2 s e 60 s para sempre,
com média muito abaixo do teto. Num RDS, cada ciclo desses é um processo criado
por sessão, e o efeito é justamente a tempestade de reinícios que o recuo
exponencial existe para evitar — sem nenhuma linha de log dizendo que está
acontecendo (`supervisor.go` não registra nada).

**Como corrigir.** Uma linha: medir a vida do filho antes de dormir.

```go
// supervisor.go — o que interessa e quanto o filho viveu, nao quanto esperamos
duracao := time.Since(inicio)
time.Sleep(espera)
if duracao > time.Minute {
	espera = 2 * time.Second
} else if espera < time.Minute {
	...
}
```

E, já que o supervisor é a única peça que enxerga o padrão das quedas, vale ele
registrar o que vê — hoje o `agente.log` não distingue "caiu e voltou seis vezes"
de "está de pé desde o logon":

```go
registraErro("o agente caiu depois de %s; proxima tentativa em %s", duracao, espera)
```

---

## 🟢 Sugestões (opcional)

### S1. O `agente-<sessão>.json` fica para trás com porta e token mortos

`escreveConfig()` (`main.go:615-641`) grava o arquivo no arranque e **nada** o
remove na saída. O `COMO-USAR.md:81-84` documenta esse arquivo como um dos três
caminhos oficiais para o backend descobrir porta e token, então um leitor que
chegue depois do agente encerrar recebe credencial morta e uma porta que outro
programa pode ter ocupado. O arquivo já carrega o `pid`; um `defer` na saída
limpa o caso normal e o leitor confere o `pid` para o caso da queda:

```go
// main.go, junto com o Shutdown
defer func() { _ = os.Remove(caminhoConfigDaSessao()) }()
```

### S2. O mesmo template é validado três vezes no caminho delegado

`handleIdentificar` normaliza cada candidato (`main.go:531`), o comparador
normaliza tudo de novo ao receber, e `nbio.identifica` normaliza uma terceira vez
dentro do worker (`sdk.go:494`). Para 5.000 candidatos são 15.000 varreduras de
uma cadeia de alguns KB, mais dois `sha256` por comparação registrada. A
validação na borda é a que importa; as internas podem virar `templateValido` em
modo `assert` — ou simplesmente sumir, já que o dado só atravessa fronteiras que
nós mesmos controlamos.

### S3. O `default` no seletor de vagas torna o ramo do contexto uma loteria

```go
// main.go:252-260
select {
case limiteHTTP <- struct{}{}:
	defer func() { <-limiteHTTP }()
case <-r.Context().Done():
	return
default:
	escreveErro(w, http.StatusServiceUnavailable, "agente ocupado")
	return
}
```

Com `default` presente, o `select` nunca bloqueia: quando o contexto já morreu e
há vaga livre, o Go escolhe **aleatoriamente** entre o primeiro e o segundo ramo.
O resultado é o mesmo em qualquer caso (a requisição some), mas o código sugere
uma ordem de prioridade que não existe. Remover o ramo do contexto deixa a
intenção — "tem vaga? entra; não tem? 503" — explícita.

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 33 (22 `.go`, 1 `.js`, 1 `.ps1`, 3 do MSI, 2 `.cmd`, 1 `.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e 2 docs)
- **Segurança**: 🚨 **Risco** — o **C1** de hoje é o primeiro crítico de segurança que chega ao nível do sistema operacional: um usuário sem privilégio alcança um parser nativo dentro de um processo `LocalSystem`. Os quatro anteriores continuam abertos: dado pessoal em log legível (**C14 do #17**), `ProgramData` gravável (**C10 do #13**), agente impostor na 5000 (**C13 do #15**) e revogação de origens que se desfaz sozinha (**C19 do #19**)
- **Qualidade**: ⚠️ Atenção — `build`, `vet` e `gofmt` limpos; o ponto fraco desta revisão é que **as constantes não foram recalculadas quando o desenho mudou** (delegação, serviço, worker), e nenhuma delas tem teste que amarre o valor ao contrato
- **Risco de produção**: 🚨 **Alto** — o C1 permite parar a biometria de todo o servidor com uma requisição repetida, de qualquer sessão; o C2 quebra a busca 1:N no tamanho que a documentação promete, com a mensagem de erro apontando para o lado errado
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que não rodam em alvo nenhum (`matched no packages` no host, `exec format error` em `windows/386`) e nenhum CI. **A8 de 30/07, aberto há 19 dias**. Nenhum dos dois críticos de hoje seria pego por teste, porque não existe teste que rode

### Inventário dos críticos abertos contra `26c9379`

| # | Origem | Crítico | Estado |
|---|---|---|---|
| 1 | main (07-30) | `bioPort` do fragmento não validado (`integra-biometria.js:26-34`, **reconferido hoje**) | **ABERTO** |
| 2 | main (07-30) | Cliente JS descarta `ignorados` (`integra-biometria.js:222-230`, **reconferido hoje**) | **ABERTO** |
| 3 | main (07-30) | 1:N é laço de 1:1 — falsa aceitação acumula | **ABERTO** |
| 4 | main (07-30) | 1:N congela as capturas da sessão por até 3 min | **ABERTO** |
| 5 | PR #10 | Parar o serviço apaga o anúncio; token novo derruba os agentes | **ABERTO** |
| 6 | PR #10 | Endereço do comparador não validado como loopback (`delegacao.go:74-85`, **reconferido hoje**) | **ABERTO** |
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
| 23 | **hoje** | **Usuário comum alcança o parser nativo dentro de um processo `LocalSystem`** | **NOVO** |
| 24 | **hoje** | **Tetos de `/identificar` contradizem a API e dobram no caminho delegado** | **NOVO** |

---

## ✅ Pontos positivos

**As constantes deste repositório sabem se explicar, e isso é raro.** `main.go:36-49`
não tem um número solto: cada teto vem com o motivo pelo qual foi escolhido, e
dois deles registram o valor **anterior** e por que ele estava errado ("manter os
2 MB antigos só aumentava o pico de memória do decodificador JSON"; "o limite
antigo de 1 MB era ~500x maior que o real e não protegia contra nada"). O C2 de
hoje não é uma crítica a esse cuidado — é a observação de que ele foi aplicado a
cada número isoladamente, e nunca à aritmética entre eles.

**A separação entre "erro daquele registro" e "erro da operação" está certa nos
dois lados da fronteira.** `sdk.go:513-522` pula o candidato com checksum
inválido e aborta em qualquer outro código, `worker.go:130-134` preserva o
diagnóstico parcial quando a operação falha, e `main.go:567-572` registra em
separado o caso em que nada conferiu **e** houve cadastro ignorado — que é
exatamente o caso em que "não confere" mente. É a distinção mais difícil de
acertar num sistema biométrico, e ela foi acertada três vezes seguidas.

**O `exigeSegredo` compara em tempo constante e explica por quê**
(`comparador.go:151-166`), com uma justificativa que leva a sério o atacante
local — o mesmo atacante do C1 de hoje. A defesa está correta; o que falta é a
conclusão seguinte, que é limitar o que ele alcança quando a credencial não é
segredo nenhum.

**A `impressaoTemplate` resolve a tensão entre diagnóstico e LGPD sem escolher um
dos dois** (`sdk.go:427-434`): tamanho e `sha256` curto bastam para seguir um
template entre processos e para flagrar truncamento por coluna curta no banco, e
não carregam dado biométrico. É o tipo de decisão que costuma ser tomada tarde,
depois do primeiro vazamento, e aqui foi tomada antes.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O sistema chega hoje a **24 críticos abertos** e **doze dias** sem um commit de
correção. Os dois de hoje têm em comum a origem: **números que estavam certos
quando foram escritos e não foram recalculados quando o desenho mudou**. A conta
de memória de `/identificar` foi feita para o caminho local e a delegação chegou
depois, dobrando o custo do mesmo corpo; a avaliação de risco do segredo do
comparador foi feita quando ele era um oráculo de comparação, e o serviço passou
a rodar como `LocalSystem` com um parser nativo atrás dele.

A sequência recomendada muda pouco em relação à do #20, e ganha um item na frente
porque é o único que reduz risco sem depender de nenhuma outra correção chegar:

1. **C1 de hoje (a troca de conta)** — `Account="NT AUTHORITY\LocalService"` no
   WiX e `-UserId 'LOCAL SERVICE'` no PS1. É uma linha em cada instalador, não
   muda uma linha de Go, e transforma "usuário comum alcança parser dentro do
   `SYSTEM`" em "usuário comum alcança parser dentro de uma conta sem
   privilégio". A validação estrutural do FIR pode vir depois, com calma.
2. **C2 (parte do log) do #19** — as três linhas de `delegacao.go:58-65` que
   fazem o agente dizer "não há comparador anunciado, vou comparar localmente".
   Continua sendo o menor diff do repositório e o que ilumina mais coisas ao
   mesmo tempo.
3. **C1 do #20** — releitura do anúncio com cache curto. Sem ela, nenhuma
   correção do lado do comparador chega a um agente já de pé.
4. **C2 do #20** — o Job Object no worker. É a correção que não depende de
   nenhum prazo estar certo, e sem ela a atualização — o canal por onde tudo isto
   seria entregue — não é confiável.
5. **C2 de hoje (a mensagem e o fluxo)** — o `413` com texto próprio e o
   `io.Pipe` na delegação. São dois diffs pequenos que tiram do caminho a única
   falha de produção que hoje aparece disfarçada de "cadastro corrompido".
