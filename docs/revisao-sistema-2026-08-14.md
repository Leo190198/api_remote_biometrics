# 🔍 Revisão técnica do sistema — 2026-08-14

> ⚠️ **`main` continua em `26c9379`.** Os PRs **#10** a **#16** seguem abertos e
> nenhum crítico foi tocado. É o **oitavo dia consecutivo** sem um commit de
> correção.

As revisões anteriores cobriram o caminho de dados, a escala do comparador, a
fronteira com a pessoa, o aperto de mão com o sistema web e os caminhos de
exceção. Esta revisão foi por outro lado: **o que o sistema deixa gravado e o
que ele obriga terceiros a carregar**. É a região que nenhuma das anteriores
abriu, porque não é um caminho de código — é o resíduo que os caminhos deixam.

O resultado são **dois críticos novos**, os dois sobre dado pessoal fora do
lugar, mais **cinco alertas** e **quatro sugestões**. Os **11 críticos abertos**
(4 do documento já em `main` e 7 dos PRs #12–#15) foram reconferidos linha a
linha contra `26c9379` e **todos continuam válidos**.

**Escopo analisado:** os 22 arquivos `.go` (5.430 linhas com os 7 de teste e as
48 funções `Test*`), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `conferir-biometria.cmd`,
`embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `go test ./...` | **não executável aqui** — as *build tags* `windows && 386` excluem todos os arquivos. Segue valendo o **A8 de 30/07** (sem CI) |
| Conferência de `main` | `26c9379`, idêntico ao de 2026-08-07 |
| Conferência das ACLs herdadas do `ProgramData` | base do C1 de hoje — detalhada abaixo |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O log do comparador fica em `ProgramData` e é legível por **qualquer usuário do servidor** — com ID de beneficiário, veredito e horário *(novo)*

**Arquivos:** `comparador.go:40`, `log.go:31-45`, `main.go:564-571`,
`main.go:457-458`, `sdk.go:516-519`, `instalador/msi/AgenteBiometria.wxs:57-62`

O comparador grava o log ao lado do anúncio, e a decisão está documentada como
uma melhoria de suporte:

```go
// comparador.go:40
// Ao lado do anuncio, e nao no perfil do usuario: como servico, o perfil e
// o do SYSTEM, e o log sumiria dentro de SysWOW64\config\systemprofile.
iniciaLogEm(diretorioCompartilhado(), "comparador.log")
```

```go
// log.go:31-41
func iniciaLogEm(dir, nome string) {
	if err := os.MkdirAll(dir, 0o700); err != nil { return }   // 0o700 nao faz nada no Windows
	path := filepath.Join(dir, nome)
	...
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
```

**Por que é um problema.** O destino é `C:\ProgramData\AgenteBiometria`, e o
`perm` do Go **não tem efeito no Windows** — a ACL vem por herança, como o
próprio `.wxs:57-62` descreve. A ACL padrão do `C:\ProgramData` é:

```
NT AUTHORITY\SYSTEM:(OI)(CI)(F)
BUILTIN\Administrators:(OI)(CI)(F)
BUILTIN\Users:(OI)(CI)(RX)      <- leitura herdada por TODO arquivo abaixo
BUILTIN\Users:(CI)(AD)
CREATOR OWNER:(OI)(CI)(IO)(F)
```

O comentário do `anuncio.go:14-17` chama isso de "exatamente a divisão aqui — o
serviço escreve, os agentes leem", e para o **anúncio** está certo: o token
precisa ser legível por todos os agentes, e o trade-off está escrito. Só que a
mesma pasta passou a receber também o **log**, e para o log a divisão está
errada: o que ele guarda não é uma credencial de serviço, é o registro de
atendimento de terceiros.

E o comparador é o processo que enxerga **todas as sessões do servidor**. O que
entra no arquivo:

```go
// main.go:564-565 — o ID do beneficiario que conferiu
registraInfo("identificacao: %d candidatos, confere=%v id=%q ignorados=%d",
	len(validos), res.id != "", res.id, len(ignorados))

// main.go:568-570 — a lista de IDs quando ninguem conferiu
registraErro("identificacao: nenhum candidato conferiu e %d cadastro(s) foram ignorados: %v",
	len(ignorados), ignorados)

// main.go:457-458 — o veredito de cada 1:1
registraInfo("comparacao: benef=[%s] confere=%v", impressaoTemplate(...), ok)

// sdk.go:516-517 — ID + impressao do template do cadastro
registraErro("identificacao: candidato %q com template adulterado (%s), ignorado", ...)
```

Com o carimbo de data/hora que o `logger` já põe (`log.go:12`), **qualquer
usuário logado no servidor RDS** — não um administrador, qualquer um — pode
abrir o arquivo e responder: quem foi verificado, em que minuto, em qual
atendimento, e se conferiu. Num plano de saúde isso é dado de atendimento de
paciente. Não é o template biométrico (o projeto acertou em nunca gravá-lo,
`sdk.go:431-434`), mas é o suficiente para a LGPD, e não há barreira nenhuma:
`type C:\ProgramData\AgenteBiometria\comparador.log`.

Piora com `impressaoTemplate` (`sdk.go:431-434`): o `sha256` truncado é
**estável entre execuções** — é para isso que ele existe. Um leitor do arquivo
consegue correlacionar o mesmo cadastro em dias diferentes sem nunca ter tido o
template, o que transforma a impressão num pseudônimo persistente.

**Como corrigir.** Separar o que é anúncio (leitura pública, por desenho) do que
é log (leitura restrita). Uma pasta própria, com ACL explícita criada pelo MSI:

```xml
<!-- AgenteBiometria.wxs: pasta de log com heranca quebrada -->
<Directory Id="PASTALOG" Name="log">
  <Component Id="CompPastaLog" Guid="*">
    <CreateFolder>
      <util:PermissionEx User="SYSTEM"         GenericAll="yes" />
      <util:PermissionEx User="Administrators" GenericAll="yes" />
    </CreateFolder>
    <RegistryValue Root="HKLM" Key="SOFTWARE\AgenteBiometria" Name="log"
                   Type="string" Value="1" KeyPath="yes" />
  </Component>
</Directory>
```

```go
// comparador.go — e o log passa a ir para la
iniciaLogEm(filepath.Join(diretorioCompartilhado(), "log"), "comparador.log")
```

E, independentemente da ACL, **o ID não precisa estar em claro no log**. O
diagnóstico que motivou essas linhas ("qual cadastro está corrompido") é
atendido por um pseudônimo por instalação:

```go
// sdk.go — no lugar de %q com o id cru
func idNoLog(id string) string {
	soma := sha256.Sum256(append(salDaInstalacao(), id...))
	return fmt.Sprintf("id:%x", soma[:6])
}
```

O suporte continua conseguindo dizer "é sempre o mesmo cadastro"; quem lê o
arquivo sem contexto não descobre de quem é.

---

### C2. A identificação 1:N obriga o navegador a receber a base biométrica — o próprio README diz para não fazer isso *(novo)*

**Arquivos:** `main.go:44`, `main.go:475-513`, `integra-biometria.js:220-230`,
`README.md:182-194`, `README.md:235`, `integracao/COMO-USAR.md:46-52`

O contrato do `/identificar` é: **a página** manda os candidatos.

```js
// integra-biometria.js:220-230
identificar: async function (tmplLido, candidatos) {
  var r = await tentaComReconexao(function () {
    return requisicao('/api/public/v1/identificar', {
      method: 'POST',
      body: { lida: tmplLido, candidatos: candidatos || [] },   // <- templates no navegador
    })
  })
```

```js
// README.md:182-187 — o uso documentado
const candidatos = registros.map((item) => ({ id: item.id, template: item.biometria }))
const resultado = await Biometria.identificar(templateLido, candidatos)
```

E o limite documentado é generoso: `maxCandidatos = 5000` (`main.go:44`,
`README.md:194`).

**Por que é um problema.** Para chamar `identificar`, o backend precisa
**entregar ao navegador até 5.000 templates biométricos em claro**. A partir daí
eles estão:

- na memória do processo do navegador, numa estação de trabalho compartilhada
  ou numa sessão RDS que outro usuário pode ter em outra hora;
- no corpo de uma resposta HTTP do sistema web (sujeita a cache, proxy
  corporativo, DevTools, HAR de suporte);
- num `POST` para `http://localhost:PORTA` que, quando o `certutil` falha, sai
  **em claro** (é o **A1 do #16**, ainda aberto).

E o mesmo README, 40 linhas abaixo, escreve exatamente a regra que o desenho
quebra:

> **README.md:235** — "Templates biométricos são dados sensíveis. Armazene-os no
> backend com controle de acesso, criptografia e políticas compatíveis com a
> LGPD. **Nunca coloque templates diretamente em HTML, URLs ou logs.**"

Não é uma contradição de documentação: é a API que não permite cumprir a própria
recomendação. Dado biométrico é irrevogável — não se troca a digital de um
beneficiário depois de vazada — e este é o único ponto do sistema onde ele sai
do backend **em lote**.

O agravante é que **a peça que resolve isso já existe e já está no ar**. O
comparador (`comparador.go:78`) expõe `/identificar` autenticado por segredo
compartilhado, escuta em loopback no mesmo host do backend e não precisa de
leitor nenhum. O 1:N não tem por que passar pelo navegador.

**Como corrigir.** Inverter o fluxo do 1:N, mantendo o 1:1 e a captura como
estão:

```js
// no sistema web: o navegador manda so o que ele legitimamente tem — a leitura
const lido = await Biometria.capturar()
const r = await fetch('/api/beneficiarios/identificar', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ lida: lido }),     // sem candidatos
})
```

```
backend (mesmo host do comparador)
  -> POST http://127.0.0.1:5150/identificar
     Authorization: Bearer <segredo do ProgramData>
     { "lida": "...", "candidatos": [ ...do banco, sem sair do servidor... ] }
```

Enquanto a migração não acontece, duas medidas de contenção que cabem hoje:

1. **Baixar `maxCandidatos` para uma dezena** e documentar que o 1:N pelo
   navegador serve para desempate, não para varrer a base. 5.000 é um convite.
2. **Registrar no `/api/status`** quantos candidatos a última chamada trouxe —
   hoje ninguém no lado do agente consegue perceber que a base inteira está
   trafegando.

---

### Críticos anteriores — reconferidos hoje contra `26c9379`

| Achado | Onde | Situação |
|---|---|---|
| **C1 30/07** — `bioPort` do fragmento não é validado: `#bioPort=5000@evil.com` vira `http://localhost:5000@evil.com`, o *host* real é `evil.com`, e token + templates saem para lá; persiste em `localStorage` | `integra-biometria.js:27-28`, `173-174` | ❌ Aberto (10º dia) |
| **C2 30/07** — o cliente JS descarta `ignorados`; cadastro corrompido vira "não é a pessoa" | `integra-biometria.js:229` | ❌ Aberto |
| **C3 30/07** — 1:N é um laço de 1:1: a falsa aceitação acumula com o número de candidatos | `sdk.go:480-527` | ❌ Aberto — e o **C2 de hoje** mostra o outro custo do mesmo desenho |
| **C4 30/07** — uma identificação 1:N congela todas as capturas da sessão | `main.go:543-556`, `main.go:107-135` | ❌ Aberto |
| **C1 #15** — a página não distingue o agente de um impostor na varredura de portas | `main.go:591-596`, `integra-biometria.js:179-188` | ❌ Aberto |
| **C1 #14** — `tituloOrigem` corta o domínio que decide; diálogo da bandeja é forjável | `main.go:655-661` | ❌ Aberto |
| **C2 #14** — pendência vencida continua clicável na bandeja | `main.go:663-744`, `origins.go:131-138` | ❌ Aberto |
| **C1 #13** — fila única do SDK no comparador, sem `limiteHTTP` no modo serviço | `comparador.go:75-79`, `101-111` | ❌ Aberto |
| **C2 #13** — ACL herdada do `ProgramData` + `endereco` do anúncio sem checagem de *loopback* | `delegacao.go:74-85` | ❌ Aberto — **é a mesma pasta do C1 de hoje** |
| **C1 #12** — parar o serviço apaga o anúncio; token novo derruba os agentes de pé | `comparador.go:99`, `anuncio.go:113-121` | ❌ Aberto |
| **C4 #12** — MSI e `instalar-servidor.ps1` disputam nome e porta | `wxs:71-99`, `instalar-servidor.ps1:167-193` | ❌ Aberto |

---

## 🟡 Alertas (recomenda correção)

### A1. O supervisor é mudo: falha permanente vira laço infinito sem uma linha em lugar nenhum *(novo)*

**Arquivo:** `supervisor.go:29-56`, `main.go:877-882`

```go
// supervisor.go:29-45
func supervisor() {
	exe, err := os.Executable()
	if err != nil { os.Exit(1) }          // <- sem log: iniciaLog() ainda nao rodou
	espera := 2 * time.Second
	for {
		cmd := exec.Command(exe)
		...
		if err := cmd.Start(); err != nil { os.Exit(1) }   // <- idem
		if cmd.Wait() == nil { os.Exit(0) }
		time.Sleep(espera)
		if time.Since(inicio) > time.Minute {
			espera = 2 * time.Second       // <- conta o sleep junto
		}
```

**Por que é um problema.** Três coisas, em ordem de dor:

1. **Os dois `os.Exit(1)` são invisíveis.** `iniciaLog()` só é chamado no ramo
   `BIO_FILHO=1` (`main.go:885`), e o binário é `-H windowsgui`: sem console,
   sem `stderr`. Se o `cmd.Start()` falhar — antivírus segurando o executável,
   política de execução, disco cheio — o agente **não sobe e não deixa rastro
   nenhum**. O sintoma para o suporte é "o ícone não aparece", com um
   `agente.log` que pode nem existir.
2. **O laço não tem teto.** Uma falha permanente (todas as portas 5000-5099
   ocupadas, `main.go:597`) faz o par supervisor/filho girar para sempre. O
   filho ao menos registra `listener: sem porta livre`, então o log cresce até
   a rotação de 5 MB (`log.go:36-38`) e vai empurrando o histórico para fora.
3. **O reset do backoff conta o próprio `sleep`.** `inicio` é marcado antes do
   `Start`, e `time.Since(inicio)` é medido **depois** do `time.Sleep(espera)`.
   Um filho que morre consistentemente aos ~59 s é classificado como saudável
   (59 s + 2 s de sleep > 1 min) e o intervalo **nunca sai de 2 segundos** — que
   é exatamente o caso que o freio existe para conter.

**Como corrigir.** Log antes de tudo, teto de tentativas e medição do tempo
**vivo**, não do tempo de ciclo:

```go
func supervisor() {
	iniciaLogArquivo("supervisor.log")   // antes de qualquer os.Exit
	exe, err := os.Executable()
	if err != nil {
		registraErro("supervisor: nao achei o proprio executavel: %v", err)
		os.Exit(1)
	}
	espera := 2 * time.Second
	for tentativa := 1; ; tentativa++ {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "BIO_FILHO=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		inicio := time.Now()
		if err := cmd.Start(); err != nil {
			registraErro("supervisor: nao consegui iniciar o agente (tentativa %d): %v", tentativa, err)
			os.Exit(1)
		}
		err := cmd.Wait()
		vivo := time.Since(inicio)          // tempo VIVO, sem o sleep
		if err == nil { os.Exit(0) }
		registraErro("supervisor: o agente caiu apos %s: %v", vivo.Round(time.Second), err)
		time.Sleep(espera)
		if vivo > time.Minute {
			espera = 2 * time.Second
		} else if espera < time.Minute {
			espera *= 2
			if espera > time.Minute { espera = time.Minute }
		}
	}
}
```

### A2. `maxCandidatos = 5000` é inalcançável com `maxCorpoIdentificar = 16 MB`, e a mensagem culpa o JSON *(novo)*

**Arquivos:** `main.go:36-46`, `main.go:497-509`, `README.md:194`

```go
maxCorpoIdentificar = 16 << 20   // 16 MB
maxTemplate         = 64 << 10   // 64 KB por template
maxCandidatos       = 5000
```

**Por que é um problema.** Os dois limites foram escolhidos por motivos
independentes e nunca foram conferidos um contra o outro. Um FIR texto do
NBioBSP tem "alguns KB" — é o que o comentário do `main.go:38-39` afirma. A
conta com 4 KB por template, mais `{"id":"...","template":"..."}` (~30 bytes de
JSON por item):

| Candidatos | Corpo aproximado | Passa em 16 MB? |
|---|---|---|
| 1.000 | ~4 MB | sim |
| 3.900 | ~16 MB | no limite |
| **5.000** | **~20 MB** | **não** |

Ou seja: o limite que o README publica (`README.md:194`, "Cada chamada aceita
entre 1 e 5.000 candidatos") **não é atingível com template de tamanho real**.
E a falha acontece do jeito mais caro possível: o `MaxBytesReader`
(`main.go:390`) só corta depois de o navegador ter enviado 16 MB, e a resposta
é `400 JSON invalido: http: request body too large` (`main.go:499-501`) — que
manda quem integra procurar erro de sintaxe num JSON perfeitamente válido.

Some-se ao **C2 de hoje**: os 5.000 não são só inatingíveis, são um número que
não deveria ser oferecido.

**Como corrigir.** Fazer o limite real ser o limite anunciado, e dizer a
verdade quando ele estourar:

```go
// main.go, em handleIdentificar
if err := decodificaJSON(w, r, maxCorpoIdentificar, &body); err != nil {
	var excedeu *http.MaxBytesError
	if errors.As(err, &excedeu) {
		escreveErro(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("a lista de candidatos passou de %d MB; envie menos candidatos por chamada",
				maxCorpoIdentificar>>20))
		return
	}
	escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
	return
}
```

E alinhar `maxCandidatos` e o README ao que cabe de fato (ou baixá-lo, como o
C2 propõe).

### A3. `/api/hello` entrega porta, versão e o nome da sessão Windows a **qualquer** origem, antes de qualquer autorização *(novo)*

**Arquivo:** `main.go:265-306` (em especial `293-297`)

```go
// main.go:292-298 — resposta a uma origem ainda NAO autorizada
if !origens.solicita(origem) {
	escreveJSON(w, http.StatusAccepted, map[string]any{
		"ok": false, "autorizacao": "pendente", "porta": porta,
		"sessao": os.Getenv("SESSIONNAME"),
	})
	return
}
```

**Por que é um problema.** O `202` é a resposta para uma origem que **ainda não
foi aprovada por ninguém** — e ela já sai com CORS aplicado (`main.go:235-241`),
portanto legível pelo JavaScript do site. Qualquer página que o operador visite
descobre, sem clique nenhum:

- que a máquina tem o agente instalado, e em qual porta;
- o `SESSIONNAME` — que num RDS é `RDP-Tcp#<n>`, o identificador da sessão
  daquele usuário naquele servidor.

O **A5 de 30/07** cobriu o outro lado deste mesmo `handler` (qualquer origem
consegue disparar o pedido na bandeja). Este é o dado que vaza **mesmo quando o
usuário nunca autoriza nada**, e ele é útil para quem monta o ataque do **C1 do
#15**: saber a porta certa dispensa a varredura.

O `200` (`main.go:300-303`) devolver `sessao` e `versao` é outra história — ali
a origem foi aprovada e o token está indo junto de qualquer forma.

**Como corrigir.** O `202` só precisa dizer "pendente":

```go
escreveJSON(w, http.StatusAccepted, map[string]any{
	// Nem porta nem sessao: esta origem ainda nao foi aprovada por ninguem, e
	// os dois campos servem para o cliente que ja passou dessa barreira.
	"ok": false, "autorizacao": "pendente",
})
```

O cliente não perde nada: `hello()` (`integra-biometria.js:114-136`) já conhece
a porta — foi ela que ele sondou.

### A4. Um `origens-autorizadas.json` corrompido apaga todas as autorizações em silêncio *(novo)*

**Arquivo:** `origins.go:74-86`

```go
func (g *gerenciadorOrigens) carrega() {
	dados, err := os.ReadFile(g.caminho)
	if err != nil { return }                      // <- sem log
	var origens []string
	if json.Unmarshal(dados, &origens) != nil { return }   // <- sem log, e descarta TUDO
	...
}
```

**Por que é um problema.** `gravaArquivoAtomico` (`storage.go:26-73`) é sólido —
`CreateTemp` + `Sync` + `MoveFileEx(WRITE_THROUGH)` — então a corrupção não vem
daqui. Vem de fora: perfil de usuário restaurado pela metade, antivírus
truncando o arquivo, edição manual durante o suporte. Quando vem, um único byte
inválido faz **todas** as origens aprovadas desaparecerem, e o usuário volta a
ver o pedido de autorização na bandeja sem nenhuma explicação, num sistema que
funcionava ontem.

O arquivo já está a uma linha do log — `carrega()` é chamado por
`novoGerenciadorOrigens()` (`origins.go:39`), que roda **depois** do
`iniciaLog()` (`main.go:885`).

**Como corrigir.** Dizer o que aconteceu e preservar o que não pôde ser lido:

```go
func (g *gerenciadorOrigens) carrega() {
	dados, err := os.ReadFile(g.caminho)
	if err != nil {
		if !os.IsNotExist(err) {
			registraErro("ler origens autorizadas (%s): %v", g.caminho, err)
		}
		return
	}
	var origens []string
	if err := json.Unmarshal(dados, &origens); err != nil {
		// Guarda o arquivo ilegivel: sem isso a proxima gravacao o sobrescreve e
		// a unica pista de por que as autorizacoes sumiram vai junto.
		_ = os.Rename(g.caminho, g.caminho+".invalido")
		registraErro("origens autorizadas ilegiveis (%v); o arquivo virou %s.invalido "+
			"e todas as origens precisarao ser autorizadas de novo", err, g.caminho)
		return
	}
	...
}
```

### A5. `agente-<sessao>.json` fica para trás com porta e token mortos *(novo)*

**Arquivos:** `main.go:615-641`, `main.go:913-915`, `main.go:953-959`

`escreveConfig()` grava o arquivo de descoberta no início (`main.go:913`) e
**nada o remove no encerramento** — o bloco final (`main.go:953-959`) fecha o
servidor e o SDK, e sai.

**Por que é um problema.** O arquivo contém `porta`, `token`, `pid` e `sessao`
(`main.go:633-636`). Depois que o agente sai, ele continua no perfil do usuário
anunciando uma porta que outro programa pode ter tomado e um token que não vale
mais. Quem lê esse arquivo — script de suporte, integração local, o próprio
operador conferindo à mão — recebe uma configuração que parece boa e falha com
`401`, ou pior, aponta para um processo qualquer que subiu na porta 5000
depois. É o mesmo raciocínio que levou o `removeAnuncio()` a existir
(`anuncio.go:98-105`), aplicado ao lado do agente, onde ele não foi aplicado.

O contraste é direto: o comparador limpa o próprio anúncio ao parar; o agente
não limpa o dele.

**Como corrigir.** Uma linha, no mesmo lugar em que o SDK é encerrado:

```go
// main.go, no encerramento
_ = servidor.Shutdown(ctxShutdown)
encerraSDK(ctxShutdown)
removeConfig()   // simetrico ao removeAnuncio() do comparador
```

```go
func removeConfig() {
	if caminho, err := caminhoConfig(); err == nil {
		if err := os.Remove(caminho); err != nil && !os.IsNotExist(err) {
			registraErro("remover arquivo de descoberta: %v", err)
		}
	}
}
```

(extraindo de `escreveConfig` a montagem do nome, hoje embutida em
`main.go:620-632`, para as duas funções usarem o mesmo caminho).

---

## 🟢 Sugestões (opcional)

- **S1.** `main.go:327-331` — `/api/status` já diz **onde** a comparação
  acontece (`comparador` = base ou `"local"`). Falta dizer **se ela está
  funcionando**: hoje um token recusado pelo comparador (o **C1 do #12**) só
  aparece como `502` no meio de um atendimento. Guardar o resultado da última
  delegação (`ok`, `401`, `inacessivel`) e expor em `/api/status` transforma um
  chamado de "a biometria parou" num diagnóstico de dez segundos.
- **S2.** `sdk.go:431-434` — `impressaoTemplate` é um pseudônimo **estável e sem
  sal**: o mesmo cadastro produz a mesma impressão em qualquer máquina, para
  sempre. É deliberado e útil (reconhecer o mesmo registro entre execuções), mas
  vale dizer isso no comentário e considerar um sal por instalação, gravado
  junto do certificado. O diagnóstico continua valendo dentro de uma instalação
  e o log deixa de ser correlacionável entre elas — o que reduz bastante o
  estrago do **C1 de hoje**.
- **S3.** As três rotas públicas devolvem três formatos diferentes:
  `/captura/Capturar` devolve uma **string nua**, `/captura` devolve um
  **booleano nu** (`main.go:381`, `main.go:459`) e `/identificar` devolve um
  **objeto** (`main.go:573-576`). O cliente JS trata cada uma na mão
  (`integra-biometria.js:201-230`) e não tem como escrever um tratamento
  genérico de resposta. Se a compatibilidade com o formato legado precisa ficar,
  vale documentar a assimetria no `README.md:206` — hoje a tabela de endpoints
  não a menciona.
- **S4.** `.gitignore` continua sem `comparador.json` (**S2 do #16**, **S6 do
  #15**, **S5 do #14** — aberto há quatro dias). É uma linha, e é o único desses
  arquivos que carrega uma credencial válida para a máquina inteira:
  `agente-*.json`, `origens-autorizadas.json`, `cert.pem` e `key.pem` já estão
  lá.

---

## 📋 Resumo

| | |
|---|---|
| **Arquivos alterados** | 1 neste PR (somente documentação); **nenhum em `main`** desde `26c9379`; 41 revisados |
| **Segurança** | 🚨 Risco |
| **Qualidade** | ⚠️ Atenção |
| **Risco de produção** | 🚨 Alto |
| **Testes** | ❌ Sem cobertura efetiva — `build` e `vet` limpos, mas `go test` **não roda em lugar nenhum** (sem CI, *build tags* `windows && 386`) |

**Contagem de hoje:** 2 críticos novos, 5 alertas novos, 4 sugestões. Os 11
críticos abertos foram reconferidos linha a linha e continuam válidos.

**O que os dois críticos de hoje têm em comum.** Nenhum dos dois é um erro de
programação — os dois são decisões corretas para o problema que resolviam,
aplicadas sem conferir o que carregavam junto:

- pôr o log do comparador em `ProgramData` resolveu um problema real (o log
  sumia no perfil do SYSTEM) e trouxe junto a ACL de leitura pública que já
  estava documentada para o **anúncio** — só que anúncio e log guardam coisas
  diferentes;
- deixar a página montar a lista de candidatos deu ao integrador o controle da
  consulta ao banco, e trouxe junto a base biométrica para dentro do navegador.

É o mesmo padrão do veredicto do **#16**, deslocado: lá o caminho de exceção não
recebeu a atenção do caminho feliz; aqui o **resíduo** de cada decisão não
recebeu a atenção da decisão.

**Cobertura de testes dos achados de hoje:**

| Achado | Por que a suíte não pega |
|---|---|
| **C1** | ACL de sistema de arquivos no Windows — nenhum teste do projeto exercita permissão, e o `perm` do Go não significa nada aqui |
| **C2**, **A3** | Estão no `.js` e no contrato HTTP; não há arranjo de teste para nenhum dos dois |
| **A1** | `supervisor()` chama `os.Exit` direto — intestável como está; extrair o laço para `supervisiona(iniciar func() error) ` daria um teste de tabela |
| **A2** | Precisaria de um teste de handler com corpo grande; `main.go` não tem nenhum teste de `handler` |
| **A4**, **A5** | `origins.go` e o par `escreveConfig`/`removeConfig` são testáveis **hoje**, com `diretorioDados` já injetável (`storage.go:13-19`) — o mesmo truque que `anuncio_test.go` usa |

`origins.go`, o middleware, `session.go`, `storage.go`, `cert.go`, `log.go` e
`supervisor.go` continuam sem uma linha de teste.

---

## ✅ Pontos positivos

- **O template biométrico nunca é gravado, e isso está sustentado em todos os
  pontos de saída.** `impressaoTemplate` (`sdk.go:431-434`) devolve tamanho e
  `sha256` truncado; `forma()` (`autoteste.go:128-139`) mostra o contorno e não o
  conteúdo; o `conferir-biometria.cmd` diz na cara por que não imprime; e há um
  teste dedicado (`TestImpressaoTemplateNaoVazaOTemplate`). O **C1 de hoje** é
  sobre o que ficou **em volta** do template, e existe justamente porque a regra
  central foi respeitada com disciplina.
- **`normalizaTemplate` (`sdk.go:407-421`) é validação de entrada feita pelo
  motivo certo.** O comentário não diz "sanitizar por higiene": diz que
  `NBioAPI_VerifyMatch` confia nos campos de tamanho embutidos e lê fora da
  alocação, que uma violação de acesso dentro da DLL **não vira panic do Go**, e
  que um registro truncado por coluna curta no banco basta para provocá-la. A
  regra resultante (ASCII imprimível contínuo, 32 a 64 KB) é estreita, e há
  quatro testes cobrindo os limites.
- **A separação entre "não confere" e "falhou" é levada a sério de ponta a
  ponta:** códigos de saída distintos no `--conferir-contra`
  (`autoteste.go:419-423`), `ignorados` atravessando a fronteira do processo
  worker (`worker.go:45-51`), a recusa de uma resposta contraditória do
  comparador (`delegacao.go:166-171`) e um teste para cada um. É a distinção que
  quase todo utilitário de biometria erra — e errá-la significa tratar falha de
  leitor como negativa de identidade.
- **A ordem dos `defer` em `capturaTexto` (`sdk.go:275-327`)**, com cinco
  liberações de memória nativa e o `freeText` registrado só depois de o SDK
  confirmar ponteiro não nulo, continua correta em todas as posições.
- **`build` e `vet` limpos** para `windows/386`, testes incluídos, em 5.430
  linhas que manipulam memória nativa, `uintptr`, FFI e quatro subsistemas do
  Windows (SCM, tabela TCP, toolhelp, loja de certificados).

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Dois críticos novos, ambos sobre dado pessoal, e onze anteriores que completam
uma semana e meia parados.

**Ordem sugerida de correção**, considerando o custo de cada uma e o que já está
aberto:

1. **C1 de hoje** — mover `comparador.log` para uma pasta com ACL própria. É a
   correção mais barata desta lista (duas linhas no `.wxs`, uma no
   `comparador.go`) e fecha uma exposição que hoje qualquer usuário do servidor
   alcança com um `type`.
2. **C1 + C2 do #14** — ~25 linhas somadas; continuam sendo o único par
   alcançável **sem nenhum acesso à máquina**. Oitavo dia.
3. **C1 de 30/07** (`bioPort`) — validação de porta como inteiro em 1..65535,
   num ponto único. Décimo dia, e é o caminho por onde template e token saem da
   máquina com um único link.
4. **C2 #13** (loopback em `delegacao.go`) — a outra metade da mesma pasta do C1
   de hoje.
5. **A1 de hoje** — o supervisor mudo. Não muda comportamento correto nenhum;
   decide se o próximo chamado tem diagnóstico ou folclore.
6. **C1 #12 + A6 #15** — o token que o serviço troca na parada limpa e o agente
   nunca reconfere. São dois lados de um defeito só.
7. **C2 de hoje** — o maior dos dois em esforço, porque mexe no contrato com o
   sistema web. As duas medidas de contenção (baixar `maxCandidatos`, registrar
   o volume) cabem num commit e valem enquanto a migração não acontece.
8. **A2 a A5 de hoje** — pequenas, independentes entre si.

Este PR **não altera código**: acrescenta apenas este documento.
