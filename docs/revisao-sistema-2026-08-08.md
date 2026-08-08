# 🔍 Revisão do PR: Revisão técnica do sistema — 2026-08-08

Revisão do sistema como um todo. O ponto de partida de hoje é diferente do de
ontem e precisa ser dito antes de qualquer achado:

> **`main` não mudou desde a revisão de 2026-08-07.** O HEAD continua em
> `26c9379` (*serviço Windows e MSI para o comparador, e verificação 1:1 pela
> linha de comando*), e o [PR #10](https://github.com/Leo190198/api_remote_biometrics/pull/10)
> — que carrega aquela revisão — continua **aberto**. Nenhum dos quatro
> problemas críticos foi corrigido.

Por isso esta revisão faz duas coisas: **reconfere** cada item ainda aberto
contra o código de hoje (todos continuam válidos, com as mesmas linhas) e
**acrescenta seis achados novos**, concentrados no ciclo de vida do serviço e na
observabilidade em sessão 0 — a parte do sistema que ninguém consegue ver quando
falha, justamente porque roda sem console e sem sessão.

**Escopo analisado:** os 40 arquivos versionados — `main.go`, `sdk.go`,
`worker.go`, `comparador.go`, `delegacao.go`, `anuncio.go`, `servico.go`,
`autoteste.go`, `log.go`, `cert.go`, `origins.go`, `session.go`, `storage.go`,
`supervisor.go`, `versaodll.go`, os 7 `*_test.go`, `instalador/msi/*`,
`instalador/instalar-servidor.ps1`, `conferir-biometria.cmd`,
`integracao/integra-biometria.js`, `integracao/COMO-USAR.md`, `README.md`,
`.gitignore` e os dois documentos em `docs/`.

**Verificações executadas:** `GOOS=windows GOARCH=386 go build ./...` (OK) e
`GOOS=windows GOARCH=386 go vet ./...` (limpo, inclusive nos arquivos de teste).
Os testes não puderam ser **executados**: as *build tags* `windows && 386`
exigem um alvo real.

---

## 🔴 Problemas Críticos (bloqueia merge)

Os quatro críticos são os mesmos de 2026-08-07 e foram reconferidos linha a
linha no código de hoje. O detalhamento completo, com o trecho e a correção
proposta de cada um, está em
[`docs/revisao-sistema-2026-08-07.md`](revisao-sistema-2026-08-07.md); aqui fica
o resumo e a confirmação de que continuam abertos.

### C1. Parar o serviço apaga o anúncio, e o token novo derruba todos os agentes de pé — **ABERTO**

**Arquivos:** `comparador.go:94-99`, `anuncio.go:101-121`, `main.go:889`

`defer removeAnuncio()` (`comparador.go:99`) apaga exatamente o arquivo de que
`tokenDoComparador` (`anuncio.go:117`) depende para reaproveitar o segredo. Numa
instalação por MSI, sem `COMPARADOR_TOKEN` no ambiente, um `net stop` seguido de
`net start` gera um segredo novo — e os agentes já de pé guardaram o antigo em
`comparadorRemoto.token`, porque `configuraComparador()` roda uma vez só
(`main.go:889`) e nunca relê o anúncio. Toda comparação passa a responder **401**
em todas as sessões, até cada usuário fazer logoff.

O achado **N2**, abaixo, mostra um caminho novo em que o mesmo anúncio *não* é
apagado — e é o caminho de falha, não o de sucesso, confirmando o diagnóstico:
hoje é a parada limpa que quebra o sistema.

### C2. O endereço do comparador vem de variável de ambiente do usuário e não é validado como loopback — **ABERTO**

**Arquivos:** `anuncio.go:47-57`, `delegacao.go:74-85`

`diretorioCompartilhado` monta o caminho a partir de `os.Getenv("ProgramData")`,
que o processo do usuário controla, e `configuraComparador` aceita **qualquer**
host `http`/`https` — não há checagem de loopback. Um `comparador.json` plantado
redireciona a comparação: os templates saem em claro e o booleano que volta é o
veredito biométrico que `main.go:436-460` entrega ao sistema web.

### C3. `bioPort` continua concatenado sem validação no cliente JS — **ABERTO desde 2026-07-30**

**Arquivo:** `integracao/integra-biometria.js:26-29`, `144-151`, `173-175`

`'://localhost:' + h.get('bioPort')` com `bioPort=5000@evil.com` produz
`http://localhost:5000@evil.com` — `localhost:5000` vira *userinfo* e o host real
é `evil.com`. A partir daí o `X-Bio-Token` e os templates vão para lá, e o
endereço persiste em `localStorage`. **Aberto há 39 dias**, em duas revisões
consecutivas.

### C4. MSI e `instalar-servidor.ps1` disputam o mesmo comparador na mesma porta — **ABERTO**

**Arquivos:** `instalador/msi/AgenteBiometria.wxs:71-99`,
`instalador/instalar-servidor.ps1:167-193`

O PS1 registra uma **tarefa agendada** `AgenteBiometriaComparador` na 5150; o MSI
registra um **serviço Windows** de mesmo nome, na mesma porta. Nos servidores que
já rodam a v1.1.0 pelo PS1, o `net.Listen` do serviço falha
(`comparador.go:85-90`), `servico.go:41-45` reporta código diferente de zero e o
`Vital="yes"` (`AgenteBiometria.wxs:81`) derruba a instalação em rollback.

---

## 🟡 Alertas (recomenda correção)

Primeiro os **seis achados novos** desta revisão; depois a lista do que continua
aberto de 2026-08-07.

### N1. Nada do que o servidor HTTP registra sozinho chega ao `comparador.log`

**Arquivos:** `comparador.go:101-111`, `156-166`, `54-59`; `main.go:927-937`

Os dois `http.Server` são construídos sem `ErrorLog`:

```go
servidor := &http.Server{
    Handler:           exigeSegredo(segredo, mux),
    ReadHeaderTimeout: 5 * time.Second,
    // ...
    BaseContext: func(net.Listener) context.Context { return ctxApp },
}                                                  // comparador.go:101-111
```

**Por que é um problema.** Com `ErrorLog` nulo, o `net/http` escreve tudo o que
apura sozinho com `log.Printf`, ou seja, em `os.Stderr`. O binário é compilado
com `-H windowsgui` (`build-msi.cmd:35`) e, como serviço, nasce **sem console e
sem stderr válido** — o próprio `ligaConsole()` (`autoteste.go:41-51`) documenta
isso. Tudo o que o servidor registra some:

- **panics em handler.** No agente, `middleware` tem `recover()`
  (`main.go:207-212`). No comparador o mux é embrulhado apenas em
  `exigeSegredo`,
  que não recupera nada. Um panic em `/comparar` cai na recuperação interna do
  `net/http` — a conexão morre, o cliente vê `comparador inacessivel`
  (`delegacao.go:116`) e **o `comparador.log` não registra uma linha**. É a
  diferença entre "achamos o *stack trace*" e "não sabemos por que caiu".
- **erros de conexão, cabeçalho malformado, `MaxHeaderBytes` estourado.**

Há um caso irmão no mesmo arquivo, e esse deixa o serviço sem subir e sem dizer
por quê:

```go
if v := os.Getenv("COMPARADOR_PORTA"); v != "" {
    n, err := strconv.Atoi(v)
    if err != nil || n < 1 || n > 65535 {
        fmt.Fprintln(os.Stderr, "COMPARADOR_PORTA invalida:", v)  // some
        return 1                                                  // servico falha
    }
```

`iniciaLogEm` já rodou (linha 40), então havia onde escrever — mas este caminho
não usa `registraErro`. O administrador vê o serviço em falha no `services.msc`,
abre o `comparador.log` e não encontra nada. O caminho vizinho, do token
(linhas 45-51), faz certo: escreve nos dois lugares.

**Como corrigir.** Ligar o `ErrorLog` ao mesmo `logger` e fechar o caminho mudo:

```go
// comparador.go
servidor := &http.Server{
    Handler:  exigeSegredo(segredo, mux),
    ErrorLog: log.New(logger.Writer(), "http: ", log.Ldate|log.Ltime),
    // ...
}

// e no caminho da porta invalida:
registraErro("comparador: COMPARADOR_PORTA invalida: %q", v)
fmt.Fprintln(os.Stderr, "COMPARADOR_PORTA invalida:", v)
```

Vale o mesmo para o agente (`main.go:927`) e, junto, embrulhar o mux do
comparador num `recover()` como o do `middleware` — o comparador atende todas as
sessões do servidor, então é onde um panic custa mais.

---

### N2. O prazo de parada do serviço é menor que o trabalho que a parada faz — e desfaz o conserto do worker órfão

**Arquivos:** `servico.go:55-62`, `comparador.go:116-141`

```go
case svc.Stop, svc.Shutdown:
    estado <- svc.Status{State: svc.StopPending, WaitHint: 15000}   // 15 s
    cancelaApp()
    select {
    case <-saida:
    case <-time.After(12 * time.Second):                            // 12 s
        registraErro("servico: o comparador nao encerrou a tempo")
    }
    return false, 0
```

Do outro lado, a parada é **serial** e pode somar mais que isso:

```go
ctx, cancela := context.WithTimeout(context.Background(), 10*time.Second)
if err := servidor.Shutdown(ctx); err != nil { ... }        // ate 10 s  (:118)
// ...
ctxSaida, cancelaSaida := context.WithTimeout(context.Background(), 10*time.Second)
encerraSDK(ctxSaida)                                        // + ate 10 s (:139)
```

**Por que é um problema.** O `Shutdown` só retorna quando as requisições em voo
terminam, e uma `/identificar` legítima roda por até 3 minutos
(`worker.go:340-343`) — o `WriteTimeout: 5 * time.Minute` existe justamente para
não cortá-la. No pior caso a parada custa ~20 s, mas `Execute` desiste em 12 s,
retorna, `svc.Run` retorna, `executa` retorna e `main` chama `os.Exit(0)`
(`main.go:964`). Isso mata a goroutine de `rodaComparadorCom` no meio, e os dois
`defer` que faltavam rodar não rodam:

1. `removeAnuncio()` (`comparador.go:99`) — o anúncio fica para trás apontando
   para uma porta morta (é o A3, agravado);
2. o `encerraSDK` (`comparador.go:139-141`) é interrompido — e o **worker fica
   órfão segurando o executável**, que é exatamente a falha descrita no
   comentário das linhas 132-138: a atualização seguinte falha com "arquivo em
   uso" já com o comparador fora do ar, e cada reinício deixa mais um para trás.

Ou seja: sob carga, a parada pelo SCM desfaz o conserto que o commit
`26c9379` introduziu. E, como o `WaitHint` é de 15 s, o SCM também já considera o
serviço travado antes de a parada limpa ter chance de terminar.

**Como corrigir.** Dar ao SCM um prazo maior que a soma real e não desistir antes
dela — mantendo o `WaitHint` vivo enquanto se espera:

```go
case svc.Stop, svc.Shutdown:
    estado <- svc.Status{State: svc.StopPending, WaitHint: 30000}
    cancelaApp()
    prazo := time.NewTimer(25 * time.Second)   // > 10 s (HTTP) + 10 s (SDK)
    defer prazo.Stop()
    bate := time.NewTicker(5 * time.Second)    // renova o WaitHint
    defer bate.Stop()
    for {
        select {
        case <-saida:
            return false, 0
        case <-bate.C:
            estado <- svc.Status{State: svc.StopPending, WaitHint: 30000}
        case <-prazo.C:
            registraErro("servico: o comparador nao encerrou a tempo")
            return false, 0
        }
    }
```

E, independente disso, encurtar o `Shutdown` para o que a parada pode esperar
(`5 s`) — uma `/identificar` de 3 minutos não vai caber em prazo nenhum de
parada, e é melhor cortá-la explicitamente do que descobrir isso pelo `os.Exit`.

---

### N3. O `comparador.log` grava o identificador da pessoa que conferiu, numa pasta legível por `Users`

**Arquivos:** `main.go:565-566`, `comparador.go:40`, `anuncio.go:14-16`

```go
registraInfo("identificacao: %d candidatos, confere=%v id=%q ignorados=%d",
    len(validos), res.id != "", res.id, len(ignorados))     // main.go:565
```

**Por que é um problema.** Este `registraInfo` roda também **dentro do
comparador** — `handleIdentificar` é a mesma função registrada em
`comparador.go:78` —, e o log do comparador vive em
`C:\ProgramData\AgenteBiometria\comparador.log`, cuja ACL herdada dá leitura a
`Users` (é o que `anuncio.go:14-16` descreve e o que o MSI assume,
`AgenteBiometria.wxs:57-59`).

O alerta A4 da revisão anterior já apontava a exposição dos vereditos, mas
tratava do que `/comparar` grava: tamanho e `sha256` curto, um pseudônimo. Aqui é
outra coisa. `res.id` é o **identificador de negócio do beneficiário**, o mesmo
que o ERP mandou na lista de candidatos — vai em claro, ao lado do carimbo de
hora e do `confere`. Num servidor RDS com dezenas de sessões, qualquer usuário
logado lê, sem privilégio nenhum, **quem foi identificado, quando e se
conferiu**. Isso é dado de atendimento de terceiros; para uma operadora de saúde,
é registro de que uma pessoa esteve presente numa data.

O cuidado com o template está certo e é consistente em todo o código
(`impressaoTemplate`, `forma`, o `.gitignore`). O identificador escapou dessa
regra.

**Como corrigir.** Duas linhas de defesa, e vale ter as duas:

```go
// main.go — o id vira pseudonimo no log, como ja acontece com o template.
registraInfo("identificacao: %d candidatos, confere=%v id=[%s] ignorados=%d",
    len(validos), res.id != "", resumoID(res.id), len(ignorados))

func resumoID(id string) string {
    if id == "" {
        return "-"
    }
    soma := sha256.Sum256([]byte(id))
    return fmt.Sprintf("sha256:%x", soma[:6])
}
```

E ACL explícita na pasta pelo instalador: `SYSTEM` e `Administradores` com
controle total, `Users` **sem** leitura no `comparador.log` — mantendo apenas o
`comparador.json` legível, que é o único arquivo que os agentes precisam ler. A
mesma correção fecha o A4.

---

### N4. Os comandos de linha de comando não abrem o log, e o diagnóstico da delegação se perde exatamente quando é preciso

**Arquivos:** `autoteste.go:424-434`, `508-518`, `log.go:12`, `main.go:885`

```go
var logger = log.New(io.Discard, "", ...)   // log.go:12 — descarta ate iniciarem
```

`iniciaLog()` só é chamado no caminho do agente (`main.go:885`) e `iniciaLogEm`
no do comparador (`comparador.go:40`). `confereContra` (`autoteste.go:424`) e
`testeDelegacao` (`autoteste.go:508`) chamam `configuraComparador()` sem abrir
log nenhum — então todo `registraErro` de dentro dela vai para `io.Discard`:

```go
registraErro("COMPARADOR_URL/TOKEN incompletos e sem anuncio utilizavel (%v): a comparacao continua local", err)  // delegacao.go:62
registraErro("endereco do comparador invalido (%q): a comparacao continua local", base)                            // delegacao.go:77
registraErro("token do comparador ausente ou curto demais: a comparacao continua local")                           // delegacao.go:83
```

**Por que é um problema.** `--conferir-contra` é a ferramenta de campo: é o que
alguém roda no servidor quando a biometria não está funcionando. Quando o anúncio
está corrompido, com porta inválida ou token truncado, a tela mostra apenas
`comparacao: local, neste processo` — o **fato**, nunca o **motivo**. O operador
descobre que não está delegando, mas não descobre se é porque o serviço não
subiu, se o JSON está quebrado ou se o token veio curto, que são três
providências diferentes. É a informação mais cara de obter no campo, e ela existe
— só está sendo escrita num `io.Discard`.

**Como corrigir.** Uma linha em cada comando, e o motivo passa a aparecer na tela
junto do resto:

```go
func confereContra(caminho string) int {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()
    ligaConsole()
    iniciaLogArquivo("conferir.log")   // <-- registraErro passa a ter destino
    configuraComparador()
```

Melhor ainda: `configuraComparador` devolver o motivo em vez de só registrá-lo,
para os comandos de linha imprimirem na tela — quem está no servidor não vai
abrir dois arquivos de log para saber por que a delegação não ligou.

---

### N5. A mensagem de `--teste-delegacao` manda configurar o que o MSI deixou de usar

**Arquivos:** `autoteste.go:513-518`, `README.md:218-220`,
`instalador/instalar-servidor.ps1:217`

```go
configuraComparador()
if comparadorRemoto == nil {
    fmt.Println("Defina COMPARADOR_URL e COMPARADOR_TOKEN antes de rodar este teste.")
    fmt.Println("Exemplo: set COMPARADOR_URL=http://127.0.0.1:5150")
    return 1
}
```

**Por que é um problema.** O commit `26c9379` mudou a descoberta de variável de
ambiente para o anúncio em `ProgramData`, e por um bom motivo — variável de
máquina não alcança sessão já aberta. Numa instalação por MSI **não existem**
`COMPARADOR_URL` nem `COMPARADOR_TOKEN`, e não devem existir. Quando o teste dá
`comparadorRemoto == nil` ali, a causa quase certa é **o serviço não estar de
pé** (ou o anúncio estar corrompido, ou apagado pelo C1) — e a mensagem manda o
operador de campo pelo caminho errado, definindo variáveis à mão e mascarando o
problema real com uma configuração paralela.

A mesma defasagem está no README (a tabela de configuração apresenta as três
variáveis como a única forma de ligar a delegação, `README.md:218-220`) e no
`instalar-servidor.ps1:217` ("Os agentes só passam a delegar no próximo logon,
quando leem o ambiente da máquina"), que descreve o comportamento anterior.

**Como corrigir.**

```go
if comparadorRemoto == nil {
    fmt.Println("Nenhum comparador encontrado.")
    fmt.Println("  1. o servico AgenteBiometriaComparador esta rodando? (sc query AgenteBiometriaComparador)")
    fmt.Printf("  2. o anuncio existe em %s?\n", caminhoAnuncio())
    fmt.Println("  3. em bancada, da para forcar com COMPARADOR_URL e COMPARADOR_TOKEN.")
    return 1
}
```

---

### N6. A resposta do comparador é lida com o limite de 16 MB em todas as rotas

**Arquivo:** `delegacao.go:123`

```go
limitado := io.LimitReader(resp.Body, maxCorpoIdentificar)   // 16 MB, sempre
```

`maxCorpoIdentificar` foi dimensionado para o **pedido** de `/identificar`, com
até 5.000 candidatos. A resposta de `/comparar` é um único booleano — `true` ou
`false`, 5 bytes. O comentário de `main.go:37-41` explica que os limites foram
apertados justamente porque o pico do decodificador JSON pesa num binário 386 com
~2 GB de espaço de endereçamento; este caminho ficou de fora.

Sozinho é pequeno, porque hoje o comparador é confiável. Somado ao C2, deixa de
ser: um comparador falso pode devolver 16 MB por comparação e pressionar a
memória do agente. Um limite por rota resolve — 4 KB para `/comparar` e o
limite atual só para `/identificar`.

---

### Alertas de 2026-08-07 — todos reconferidos e **abertos**

| | Alerta | Onde | Estado |
|---|---|---|---|
| A1 | Comparador sem limite de concorrência; toda comparação serializada numa thread do SDK | `comparador.go:75-78`, `main.go:100-105` | aberto |
| A2 | `worker.log` do comparador vai para o perfil do SYSTEM | `worker.go:68`, `log.go:16-22` | aberto |
| A3 | Anúncio órfão nunca detectado; sem volta para comparação local | `anuncio.go:59-80` | aberto — **agravado pelo N2** |
| A4 | `comparador.log` expõe os vereditos de todos os usuários | `comparador.go:40` | aberto — **agravado pelo N3** |
| A5 | Cliente JS descarta `ignorados` | `integra-biometria.js:222-230` | aberto |
| A6 | MSI não fecha os agentes das sessões; arquivo em uso na atualização | `AgenteBiometria.wxs:31-35` | aberto |
| A7 | `VersionNT64 OR VersionNT >= 603` deixa passar Windows 7 x64 | `AgenteBiometria.wxs:39-41` | aberto |
| A8 | Sem CI; os 48 testes nunca rodam automaticamente | — | aberto |

---

## 🟢 Sugestões (opcional)

Continuam válidas as sete de 2026-08-07 — **S1** (README sem MSI, serviço nem
anúncio; confirmado hoje: `README.md` não menciona nenhum dos três), **S2**
(`conferir-biometria.cmd` não é instalado pelo MSI), **S3** (`build-msi.cmd:35`
sem `-trimpath`, que o próprio README prescreve na linha 94, e caminhos fixos
`D:\dotnet-sdk`/`D:\nuget-cache`), **S4** (`0o600`/`0o644` não têm efeito no
Windows), **S5** (`default` e `<-r.Context().Done()` no mesmo `select`,
`main.go:252-260`), **S6** (`comparador.log.1` não é removido pelo MSI) e **S7**
(`rundll32`/`certutil` resolvidos pelo `PATH`).

Acrescento duas:

- **S8.** `conferir-biometria.cmd:25-26` prefere o `AgenteBiometria.exe` que
  estiver **ao lado do script**, e só cai para `%ProgramFiles(x86)%` se não
  achar. Como o script é feito para ser copiado para onde o operador precisar,
  basta ele parar numa pasta gravável para um executável plantado ali ter
  preferência sobre o instalado. Inverter a ordem — instalado primeiro, vizinho
  como *fallback* — não custa nada e fecha a porta.
- **S9.** `servico.go` continua sem nenhum teste, e é testável sem Windows real:
  `Execute` conversa apenas por canais (`svc.ChangeRequest`, `svc.Status`), então
  dá para verificar que `Running` só é reportado depois de `pronto`, que um
  código de saída antes de `pronto` vira `Stopped`, e — se o N2 for corrigido —
  que a parada respeita o prazo. É o arquivo mais novo do sistema e o único do
  caminho crítico sem cobertura.

---

## 📋 Resumo

- **Arquivos alterados**: 1 neste PR (apenas documentação). Em `main`, **nenhum
  desde a revisão anterior** — o HEAD segue em `26c9379`. Foram revisados os 40
  arquivos versionados.
- **Segurança**: 🚨 Risco — C2 (delegação para host arbitrário) e C3 (`bioPort`
  não validado, **aberto há 39 dias**) permitem exfiltração de template e
  veredito biométrico forjado; N3 acrescenta exposição do identificador do
  beneficiário a qualquer usuário logado do servidor.
- **Qualidade**: ⚠️ Atenção — o código continua excepcionalmente bem
  fundamentado, mas há um padrão nos achados novos: o que o sistema sabe sobre as
  próprias falhas não chega a quem precisa (N1, N4, N5). Em sessão 0, sem console
  e sem stderr, isso é a diferença entre diagnosticar e adivinhar.
- **Risco de produção**: 🚨 Alto — C1 derruba a biometria de todas as sessões a
  cada parada do serviço; C4 faz a instalação falhar exatamente nos servidores
  que já rodam a v1.1.0; N2 mostra que, sob carga, a parada pelo SCM desfaz o
  conserto do worker órfão que este commit introduziu.
- **Testes**: ⚠️ Parcial — 48 funções `Test*` cobrem bem anúncio, delegação,
  normalização, worker e leitura de template; `servico.go` segue sem cobertura
  (S9), não há teste do ciclo publicar/parar/reiniciar (que é o C1) e nada roda
  automaticamente (A8). `go build` e `go vet` para `windows/386` passam limpos.

---

## ✅ Pontos positivos

- **O anúncio publicado só depois de a porta abrir** (`comparador.go:92-98`) e o
  `Running` reportado ao SCM só depois disso (`servico.go:38-46`). São duas
  ordens de operação que quase sempre saem erradas, e as duas estão certas aqui:
  o serviço nunca aparece de pé antes de atender.
- **`exigeSegredo` compara em tempo constante** (`comparador.go:156-166`) com a
  justificativa correta — segredo de vida longa contra atacante local — e o teste
  `TestComparadorRecusaCredencialComSobra` cobre o caso que um `strings.HasPrefix`
  ingênuo deixaria passar.
- **A resposta contraditória do comparador é recusada** (`delegacao.go:166-171`):
  `confere` e `id` precisam contar a mesma história, e adivinhar qual vale daria
  um veredito biométrico inventado. É o tipo de checagem que só existe quando
  alguém pensou no que acontece quando o outro lado está errado.
- **`normalizaTemplate` é rigoroso pelo motivo certo** (`sdk.go:396-421`): a
  violação de acesso dentro da DLL não vira `panic` do Go, então validar antes é
  a única defesa possível.
- **`identifica` não deixa um cadastro podre bloquear a lista** (`sdk.go:473-533`)
  e distingue checksum — que pula — de erro de SDK — que aborta. A lista de
  `ignorados` atravessa o worker e a delegação inteira para que "não é a pessoa"
  nunca se confunda com "o cadastro dela está corrompido".
- **Os códigos de saída do `--conferir-contra`** (`autoteste.go:411-423`) separam
  "não é a pessoa" (2) de "quebrou" (1) — providências opostas que um script
  confundiria em silêncio. O `conferir-biometria.cmd` traduz os três para texto.
- **Templates nunca aparecem em log.** `impressaoTemplate` (`sdk.go:427-434`) e
  `forma` (`autoteste.go:128-139`) foram desenhados para diagnosticar sem expor,
  e o `.gitignore` fecha o cerco com `*.hash`, `.env.*` e `template*.txt`. A
  observação do N3 é justamente que o identificador ficou de fora dessa regra
  bem estabelecida.
- **A remoção do `--salvar-template`** neste commit: era o único comando que
  gravava uma digital em arquivo desprotegido, e sair do binário é a decisão
  certa depois que o diagnóstico que o motivou fechou.
- **Os comentários explicam o porquê e a alternativa descartada**
  (`anuncio.go:5-23`, `comparador.go:5-16`, `delegacao.go:5-23`,
  `servico.go:5-13`). Vários achados desta revisão — C1, N2 — saíram de comparar
  o comentário com o comportamento, o que só é possível porque o comentário
  afirma algo verificável.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O desenho continua certo e nada do que foi apontado exige refazê-lo. O que mudou
desde ontem é o tempo: os quatro críticos seguem abertos, o C3 já dura 39 dias e
duas revisões, e o PR #10 permanece sem merge.

A ordem sugerida não é a da gravidade, e sim a do que destrava o resto:

1. **C1** — não depende de atacante nenhum. Um `net stop`/`net start` basta para
   toda a biometria do servidor responder 401. É também o de correção menor:
   separar o arquivo do segredo do arquivo de liveness.
2. **C4 + N2** — os dois estão no caminho de implantação da v1.2.0. O C4 faz o
   MSI falhar nas cinco máquinas onde ele precisa entrar; o N2 faz cada parada
   sob carga deixar um worker órfão que trava a atualização seguinte.
3. **C3** — o de maior impacto se explorado, e o mais barato de corrigir: uma
   função de validação e três chamadas trocadas no cliente JS.
4. **C2**, e com ele o N3 e o A4 — todos se resolvem com a mesma ACL explícita
   em `C:\ProgramData\AgenteBiometria` mais a checagem de loopback em
   `delegacao.go`.

Os achados N1, N4 e N5 são baratos e valem entrar junto: sem eles, qualquer
diagnóstico dos itens acima, feito no servidor, começa às cegas.
