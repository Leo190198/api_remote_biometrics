//go:build windows && 386

package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxOrigensPendentes = 8
	validadePendente    = 10 * time.Minute
)

type gerenciadorOrigens struct {
	mu           sync.RWMutex
	aprovadas    map[string]struct{}
	pendentes    map[string]time.Time
	predefinidas map[string]struct{}
	novas        chan string
	caminho      string
}

func novoGerenciadorOrigens() *gerenciadorOrigens {
	g := &gerenciadorOrigens{
		aprovadas:    make(map[string]struct{}),
		pendentes:    make(map[string]time.Time),
		predefinidas: make(map[string]struct{}),
		novas:        make(chan string, maxOrigensPendentes),
		caminho:      filepath.Join(diretorioDados(), "origens-autorizadas.json"),
	}
	g.carrega()
	g.carregaPredefinidas(os.Getenv("CORS_ORIGEM"))
	if sistema := os.Getenv("SISTEMA_URL"); sistema != "" {
		if origem, err := normalizaOrigem(sistema); err == nil {
			g.predefinidas[origem] = struct{}{}
		}
	}
	return g
}

func normalizaOrigem(valor string) (string, error) {
	valor = strings.TrimSpace(valor)
	u, err := url.Parse(valor)
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("origem invalida")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("origem deve usar http ou https")
	}
	// SISTEMA_URL pode conter caminho; para o header Origin interessa so o host.
	return scheme + "://" + strings.ToLower(u.Host), nil
}

func (g *gerenciadorOrigens) carregaPredefinidas(lista string) {
	for _, item := range strings.FieldsFunc(lista, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.TrimSpace(item) == "*" {
			continue
		}
		if origem, err := normalizaOrigem(item); err == nil {
			g.predefinidas[origem] = struct{}{}
		}
	}
}

func (g *gerenciadorOrigens) carrega() {
	dados, err := os.ReadFile(g.caminho)
	if err != nil {
		return
	}
	var origens []string
	if json.Unmarshal(dados, &origens) != nil {
		return
	}
	for _, item := range origens {
		if origem, err := normalizaOrigem(item); err == nil {
			g.aprovadas[origem] = struct{}{}
		}
	}
}

func (g *gerenciadorOrigens) permitida(origem string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, fixa := g.predefinidas[origem]
	_, aprovada := g.aprovadas[origem]
	return fixa || aprovada
}

func (g *gerenciadorOrigens) solicita(origem string) bool {
	g.mu.Lock()
	agora := time.Now()
	for pendente, criadaEm := range g.pendentes {
		if agora.Sub(criadaEm) >= validadePendente {
			delete(g.pendentes, pendente)
		}
	}
	if _, ok := g.predefinidas[origem]; ok {
		g.mu.Unlock()
		return true
	}
	if _, ok := g.aprovadas[origem]; ok {
		g.mu.Unlock()
		return true
	}
	if _, ok := g.pendentes[origem]; ok {
		g.mu.Unlock()
		return false
	}
	if len(g.pendentes) >= maxOrigensPendentes {
		g.mu.Unlock()
		return false
	}
	g.pendentes[origem] = agora
	g.mu.Unlock()
	select {
	case g.novas <- origem:
	default:
	}
	return false
}

func (g *gerenciadorOrigens) aprova(origem string) error {
	g.mu.Lock()
	delete(g.pendentes, origem)
	g.aprovadas[origem] = struct{}{}
	err := g.salvaBloqueado()
	g.mu.Unlock()
	return err
}

func (g *gerenciadorOrigens) revogaTodas() error {
	g.mu.Lock()
	g.aprovadas = make(map[string]struct{})
	g.pendentes = make(map[string]time.Time)
	err := g.salvaBloqueado()
	g.mu.Unlock()
	return err
}

func (g *gerenciadorOrigens) salvaBloqueado() error {
	origens := make([]string, 0, len(g.aprovadas))
	for origem := range g.aprovadas {
		origens = append(origens, origem)
	}
	sort.Strings(origens)
	dados, err := json.MarshalIndent(origens, "", "  ")
	if err != nil {
		return err
	}
	dados = append(dados, '\n')
	return gravaArquivoAtomico(g.caminho, dados, 0o600)
}
