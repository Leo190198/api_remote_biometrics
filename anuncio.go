//go:build windows && 386

package main

// Como o agente de cada sessao acha o comparador da sessao 0.
//
// O comparador publica porta e token num arquivo em ProgramData; o agente le.
// Isso substitui as variaveis de ambiente da maquina, que tinham dois defeitos
// praticos: uma sessao ja aberta so enxerga o ambiente que tinha no logon (o
// Explorer nao recebe a mudanca, entao um agente aberto logo apos a instalacao
// nao delegava nada), e o instalador precisava sortear o segredo antes de o
// servico existir.
//
// ProgramData e o lugar certo: a ACL herdada da pasta ja da leitura a Users e
// escrita so a SYSTEM e administradores, que e exatamente a divisao aqui - o
// servico escreve, os agentes leem.
//
// Sobre o segredo: qualquer usuario logado consegue le-lo. Isso e inerente ao
// desenho, nao um descuido - o agente roda como o usuario e precisa se
// autenticar. O comparador nao guarda digital nenhuma; o que um leitor do
// arquivo ganha e poder confirmar um par de templates que ele ja tenha em
// maos. A alternativa (segredo por sessao) exigiria um canal privilegiado que
// nao existe hoje.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const nomeArquivoAnuncio = "comparador.json"

type anuncioComparador struct {
	Porta    int    `json:"porta"`
	Token    string `json:"token"`
	PID      int    `json:"pid"`
	Versao   string `json:"versao"`
	Desde    string `json:"desde"`
	Endereco string `json:"endereco"`
}

// diretorioCompartilhado e variavel para os testes poderem apontar para outro
// lugar sem escrever no ProgramData da maquina.
var diretorioCompartilhado = func() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "AgenteBiometria")
}

func caminhoAnuncio() string {
	return filepath.Join(diretorioCompartilhado(), nomeArquivoAnuncio)
}

func leAnuncio() (*anuncioComparador, error) {
	dados, err := os.ReadFile(caminhoAnuncio())
	if err != nil {
		return nil, err
	}
	var a anuncioComparador
	if err := json.Unmarshal(dados, &a); err != nil {
		return nil, err
	}
	// Um anuncio sem porta util ou com token curto nao serve para nada, e
	// deixar passar so adiaria a falha para a primeira comparacao.
	if a.Porta < 1 || a.Porta > 65535 {
		return nil, errors.New("anuncio do comparador tem porta invalida")
	}
	if len(a.Token) < 32 {
		return nil, errors.New("anuncio do comparador tem token curto demais")
	}
	if a.Endereco == "" {
		a.Endereco = "http://127.0.0.1:" + strconv.Itoa(a.Porta)
	}
	return &a, nil
}

func publicaAnuncio(porta int, token string) error {
	a := anuncioComparador{
		Porta:    porta,
		Token:    token,
		PID:      os.Getpid(),
		Versao:   versao,
		Desde:    time.Now().Format(time.RFC3339),
		Endereco: "http://127.0.0.1:" + strconv.Itoa(porta),
	}
	dados, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return gravaArquivoAtomico(caminhoAnuncio(), append(dados, '\n'), 0o644)
}

// removeAnuncio apaga o anuncio ao parar. Sem isso, um agente aberto depois de
// o servico cair tentaria delegar para uma porta morta e so descobriria o
// problema na primeira comparacao, com o usuario e o dedo ja esperando.
func removeAnuncio() {
	if err := os.Remove(caminhoAnuncio()); err != nil && !os.IsNotExist(err) {
		registraErro("remover anuncio do comparador: %v", err)
	}
}

// tokenDoComparador decide o segredo do servico, na ordem: o que o ambiente
// mandar, o que ja foi publicado antes, ou um novo.
//
// Reaproveitar o token publicado importa: a cada reinicio do servico um token
// novo invalidaria os agentes que ja estao de pe, e eles so descobririam isso
// como 401 no meio de um atendimento.
func tokenDoComparador() (string, error) {
	if t := os.Getenv("COMPARADOR_TOKEN"); len(t) >= 32 {
		return t, nil
	}
	if a, err := leAnuncio(); err == nil {
		return a.Token, nil
	}
	return geraToken()
}
