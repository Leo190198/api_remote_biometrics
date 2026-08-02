# 🔍 Revisão técnica do sistema — 2026-08-02

Revisão do sistema no commit `d1c3846`, que é ao mesmo tempo `origin/main` e a
ponta do branch `claude/peaceful-albattani-x2qq16`.

**Escopo analisado:** `main.go`, `sdk.go`, `worker.go`, `comparador.go`,
`delegacao.go`, `autoteste.go`, `versaodll.go`, `cert.go`, `origins.go`,
`session.go`, `storage.go`, `supervisor.go`, `log.go`, os cinco `*_test.go`,
`instalador/instalar-servidor.ps1`, `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `embutir-icone.py`, `README.md` e `docs/`.

**Verificações executadas:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | OK |
| `GOOS=windows GOARCH=386 go vet ./...` | limpo |
| `go test ./...` | `no packages to test` — as *build tags* excluem tudo fora de `windows/386` |

---

## ⚠️ Antes de tudo: o código não mudou desde a última revisão

`git diff origin/main...HEAD` é **vazio**. O último commit de código é
`735402a` (delegação da comparação), de 2026-07-31, e o último commit de
qualquer natureza é `d1c3846`, de docs.

Isso muda o que esta revisão pode ser. Não há diferencial para revisar, então
ela faz duas coisas: **confere o estado dos achados anteriores** (todos abertos,
porque nenhuma linha mudou) e **traz o que as duas revisões anteriores não
tinham visto**. Os achados repetidos aparecem só na tabela de situação, sem
reproduzir o texto — cada um continua descrito no seu próprio documento.

Há hoje **três PRs de revisão abertos e nenhum mergeado**: #1 (2026-07-29),
#3 (2026-07-31) e #4 (2026-08-01). Os três só acrescentam documento em `docs/`;
nenhum toca no código que descrevem. Seis achados críticos permanecem abertos há
até cinco dias, e a fila de revisão passou a crescer mais rápido que a de
correção — este documento é o quarto.

---

## 🔴 Problemas Críticos (bloqueia merge)

Nenhum crítico **novo** foi encontrado nesta passagem. Os seis de 2026-08-01 e os
quatro de 2026-07-30 continuam válidos e sem correção — ver a tabela de situação
mais abaixo. O mais grave deles segue sendo o C1 de 2026-08-01: o comparador roda
como `SYSTEM` e recebe bytes de qualquer usuário logado no servidor.

---

## 🟡 Alertas (recomenda correção)

### A1. `maxCandidatos` e `maxCorpoIdentificar` não conversam — o teto real de 1:N é invisível

**Arquivos:** `main.go:41,45-46`, `main.go:492-504`

```go
maxCorpoIdentificar = 16 << 20   // 16 MB de corpo
maxTemplate         = 64 << 10   // 64 KB por template
maxCandidatos       = 5000       // candidatos por chamada
```

**Por que é um problema.** Os três limites foram escolhidos separadamente e o
produto deles não fecha. Cada candidato ocupa no JSON `{"id":"…","template":"…"}`
mais a vírgula: ~26 bytes de estrutura, mais o id, mais o template. Para caber
5.000 candidatos em 16 MB, o template médio precisa ficar **abaixo de ~3,3 KB**:

```
5000 × (32 + T) ≤ 16.777.216   ⇒   T ≤ 3.323 bytes
```

Nada no código garante isso — `maxTemplate` permite 64 KB, vinte vezes mais. Com
templates de dois dedos, ou com um FIR mais gordo de outra versão do SDK, o teto
real cai para algumas centenas de candidatos, muito antes dos 5.000 que o
`README.md:28,194` anuncia e que o `COMO-USAR.md` ensina a usar.

O que o integrador recebe quando cruza a linha é pior que o limite em si. A
ordem em `handleIdentificar` é: reserva a vaga, decodifica, valida. O
`http.MaxBytesReader` estoura dentro do `Decode`, e o erro sai pelo caminho
genérico de JSON malformado (`main.go:492-495`):

```
400 {"ok":false,"erro":"JSON invalido: http: request body too large"}
```

Quem lê isso procura defeito no JSON que montou, não no tamanho da lista. E o
sintoma é intermitente por natureza: aparece quando a lista de beneficiários
cresce, ou quando um cadastro com template maior entra nela.

**Como corrigir.** Separar os dois erros e dizer o limite real:

```go
if err := decodificaJSON(w, r, maxCorpoIdentificar, &body); err != nil {
    var grande *http.MaxBytesError
    if errors.As(err, &grande) {
        escreveErro(w, http.StatusRequestEntityTooLarge,
            "lista de candidatos grande demais; envie em lotes menores")
        return
    }
    escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
    return
}
```

E, no `README.md`, trocar "entre 1 e 5.000 candidatos" por um teto que também
mencione o tamanho do corpo — ou derivar `maxCandidatos` do tamanho observado
dos templates em produção.

---

### A2. `CORS_ORIGEM` inválida é descartada em silêncio

**Arquivos:** `origins.go:63-72,74-88`, `instalador/instalar-servidor.ps1:161-165`

```go
func (g *gerenciadorOrigens) carregaPredefinidas(lista string) {
	for _, item := range strings.FieldsFunc(lista, ...) {
		if strings.TrimSpace(item) == "*" {
			continue                                  // descarta calado
		}
		if origem, err := normalizaOrigem(item); err == nil {
			g.predefinidas[origem] = struct{}{}       // e o else também
		}
	}
}
```

**Por que é um problema.** `normalizaOrigem` recusa qualquer coisa sem esquema
`http`/`https` ou sem host. `-CorsOrigem "sistema.exemplo.com"` — sem esquema,
que é exatamente como alguém digita um endereço — é rejeitada e **some**. O
instalador aceita (só barra o `*`, na linha 162), grava a variável de máquina,
imprime sucesso; o agente sobe sem uma linha no log; e o site fica preso no 403
de origem não autorizada. O `carrega()` das origens salvas em disco tem o mesmo
comportamento: um arquivo corrompido volta como zero origens aprovadas, sem
aviso.

O caminho de recuperação existe — autorizar pela bandeja — mas o operador só
chega nele depois de investigar um CORS que ele acredita ter configurado.

**Como corrigir.** Registrar o que foi descartado, dos dois lados:

```go
if origem, err := normalizaOrigem(item); err == nil {
    g.predefinidas[origem] = struct{}{}
} else {
    registraErro("CORS_ORIGEM: %q ignorada (%v)", item, err)
}
```

`novoGerenciadorOrigens` já roda depois de `iniciaLog()` (`main.go:871-875`),
então o log está disponível. No instalador, validar com `[uri]` antes de gravar
e falhar cedo é melhor ainda: o erro aparece para quem pode corrigi-lo na hora.

---

### A3. A instância única protege o supervisor, não o agente

**Arquivos:** `supervisor.go:17-27,29-56`, `main.go:863-869`

```go
if os.Getenv("BIO_FILHO") != "1" {
	if !instanciaUnica() {          // só o supervisor confere o mutex
		return 0
	}
	supervisor()
	return 0
}
```

**Por que é um problema.** O mutex `Local\AgenteBiometriaGo` é criado pelo
processo supervisor e vive enquanto ele viver. O filho — que é quem abre a porta,
fala com o leitor e serve a API — nasce com `BIO_FILHO=1` e nunca passa por essa
checagem.

Junte com a ausência de *Job Object* (alerta A12 de 2026-07-30): se o supervisor
morre — `Stop-Process` manual, encerramento de sessão parcial, uma falha dele
mesmo — o filho continua vivo e o mutex é liberado com o processo morto. O
próximo gatilho de inicialização (a chave `Run` do HKLM em um novo logon, ou o
operador clicando no exe) encontra o mutex livre e sobe **um segundo agente na
mesma sessão**.

A partir daí:

- os dois disputam o leitor. `abre()` trata `0x0104` (dispositivo já aberto)
  como sucesso (`sdk.go:201-207`), então nenhum dos dois recusa a captura — os
  dois tentam, e o próprio `README.md:266-267` avisa que "dois processos
  disputando o mesmo leitor derrubam a captura";
- o segundo pega a porta seguinte (5001) e **sobrescreve**
  `agente-<sessão>.json` (`main.go:607-633`), então a opção 3 do `COMO-USAR.md`
  passa a entregar ao backend a porta e o token do agente errado;
- a auto-descoberta do JS varre em ordem crescente e acha o primeiro, que pode
  ser qualquer um dos dois.

**Como corrigir.** Mover a checagem para o processo que importa, deixando o
supervisor passar direto:

```go
if os.Getenv("BIO_FILHO") == "1" && !instanciaUnica() {
    registraErro("ja existe um agente nesta sessao; encerrando")
    return 0
}
```

Ou, melhor, resolver os dois de uma vez: colocar o filho num *Job Object* com
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, que faz o filho morrer junto com o
supervisor e elimina a janela em que o órfão existe.

---

### A4. O vazamento do `EnumerateDevice` foi reduzido, não limitado

**Arquivos:** `main.go:789-818`, `sdk.go:217-229`, `worker.go:184-239`

```go
// main.go:790-793 — o comentário reconhece o vazamento
// NBioAPI_EnumerateDevice devolve uma lista que o SDK aloca e nao expoe
// funcao para liberar. A cada 5 segundos eram ~17 mil chamadas por dia...
ticker := time.NewTicker(15 * time.Second)
```

**Por que é um problema.** A correção anterior atacou a taxa (17 mil → 5.760
chamadas por dia), e não o acúmulo. Quem hospeda o vazamento é o **processo
worker**, e o `clienteWorker` só o recria quando ele morre ou trava
(`worker.go:279-320`): num agente saudável, o mesmo worker atende do logon ao
logoff. Numa sessão RDP que fica semanas aberta — o caso normal de um servidor de
atendimento — são ~40 mil enumerações por semana num processo de 32 bits, com
espaço de endereçamento de ~2 GB e uma DLL de terceiros dentro.

O modo comparador não sofre disso (não roda `monitorLeitor`), o que confirma o
diagnóstico: é o pulso do ícone da bandeja que paga a conta.

**Como corrigir.** Limitar o tempo de vida do worker, em vez de a frequência da
chamada. Reciclá-lo quando estiver ocioso zera qualquer crescimento interno da
DLL sem custar uma captura:

```go
const idadeMaximaWorker = 6 * time.Hour

// em sobe(), antes de reaproveitar o processo existente
if c.cmd != nil && time.Since(c.subidoEm) > idadeMaximaWorker {
    c.derruba()   // o próximo pedido sobe um worker novo
}
```

Vale medir antes: `--autoteste` já dá o gancho para registrar o *working set* do
worker depois de N enumerações e transformar a suspeita em número.

---

## 🟢 Sugestões (opcional)

**S1.** `instalar-servidor.ps1:127` manda executar `Compilar-Go.ps1` quando o
exe não é encontrado, e esse arquivo não existe no repositório — nem o layout
`..\..\dist\` do segundo candidato (linha 123). O `README.md:86-101` documenta um
`go build` manual. A mensagem de erro aparece justamente para quem está
instalando pela primeira vez e manda essa pessoa procurar um arquivo inexistente:
ou o script entra no repositório, ou a mensagem passa a citar o comando do README.

**S2.** O JS fala com `localhost` (`integra-biometria.js:28,37,120,146`) e o
listener abre só em IPv4 (`main.go:577,584`, `net.Listen("tcp4", …)`). No Windows
`localhost` costuma resolver primeiro para `::1`, então cada uma das 100 portas
da descoberta paga uma tentativa perdida antes do *fallback*. O certificado já
cobre `127.0.0.1` (`cert.go:76`): usar o IP direto no cliente encurta a
descoberta sem mudar nada no agente.

**S3.** `escreveConfig` (`main.go:607-633`) grava o arquivo de descoberta na
subida e nunca o remove no encerramento (`main.go:936-946`). Com o agente
parado, o arquivo continua anunciando porta e token de um processo que não
existe, e a opção 3 do `COMO-USAR.md` entrega credencial morta ao backend, que
só descobre no 401. Apagar o arquivo no *shutdown* — ou gravar o `pid`, que já
está lá, e mandar quem lê conferir se o processo vive — resolve.

**S4.** Quando `novoSDK` falha (`worker.go:109-119`), a resposta é um erro válido
e o `clienteWorker` zera `falhasSeguidas` (`worker.go:301-302`), corretamente:
o worker está vivo. Só que o resfriamento nunca entra em cena para o caso "a DLL
existe mas não inicializa", e cada requisição repete `LoadLibrary` +
`NBioAPI_Init` do zero. Guardar o último erro de inicialização por alguns
segundos evita a repetição sem afetar a recuperação legítima.

**S5.** `COMO-USAR.md` diz que "o **instalador do servidor** gera um certificado
autoassinado de `localhost` e o registra na loja de raízes confiáveis da
**máquina**". Quem gera é o agente, na subida (`cert.go:125-141`), e o registro é
na loja do **usuário** (`certutil -user -addstore Root`, `cert.go:116`). A
diferença importa para quem audita o servidor: a instalação não mexe em loja
nenhuma, cada perfil ganha a sua raiz. (O item se soma ao A13 de 2026-07-30,
sobre as demais divergências do mesmo arquivo.)

**S6.** `enroll()` é descrito no `COMO-USAR.md` como "o agente lê o dedo 2x". O
caminho é uma única `NBioAPI_Capture` com `purpose=3` (`main.go:352-354`,
`sdk.go:275-327`); quantas leituras acontecem é decisão interna do SDK e da
janela dele. Como o texto orienta o operador que vai conduzir o cadastro, vale
descrever o que o código garante.

---

## 📋 Resumo

- **Arquivos alterados**: 0 no código (`git diff origin/main...HEAD` vazio);
  21 arquivos analisados, 1 documento acrescentado por esta revisão
- **Segurança**: 🚨 Risco — nenhum dos achados de segurança anteriores foi
  corrigido: comparador como `SYSTEM` com entrada de usuário sem privilégio,
  `COMPARADOR_TOKEN` no ambiente da máquina, e `bioPort` sem validação
  vazando token e template para host arbitrário
- **Qualidade**: ⚠️ Atenção — o código em si é cuidadoso e bem comentado; o que
  preocupa é a distância entre o que foi apontado e o que foi corrigido
- **Risco de produção**: 🚨 Alto — inalterado desde 2026-08-01 (1:N serializa o
  servidor inteiro; `-ComparadorPorta` gera instalação que nunca compara), com
  o A1 desta revisão somando um teto de 1:N que ninguém sabe onde está
- **Testes**: ⚠️ Parcial — 33 funções de teste, boa cobertura de normalização,
  worker e delegação; nenhuma roda fora de um `windows/386` real e continua sem
  CI (quinto dia)

### Situação dos achados anteriores

Nenhuma linha de código mudou desde 2026-08-01, então **todos** continuam
abertos. A tabela existe para que o estado seja explícito, e não para repetir a
descrição de cada um.

| Achado | Origem | Situação |
|---|---|---|
| Comparador roda como `SYSTEM` com entrada de usuário sem privilégio | 08-01 C1 | 🔴 aberto |
| 1:N congela a comparação de todas as sessões | 08-01 C2 / 07-30 C4 | 🔴 aberto |
| `-ComparadorPorta` sem `-ComparadorUrl` → instalação que nunca compara | 08-01 C3 | 🔴 aberto |
| `bioPort` do fragmento não validado (vazamento de token e template) | 08-01 C4 / 07-30 C1 | 🔴 aberto |
| Cliente JS descarta `ignorados` | 08-01 C5 / 07-30 C2 | 🔴 aberto |
| 1:N como laço de 1:1 (falsa aceitação acumulada) | 08-01 C6 / 07-30 C3 | 🔴 aberto |
| Comparador sem `recover`, sem `limiteHTTP`, sem `ErrorLog` | 08-01 A1 | 🟡 aberto |
| Rotação de log só no *boot* | 08-01 A2 / 07-30 A3 | 🟡 aberto |
| Worker não registra os módulos carregados | 08-01 A3 | 🟡 aberto |
| `COMPARADOR_URL` aceita host remoto e HTTP puro | 08-01 A4 | 🟡 aberto |
| Sem verificação de saúde do comparador | 08-01 A5 | 🟡 aberto |
| Reinstalação deixa comparador e agentes em versões diferentes | 08-01 A6 | 🟡 aberto |
| Integração agente↔comparador não é exercitada em teste | 08-01 A7 | 🟡 aberto |
| Sem CI | 08-01 A8 / 07-30 A8 | 🟡 aberto |
| `ignorados` descartado no handler quando há erro | 08-01 A9 / 07-30 A1 | 🟡 aberto |
| Troca de segurança do token da máquina fora do README | 08-01 A10 | 🟡 aberto |
| Demais alertas de 07-30 (A2, A4–A7, A9–A14) | 07-30 | 🟡 abertos |

---

## ✅ Pontos positivos

**O código não regrediu.** Cinco dias de revisão não encontraram um achado
crítico novo hoje, e `go build` e `go vet` continuam limpos no alvo real
(`windows/386`). Num projeto que conversa com uma DLL de terceiros por ponteiro
cru, isso não é pouco.

**O tratamento de memória nativa continua sendo o ponto mais forte.**
`novaInputFIRNativa` documenta o layout de 32 bits campo a campo antes de
montá-lo (`sdk.go:105-117`), `cStrLimitada` degrada para leitura byte a byte
quando um bloco cruza o fim da alocação do SDK (`sdk.go:556-570`), e a ordem dos
`defer` em `capturaTexto` garante que `NBioAPI_FreeTextFIR` roda **antes** do
`LocalFree` da struct que ele precisa ler (`sdk.go:306-326`). Nada disso aparece
sozinho — cada um é um erro que alguém já cometeu e corrigiu.

**A validação de entrada tem justificativa mensurável.** `normalizaTemplate`
(`sdk.go:396-421`) não é "sanitização por precaução": o comentário explica que a
DLL confia em campos de tamanho embutidos, que uma violação de acesso lá dentro
não vira `panic` recuperável, e que uma coluna curta no banco basta para
provocá-la. É a diferença entre um filtro que alguém vai relaxar na próxima
pressa e um que ninguém mexe.

**O isolamento em processo worker resolve o problema certo.** O comentário de
abertura de `worker.go:18-23` explica por que `recover()` não serve para uma
exceção dentro da DLL, e a consequência do desenho é exatamente a desejada: a
queda custa uma requisição, não o agente. O resfriamento após três falhas
seguidas (`worker.go:162-165,198-200`) evita trocar o *crash* por uma tempestade
de processos.

**Os testes cobrem os caminhos difíceis, não os fáceis.**
`TestClienteWorkerSobreviveAMorteDoWorker`, `TestClienteWorkerDesisteDeWorkerTravado`
e `TestClienteWorkerEsfriaAposFalhasSeguidas` usam o próprio binário de teste
como worker falso (`worker_test.go:18-24`) para reproduzir morte e travamento
reais. `TestDelegacaoRecusaRespostaContraditoria` cobre os quatro casos de
`confere` × `id`. `TestImpressaoTemplateNaoVazaOTemplate` transforma uma promessa
de privacidade em asserção.

**A documentação de diagnóstico é melhor que a da maioria dos projetos
comerciais.** `docs/diagnostico-verifymatch-rdp-2026-07-30.md` mede endereços de
módulo em vez de concluir por eliminação, e a tabela de `--salvar-template` /
`--conferir-template` do `README.md:280-283` explica a única pergunta que uma
máquina sozinha não responde: o defeito está no extrator ou no comparador.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O veredicto não é sobre este documento — é sobre o estado do sistema, e ele não
mudou. Os seis achados críticos seguem abertos, três deles envolvendo dado
biométrico saindo para onde não deveria (`bioPort` sem validação, token de
máquina legível por todos, comparador `SYSTEM` recebendo bytes de usuário sem
privilégio).

O que esta revisão acrescenta é modesto de propósito: quatro alertas que
sobreviveram a duas leituras anteriores (A1 a A4) e seis sugestões. O código está
bem escrito e bem justificado; o gargalo do projeto hoje não é a qualidade do que
se escreve, e sim o fato de que **o resultado das revisões não está virando
commit**.

A recomendação prática, antes de qualquer coisa nova: fechar os PRs #1, #3 e #4
num único documento consolidado, e abrir um PR de código que resolva os três
críticos de segurança. Nenhum deles é grande — `LocalService` no lugar de
`SYSTEM`, uma expressão regular em `bioPort`, e um campo a mais no retorno de
`Biometria.identificar`.
