# 🔍 Revisão técnica do sistema — Agente de Biometria Remota

Revisão completa do código-fonte na branch `claude/peaceful-albattani-bara2q`
(idêntica a `main` no commit `0fc4248`).

Escopo: 8 arquivos Go, `go.mod`/`go.sum`, `integracao/integra-biometria.js`,
`instalador/instalar-servidor.ps1`, `embutir-icone.py`, `README.md` e
`integracao/COMO-USAR.md`.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. `go.sum` desatualizado — o projeto não compila a partir de um clone limpo

**Arquivos:** `go.mod:5-12`, `go.sum:3-6`

`go.mod` declara `golang.org/x/sys v0.47.0` e `github.com/godbus/dbus/v5 v5.2.2`,
mas o `go.sum` versionado só tem hashes de `v0.15.0` e `v5.1.0`:

```
go.mod                                go.sum
golang.org/x/sys v0.47.0        →     golang.org/x/sys v0.15.0
github.com/godbus/dbus/v5 v5.2.2 →    github.com/godbus/dbus/v5 v5.1.0
```

Resultado verificado em clone limpo:

```
$ GOOS=windows GOARCH=386 go build .
missing go.sum entry for module providing package github.com/godbus/dbus/v5
missing go.sum entry for module providing package golang.org/x/sys/windows
```

**Por que é um problema:** ninguém consegue compilar o agente sem editar o
repositório. Como não há CI (ver A13), a regressão passou despercebida e chegou
à branch principal. O `README.md:63-76` documenta um `go build` que não funciona.

**Correção:**

```powershell
go mod tidy
git add go.mod go.sum
```

Confirmado nesta revisão: com o `go.sum` regenerado, `go build` e `go vet`
passam limpos para `windows/386`.

---

### C2. Zero testes automatizados — e as *build tags* tornam o teste impossível

**Arquivos:** todos os `.go` (linha 1 de cada: `//go:build windows && 386`)

Não existe nenhum arquivo `*_test.go` no repositório. Pior: **todo** o pacote
está atrás de `//go:build windows && 386`, então em qualquer máquina que não seja
Windows/386 o comando `go test ./...` compila e testa exatamente **zero** linhas —
sem falhar, o que dá uma falsa sensação de segurança.

**Por que é um problema:** trata-se de um componente de segurança que manipula
dados biométricos, faz *pinning* de origem CORS, compara token em tempo constante
e interpreta a tabela TCP do Windows. Várias funções são puras e triviais de
testar, mas hoje são inverificáveis:

| Função | Arquivo | O que um teste pegaria |
|---|---|---|
| `normalizaOrigem` / `origemDoHeader` | `origins.go:49`, `main.go:145` | *bypass* de allowlist de origem |
| `templateValido` | `sdk.go:218` | entrada malformada chegando à DLL |
| `pidNaTabelaTCP` | `session.go:31` | quebra do isolamento entre sessões RDP |
| `cStrLimitada` | `sdk.go:272` | leitura fora do buffer do SDK |
| backoff do supervisor | `supervisor.go:34` | o bug A6 abaixo |

**Correção sugerida:** mover a lógica pura para arquivos sem *build tag*
(ex.: `origem.go`, `template.go`, `tcptable.go`), mantendo só as chamadas
`syscall`/`windows` sob a tag, e adicionar os testes correspondentes:

```go
// origem_test.go  (sem //go:build)
func TestOrigemDoHeaderRejeitaSufixo(t *testing.T) {
    for _, mau := range []string{
        "https://bom.com.evil.com", "https://bom.com#x",
        "https://user@bom.com", "https://bom.com/path",
    } {
        if _, err := origemDoHeader(mau); err == nil {
            t.Errorf("aceitou origem invalida: %q", mau)
        }
    }
}
```

---

### C3. Identificação 1:N é um laço de comparações 1:1 — a falsa aceitação acumula

**Arquivos:** `sdk.go:242-270`, `main.go:388-433`, `main.go:39`

```go
// sdk.go:251-268
for _, candidato := range candidatos {
    ...
    r, _, _ := n.verify.Call(n.h, inLida, inCandidato, uintptr(unsafe.Pointer(&resultado)), 0)
    ...
    if resultado != 0 {
        return candidato.ID, nil   // devolve o PRIMEIRO que bater
    }
}
```

**Por que é um problema:** `NBioAPI_VerifyMatch` é calibrado para verificação
1:1. Se a taxa de falsa aceitação por comparação é `f`, a probabilidade de uma
falsa identificação na lista inteira é `1 - (1-f)^N`. Com `maxCandidatos = 5000`
(`main.go:39`) e um FAR típico de 1/100.000 no nível de segurança padrão, isso dá
**~5% de chance de identificar a pessoa errada em uma única leitura** — e o
endpoint devolve o *primeiro* candidato que passa do limiar, não o de maior
score. Num sistema de plano de saúde, isso é autorização de atendimento para o
beneficiário errado.

**Correção sugerida:**

1. Usar `NBioAPI_IdentifyMatch` (que é a função 1:N do SDK) em vez do laço, ou
   no mínimo elevar o nível de segurança (`NBioAPI_SetSecurityLevel`) para as
   chamadas 1:N;
2. Comparar **todos** os candidatos e devolver o de maior score + a margem para o
   segundo colocado, recusando quando os dois melhores estiverem empatados;
3. Reduzir `maxCandidatos` para uma ordem de grandeza compatível com o FAR
   escolhido, e documentar o cálculo no `README.md`.

---

### C4. `/api/identificar` não é cancelável e monopoliza a thread única do SDK

**Arquivos:** `main.go:53`, `main.go:81-106`, `main.go:420-426`, `sdk.go:242-270`

Todo o acesso ao SDK passa por **uma única goroutine** (`sdkThreadMain`,
`main.go:74-79`) com fila de 16. O `naThreadSDK` respeita o `context`:

```go
// main.go:100-105
select {
case res := <-resultado:
    return res.valor, res.err
case <-ctx.Done():
    return zero, ctx.Err()   // o HTTP volta...
}
```

...mas a tarefa **já enfileirada continua rodando até o fim** — e o laço de
`identifica` (`sdk.go:251`) não olha nenhum `context`. Um único POST com 5000
candidatos prende a thread do SDK por minutos.

**Por que é um problema:** enquanto isso, **tudo** para: `/api/status`,
`/api/capturar`, `/api/comparar` e o `monitorLeitor` (`main.go:627-652`, que
enfileira uma tarefa a cada 5 s sem timeout) ficam bloqueados. O ícone da bandeja
congela. Basta o cliente abortar e repetir para empilhar até 16 varreduras
completas na fila e deixar a estação biométrica inutilizável — sem nenhum
privilégio além de um token válido.

**Correção sugerida:** propagar o `context` até o laço e reverificá-lo antes de
cada chamada ao SDK:

```go
func (n *nbio) identifica(ctx context.Context, lida string, candidatos []candidatoJSON) (string, error) {
    ...
    for _, candidato := range candidatos {
        if err := ctx.Err(); err != nil {
            return "", err
        }
        ...
    }
}
```

---

### C5. Capturas órfãs: uma requisição já expirada ainda aciona o leitor

**Arquivos:** `main.go:81-106`, `main.go:302-332`

Mesma raiz do C4, mas com efeito visível para o usuário. Se a tarefa fica na fila
até o `context` da requisição estourar (`main.go:314`: `timeout + 5s`), o
`naThreadSDK` devolve `ctx.Err()` ao cliente — **e depois** a tarefa executa,
abrindo o dispositivo e esperando 15 s (captura) ou 30 s (enroll) por um dedo que
ninguém pediu, com o resultado sendo descartado.

**Por que é um problema:** o leitor fica ocupado e pisca pedindo o dedo fora de
contexto; a próxima captura legítima do usuário falha ou é atendida pela chamada
fantasma. Duas abas do sistema web abertas já reproduzem o cenário.

**Correção sugerida:** verificar o `context` dentro da tarefa, imediatamente
antes de chamar o SDK:

```go
tarefa := func() {
    res := resultadoSDK[T]{}
    defer func() { ...; resultado <- res }()
    if err := ctx.Err(); err != nil {   // <-- descarta trabalho já abandonado
        res.err = err
        return
    }
    res.valor, res.err = fn()
}
```

---

## 🟡 Alertas (recomenda correção)

### A6. O backoff do supervisor nunca estabiliza e o crash-loop é invisível

**Arquivo:** `supervisor.go:34-54`

```go
inicio := time.Now()
if err := cmd.Start(); err != nil { os.Exit(1) }
if cmd.Wait() == nil { os.Exit(0) }
time.Sleep(espera)                       // <-- o sleep entra na conta
if time.Since(inicio) > time.Minute {    // "o filho ficou de pé > 1 min"
    espera = 2 * time.Second
} else if espera < time.Minute { ... }
```

A intenção é "se o filho sobreviveu mais de um minuto, zera o backoff". Mas
`time.Since(inicio)` é medido **depois** do `Sleep`, então o próprio sleep
satisfaz a condição: com um filho que morre instantaneamente, `espera` sobe
2→4→…→60 s e, ao chegar em 60 s, o `Sleep(60s)` faz `time.Since(inicio) > 1min`
ser verdadeiro e o backoff volta para 2 s. O ciclo se repete para sempre.

Agrava: o `iniciaLog()` só roda no filho (`main.go:684`), então o processo pai
**não registra nada**. Um agente em crash-loop permanente é completamente
silencioso, sem contador de tentativas nem limite.

**Correção:**

```go
inicio := time.Now()
if err := cmd.Start(); err != nil { os.Exit(1) }
if cmd.Wait() == nil { os.Exit(0) }
duracao := time.Since(inicio)   // <-- mede só o tempo de vida do filho
time.Sleep(espera)
if duracao > time.Minute {
    espera = 2 * time.Second
} else if espera < time.Minute {
    espera = min(2*espera, time.Minute)
}
```

E inicializar o log também no pai, registrando cada reinício.

---

### A7. O token vai na linha de comando do `rundll32`

**Arquivos:** `main.go:499-509`, `main.go:612`

```go
// main.go:508
return fmt.Sprintf("%s%sbioPort=%d&bioToken=%s", base, separador, porta, token)
// main.go:612
exec.Command("rundll32", "url.dll,FileProtocolHandler", urlSistema()).Start()
```

**Por que é um problema:** a linha de comando de um processo é legível por
qualquer processo da máquina (`Get-CimInstance Win32_Process`), fica em logs de
EDR/auditoria e em relatórios de telemetria. O token de 256 bits que dá acesso
total ao leitor biométrico daquela sessão vaza aí. O fragmento também entra no
histórico do navegador antes do `history.replaceState`
(`integra-biometria.js:32`).

**Correção sugerida:** o `/api/hello` já resolve a descoberta sozinho — o
fragmento com token é redundante. Passe apenas a URL base e deixe o
`Biometria.garantirConexao()` obter o token:

```go
return base   // sem #bioPort/#bioToken
```

Se o parâmetro for mesmo necessário, use `ShellExecute` via `windows.ShellExecute`
em vez de montar uma linha de comando.

---

### A8. `BIO_TOKEN_QUERY=1` aceita o token na query string

**Arquivo:** `main.go:203-212`

```go
if tok == "" && os.Getenv("BIO_TOKEN_QUERY") == "1" {
    tok = r.URL.Query().Get("token")
}
```

**Por que é um problema:** token em URL vaza para o histórico do navegador, para
o header `Referer` e para qualquer log de acesso/proxy. A variável não está
documentada no `README.md:187-196`, então nem dá para saber que ela existe.

**Correção:** remover o caminho, ou — se for imprescindível para depuração —
documentar, exigir também `BIO_DEBUG`, e registrar um aviso no log a cada uso.

---

### A9. Certificado autoassinado adicionado à raiz confiável a cada boot e nunca removido

**Arquivos:** `cert.go:115-123`, `cert.go:125-141`, `instalador/instalar-servidor.ps1:67-77`

```go
cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
```

O `carregaTLS()` chama `instalaCertificadoUsuario` em **toda** inicialização
(`main.go:703`). O certificado é renovado a cada ~2 anos (`cert.go:47`,
`cert.go:72`), mas o anterior **nunca é apagado** da loja `Root` do usuário. A
desinstalação (`instalar-servidor.ps1:67-77`) também não remove nada — a mensagem
diz explicitamente "certificado de cada usuário foram preservados".

**Por que é um problema:** a loja de raízes confiáveis do usuário acumula
certificados órfãos indefinidamente, cujas chaves privadas continuam em disco.
Mesmo com escopo restrito (`DNSNames: localhost`, `KeyUsage: DigitalSignature`,
sem `IsCA`), é higiene de PKI ruim e um achado garantido em auditoria.

**Correção sugerida:** guardar o *thumbprint* instalado, remover o anterior com
`certutil -user -delstore Root <thumbprint>` na renovação, e remover o atual no
`-Desinstalar` do instalador.

---

### A10. Falha do `certutil` desativa o HTTPS silenciosamente

**Arquivos:** `cert.go:125-141`, `main.go:703-707`

```go
tlsCfg, err := carregaTLS()
if err != nil {
    registraErro("TLS desabilitado: %v", err)   // só no arquivo de log
}
usaTLS = tlsCfg != nil
```

Se `certutil` falhar (política de grupo, loja bloqueada, antivírus), o agente cai
para HTTP puro sem nenhum sinal para o usuário: a bandeja continua verde, o
tooltip não muda, e o `COMO-USAR.md:88-94` afirma que o fallback é normal.

**Por que é um problema:** o operador não tem como saber que a estação
biométrica degradou. Além disso, o `/api/status` expõe `"https": usaTLS`, mas
sem o motivo.

**Correção sugerida:** refletir a degradação no tooltip da bandeja e no
`/api/status` (`main.go:284-287`), incluindo o erro:

```go
info := map[string]any{"https": usaTLS, "httpsErro": erroTLS, ...}
```

---

### A11. `0o600` não protege nada no Windows; token e chave privada ficam em texto claro

**Arquivos:** `storage.go:29-73`, `main.go:471-497`, `cert.go:102-107`

`gravaArquivoAtomico(..., 0o600)` é usado para `key.pem`, `cert.pem`,
`origens-autorizadas.json` e `agente-<sessao>.json` (que contém o token em texto
claro, `main.go:489-492`). No Windows, o `os.FileMode` do Go só mapeia para o
atributo *read-only* — **nenhuma ACL é aplicada**. A proteção real vem apenas da
ACL herdada de `%LOCALAPPDATA%`.

Além disso, o `agente-<sessao>.json` **nunca é removido** no encerramento, então
porta e token obsoletos ficam em disco até o próximo logon.

**Correção sugerida:** aplicar uma DACL explícita (só o dono) via
`windows.SetNamedSecurityInfo` ou criando o arquivo com `SECURITY_ATTRIBUTES`
próprio; e apagar o arquivo de descoberta no shutdown (`main.go:744-753`).

---

### A12. `templateValido` não valida o alfabeto do template

**Arquivo:** `sdk.go:218-221`

```go
func templateValido(t string) bool {
    limpo := strings.TrimSpace(t)
    return len(limpo) >= 20 && len(t) <= maxTemplate && !strings.ContainsRune(t, '\x00')
}
```

Qualquer sequência de ≥20 bytes sem NUL passa e é copiada para memória nativa
(`sdk.go:198-216`) para ser interpretada por uma DLL de terceiros, 32 bits e de
código fechado. `maxTemplate = 1 << 20` (`main.go:38`) é ~3 ordens de grandeza
maior que um *text FIR* real do NITGEN.

**Por que é um problema:** é a única barreira entre a entrada da web e o parser
nativo. Qualquer bug de parsing na `NBioBSP.dll` fica diretamente alcançável.

**Correção sugerida:**

```go
var alfabetoFIR = regexp.MustCompile(`^[A-Za-z0-9+/=\r\n]+$`)

func templateValido(t string) bool {
    limpo := strings.TrimSpace(t)
    return len(limpo) >= 20 && len(limpo) <= maxTemplate && alfabetoFIR.MatchString(limpo)
}
```

com `maxTemplate` reduzido para o tamanho real observado (ex.: `64 << 10`).

---

### A13. Sem CI, sem `SECURITY.md`

Não existe diretório `.github/`. Nada roda `go build`, `go vet`, `gofmt -l`,
`go mod verify` ou lint de PowerShell. O C1 (repositório que não compila) é a
consequência direta disso.

**Correção sugerida:** um workflow mínimo já evitaria C1:

```yaml
name: ci
on: [push, pull_request]
jobs:
  build:
    runs-on: ubuntu-latest
    env: { GOOS: windows, GOARCH: "386" }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.26' }
      - run: go build ./...
      - run: go vet ./...
      - run: test -z "$(gofmt -l .)"
      - run: go test ./...
```

---

### A14. `COMO-USAR.md` contradiz o código em quatro pontos

**Arquivo:** `integracao/COMO-USAR.md`

| Linha | Documento afirma | Código faz |
|---|---|---|
| 114 | "**CORS:** já liberado pelo agente (`Access-Control-Allow-Origin: *`)" | `aplicaCORS` (`main.go:156-164`) ecoa a **origem exata** e o middleware responde **403** para origem não autorizada (`main.go:196-202`). O curinga é recusado de propósito (`origins.go:65-67`, `instalar-servidor.ps1:120`). |
| 64-67 | "você acha o agente certo **sem tray** e sem backend" | `/api/hello` devolve **202 `autorizacao: pendente`** para origem desconhecida (`main.go:254-260`); o usuário *precisa* aprovar na bandeja. O próprio `README.md:132` documenta isso corretamente. |
| 57-58 | "se o agente for antigo (sem `/identificar`), o script cai sozinho no loop de `comparar`" | `Biometria.identificar` (`integra-biometria.js:222-230`) **não tem fallback nenhum** — um 404 simplesmente propaga a exceção. |
| 89-92 | "o **instalador do servidor** gera um certificado autoassinado e o registra na loja de raízes confiáveis **da máquina**" | Quem gera e registra é o **próprio agente**, no boot, **por usuário** (`certutil -user`, `cert.go:116`). O `instalar-servidor.ps1` não toca em certificado algum. |

**Por que é um problema:** a linha 114 é a mais grave — diz ao integrador que a
API é aberta a qualquer origem quando o modelo de segurança é exatamente o
oposto. Quem seguir o documento vai construir a integração errada e culpar o
agente pelos 403.

**Correção:** reescrever essas quatro passagens alinhando com o `README.md`
(que está correto) e corrigir também o caminho `agente-go/instalador/...`
(linha 91), que não existe neste repositório.

---

### A15. `defer` com `unsafe.Pointer` convertido para `uintptr` fora da chamada

**Arquivo:** `sdk.go:178`

```go
defer n.freeText.Call(n.h, uintptr(unsafe.Pointer(&te)))
```

Os argumentos de um `defer` são avaliados **no momento do `defer`**, então o
`uintptr` é calculado e guardado como inteiro comum. Isso quebra a regra (4) de
`unsafe.Pointer`: a garantia de que o objeto permanece vivo e imóvel só vale
quando a conversão está **na própria lista de argumentos** da chamada. Entre o
registro e a execução do `defer`, `te` não tem mais nenhuma referência visível ao
GC (o `return cStrLimitada(te.TextFIR, ...)` na linha 179 é avaliado antes).

`go vet` não detecta esse caso — o *check* `unsafeptr` cobre a direção oposta.

**Correção:**

```go
defer func() { n.freeText.Call(n.h, uintptr(unsafe.Pointer(&te))) }()
```

(o `defer n.freeFIR.Call(n.h, hFIR)` da linha 171 está correto: `hFIR` é um
handle nativo, não um ponteiro Go.)

---

### A16. Descoberta no JS varre 100 portas × 2 protocolos em série

**Arquivo:** `integracao/integra-biometria.js:114-138`, `179-189`

```js
for (var p = inicio; p <= fim; p++) {
  var achado = await hello(p)   // hello() tenta 2 protocolos, 700 ms cada
```

No pior caso (firewall que descarta pacotes em vez de recusar), são
`100 × 2 × 700 ms ≈ 98 s` até `garantirConexao()` desistir — com a UI travada
esperando.

**Correção sugerida:** tentar primeiro a porta já conhecida em `localStorage`, e
depois varrer em lotes paralelos:

```js
for (var base = inicio; base <= fim; base += 10) {
  var lote = []
  for (var p = base; p < Math.min(base + 10, fim + 1); p++) lote.push(hello(p))
  var achados = (await Promise.all(lote)).filter(Boolean)
  if (achados.length) { ... }
}
```

---

### A17. Erros internos e caminho da DLL devolvidos ao cliente

**Arquivos:** `main.go:284-299`, `main.go:328`, `main.go:377`, `main.go:429`

`/api/status` devolve `"dll": dllOuNada()` — o caminho completo no disco. As
rotas de captura/comparação devolvem `err.Error()` cru, o que inclui mensagens
como `nao carregou C:\Program Files (x86)\Clinic Solution Plano de Saúde\NBioBSP.dll`
(`sdk.go:89`).

**Por que é um problema:** revela estrutura de diretórios e software instalado
para qualquer origem autorizada. Nomes de fornecedor em caminho de disco também
identificam o cliente final.

**Correção sugerida:** devolver mensagem genérica + um identificador de
correlação, mantendo o detalhe só no log:

```go
id := correlacao()
registraErro("[%s] captura: %v", id, err)
escreveErro(w, http.StatusUnprocessableEntity, "falha na captura (ref "+id+")")
```

---

### A18. Log sem rotação durante a execução

**Arquivo:** `log.go:14-28`

A verificação de 5 MiB e o *rename* para `.1` acontecem **apenas** dentro de
`iniciaLog()`, chamado uma vez no boot (`main.go:684`). Um agente que fica meses
no ar em uma sessão RDP faz `agente.log` crescer sem limite.

**Correção sugerida:** checar o tamanho dentro de `registraErro` (com um
intervalo mínimo) ou usar um `io.Writer` com rotação.

---

### A19. Listener misto: conexões pendentes vazam no shutdown; API depreciada

**Arquivo:** `cert.go:185-213`, `cert.go:242-261`

1. `Close()` (`cert.go:257-261`) fecha o listener bruto e sinaliza `done`, mas
   **não drena `m.conns`** — conexões já "espiadas" e bufferizadas no canal ficam
   abertas até o processo morrer.
2. `ne.Temporary()` (`cert.go:190`) está depreciado desde o Go 1.18 e o retorno
   não é confiável.
3. `espia` (`cert.go:215`) segura um dos 64 slots de `m.sem` por até 10 s por
   conexão sem enviar byte algum — um cliente lento consome a capacidade.

**Correção:**

```go
func (m *listenerMista) Close() error {
    err := m.bruto.Close()
    m.falha(net.ErrClosed)
    for {
        select {
        case c := <-m.conns:
            _ = c.Close()
        default:
            return err
        }
    }
}
```

e trocar `ne.Temporary()` por `errors.Is(err, syscall.ECONNABORTED)` /
`net.Error.Timeout()`.

---

## 🟢 Sugestões (opcional)

- **S20 — Item pendente na bandeja não expira junto com o registro.**
  `origins.go:101-105` descarta pendências após 10 min, mas o item do submenu
  criado em `main.go:532` só some quando clicado. O estado
  `pai.Enable()`/`Disable()` pode ficar dessincronizado do conjunto real.

- **S21 — Envelope de resposta inconsistente.** `/captura/Capturar` devolve uma
  *string* JSON crua (`main.go:331`), `/captura` devolve um booleano cru
  (`main.go:380`), mas `/identificar` e `/status` devolvem `{"ok": ..., ...}`.
  Padronizar em `{"ok": bool, "dados": ...}` simplifica o cliente.

- **S22 — `Access-Control-Max-Age` ausente** em `aplicaCORS` (`main.go:156-164`):
  cada POST paga um preflight completo. `h.Set("Access-Control-Max-Age", "600")`
  resolve.

- **S23 — `monitorLeitor` carrega a DLL no boot** e consulta o leitor a cada 5 s
  (`main.go:627-652`) mesmo em sessões que nunca usam biometria. Vale só
  inicializar sob demanda ou espaçar o *polling* quando não houver requisições.

- **S24 — Offsets mágicos sem documentação** em `novaInputFIRNativa`
  (`sdk.go:198-216`): `4:8`, `12:16`, `p+8`, `p+16`. Extrair constantes com o
  nome dos campos de `NBioAPI_INPUT_FIR` / `NBioAPI_FIR_TEXTENCODE`. O
  `WriteProcessMemory` no próprio processo também é desnecessário — um `copy`
  em `unsafe.Slice((*byte)(unsafe.Pointer(p)), tamanho)` é equivalente e mais
  legível.

- **S25 — `sdkTasks` nunca é fechado** (`main.go:53`, `main.go:712`): a goroutine
  `sdkThreadMain` vaza no shutdown. Fechar o canal após `encerraSDK`.

- **S26 — Códigos HTTP imprecisos:** `handleHello` devolve 500 quando
  `net.SplitHostPort` falha (`main.go:230-239`) — é condição de requisição, não
  erro interno. `permiteMetodo` (`main.go:136-143`) omite `OPTIONS` do header
  `Allow`.

- **S27 — Corrida no start:** se `servidor.Serve` falhar imediatamente
  (`main.go:732-741`), `systray.Quit()` pode ser chamado antes de
  `systray.Run()` (`main.go:743`).

- **S28 — `Biometria.configurar({ endereco })`** grava em `localStorage` sem
  validar o host (`integra-biometria.js:168-177`); um endereço arbitrário passaria
  a receber o `X-Bio-Token`. Restringir a `localhost`/`127.0.0.1`.

- **S29 — `Access-Control-Allow-Private-Network`** (`main.go:161`) é do esquema
  PNA antigo do Chrome, hoje substituído pelo prompt de *Local Network Access*
  (que o `README.md:132` já descreve corretamente). Manter é inofensivo, mas vale
  um comentário indicando que é compatibilidade.

- **S30 — `README.md:213-230`** omite `log.go` na árvore do projeto.

---

## 📋 Resumo

- **Arquivos analisados**: 15 (8 Go, 1 JS, 1 PowerShell, 1 Python, 2 Markdown, `go.mod`, `go.sum`)
- **Segurança**: ⚠️ Atenção — o modelo (loopback + mesma sessão + origem aprovada + token) é sólido, mas há vazamento de token na linha de comando (A7/A8), acúmulo de certificados na raiz confiável (A9) e permissões de arquivo ineficazes no Windows (A11)
- **Qualidade**: ⚠️ Atenção — código limpo e bem estruturado, porém com bugs de concorrência reais (C4, C5) e um bug lógico no supervisor (A6)
- **Risco de produção**: 🚨 Alto — o repositório **não compila** como está (C1) e a identificação 1:N tem taxa de erro inaceitável na escala documentada (C3)
- **Testes**: ❌ Sem cobertura — zero arquivos `_test.go` e *build tags* que impedem qualquer execução de teste fora de Windows/386 (C2)

---

## ✅ Pontos positivos

1. **Validação de `Origin` excepcionalmente bem feita.** `origemDoHeader`
   (`main.go:145-154`) exige que o header cru seja igual (case-insensitive) à
   forma normalizada. Isso bloqueia de uma vez `https://bom.com.evil.com`,
   `https://bom.com#x`, `https://user@bom.com`, caminhos e barras finais — um
   padrão que muitas implementações erram.

2. **Curinga recusado explicitamente** em dois níveis: no agente
   (`origins.go:65-67`) e no instalador (`instalar-servidor.ps1:120`).

3. **Isolamento entre sessões RDP bem pensado**: descoberta do PID pela tabela
   TCP estendida + `ProcessIdToSessionId` (`session.go`), com `tamanhoLinhaTCP`
   correto para `MIB_TCPROW_OWNER_PID` e validação de `dwNumEntries` contra o
   tamanho do buffer (`session.go:35-38`) antes de fatiar — nada de leitura fora
   dos limites.

4. **Comparação do token em tempo constante** com `subtle.ConstantTimeCompare`
   (`main.go:208`), e token de 256 bits de `crypto/rand` por execução.

5. **Parsing de JSON defensivo**: `MaxBytesReader` com limite por rota,
   `DisallowUnknownFields`, e rejeição explícita de múltiplos objetos no corpo
   (`main.go:339-353`). Validação de IDs duplicados e vazios em `/identificar`
   (`main.go:408-419`).

6. **Higiene de servidor HTTP completa**: `ReadHeaderTimeout`, `ReadTimeout`,
   `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes`, semáforo global de
   requisições, `X-Content-Type-Options: nosniff` e `Cache-Control: no-store`
   (`main.go:722-730`, `main.go:123-130`, `main.go:174`).

7. **Recuperação de panic em duas camadas**: no handler HTTP (`main.go:168-173`)
   e na thread do SDK (`main.go:86-92`), ambas com `debug.Stack()` no log.

8. **Escrita atômica de verdade**: `os.CreateTemp` + `Sync` +
   `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)` (`storage.go:29-73`), com
   remoção do temporário em caso de falha.

9. **Instalador cuidadoso**: valida o cabeçalho PE como x86 lendo os bytes
   (`instalar-servidor.ps1:43-58`), exige Authenticode válido salvo opt-in
   explícito, instala em diretório versionado por hash (atualização sem parada
   nas outras sessões RDP) e **recusa remover qualquer caminho fora da
   instalação** (`Remove-DiretorioInstalacao`, linhas 60-65).

10. **Documentação acima da média** no `README.md`, com diagrama de arquitetura,
    tabela de rotas, tabela de variáveis, guia de solução de problemas e um aviso
    de LGPD explícito sobre templates biométricos.

11. **`go vet` limpo** e código idiomático (nomes consistentes em português,
    genéricos usados com propósito em `naThreadSDK`) — verificado após corrigir
    o `go.sum`.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O desenho de segurança é o ponto forte do projeto e boa parte do código está
acima da média. O bloqueio vem de cinco pontos objetivos:

1. `go mod tidy` — o repositório precisa compilar (C1);
2. propagar `context` até o laço do SDK e descartar tarefas abandonadas (C4, C5);
3. revisar a identificação 1:N ou reduzir drasticamente `maxCandidatos` (C3);
4. extrair a lógica pura das *build tags* e criar a primeira bateria de testes,
   com CI (C2, A13);
5. corrigir o `COMO-USAR.md`, que hoje descreve um modelo de CORS que não existe
   (A14).
