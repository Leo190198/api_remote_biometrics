# 🔍 Revisão técnica do sistema — 2026-08-15

> ⚠️ **`main` continua em `26c9379`.** Os PRs **#10** a **#17** seguem abertos e
> nenhum crítico foi tocado. É o **nono dia consecutivo** sem um commit de
> correção.

As revisões anteriores foram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos
caminhos de exceção e pelo resíduo que tudo isso deixa gravado. Esta foi por
uma região que nenhuma delas abriu: **o que o sistema sabe sobre si mesmo
quando existe mais de uma versão dele no ar** — e, junto, **o que ele sabe
sobre a metade de si que roda no outro processo**.

Não é uma pergunta acadêmica neste projeto. A arquitetura *produz* frota
heterogênea de propósito: o serviço comparador é atualizado na hora pelo MSI,
e o agente de cada sessão RDP só troca de binário **no próximo logon** — está
escrito em `instalador/instalar-servidor.ps1:213` e repetido no `README`. Ou
seja, "agente de uma geração falando com comparador de outra" não é o caso
raro: é o estado normal do servidor durante a janela de atualização, que pode
durar dias num RDS onde ninguém desconecta.

O resultado são **três críticos novos**, **cinco alertas** e **quatro
sugestões**. Os críticos abertos foram reconferidos linha a linha contra
`26c9379` e **todos continuam válidos** (inventário auditável no fim do
documento).

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, das quais 1.055 em 7
arquivos de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/AgenteBiometria.wixproj`,
`instalador/msi/build-msi.cmd`, `conferir-biometria.cmd`, `embutir-icone.py`,
`go.mod`/`go.sum`, `.gitignore`, `README.md` e `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `go test ./...` | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo. Segue valendo o **A8 de 30/07** (sem CI) |
| `git rev-parse origin/main` | `26c9379`, idêntico ao de 2026-08-07 |
| `grep -n "versao" *.go integracao/*.js` | base do C1 — **4 lugares publicam, 0 leem** |
| `grep -rn "/status"` fora de `api/status` | base do C2 — **só `comparador.go:76` e os testes** |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Nada na cadeia confere versão — e o agente desatualizado volta a comparar dentro da jaula, que é exatamente o crash que o desenho inteiro existe para evitar *(novo)*

**Arquivos:** `main.go:52`, `main.go:302`, `main.go:325`, `anuncio.go:40`,
`anuncio.go:87`, `anuncio.go:59-80`, `delegacao.go:238-288`,
`comparador.go:168-181`, `integra-biometria.js:144-151`,
`instalador/instalar-servidor.ps1:213`, `AgenteBiometria.wxs:109-117`

O sistema publica a própria versão em **quatro** pontos:

```go
// main.go:302 — /api/hello, para o navegador
"sessao": os.Getenv("SESSIONNAME"), "https": usaTLS, "versao": versao,

// main.go:325 — /api/status
"versao": versao, "commit": commit,

// anuncio.go:87 — comparador.json, para o agente de cada sessao
Versao:   versao,

// comparador.go:175 — /status do comparador
"versao":  versao,
```

E consome em **zero**. `leAnuncio()` valida porta e token e ignora `Versao`:

```go
// anuncio.go:68-79 — a validacao inteira do anuncio
if a.Porta < 1 || a.Porta > 65535 { return nil, errors.New("...porta invalida") }
if len(a.Token) < 32            { return nil, errors.New("...token curto demais") }
if a.Endereco == "" { a.Endereco = "http://127.0.0.1:" + strconv.Itoa(a.Porta) }
return &a, nil          // Versao, PID e Desde saem daqui e morrem aqui
```

```js
// integra-biometria.js:144-151 — o cliente web tambem descarta
function conecta(achado) {
  localStorage.setItem(LS_ADDR, achado.proto + '://localhost:' + achado.dados.porta)
  sessionStorage.setItem(SS_TOKEN, achado.dados.token)
  return { porta: achado.dados.porta, sessao: achado.dados.sessao }   // versao fica pra tras
}
```

**Por que é um problema.** A frota heterogênea é *produzida pelo desenho*, não
tolerada por ele:

```powershell
# instalador/instalar-servidor.ps1:213
Write-Host 'As demais sessoes RDP serao atualizadas no proximo logon, sem interrupcao.'
```

```xml
<!-- AgenteBiometria.wxs:109-117 — o agente entra pelo Run, que so roda no logon -->
<RegistryValue Root="HKLM" Key="SOFTWARE\Microsoft\Windows\CurrentVersion\Run"
               Name="AgenteBiometria" ... />
```

O `ServiceControl` do MSI (`.wxs:93-99`, `Stop="both" Start="install"`) troca o
**comparador na hora**. O agente de cada sessão RDP aberta continua sendo o
processo antigo, com o binário antigo, até o usuário deslogar. Num RDS isso
significa semanas.

Agora o caso concreto que dói: um agente anterior ao commit `735402a`
("comparacao delegada para fora da sessao RDP") **não tem `delegacao.go`**. Ele
não procura anúncio, não conhece `COMPARADOR_URL`, e compara no próprio
processo. Dentro da sessão RDP, com o `ftapihook32` injetado, é precisamente o
caminho documentado em
[docs/diagnostico-verifymatch-rdp-2026-07-30.md](diagnostico-verifymatch-rdp-2026-07-30.md)
como fatal: `NBioAPI_VerifyMatch` corrompe memória e derruba o processo. E
nada — nem o agente, nem o comparador, nem a página — tem como perceber que a
metade velha da frota está fazendo isso. O administrador instalou o MSI, viu
"instalação concluída", e metade do servidor continua no bug.

O inverso também está aberto: um agente **novo** apontado para um comparador
**velho**. Ele acha o anúncio, valida token e porta, delega — e a resposta vem
de um binário que pode não ter as correções que motivaram a atualização. O log
do agente registra só o endereço:

```go
// delegacao.go:287
registraInfo("comparacao delegada para %s", base)
```

"para onde" sem "para qual versão" é a metade inútil da informação num servidor
onde o outro lado acabou de ser trocado.

**Como corrigir.** Os dados já estão publicados; falta lê-los. Três pontos, os
três baratos:

```go
// anuncio.go — o anuncio ja carrega a versao: registre-a
registraInfo("comparacao delegada para %s (comparador versao %s, desde %s, pid %d)",
    base, a.Versao, a.Desde, a.PID)
```

```go
// delegacao.go — e um minimo exigido, para a incompatibilidade aparecer no
// arranque e nao no meio de um atendimento
if a.Versao != "" && versaoMenorQue(a.Versao, protocoloMinimo) {
    registraErro("comparador %s e mais antigo que o minimo %s: %s",
        a.Versao, protocoloMinimo, "corrija a instalacao do servico")
}
```

```js
// integra-biometria.js — o cliente web ja recebe a versao em /api/hello
function conecta(achado) {
  if (achado.dados.versao && menorQue(achado.dados.versao, Biometria.versaoMinima)) {
    var e = new Error('O agente desta sessao esta desatualizado (' + achado.dados.versao +
                      '). Feche a sessao e entre de novo.')
    e.desatualizado = true
    throw e
  }
  ...
}
```

E, do lado do servidor, o que fecha o buraco de verdade: o MSI precisa **matar
os agentes das sessões** na atualização (é o **A6 do #10**, ainda aberto) ou o
agente precisa se auto-encerrar quando detectar que o binário em
`[PASTAINSTALACAO]` mudou. Sem uma das duas, "atualizar" continua significando
"atualizar metade".

---

### C2. `/api/status` responde "OK" olhando só o leitor local — o comparador, que decide **todo** veredito, nunca é sondado, e o `/status` dele não é chamado por ninguém *(novo)*

**Arquivos:** `main.go:312-346`, `comparador.go:76`, `comparador.go:168-181`,
`delegacao.go:275-288`, `integra-biometria.js:232-239`,
`comparador_test.go:35`, `comparador_test.go:50`, `comparador_test.go:66`

`handleStatus` monta o veredito de saúde inteiro a partir de uma pergunta só —
"quantos leitores existem aqui?":

```go
// main.go:316-345
n, err := naThreadSDK(r.Context(), func() (uint32, error) {
    s, err := ensureSDK()
    if err != nil { return 0, err }
    return s.contaDispositivos()          // <- so isso
})
info := map[string]any{"dll": dllOuNada(), "arch": "386", ...}
if comparadorRemoto != nil {
    info["comparador"] = comparadorRemoto.base    // <- o endereco, nunca o estado
} else {
    info["comparador"] = "local"
}
...
info["dispositivos"] = n
info["ok"] = n > 0                        // <- "ok" e uma afirmacao sobre o leitor
```

E o comparador expõe exatamente o endpoint que responderia a outra metade:

```go
// comparador.go:76
mux.HandleFunc("/status", comparadorStatus)

// comparador.go:172-180 — versao, commit, DLL e maquina do lado que compara
escreveJSON(w, http.StatusOK, map[string]any{
    "ok": true, "modo": "comparador", "versao": versao, "commit": commit,
    "dll": descreveDLL(caminhoDLL()), "leitor": false, "maquina": nomeMaquina(),
})
```

**Ninguém chama esse endpoint.** Um `grep` por `/status` fora de `api/status`
devolve três ocorrências, e as três são `comparador_test.go`. O
`clienteComparador` (`delegacao.go:329-362`) só conhece `/comparar` e
`/identificar`. Nem o `--teste-delegacao`, que existe para provar o desenho
inteiro, pergunta ao comparador quem ele é: imprime `comparadorRemoto.base` e
parte para a captura (`autoteste.go:520-524`).

**Por que é um problema.** Dentro da sessão RDP, a captura passa ilesa pelo
gancho da FabulaTech — é justamente o que o redirecionador foi feito para
implementar. Então `contaDispositivos()` responde `n > 0` e o `/api/status`
devolve `ok: true` **mesmo com o serviço comparador parado, com token
dessincronizado (o C1 do #10, aberto) ou apontando para um anúncio órfão (o A3
do #10, aberto)**. O sistema web pergunta "está tudo bem?", ouve "sim", chama o
beneficiário, o atendente pede o dedo — e só aí, com a pessoa na frente do
balcão, aparece o `502 comparador inacessivel`.

É pior do que não ter status: é um status que responde com confiança sobre a
única parte que não vai falhar, e cala sobre a parte que decide o veredito.
Note que `disponivel()` no cliente JS (`integra-biometria.js:232-239`) usa
`/api/ping`, que é ainda mais fino — `{"ok": true, "versao": ...}` fixo, sem
tocar em nada.

**Como corrigir.** O cliente e o endpoint já existem; falta ligá-los. Uma
sondagem curta, com prazo próprio, e um campo separado na resposta:

```go
// delegacao.go
func (c *clienteComparador) status(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/status", nil)
	if err != nil { return nil, err }
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil { return nil, fmt.Errorf("comparador inacessivel: %w", err) }
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comparador respondeu %d", resp.StatusCode)
	}
	var s map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&s); err != nil {
		return nil, err
	}
	return s, nil
}
```

```go
// main.go, em handleStatus
if comparadorRemoto != nil {
	// Prazo curto de proposito: status e uma pergunta de tela, nao de
	// atendimento. Melhor responder "nao sei" em 2s do que travar a pagina.
	ctx, cancela := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancela()
	s, err := comparadorRemoto.status(ctx)
	if err != nil {
		info["comparador"] = map[string]any{"base": comparadorRemoto.base, "ok": false, "erro": err.Error()}
		info["ok"] = false          // <- e isto que muda o veredito
		info["erro"] = "o servico comparador nao respondeu: a verificacao biometrica esta fora do ar"
		escreveJSON(w, http.StatusServiceUnavailable, info)
		return
	}
	info["comparador"] = map[string]any{
		"base": comparadorRemoto.base, "ok": true,
		"versao": s["versao"], "commit": s["commit"], "dll": s["dll"], "maquina": s["maquina"],
	}
}
```

Com isso o `ok` do `/api/status` passa a significar "esta sessão consegue
concluir uma verificação", que é a única pergunta que o sistema web precisa
fazer — e, de brinde, a versão do outro lado chega junto, cobrindo metade do
C1.

---

### C3. `build-msi.cmd` carimba `VERSAO=1.2.0` fixo: a segunda release não é reconhecida como atualização, e dois binários diferentes se anunciam com o mesmo número *(novo)*

**Arquivos:** `instalador/msi/build-msi.cmd:12-13`,
`instalador/msi/build-msi.cmd:29-35`, `AgenteBiometria.wxs:22-35`,
`AgenteBiometria.wixproj:8`, `main.go:52`

```bat
rem build-msi.cmd:12-13
set "VERSAO=%~1"
if "%VERSAO%"=="" set "VERSAO=1.2.0"
```

```bat
rem build-msi.cmd:35 — a mesma variavel vira ProductVersion E main.versao
go build -ldflags "-s -w -H windowsgui -X main.versao=%VERSAO% -X main.commit=%COMMIT%" ...
```

```xml
<!-- AgenteBiometria.wxs:22-35 -->
<Package Version="$(var.Versao)" UpgradeCode="8f3c1a94-..." Scope="perMachine" ...>
  <MajorUpgrade Schedule="afterInstallInitialize"
      DowngradeErrorMessage="Ja existe uma versao mais nova ..." />
```

**Por que é um problema.** São dois estragos, e o segundo é o grave.

**Primeiro: o número deixa de identificar o binário.** `main.versao` recebe a
mesma string. Rodar `build-msi.cmd` hoje e daqui a um mês, com o código
corrigido no meio, produz dois executáveis diferentes que respondem `1.2.0` em
`/api/status`, em `/api/hello`, no `comparador.json` e no `/status` do
comparador. Só `commit` distingue os dois — e o `commit` não aparece em
`/api/hello` nem no anúncio (`anuncio.go:83-90`), justamente os dois lugares
onde o C1 precisaria dele.

**Segundo: o `MajorUpgrade` não dispara.** O `.wxs` não declara
`ProductCode`, então o WiX v4 gera um novo a cada empacotamento; e não declara
`AllowSameVersionUpgrades`, cujo padrão é `no`. A faixa que o `MajorUpgrade`
monta é `[0, ProductVersion)` com o máximo **excluído** — e `1.2.0` não é menor
que `1.2.0`. Resultado: `FindRelatedProducts` não encontra a instalação
anterior, e o pacote novo **não remove o antigo**. Ficam dois produtos com o
mesmo `UpgradeCode` registrados na máquina, disputando:

- o mesmo `ServiceInstall` `AgenteBiometriaComparador` (`.wxs:71-91`);
- a mesma chave `HKLM\...\Run\AgenteBiometria` (`.wxs:109-117`);
- o mesmo `[PASTAINSTALACAO]AgenteBiometria.exe`.

E desinstalar qualquer um dos dois roda o `ServiceControl Remove="uninstall"` e
o `RemoveFile` do outro: some o serviço, some o binário, e o produto que
"continua instalado" fica registrado apontando para um arquivo que não existe.
Num servidor RDS de produção isso é biometria parada para todas as sessões, com
o Painel de Controle dizendo que o agente está instalado.

Isso importa mais do que um defeito de empacotamento comum: **é o caminho pelo
qual as correções dos 15 críticos abertos chegariam às máquinas.** Enquanto ele
estiver assim, "corrigimos e publicamos" e "as máquinas receberam a correção"
são duas afirmações diferentes.

**Como corrigir.** Derivar a versão do repositório e recusar empacotar sem ela:

```bat
rem build-msi.cmd — a versao vem do repositorio, nunca de um literal
set "VERSAO=%~1"
if "%VERSAO%"=="" for /f %%v in ('git describe --tags --abbrev^=0 2^>nul') do set "VERSAO=%%v"
if "%VERSAO%"=="" (
    echo Sem versao: passe-a como argumento ou crie uma tag. Nao empacoto com numero fixo.
    exit /b 1
)

rem A arvore precisa estar limpa: o -X main.commit=%COMMIT% carimba o HEAD, e
rem com arquivo modificado o binario nao corresponde ao commit que ele anuncia.
for /f %%s in ('git status --porcelain 2^>nul') do (
    echo Arvore suja: o commit carimbado nao descreveria este binario.
    exit /b 1
)
```

E, no `.wxs`, tornar o mesmo-número um erro explícito em vez de um
side-by-side silencioso:

```xml
<MajorUpgrade
    Schedule="afterInstallInitialize"
    AllowSameVersionUpgrades="yes"
    DowngradeErrorMessage="Ja existe uma versao mais nova do Agente de Biometria nesta maquina." />
```

`AllowSameVersionUpgrades="yes"` faz o pacote de mesma versão substituir o
anterior em vez de conviver com ele. (Vale saber: o MSI compara só os **três
primeiros campos** do `ProductVersion` — um `1.2.0.5` é indistinguível de
`1.2.0.4` para o instalador, então o quarto campo não serve como número de
build.)

---

## 🟡 Alertas (recomenda correção)

### A1. `DisallowUnknownFields` na fronteira interna agente↔comparador transforma qualquer campo novo em `400` para o servidor inteiro *(novo)*

**Arquivos:** `main.go:389-403`, `main.go:405-413`, `main.go:494-501`,
`comparador.go:76-78`, `delegacao.go:313-322`

O mesmo `decodificaJSON` atende as duas fronteiras — a do navegador e a
interna:

```go
// main.go:389-393
func decodificaJSON(w http.ResponseWriter, r *http.Request, limite int64, destino any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limite)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()      // <- vale tambem para /comparar e /identificar do comparador
```

```go
// comparador.go:76-78 — os mesmos handlers, agora recebendo do agente
mux.HandleFunc("/comparar", handleComparar)
mux.HandleFunc("/identificar", handleIdentificar)
```

Na superfície do navegador a rigidez é uma defesa boa: um `BiometriaBenef`
digitado errado vira erro em vez de comparação com string vazia. Na superfície
interna ela vira o contrário. Pelo C1, agente e comparador são de gerações
diferentes por desenho; acrescentar um campo em `compararJSON` — um
`OrigemSessao` para o log, um `Protocolo` para resolver o próprio C1 — faz o
lado que ainda não conhece o campo responder `400`, e o agente traduz assim:

```go
// delegacao.go:319 — o erro que o operador ve
return fmt.Errorf("comparador recusou (%d): %s", resp.StatusCode, falha.Erro)
// => "comparador recusou (400): JSON invalido: json: unknown field \"protocolo\""
```

Quem lê isso às 9h da manhã conclui "dado corrompido no banco", que é a
hipótese errada, e vai procurar template truncado. A causa é versão.

**Como corrigir.** Manter a rigidez onde ela protege e afrouxar onde ela
engessa — um parâmetro basta:

```go
func decodificaJSON(w http.ResponseWriter, r *http.Request, limite int64, destino any, estrito bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, limite)
	dec := json.NewDecoder(r.Body)
	// Campo desconhecido vindo do navegador e erro de integracao e precisa
	// aparecer. Vindo do agente, e so uma versao mais nova falando: ignorar o
	// que nao se conhece e o que mantem a frota mista funcionando.
	if estrito {
		dec.DisallowUnknownFields()
	}
	...
}
```

### A2. O corpo delegado é re-serializado com escape de HTML e pode ficar **maior** que o limite que o próprio agente acabou de aceitar *(novo)*

**Arquivos:** `main.go:41`, `sdk.go:412-419`, `delegacao.go:291-296`,
`main.go:498`

`normalizaTemplate` aceita todo ASCII imprimível:

```go
// sdk.go:416
if c := limpo[i]; c < 0x21 || c > 0x7E { return "" }
```

Isso inclui `<` (0x3C), `>` (0x3E) e `&` (0x26). E a delegação re-serializa com
`json.Marshal`, que escapa esses três por padrão — cada byte vira seis:

```go
// delegacao.go:292
// json.Marshal escapa '<', '>' e '&' por padrao: 1 byte vira 6.
//   '<' -> \u003c   '>' -> \u003e   '&' -> \u0026
dados, err := json.Marshal(corpo)
```

O agente aceitou o corpo original contra `maxCorpoIdentificar = 16 << 20`
(`main.go:41`, `main.go:498`); o comparador aplica o **mesmo** limite ao corpo
reencodado. Um corpo perto do teto, com templates que carreguem esses
caracteres, entra no agente e não cabe no comparador. A recusa aparece como
`comparador recusou (400): JSON invalido: http: request body too large` — de
novo culpando o JSON, quando a causa é a fronteira ter dois limites iguais para
dois corpos de tamanhos diferentes.

Isto **soma-se** ao **A3 do #14** (5.000 candidatos não cabem em 16 MB), não o
substitui: lá o teto é inalcançável pela contagem; aqui ele é atravessável pela
re-serialização.

**Como corrigir.** Desligar o escape na ponte interna, onde ninguém vai injetar
o corpo em HTML, e dar folga explícita ao receptor:

```go
// delegacao.go — sem escape de HTML: e um POST entre dois processos nossos
var buf bytes.Buffer
enc := json.NewEncoder(&buf)
enc.SetEscapeHTML(false)
if err := enc.Encode(corpo); err != nil {
	return fmt.Errorf("montar pedido ao comparador: %w", err)
}
```

### A3. `instalar-servidor.ps1` procura o binário **fora do repositório** e manda rodar um script que não existe *(novo)*

**Arquivos:** `instalador/instalar-servidor.ps1:121-127`

```powershell
$candidatos = @(
    (Join-Path $PSScriptRoot 'AgenteBiometria.exe'),
    (Join-Path $PSScriptRoot '..\..\dist\AgenteBiometria.exe')
)
$origem = $candidatos | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $origem) {
    throw 'AgenteBiometria.exe nao encontrado. Execute Compilar-Go.ps1 primeiro.'
}
```

`$PSScriptRoot` é `<repo>\instalador`. Então `..\..\dist\` resolve para
`<pai-do-repo>\dist\` — **um nível acima da raiz do projeto**, um caminho que
não existe em nenhuma máquina e que o repositório não controla. Deveria ser
`..\dist\`.

E `Compilar-Go.ps1` não existe em lugar nenhum da árvore (`grep -rn
"Compilar-Go" .` devolve só esta linha). O que produz o binário hoje é o
`README.md:94` (um `go build` à mão) ou o `build-msi.cmd:35`, e este último
grava em `<repo>\AgenteBiometria.exe` — que também não é nenhum dos dois
candidatos.

Na prática o script só funciona se alguém copiar o `.exe` para dentro de
`instalador\`, que é o que o `README.md:105` manda fazer. O segundo candidato e
a mensagem de erro apontam para lugares imaginários, e é a mensagem de erro que
a pessoa lê quando o script falha.

**Como corrigir.**

```powershell
$candidatos = @(
    (Join-Path $PSScriptRoot 'AgenteBiometria.exe'),
    (Join-Path $PSScriptRoot '..\AgenteBiometria.exe')       # saida do build-msi.cmd
)
...
    throw @'
AgenteBiometria.exe nao encontrado. Compile antes, na raiz do repositorio:
  $env:GOOS='windows'; $env:GOARCH='386'; go build -o AgenteBiometria.exe .
  python embutir-icone.py AgenteBiometria.exe app.ico
Ou use instalador\msi\build-msi.cmd, que faz os dois passos.
'@
```

### A4. `build-msi.cmd` empacota sem `go vet`, sem `go test` e sem conferir a árvore — e carimba o `commit` do `HEAD` mesmo com arquivo modificado *(novo)*

**Arquivos:** `instalador/msi/build-msi.cmd:26-51`

```bat
for /f %%c in ('git rev-parse --short HEAD 2^>nul') do set "COMMIT=%%c"
if "%COMMIT%"=="" set "COMMIT=local"
...
go build -ldflags "... -X main.commit=%COMMIT%" -o AgenteBiometria.exe .
```

O único portão antes do MSI é o `errorlevel` do `go build`. Com a árvore suja,
`git rev-parse HEAD` devolve um commit que **não descreve** o binário — e
`commit` é hoje o único campo que distingue duas builds (C3), o que faz dele um
identificador que mente exatamente quando mais importa: numa build de
emergência feita direto no servidor.

Some-se a isso que `go test` não roda nem localmente (as *build tags* excluem
tudo fora de `windows/386`) e que não há CI (**A8 de 30/07**, aberto desde a
primeira revisão): as 48 funções `Test*` existem e, na prática, nunca são
executadas por ninguém antes de um pacote sair.

**Como corrigir.** Além do teste de árvore limpa do C3, um portão de qualidade
que roda no alvo certo:

```bat
echo [0/3] conferindo...
set GOOS=windows
set GOARCH=386
go vet ./... || (echo go vet reprovou. & exit /b 1)
go test ./... || (echo go test reprovou. & exit /b 1)
```

### A5. `leAnuncio()` descarta `Versao`, `PID` e `Desde` — os três campos que responderiam "esse anúncio ainda vale?" *(novo)*

**Arquivos:** `anuncio.go:36-43`, `anuncio.go:59-80`, `anuncio.go:82-96`

O anúncio grava seis campos e a leitura usa três:

```go
type anuncioComparador struct {
	Porta    int    `json:"porta"`      // usado
	Token    string `json:"token"`      // usado
	PID      int    `json:"pid"`        // gravado e nunca lido
	Versao   string `json:"versao"`     // gravado e nunca lido
	Desde    string `json:"desde"`      // gravado e nunca lido
	Endereco string `json:"endereco"`   // usado
}
```

O **A3 do #10** (anúncio órfão nunca detectado) e o **A1 do #13** (sem
recuperação quando nunca houve anúncio) continuam abertos — e o dado que
resolveria boa parte deles já está no arquivo, gravado por
`publicaAnuncio` (`anuncio.go:83-90`), esperando alguém ler. Um `PID` que não
existe mais, ou um `Desde` anterior ao último boot, identificam um anúncio
morto sem precisar bater na porta.

Vale registrar o cuidado: **isto não é sugestão de confiar no arquivo** — ele é
gravável por `Users` por herança (**C2 do #13**, aberto), então `PID` e `Desde`
são pistas de diagnóstico, não credenciais. Servem para o log dizer a coisa
certa, não para autorizar nada.

**Como corrigir.**

```go
// anuncio.go — no fim de leAnuncio, antes do return
if a.PID > 0 {
	if p, err := os.FindProcess(a.PID); err != nil || !processoVivo(p) {
		// Nao e erro fatal: o anuncio pode ser de um servico que acabou de
		// reiniciar com outro pid. Mas precisa aparecer no log, porque e a
		// primeira hipotese quando a delegacao falha.
		registraErro("anuncio do comparador aponta para o pid %d, que nao esta vivo", a.PID)
	}
}
```

---

## 🟢 Sugestões (opcional)

### S1. `rodaComparadorCom` sombreia a global `porta`

```go
// comparador.go:53
porta := portaComparadorPadrao      // <- declara uma local sobre a global porta int (main.go:55)
```

Hoje é inofensivo: nenhum handler registrado no comparador lê a global (o
`handleHello`, que a usa em `main.go:279` e `main.go:295`, só existe no modo
agente). Mas a global fica em `0` durante toda a vida do serviço, e qualquer
rota futura movida para o comparador vai anunciar porta zero sem que o
compilador reclame. `portaEscuta := portaComparadorPadrao` custa uma palavra e
remove a armadilha.

### S2. `AgenteBiometria.wixproj` não define valor padrão para `Versao`

```xml
<!-- AgenteBiometria.wixproj:8 -->
<DefineConstants>Versao=$(Versao);PastaFontes=$(PastaFontes)</DefineConstants>
```

Compilar o `.wixproj` direto pelo `dotnet build` (sem passar por
`build-msi.cmd`) produz `Versao=` vazio, e o `Package Version=""` falha tarde,
já dentro do WiX. Um `<Versao Condition="'$(Versao)'==''">0.0.0</Versao>`
transforma isso num pacote obviamente marcado como não-oficial em vez de num
erro de empacotamento.

### S3. Um campo `protocolo` no anúncio resolveria o C1 melhor que comparar versões

Comparar strings de versão semântica em Go pede código de comparação e uma
convenção que ninguém está mantendo. Um inteiro que só sobe quando a fronteira
agente↔comparador muda é mais barato e diz exatamente o que precisa ser dito:

```go
const protocoloComparador = 1   // sobe quando /comparar ou /identificar mudam

type anuncioComparador struct {
	...
	Protocolo int `json:"protocolo"`
}
```

O agente recusa (ou avisa) quando `a.Protocolo > protocoloComparador`, e o
campo ausente significa "geração anterior à contagem", que é informação útil
por si só.

### S4. O arranque normal poderia reaproveitar o diagnóstico que o `--conferir-contra` já faz

`confereContra` avisa, em uma linha, o cenário mais perigoso do sistema:

```go
// autoteste.go:449-455
if comparadorRemoto == nil {
	fmt.Println("comparacao: local, neste processo")
	if temGanchoDeRedirecionamento() {
		fmt.Println("  ATENCAO: ha gancho de redirecionamento neste processo e nenhum")
		fmt.Println("  comparador anunciado. A comparacao vai falhar ou derrubar o")
		fmt.Println("  processo. Instale o servico comparador nesta maquina.")
	}
}
```

O `executa()` (`main.go:885-895`) não faz essa checagem — é o **A7 do #12**,
ainda aberto. A sugestão nova é de reuso: a função já existe, já está testada
em produção pelo caminho de diagnóstico, e cabe em três linhas no arranque, com
`registraErro` no lugar do `fmt.Println`. Junto com o C1, é o que faria um
agente desatualizado gritar em vez de derrubar o processo em silêncio.

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 33 (22 `.go`, 1 `.js`, 1 `.ps1`, 3 do MSI, 2 `.cmd`, 1 `.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e 2 docs)
- **Segurança**: 🚨 **Risco** — sem mudança desde ontem. Os críticos de dado pessoal (**C1/C2 do #17**), de gravação em `ProgramData` por `Users` (**C2 do #13**) e de agente impostor na porta 5000 (**C1 do #15**) continuam abertos
- **Qualidade**: ⚠️ Atenção — `build` e `vet` limpos; a cadeia de entrega (C3, A3, A4) é o ponto fraco desta revisão
- **Risco de produção**: 🚨 **Alto** — o C1 e o C2 de hoje descrevem um servidor que fica meio atualizado e reporta "OK" enquanto isso; o C3 explica por que a correção pode não chegar às máquinas
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que `go test ./...` não executa (`matched no packages`) e nenhum CI. **A8 de 30/07, aberto há 16 dias**

### Inventário dos críticos abertos contra `26c9379`

| # | Origem | Crítico | Estado |
|---|---|---|---|
| 1 | main (07-30) | `bioPort` do fragmento não validado | **ABERTO** |
| 2 | main (07-30) | Cliente JS descarta `ignorados` | **ABERTO** |
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
| 16 | **hoje** | **Nada confere versão; agente velho volta a comparar na jaula** | **NOVO** |
| 17 | **hoje** | **`/api/status` diz OK sem sondar o comparador** | **NOVO** |
| 18 | **hoje** | **`VERSAO` fixa: `MajorUpgrade` não dispara** | **NOVO** |

> Nota de contagem: a revisão de 14/08 registrou "11 críticos abertos". A
> recontagem item a item feita hoje, com a origem de cada um, chega a **15**
> antes dos três de hoje — a diferença está nos C1–C4 do PR #12, que são
> reapresentação dos do PR #10 e foram contados como conjunto único, e no C1 do
> PR #15, que ficou de fora. Nenhum achado novo saiu dessa recontagem; ela só
> torna o número auditável.

---

## ✅ Pontos positivos

**O isolamento da sessão salva a descoberta de um erro difícil.** O cliente web
guarda o endereço do agente em `localStorage` (`integra-biometria.js:16`,
`:144-151`), que sobrevive a reinícios. Num RDS onde cada agente pega a
primeira porta livre entre 5000 e 5099, o endereço guardado pela sessão A pode
acabar apontando para o agente da sessão B depois de um reinício. A cadeia
`401 → erroToken() → descobrir() → /api/hello → mesmaSessao() → 403` fecha isso
sozinha: `handleHello` (`main.go:279-287`) casa a porta de origem da conexão TCP
com o PID e compara as sessões Windows, o `hello()` do JS trata `403` devolvendo
`null` (`integra-biometria.js:123`) e a varredura segue para a porta seguinte.
Três peças escritas em momentos diferentes que se encaixam no caso errado — é o
tipo de coisa que costuma não funcionar.

**O `--conferir-contra` é o diagnóstico certo pelas razões certas.** Códigos de
saída que separam "não confere" de "quebrou" (`autoteste.go:417-421` e
`conferir-biometria.cmd:44-49`), o aviso de gancho-sem-comparador
(`autoteste.go:449-455`), a recusa explícita a imprimir o template
(`conferir-biometria.cmd:16-18`) e o uso do **mesmo** cliente de produção em vez
de uma imitação (`autoteste.go:501-505`). Um diagnóstico que exercita outro
caminho responde outra pergunta; este não cai nessa.

**A escolha de nunca gravar template continua sendo respeitada em todo lugar
novo.** `impressaoTemplate` (`sdk.go:431-434`) é usada nas dez linhas de log que
tocam em biometria, e o `.gitignore:17-30` documenta a regra junto com o motivo
("senha trocada vaza uma vez; digital vazada acompanha a pessoa pelo resto da
vida"). Numa base que já acumulou 15 críticos, esta decisão não escorregou uma
vez.

**Os comentários continuam registrando a decisão, e não o código.** O
`delegacao.go:196-212` explica por que a delegação existe e por que ela é
opcional; o `comparador.go:80-83` explica por que o serviço **não** tem TLS e
qual é a condição que mudaria isso; o `worker.go:18-23` explica por que o SDK
precisa de um processo separado (`recover()` não pega violação de acesso dentro
da DLL). É o que permitiu que esta revisão chegasse rápido ao ponto: o código
diz onde estão as fronteiras, e as fronteiras é que estão sem verificação.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Não pelos três críticos de hoje isoladamente, mas pelo que eles somam ao
quadro: o sistema agora tem **18 críticos abertos**, nenhum commit de correção
em nove dias, e — este é o achado novo mais desconfortável — **um caminho de
entrega que não garante que uma correção chegue às máquinas** (C3), num
servidor que **não sabe dizer quando está meio atualizado** (C1) e cujo
endpoint de saúde **responde "OK" sobre a única parte que não vai falhar**
(C2).

A recomendação de sequência muda por causa disso. Antes de qualquer correção
funcional, valem os dois itens que tornam as demais verificáveis:

1. **C3** — versão derivada do repositório e `AllowSameVersionUpgrades`. Sem
   isso não há como afirmar que uma máquina recebeu a correção.
2. **C2** — `/api/status` sondando o comparador. É o menor diff dos três (uma
   função no `clienteComparador` e um bloco no `handleStatus`) e passa a
   revelar, de fora, os estados que hoje só aparecem com o beneficiário
   esperando: serviço parado, token dessincronizado (C1 do #10), anúncio órfão
   (A3 do #10).

Com esses dois no lugar, atacar o **C1** e a fila do comparador (**C1 do #13**)
passa a ser trabalho mensurável em vez de aposta.
