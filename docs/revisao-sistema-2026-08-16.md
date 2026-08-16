# 🔍 Revisão técnica do sistema — 2026-08-16

> ⚠️ **`main` continua em `26c9379`, de 2026-08-06.** Os PRs **#10** a **#18**
> seguem abertos e nenhum crítico foi tocado. É o **décimo dia consecutivo** sem
> um commit de correção.

As revisões anteriores foram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos
caminhos de exceção, pelo resíduo gravado em disco e pela cadeia de entrega.
Esta foi por uma região que nenhuma delas abriu: **o que acontece quando existe
mais de uma instância do mesmo binário viva ao mesmo tempo na mesma máquina** —
dois agentes do mesmo usuário em sessões diferentes, e dois comparadores (o
serviço e o de diagnóstico) disputando o mesmo arquivo de anúncio.

Não é hipótese: as duas situações são **produzidas pelo próprio desenho**. O
`instanciaUnica()` usa o espaço de nomes `Local\`, que é por sessão
(`supervisor.go:18`), ou seja, o código autoriza explicitamente um agente por
sessão do mesmo usuário — enquanto todo o estado desse usuário mora num
diretório só, por usuário e não por sessão (`storage.go:13-19`). E o
`--comparador` de terminal é apresentado como ferramenta de diagnóstico no
próprio comentário de `main.go:872-873`, usando a mesma função que o serviço
usa, com o mesmo `defer removeAnuncio()`.

O resultado são **dois críticos novos**, **cinco alertas** e **quatro
sugestões**. Os 18 críticos abertos foram reconferidos contra `26c9379` e
**todos continuam válidos**.

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
| `go test ./...` (alvo do host) | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo |
| `GOOS=windows GOARCH=386 go test ./...` | **`exec format error`** — compila e não roda. As 48 funções `Test*` continuam sem executar em lugar nenhum. Segue valendo o **A8 de 30/07** |
| `git log -1 origin/main` | `26c9379`, de **2026-08-06** |
| `find . -iname "Assinar*" -o -iname "Compilar*"` | **nada** — os dois scripts citados pelo instalador não existem na árvore |
| `grep -n "diretorioDados()" *.go` | base do C1 — um diretório por **usuário**, com um agente por **sessão** |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O estado do usuário é compartilhado por todas as sessões dele, e o agente grava o arquivo inteiro a partir de uma cópia de memória velha — uma revogação de sites feita numa sessão é desfeita pela outra *(novo)*

**Arquivos:** `supervisor.go:17-27`, `main.go:877-883`, `storage.go:13-19`,
`origins.go:31-47`, `origins.go:74-88`, `origins.go:131-147`,
`origins.go:149-161`, `README.md:21`

O trava-instância é por sessão, de propósito:

```go
// supervisor.go:18 — "Local\" e o espaco de nomes POR SESSAO do Windows
nome, err := syscall.UTF16PtrFromString(`Local\AgenteBiometriaGo`)
```

```go
// main.go:877-883 — e so o supervisor confere; um agente por sessao e o desenho
if os.Getenv("BIO_FILHO") != "1" {
	if !instanciaUnica() {
		return 0
	}
	supervisor()
	return 0
}
```

O estado, porém, é por **usuário**:

```go
// storage.go:13-19 — LOCALAPPDATA e do usuario, nao da sessao
var diretorioDados = func() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "BiometriaAgente")
}
```

```go
// origins.go:37 — a lista de sites autorizados mora nesse diretorio
caminho: filepath.Join(diretorioDados(), "origens-autorizadas.json"),
```

E a gravação é **substituição total a partir de uma foto tirada no arranque**.
`carrega()` roda uma vez, dentro de `novoGerenciadorOrigens()` (`origins.go:39`):

```go
// origins.go:74-88 — le o arquivo uma unica vez, na criacao do gerenciador
func (g *gerenciadorOrigens) carrega() {
	dados, err := os.ReadFile(g.caminho)
	if err != nil {
		return
	}
	...
}
```

```go
// origins.go:149-161 — e reescreve o arquivo inteiro a partir do mapa em memoria
func (g *gerenciadorOrigens) salvaBloqueado() error {
	origens := make([]string, 0, len(g.aprovadas))
	for origem := range g.aprovadas {
		origens = append(origens, origem)
	}
	sort.Strings(origens)
	...
	return gravaArquivoAtomico(g.caminho, dados, 0o600)
}
```

O `sync.RWMutex` de `origins.go:23` protege as goroutines de **um** processo. Ele
não sabe que existe outro processo com outro mapa.

**Por que é um problema.** A autorização de origem é a única barreira entre um
site qualquer e o leitor biométrico do usuário: `middleware` (`main.go:237-240`)
recusa tudo o que não estiver na lista, e é a bandeja que põe alguém nela. Com
duas sessões do mesmo usuário abertas — o console mais uma sessão RDP, ou duas
sessões RDP num servidor que não restringe o usuário a uma só, que é
configuração comum de RDS — a sequência abaixo desfaz uma revogação **sem que
ninguém peça**:

1. Sessão A e sessão B sobem. Ambas leem o arquivo com `{X, Y}` e guardam essa
   lista em memória.
2. Na sessão A o usuário clica em **"Revogar sites autorizados"**. `revogaTodas()`
   (`origins.go:140-147`) zera o mapa de A e grava `[]` no arquivo. Do ponto de
   vista de quem clicou, os sites foram revogados.
3. Na sessão B, algum tempo depois, o usuário autoriza um site novo `Z`.
   `aprova()` (`origins.go:131-138`) acrescenta `Z` ao mapa **de B**, que ainda é
   `{X, Y}`, e grava `{X, Y, Z}`.

O arquivo volta a ter `X` e `Y`. Nenhuma mensagem, nenhuma linha de log, nenhum
item na bandeja: a revogação simplesmente deixou de existir. Basta o agente de B
ser reiniciado — pelo supervisor após uma queda, por exemplo — para `X` e `Y`
valerem também na sessão A. E a ordem inversa perde autorizações legítimas: o
site aprovado em A some quando B grava.

Vale notar que **isso não exige duas sessões para acontecer**. Qualquer edição
externa do arquivo — um script de suporte tirando um domínio, o administrador
corrigindo uma entrada à mão — é silenciosamente revertida na primeira
aprovação seguinte, porque o agente nunca relê o que gravou.

O `README.md:21` promete o contrário:

> cada usuário possui sua própria instância, porta, token **e lista de sites
> autorizados**.

A instância, a porta e o token são mesmo por sessão (`main.go:896-907`,
`main.go:615-641`). A lista de sites autorizados não é — e, pior, é
compartilhada de um jeito que perde escrita.

O mesmo diretório carrega ainda `cert.pem`/`key.pem` (`cert.go:26-29`) e
`agente.log` (`log.go:14`). O log é a consequência menor, mas ilustra o padrão:
a rotação de `iniciaLogEm` (`log.go:36-39`) faz `os.Rename` sobre um arquivo que
o agente da outra sessão mantém aberto sem `FILE_SHARE_DELETE`; o `os.Rename`
falha, o erro é descartado com `_ =`, e o log dos dois processos passa a crescer
sem rotação nenhuma.

**Como corrigir.** Duas mudanças pequenas, e nesta ordem:

```go
// origins.go — reler antes de gravar, e gravar a uniao. O arquivo e a verdade;
// o mapa em memoria e so um cache dela.
func (g *gerenciadorOrigens) salvaBloqueado() error {
	// Outro agente do mesmo usuario (outra sessao) pode ter gravado depois de
	// nos carregarmos. Sem esta releitura, a gravacao apaga o que ele aprovou.
	doDisco := leOrigensDoArquivo(g.caminho)
	for origem := range doDisco {
		g.aprovadas[origem] = struct{}{}
	}
	...
}
```

A revogação precisa do tratamento oposto — ela **quer** apagar o que está no
disco, então não pode passar pela união:

```go
// origins.go — revogar grava a lista vazia direto, sem reler
func (g *gerenciadorOrigens) revogaTodas() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.aprovadas = make(map[string]struct{})
	g.pendentes = make(map[string]time.Time)
	// Sem uniao: o objetivo aqui e justamente sobrescrever o que houver.
	return gravaArquivoAtomico(g.caminho, []byte("[]\n"), 0o600)
}
```

E, para o agente da outra sessão não continuar servindo a lista velha da
memória, `permitida()` (`origins.go:90-96`) precisa reconferir o arquivo quando
ele mudou de `ModTime` — ou o gerenciador precisa recarregar num `ticker` curto.
A alternativa mais simples, e provavelmente a certa, é **tornar o estado por
sessão de fato**, como o `README` já promete:

```go
// storage.go — um diretorio por sessao, coerente com o mutex Local\ e com o
// agente-<sessao>.json que ja existe em main.go:640
func diretorioDadosDaSessao() string {
	return filepath.Join(diretorioDados(), "sessao-"+sessaoSegura())
}
```

Isso resolve o conflito de escrita, o log sem rotação e a promessa do `README`
de uma vez. O custo é o usuário reautorizar o site na primeira vez em cada
sessão — que, num sistema em que a autorização é a única barreira, é o lado
seguro do erro.

---

### C2. O `--comparador` de terminal — o modo de diagnóstico que o próprio código recomenda — republica e depois **apaga** o anúncio do serviço em produção, e o agente que sobe sem anúncio não escreve uma linha de log *(novo)*

**Arquivos:** `comparador.go:32-37`, `comparador.go:53-61`, `comparador.go:85-99`,
`servico.go:32`, `main.go:872-876`, `anuncio.go:82-96`, `anuncio.go:101-105`,
`anuncio.go:113-121`, `delegacao.go:58-65`,
`instalador/instalar-servidor.ps1:180`

A mesma função atende os dois modos:

```go
// comparador.go:32 — o terminal
func rodaComparador() int { return rodaComparadorCom(nil) }
```

```go
// servico.go:32 — o servico do MSI
go func() { saida <- rodaComparadorCom(pronto) }()
```

```go
// main.go:872-876 — e o comentario convida a usar o modo de terminal
// O comparador vem antes do supervisor e da bandeja: e um servico de
// servidor, sem sessao Windows, sem leitor e sem interface. No terminal ele
// escreve na tela, o que mantem o --comparador util para diagnostico.
if os.Getenv("MODO_COMPARADOR") == "1" || (len(os.Args) > 1 && os.Args[1] == "--comparador") {
	return rodaComparador()
}
```

E `rodaComparadorCom` publica e, ao sair, apaga:

```go
// comparador.go:94-99
if err := publicaAnuncio(porta, segredo); err != nil {
	registraErro("comparador: publicar anuncio: %v", err)
} else {
	registraInfo("comparador: anunciado em %s", caminhoAnuncio())
}
defer removeAnuncio()
```

`publicaAnuncio` **sobrescreve** `comparador.json` (`anuncio.go:82-96`) e
`removeAnuncio` **apaga** o arquivo (`anuncio.go:101-105`). Nenhum dos dois
pergunta quem o escreveu.

**Por que é um problema.** Uma execução de diagnóstico numa porta diferente
sequestra e depois destrói a configuração de produção do servidor inteiro.

Vale registrar o que **não** acontece, porque é o que dá o contorno exato do
defeito: `--comparador` sem nada configurado tenta escutar na 5150, esbarra no
serviço que já está lá, e sai em `comparador.go:85-90` — **antes** de tocar no
anúncio. Esse caminho é seguro.

O problema aparece quando `COMPARADOR_PORTA` está definida, e ela não é exótica:
o instalador antigo a grava como variável **de máquina**, para todo mundo:

```powershell
# instalador/instalar-servidor.ps1:180
[Environment]::SetEnvironmentVariable('COMPARADOR_PORTA', "$ComparadorPorta", 'Machine')
```

Num servidor que passou pelo `instalar-servidor.ps1` — e depois recebeu o MSI,
que é o caminho de migração descrito no **C4 do #12** — qualquer terminal já
nasce com essa variável no ambiente. Aí:

1. O técnico roda `AgenteBiometria.exe --comparador` para ver o log na tela.
   `COMPARADOR_PORTA` está no ambiente, então ele escuta noutra porta e **sobe**.
2. `tokenDoComparador()` (`anuncio.go:113-121`) lê o anúncio existente e
   reaproveita o token do serviço. O anúncio novo sai com o **mesmo token** e a
   **porta nova**.
3. Todo agente que arrancar a partir daí lê esse anúncio e delega para a
   instância de terminal — que é temporária, tem outro PID e vai embora. O token
   bate, então nada acusa a troca.
4. O técnico fecha o terminal. O `defer removeAnuncio()` roda e **apaga o
   anúncio**. O serviço continua no ar, ouvindo na 5150, e agora invisível.

O estado final é o pior possível: o comparador está de pé, saudável, e nenhum
agente novo o encontra. E o agente que não encontra **não diz nada**:

```go
// delegacao.go:58-65 — o unico registraErro esta atras de uma condicao que,
// numa instalacao por MSI, e sempre falsa
if base == "" || len(token) < 32 {
	a, err := leAnuncio()
	if err != nil {
		if base != "" || token != "" {          // <- MSI nao define nenhuma das duas
			registraErro("COMPARADOR_URL/TOKEN incompletos e sem anuncio utilizavel (%v): a comparacao continua local", err)
		}
		return                                   // <- sai calado
	}
	...
}
```

Com `COMPARADOR_URL` e `COMPARADOR_TOKEN` vazias — que é exatamente o desenho do
MSI, escrito com todas as letras no `AgenteBiometria.wxs:15-17` ("Não há nada a
configurar depois") — a condição é falsa e a função **retorna sem escrever
nada**. O agente passa a comparar localmente, dentro da sessão RDP, com o
`ftapihook32` injetado: o caminho documentado em
[docs/diagnostico-verifymatch-rdp-2026-07-30.md](diagnostico-verifymatch-rdp-2026-07-30.md)
como fatal. O `agente.log` daquela sessão não tem uma linha sobre isso, e o
`/api/status` responde `comparador: "local"` (`main.go:329-333`) como se fosse
uma escolha.

A recuperação também não é óbvia: reiniciar o agente não adianta, porque não há
anúncio para achar. Só reiniciar o **serviço** republica — e ninguém tem motivo
para suspeitar do serviço, que nunca parou.

**Como corrigir.** Três mudanças, em ordem de importância:

```go
// comparador.go — so quem e servico publica. O modo de terminal existe para
// olhar, nao para assumir o lugar de quem esta atendendo.
ehServico := pronto != nil
if ehServico {
	if err := publicaAnuncio(porta, segredo); err != nil {
		registraErro("comparador: publicar anuncio: %v", err)
	} else {
		registraInfo("comparador: anunciado em %s", caminhoAnuncio())
	}
	defer removeAnuncio()
} else {
	registraInfo("comparador: modo terminal, nao publica anuncio (porta %d)", porta)
	fmt.Println("modo terminal: o anuncio em", caminhoAnuncio(), "nao foi alterado.")
}
```

```go
// anuncio.go — e removeAnuncio nunca apaga anuncio de outro processo
func removeAnuncio() {
	if a, err := leAnuncio(); err == nil && a.PID != os.Getpid() {
		registraInfo("anuncio pertence ao pid %d, nao ao meu (%d): nao removo", a.PID, os.Getpid())
		return
	}
	if err := os.Remove(caminhoAnuncio()); err != nil && !os.IsNotExist(err) {
		registraErro("remover anuncio do comparador: %v", err)
	}
}
```

(Este é também o primeiro consumidor útil do campo `PID`, que hoje é gravado e
nunca lido — o **A5 do #18**.)

E o silêncio do agente precisa acabar de qualquer maneira, porque ele esconde
muito mais do que este crítico:

```go
// delegacao.go — ausencia de anuncio nao e erro, mas nunca e detalhe
if err != nil {
	if base != "" || token != "" {
		registraErro("COMPARADOR_URL/TOKEN incompletos e sem anuncio utilizavel (%v): a comparacao continua local", err)
	} else {
		registraInfo("sem anuncio de comparador (%v): a comparacao sera local neste processo", err)
	}
	return
}
```

Junto com o **A7 do #12** (checar o gancho da FabulaTech no arranque), essas duas
linhas transformam a falha silenciosa mais cara do sistema numa linha de log que
diz o que fazer.

---

## 🟡 Alertas (recomenda correção)

### A1. O MSI abandonou a exigência de assinatura que o instalador antigo trata como obrigatória — e os dois scripts citados nas mensagens de erro não existem *(novo)*

**Arquivos:** `instalador/instalar-servidor.ps1:127`,
`instalador/instalar-servidor.ps1:134-140`, `instalador/msi/build-msi.cmd:26-61`,
`instalador/msi/AgenteBiometria.wxs:66-100`

O instalador antigo recusa binário não assinado:

```powershell
# instalar-servidor.ps1:134-136
$assinatura = Get-AuthenticodeSignature -LiteralPath $origem
if ($assinatura.Status -ne 'Valid' -and -not $PermitirNaoAssinado) {
    throw "Assinatura Authenticode invalida: $($assinatura.Status). Assine com Assinar.ps1 ou use -PermitirNaoAssinado somente em laboratorio."
}
```

O `build-msi.cmd` compila, embute o ícone e empacota (`build-msi.cmd:26-61`)
**sem assinar nada**, e o `.wxs` instala o `.exe` em `Program Files (x86)` e o
registra como serviço `LocalSystem` (`AgenteBiometria.wxs:71-81`) sem qualquer
verificação. A regra que existia no caminho antigo desapareceu no caminho que o
substituiu, e ninguém decidiu isso: ela simplesmente não foi transportada.

Isso importa além do princípio. Sem Authenticode, o binário não é verificável
depois da instalação, não passa por política de AppLocker/WDAC baseada em
publicador, e — junto com o **C3 do #18**, em que dois pacotes diferentes se
anunciam como `1.2.0` — não sobra nenhuma forma de responder "este exe no
servidor é o que nós publicamos?".

Some-se a isso que **`Assinar.ps1` não existe na árvore**, assim como
`Compilar-Go.ps1`, citado três linhas antes (`instalar-servidor.ps1:127`, já
apontado no **A3 do #18**). As duas mensagens que o operador lê quando o script
falha mandam rodar coisas que não estão no repositório.

**Como corrigir.** Assinar no `build-msi.cmd`, entre o ícone e o empacotamento,
e assinar também o MSI:

```bat
rem build-msi.cmd — depois de embutir o icone
if not "%CERT_THUMBPRINT%"=="" (
    signtool sign /sha1 %CERT_THUMBPRINT% /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 AgenteBiometria.exe
    if errorlevel 1 ( echo Falhou a assinatura do exe. & exit /b 1 )
) else (
    echo AVISO: CERT_THUMBPRINT nao definido. Pacote NAO ASSINADO, so para laboratorio.
)
```

E, ou criar o `Assinar.ps1` que as duas mensagens prometem, ou trocar as
mensagens pelo comando real.

### A2. 64 conexões meio-abertas deixam o agente inalcançável, e cada conexão descartada some sem uma linha de log *(novo)*

**Arquivos:** `cert.go:161-174`, `cert.go:185-213`, `cert.go:215-240`

A escuta mista aceita, mas atende no máximo 64 apertos de mão simultâneos, e
descarta o excedente calada:

```go
// cert.go:206-211
select {
case m.sem <- struct{}{}:
	go m.espia(c)
default:
	_ = c.Close()          // <- conexao recusada, sem registro nenhum
}
```

```go
// cert.go:215-226 — cada slot fica preso ate 10 segundos esperando o 1o byte
func (m *listenerMista) espia(c net.Conn) {
	defer func() { <-m.sem }()
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		...
	}
	br := bufio.NewReader(c)
	b, err := br.Peek(1)     // <- bloqueia aqui ate o byte chegar ou o prazo vencer
	...
}
```

Qualquer processo local que abra 64 conexões e não envie byte nenhum ocupa os 64
slots por 10 segundos, renovando indefinidamente. Enquanto isso, o `fetch()` da
página recebe conexão fechada; o cliente JS trata como `erroConexao`
(`integra-biometria.js:51-57`), marca `reconectavel`, e cai em `descobrir()`, que
varre 100 portas × 2 protocolos em série (`integra-biometria.js:179-189`) — ou
seja, a resposta ao ataque é a operação mais cara que o cliente tem.

O ponto que mais dói não é o ataque, que exige um processo local já rodando como
o usuário. É que **um programa local mal-comportado, um scanner de porta ou um
antivírus que sonda a 5000 produzem exatamente o mesmo sintoma**, e não há uma
linha em lugar nenhum ligando "a biometria parou" a "as conexões estão sendo
recusadas na porta". Note ainda que essa camada só existe quando o TLS sobe
(`cert.go:161-163` devolve o listener cru quando `cfg == nil`), então o mesmo
servidor se comporta de dois jeitos conforme o `certutil` tenha funcionado ou não
— o que é o **A1 do #13**, ainda aberto.

**Como corrigir.** Baixar o prazo do primeiro byte, que num `fetch()` de
`localhost` chega em milissegundos, e registrar a saturação com amortecimento:

```go
// cert.go — 2s bastam para localhost; 10s so ajudam quem nao vai enviar nada
if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {

// cert.go — e a recusa precisa aparecer, no maximo uma vez por minuto
default:
	_ = c.Close()
	if time.Since(m.ultimoAviso) > time.Minute {
		m.ultimoAviso = time.Now()
		registraErro("escuta cheia: 64 conexoes em aperto de mao, recusando novas")
	}
```

### A3. O token da sessão viaja na linha de comando do `rundll32` e cai na telemetria de segurança do servidor *(novo)*

**Arquivos:** `main.go:643-653`, `main.go:782-784`

```go
// main.go:652 — o token entra na URL
return fmt.Sprintf("%s%sbioPort=%d&bioToken=%s", base, separador, porta, token)
```

```go
// main.go:782 — e a URL vira argumento de um processo
if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", urlSistema()).Start(); err != nil {
```

O fragmento (`#`) não é enviado ao servidor web, e isso está certo. Mas a linha
de comando de um processo é outra história: ela é lida por qualquer coisa com
`PROCESS_QUERY_LIMITED_INFORMATION` no servidor, e — o que importa mais na
prática — **é justamente o campo que Sysmon (evento 1), o log de auditoria de
criação de processo (4688 com auditoria de linha de comando ligada) e qualquer
EDR coletam e enviam para fora da máquina**, onde ficam retidos por meses.

O token dá acesso a `/api/public/v1/captura/*` e a `/api/public/v1/identificar`
daquela sessão. Ele morre quando o agente reinicia (`main.go:902`), o que limita
a janela — mas num RDS a sessão dura semanas, e o log de segurança dura mais.

**Como corrigir.** Não passar segredo por argumento. O `bioPort` sozinho já
basta para o cliente achar o agente, e o `/api/hello` entrega o token pelo canal
que foi feito para isso:

```go
// main.go — a URL leva so a porta; o token vem do /api/hello, com checagem de
// sessao e de origem autorizada, que e o caminho para o qual ele foi desenhado.
func urlSistema() string {
	base := os.Getenv("SISTEMA_URL")
	if base == "" {
		return ""
	}
	separador := "#"
	if strings.Contains(base, "#") {
		separador = "&"
	}
	return fmt.Sprintf("%s%sbioPort=%d", base, separador, porta)
}
```

O cliente JS já sabe fazer isso: `garantirConexao()` chama `descobrir()`, que
chama `/api/hello` e guarda o token (`integra-biometria.js:144-151`). O
`bioToken` do fragmento é um atalho que custa mais do que economiza. Como efeito
colateral, some também a metade do **C3 de 30/07** que trata do `bioToken`.

### A4. `/api/status` é o único chamador do SDK sem prazo próprio — o endpoint que o sistema web usa para perguntar "está tudo bem?" pode ficar preso o tempo inteiro de uma identificação *(novo)*

**Arquivos:** `main.go:312-322`, `main.go:107-138`, `main.go:63`, `main.go:252-260`,
`main.go:356-364`, `main.go:543`

Todos os caminhos que tocam o SDK trazem o próprio prazo, menos um:

```go
// main.go:363 — captura: contexto explicito, folgado em relacao ao worker
ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Millisecond+25*time.Second)

// main.go:432 — comparacao: 45s
ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)

// main.go:543 — identificacao: 4 minutos
ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
```

```go
// main.go:316 — status: o contexto da requisicao, cru
n, err := naThreadSDK(r.Context(), func() (uint32, error) {
```

A fila do SDK tem 16 posições (`main.go:63`) e uma via só. Com uma identificação
1:N ocupando a thread por até 3 minutos — que é o **C4 de 30/07**, aberto — a
tarefa do `/api/status` espera atrás dela **sem prazo nenhum**, e enquanto espera
segura um dos 32 slots de `limiteHTTP` (`main.go:252-260`). O contexto da
requisição só cai quando o navegador desiste; um `fetch()` sem `AbortController`
não desiste.

O `/api/ping` não sofre disso, porque não toca no SDK (`main.go:306-310`) — mas
por isso mesmo ele não responde a pergunta. É o `/api/status` que o painel de
suporte consulta, e é ele que trava.

Isto **soma-se** ao **C2 do #18** (o `/api/status` responde `ok` sem sondar o
comparador), não o substitui: lá o defeito é o que a resposta afirma; aqui é ela
não chegar.

**Como corrigir.** Status é pergunta de tela, não de atendimento:

```go
// main.go, no comeco de handleStatus — melhor "nao sei em 3s" do que travar
ctx, cancela := context.WithTimeout(r.Context(), 3*time.Second)
defer cancela()
n, err := naThreadSDK(ctx, func() (uint32, error) {
	...
})
```

E o erro de prazo merece texto próprio, porque ele diz uma coisa útil e
diferente de "não há leitor":

```go
if errors.Is(err, context.DeadlineExceeded) {
	info["ok"] = false
	info["erro"] = "o leitor esta ocupado com outra operacao; tente de novo em instantes"
	escreveJSON(w, http.StatusServiceUnavailable, info)
	return
}
```

### A5. O arquivo de descoberta não se multiplica por sessão, e sim por **reconexão** — o `SESSIONNAME` do RDP carrega um contador *(novo; complementa o A5 do #17)*

**Arquivos:** `main.go:615-641`

```go
// main.go:620-632
sessao := os.Getenv("SESSIONNAME")
if sessao == "" {
	sessao = "console"
}
segura := strings.Map(func(r rune) rune {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
		return r
	}
	return -1
}, sessao)
...
return gravaArquivoAtomico(filepath.Join(dir, "agente-"+segura+".json"), append(dados, '\n'), 0o600)
```

Num servidor RDS o `SESSIONNAME` não é estável: ele vem como `RDP-Tcp#12`,
`RDP-Tcp#13`, e o número **muda a cada reconexão**, porque identifica a conexão e
não a sessão. O `strings.Map` remove o `#`, então cada reconexão gera um arquivo
novo: `agente-RDP-Tcp12.json`, `agente-RDP-Tcp13.json`, `agente-RDP-Tcp14.json`.

O **A5 do #17** já registrou que nada apaga esses arquivos no encerramento. O que
ele assumiu — e o que muda o tamanho do problema — é que haveria um por sessão.
São um por **reconexão**: num usuário que entra e sai algumas vezes por dia, são
dezenas por mês, cada um com uma porta e um token em claro, acumulando no perfil
indefinidamente. E como o nome muda, nem sobrescrever o anterior acontece.

**Como corrigir.** A correção do #17 (apagar no encerramento) continua sendo a
principal, mas ela não cobre o agente derrubado sem saída limpa. Vale somar uma
limpeza no arranque:

```go
// main.go, junto de escreveConfig — o arquivo de uma conexao que ja passou nao
// serve para ninguem, e o processo que o escreveu nao existe mais.
func limpaConfigsVelhas(dir string) {
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entradas {
		nome := e.Name()
		if !strings.HasPrefix(nome, "agente-") || !strings.HasSuffix(nome, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < 24*time.Hour {
			continue
		}
		_ = os.Remove(filepath.Join(dir, nome))
	}
}
```

---

## 🟢 Sugestões (opcional)

### S1. `ne.Temporary()` está obsoleto e não é confiável para decidir retentativa

```go
// cert.go:190
if ne, ok := err.(net.Error); ok && ne.Temporary() {
```

`net.Error.Temporary()` está marcado como *deprecated* desde o Go 1.18
justamente por não ter significado bem definido — a implementação decide sozinha
o que é temporário, e para vários erros de `Accept` ela responde `false` onde o
recuo com espera seria o certo. O `go vet` não avisa; o `staticcheck` (SA1019)
avisaria, e é mais um argumento para o CI do **A8 de 30/07**. O padrão atual da
biblioteca é recuar em qualquer erro de `Accept` que não seja `net.ErrClosed`:

```go
if errors.Is(err, net.ErrClosed) {
	m.falha(err)
	return
}
// qualquer outro erro de Accept: recua e tenta de novo
```

### S2. `listaDispositivos()` já existe e responderia "qual leitor", que é a pergunta do próprio comentário

`sdk.go:231-236` explica que a contagem sozinha não distingue um modelo de outro
e que "o leitor é o mesmo?" virou a pergunta central. A função existe, funciona,
e é usada só pelo autoteste (`autoteste.go:200`). O `/api/status` continua
respondendo apenas `dispositivos: n` (`main.go:340`). Levar os IDs para a
resposta custa uma linha e responde, de fora, a pergunta que hoje só se responde
indo até a máquina.

### S3. Um `origens-autorizadas.json` com carimbo de sessão tornaria o C1 auto-evidente

Mesmo que a correção escolhida para o C1 seja a união na gravação, gravar quem
escreveu por último transforma um bug silencioso em algo diagnosticável:

```json
{ "origens": ["https://sistema.exemplo"], "gravado_por": {"sessao": "RDP-Tcp#12", "pid": 4812, "em": "2026-08-16T09:14:02Z"} }
```

O formato hoje é um array puro (`origins.go:150-155`), e `carrega()`
(`origins.go:79-82`) desiste em silêncio se o JSON não for um array — o que é o
**A4 do #17**. Trocar o formato exige tratar os dois, e vale fazer as duas coisas
no mesmo commit.

### S4. `souServico()` já sabe distinguir os dois modos e podia decidir mais coisas

O C2 propõe usar `pronto != nil` para saber se o comparador é serviço. O sistema
já tem uma resposta melhor e mais direta em `servico.go:87-94`, e ela é a mesma
que `executa()` usa para escolher o caminho (`main.go:868`). Guardar esse
resultado numa variável de processo, decidida uma vez no arranque, deixaria o
comparador tomar as três decisões que hoje ele toma por dedução: publicar ou não
o anúncio, escrever ou não na tela, e onde gravar o log — inclusive o do worker,
que continua indo para o perfil do SYSTEM (**A1 do #14**, aberto).

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 33 (22 `.go`, 1 `.js`, 1 `.ps1`, 3 do MSI, 2 `.cmd`, 1 `.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e 2 docs)
- **Segurança**: 🚨 **Risco** — o C1 de hoje descreve um controle de segurança (a autorização de origem) que pode ser desfeito sozinho; o A3 põe o token da sessão na telemetria; e nenhum dos críticos de dado pessoal (**C1/C2 do #17**), de `ProgramData` (**C2 do #13**) ou de agente impostor na 5000 (**C1 do #15**) foi tocado
- **Qualidade**: ⚠️ Atenção — `build` e `vet` limpos; o ponto fraco desta revisão é o comportamento com mais de uma instância viva, que nenhum teste cobre
- **Risco de produção**: 🚨 **Alto** — o C2 descreve uma ação de diagnóstico rotineira que desliga a biometria do servidor inteiro e não deixa vestígio
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que não rodam em alvo nenhum (`matched no packages` no host, `exec format error` em `windows/386`) e nenhum CI. **A8 de 30/07, aberto há 17 dias**

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
| 16 | PR #18 | Nada confere versão; agente velho volta a comparar na jaula | **ABERTO** |
| 17 | PR #18 | `/api/status` diz OK sem sondar o comparador | **ABERTO** |
| 18 | PR #18 | `VERSAO` fixa: `MajorUpgrade` não dispara | **ABERTO** |
| 19 | **hoje** | **Estado por usuário compartilhado entre sessões: revogação desfeita** | **NOVO** |
| 20 | **hoje** | **`--comparador` de diagnóstico apaga o anúncio de produção** | **NOVO** |

---

## ✅ Pontos positivos

**O anúncio já carrega tudo o que falta ler.** Os dois críticos de hoje têm a
mesma correção de base, e ela é barata porque o dado certo já foi gravado:
`publicaAnuncio` (`anuncio.go:82-96`) põe `PID`, `Versao` e `Desde` no arquivo
desde o começo. O C2 se fecha comparando o `PID` do anúncio com o próprio antes
de apagar — uma condição de três linhas, sobre um campo que já existe. É o tipo
de coisa que só é barata porque alguém, meses antes, gravou mais do que precisava
naquele momento.

**A separação entre "não confere" e "quebrou" é respeitada em todas as
camadas.** `delegacao.go:163-171` recusa uma resposta em que `confere` e `id` se
contradizem em vez de escolher um; `sdk.go:512-522` distingue o candidato com
checksum ruim (pula) de uma falha do SDK (aborta); `handleComparar`
(`main.go:449-452`) devolve `502`, e não `false`, quando a comparação não pôde ser
feita; e `confereContra` separa isso em códigos de saída diferentes
(`autoteste.go:420-424`). Num sistema biométrico é a distinção que mais importa e
a mais fácil de perder no caminho, e ela sobreviveu a quatro camadas de processo.

**A escuta mista resolve um problema real sem truque.** `cert.go:215-240` espia
um byte para decidir entre TLS e texto na mesma porta, e o faz sem duplicar
listener, sem porta extra e sem heurística frágil — `0x16` é o primeiro byte de
um `ClientHello` e nunca é o de um método HTTP. O `connEspiada` devolve o byte
espiado ao fluxo em vez de descartá-lo, que é onde implementações parecidas
costumam errar. O alerta A2 é sobre o orçamento dessa camada, não sobre o
desenho dela.

**O trabalho de campo continua entrando no código como comentário, não como
lenda.** `worker.go:18-23` explica por que `recover()` não salva o processo;
`delegacao.go:5-23` explica por que a delegação existe e por que é opcional;
`sdk.go:322-325` explica por que `NBioAPI_FreeTextFIR` só é chamado depois de o
SDK entregar um ponteiro. Esta revisão foi rápida por causa disso: o código diz
onde estão as fronteiras, e sobra tempo para perguntar o que atravessa cada uma.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O sistema chega hoje a **20 críticos abertos** e **dez dias** sem um commit de
correção. Os dois de hoje têm em comum uma coisa que as revisões anteriores não
tinham tocado: **o sistema não sabe que existe mais de um dele**. Um agente
sobrescreve a lista de sites que o outro revogou; um comparador de terminal
apaga o anúncio do comparador de produção. Nos dois casos o processo que causa o
estrago acha que está fazendo o certo, o que causa o estrago não deixa registro,
e o efeito só aparece muito depois, num lugar que não aponta para a causa.

A sequência recomendada muda pouco em relação à do #18, e ganha um item barato
na frente:

1. **C2 (parte do log)** — as três linhas de `delegacao.go:58-65` que fazem o
   agente dizer "não há comparador anunciado, vou comparar localmente". É o
   menor diff do repositório inteiro e ilumina, de uma vez, este crítico, o
   **C1 do #10**, o **A3 do #10** e o **A1 do #13**.
2. **C2 (resto)** — `removeAnuncio` conferindo o `PID`, e o modo de terminal
   deixando de publicar. Fecha o caminho pelo qual um diagnóstico derruba a
   produção.
3. **C3 do #18** — versão derivada do repositório e `AllowSameVersionUpgrades`,
   agora com a assinatura do **A1** de hoje junto. Sem os dois não há como
   afirmar que uma máquina recebeu uma correção nem que o que ela recebeu é o
   que foi publicado.
4. **C1** — a união na gravação de `origens-autorizadas.json`, ou o diretório
   por sessão. É o único dos abertos em que o sistema desfaz sozinho uma decisão
   de segurança que o usuário tomou.
