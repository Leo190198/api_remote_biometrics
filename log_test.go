//go:build windows && 386

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// O log so rotacionava na abertura, entao o comparador - que e servico e fica
// meses de pe - crescia sem teto. Este teste fixa que a rotacao acontece
// durante a escrita, e que ela guarda exatamente uma geracao anterior.
func TestArquivoRotativoCortaDuranteAEscrita(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "teste.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	a := &arquivoRotativo{path: path, f: f}

	linha := []byte(strings.Repeat("x", 64<<10) + "\n")
	for escrito := 0; escrito <= limiteLog+len(linha); escrito += len(linha) {
		if _, err := a.Write(linha); err != nil {
			t.Fatalf("escrever: %v", err)
		}
	}
	_ = a.f.Close()

	atual, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat do log atual: %v", err)
	}
	if atual.Size() > limiteLog {
		t.Errorf("o log atual tem %d bytes, acima do limite de %d", atual.Size(), limiteLog)
	}
	anterior, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("a geracao anterior deveria existir: %v", err)
	}
	if anterior.Size() == 0 {
		t.Error("a geracao anterior ficou vazia")
	}
	// Uma terceira geracao significaria log se acumulando de novo, so que com
	// outro nome.
	if _, err := os.Stat(path + ".1.1"); !os.IsNotExist(err) {
		t.Error("apareceu uma geracao alem do .1")
	}
}

// Se uma rotacao anterior nao conseguiu reabrir o arquivo, f fica nulo. A
// escrita seguinte tem de descartar a linha em silencio: derrubar o processo
// de dentro do logger seria trocar um log cheio por uma parada total.
//
// n fica bem abaixo do limite de proposito: o alvo aqui e a guarda do Write,
// e nao a rotacao - que, ao reabrir o arquivo, se recupera sozinha.
func TestArquivoRotativoNaoEstouraComArquivoNulo(t *testing.T) {
	a := &arquivoRotativo{path: filepath.Join(t.TempDir(), "x.log"), f: nil, n: 0}
	n, err := a.Write([]byte("linha perdida\n"))
	if err != nil {
		t.Fatalf("nao deveria devolver erro: %v", err)
	}
	if n != len("linha perdida\n") {
		t.Errorf("devolveu %d bytes, esperado %d", n, len("linha perdida\n"))
	}
}
