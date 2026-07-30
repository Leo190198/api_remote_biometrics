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
