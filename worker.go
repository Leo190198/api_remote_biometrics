//go:build windows && 386

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// A NBioBSP.dll e carregada exclusivamente pelo processo worker. Uma violacao
// de acesso dentro dela nao pode ser recuperada com recover(): o handler de
// excecoes do Go so converte a excecao em panic quando o endereco que falhou
// esta em codigo Go, e para uma falha dentro da DLL o Windows encerra o
// processo. Isolando o SDK, essa falha custa uma requisicao em vez do agente
// inteiro — o servidor HTTP e o icone da bandeja continuam de pe.
const (
	opContar      = "contar"
	opCapturar    = "capturar"
	opComparar    = "comparar"
	opIdentificar = "identificar"
	opEncerrar    = "encerrar"
)

type pedidoWorker struct {
	Op         string          `json:"op"`
	Purpose    uint16          `json:"purpose,omitempty"`
	Timeout    int32           `json:"timeout,omitempty"`
	A          string          `json:"a,omitempty"`
	B          string          `json:"b,omitempty"`
	Candidatos []candidatoJSON `json:"candidatos,omitempty"`
}

type respostaWorker struct {
	OK      bool   `json:"ok"`
	Erro    string `json:"erro,omitempty"`
	Num     uint32 `json:"num,omitempty"`
	Texto   string `json:"texto,omitempty"`
	Confere bool   `json:"confere,omitempty"`
	ID      string `json:"id,omitempty"`
	// Candidatos que nao puderam ser comparados. Precisa atravessar o
	// processo: sem isso o agente nao tem como distinguir "nao e a pessoa" de
	// "o cadastro dessa pessoa esta corrompido".
	Ignorados []string `json:"ignorados,omitempty"`
}

var (
	ole32              = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
)

const coinitApartmentThreaded = 0x2

// workerMain e o processo filho: le pedidos JSON do stdin, executa no SDK e
// devolve a resposta pelo stdout. Sai quando o agente fecha a entrada.
func workerMain() int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	iniciaLogArquivo("worker.log")

	// A captura da NBioBSP usa recursos de janela. Sem inicializar o
	// apartamento STA nesta thread, algumas versoes de driver so retornam
	// quando o timeout estoura.
	if r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); r == 0 || r == 1 {
		defer procCoUninitialize.Call()
	}

	entrada := json.NewDecoder(bufio.NewReaderSize(os.Stdin, 64<<10))
	saida := json.NewEncoder(os.Stdout)
	dll := os.Getenv("BIO_WORKER_DLL")

	var sdk *nbio
	defer func() {
		if sdk != nil {
			_ = sdk.encerra()
		}
	}()

	for {
		var pedido pedidoWorker
		if err := entrada.Decode(&pedido); err != nil {
			return 0
		}
		if pedido.Op == opEncerrar {
			if sdk != nil {
				_ = sdk.encerra()
				sdk = nil
			}
			_ = saida.Encode(respostaWorker{OK: true})
			return 0
		}
		if err := saida.Encode(atendePedido(&sdk, dll, pedido)); err != nil {
			registraErro("worker: responder %s: %v", pedido.Op, err)
			return 1
		}
	}
}

func atendePedido(sdk **nbio, dll string, pedido pedidoWorker) respostaWorker {
	if *sdk == nil {
		if dll == "" {
			return respostaWorker{Erro: "NBioBSP.dll nao encontrada"}
		}
		inst, err := novoSDK(dll)
		if err != nil {
			return respostaWorker{Erro: err.Error()}
		}
		*sdk = inst
	}
	resposta, err := executaOperacao(*sdk, pedido)
	if err != nil {
		if exigeReinicioSDK(err) {
			// NBioBSP so volta a enxergar o leitor depois de Terminate/Init.
			// Descartar a instancia aqui e o que evita o agente ficar
			// devolvendo erro ate alguem reiniciar tudo na mao.
			registraErro("worker: recriando SDK apos %v", err)
			_ = (*sdk).encerra()
			*sdk = nil
		}
		// Preserva o que a operacao ja tinha apurado (candidatos ignorados, por
		// exemplo): OK continua falso, entao o cliente le o erro, mas o
		// diagnostico parcial nao se perde.
		resposta.Erro = err.Error()
		return resposta
	}
	resposta.OK = true
	return resposta
}

func executaOperacao(sdk *nbio, pedido pedidoWorker) (respostaWorker, error) {
	switch pedido.Op {
	case opContar:
		n, err := sdk.contaDispositivos()
		return respostaWorker{Num: n}, err
	case opCapturar:
		texto, err := sdk.capturaTexto(pedido.Purpose, pedido.Timeout)
		return respostaWorker{Texto: texto}, err
	case opComparar:
		confere, err := sdk.comparaTextos(pedido.A, pedido.B)
		return respostaWorker{Confere: confere}, err
	case opIdentificar:
		id, ignorados, err := sdk.identifica(pedido.A, pedido.Candidatos)
		return respostaWorker{ID: id, Confere: id != "", Ignorados: ignorados}, err
	}
	return respostaWorker{}, fmt.Errorf("operacao desconhecida: %q", pedido.Op)
}

const (
	falhasParaEsfriar = 3
	esperaAposFalhas  = 5 * time.Second
)

// clienteWorker implementa sdkAPI conversando com o processo worker. As
// chamadas ja chegam serializadas pela goroutine do SDK; o mutex cobre apenas
// o encerramento concorrente.
type clienteWorker struct {
	mu  sync.Mutex
	dll string

	cmd        *exec.Cmd
	enc        *json.Encoder
	dec        *json.Decoder
	escritaPai *os.File
	leituraPai *os.File

	falhasSeguidas int
	ultimaFalha    time.Time
}

func novoClienteWorker(dll string) (sdkAPI, error) {
	if dll == "" {
		return nil, errors.New("NBioBSP.dll nao encontrada")
	}
	return &clienteWorker{dll: dll}, nil
}

func (c *clienteWorker) sobe() error {
	if c.cmd != nil {
		return nil
	}
	// Se a DLL derruba o worker de forma deterministica (um template
	// corrompido reenviado a cada tentativa), subir um processo novo por
	// requisicao so troca o crash por uma tempestade de spawns.
	if c.falhasSeguidas >= falhasParaEsfriar && time.Since(c.ultimaFalha) < esperaAposFalhas {
		return errors.New("o leitor biometrico falhou varias vezes seguidas; aguarde alguns segundos")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	leituraFilho, escritaPai, err := os.Pipe()
	if err != nil {
		return err
	}
	leituraPai, escritaFilho, err := os.Pipe()
	if err != nil {
		_ = leituraFilho.Close()
		_ = escritaPai.Close()
		return err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "BIO_WORKER=1", "BIO_WORKER_DLL="+c.dll)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// Stdin/Stdout como *os.File faz o exec repassar os handles direto, sem
	// goroutines de copia: assim derruba() pode fechar os canos na hora sem
	// disputar com StdinPipe/StdoutPipe.
	cmd.Stdin = leituraFilho
	cmd.Stdout = escritaFilho
	if err := cmd.Start(); err != nil {
		_ = leituraFilho.Close()
		_ = escritaFilho.Close()
		_ = leituraPai.Close()
		_ = escritaPai.Close()
		c.registraFalha()
		return fmt.Errorf("subir worker do SDK: %w", err)
	}
	_ = leituraFilho.Close()
	_ = escritaFilho.Close()
	c.cmd = cmd
	c.escritaPai = escritaPai
	c.leituraPai = leituraPai
	c.enc = json.NewEncoder(escritaPai)
	c.dec = json.NewDecoder(bufio.NewReaderSize(leituraPai, 64<<10))
	return nil
}

// derruba encerra o worker e devolve como ele saiu, para o log distinguir uma
// saida limpa de uma violacao de acesso (exit status 3221225477 = 0xC0000005).
func (c *clienteWorker) derruba() string {
	if c.cmd == nil {
		return "sem worker"
	}
	if c.escritaPai != nil {
		_ = c.escritaPai.Close()
	}
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	saida := "saida limpa"
	if err := c.cmd.Wait(); err != nil {
		saida = err.Error()
	}
	if c.leituraPai != nil {
		_ = c.leituraPai.Close()
	}
	c.cmd, c.enc, c.dec, c.escritaPai, c.leituraPai = nil, nil, nil, nil, nil
	return saida
}

func (c *clienteWorker) registraFalha() {
	c.falhasSeguidas++
	c.ultimaFalha = time.Now()
}

func (c *clienteWorker) envia(pedido pedidoWorker, limite time.Duration) (respostaWorker, error) {
	if limite < 5*time.Second {
		limite = 5 * time.Second
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.sobe(); err != nil {
		return respostaWorker{}, err
	}
	if err := c.enc.Encode(pedido); err != nil {
		saida := c.derruba()
		c.registraFalha()
		registraErro("worker do SDK caiu ao receber %s: %v (%s)", pedido.Op, err, saida)
		return respostaWorker{}, fmt.Errorf("o leitor biometrico falhou durante %s; tente novamente", pedido.Op)
	}

	dec := c.dec
	recebido := make(chan respostaWorker, 1)
	falha := make(chan error, 1)
	go func() {
		var r respostaWorker
		if err := dec.Decode(&r); err != nil {
			falha <- err
			return
		}
		recebido <- r
	}()

	prazo := time.NewTimer(limite)
	defer prazo.Stop()
	select {
	case r := <-recebido:
		// Erro devolvido pelo SDK e resposta valida: o worker esta saudavel.
		c.falhasSeguidas = 0
		if !r.OK {
			if r.Erro == "" {
				r.Erro = "erro desconhecido no SDK"
			}
			return r, errors.New(r.Erro)
		}
		return r, nil
	case err := <-falha:
		saida := c.derruba()
		c.registraFalha()
		registraErro("worker do SDK morreu durante %s: %v (%s)", pedido.Op, err, saida)
		return respostaWorker{}, fmt.Errorf("o leitor biometrico falhou durante %s; tente novamente", pedido.Op)
	case <-prazo.C:
		saida := c.derruba()
		c.registraFalha()
		registraErro("worker do SDK travou em %s por mais de %s (%s)", pedido.Op, limite, saida)
		return respostaWorker{}, fmt.Errorf("o leitor biometrico nao respondeu em %s", limite)
	}
}

func (c *clienteWorker) contaDispositivos() (uint32, error) {
	r, err := c.envia(pedidoWorker{Op: opContar}, 20*time.Second)
	return r.Num, err
}

func (c *clienteWorker) capturaTexto(purpose uint16, timeoutMs int32) (string, error) {
	limite := time.Duration(timeoutMs)*time.Millisecond + 15*time.Second
	r, err := c.envia(pedidoWorker{Op: opCapturar, Purpose: purpose, Timeout: timeoutMs}, limite)
	return r.Texto, err
}

func (c *clienteWorker) comparaTextos(a, b string) (bool, error) {
	r, err := c.envia(pedidoWorker{Op: opComparar, A: a, B: b}, 30*time.Second)
	return r.Confere, err
}

func (c *clienteWorker) identifica(lida string, candidatos []candidatoJSON) (string, []string, error) {
	limite := 30*time.Second + time.Duration(len(candidatos))*25*time.Millisecond
	if limite > 3*time.Minute {
		limite = 3 * time.Minute
	}
	r, err := c.envia(pedidoWorker{Op: opIdentificar, A: lida, Candidatos: candidatos}, limite)
	return r.ID, r.Ignorados, err
}

func (c *clienteWorker) encerra() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return nil
	}
	if err := c.enc.Encode(pedidoWorker{Op: opEncerrar}); err == nil {
		dec := c.dec
		pronto := make(chan struct{})
		go func() {
			var r respostaWorker
			_ = dec.Decode(&r)
			close(pronto)
		}()
		prazo := time.NewTimer(3 * time.Second)
		defer prazo.Stop()
		select {
		case <-pronto:
		case <-prazo.C:
		}
	}
	c.derruba()
	return nil
}
