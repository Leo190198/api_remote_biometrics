//go:build windows && 386

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func arquivoCom(t *testing.T, conteudo string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "cadastro.hash")
	if err := os.WriteFile(caminho, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// O template pode terminar em '=' de preenchimento do base64. Cortar no
// primeiro '=' encontrado destruiria o dado silenciosamente, e a comparacao
// falharia por "template invalido" sem ninguem entender por que.
func TestLeTemplatePreservaPaddingBase64(t *testing.T) {
	template := "AQAAABQAAABk" + strings.Repeat("x", 40) + "=="
	chave, lido, err := leTemplateDeArquivo(arquivoCom(t, template))
	if err != nil {
		t.Fatalf("nao esperava erro: %v", err)
	}
	if chave != "" {
		t.Fatalf("inventou uma chave: %q", chave)
	}
	if lido != template {
		t.Fatalf("o template foi alterado:\n  queria %q\n  veio   %q", template, lido)
	}
}

func TestLeTemplateSeparaChaveEValor(t *testing.T) {
	template := "AQAAABQAAABk" + strings.Repeat("y", 40) + "=="
	chave, lido, err := leTemplateDeArquivo(arquivoCom(t, "BIOMETRIA_LEO="+template+"\n"))
	if err != nil {
		t.Fatalf("nao esperava erro: %v", err)
	}
	if chave != "BIOMETRIA_LEO" {
		t.Fatalf("chave errada: %q", chave)
	}
	if lido != template {
		t.Fatalf("valor errado: %q", lido)
	}
}

func TestLeTemplateRecusaConteudoInutil(t *testing.T) {
	casos := map[string]string{
		"vazio":             "",
		"curto demais":      "AQAAABQ=",
		"so a chave":        "BIOMETRIA_LEO=",
		"com acento":        "BIOMETRIA_LEO=" + strings.Repeat("á", 40),
		"com espaco dentro": "BIOMETRIA_LEO=AQAA BBQA" + strings.Repeat("z", 40),
	}
	for nome, conteudo := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, _, err := leTemplateDeArquivo(arquivoCom(t, conteudo)); err == nil {
				t.Fatal("esperava erro")
			}
		})
	}
}

func TestLeTemplateReclamaDeArquivoAusente(t *testing.T) {
	if _, _, err := leTemplateDeArquivo(filepath.Join(t.TempDir(), "nao-existe.hash")); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestEhNomeDeVariavel(t *testing.T) {
	validos := []string{"BIOMETRIA_LEO", "_x", "A1", "biometria_leo_2"}
	for _, s := range validos {
		if !ehNomeDeVariavel(s) {
			t.Errorf("%q devia ser nome valido", s)
		}
	}
	// O ultimo caso e o que importa: um template base64 nunca pode ser
	// confundido com nome de variavel, senao o '=' de padding vira separador.
	invalidos := []string{"", "1abc", "com-traco", "com espaco", "AQAAABQAAABk+/xyz", strings.Repeat("a", 65)}
	for _, s := range invalidos {
		if ehNomeDeVariavel(s) {
			t.Errorf("%q NAO devia ser nome valido", s)
		}
	}
}
