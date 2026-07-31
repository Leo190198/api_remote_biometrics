# 🔍 Revisão técnica do sistema — 2026-07-31

Revisão do sistema no estado de `main` (`1175e27`), com foco no que entrou depois
da revisão de 2026-07-30 (PR #1): o **modo comparador** (`comparador.go`), a
listagem de módulos carregados (`versaodll.go`), os comandos de diagnóstico novos
(`--salvar-template`, `--conferir-template`, `--teste-forma`) e as mudanças de
`sdk.go`/`main.go` que vieram junto.

**Escopo analisado:** `main.go`, `comparador.go`, `sdk.go`, `worker.go`,
`versaodll.go`, `autoteste.go`, `log.go`, `cert.go`, `origins.go`, `session.go`,
`storage.go`, `supervisor.go`, os cinco arquivos de teste,
`integracao/integra-biometria.js`, `integracao/COMO-USAR.md`,
`instalador/instalar-servidor.ps1`, `README.md`, `.gitignore` e
`docs/diagnostico-verifymatch-rdp-2026-07-30.md`.

**Verificações executadas:** `GOOS=windows GOARCH=386 go build ./...` (OK) e
`GOOS=windows GOARCH=386 go vet ./...` (limpo). Os testes não rodam aqui — as
*build tags* exigem um alvo `windows/386` real.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O comparador serializa a instituição inteira numa fila de um único SDK — e, ao contrário do modo normal, sem nenhum limite de concorrência

**Arquivos:** `comparador.go:64,66-69`, `main.go:63,107-138,425,522`,
`worker.go:339-343`

```go
// comparador.go
go sdkThreadMain()          // uma goroutine, um worker, uma DLL

mux := http.NewServeMux()
mux.HandleFunc("/status", comparadorStatus)
mux.HandleFunc("/comparar", handleComparar)
mux.HandleFunc("/identificar", handleIdentificar)
...
Handler: exigeSegredo(segredo, mux),   // <- sem o middleware do modo normal
```

**Por que é um problema.** No modo normal essa fila atende **um usuário** numa
estação. O comparador atende **todas as estações da rede**, com a mesma fila de
16 (`sdkTasks`, `main.go:63`), a mesma goroutine única (`sdkThreadMain`,
`main.go:100-105`) e o mesmo processo worker único.

A conta fecha contra o serviço:

- uma `/identificar` com 5.000 candidatos segura a thread do SDK por até **3
  minutos** (`worker.go:340-343`);
- durante esse tempo, toda `/comparar` do prédio inteiro espera na fila com
  contexto de **45 segundos** (`main.go:425`) e volta como erro. Da perspectiva
  do backend, a biometria simplesmente parou;
- `limiteIdentificar` (2 vagas, `main.go:48`) não resolve: duas identificações
  simultâneas serializam, e a terceira recebe **503** depois de 2 segundos
  (`main.go:461-468`) — comportamento que o backend precisa tratar e que não está
  documentado em lugar nenhum.

E há um agravante que é novo, não herdado: `exigeSegredo` substitui o
`middleware`, e com ele **some o `limiteHTTP`** (`main.go:252-260`), que era quem
devolvia `503 agente ocupado` em vez de deixar requisição se acumular. No
comparador, cada requisição vira uma goroutine bloqueada no envio para `sdkTasks`
(`main.go:127-131`) até o contexto expirar. Some também o `X-Content-Type-Options`
e o `recover` por requisição.

**Como corrigir.** Três medidas, todas pequenas:

1. **Pool de workers.** O worker já é um processo isolado com instância própria do
   SDK — é a saída natural para o problema de a DLL não ser thread-safe. Um pool
   de N processos remove a serialização sem tocar em `sdk.go`:

   ```go
   // N = número de comparações simultâneas aceitáveis no servidor
   sdkPool := make(chan sdkAPI, N)
   ```

2. **Separar o caminho 1:N do 1:1**, ou fatiar a identificação em lotes
   (ex.: 250 candidatos por tarefa) e reenfileirar entre lotes, para que
   comparações interativas passem entre os lotes.

3. **Repor o portão de concorrência** no comparador — mesmo que seja só envolver
   o `mux` com o mesmo `limiteHTTP`, para que excesso de carga vire `503`
   imediato em vez de fila invisível.

---

### C2. `/status` do comparador responde `ok: true` mesmo com o SDK inutilizável

**Arquivo:** `comparador.go:120-133`

```go
func comparadorStatus(w http.ResponseWriter, r *http.Request) {
	...
	escreveJSON(w, http.StatusOK, map[string]any{
		"ok":      true,          // <- constante
		"modo":    "comparador",
		"dll":     descreveDLL(caminhoDLL()),
		...
	})
}
```

**Por que é um problema.** `ok` é literal: não consulta `ensureSDK()`, não olha o
worker, não tenta uma comparação. O serviço responde "estou bem" quando a
`NBioBSP.dll` não foi encontrada, quando o worker está em *cooldown* por falhas
seguidas (`worker.go:198-200`) e quando toda comparação está voltando `0x000B`.

Isso é crítico pelo papel que o endpoint ocupa: o comparador é um serviço de
servidor, sem bandeja, sem ícone e sem usuário na frente. `/status` é a **única**
forma de monitoramento que ele oferece, e é justamente a que o balanceador ou o
Zabbix da casa vai consultar. Um *health check* que nunca falha faz uma queda de
biometria parecer, do lado de fora, um problema do backend.

O modo normal acerta isso — `handleStatus` (`main.go:312-339`) chama o SDK de
verdade e devolve `503` quando ele não responde.

**Como corrigir.** Exercitar o caminho, com prazo curto para não herdar a fila:

```go
ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
defer cancel()
_, err := naThreadSDK(ctx, func() (struct{}, error) {
	_, err := ensureSDK()      // sobe o worker e carrega a DLL
	return struct{}{}, err
})
info := map[string]any{"modo": "comparador", "dll": descreveDLL(caminhoDLL()), ...}
if err != nil {
	info["ok"], info["erro"] = false, err.Error()
	escreveJSON(w, http.StatusServiceUnavailable, info)
	return
}
info["ok"] = true
escreveJSON(w, http.StatusOK, info)
```

Melhor ainda: guardar um par de templates conhecido (gerado por
`--salvar-template` na instalação) e, a cada N minutos, comparar um com o outro.
É o mesmo teste do `--conferir-template`, e é o único que detecta o gancho da
FabulaTech aparecendo depois da subida.

---

### C3. Achados críticos da revisão anterior seguem abertos

Reconferidos linha a linha no código atual; nenhum foi tratado desde o PR #1.
Detalhamento completo em [`docs/revisao-sistema-2026-07-30.md`](revisao-sistema-2026-07-30.md).

| # | Achado | Onde está hoje | Situação |
|---|---|---|---|
| C1/30-07 | `bioPort` do fragmento da URL não é validado: `'//localhost:' + '5000@evil.com'` faz o `fetch` sair para `evil.com` levando o token e os templates | `integracao/integra-biometria.js:27-28,145-156,167-174` | ❌ Aberto |
| C2/30-07 | O cliente JS descarta `ignorados`, e um cadastro corrompido chega ao atendente como "digital não encontrada" | `integracao/integra-biometria.js:229` — `return { confere: !!r.confere, id: r.id \|\| '' }` | ❌ Aberto |
| C3/30-07 | 1:N é um laço de `VerifyMatch` 1:1 com nível de segurança `0`, retornando no primeiro acerto: o FAR acumula em `1-(1-FAR)^N` | `sdk.go:643,661-663`, `main.go:46` (`maxCandidatos = 5000`) | ❌ Aberto |

O C3/30-07 pesa mais agora do que pesava ontem: com o comparador, a identificação
1:N deixa de ser uma operação de uma estação e passa a ser um serviço central,
chamado pelo backend para qualquer beneficiário. O erro que ele pode cometer —
identificar a pessoa errada — passou a ser institucional.

---

## 🟡 Alertas (recomenda correção)

### A1. O comparador não verifica a presença do gancho que motivou sua existência

**Arquivos:** `comparador.go:58-62`, `docs/diagnostico-verifymatch-rdp-2026-07-30.md`

O modo existe por um motivo só: tirar o `VerifyMatch` de uma máquina onde o
`ftapihook32.dll`/`ftfpstub.dll` da FabulaTech está injetado. Ele registra a
lista de módulos na subida — e não faz nada com ela:

```go
for _, m := range modulosBiometricos() {
	registraInfo("comparador: modulo %s", m)
}
```

Instalado por engano numa máquina com o gancho (o próprio servidor RDP, por
exemplo, que é onde muita gente vai supor que "o backend" mora), o serviço sobe
normalmente, anuncia `ok: true` (ver C2) e reproduz o `0x000B` — exatamente o
defeito que ele deveria evitar, agora para todas as estações de uma vez.

**Correção:** o diagnóstico já sabe quais são os nomes; basta agir sobre eles.

```go
for _, m := range modulosBiometricos() {
	registraInfo("comparador: modulo %s", m)
	if nome := strings.ToLower(m); strings.Contains(nome, "ftapihook") || strings.Contains(nome, "ftfpstub") {
		fmt.Fprintln(os.Stderr, "ATENCAO: gancho da FabulaTech neste processo — o VerifyMatch vai falhar aqui.")
		registraErro("comparador: gancho da FabulaTech presente (%s); veja docs/diagnostico-verifymatch-rdp-2026-07-30.md", m)
	}
}
```

Recusar subir seria defensável; avisar em `stderr` e no log é o mínimo.

### A2. A lista de módulos é coletada no processo que não carrega a DLL

**Arquivos:** `versaodll.go:51-70`, `comparador.go:60`, `worker.go:64-107`, `README.md:259-260`

`modulosBiometricos()` lista os módulos **do processo que a chama**, e desde o
isolamento quem carrega a `NBioBSP.dll` é o worker, não o pai. No comparador a
chamada acontece no processo pai, antes de qualquer `ensureSDK()`: a lista nunca
contém a `NBioBSP.dll`, nem o motor que ela abriu por nome em tempo de execução —
que é literalmente a pergunta que o comentário do arquivo diz querer responder
("um modulo injetado por terceiros não casa com nenhum desses nomes e ficava
invisível justamente na pergunta que importava").

Ela funciona nos comandos de diagnóstico (`--autoteste`, `--conferir-template`,
`--teste-forma`), porque esses carregam a DLL no próprio processo. No modo agente
normal, `modulosBiometricos()` **não é chamada em lugar nenhum** — apesar de o
README afirmar o contrário:

> Toda execução registra qual `NBioBSP.dll` foi aberta, com versão e tamanho, e a
> lista dos módulos nativos carregados no processo. (`README.md:259-260`)

**Correção:** registrar a lista dentro do worker, logo depois de `novoSDK`
suceder — é lá que a DLL e o motor estão:

```go
// worker.go, em atendePedido, apos *sdk = inst
for _, m := range modulosBiometricos() {
	registraInfo("worker: modulo %s", m)
}
```

Assim o `worker.log` de produção passa a responder "qual motor este processo
abriu", e a chamada no pai continua útil para o que ela de fato prova: se o
gancho está injetado na máquina.

### A3. O `comparador.log` cresce sem limite

**Arquivos:** `log.go:16-30`, `comparador.go:34`, `main.go:423-424`, `worker.go:151-152`

A rotação é avaliada **uma vez, na abertura do arquivo**:

```go
if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}
```

No agente da estação isso é tolerável: o processo sobe e desce com a sessão. O
comparador é um serviço que fica meses de pé e registra **duas linhas por
comparação** (`main.go:423` no pai, `worker.go:151` no worker). Numa casa com
alguns milhares de atendimentos por dia, o arquivo passa de 5 MB em dias e nunca
mais é rotacionado enquanto o serviço não reiniciar.

**Correção:** checar o tamanho no momento da escrita — um `io.Writer` que envolve
o arquivo e rotaciona quando passa do limite —, ou, no mínimo, um tick diário que
chame a rotação. E ver A4: metade dessas linhas não deveria existir.

### A4. Impressão de cada template comparado, agora centralizada no servidor

**Arquivos:** `main.go:419-424`, `worker.go:149-152`, `sdk.go:431-434`

`impressaoTemplate` não expõe o template — mas `sha256[:6]` é um identificador
estável do registro biométrico de uma pessoa, gravado em claro, para **toda**
comparação de produção, sem chave para desligar. Era o achado A3 da revisão
anterior; o comparador o agrava, porque o que antes ficava espalhado em logs de
estação vira um arquivo único no servidor correlacionando o dia inteiro de
atendimentos da casa.

**Correção:** condicionar a `BIO_DIAGNOSTICO=1` e, quando ligado, usar
`hmac-sha256` com chave aleatória por execução em vez de `sha256` puro — continua
servindo para seguir o mesmo template dentro de uma execução (o caso de uso do
comentário) sem produzir identificador estável entre máquinas e arquivos.

### A5. O modo comparador não está documentado em lugar nenhum

**Arquivos:** `README.md` (seções "Configuração", "API local", "Diagnóstico",
"Estrutura do projeto"), `integracao/COMO-USAR.md`, `instalador/instalar-servidor.ps1`

Nada no repositório fora do próprio `comparador.go` menciona `MODO_COMPARADOR`,
`COMPARADOR_TOKEN`, `COMPARADOR_PORTA`, a porta `5150`, as rotas `/comparar` e
`/identificar` ou a flag `--comparador`. A mensagem do commit afirma que a
exigência de TLS para outro host "está escrito no código e na documentação" — só
está no código. O instalador também não conhece o modo: não há script, serviço
Windows, nem instrução de como manter o processo de pé.

Quem for instalar isso em produção depende de ler o fonte. Faltam, no mínimo:

- as três variáveis na tabela de **Configuração** (junto de `BIO_WORKER`,
  `BIO_WORKER_DLL` e `BIO_TOKEN_QUERY`, que também nunca foram documentadas);
- uma seção **Modo comparador** com o `curl` de exemplo, o formato dos corpos, e
  os códigos que o backend precisa tratar — em especial o `503` de
  `limiteIdentificar` (`main.go:461-468`) e o `502` de erro do SDK;
- `--comparador` e `--teste-forma` na tabela de **Diagnóstico**;
- `comparador.go` na árvore de **Estrutura do projeto**.

### A6. O comparador não tem supervisor nem encerramento ordenado

**Arquivos:** `comparador.go:33-101` versus `main.go:847-853,919-928`

O modo normal roda sob `supervisor()`, que reinicia o filho com espera
exponencial, e no fim chama `servidor.Shutdown` + `encerraSDK`. O comparador não
tem nem um nem outro: se o processo cair, nada o levanta; ao ser encerrado, o
worker morre sem `opEncerrar` e sem `NBioAPI_Terminate`.

Para um serviço do qual toda a rede depende, é a diferença entre um soluço de 2
segundos e uma manhã sem biometria.

**Correção:** rodar sob supervisão — o `supervisor()` que já existe, ou o Gerenciador
de Serviços do Windows (`sc create` / NSSM), que é o caminho natural para um serviço
de servidor e traz reinício automático de fábrica. E tratar `SIGTERM`/`CTRL_CLOSE`
para chamar `servidor.Shutdown` e `encerraSDK` antes de sair.

### A7. Templates gravados em texto claro no disco, sem aviso e sem regra de ignore

**Arquivos:** `autoteste.go:395,522`, `.gitignore`,
`docs/diagnostico-verifymatch-rdp-2026-07-30.md` ("Como reproduzir")

```go
if err := os.WriteFile(caminho, []byte(template), 0o600); err != nil {
```

`--salvar-template` e `--teste-forma` gravam o FIR **em claro**, num caminho
arbitrário escolhido por quem chama, sem aviso e sem prazo de validade. O
diagnóstico instrui o operador a criar exatamente esse arquivo
(`--conferir-template <template-valido.txt>`), e o `.gitignore` cobre
`AgenteBiometria-*.exe` e `testar-*.cmd` — mas não cobre o template. É plausível
que um `template-valido.txt` acabe commitado.

Dado biométrico é irrevogável, e o próprio README avisa (`> [!CAUTION]`) para
nunca colocá-lo em logs ou URLs.

**Correção:** imprimir um aviso na gravação ("apague este arquivo depois do
diagnóstico — é dado biométrico irrevogável"), acrescentar `/template*.txt` e
`/*.fir` ao `.gitignore`, e dizer no README que o arquivo deve ser destruído
depois do teste.

### A8. Sem CI — e o modo comparador só tem teste do porteiro

**Arquivos:** ausência de `.github/workflows/`, `comparador_test.go`

Os 29 testes existentes (bom número, e bem escritos) continuam presos a
`//go:build windows && 386`, sem *runner* que os execute. Sobre o comparador,
`comparador_test.go` cobre bem `exigeSegredo` — inclusive o caso do prefixo com
sobra, que é o que costuma escapar —, mas nada cobre o resto: método errado,
corpo inválido, limite de tamanho, o `503` de `limiteIdentificar`, ou o
comportamento de `/status`.

**Correção:** um workflow `runs-on: windows-latest` com `GOARCH=386`,
`go vet ./... && go test ./...`; e mover para arquivos sem *build tag* de
arquitetura os testes que não tocam a DLL (`exigeSegredo`, `middleware`,
`origins`, `storage`), para rodarem em qualquer runner.

---

## 🟢 Sugestões (opcional)

1. **`porta` local sombreia a global** (`comparador.go:44`). No modo comparador a
   variável global `porta` (`main.go:55`) fica em `0` para sempre; qualquer log ou
   handler futuro que a use vai mentir. Renomear para `portaEscuta` remove a
   armadilha.

2. **Registrar tentativa de credencial inválida** (`comparador.go:110-115`). Hoje
   um `401` não deixa rastro: uma varredura local no serviço de biometria passa
   despercebida. Um `registraErro` com o `RemoteAddr`, limitado a uma linha por
   segundo, resolve sem virar vetor de inundação de log.

3. **`/status` poderia devolver os módulos carregados** (`comparador.go:120-133`).
   Já estão em memória via `modulosBiometricos()`; expô-los atrás do segredo
   pouparia uma sessão RDP no servidor toda vez que o suporte precisar saber se o
   gancho apareceu.

4. **`ehModuloDoSistema` fixa `C:\WINDOWS`** (`versaodll.go:108-111`). Usar
   `os.Getenv("SystemRoot")` cobre instalações fora do disco padrão, que existem
   em servidor.

5. **`templateValido` só é usado nos testes** (`sdk.go:423-425`, `sdk_test.go:58,70`).
   Ou o código de produção passa a usá-la, ou ela sai e o teste chama
   `normalizaTemplate` direto — hoje é uma função pública do pacote que só o teste
   sustenta.

6. As sugestões 1–8 da revisão anterior (LockOSThread vestigial, wrappers tipados
   de `x/sys/windows`, `opEco` no worker, `listenerMista.Close`, remoção do
   arquivo de descoberta na saída, listener `::1`, mapa de erros `0x03xx`/`0x04xx`)
   continuam válidas e não foram tratadas.

---

## 📋 Resumo

- **Arquivos alterados** desde a revisão anterior: 12 (`comparador.go`,
  `comparador_test.go`, `versaodll.go`, `versaodll_test.go`, `autoteste.go`,
  `sdk.go`, `main.go`, `log.go`, `README.md`, `.gitignore` e dois documentos em
  `docs/`) — 5 commits, ~1.200 linhas
- **Arquivos analisados**: 22
- **Segurança**: 🚨 Risco — o vazamento de token e template por `bioPort`
  (C3/30-07) segue aberto no cliente JS; o modo comparador em si tem um porteiro
  correto e bem testado
- **Qualidade**: ⚠️ Atenção — `build` e `vet` limpos, comentários de arquitetura
  acima da média; documentação não acompanha (modo comparador ausente, e o README
  afirma um registro de módulos que não acontece)
- **Risco de produção**: 🚨 Alto — C1 (fila única para toda a instituição) e C2
  (*health check* que nunca falha) atingem um serviço central, e o 1:N de
  C3/30-07 passou a ser institucional
- **Testes**: ⚠️ Parcial — 29 testes bem escritos, porém nenhum executa em CI, e
  do comparador só o segredo está coberto

---

## ✅ Pontos positivos

- **A decisão de mover só a comparação para fora do servidor RDP é a leitura
  certa do diagnóstico.** O documento de 30-07 provou que o defeito é do gancho da
  FabulaTech e que não há conserto do nosso lado; a resposta não foi insistir em
  contornos, foi mudar *onde* a operação roda, aproveitando que `VerifyMatch` não
  precisa de leitor. É a única saída que não depende de um fornecedor terceiro
  consertar um bug.

- **O porteiro do comparador foi feito com cuidado e está testado no ponto certo.**
  Recusar subir sem `COMPARADOR_TOKEN` de 32+ caracteres é falhar fechado;
  comparar o `Bearer` inteiro em tempo constante é a defesa correta para um
  segredo fixo e longevo; e `TestComparadorRecusaCredencialComSobra` cobre
  justamente a variante que uma implementação por prefixo deixaria passar. O
  comentário que explica por que o middleware do modo normal *não* foi
  reaproveitado (modelo de chamador diferente) evita a pior versão dessa mudança,
  que seria esticar o middleware existente até ele não significar mais nada.

- **A ausência de TLS está justificada, não esquecida** (`comparador.go:71-74`).
  A regra que fecha o comentário — "se algum dia precisar atender outro host, TLS
  deixa de ser opcional" — é o tipo de nota que impede a próxima pessoa de expor o
  serviço na rede sem perceber o que mudou.

- **O documento de diagnóstico é exemplar.** A tabela de hipóteses eliminadas com
  *a medição que matou cada uma* — banco truncando, transporte, worker, versão da
  DLL, `Venus.dll`, `NGStar.dll` — é o que impede que uma investigação já
  encerrada seja refeita do zero daqui a seis meses. A observação de que o motor
  sequer é carregado durante o `VerifyMatch` reorienta qualquer investigação
  futura, e está registrada com a evidência que a sustenta.

- **A separação extrator × comparador (`--salvar-template` / `--conferir-template`)
  é um bom instrumento de campo.** Duas metades que sempre falhavam juntas passaram
  a ser mensuráveis separadamente, e `--conferir-template` roda sem hardware —
  detalhe que permite testar o comparador numa máquina qualquer.

- **`--teste-forma` roda um caso por processo, de propósito** (`autoteste.go:471-475`).
  O comentário explica que a primeira versão rodava tudo em sequência e o primeiro
  caso derrubava os demais. Aceitar uma encostada de dedo a mais para garantir que
  todo caso seja medido é a escolha certa em ferramenta de diagnóstico.

- **`ligaConsole` não rouba saída já redirecionada** (`autoteste.go:41-51`). Um
  detalhe minúsculo, aprendido do jeito difícil ("foi assim que dois diagnósticos
  se perderam numa janela de console que fechou"), e resolvido testando o handle
  padrão em vez de assumir.

- **O README ganhou a seção de Diagnóstico e a árvore atualizada** — os itens A14
  da revisão anterior foram parcialmente endereçados, e a linha de "Solução de
  problemas" que aponta `ftapihook32.dll`/`ftfpstub.dll` para o documento de
  diagnóstico é exatamente o atalho que o suporte precisa.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

O modo comparador é a resposta certa para o problema certo, e a parte que
normalmente se erra — a autenticação — está bem feita e testada. O que falta é o
que separa um contorno de um serviço de produção: C1 (uma fila única para toda a
instituição, agora sem nem o limite de concorrência que o modo normal tinha) e C2
(*health check* que não checa nada) precisam ser tratados **antes** de o
comparador entrar em produção, porque os dois só se manifestam sob carga real e
os dois são invisíveis de fora.

C1 e C2 da revisão anterior continuam sendo duas correções pequenas e localizadas
no cliente JS. Valem um commit imediato, independentemente de tudo o mais.
