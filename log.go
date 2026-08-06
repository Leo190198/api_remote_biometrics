//go:build windows && 386

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)

func iniciaLog() { iniciaLogArquivo("agente.log") }

func iniciaLogArquivo(nome string) {
	dir, err := garanteDiretorioDados()
	if err != nil {
		return
	}
	iniciaLogEm(dir, nome)
}

// iniciaLogEm grava o log num diretorio escolhido.
//
// Existe para o comparador: rodando como servico, o diretorio de dados do
// usuario e o perfil do SYSTEM, e o log ficaria enterrado em
// C:\Windows\SysWOW64\config\systemprofile - um lugar que ninguem procura e que
// o instalador nao consegue limpar. Ao lado do anuncio, em ProgramData, ele
// fica onde quem der suporte vai olhar.
func iniciaLogEm(dir, nome string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, nome)
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		logger.SetOutput(f)
	}
}

func registraErro(formato string, args ...any) {
	logger.Printf("ERRO: "+formato, args...)
}

func registraInfo(formato string, args ...any) {
	logger.Printf(formato, args...)
}
