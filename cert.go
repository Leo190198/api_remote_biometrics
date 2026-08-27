//go:build windows && 386

package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

func caminhosCert() (cert, key string) {
	dir := diretorioDados()
	return filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
}

// caminhosCertMaquina aponta para o certificado unico da maquina, ao lado do
// anuncio do comparador em ProgramData.
//
// E o mesmo diretorio de onde todo agente de sessao ja le o comparador.json
// com o token do servico, entao a ACL necessaria (Users: leitura) esta provada
// em producao por aquele arquivo.
func caminhosCertMaquina() (cert, key string) {
	dir := diretorioCompartilhado()
	return filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
}

func certificadoValido(certPath, keyPath string, agora time.Time) error {
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		return fmt.Errorf("par certificado/chave invalido: %w", err)
	}
	dados, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	bloco, _ := pem.Decode(dados)
	if bloco == nil || bloco.Type != "CERTIFICATE" {
		return errors.New("certificado PEM invalido")
	}
	cert, err := x509.ParseCertificate(bloco.Bytes)
	if err != nil {
		return err
	}
	if agora.Before(cert.NotBefore) || agora.Add(30*24*time.Hour).After(cert.NotAfter) {
		return errors.New("certificado expirado ou proximo da expiracao")
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		return fmt.Errorf("certificado nao vale para localhost: %w", err)
	}
	return nil
}

func materialCertificado(agora time.Time) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Agente de Biometria (localhost)",
			Organization: []string{"BiometriaAgente"},
		},
		NotBefore:             agora.Add(-time.Hour),
		NotAfter:              agora.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func gerarCert() error {
	certPath, keyPath := caminhosCert()
	agora := time.Now()
	if certificadoValido(certPath, keyPath, agora) == nil {
		return nil
	}
	certPEM, keyPEM, err := materialCertificado(agora)
	if err != nil {
		return err
	}
	if err := gravaArquivoAtomico(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("gravar chave: %w", err)
	}
	if err := gravaArquivoAtomico(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("gravar certificado: %w", err)
	}
	if err := certificadoValido(certPath, keyPath, agora); err != nil {
		return err
	}
	fmt.Println("certificado gerado em", certPath)
	return nil
}

// prazoCertutil limita o certutil, que roda no caminho de subida do agente.
//
// Sem prazo, um certutil travado deixa o agente pendurado antes de a bandeja
// aparecer: a porta ja esta aberta, nada atende, e o relato de campo e "o
// agente nao abre" sem nenhuma pista no log. Num servidor RDP isso acontece por
// usuario - trinta pessoas logando de manha sao trinta certutil simultaneos
// contra o mesmo repositorio de certificados.
//
// Um minuto e folga larga de proposito. Desistir cedo trocaria HTTPS por HTTP
// numa maquina que so estava lenta, e essa troca deve acontecer so quando o
// certutil realmente nao vai responder.
const prazoCertutil = time.Minute

func instalaCertificadoUsuario(certPath string) error {
	ctx, cancela := context.WithTimeout(context.Background(), prazoCertutil)
	defer cancela()
	cmd := exec.CommandContext(ctx, "certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	saida, err := cmd.CombinedOutput()
	// Checa o contexto antes do erro: um processo morto pelo prazo devolve
	// "exit status 1", que mandaria quem le o log atras do certificado em vez
	// do travamento.
	if ctx.Err() != nil {
		return fmt.Errorf("certutil nao respondeu em %s", prazoCertutil)
	}
	if err != nil {
		return fmt.Errorf("certutil: %w: %s", err, string(saida))
	}
	return nil
}

// instalaCertificadoMaquina registra o certificado na loja Root da maquina.
//
// Sem "-user", ao contrario do caminho do usuario. Essa e a diferenca que
// resolve o problema: adicionar a loja Root **do usuario** sempre abre o aviso
// de seguranca do Windows, e nao existe forma silenciosa de faze-lo - e por
// design. Na loja da maquina, com processo elevado, o registro e silencioso.
// Foi por isso que o caminho antigo acumulou 226 recusas em producao: os
// usuarios negavam um dialogo que aparecia a cada logon.
func instalaCertificadoMaquina(certPath string) error {
	ctx, cancela := context.WithTimeout(context.Background(), prazoCertutil)
	defer cancela()
	cmd := exec.CommandContext(ctx, "certutil.exe", "-addstore", "-f", "Root", certPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	saida, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("certutil nao respondeu em %s", prazoCertutil)
	}
	if err != nil {
		return fmt.Errorf("certutil: %w: %s", err, string(saida))
	}
	return nil
}

// garanteCertificadoMaquina gera e registra o certificado da maquina, criando
// um novo quando o atual esta perto de vencer. Quem chama e o servico
// comparador, que roda como SYSTEM e sobe a cada boot - e por isso a renovacao
// acontece sozinha, sem depender de reinstalar o pacote daqui a dois anos.
func garanteCertificadoMaquina() error {
	certPath, keyPath := caminhosCertMaquina()
	agora := time.Now()
	if certificadoValido(certPath, keyPath, agora) == nil {
		return nil
	}
	certPEM, keyPEM, err := materialCertificado(agora)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("criar diretorio compartilhado: %w", err)
	}
	// 0o644, e nao 0o600 como no certificado do usuario: aqui a chave precisa
	// ser legivel por todo agente de sessao. E a contrapartida aceita deste
	// desenho - a defesa contra um agente impostor de outra sessao continua
	// sendo mesmaSessao() mais o token, nao o TLS.
	if err := gravaArquivoAtomico(keyPath, keyPEM, 0o644); err != nil {
		return fmt.Errorf("gravar chave da maquina: %w", err)
	}
	if err := gravaArquivoAtomico(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("gravar certificado da maquina: %w", err)
	}
	if err := certificadoValido(certPath, keyPath, agora); err != nil {
		return err
	}
	return instalaCertificadoMaquina(certPath)
}

// impressaoDigital devolve o thumbprint SHA-1 do certificado, que e como o
// Windows indexa a loja e o que o certutil aceita para apagar um registro
// especifico. Nao e escolha de seguranca: e o identificador do repositorio.
func impressaoDigital(der []byte) string {
	soma := sha1.Sum(der)
	return fmt.Sprintf("%x", soma)
}

// removeCertificadoMaquina desfaz o que garanteCertificadoMaquina fez.
//
// Sem isto, desinstalar o agente deixava uma raiz confiavel orfa na maquina —
// exatamente o que o instalar-servidor.ps1 hoje assume ao dizer que preserva o
// certificado de cada usuario. Raiz confiavel que ninguem sabe de onde veio nao
// deve sobreviver a desinstalacao.
func removeCertificadoMaquina() error {
	certPath, keyPath := caminhosCertMaquina()
	dados, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	bloco, _ := pem.Decode(dados)
	if bloco == nil || bloco.Type != "CERTIFICATE" {
		return errors.New("certificado da maquina ilegivel")
	}

	ctx, cancela := context.WithTimeout(context.Background(), prazoCertutil)
	defer cancela()
	cmd := exec.CommandContext(ctx, "certutil.exe", "-delstore", "Root", impressaoDigital(bloco.Bytes))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	saida, erroCertutil := cmd.CombinedOutput()

	// Os arquivos saem mesmo se o certutil falhar: deixar a chave privada para
	// tras seria pior do que deixar o registro na loja, e o registro sem a
	// chave correspondente nao serve para nada.
	if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(certPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("certutil nao respondeu em %s", prazoCertutil)
	}
	if erroCertutil != nil {
		return fmt.Errorf("certutil: %w: %s", erroCertutil, string(saida))
	}
	return nil
}

func configuracaoTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}

func carregaTLS() (*tls.Config, error) {
	certPath, keyPath := caminhosCertMaquina()
	if certificadoValido(certPath, keyPath, time.Now()) == nil {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err == nil {
			registraInfo("TLS: certificado de maquina em %s", certPath)
			return configuracaoTLS(cert), nil
		}
		registraErro("certificado de maquina ilegivel: %v", err)
	}

	// Onde existe comparador, o certificado da maquina e responsabilidade dele.
	// Cair no caminho do usuario aqui traria de volta o dialogo no logon, que e
	// justamente o defeito que este desenho remove. Melhor servir HTTP: o
	// proprio integra-biometria.js ja tenta os dois protocolos, e Chrome, Edge
	// e Firefox aceitam http://localhost a partir de pagina https.
	if _, err := leAnuncio(); err == nil {
		return nil, errors.New("certificado da maquina ainda nao publicado pelo comparador")
	}

	// Estacao de trabalho, sem servico: continua como sempre foi. Ali o
	// certificado e do proprio usuario, o dialogo aparece uma vez e quem
	// responde e o dono da maquina.
	if err := gerarCert(); err != nil {
		return nil, err
	}
	certPath, keyPath = caminhosCert()
	if err := instalaCertificadoUsuario(certPath); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return configuracaoTLS(cert), nil
}

type connEspiada struct {
	net.Conn
	r *bufio.Reader
}

func (c *connEspiada) Read(b []byte) (int, error) { return c.r.Read(b) }

type listenerMista struct {
	bruto net.Listener
	cfg   *tls.Config
	conns chan net.Conn
	done  chan struct{}
	sem   chan struct{}
	once  sync.Once
	errMu sync.RWMutex
	err   error
}

func novaListenerMista(l net.Listener, cfg *tls.Config) net.Listener {
	if cfg == nil {
		return l
	}
	m := &listenerMista{
		bruto: l,
		cfg:   cfg,
		conns: make(chan net.Conn, 32),
		done:  make(chan struct{}),
		sem:   make(chan struct{}, 64),
	}
	go m.aceita()
	return m
}

func (m *listenerMista) falha(err error) {
	m.once.Do(func() {
		m.errMu.Lock()
		m.err = err
		m.errMu.Unlock()
		close(m.done)
	})
}

func (m *listenerMista) aceita() {
	atraso := time.Duration(0)
	for {
		c, err := m.bruto.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if atraso == 0 {
					atraso = 5 * time.Millisecond
				} else {
					atraso *= 2
				}
				if atraso > time.Second {
					atraso = time.Second
				}
				time.Sleep(atraso)
				continue
			}
			m.falha(err)
			return
		}
		atraso = 0
		select {
		case m.sem <- struct{}{}:
			go m.espia(c)
		default:
			_ = c.Close()
		}
	}
}

func (m *listenerMista) espia(c net.Conn) {
	defer func() { <-m.sem }()
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = c.Close()
		return
	}
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return
	}
	var pronta net.Conn = &connEspiada{Conn: c, r: br}
	if b[0] == 0x16 {
		pronta = tls.Server(pronta, m.cfg)
	}
	select {
	case m.conns <- pronta:
	case <-m.done:
		_ = pronta.Close()
	}
}

func (m *listenerMista) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		m.errMu.RLock()
		err := m.err
		m.errMu.RUnlock()
		if err == nil {
			err = net.ErrClosed
		}
		return nil, err
	}
}

func (m *listenerMista) Close() error {
	err := m.bruto.Close()
	m.falha(net.ErrClosed)
	return err
}

func (m *listenerMista) Addr() net.Addr { return m.bruto.Addr() }
