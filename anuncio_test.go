//go:build windows && 386

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// usaDiretorioTemporario aponta o anuncio para uma pasta descartavel: os testes
// nao podem escrever no ProgramData da maquina que os roda.
func usaDiretorioTemporario(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	anterior := diretorioCompartilhado
	diretorioCompartilhado = func() string { return dir }
	t.Cleanup(func() { diretorioCompartilhado = anterior })
	return dir
}

func TestAnuncioVaiEVolta(t *testing.T) {
	usaDiretorioTemporario(t)
	const token = "umsegredoqueprecisatermaisde32letras"

	if err := publicaAnuncio(5150, token); err != nil {
		t.Fatalf("publicar: %v", err)
	}
	a, err := leAnuncio()
	if err != nil {
		t.Fatalf("ler: %v", err)
	}
	if a.Porta != 5150 || a.Token != token {
		t.Fatalf("anuncio veio diferente: %+v", a)
	}
	if a.Endereco != "http://127.0.0.1:5150" {
		t.Fatalf("endereco errado: %q", a.Endereco)
	}
}

// Um anuncio corrompido tem de ser recusado aqui. Passar adiante uma porta zero
// ou um token truncado so adiaria a falha para a primeira comparacao, com o
// usuario e o dedo ja esperando.
func TestAnuncioRecusaConteudoInutil(t *testing.T) {
	casos := map[string]string{
		"porta zero":    `{"porta":0,"token":"umsegredoqueprecisatermaisde32letras"}`,
		"porta alta":    `{"porta":70000,"token":"umsegredoqueprecisatermaisde32letras"}`,
		"token curto":   `{"porta":5150,"token":"curto"}`,
		"token ausente": `{"porta":5150}`,
		"nao e json":    `nao sou json`,
		"json truncado": `{"porta":5150,`,
	}
	for nome, conteudo := range casos {
		t.Run(nome, func(t *testing.T) {
			dir := usaDiretorioTemporario(t)
			if err := os.WriteFile(filepath.Join(dir, nomeArquivoAnuncio), []byte(conteudo), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := leAnuncio(); err == nil {
				t.Fatal("esperava erro")
			}
		})
	}
}

func TestAnuncioAusenteNaoEErroSilencioso(t *testing.T) {
	usaDiretorioTemporario(t)
	if _, err := leAnuncio(); err == nil {
		t.Fatal("esperava erro quando o arquivo nao existe")
	}
	// removeAnuncio sobre arquivo inexistente nao pode reclamar: e o caminho
	// normal de um comparador que nunca chegou a publicar.
	removeAnuncio()
}

// Reaproveitar o token publicado importa: token novo a cada reinicio do servico
// invalidaria os agentes ja de pe, e eles descobririam isso como 401 no meio de
// um atendimento.
func TestTokenDoComparadorReaproveitaOPublicado(t *testing.T) {
	usaDiretorioTemporario(t)
	t.Setenv("COMPARADOR_TOKEN", "")
	const token = "umsegredoqueprecisatermaisde32letras"
	if err := publicaAnuncio(5150, token); err != nil {
		t.Fatal(err)
	}
	obtido, err := tokenDoComparador()
	if err != nil {
		t.Fatalf("obter token: %v", err)
	}
	if obtido != token {
		t.Fatalf("gerou token novo em vez de reaproveitar: %q", obtido)
	}
}

func TestTokenDoComparadorGeraQuandoNaoHaNada(t *testing.T) {
	usaDiretorioTemporario(t)
	t.Setenv("COMPARADOR_TOKEN", "")
	obtido, err := tokenDoComparador()
	if err != nil {
		t.Fatalf("obter token: %v", err)
	}
	if len(obtido) < 32 {
		t.Fatalf("token gerado curto demais: %d caracteres", len(obtido))
	}
}

func TestTokenDoComparadorPrefereOAmbiente(t *testing.T) {
	usaDiretorioTemporario(t)
	const doAmbiente = "tokenvindodoambientecommaisde32letras"
	const publicado = "umsegredoqueprecisatermaisde32letras"
	if err := publicaAnuncio(5150, publicado); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPARADOR_TOKEN", doAmbiente)
	obtido, err := tokenDoComparador()
	if err != nil {
		t.Fatal(err)
	}
	if obtido != doAmbiente {
		t.Fatalf("ignorou o ambiente: %q", obtido)
	}
}

// O agente tem de achar o comparador sem variavel nenhuma - e o caminho normal
// depois da instalacao pelo MSI, e o que evita depender de logoff.
func TestConfiguraComparadorDescobrePeloAnuncio(t *testing.T) {
	usaDiretorioTemporario(t)
	t.Setenv("COMPARADOR_URL", "")
	t.Setenv("COMPARADOR_TOKEN", "")
	const token = "umsegredoqueprecisatermaisde32letras"
	if err := publicaAnuncio(5151, token); err != nil {
		t.Fatal(err)
	}
	comparadorRemoto = nil
	t.Cleanup(func() { comparadorRemoto = nil })

	configuraComparador()
	if comparadorRemoto == nil {
		t.Fatal("nao descobriu o comparador pelo anuncio")
	}
	if comparadorRemoto.base != "http://127.0.0.1:5151" {
		t.Fatalf("endereco errado: %q", comparadorRemoto.base)
	}
	if comparadorRemoto.token != token {
		t.Fatalf("token errado")
	}
}

// Sem anuncio e sem variaveis, comparar localmente e o comportamento certo:
// e uma estacao de trabalho, onde nao existe gancho nenhum.
func TestConfiguraComparadorFicaLocalSemAnuncio(t *testing.T) {
	usaDiretorioTemporario(t)
	t.Setenv("COMPARADOR_URL", "")
	t.Setenv("COMPARADOR_TOKEN", "")
	comparadorRemoto = nil
	t.Cleanup(func() { comparadorRemoto = nil })

	configuraComparador()
	if comparadorRemoto != nil {
		t.Fatalf("delegou sem ter para onde: %q", comparadorRemoto.base)
	}
}

// O ambiente continua mandando: e como a bancada do 10.10.11.30 esta montada, e
// e a saida quando o comparador vive noutra porta.
func TestConfiguraComparadorAindaAceitaOAmbiente(t *testing.T) {
	usaDiretorioTemporario(t)
	const token = "umsegredoqueprecisatermaisde32letras"
	t.Setenv("COMPARADOR_URL", "http://127.0.0.1:5199/")
	t.Setenv("COMPARADOR_TOKEN", token)
	comparadorRemoto = nil
	t.Cleanup(func() { comparadorRemoto = nil })

	configuraComparador()
	if comparadorRemoto == nil {
		t.Fatal("ignorou o ambiente")
	}
	if strings.HasSuffix(comparadorRemoto.base, "/") {
		t.Fatalf("a barra final ficou na base: %q", comparadorRemoto.base)
	}
}
