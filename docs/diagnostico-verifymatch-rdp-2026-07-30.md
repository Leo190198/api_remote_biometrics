# Diagnóstico: `NBioAPI_VerifyMatch` falha no servidor RDP

**Data:** 2026-07-30
**Sintoma relatado:** `NBioAPI_VerifyMatch: Erro 0x000B do SDK NBioBSP` ao verificar
biometria, e às vezes o processo simplesmente morria.

---

## Conclusão

O defeito **não está no agente**. Está no gancho de API que o
**FabulaTech Biometrics for Remote Desktop (Server)** injeta nos processos do
servidor RDP.

Três módulos da FabulaTech são carregados dentro do nosso processo em
`10.10.11.30` e não existem na workstation:

| módulo | versão | onde |
|---|---|---|
| `ftapihook32.dll` | 3.4.1.2 | `C:\Windows\SysWOW64` |
| `ftfpstub.dll` | 2.2.0.0 | `C:\Windows\SysWOW64` e `System32` |
| `FTCOMS~1.DLL` | — | — |

`ftapihook32.dll` entra antes até do `USER32.dll`, o que caracteriza injeção no
arranque do processo. Com ele no caminho, a chamada a `NBioAPI_VerifyMatch`
corrompe memória: ora recusa o template com `0x000B`
(`NBioAPIERROR_INTERNAL_CHECKSUM_FAIL`, "data forged"), ora derruba o processo
com violação de acesso.

Que é corrupção, e não dado inválido, fica claro pelo código de exceção
alternando entre **leitura** (`0xc0000005 0x0`) e **escrita** (`0xc0000005 0x1`)
no mesmo ponto. Dado inválido falharia sempre igual.

---

## A evidência decisiva

Experimento controlado, com uma única variável:

| | workstation (cliente RDP) | `10.10.11.30` (servidor RDP) |
|---|---|---|
| `NBioBSP.dll` | SHA256 `78D3AC04…E41CB8E2` | **o mesmo arquivo**, SHA256 idêntico |
| template | 635 bytes, `sha256:33fd51ca024e` | os mesmos bytes |
| chamada | `NBioAPI_VerifyMatch`, 5 args, ponteiros iguais | idem |
| ganchos FabulaTech no processo | **nenhum** | `ftapihook32` + `ftfpstub` + `FTCOMS~1` |
| **resultado** | **confere** | `0xc0000005` ou `0x000B` |

O `PC` da falha (`0x66fa0116`, depois `0x66fa013b`) cai dentro da faixa da
própria `NBioBSP.dll` (`0x66f70000`–`0x67431000`), ou seja, a execução já estava
no código do SDK quando estourou — com argumentos que passaram pelo gancho.

---

## Hipóteses eliminadas, e a medição que matou cada uma

Todas foram descartadas por medida, não por opinião. Registradas aqui porque
várias parecem plausíveis e voltarão a ser levantadas.

| hipótese | como foi eliminada |
|---|---|
| Banco truncando o template (coluna curta) | O template `cf84a88bd7a8` foi gravado às 16:42:39, o agente reiniciou, e às 17:26:17 voltou do banco com **hash idêntico** |
| Navegador / transporte HTTP | Mesmo `sha256` na saída do `/captura` e na entrada do `/comparar` |
| Processo worker / serialização JSON | Mesmo `sha256` atravessando a fronteira do processo |
| `normalizaTemplate` corrompendo | A linha de erro imprime o valor **já normalizado**, e ele é idêntico ao capturado |
| Qualidade da leitura, dedo errado | Um template comparado **consigo mesmo** falha. Erro de checksum não é erro de correspondência |
| Versão da `NBioBSP.dll` | 4.8.8.6 e 5.2.0.6 falham igual no servidor. A 4.8.8.6 é byte a byte a mesma que funciona no cliente |
| Layout do `NBioAPI_INPUT_FIR` na nossa FFI | Mesmo código e mesma DLL passam na workstation |
| Descasamento de versão da FabulaTech (servidor 1.9.9.0 × cliente 2.0.0.0) | Alinhados em 2.2.0.0 nas duas pontas; continuou falhando |
| Motor `Venus.dll` desalinhado (4.4.1.0 × 4.3.0.35) | Alinhado em 4.3.0.35; continuou falhando. E o `Venus.dll` **nem é carregado** durante o `VerifyMatch` |
| `NGStar.dll` (UnionCommunity, HFDU06) presente só no servidor | Não é carregado durante o `VerifyMatch` |

Também verificado: a captura **funciona** através do redirecionamento. Um
template capturado no servidor confere sem erro na workstation. Só o
`VerifyMatch` está quebrado.

---

## Arquitetura do SDK, conforme observado

Descoberta que reorienta qualquer investigação futura: a comparação é
**autocontida na `NBioBSP.dll`**.

| camada | arquivo | papel |
|---|---|---|
| driver (kernel) | `VenusDrv.sys`, serviço `FPUSB` | fala USB com o sensor. Não compara nada |
| módulos de dispositivo | `Venus.dll`, `NGStar.dll`, `NFRD*`, `NFPC*` | carregados **por nome, em tempo de execução**, apenas para captura |
| fachada + comparação | `NBioBSP.dll` | expõe os `NBioAPI_*` e faz o casamento |

A listagem de módulos do processo confirma: durante um `VerifyMatch`, nenhum
`Venus.dll` ou `NGStar.dll` é carregado. Comparar esses arquivos em disco entre
máquinas é perda de tempo — foi o que custou duas rodadas de investigação.

---

## Ferramentas de diagnóstico criadas

Todas continuam no binário e servem para o próximo problema de campo.

| comando | o que responde | precisa de leitor? |
|---|---|---|
| `--autoteste` | Caminho completo: captura, comparação direta e pelo worker | sim, 3 leituras |
| `--salvar-template <arq>` | Grava um template capturado em arquivo | sim |
| `--conferir-template <arq>` | Compara um template com ele mesmo | **não** |

O par `--salvar-template` / `--conferir-template` é o que separa extrator de
comparador: leva-se um template de uma máquina sadia para a doente. Se ele passa
lá, o comparador está bom e o problema é a captura; se falha, é o comparador.
Sem isso, os dois sempre falham juntos e nada se distingue.

O que cada execução passou a registrar:

- **qual DLL foi aberta**, com caminho, versão e tamanho — no `agente.log`, no
  `worker.log` e no relatório do autoteste. Era a única pergunta que o log não
  respondia, e a ausência dela custou uma rodada inteira: duas máquinas com a
  mesma instalação aparente carregavam DLLs diferentes, porque `achaDLL` prefere
  `C:\Windows\SysWOW64`.
- **todos os módulos nativos carregados**, com endereço base e fim. É o que
  revelou os ganchos. A primeira versão filtrava por nomes NITGEN e não mostrava
  nada além da própria `NBioBSP` — parecia conclusivo e era ponto cego: módulo
  injetado por terceiros não casa com nenhum nome NITGEN.
- **relatório gravado linha a linha**, com marcação de passo antes de cada
  entrada no SDK. O relatório era montado em memória e só ia ao disco no fim;
  quando a DLL derrubava o processo, não sobrava uma linha da execução que mais
  importava. Hoje a última linha do arquivo nomeia a chamada que matou o
  processo.
- **impressão do template** (tamanho + `sha256` curto) em cada ponto do
  caminho. Nunca o template em si: é dado pessoal irrevogável.

---

## Recomendações

**1. Abrir chamado na FabulaTech.** É bug do `ftfpstub` 2.2.0.0 / `ftapihook32`
3.4.1.2, não do agente nem do NITGEN. O repro é limpo e está descrito acima. O
produto tem a política `Log Level` (0–10; 3 = Debug, 4 = Dump), documentada como
"normalmente habilitada a pedido do suporte técnico", que gera o rastro do lado
deles.

Não há política de exclusão de processo. As únicas oferecidas no `policies.zip`
são `Licensing`, `LoggingLevel`, `LoggingRotation` e `DisableCheckNewVersion` —
ou seja, não dá para tirar o agente do gancho por configuração.

**2. Rodar o agente na workstation**, onde o leitor é físico e não há gancho.
É o desenho original: serviço em localhost ao lado do hardware. `VerifyMatch`
não precisa de leitor — comprovado, o `--conferir-template` roda sem hardware
nenhum. Fica pendente resolver como o navegador dentro da sessão RDP alcança o
agente do lado cliente, que é questão de rede.

Atenção: enquanto a sessão RDP está aberta com redirecionamento biométrico, o
leitor fica tomado com exclusividade e o `NBioAPI_Init` local bloqueia. Para o
agente rodar na workstation, o redirecionamento precisa estar desligado.

**3. Contorno em código, ainda não investigado.** A captura atravessa o gancho
sem problema, então o `ftfpstub` não está quebrado inteiro — só na forma de
entrada que usamos (`NBioAPI_FIR_FORM_TEXTENCODE`, ponteiro para ponteiro para
texto). Se houver forma de FIR que ele marshale corretamente, o conserto seria
local. A lista de exports da 4.8.8.6 não mostrou um `TextDecodeFIR`, então não
há caminho óbvio de texto para handle.

---

## Como reproduzir

Na máquina com o problema, com o agente fechado:

```
AgenteBiometria.exe --conferir-template <template-valido.txt>
```

Um template válido é qualquer um capturado numa máquina onde
`--conferir-template` devolve código 0. A saída lista a DLL, os módulos
carregados com faixa de endereço e o veredito. Se aparecer `ftapihook32.dll` ou
`ftfpstub.dll` na lista, é este caso.

Códigos de saída: `0` passou, `1` o SDK recusou com erro tratado, `2` a DLL
derrubou o processo (o Go imprime o traceback e o `PC` da falha, que se compara
com as faixas dos módulos).

---

## Continuação — 2026-07-31: a solução

### O que foi eliminado nesta rodada

| hipótese | como foi eliminada |
|---|---|
| Outro ponto de entrada da API plana resolve | `NBioAPI_Verify` (captura e compara numa chamada só, passando pelo leitor) **também derruba o processo**, em `0x66835987`, dentro da faixa da `NBioBSP.dll`. Registrado em `verify.log` |
| O objeto COM (`NBioBSPCOM.dll`) tem matcher próprio | Não tem. É um invólucro ATL fino: importa `NBioBSP.dll` e chama `NBioAPI_CompareTwoFIR`, que já havia sido descartado por derrubar o processo |
| O wrapper .NET (`NITGEN.SDK.NBioBSP.dll`) usa outro caminho | Não usa. É `DllImport` na mesma `NBioBSP.dll` |

Ou seja: **o problema não é a função chamada**. Todo caminho de comparação da NITGEN termina no mesmo código corrompido. Procurar outra assinatura era beco sem saída.

### Como o gancho entra

Não é injeção global. Medido em `10.10.11.30`:

- `AppInit_DLLs` está **vazio** e `LoadAppInit_DLLs` é `0`.
- `AppCertDlls` **não existe**.
- Existe o driver **`ftsjail.sys` 3.4.1.2**, mesma versão do `ftapihook32.dll`.

O nome diz o desenho: *session jail*. O driver injeta o gancho nos processos **de uma sessão**, que é onde o dispositivo redirecionado precisa aparecer. Serviços não têm sessão de usuário nem leitor redirecionado, então não há por que enjaulá-los.

### A medição que abriu a saída

Rodando o mesmo binário nos dois ambientes do **mesmo servidor**:

| | sessão RDP | sessão 0 (SYSTEM) |
|---|---|---|
| `ftapihook32.dll` | carregado em `0x6cbc0000` | **ausente** |
| `ftfpstub.dll` | carregado em `0x6c1b0000` | **ausente** |
| `FTCOMS~1.DLL` | carregado em `0x70750000` | **ausente** |

A sessão 0 está limpa. Não é preciso tirar a comparação da máquina — basta tirá-la da **sessão**.

Isso também explica por que o ERP da P&F compara sem problema neste mesmo servidor: a comparação dele roda do lado servidor, fora da jaula.

### O desenho

A sessão RDP captura, porque só ela enxerga o leitor. A sessão 0 compara, porque só ela tem a DLL intacta.

- O modo comparador (`--comparador`) roda como tarefa agendada `SYSTEM`, em `ONSTART`.
- O agente da sessão recebe `COMPARADOR_URL` e `COMPARADOR_TOKEN`, e passa a delegar `/captura` e `/identificar` (`delegacao.go`).
- Sem essas variáveis nada muda: na estação de trabalho, onde não há gancho, o agente segue comparando sozinho.

Verificação de ponta a ponta, de dentro da sessão RDP:

```
AgenteBiometria.exe --teste-delegacao
```

Ele captura, exige que o gancho **esteja** presente neste processo — senão o teste não provaria nada — e manda o comparador conferir o template com ele mesmo, para o veredito não depender da qualidade da leitura.

## Ambiente

| | workstation | `10.10.11.30` |
|---|---|---|
| papel | cliente RDP | servidor RDP |
| leitor | NITGEN HamsterDX, `USB\VID_0A86&PID_0100` | virtual, via redirecionamento |
| driver | `VenusDrv.sys` 2.3.0.18 (2018), serviço `FPUSB` | `VenusDrv.sys` 2.3.0.17 (2012) |
| `NBioBSP.dll` usada | `Clinic Solution…` 4.8.8.6 | `SysWOW64` 5.2.0.6 (preferida por `achaDLL`) |
| FabulaTech | Workstation 2.2.0.0 | Server 2.2.0.0 |

O agente é 32 bits (`windows/386`) porque a `NBioBSP.dll` só existe em 32 bits.
