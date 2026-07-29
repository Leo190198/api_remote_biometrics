//go:build windows && 386

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// O processo de teste faz as vezes de worker quando BIO_FAKE_WORKER esta
// definido: clienteWorker sobe os.Executable(), que dentro do teste e o
// proprio binario de teste.
func TestMain(m *testing.M) {
	if modo := os.Getenv("BIO_FAKE_WORKER"); modo != "" {
		os.Exit(workerFalso(modo))
	}
	go sdkThreadMain()
	os.Exit(m.Run())
}

func workerFalso(modo string) int {
	entrada := json.NewDecoder(os.Stdin)
	saida := json.NewEncoder(os.Stdout)
	for {
		var p pedidoWorker
		if err := entrada.Decode(&p); err != nil {
			return 0
		}
		switch modo {
		case "morre":
			// Imita uma violacao de acesso dentro da NBioBSP.dll: o processo
			// some sem responder.
			return 3
		case "trava":
			time.Sleep(time.Hour)
		}
		switch p.Op {
		case opEncerrar:
			_ = saida.Encode(respostaWorker{OK: true})
			return 0
		case opContar:
			_ = saida.Encode(respostaWorker{OK: true, Num: 2})
		case opCapturar:
			_ = saida.Encode(respostaWorker{OK: true, Texto: fmt.Sprintf("captura-%d-%d", p.Purpose, p.Timeout)})
		case opComparar:
			_ = saida.Encode(respostaWorker{OK: true, Confere: p.A == p.B})
		case opIdentificar:
			r := respostaWorker{OK: true}
			for _, c := range p.Candidatos {
				if c.Template == p.A {
					r.ID, r.Confere = c.ID, true
					break
				}
			}
			_ = saida.Encode(r)
		default:
			_ = saida.Encode(respostaWorker{Erro: "operacao desconhecida"})
		}
	}
}

func clienteDeTeste(t *testing.T, modo string) *clienteWorker {
	t.Helper()
	t.Setenv("BIO_FAKE_WORKER", modo)
	c := &clienteWorker{dll: "fake"}
	t.Cleanup(func() { _ = c.encerra() })
	return c
}

func TestClienteWorkerRoundTrip(t *testing.T) {
	c := clienteDeTeste(t, "eco")

	n, err := c.contaDispositivos()
	if err != nil || n != 2 {
		t.Fatalf("contaDispositivos = %d, %v; queria 2, nil", n, err)
	}
	texto, err := c.capturaTexto(purposeEnroll, 30000)
	if err != nil || texto != "captura-3-30000" {
		t.Fatalf("capturaTexto = %q, %v", texto, err)
	}
	confere, err := c.comparaTextos("abc", "abc")
	if err != nil || !confere {
		t.Fatalf("comparaTextos = %v, %v; queria true, nil", confere, err)
	}
	id, err := c.identifica("tmpl-b", []candidatoJSON{
		{ID: "a", Template: "tmpl-a"},
		{ID: "b", Template: "tmpl-b"},
	})
	if err != nil || id != "b" {
		t.Fatalf("identifica = %q, %v; queria \"b\", nil", id, err)
	}
	// O mesmo processo atendeu tudo: nada de subir um worker por chamada.
	if c.cmd == nil {
		t.Fatal("worker deveria continuar vivo entre chamadas")
	}
}

// A correcao principal: o worker morrer nao pode derrubar o agente nem
// travar a chamada, e a proxima requisicao tem que subir um worker novo.
func TestClienteWorkerSobreviveAMorteDoWorker(t *testing.T) {
	c := clienteDeTeste(t, "morre")

	if _, err := c.contaDispositivos(); err == nil {
		t.Fatal("queria erro quando o worker morre sem responder")
	}
	if c.cmd != nil {
		t.Fatal("worker morto deveria ter sido descartado")
	}
	if c.falhasSeguidas != 1 {
		t.Fatalf("falhasSeguidas = %d; queria 1", c.falhasSeguidas)
	}
	// Uma segunda tentativa sobe outro processo em vez de reusar o cadaver.
	if _, err := c.contaDispositivos(); err == nil {
		t.Fatal("queria erro na segunda tentativa tambem")
	}
	if c.falhasSeguidas != 2 {
		t.Fatalf("falhasSeguidas = %d; queria 2", c.falhasSeguidas)
	}
}

// Depois de falhas seguidas o cliente esfria em vez de abrir um processo novo
// por requisicao — um template corrompido reenviado nao vira tempestade de spawn.
func TestClienteWorkerEsfriaAposFalhasSeguidas(t *testing.T) {
	c := clienteDeTeste(t, "morre")

	for i := 0; i < falhasParaEsfriar; i++ {
		if _, err := c.contaDispositivos(); err == nil {
			t.Fatalf("tentativa %d: queria erro", i)
		}
	}
	inicio := time.Now()
	_, err := c.contaDispositivos()
	if err == nil {
		t.Fatal("queria erro durante o periodo de espera")
	}
	if !strings.Contains(err.Error(), "aguarde") {
		t.Fatalf("erro = %q; queria a mensagem de espera", err)
	}
	if decorrido := time.Since(inicio); decorrido > time.Second {
		t.Fatalf("recusa demorou %s; deveria ser imediata", decorrido)
	}
}

func TestClienteWorkerDesisteDeWorkerTravado(t *testing.T) {
	if testing.Short() {
		t.Skip("usa o prazo minimo de 5s")
	}
	c := clienteDeTeste(t, "trava")

	inicio := time.Now()
	_, err := c.envia(pedidoWorker{Op: opContar}, time.Second)
	if err == nil {
		t.Fatal("queria erro de prazo esgotado")
	}
	if !strings.Contains(err.Error(), "nao respondeu") {
		t.Fatalf("erro = %q; queria mensagem de prazo", err)
	}
	// envia() nunca espera menos que 5s, mas tem que devolver logo depois.
	if decorrido := time.Since(inicio); decorrido > 15*time.Second {
		t.Fatalf("desistiu depois de %s; prazo nao foi respeitado", decorrido)
	}
	if c.cmd != nil {
		t.Fatal("worker travado deveria ter sido morto")
	}
}

func TestClienteWorkerPropagaErroDoSDK(t *testing.T) {
	c := clienteDeTeste(t, "eco")

	// Operacao desconhecida devolve OK=false: e erro da chamada, mas o worker
	// segue vivo e nao conta como falha.
	if _, err := c.envia(pedidoWorker{Op: "inexistente"}, 10*time.Second); err == nil {
		t.Fatal("queria erro do SDK")
	}
	if c.cmd == nil {
		t.Fatal("erro de SDK nao deveria derrubar o worker")
	}
	if c.falhasSeguidas != 0 {
		t.Fatalf("falhasSeguidas = %d; erro de SDK nao e falha de worker", c.falhasSeguidas)
	}
}

func TestClienteWorkerEncerraSemDeixarProcesso(t *testing.T) {
	c := clienteDeTeste(t, "eco")
	if _, err := c.contaDispositivos(); err != nil {
		t.Fatalf("contaDispositivos: %v", err)
	}
	if err := c.encerra(); err != nil {
		t.Fatalf("encerra: %v", err)
	}
	if c.cmd != nil {
		t.Fatal("encerra deveria ter limpado o worker")
	}
	if err := c.encerra(); err != nil {
		t.Fatalf("encerra repetido: %v", err)
	}
}

// Uma tarefa que ficou na fila enquanto o cliente desistia nao pode acender o
// leitor depois: era isso que segurava a thread do SDK por mais 15-30s e
// empurrava todas as requisicoes seguintes para o timeout.
func TestNaThreadSDKIgnoraTarefaCancelada(t *testing.T) {
	ocupado := make(chan struct{})
	liberado := make(chan struct{})
	go func() {
		_, _ = naThreadSDK(context.Background(), func() (struct{}, error) {
			close(ocupado)
			<-liberado
			return struct{}{}, nil
		})
	}()
	<-ocupado

	ctx, cancela := context.WithCancel(context.Background())
	var executou atomic.Bool
	pronto := make(chan error, 1)
	go func() {
		_, err := naThreadSDK(ctx, func() (struct{}, error) {
			executou.Store(true)
			return struct{}{}, nil
		})
		pronto <- err
	}()

	// Deixa a tarefa entrar na fila antes de desistir dela.
	time.Sleep(100 * time.Millisecond)
	cancela()
	if err := <-pronto; err == nil {
		t.Fatal("queria erro de contexto cancelado")
	}
	close(liberado)

	// A fila e FIFO: quando esta tarefa roda, a cancelada ja passou.
	if _, err := naThreadSDK(context.Background(), func() (struct{}, error) {
		return struct{}{}, nil
	}); err != nil {
		t.Fatalf("tarefa de sincronizacao: %v", err)
	}
	if executou.Load() {
		t.Fatal("tarefa cancelada rodou mesmo assim")
	}
}
