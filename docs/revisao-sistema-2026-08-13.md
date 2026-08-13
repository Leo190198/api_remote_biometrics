# 🔍 Revisão técnica do sistema — 2026-08-13

> ⚠️ **`main` continua em `26c9379`.** Os PRs **#10**, **#11**, **#12**, **#13**,
> **#14** e **#15** seguem abertos e nenhum crítico foi tocado. Este é o **sétimo
> dia consecutivo**, e a primeira semana fechada sem um único commit de correção.

As seis revisões anteriores cobriram o caminho de dados (#10, #11), a escala do
comparador (#12, #13), a fronteira com a pessoa (#14) e o aperto de mão com o
sistema web (#15). Restaram três regiões que nenhuma delas abriu, e as três têm
o mesmo perfil: **são os caminhos que só existem quando alguma coisa já deu
errado** — o TLS quando o ambiente não colabora, a linha de comando quando
alguém foi diagnosticar, e a retentativa da tabela TCP quando a primeira leitura
não serviu.

É onde esta revisão entra. O resultado é **nenhum crítico novo** — e isso não é
uma boa notícia, é a constatação de que os sete que já existem continuam sendo o
problema — mais **seis alertas novos**, todos verificados contra o código, e um
deles com um efeito direto no cliente web que o **A2 do #15** não alcançou.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, incluindo os 7 de teste
e as 48 funções de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/build-msi.cmd`,
`instalador/msi/AgenteBiometria.wixproj`, `conferir-biometria.cmd`,
`embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`, `README.md` e os documentos
em `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, inclusive nos arquivos de teste |
| `go test ./...` | **não executável aqui** — `matched no packages`: as *build tags* `windows && 386` excluem todos os arquivos. Segue valendo o **A8 do #10** (sem CI) e o **A1 do #15** (a suíte tem um defeito próprio que ninguém consegue ver) |
| Contagem de arquivos e testes | 4.965 linhas em 22 `.go`; 48 funções `Test*` em 7 arquivos |
| Conferência de `main` | `26c9379`, idêntico ao de 2026-08-07 |

---

## 🔴 Problemas Críticos (bloqueia merge)

**Nenhum crítico novo hoje.** Os sete que já existiam foram reconferidos linha a
linha contra `26c9379` e **todos continuam abertos**. Não repito os textos — eles
estão nos PRs indicados e continuam corretos nas mesmas linhas.

| Achado | Onde | Situação |
|---|---|---|
| **C1 #15** — a página não distingue o agente de um impostor: `escolheListener` pega a primeira porta livre e `descobrir()` aceita quem responder primeiro; o impostor recebe os templates de cadastro | `main.go:591-596`, `integra-biometria.js:179-188`, `144-151` | ❌ Aberto — e **agravado** pelo A4 de hoje |
| **C1 #14** — `tituloOrigem` corta justamente o domínio que decide; o diálogo da bandeja é forjável de qualquer site | `main.go:655-661`, `696-703` | ❌ Aberto |
| **C2 #14** — pendência expirada nunca sai da bandeja; `aprova()` não consulta `pendentes` | `main.go:663-744`, `origins.go:131-138` | ❌ Aberto |
| **C1 #13** — fila única do SDK no comparador; sem `limiteHTTP` no modo serviço | `comparador.go:75-79`, `101-111`, `main.go:100-105` | ❌ Aberto |
| **C2 #13** — ACL herdada do `ProgramData` + `endereco` do anúncio sem checagem de loopback | `wxs:57-62`, `123-127`, `delegacao.go:74-85` | ❌ Aberto |
| **C1 #12** — parar o serviço apaga o anúncio; token novo derruba os agentes de pé | `comparador.go:99`, `anuncio.go:113-121` | ❌ Aberto |
| **C4 #12** — MSI e `instalar-servidor.ps1` disputam nome e porta | `wxs:71-99`, `instalar-servidor.ps1:167-193` | ❌ Aberto |

**O que mudou, então?** Só o tempo. E o tempo aqui não é neutro: o **C1 #14** e o
**C2 #14** são os únicos alcançáveis **sem nenhum acesso à máquina**, custam ~25
linhas somadas e estão parados há três dias. O **C2 #13** é o que impede template
biométrico de sair da máquina e está parado há quatro. Uma revisão que só
acrescenta achados novos sobre uma base que não se move deixa de ser útil em
algum momento; este é o ponto em que vale dizer isso em voz alta.

---

## 🟡 Alertas (recomenda correção)

### A1. Falhar ao *confiar* no certificado desliga o TLS inteiro — o agente cai para HTTP em claro, em silêncio *(novo)*

**Arquivos:** `cert.go:125-141`, `cert.go:115-123`, `main.go:908-912`,
`main.go:300-303`, `integra-biometria.js:19-23`

`carregaTLS()` trata a instalação do certificado na loja do usuário como
obrigatória para *servir* TLS:

```go
// cert.go:125-141
func carregaTLS() (*tls.Config, error) {
	if err := gerarCert(); err != nil {
		return nil, err
	}
	certPath, keyPath := caminhosCert()
	if err := instalaCertificadoUsuario(certPath); err != nil {
		return nil, err          // <- aborta o TLS por causa do certutil
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	...
}
```

```go
// cert.go:116 — o passo que pode falhar
cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
```

E o chamador aceita a recusa e segue:

```go
// main.go:908-912
tlsCfg, err := carregaTLS()
if err != nil {
	registraErro("TLS desabilitado: %v", err)
}
usaTLS = tlsCfg != nil        // <- false
```

**Por que é um problema.** Instalar o certificado na loja `Root` do usuário serve
para **uma coisa só**: tirar o aviso do navegador. Não é necessário para o
handshake — o par `cert.pem`/`key.pem` já está gravado, já foi validado por
`certificadoValido` (`cert.go:95` e `108`) e `tls.LoadX509KeyPair` carregaria sem
reclamar. O código faz um passo cosmético derrubar um controle de segurança.

E a falha do `certutil.exe` não é hipotética justamente no ambiente em que este
agente roda. `certutil.exe` é um binário de sistema notoriamente usado para abuso
e é dos primeiros a entrar em regra de bloqueio de AppLocker/SRP em servidor RDS
endurecido. Basta isso, ou uma GPO que tranque a loja `Root` do usuário, para
que **todas as sessões daquele servidor passem a atender em HTTP puro** —
enquanto a instalação continua parecendo correta.

O efeito não para no loopback:

1. `usaTLS` alimenta `handleHello` (`main.go:302`, campo `https`) e
   `escreveConfig` (`main.go:635`), então o próprio agente **anuncia** que não
   tem TLS;
2. `protocolos()` (`integra-biometria.js:19-23`) já tenta `http` primeiro quando
   a página é `http`, e agora não há sequer um `https` para achar depois;
3. o **C1 do #15** fica mais barato: a medida paliativa proposta lá — *"não
   aceitar em claro o que o agente oferece cifrado"* — deixa de existir, porque o
   agente legítimo passa a oferecer em claro também.

Nada disso aparece para quem opera. O binário é `-H windowsgui`, não há console,
e o único vestígio é **uma linha** em `agente.log` dizendo `TLS desabilitado`.

**Como corrigir.** Inverter a ordem e degradar só o que de fato falhou:

```go
// cert.go
func carregaTLS() (*tls.Config, error) {
	if err := gerarCert(); err != nil {
		return nil, err
	}
	certPath, keyPath := caminhosCert()
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	// Instalar na loja Root do usuario so evita o aviso do navegador; o
	// handshake nao depende disso. Cair para HTTP em claro por causa do
	// certutil e trocar um aviso visivel por um canal sem protecao nenhuma.
	if err := instalaCertificadoUsuario(certPath); err != nil {
		registraErro("o certificado nao pode ser confiado no perfil do usuario (%v); "+
			"o TLS continua ligado e o navegador vai avisar na primeira conexao", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
```

E, já que `usaTLS` é o que a página lê para decidir, vale expor a diferença entre
"sem TLS" e "com TLS não confiado" em `/api/status` — são dois chamados de
suporte completamente diferentes.

### A2. `descobrir()` desiste da varredura inteira na primeira porta que responder "pendente" — 60 s de espera e nenhum agente, sem privilégio nenhum *(novo)*

**Arquivos:** `integracao/integra-biometria.js:179-189`, `153-165`, `191-195`,
`main.go:293-299`

O laço da descoberta trata "não é ele" e "é ele, mas pendente" de formas
opostas — e só o primeiro continua:

```js
// integra-biometria.js:182-188
for (var p = inicio; p <= fim; p++) {
  var achado = await hello(p)
  if (!achado) continue                                 // porta descartada, segue
  if (!achado.pendente) return conecta(achado)
  return aguardaAutorizacao(achado, 60000)              // <- RETURN, nao continue
}
```

E `aguardaAutorizacao` fica preso naquela porta, e só naquela:

```js
// integra-biometria.js:158-164
var fim = Date.now() + limiteMs
while (Date.now() < fim) {
  await espera(1000)
  var atual = await hello(achado.dados.porta, achado.proto)   // sempre a mesma porta
  if (atual && !atual.pendente) return conecta(atual)
}
return null
```

**Por que é um problema.** São dois efeitos, e o segundo é o que importa.

**No caso benigno:** o usuário que **recusa** a autorização na bandeja (ou que
simplesmente não vê o balão) não recebe um "não autorizado". Ele recebe 60
segundos de nada e depois `garantirConexao()` devolve `false`
(`integra-biometria.js:191-195`), o que a aplicação traduz como
*"Nao foi possivel falar com o agente em ..."*. A resposta certa — *"você precisa
autorizar este site no ícone da bandeja"* — existe no protocolo (`202` com
`autorizacao: "pendente"`, `main.go:293-299`) e é jogada fora pelo cliente.

**No caso hostil:** para negar biometria a uma estação, um processo local **sem
privilégio nenhum** não precisa nem falsificar um agente. Basta ocupar a porta
5000 e responder:

```
HTTP/1.1 202 Accepted
Content-Type: application/json

{"ok":false,"autorizacao":"pendente","porta":5000,"sessao":"RDP-Tcp#7"}
```

`hello()` classifica isso como `pendente: true` (`integra-biometria.js:127-129`),
`descobrir()` faz `return` e **nunca chega em 5001**, onde o agente de verdade
está atendendo. Cada tentativa custa 60 segundos ao operador, indefinidamente.
Isto é diferente do **C1 do #15**: lá o impostor precisa devolver um token e
sustentar o papel de agente para receber os templates; aqui ele não precisa de
nada além de dizer "pendente", e o alvo não é o dado — é o atendimento.

O **A2 do #15** cobriu o outro ramo deste mesmo laço (o `if (!achado) continue`,
e as respostas transitórias que fazem a porta ser riscada). Este é o ramo que
sobrou, e é o único dos dois em que a varredura **para**.

**Como corrigir.** Anotar a pendência e terminar a varredura antes de esperar por
ela — o agente real pode estar três portas adiante:

```js
// integra-biometria.js, em descobrir()
descobrir: async function (inicio, fim) {
  inicio = inicio || 5000
  fim = fim || 5099
  var pendente = null
  for (var p = inicio; p <= fim; p++) {
    var achado = await hello(p)
    if (!achado) continue
    if (!achado.pendente) return conecta(achado)
    // Guarda e segue: uma porta que responde "pendente" nao prova ser o
    // agente desta sessao, e o agente de verdade pode estar adiante.
    if (!pendente) pendente = achado
  }
  if (!pendente) return null
  return aguardaAutorizacao(pendente, 60000)
},
```

E distinguir a espera do fracasso, para a aplicação poder dizer a coisa certa:

```js
garantirConexao: async function () {
  if (token() && (await this.disponivel())) return true
  var r = await this.descobrir()
  if (!r && this.ultimaPendencia) {
    throw new Error('Autorize este site no icone do Agente de Biometria na ' +
      'bandeja (menu "Autorizar acesso") e tente novamente.')
  }
  return token() !== ''
},
```

### A3. Qualquer argumento que o `executa()` não reconheça sobe o agente da bandeja em silêncio — inclusive `--conferir-contra` sem arquivo *(novo)*

**Arquivos:** `main.go:842-884`, `conferir-biometria.cmd:39`

O despacho de linha de comando é uma escada de `if`, sem `else` final:

```go
// main.go:843-876
if len(os.Args) > 1 && os.Args[1] == "--gerar-cert"        { ... }
if len(os.Args) > 1 && os.Args[1] == "--autoteste"         { ... }
if len(os.Args) > 2 && os.Args[1] == "--conferir-contra"   { ... }   // <- exige 2 args
if len(os.Args) > 1 && os.Args[1] == "--teste-delegacao"   { ... }
if os.Getenv("BIO_WORKER") == "1"                          { ... }
if souServico()                                            { ... }
if os.Getenv("MODO_COMPARADOR") == "1" || ... "--comparador" { ... }
if os.Getenv("BIO_FILHO") != "1" {
	if !instanciaUnica() {
		return 0                 // <- sai calado
	}
	supervisor()                 // <- sobe o agente da bandeja
	return 0
}
```

**Por que é um problema.** `AgenteBiometria.exe --conferir-contra` — sem o
caminho do arquivo — falha a condição `len(os.Args) > 2`, **atravessa a escada
inteira** e cai no último bloco. O que acontece então depende do que já está
rodando:

| Estado da máquina | O que o operador queria | O que acontece |
|---|---|---|
| Nenhum agente na sessão | conferir uma biometria | **sobe o agente da bandeja** e fica rodando |
| Agente já rodando | conferir uma biometria | `instanciaUnica()` é falso → **`return 0`**, saída silenciosa com código 0 |

E vale para **todo** argumento não reconhecido: `--autotest` (o typo mais
provável), `--comparator`, `--help`, `-h`, `--versao`. Nenhum deles produz uma
linha de texto. O binário é `-H windowsgui`: sem console próprio e sem
`ligaConsole()` nesse caminho, **não há para onde a mensagem ir mesmo que
existisse**.

O segundo caso é o pior dos dois. Código de saída `0` significa "confere" no
contrato que o próprio projeto publicou (`autoteste.go:419-423` e
`conferir-biometria.cmd:49`). Um script de terceiros que chame
`--conferir-contra` com o caminho vazio — variável não expandida, `%~1`
ausente — recebe **`0` = "é a mesma pessoa"** sem que nenhuma digital tenha sido
lida. O `conferir-biometria.cmd` se protege disso (linhas 32-37, checa o arquivo
antes), mas o binário não, e ele é o que a integração chama.

**Como corrigir.** Um `switch` com `default`, e argumento obrigatório tratado
como erro e não como "não é comigo":

```go
// main.go, no topo de executa()
if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "-") {
	ligaConsole()
	switch os.Args[1] {
	case "--gerar-cert":
		...
	case "--autoteste":
		return rodaAutoteste()
	case "--conferir-contra":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "uso: AgenteBiometria.exe --conferir-contra <arquivo>")
			return 1        // nunca 0: 0 significa "confere"
		}
		return confereContra(os.Args[2])
	case "--teste-delegacao":
		return testeDelegacao()
	case "--comparador":
		return rodaComparador()
	default:
		fmt.Fprintf(os.Stderr, "argumento desconhecido: %s\n", os.Args[1])
		fmt.Fprintln(os.Stderr, "use --autoteste, --conferir-contra <arquivo>, "+
			"--teste-delegacao, --comparador ou --gerar-cert")
		return 2
	}
}
```

### A4. `--conferir-contra` compara no próprio processo e pode ser derrubado pela DLL — e aí o `.cmd` não imprime veredito nenhum *(novo)*

**Arquivos:** `autoteste.go:424-499`, `autoteste.go:343-358`,
`conferir-biometria.cmd:39-50`, `worker.go:18-23`

O `worker.go` existe por um motivo escrito em letras grandes no topo do arquivo:

```go
// worker.go:18-23
// A NBioBSP.dll e carregada exclusivamente pelo processo worker. Uma violacao
// de acesso dentro dela nao pode ser recuperada com recover(): (...) para uma
// falha dentro da DLL o Windows encerra o processo. Isolando o SDK, essa falha
// custa uma requisicao em vez do agente inteiro.
```

`confereContra` não usa o worker. Ele abre a DLL no próprio processo:

```go
// autoteste.go:458 -> 347-357
sdk, dll, err := abreSDKDireto()      // novoSDK(dll) neste processo
```

...e, quando não há comparador anunciado, avisa que vai quebrar e quebra assim
mesmo:

```go
// autoteste.go:451-455
if temGanchoDeRedirecionamento() {
	fmt.Println("  ATENCAO: ha gancho de redirecionamento neste processo e nenhum")
	fmt.Println("  comparador anunciado. A comparacao vai falhar ou derrubar o")
	fmt.Println("  processo. Instale o servico comparador nesta maquina.")
}
```

```go
// autoteste.go:478-484 — e segue em frente
if comparadorRemoto != nil {
	confere, err = comparadorRemoto.compara(ctx, guardado, lido)
} else {
	confere, err = sdk.comparaTextos(guardado, lido)     // <- pode matar o processo
}
```

**Por que é um problema.** Para o `--autoteste` isso é deliberado e bem feito: o
relatório é gravado linha a linha com `Sync()` (`autoteste.go:96-106`) justamente
para que a última linha aponte quem matou o processo. O `--conferir-contra`
**não tem relatório em disco** — ele escreve só em `stdout` com `fmt.Println` — e
não é uma ferramenta de diagnóstico do SDK: é a verificação 1:1 de produção,
empacotada para o operador como `conferir-biometria.cmd`.

Quando a DLL derruba o processo, a saída é `0xC0000005` = **3221225477**. E o
`.cmd` não tem ramo para isso:

```bat
rem conferir-biometria.cmd:44-50
if "%RC%"=="0" echo   RESULTADO: CONFERE - e a mesma pessoa.
if "%RC%"=="2" echo   RESULTADO: NAO CONFERE - dedo diferente ou leitura ruim.
if "%RC%"=="1" echo   RESULTADO: FALHOU - veja o erro acima. Nao e veredito
if "%RC%"=="1" echo              biometrico: nada foi comparado.
echo ----------------------------------------------------------
echo   codigo de saida: %RC%   ^(0 confere, 1 falhou, 2 nao confere^)
```

O operador vê o cabeçalho, três linhas de traço, e
`codigo de saida: -1073741819   (0 confere, 1 falhou, 2 nao confere)`. Nenhum dos
três rótulos. Exatamente no cenário — sessão RDP sem comparador — que o projeto
inteiro foi construído para tratar.

**Como corrigir.** Duas mudanças pequenas e independentes.

No Go, recusar em vez de tentar, já que o próprio código sabe o que vai
acontecer:

```go
// autoteste.go, em confereContra
if comparadorRemoto == nil && temGanchoDeRedirecionamento() {
	fmt.Println("FALHOU: ha gancho de redirecionamento neste processo e nenhum")
	fmt.Println("comparador anunciado. Comparar aqui derruba o processo sem")
	fmt.Println("veredito. Instale o servico comparador nesta maquina.")
	return 1        // 1 = falhou, nunca 2 = nao confere
}
```

E, no `.cmd`, fechar o conjunto para que nenhum código fique sem rótulo:

```bat
if "%RC%"=="0" goto :fim
if "%RC%"=="1" goto :fim
if "%RC%"=="2" goto :fim
echo   RESULTADO: O PROGRAMA FOI ENCERRADO SEM RESPONDER ^(codigo %RC%^).
echo              Nada foi comparado. Se o codigo for 3221225477, a
echo              NBioBSP.dll derrubou o processo: instale o servico
echo              comparador nesta maquina.
:fim
```

Vale considerar também mandar `confereContra` pelo `novoClienteWorker`, que é o
caminho que produção usa — o custo é um processo a mais e o ganho é que a queda
da DLL vira uma mensagem em vez de um sumiço.

### A5. A retentativa da tabela TCP faz a segunda sondagem com um tamanho velho e ponteiro nulo — e uma tabela que cresceu vira `503` *(novo)*

**Arquivos:** `session.go:54-73`, `main.go:279-283`

`tamanho` é declarado **fora** do laço e nunca volta a zero:

```go
// session.go:54-73
func pidDaConexao(portaOrigem, portaDestino uint16) (uint32, bool) {
	var tamanho uint32
	for tentativa := 0; tentativa < 4; tentativa++ {
		r, _, _ := procGetTCPTable.Call(0, uintptr(unsafe.Pointer(&tamanho)), 0, afInet, tcpTableOwnerPIDAll, 0)
		if r != errorInsufficientBuffer || tamanho < 4 {
			return 0, false                  // <- qualquer outra resposta encerra
		}
		buf := make([]byte, tamanho)
		r, _, _ = procGetTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&tamanho)),
			0, afInet, tcpTableOwnerPIDAll, 0)
		if r == errorInsufficientBuffer {
			continue                         // <- volta ao topo com tamanho ja grande
		}
		...
	}
	return 0, false
}
```

**Por que é um problema.** A primeira passagem funciona como se espera: `tamanho`
vale 0, o ponteiro é `NULL`, e `GetExtendedTcpTable` responde
`ERROR_INSUFFICIENT_BUFFER` com o tamanho necessário. É o padrão documentado.

A partir da **segunda** volta o padrão deixa de valer: o ponteiro continua
`NULL`, mas `tamanho` agora carrega o valor da volta anterior — que pode já ser
maior ou igual ao necessário. O laço só continua correto se a sondagem **insistir
em devolver `ERROR_INSUFFICIENT_BUFFER`** mesmo com um tamanho suficiente, e não
há nada — nem no código, nem em comentário — que sustente essa premissa. Se ela
não valer, a primeira linha do laço cai no `return 0, false`.

E a consequência desse `false` está a duas chamadas de distância do usuário:

```go
// main.go:279-283
mesma, ok := mesmaSessao(uint16(p), uint16(porta))
if !ok {
	escreveErro(w, http.StatusServiceUnavailable, "chamador nao identificado")
	return
}
```

`503` em `/api/hello` faz `hello()` devolver `null` e a porta ser riscada da
varredura para sempre (**A2 do #15**). Ou seja: o momento em que a retentativa
existe — tabela TCP mudando de tamanho entre duas chamadas, que num RDS com
dezenas de sessões é o momento *normal* — é exatamente o momento em que ela pode
não retentar. É a mesma classe do **A3 do #15**: o endpoint mais barato de
alcançar é o mais caro e o mais frágil de atender.

**Como corrigir.** Uma linha. A sondagem tem que ser sempre a sondagem:

```go
for tentativa := 0; tentativa < 4; tentativa++ {
	// Zera antes de cada sondagem: com ponteiro nulo, o contrato de
	// GetExtendedTcpTable so vale quando o tamanho informado e insuficiente.
	tamanho = 0
	r, _, _ := procGetTCPTable.Call(0, uintptr(unsafe.Pointer(&tamanho)), ...)
	...
}
```

E, já que estamos aqui, registrar por que desistiu — hoje as quatro saídas de
`pidDaConexao` são indistinguíveis no log, que não recebe nenhuma delas:

```go
registraErro("tabela TCP: GetExtendedTcpTable devolveu %d na tentativa %d", r, tentativa)
```

### A6. Cada renovação do certificado deixa a anterior confiada na loja `Root` do usuário, e a desinstalação por MSI não menciona nenhuma delas *(novo)*

**Arquivos:** `cert.go:31-54`, `cert.go:92-113`, `cert.go:115-123`,
`instalador/msi/AgenteBiometria.wxs:123-127`,
`instalador/instalar-servidor.ps1:117`

`certificadoValido` renova 30 dias antes do vencimento de 2 anos:

```go
// cert.go:47
if agora.Before(cert.NotBefore) || agora.Add(30*24*time.Hour).After(cert.NotAfter) {
	return errors.New("certificado expirado ou proximo da expiracao")
}
```

`gerarCert()` então gera **chave nova e serial novo** (`cert.go:56-90`), e
`instalaCertificadoUsuario` grava:

```go
// cert.go:116
cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
```

**Por que é um problema.** O `-f` força a gravação, mas a loja é indexada por
impressão digital: um certificado com chave e serial novos é uma **entrada
nova**, e a anterior fica onde estava até vencer sozinha — dois anos depois.
Nenhum caminho do projeto executa `certutil -delstore`. Na prática, cada perfil
acumula uma entrada "Agente de Biometria (localhost)" por renovação, e todas
continuam confiáveis enquanto a `key.pem` correspondente pode ter sido
sobrescrita e já não existir mais.

O impacto é limitado, e vale dizer com clareza por quê: o certificado é de
entidade final (`BasicConstraintsValid: true` sem `IsCA`), então uma entrada
velha na `Root` não assina nada além de si mesma. Não é o C1 do #15 com outro
nome. O que sobra é: (a) a chave privada da entrada **corrente** vive em
`%LOCALAPPDATA%\BiometriaAgente\key.pem`, sem proteção de ACL própria — o
argumento `perm` de `gravaArquivoAtomico` não faz nada no Windows (**A4 do
#14**); e (b) a desinstalação não limpa nada disso.

Sobre (b), os dois instaladores divergem, e só um é honesto:

```powershell
# instalar-servidor.ps1:117 — diz o que faz
Write-Host 'Agente desinstalado. Dados e certificado de cada usuario foram preservados.'
```

```xml
<!-- wxs:123-127 — remove o que esta em ProgramData e nada do perfil do usuario -->
<RemoveFile Id="RemoveAnuncio" Name="comparador.json" On="uninstall" />
<RemoveFile Id="RemoveLogComparador" Name="comparador.log" On="uninstall" />
<RemoveFolder Id="RemovePastaDados" On="uninstall" />
```

O MSI não diz nada. Depois de desinstalar por MSI, cada usuário que já abriu o
agente continua com um certificado confiado e uma chave privada no perfil, e não
há nada na interface de desinstalação que sugira isso.

**Como corrigir.** Remover a anterior antes de instalar a nova, no único momento
em que o programa sabe qual é qual:

```go
// cert.go, em gerarCert() — antes de sobrescrever cert.pem
if anterior, err := os.ReadFile(certPath); err == nil {
	if b, _ := pem.Decode(anterior); b != nil {
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			removeCertificadoUsuario(fmt.Sprintf("%x", sha1.Sum(c.Raw)))
		}
	}
}
```

```go
// cert.go — o par de instalaCertificadoUsuario
func removeCertificadoUsuario(impressao string) {
	cmd := exec.Command("certutil.exe", "-user", "-delstore", "Root", impressao)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if saida, err := cmd.CombinedOutput(); err != nil {
		registraErro("remover certificado anterior da loja Root: %v: %s", err, saida)
	}
}
```

E, no mínimo, alinhar a mensagem do MSI com a do `.ps1` — ou dar ao agente um
`--limpar-certificado` que a desinstalação por usuário possa chamar. Um passo que
o `.ps1` documenta e o MSI silencia é o tipo de divergência que já produziu o
**C4 do #12** e o **A5 do #14**.

---

## 🟢 Sugestões (opcional)

- **S1.** `main.go:312-346` — `handleStatus` é o **único** handler sem prazo
  próprio: usa `r.Context()` cru, enquanto `capturaEResponde` monta
  `timeout+25s` (`main.go:363`), `handleComparar` usa 45 s (`main.go:432`) e
  `handleIdentificar` usa 4 min (`main.go:543`). Na prática ele fica atrás da
  fila do SDK e pode segurar uma vaga de `limiteHTTP` por ~50 s (20 s do
  `contaDispositivos` via worker, mais uma captura de 30 s à frente na fila).
  Um `context.WithTimeout(r.Context(), 25*time.Second)` alinha `/api/status` com
  o resto e tira um caminho de fome de vagas.
- **S2.** `.gitignore` continua sem `comparador.json` (**S6 do #15**, **S5 do
  #14**, aberto há três dias). É uma linha, e é o único desses arquivos que
  carrega uma credencial válida para a máquina inteira — `agente-*.json`,
  `origens-autorizadas.json`, `cert.pem` e `key.pem` já estão lá.
- **S3.** `integra-biometria.js:66-81` — quando `r.json()` falha num `200`,
  `respostaJSON` devolve `null` em vez de erro, e `capturar()`/`enroll()`
  resolvem com `null`. Esse `null` segue para `comparar()`, vira `"null"` no
  JSON, e o Go responde `400 BiometriaLida nao e um template valido`
  (`main.go:422-425`) — falha fechada, mas com a mensagem apontando para o dado
  errado. Um `if (dados === null) throw new Error('resposta do agente ilegivel')`
  no ramo `r.ok` põe a culpa onde ela está.
- **S4.** `cert.go:185-213` — `listenerMista.aceita()` fecha em silêncio toda
  conexão que chega com o semáforo de 64 cheio (`default: _ = c.Close()`), sem
  uma linha de log. É o mesmo padrão do `503 agente ocupado`, que ao menos
  responde alguma coisa; aqui o navegador vê apenas a conexão morrer, e o **A2
  do #15** transforma isso numa porta riscada da varredura.
- **S5.** `autoteste.go:128-139` — `forma()` fatia `t[0]` e `t[len(t)-1]` em
  bytes e imprime com `%q`. Para um FIR texto (ASCII imprimível garantido por
  `normalizaTemplate`) está certo, mas `forma()` também é chamada em
  `confereContra:445` **antes** de qualquer garantia sobre o conteúdo cru do
  arquivo — o caminho passa por `leTemplateDeArquivo`, que normaliza, então hoje
  fecha. Vale o comentário dizendo que a garantia vem de lá, porque é o tipo de
  invariante que some numa refatoração.
- **S6.** `supervisor.go:17-27` — `instanciaUnica()` cria o mutex e **nunca
  fecha o handle**, o que é correto (o handle precisa viver enquanto o processo
  vive), mas não está escrito. Alguém que "arrume" isso com um `defer
  CloseHandle` reintroduz o agente duplicado por sessão, que é justamente o que
  o `Local\` do nome resolve.
- **S7.** `worker.go:109-119` — quando `novoSDK(dll)` falha em `atendePedido`,
  não há `c.registraFalha()`: o freio de `falhasParaEsfriar`
  (`worker.go:162-165`, `198-200`) só conta as quedas do processo, não as
  recusas do SDK. Uma DLL presente mas quebrada faz o cliente tentar de novo a
  cada requisição, sem o intervalo de 5 s que o freio existe para dar.

---

## 📋 Resumo

| | |
|---|---|
| **Arquivos alterados** | 1 neste PR (só documentação); **nenhum em `main`** desde `26c9379`; 41 revisados |
| **Segurança** | 🚨 Risco |
| **Qualidade** | ⚠️ Atenção |
| **Risco de produção** | 🚨 Alto |
| **Testes** | ⚠️ Parcial — `build` e `vet` limpos; `go test` **não roda em lugar nenhum** |

**Contagem de hoje:** 0 críticos novos, 6 alertas novos, 7 sugestões novas. Os 7
críticos dos PRs #12 a #15 seguem abertos e foram reconferidos linha a linha.

**Onde os alertas de hoje caem no sistema:**

| Achado | Quem sente, e quando |
|---|---|
| **A1** | Todo mundo daquele servidor, a partir do primeiro logon depois de o `certutil` ser bloqueado. Silencioso |
| **A2** | O operador que recusa (ou não vê) a autorização; e qualquer processo local que queira parar a biometria da estação por 60 s de cada vez |
| **A3** | Quem digita errado, e qualquer integração que chame o binário com caminho vazio — recebendo `0` = "confere" |
| **A4** | O operador numa sessão RDP sem comparador: a ferramenta de conferência morre sem veredito |
| **A5** | `/api/hello` num RDS movimentado — some da varredura pelo **A2 do #15** |
| **A6** | O perfil de cada usuário, acumulando entradas; e a desinstalação por MSI, que não avisa |

**Sobre a cobertura de testes, mais uma vez.** Nenhum dos seis achados de hoje
seria pego pela suíte, e por três motivos distintos que vale separar:

| Achado | Por que a suíte não pega |
|---|---|
| **A1**, **A6** | `cert.go` não tem uma linha de teste. `certificadoValido` e `materialCertificado` são funções puras com `agora time.Time` injetável — foram **escritas** para serem testáveis e nunca foram testadas |
| **A3** | `executa()` não é decomponível hoje: o despacho está embutido na função que também sobe o servidor. Extrair `despacha(args []string) (int, bool)` daria um teste de tabela de 20 linhas |
| **A2**, **A4** | Estão fora do Go — no `.js` e no `.cmd`, que não têm nenhum arranjo de teste |
| **A5** | `pidNaTabelaTCP` é testável com tabela sintética (o **S1 do #15** já fez isso à mão); `pidDaConexao` não é, porque chama a API direto |

`origins.go`, o middleware, `session.go`, `storage.go`, `cert.go` e `log.go`
continuam sem uma linha de teste — e é onde moram os críticos dos últimos quatro
dias e quatro dos seis alertas de hoje.

---

## ✅ Pontos positivos

- **O modo comparador não delega para si mesmo, e isso está garantido por
  construção e não por sorte.** `rodaComparadorCom` (`comparador.go:37-149`)
  nunca chama `configuraComparador()`, então `comparadorRemoto` é `nil` e o
  `handleComparar` compartilhado cai no ramo local. Reconferi os quatro pontos de
  entrada (`--comparador`, `MODO_COMPARADOR=1`, SCM, e os comandos de linha) e a
  propriedade se mantém em todos. É uma armadilha de reentrância clássica —
  o mesmo handler servindo os dois lados de uma delegação — e ela está fechada.
- **A sequência de `defer` em `capturaTexto` (`sdk.go:275-327`) está na ordem
  exata que o SDK exige.** `NBioAPI_FreeTextFIR` roda antes do `LocalFree` da
  struct que ele recebe, e `NBioAPI_FreeFIRHandle` depois — e o `defer` do
  `freeText` só é registrado **depois** de o SDK confirmar um ponteiro não nulo
  (`sdk.go:319-325`), com o comentário explicando que liberar uma struct zerada
  seria pedir para a DLL soltar um endereço que ela nunca alocou. São cinco
  `defer` numa função com FFI e memória nativa, e a ordem LIFO resultante está
  certa em todas as posições.
- **`leTemplateDeArquivo` (`autoteste.go:381-409`) resolve uma ambiguidade real
  em vez de escolher um lado.** Um template base64 termina em `=`, e uma linha de
  ambiente é `CHAVE=valor`: o código não adivinha pelo formato da chave — exige
  que o que sobra depois do `=` **seja um template válido**, senão trata a linha
  inteira como template. E `TestEhNomeDeVariavel` (`conferir_test.go:75-90`) tem
  no comentário exatamente o caso que motivou a regra. É o padrão que o resto da
  suíte segue e é o que faz esses testes sobreviverem a refatoração.
- **O `.cmd` do operador separa "não confere" de "falhou" nos códigos de saída**
  (`autoteste.go:419-423`, `conferir-biometria.cmd:44-49`). É uma distinção que
  quase todo utilitário de biometria erra — e errá-la significa um sistema tratar
  falha de leitor como negativa de identidade. O **A4** de hoje é sobre o buraco
  que sobrou nessa tabela, não sobre a ideia, que está certa.
- **`build` e `vet` limpos** para `windows/386`, arquivos de teste incluídos, em
  4.965 linhas que manipulam memória nativa, `uintptr`, FFI e três subsistemas do
  Windows (SCM, TCP table, toolhelp).

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Hoje não há crítico novo, e o motivo não é que o sistema melhorou — é que as seis
revisões anteriores já encontraram os caminhos onde um crítico cabe, e nenhum
deles foi fechado. O que os seis alertas de hoje têm em comum é um padrão que
vale nomear, porque ele explica mais do que os achados isolados:

**todo caminho de exceção deste sistema falha para o lado silencioso.** O
`certutil` falha e o TLS some (A1). A autorização fica pendente e o cliente
mostra "não foi possível falar com o agente" (A2). O argumento está errado e o
programa sobe outra coisa, ou nada, com código 0 (A3). A DLL derruba o processo e
o `.cmd` imprime uma linha de traços (A4). A tabela TCP muda de tamanho e o
agente vira 503 (A5). O certificado é renovado e o anterior fica (A6).

O caminho feliz deste projeto é cuidadoso — os comentários explicam o *porquê*, a
ordem dos `defer` está certa, a reentrância da delegação está fechada, os códigos
de saída distinguem o que precisa ser distinguido. O caminho infeliz não recebeu
a mesma atenção, e é ele que decide se o próximo chamado vai ser um diagnóstico
ou um folclore.

**Ordem sugerida de correção**, considerando tudo que está aberto há uma semana:

1. **C1 + C2 do #14** — juntos, ~25 linhas em `main.go` e `origins.go`. Continuam
   sendo o único par alcançável **sem nenhum acesso à máquina**. Sétimo dia.
2. **A1 de hoje** — cinco linhas em `cert.go`, e é o que impede uma configuração
   de servidor endurecido de desligar o TLS de todas as sessões em silêncio.
3. **A3 de hoje** — o `switch` com `default`. É pequeno e fecha o único caminho
   em que o binário devolve `0` sem ter comparado nada.
4. **C2 #13 (loopback em `delegacao.go` + ACL do `ProgramData`)** — o que impede
   template biométrico de sair da máquina.
5. **A2 de hoje + A2 do #15** — os dois ramos do mesmo laço de `descobrir()`.
   Corrigir só um deixa metade do sintoma de pé, e é o mesmo trecho de código.
6. **A6 do #15 + C1 do #12** — o serviço troca o token na parada limpa e o agente
   nunca reconfere; também são dois lados de um defeito só.
7. **A4 e A5 de hoje** — não mudam o comportamento correto, mas decidem se o
   próximo chamado tem diagnóstico.
8. **C1 #13 (fila do SDK no comparador)** — o mais trabalhoso; as três medidas
   paliativas cabem num commit.

Este PR **não altera código**: acrescenta apenas este documento.
