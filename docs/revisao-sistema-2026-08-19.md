# 🔍 Revisão técnica do sistema — 2026-08-19

> ⚠️ **`main` continua em `26c9379`, de 2026-08-06.** Os PRs **#10** a **#21**
> seguem abertos e nenhum crítico foi tocado. É o **décimo terceiro dia
> consecutivo** sem um commit de correção. O `git diff origin/main...` deste
> branch é vazio: o código revisado hoje é **byte a byte** o mesmo que as
> revisões anteriores leram, então os 24 críticos abertos continuam válidos por
> construção.

As revisões anteriores foram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos
caminhos de exceção, pelo resíduo em disco, pela cadeia de entrega, pela
convivência de mais de uma instância, pelos caminhos de recuperação e pelos
números do sistema. Esta foi por um eixo ainda não aberto: **a evidência** — o
que sobra escrito depois que o atendimento acabou, quem consegue ler, e o que
essa leitura permite reconstruir.

O eixo se escolheu sozinho. Este repositório é, entre outras coisas, um estudo
de diagnóstico: existe um `--autoteste` que grava linha a linha porque o passo
seguinte pode matar o processo (`autoteste.go:75-106`), um `modulosBiometricos`
que lista endereço base de cada DLL para casar com o PC de uma violação de
acesso (`versaodll.go:51-70`), um `descreveDLL` que junta caminho, versão,
tamanho e data porque duas máquinas "iguais" carregavam SDKs diferentes, e uma
`impressaoTemplate` inventada para seguir o mesmo template entre processos sem
gravar dado biométrico. Poucos sistemas deste tamanho pensaram tanto em quem vai
ler o log depois.

A pergunta desta revisão foi outra: **e quem mais vai ler?** A resposta produz
**dois críticos novos**: a impressão que substitui o template é um identificador
estável e não-salgado, que transforma qualquer log em um oráculo de presença de
uma pessoa nomeada; e o comparador — o único processo que atende o prédio
inteiro — serve os handlers do agente **sem o `middleware`**, e com isso sem
teto de concorrência e sem o `recover` que existe justamente porque neste
sistema processos morrem.

Além deles, **três alertas** e **três sugestões**.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, das quais 1.055 em 7
arquivos de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `conferir-biometria.cmd`,
`embutir-icone.py`, `go.mod`/`go.sum`, `README.md` e `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `gofmt -l .` | **nada a formatar** |
| `go test ./...` (alvo do host) | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo |
| `GOOS=windows GOARCH=386 go test ./...` | **`exec format error`** — compila e não roda. As **48** funções `Test*` continuam sem executar em lugar nenhum. Segue valendo o **A8 de 30/07** |
| `git log -1 origin/main` | `26c9379`, de **2026-08-06** (13 dias) |
| `git diff origin/main...HEAD` | **vazio** — nenhuma mudança de código nesta revisão |
| `grep -rn "impressaoTemplate" *.go` | 8 pontos de gravação — base do C1 |
| `grep -rn "Stderr" *.go` | nenhum `cmd.Stderr` — base do A1 |
| `grep -n "sharemode" $(go env GOROOT)/src/syscall/syscall_windows.go` | `FILE_SHARE_READ \| FILE_SHARE_WRITE` — base do A3 |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. A `impressaoTemplate` é um identificador estável da digital de uma pessoa, e está gravada em texto puro em quatro arquivos de log *(novo)*

**Arquivos:** `sdk.go:427-434`, `sdk.go:460-463`, `sdk.go:516-519`,
`main.go:380`, `main.go:426-431`, `main.go:458-459`, `worker.go:150-152`,
`autoteste.go:125-139`

A função e a intenção declarada:

```go
// sdk.go:427-434
// impressaoTemplate descreve um template no log sem expor o dado biometrico:
// so o tamanho e um resumo curto. E o bastante para reconhecer o mesmo
// registro entre execucoes e para flagrar truncamento — varios templates
// parando exatamente no mesmo tamanho denunciam coluna curta no banco.
func impressaoTemplate(t string) string {
	soma := sha256.Sum256([]byte(t))
	return fmt.Sprintf("%d bytes sha256:%x", len(t), soma[:6])
}
```

**Por que é um problema.** A afirmação "não expõe o dado biométrico" está
correta e é insuficiente. O `sha256` aqui é **simples, sem chave e sem sal**:
o mesmo template produz sempre os mesmos 48 bits, em qualquer máquina, em
qualquer instalação, hoje e daqui a cinco anos. Isso não é um resumo opaco — é
um **identificador persistente derivado de dado biométrico**, e na LGPD um
identificador persistente derivado de dado sensível continua sendo dado pessoal
(art. 5º, I e II).

O que ele permite, concretamente, para quem tem uma cópia de um log:

1. **Confirmar presença.** Quem tiver um template em mãos — e o sistema web tem
   todos, o navegador recebe até 5.000 por chamada de 1:N (**C15 do #17**), e um
   chamado de suporte com um cadastro exportado tem um — calcula
   `sha256(template)[:6]` e procura no arquivo. Achou, sabe que **aquela pessoa**
   foi atendida naquela máquina, naquele minuto, e qual foi o veredito:

   ```go
   // main.go:458-459 — a linha que vira o registro de presença
   registraInfo("comparacao: benef=[%s] confere=%v",
       impressaoTemplate(body.BiometriaBenef), ok)
   ```

2. **Cruzar arquivos e instalações.** A mesma impressão aparece em `agente.log`
   (`main.go:380`, `430-431`, `458-459`), em `worker.log` (`worker.go:151-152`),
   em `comparador.log` (os mesmos handlers rodando sob o serviço), em
   `autoteste.log` (`autoteste.go:138`) e na tela do `--conferir-contra`
   (`autoteste.go:445`). Por ser determinística, ela liga logs de máquinas
   diferentes, de unidades diferentes e de épocas diferentes — o oposto do que
   pseudonimização deveria fazer.

3. **Montar a tabela `id ↔ digital` sem sair do próprio sistema.** Há um ponto em
   que os dois aparecem na mesma linha:

   ```go
   // sdk.go:516-519
   if uint32(r) == erroChecksum {
       registraErro("identificacao: candidato %q com template adulterado (%s), ignorado",
           candidato.ID, impressaoTemplate(limpo))
   ```

   É o caminho do cadastro truncado por coluna curta — que a própria
   documentação do repositório trata como o defeito recorrente, ou seja, um
   caminho que acontece. A partir de um punhado dessas linhas, toda impressão
   solta nos outros logs passa a ter nome.

O agravante é onde esses arquivos moram. O `comparador.log` fica em
`C:\ProgramData\AgenteBiometria` e é legível por qualquer usuário logado
(**C14 do #17**) — mas o C1 de hoje **não depende** disso: log é a primeira coisa
que se anexa num chamado, e a impressão continua sendo um identificador de
pessoa depois de o arquivo sair da máquina, atravessar um e-mail e parar num
sistema de tickets. Corrigir a ACL do C14 fecha o acesso local e não fecha este.

**Como corrigir.** A impressão precisa continuar servindo para o que foi criada
— seguir o mesmo template entre processos e flagrar truncamento — e deixar de
servir para reconhecer uma pessoa a partir de um template que alguém já tenha.
Isso é exatamente a diferença entre `sha256` e um **HMAC com chave por
instalação**:

```go
// sdk.go — a impressao passa a valer so dentro desta instalacao.
//
// A chave nasce uma vez, fica ao lado dos logs e nunca sai da maquina. Quem le
// o log continua conseguindo casar a mesma digital entre agente, worker e
// comparador; quem tem um template em maos deixa de conseguir dizer se ele
// aparece ali, porque a conta nao fecha sem a chave.
var chaveImpressao = carregaOuCriaChaveImpressao() // 32 bytes de crypto/rand

func impressaoTemplate(t string) string {
	mac := hmac.New(sha256.New, chaveImpressao)
	mac.Write([]byte(t))
	return fmt.Sprintf("%d bytes id:%x", len(t), mac.Sum(nil)[:6])
}
```

Três observações sobre a correção:

- A chave precisa ser **compartilhada entre o agente e o comparador** para o
  cruzamento agente↔comparador continuar funcionando: `diretorioCompartilhado()`
  já é o lugar, e o `comparador.json` já é o precedente de um segredo publicado
  ali pelo serviço e lido pelos agentes (`anuncio.go:82-96`).
- O tamanho (`%d bytes`) pode e deve ficar: é ele que denuncia truncamento, e
  sozinho não identifica ninguém.
- A linha do `sdk.go:516-519` deveria parar de imprimir `id` e impressão juntos
  mesmo com HMAC. Para o diagnóstico daquele caso ("qual cadastro está podre"),
  o `id` basta — a impressão ali não acrescenta nada que o `id` já não diga.

---

### C2. O comparador serve os handlers do agente **sem o `middleware`** — o único processo que atende o prédio inteiro é o único sem teto de concorrência e sem `recover` *(novo)*

**Arquivos:** `comparador.go:75-111`, `main.go:205-263`, `main.go:918-937`,
`main.go:68-72`, `delegacao.go:114-133`, `main.go:449-452`

Os dois servidores montam o mesmo `mux` com os **mesmos dois handlers** e o
embrulham de formas diferentes:

```go
// main.go:927-937 — o agente
servidor := &http.Server{
	Handler: middleware(mux, origens),
	...
}
```

```go
// comparador.go:101-111 — o comparador
servidor := &http.Server{
	Handler: exigeSegredo(segredo, mux),
	...
}
```

O `exigeSegredo` (`comparador.go:156-166`) faz uma coisa só: confere o
`Authorization`. Tudo o mais que o `middleware` faz fica de fora. O arquivo
explica, e com razão, por que o **modelo de acesso** não é reaproveitado
(`comparador.go:13-16`: lá o chamador é um navegador na mesma sessão, aqui é o
backend com segredo compartilhado). O problema é que junto com o modelo de
acesso foram embora duas coisas que **não** têm nada a ver com quem chama.

**Primeiro: o teto de requisições simultâneas.**

```go
// main.go:252-260 — no middleware, e so nele
select {
case limiteHTTP <- struct{}{}:
	defer func() { <-limiteHTTP }()
...
default:
	escreveErro(w, http.StatusServiceUnavailable, "agente ocupado")
	return
}
```

`limiteHTTP` tem 32 vagas (`main.go:47, 68`) e existe no binário do comparador —
declarado, alocado e **nunca usado por ele**. Ou seja: o agente de uma estação
de trabalho, que atende **um** navegador de **uma** sessão, tem controle de
admissão; o comparador, que é o funil por onde passa a comparação de **todas** as
sessões do servidor RDS, não tem nenhum.

O que acontece sem ele, no pico de logon da manhã: as requisições entram todas,
e todas vão parar na mesma fila de uma via. `naThreadSDK` bloqueia enfileirando
em `sdkTasks`, que tem 16 posições (`main.go:63`), e as demais ficam paradas ali
até o contexto de 45 s de `/comparar` estourar (`main.go:432`). Ninguém recebe
"ocupado": todo mundo espera o prazo inteiro e falha junto, e o agente traduz
isso para o navegador como `502` (`main.go:449-452`), que é o código de "o outro
lado quebrou". O operador encosta o dedo de novo, e a segunda leva entra na
mesma fila. Isto **agrava** o C9 do #13 (fila de SDK de uma via para o servidor
inteiro) em vez de repeti-lo: o C9 é o teto de vazão; este é a ausência de
qualquer freio na frente dele — e é o que transforma lentidão em avalanche.

**Segundo, e pior de diagnosticar: o `recover`.**

```go
// main.go:206-212 — no middleware, e so nele
defer func() {
	if v := recover(); v != nil {
		registraErro("panic HTTP em %s: %v\n%s", r.URL.Path, v, debug.Stack())
		escreveErro(w, http.StatusInternalServerError, "erro interno")
	}
}()
```

Num agente, um panic dentro de `handleComparar` vira uma linha com pilha
completa no `agente.log` e um `500 {"erro":"erro interno"}` para o chamador. No
comparador, o mesmo panic cai no `recover` interno do `net/http`, que **fecha a
conexão sem escrever resposta** e registra a pilha no `Server.ErrorLog` — que
aqui é `nil`, então o `net/http` usa o logger padrão, que escreve em
`os.Stderr`, que num processo de serviço não vai a lugar nenhum. O `logger`
deste repositório é outro objeto (`log.go:12`) e não recebe nada.

O resultado prático, do lado de quem investiga:

```go
// delegacao.go:114-117 — o que o agente registra
resp, err := c.http.Do(req)
if err != nil {
	return fmt.Errorf("comparador inacessivel: %w", err)
}
// => agente.log: ERRO: comparacao: comparador inacessivel: Post
//    "http://127.0.0.1:5150/comparar": EOF
```

"Comparador inacessível" manda o suporte conferir se o serviço está de pé,
se a porta 5150 está ocupada, se o firewall mudou. O serviço está de pé, a porta
está certa, o firewall não mudou: houve um panic e **a única cópia da pilha foi
para o `NUL`**. Num sistema cuja premissa declarada é que este código derruba
processos (`worker.go:18-23`), perder a pilha justamente no processo sem
interface é caro.

**Como corrigir.** Separar o que é modelo de acesso do que é higiene de
servidor. O `middleware` de hoje faz as duas coisas juntas; basta extrair a
segunda:

```go
// main.go — o que vale para qualquer servidor deste binario
func protege(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				registraErro("panic HTTP em %s: %v\n%s", r.URL.Path, v, debug.Stack())
				escreveErro(w, http.StatusInternalServerError, "erro interno")
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		select {
		case limiteHTTP <- struct{}{}:
			defer func() { <-limiteHTTP }()
		default:
			escreveErro(w, http.StatusServiceUnavailable, "ocupado")
			return
		}
		proximo.ServeHTTP(w, r)
	})
}
```

```go
// comparador.go — o segredo continua sendo a porta de entrada; a higiene passa a existir
Handler: exigeSegredo(segredo, protege(mux)),
```

```go
// main.go — o middleware do agente passa a delegar a parte comum
// (origem, token e sessao continuam so aqui)
mux.ServeHTTP(w, r)  ->  protege(mux).ServeHTTP(w, r)
```

Duas notas sobre o valor do teto no comparador: 32 vagas é um número do agente e
provavelmente não é o número do comparador — ele merece o seu, derivado de
quantas sessões o servidor hospeda. E o `503` precisa chegar ao navegador como
`503`, e não como `502`, que é o assunto do **A2** logo abaixo.

Vale registrar também, sem inflar: o `nosniff` some no comparador, mas
`escreveJSON` sempre define `Content-Type: application/json; charset=utf-8`
(`main.go:162-168`), então esse pedaço é higiene, não risco.

---

## 🟡 Alertas (recomenda correção)

### A1. O `stderr` do worker vai para o `NUL`, e o worker é o único processo do sistema sem `recover` *(novo)*

**Arquivos:** `worker.go:215-222`, `worker.go:64-107`, `worker.go:18-23`,
`main.go:112-118`

O worker é o processo que existe para morrer:

```go
// worker.go:18-23
// A NBioBSP.dll e carregada exclusivamente pelo processo worker. Uma violacao
// de acesso dentro dela nao pode ser recuperada com recover() ... Isolando o
// SDK, essa falha custa uma requisicao em vez do agente inteiro
```

E é o único cuja saída de erro não é lida por ninguém:

```go
// worker.go:215-222
cmd := exec.Command(exe)
cmd.Env = append(os.Environ(), "BIO_WORKER=1", "BIO_WORKER_DLL="+c.dll)
cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
cmd.Stdin = leituraFilho
cmd.Stdout = escritaFilho
// cmd.Stderr nao e definido em lugar nenhum do repositorio
```

**Por que é um problema.** No `os/exec`, `Stderr` nulo significa `os.DevNull`.
Como o worker também não tem `recover` — `atendePedido` e `executaOperacao`
(`worker.go:109-160`) não têm nenhum, ao contrário de `naThreadSDK`
(`main.go:112-118`) e do `middleware` (`main.go:206-212`) — um **panic do Go**
dentro dele imprime a pilha no `stderr`, o `stderr` é o `NUL`, e o processo
morre com código 2. Do lado do pai sobra exatamente uma linha:

```
ERRO: worker do SDK morreu durante comparar: EOF (exit status 2)
```

Ou seja: dá para saber a **classe** da morte (`exit status 2` = panic do Go;
`3221225477` = `0xC0000005`, violação de acesso — distinção que o comentário do
`derruba()` em `worker.go:241-242` já sabe fazer) e nunca o **lugar** dela.
Arquivo, linha e stack ficam de fora. O `worker.log` também não recebe nada,
porque um panic não passa pelo `logger`.

O contraste com o resto do repositório é o que torna isto um alerta e não um
detalhe: o `--autoteste` grava e dá `Sync()` linha a linha só para que "a última
linha do arquivo seja o passo que matou o processo" (`autoteste.go:75-106`). O
caminho de produção, que roda todo dia, não tem esse cuidado.

**Como corrigir.** Duas mudanças pequenas e independentes. O pai passa a guardar
o `stderr` do filho:

```go
// worker.go, em sobe() — o que o filho gritar fica gravado
if dir, err := garanteDiretorioDados(); err == nil {
	if f, err := os.OpenFile(filepath.Join(dir, "worker-stderr.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stderr = f
		c.stderr = f // fechado em derruba()
	}
}
```

E o filho passa a registrar o próprio panic antes de cair, sem fingir que
sobreviveu:

```go
// worker.go, em workerMain() — recupera para registrar, nao para continuar
defer func() {
	if v := recover(); v != nil {
		registraErro("worker: panic %v\n%s", v, debug.Stack())
		os.Exit(2)
	}
}()
```

Isso não muda nada para a violação de acesso dentro da DLL, que continua
matando o processo sem passar por Go nenhum — e é justamente por isso que vale
separar os dois casos no log em vez de deixá-los indistinguíveis.

### A2. Todo erro do comparador vira `502` no agente: `400`, `401` e `503` chegam ao sistema web como "o outro lado quebrou" *(novo)*

**Arquivos:** `delegacao.go:123-133`, `main.go:449-452`, `main.go:559-562`,
`integracao/integra-biometria.js:66-101`

O cliente do comparador transforma qualquer status diferente de 200 em `error`:

```go
// delegacao.go:124-133
if resp.StatusCode != http.StatusOK {
	var falha struct{ Erro string `json:"erro"` }
	_ = json.NewDecoder(limitado).Decode(&falha)
	if falha.Erro != "" {
		return fmt.Errorf("comparador recusou (%d): %s", resp.StatusCode, falha.Erro)
	}
	return fmt.Errorf("comparador respondeu %d", resp.StatusCode)
}
```

E os dois handlers do agente transformam qualquer `error` em `502`:

```go
// main.go:449-452 e 559-562, identicos
if err != nil {
	registraErro("comparacao: %v", err)
	escreveErro(w, http.StatusBadGateway, err.Error())
	return
}
```

**Por que é um problema.** As três respostas que o comparador realmente emite
pedem reações opostas, e chegam ao sistema web com o mesmo código:

| O comparador diz | Significa | O navegador recebe |
|---|---|---|
| `401 credencial invalida` | o token do agente envelheceu em relação ao do serviço | `502` |
| `503 identificacao ocupada` | tente de novo em alguns segundos | `502` |
| `400 quantidade de candidatos invalida` | o pedido está errado, repetir não adianta | `502` |
| conexão recusada | o serviço caiu | `502` |

Do lado do JS, `respostaJSON` (`integra-biometria.js:66-81`) marca só o `401`
como `reconectavel`; um `502` não é, então `tentaComReconexao`
(`integra-biometria.js:103-112`) não tenta nada e o texto cru sobe para a tela:
*"comparador recusou (401): credencial invalida"*. Quem está no balcão lê uma
frase sobre credenciais de um serviço que não conhece, e o suporte é chamado
para investigar rede numa falha de configuração — enquanto o `503`, que era só
"espere três segundos", termina do mesmo jeito.

O caso do `401` é o mais provável de todos, porque ele é o sintoma de dois
críticos já abertos: o token novo a cada parada do serviço (**C5 do #10**) e o
anúncio lido uma única vez no arranque do agente (**C1 do #20**).

**Como corrigir.** Preservar a classe da resposta na travessia. Um erro tipado
resolve os dois lados:

```go
// delegacao.go — o status atravessa junto com a mensagem
type erroComparador struct {
	status int
	msg    string
}

func (e *erroComparador) Error() string {
	return fmt.Sprintf("comparador recusou (%d): %s", e.status, e.msg)
}
```

```go
// main.go — em handleComparar e handleIdentificar
if err != nil {
	registraErro("comparacao: %v", err)
	var rec *erroComparador
	switch {
	case errors.As(err, &rec) && rec.status == http.StatusServiceUnavailable:
		w.Header().Set("Retry-After", "3")
		escreveErro(w, http.StatusServiceUnavailable, "comparador ocupado, tente novamente")
	case errors.As(err, &rec) && rec.status == http.StatusBadRequest:
		escreveErro(w, http.StatusBadRequest, rec.msg)
	case errors.As(err, &rec) && rec.status == http.StatusUnauthorized:
		// Nao e problema de quem chamou: e configuracao desta maquina.
		registraErro("comparacao: o token do comparador nao confere; releia o anuncio em %s",
			caminhoAnuncio())
		escreveErro(w, http.StatusBadGateway, "comparador nao aceitou a credencial deste agente")
	default:
		escreveErro(w, http.StatusBadGateway, err.Error())
	}
	return
}
```

E, do lado do JS, tratar `503` como reconectável com espera — hoje ele não é
tratado em lugar nenhum.

### A3. A rotação do log falha em silêncio no Windows quando outro processo tem o arquivo aberto *(novo)*

**Arquivos:** `log.go:31-44`, `storage.go:13-19`, `supervisor.go:17-27`

```go
// log.go:36-40
if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}
f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
```

**Por que é um problema.** No Windows, `os.OpenFile` abre pelo
`syscall.Open`, que usa `sharemode := FILE_SHARE_READ | FILE_SHARE_WRITE`
(conferido no `syscall_windows.go` do Go 1.26, linha 395) — **sem
`FILE_SHARE_DELETE`**. Enquanto um processo mantém o log aberto, `os.Remove` e
`os.Rename` sobre aquele arquivo falham com violação de compartilhamento. E os
dois resultados são descartados com `_ =`, então a falha não vira log, não vira
erro e não vira nada: o `os.OpenFile` seguinte funciona (esse sim é permitido
por `FILE_SHARE_WRITE`) e o processo novo simplesmente continua escrevendo no
mesmo arquivo, agora com mais de 5 MB e sem teto nenhum.

Quem mantém um log aberto por muito tempo é justamente quem mais escreve: o
comparador segura `comparador.log` durante meses, e o agente segura `agente.log`
durante todo o logon. Basta o mesmo usuário ter duas sessões — console mais RDP,
ou uma conta de operador compartilhada, que é o normal num RDS — para
`%LOCALAPPDATA%` ser o mesmo para os dois agentes e a rotação parar de acontecer
em definitivo. Isto não substitui o **A2 do #21** (nada rotaciona depois do
arranque); ele explica por que, no cenário mais comum de servidor, a rotação
falha **também** no único momento em que ela deveria funcionar.

**Como corrigir.** A correção estrutural é a do A2 do #21 — um `io.Writer` que
confere o tamanho na escrita e rotaciona sozinho, fechando o arquivo antes de
renomear. Enquanto ela não vem, o mínimo é parar de descartar o resultado:

```go
// log.go — uma rotacao que nao acontece precisa aparecer no log seguinte
if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
	_ = os.Remove(path + ".1")
	if err := os.Rename(path, path+".1"); err != nil {
		// Nao ha logger ainda: guarda para registrar depois de abrir.
		pendente = fmt.Sprintf("rotacao de %s falhou (%v); o arquivo segue crescendo", path, err)
	}
}
```

---

## 🟢 Sugestões (opcional)

### S1. Nenhuma linha de log diz qual processo ou qual sessão a escreveu

```go
// log.go:12
var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)
```

Data, hora e microssegundos — mais nada. O sistema é de três processos
(supervisor → agente → worker) e de uma instância por sessão: `instanciaUnica`
usa o namespace `Local\` de propósito (`supervisor.go:17-27`), e `escreveConfig`
já sabe que sessões colidem, tanto que usa `SESSIONNAME` como parte do nome do
arquivo (`main.go:620-632`). O log é o único lugar onde essa distinção
desapareceu. Um prefixo resolve, e ele já existe pronto na biblioteca padrão:

```go
// log.go — quem escreveu passa a caber na propria linha
logger.SetPrefix(fmt.Sprintf("[%s/%d] ", os.Getenv("SESSIONNAME"), os.Getpid()))
```

### S2. `CORS_ORIGEM=https://sistema.com:443` nunca vai casar

`normalizaOrigem` devolve `scheme + "://" + strings.ToLower(u.Host)`
(`origins.go:49-61`), e `u.Host` carrega a porta quando ela está escrita. O
navegador, ao contrário, omite a porta padrão do esquema no header `Origin`.
Então uma configuração escrita com `:443` (ou `:80`) produz uma origem
pré-aprovada que nenhum navegador jamais vai mandar, e o resultado é um `403`
que aponta para o lado errado — o operador confere a URL, ela está "certa", e o
acesso continua negado. Normalizar a porta padrão desfaz a armadilha:

```go
// origins.go, em normalizaOrigem
host := strings.ToLower(u.Host)
if (scheme == "https" && strings.HasSuffix(host, ":443")) ||
	(scheme == "http" && strings.HasSuffix(host, ":80")) {
	host = host[:strings.LastIndex(host, ":")]
}
return scheme + "://" + host, nil
```

### S3. O `certutil` roda a cada arranque, e o certificado antigo nunca sai do repositório Raiz

`carregaTLS` chama `gerarCert()`, que sai cedo quando o par ainda vale, e em
seguida chama `instalaCertificadoUsuario` **sem condição nenhuma**
(`cert.go:125-132`). Isso significa um `certutil.exe -user -addstore -f Root`
nascendo a cada logon de cada sessão, para reinstalar um certificado que já está
lá. E quando a renovação acontece (o par vale 2 anos, e `certificadoValido`
recusa nos últimos 30 dias — `cert.go:47-49`), o certificado anterior continua no
repositório Raiz do usuário: `-addstore -f` acrescenta, não substitui. Depois de
alguns anos há vários "Agente de Biometria (localhost)" confiáveis, todos com
chave privada em disco, e nada os remove — nem a desinstalação, que preserva os
dados do usuário de propósito (`instalar-servidor.ps1:117`).

Instalar só quando o repositório ainda não tem aquele certificado (comparar pela
impressão digital do `.pem` com `certutil -user -verifystore Root`), e remover o
anterior pelo `serial` no momento da renovação, resolve os dois de uma vez.

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 32 (22 `.go`, 1 `.js`, 1 `.ps1`, 2 do MSI, 2 `.cmd`, 1 `.py`, `go.mod`/`go.sum`, `README.md` e 2 docs)
- **Segurança**: 🚨 **Risco** — o **C1** de hoje mostra que a pseudonimização do log não pseudonimiza: a impressão do template é um identificador estável que liga uma pessoa nomeada a um atendimento, e sobrevive ao arquivo sair da máquina. Os cinco críticos de segurança anteriores continuam abertos: parser nativo sob `LocalSystem` (**C23 do #21**), dado pessoal em log legível (**C14 do #17**), `ProgramData` gravável (**C10 do #13**), agente impostor na 5000 (**C13 do #15**) e revogação que se desfaz sozinha (**C19 do #19**)
- **Qualidade**: ⚠️ Atenção — `build`, `vet` e `gofmt` limpos; o ponto fraco desta revisão é que **o cuidado com diagnóstico foi aplicado ao que se escreve e não a quem lê**: os handlers foram reaproveitados entre dois servidores sem o embrulho que os protegia, o `stderr` do processo feito para morrer vai para o `NUL`, e a rotação do log falha em silêncio
- **Risco de produção**: 🚨 **Alto** — o **C2** deixa o funil do prédio inteiro sem controle de admissão e sem pilha quando algo quebra; um pico de logon vira uma avalanche de `502` sem nenhum `503` no meio para segurar a onda
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que não rodam em alvo nenhum (`matched no packages` no host, `exec format error` em `windows/386`) e nenhum CI. **A8 de 30/07, aberto há 20 dias**. Dos dois críticos de hoje, o C2 seria pego por um teste de handler trivial — se houvesse teste que rodasse

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
| 25 | **hoje** | **A impressão do template é identificador estável de pessoa, gravado em log** | **NOVO** |
| 26 | **hoje** | **Comparador sem `middleware`: sem teto de concorrência e sem `recover`** | **NOVO** |

---

## ✅ Pontos positivos

**O sistema pode ser interrogado na máquina onde ele falha, e os testes se
recusam a mentir.** `--conferir-contra` faz a verificação 1:1 de produção pela
linha de comando e separa "não é a pessoa" de "quebrou" em códigos de saída
diferentes (`autoteste.go:415-424`), porque as duas coisas pedem providências
opostas. `--teste-delegacao` vai além: antes de dar veredito, ele confere se o
gancho da FabulaTech está mesmo dentro do processo e, se não estiver, avisa que
"fora da sessão RDP o teste não prova nada" (`autoteste.go:525-531`). Um teste
que declara quando o próprio resultado é irrelevante é raro, e é o oposto do
teste verde que engana.

**`leTemplateDeArquivo` resolve uma ambiguidade que quase todo mundo erraria em
silêncio.** Um template base64 pode terminar em `==`, e um cadastro exportado do
banco costuma vir como `CHAVE=valor`; separar no primeiro `=` destruiria o dado
sem nenhum sintoma além de um "template inválido" solto. A solução —
`ehNomeDeVariavel` mais a exigência de que o **resto** seja um template válido
(`autoteste.go:373-409`) — é curta, e o comentário explica exatamente o modo de
falha que ela evita.

**A conta de bytes de `session.go` foi feita direito, incluindo a parte chata.**
`pidNaTabelaTCP` confere `quantidade > (len(buf)-4)/tamanhoLinhaTCP` **antes** de
indexar (`session.go:35-38`), e `pidDaConexao` refaz a chamada quando a tabela
cresce entre a medição e a leitura (`session.go:54-73`). É código que percorre
uma estrutura nativa a partir de um buffer, o lugar onde erros de um em um viram
leitura fora da alocação, e ele está com as bordas certas nos dois pontos.

**O certificado que entra no repositório Raiz do usuário não pode assinar mais
nada.** `materialCertificado` monta um par sem `IsCA`, com `KeyUsage:
DigitalSignature` apenas e `ExtKeyUsage: ServerAuth`, preso a `localhost`,
`127.0.0.1` e `::1` (`cert.go:65-78`). Instalar certificado em repositório Raiz
é normalmente o começo de um problema grande; aqui o raio de alcance foi cortado
antes, e sobra exatamente o que o `https://localhost` precisa.

**A separação entre "erro daquele registro" e "erro da operação" continua sendo
a melhor decisão do repositório** (`sdk.go:512-522`, `worker.go:130-134`,
`main.go:567-572`): um cadastro podre não bloqueia a identificação dos outros, e
"nenhum conferiu **e** houve ignorado" vira uma linha de log própria, porque é
exatamente o caso em que "não confere" mente.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O sistema chega hoje a **26 críticos abertos** e **treze dias** sem um commit de
correção. Os dois de hoje têm em comum a origem: **uma proteção que foi pensada
uma vez, num lugar, e não acompanhou o código quando ele se mudou**. A
pseudonimização do log foi desenhada quando o log era lido pela pessoa que
estava depurando a máquina, e continuou igual quando o mesmo log passou a ser
anexado em chamados e a conviver com uma base de templates. O embrulho que
limita, recupera e responde por um servidor HTTP foi escrito para o agente, e o
comparador nasceu depois reaproveitando os handlers sem ele.

A sequência recomendada muda pouco em relação à do #21, e ganha um item barato
na frente:

1. **C2 de hoje (o `protege`)** — extrair `recover` + `limiteHTTP` do
   `middleware` e aplicá-los também no comparador. São ~15 linhas movidas de
   lugar, não mudam nenhum handler, e transformam um funil silencioso num funil
   que diz "ocupado" e grava a pilha quando quebra. É também o que torna os
   próximos diagnósticos possíveis.
2. **C23 do #21 (a troca de conta)** — `Account="NT AUTHORITY\LocalService"` no
   WiX e `-UserId 'LOCAL SERVICE'` no PS1. Uma linha em cada instalador, nenhuma
   linha de Go, e o parser nativo deixa de ficar dentro do `SYSTEM`.
3. **C2 (parte do log) do #19** — as três linhas de `delegacao.go:58-65` que
   fazem o agente dizer "não há comparador anunciado, vou comparar localmente".
   Continua sendo o menor diff do repositório e o que ilumina mais coisas ao
   mesmo tempo.
4. **C1 do #20** — releitura do anúncio com cache curto. Sem ela, nenhuma
   correção do lado do comparador chega a um agente já de pé — e é ela que
   apaga a maior fonte dos `401` descritos no **A2** de hoje.
5. **C1 de hoje (o HMAC na impressão)** — é uma função de seis linhas mais a
   chave por instalação. Não muda nenhum caminho de dados, não muda o formato do
   log e não atrapalha nenhum diagnóstico existente; só deixa de entregar, de
   graça, a resposta para "esta pessoa esteve aí?".
