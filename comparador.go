//go:build windows && 386

package main

// Modo comparador: o agente sem leitor, sem bandeja e sem navegador.
//
// Existe porque o NBioAPI_VerifyMatch e inutilizavel no servidor RDP - o gancho
// de API da FabulaTech corrompe a chamada, e isso nao tem conserto do nosso
// lado (ver docs/diagnostico-verifymatch-rdp-2026-07-30.md). Comparar nao
// precisa de leitor nenhum, entao a comparacao sai da estacao e passa a rodar
// ao lado do backend, numa maquina sem o gancho.
//
// O modelo de acesso e outro e por isso nao reaproveita o middleware do modo
// normal. La o chamador e um navegador na mesma sessao Windows, identificado
// por porta de origem e origem autorizada. Aqui o chamador e o proprio backend,
// no mesmo host, autenticado por segredo compartilhado.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

const portaComparadorPadrao = 5150

// rodaComparador sobe o servico e so retorna quando ele cai.
func rodaComparador() int {
	iniciaLogArquivo("comparador.log")

	segredo := os.Getenv("COMPARADOR_TOKEN")
	if len(segredo) < 32 {
		fmt.Fprintln(os.Stderr, "COMPARADOR_TOKEN ausente ou curto demais (minimo 32 caracteres).")
		fmt.Fprintln(os.Stderr, "Sem ele qualquer processo local compararia biometria em nome do sistema.")
		registraErro("comparador: recusou subir sem COMPARADOR_TOKEN utilizavel")
		return 1
	}

	porta := portaComparadorPadrao
	if v := os.Getenv("COMPARADOR_PORTA"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintln(os.Stderr, "COMPARADOR_PORTA invalida:", v)
			return 1
		}
		porta = n
	}

	ctxApp, cancelaApp = context.WithCancel(context.Background())
	defer cancelaApp()

	registraInfo("comparador: iniciando versao=%s commit=%s porta=%d", versao, commit, porta)
	defineDLL(achaDLL())
	registraInfo("comparador: DLL %s", descreveDLL(caminhoDLL()))
	for _, m := range modulosBiometricos() {
		registraInfo("comparador: modulo %s", m)
	}

	go sdkThreadMain()

	mux := http.NewServeMux()
	mux.HandleFunc("/status", comparadorStatus)
	mux.HandleFunc("/comparar", handleComparar)
	mux.HandleFunc("/identificar", handleIdentificar)

	// Escuta so no loopback. O servico nao tem TLS de proposito: um certificado
	// para 127.0.0.1 acrescentaria gestao de chave sem proteger nada que ja nao
	// esteja protegido pelo fato de o trafego nunca sair da maquina. Se algum
	// dia precisar atender outro host, TLS deixa de ser opcional.
	endereco := net.JoinHostPort("127.0.0.1", strconv.Itoa(porta))
	listener, err := net.Listen("tcp", endereco)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nao consegui escutar em", endereco, "-", err)
		registraErro("comparador: escutar em %s: %v", endereco, err)
		return 1
	}

	servidor := &http.Server{
		Handler:           exigeSegredo(segredo, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		// Maior que o contexto de /identificar, senao uma busca legitima em
		// milhares de candidatos e cortada na hora de escrever a resposta.
		WriteTimeout:   5 * time.Minute,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 32 << 10,
		BaseContext:    func(net.Listener) context.Context { return ctxApp },
	}

	fmt.Println("comparador ouvindo em http://" + endereco)
	if err := servidor.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		registraErro("comparador: servidor HTTP: %v", err)
		return 1
	}
	return 0
}

// exigeSegredo confere o Authorization antes de qualquer rota.
//
// Comparacao em tempo constante: o segredo e fixo e vive enquanto o servico
// vive, entao um atacante local poderia descobri-lo byte a byte medindo a
// resposta de um comparador ingenuo.
func exigeSegredo(segredo string, proximo http.Handler) http.Handler {
	esperado := []byte("Bearer " + segredo)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recebido := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(recebido, esperado) != 1 {
			escreveErro(w, http.StatusUnauthorized, "credencial invalida")
			return
		}
		proximo.ServeHTTP(w, r)
	})
}

func comparadorStatus(w http.ResponseWriter, r *http.Request) {
	if !permiteMetodo(w, r, http.MethodGet) {
		return
	}
	escreveJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"modo":    "comparador",
		"versao":  versao,
		"commit":  commit,
		"dll":     descreveDLL(caminhoDLL()),
		"leitor":  false,
		"maquina": nomeMaquina(),
	})
}

func nomeMaquina() string {
	nome, err := os.Hostname()
	if err != nil {
		return ""
	}
	return nome
}
