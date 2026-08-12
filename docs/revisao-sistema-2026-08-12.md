# 🔍 Revisão técnica do sistema — 2026-08-12

> ⚠️ **`main` continua em `26c9379`.** Os PRs **#10**, **#11**, **#12**, **#13** e
> **#14** seguem abertos e nenhum crítico foi tocado. Este é o sexto dia
> consecutivo.

As cinco revisões anteriores percorreram o caminho de dados (captura, template,
worker, delegação, comparador) e, no PR #14, o único ponto onde a decisão passa
por uma pessoa — o menu da bandeja.

Esta revisão entra pela porta que sobrou, e que é a mais estranha de todas: **o
aperto de mão que apresenta o agente ao sistema web**, e **a própria suíte de
testes**. São as duas peças que nenhuma revisão examinou, e as duas pelo mesmo
motivo — a descoberta "sempre funcionou" e os testes "existem". A suíte, em
particular, é a única parte do sistema que não consegue relatar o próprio
defeito: as *build tags* `windows && 386` impedem que `go test ./...` rode aqui,
e o **A8 do PR #10** já registrou que não há CI. Um teste quebrado neste projeto
é invisível por construção.

O resultado é **um crítico novo** — alcançável por qualquer processo local sem
privilégio nenhum, e que termina com template biométrico de cadastro na mão de
quem não devia — e **um defeito real dentro da própria suíte**, reproduzido e
executado.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, incluindo os 7 de
teste e as 48 funções de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/build-msi.cmd`,
`instalador/msi/AgenteBiometria.wixproj`, `conferir-biometria.cmd`,
`embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e os quatro
documentos em `docs/`.

**Verificações executadas:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | OK |
| `GOOS=windows GOARCH=386 go vet ./...` | limpo, inclusive nos arquivos de teste |
| `go test ./...` | **não executável aqui** — `matched no packages`: as *build tags* excluem todos os arquivos |
| Reprodução isolada de `configuraComparador` + `leAnuncio` + o teste do `delegacao_test.go:123` (cópia fiel, compilada e **executada**) | confirma **A1**: 3 dos 6 casos falham numa máquina com o comparador instalado, e passam numa sem |
| Reprodução isolada de `pidNaTabelaTCP` com tabela TCP sintética (compilada e executada) | confirma **S1**: o campo `dwState` é lido por cima e nunca conferido |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O cliente web não tem como distinguir o agente de um impostor — qualquer processo local sem privilégio ocupa a porta 5000 e recebe os templates de cadastro *(novo)*

**Arquivos:** `main.go:579-598`, `integracao/integra-biometria.js:114-137`,
`integracao/integra-biometria.js:144-151`, `179-189`, `main.go:265-305`,
`cert.go:116-122`

O agente escolhe a porta pegando a primeira livre, de baixo para cima:

```go
// main.go:591-596
for p := 5000; p <= 5099; p++ {
	l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p))
	if err == nil {
		return l, p, nil
	}
}
```

E o cliente aceita **o primeiro que responder**, na mesma ordem:

```js
// integra-biometria.js:179-188
descobrir: async function (inicio, fim) {
  inicio = inicio || 5000
  fim = fim || 5099
  for (var p = inicio; p <= fim; p++) {
    var achado = await hello(p)
    if (!achado) continue
    if (!achado.pendente) return conecta(achado)
```

```js
// integra-biometria.js:144-151 — o que basta para ser "o agente"
function conecta(achado) {
  localStorage.setItem(LS_ADDR, achado.proto + '://localhost:' + achado.dados.porta)
  sessionStorage.setItem(SS_TOKEN, achado.dados.token)
  return { porta: achado.dados.porta, sessao: achado.dados.sessao }
}
```

**Por que é um problema.** Um processo qualquer, rodando como o próprio usuário
e **sem privilégio nenhum**, faz `bind` em `127.0.0.1:5000` antes do agente. A
partir daí:

1. o agente legítimo sobe em 5001 (o `for` acha a próxima livre e não reclama —
   é o comportamento desenhado);
2. `descobrir()` começa em 5000, encontra o impostor e **para ali** — nunca
   chega em 5001;
3. o impostor responde `{"ok":true,"porta":5000,"token":"qualquercoisa","sessao":"RDP-Tcp#7"}`
   e `conecta()` o aceita, porque não existe nada na resposta que prove quem
   respondeu.

O `mesmaSessao` (`main.go:279`) é o único controle deste caminho, e ele protege
**o agente** de um chamador de outra sessão. Não protege — nem tem como
proteger — **a página** de um agente falso. São direções opostas do mesmo canal,
e só uma delas está coberta.

**O que o impostor ganha não é o token: é o template de cadastro.** O token do
agente real ele nunca vê, e não precisa. O sistema web, achando que fala com o
agente, chama:

```js
// integra-biometria.js:213-220
comparar: function (tmplGuardado, tmplLido) {
  return requisicao('/api/public/v1/captura', {
    method: 'POST',
    body: { BiometriaBenef: tmplGuardado, BiometriaLida: tmplLido },
  })
}
```

`BiometriaBenef` é o template **que veio do banco do sistema** — o cadastro do
beneficiário. Cada conferência entrega um. O mesmo vale para `identificar()`,
que manda até 5.000 cadastros de uma vez (`main.go:507`). O impostor
responde `true` e o atendimento segue normalmente; nada no fluxo denuncia. Dado
biométrico é irrevogável e é dado pessoal sensível pela LGPD — não há como
trocar a digital de quem vazou.

**E o HTTPS não salva.** `protocolos()` (`integra-biometria.js:19-23`) tenta
`http` primeiro quando a página é `http`, então em muitas instalações o
impostor nem precisa de certificado. Quando precisa, ele consegue: o próprio
agente instala o seu certificado na loja `Root` **do usuário**, e isso **não
exige elevação**:

```go
// cert.go:116 — sem -f de máquina, sem UAC: qualquer processo do usuario faz igual
cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
```

Um processo sem privilégio pode gerar o seu próprio certificado para `localhost`
e instalá-lo pelo mesmo comando. O cadeado fica verde para os dois.

**Como corrigir.** A saída certa já existe no código e só não é obrigatória: o
canal fora de banda que o próprio agente abre pela bandeja.

```go
// main.go:643-654 — o agente ja entrega porta e token pela URL que ELE abre
func urlSistema() string {
	...
	return fmt.Sprintf("%s%sbioPort=%d&bioToken=%s", base, separador, porta, token)
}
```

Quem chega ao sistema por "Abrir sistema" chega com `bioPort` e `bioToken` no
fragmento, vindos do processo real — um impostor não tem como injetar isso na
navegação do usuário. O cliente já lê os dois (`integra-biometria.js:25-31`).
Falta fazer disso o caminho **principal**, e não uma conveniência:

```js
// integra-biometria.js — a varredura passa a ser o ultimo recurso, e avisa.
garantirConexao: async function () {
  if (token() && (await this.disponivel())) return true
  // Token fora de banda (bioToken no fragmento) e o unico caminho em que a
  // pagina sabe com quem esta falando. A varredura nao prova nada: ela aceita
  // quem responder primeiro na porta mais baixa.
  if (!token()) {
    throw new Error('Abra o sistema pelo icone do agente na bandeja ' +
      '(menu "Abrir sistema") para estabelecer a conexao.')
  }
  ...
}
```

Enquanto a varredura existir, duas medidas a tornam bem mais cara, e ambas são
pequenas:

```go
// main.go, em escolheListener — porta derivada da sessao, e nao "a primeira
// livre". Um impostor precisaria adivinhar qual, e ocupar a errada nao serve
// de nada.
inicio := 5000 + int(hashSessao(os.Getenv("SESSIONNAME"))%100)
```

```js
// integra-biometria.js, em hello() — nao aceitar em claro o que o agente
// oferece cifrado. Se o agente diz https:true, so vale a resposta em https.
if (dados && dados.https && lista[i] !== 'https') continue
```

O PR #13 fixou o critério que separa risco alto de risco teórico: precisar ou
não de **acesso local**. Este precisa — mas de acesso local **sem privilégio
algum**, num servidor RDS com dezenas de sessões, que é o cenário em que essa
barreira é mais baixa. É o mesmo patamar do C2 do #13.

---

### Reincidentes — reconferidos hoje, linha a linha

Nenhum arquivo mudou desde `26c9379`. Não repito os textos; todos continuam
válidos nas mesmas linhas.

| Achado | Onde | Situação |
|---|---|---|
| **C1 #14** — `tituloOrigem` corta o domínio que decide; diálogo forjável de qualquer site | `main.go:655-661`, `696-703` | ❌ Aberto |
| **C2 #14** — pendência expirada nunca sai da bandeja; `aprova()` não consulta `pendentes` | `main.go:663-744`, `origins.go:131-138` | ❌ Aberto |
| **C1 #13** — fila única do SDK no comparador; sem `limiteHTTP` no modo serviço | `comparador.go:77-78`, `104`, `main.go:100-105` | ❌ Aberto |
| **C2 #13** — ACL herdada do `ProgramData` + `endereco` do anúncio sem checagem de loopback | `wxs:57-62`, `123-135`, `delegacao.go:74-85` | ❌ Aberto |
| **C1 #12** — parar o serviço apaga o anúncio; token novo derruba os agentes de pé | `comparador.go:99`, `anuncio.go:113-121` | ❌ Aberto. Reconferido: o `defer removeAnuncio()` (`comparador.go:99`) apaga justamente o arquivo que `tokenDoComparador()` releria — **o token é preservado quando o serviço morre de forma suja e trocado quando ele para direito**, que é o inverso do que o comentário do `anuncio.go:110-113` pretende |
| **C4 #12** — MSI e `.ps1` disputam nome e porta | `wxs:71-99`, `instalar-servidor.ps1:167-193` | ❌ Aberto |
| **R1 #13** — `bioPort` sem validação (negação de serviço, não vazamento) | `integra-biometria.js:25-34` | ❌ Aberto |
| **C2 de 2026-07-30** — o cliente JS descarta `ignorados` | `integra-biometria.js:229` | ❌ Aberto |
| **A1..A5 #14, A1..A4 #13, A1..A11 e S1..S10 #12** | — | ❌ Todos abertos |

---

## 🟡 Alertas (recomenda correção)

### A1. Um teste lê o `ProgramData` real e falha exatamente no servidor que ele deveria proteger *(novo — reproduzido e executado)*

**Arquivos:** `delegacao_test.go:123-147`, `anuncio_test.go:13-21`,
`delegacao.go:52-72`

Todos os testes do `anuncio_test.go` redirecionam o diretório compartilhado
antes de qualquer coisa — é a primeira linha de cada um deles:

```go
// anuncio_test.go:13-21
func usaDiretorioTemporario(t *testing.T) string {
	dir := t.TempDir()
	anterior := diretorioCompartilhado
	diretorioCompartilhado = func() string { return dir }
	t.Cleanup(func() { diretorioCompartilhado = anterior })
	return dir
}
```

`TestConfiguraComparadorSoLigaComConfiguracaoUtil` **não chama** (linhas 24, 56,
68, 81, 97, 109, 128, 153, 168 do `anuncio_test.go` chamam; `delegacao_test.go:123`
é o único que não). E ele exercita justamente a função que lê o `ProgramData`:

```go
// delegacao_test.go:128-130 — tres casos que dependem de nao existir anuncio
{"sem url", "", segredoTeste, false},
{"token curto", "http://127.0.0.1:5150", "curto", false},
{"sem token", "http://127.0.0.1:5150", "", false},
```

```go
// delegacao.go:58-59 — qualquer um dos tres cai aqui
if base == "" || len(token) < 32 {
	a, err := leAnuncio()      // <- C:\ProgramData\AgenteBiometria\comparador.json, o de verdade
```

**Por que é um problema.** Numa máquina com o comparador instalado e no ar — ou
seja, **no servidor RDS**, o único lugar onde faz sentido rodar esta suíte — o
anúncio existe, `leAnuncio()` devolve endereço e token válidos, `configuraComparador()`
liga a delegação e os três casos esperam `false`. Reproduzi a função e o teste
num programa isolado, compilado e executado nos dois cenários:

```
### Maquina SEM comparador (nenhum comparador.json):
ok      t2      0.004s

### Maquina COM comparador instalado e no ar:
    --- FAIL: .../sem_url
    --- FAIL: .../token_curto
    --- FAIL: .../sem_token
    --- PASS: .../url_sem_esquema
    --- PASS: .../completa
    --- PASS: .../com_barra_no_fim
FAIL
```

Três consequências, e a terceira é a que importa:

1. o teste **passa na máquina de quem escreveu** (sem comparador) e falha na de
   produção — o pior padrão possível de falso vermelho;
2. ele **lê o `ProgramData` da máquina que o roda**, coisa que o comentário do
   `usaDiretorioTemporario` diz explicitamente que os testes não podem fazer;
3. o A8 do PR #10 pede CI. **No dia em que o CI subir, a primeira execução num
   servidor real vem vermelha por um defeito que não é do código** — e essa é
   exatamente a maneira como uma equipe aprende a ignorar a própria suíte. O
   custo não é o teste: é a credibilidade dos outros 47.

E nada disso apareceria hoje, porque as *build tags* impedem `go test` de rodar
em qualquer máquina que não seja `windows/386`. A suíte é a única parte do
sistema que não consegue relatar o próprio defeito.

**Como corrigir.** Uma linha, igual às outras nove:

```go
// delegacao_test.go:123
func TestConfiguraComparadorSoLigaComConfiguracaoUtil(t *testing.T) {
	usaDiretorioTemporario(t)   // <- isola do ProgramData da maquina
	casos := []struct {
```

E, para que o próximo esquecimento não custe outro dia de diagnóstico, tornar o
isolamento a regra e não a lembrança:

```go
// anuncio_test.go — falha alto e claro se alguem esquecer.
func TestMain(m *testing.M) {
	if os.Getenv("BIO_FAKE_WORKER") != "" { ... }
	// Nenhum teste pode enxergar o ProgramData real.
	base, _ := os.MkdirTemp("", "bio-testes-")
	diretorioCompartilhado = func() string { return base }
	go sdkThreadMain()
	os.Exit(m.Run())
}
```

### A2. A descoberta é de passe único: uma falha passageira em `/api/hello` faz o agente sumir da varredura inteira *(novo)*

**Arquivos:** `integracao/integra-biometria.js:114-137`, `179-189`, `192-196`,
`main.go:265-305`, `main.go:250-259`

O cliente dá **um** passe pelas 100 portas, com 700 ms por protocolo, e não
volta:

```js
// integra-biometria.js:117-118
var ctl = new AbortController()
var timeout = setTimeout(function () { ctl.abort() }, 700)
```

```js
// integra-biometria.js:123 — 403 encerra as duas tentativas de protocolo
if (r.status === 403) return null
```

```js
// integra-biometria.js:183-187
var achado = await hello(p)
if (!achado) continue        // <- a porta e descartada e nunca revisitada
```

**Por que é um problema.** `handleHello` tem **quatro** saídas que produzem
exatamente o mesmo efeito no cliente — a porta é riscada da lista — e três delas
são passageiras:

| Saída | Onde | Passageira? |
|---|---|---|
| `503 agente ocupado` (as 32 vagas de `limiteHTTP` cheias) | `main.go:258` | **sim** |
| `503 chamador nao identificado` (a linha da conexão não estava na tabela TCP) | `main.go:281` | **sim** |
| `403 sessao diferente` | `main.go:285` | não, mas ver **S1** |
| `abort` aos 700 ms | `integra-biometria.js:118` | **sim** |

As 32 vagas de `limiteHTTP` são compartilhadas com `/identificar`, que segura a
vaga por até **4 minutos** (`main.go:543`), e com `/captura/Enroll`, que segura
por até 55 s (`main.go:356-364`). Numa estação com duas abas abertas, ou num servidor
onde o operador acabou de disparar uma identificação 1:N, uma varredura que
começar nesse intervalo pode simplesmente não achar o agente.

E o prazo de 700 ms é curto para o que `/api/hello` faz: cada chamada percorre a
**tabela TCP inteira da máquina** (ver **A3**). Num RDS com dezenas de sessões,
isso não é uma operação de microssegundos.

O que o usuário vê é `descobrir()` devolver `null` e a mensagem
*"Nao foi possivel falar com o agente em ..."* (`integra-biometria.js:52-54`) —
com o agente rodando, o ícone verde na bandeja e a porta atendendo. É o tipo de
falha que vira folclore de "reinicia o agente que resolve", porque reiniciar
**de fato** resolve: a varredura seguinte pega o sistema em outro momento.

**Como corrigir.** Distinguir "não é ele" de "não deu para saber agora", e
repetir só o segundo:

```js
// integra-biometria.js, em hello() — separar as respostas transitorias.
if (r.status === 403) return null            // essa porta e de outra sessao
if (r.status === 503) return { retentar: true }  // ocupado ou nao identificado
```

```js
// integra-biometria.js, em descobrir() — um segundo passe so nas transitorias.
var retentar = []
for (var p = inicio; p <= fim; p++) {
  var achado = await hello(p)
  if (achado && achado.retentar) { retentar.push(p); continue }
  ...
}
for (var i = 0; i < retentar.length; i++) {
  await espera(400)
  var segundo = await hello(retentar[i])
  if (segundo && !segundo.retentar) return conecta(segundo)
}
```

E subir o prazo do `abort` de 700 ms para algo compatível com o custo real de
`/api/hello` — 2 s por porta ainda dá uma varredura tolerável, porque o caso
comum é a porta fechada, que falha em microssegundos.

### A3. `/api/hello` é o único endpoint sem autenticação e, ao mesmo tempo, o mais caro do agente *(novo)*

**Arquivos:** `main.go:224-247`, `main.go:277-285`, `session.go:54-73`,
`session.go:31-52`

O middleware libera `/api/hello` do token de propósito — é o endpoint que
**entrega** o token:

```go
// main.go:242-249
if strings.HasPrefix(r.URL.Path, "/api/") && !hello {
	tok := r.Header.Get("X-Bio-Token")
	...
}
```

E o handler, antes de qualquer outra coisa, resolve a sessão do chamador:

```go
// main.go:279
mesma, ok := mesmaSessao(uint16(p), uint16(porta))
```

`mesmaSessao` → `pidDaConexao` → `GetExtendedTcpTable` com
`TCP_TABLE_OWNER_PID_ALL`: **a tabela TCP inteira da máquina**, alocada e
percorrida linha a linha, com até 4 tentativas quando o tamanho muda entre as
duas chamadas (`session.go:55-72`).

**Por que é um problema.** É a inversão da regra usual: o endpoint mais barato de
alcançar é o mais caro de atender. E ele é alcançável de qualquer página da
internet, porque `aplicaCORS` responde `Access-Control-Allow-Private-Network: true`
para **qualquer** origem (`main.go:200`) — o cabeçalho que faz o navegador
liberar a chamada de um site público para o `localhost`.

O custo por chamada é machine-wide e o número de agentes também: num RDS com 40
sessões, 40 agentes, cada um enumerando a tabela TCP do servidor inteiro a cada
`/api/hello`. E o cliente oficial já é generoso: 100 portas × 2 protocolos por
varredura, mais uma chamada por segundo durante 60 s enquanto espera autorização
(`integra-biometria.js:153-165`).

Não chamo isso de negação de serviço porque o navegador limita as conexões
simultâneas por host, e o atacante não consegue segurar as 32 vagas de
`limiteHTTP`. Mas ele consegue impor carga desproporcional sem nenhuma
credencial, e — pelo **A2** — cada agente que ficar lento nesse intervalo some da
varredura de quem estiver tentando trabalhar.

**Como corrigir.** A resposta da sessão para uma mesma porta de origem não muda:
dá para memorizar por poucos segundos, e cortar o custo do caso repetido a zero.

```go
// session.go — cache curto por porta de origem. A conexao vive alguns
// segundos; a sessao do dono dela nao muda no meio.
var (
	cacheSessaoMu sync.Mutex
	cacheSessao   = map[uint16]struct {
		mesma, ok bool
		em        time.Time
	}{}
)

func mesmaSessaoCache(portaOrigem, portaDestino uint16) (bool, bool) {
	cacheSessaoMu.Lock()
	if e, achou := cacheSessao[portaOrigem]; achou && time.Since(e.em) < 3*time.Second {
		cacheSessaoMu.Unlock()
		return e.mesma, e.ok
	}
	cacheSessaoMu.Unlock()
	mesma, ok := mesmaSessao(portaOrigem, portaDestino)
	...
}
```

E limitar `/api/hello` por origem — algo como 10 chamadas por minuto por origem
não atrapalha o cliente oficial (que faz 1/s durante a espera de autorização) e
fecha o abuso.

### A4. Com a fila de pendentes cheia, uma origem legítima fica trancada por 10 minutos — e a resposta é idêntica à de uma pendência de verdade *(novo)*

**Arquivos:** `origins.go:98-129`, `origins.go:17-20`, `main.go:293-299`

`solicita()` tem **três** caminhos que devolvem `false`, e o handler trata os
três da mesma forma:

```go
// origins.go:114-127
if _, ok := g.pendentes[origem]; ok {
	g.mu.Unlock()
	return false                       // (1) ja pendente — correto
}
if len(g.pendentes) >= maxOrigensPendentes {
	g.mu.Unlock()
	return false                       // (2) fila cheia — a origem NAO entra
}
g.pendentes[origem] = agora
g.mu.Unlock()
select {
case g.novas <- origem:
default:                               // (3) canal cheio — o aviso e DESCARTADO
}
return false
```

```go
// main.go:292-298 — a mesma resposta para os tres
if !origens.solicita(origem) {
	escreveJSON(w, http.StatusAccepted, map[string]any{
		"ok": false, "autorizacao": "pendente", ...
	})
```

**Por que é um problema.** No caminho (2), a origem legítima **nem entra em
`pendentes`** e nunca chega à bandeja — mas recebe `202 pendente`, e o cliente
entra no laço de 60 s esperando uma autorização que não tem onde acontecer
(`integra-biometria.js:153-165`). Como `maxOrigensPendentes = 8` e o mapa só é
varrido dentro do próprio `solicita` (**S6 do #14**), as 8 vagas podem ficar
ocupadas por até 10 minutos. Uma página hostil que troque de subdomínio —
`a1.evil.example`, `a2.evil.example`, … — enche as 8 quando quiser, e o sistema
de verdade não consegue nem pedir autorização.

No caminho (3) é pior, porque a origem **entra** em `pendentes` mas o aviso é
descartado em silêncio: nas próximas 10 tentativas o caminho (1) devolve `false`
antes de qualquer coisa, e a bandeja nunca soube que havia alguém pedindo.
Nenhum dos dois escreve uma linha de log.

Isto não substitui o C2 do #14 — lá o problema é o item forjado que **fica**;
aqui é o pedido legítimo que **não chega**. São os dois lados da mesma fila.

**Como corrigir.** Dizer a verdade ao cliente e ao log:

```go
// origins.go — distinguir "esta na fila" de "a fila esta cheia".
func (g *gerenciadorOrigens) solicita(origem string) (autorizada, enfileirada bool) {
	...
	if len(g.pendentes) >= maxOrigensPendentes {
		g.mu.Unlock()
		registraErro("fila de autorizacoes cheia (%d); %q nao pode ser oferecida ao usuario",
			maxOrigensPendentes, origem)
		return false, false
	}
	...
	select {
	case g.novas <- origem:
	default:
		delete(g.pendentes, origem)     // nao registra o que a bandeja nao viu
		registraErro("aviso de autorizacao descartado para %q", origem)
		g.mu.Unlock()
		return false, false
	}
	return false, true
}
```

```go
// main.go, em handleHello — 503 diz "tente de novo"; 202 diz "espere".
if !enfileirada {
	escreveErro(w, http.StatusServiceUnavailable,
		"ha autorizacoes pendentes demais; feche o menu da bandeja e tente de novo")
	return
}
```

### A5. Todo o arranque anterior a `iniciaLog()` é mudo: o agente que não sobe não deixa vestígio em lugar nenhum *(novo)*

**Arquivos:** `main.go:877-885`, `supervisor.go:17-27`, `supervisor.go:29-56`,
`log.go:12-13`

O log só existe a partir de `main.go:885`:

```go
// main.go:877-885
if os.Getenv("BIO_FILHO") != "1" {
	if !instanciaUnica() {
		return 0                 // <- sai calado
	}
	supervisor()
	return 0
}

iniciaLog()                      // <- a primeira linha de log do processo
```

Antes disso, `logger` escreve em `io.Discard` (`log.go:12`). E os caminhos de
falha desse trecho não escrevem em lugar nenhum:

```go
// supervisor.go:19-26
if err != nil {
	return false          // UTF16PtrFromString falhou -> agente nao sobe, sem log
}
h, _, errno := procCreateMutexW.Call(...)
if h == 0 {
	return false          // CreateMutexW falhou -> agente nao sobe, sem log
}
```

```go
// supervisor.go:30-33
exe, err := os.Executable()
if err != nil {
	os.Exit(1)            // sem log, sem mensagem, sem janela
}
```

**Por que é um problema.** O binário é compilado com `-H windowsgui`
(`README.md`, seção Compilação), então não há console e não há janela. Um agente
que morra nesse trecho não deixa **nada**: nem log, nem saída de tela, nem
código de saída visível (a chave `Run` não guarda o resultado). Para quem dá
suporte, "o agente não abre" é literalmente tudo o que existe.

Pior: `supervisor()` reinicia o filho em laço com espera exponencial
(`supervisor.go:36-55`). Se o filho morrer **antes** de `iniciaLog()` — por
exemplo, `escolheListener` não achar porta livre entre 5000 e 5099
(`main.go:597`) — o laço roda para sempre, um processo por minuto, sem uma linha
escrita.

**Como corrigir.** Subir o log para antes de tudo. Ele já sabe se virar quando o
diretório não existe (`log.go:32-33` desiste em silêncio), então não há risco em
chamá-lo cedo:

```go
// main.go, em executa() — antes do bloco de BIO_FILHO.
iniciaLog()
registraInfo("arranque: versao=%s commit=%s filho=%v", versao, commit, os.Getenv("BIO_FILHO") == "1")

if os.Getenv("BIO_FILHO") != "1" {
	if !instanciaUnica() {
		registraInfo("ja existe um agente nesta sessao; saindo")
		return 0
	}
	...
}
```

```go
// supervisor.go — dizer por que desistiu.
if err := cmd.Start(); err != nil {
	registraErro("supervisor: nao consegui subir o filho: %v", err)
	os.Exit(1)
}
```

E, no laço, registrar o código de saída do filho: é o dado que responde
"morreu sozinho ou alguém matou?" sem nenhuma outra ferramenta.

### A6. `configuraComparador()` roda uma vez e o token nunca é reconferido — a correção proposta no A1 do #13 não cobre o modo de falha do C1 do #12 *(novo)*

**Arquivos:** `main.go:890`, `delegacao.go:52-102`, `anuncio.go:105-122`,
`main.go:565-577`

`configuraComparador()` é chamada exatamente uma vez, no arranque
(`main.go:890`), e grava `base` e `token` numa struct que ninguém mais toca.

O A1 do PR #13 propõe reler o anúncio **quando `comparadorRemoto == nil`**. Isso
cobre o agente que subiu antes do serviço. **Não cobre** o caso do C1 do #12,
que é o mais frequente: o agente subiu com um token, o serviço foi reiniciado e
gerou outro. Aí `comparadorRemoto` **não é nil** — é um cliente perfeitamente
formado, com um token morto. A releitura condicionada a `nil` nunca dispara, e
cada comparação vira:

```
comparador recusou (401): credencial invalida
```

...que chega ao operador como `502` (`main.go:451`), no meio de um atendimento,
com o dedo já no leitor. Reconferindo hoje: `defer removeAnuncio()`
(`comparador.go:99`) apaga o arquivo na parada limpa, e `tokenDoComparador()`
(`anuncio.go:113-121`) o releria para reaproveitar o segredo — ou seja, **a
parada limpa é justamente a que troca o token**, e a morte suja é a que o
preserva. É o inverso do que o comentário do `anuncio.go:110-113` pretende, e é
por isso que o C1 do #12 acontece no caminho normal.

**Como corrigir.** Tratar o 401 como o sinal que ele é, e não como erro de
aplicação:

```go
// delegacao.go, em chama() — 401 significa "o anuncio mudou", nao "o pedido e ruim".
if resp.StatusCode == http.StatusUnauthorized {
	registraErro("comparador recusou a credencial; relendo o anuncio")
	if a, erroAnuncio := leAnuncio(); erroAnuncio == nil && a.Token != c.token {
		c.token = a.Token
		return errRecarregado      // o chamador repete uma unica vez
	}
}
```

```go
// main.go, em handleComparar/handleIdentificar — uma repeticao, e so uma.
ok, err = comparadorRemoto.compara(ctx, benef, lida)
if errors.Is(err, errRecarregado) {
	ok, err = comparadorRemoto.compara(ctx, benef, lida)
}
```

E `/api/status` (`main.go:573-577`) devolve `info["comparador"]` sem dizer se ele
**responde**. Um `GET /status` ao comparador, com prazo curto, transformaria a
pergunta "a comparação está de pé?" numa resposta em vez de uma dedução.

---

## 🟢 Sugestões (opcional)

- **S1.** `session.go:40-48` — `pidNaTabelaTCP` lê o campo `dwState` da
  `MIB_TCPROW_OWNER_PID` (é o `linha[0:4]`, pulado direto para o `linha[4:8]`) e
  **nunca o confere**, e devolve a primeira linha que casar sem checar se o PID
  é zero. Reproduzi com uma tabela sintética: com uma linha em `TIME_WAIT`
  (PID 0, como o Windows reporta) antes da conexão viva, a função devolve
  `pid=0, ok=true`. Numa pilha sadia o mesmo par de portas não fica duas vezes na
  tabela, então isto **não é explorável hoje** — mas é uma premissa não escrita
  numa função que decide isolamento entre sessões RDP. Duas linhas fecham:
  `if estado != mibTCPStateEstab || pid == 0 { continue }`, seguindo a varredura
  em vez de devolver a primeira.
- **S2.** `integra-biometria.js:154-156` — `aguardaAutorizacao` grava o endereço
  do agente em `localStorage` **antes** de a autorização existir, e não o apaga
  quando o laço de 60 s estoura (linha 164 devolve `null` e pronto). O endereço
  de um agente que o usuário recusou fica gravado para a próxima sessão.
- **S3.** Nenhum teste prova que `/comparar` e `/identificar` estão **sob**
  `exigeSegredo`. `comparador_test.go:11-17` monta a própria cadeia
  (`servidorProtegido`) e testa a função isolada; a montagem real está em
  `comparador.go:75-79`. Uma rota registrada por fora do envelope passaria pela
  suíte inteira. Um teste que chame `rodaComparadorCom` numa porta efêmera e
  bata em `/comparar` **sem** `Authorization` fecha isso.
- **S4.** `integra-biometria.js:83-101` — `requisicao()` não passa `signal` nem
  prazo. `capturar()` e `enroll()` ficam presas indefinidamente se o agente
  parar de responder no meio (`hello()`, na linha 118, tem prazo; o caminho de
  produção não). Vale o mesmo `AbortController`, com o prazo do lado do servidor
  (`main.go:356-364`) mais uma folga.
- **S5.** `worker_test.go:18-25` — o `TestMain` substitui o worker real por
  `workerFalso`, então `workerMain()` (`worker.go:64-102`) — inclusive a linha
  do `iniciaLogArquivo("worker.log")` que o **A1 do #14** aponta — não é
  exercitada por teste nenhum. Não é defeito da suíte (isolar a DLL é o ponto),
  mas convém dizê-lo no comentário, para ninguém ler a cobertura do
  `clienteWorker` como cobertura do worker.
- **S6.** `.gitignore` continua sem `comparador.json` (**S5 do #14**, aberto). É
  uma linha, e é o único desses arquivos que carrega uma credencial válida para
  a máquina inteira.
- **S7.** `main.go:224` — `hello := r.URL.Path == "/api/hello"` compara o caminho
  cru, antes de o `ServeMux` normalizar. Conferi os desvios óbvios (`//api/...`,
  `/api/./hello`, `%61pi`) e **todos falham fechado**: ou o token passa a ser
  exigido, ou o mux responde 301 para o caminho limpo e a requisição volta pelo
  middleware. Está correto — só não está escrito que é de propósito, e é o tipo
  de linha que alguém "simplifica" para `strings.HasSuffix` num dia ruim.

---

## 📋 Resumo

| | |
|---|---|
| **Arquivos alterados** | 1 neste PR (só documentação); **nenhum em `main`** desde `26c9379`; 41 revisados |
| **Segurança** | 🚨 Risco |
| **Qualidade** | ⚠️ Atenção |
| **Risco de produção** | 🚨 Alto |
| **Testes** | ⚠️ Parcial — e com um defeito próprio (**A1**) |

**Contagem de hoje:** 1 crítico novo, 6 alertas novos, 7 sugestões novas. Os
críticos dos PRs #12, #13 e #14 seguem todos abertos e foram reconferidos linha
a linha.

**Sobre os testes — desta vez como objeto, e não como ausência.** As 48 funções
cobrem bem o que é difícil de acertar em C: `sdk.go`, `worker.go`, `delegacao.go`,
`anuncio.go`, `versaodll.go`. São testes bem escritos, com o *porquê* no
comentário — `TestLeTemplatePreservaPaddingBase64` e
`TestComparadorRecusaCredencialComSobra` são exemplares. O problema é o entorno:

| Achado | O que a suíte teria pego, e por que não pegou |
|---|---|
| **C1** | Nada — não há teste algum de `handleHello`, de `escolheListener` ou do cliente JS |
| **A1** | Ela mesma. Não pegou porque **nunca roda**: *build tags* `windows && 386` + sem CI (**A8 do #10**) |
| **A4** | `TestSolicitaComFilaCheia`: 9 origens seguidas, assertar que a nona é distinguível das 8 primeiras. Não depende de systray |
| **A6** | Um `httptest` devolvendo 401 e a asserção de que o cliente relê o anúncio. `delegacao_test.go` já tem toda a montagem |

`origins.go`, o middleware, `session.go`, `storage.go`, `cert.go` e `log.go`
continuam sem uma linha de teste — e é onde moram os críticos dos últimos dois
dias.

---

## ✅ Pontos positivos

- **O middleware falha fechado em todos os desvios de caminho que tentei.**
  `//api/public/v1/captura` não casa com o prefixo `/api/`, mas o `ServeMux`
  responde 301 em vez de servir, e a requisição limpa volta pelo middleware com
  o token exigido. `/api/hello` sem header `Origin` chega ao handler e morre em
  `origemDoHeader` com 400 (`main.go:288-291`) — não há caminho para o token sem
  uma origem válida. É o tipo de coisa que costuma estar errada e aqui está
  certa em três lugares independentes.
- **O isolamento entre sessões RDP está desenhado na direção certa.** O
  `Local\AgenteBiometriaGo` do mutex (`supervisor.go:18`) é por sessão, e não
  global — trocar por `Global\` faria o segundo usuário do servidor não ter
  agente, e é um erro comum. O token vive só em memória e no perfil do usuário,
  e `mesmaSessao` fecha o único caminho pelo qual ele sairia. O C1 de hoje é o
  outro sentido do mesmo canal, e não uma falha deste.
- **A suíte de testes explica cada asserção pelo defeito que a motivou.**
  `TestIdentificaIgnoraCandidatoCorrompidoESegue`,
  `TestExigeReinicioSDKAtravessaWrap`, `TestNormalizaTemplateRejeitaTruncamentoSevero`
  — cada um traz no comentário o incidente de campo que o gerou. Um teste assim
  sobrevive à refatoração porque quem o quebrar entende o que está apagando. É
  raro, e é o motivo pelo qual o **A1** vale a pena consertar em vez de
  contornar.
- **`normalizaTemplate` (`sdk.go:407-421`) é a barreira certa no lugar certo**, e
  o comentário diz exatamente por quê: a DLL confia nos campos de tamanho
  embutidos e lê fora da alocação quando eles não batem, e uma violação de acesso
  lá dentro não vira `panic` recuperável. Rejeitar tudo que não seja ASCII
  imprimível contínuo é agressivo — e é a única postura defensável quando o
  parser do outro lado não perdoa.
- **`build` e `vet` limpos** para `windows/386`, arquivos de teste incluídos, em
  4.965 linhas que manipulam memória nativa, `uintptr` e FFI.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O achado de hoje tem uma origem diferente da dos anteriores. O PR #13 encontrou
um problema de **escala** (o comparador cresceu e os pressupostos não foram
recontados); o #14, um de **fronteira** (o único ponto que chega aos olhos de uma
pessoa foi tratado como apresentação). Este é de **confiança presumida**: o
sistema inteiro foi construído para o agente não confiar em quem chega —
sessão, origem, token, normalização de template, tudo — e ninguém escreveu a
outra metade, que é o sistema web não ter como saber com quem está falando. A
varredura de portas parece uma conveniência de instalação; ela é, na prática, uma
delegação de confiança para "quem responder primeiro na porta mais baixa".

E o **A1** é o mesmo problema aplicado ao processo: existe uma suíte, ela é boa,
e ninguém sabe se ela passa — porque nada nem ninguém a executa.

**Ordem sugerida de correção**, considerando tudo que está aberto:

1. **C1 + C2 do #14** — juntos, ~25 linhas em `main.go` e `origins.go`. Continuam
   sendo o único par alcançável **sem nenhum acesso à máquina**, e não dependem
   de decisão de arquitetura pendente.
2. **A1 de hoje** — uma linha (`delegacao_test.go:123`), e é o que precisa estar
   verde **antes** de qualquer CI subir. Fazer o contrário é ensinar a equipe a
   ignorar a suíte na primeira semana.
3. **C1 de hoje, medida imediata** — a porta derivada da sessão e a recusa de
   `http` quando o agente anuncia `https:true`. São duas mudanças pequenas que
   não exigem tocar no fluxo de autorização, e encarecem muito o impostor.
4. **C2 #13 (loopback em `delegacao.go` + ACL do `ProgramData`)** — o que impede
   template biométrico de sair da máquina.
5. **A6 de hoje + C1 #12** — juntos, porque são o mesmo defeito visto dos dois
   lados: o serviço troca o token na parada limpa, e o agente nunca reconfere.
   Corrigir só um dos dois deixa metade do sintoma de pé.
6. **A2 e A5 de hoje** — não mudam o comportamento correto do sistema, mas são o
   que decide se o próximo chamado vai ser "o agente não abre" ou um diagnóstico.
7. **C1 #13 (fila do SDK no comparador)** — o mais trabalhoso; as três medidas
   paliativas cabem num commit.

**C1 de hoje, solução definitiva:** tornar o `bioToken` fora de banda o único
caminho de conexão. O código já existe dos dois lados (`main.go:643-654` e
`integra-biometria.js:25-31`); falta decidir que a varredura é fallback, e não a
regra.

Este PR **não altera código**: acrescenta apenas este documento.
