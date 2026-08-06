# 🔍 Review do PR #9: docs: revisão técnica do sistema — 2026-08-06

Nona revisão técnica do sistema, sobre o commit `d1c3846` — o mesmo desde
2026-08-01. Nenhuma correção das oito revisões anteriores foi aplicada.

**Escopo analisado:** `main.go`, `sdk.go`, `worker.go`, `comparador.go`,
`delegacao.go`, `autoteste.go`, `versaodll.go`, `session.go`, `cert.go`,
`origins.go`, `storage.go`, `supervisor.go`, `log.go`, os cinco arquivos de
teste, `go.mod`/`go.sum`, `.gitignore`, `embutir-icone.py`,
`integracao/integra-biometria.js`, `integracao/COMO-USAR.md`,
`instalador/instalar-servidor.ps1`, `README.md`.

**Verificações executadas nesta revisão:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | ✅ limpo |
| `GOOS=windows GOARCH=386 go vet ./...` | ✅ limpo |
| `go test ./...` | ❌ **não executável aqui** — `go: warning: "./..." matched no packages`; as *build tags* `windows && 386` excluem todos os arquivos deste alvo |

Nono dia seguido em que os 34 testes do repositório não rodam em lugar
verificável: não há `.github/`, não há CI, e o único alvo que compila os
arquivos é um Windows x86 real.

**Nota de processo.** As revisões estão se acumulando como PRs abertos: #1, #3,
#4, #5, #6, #7 e #8 seguem abertos, todos com base no mesmo commit, cada um
adicionando um arquivo em `docs/`. Só a revisão de 07-30 chegou à `main`. Vale
decidir se essas revisões devem virar *issues* rastreáveis em vez de PRs de
documentação — do jeito atual, o backlog de defeitos mora em sete branches que
ninguém precisa abrir para dar merge em nada.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Os dez críticos das oito revisões anteriores seguem abertos, linha por linha

Esta revisão **não encontrou defeito novo de severidade crítica**. O que ela
encontra é que o backlog crítico não se moveu: cada trecho abaixo foi reaberto e
reconferido no código de hoje, e todos continuam idênticos.

| # | Achado | Origem | Trecho conferido hoje | Situação |
|---|---|---|---|---|
| 1 | `bioPort` do fragmento vira *userinfo* e desvia token + template para host arbitrário | 07-30 C1 | `integra-biometria.js:27-34` | ❌ Aberto |
| 2 | O cliente JS descarta `ignorados` — cadastro corrompido vira "digital não encontrada" | 07-30 C2 | `integra-biometria.js:229` | ❌ Aberto |
| 3 | 1:N é um laço de 1:1 — a falsa aceitação acumula com o nº de candidatos | 07-30 C3 | `sdk.go:480-533` | ❌ Aberto |
| 4 | Fila única do SDK: uma identificação congela toda captura da sessão | 07-30 C4 | `main.go:107-138`, `main.go:140-158` | ❌ Aberto |
| 5 | O comparador serializa o servidor inteiro numa fila só, sem `limiteHTTP` | 07-31 C1 / 08-01 C2 | `comparador.go:64-93` | ❌ Aberto |
| 6 | `/status` do comparador responde `ok: true` sem nunca tocar no SDK | 07-31 C2 | `comparador.go:120-133` | ❌ Aberto |
| 7 | O comparador roda como `SYSTEM` e recebe bytes de qualquer usuário logado | 08-01 C1 | `instalar-servidor.ps1:178-192` | ❌ Aberto |
| 8 | `-ComparadorPorta` sem `-ComparadorUrl` instala um par que nunca conversa | 08-01 C3 | `instalar-servidor.ps1:17-18, 167-193` | ❌ Aberto |
| 9 | `PORTA`/`MODO_COMPARADOR` como variável de máquina + laço infinito do supervisor | 08-03 C1 | `main.go:571-590`, `supervisor.go:29-56` | ❌ Aberto |
| 10 | O agente acredita em qualquer processo que atenda em `COMPARADOR_URL` | 08-04 C1 | `delegacao.go:49-80` | ❌ Aberto |

**Por que isso continua sendo o item que bloqueia.** Os itens 1 e 2 são
correções de dez linhas no cliente JS, e nenhum dos dois depende de decisão de
arquitetura:

```js
// integra-biometria.js:27-28 — hoje
if (h.get('bioPort')) {
  localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + h.get('bioPort'))
}
```

`bioPort=5000@evil.com` produz `http://localhost:5000@evil.com`, onde
`localhost:5000` vira *userinfo* e o host real é o do atacante. O fragmento
nunca chega ao servidor do integrador, então **não existe log em lugar nenhum**
que registre o desvio — e o que sai por ali é template biométrico em claro, dado
pessoal irrevogável sob a LGPD.

```js
// integra-biometria.js:229 — hoje
return { confere: !!r.confere, id: r.id || '' }
```

O campo `ignorados` atravessa três camadas de Go para chegar até aqui
(`worker.go:48-51` → `sdk.go:493-527` → `main.go:558-564`) e é jogado fora na
última linha. O efeito é o sistema afirmar "não é a pessoa" quando a verdade é
"o cadastro dessa pessoa não pôde nem ser comparado".

**Sugestão de correção** (a mesma das revisões anteriores, ainda válida):

```js
function portaValida(valor) {
  var n = Number(valor)
  return Number.isInteger(n) && n >= 1 && n <= 65535 ? n : null
}

// no bloco do fragmento:
var p = portaValida(h.get('bioPort'))
if (p) localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + p)

// em identificar():
return { confere: !!r.confere, id: r.id || '', ignorados: r.ignorados || [] }
```

---

## 🟡 Alertas (recomenda correção)

### A1. "Revogar sites autorizados" não revoga as origens de `CORS_ORIGEM` e `SISTEMA_URL` — e o log afirma que revogou

**Arquivos:** `origins.go:90-96, 140-147`, `main.go:719-731, 751`

```go
// origins.go:90-96
func (g *gerenciadorOrigens) permitida(origem string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, fixa := g.predefinidas[origem]      // <- CORS_ORIGEM / SISTEMA_URL
	_, aprovada := g.aprovadas[origem]     // <- aprovadas na bandeja
	return fixa || aprovada
}

// origins.go:140-147
func (g *gerenciadorOrigens) revogaTodas() error {
	g.mu.Lock()
	g.aprovadas = make(map[string]struct{})   // limpa só metade da autorização
	g.pendentes = make(map[string]time.Time)
	err := g.salvaBloqueado()
	g.mu.Unlock()
	return err
}
```

**Por que é um problema.** O menu diz **"Revogar sites autorizados"**
(`main.go:751`) e o log diz `origens autorizadas foram revogadas`
(`main.go:726`). Nenhuma das duas frases é verdadeira: `predefinidas` — as
origens vindas de `CORS_ORIGEM` e de `SISTEMA_URL`, carregadas em
`origins.go:41-45, 63-72` — não são tocadas, e `permitida()` continua devolvendo
`true` para elas.

Num servidor RDS instalado com `-CorsOrigem "https://sistema.exemplo.com"`
(exatamente o comando do README), **essa é a única origem que existe na
prática**. O usuário que clicar em "Revogar" para cortar o acesso — por
suspeita de comprometimento do site, por trocar de fornecedor, por
procedimento de incidente — recebe a confirmação no log e **não corta nada**.
O agente segue capturando e comparando para a mesma origem no próximo `fetch`,
sem nem o passo de reaprovação pela bandeja.

Vale notar a assimetria com `solicita()` (`origins.go:106-109`), que trata
`predefinidas` como "já autorizada, nem pede na bandeja". A entrada tem um
caminho automático; a saída, nenhum.

**Como corrigir.** Duas opções, ambas pequenas — a escolha é de produto:

1. **Revogar de verdade**, esvaziando também as predefinidas na sessão em curso
   (elas voltam no próximo *boot*, porque vêm do ambiente — o que já é um aviso
   útil de que a fonte é a configuração da máquina):

   ```go
   func (g *gerenciadorOrigens) revogaTodas() error {
   	g.mu.Lock()
   	g.aprovadas = make(map[string]struct{})
   	g.pendentes = make(map[string]time.Time)
   	g.predefinidas = make(map[string]struct{})
   	err := g.salvaBloqueado()
   	g.mu.Unlock()
   	return err
   }
   ```

2. **Ou dizer a verdade**, se as predefinidas são política de máquina e não devem
   cair no clique de um usuário: renomear o item para "Revogar sites aprovados
   nesta bandeja" e registrar quantas origens ficaram de fora —

   ```go
   logger.Printf("origens aprovadas na bandeja foram revogadas; %d origem(ns) de CORS_ORIGEM/SISTEMA_URL seguem autorizadas", len(g.predefinidas))
   ```

Falta ainda, nos dois casos, uma forma de **ver** o que está autorizado: hoje o
único jeito é abrir `origens-autorizadas.json` — que não lista as predefinidas.

---

### A2. A faixa `5000–5099` é o teto de sessões simultâneas do produto, e estourá-la é invisível

**Arquivos:** `main.go:583-589`, `supervisor.go:35-55`

```go
// main.go:583-589
for p := 5000; p <= 5099; p++ {
	l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p))
	if err == nil {
		return l, p, nil
	}
}
return nil, 0, errors.New("sem porta livre entre 5000 e 5099")
```

**Por que é um problema.** Num servidor Windows/RDS a pilha TCP é **uma só**,
compartilhada por todas as sessões — não existe um `127.0.0.1` por sessão RDP.
Logo, a faixa de 100 portas é o número máximo de agentes que podem coexistir no
servidor inteiro, e o produto é anunciado justamente para "servidores Windows
com múltiplas sessões RDP" (`README.md`, seção *Sobre o projeto*). Servidores
RDS com 100+ usuários simultâneos são comuns, e qualquer outro serviço do
servidor que ocupe uma porta nesse intervalo consome um lugar.

O modo de falha é o pior possível — silencioso e permanente:

```go
// main.go:882-886
listener, p, err := escolheListener()
if err != nil {
	registraErro("listener: %v", err)
	return 1                      // <- sai com código 1
}
```

O filho sai com `1`; o supervisor (`supervisor.go:43`) só encerra quando
`cmd.Wait() == nil`, então ele **reinicia para sempre**, com espera crescendo até
um minuto e voltando a 2 s a cada tentativa que passe de um minuto de vida. Não
há ícone na bandeja (o processo morre antes do `systray.Run`), não há caixa de
mensagem, e o log é o do agente que não subiu. Para o usuário da sessão 101, o
sintoma é "a biometria simplesmente não existe nesta máquina", e para o suporte
não há nada que aponte para a causa — o mesmo executável funciona nas outras 100
sessões.

Isso se soma ao 08-03 C1: lá o laço infinito nasce de uma `PORTA` fixa em
variável de máquina; aqui ele nasce da capacidade esgotada, sem configuração
errada nenhuma.

**Como corrigir.** Três medidas independentes:

1. **Diagnosticar antes de morrer** — a única mensagem que o operador tem
   chance de ler é a da bandeja:

   ```go
   listener, p, err := escolheListener()
   if err != nil {
   	registraErro("listener: %v", err)
   	avisaUsuario("Nenhuma porta livre entre 5000 e 5099 nesta máquina. " +
   		"O agente não pode iniciar nesta sessão.")
   	return 1
   }
   ```

2. **Não reiniciar em erro de configuração/capacidade.** Distinguir "o filho
   caiu" de "o filho recusou subir" — por exemplo, reservando o código de saída
   `3` para falha permanente e fazendo o supervisor sair sem retentar:

   ```go
   if err := cmd.Wait(); err == nil {
   	os.Exit(0)
   } else if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
   	os.Exit(3)   // configuração/capacidade: retentar não muda nada
   }
   ```

3. **Documentar o teto.** O `README.md` descreve a faixa `5000–5099` como
   detalhe de descoberta; ela é, na prática, o limite de sessões concorrentes.
   Uma faixa configurável (ou mais larga) resolve para instalações grandes — mas
   o cliente JS varre a mesma faixa (`integra-biometria.js:179-188`), então os
   dois lados precisam mudar juntos, e a varredura já custa caro hoje
   (07-30 A11).

---

### A3. `http.Server.ErrorLog` é nulo nos dois servidores — no comparador, um `panic` de handler desaparece por completo

**Arquivos:** `comparador.go:83-93`, `main.go:913-923`

```go
// comparador.go:83-93 — nenhum ErrorLog, e o Handler não tem recover
servidor := &http.Server{
	Handler:           exigeSegredo(segredo, mux),
	ReadHeaderTimeout: 5 * time.Second,
	...
}
```

**Por que é um problema.** Quando `ErrorLog` é `nil`, o `net/http` escreve com o
*logger* padrão do pacote `log`, que vai para `os.Stderr`. Os dois processos
onde isso acontece não têm `stderr` para lugar nenhum:

- o **agente** é compilado com `-H windowsgui` e é iniciado pela chave `Run` do
  `HKLM` — nasce sem console e com os *handles* padrão nulos (é exatamente o que
  `ligaConsole()` documenta em `autoteste.go:41-52`);
- o **comparador** roda como tarefa agendada sob `SYSTEM`, em sessão 0
  (`instalar-servidor.ps1:186-193`) — também sem console.

O agente ainda se salva em parte, porque o `middleware` tem `recover` próprio
(`main.go:207-212`) e manda o *stack* para `agente.log`. **O comparador não
tem.** O `Handler` dele é `exigeSegredo(segredo, mux)` e mais nada: um `panic`
dentro de `handleComparar`/`handleIdentificar` é recuperado pelo `net/http`, que
tenta registrar `http: panic serving ...` — e essa linha cai no `stderr`
inexistente. Do lado do agente, `chama()` (`delegacao.go:97-99`) vê a conexão
morrer e devolve `comparador inacessivel: ... EOF`; o operador vai procurar
rede, firewall e a tarefa agendada, enquanto a causa real — um defeito de código
no serviço central de comparação de toda a instituição — não deixou rastro em
arquivo nenhum.

Perdem-se pelo mesmo motivo, nos dois processos: erros de *handshake* TLS,
requisições malformadas e o aviso de `http.MaxBytesReader` estourado.

**Como corrigir.** Uma linha em cada servidor, reaproveitando o `logger` que já
grava em arquivo:

```go
servidor := &http.Server{
	Handler:  exigeSegredo(segredo, mux),
	ErrorLog: logger,     // agente.log / comparador.log
	...
}
```

E, no comparador, envolver o `mux` com o mesmo `recover` do modo normal, para
que o *panic* vire `500` com corpo JSON em vez de conexão cortada:

```go
func protegeComparador(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				registraErro("panic no comparador em %s: %v\n%s", r.URL.Path, v, debug.Stack())
				escreveErro(w, http.StatusInternalServerError, "erro interno")
			}
		}()
		proximo.ServeHTTP(w, r)
	})
}
```

---

### A4. Os IDs dos beneficiários vão em claro para o log — no projeto que teve o cuidado de não logar templates

**Arquivos:** `main.go:527`, `sdk.go:496, 517-518`, `main.go:559-564`

```go
// main.go:527
registraErro("identificacao: template ilegivel no candidato %q (posicao %d), ignorado", c.ID, i)

// sdk.go:517-518
registraErro("identificacao: candidato %q com template adulterado (%s), ignorado",
	candidato.ID, impressaoTemplate(limpo))

// main.go:562-563
registraErro("identificacao: nenhum candidato conferiu e %d cadastro(s) foram ignorados: %v",
	len(ignorados), ignorados)
```

**Por que é um problema.** O tratamento de dado biométrico neste repositório é
exemplar: `impressaoTemplate` (`sdk.go:431-434`) leva só tamanho e um `sha256`
curto, o `.gitignore` bloqueia `template*.txt` com o motivo escrito no arquivo,
e o README traz um `[!CAUTION]` sobre LGPD. Mas o `id` do candidato é o
identificador do beneficiário no sistema do integrador — o `String(item.id)` do
exemplo do próprio README — e ele é gravado **em claro**, repetidamente, em
`agente.log` e em `comparador.log`.

Duas agravantes específicas deste desenho:

1. **No comparador, isso é centralizado.** Todas as sessões RDP do servidor
   delegam para o mesmo processo, então `comparador.log` acumula a relação
   "beneficiário X teve tentativa de identificação biométrica em tal data e
   hora" de **toda a instituição**, num arquivo sob o perfil do `SYSTEM`. Isso
   é dado pessoal de tratamento biométrico mesmo sem template junto: registra
   quem foi submetido a verificação e quando.
2. **Não há retenção nem desligamento.** A rotação é por tamanho e só no *boot*
   (`log.go:21-25`, e o comparador nunca reinicia — 08-01 A2), a desinstalação
   preserva os dados do usuário de propósito
   (`instalar-servidor.ps1:117`), e não existe chave para desligar esse registro
   (07-30 A3, sobre as impressões de template, segue aberta e vale igual aqui).

Não é um erro de código — é uma decisão de log que ficou fora da mesma régua que
o resto do projeto aplicou ao template.

**Como corrigir.** Registrar a **quantidade** por padrão e os IDs só sob uma
chave explícita, do mesmo jeito que se faria com o template:

```go
registraErro("identificacao: %d cadastro(s) com template ilegivel entre %d candidatos",
	len(ignorados), len(body.Candidatos))
if os.Getenv("BIO_LOG_IDS") == "1" {
	registraErro("identificacao: ids ignorados: %v", ignorados)
}
```

Os `ignorados` já voltam na resposta HTTP (`main.go:565-568`), que é onde o
integrador precisa deles — e onde eles vivem sob o controle de acesso do sistema
web, não num `.log` de disco local.

---

### A5. `/api/status` e o monitor da bandeja chamam o SDK sem prazo próprio

**Arquivos:** `main.go:312-322`, `main.go:789-804`

```go
// main.go:316 — contexto da requisição, sem timeout
n, err := naThreadSDK(r.Context(), func() (uint32, error) { ... })

// main.go:797 — ctxApp, que só termina quando o agente encerra
conectado, err := naThreadSDK(ctx, func() (bool, error) { ... })
```

**Por que é um problema.** Todo o resto do código dá prazo explícito ao SDK:
captura usa `timeout + 25s` (`main.go:363`), comparação usa 45 s
(`main.go:432`), identificação usa 4 min (`main.go:537`). Estes dois não usam
nenhum.

`naThreadSDK` enfileira em `sdkTasks` (16 vagas) e uma única *goroutine* atende
tudo em série. Basta um *enroll* de 30 s à frente na fila — ou uma identificação
de 5.000 candidatos, que segura a thread por até 3 minutos (07-30 C4) — para que:

- **`/api/status` fique pendurado** o tempo que for, ocupando uma das 32 vagas de
  `limiteHTTP` e sem nenhum limite do lado do servidor (`WriteTimeout` é 5 min).
  É justamente o endpoint que o `COMO-USAR.md` indica como "a primeira coisa a
  olhar quando uma máquina confere e outra não" — e ele é o que menos responde
  quando a máquina está com problema.
- **`monitorLeitor` pare de atualizar o ícone.** A *goroutine* fica parada no
  `naThreadSDK`, o `ticker` de 15 s continua correndo sem ninguém lendo, e o
  ícone congela no último estado. Se o SDK travar de vez, a bandeja mostra
  "leitor conectado" para sempre.

**Como corrigir.** Prazo curto nos dois — status é uma pergunta barata:

```go
ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
defer cancel()
n, err := naThreadSDK(ctx, func() (uint32, error) { ... })
```

```go
ctxTick, cancelTick := context.WithTimeout(ctx, 10*time.Second)
conectado, err := naThreadSDK(ctxTick, func() (bool, error) { ... })
cancelTick()
```

O `naThreadSDK` já descarta tarefa cancelada antes de tocar no leitor
(`main.go:119-125`), então o custo de desistir é zero.

---

## 🟢 Sugestões (opcional)

### S1. Erro de origem inválida sai sem cabeçalho CORS e o JS o lê como "agente fora do ar"

`main.go:215-222`: quando `origemDoHeader` falha, o `403` é escrito **antes** de
`aplicaCORS`. O navegador bloqueia a resposta, o `fetch` rejeita com
`TypeError`, e `requisicao()` (`integra-biometria.js:100-104`) transforma isso
em `erroConexao` — "Não foi possível falar com o agente em ...". O usuário vê um
problema de conexão onde o problema é de autorização. O `403` de *origem não
autorizada* (`main.go:238`) não sofre disso, porque o CORS já foi aplicado na
linha anterior.

### S2. `handleHello` devolve `500` para condição de cliente

`main.go:269-277`: falha em `net.SplitHostPort(r.RemoteAddr)` ou no
`ParseUint` da porta vira `500 sem porta de origem`. Não é erro interno — é o
mesmo caso de "chamador não identificado" que a linha 281 trata como `503`.
Unificar evita que um `500` no painel do integrador pareça defeito do agente.

### S3. `contaDispositivos` e `listaDispositivos` fazem a mesma chamada

`sdk.go:217-235` e `sdk.go:237-275` chamam `NBioAPI_EnumerateDevice` com o mesmo
par de saídas; a segunda já tem a quantidade em mãos antes de ler a lista. Como
o próprio comentário de `monitorLeitor` explica que essa chamada vaza memória a
cada invocação (`main.go:790-793`), unificar as duas em uma só função — que
devolve `([]uint16, error)` e deixa a contagem para `len()` — remove uma fonte de
vazamento no autoteste, onde as duas são chamadas em sequência
(`autoteste.go:200-204`, `385-389`, `427-431`).

### S4. `--conferir-template` lê o arquivo sem `TrimSpace` e compara os bytes crus

`autoteste.go:409-415` faz `template := string(dados)` e entrega direto a
`comparaBrutos` (`autoteste.go:438`). É deliberado — o comando existe para
exercitar o comparador sem normalização. Mas um arquivo gravado por
`--salvar-template` em uma máquina e copiado para outra por e-mail, chat ou
área de transferência chega com `\r\n` no fim, e o comando falha por motivo que
nada tem a ver com a hipótese em teste. Basta imprimir o aviso — `forma()`
(`autoteste.go:128-137`) já calcula "espaço nas bordas: SIM" e o texto não é
usado para nada:

```go
if limpo := strings.TrimSpace(template); limpo != template {
	fmt.Printf("ATENCAO: o arquivo tem espaco nas bordas (%d -> %d bytes); "+
		"o teste segue com os bytes crus, como o SDK os receberia.\n", len(template), len(limpo))
}
```

### S5. Achados menores das revisões anteriores que continuam abertos e ainda cabem num commit

`main.go:252-260` (caso de contexto inalcançável no `select` de `limiteHTTP` —
08-05 S1), `comparador.go:44` (`porta` local sombreia a global — 08-05 S3),
`cert.go:190` (`ne.Temporary()` *deprecated* — 08-05 S5), `cert.go:257-261`
(conexões enfileiradas não são fechadas no `Close` — 08-05 S6).

---

## 📋 Resumo

- **Arquivos alterados neste PR**: 1 (`docs/revisao-sistema-2026-08-06.md`) —
  nenhuma linha de código
- **Arquivos analisados**: 21
- **Segurança**: 🚨 Risco — o vazamento de template biométrico (C1 item 1) está
  aberto há nove revisões, e hoje soma-se A1: o botão de revogação não revoga a
  origem que, na instalação recomendada pelo README, é a única que existe
- **Qualidade**: ⚠️ Atenção — `build` e `vet` limpos; os defeitos novos são de
  revogação, capacidade, observabilidade e privacidade de log
- **Risco de produção**: 🚨 Alto — fila única do comparador e 1:N como laço de
  1:1 seguem sem decisão; A2 acrescenta um teto de 100 sessões por servidor que
  falha em silêncio e A3 deixa um `panic` do comparador sem rastro
- **Testes**: ❌ Sem cobertura verificável — 34 testes que nunca rodaram em CI;
  camada HTTP, autorização de origem, `session.go` e `cert.go` sem teste algum

### Situação dos achados anteriores

| Revisão | Críticos | Situação hoje |
|---|---|---|
| 07-30 (em `main`) | C1–C4 | ❌ 4 de 4 abertos |
| 07-31 (PR #3) | C1–C2 | ❌ 2 de 2 abertos |
| 08-01 (PR #4) | C1–C3 | ❌ 3 de 3 abertos |
| 08-02 (PR #5) | — | sem crítico novo |
| 08-03 (PR #6) | C1 | ❌ aberto |
| 08-04 (PR #7) | C1 | ❌ aberto |
| 08-05 (PR #8) | — | sem crítico novo |
| 08-06 (este) | — | sem crítico novo |

Continua valendo o registro de 08-05: a **única** correção aplicada em nove
revisões foi de documentação (`d1c3846` acrescentou ao `README.md` a seção
"Dentro de um servidor RDP" e as variáveis `COMPARADOR_*`, fechando o 07-31 A5).

---

## ✅ Pontos positivos

- **A separação entre o que é dado e o que é operação está certa no caminho de
  1:N.** `sdk.go:493-527` trata checksum como defeito **daquele registro** e
  qualquer outro código do SDK como falha **da operação** — pula o primeiro,
  aborta na segunda. A distinção é a diferença entre "uma linha podre no banco
  bloqueia a identificação de todos os beneficiários" e "ela sai da lista e o
  resto funciona", e `TestIdentificaIgnoraCandidatoCorrompidoESegue` trava o
  comportamento. O único furo é o cliente JS jogar `ignorados` fora (C1 item 2);
  as três camadas de Go abaixo dele estão corretas.

- **`novaInputFIRNativa` tem o teste que o resto do arquivo não poderia ter.**
  `sdk_test.go:180-209` confere o *layout* da `NBioAPI_INPUT_FIR` campo a campo —
  `Form`, os dois ponteiros internos e o texto terminado em NUL — sem precisar
  da DLL. É a estrutura cujo erro derruba o processo dentro do SDK, sem `panic`
  recuperável, e é justamente a que dá para verificar sem hardware. Escolha de
  teste bem feita.

- **`clienteWorker` degrada em vez de amplificar.** O esfriamento após três
  falhas seguidas (`worker.go:162-166`, `worker.go:196-200`) reconhece o caso em que subir um processo
  novo por requisição troca um *crash* por uma tempestade de *spawns*, e o teste
  cobre inclusive que a recusa é **imediata**
  (`TestClienteWorkerEsfriaAposFalhasSeguidas`). O contraponto — erro devolvido
  pelo SDK zera o contador, porque o worker está saudável (`worker.go:300-302`) —
  também está testado. A crítica que resta é o escopo global desse esfriamento
  (08-04 A2), não o mecanismo.

- **O `.gitignore` explica por que ignora.** `template*.txt`, `cert.pem`,
  `key.pem`, `agente-*.json` e `origens-autorizadas.json` estão lá com o motivo
  escrito acima: *"Templates biométricos NUNCA entram no repositório: são dado
  pessoal irrevogável, e este repositório é público."* Um arquivo de ignore que
  diz o motivo sobrevive à próxima pessoa que quiser adicionar uma exceção.

- **`normalizaTemplate` continua sendo o melhor exemplo de validação com
  justificativa do repositório** (`sdk.go:407-421`): ASCII imprimível contínuo,
  com piso e teto, aplicado antes de o dado atravessar para a DLL — porque
  `VerifyMatch` confia nos campos de tamanho embutidos e lê fora da alocação, e
  uma violação de acesso lá dentro não vira `panic` recuperável. Doze casos de
  entrada perigosa cobertos em `sdk_test.go:37-64`.

- **`exigeSegredo` compara em tempo constante e tem os quatro testes que
  importam** (`comparador.go:103-115`, `comparador_test.go:22-73`): header
  ausente, prefixo errado, `Bearer ` vazio, `Basic`, e ainda o caso do prefixo
  correto com sobra. É o porteiro de um serviço que atende em nome da
  instituição inteira, e ele está testado como tal.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Nenhum defeito crítico novo apareceu hoje, e o código segue estável: `build` e
`vet` limpos, as decisões estruturais (isolamento da DLL em worker, comparação
fora da sessão RDP) continuam certas, e os trechos que este repositório
escreveu com cuidado — validação de template, esfriamento do worker, porteiro do
comparador — continuam bem feitos.

O que impede a aprovação é que o backlog crítico completou **nove dias** sem uma
linha de correção, e dois dos dez itens (`bioPort` e `ignorados`) são alterações
de dez linhas num arquivo JavaScript que evitam, respectivamente, vazamento de
template biométrico para um host arbitrário e veredito de identidade errado.

Dos alertas novos, **A1** é o de maior consequência prática: num servidor
instalado pelo comando do README, "Revogar sites autorizados" confirma no log
uma revogação que não acontece. É uma função de segurança que responde o
contrário do que faz — e a correção cabe em uma linha.
