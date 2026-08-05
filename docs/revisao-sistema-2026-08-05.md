# 🔍 Review do PR #NN: docs: revisão técnica do sistema — 2026-08-05

Oitava revisão técnica do sistema, sobre o commit `d1c3846` — o mesmo desde
2026-08-01. Nenhuma correção das sete revisões anteriores foi aplicada.

**Escopo analisado:** `main.go`, `sdk.go`, `worker.go`, `comparador.go`,
`delegacao.go`, `autoteste.go`, `versaodll.go`, `session.go`, `cert.go`,
`origins.go`, `storage.go`, `supervisor.go`, `log.go`, os cinco arquivos de
teste, `go.mod`/`go.sum`, `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`, `README.md`.

**Verificações executadas nesta revisão:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | ✅ limpo |
| `GOOS=windows GOARCH=386 go vet ./...` | ✅ limpo |
| `go test ./...` | ❌ **não executável aqui** — as *build tags* `windows && 386` excluem todos os arquivos deste alvo |

Os 34 testes do repositório continuam sem nunca ter rodado em lugar
verificável: não há `.github/`, não há CI, e o único alvo que compila os
arquivos é um Windows x86 real.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Os oito críticos das sete revisões anteriores seguem abertos, linha por linha

Esta revisão não encontrou defeito **novo** de severidade crítica. O que ela
encontra é que o backlog crítico não se moveu: cada trecho citado abaixo foi
reaberto e reconferido no código atual, e todos continuam idênticos.

| # | Achado | Origem | Trecho conferido hoje | Situação |
|---|---|---|---|---|
| 1 | `bioPort` do fragmento vira *userinfo* e desvia token + template para host arbitrário | 07-30 C1 | `integra-biometria.js:25-34` | ❌ Aberto |
| 2 | O cliente JS descarta `ignorados` — cadastro corrompido vira "digital não encontrada" | 07-30 C2 | `integra-biometria.js:222-230` | ❌ Aberto |
| 3 | 1:N é um laço de 1:1 — a falsa aceitação acumula com o nº de candidatos | 07-30 C3 | `sdk.go:480-533` | ❌ Aberto |
| 4 | Fila única do SDK: uma identificação congela toda captura da sessão | 07-30 C4 | `main.go:100-138`, `537` | ❌ Aberto |
| 5 | O comparador serializa o servidor inteiro numa fila só, sem `limiteHTTP` | 07-31 C1 / 08-01 C2 | `comparador.go:64-93` | ❌ Aberto |
| 6 | `/status` do comparador responde `ok: true` sem nunca tocar no SDK | 07-31 C2 | `comparador.go:120-133` | ❌ Aberto |
| 7 | O comparador roda como `SYSTEM` e recebe bytes de qualquer usuário logado | 08-01 C1 | `instalar-servidor.ps1:178-192` | ❌ Aberto |
| 8 | `-ComparadorPorta` sem `-ComparadorUrl` instala um par que nunca conversa | 08-01 C3 | `instalar-servidor.ps1:17-18, 167-193` | ❌ Aberto |
| 9 | `PORTA`/`MODO_COMPARADOR` como variável de máquina + laço infinito do supervisor | 08-03 C1 | `main.go:571-590`, `supervisor.go:29-56` | ❌ Aberto |
| 10 | O agente acredita em qualquer processo que atenda em `COMPARADOR_URL` | 08-04 C1 | `delegacao.go:49-80` | ❌ Aberto |

**Por que isso é um problema, e não só contabilidade.** Os itens 1 e 2 são
correções de dez linhas no cliente JS. O item 1 vaza **template biométrico em
claro** — dado pessoal irrevogável sob a LGPD — para um host escolhido por quem
montar o link, e o fragmento nunca chega ao servidor do integrador, então não
aparece em log nenhum. O item 2 faz o sistema dizer "não é a pessoa" quando a
verdade é "o cadastro dessa pessoa está corrompido" — a Go percorre três camadas
para carregar `ignorados` até o navegador (`worker.go:48-51`, `sdk.go:473-479`,
`main.go:558-568`) e a última linha joga fora.

**Sugestão de correção.** Os dois primeiros valem um commit hoje, isolado:

```js
function portaValida(valor) {
  var n = Number(valor)
  return Number.isInteger(n) && n >= 1 && n <= 65535 ? n : null
}

function defineEndereco(proto, porta) {
  var p = portaValida(porta)
  if (!p) return false
  localStorage.setItem(LS_ADDR, proto + '://localhost:' + p)
  return true
}

// e em identificar():
return { confere: !!r.confere, id: r.id || '', ignorados: r.ignorados || [] }
```

---

## 🟡 Alertas (recomenda correção)

### A1. O encerramento ordenado do SDK é sorteio — o contexto já chega esgotado

**Arquivo:** `main.go:936-941`

```go
systray.Run(onReady, cancelaApp)
cancelaApp()
ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = servidor.Shutdown(ctxShutdown)
encerraSDK(ctxShutdown)   // <- mesmo contexto que o Shutdown pode ter consumido
```

**Por que é um problema.** `servidor.Shutdown` espera as requisições em curso.
Uma captura de *enroll* segura o handler por até 55 s (`main.go:363`), muito
além dos 10 s do `ctxShutdown` — então `Shutdown` retorna com o contexto
**já expirado**, e `encerraSDK` recebe esse mesmo contexto morto.

Em `naThreadSDK` (`main.go:127-131`) isso cai num `select` onde as duas guardas
estão prontas ao mesmo tempo:

```go
select {
case sdkTasks <- tarefa:   // fila com 16 vagas: quase sempre pronta
case <-ctx.Done():         // contexto já expirado: sempre pronta
}
```

O Go escolhe **aleatoriamente** entre casos prontos. Em metade das saídas a
tarefa nem é enfileirada e `sdkInst.encerra()` nunca roda; na outra metade ela é
enfileirada, mas o segundo `select` devolve `ctx.Err()` na hora e `executa()`
segue para o `return`, sem esperar. Ou seja: **o worker praticamente nunca
recebe o `opEncerrar`**, e o `clienteWorker.derruba()` — que é quem mata o
processo filho e registra o *exit status* — não roda.

Na maioria das vezes o worker morre sozinho: o cano de stdin fecha quando o pai
sai e `entrada.Decode` devolve erro (`worker.go:91-93`). Mas se ele estiver
preso dentro da `NBioBSP.dll` — o cenário que o repositório inteiro documenta
(`docs/diagnostico-verifymatch-rdp-2026-07-30.md`) — ele só lê o stdin quando a
DLL devolver, e **enquanto isso segura o leitor**. Sem *Job Object* (07-30 A12)
e sem supervisor sobre o filho, o usuário reabre o agente e cai exatamente no
caso que o README avisa: "dois processos disputando o mesmo leitor derrubam a
captura".

**Como corrigir.** Dar prazo próprio ao SDK, depois do servidor:

```go
_ = servidor.Shutdown(ctxShutdown)
ctxSDK, cancelSDK := context.WithTimeout(context.Background(), 5*time.Second)
defer cancelSDK()
encerraSDK(ctxSDK)
```

E, em `naThreadSDK`, checar o contexto antes do `select` para tornar a
preferência explícita em vez de sorteada:

```go
if err := ctx.Err(); err != nil {
    return zero, err
}
```

---

### A2. Flag de diagnóstico incompleta ou desconhecida sobe o agente em silêncio

**Arquivo:** `main.go:834-869`

```go
if len(os.Args) > 2 && os.Args[1] == "--salvar-template" {
    return salvaTemplate(os.Args[2])
}
...
if os.Getenv("BIO_FILHO") != "1" {
    if !instanciaUnica() { return 0 }
    supervisor()          // <- destino de TUDO que não casou acima
    return 0
}
```

**Por que é um problema.** Não existe ramo de "argumento desconhecido". Quem
digitar `AgenteBiometria.exe --salvar-template` sem o caminho, ou
`--conferir-template` sem o arquivo, ou `--help`, ou `--Autoteste` com maiúscula,
não recebe erro nenhum: cai no `supervisor()` e **sobe o agente normal**, sem
janela, sem mensagem e com código de saída `0`.

Isso é pior do que parece pelo contexto em que os comandos são usados. O README
manda **fechar o agente** antes de rodar diagnóstico, justamente porque dois
processos disputando o leitor derrubam a captura. Um erro de digitação faz o
oposto do pedido: em vez de diagnosticar, **inicia** um agente — e o operador,
que não viu mensagem alguma, tenta o comando de novo com a sintaxe certa e agora
tem os dois brigando pelo leitor. O sintoma resultante ("a captura parou de
funcionar depois que rodei o diagnóstico") não aponta para a causa.

**Como corrigir.** Separar o despacho de comandos e recusar o desconhecido:

```go
if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "--") {
    switch os.Args[1] {
    case "--gerar-cert":
        if err := gerarCert(); err != nil {
            fmt.Fprintln(os.Stderr, "gerar-cert:", err)
            return 1
        }
        return 0
    case "--autoteste":    return rodaAutoteste()
    case "--comparador":   return rodaComparador()
    case "--teste-delegacao": return testeDelegacao()
    case "--salvar-template", "--conferir-template":
        if len(os.Args) < 3 {
            ligaConsole()
            fmt.Fprintf(os.Stderr, "%s exige o caminho de um arquivo.\n", os.Args[1])
            return 2
        }
        ...
    default:
        ligaConsole()
        fmt.Fprintf(os.Stderr, "comando desconhecido: %s\n", os.Args[1])
        return 2
    }
}
```

---

### A3. Os comandos de diagnóstico nunca ligam o logger — e a evidência do checksum se perde

**Arquivos:** `log.go:12`, `autoteste.go:141-149, 373-449`, `sdk.go:458-465`

```go
// log.go
var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)
```

`iniciaLog()`/`iniciaLogArquivo()` são chamados em três lugares: no agente
(`main.go:871`), no worker (`worker.go:68`) e no comparador (`comparador.go:34`).
**Nenhum** dos comandos de diagnóstico chama. `rodaAutoteste`, `salvaTemplate`,
`confereTemplate` e `testeDelegacao` rodam com `logger` apontando para
`io.Discard`.

**Por que é um problema.** O que se perde não é ruído — é a única linha que
esses comandos existem para produzir:

```go
// sdk.go:458-465, dentro de comparaBrutos
if uint32(r) == erroChecksum {
    registraErro("VerifyMatch recusou o par por checksum: cadastrado %s; lido %s",
        impressaoTemplate(limpoA), impressaoTemplate(limpoB))
}
```

`--conferir-template` chama `comparaBrutos` (`autoteste.go:438`) e existe
exatamente para investigar o `0x000B`. Quando o erro acontece, essa linha
**é descartada** e o operador vê só `FALHOU: NBioAPI_VerifyMatch: Template
adulterado: ...` — sem as duas impressões `sha256`, que são o dado que permite
comparar o template entre a máquina sadia e a doente. O procedimento descrito no
README ("leve um template de uma máquina que funciona para a que falha") depende
justamente dessas impressões para dar veredito.

O mesmo vale para `registraErro("fechar leitor apos captura: %v", err)`
(`sdk.go:283-285`), que é um sinal direto de redirecionamento RDP oscilando e
some em todo `--autoteste`.

**Como corrigir.** Uma linha em cada comando, antes do primeiro toque no SDK:

```go
func rodaAutoteste() (codigo int) {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()
    iniciaLogArquivo("autoteste-sdk.log")   // <- registraErro passa a ir para disco
    ligaConsole()
```

Ou, mais direto para quem lê o relatório: apontar o `logger` para o próprio
`autoteste.log` que o comando já abre, de modo que as duas fontes contem a mesma
história em ordem cronológica.

---

### A4. `agente-<sessão>.json` acumula um arquivo por reconexão RDP, cada um com um token

**Arquivo:** `main.go:607-633`; documentado em `integracao/COMO-USAR.md:81-83`

```go
sessao := os.Getenv("SESSIONNAME")
...
segura := strings.Map(func(r rune) rune { ... }, sessao)   // "RDP-Tcp#42" -> "RDP-Tcp42"
dados, err := json.Marshal(map[string]any{
    "porta": porta, "token": token, "pid": os.Getpid(), ...
})
return gravaArquivoAtomico(filepath.Join(dir, "agente-"+segura+".json"), ...)
```

**Por que é um problema.** Em RDS, `SESSIONNAME` é `RDP-Tcp#N`, com `N` mudando
a cada reconexão. Depois de `strings.Map`, cada reconexão gera **um arquivo
novo** — `agente-RDP-Tcp41.json`, `agente-RDP-Tcp42.json`, … — e nada apaga os
anteriores, nem na saída do agente, nem na desinstalação (o instalador preserva
os dados do usuário de propósito, `instalar-servidor.ps1:117`).

Duas consequências:

1. **Credencial morta acumulada em disco.** Cada arquivo carrega o token daquela
   execução. Os tokens ficam inúteis quando o processo morre, mas o rastro cresce
   sem limite no perfil do usuário, e `0o600` não protege nada no Windows
   (07-30 A7).
2. **A via de descoberta documentada aponta para arquivos mortos.** O
   `COMO-USAR.md:81-83` orienta o backend a ler
   `%LOCALAPPDATA%\BiometriaAgente\agente-<sessão>.json` e injetar porta e token
   via `Biometria.configurar(...)`. Um backend que faça `glob` de `agente-*.json`
   — o caminho natural, já que ele não sabe o `SESSIONNAME` da sessão do usuário
   — pega o arquivo errado e tenta falar com uma porta que outro processo
   qualquer pode ter reciclado. O JSON traz `pid`, mas a documentação não diz
   para conferi-lo.

**Como corrigir.** Gravar um nome estável e apagar na saída:

```go
caminho := filepath.Join(dir, "agente-"+segura+".json")
// ...
// e no encerramento de executa():
_ = os.Remove(caminhoConfig)
```

Se o nome por sessão for necessário, remover os `agente-*.json` cujo `pid` não
esteja mais vivo no *boot* do agente, e documentar em `COMO-USAR.md` que o
backend **precisa** validar `pid` antes de confiar no arquivo.

---

### A5. `COMO-USAR.md` atribui ao instalador um certificado que quem gera é o agente — e na loja errada

**Arquivo:** `integracao/COMO-USAR.md:90-93` × `cert.go:115-141`,
`instalar-servidor.ps1` (inteiro)

O documento diz:

> Para o HTTPS funcionar, o **instalador do servidor** gera um certificado
> autoassinado de `localhost` e o registra na loja de raízes confiáveis **da
> máquina** (veja `agente-go/instalador/instalar-servidor.ps1`).

Nenhuma das três afirmações confere:

1. O instalador **não tem uma linha sequer** sobre certificado — `grep -i cert`
   em `instalar-servidor.ps1` só encontra a palavra numa mensagem de
   desinstalação.
2. Quem gera é o **agente**, em todo *boot*, por usuário (`cert.go:92-113`,
   chamado por `carregaTLS` em `main.go:894`).
3. O registro é na loja do **usuário**, não da máquina:
   `certutil.exe -user -addstore -f Root` (`cert.go:116`).

**Por que é um problema.** O caminho citado no documento é o único lugar que um
administrador vai abrir quando o HTTPS não subir, e lá não há nada. Pior: a
diferença entre loja da máquina e loja do usuário muda quem precisa fazer o quê
— ninguém vai procurar uma raiz por perfil de usuário num servidor RDS lendo
esse texto. E a frase seguinte ("Sem o certificado, o agente segue só em HTTP")
esconde que a falha do `certutil` **desliga o HTTPS inteiro em silêncio**
(`cert.go:130-132`, 07-30 A10): o certificado existe, é válido, e mesmo assim
`carregaTLS` devolve erro e o agente cai para HTTP-só com uma linha no log.

Vale registrar que este arquivo foi "corrigido" no commit `d1c3846`
(*"corrige o COMO-USAR"*) e a contradição sobreviveu — sinal de que a revisão
foi por trecho, não pelo documento todo.

**Como corrigir.** Trocar o parágrafo por:

> O **próprio agente** gera, a cada início, um certificado autoassinado de
> `localhost` em `%LOCALAPPDATA%\BiometriaAgente\cert.pem` e o registra na loja
> de raízes confiáveis **do usuário** (`certutil -user -addstore Root`). Se esse
> registro falhar, o agente segue **só em HTTP** e grava o motivo em
> `agente.log` — não há aviso na bandeja.

---

## 🟢 Sugestões (opcional)

### S1. O caso de contexto no `select` de `limiteHTTP` é inalcançável

**Arquivo:** `main.go:252-260`

```go
select {
case limiteHTTP <- struct{}{}:
    defer func() { <-limiteHTTP }()
case <-r.Context().Done():   // <- morto
    return
default:
    escreveErro(w, http.StatusServiceUnavailable, "agente ocupado")
    return
}
```

Um `select` com `default` nunca bloqueia. Se o semáforo está cheio e o contexto
ainda vivo, o `default` dispara **na mesma hora** — não existe instante em que a
goroutine espere pelo contexto. O caso do meio só seria escolhido num sorteio
contra o `default` quando o contexto já estivesse encerrado, o que é
indistinguível de devolver 503. O efeito prático é que a 33ª requisição recebe
503 instantâneo, sem nenhuma janela de espera — provavelmente não era a
intenção, já que `handleIdentificar` (`main.go:476-486`) usa o padrão oposto,
com `time.NewTimer(2 * time.Second)`. Ou remova o caso morto, ou dê ao semáforo
geral a mesma espera curta do de identificação.

### S2. `--teste-delegacao` e `--comparador` não estão em nenhuma tabela do README

O `README.md:269-275` lista quatro comandos de diagnóstico e omite
`--teste-delegacao`, que o próprio instalador manda o operador rodar
(`instalar-servidor.ps1:218`). A tabela de configuração (`README.md:212-221`)
também não cita `MODO_COMPARADOR` nem o argumento `--comparador`, embora ambos
sejam o gatilho do modo (`main.go:860`).

### S3. `rodaComparador` sombreia o `porta` global

`comparador.go:44` declara `porta := portaComparadorPadrao`, sombreando o
`porta int` de pacote (`main.go:55`). Funciona porque os handlers compartilhados
não usam a global, mas `handleHello` usa — e ele não está registrado no modo
comparador *hoje*. Renomear para `portaComparador` remove uma armadilha para
quem adicionar uma rota amanhã.

### S4. O erro do comparador chega ao navegador com a topologia interna

`main.go:451` devolve `err.Error()` direto ao cliente web, o que produz coisas
como `comparador inacessivel: Post "http://127.0.0.1:5150/comparar": dial tcp
...`. Uma mensagem estável ("a comparação não está disponível") no corpo, com o
detalhe só em `agente.log`, evita expor porta e rota interna a qualquer página
autorizada.

### S5. `ne.Temporary()` está *deprecated* desde o Go 1.18

`cert.go:190`. O substituto para o laço de `Accept` é testar `errors.Is(err,
net.ErrClosed)` para sair e tratar o resto com o mesmo *backoff*.

### S6. `listenerMista.Close()` deixa as conexões enfileiradas abertas

`cert.go:257-261` fecha o listener bruto e sinaliza `done`, mas as conexões já
aceitas e paradas no canal `m.conns` (buffer de 32) nunca são fechadas. No fluxo
atual o processo termina logo em seguida e o SO recolhe tudo; se um dia o
listener for reiniciado sem sair do processo, isso vira vazamento de socket.

---

## 📋 Resumo

- **Arquivos alterados neste PR**: 1 (`docs/revisao-sistema-2026-08-05.md`) —
  nenhuma linha de código
- **Arquivos analisados**: 20
- **Segurança**: 🚨 Risco — não por achado novo, mas porque o vazamento de
  template biométrico (C1 item 1) está aberto há sete revisões
- **Qualidade**: ⚠️ Atenção — `build` e `vet` limpos; os defeitos novos são de
  encerramento, despacho de comandos e observabilidade do diagnóstico
- **Risco de produção**: 🚨 Alto — fila única do comparador e 1:N como laço de
  1:1 seguem sem decisão, e A1 pode deixar o leitor preso por um worker órfão
- **Testes**: ❌ Sem cobertura verificável — 34 testes que nunca rodaram em CI;
  camada HTTP, autorização de origem, `session.go` e `cert.go` sem teste algum

### Situação dos achados anteriores

| Revisão | Críticos | Situação hoje |
|---|---|---|
| 07-30 (em `main`) | C1–C4 | ❌ 4 de 4 abertos |
| 07-31 (PR #3) | C1–C2 | ❌ 2 de 2 abertos |
| 08-01 (PR #4) | C1–C3 (+3 repetidos) | ❌ 3 de 3 abertos |
| 08-02 (PR #5) | — | sem crítico novo |
| 08-03 (PR #6) | C1 | ❌ aberto |
| 08-04 (PR #7) | C1 | ❌ aberto |

Um achado mudou de estado desde 07-31: **A5 daquela revisão ("o modo comparador
não está documentado em lugar nenhum") está resolvido** — `d1c3846` acrescentou
ao `README.md` a seção "Dentro de um servidor RDP", o diagrama da sessão 0 e as
três variáveis `COMPARADOR_*` na tabela de configuração. É a única correção
aplicada em oito revisões, e é uma correção de documentação.

---

## ✅ Pontos positivos

- **A fronteira de autorização do `middleware` é sólida contra o ataque óbvio.**
  Tentei o desvio clássico — chegar a um handler de `/api/` com um
  `r.URL.Path` que não começa com `/api/`, via `%2e%2e`, barras duplicadas ou
  forma absoluta na *request line*. O `ServeMux` do Go 1.22+ casa sobre o caminho
  já decodificado e **redireciona** (301) qualquer caminho não canônico em vez de
  servi-lo, então o cliente é obrigado a repetir o pedido na forma limpa, que
  passa pela checagem de token em `main.go:242-251`. O `hello :=
  r.URL.Path == "/api/hello"` também é exato: `/api/hello/` não casa com nenhum
  padrão registrado e cai em 404 **com** exigência de token. Não há furo aqui.

- **O `naThreadSDK` descarta tarefa cancelada antes de acender o leitor**
  (`main.go:119-125`). O comentário explica o que se ganha — não acender o leitor
  para ninguém e não segurar a thread do SDK por mais 15 ou 30 segundos — e
  `TestNaThreadSDKIgnoraTarefaCancelada` trava esse comportamento. É a diferença
  entre um *timeout* que custa uma requisição e um que empurra todas as
  seguintes.

- **`normalizaTemplate` (`sdk.go:407-421`) valida pelo motivo certo, não por
  hábito.** ASCII imprimível contínuo, com piso e teto de tamanho, aplicado
  **antes** de o dado atravessar para a DLL, porque `VerifyMatch` confia nos
  campos de tamanho embutidos e lê fora da alocação — e uma violação de acesso lá
  dentro não vira `panic` recuperável. Chamar `normalizaTemplate` no handler
  *e* de novo no `comparaTextos`/`identifica` é redundância deliberada e correta:
  o comparador remoto não confia no que o agente já validou.

- **A separação extrator × comparador (`--salvar-template` /
  `--conferir-template`) é um bom desenho de diagnóstico.** O comentário em
  `autoteste.go:361-372` enuncia a hipótese e o que cada resultado
  descarta — "template da máquina sadia PASSA na doente → o comparador de lá está
  bom" — e `--conferir-template` roda **sem leitor**, o que permite exercitar só
  o comparador numa máquina sem hardware. É um instrumento que responde uma
  pergunta, não um teste que só diz "falhou".

- **`clienteComparador.identifica` recusa resposta contraditória**
  (`delegacao.go:150-152`): se `confere` e `id` discordarem, ele erra em vez de
  escolher um. Num sistema que emite veredito biométrico, adivinhar seria
  inventar identidade — e há teste cobrindo
  (`TestDelegacaoRecusaRespostaContraditoria`).

- **`exigeSegredo` tem o teste que importa.** `comparador_test.go:22-46` cobre
  header ausente, prefixo errado, `Bearer ` vazio e `Basic` — os quatro jeitos de
  um porteiro ingênuo deixar passar. Comparação em tempo constante com
  justificativa escrita (`comparador.go:103-107`).

- **O log nunca carrega template.** `impressaoTemplate` (`sdk.go:431-434`) leva
  tamanho e `sha256` curto, o suficiente para seguir o mesmo registro por todo o
  caminho e flagrar truncamento por coluna curta, e `.gitignore` bloqueia
  `template*.txt`, `cert.pem`, `key.pem` e `agente-*.json` com o motivo escrito
  no arquivo. O cuidado com dado biométrico é consistente **dentro** do Go; o
  furo (C1 item 1) está no cliente JS.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Nenhum defeito crítico novo apareceu hoje — o código está estável e as decisões
estruturais (isolamento da DLL em worker, comparação fora da sessão RDP) seguem
certas. O que impede a aprovação é que o backlog crítico completou oito dias sem
uma linha de correção, e dois dos dez itens (`bioPort` e `ignorados`) são
alterações de dez linhas no cliente JS que evitam, respectivamente, vazamento de
template biométrico e veredito de identidade errado.

Dos alertas novos, **A1** é o que mais merece atenção operacional: é uma
correção de três linhas que fecha o único caminho em que um worker preso na DLL
sobrevive ao agente e captura o leitor para a próxima sessão.
