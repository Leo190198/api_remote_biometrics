# 🔍 Revisão técnica do sistema — 2026-08-01

Revisão do branch `claude/peaceful-albattani-a1sz9n` (modo comparador em sessão 0
+ delegação da comparação) e do sistema como um todo, contra `origin/main`.

**Escopo analisado:** `main.go`, `comparador.go`, `delegacao.go`, `sdk.go`,
`worker.go`, `autoteste.go`, `versaodll.go`, `log.go`, `cert.go`, `origins.go`,
`session.go`, `storage.go`, `supervisor.go`, todos os `*_test.go`,
`instalador/instalar-servidor.ps1`, `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `README.md`, `docs/`.

**Verificações executadas:** `GOOS=windows GOARCH=386 go build ./...` (OK) e
`GOOS=windows GOARCH=386 go vet ./...` (limpo). Os testes não podem ser
executados aqui — as *build tags* exigem um alvo `windows/386` real.

**Arquivos alterados em relação a `main`:** 21 (3.751 inserções, 130 remoções).

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O comparador roda como SYSTEM e aceita bytes de qualquer usuário logado

**Arquivos:** `instalador/instalar-servidor.ps1:176-192`, `comparador.go:33-101`,
`sdk.go:407-421`

```powershell
# ps1:178 — token no ambiente da MÁQUINA
[Environment]::SetEnvironmentVariable('COMPARADOR_TOKEN', $ComparadorToken, 'Machine')
...
# ps1:187 — e o serviço que ele protege roda como SYSTEM
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
```

**Por que é um problema.** Os três elos existem ao mesmo tempo:

1. `COMPARADOR_TOKEN` vive no ambiente da máquina, ou seja, em
   `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, legível
   por qualquer usuário autenticado. O próprio instalador reconhece isso no
   comentário das linhas 176-177 — mas o trata só como "oráculo de comparação".
2. O serviço escuta em `127.0.0.1:5150`, alcançável por qualquer processo local,
   inclusive o de um usuário de sessão RDP sem privilégio nenhum.
3. O que chega ali é entregue à `NBioBSP.dll` — a mesma DLL que este repositório
   documenta como capaz de ler fora da alocação quando os campos de tamanho
   internos não batem com o conteúdo (`sdk.go:396-406`,
   `docs/diagnostico-verifymatch-rdp-2026-07-30.md`).

`normalizaTemplate` (`sdk.go:407-421`) valida apenas comprimento (32..64 KB) e
ASCII imprimível. Isso não restringe nenhum campo interno do FIR: um template
sintático válido e semanticamente adulterado passa a barreira e chega à DLL.

O resultado não é o oráculo descrito no comentário, e sim **entrada controlada
por usuário sem privilégio sendo processada por uma DLL de terceiros dentro de
um processo SYSTEM**. No mínimo, é uma negação de serviço trivial e permanente:
três quedas seguidas do worker acionam o resfriamento de `worker.go:162-165,198`
e a biometria do servidor inteiro para. No pior caso, a corrupção de memória
dentro da DLL vira execução de código com o token mais alto da máquina.

**Como corrigir.** A comparação não precisa de privilégio nenhum — precisa de
**sessão 0**, que é o que escapa do gancho da FabulaTech. `LocalService` também
roda em sessão 0 e mantém o desenho intacto:

```powershell
$principal = New-ScheduledTaskPrincipal -UserId 'NT AUTHORITY\LocalService' `
    -LogonType ServiceAccount -RunLevel Limited
```

Vale somar, na mesma correção:

- validar o cabeçalho do FIR antes de repassar à DLL (forma, versão e
  consistência dos campos de tamanho), em vez de só filtrar caracteres;
- publicar o resfriamento como métrica/log de alerta, para que o DoS apareça
  como incidente em vez de "a biometria está lenta hoje".

---

### C2. Uma identificação 1:N congela a comparação de **todas** as sessões do servidor

**Arquivos:** `comparador.go:64-93`, `main.go:100-138,469-486,537-552`,
`worker.go:339-346`

O comparador tem exatamente uma goroutine de SDK (`comparador.go:64`,
`go sdkThreadMain()`) e um único processo worker por trás dela. Toda operação —
de todas as sessões RDP do servidor — atravessa esse funil de um.

Uma identificação de 5.000 candidatos leva, pelo limite do próprio cliente,
`30s + 25ms × 5000 = 2min35s` (`worker.go:340-343`) e segura a thread do SDK o
tempo todo. Enquanto ela roda:

- todo `/comparar` que chegar fica parado em `naThreadSDK` (`main.go:127-137`);
- o contexto de 45s de `handleComparar` (`main.go:432`) estoura antes;
- o agente da sessão traduz isso em `502` (`main.go:449-452`) e o operador vê
  uma falha de verificação que não tem nada a ver com a digital dele.

Ou seja: **uma busca 1:N num guichê derruba a verificação 1:1 de todos os outros
guichês por dois a três minutos.** Dentro da sessão isso já era o alerta C4 da
revisão anterior; ao mover a comparação para um serviço compartilhado, o raio de
alcance saiu de um usuário e virou o servidor inteiro. `limiteIdentificar`
(`main.go:479`) limita quantas identificações coexistem, não o dano que cada uma
causa às comparações.

**Como corrigir.** A serialização em uma única thread existe por causa do
**leitor**, e o comparador não tem leitor: `NBioAPI_VerifyMatch` não precisa de
dispositivo aberto (é o que `--conferir-template` prova). Então no modo
comparador dá para manter um pool de workers e despachar por disponibilidade:

```go
// no modo comparador, N processos independentes; cada um com sua instância do SDK
const workersComparador = 4
```

Alternativa mínima, se o pool ficar para depois: fatiar a identificação em blocos
(por exemplo 250 candidatos) e devolver a thread do SDK entre os blocos, para que
uma comparação 1:1 nunca espere mais que um bloco.

---

### C3. `-ComparadorPorta` sozinho gera uma instalação que nunca compara

**Arquivo:** `instalador/instalar-servidor.ps1:15-18,179-180`

```powershell
[string]$ComparadorUrl = 'http://127.0.0.1:5150',
[int]$ComparadorPorta  = 5150
```

**Por que é um problema.** Os dois valores descrevem a mesma porta e não estão
amarrados. Um administrador que mude a porta por conflito local —
`-InstalarComparador -ComparadorPorta 6000` — recebe um serviço ouvindo em 6000
e todos os agentes configurados com `COMPARADOR_URL=http://127.0.0.1:5150`. A
instalação termina com mensagem de sucesso (linha 215 imprime `$ComparadorUrl`,
não a porta real) e o defeito só aparece no primeiro atendimento real, como
`comparador inacessivel: dial tcp 127.0.0.1:5150: connectex...`, num ambiente
onde a comparação local já não funciona. É perda de produção silenciosa.

**Como corrigir.** Derivar a URL da porta e só aceitar as duas juntas quando
forem coerentes:

```powershell
if (-not $PSBoundParameters.ContainsKey('ComparadorUrl')) {
    $ComparadorUrl = "http://127.0.0.1:$ComparadorPorta"
} elseif (([uri]$ComparadorUrl).Port -ne $ComparadorPorta) {
    throw "ComparadorUrl ($ComparadorUrl) aponta para uma porta diferente de ComparadorPorta ($ComparadorPorta)."
}
```

E, ao final, imprimir o endereço efetivamente registrado.

---

### C4. `bioPort` do fragmento não é validado — token e biometria vazam para um host arbitrário *(apontado na revisão anterior, segue aberto)*

**Arquivo:** `integracao/integra-biometria.js:25-34,173-175`

O arquivo não foi tocado neste branch. `'://localhost:' + h.get('bioPort')` com
`bioPort=5000@evil.com` produz `http://localhost:5000@evil.com`, onde
`localhost:5000` vira *userinfo* e o host real passa a ser o do atacante — com o
`X-Bio-Token` e os templates em claro no corpo. O endereço fica em
`localStorage` e o vazamento persiste. Correção detalhada em
[`revisao-sistema-2026-07-30.md`](revisao-sistema-2026-07-30.md) (C1).

**Agravante novo:** com a delegação ligada, `/api/status` passa a devolver a URL
interna do comparador (`main.go:329-333`), então o mesmo desvio entrega também a
topologia interna do servidor.

---

### C5. O cliente JS descarta `ignorados` *(apontado na revisão anterior, segue aberto)*

**Arquivo:** `integracao/integra-biometria.js:222-230`

```js
return { confere: !!r.confere, id: r.id || '' }   // ignorados some aqui
```

Toda a mecânica de distinguir "não é a pessoa" de "o cadastro dela não pôde ser
comparado" — `sdk.go:480-533`, `worker.go:48-51`, `main.go:558-568` — morre na
última linha do caminho, e o `COMO-USAR.md` ensina o `else` cego que transforma
isso em erro de atendimento. Este branch **estendeu** o esforço: `delegacao.go:130-154`
carrega `ignorados` de volta pela fronteira HTTP e há teste dedicado para isso
(`delegacao_test.go:83-98`). O campo agora atravessa três processos e uma rede
para ser jogado fora no navegador.

---

### C6. Identificação 1:N é um laço de comparações 1:1 — a falsa aceitação acumula *(apontado na revisão anterior, segue aberto)*

**Arquivo:** `sdk.go:480-533`

`identifica` chama `NBioAPI_VerifyMatch` uma vez por candidato e devolve o
primeiro que passar. Com a FAR de um par aplicada 5.000 vezes, a probabilidade
de aceitar alguém errado em uma busca cheia é ordens de grandeza maior que a de
uma verificação 1:1 — e é justamente a chamada que o `README.md` anuncia com
"até 5.000 candidatos". O SDK NBioBSP tem `NBioAPI_Identify`/motor de indexação
próprio para isso, com limiar ajustável.

---

## 🟡 Alertas (recomenda correção)

### A1. O comparador não reaproveita nada da proteção do modo normal

**Arquivo:** `comparador.go:83-93` versus `main.go:205-263`

```go
servidor := &http.Server{
    Handler: exigeSegredo(segredo, mux),   // e mais nada
```

O `middleware` do agente traz três coisas que o comparador perdeu:

- **`recover()` com log** (`main.go:207-212`). No comparador, um panic num
  handler é recuperado pelo `net/http`, que o escreve em `srv.ErrorLog` — nil,
  portanto o `log` padrão, portanto o `stderr` de uma tarefa SYSTEM: lugar
  nenhum. O cliente vê a conexão cair sem corpo e `comparador.log` não registra
  uma linha. Some justamente o rastro do defeito mais grave.
- **`limiteHTTP`** (`main.go:252-260`). No comparador não há teto de requisições
  simultâneas; cada uma decodifica até 16 MB (`main.go:41`) num processo de 32
  bits com ~2 GB de espaço de endereçamento.
- **`X-Content-Type-Options`** (`main.go:213`), barato de manter mesmo sem
  navegador na frente.

**Como corrigir.** Extrair o recover/limite para um `protecoesBasicas(next)`
usado pelos dois modos, e definir `servidor.ErrorLog = log.New(...)` apontando
para o mesmo arquivo de `iniciaLogArquivo`.

---

### A2. A rotação de log só acontece no boot — e o comparador nunca reinicia

**Arquivo:** `log.go:16-30`

```go
if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 { ... }
```

O teste de tamanho roda uma única vez, na abertura. No agente isso é tolerável:
ele sobe a cada logon. O comparador é uma tarefa `AtStartup` que fica de pé por
semanas e escreve uma linha por comparação (`main.go:430-431`) mais uma no
worker (`worker.go:151-152`). `comparador.log` e `worker.log` crescem sem teto e
sem rodízio, dentro do perfil do SYSTEM, onde ninguém olha.

**Como corrigir.** Verificar o tamanho na escrita (um `io.Writer` que roda o
arquivo ao cruzar o limite), ou reciclar por tempo num ticker.

---

### A3. O worker não registra os módulos carregados — justo o processo que compara

**Arquivos:** `worker.go:64-107`, `comparador.go:60-62`

O comparador lista os módulos do **seu** processo no boot, e é dessa lista que
sai a afirmação "sobe sem nenhum módulo da FabulaTech"
(`delegacao.go:14-18`). Só que quem chama `NBioAPI_VerifyMatch` não é ele: é o
processo worker (`worker.go:109-138`), que só registra a DLL escolhida. A
pergunta central de todo o diagnóstico — *o gancho está dentro do processo que
compara?* — não é respondida no único processo em que ela importa.

**Como corrigir.** Uma linha em `workerMain`, depois do `registraInfo` da DLL:

```go
for _, m := range modulosBiometricos() {
    registraInfo("worker: modulo %s", m)
}
```

---

### A4. `COMPARADOR_URL` aceita host remoto e HTTP puro

**Arquivo:** `delegacao.go:49-80`

```go
if err != nil || endereco.Host == "" || (endereco.Scheme != "http" && endereco.Scheme != "https") {
```

Qualquer host é aceito. O comentário de `comparador.go:71-74` justifica a
ausência de TLS com "o tráfego nunca sai da máquina" — mas nada no código impõe
isso do lado do cliente. Um `COMPARADOR_URL=http://outro-servidor:5150`, que a
documentação não proíbe em lugar nenhum, coloca **templates biométricos em claro
e o *bearer* fixo na rede**, com o agravante de que o serviço do outro lado não
sabe falar TLS.

**Como corrigir.** Exigir loopback, ou HTTPS quando não for:

```go
host := endereco.Hostname()
if host != "127.0.0.1" && host != "localhost" && host != "::1" && endereco.Scheme != "https" {
    registraErro("COMPARADOR_URL remota exige https: a comparacao continua local")
    return
}
```

---

### A5. Ninguém verifica se o comparador responde antes do primeiro atendimento

**Arquivos:** `delegacao.go:79`, `main.go:312-346`

`configuraComparador` registra "comparação delegada para X" sem trocar um pacote
com X. Se a tarefa agendada não subiu (porta ocupada — `comparador.go:76-81`
devolve 1, e o `-RestartCount 3` do instalador desiste em silêncio), o agente
sobe verde, o ícone da bandeja fica verde, `/api/status` responde `ok: true`
porque o **leitor** está lá — e a primeira digital do dia falha.

`/api/status` chega a expor `comparador` (`main.go:329-333`), mas só ecoa a
configuração; não diz se ela funciona. O `COMO-USAR.md` promete que esse campo é
"a primeira coisa a olhar quando uma máquina confere e outra não".

**Como corrigir.** Um `GET /status` no comparador durante o boot (falha só
registra, não impede o agente de subir) e um campo real em `/api/status`:

```go
info["comparador"] = map[string]any{"url": comparadorRemoto.base, "ok": vivo, "erro": motivo}
```

---

### A6. Reinstalar sem `-InstalarComparador` deixa comparador e agentes em versões diferentes

**Arquivo:** `instalador/instalar-servidor.ps1:167-193,204-210`

O bloco do comparador só roda com o *switch*. Uma atualização de rotina
(`instalar-servidor.ps1 -SistemaUrl ...`) instala a nova versão dos agentes,
mantém a tarefa apontando para o `$exe` da versão **antiga** e preserva o
diretório antigo — corretamente, porque o processo está vivo e entra em
`$ativos`. O servidor fica com agentes novos conversando com um comparador
velho, sem uma linha de aviso.

**Como corrigir.** Detectar a tarefa existente e re-registrá-la no novo `$exe`
(ou, no mínimo, avisar):

```powershell
if (-not $InstalarComparador -and (Get-ScheduledTask -TaskName $nomeTarefa -ErrorAction SilentlyContinue)) {
    Write-Warning "A tarefa $nomeTarefa continua na versao anterior. Reexecute com -InstalarComparador."
}
```

---

### A7. As duas pontas da delegação são testadas separadamente, contra fixtures escritas à mão

**Arquivos:** `delegacao_test.go`, `comparador_test.go`

`delegacao_test.go` testa o cliente contra um `httptest` que devolve JSON
literal; `comparador_test.go` testa só `exigeSegredo`. **Nada exercita o par
real** — o `mux` de `rodaComparador` respondendo ao `clienteComparador`. Um
`DisallowUnknownFields` (`main.go:392`) ou uma renomeação de campo em
`compararJSON` passa por `go build`, passa por `go vet`, passa pelos dois testes
e só quebra em produção, dentro do ambiente onde ninguém consegue depurar.

**Como corrigir.** Um teste de ida e volta com o mux verdadeiro e o SDK
substituído por `criaSDK` (o *hook* já existe em `main.go:64`):

```go
srv := httptest.NewServer(exigeSegredo(segredo, muxComparador()))
c := &clienteComparador{base: srv.URL, token: segredo, http: srv.Client()}
// compara/identifica atravessando o handler de verdade
```

Isso pede extrair o `mux` de `rodaComparador` para uma função — refatoração
pequena e que também resolve a S2 abaixo.

---

### A8. Sem CI, e todo o teste depende de um alvo `windows/386` *(apontado na revisão anterior, segue aberto)*

Não existe `.github/`. Todos os `*_test.go` carregam `//go:build windows && 386`,
então nenhum deles roda em máquina de desenvolvimento comum nem em nenhum
pipeline. As 33 funções de teste do repositório valem exatamente o que alguém se
lembrar de rodar à mão num Windows x86 — inclusive os 8 testes novos do modo
comparador.

Um *workflow* com `runs-on: windows-latest` e `GOARCH=386 go test ./...` resolve
— o `windows-latest` do GitHub Actions executa binários 32 bits.

---

### A9. O diagnóstico parcial continua descartado no handler *(apontado na revisão anterior, segue aberto)*

**Arquivo:** `main.go:553-557`

```go
if err != nil {
    registraErro("identificacao: %v", err)
    escreveErro(w, http.StatusBadGateway, err.Error())
    return          // res.ignorados vai para o lixo
}
```

`worker.go:130-134` preserva o apurado de propósito e `delegacao.go` carrega o
campo pela rede; o handler joga fora nos dois caminhos. Devolver `ignorados`
junto do erro custa duas linhas.

---

### A10. A troca de segurança do token da máquina não está no README

**Arquivos:** `README.md` (seção *Segurança*), `instalador/instalar-servidor.ps1:174-177`

O único lugar do repositório que diz que `COMPARADOR_TOKEN` é legível por
qualquer usuário logado é um comentário dentro do `.ps1`. A seção *Segurança* do
`README.md` lista sete garantias e não menciona o comparador. Quem decide
instalar lê o README.

---

## 🟢 Sugestões (opcional)

**S1.** `escreveErro(w, http.StatusBadGateway, err.Error())` (`main.go:451,555`)
repassa ao navegador o texto interno — inclusive `dial tcp 127.0.0.1:5150`.
Mensagem estável para o cliente, detalhe só no log.

**S2.** Os handlers compartilhados dependem de globais que o modo comparador não
inicializa (`origens`, `porta`, `token`). Hoje nenhum deles é tocado nas rotas
registradas em `comparador.go:66-69`, mas isso é uma propriedade que ninguém
verifica e que a próxima linha de código pode quebrar com um *nil panic* em
sessão 0. Extrair o mux e cobri-lo com o teste da A7 fecha a porta.

**S3.** `delegacao.go:104` usa `maxCorpoIdentificar` (16 MB) como teto de leitura
também para `/comparar`, cuja resposta é `true` ou `false`.

**S4.** Distinguir os dois 502 de `handleComparar`: "comparador inacessível"
(serviço caiu → `503`) e "comparador recusou" (o SDK rejeitou → `502`). O
`COMO-USAR.md` já ensina o operador a tratar os dois de formas diferentes.

**S5.** `--teste-delegacao` (`main.go:851-853`, `autoteste.go:458-519`) não está
na tabela de diagnóstico do `README.md` — só aparece na saída do instalador. É o
comando mais útil do branch para quem for validar a instalação.

**S6.** `comparadorStatus` exige credencial. Um *health check* de tarefa agendada
ou de monitoração precisaria carregar o segredo para perguntar "você está vivo?".
Vale abrir `/status` sem dado sensível, ou documentar que a monitoração precisa
do token.

---

## 📋 Resumo

- **Arquivos alterados**: 21 (3.751 inserções, 130 remoções)
- **Segurança**: 🚨 Risco — comparador SYSTEM com entrada de usuário sem
  privilégio (C1); token da máquina legível por todos; vazamento de token e
  template pelo cliente JS ainda aberto (C4)
- **Qualidade**: ⚠️ Atenção — o modo comparador reaproveita os handlers mas
  nenhuma das proteções que os cercavam (A1)
- **Risco de produção**: 🚨 Alto — C2 (1:N congela o servidor inteiro) e C3
  (instalação silenciosamente inconsistente) atingem justamente o ambiente que
  este branch existe para atender
- **Testes**: ⚠️ Parcial — 33 funções de teste, boa cobertura do cliente de
  delegação e do `exigeSegredo`, mas nenhuma roda em CI (A8) e a integração
  agente↔comparador não é exercitada (A7)

### Situação dos achados da revisão de 2026-07-30

| Achado | Situação |
|---|---|
| C1 `bioPort` não validado | 🔴 aberto (arquivo não tocado) |
| C2 `ignorados` descartado no JS | 🔴 aberto (e agora atravessa mais uma fronteira) |
| C3 1:N como laço de 1:1 | 🔴 aberto |
| C4 1:N congela capturas | 🔴 agravado — o escopo saiu da sessão e virou o servidor (C2 desta revisão) |
| A1 diagnóstico parcial descartado | 🟡 aberto (A9 desta revisão) |
| A8 sem CI | 🟡 aberto (A8 desta revisão) |

---

## ✅ Pontos positivos

**O diagnóstico que originou o branch é exemplar.** `docs/diagnostico-verifymatch-rdp-2026-07-30.md`
não conclui por eliminação: mede, compara endereços de módulo, mostra que
`NBioAPI_VerifyMatch` e `NBioAPI_Verify` caem no mesmo endereço e só então aponta
o culpado. `--salvar-template` / `--conferir-template` (`autoteste.go:373-449`)
separam extrator de comparador — a única pergunta que a máquina sozinha não
respondia. `--teste-delegacao` verifica que o gancho está presente **antes** de
concluir qualquer coisa (`autoteste.go:477-482`); sem essa checagem o teste
provaria só que dois processos sadios conversam por HTTP.

**A delegação é conservadora por padrão.** Sem `COMPARADOR_URL` nada muda
(`delegacao.go:20-23,49-53`), e configuração pela metade — token curto, URL
quebrada — desliga a delegação com log em vez de derrubar a estação
(`delegacao.go:56-66`). É a escolha certa: a estação de trabalho, que não tem
gancho nenhum, não pode virar refém de um serviço que não existe.

**O comparador não confia na própria resposta.** `delegacao.go:144-152` recusa um
resultado em que `confere` e `id` se contradizem, em vez de escolher um dos dois
— um veredito biométrico inventado seria pior que um erro. O
`TestDelegacaoRecusaRespostaContraditoria` cobre os quatro casos.

**Comparação em tempo constante com justificativa correta** (`comparador.go:103-118`),
e o teste `TestComparadorRecusaCredencialComSobra` cobre exatamente o erro que um
`strings.HasPrefix` cometeria.

**Templates nunca aparecem nos logs** — só tamanho e `sha256` curto
(`sdk.go:427-434`), e a mesma impressão é registrada em cada fronteira, o que
permite seguir os bytes por três processos e achar onde eles mudam.

**A cadeia de tempo-limite foi pensada de ponta a ponta**, com o comentário
dizendo por quê em cada elo: `WriteTimeout` maior que o contexto de
`/identificar` (`comparador.go:87-89`), contexto da captura maior que o prazo do
worker (`main.go:360-363`), cliente HTTP sem `Timeout` fixo porque quem manda é o
contexto (`delegacao.go:70-72`).

**O instalador valida antes de agir:** arquitetura do PE lida do cabeçalho
(`instalar-servidor.ps1:80-95`), Authenticode exigido fora de laboratório,
instalação por hash com sessões antigas preservadas, e `Remove-DiretorioInstalacao`
recusa apagar qualquer caminho fora da instalação.

**Os comentários explicam decisões, não mecânica.** `ligaConsole` conta que dois
diagnósticos se perderam numa janela de console que fechou; `monitorLeitor`
justifica os 15 segundos com a conta do vazamento do `EnumerateDevice`;
`modulosBiometricos` explica por que a primeira versão, que filtrava por nome,
era pior. Um mês depois isso ainda vai dizer alguma coisa.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O desenho está certo e o diagnóstico que levou a ele é sólido: tirar a comparação
da sessão RDP é a resposta correta ao gancho da FabulaTech, e a implementação
acerta os detalhes difíceis — tempo-limite, contradição de resposta, degradação
para local.

O que bloqueia é o entorno do serviço, não a ideia. C1 e C2 nascem do mesmo
ponto cego: o comparador deixou de ser um processo do usuário e virou
infraestrutura compartilhada do servidor, mas herdou o modelo de recursos
(privilégio máximo, uma thread, um worker) de quando atendia uma pessoa só. C3
falha na instalação, que é onde o erro custa mais caro. C4, C5 e C6 já estavam
abertos e o branch passou ao lado deles.

Nenhum dos três críticos novos exige repensar o desenho: `LocalService` no lugar
de SYSTEM, um pool de workers no modo comparador, e amarrar porta e URL no
instalador.
