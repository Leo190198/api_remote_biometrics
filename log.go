//go:build windows && 386

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// limiteLog e o mesmo tamanho que a abertura ja usava para decidir a rotacao.
const limiteLog = 5 << 20

var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)

// arquivoRotativo corta o log quando ele passa do limite, e nao so na abertura.
//
// A checagem existia apenas em iniciaLogEm, o que basta para o agente, que
// reinicia com frequencia. Nao basta para o comparador: ele e um servico,
// projetado para ficar meses de pe, e cada comparacao escreve algumas linhas.
// O comparador.log crescia sem teto ate encher o disco do servidor - e ai o que
// para nao e uma estacao, e a conferencia de todo mundo.
//
// Nao precisa de trava propria: log.Logger segura o mutex dele durante o
// Write, entao as chamadas chegam aqui uma de cada vez.
type arquivoRotativo struct {
	path string
	f    *os.File
	n    int64
}

func (a *arquivoRotativo) Write(p []byte) (int, error) {
	if a.n+int64(len(p)) > limiteLog {
		a.rotaciona()
	}
	// Uma rotacao que nao conseguiu reabrir o arquivo deixa f nulo. Escrever
	// nele seria trocar um log cheio por um panic dentro do proprio logger,
	// que e o ultimo lugar de onde se quer derrubar o agente.
	if a.f == nil {
		return len(p), nil
	}
	n, err := a.f.Write(p)
	a.n += int64(n)
	return n, err
}

// rotaciona repete exatamente o que a abertura fazia: descarta o .1 anterior,
// move o atual para .1 e recomeca. Mesmo limite, mesmos nomes, mesmo numero de
// geracoes guardadas.
func (a *arquivoRotativo) rotaciona() {
	_ = a.f.Close()
	_ = os.Remove(a.path + ".1")
	_ = os.Rename(a.path, a.path+".1")
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		// Sem arquivo novo nao da para escrever nem para rotacionar de novo.
		// Descartar as linhas e melhor do que derrubar quem chamou o log.
		a.f = nil
		a.n = 0
		return
	}
	a.f = f
	a.n = 0
}

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
	if info, err := os.Stat(path); err == nil && info.Size() > limiteLog {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	// Parte do tamanho que o arquivo ja tem, senao um log reaberto perto do
	// limite so rotacionaria depois de escrever 5 MB novos por cima.
	var atual int64
	if info, err := f.Stat(); err == nil {
		atual = info.Size()
	}
	logger.SetOutput(&arquivoRotativo{path: path, f: f, n: atual})
}

func registraErro(formato string, args ...any) {
	logger.Printf("ERRO: "+formato, args...)
}

func registraInfo(formato string, args ...any) {
	logger.Printf(formato, args...)
}
