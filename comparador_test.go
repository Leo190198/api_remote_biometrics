//go:build windows && 386

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func servidorProtegido(segredo string) http.Handler {
	miolo := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chegou"))
	})
	return exigeSegredo(segredo, miolo)
}

// O comparador atende o backend sem checagem de sessao nem de origem, entao o
// segredo e a unica barreira. Um furo aqui deixa qualquer processo local
// comparar biometria em nome do sistema.
func TestComparadorRecusaSemCredencial(t *testing.T) {
	casos := map[string]string{
		"sem header":            "",
		"sem o prefixo Bearer":  "umsegredoqueprecisatermaisde32letras",
		"segredo errado":        "Bearer outrosegredoqueprecisatermais32",
		"prefixo so":            "Bearer ",
		"prefixo de outro tipo": "Basic umsegredoqueprecisatermaisde32letras",
	}
	const segredo = "umsegredoqueprecisatermaisde32letras"
	h := servidorProtegido(segredo)

	for nome, autorizacao := range casos {
		t.Run(nome, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/status", nil)
			if autorizacao != "" {
				req.Header.Set("Authorization", autorizacao)
			}
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("esperava 401, veio %d com corpo %q", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestComparadorAceitaCredencialCorreta(t *testing.T) {
	const segredo = "umsegredoqueprecisatermaisde32letras"
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer "+segredo)
	resp := httptest.NewRecorder()
	servidorProtegido(segredo).ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", resp.Code)
	}
	if resp.Body.String() != "chegou" {
		t.Fatalf("a requisicao nao chegou ao miolo: %q", resp.Body.String())
	}
}

// Um prefixo correto seguido de sobra nao pode passar: comparar so o inicio
// deixaria "Bearer <segredo>qualquercoisa" valer tanto quanto o segredo.
func TestComparadorRecusaCredencialComSobra(t *testing.T) {
	const segredo = "umsegredoqueprecisatermaisde32letras"
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer "+segredo+"sobra")
	resp := httptest.NewRecorder()
	servidorProtegido(segredo).ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401 para credencial com sobra, veio %d", resp.Code)
	}
}
