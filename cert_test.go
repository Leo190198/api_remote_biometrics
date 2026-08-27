//go:build windows && 386

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
	"time"
)

// decodificaPrimeiroPEM devolve o DER e o certificado ja analisado.
func decodificaPrimeiroPEM(t *testing.T, dados []byte) ([]byte, *x509.Certificate) {
	t.Helper()
	bloco, _ := pem.Decode(dados)
	if bloco == nil || bloco.Type != "CERTIFICATE" {
		t.Fatal("PEM nao contem certificado")
	}
	cert, err := x509.ParseCertificate(bloco.Bytes)
	if err != nil {
		t.Fatalf("analisar certificado: %v", err)
	}
	return bloco.Bytes, cert
}

// escreveCertificadoDaMaquina monta um par valido no diretorio compartilhado
// sem passar pelo certutil: os testes nao podem mexer na loja de certificados
// da maquina que os roda.
func escreveCertificadoDaMaquina(t *testing.T) (certPEM []byte, certPath string) {
	t.Helper()
	certPEM, keyPEM, err := materialCertificado(time.Now())
	if err != nil {
		t.Fatalf("materialCertificado: %v", err)
	}
	certPath, keyPath := caminhosCertMaquina()
	if err := gravaArquivoAtomico(keyPath, keyPEM, 0o644); err != nil {
		t.Fatalf("gravar chave: %v", err)
	}
	if err := gravaArquivoAtomico(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("gravar certificado: %v", err)
	}
	return certPEM, certPath
}

// Havendo certificado de maquina, e ele que vale — e o caminho do usuario, que
// e o unico capaz de abrir dialogo no logon, nem chega a ser tentado.
func TestCarregaTLSPrefereCertificadoDaMaquina(t *testing.T) {
	usaDiretorioTemporario(t)
	certPEM, _ := escreveCertificadoDaMaquina(t)

	// Aponta o diretorio do usuario para outro lugar vazio: se carregaTLS
	// caisse no caminho antigo, geraria um par diferente aqui, e a comparacao
	// abaixo denunciaria.
	dirUsuario := t.TempDir()
	anterior := diretorioDados
	diretorioDados = func() string { return dirUsuario }
	t.Cleanup(func() { diretorioDados = anterior })

	cfg, err := carregaTLS()
	if err != nil {
		t.Fatalf("carregaTLS: %v", err)
	}
	if len(cfg.Certificates) != 1 || len(cfg.Certificates[0].Certificate) == 0 {
		t.Fatal("configuracao TLS veio sem certificado")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d; queria TLS 1.2", cfg.MinVersion)
	}

	certDaMaquina, _ := decodificaPrimeiroPEM(t, certPEM)
	if !bytes.Equal(cfg.Certificates[0].Certificate[0], certDaMaquina) {
		t.Fatal("carregaTLS nao usou o certificado da maquina")
	}

	// E nada foi gerado no perfil do usuario.
	certUsuario, _ := caminhosCert()
	if _, err := os.Stat(certUsuario); err == nil {
		t.Fatal("carregaTLS gerou certificado de usuario havendo o da maquina")
	}
}

// Numa maquina com comparador mas ainda sem o certificado dele, o agente serve
// HTTP em vez de cair no caminho do usuario. Cair ali traria de volta o dialogo
// que os usuarios negavam a cada logon — 226 recusas medidas em producao.
func TestCarregaTLSNaoPedeCertificadoAoUsuarioOndeHaComparador(t *testing.T) {
	usaDiretorioTemporario(t)
	if err := publicaAnuncio(5150, "umsegredoqueprecisatermaisde32letras"); err != nil {
		t.Fatalf("publicar anuncio: %v", err)
	}

	dirUsuario := t.TempDir()
	anterior := diretorioDados
	diretorioDados = func() string { return dirUsuario }
	t.Cleanup(func() { diretorioDados = anterior })

	cfg, err := carregaTLS()
	if err == nil {
		t.Fatal("queria erro para o agente seguir em HTTP")
	}
	if cfg != nil {
		t.Fatal("configuracao TLS deveria vir nula")
	}
	certUsuario, _ := caminhosCert()
	if _, err := os.Stat(certUsuario); err == nil {
		t.Fatal("nao pode gerar nem instalar certificado de usuario havendo comparador")
	}
}

// O certificado e folha, nao autoridade. E isso que confina o estrago ao
// localhost: confiar nele na loja Root da maquina e confiar exatamente nele, e
// nao numa CA capaz de emitir certificado para qualquer dominio.
func TestCertificadoDaMaquinaNaoEAutoridade(t *testing.T) {
	certPEM, _, err := materialCertificado(time.Now())
	if err != nil {
		t.Fatalf("materialCertificado: %v", err)
	}
	_, cert := decodificaPrimeiroPEM(t, certPEM)
	if cert.IsCA {
		t.Fatal("certificado nao pode ser CA")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Fatal("certificado nao pode ter permissao de assinar outros certificados")
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Fatalf("certificado precisa valer para localhost: %v", err)
	}
	if err := cert.VerifyHostname("intranet.exemplo"); err == nil {
		t.Fatal("certificado nao pode valer para outro host")
	}
}
