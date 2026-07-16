//go:build windows && 386

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	deviceIDAuto   = 255
	purposeVerify  = 1
	purposeEnroll  = 3
	formTextEncode = 4
)

var erros = map[uint32]string{
	0x0000: "Sucesso",
	0x0001: "Handle invalido",
	0x0004: "Falha na funcao",
	0x0017: "Template invalido",
	0x0101: "Falha ao abrir o dispositivo",
	0x0102: "Nenhum leitor encontrado",
	0x0104: "Dispositivo ja aberto",
	0x0105: "Dispositivo nao aberto",
	0x0109: "Driver com versao antiga",
	0x010A: "Falha ao inicializar o dispositivo",
	0x010B: "Dispositivo perdido ou desconectado",
	0x010C: "Falha ao carregar a DLL do dispositivo",
	0x0201: "Operacao cancelada pelo usuario",
	0x0203: "Tempo esgotado na captura",
	0x0204: "Suspeita de dedo falso",
}

func descreveErro(c uint32) string {
	if s, ok := erros[c]; ok {
		return s
	}
	return fmt.Sprintf("Erro 0x%04X do SDK NBioBSP", c)
}

type firTextEncode struct {
	IsWideChar int32
	TextFIR    uintptr
}

func achaDLL() string {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	candidatos := []string{
		os.Getenv("NBIOBSP_DLL"),
		filepath.Join(dir, "NBioBSP.dll"),
		`C:\Windows\SysWOW64\NBioBSP.dll`,
		`C:\Windows\System32\NBioBSP.dll`,
		`C:\Program Files\Clinic Solution Plano de Saúde\NBioBSP.dll`,
		`C:\Program Files (x86)\Clinic Solution Plano de Saúde\NBioBSP.dll`,
		`C:\Program Files\Clinic Solution Plano de Saude\NBioBSP.dll`,
		`C:\Program Files (x86)\Clinic Solution Plano de Saude\NBioBSP.dll`,
		`C:\Program Files (x86)\NITGEN eNBSP\SDK\Bin\NBioBSP.dll`,
	}
	for _, candidato := range candidatos {
		if candidato != "" {
			if _, err := os.Stat(candidato); err == nil {
				return candidato
			}
		}
	}
	return ""
}

type nbio struct {
	h uintptr

	init, term, open, closeD, enum, capture,
	getText, verify, freeFIR, freeText *syscall.LazyProc
}

func novoSDK(dllPath string) (*nbio, error) {
	dll := syscall.NewLazyDLL(dllPath)
	if err := dll.Load(); err != nil {
		return nil, fmt.Errorf("nao carregou %s: %w", dllPath, err)
	}
	n := &nbio{
		init: dll.NewProc("NBioAPI_Init"), term: dll.NewProc("NBioAPI_Terminate"),
		open: dll.NewProc("NBioAPI_OpenDevice"), closeD: dll.NewProc("NBioAPI_CloseDevice"),
		enum: dll.NewProc("NBioAPI_EnumerateDevice"), capture: dll.NewProc("NBioAPI_Capture"),
		getText: dll.NewProc("NBioAPI_GetTextFIRFromHandle"), verify: dll.NewProc("NBioAPI_VerifyMatch"),
		freeFIR: dll.NewProc("NBioAPI_FreeFIRHandle"), freeText: dll.NewProc("NBioAPI_FreeTextFIR"),
	}
	for nome, proc := range map[string]*syscall.LazyProc{
		"NBioAPI_Init": n.init, "NBioAPI_Terminate": n.term,
		"NBioAPI_OpenDevice": n.open, "NBioAPI_CloseDevice": n.closeD,
		"NBioAPI_EnumerateDevice": n.enum, "NBioAPI_Capture": n.capture,
		"NBioAPI_GetTextFIRFromHandle": n.getText, "NBioAPI_VerifyMatch": n.verify,
		"NBioAPI_FreeFIRHandle": n.freeFIR, "NBioAPI_FreeTextFIR": n.freeText,
	} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("simbolo %s ausente: %w", nome, err)
		}
	}
	var h uintptr
	if r, _, _ := n.init.Call(uintptr(unsafe.Pointer(&h))); r != 0 {
		return nil, errors.New("NBioAPI_Init: " + descreveErro(uint32(r)))
	}
	n.h = h
	return n, nil
}

func (n *nbio) encerra() error {
	if n.h == 0 {
		return nil
	}
	r, _, _ := n.term.Call(n.h)
	n.h = 0
	if r != 0 {
		return errors.New("NBioAPI_Terminate: " + descreveErro(uint32(r)))
	}
	return nil
}

func (n *nbio) abre() error {
	r, _, _ := n.open.Call(n.h, uintptr(deviceIDAuto))
	if r != 0 && r != 0x0104 {
		return errors.New(descreveErro(uint32(r)))
	}
	return nil
}

func (n *nbio) fecha() error {
	r, _, _ := n.closeD.Call(n.h, uintptr(deviceIDAuto))
	if r != 0 && r != 0x0105 {
		return errors.New(descreveErro(uint32(r)))
	}
	return nil
}

func (n *nbio) contaDispositivos() (uint32, error) {
	var count uint32
	var ids uintptr
	r, _, _ := n.enum.Call(n.h, uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&ids)))
	if r != 0 {
		return 0, errors.New(descreveErro(uint32(r)))
	}
	return count, nil
}

func (n *nbio) capturaTexto(purpose uint16, timeoutMs int32) (texto string, err error) {
	if err = n.abre(); err != nil {
		return "", err
	}
	defer func() {
		if fecharErr := n.fecha(); err == nil && fecharErr != nil {
			err = fecharErr
		}
	}()

	var hFIR uintptr
	r, _, _ := n.capture.Call(n.h, uintptr(purpose), uintptr(unsafe.Pointer(&hFIR)),
		uintptr(uint32(timeoutMs)), 0, 0)
	if r != 0 {
		return "", errors.New(descreveErro(uint32(r)))
	}
	defer n.freeFIR.Call(n.h, hFIR)

	var te firTextEncode
	r, _, _ = n.getText.Call(n.h, hFIR, uintptr(unsafe.Pointer(&te)), 0)
	if r != 0 {
		return "", errors.New(descreveErro(uint32(r)))
	}
	defer n.freeText.Call(n.h, uintptr(unsafe.Pointer(&te)))
	return cStrLimitada(te.TextFIR, maxTemplate)
}

const memFixaZerada = 0x0040

func nativoAloca(n int) uintptr {
	p, err := windows.LocalAlloc(memFixaZerada, uint32(n))
	if err != nil {
		return 0
	}
	return p
}

func nativoLibera(p uintptr) {
	if p != 0 {
		_, _ = windows.LocalFree(windows.Handle(p))
	}
}

func novaInputFIRNativa(s string) uintptr {
	tamanho := 16 + len(s) + 1
	p := nativoAloca(tamanho)
	if p == 0 {
		return 0
	}
	dados := make([]byte, tamanho)
	dados[0] = formTextEncode
	binary.LittleEndian.PutUint32(dados[4:8], uint32(p+8))
	binary.LittleEndian.PutUint32(dados[12:16], uint32(p+16))
	copy(dados[16:], s)
	var escritos uintptr
	err := windows.WriteProcessMemory(windows.CurrentProcess(), p, &dados[0], uintptr(tamanho), &escritos)
	if err != nil || escritos != uintptr(tamanho) {
		nativoLibera(p)
		return 0
	}
	return p
}

func templateValido(t string) bool {
	limpo := strings.TrimSpace(t)
	return len(limpo) >= 20 && len(t) <= maxTemplate && !strings.ContainsRune(t, '\x00')
}

func (n *nbio) comparaTextos(a, b string) (bool, error) {
	if !templateValido(a) || !templateValido(b) {
		return false, errors.New("template invalido")
	}
	inA := novaInputFIRNativa(a)
	inB := novaInputFIRNativa(b)
	defer nativoLibera(inA)
	defer nativoLibera(inB)
	if inA == 0 || inB == 0 {
		return false, errors.New("sem memoria nativa")
	}
	var resultado int32
	r, _, _ := n.verify.Call(n.h, inA, inB, uintptr(unsafe.Pointer(&resultado)), 0)
	if r != 0 {
		return false, errors.New(descreveErro(uint32(r)))
	}
	return resultado != 0, nil
}

func (n *nbio) identifica(lida string, candidatos []candidatoJSON) (string, error) {
	if !templateValido(lida) {
		return "", errors.New("template lido invalido")
	}
	inLida := novaInputFIRNativa(lida)
	if inLida == 0 {
		return "", errors.New("sem memoria nativa")
	}
	defer nativoLibera(inLida)
	for _, candidato := range candidatos {
		if !templateValido(candidato.Template) {
			return "", fmt.Errorf("template invalido para candidato %q", candidato.ID)
		}
		inCandidato := novaInputFIRNativa(candidato.Template)
		if inCandidato == 0 {
			return "", errors.New("sem memoria nativa")
		}
		var resultado int32
		r, _, _ := n.verify.Call(n.h, inLida, inCandidato, uintptr(unsafe.Pointer(&resultado)), 0)
		nativoLibera(inCandidato)
		if r != 0 {
			return "", fmt.Errorf("comparar candidato %q: %s", candidato.ID, descreveErro(uint32(r)))
		}
		if resultado != 0 {
			return candidato.ID, nil
		}
	}
	return "", nil
}

func cStrLimitada(p uintptr, max int) (string, error) {
	if p == 0 {
		return "", errors.New("ponteiro de template nulo")
	}
	const tamanhoBloco = 256
	resultado := make([]byte, 0, min(max, 4096))
	bloco := make([]byte, tamanhoBloco)
	for deslocamento := 0; deslocamento < max; {
		tamanho := min(tamanhoBloco, max-deslocamento)
		var lidos uintptr
		err := windows.ReadProcessMemory(windows.CurrentProcess(), p+uintptr(deslocamento), &bloco[0], uintptr(tamanho), &lidos)
		if err == nil && lidos == uintptr(tamanho) {
			for i, b := range bloco[:tamanho] {
				if b == 0 {
					return string(append(resultado, bloco[:i]...)), nil
				}
			}
			resultado = append(resultado, bloco[:tamanho]...)
			deslocamento += tamanho
			continue
		}

		// Um bloco pode cruzar o fim da alocacao do SDK. Nesse caso,
		// le byte a byte ate encontrar o terminador sem ultrapassar a pagina.
		for i := 0; i < tamanho; i++ {
			var b byte
			lidos = 0
			if erroLeitura := windows.ReadProcessMemory(windows.CurrentProcess(), p+uintptr(deslocamento+i), &b, 1, &lidos); erroLeitura != nil || lidos != 1 {
				return "", errors.New("falha ao ler template retornado pelo SDK")
			}
			if b == 0 {
				return string(resultado), nil
			}
			resultado = append(resultado, b)
		}
		deslocamento += tamanho
	}
	return "", errors.New("template do SDK excede o limite")
}
