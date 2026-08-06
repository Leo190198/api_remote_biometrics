//go:build windows && 386

package main

// O comparador rodando sob o Gerenciador de Servicos.
//
// Precisa existir porque o binario iniciado por "sc create" sem falar com o SCM
// e morto em 30 segundos por nao responder ao pedido de start. Com isso o WiX
// pode instalar, iniciar, parar e remover o comparador nativamente, e o Windows
// cuida do reinicio em falha - coisas que a tarefa agendada nao dava de graca.
//
// Rodar como servico tambem e o que garante a sessao 0, que e o motivo de tudo:
// e la que o driver ftsjail.sys da FabulaTech nao alcanca o processo.

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
)

const nomeServico = "AgenteBiometriaComparador"

type servicoComparador struct{}

func (s *servicoComparador) Execute(_ []string, pedidos <-chan svc.ChangeRequest, estado chan<- svc.Status) (bool, uint32) {
	const aceita = svc.AcceptStop | svc.AcceptShutdown
	estado <- svc.Status{State: svc.StartPending, WaitHint: 20000}

	pronto := make(chan struct{})
	saida := make(chan int, 1)
	go func() { saida <- rodaComparadorCom(pronto) }()

	// So reporta Running depois que a porta esta aceitando conexoes. Reportar
	// antes faria o SCM dar o servico como de pe enquanto ele ainda podia
	// morrer por porta ocupada, e o erro apareceria como comparacao falhando
	// no meio de um atendimento, longe da causa.
	select {
	case <-pronto:
		estado <- svc.Status{State: svc.Running, Accepts: aceita}
	case code := <-saida:
		// Nem chegou a escutar. Codigo diferente de zero faz o SCM registrar a
		// falha e aplicar a politica de reinicio.
		estado <- svc.Status{State: svc.Stopped}
		return false, uint32(code)
	}

	for {
		select {
		case pedido := <-pedidos:
			switch pedido.Cmd {
			case svc.Interrogate:
				estado <- pedido.CurrentStatus
			case svc.Stop, svc.Shutdown:
				estado <- svc.Status{State: svc.StopPending, WaitHint: 15000}
				cancelaApp()
				select {
				case <-saida:
				case <-time.After(12 * time.Second):
					registraErro("servico: o comparador nao encerrou a tempo")
				}
				return false, 0
			default:
				registraErro("servico: pedido inesperado do SCM: %v", pedido.Cmd)
			}
		case code := <-saida:
			registraErro("servico: o comparador encerrou sozinho com codigo %d", code)
			return false, uint32(code)
		}
	}
}

// rodaComoServico e o ponto de entrada quando o Windows iniciou o processo.
func rodaComoServico() int {
	if err := svc.Run(nomeServico, &servicoComparador{}); err != nil {
		registraErro("servico: %v", err)
		return 1
	}
	return 0
}

// souServico diz se este processo foi iniciado pelo SCM.
//
// A mesma linha de comando serve para os dois casos: iniciado pelo Windows,
// vira servico; rodado no terminal, escreve na tela como antes. Isso mantem o
// "--comparador" util para diagnostico sem uma segunda flag para lembrar.
func souServico() bool {
	ehServico, err := svc.IsWindowsService()
	if err != nil {
		fmt.Println("nao consegui saber se estou sob o SCM:", err)
		return false
	}
	return ehServico
}
