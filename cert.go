//go:build windows && 386

package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

func instalaCertificadoUsuario(certPath string) error {
	cmd := exec.Command("certutil.exe", "-user", "-addstore", "-f", "Root", certPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	saida, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil: %w: %s", err, string(saida))
	}
	return nil
}

func carregaTLS() (*tls.Config, error) {
	if err := gerarCert(); err != nil {
		return nil, err
	}
	certPath, keyPath := caminhosCert()
	if err := instalaCertificadoUsuario(certPath); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
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
