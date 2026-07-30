//go:build windows && 386

package main

// Autoteste do caminho de biometria, para rodar na maquina que tem leitor e
// NBioBSP.dll:
//
//	AgenteBiometria.exe --autoteste
//
// Existe porque VerifyMatch devolvia 0x000B (checksum) com dois templates
// recem-capturados comparados em memoria, sem passar por banco nenhum.
//
// A fase 1 fala com a DLL no proprio processo e compara um template com ele
// mesmo, byte a byte como saiu do SDK: responde se a chamada a DLL esta montada
// certa e se a normalizacao preserva o template. A fase 2 repete a comparacao
// pelo caminho de producao, atravessando o processo worker e o JSON, usando os
// mesmos templates da fase 1 — se a fase 1 passa e a fase 2 falha, os bytes
// mudam ao cruzar a fronteira do processo, e nao dentro do SDK.
//
// O relatorio vai para autoteste.log junto dos outros logs, sem nenhum template
// dentro: so tamanho, caracteres das pontas e um sha256 curto.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// O executavel e compilado com -H windowsgui e por isso nasce sem console: sem
// religar a saida ao terminal de quem chamou, as instrucoes de encostar o dedo
// nao apareceriam em lugar nenhum. Falhar aqui nao e problema - o relatorio em
// disco continua sendo gravado.
func ligaConsole() {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	const consoleDoPai = ^uintptr(0) // ATTACH_PARENT_PROCESS
	if r, _, _ := proc.Call(consoleDoPai); r == 0 {
		return
	}
	nome, err := windows.UTF16PtrFromString("CONOUT$")
	if err != nil {
		return
	}
	h, err := windows.CreateFile(nome, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return
	}
	os.Stdout = os.NewFile(uintptr(h), "CONOUT$")
	os.Stderr = os.Stdout
}

type autoteste struct {
	arquivo *os.File
	falhou  bool
}

// abreRelatorio deixa o arquivo pronto antes da primeira chamada ao SDK.
//
// O relatorio era montado na memoria e gravado so no fim. Quando a DLL derrubou
// o processo no meio da fase 1, nao sobrou uma linha sequer para dizer onde
// parou - justamente na execucao que mais precisava ser lida. Agora cada linha
// vai para o disco na hora, e a ultima que aparecer no arquivo e o passo que
// matou o processo.
func (a *autoteste) abreRelatorio() string {
	destino := "autoteste.log"
	if dir, err := garanteDiretorioDados(); err == nil {
		destino = filepath.Join(dir, "autoteste.log")
	}
	f, err := os.OpenFile(destino, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "abrir o relatorio:", err)
		return ""
	}
	a.arquivo = f
	return destino
}

func (a *autoteste) diz(formato string, args ...any) {
	linha := fmt.Sprintf(formato, args...)
	fmt.Println(linha)
	if a.arquivo == nil {
		return
	}
	// Sync a cada linha porque o proximo passo pode ser o que mata o processo,
	// e linha presa em buffer nao ajuda ninguem.
	_, _ = a.arquivo.WriteString(linha + "\r\n")
	_ = a.arquivo.Sync()
}

func (a *autoteste) erro(formato string, args ...any) {
	a.falhou = true
	a.diz("FALHOU: "+formato, args...)
}

// confirma registra o resultado de uma comparacao que deveria conferir.
func (a *autoteste) confirma(oque string, confere bool, err error) {
	switch {
	case err != nil:
		a.erro("%s: %v", oque, err)
	case !confere:
		a.erro("%s: nao conferiu, e o SDK nao acusou erro", oque)
	default:
		a.diz("OK: %s", oque)
	}
}

// forma descreve o template sem expor o dado biometrico. O que interessa aqui
// e o contorno: tamanho, se sobrou espaco nas bordas e quais sao os caracteres
// extremos - e por ali que truncamento e sujeira aparecem.
func forma(nome, t string) string {
	if t == "" {
		return fmt.Sprintf("%s: vazio", nome)
	}
	limpo := strings.TrimSpace(t)
	borda := "nao"
	if len(limpo) != len(t) {
		borda = fmt.Sprintf("SIM (%d -> %d bytes)", len(t), len(limpo))
	}
	return fmt.Sprintf("%s: %d bytes, primeiro %q, ultimo %q, espaco nas bordas: %s, %s",
		nome, len(t), t[0], t[len(t)-1], borda, impressaoTemplate(t))
}

func rodaAutoteste() (codigo int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ligaConsole()

	a := &autoteste{}
	if destino := a.abreRelatorio(); destino != "" {
		fmt.Println("relatorio em", destino)
	}
	// Um panic do Go e recuperavel e precisa entrar no relatorio: e a diferenca
	// entre "a DLL derrubou o processo" e "nosso codigo errou". Violacao de
	// acesso dentro da DLL nao passa por aqui - nesse caso quem aponta o culpado
	// e a ultima linha gravada.
	defer func() {
		if p := recover(); p != nil {
			a.erro("panic do Go: %v", p)
			a.diz("%s", debug.Stack())
			codigo = a.encerra()
		}
	}()

	a.diz("autoteste do agente de biometria - versao %s commit %s", versao, commit)
	a.diz("horario: %s", time.Now().Format("2006-01-02 15:04:05"))

	dll := achaDLL()
	if dll == "" {
		a.erro("NBioBSP.dll nao encontrada. Defina NBIOBSP_DLL e tente de novo.")
		return a.encerra()
	}
	a.diz("DLL: %s", descreveDLL(dll))

	if r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); r == 0 || r == 1 {
		defer procCoUninitialize.Call()
	}

	cadastro, verificacao, ok := a.faseDireta(dll)
	if !ok {
		return a.encerra()
	}
	a.faseWorker(dll, cadastro, verificacao)
	return a.encerra()
}

// faseDireta fala com a DLL no proprio processo, sem worker e sem JSON.
func (a *autoteste) faseDireta(dll string) (cadastro, verificacao string, ok bool) {
	a.diz("")
	a.diz("=== fase 1: DLL no proprio processo ===")

	a.diz("passo: carregar a DLL e chamar NBioAPI_Init")
	sdk, err := novoSDK(dll)
	if err != nil {
		a.erro("abrir o SDK: %v", err)
		return "", "", false
	}
	// Fecha antes da fase 2 para as duas instancias do SDK nao disputarem o
	// leitor.
	defer func() { _ = sdk.encerra() }()

	a.diz("passo: NBioAPI_EnumerateDevice")
	if ids, err := sdk.listaDispositivos(); err == nil && len(ids) > 0 {
		a.diz("IDs dos leitores: %v", ids)
	}
	n, err := sdk.contaDispositivos()
	if err != nil {
		a.erro("contar dispositivos: %v", err)
		return "", "", false
	}
	a.diz("leitores encontrados: %d", n)
	if n == 0 {
		a.erro("nenhum leitor conectado; o resto do teste precisa de hardware")
		return "", "", false
	}

	a.diz("")
	a.diz(">>> Encoste o dedo no leitor (captura 1 de 3)")
	a.diz("passo: NBioAPI_Capture purpose=%d (cadastro)", purposeEnroll)
	cadastro, err = sdk.capturaTexto(purposeEnroll, 30000)
	if err != nil {
		a.erro("captura 1: %v", err)
		return "", "", false
	}
	a.diz("%s", forma("captura 1 (cadastro)", cadastro))

	// O teste decisivo da fase 1. Um template comparado consigo mesmo, byte a
	// byte como saiu da DLL, tem que conferir. Se falhar por checksum aqui,
	// nenhuma normalizacao nem transporte esteve envolvido: o defeito esta em
	// como montamos a chamada.
	a.diz("")
	a.diz("passo: NBioAPI_VerifyMatch com a captura 1 contra ela mesma, bytes crus")
	confere, err := sdk.comparaBrutos(cadastro, cadastro)
	a.confirma("captura 1 confere com ela mesma (bytes crus)", confere, err)

	// Mesmo par pelo caminho normalizado. Se este falhar e o de cima passar, a
	// normalizacao e a culpada.
	if limpo := normalizaTemplate(cadastro); limpo == "" {
		a.erro("normalizaTemplate rejeitou um template recem-capturado pelo proprio SDK")
	} else {
		if limpo != cadastro {
			a.diz("ATENCAO: a normalizacao alterou o template (%d -> %d bytes)", len(cadastro), len(limpo))
		}
		confere, err = sdk.comparaTextos(cadastro, cadastro)
		a.confirma("captura 1 confere com ela mesma (normalizado)", confere, err)
	}

	a.diz("")
	a.diz(">>> Encoste o MESMO dedo no leitor (captura 2 de 3)")
	a.diz("passo: NBioAPI_Capture purpose=%d (verificacao)", purposeVerify)
	verificacao, err = sdk.capturaTexto(purposeVerify, 30000)
	if err != nil {
		a.erro("captura 2: %v", err)
		return "", "", false
	}
	a.diz("%s", forma("captura 2 (verificacao)", verificacao))

	// A captura 1 usou purpose de cadastro e a 2 de verificacao: e exatamente o
	// par que /comparar usa em producao. Se so uma das duas falhar consigo
	// mesma, o purpose e a variavel que importa.
	a.diz("")
	confere, err = sdk.comparaBrutos(verificacao, verificacao)
	a.confirma("captura 2 confere com ela mesma (bytes crus)", confere, err)

	confere, err = sdk.comparaTextos(cadastro, verificacao)
	switch {
	case err != nil:
		a.erro("comparar captura 1 com captura 2: %v", err)
	case confere:
		a.diz("OK: cadastro x verificacao conferem no processo direto")
	default:
		a.diz("cadastro x verificacao nao conferem (sem erro do SDK)")
		a.diz("  => se foi mesmo o mesmo dedo, e qualidade de leitura, nao defeito de codigo.")
	}
	return cadastro, verificacao, true
}

// faseWorker repete a comparacao pelo caminho de producao: os templates
// atravessam o processo worker como JSON antes de chegar na DLL. Reaproveita os
// templates da fase 1 de proposito — bytes identicos, unica variavel nova e a
// fronteira do processo.
func (a *autoteste) faseWorker(dll, cadastro, verificacao string) {
	a.diz("")
	a.diz("=== fase 2: caminho de producao (processo worker + JSON) ===")

	// A conclusao da fase 2 so vale se a fase 1 tiver passado: sem isso o
	// relatorio acusava a travessia do processo mesmo quando o SDK ja havia
	// falhado sozinho, e mandava quem le para o lado errado.
	falhouAntes := a.falhou

	a.diz("passo: subir o processo worker")
	cliente, err := novoClienteWorker(dll)
	if err != nil {
		a.erro("subir o worker: %v", err)
		return
	}
	defer func() { _ = cliente.encerra() }()

	confere, err := cliente.comparaTextos(cadastro, cadastro)
	a.confirma("captura 1 confere com ela mesma atravessando o worker", confere, err)

	confere, err = cliente.comparaTextos(cadastro, verificacao)
	switch {
	case err != nil:
		a.erro("cadastro x verificacao pelo worker: %v", err)
		if !falhouAntes {
			a.diz("  => a fase 1 comparou estes mesmos bytes sem erro, entao o")
			a.diz("     problema esta na travessia do processo, nao no SDK.")
		}
	case confere:
		a.diz("OK: cadastro x verificacao conferem pelo worker")
	default:
		a.diz("cadastro x verificacao nao conferem pelo worker (sem erro do SDK)")
	}

	// Ate aqui os templates nasceram no processo direto. Falta o caminho de
	// volta: um template capturado dentro do worker e devolvido por JSON e o que
	// o navegador realmente recebe.
	a.diz("")
	a.diz(">>> Encoste o MESMO dedo no leitor (captura 3 de 3)")
	peloWorker, err := cliente.capturaTexto(purposeVerify, 30000)
	if err != nil {
		a.erro("captura 3 (via worker): %v", err)
		return
	}
	a.diz("%s", forma("captura 3 (via worker)", peloWorker))

	confere, err = cliente.comparaTextos(peloWorker, peloWorker)
	a.confirma("captura 3 confere com ela mesma (ida e volta pelo worker)", confere, err)

	confere, err = cliente.comparaTextos(cadastro, peloWorker)
	switch {
	case err != nil:
		a.erro("cadastro x captura 3 pelo worker: %v", err)
	case confere:
		a.diz("OK: cadastro x captura 3 conferem - o caminho de producao esta sadio")
	default:
		a.diz("cadastro x captura 3 nao conferem (sem erro do SDK)")
	}
}

// abreSDKDireto prepara o SDK no proprio processo, como a fase 1 faz.
//
// Nao chama CoUninitialize: quem usa isto sao comandos de uma tacada so, que
// terminam o processo logo em seguida.
func abreSDKDireto() (*nbio, string, error) {
	dll := achaDLL()
	if dll == "" {
		return nil, "", errors.New("NBioBSP.dll nao encontrada")
	}
	procCoInitializeEx.Call(0, coinitApartmentThreaded)
	sdk, err := novoSDK(dll)
	if err != nil {
		return nil, dll, err
	}
	return sdk, dll, nil
}

// salvaTemplate captura um template e grava o texto num arquivo, sem comparar
// nada.
//
// Junto com confereTemplate, separa as duas metades do SDK, que ate agora so
// foram testadas na mesma maquina e por isso sempre falharam ou passaram juntas:
//
//   - template gerado na maquina sadia PASSA na doente  -> o comparador de la
//     esta bom, e quem produz dado ruim e o extrator/leitor.
//   - template gerado na maquina sadia FALHA na doente  -> o comparador e que
//     esta quebrado, e o leitor nao tem culpa.
//
// E a unica pergunta que sobrou depois que a versao da DLL foi descartada: a
// mesma DLL, byte a byte, passa numa maquina e falha na outra.
func salvaTemplate(caminho string) int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ligaConsole()

	sdk, dll, err := abreSDKDireto()
	if err != nil {
		fmt.Println("erro ao abrir o SDK:", err)
		return 1
	}
	defer func() { _ = sdk.encerra() }()
	fmt.Println("DLL:", descreveDLL(dll))
	if ids, err := sdk.listaDispositivos(); err == nil && len(ids) > 0 {
		fmt.Printf("IDs dos leitores: %v\n", ids)
	}

	fmt.Println(">>> Encoste o dedo no leitor")
	template, err := sdk.capturaTexto(purposeEnroll, 30000)
	if err != nil {
		fmt.Println("erro na captura:", err)
		return 1
	}
	if err := os.WriteFile(caminho, []byte(template), 0o600); err != nil {
		fmt.Println("erro ao gravar:", err)
		return 1
	}
	fmt.Println(forma("template salvo", template))
	fmt.Println("arquivo:", caminho)
	return 0
}

// confereTemplate le um template de arquivo e o compara com ele mesmo.
//
// Nao encosta no leitor: VerifyMatch nao precisa de dispositivo aberto. Da para
// rodar numa maquina sem hardware nenhum, e e so o comparador sendo exercitado.
func confereTemplate(caminho string) int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ligaConsole()

	dados, err := os.ReadFile(caminho)
	if err != nil {
		fmt.Println("erro ao ler o arquivo:", err)
		return 1
	}
	template := string(dados)

	sdk, dll, err := abreSDKDireto()
	if err != nil {
		fmt.Println("erro ao abrir o SDK:", err)
		return 1
	}
	defer func() { _ = sdk.encerra() }()
	fmt.Println("DLL:", descreveDLL(dll))
	fmt.Println(forma("template lido do arquivo", template))

	confere, err := sdk.comparaBrutos(template, template)
	switch {
	case err != nil:
		fmt.Println("FALHOU:", err)
		return 1
	case !confere:
		fmt.Println("FALHOU: nao conferiu consigo mesmo, e o SDK nao acusou erro")
		return 1
	}
	fmt.Println("OK: o template confere consigo mesmo nesta maquina")
	return 0
}

func (a *autoteste) encerra() int {
	a.diz("")
	if a.falhou {
		a.diz("RESULTADO: falhou - veja as linhas marcadas com FALHOU acima")
	} else {
		a.diz("RESULTADO: tudo passou")
	}

	if a.arquivo != nil {
		_ = a.arquivo.Close()
		a.arquivo = nil
	}
	if a.falhou {
		return 1
	}
	return 0
}
