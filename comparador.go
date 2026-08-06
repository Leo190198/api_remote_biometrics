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

func rodaComparador() int { return rodaComparadorCom(nil) }

// rodaComparadorCom sobe o servico e so retorna quando ele cai ou quando
// cancelaApp e chamado. Se pronto nao for nil, e fechado assim que a porta
// esta aceitando conexoes - e o que o servico espera para reportar Running.
func rodaComparadorCom(pronto chan<- struct{}) int {
	// Ao lado do anuncio, e nao no perfil do usuario: como servico, o perfil e
	// o do SYSTEM, e o log sumiria dentro de SysWOW64\config\systemprofile.
	iniciaLogEm(diretorioCompartilhado(), "comparador.log")

	// O segredo deixou de vir so do ambiente: o servico gera o seu e publica
	// junto com a porta, para o agente de cada sessao achar sem depender de
	// variavel de maquina, que uma sessao ja aberta nao enxerga.
	segredo, err := tokenDoComparador()
	if err != nil || len(segredo) < 32 {
		fmt.Fprintln(os.Stderr, "nao consegui obter um token utilizavel para o comparador.")
		fmt.Fprintln(os.Stderr, "Sem ele qualquer processo local compararia biometria em nome do sistema.")
		registraErro("comparador: sem token utilizavel: %v", err)
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

	// Publica so agora: um anuncio gravado antes de a porta abrir mandaria os
	// agentes para um endereco que ainda nao atende.
	if err := publicaAnuncio(porta, segredo); err != nil {
		registraErro("comparador: publicar anuncio: %v", err)
	} else {
		registraInfo("comparador: anunciado em %s", caminhoAnuncio())
	}
	defer removeAnuncio()

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

	// Serve nao retorna quando o contexto cai, entao o desligamento tem de ser
	// pedido explicitamente. Sem isto o SCM esperaria o prazo de parada inteiro
	// e mataria o processo, deixando o anuncio para tras.
	go func() {
		<-ctxApp.Done()
		ctx, cancela := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancela()
		if err := servidor.Shutdown(ctx); err != nil {
			registraErro("comparador: desligar: %v", err)
		}
	}()

	fmt.Println("comparador ouvindo em http://" + endereco)
	registraInfo("comparador: ouvindo em %s", endereco)
	if pronto != nil {
		close(pronto)
	}
	erroServir := servidor.Serve(listener)

	// Encerra o worker antes de sair. Sem isto ele sobrevive ao servico: fica
	// orfao segurando o proprio executavel, e a atualizacao seguinte falha com
	// "arquivo em uso" depois de o servico ja ter parado - ou seja, com o
	// comparador fora do ar. Cada reinicio ainda deixaria mais um para tras.
	//
	// Contexto novo de proposito: ctxApp ja foi cancelado neste ponto, e
	// naThreadSDK desiste de imediato com um contexto morto.
	ctxSaida, cancelaSaida := context.WithTimeout(context.Background(), 10*time.Second)
	encerraSDK(ctxSaida)
	cancelaSaida()

	if erroServir != nil && !errors.Is(erroServir, http.ErrServerClosed) {
		registraErro("comparador: servidor HTTP: %v", erroServir)
		return 1
	}
	registraInfo("comparador: encerrado")
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
