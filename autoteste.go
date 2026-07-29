//go:build windows && 386

package main

// Autoteste do caminho de biometria, para rodar na maquina que tem leitor e
// NBioBSP.dll:
//
//	AgenteBiometria.exe --autoteste
//
// Existe porque VerifyMatch estava devolvendo 0x000B (checksum) mesmo com os
// dois templates recem-capturados e comparados em memoria, sem passar por banco
// nenhum. Nesse ponto sobram duas explicacoes possiveis, e elas exigem
// correcoes opostas:
//
//  1. o template e alterado no caminho ate a DLL (normalizacao, transporte);
//  2. a chamada a DLL esta montada errada (layout do INPUT_FIR, argumentos).
//
// Comparar um template com ele mesmo, cru e no mesmo processo, separa as duas:
// se nem isso confere, nada foi alterado e o defeito e da chamada. O resultado
// vai para autoteste.log ao lado dos outros logs, sem nenhum template dentro.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	linhas []string
	falhou bool
}

func (a *autoteste) diz(formato string, args ...any) {
	linha := fmt.Sprintf(formato, args...)
	a.linhas = append(a.linhas, linha)
	fmt.Println(linha)
}

func (a *autoteste) erro(formato string, args ...any) {
	a.falhou = true
	a.diz("FALHOU: "+formato, args...)
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

func rodaAutoteste() int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ligaConsole()

	a := &autoteste{}
	a.diz("autoteste do agente de biometria - versao %s commit %s", versao, commit)
	a.diz("horario: %s", time.Now().Format("2006-01-02 15:04:05"))

	dll := achaDLL()
	if dll == "" {
		a.erro("NBioBSP.dll nao encontrada. Defina NBIOBSP_DLL e tente de novo.")
		return a.encerra()
	}
	a.diz("DLL: %s", dll)

	if r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); r == 0 || r == 1 {
		defer procCoUninitialize.Call()
	}

	sdk, err := novoSDK(dll)
	if err != nil {
		a.erro("abrir o SDK: %v", err)
		return a.encerra()
	}
	defer func() { _ = sdk.encerra() }()

	n, err := sdk.contaDispositivos()
	if err != nil {
		a.erro("contar dispositivos: %v", err)
		return a.encerra()
	}
	a.diz("leitores encontrados: %d", n)
	if n == 0 {
		a.erro("nenhum leitor conectado; o resto do teste precisa de hardware")
		return a.encerra()
	}

	a.diz("")
	a.diz(">>> Encoste o dedo no leitor (captura 1 de 2)")
	primeiro, err := sdk.capturaTexto(purposeEnroll, 30000)
	if err != nil {
		a.erro("captura 1: %v", err)
		return a.encerra()
	}
	a.diz("%s", forma("captura 1", primeiro))

	// O teste decisivo. Um template comparado consigo mesmo, byte a byte como
	// saiu da DLL, tem que conferir. Se falhar por checksum aqui, nenhuma
	// normalizacao nem transporte esteve envolvido: o defeito esta em como
	// montamos a chamada, e nao no dado.
	a.diz("")
	confere, err := sdk.comparaBrutos(primeiro, primeiro)
	switch {
	case err != nil:
		a.erro("comparar a captura 1 com ela mesma (bytes crus): %v", err)
		a.diz("  => o template nao foi alterado por nada nosso, logo o problema")
		a.diz("     esta na montagem da chamada ao SDK, nao no dado.")
	case !confere:
		a.erro("a captura 1 nao confere com ela mesma, e o SDK nao acusou erro")
	default:
		a.diz("OK: captura 1 confere com ela mesma (bytes crus)")
	}

	// Mesmo par, agora pelo caminho normal. Se este falhar e o de cima passar,
	// a normalizacao e a culpada.
	limpo := normalizaTemplate(primeiro)
	if limpo == "" {
		a.erro("normalizaTemplate rejeitou um template recem-capturado pelo proprio SDK")
	} else {
		if limpo != primeiro {
			a.diz("ATENCAO: a normalizacao alterou o template (%d -> %d bytes)", len(primeiro), len(limpo))
		}
		confere, err = sdk.comparaTextos(primeiro, primeiro)
		switch {
		case err != nil:
			a.erro("comparar a captura 1 com ela mesma (normalizado): %v", err)
			a.diz("  => como o teste com bytes crus passou, a normalizacao e a causa.")
		case !confere:
			a.erro("captura 1 nao confere com ela mesma pelo caminho normalizado")
		default:
			a.diz("OK: captura 1 confere com ela mesma (normalizado)")
		}
	}

	a.diz("")
	a.diz(">>> Encoste o MESMO dedo no leitor (captura 2 de 2)")
	segundo, err := sdk.capturaTexto(purposeVerify, 30000)
	if err != nil {
		a.erro("captura 2: %v", err)
		return a.encerra()
	}
	a.diz("%s", forma("captura 2", segundo))

	// A captura 1 foi feita com purpose de cadastro e a 2 com purpose de
	// verificacao, que e exatamente o par que /comparar usa em producao. Se so
	// uma das duas falhar consigo mesma, o purpose e a variavel que importa.
	a.diz("")
	confere, err = sdk.comparaBrutos(segundo, segundo)
	switch {
	case err != nil:
		a.erro("comparar a captura 2 com ela mesma (bytes crus): %v", err)
	case !confere:
		a.erro("a captura 2 nao confere com ela mesma, e o SDK nao acusou erro")
	default:
		a.diz("OK: captura 2 confere com ela mesma (bytes crus)")
	}

	a.diz("")
	confere, err = sdk.comparaTextos(primeiro, segundo)
	switch {
	case err != nil:
		a.erro("comparar captura 1 com captura 2: %v", err)
	case confere:
		a.diz("OK: as duas capturas do mesmo dedo conferem - o caminho esta sadio")
	default:
		a.diz("as duas capturas nao conferem (sem erro do SDK)")
		a.diz("  => se foi mesmo o mesmo dedo, e questao de qualidade da leitura,")
		a.diz("     nao de defeito no codigo.")
	}

	return a.encerra()
}

func (a *autoteste) encerra() int {
	a.diz("")
	if a.falhou {
		a.diz("RESULTADO: falhou - veja as linhas marcadas com FALHOU acima")
	} else {
		a.diz("RESULTADO: tudo passou")
	}

	destino := "autoteste.log"
	if dir, err := garanteDiretorioDados(); err == nil {
		destino = filepath.Join(dir, "autoteste.log")
	}
	conteudo := strings.Join(a.linhas, "\r\n") + "\r\n"
	if err := os.WriteFile(destino, []byte(conteudo), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "gravar o relatorio:", err)
	} else {
		fmt.Println("relatorio gravado em", destino)
	}
	if a.falhou {
		return 1
	}
	return 0
}
