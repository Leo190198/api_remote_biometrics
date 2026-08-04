# 🔍 Revisão técnica do sistema — 2026-08-04

**Commit revisado:** `d1c3846` (`main`, sem alteração desde 2026-08-01)
**Escopo:** todo o sistema — 14 arquivos Go, o cliente JS, o instalador
PowerShell, o script de build em Python e a documentação.
**Alvo verificado:** `GOOS=windows GOARCH=386 go vet ./...` limpo e
`go build` bem-sucedido nesta árvore.

---

## ⚠️ Antes de tudo: sétima revisão, mesmo commit, nenhuma correção aplicada

O código não muda desde `d1c3846` (2026-08-01). Este é o sétimo documento de
revisão sobre a mesma árvore, e os seis anteriores continuam abertos como PRs
que ninguém mesclou:

| PR | Data | Documento | Estado |
|----|------|-----------|--------|
| #1 | 07-29 | `revisao-sistema.md` | aberto |
| #3 | 07-31 | `revisao-sistema-2026-07-31.md` | aberto |
| #4 | 08-01 | `revisao-sistema-2026-08-01.md` | aberto |
| #5 | 08-02 | `revisao-sistema-2026-08-02.md` | aberto |
| #6 | 08-03 | `revisao-sistema-2026-08-03.md` | aberto |
| — | 07-30 | `revisao-sistema-2026-07-30.md` | mesclado (via PR #2) |

Só o documento de 07-30 chegou à `main`, e mesmo ele veio junto do código que
descrevia: **nenhum achado de nenhuma revisão foi corrigido até hoje.** Vinte e
poucos problemas apontados, zero linhas de correção. O gargalo do projeto hoje
não é encontrar defeito — é decidir qual deles vira trabalho.

Duas consequências práticas para quem for ler este documento:

1. **Todos os críticos anteriores continuam válidos por construção**, já que o
   código é byte a byte o mesmo. Eles estão consolidados na seção C2, com
   arquivo e linha, para servirem de backlog único.
2. **Esta revisão só gasta espaço com o que ainda não foi dito.** Cada achado
   novo abaixo foi conferido contra o código atual e contra os seis documentos
   anteriores para não repetir ninguém.

---

## 🔴 Problemas Críticos (bloqueia merge)

### C1. O agente acredita em qualquer processo que atenda em `COMPARADOR_URL` — o veredito biométrico é forjável por um usuário local sem privilégio algum

**Arquivos:** `delegacao.go:83-128`, `comparador.go:75-81`,
`instalador/instalar-servidor.ps1:186-192`

O segredo compartilhado autentica **o agente perante o comparador**, e só isso:

```go
// delegacao.go:92-93
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Authorization", "Bearer "+c.token)
...
// delegacao.go:115 — 200 OK basta; nada prova quem respondeu
if err := json.NewDecoder(limitado).Decode(destino); err != nil {
```

```go
// main.go:436-439 — o que voltar dali vira o veredito, sem mais nenhuma checagem
if comparadorRemoto != nil {
    ok, err = comparadorRemoto.compara(ctx, body.BiometriaBenef, body.BiometriaLida)
}
```

**Por que é um problema.** O comparador nunca prova que é o comparador. Quem
estiver escutando em `127.0.0.1:5150` responde `true` e a comparação confere —
e **não precisa conhecer o token para isso**, porque quem valida credencial é o
lado que recebe, não o que responde. Um usuário qualquer do servidor RDP faz um
`net.Listen("tcp", "127.0.0.1:5150")` e passa a decidir a biometria de **todas
as sessões**: 5150 é porta alta, sem privilégio, e o serviço não usa reserva
exclusiva. A janela para tomar a porta existe e é ordinária:

- no boot, antes de a tarefa `AtStartup` subir;
- depois que o comparador cai — `RestartCount 3` esgota e a tarefa desiste,
  deixando a porta livre, e `rodaComparador` sai com código 1 quando o `Listen`
  falha (`comparador.go:76-81`), sem tentar de novo;
- em qualquer troca de `COMPARADOR_PORTA` que deixe agentes apontando para o
  valor antigo.

De brinde, o impostor **coleta o token da máquina** a cada requisição, que é o
mesmo para todas as sessões (`instalar-servidor.ps1:178`).

O impacto é o pior possível para este produto: qualquer pessoa se autentica como
qualquer beneficiário, em todas as sessões do servidor, sem tocar no leitor. E
não aparece em log nenhum — do ponto de vista do agente, a comparação
simplesmente conferiu.

Vale registrar o que **está** certo aqui, porque limita o ataque a este caminho:
quando o comparador falha, `handleComparar` devolve 502 e **não** cai para a
comparação local (`main.go:449-452`). O sistema falha fechado. O buraco é só a
confiança cega em quem responde.

**Como corrigir.** Autenticar a resposta, não só o pedido. A correção mais
barata é o comparador provar posse do segredo sobre um nonce por requisição:

```go
// delegacao.go — no pedido
nonce := make([]byte, 16)
if _, err := rand.Read(nonce); err != nil { return err }
req.Header.Set("X-Bio-Nonce", base64.RawURLEncoding.EncodeToString(nonce))

// ... na resposta, antes de decodificar o veredito
corpo, err := io.ReadAll(limitado)
if err != nil { return err }
mac := hmac.New(sha256.New, []byte(c.token))
mac.Write(nonce)
mac.Write(corpo)
esperado := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
if subtle.ConstantTimeCompare([]byte(resp.Header.Get("X-Bio-Prova")), []byte(esperado)) != 1 {
    return errors.New("resposta do comparador nao autenticada: possivel impostor na porta")
}
```

Alternativa que reaproveita o que o projeto já tem: `session.go` já sabe cruzar
a tabela TCP com a sessão do dono (`pidDaConexao`, `sessaoDoPID`). O mesmo
mecanismo responde "quem está escutando em 5150 roda em sessão 0?" antes da
primeira delegação, e o agente recusa delegar se a resposta for não. As duas
medidas somadas — prova por HMAC em cada resposta e checagem do dono do
*listener* na configuração — fecham o caso sem introduzir TLS e gestão de chave
numa porta de loopback.

---

### C2. Os críticos das seis revisões anteriores seguem abertos, no mesmo código

Não é achado novo, é o backlog consolidado. Como o commit não mudou, cada item
abaixo foi reconferido linha a linha nesta árvore e continua exatamente como
descrito na revisão de origem:

| # | Achado | Arquivo | Origem |
|---|--------|---------|--------|
| 1 | O comparador roda como `SYSTEM`/`RunLevel Highest` e entrega bytes de qualquer usuário logado a uma DLL que o próprio projeto documenta como capaz de ler fora da alocação — caminho de escalonamento local | `instalar-servidor.ps1:186-192`, `sdk.go:396-421` | 08-01 C1 |
| 2 | `bioPort` do fragmento da URL não é validado: token e biometria podem ir para um host arbitrário | `integra-biometria.js:25-34` | 07-30 C1 |
| 3 | O cliente JS descarta `ignorados`: um cadastro corrompido vira "digital não encontrada" — exatamente o erro que `main.go:559-564` se esforça para evitar | `integra-biometria.js:222-230`, `COMO-USAR.md:46-56` | 07-30 C2 |
| 4 | Identificação 1:N é um laço de comparações 1:1 no limiar de 1:1 — a falsa aceitação acumula com o tamanho da lista | `sdk.go:480-533` | 07-30 C3 |
| 5 | Uma identificação 1:N segura a fila única do SDK por minutos — na estação congela as capturas da sessão; no comparador, as de todo o servidor | `main.go:537-552`, `worker.go:339-346` | 07-30 C4, 07-31 C1 |
| 6 | `/status` do comparador responde `ok: true` sem nunca ter tocado no SDK | `comparador.go:120-133` | 07-31 C2 |
| 7 | `PORTA` e `MODO_COMPARADOR` são variáveis de máquina num produto multi-sessão, e o supervisor transforma o erro em laço infinito | `main.go:571-590`, `supervisor.go:29-55` | 08-03 C1 |
| 8 | `-ComparadorPorta` sozinho gera instalação que nunca compara | `instalar-servidor.ps1:167-193` | 08-01 C3 |

---

## 🟡 Alertas (recomenda correção)

### A1. O vocabulário de erro do worker atravessa a delegação e manda o suporte para a máquina errada

**Arquivos:** `worker.go:269-321`, `comparador.go:66-69`, `delegacao.go:105-113`

`clienteWorker` foi escrito para a estação de trabalho, onde todo erro do SDK é
de fato um problema de leitor. As mensagens dizem isso literalmente:

```go
// worker.go:282, 314, 319
return respostaWorker{}, fmt.Errorf("o leitor biometrico falhou durante %s; tente novamente", pedido.Op)
return respostaWorker{}, fmt.Errorf("o leitor biometrico nao respondeu em %s", limite)
// worker.go:199
return errors.New("o leitor biometrico falhou varias vezes seguidas; aguarde alguns segundos")
```

O comparador usa o **mesmo** `clienteWorker` (`criaSDK` continua sendo
`novoClienteWorker`, `main.go:64`), num processo que por desenho **não tem
leitor nenhum** — `comparadorStatus` até anuncia `"leitor": false`. O texto
sobe intacto pela delegação e chega ao sistema web como:

> `comparador recusou (502): o leitor biometrico falhou durante comparar`

**Por que é um problema.** O suporte lê "leitor" e vai conferir cabo, driver e
redirecionamento RDP na estação do usuário — enquanto o defeito está no serviço
em sessão 0, em outra máquina lógica, com log em outro diretório. Este projeto
gastou dois documentos de diagnóstico justamente para separar essas duas
metades; a mensagem de erro reúne as duas de novo.

**Como corrigir.** Dar ao cliente do worker um rótulo do que ele está operando:

```go
type clienteWorker struct {
    mu    sync.Mutex
    dll   string
    papel string // "leitor biometrico" na estacao, "comparador" na sessao 0
    ...
}

// e nas mensagens
return respostaWorker{}, fmt.Errorf("o %s falhou durante %s; tente novamente", c.papel, pedido.Op)
```

`rodaComparador` passa `"comparador"`, o agente mantém `"leitor biometrico"`.

---

### A2. O esfriamento do worker é global: um template ruim de um usuário para a verificação de todas as sessões

**Arquivos:** `worker.go:162-199`, `comparador.go:66-69`

```go
// worker.go:198-200
if c.falhasSeguidas >= falhasParaEsfriar && time.Since(c.ultimaFalha) < esperaAposFalhas {
    return errors.New("o leitor biometrico falhou varias vezes seguidas; aguarde alguns segundos")
}
```

Na estação isso é acerto: evita trocar um crash por tempestade de processos. No
comparador, `clienteWorker` é **um só para o servidor inteiro** e não existe
nenhuma noção de chamador. Três quedas seguidas — que é exatamente o que um
template forjado provoca, já que `normalizaTemplate` valida alfabeto e tamanho,
nunca a estrutura interna (`sdk.go:396-421`) — e **toda** verificação biométrica
do servidor passa a receber erro por 5 segundos. Repetir o envio mantém o estado
indefinidamente: o caminho custa três requisições HTTP autenticadas por ciclo, e
o token está no ambiente da máquina, ao alcance de qualquer usuário logado.

A revisão de 07-31 (C1) já apontou que o comparador serializa a instituição numa
fila só. Este é o outro lado da mesma moeda: além de lento sob carga legítima,
ele é **desligável** por carga ilegítima barata.

**Como corrigir.** Duas medidas independentes, ambas pequenas:

1. Não deixar o esfriamento ser global no modo servidor: manter um pool de N
   workers (um por núcleo, por exemplo) e esfriar só o que caiu.
2. Contabilizar quedas por origem do pedido antes de contabilizá-las no processo
   — um template que derruba o worker é defeito **daquele** par, e a resposta
   correta é recusar aquele par (como já se faz com `0x000B` em
   `sdk.go:513-521`), não parar o serviço.

---

### A3. O instalador manda rodar dois scripts que não existem no repositório

**Arquivo:** `instalador/instalar-servidor.ps1:127,136`

```powershell
throw 'AgenteBiometria.exe nao encontrado. Execute Compilar-Go.ps1 primeiro.'
...
throw "Assinatura Authenticode invalida: $($assinatura.Status). Assine com Assinar.ps1 ou use -PermitirNaoAssinado somente em laboratorio."
```

Nem `Compilar-Go.ps1` nem `Assinar.ps1` existem — `instalador/` tem um arquivo
só, o próprio `instalar-servidor.ps1`. O `README.md:87-99` documenta o build
como um `go build` manual seguido de `embutir-icone.py`, que é outro
procedimento com outro nome.

**Por que é um problema.** As duas mensagens aparecem justamente quando alguém
está instalando pela primeira vez, ou tentando entender por que a instalação
parou. Mandar procurar um arquivo inexistente transforma um erro de dois
minutos em meia hora de busca — e é o tipo de defeito que só some se alguém
reparar, porque nada quebra em teste.

**Como corrigir.** Ou criar os dois scripts, ou apontar para o que existe:

```powershell
throw 'AgenteBiometria.exe nao encontrado. Compile conforme a secao "Compilacao" do README.md.'
```

---

### A4. A regra de `.gitignore` que protege template biométrico é por nome, e `--salvar-template` aceita qualquer nome

**Arquivos:** `.gitignore:11-18`, `autoteste.go:373-402`

```
# Templates biometricos NUNCA entram no repositorio: sao dado pessoal
# irrevogavel, e este repositorio e publico.
template*.txt
```

A intenção está certa e o comentário é explícito sobre o risco. Mas o comando
que produz esses arquivos aceita **um caminho arbitrário**:

```go
// autoteste.go:373
func salvaTemplate(caminho string) int {
    ...
    if err := os.WriteFile(caminho, []byte(template), 0o600); err != nil {
```

`AgenteBiometria.exe --salvar-template digital-joao.txt` — o nome que qualquer
pessoa escreveria ao comparar duas máquinas, que é o cenário para o qual o
comando foi criado — passa direto pela regra. Num repositório público, um
`git add .` distraído publica dado biométrico irrevogável de uma pessoa real.

**Como corrigir.** Fazer o próprio comando garantir a regra, em vez de confiar
na disciplina de quem digita:

```go
const sufixoTemplate = ".template.txt" // casado com a regra do .gitignore

if !strings.HasSuffix(caminho, sufixoTemplate) {
    caminho += sufixoTemplate
    fmt.Println("gravando como", caminho, "- o .gitignore so protege este sufixo")
}
```

e trocar a regra para `*.template.txt`. Complemento barato: gravar em
`%LOCALAPPDATA%\BiometriaAgente` quando o caminho for relativo, que já é onde
mora o resto dos dados do agente e nunca é uma árvore de trabalho do Git.

---

### A5. O único passo de build fora do Go nunca foi revisado — e ele reescreve o PE depois do compilador

**Arquivo:** `embutir-icone.py`

Sete revisões e este arquivo nunca apareceu em nenhuma. Ele roda entre o
`go build` e a assinatura Authenticode, e tem dois defeitos concretos:

**1. Fatiamento do `.ico` sem validação de limites (`embutir-icone.py:42-45`):**

```python
for i in range(qtd):
    ent = dados[6 + 16 * i : 6 + 16 * i + 16]
    tam, off = struct.unpack_from("<II", ent, 8)
    imagens.append((ent[:12], dados[off : off + tam]))
```

Fatia de `bytes` em Python nunca levanta erro: um `.ico` truncado ou com
`offset` inconsistente produz `img` menor que `tam` — ou vazio — e o script
grava assim mesmo com `len(img)`, imprime "icone embutido" e sai com código 0.
O resultado é um executável com recurso de ícone corrompido, descoberto só
quando alguém olha a bandeja.

```python
img = dados[off : off + tam]
if len(img) != tam:
    raise SystemExit(f"{ico}: entrada {i} declara {tam} bytes e o arquivo so tem {len(img)}")
```

**2. `GetLastError` chamado por `ctypes` (`embutir-icone.py:31-32`):**

```python
def falha(msg):
    raise SystemExit(f"erro: {msg} (GetLastError={k32.GetLastError()})")
```

A documentação do `ctypes` desaconselha exatamente isso: a própria máquina do
`ctypes` pode chamar outras funções da API entre a falha e a leitura, e o código
devolvido deixa de ser o do erro real. O jeito correto é `ctypes.WinError()`
sobre um `WinDLL(..., use_last_error=True)`:

```python
k32 = ctypes.WinDLL("kernel32", use_last_error=True)

def falha(msg):
    raise SystemExit(f"erro: {msg} ({ctypes.WinError(ctypes.get_last_error())})")
```

Num script cujo propósito inteiro é reportar por que a gravação de recurso
falhou, o diagnóstico enganoso custa mais que o defeito.

---

## 🟢 Sugestões (opcional)

1. **`tituloOrigem` corta bytes no meio de um caractere** (`main.go:647-653`):
   `origem[:max-3]` fatia a string em bytes. Uma origem IDN com acento acima de
   72 bytes vira UTF-8 inválido no menu da bandeja. `[]rune(origem)[:max-3]`
   resolve.
2. **Origem pendente descartada em silêncio** (`origins.go:124-127`): quando o
   canal `novas` está cheio, o `default` engole o pedido; a origem fica em
   `pendentes` por 10 minutos sem nunca aparecer na bandeja, e o JS desiste
   depois de 60 segundos sem que ninguém saiba por quê. Um `registraErro` no
   `default` já daria o rastro.
3. **`ignorados` mistura duas causas distintas** (`main.go:525-531` e
   `sdk.go:494-521`): "template ilegível antes de chegar ao SDK" e "o SDK
   recusou por checksum" saem na mesma lista. São dois defeitos de banco
   diferentes, com correções diferentes; devolver `{"id":..., "motivo":...}`
   custa pouco e poupa uma investigação.
4. **`opIdentificar` não registra a impressão do template lido**
   (`worker.go:155-157`), ao contrário de `opComparar` (`worker.go:150-152`).
   É justamente o caminho com mais dados atravessando a fronteira do processo, e
   o único sem o par de impressões que permite localizar onde os bytes mudaram.
5. **`ne.Temporary()` está depreciado** (`cert.go:190`) desde o Go 1.18 e o
   projeto compila com Go 1.26. Trocar por um teste explícito de
   `net.ErrClosed`/`os.ErrDeadlineExceeded` evita depender de um método que a
   biblioteca padrão já não mantém.

---

## 📋 Resumo

- **Arquivos alterados**: 1 (`docs/revisao-sistema-2026-08-04.md`) — revisão de
  20 arquivos de código, sem alteração de comportamento
- **Segurança**: 🚨 Risco — o veredito biométrico é forjável por qualquer
  usuário local do servidor (C1), somado ao escalonamento para `SYSTEM` que
  segue aberto desde 08-01
- **Qualidade**: ⚠️ Atenção — o código é cuidadoso e bem justificado; o que
  falha é o ciclo de correção, com 7 revisões e nenhum reparo
- **Risco de produção**: 🚨 Alto — a delegação está no caminho de todas as
  verificações do servidor RDP e não tem prova de identidade do outro lado
- **Testes**: ⚠️ Parcial — 33 testes de boa qualidade, mas `go test ./...` não
  seleciona pacote algum fora de `windows/386` e não há CI: na prática a suíte
  só roda se alguém lembrar, numa máquina Windows x86

---

## ✅ Pontos positivos

- **A delegação falha fechada.** Quando o comparador não responde,
  `handleComparar` devolve 502 e não cai silenciosamente para a comparação local
  (`main.go:449-452`) — que, dentro da sessão RDP, produziria um resultado
  corrompido. É a decisão certa, e é o motivo de C1 ser um problema de
  autenticação e não de disponibilidade.
- **O isolamento do SDK em processo separado é a decisão estrutural correta**, e
  o comentário em `worker.go:18-23` explica exatamente por quê: violação de
  acesso dentro da DLL não vira `panic` recuperável. Custa uma requisição em vez
  do agente inteiro.
- **`normalizaTemplate` é rigoroso pelo motivo certo** (`sdk.go:396-421`): não
  valida "por higiene", valida porque a DLL lê fora da alocação quando os campos
  de tamanho não batem. A justificativa está escrita ao lado da regra.
- **`impressaoTemplate` resolve diagnóstico sem expor dado biométrico**
  (`sdk.go:427-434`): tamanho e 6 bytes de SHA-256 bastam para seguir o template
  pelo caminho todo e flagrar truncamento de coluna no banco.
- **O autoteste separa as variáveis uma a uma** (`autoteste.go`): fase direta
  contra fase worker, bytes crus contra normalizados, e grava linha a linha
  porque a próxima linha pode ser a que mata o processo. É instrumentação
  desenhada por quem já perdeu um diagnóstico.
- **O instalador não confia no caminho que recebe**: `Test-CaminhoInstalado`
  antes de qualquer `Remove-Item -Recurse` (`instalar-servidor.ps1:57-62,97-102`)
  e verificação de PE x86 antes de copiar. Cuidado raro em script de instalação.
- **A troca de segurança do token de máquina está escrita no próprio
  instalador** (`instalar-servidor.ps1:173-177`), com as palavras exatas do
  risco. Documentar a decisão ruim que se escolheu tomar vale mais que escondê-la.
- **A comparação do token é em tempo constante nos dois lados**
  (`main.go:247`, `comparador.go:108-118`), com a justificativa escrita.

---

## Veredicto: **MUDANÇAS NECESSÁRIAS**

C1 sozinho já sustenta o veredicto: enquanto o agente aceitar como verdade o que
quer que responda em `COMPARADOR_URL`, a verificação biométrica do servidor RDP
tem um portão que qualquer usuário local abre sem credencial nenhuma. É também o
achado mais barato de corrigir de toda a lista — um HMAC sobre um nonce, umas
quinze linhas em `delegacao.go` e outras tantas no comparador.

O recado maior, porém, não está em nenhum achado: sete revisões sobre o mesmo
commit, seis PRs abertos e zero correções mescladas. Sugiro que a próxima
iteração **não** produza um oitavo documento, e sim escolha três itens desta
lista — C1, o `SYSTEM` do comparador (08-01 C1) e o `ignorados` do cliente JS
(07-30 C2) — e os transforme em código. Uma revisão que ninguém aplica custa
mais caro que uma revisão que não aconteceu, porque dá a impressão de que o
problema foi tratado.
