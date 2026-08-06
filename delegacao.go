//go:build windows && 386

package main

// Delegacao da comparacao para o comparador.
//
// Dentro da sessao RDP o driver ftsjail.sys da FabulaTech injeta o ftapihook32
// em todo processo, e com ele no caminho qualquer comparacao morre dentro da
// NBioBSP.dll. Nao adianta trocar de funcao: NBioAPI_VerifyMatch e
// NBioAPI_Verify caem no mesmo endereco
// (docs/diagnostico-verifymatch-rdp-2026-07-30.md). A captura passa ilesa,
// porque e justamente o que o redirecionador foi feito para implementar.
//
// A sessao 0 nao e alcancada pelo driver: medido em 2026-07-31, o comparador
// rodando como servico no MESMO servidor sobe sem nenhum modulo da FabulaTech,
// enquanto o processo da sessao RDP carrega os tres. Dai a divisao de trabalho:
// a sessao RDP captura, porque so ela enxerga o leitor, e o comparador compara,
// porque so ele tem a DLL intacta.
//
// Sem COMPARADOR_URL nada disso liga e o agente compara localmente, como sempre
// fez. E o certo na estacao de trabalho, onde nao existe gancho nenhum, e evita
// que uma configuracao esquecida transforme uma instalacao boa numa que depende
// de um servico que nao existe.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type clienteComparador struct {
	base  string
	token string
	http  *http.Client
}

// comparadorRemoto e nil quando a comparacao e local. O modo comparador nunca
// chama configuraComparador, entao ele nunca delega para si mesmo.
var comparadorRemoto *clienteComparador

func configuraComparador() {
	base := strings.TrimSpace(os.Getenv("COMPARADOR_URL"))
	token := os.Getenv("COMPARADOR_TOKEN")

	// Sem as variaveis, procura o anuncio que o servico publica em ProgramData.
	// E o caminho normal de uma instalacao pelo MSI: nada de ambiente de
	// maquina, que uma sessao ja aberta nem enxergaria. Ausencia do arquivo
	// significa que nao ha comparador nesta maquina, e ai comparar localmente e
	// o comportamento certo, nao uma falha.
	if base == "" || len(token) < 32 {
		a, err := leAnuncio()
		if err != nil {
			if base != "" || token != "" {
				registraErro("COMPARADOR_URL/TOKEN incompletos e sem anuncio utilizavel (%v): a comparacao continua local", err)
			}
			return
		}
		if base == "" {
			base = a.Endereco
		}
		if len(token) < 32 {
			token = a.Token
		}
	}

	base = strings.TrimRight(base, "/")
	endereco, err := url.Parse(base)
	if err != nil || endereco.Host == "" || (endereco.Scheme != "http" && endereco.Scheme != "https") {
		registraErro("endereco do comparador invalido (%q): a comparacao continua local", base)
		return
	}
	// O mesmo minimo do lado do servico. Um token curto aqui so produziria 401
	// em toda comparacao, e o motivo apareceria tarde, ja em producao.
	if len(token) < 32 {
		registraErro("token do comparador ausente ou curto demais: a comparacao continua local")
		return
	}
	comparadorRemoto = &clienteComparador{
		base:  base,
		token: token,
		// Sem Timeout no cliente de proposito: quem manda no prazo e o contexto
		// da requisicao, que ja distingue os 45s da comparacao dos 4 minutos da
		// identificacao. Um teto fixo aqui cortaria a busca longa pela metade.
		http: &http.Client{Transport: &http.Transport{
			MaxIdleConns:        4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
		}},
	}
	registraInfo("comparacao delegada para %s", base)
}

// chama faz um POST JSON e decodifica a resposta em destino.
func (c *clienteComparador) chama(ctx context.Context, rota string, corpo, destino any) error {
	dados, err := json.Marshal(corpo)
	if err != nil {
		return fmt.Errorf("montar pedido ao comparador: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+rota, bytes.NewReader(dados))
	if err != nil {
		return fmt.Errorf("montar pedido ao comparador: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("comparador inacessivel: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	limitado := io.LimitReader(resp.Body, maxCorpoIdentificar)
	if resp.StatusCode != http.StatusOK {
		var falha struct {
			Erro string `json:"erro"`
		}
		_ = json.NewDecoder(limitado).Decode(&falha)
		if falha.Erro != "" {
			return fmt.Errorf("comparador recusou (%d): %s", resp.StatusCode, falha.Erro)
		}
		return fmt.Errorf("comparador respondeu %d", resp.StatusCode)
	}
	if err := json.NewDecoder(limitado).Decode(destino); err != nil {
		return fmt.Errorf("resposta do comparador ilegivel: %w", err)
	}
	return nil
}

func (c *clienteComparador) compara(ctx context.Context, benef, lida string) (bool, error) {
	var confere bool
	err := c.chama(ctx, "/comparar", compararJSON{
		BiometriaBenef: benef,
		BiometriaLida:  lida,
	}, &confere)
	return confere, err
}

func (c *clienteComparador) identifica(ctx context.Context, lida string, candidatos []candidatoJSON) (resultadoIdentificacao, error) {
	var resposta struct {
		OK        bool     `json:"ok"`
		Confere   bool     `json:"confere"`
		ID        string   `json:"id"`
		Ignorados []string `json:"ignorados"`
	}
	corpo := struct {
		Lida       string          `json:"lida"`
		Candidatos []candidatoJSON `json:"candidatos"`
	}{Lida: lida, Candidatos: candidatos}
	if err := c.chama(ctx, "/identificar", corpo, &resposta); err != nil {
		return resultadoIdentificacao{}, err
	}
	if !resposta.OK {
		return resultadoIdentificacao{}, errors.New("comparador nao concluiu a identificacao")
	}
	// Confere e id precisam contar a mesma historia: se divergirem, algo entre
	// os dois processos mexeu na resposta, e adivinhar qual vale daria um
	// veredito biometrico inventado.
	if resposta.Confere != (resposta.ID != "") {
		return resultadoIdentificacao{}, errors.New("resposta do comparador e contraditoria")
	}
	return resultadoIdentificacao{id: resposta.ID, ignorados: resposta.Ignorados}, nil
}
