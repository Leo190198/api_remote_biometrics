//go:build windows && 386

package main

// Descobrir qual NBioBSP.dll o agente abriu, e de que versao ela e.
//
// Sem isso, duas maquinas com a mesma instalacao aparente podem estar
// carregando DLLs diferentes e nada no log denuncia. Foi o que aconteceu: a
// ordem de busca em achaDLL prefere C:\Windows\SysWOW64, entao uma maquina com
// uma copia ali usa uma versao do SDK e a outra, sem essa copia, usa a do
// diretorio do sistema legado.

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// versaoArquivo devolve a versao gravada no recurso do PE, ou "" se o arquivo
// nao trouxer essa informacao.
//
// O unsafe.Pointer aqui e sobre memoria do proprio Go, viva durante a chamada.
// E diferente do cuidado que sdk.go toma: la o perigo e guardar uintptr vindo
// da DLL e converter depois, quando o coletor ja pode ter movido tudo.
func versaoArquivo(caminho string) string {
	var ignorado windows.Handle
	tamanho, err := windows.GetFileVersionInfoSize(caminho, &ignorado)
	if err != nil || tamanho == 0 {
		return ""
	}
	buffer := make([]byte, tamanho)
	if err := windows.GetFileVersionInfo(caminho, 0, tamanho, unsafe.Pointer(&buffer[0])); err != nil {
		return ""
	}
	var fixo *windows.VS_FIXEDFILEINFO
	var tamFixo uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&fixo), &tamFixo); err != nil {
		return ""
	}
	if fixo == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d",
		fixo.FileVersionMS>>16, fixo.FileVersionMS&0xffff,
		fixo.FileVersionLS>>16, fixo.FileVersionLS&0xffff)
}

// modulosBiometricos descreve os modulos do SDK ja carregados neste processo,
// com endereco base e tamanho.
//
// A NBioBSP carrega o motor de comparacao por nome, em tempo de execucao. Ate
// agora so dava para adivinhar qual entrou, e numa maquina onde convivem o
// Venus da NITGEN e o NGStar da UnionCommunity a diferenca e o algoritmo
// inteiro. Comparar arquivos em disco nao responde isso: so a lista do que o
// processo abriu responde.
//
// O endereco base e o que fecha a conta. Quando a DLL derruba o processo, o Go
// imprime o PC da falha; com a faixa de cada modulo em maos, da para dizer qual
// deles estourou, em vez de deduzir pelo nome do arquivo.
// Lista todos os modulos, sem filtro de nome.
//
// A primeira versao filtrava por nomes da NITGEN e nao mostrou nada alem da
// propria NBioBSP - o que parecia conclusivo e nao era: um modulo injetado por
// terceiros nao casa com nenhum desses nomes e ficava invisivel justamente na
// pergunta que importava. Numa lista curta como a de um binario Go, filtrar
// custa mais do que economiza.
func modulosBiometricos() []string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE, uint32(windows.GetCurrentProcessId()))
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var entrada windows.ModuleEntry32
	entrada.Size = uint32(unsafe.Sizeof(entrada))
	if err := windows.Module32First(snap, &entrada); err != nil {
		return nil
	}

	var achados []string
	for {
		nome := windows.UTF16ToString(entrada.Module[:])
		caminho := windows.UTF16ToString(entrada.ExePath[:])
		// No Windows o HMODULE de um modulo carregado e o proprio endereco
		// base, entao nao ha ponteiro para converter aqui.
		inicio := uintptr(entrada.ModuleHandle)
		fim := inicio + uintptr(entrada.ModBaseSize)

		// Modulo do sistema aparece so com o nome; qualquer outro vem com
		// caminho e versao, porque e ai que mora o que uma maquina tem e a
		// outra nao.
		detalhe := ""
		if !ehModuloDoSistema(caminho) {
			detalhe = "  " + descreveDLL(caminho)
		}
		achados = append(achados, fmt.Sprintf("%-24s base 0x%08x fim 0x%08x%s", nome, inicio, fim, detalhe))

		if err := windows.Module32Next(snap, &entrada); err != nil {
			break
		}
	}
	return achados
}

func ehModuloDoSistema(caminho string) bool {
	c := strings.ToUpper(caminho)
	return strings.HasPrefix(c, `C:\WINDOWS\SYSTEM32\`) || strings.HasPrefix(c, `C:\WINDOWS\SYSWOW64\`)
}

// descreveDLL junta o que distingue uma instalacao da outra: caminho, versao e
// tamanho. Duas DLLs podem anunciar a mesma versao e serem builds diferentes,
// e ai o tamanho e a data resolvem.
func descreveDLL(caminho string) string {
	if caminho == "" {
		return "nenhuma NBioBSP.dll encontrada"
	}
	descricao := caminho
	if v := versaoArquivo(caminho); v != "" {
		descricao += " versao " + v
	}
	if info, err := os.Stat(caminho); err == nil {
		descricao += fmt.Sprintf(" (%d bytes, %s)", info.Size(), info.ModTime().Format("2006-01-02"))
	}
	return descricao
}
