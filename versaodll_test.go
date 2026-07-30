//go:build windows && 386

package main

import (
	"strconv"
	"strings"
	"testing"
)

// A leitura de versao usa tres chamadas encadeadas do Windows com ponteiro para
// ponteiro. Se o encadeamento estiver errado ela devolve "" caladamente, e o log
// volta a nao dizer qual SDK foi carregado - que e exatamente a lacuna que ela
// existe para fechar. kernel32.dll serve de referencia por estar sempre
// presente e sempre ter recurso de versao.
func TestVersaoArquivoLeDLLConhecida(t *testing.T) {
	v := versaoArquivo(`C:\Windows\System32\kernel32.dll`)
	if v == "" {
		t.Fatal("nao leu a versao de kernel32.dll")
	}
	campos := strings.Split(v, ".")
	if len(campos) != 4 {
		t.Fatalf("versao %q nao tem os quatro campos", v)
	}
	for _, campo := range campos {
		if _, err := strconv.Atoi(campo); err != nil {
			t.Fatalf("campo %q da versao %q nao e numero", campo, v)
		}
	}
	if campos[0] == "0" {
		t.Fatalf("versao %q comeca em zero, sinal de leitura no offset errado", v)
	}
}

func TestVersaoArquivoNaoQuebraComArquivoAusente(t *testing.T) {
	if v := versaoArquivo(`C:\nao\existe\NBioBSP.dll`); v != "" {
		t.Fatalf("esperava vazio para arquivo ausente, veio %q", v)
	}
}

func TestDescreveDLLDizQuandoNaoAchou(t *testing.T) {
	if d := descreveDLL(""); !strings.Contains(d, "nenhuma") {
		t.Fatalf("descricao de caminho vazio ficou %q", d)
	}
}

// A descricao precisa trazer versao e tamanho juntos: duas DLLs podem anunciar
// a mesma versao e serem builds diferentes.
func TestDescreveDLLTrazVersaoETamanho(t *testing.T) {
	d := descreveDLL(`C:\Windows\System32\kernel32.dll`)
	if !strings.Contains(d, "versao ") {
		t.Fatalf("descricao sem versao: %q", d)
	}
	if !strings.Contains(d, " bytes,") {
		t.Fatalf("descricao sem tamanho: %q", d)
	}
}
