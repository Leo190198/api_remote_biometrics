# 🔍 Revisão técnica do sistema — 2026-08-20

> ⚠️ **`main` continua em `26c9379`, de 2026-08-06.** Os PRs **#10** a **#22**
> seguem abertos e nenhum crítico foi tocado. É o **décimo quarto dia
> consecutivo** sem um commit de correção. `git diff origin/main...HEAD` é vazio:
> o código revisado hoje é **byte a byte** o mesmo das revisões anteriores, então
> os 26 críticos abertos continuam válidos por construção.

As revisões anteriores entraram pelo caminho de dados, pela escala do comparador,
pela fronteira com a pessoa, pelo aperto de mão com o sistema web, pelos caminhos
de exceção, pelo resíduo em disco, pela cadeia de entrega, pela convivência de
mais de uma instância, pelos caminhos de recuperação, pelos números do sistema e
pela evidência que sobra depois do atendimento.

Esta foi por um eixo que nenhuma delas abriu: **o despacho**. Antes de qualquer
coisa acontecer, este executável precisa decidir **qual dos seis papéis ele é** —
supervisor, agente da bandeja, processo worker, comparador de terminal, serviço
do SCM ou ferramenta de diagnóstico. Um binário só, seis vidas, e a escolha é
feita em 40 linhas de `executa()` (`main.go:842-884`).

O eixo se justifica porque este é o único estágio do sistema que **roda antes de
existir log**. O `logger` nasce apontando para `io.Discard` (`log.go:12`) e só
recebe um arquivo depois que o papel já foi escolhido: `iniciaLog()` no ramo do
agente (`main.go:885`), `iniciaLogEm()` dentro do comparador (`comparador.go:40`)
e `iniciaLogArquivo()` dentro do worker (`worker.go:68`). Tudo o que acontece
antes disso — inclusive as duas detecções que decidem o papel — é invisível.

O resultado são **dois críticos novos**: um argumento desconhecido, ou um comando
de diagnóstico digitado errado, faz o executável **iniciar um agente completo em
silêncio** — exatamente a duplicação que o README diz que derruba a captura; e o
estágio inteiro de despacho **não grava uma única linha em lugar nenhum**, com as
duas detecções resolvendo o erro para o lado errado.

Além deles, **quatro alertas** (três deles de documentação que contradiz o
código) e **quatro sugestões**.

**Escopo analisado:** os 22 arquivos `.go` (4.965 linhas, das quais 1.055 em 7
arquivos de teste), `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`,
`instalador/msi/AgenteBiometria.wxs`, `instalador/msi/build-msi.cmd`,
`instalador/msi/AgenteBiometria.wixproj`, `conferir-biometria.cmd`,
`embutir-icone.py`, `.gitignore`, `go.mod`/`go.sum`, `README.md` e `docs/`.

**Verificações executadas hoje:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | **OK** |
| `GOOS=windows GOARCH=386 go vet ./...` | **limpo**, arquivos de teste incluídos |
| `gofmt -l .` | **nada a formatar** |
| `go test ./...` (alvo do host) | **`matched no packages`** — as *build tags* `windows && 386` excluem tudo |
| `GOOS=windows GOARCH=386 go test ./...` | **`exec format error`** — compila e não roda. As **48** funções `Test*` continuam sem executar em lugar nenhum. Segue valendo o **A8 de 30/07** |
| `git log -1 origin/main` | `26c9379`, de **2026-08-06** (14 dias) |
| `git diff origin/main...HEAD` | **vazio** — nenhuma mudança de código nesta revisão |
| Leitura de `IsWindowsService()` em `x/sys@v0.47.0/windows/svc/security.go` | confirma a base do **C2**: a detecção é uma heurística sobre o **processo pai** (sessão 0 **e** chamado `services.exe`) |
| `grep -n "iniciaLog" *.go` | 3 chamadas, **todas depois** do despacho — base do **C2** e do **A1** |
| `grep -rn "cert" instalador/instalar-servidor.ps1` | 1 ocorrência, e é uma mensagem de texto — base do **A2** |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. Qualquer argumento desconhecido — inclusive um comando de diagnóstico digitado errado — **inicia um agente completo, sem uma palavra na tela** *(novo)*

**Arquivos:** `main.go:842-884`, `supervisor.go:17-27`, `autoteste.go:41-68`,
`conferir-biometria.cmd:39`, `README.md` (seção *Diagnóstico*)

O despacho inteiro é uma sequência de `if` sem `else` final:

```go
// main.go:842-884 (resumido, na ordem exata)
func executa() int {
	if len(os.Args) > 1 && os.Args[1] == "--gerar-cert"        { ... }
	if len(os.Args) > 1 && os.Args[1] == "--autoteste"         { return rodaAutoteste() }
	if len(os.Args) > 2 && os.Args[1] == "--conferir-contra"   { return confereContra(os.Args[2]) }
	if len(os.Args) > 1 && os.Args[1] == "--teste-delegacao"   { return testeDelegacao() }
	if os.Getenv("BIO_WORKER") == "1"                          { return workerMain() }
	if souServico()                                            { return rodaComoServico() }
	if os.Getenv("MODO_COMPARADOR") == "1" || (len(os.Args) > 1 && os.Args[1] == "--comparador") { ... }
	if os.Getenv("BIO_FILHO") != "1" {
		if !instanciaUnica() { return 0 }
		supervisor()          // <- cai aqui QUALQUER coisa que nao casou acima
		return 0
	}
	// ... o agente de verdade
}
```

Não existe `--help`, não existe caso padrão, não existe mensagem de argumento
inválido. **Tudo o que não casou vira o modo normal**: sobe supervisor, sobe
agente, sobe bandeja, abre porta, publica o arquivo de descoberta.

**Por que é um problema.** Repare na terceira linha: `--conferir-contra` exige
`len(os.Args) > 2`. Digitar o comando **sem o arquivo** — que é o erro mais
natural que existe, porque o `.cmd` documenta um padrão e convida a chamar sem
argumento (`conferir-biometria.cmd:23`) — não produz "faltou o arquivo". Produz
um agente.

E o agente que nasce daí é invisível por construção: o binário é compilado com
`-H windowsgui` (`README.md`, seção *Compilação*), então **não tem console e não
escreve nada**. As três ferramentas de diagnóstico reconectam a saída de
propósito, com `ligaConsole()` (`autoteste.go:41-68`); o caminho do agente,
não — ele nunca precisou.

O encadeamento que fecha o problema está no próprio README:

> **Diagnóstico** — *"Feche o agente antes de usá-los: dois processos disputando
> o mesmo leitor derrubam a captura."*

Ou seja, o procedimento documentado é: **fechar o agente** e rodar o comando. Um
erro de digitação nesse exato momento reabre o agente — calado — e a tentativa
seguinte, agora escrita certa, roda **contra um agente vivo**, que é a condição
que o README declara que derruba a captura. O operador conclui que o leitor, o
driver ou a DLL está com defeito, e o diagnóstico começa pelo lado errado. Foi
para evitar exatamente esse tipo de conclusão invertida que o
`--conferir-contra` separa "não é a pessoa" de "quebrou" em códigos de saída
diferentes (`autoteste.go:415-424`) — o cuidado existe dentro da ferramenta e
não existe na porta de entrada dela.

Há ainda a segunda metade, que decide quão confuso fica: se um agente **já**
estiver rodando naquela sessão, `instanciaUnica()` devolve falso e o processo
sai com **código 0** (`main.go:877-882`), sem escrever nada. O operador digita um
comando, o prompt volta na hora, sem saída, sem erro e com sucesso declarado.
Não há como distinguir "rodou e passou" de "eu nem entendi o que você pediu".

**Como corrigir.** Um despacho explícito, com erro para o que não é conhecido, e
com a saída religada antes de reclamar — para a reclamação chegar a quem digitou:

```go
// main.go — o despacho passa a ter porta de entrada e porta de erro.
func comandoDeLinha() (string, []string, bool) {
	if len(os.Args) < 2 || !strings.HasPrefix(os.Args[1], "-") {
		return "", nil, false
	}
	return os.Args[1], os.Args[2:], true
}

func executa() int {
	if nome, extras, temComando := comandoDeLinha(); temComando {
		switch nome {
		case "--gerar-cert":
			...
		case "--conferir-contra":
			if len(extras) < 1 {
				ligaConsole()
				fmt.Fprintln(os.Stderr, "uso: AgenteBiometria.exe --conferir-contra <arquivo>")
				fmt.Fprintln(os.Stderr, "O arquivo tem o cadastro guardado: template puro ou CHAVE=valor.")
				return 2
			}
			return confereContra(extras[0])
		case "--comparador":
			return rodaComparador()
		case "--help", "-h", "/?":
			ligaConsole()
			imprimeAjuda()
			return 0
		default:
			// Nunca cair no modo normal por engano: quem digitou espera resposta,
			// e um agente que nasce calado disputa o leitor com o comando seguinte.
			ligaConsole()
			fmt.Fprintf(os.Stderr, "argumento desconhecido: %s\n", nome)
			imprimeAjuda()
			return 2
		}
	}
	// Sem argumentos: os modos decididos pelo ambiente (worker, servico, agente).
	...
}
```

E, no caminho de instância única, dizer o que aconteceu em vez de sair calado:

```go
// main.go
if !instanciaUnica() {
	registraInfo("ja existe um agente nesta sessao; nada a fazer")
	return 0
}
```

Vale notar que a correção **também resolve o buraco do `--comparador`**: hoje ele
é reconhecido por `os.Args[1]`, mas só depois de `souServico()` — a tabela acima
deixa a ordem explícita em vez de implícita em quatro `if` encadeados.

---

### C2. O estágio que decide o papel do processo **não grava uma linha em lugar nenhum**, e as duas detecções resolvem o erro para o lado errado *(novo)*

**Arquivos:** `log.go:12-22`, `main.go:842-885`, `servico.go:74-94`,
`supervisor.go:29-45`, `comparador.go:40`, `worker.go:68`

O `logger` nasce descartando tudo, e só passa a escrever quando o papel já foi
escolhido:

```go
// log.go:12 — ate alguem chamar iniciaLog*, TUDO some
var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)
```

| Papel | Onde o log começa | Quantas decisões vieram antes |
|---|---|---|
| agente | `main.go:885` (`iniciaLog()`) | 7 |
| comparador | `comparador.go:40` (`iniciaLogEm`) | 7 |
| worker | `worker.go:68` (`iniciaLogArquivo`) | 5 |
| serviço | **nunca**, até entrar em `rodaComparadorCom` | 6 |
| supervisor | **nunca** | 7 |

**Por que é um problema.** Não é a falta de uma linha informativa: são três
caminhos de falha reais em que o sistema **não deixa nenhum rastro**, e dois
deles resolvem o erro para o modo que não funciona.

**Primeiro: a detecção de serviço falha para "não sou serviço".**

```go
// servico.go:87-94
func souServico() bool {
	ehServico, err := svc.IsWindowsService()
	if err != nil {
		fmt.Println("nao consegui saber se estou sob o SCM:", err)
		return false
	}
	return ehServico
}
```

Li a implementação de `IsWindowsService()` em `x/sys@v0.47.0`
(`windows/svc/security.go`): ela consulta `NtQueryInformationProcess`, percorre a
tabela de processos do sistema e responde **verdadeiro só se o processo pai
estiver na sessão 0 e se chamar `services.exe`**. É uma heurística sobre o pai —
e quando ela não consegue responder (chamada nativa recusada, pai já ausente da
tabela), o erro vira `false`.

`false` sob o SCM significa cair no `if` seguinte, casar com o
`Arguments="--comparador"` do WiX (`AgenteBiometria.wxs:80`) e rodar
`rodaComparador()` **avulso**. O que acontece a partir dali está escrito no
próprio arquivo que existe para impedir isso:

```go
// servico.go:6-10
// Precisa existir porque o binario iniciado por "sc create" sem falar com o SCM
// e morto em 30 segundos por nao responder ao pedido de start.
```

Só que agora com uma diferença: antes de ser morto, o comparador avulso **publica
o anúncio** (`comparador.go:94`), e quem é morto não roda o `defer removeAnuncio()`
(`comparador.go:99`). O SCM aplica a política de reinício automático a cada 60
segundos (`AgenteBiometria.wxs:85-90`), e o ciclo se repete: sobe, anuncia, morre
aos 30 s, deixa o anúncio, reinicia. Do lado das sessões, os agentes leem um
anúncio válido e delegam para uma porta que existe metade do tempo — e o sintoma
que chega ao balcão é biometria que funciona e para de funcionar em ciclos de um
minuto. **Nada disso aparece em log nenhum**: o `fmt.Println` de um serviço não
tem para onde ir, e o `registraErro` de `rodaComoServico` (`servico.go:76`) —
que é onde cairia a falha mais comum de todas, o `svc.Run` recusado com *"the
service process could not connect to the service controller"* — escreve em
`io.Discard`, porque nenhum `iniciaLog*` rodou ainda.

**Segundo: a instância única falha para "já existe outra".**

```go
// supervisor.go:17-27
func instanciaUnica() bool {
	nome, err := syscall.UTF16PtrFromString(`Local\AgenteBiometriaGo`)
	if err != nil {
		return false                      // <- nao consegui perguntar
	}
	h, _, errno := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(nome)))
	if h == 0 {
		return false                      // <- nao consegui criar
	}
	return errno != erroJaExiste
}
```

Os dois `return false` de erro são indistinguíveis do `return false` legítimo, e
o chamador trata os três do mesmo jeito: `return 0`, saída limpa, silêncio. Numa
máquina onde `CreateMutexW` falhe — política de sandbox, EDR, esgotamento de
handles —, **o agente nunca mais sobe naquela sessão** e não existe uma linha em
lugar nenhum dizendo por quê. O sintoma é "o ícone não aparece mais depois que
logo", que é o pior tipo de chamado: sem evidência e sem erro.

**Terceiro: o supervisor morre mudo.**

```go
// supervisor.go:29-45
func supervisor() {
	exe, err := os.Executable()
	if err != nil { os.Exit(1) }          // sem log
	...
		if err := cmd.Start(); err != nil { os.Exit(1) }   // sem log
```

O supervisor é o processo que a chave `Run` inicia — o primeiro elo de todo
logon. Se ele não conseguir iniciar o filho (antivírus segurando o arquivo,
diretório de versão apagado pelo `-Desinstalar` do outro instalador, perfil sem
permissão), ele sai com código 1 e **zero rastro**.

O contraste com o resto do repositório é o que faz disto um crítico e não uma
observação de estilo: este é o mesmo projeto que grava o autoteste linha a linha
com `Sync()` para que "a última linha do arquivo seja o passo que matou o
processo" (`autoteste.go:75-106`), que lista o endereço base de cada módulo para
casar com o PC de uma violação de acesso (`versaodll.go:51-70`) e que registra
qual DLL abriu porque "é a primeira pergunta de qualquer diagnóstico"
(`main.go:892-894`). Todo esse cuidado começa **depois** do ponto em que o
processo decide quem ele é — e é justamente antes desse ponto que moram as falhas
que ninguém consegue explicar.

**Como corrigir.** Abrir o log antes de decidir, e registrar a decisão:

```go
// log.go — um destino provisorio que serve a qualquer papel.
//
// ProgramData porque e o unico diretorio que existe e e gravavel nos seis
// papeis, inclusive no do SYSTEM, onde %LOCALAPPDATA% aponta para um perfil
// que ninguem consulta.
func iniciaLogDeArranque() {
	iniciaLogEm(diretorioCompartilhado(), "arranque.log")
}
```

```go
// main.go, primeira linha de executa()
iniciaLogDeArranque()
defer func() { registraInfo("arranque: saindo") }()
```

```go
// servico.go — o erro passa a ser um erro, e nao um voto.
func souServico() bool {
	ehServico, err := svc.IsWindowsService()
	if err != nil {
		// Nao da para adivinhar: assumir "nao sou servico" leva ao modo que o
		// SCM mata em 30s, e o anuncio fica para tras a cada ciclo.
		registraErro("nao consegui saber se estou sob o SCM: %v", err)
		return os.Getenv("MODO_COMPARADOR") != "1" && !temArgumento("--comparador")
	}
	registraInfo("arranque: souServico=%v", ehServico)
	return ehServico
}
```

```go
// supervisor.go — distinguir "ja existe" de "nao consegui perguntar".
func instanciaUnica() (bool, error) {
	nome, err := syscall.UTF16PtrFromString(`Local\AgenteBiometriaGo`)
	if err != nil {
		return false, err
	}
	h, _, errno := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(nome)))
	if h == 0 {
		return false, fmt.Errorf("CreateMutexW: %w", errno)
	}
	return errno != erroJaExiste, nil
}
// no chamador: erro -> registraErro e SEGUE (um agente a mais e melhor que
// nenhum); false sem erro -> registraInfo e sai.
```

Uma nota de escopo: o **A1 do #14** (o worker do comparador grava no perfil do
SYSTEM) e o **A1 do #22** (o `stderr` do worker vai para o `NUL`) são o mesmo
tema visto de outros ângulos, e a correção acima é compatível com as duas — um
`iniciaLogDeArranque()` comum resolve o destino do log para qualquer papel,
inclusive o do worker.

---

## 🟡 Alertas (recomenda correção)

### A1. As três ferramentas de diagnóstico rodam com o `logger` em `io.Discard` — o `registraErro` do SDK some justamente nas execuções feitas para investigar *(novo)*

**Arquivos:** `autoteste.go:141-182`, `autoteste.go:424-499`,
`autoteste.go:508-569`, `sdk.go:279-286`, `sdk.go:458-464`, `log.go:12-14`

`rodaAutoteste`, `confereContra` e `testeDelegacao` são chamados no topo de
`executa()` (`main.go:850-858`) e **nenhum dos três chama `iniciaLog*`**. O
autoteste tem o relatório próprio (`autoteste.go:82-106`), e os outros dois
escrevem na tela — mas o SDK não sabe disso e continua registrando pelo
`logger`, que ainda aponta para `io.Discard`:

```go
// sdk.go:458-464 — a linha mais informativa do repositorio sobre o 0x000B
if uint32(r) == erroChecksum {
	registraErro("VerifyMatch recusou o par por checksum: cadastrado %s; lido %s",
		impressaoTemplate(limpoA), impressaoTemplate(limpoB))
}
return false, novoErroSDK(uint32(r), "NBioAPI_VerifyMatch")
```

**Por que é um problema.** O `0x000B` é o defeito que originou metade deste
repositório, e essa linha é a única que mostra **as duas impressões lado a lado**
— o par que diz se o template que veio do banco é o mesmo que saiu do SDK. Quem
roda `--conferir-contra` recebe só o texto do erro (`Template adulterado: o
checksum interno nao confere com o conteudo`), sem as impressões, porque o
`erroSDK` carrega apenas código e origem. O mesmo vale para
`registraErro("fechar leitor apos captura: %v", err)` (`sdk.go:283`), que é
sintoma de leitor oscilando no redirecionamento RDP.

A consequência prática é irônica: o único modo do binário que **não** perde essas
linhas é o modo normal, que é o que o README manda fechar antes de diagnosticar.

**Como corrigir.** Uma linha em cada ferramenta, logo depois de `ligaConsole()`:

```go
// autoteste.go, em rodaAutoteste/confereContra/testeDelegacao
iniciaLogArquivo("diagnostico.log")
```

Melhor ainda, junto com o C2: se `iniciaLogDeArranque()` for a primeira linha de
`executa()`, os três já nascem com log e nada precisa ser lembrado caso apareça
uma quarta ferramenta.

### A2. README e COMO-USAR atribuem ao instalador a geração e o registro do certificado; o instalador não tem uma linha sobre certificado *(novo)*

**Arquivos:** `integracao/COMO-USAR.md:91-96`, `integracao/COMO-USAR.md:123`,
`cert.go:92-141`, `instalador/instalar-servidor.ps1` (arquivo inteiro)

```markdown
<!-- COMO-USAR.md:91-96 -->
Para o HTTPS funcionar, o **instalador do servidor** gera um certificado
autoassinado de `localhost` e o registra na loja de raízes confiáveis da
máquina (veja `agente-go/instalador/instalar-servidor.ps1`).
```

Três afirmações, e as três estão erradas:

1. **Quem gera é o agente**, a cada arranque, dentro de `carregaTLS()`
   (`cert.go:125-132`) — não o instalador. `grep -i cert instalar-servidor.ps1`
   devolve **uma** ocorrência, e é a frase *"Dados e certificado de cada usuario
   foram preservados"* na desinstalação (linha 117). Não existe geração, não
   existe `certutil`, não existe importação.
2. **Não é a loja da máquina, é a do usuário.** `instalaCertificadoUsuario` roda
   `certutil.exe -user -addstore -f Root` (`cert.go:115-123`) — repositório
   Raiz **por usuário**. Num RDS isso significa um certificado por conta que
   logar, e não um por servidor.
3. **O caminho não existe.** `agente-go/instalador/instalar-servidor.ps1` não é
   um caminho deste repositório; o arquivo está em `instalador/`.

O mesmo texto reaparece na linha 123: *"Isso copia o agente para Program Files,
registra o certificado e o auto-início em HKLM"*.

**Por que é um problema.** O HTTPS local é a parte do sistema que mais gera
chamado ("o navegador não confia no certificado"), e a documentação manda o
operador procurar no lugar errado: ele vai conferir a loja Raiz **da máquina**,
não achar nada, e concluir que o instalador falhou — quando o certificado está na
loja do usuário e foi posto lá por outro processo, em outro momento. O README
agrava ao listar os seis passos do instalador (seção *Instalação no servidor*)
sem mencionar certificado nenhum: os dois documentos discordam entre si.

**Como corrigir.** Trocar as três afirmações pelo que o código faz:

```markdown
Para o HTTPS funcionar, **o próprio agente** gera, no primeiro arranque de cada
usuário, um certificado autoassinado de `localhost` em
`%LOCALAPPDATA%\BiometriaAgente\cert.pem` e o registra na loja Raiz **do
usuário** (`certutil -user -addstore Root`). O instalador não toca em
certificado. Para regerar na mão: `AgenteBiometria.exe --gerar-cert`.
```

E corrigir o caminho `agente-go/instalador/...` para `instalador/...` nas duas
ocorrências.

### A3. A tabela de códigos de saída do README está errada: `2` nunca significa "a DLL derrubou o processo" *(novo)*

**Arquivos:** `README.md` (seção *Diagnóstico*), `autoteste.go:583-599`,
`autoteste.go:415-424`, `autoteste.go:508-569`, `worker.go:241-262`

O README declara:

> Códigos de saída: `0` passou, `1` o SDK recusou com erro tratado, `2` a DLL
> derrubou o processo — nesse caso o traceback traz o endereço da falha (...)

O que o código realmente devolve:

| Comando | Códigos possíveis | Onde |
|---|---|---|
| `--autoteste` | **0 ou 1**, só | `a.encerra()` — `autoteste.go:595-598` |
| `--teste-delegacao` | **0 ou 1**, só | `autoteste.go:517, 537, 548, 559, 563, 567` |
| `--conferir-contra` | 0, 1 ou **2 = não confere** | `autoteste.go:415-424` |
| `--gerar-cert` | 0 ou 1 | `main.go:843-849` |

E quando a DLL **realmente** derruba o processo, o código de saída não é `2`: é
o da exceção do Windows. O próprio repositório documenta isso no lugar certo:

```go
// worker.go:241-242
// derruba encerra o worker e devolve como ele saiu, para o log distinguir uma
// saida limpa de uma violacao de acesso (exit status 3221225477 = 0xC0000005).
```

`2` é o código de saída de um **panic do Go**, que é outro caso — e o próprio
README diagnostica os dois juntos.

**Por que é um problema.** A tabela existe para um script de suporte tratar as
respostas de forma diferente, e um script que a siga classifica errado nos dois
sentidos: nunca verá o `2` que espera para "a DLL caiu" (verá `3221225477`), e
tratará como queda o `2` do `--conferir-contra`, que é **veredito biométrico
legítimo**. O parágrafo seguinte do README até avisa que "em `--conferir-contra`
o `2` tem outro sentido" — mas isso conserta a exceção e deixa a regra errada.

**Como corrigir.** Separar o que é código do programa do que é código do sistema
operacional:

```markdown
Códigos de saída dos comandos: `0` passou, `1` falhou com erro tratado.
`--conferir-contra` acrescenta o `2` = **não confere** (veredito biométrico, não
falha).

Se a `NBioBSP.dll` derrubar o processo, o código de saída é da **exceção do
Windows**, não do programa: `3221225477` (`0xC0000005`) para violação de acesso.
Um `2` inesperado nos demais comandos é *panic* do Go — nesse caso o traceback
traz o endereço da falha, que se compara com as faixas dos módulos listados
logo antes.
```

### A4. O executável entregue não carrega recurso de versão — e o sistema exige de terceiros exatamente o que não fornece de si *(novo)*

**Arquivos:** `instalador/msi/build-msi.cmd:26-51`, `embutir-icone.py:12-62`,
`versaodll.go:22-49`, `versaodll.go:113-128`,
`instalador/instalar-servidor.ps1:142-149`

O `go build` não gera recurso PE (o próprio `build-msi.cmd:42-43` diz isso), e o
passo seguinte grava **só ícone**:

```python
# embutir-icone.py:12-13 — os dois unicos tipos de recurso escritos
RT_ICON = 3
RT_GROUP_ICON = 14
```

Não há `RT_VERSION`. O `AgenteBiometria.exe` instalado numa máquina não responde
"qual versão sou eu" nem ao Explorer, nem ao `Get-Item ... .VersionInfo`, nem ao
inventário do parque. A versão existe apenas dentro do binário, injetada por
`-X main.versao=` (`build-msi.cmd:35`), e só sai por `/api/status` — que exige
token e um agente vivo.

**Por que é um problema.** O sistema inteiro depende dessa informação **quando
ela vem dos outros**: `versaoArquivo()` (`versaodll.go:22-49`) lê exatamente o
`RT_VERSION` de uma DLL, e `descreveDLL()` junta caminho, versão, tamanho e data
porque — nas palavras do arquivo — "duas máquinas com a mesma instalação aparente
podem estar carregando DLLs diferentes e nada no log denuncia"
(`versaodll.go:5-11`). É o mesmo problema, no próprio binário, e aqui não há nem
o consolo do tamanho: o `instalar-servidor.ps1` guarda cada versão num diretório
nomeado pelo **hash** (`instalar-servidor.ps1:142-149`), então uma máquina com
três diretórios não diz qual é a mais nova sem comparar hashes com o servidor de
build. Isso é a base prática do **C16 do #18** ("nada confere versão"): não dá
para conferir o que o arquivo não declara.

**Como corrigir.** Escrever também o `RT_VERSION`, no mesmo passo que já abre o
recurso, aproveitando que `BeginUpdateResource` já está ali:

```python
# embutir-icone.py — o VS_VERSIONINFO cabe no mesmo UpdateResource.
RT_VERSION = 16
# ... montar o VS_FIXEDFILEINFO com a versao recebida por argumento e gravar:
k32.UpdateResourceW(h, RT_VERSION, 1, LANG_NEUTRO, versao_bin, len(versao_bin))
```

```bat
rem build-msi.cmd — a versao ja existe na variavel; basta passar adiante
python embutir-icone.py AgenteBiometria.exe app.ico %VERSAO%
```

O ganho imediato é o inventário responder sem o agente estar de pé, e o
`instalar-servidor.ps1` poder recusar uma instalação para trás comparando versão
em vez de hash.

---

## 🟢 Sugestões (opcional)

### S1. O `case <-r.Context().Done()` do middleware é inalcançável

```go
// main.go:252-260
select {
case limiteHTTP <- struct{}{}:
	defer func() { <-limiteHTTP }()
case <-r.Context().Done():      // <- morto: com default, o select nunca bloqueia
	return
default:
	escreveErro(w, http.StatusServiceUnavailable, "agente ocupado")
	return
}
```

Um `select` com `default` nunca espera, então o caso do contexto só é escolhido
se ele já estiver cancelado no instante exato — e, mesmo aí, o Go escolhe entre
os casos prontos ao acaso. A intenção (não responder a quem já desistiu) é boa e
está implementada de verdade em `handleIdentificar` (`main.go:484-492`), onde o
`select` **espera** por até 2 segundos. Aqui, ou se remove o caso morto, ou se
troca o `default` por um `time.NewTimer` curto, como no outro handler.

### S2. `Arguments="--comparador"` no WiX nunca é lido no caminho feliz

`souServico()` decide antes de qualquer argumento (`main.go:862-876`), então o
`Arguments` do `ServiceInstall` (`AgenteBiometria.wxs:80`) só tem efeito **se a
detecção falhar** — que é o cenário do **C2**. Ele é, na prática, a rede de
segurança desse crítico, e vale um comentário no `.wxs` dizendo isso: hoje ele
parece a forma normal de escolher o modo, e não é.

### S3. A árvore do projeto no README não lista os quatro arquivos do comparador

A seção *Estrutura do projeto* enumera `main.go`, `sdk.go`, `worker.go`,
`autoteste.go`, `versaodll.go`, `log.go`, `session.go`, `origins.go`, `cert.go`,
`supervisor.go` e `storage.go` — e **omite `anuncio.go`, `comparador.go`,
`delegacao.go` e `servico.go`**, exatamente os quatro que implementam o recurso
que o README passa metade do tempo explicando. Também faltam `instalador/msi/`,
`conferir-biometria.cmd` e `embutir-icone.py`. Quem chega ao repositório pelo
README não encontra o código do que leu.

### S4. `net.Error.Temporary()` está depreciado desde o Go 1.18

```go
// cert.go:190
if ne, ok := err.(net.Error); ok && ne.Temporary() {
```

O módulo declara `go 1.26` (`go.mod`). `Temporary()` sempre devolve falso em
vários erros que **são** temporários, então o laço de aceitação da
`listenerMista` pode desistir por engano em vez de aplicar o backoff que está
logo abaixo. O padrão atual é testar `errors.Is(err, net.ErrClosed)` para sair e
tratar o resto como temporário.

---

## 📋 Resumo

- **Arquivos alterados**: 1 — apenas este documento. **Nenhuma mudança de código.**
- **Arquivos analisados**: 33 (22 `.go`, 1 `.js`, 1 `.ps1`, 3 do MSI, 2 `.cmd`, 1 `.py`, `.gitignore`, `go.mod`/`go.sum`, `README.md` e 3 docs)
- **Segurança**: 🚨 **Risco** — os achados de hoje não são de segurança, mas nenhum dos seis críticos de segurança anteriores foi tocado: impressão de template como identificador estável (**C25 do #22**), parser nativo sob `LocalSystem` (**C23 do #21**), dado pessoal em log legível (**C14 do #17**), `ProgramData` gravável (**C10 do #13**), agente impostor na 5000 (**C13 do #15**) e revogação que se desfaz sozinha (**C19 do #19**)
- **Qualidade**: ⚠️ Atenção — `build`, `vet` e `gofmt` limpos. O ponto fraco de hoje é de **fronteira do programa**: o despacho não tem caso padrão, não tem ajuda e não tem log, num binário que assume seis papéis
- **Risco de produção**: 🚨 **Alto** — o **C1** transforma um erro de digitação em captura derrubada durante um diagnóstico, e o **C2** garante que nenhum dos dois caminhos deixe evidência
- **Testes**: ❌ Sem cobertura efetiva — 48 funções `Test*` que não rodam em alvo nenhum (`matched no packages` no host, `exec format error` em `windows/386`) e nenhum CI. **A8 de 30/07, aberto há 21 dias**. O **C1** de hoje é testável sem Windows: `executa()` extraído para uma função de despacho puro (argumentos + ambiente → papel) cabe numa tabela de casos

### Inventário dos críticos abertos contra `26c9379`

| # | Origem | Crítico | Estado |
|---|---|---|---|
| 1 | main (07-30) | `bioPort` do fragmento não validado (`integra-biometria.js:26-34`) | **ABERTO** |
| 2 | main (07-30) | Cliente JS descarta `ignorados` (`integra-biometria.js:222-230`) | **ABERTO** |
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
| 19 | PR #19 | Estado por usuário compartilhado entre sessões: revogação desfeita | **ABERTO** |
| 20 | PR #19 | `--comparador` de diagnóstico apaga o anúncio de produção | **ABERTO** |
| 21 | PR #20 | Agente decide delegar uma vez e nunca reavalia | **ABERTO** |
| 22 | PR #20 | Parada do comparador deixa worker órfão e quebra a atualização | **ABERTO** |
| 23 | PR #21 | Usuário comum alcança o parser nativo dentro de um processo `LocalSystem` | **ABERTO** |
| 24 | PR #21 | Tetos de `/identificar` contradizem a API e dobram no caminho delegado | **ABERTO** |
| 25 | PR #22 | Impressão do template é identificador estável de pessoa, gravado em log | **ABERTO** |
| 26 | PR #22 | Comparador sem `middleware`: sem teto de concorrência e sem `recover` | **ABERTO** |
| 27 | **hoje** | **Argumento desconhecido inicia um agente completo, em silêncio** | **NOVO** |
| 28 | **hoje** | **O despacho não grava log e resolve os dois erros de detecção para o lado errado** | **NOVO** |

---

## ✅ Pontos positivos

**O `.gitignore` foi escrito por quem pensou no arquivo que o operador cria às
três da tarde.** Ele não ignora só artefato de build: ignora `template*.txt`,
`.env`, `.env.*` e `*.hash` — que é exatamente o nome do arquivo que o
`conferir-biometria.cmd` sugere por padrão (`conferir-biometria.cmd:23`) — com o
motivo escrito ao lado: *"Templates biometricos NUNCA entram no repositorio: sao
dado pessoal irrevogavel, e este repositorio e publico. Senha trocada vaza uma
vez; digital vazada acompanha a pessoa pelo resto da vida."* Procurei uma brecha
aí — o fluxo documentado do `--conferir-contra` cria um arquivo com um template
real dentro da árvore — e ela já estava fechada, com `*.log` de quebra. É raro um
projeto fechar o caminho que a própria ferramenta de diagnóstico abre.

**`souServico()` decide pela origem do processo, não pelos argumentos, e o
comentário explica por quê** (`servico.go:82-94`): *"decidir pela origem do
processo em vez dos argumentos evita depender de eles chegarem pelo ImagePath,
que e detalhe do instalador"*. Está certo — o mesmo binário serve ao terminal e
ao SCM sem uma segunda flag para alguém esquecer. O C2 de hoje não contesta a
decisão; contesta o que acontece quando ela não consegue ser tomada.

**O serviço só reporta `Running` depois que a porta aceita conexões**
(`servico.go:38-46`). Reportar antes é o padrão da indústria e está errado: o SCM
daria o serviço como de pé enquanto ele ainda podia morrer por porta ocupada, e o
erro apareceria como comparação falhando no meio de um atendimento, longe da
causa. Aqui o `pronto` atravessa a fronteira entre `rodaComparadorCom` e
`Execute` só para isso.

**`embutir-icone.py` declara `restype`/`argtypes` de cada função do
`kernel32`** (`embutir-icone.py:18-28`), com o motivo: em Python 64 bits o
`HANDLE` seria truncado para 32 bits e o `EndUpdateResource` corromperia o
executável em silêncio. É uma armadilha que derruba scripts de build em produção
e quase nunca é antecipada — aqui foi, e ficou documentada.

**A separação entre "erro daquele registro" e "erro da operação" continua sendo a
melhor decisão do repositório** (`sdk.go:512-522`, `worker.go:130-134`,
`main.go:567-572`). Reconferida hoje pelo eixo do despacho: é o mesmo princípio
que falta no `instanciaUnica()` — lá, "não consegui perguntar" e "a resposta é
não" viraram o mesmo `false`.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O sistema chega a **28 críticos abertos** e **quatorze dias** sem um commit de
correção. Os dois de hoje têm a mesma origem, e ela é diferente das anteriores:
**o rigor deste projeto começa depois que o programa já sabe quem é**. Existe
`recover` por requisição, isolamento do SDK em processo, relatório gravado linha
a linha, impressão de template para seguir bytes entre processos — e nada disso
alcança as 40 linhas que decidem, antes de tudo, qual dos seis papéis o
executável vai assumir. Nessas 40 linhas não há caso padrão, não há ajuda, não há
log, e os dois erros possíveis são resolvidos para o lado que não funciona.

A sequência recomendada muda pouco em relação à do #22, e ganha um item barato na
frente:

1. **C1 + C2 de hoje** — um `switch` com caso padrão, `ligaConsole()` antes de
   reclamar e `iniciaLogDeArranque()` na primeira linha de `executa()`. São
   ~40 linhas, não mudam nenhum handler e não alteram nenhum caminho de dados —
   e são o que torna diagnosticável tudo o que vier depois, incluindo os 26
   críticos que já estão na fila.
2. **C26 do #22 (o `protege`)** — extrair `recover` + `limiteHTTP` do
   `middleware` e aplicá-los também no comparador.
3. **C23 do #21 (a troca de conta)** — `Account="NT AUTHORITY\LocalService"` no
   WiX e `-UserId 'LOCAL SERVICE'` no PS1.
4. **C20 (parte do log) do #19** e **C21 do #20** — releitura do anúncio com
   cache curto, que apaga a maior fonte dos `401` do **A2 do #22**.
5. **C25 do #22 (o HMAC na impressão)** — seis linhas mais a chave por
   instalação.

E os três alertas de documentação (**A2**, **A3** e **A4**) custam meia hora
somados. Eles não mudam o comportamento de nada — mudam para onde o operador
olha quando algo quebra, que é a diferença entre um chamado de vinte minutos e um
de dois dias.

Este PR **não altera código**: acrescenta apenas este documento.
