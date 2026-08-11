# 🔍 Revisão técnica do sistema — 2026-08-11

> ⚠️ **`main` continua em `26c9379`.** Os PRs **#10**, **#11**, **#12** e **#13**
> seguem abertos e nenhum crítico foi tocado. Este é o quinto dia consecutivo.

As quatro revisões anteriores esgotaram o caminho de dados: captura, template,
worker, delegação, comparador. Todas partiram do mesmo ponto de entrada — o
sistema web já autorizado — e perguntaram o que acontece dali para dentro.

Esta revisão entra por outra porta: **como uma origem web vira uma origem
autorizada**. É o único ponto do sistema onde a decisão de segurança é tomada por
uma pessoa, olhando uma tela, e é o único que nenhuma das quatro revisões
examinou — `origins.go` e o menu da bandeja aparecem em todas elas apenas na
linha "sem cobertura de teste".

O resultado são **dois críticos novos** que formam uma cadeia única, alcançável
de qualquer site da internet, **sem nenhum acesso local à máquina** — a condição
que o PR #13 identificou corretamente como a que separa um risco alto de um
risco teórico, ao rebaixar o `bioPort`.

**Escopo analisado:** os 22 arquivos `.go` (5.915 linhas com os 7 de teste),
`integracao/integra-biometria.js`, `integracao/COMO-USAR.md`,
`instalador/instalar-servidor.ps1`, `instalador/msi/AgenteBiometria.wxs`,
`instalador/msi/build-msi.cmd`, `instalador/msi/AgenteBiometria.wixproj`,
`conferir-biometria.cmd`, `embutir-icone.py`, `go.mod`/`go.sum`, `.gitignore`,
`README.md` e os três documentos em `docs/`.

**Verificações executadas:**

| Comando | Resultado |
|---|---|
| `GOOS=windows GOARCH=386 go build ./...` | OK |
| `GOOS=windows GOARCH=386 go vet ./...` | limpo, inclusive nos arquivos de teste |
| `go test ./...` | **não executável aqui** — `matched no packages`: as *build tags* `windows && 386` excluem todos os arquivos |
| Reprodução isolada de `tituloOrigem` + `normalizaOrigem` (cópia fiel, compilada e executada) | confirma **C1**: o domínio do atacante some da tela |
| Serialização real de um corpo de `/identificar` com 5.000 candidatos | confirma **A3**: o limite de 16 MB corta antes dos 5.000 anunciados |

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O diálogo de autorização da bandeja pode ser forjado — `tituloOrigem` corta justamente o domínio que decide *(novo)*

**Arquivos:** `main.go:655-661`, `main.go:696-703`, `main.go:224-241`,
`main.go:288-299`, `origins.go:98-129`

```go
// main.go:655-661 — o unico lugar onde o usuario ve a origem
func tituloOrigem(origem string) string {
	const max = 72
	if len(origem) <= max {
		return origem
	}
	return origem[:max-3] + "..."
}
```

```go
// main.go:697-703 — usado no item de menu e no tooltip, e em nenhum outro lugar
p := &pendencia{
	item:     pai.AddSubMenuItem(tituloOrigem(origem), "Autorizar esta origem web"),
	cancelar: make(chan struct{}),
}
itens[origem] = p
pai.Enable()
systray.SetTooltip("Biometria: autorizacao pendente para " + tituloOrigem(origem))
```

**Por que é um problema.** Um nome de domínio é lido **da direita para a
esquerda**: quem registra `sistema.exemplo.com.qualquer-coisa.atacante.example` é
o dono de `atacante.example`, e tudo à esquerda é escolha livre dele.
`tituloOrigem` corta **a direita** — ou seja, corta exatamente a parte que
determina de quem é o domínio, e preserva exatamente a parte que o atacante
controla.

Reproduzi a função e `normalizaOrigem` num programa isolado, compilado e
executado:

```
real     (27 bytes): https://sistema.exemplo.com
bandeja           : https://sistema.exemplo.com

real     (86 bytes): https://sistema.exemplo.com.portal-de-autorizacao-da-biometria-remota.atacante.example
bandeja           : https://sistema.exemplo.com.portal-de-autorizacao-da-biometria-remota...
```

O usuário vê o domínio do próprio sistema, seguido de um texto que descreve o que
ele está prestes a fazer, e **`atacante.example` não aparece em lugar nenhum**. O
tooltip do item de menu é a string fixa `"Autorizar esta origem web"`, então não
há um segundo lugar com o valor completo. O log só registra a origem **depois**
da aprovação (`main.go:721`) — tarde demais para a decisão.

**E o prompt é disparável de qualquer site.** O middleware libera `/api/hello`
sem autorização prévia, de propósito:

```go
// main.go:224-241
hello := r.URL.Path == "/api/hello"
permitida := origem != "" && autorizacoes.permitida(origem)
...
if origem != "" {
	aplicaCORS(w, origem)
	if !hello && !permitida {          // <- /api/hello passa mesmo sem autorizacao
		escreveErro(w, http.StatusForbidden, "origem nao autorizada")
		return
	}
}
```

E `aplicaCORS` responde com `Access-Control-Allow-Private-Network: true`
(`main.go:200`) para **qualquer** origem, que é justamente o cabeçalho que faz o
navegador liberar a chamada de um site público para o `localhost`. A varredura de
portas 5000–5099 que o cliente oficial faz (`integra-biometria.js:179-189`)
qualquer página pode fazer igual.

Encadeando: o usuário visita uma página qualquer → ela varre as portas, acha o
agente e chama `/api/hello` → `origens.solicita` cria a pendência
(`origins.go:98-129`) → a bandeja pisca *"autorizacao pendente"* e ganha um item
com o nome forjado. Na próxima vez que o usuário abrir a bandeja para autorizar o
sistema de verdade — que é o momento em que ele **espera** encontrar um item para
clicar — ele encontra dois, ambos começando com o endereço do sistema. Um clique
errado grava a origem do atacante em `origens-autorizadas.json`
(`origins.go:131-138`), **permanentemente**, e a partir daí ela chama
`/api/public/v1/captura/Capturar` e `/api/public/v1/captura` com o token vivo:
captura a digital de quem estiver no leitor e recebe o template.

O PR #13 estabeleceu o critério certo ao rebaixar o `bioPort`: o que separa risco
alto de risco teórico é precisar ou não de acesso local. **Este não precisa.**

**Como corrigir.** Duas mudanças pequenas, e a primeira é a que decide:

```go
// main.go — mostrar o host inteiro, sempre, e elidir o MEIO, nunca o fim.
// O host e o que decide; o caminho e o esquema sao contexto.
func tituloOrigem(origem string) string {
	const max = 72
	if len(origem) <= max {
		return origem
	}
	// Preserva as duas pontas: o inicio situa, o fim identifica o dono.
	cabeca := max/2 - 2
	cauda := max - cabeca - 3
	return origem[:cabeca] + "..." + origem[len(origem)-cauda:]
}
```

```go
// origins.go — recusar de saida o que nao cabe na tela de decisao. Um host
// legitimo nao passa disso; um host longo demais existe para nao ser lido.
const maxTamanhoOrigem = 64

func normalizaOrigem(valor string) (string, error) {
	...
	if len(u.Host) > maxTamanhoOrigem {
		return "", errors.New("origem longa demais para ser autorizada com seguranca")
	}
```

Vale ainda pôr o host em destaque no item — `AddSubMenuItem("Autorizar "+u.Hostname(), origem)` deixa o valor completo no tooltip, que é onde ele deveria estar desde o começo.

---

### C2. Uma pendência expirada nunca sai da bandeja — o item forjado espera indefinidamente, e a lista cresce sem limite *(novo)*

**Arquivos:** `main.go:663-744`, `origins.go:98-129`, `origins.go:17-20`

Os dois lados discordam sobre quanto tempo uma pendência vive. `origins.go` diz
dez minutos:

```go
// origins.go:17-20 e 98-105
const (
	maxOrigensPendentes = 8
	validadePendente    = 10 * time.Minute
)

func (g *gerenciadorOrigens) solicita(origem string) bool {
	g.mu.Lock()
	agora := time.Now()
	for pendente, criadaEm := range g.pendentes {
		if agora.Sub(criadaEm) >= validadePendente {
			delete(g.pendentes, pendente)      // <- expira aqui
		}
	}
```

A bandeja diz *para sempre*. `remove(origem)` só é chamado em três lugares
(`main.go:715-739`): quando o usuário **clica** no item, quando ele **revoga
tudo**, e no `defer` de encerramento. **Não existe caminho de expiração.**

```go
// main.go:715-726 — os unicos caminhos que removem um item
case decisao := <-decisoes:
	remove(decisao.origem)
	...
case _, ok := <-revogar.ClickedCh:
	...
	for origem := range itens {
		remove(origem)
	}
```

**Por que é um problema.** Três consequências, em ordem de gravidade:

1. **O item forjado do C1 espera o tempo que for preciso.** Sem isto, o ataque
   dependeria de o usuário abrir a bandeja na mesma janela de dez minutos em que
   visitou a página — coincidência improvável. Com isto, o atacante planta o item
   hoje e ele continua clicável na semana que vem, até que o usuário tenha um
   motivo próprio para abrir a bandeja. **É o C2 que transforma o C1 de
   possibilidade em algo com que se pode contar.**
2. **O clique num item expirado ainda autoriza.** `aprova()` (`origins.go:131-138`)
   grava em `aprovadas` sem consultar `pendentes`. Uma pendência que o
   `gerenciadorOrigens` já descartou por velha vira autorização permanente pelo
   caminho normal.
3. **A lista cresce sem teto.** `maxOrigensPendentes = 8` limita o mapa
   `pendentes`, que se esvazia sozinho a cada dez minutos — mas **não** limita
   `itens`, que só cresce. Uma página que troca de subdomínio (`a1.evil.example`,
   `a2.evil.example`, …) planta 8 entradas a cada 10 minutos: ~1.150 por dia,
   cada uma com um item de menu do Windows e uma goroutine bloqueada em
   `ClickedCh` até o processo morrer. O menu fica inutilizável muito antes de a
   memória importar — e um menu inutilizável é, por si só, a negação do único
   controle de autorização que o sistema tem.

**Como corrigir.** Fazer a bandeja respeitar o mesmo prazo que o gerenciador já
aplica:

```go
// main.go, em gerenciaMenuOrigens — um ticker basta, o estado ja existe.
type pendencia struct {
	item     *systray.MenuItem
	cancelar chan struct{}
	criadaEm time.Time
}

limpeza := time.NewTicker(time.Minute)
defer limpeza.Stop()

// ...dentro do select:
case <-limpeza.C:
	for origem, p := range itens {
		if time.Since(p.criadaEm) >= validadePendente {
			remove(origem)
		}
	}
	if len(itens) == 0 {
		pai.Disable()
	}
```

E fechar o item 2, que é o que realmente decide — a bandeja é interface, e
interface não deve ser a única guardiã de um prazo:

```go
// origins.go — so aprova o que ainda esta pendente de verdade.
func (g *gerenciadorOrigens) aprova(origem string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	criadaEm, pendente := g.pendentes[origem]
	if !pendente || time.Since(criadaEm) >= validadePendente {
		return errors.New("pedido de autorizacao expirado; recarregue a pagina e tente de novo")
	}
	delete(g.pendentes, origem)
	g.aprovadas[origem] = struct{}{}
	return g.salvaBloqueado()
}
```

---

### Reincidentes — reconferidos hoje, linha a linha

Nenhum arquivo mudou desde `26c9379`. Não repito os textos; todos continuam
válidos nas mesmas linhas.

| Achado | Onde | Situação |
|---|---|---|
| **C1 #13** — fila única do SDK no comparador; sem `limiteHTTP` no modo serviço | `comparador.go:73`, `comparador.go:102`, `main.go:100-105` | ❌ Aberto |
| **C2 #13** — ACL herdada do `ProgramData` + `endereco` do anúncio sem checagem de loopback | `wxs:57-62`, `123-135`, `delegacao.go:74-85` | ❌ Aberto |
| **C1 #12** — parar o serviço apaga o anúncio; token novo derruba os agentes de pé | `comparador.go:99`, `anuncio.go:113-121` | ❌ Aberto |
| **C4 #12** — MSI e `.ps1` disputam nome e porta | `wxs:71-99`, `instalar-servidor.ps1:167-193` | ❌ Aberto, e com uma consequência nova: ver **A5** |
| **R1 #13** — `bioPort` sem validação (negação de serviço, não vazamento) | `integra-biometria.js:25-34` | ❌ Aberto |
| **C2 de 2026-07-30** — o cliente JS descarta `ignorados` | `integra-biometria.js:222-230` | ❌ Aberto. `return { confere: ..., id: ... }` inalterado: um cadastro corrompido continua chegando ao operador como "não confere" |
| **A1..A4 #13, A1..A11 e S1..S10 #12** | — | ❌ Todos abertos |

---

## 🟡 Alertas (recomenda correção)

### A1. O worker do comparador continua gravando log no perfil do SYSTEM — a correção foi aplicada ao pai e não ao filho *(novo)*

**Arquivos:** `worker.go:68`, `log.go:24-31`, `comparador.go:40`

O `log.go` explica por que `iniciaLogEm` existe, e a explicação é exata:

```go
// log.go:24-30
// Existe para o comparador: rodando como servico, o diretorio de dados do
// usuario e o perfil do SYSTEM, e o log ficaria enterrado em
// C:\Windows\SysWOW64\config\systemprofile - um lugar que ninguem procura e que
// o instalador nao consegue limpar.
```

O comparador usa a função nova (`comparador.go:40`). **O worker que ele sobe, não:**

```go
// worker.go:68 — dentro do processo filho, que herda o contexto do pai
iniciaLogArquivo("worker.log")   // -> diretorioDados() -> %LOCALAPPDATA%
```

**Por que é um problema.** Sob o serviço, o worker roda como `LocalSystem`, então
`worker.log` vai para
`C:\Windows\SysWOW64\config\systemprofile\AppData\Local\BiometriaAgente\` — o
lugar exato que o comentário do `log.go` descreve como "ninguém procura". E o que
está nesse arquivo não é acessório: é o lado da comparação onde a `VerifyMatch`
realmente acontece.

```go
// worker.go:150-152 — a metade que falta para o par funcionar
// Par com a impressao registrada em agente.log: se as duas nao baterem,
// os bytes mudaram ao atravessar o processo, e nao dentro do SDK.
registraInfo("comparar: recebeu a=[%s] b=[%s]", ...)
```

O desenho de diagnóstico do projeto — comparar a impressão dos dois lados para
saber onde os bytes mudaram — depende de as duas metades serem legíveis. Numa
instalação por MSI, uma delas está em `ProgramData` e a outra num perfil de
sistema. Quem for investigar uma comparação que falhou só encontra metade, e não
tem como saber que a outra existe.

**Como corrigir.** O pai já sabe onde quer o log; falta dizer ao filho:

```go
// worker.go, em sobe()
cmd.Env = append(os.Environ(), "BIO_WORKER=1", "BIO_WORKER_DLL="+c.dll,
	"BIO_LOG_DIR="+dirDeLogAtual())

// worker.go, em workerMain()
if dir := os.Getenv("BIO_LOG_DIR"); dir != "" {
	iniciaLogEm(dir, "worker.log")
} else {
	iniciaLogArquivo("worker.log")
}
```

### A2. O worker sobrevive à morte não-graciosa do pai — a proteção cobre só o desligamento limpo *(novo)*

**Arquivos:** `comparador.go:132-141`, `worker.go:191-239`, `supervisor.go:29-56`

O comentário do `comparador.go` descreve o problema com precisão e implementa a
solução para um caso só:

```go
// comparador.go:132-136
// Encerra o worker antes de sair. Sem isto ele sobrevive ao servico: fica
// orfao segurando o proprio executavel, e a atualizacao seguinte falha com
// "arquivo em uso" depois de o servico ja ter parado - ou seja, com o
// comparador fora do ar. Cada reinicio ainda deixaria mais um para tras.
```

Esse `encerraSDK` roda depois de `servidor.Serve` retornar — o caminho gracioso.
Ele **não** roda quando o processo é morto: SCM estourando o prazo de parada
(`servico.go:59` já prevê que isso acontece e apenas registra), `MajorUpgrade` do
MSI parando o serviço com `Stop="both"` (`wxs:93-99`), `Stop-Process -Force` do
instalador `.ps1` (`instalar-servidor.ps1:52`), ou uma falha do próprio serviço.
Em todos esses casos o worker fica órfão segurando `AgenteBiometria.exe` — que é
exatamente a falha descrita, reinstaurada pela porta dos fundos. O mesmo vale
para o agente e o seu supervisor.

**Como corrigir.** No Windows o mecanismo para isso é o *Job Object* com
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`: quando o handle do pai fecha — por saída
limpa **ou** por `TerminateProcess` — o Windows mata os filhos.

```go
// worker.go — criar o job uma vez e atribuir cada worker a ele.
job, err := windows.CreateJobObject(nil, nil)
info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
	BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
	},
}
_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
	uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
// apos cmd.Start():
_ = windows.AssignProcessToJobObject(job, windows.Handle(cmd.Process.Pid))
```

### A3. O limite de 16 MB contradiz os 5.000 candidatos anunciados, e a recusa aparece como "JSON inválido" *(novo)*

**Arquivos:** `main.go:41`, `main.go:46`, `main.go:389-403`, `main.go:498-501`,
`README.md`

```go
// main.go:389-395
func decodificaJSON(w http.ResponseWriter, r *http.Request, limite int64, destino any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limite)
```

```go
// main.go:498-501 — todo erro de decodificacao vira a mesma mensagem
if err := decodificaJSON(w, r, maxCorpoIdentificar, &body); err != nil {
	escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
	return
}
```

O README anuncia *"Identificação 1:N com até 5.000 candidatos por chamada"* e
`maxCandidatos = 5000` confirma. Mas `maxCorpoIdentificar = 16 << 20` é conferido
**antes**, e nada liga os dois números. Serializei corpos reais para medir onde a
conta vira:

| Template médio | Corpo de 5.000 candidatos | Resultado |
|---|---|---|
| 1.024 bytes | 5,06 MB | passa |
| 2.048 bytes | 9,94 MB | passa |
| 3.072 bytes | 14,82 MB | passa, com 7% de folga |
| 4.096 bytes | 19,71 MB | **400 `JSON invalido: http: request body too large`** |

Ou seja, os 5.000 anunciados valem **enquanto o template médio ficar abaixo de
~3,3 KB** — um teto que não está escrito em lugar nenhum, enquanto
`maxTemplate` permite 64 KB por candidato. O sistema web que crescer o template
(outro leitor, outra versão de SDK) descobre isso em produção.

E descobre mal: a mensagem diz **"JSON inválido"**. Quem receber isso vai
procurar dado corrompido no banco — a hipótese que o resto do sistema treinou o
operador a considerar primeiro, com toda a razão. O erro real é de tamanho, e não
tem nada a ver com o conteúdo.

**Como corrigir.** Separar o erro de tamanho dos demais e dizer o que fazer:

```go
// main.go, em decodificaJSON — MaxBytesError e um tipo proprio desde o Go 1.19.
var excedeu *http.MaxBytesError
if errors.As(err, &excedeu) {
	escreveErro(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
		"lista grande demais (limite de %d MB por chamada); envie em lotes menores",
		limite>>20))
	return
}
```

E documentar a relação no `const`, como `main.go:37-48` já faz para os outros
limites: *"5.000 candidatos só cabem em 16 MB enquanto o template médio ficar
abaixo de ~3,3 KB"*.

### A4. O argumento `perm` de `gravaArquivoAtomico` não faz nada no Windows — a proteção declarada em cada chamada não existe *(novo)*

**Arquivos:** `storage.go:29-73`, `anuncio.go:95`, `main.go:640`,
`origins.go:160`, `cert.go:102-107`

```go
// storage.go:45 — no Windows, File.Chmod so liga/desliga FILE_ATTRIBUTE_READONLY
if err := f.Chmod(perm); err != nil {
```

Todos os chamadores expressam uma intenção de segurança pelo argumento:
`0o600` para o arquivo de descoberta com o token vivo (`main.go:640`), para a
chave privada TLS (`cert.go:102`) e para a lista de origens autorizadas
(`origins.go:160`); `0o644` para o anúncio do comparador (`anuncio.go:95`). No
Windows **nenhum desses valores tem efeito**: qualquer modo com o bit de escrita
ligado resulta no mesmo arquivo, e quem decide o acesso é a ACL herdada do
diretório.

Para os arquivos em `%LOCALAPPDATA%` o resultado prático é aceitável — a ACL do
perfil já restringe ao usuário. Para o anúncio em `ProgramData`, **não é**: é
precisamente a premissa errada que o C2 do PR #13 documentou no comentário do
WiX, aparecendo uma segunda vez, agora no código Go. Um `0o644` ao lado de um
arquivo que carrega credencial de serviço sugere um controle deliberado onde não
há nenhum.

**Como corrigir.** Ou remover o parâmetro e documentar que a proteção vem da ACL
do diretório, ou aplicá-la de fato onde importa:

```go
// storage.go — deixar explicito no unico lugar que decide.
// perm e mantido por clareza de intencao; no Windows quem protege e a ACL
// herdada do diretorio (ver anuncio.go e a discussao de ProgramData).
```

...e, para o anúncio, o `util:PermissionEx` do C2 do #13 mais a checagem de dono
em `leAnuncio`. As duas correções são a mesma correção.

### A5. `instalar-servidor.ps1 -Desinstalar` numa máquina instalada por MSI apaga o binário e deixa o serviço registrado *(novo; consequência concreta do C4 do #12)*

**Arquivos:** `instalar-servidor.ps1:104-119`, `instalar-servidor.ps1:97-102`,
`wxs:53-55`, `wxs:71-99`

O `.ps1` foi escrito antes do serviço existir e nunca soube dele. O
`-Desinstalar` remove a chave `Run`, para e desregistra a **tarefa agendada**,
limpa as variáveis de máquina, mata os processos e apaga o diretório de
instalação:

```powershell
# instalar-servidor.ps1:114-116
if (Test-Path -LiteralPath $destino) {
    Remove-DiretorioInstalacao $destino
}
```

`$destino` é `%ProgramFiles(x86)%\AgenteBiometria` — **o mesmo diretório onde o
MSI instala** (`wxs:53-55`, `PASTAINSTALACAO`). Numa máquina instalada pelo MSI,
o resultado é:

1. `AgenteBiometria.exe` some do disco;
2. o serviço `AgenteBiometriaComparador` **continua registrado**, com `ImagePath`
   apontando para um arquivo que não existe, e com política de reinício
   automático (`wxs:85-90`) — o SCM tenta, falha, e repete a cada 60 segundos,
   para sempre;
3. `C:\ProgramData\AgenteBiometria\comparador.json` **fica para trás**: o `.ps1`
   nunca toca em `ProgramData`, e `removeAnuncio()` (`comparador.go:99`) não roda
   porque o processo foi morto, não desligado;
4. o Windows Installer continua achando que o produto está instalado, então o
   `msiexec /x` seguinte falha ou repara pela metade.

Qualquer agente que suba depois lê o anúncio órfão e delega para uma porta morta
— e o `--conferir-contra` vai dizer `comparador inacessivel` numa máquina onde,
para quem olhar o `services.msc`, o comparador *está* instalado.

**Como corrigir.** Enquanto os dois instaladores coexistirem, o `.ps1` precisa
reconhecer o outro e recusar:

```powershell
# No inicio, antes de qualquer alteracao.
$servico = Get-Service -Name 'AgenteBiometriaComparador' -ErrorAction SilentlyContinue
if ($servico) {
    throw 'Esta maquina foi instalada pelo MSI. Use "msiexec /x AgenteBiometria.msi"; este script removeria o binario e deixaria o servico quebrado.'
}
```

E, no `-Desinstalar`, apagar também `$env:ProgramData\AgenteBiometria\comparador.json`.
A saída definitiva continua sendo a do C4 do #12: um instalador só.

---

## 🟢 Sugestões (opcional)

- **S1.** `conferir-biometria.cmd:16-18` afirma que *"o template NUNCA e
  impresso"*, mas `forma()` (`autoteste.go:128-139`) imprime o primeiro e o
  último caractere: `primeiro %q, ultimo %q`. São dois bytes, sem valor prático
  para um atacante — mas a frase é uma garantia, e garantia que não é literal
  envelhece mal. Ou o texto acompanha o código, ou `forma()` passa a descrever a
  classe do caractere (`"imprimivel"`, `"digito"`) em vez do valor.
- **S2.** `tituloOrigem` (`main.go:660`) fatia **bytes**, não runas. Origem com
  caractere fora do ASCII — possível via `CORS_ORIGEM` ou `SISTEMA_URL`, que não
  passam por navegador nenhum (`origins.go:40-45`) — sai cortada no meio de uma
  runa e o menu mostra o losango de substituição. Ao corrigir o C1, usar
  `[]rune` resolve os dois de uma vez.
- **S3.** `main.go:244-246` — `BIO_TOKEN_QUERY=1` aceita o token na *query
  string*, onde ele entra no histórico do navegador e em qualquer `Referer`. A
  escotilha não tem prazo nem registra uma linha de log dizendo que está ligada.
  No mínimo, um `registraErro("BIO_TOKEN_QUERY ligado: o token trafega na URL")`
  no arranque.
- **S4.** `main.go:293-298` — a resposta `202` de autorização pendente devolve
  `porta` e `SESSIONNAME` para uma origem que ainda **não** foi autorizada, e o
  CORS permite que ela leia. É a metade de reconhecimento do C1: um site
  descobre que há agente, em que porta e em qual sessão RDP o usuário está. A
  pendência não precisa carregar nenhum dos dois campos — `{"ok":false,
  "autorizacao":"pendente"}` basta para o cliente.
- **S5.** `.gitignore` ignora `agente-*.json` e `origens-autorizadas.json`, mas
  **não** `comparador.json` — o único desses arquivos que carrega uma credencial
  compartilhada por toda a máquina. Quem copiar um anúncio para dentro da árvore
  ao diagnosticar um servidor commita o segredo do serviço num repositório
  público. Uma linha.
- **S6.** `origins.go:98-129` — `solicita()` só expira pendências quando é
  chamada. Num agente ocioso, uma pendência de dez minutos atrás continua no mapa
  até a próxima chamada de `/api/hello`. Não é explorável (o C2 acima é o
  problema real), mas o prazo declarado não é o prazo aplicado, e a mesma
  varredura do ticker proposto no C2 pode cuidar dos dois lados.

---

## 📋 Resumo

| | |
|---|---|
| **Arquivos alterados** | 1 neste PR (só documentação); **nenhum em `main`** desde `26c9379`; 41 revisados |
| **Segurança** | 🚨 Risco |
| **Qualidade** | ⚠️ Atenção |
| **Risco de produção** | 🚨 Alto |
| **Testes** | ❌ Sem cobertura na área revisada hoje |

**Contagem de hoje:** 2 críticos novos, 5 alertas novos, 6 sugestões novas. Os
críticos dos PRs #12 e #13 seguem todos abertos e foram reconferidos linha a
linha.

**Sobre os testes.** As 47 funções de teste cobrem bem o que é difícil de acertar
em C — `sdk.go`, `worker.go`, `delegacao.go`, `anuncio.go`, `versaodll.go`. A
área revisada hoje — `origins.go`, `middleware`, o menu da bandeja — **não tem
uma única linha de teste**, e é onde mora a decisão de segurança mais importante
do sistema:

| Achado | Teste que o teria pegado |
|---|---|
| **C1** | `TestTituloOrigemNaoEscondeODominio`: uma origem de 86 bytes, e assertar que o host aparece inteiro no título. Três linhas |
| **C2** | Um teste de `aprova()` com pendência vencida, assertando erro. Não depende de systray |
| **A3** | Um caso de `decodificaJSON` com corpo acima do limite, assertando a mensagem e o status |

---

## ✅ Pontos positivos

- **O sistema separa "não é a pessoa" de "a comparação quebrou" em todos os
  níveis** — código de saída `2` em `--conferir-contra` (`autoteste.go:411-423`),
  `ignorados` atravessando o processo worker (`worker.go:48-51`), a checagem de
  coerência entre `confere` e `id` na resposta do comparador
  (`delegacao.go:166-171`). É a decisão mais importante de um sistema biométrico
  e o Go nunca a erra. O único lugar que ainda confunde as duas coisas é o
  cliente JS (`integra-biometria.js:229`), e é justamente a peça sem teste.
- **O middleware acerta os detalhes que costumam passar batido**: comparação de
  token em tempo constante (`main.go:247`), `Vary` em `Origin` e no cabeçalho de
  *private network* (`main.go:201-202`), `X-Content-Type-Options` antes de
  qualquer decisão (`main.go:213`), `recover` por requisição que ainda responde
  500 em vez de derrubar o processo (`main.go:207-212`), rejeição de `Origin` que
  não sobrevive à normalização (`main.go:184-193`). O C1 de hoje não é um
  descuido de quem escreveu isso: é a única parte do fluxo que sai do código e
  vai para os olhos de uma pessoa.
- **`decodificaJSON` recusa campo desconhecido e segundo objeto no corpo**
  (`main.go:392-401`). O segundo é uma classe inteira de ataque de
  *request smuggling* de aplicação que quase ninguém fecha, e aqui está fechado
  em quatro linhas, com o `io.EOF` conferido do jeito certo.
- **A conta de cada limite está escrita ao lado do `const`, com a falha que a
  motivou** (`main.go:36-49`). É por isso que o A3 pôde ser encontrado: dava para
  comparar a conta documentada com a capacidade anunciada no README e ver que
  ninguém tinha multiplicado uma pela outra.
- **`build` e `vet` limpos** para `windows/386`, arquivos de teste incluídos, em
  5.915 linhas que manipulam memória nativa, `uintptr` e FFI.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

Os dois críticos de hoje têm uma origem comum, e ela é diferente da do PR #13. Lá
o problema era de **escala**: o comparador mudou de tamanho e os pressupostos não
foram recontados. Aqui o problema é de **fronteira**: todo o rigor do sistema
está aplicado ao que atravessa a rede e a DLL — e o único ponto em que a decisão
atravessa uma tela e chega a um ser humano é tratado como apresentação, não como
segurança. `tituloOrigem` é uma função de formatação de 6 linhas; ela é, na
prática, o controle de acesso do sistema inteiro.

**Ordem sugerida de correção**, considerando tudo que está aberto:

1. **C1 + C2 de hoje** — juntos, ~25 linhas em `main.go` e `origins.go`. São o
   único par alcançável **sem acesso local à máquina**, o mesmo critério que o
   PR #13 usou para reordenar a lista, e não dependem de nenhuma decisão de
   arquitetura pendente.
2. **C2 #13 (loopback em `delegacao.go` + ACL do `ProgramData`)** — o que impede
   template biométrico de sair da máquina. Exige acesso local, que num RDS com
   dezenas de sessões é uma barreira baixa.
3. **C1 #12 (`comparador.key`)** — sem ela, todo `net stop` para a biometria do
   servidor até cada usuário fazer logoff.
4. **A1 #13 (reler o anúncio quando `comparadorRemoto == nil`)** — cinco linhas,
   e é o que faz uma instalação por MSI num RDS em produção funcionar sem logoff
   geral.
5. **C1 #13 (fila do SDK no comparador)** — o mais trabalhoso; as três medidas
   paliativas cabem num commit.
6. **A1 e A5 de hoje** — não mudam comportamento de produção, mas são as duas
   coisas que decidem se o próximo incidente vai ser diagnosticável.

Este PR **não altera código**: acrescenta apenas este documento.
