# 🔍 Revisão técnica do sistema — 2026-08-03

Revisão do sistema no commit `d1c3846`, que é ao mesmo tempo `origin/main` e a
ponta do branch `claude/peaceful-albattani-na03w3`.

**Escopo analisado:** `main.go`, `sdk.go`, `worker.go`, `comparador.go`,
`delegacao.go`, `autoteste.go`, `versaodll.go`, `cert.go`, `origins.go`,
`session.go`, `storage.go`, `supervisor.go`, `log.go`, os cinco `*_test.go`,
`instalador/instalar-servidor.ps1`, `integracao/integra-biometria.js`,
`integracao/COMO-USAR.md`, `embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`,
`README.md` e `docs/`.

**Verificações executadas:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | OK |
| `GOOS=windows GOARCH=386 go vet ./...` | limpo |
| `go test ./...` | `no packages to test` — as *build tags* excluem tudo fora de `windows/386` |
| contagem de testes | 33 funções `Test*` + `TestMain`, em 5 arquivos |

---

## ⚠️ Antes de tudo: sexto dia, mesmo commit

`git diff origin/main...HEAD` continua **vazio**. O último commit de código é
`735402a`, de 2026-07-31; o último commit de qualquer natureza é `d1c3846`, do
mesmo dia. Passaram-se três dias sem uma linha alterada, e há agora **quatro PRs
de revisão abertos e nenhum mergeado** (#1, #3, #4, #5), além deste.

Repetir os achados anteriores não acrescentaria nada, então eles aparecem só na
tabela de situação no fim. O corpo desta revisão é o que as quatro leituras
anteriores **não** viram — e o que sobrou de novo desta vez é um tema só, que
atravessa código, instalador e README: **o sistema é multi-sessão, mas a
configuração dele é de máquina.**

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. `PORTA` e `MODO_COMPARADOR` são variáveis de máquina num produto multi-sessão — e o supervisor transforma o erro num laço infinito

**Arquivos:** `main.go:571-590`, `main.go:854-869`, `supervisor.go:29-56`,
`README.md:216`, `instalador/instalar-servidor.ps1:157-183`

```go
// main.go:571-582
func escolheListener() (net.Listener, int, error) {
	if valor := os.Getenv("PORTA"); valor != "" {
		p, err := strconv.Atoi(valor)
		if err != nil || p < 1 || p > 65535 {
			return nil, 0, errors.New("PORTA invalida")
		}
		l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			return nil, 0, fmt.Errorf("abrir PORTA %d: %w", p, err)  // <- e acabou
		}
		return l, p, nil
	}
	for p := 5000; p <= 5099; p++ { ... }   // a descoberta so roda sem PORTA
}
```

**Por que é um problema.** O `README.md:216` oferece `PORTA` como um ajuste
comum — *"Fixa uma porta em vez de usar a descoberta entre 5000–5099"* — sem
dizer que ela é incompatível com o ambiente que o projeto inteiro existe para
atender. E o único exemplo de configuração que o repositório tem é o instalador,
que grava **tudo** no escopo `Machine` (`SISTEMA_URL`, `CORS_ORIGEM`,
`COMPARADOR_*` — linhas 158, 163, 178-183). É o padrão que o administrador vai
copiar.

Num servidor RDS, uma `PORTA` de máquina significa que **as N sessões tentam
escutar o mesmo `127.0.0.1:P`**. O Go não liga `SO_REUSEADDR` em listeners TCP no
Windows — deliberadamente, para que um socket não sequestre a porta de outro —,
então a primeira sessão vence e todas as outras recebem `WSAEADDRINUSE`.

A partir daí o desenho do supervisor amplifica o dano:

```go
// supervisor.go:34-55 — nao distingue "erro de configuracao" de "crash"
for {
	cmd := exec.Command(exe)
	...
	if cmd.Wait() == nil { os.Exit(0) }
	time.Sleep(espera)   // 2s, 4s, 8s ... ate 1 minuto, para sempre
}
```

`executa()` devolve `1` (`main.go:882-886`) para um erro **determinístico**: a
porta vai estar ocupada na próxima tentativa também, e na seguinte. O supervisor
não sabe disso e reinicia o filho para sempre, um processo por minuto, por
sessão, indefinidamente. O usuário não vê nada: sem listener não há bandeja, sem
bandeja não há ícone, e o `agente.log` de cada perfil — onde a linha
`ERRO: listener: abrir PORTA 5000: ...` de fato aparece — é o último lugar em que
alguém olha quando "a biometria não abre em lugar nenhum".

**`MODO_COMPARADOR` é a mesma armadilha, mais silenciosa ainda:**

```go
// main.go:860 — antes do supervisor, antes da bandeja
if os.Getenv("MODO_COMPARADOR") == "1" || (len(os.Args) > 1 && os.Args[1] == "--comparador") {
	return rodaComparador()
}
```

A variável não é mencionada em nenhum lugar do `README.md` nem do
`COMO-USAR.md` — só existe no código. Quem a descobrir e a definir no ambiente da
máquina (que é onde as outras três `COMPARADOR_*` moram) converte **todo agente de
toda sessão** em comparador: sem bandeja, sem leitor, todos disputando a porta
5150. O primeiro sobe; os demais saem com `1` e caem no mesmo laço do supervisor.

**Como corrigir.** Três correções pequenas e independentes:

1. **Não reiniciar erro determinístico.** Um código de saída dedicado para falha
   de configuração, que o supervisor respeita:

   ```go
   const saidaConfiguracaoInvalida = 2

   // supervisor.go
   if err := cmd.Wait(); err != nil {
       if code, ok := codigoDeSaida(err); ok && code == saidaConfiguracaoInvalida {
           os.Exit(code)   // reiniciar nao vai consertar
       }
       ...
   }
   ```

2. **Degradar em vez de morrer.** Se a `PORTA` fixa estiver ocupada, cair na
   faixa de descoberta e registrar o motivo — é o comportamento que o usuário
   espera, e o JS já varre 5000–5099:

   ```go
   l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p))
   if err != nil {
       registraErro("PORTA %d ocupada (outra sessao?); voltando a descoberta: %v", p, err)
   } else {
       return l, p, nil
   }
   ```

3. **Dizer o escopo no README.** A tabela de Configuração precisa separar as
   variáveis que podem ser de máquina (`SISTEMA_URL`, `CORS_ORIGEM`,
   `COMPARADOR_URL`) das que **têm** de ser por usuário (`PORTA`,
   `NBIOBSP_DLL` quando as sessões usam SDKs diferentes) — e documentar
   `MODO_COMPARADOR` como interna, ou removê-la em favor do `--comparador` que o
   instalador já usa.

---

## 🟡 Alertas (recomenda correção)

### A1. O `comparador.log` não está onde a documentação diz — o binário é 386 e o serviço é SYSTEM

**Arquivos:** `storage.go:13-19`, `comparador.go:34`, `worker.go:68`,
`instalador/instalar-servidor.ps1:186-191`, `README.md:271,285-288`

```go
// storage.go:13-19
var diretorioDados = func() string {
	base := os.Getenv("LOCALAPPDATA")
	...
	return filepath.Join(base, "BiometriaAgente")
}
```

**Por que é um problema.** Para o agente de uma sessão isso resolve em
`C:\Users\<user>\AppData\Local\BiometriaAgente`, que é o caminho que o
`README.md` documenta. Para o comparador não: ele roda como `SYSTEM`
(`ps1:187`), cujo `LOCALAPPDATA` é
`C:\Windows\system32\config\systemprofile\AppData\Local`.

E o executável é **32 bits** — exigência do projeto, validada pelo próprio
instalador (`ps1:130`). O redirecionador de sistema de arquivos do WOW64 reescreve
`%windir%\System32` para `%windir%\SysWOW64` em qualquer processo de 32 bits, e
esse caminho está sob `System32`. O `comparador.log` e o `worker.log` do
comparador acabam, na prática, em:

```
C:\Windows\SysWOW64\config\systemprofile\AppData\Local\BiometriaAgente\
```

Quem abrir o Explorer (64 bits) ou um PowerShell (64 bits) no caminho
"documentado" encontra um diretório vazio e conclui que o serviço não está
logando. Some justamente o registro do processo que a
[revisão de 08-01](revisao-sistema-2026-08-01.md) apontou como o mais crítico e o
mais difícil de depurar — o que roda em sessão 0, sem console e sem interface.

**Confirmação no servidor** (não dá para verificar daqui):

```powershell
Get-ChildItem C:\Windows\SysWOW64\config\systemprofile\AppData\Local\BiometriaAgente
Get-ChildItem C:\Windows\System32\config\systemprofile\AppData\Local\BiometriaAgente
```

**Como corrigir.** Não deixar o caminho depender do bitness nem do perfil do
serviço. O modo comparador é um serviço de máquina e os logs dele são de máquina:

```go
// no modo comparador, gravar ao lado do executavel instalado, ou em ProgramData
func diretorioDadosServico() string {
	if base := os.Getenv("ProgramData"); base != "" {
		return filepath.Join(base, "BiometriaAgente")
	}
	return diretorioDados()
}
```

E, em qualquer caso, `rodaComparador` deve **imprimir** o caminho do log logo
depois de abri-lo — hoje ele imprime só o endereço de escuta (`comparador.go:95`).

---

### A2. `--teste-delegacao` engole o motivo real da falha — justo o comando que o instalador manda rodar

**Arquivos:** `autoteste.go:458-468`, `delegacao.go:49-66`, `log.go:12`,
`main.go:851-853,871`, `instalador/instalar-servidor.ps1:217-218`

```go
// autoteste.go:461-468
ligaConsole()

configuraComparador()
if comparadorRemoto == nil {
	fmt.Println("Defina COMPARADOR_URL e COMPARADOR_TOKEN antes de rodar este teste.")
	fmt.Println("Exemplo: set COMPARADOR_URL=http://127.0.0.1:5150")
	return 1
}
```

**Por que é um problema.** `configuraComparador` sabe exatamente por que desistiu
e registra em três variantes distintas (`delegacao.go:57,64`):

- `COMPARADOR_URL invalida (%q)`
- `COMPARADOR_TOKEN ausente ou curto demais`
- (ou nem chega lá: `COMPARADOR_URL` vazia)

Só que o despacho dos comandos de diagnóstico acontece **antes** de `iniciaLog()`
(`main.go:851-853` versus `main.go:871`), e `logger` nasce escrevendo em
`io.Discard` (`log.go:12`). As três mensagens vão para lugar nenhum. O que sobra
na tela é uma frase única que só cobre um dos casos, e que aponta para a causa
errada nos outros dois.

O agravante é o contexto. O instalador termina dizendo:

```
Os agentes so passam a delegar no proximo logon, quando leem o ambiente da maquina.
Confira de dentro de uma sessao RDP com: AgenteBiometria.exe --teste-delegacao
```

Uma sessão RDP que já estava aberta quando o comparador foi instalado **não tem**
as variáveis de máquina no ambiente — é o que a linha anterior do próprio
instalador acabou de dizer. Rodar o comando ali imprime "Defina COMPARADOR_URL e
COMPARADOR_TOKEN", que lido literalmente significa "a instalação não configurou
nada", quando a verdade é "faça logoff e logon". O único comando que existe para
validar a instalação da delegação é o que dá o diagnóstico mais enganoso dela.

**Como corrigir.** Duas linhas em cada ponta. Fazer `configuraComparador`
devolver o motivo em vez de só registrá-lo:

```go
func configuraComparador() error { ... }   // nil = ligou, err = por que nao ligou
```

e imprimi-lo no teste, junto do que foi realmente lido do ambiente:

```go
if err := configuraComparador(); err != nil {
	fmt.Println("delegacao desligada:", err)
	fmt.Printf("COMPARADOR_URL=%q  COMPARADOR_TOKEN=%d caracteres (minimo 32)\n",
		os.Getenv("COMPARADOR_URL"), len(os.Getenv("COMPARADOR_TOKEN")))
	fmt.Println("Se o comparador foi instalado agora, faca logoff e logon: as")
	fmt.Println("variaveis de maquina so entram no ambiente de uma sessao nova.")
	return 1
}
```

Chamar `iniciaLog()` no começo de cada comando de diagnóstico resolve o problema
maior por trás deste: hoje `salvaTemplate`, `confereTemplate` e `testeDelegacao`
rodam todos com o log desligado (ver S3).

---

### A3. Cada certificado gerado vira uma raiz confiável permanente — e a desinstalação preserva todas

**Arquivos:** `cert.go:47,72,92-113,115-123`,
`instalador/instalar-servidor.ps1:104-118`, `README.md:135`

```go
// cert.go:115-123 — roda em TODA subida do agente
func instalaCertificadoUsuario(certPath string) error {
	cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
	...
}
```

**Por que é um problema.** O certificado tem validade de 2 anos (`cert.go:72`) e é
regenerado sempre que faltarem menos de 30 dias para o vencimento — ou sempre que
o par `cert.pem`/`key.pem` não estiver legível em `%LOCALAPPDATA%`
(`cert.go:95,31-53`). Cada certificado novo é acrescentado à loja **Root do
usuário**, e nada nunca remove o anterior: `-addstore -f` sobrescreve uma entrada
de mesma impressão digital, não a entrada antiga de uma chave diferente.

Numa estação isso é um certificado a cada dois anos — irrelevante. Numa fazenda
RDS com perfil de roaming, não: `AppData\Local` é **excluído do roaming por
padrão**, enquanto a loja de certificados do usuário viaja no `NTUSER.DAT`. O
resultado é um certificado novo por servidor em que a pessoa loga pela primeira
vez, todos acumulando no mesmo perfil que roda de máquina em máquina.

E o ciclo não fecha. O `-Desinstalar` remove o executável, a chave `Run`, a tarefa
agendada e as três variáveis do comparador (`ps1:104-118`), mas **preserva os
dados do usuário** — o `README.md:135` anuncia isso como recurso. Depois de
desinstalado o produto, ficam para trás, em cada perfil: N certificados
autoassinados válidos para `localhost` marcados como raízes confiáveis, e a chave
privada de cada um em texto claro num arquivo `key.pem`. Qualquer processo do
próprio usuário pode ler essa chave, abrir um socket em `localhost` e ser aceito
pelo navegador sem aviso.

O risco é contido — o certificado não é CA (`cert.go:77`, sem `IsCA`), então só
vale para si mesmo, e a chave só é legível pelo dono do perfil. Mas é um resíduo
que ninguém pediu, que cresce sozinho e que sobrevive à decisão de parar de usar
o produto.

**Como corrigir.**

1. Remover a raiz anterior antes de instalar a nova, guardando a impressão digital
   junto do certificado:

   ```go
   // ao regenerar: certutil -user -delstore Root <thumbprint antigo>
   ```

2. Acrescentar ao `-Desinstalar` uma opção `-RemoverCertificados` que apague
   `cert.pem`/`key.pem` e a entrada da loja Root de cada perfil, e mencioná-la no
   `README.md` ao lado da frase sobre preservação.

3. Reduzir a validade para algo como 90 dias com renovação automática — hoje a
   janela em que uma chave vazada continua sendo aceita é de dois anos.

---

### A4. O token da sessão entra na linha de comando do `rundll32` — e daí no log de auditoria

**Arquivos:** `main.go:635-645`, `main.go:770-776`

```go
// main.go:644
return fmt.Sprintf("%s%sbioPort=%d&bioToken=%s", base, separador, porta, token)

// main.go:774 — o menu "Abrir sistema"
if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", urlSistema()).Start(); err != nil {
```

**Por que é um problema.** O lado do navegador está bem resolvido: o fragmento
não é enviado ao servidor e o `integra-biometria.js:31-33` limpa a barra de
endereços com `history.replaceState` assim que lê os valores. O que ninguém
tratou é o lado do Windows — a URL inteira, com o token de 256 bits em claro,
vira **argumento de processo**:

- fica visível em `Win32_Process.CommandLine` para o próprio usuário e para
  qualquer administrador da máquina, enquanto o `rundll32` viver;
- e, quando a política *"Incluir linha de comando em eventos de criação de
  processo"* está ligada — configuração comum em ambiente corporativo auditado —,
  vai inteira para o **evento 4688 do log de Segurança**, que é retido por meses e
  normalmente encaminhado a um SIEM.

O token é a única credencial da API local: com ele, e com a porta que está na
mesma string, qualquer processo do host fala com o agente sem passar pela
autorização de origem. Ele foi projetado para viver só na `sessionStorage` de uma
aba — a seção *Segurança* do `README.md:228,230` descreve exatamente isso —, e o
caminho do menu **Abrir sistema** o publica num log que sobrevive ao processo.

**Como corrigir.** Não passar o segredo pela linha de comando. O agente já grava
`agente-<sessão>.json` com porta e token (`main.go:607-633`); a URL pode carregar
só um identificador de uso único que o JS troca por token no `/api/hello`:

```go
// em vez de bioToken=<token>
return fmt.Sprintf("%s%sbioPort=%d&bioNonce=%s", base, separador, porta, nonceDeUsoUnico())
```

Correção mínima, se a troca de desenho ficar para depois: manter só `bioPort` na
URL e deixar o token para a auto-descoberta (`/api/hello` já o devolve para uma
origem autorizada da mesma sessão, `main.go:300-303`) — o menu passa a abrir o
sistema sem nenhum segredo no comando.

---

## 🟢 Sugestões (opcional)

**S1. `modulosBiometricos` não filtra mais nada — o nome ficou mentindo**
(`versaodll.go:55-113`). O próprio comentário conta que a versão que filtrava por
nomes da NITGEN foi descartada, e com razão: era ela que escondia o
`ftapihook32.dll`. Hoje a função lista todos os módulos do processo, que é o
comportamento certo com o nome errado. `modulosCarregados` diria a verdade, e as
sete chamadas cabem num `sed`.

**S2. `templateValido` (`sdk.go:423-425`) só é usado pelos testes**
(`sdk_test.go:58,70`). O código de produção chama `normalizaTemplate` direto e
compara com `""`. Ou o *helper* vira o caminho oficial nos handlers — que é mais
legível que `if x == ""` — ou ele sai e os testes passam a exercitar o que a
produção usa.

**S3. Nenhum comando de diagnóstico chama `iniciaLog()`.** `rodaAutoteste`,
`salvaTemplate`, `confereTemplate` e `testeDelegacao` (`main.go:842-853`) rodam
antes de `iniciaLog()` (`main.go:871`), então todo `registraErro` disparado lá
dentro — incluindo o `fechar leitor apos captura` de `sdk.go:283-285` e os três
motivos de `configuraComparador` — cai em `io.Discard`. Uma linha no começo de
cada comando resolve, e o relatório do `--autoteste` ganha o contexto que hoje se
perde.

**S4. O `.gitignore` protege `template*.txt`, mas o `README.md` ensina nome
livre.** A regra (`.gitignore:16`, com um comentário exemplar sobre dado pessoal
irrevogável) cobre o padrão sugerido, e `--salvar-template <arquivo>`
(`README.md:272`) aceita qualquer caminho. Documentar a convenção junto do
comando — *"grave sempre como `template-<algo>.txt`, que o `.gitignore` já
cobre"* — fecha a distância entre a regra e o uso.

**S5. `escreveConfig` grava o `pid` e ninguém o lê** (`main.go:626`). O campo já
está lá; a opção 3 do `COMO-USAR.md:81-83` só precisaria mandar o backend
conferir se o processo existe antes de confiar na porta e no token. É metade da
sugestão S3 de 08-02, e a metade que não exige mexer no agente.

---

## 📋 Resumo

- **Arquivos alterados**: 0 no código (`git diff origin/main...HEAD` vazio);
  23 arquivos analisados, 1 documento acrescentado por esta revisão
- **Segurança**: 🚨 Risco — os achados anteriores seguem todos abertos
  (comparador `SYSTEM` com entrada de usuário sem privilégio, token de máquina
  legível por todos, `bioPort` sem validação), e somam-se dois novos vazamentos
  de menor alcance: token na linha de comando (A4) e raízes confiáveis órfãs (A3)
- **Qualidade**: ⚠️ Atenção — `build` e `vet` limpos, comentários de primeira
  linha; o problema é de configuração e documentação, não de código
- **Risco de produção**: 🚨 Alto — inalterado, e o C1 desta revisão acrescenta um
  modo de falha novo: uma variável documentada no README que apaga a biometria de
  todas as sessões menos uma, sem sintoma visível
- **Testes**: ⚠️ Parcial — 33 funções de teste, boa cobertura de normalização,
  memória nativa, worker e delegação; nenhuma roda fora de um `windows/386` real
  e continua sem CI (sexto dia). `middleware`, `origins.go`, `session.go`,
  `cert.go` e `storage.go` seguem sem um único teste

### Situação dos achados anteriores

Nenhuma linha de código mudou desde 2026-07-31, então **todos** continuam
abertos. A tabela existe para tornar o estado explícito.

| Achado | Origem | Situação |
|---|---|---|
| Comparador roda como `SYSTEM` com entrada de usuário sem privilégio | 08-01 C1 | 🔴 aberto |
| 1:N congela a comparação de todas as sessões | 08-01 C2 / 07-31 C1 / 07-30 C4 | 🔴 aberto |
| `-ComparadorPorta` sem `-ComparadorUrl` → instalação que nunca compara | 08-01 C3 | 🔴 aberto |
| `bioPort` do fragmento não validado (vaza token e template) | 08-01 C4 / 07-30 C1 | 🔴 aberto |
| Cliente JS descarta `ignorados` | 08-01 C5 / 07-30 C2 | 🔴 aberto |
| 1:N como laço de 1:1 (falsa aceitação acumulada) | 08-01 C6 / 07-30 C3 | 🔴 aberto |
| `/status` do comparador responde `ok:true` com o SDK inutilizável | 07-31 C2 | 🟡 aberto |
| Teto real de 1:N invisível (`maxCandidatos` × `maxCorpoIdentificar`) | 08-02 A1 | 🟡 aberto |
| `CORS_ORIGEM` inválida descartada em silêncio | 08-02 A2 | 🟡 aberto |
| Instância única protege o supervisor, não o agente | 08-02 A3 | 🟡 aberto |
| Vazamento do `EnumerateDevice` reduzido, não limitado | 08-02 A4 | 🟡 aberto |
| Comparador sem `recover`, sem `limiteHTTP`, sem `ErrorLog` | 08-01 A1 | 🟡 aberto |
| Rotação de log só no *boot* | 08-01 A2 / 07-31 A3 / 07-30 A3 | 🟡 aberto |
| Worker não registra os módulos carregados | 08-01 A3 / 07-31 A2 | 🟡 aberto |
| `COMPARADOR_URL` aceita host remoto e HTTP puro | 08-01 A4 | 🟡 aberto |
| Sem verificação de saúde do comparador | 08-01 A5 / 07-31 A1 | 🟡 aberto |
| Reinstalação deixa comparador e agentes em versões diferentes | 08-01 A6 | 🟡 aberto |
| Integração agente↔comparador não exercitada em teste | 08-01 A7 | 🟡 aberto |
| Sem CI | 08-01 A8 / 07-31 A8 / 07-30 A8 | 🟡 aberto |
| `ignorados` descartado no handler quando há erro | 08-01 A9 / 07-30 A1 | 🟡 aberto |
| Troca de segurança do token da máquina fora do README | 08-01 A10 | 🟡 aberto |
| Demais alertas de 07-30 (A2, A4–A7, A9–A14) e de 07-31 (A4–A7) | 07-30 / 07-31 | 🟡 abertos |

Uma exceção, pequena mas real: o `.gitignore` **passou** a cobrir
`template*.txt`, `cert.pem`, `key.pem`, `agente-*.json` e `origens-autorizadas.json`
— o alerta A7 de 07-31 (templates em texto claro sem regra de ignore) está
parcialmente atendido (ver S4).

---

## ✅ Pontos positivos

**A cadeia de tempo-limite continua sendo o melhor exemplo de projeto do
repositório.** Cada elo tem um comentário dizendo por que o número é aquele, e os
números fecham: `WriteTimeout` de 5 min > contexto de `/identificar` de 4 min >
prazo do worker de 3 min (`main.go:919`, `main.go:537`, `worker.go:340-343`); o
contexto da captura é `timeout + 25s` porque o prazo do worker é `timeout + 15s`
(`main.go:360-363`, `worker.go:329`); e o cliente HTTP da delegação não tem
`Timeout` fixo justamente para não cortar pela metade a busca longa
(`delegacao.go:70-72`). É raro ver uma cadeia dessas sem um elo esquecido.

**O `clienteWorker` trata a fronteira de processo com o rigor que ela exige.** O
decodificador é capturado numa variável local antes de subir a goroutine
(`worker.go:285-295`), então uma resposta atrasada do worker morto nunca chega ao
decodificador do worker novo; os canais são bufferizados para que a goroutine
órfã termine em vez de vazar; e `derruba()` devolve o *exit status* para o log
distinguir saída limpa de `0xC0000005` (`worker.go:241-262`). Cada detalhe é um
bug que costuma aparecer só em produção.

**A ordem dos `defer` em `capturaTexto` está certa pelo motivo certo**
(`sdk.go:304-326`). `NBioAPI_FreeTextFIR` precisa rodar antes do `LocalFree` da
struct que ele lê, e o `defer` só é armado depois que o SDK confirma um ponteiro
não nulo — "chamar `FreeTextFIR` sobre a struct zerada seria pedir para ele
liberar um endereço que ele nunca alocou". Esse comentário vale mais que o código.

**`escreveConfig` sanitiza o `SESSIONNAME` antes de usá-lo como nome de arquivo**
(`main.go:616-624`), com uma lista de permissão em vez de uma de bloqueio, e ainda
tem um valor de reserva para quando a sanitização esvazia tudo. É o tipo de
cuidado que quase sempre falta em código que monta caminho a partir de variável de
ambiente.

**A validação do PE no instalador lê o cabeçalho, não a extensão**
(`ps1:80-95`): assinatura `MZ`, deslocamento do `PE\0\0` conferido contra o
tamanho do arquivo antes de ser usado como posição, e só então a máquina `0x014C`.
E `Remove-DiretorioInstalacao` (`ps1:97-102`) se recusa a apagar qualquer caminho
fora da instalação — proteção que quase nenhum desinstalador tem.

**O `.gitignore` explica por que a regra existe**, e não só o que ela ignora:
*"Templates biometricos NUNCA entram no repositorio: sao dado pessoal
irrevogavel, e este repositorio e publico."* Uma regra com motivo escrito é uma
regra que ninguém remove por engano.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O código segue sólido e o desenho segue correto. O que esta revisão encontrou de
novo não contradiz isso — os quatro alertas e o crítico têm todos a mesma
natureza: **decisões tomadas quando o agente atendia uma pessoa numa estação, que
não foram revistas quando ele virou infraestrutura de um servidor multi-sessão.**
A configuração é de máquina (C1), o log do serviço cai no perfil errado (A1), o
comando que valida a instalação não enxerga o ambiente que a instalação criou
(A2), o certificado por usuário acumula numa fazenda de servidores (A3) e o token
por sessão vaza para um log de máquina (A4).

Nenhum deles é grande. Todos os cinco cabem, juntos, em menos linhas do que este
documento.

O que não muda desde 08-01 é a recomendação prática, e ela fica mais urgente a
cada dia: **os seis críticos de segurança seguem abertos há até seis dias, em
cinco PRs de revisão que ninguém mergeou.** A fila de revisão está crescendo mais
rápido que a de correção, e este documento é o quinto. Antes de qualquer coisa
nova, valeria fechar #1, #3, #4 e #5 num consolidado e abrir um PR de código com
as três correções de segurança que são pequenas: `LocalService` no lugar de
`SYSTEM`, validação de `bioPort` no JS, e `ignorados` no retorno de
`Biometria.identificar`.
