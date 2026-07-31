//go:build windows && 386

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const segredoTeste = "umsegredoqueprecisatermaisde32letras"

func clientePara(t *testing.T, h http.HandlerFunc) *clienteComparador {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &clienteComparador{base: srv.URL, token: segredoTeste, http: srv.Client()}
}

func ctxTeste(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// O comparador so aceita Bearer, e o agente e o unico cliente dele. Se o
// cabecalho sair errado daqui, toda comparacao vira 401 em producao.
func TestDelegacaoEnviaCredencialETemplates(t *testing.T) {
	var recebido compararJSON
	var autorizacao, rota string
	c := clientePara(t, func(w http.ResponseWriter, r *http.Request) {
		autorizacao = r.Header.Get("Authorization")
		rota = r.URL.Path
		corpo, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(corpo, &recebido)
		_, _ = w.Write([]byte("true"))
	})

	confere, err := c.compara(ctxTeste(t), "BENEF", "LIDA")
	if err != nil {
		t.Fatalf("nao esperava erro: %v", err)
	}
	if !confere {
		t.Fatal("esperava que conferisse")
	}
	if autorizacao != "Bearer "+segredoTeste {
		t.Fatalf("credencial errada: %q", autorizacao)
	}
	if rota != "/comparar" {
		t.Fatalf("rota errada: %q", rota)
	}
	if recebido.BiometriaBenef != "BENEF" || recebido.BiometriaLida != "LIDA" {
		t.Fatalf("templates trocados ou perdidos: %+v", recebido)
	}
}

// "Nao conferiu" e um veredito biometrico. Se o comparador falhou, isso precisa
// chegar como erro, senao o sistema le a falha como "nao e a pessoa".
func TestDelegacaoNaoConfundeFalhaComNaoConfere(t *testing.T) {
	c := clientePara(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"erro":"Template adulterado"}`))
	})

	confere, err := c.compara(ctxTeste(t), "BENEF", "LIDA")
	if err == nil {
		t.Fatal("esperava erro quando o comparador recusa")
	}
	if confere {
		t.Fatal("nao pode conferir quando houve erro")
	}
	if !strings.Contains(err.Error(), "Template adulterado") {
		t.Fatalf("o motivo do comparador se perdeu: %v", err)
	}
}

func TestDelegacaoIdentificaDevolveIdEIgnorados(t *testing.T) {
	c := clientePara(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"confere":true,"id":"42","ignorados":["7"]}`))
	})

	res, err := c.identifica(ctxTeste(t), "LIDA", []candidatoJSON{{ID: "42", Template: "T"}})
	if err != nil {
		t.Fatalf("nao esperava erro: %v", err)
	}
	if res.id != "42" {
		t.Fatalf("id errado: %q", res.id)
	}
	if len(res.ignorados) != 1 || res.ignorados[0] != "7" {
		t.Fatalf("ignorados perdidos: %v", res.ignorados)
	}
}

// confere e id tem de contar a mesma historia. Aceitar a divergencia seria
// escolher sozinho entre "e a pessoa" e "nao e".
func TestDelegacaoRecusaRespostaContraditoria(t *testing.T) {
	casos := map[string]string{
		"confere sem id":     `{"ok":true,"confere":true,"id":"","ignorados":[]}`,
		"id sem conferir":    `{"ok":true,"confere":false,"id":"42","ignorados":[]}`,
		"nao concluiu":       `{"ok":false,"confere":true,"id":"42","ignorados":[]}`,
		"corpo nao e o JSON": `nao sou json`,
	}
	for nome, corpo := range casos {
		t.Run(nome, func(t *testing.T) {
			c := clientePara(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(corpo))
			})
			if _, err := c.identifica(ctxTeste(t), "LIDA", nil); err == nil {
				t.Fatal("esperava erro")
			}
		})
	}
}

// Sem as variaveis o agente compara localmente. Ligar a delegacao por engano,
// com token curto ou URL quebrada, deixaria a estacao sem comparar nada.
func TestConfiguraComparadorSoLigaComConfiguracaoUtil(t *testing.T) {
	casos := []struct {
		nome, url, token string
		esperaLigado     bool
	}{
		{"sem url", "", segredoTeste, false},
		{"token curto", "http://127.0.0.1:5150", "curto", false},
		{"sem token", "http://127.0.0.1:5150", "", false},
		{"url sem esquema", "127.0.0.1:5150", segredoTeste, false},
		{"completa", "http://127.0.0.1:5150", segredoTeste, true},
		{"com barra no fim", "http://127.0.0.1:5150/", segredoTeste, true},
	}
	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			t.Setenv("COMPARADOR_URL", caso.url)
			t.Setenv("COMPARADOR_TOKEN", caso.token)
			comparadorRemoto = nil
			t.Cleanup(func() { comparadorRemoto = nil })

			configuraComparador()
			if ligado := comparadorRemoto != nil; ligado != caso.esperaLigado {
				t.Fatalf("ligado=%v, esperava %v", ligado, caso.esperaLigado)
			}
			if caso.esperaLigado && strings.HasSuffix(comparadorRemoto.base, "/") {
				t.Fatalf("a barra final ficou na base: %q", comparadorRemoto.base)
			}
		})
	}
}
