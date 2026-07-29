//go:build windows && 386

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	deviceIDAuto   = 255
	purposeVerify  = 1
	purposeEnroll  = 3
	formTextEncode = 4

	// NBioAPIERROR_INTERNAL_CHECKSUM_FAIL. O SDK assina o template e confere a
	// assinatura no VerifyMatch; este codigo significa que os bytes recebidos
	// nao sao os que o SDK gerou no cadastro. Nunca e falha de leitor nem de
	// dedo: e o dado que chegou diferente de como saiu.
	erroChecksum = 0x000B
)

// Faixa geral (NBioAPIERROR_BASE_GENERAL = 0), conforme o cabecalho do SDK.
// Sem esses nomes, um template alterado no banco aparecia so como
// "Erro 0x000B do SDK NBioBSP", que nao diz a ninguem o que fazer.
var erros = map[uint32]string{
	0x0000: "Sucesso",
	0x0001: "Handle invalido",
	0x0002: "Ponteiro invalido",
	0x0003: "Tipo invalido",
	0x0004: "Falha na funcao",
	0x0005: "Tipo de estrutura nao corresponde",
	0x0006: "Dado ja processado",
	0x0007: "Falha ao abrir o extrator",
	0x0008: "Falha ao abrir o verificador",
	0x0009: "Falha ao processar o dado",
	0x000A: "O dado precisa estar processado",
	0x000B: "Template adulterado: o checksum interno nao confere com o conteudo",
	0x000C: "Erro no dado criptografado",
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

// erroSDK guarda o codigo devolvido pela DLL, e nao so o texto. Alguns codigos
// (leitor perdido, falha ao inicializar) deixam a instancia inutilizavel ate um
// Terminate/Init, e quem trata o erro precisa distinguir esses de um erro
// rotineiro como "tempo esgotado na captura".
type erroSDK struct {
	codigo uint32
	origem string
}

func novoErroSDK(codigo uint32, origem string) *erroSDK {
	return &erroSDK{codigo: codigo, origem: origem}
}

func (e *erroSDK) Error() string {
	if e.origem == "" {
		return descreveErro(e.codigo)
	}
	return e.origem + ": " + descreveErro(e.codigo)
}

func (e *erroSDK) exigeReinicio() bool {
	switch e.codigo {
	case 0x0001, 0x0101, 0x010A, 0x010B:
		return true
	}
	return false
}

// exigeReinicioSDK responde se vale a pena descartar a instancia do SDK e
// recria-la. Sem isso o agente devolve erro para sempre depois que o usuario
// desconecta e reconecta o leitor, ou depois de reconectar a sessao RDP.
func exigeReinicioSDK(err error) bool {
	var e *erroSDK
	return errors.As(err, &e) && e.exigeReinicio()
}

// Layout de NBioAPI_INPUT_FIR e NBioAPI_FIR_TEXTENCODE em 32 bits, como
// montados por novaInputFIRNativa:
//
//	 0..3   Form (formTextEncode)
//	 4..7   ponteiro para o TEXTENCODE logo abaixo (base+8)
//	 8..11  IsWideChar
//	12..15  ponteiro para o texto logo abaixo (base+16)
//	16..    texto do template terminado em NUL
const (
	tamTextEncode       = 8
	offsetTextEncodePtr = 4
	cabecalhoInputFIR   = 16
)

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
	saida := nativoAloca(4)
	if saida == 0 {
		return nil, errors.New("sem memoria nativa")
	}
	defer nativoLibera(saida)
	if r, _, _ := n.init.Call(saida); r != 0 {
		return nil, novoErroSDK(uint32(r), "NBioAPI_Init")
	}
	h, err := leUint32Nativo(saida)
	if err != nil {
		return nil, err
	}
	n.h = uintptr(h)
	return n, nil
}

func (n *nbio) encerra() error {
	if n.h == 0 {
		return nil
	}
	r, _, _ := n.term.Call(n.h)
	n.h = 0
	if r != 0 {
		return novoErroSDK(uint32(r), "NBioAPI_Terminate")
	}
	return nil
}

func (n *nbio) abre() error {
	r, _, _ := n.open.Call(n.h, uintptr(deviceIDAuto))
	if r != 0 && r != 0x0104 {
		return novoErroSDK(uint32(r), "NBioAPI_OpenDevice")
	}
	return nil
}

func (n *nbio) fecha() error {
	r, _, _ := n.closeD.Call(n.h, uintptr(deviceIDAuto))
	if r != 0 && r != 0x0105 {
		return novoErroSDK(uint32(r), "NBioAPI_CloseDevice")
	}
	return nil
}

func (n *nbio) contaDispositivos() (uint32, error) {
	// 4 bytes para a quantidade + 4 para o ponteiro da lista (que nao usamos).
	saida := nativoAloca(8)
	if saida == 0 {
		return 0, errors.New("sem memoria nativa")
	}
	defer nativoLibera(saida)
	r, _, _ := n.enum.Call(n.h, saida, saida+4)
	if r != 0 {
		return 0, novoErroSDK(uint32(r), "NBioAPI_EnumerateDevice")
	}
	return leUint32Nativo(saida)
}

func (n *nbio) capturaTexto(purpose uint16, timeoutMs int32) (string, error) {
	if err := n.abre(); err != nil {
		return "", err
	}
	defer func() {
		// Falhar ao fechar (leitor removido, redirecionamento RDP oscilando)
		// nao invalida uma captura que ja terminou: registra e segue, senao um
		// template bom vira erro e o usuario tem que encostar o dedo de novo.
		if err := n.fecha(); err != nil {
			registraErro("fechar leitor apos captura: %v", err)
		}
	}()

	saidaFIR := nativoAloca(4)
	if saidaFIR == 0 {
		return "", errors.New("sem memoria nativa")
	}
	defer nativoLibera(saidaFIR)
	r, _, _ := n.capture.Call(n.h, uintptr(purpose), saidaFIR, uintptr(uint32(timeoutMs)), 0, 0)
	if r != 0 {
		return "", novoErroSDK(uint32(r), "NBioAPI_Capture")
	}
	hFIR, err := leUint32Nativo(saidaFIR)
	if err != nil {
		return "", err
	}
	if hFIR == 0 {
		return "", errors.New("NBioAPI_Capture devolveu handle nulo")
	}
	defer n.freeFIR.Call(n.h, uintptr(hFIR))

	texto := nativoAloca(tamTextEncode)
	if texto == 0 {
		return "", errors.New("sem memoria nativa")
	}
	defer nativoLibera(texto)
	r, _, _ = n.getText.Call(n.h, uintptr(hFIR), texto, 0)
	if r != 0 {
		return "", novoErroSDK(uint32(r), "NBioAPI_GetTextFIRFromHandle")
	}
	ponteiro, err := leUint32Nativo(texto + offsetTextEncodePtr)
	if err != nil {
		return "", err
	}
	if ponteiro == 0 {
		return "", errors.New("NBioAPI_GetTextFIRFromHandle devolveu texto nulo")
	}
	// So libera quando o SDK realmente entregou um ponteiro: chamar
	// NBioAPI_FreeTextFIR sobre a struct zerada seria pedir para ele liberar
	// um endereco que ele nunca alocou.
	defer n.freeText.Call(n.h, texto)
	return cStrLimitada(uintptr(ponteiro), maxTemplate)
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

// leUint32Nativo e escreveUint32Nativo acessam a memoria devolvida por
// nativoAloca sem converter uintptr de volta para unsafe.Pointer, no mesmo
// estilo do resto do arquivo.
func leUint32Nativo(p uintptr) (uint32, error) {
	var b [4]byte
	var lidos uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), p, &b[0], 4, &lidos); err != nil || lidos != 4 {
		return 0, errors.New("falha ao ler memoria nativa")
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func escreveUint32Nativo(p uintptr, v uint32) error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	var escritos uintptr
	if err := windows.WriteProcessMemory(windows.CurrentProcess(), p, &b[0], 4, &escritos); err != nil || escritos != 4 {
		return errors.New("falha ao escrever memoria nativa")
	}
	return nil
}

func novaInputFIRNativa(s string) uintptr {
	tamanho := cabecalhoInputFIR + len(s) + 1
	p := nativoAloca(tamanho)
	if p == 0 {
		return 0
	}
	dados := make([]byte, tamanho)
	dados[0] = formTextEncode
	binary.LittleEndian.PutUint32(dados[4:8], uint32(p+8))
	binary.LittleEndian.PutUint32(dados[12:16], uint32(p+cabecalhoInputFIR))
	copy(dados[cabecalhoInputFIR:], s)
	var escritos uintptr
	err := windows.WriteProcessMemory(windows.CurrentProcess(), p, &dados[0], uintptr(tamanho), &escritos)
	if err != nil || escritos != uintptr(tamanho) {
		nativoLibera(p)
		return 0
	}
	return p
}

// normalizaTemplate devolve o template pronto para entregar ao SDK, ou "" se
// ele nao puder ser um FIR texto do NBioBSP.
//
// A checagem precisa ser rigorosa: NBioAPI_VerifyMatch confia nos campos de
// tamanho embutidos no template e le fora da alocacao quando eles nao batem
// com o conteudo. Uma violacao de acesso dentro da DLL nao vira panic do Go —
// o Windows derruba o processo e nenhum recover() roda. Um registro truncado
// por coluna curta demais no banco basta para isso.
//
// O corte de espacos tambem importa: um template que voltou do banco com CRLF
// no fim precisa chegar limpo na DLL, e nao do jeito que veio.
func normalizaTemplate(t string) string {
	limpo := strings.TrimSpace(t)
	if len(limpo) < minTemplate || len(limpo) > maxTemplate {
		return ""
	}
	for i := 0; i < len(limpo); i++ {
		// FIR texto do NBioBSP e ASCII imprimivel continuo. Isso corta NUL,
		// controle, BOM UTF-8, acentuacao e espaco no meio — tudo sinal de
		// dado corrompido, e todos capazes de despencar dentro do parser.
		if c := limpo[i]; c < 0x21 || c > 0x7E {
			return ""
		}
	}
	return limpo
}

func templateValido(t string) bool {
	return normalizaTemplate(t) != ""
}

// impressaoTemplate descreve um template no log sem expor o dado biometrico:
// so o tamanho e um resumo curto. E o bastante para reconhecer o mesmo
// registro entre execucoes e para flagrar truncamento — varios templates
// parando exatamente no mesmo tamanho denunciam coluna curta no banco.
func impressaoTemplate(t string) string {
	soma := sha256.Sum256([]byte(t))
	return fmt.Sprintf("%d bytes sha256:%x", len(t), soma[:6])
}

func (n *nbio) comparaTextos(a, b string) (bool, error) {
	limpoA, limpoB := normalizaTemplate(a), normalizaTemplate(b)
	if limpoA == "" || limpoB == "" {
		return false, errors.New("template invalido")
	}
	inA := novaInputFIRNativa(limpoA)
	defer nativoLibera(inA)
	inB := novaInputFIRNativa(limpoB)
	defer nativoLibera(inB)
	saida := nativoAloca(4)
	defer nativoLibera(saida)
	if inA == 0 || inB == 0 || saida == 0 {
		return false, errors.New("sem memoria nativa")
	}
	r, _, _ := n.verify.Call(n.h, inA, inB, saida, 0)
	if r != 0 {
		if uint32(r) == erroChecksum {
			registraErro("VerifyMatch recusou o par por checksum: cadastrado %s; lido %s",
				impressaoTemplate(limpoA), impressaoTemplate(limpoB))
		}
		return false, novoErroSDK(uint32(r), "NBioAPI_VerifyMatch")
	}
	resultado, err := leUint32Nativo(saida)
	if err != nil {
		return false, err
	}
	return resultado != 0, nil
}

// identifica devolve o id do candidato que conferiu e a lista dos que nao
// puderam ser comparados.
//
// Um registro corrompido no banco nao aborta a busca. Abortar deixava uma
// unica linha podre bloquear a identificacao de todos os beneficiarios da
// lista — e justamente as linhas corrompidas sao as que ninguem sabe que
// existem ate alguem tentar usa-las.
func (n *nbio) identifica(lida string, candidatos []candidatoJSON) (string, []string, error) {
	limpa := normalizaTemplate(lida)
	if limpa == "" {
		return "", nil, errors.New("template lido invalido")
	}
	inLida := novaInputFIRNativa(limpa)
	defer nativoLibera(inLida)
	saida := nativoAloca(4)
	defer nativoLibera(saida)
	if inLida == 0 || saida == 0 {
		return "", nil, errors.New("sem memoria nativa")
	}
	var ignorados []string
	for _, candidato := range candidatos {
		limpo := normalizaTemplate(candidato.Template)
		if limpo == "" {
			registraErro("identificacao: candidato %q tem template ilegivel, ignorado", candidato.ID)
			ignorados = append(ignorados, candidato.ID)
			continue
		}
		inCandidato := novaInputFIRNativa(limpo)
		if inCandidato == 0 {
			return "", ignorados, errors.New("sem memoria nativa")
		}
		// NBioAPI_BOOL pode ocupar menos de 4 bytes: zera antes de cada
		// comparacao para nao herdar o resultado da anterior.
		if err := escreveUint32Nativo(saida, 0); err != nil {
			nativoLibera(inCandidato)
			return "", ignorados, err
		}
		r, _, _ := n.verify.Call(n.h, inLida, inCandidato, saida, 0)
		nativoLibera(inCandidato)
		if r != 0 {
			// Checksum e defeito daquele registro, nao da operacao: pula.
			// Qualquer outro codigo (leitor perdido, handle invalido) e falha
			// do SDK e continuar so repetiria o erro em cada candidato.
			if uint32(r) == erroChecksum {
				registraErro("identificacao: candidato %q com template adulterado (%s), ignorado",
					candidato.ID, impressaoTemplate(limpo))
				ignorados = append(ignorados, candidato.ID)
				continue
			}
			return "", ignorados, fmt.Errorf("comparar candidato %q: %w", candidato.ID, novoErroSDK(uint32(r), "NBioAPI_VerifyMatch"))
		}
		resultado, err := leUint32Nativo(saida)
		if err != nil {
			return "", ignorados, err
		}
		if resultado != 0 {
			return candidato.ID, ignorados, nil
		}
	}
	return "", ignorados, nil
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
