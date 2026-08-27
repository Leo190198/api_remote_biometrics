//go:build windows && 386

package main

import (
	"errors"
	"testing"
)

// errWorkerMorto imita a falha que o clienteWorker devolve quando o processo do
// SDK morre: erro comum, sem codigo do NBioBSP dentro.
var errWorkerMorto = errors.New("o leitor biometrico falhou durante contar; tente novamente")

// zeraEstadoLeitor devolve o estado global ao ponto de partida entre testes.
func zeraEstadoLeitor(t *testing.T) {
	t.Helper()
	leitorMu.Lock()
	leitorConectado, leitorAferido = false, false
	leitorMu.Unlock()
}

func estadoLeitor() (conectado, aferido bool) {
	leitorMu.Lock()
	defer leitorMu.Unlock()
	return leitorConectado, leitorAferido
}

// O estado do leitor so muda quando alguem usa o leitor. Era isso que a
// sondagem de 15 s fazia por conta propria, e foi ela que travou a pilha da
// FabulaTech 1263 vezes em producao para pintar um icone.
func TestEstadoLeitorSoMudaComOperacaoReal(t *testing.T) {
	zeraEstadoLeitor(t)

	if _, aferido := estadoLeitor(); aferido {
		t.Fatal("estado nao deveria nascer aferido")
	}

	anotaContagemLeitor(2, nil)
	if conectado, aferido := estadoLeitor(); !conectado || !aferido {
		t.Fatalf("apos contar 2 leitores: conectado=%v aferido=%v", conectado, aferido)
	}

	anotaContagemLeitor(0, nil)
	if conectado, _ := estadoLeitor(); conectado {
		t.Fatal("contagem zero deveria apagar o leitor")
	}
}

// Dedo falso, tempo esgotado e captura cancelada sao desfechos rotineiros de
// quem acabou de encostar o dedo. Pintar o icone de vermelho ali diria "sem
// leitor" logo depois de o leitor ter funcionado.
func TestErroRotineiroDeCapturaNaoApagaOLeitor(t *testing.T) {
	rotineiros := map[string]uint32{
		"cancelado pelo usuario": 0x0201,
		"tempo esgotado":         0x0203,
		"suspeita de dedo falso": 0x0204,
		"template invalido":      0x0017,
	}
	for nome, codigo := range rotineiros {
		t.Run(nome, func(t *testing.T) {
			zeraEstadoLeitor(t)
			anotaCapturaLeitor(nil)
			if conectado, _ := estadoLeitor(); !conectado {
				t.Fatal("captura boa deveria acender o leitor")
			}
			anotaCapturaLeitor(novoErroSDK(codigo, "NBioAPI_Capture"))
			if conectado, _ := estadoLeitor(); !conectado {
				t.Fatalf("erro 0x%04X nao e ausencia de leitor", codigo)
			}
		})
	}
}

func TestErroDeDispositivoApagaOLeitor(t *testing.T) {
	deDispositivo := map[string]uint32{
		"falha ao abrir":       0x0101,
		"nenhum leitor":        0x0102,
		"falha ao inicializar": 0x010A,
		"leitor perdido":       0x010B,
		"DLL do dispositivo":   0x010C,
	}
	for nome, codigo := range deDispositivo {
		t.Run(nome, func(t *testing.T) {
			zeraEstadoLeitor(t)
			anotaCapturaLeitor(nil)
			anotaCapturaLeitor(novoErroSDK(codigo, "NBioAPI_Capture"))
			if conectado, _ := estadoLeitor(); conectado {
				t.Fatalf("erro 0x%04X deveria apagar o leitor", codigo)
			}
		})
	}
}

// Erro que nao vem do SDK (worker morto, prazo do processo) nao diz nada sobre
// o leitor estar ou nao conectado, entao nao pode mexer no estado.
func TestErroSemCodigoDeSDKNaoMexeNoEstado(t *testing.T) {
	zeraEstadoLeitor(t)
	anotaContagemLeitor(1, nil)

	anotaContagemLeitor(0, errWorkerMorto)
	if conectado, _ := estadoLeitor(); !conectado {
		t.Fatal("falha do worker nao e ausencia de leitor")
	}
	if erroDeDispositivo(errWorkerMorto) {
		t.Fatal("erro sem codigo de SDK nao e erro de dispositivo")
	}
}
