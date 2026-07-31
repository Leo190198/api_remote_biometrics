# Integrar o SEU sistema web com o agente de biometria

O agente (`AgenteBiometria.exe`) fica na bandeja e expõe a API local. O seu
sistema só precisa incluir `integra-biometria.js` e chamar as funções.

## 1) Incluir o script

```html
<script src="/caminho/integra-biometria.js"></script>
```

## 2) Usar (o objeto global `Biometria`)

```html
<button id="btnCadastrar">Cadastrar digital</button>
<button id="btnVerificar">Verificar</button>
<script>
  // Cadastro: o agente lê o dedo 2x e devolve o template.
  document.getElementById('btnCadastrar').onclick = async () => {
    try {
      const template = await Biometria.enroll();
      // >>> GUARDE no SEU banco, junto ao beneficiário <<<
      await fetch('/meu-backend/beneficiarios/123/biometria', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ template }),
      });
      alert('Digital cadastrada!');
    } catch (e) { alert(e.message); }
  };

  // Verificação 1:1: lê o dedo e compara com o template guardado.
  document.getElementById('btnVerificar').onclick = async () => {
    try {
      const guardado = await (await fetch('/meu-backend/beneficiarios/123/biometria')).json();
      const lido = await Biometria.capturar();
      const ok = await Biometria.comparar(guardado.template, lido);
      alert(ok ? 'CONFERE' : 'NÃO confere');
    } catch (e) { alert(e.message); }
  };
</script>
```

### Identificação 1:N (uma leitura contra N cadastros)

Use `Biometria.identificar` — **uma única chamada** ao agente, que compara
tudo internamente (não faça loop de `comparar`, que é lento e estressa o SDK):

```js
const lido = await Biometria.capturar();
// candidatos: [{ id, template }] vindos do SEU banco
const r = await Biometria.identificar(lido, candidatos);
if (r.confere) alert('É o beneficiário ' + r.id);
else alert('Digital não encontrada');
```

Contra um agente antigo, sem o endpoint `/identificar`, a chamada falha com
`HTTP 404` — **o script não cai sozinho no loop de `comparar`**. Se você
precisa suportar agentes antigos, trate o `404` e faça o loop no seu código,
ou atualize o agente.

## 3) Como o script sabe a PORTA e o TOKEN da sessão

Cada sessão RDP tem seu agente numa porta/token próprios. Escolha uma:

1. **Auto-descoberta (para uma app web qualquer):** chame uma vez no início.
   O script varre `5000–5099` e pergunta `/api/hello`; **cada agente só
   responde à sua própria sessão** (os de outras sessões devolvem 403), então
   você acha o agente certo sem tray e sem backend:
   ```js
   if (!(await Biometria.garantirConexao())) {
     alert('Agente de biometria não encontrado nesta sessão.');
   }
   // a partir daqui, Biometria.enroll()/capturar()/comparar() já funcionam
   ```

2. **Via tray:** o menu **"Abrir sistema"** da bandeja abre seu sistema com
   `#bioPort=..&bioToken=..` — o script lê e guarda sozinho. Defina no ambiente
   do agente `SISTEMA_URL=http://SEU-SISTEMA`.

3. **Via backend:** o agente grava `%LOCALAPPDATA%\BiometriaAgente\agente-<sessão>.json`
   (`{porta, token, ...}`). Se o SEU backend roda na mesma sessão, leia o arquivo
   e injete: `Biometria.configurar({ porta: 5001, token: 'XXXX' });`

## HTTPS e HTTP — funciona com os dois

O agente aceita **HTTP e HTTPS na mesma porta**. O script escolhe sozinho:
página `https://` fala `https://localhost:PORTA`; página `http://` fala
`http://localhost:PORTA` (com fallback para o outro protocolo).

Para o HTTPS funcionar, o **instalador do servidor** gera um certificado
autoassinado de `localhost` e o registra na loja de raízes confiáveis da
máquina (veja `agente-go/instalador/instalar-servidor.ps1`). Sem o
certificado, o agente segue só em HTTP — o que Chrome/Edge/Firefox ainda
aceitam a partir de páginas https (exceção de conteúdo misto para
`localhost`); só o Safari bloqueia.

## Instalação no servidor (todos os usuários)

Rode como administrador, com o exe ao lado do script:

```powershell
powershell -ExecutionPolicy Bypass -File instalar-servidor.ps1
```

**Num servidor RDP com o redirecionamento da FabulaTech, acrescente
`-InstalarComparador`:**

```powershell
powershell -ExecutionPolicy Bypass -File instalar-servidor.ps1 -InstalarComparador
```

Nesse ambiente a comparação não pode rodar dentro da sessão: o driver
`ftsjail.sys` injeta um gancho em todo processo dela e a `NBioBSP.dll` passa a
corromper memória. A sessão 0 não é alcançada, então o comparador vira um
serviço no próprio servidor e a sessão só captura.

**Isso não muda nada no seu código.** `Biometria.comparar()` e
`Biometria.identificar()` continuam idênticos — quem decide onde comparar é o
agente, não o navegador. Detalhes em
[docs/diagnostico-verifymatch-rdp-2026-07-30.md](../docs/diagnostico-verifymatch-rdp-2026-07-30.md).

Isso copia o agente para Program Files, registra o certificado e o
auto-início em HKLM: **cada usuário que logar (cada sessão RDP) ganha
automaticamente o seu agente** na bandeja, com porta e token próprios.
O agente roda com um supervisor: se crashar, é reiniciado sozinho em segundos.

## Atenção (gotchas)

- **Outro host:** se o sistema chama o agente a partir de outro nome de host,
  o certificado só cobre `localhost`/`127.0.0.1` — mantenha as chamadas em
  `localhost`.
- **CORS:** o agente responde `Access-Control-Allow-Origin` com a **sua origem
  exata**, nunca `*`, e só depois que ela for autorizada — pela bandeja
  (**Autorizar acesso**) ou previamente por `CORS_ORIGEM`. Origem não
  autorizada leva `403`. Não conte com liberação geral.
- **`comparar` falhando com "comparador inacessivel":** só aparece onde a
  comparação foi delegada (servidor RDP). Significa que o serviço da sessão 0
  caiu, não que a digital não confere nem que o leitor tem problema. Verifique
  a tarefa `AgenteBiometriaComparador`. O script **não** tenta reconectar nesse
  caso, e está certo: reconectar ao agente não levanta um serviço caído.
- **Onde a comparação acontece:** `GET /api/status` traz o campo `comparador`,
  com `local` ou a URL do serviço. É a primeira coisa a olhar quando uma
  máquina confere e outra não.
