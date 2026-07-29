//go:build windows && 386

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Um FIR texto do NBioBSP e uma sequencia continua de ASCII imprimivel com
// alguns milhares de caracteres.
func templateDeTeste(n int) string {
	return strings.Repeat("A1b2C3d4", n/8+1)[:n]
}

func TestNormalizaTemplateAceitaTemplateRealista(t *testing.T) {
	for _, n := range []int{minTemplate, 512, 2048, maxTemplate} {
		tmpl := templateDeTeste(n)
		if got := normalizaTemplate(tmpl); got != tmpl {
			t.Errorf("normalizaTemplate(%d chars) = %d chars; queria inalterado", n, len(got))
		}
	}
}

func TestNormalizaTemplateCortaEspacosDasBordas(t *testing.T) {
	tmpl := templateDeTeste(512)
	// O caso que motiva o corte: template que voltou do banco com CRLF.
	for _, sujo := range []string{tmpl + "\r\n", "\n" + tmpl, "  " + tmpl + "\t", "\r\n" + tmpl + "\r\n"} {
		if got := normalizaTemplate(sujo); got != tmpl {
			t.Errorf("normalizaTemplate(%q...) nao devolveu o template limpo", sujo[:4])
		}
	}
}

func TestNormalizaTemplateRejeitaEntradaPerigosa(t *testing.T) {
	tmpl := templateDeTeste(512)
	casos := map[string]string{
		"vazio":             "",
		"so espacos":        "        ",
		"curto demais":      templateDeTeste(minTemplate - 1),
		"longo demais":      templateDeTeste(maxTemplate + 1),
		"NUL no meio":       tmpl[:100] + "\x00" + tmpl[100:],
		"NUL no fim":        tmpl + "\x00",
		"espaco no meio":    tmpl[:100] + " " + tmpl[100:],
		"quebra no meio":    tmpl[:100] + "\n" + tmpl[100:],
		"BOM UTF-8":         "\xEF\xBB\xBF" + tmpl,
		"acentuado":         tmpl[:100] + "ç" + tmpl[100:],
		"byte alto":         tmpl[:100] + "\xFF" + tmpl[100:],
		"controle":          tmpl[:100] + "\x1B" + tmpl[100:],
		"base64 com quebra": tmpl[:100] + "\r\n" + tmpl[100:],
	}
	for nome, entrada := range casos {
		if got := normalizaTemplate(entrada); got != "" {
			t.Errorf("%s: normalizaTemplate aceitou (devolveu %d chars)", nome, len(got))
		}
		if templateValido(entrada) {
			t.Errorf("%s: templateValido aceitou", nome)
		}
	}
}

// Truncamento por coluna curta no banco e o gatilho mais provavel em producao.
// A validacao por tamanho nao pega todo truncamento, mas pega o caso em que
// sobra pouca coisa, que e onde o parser da DLL costuma se perder.
func TestNormalizaTemplateRejeitaTruncamentoSevero(t *testing.T) {
	tmpl := templateDeTeste(2048)
	for _, n := range []int{1, 8, 20, minTemplate - 1} {
		if templateValido(tmpl[:n]) {
			t.Errorf("truncado em %d chars foi aceito", n)
		}
	}
}

func TestExigeReinicioSDK(t *testing.T) {
	reinicia := []uint32{0x0001, 0x0101, 0x010A, 0x010B}
	naoReinicia := []uint32{0x0004, 0x0017, 0x0102, 0x0104, 0x0105, 0x0201, 0x0203, 0x0204}

	for _, c := range reinicia {
		if !exigeReinicioSDK(novoErroSDK(c, "teste")) {
			t.Errorf("codigo 0x%04X deveria exigir reinicio do SDK", c)
		}
	}
	for _, c := range naoReinicia {
		if exigeReinicioSDK(novoErroSDK(c, "teste")) {
			t.Errorf("codigo 0x%04X nao deveria exigir reinicio", c)
		}
	}
	if exigeReinicioSDK(errors.New("erro comum")) {
		t.Error("erro sem codigo nao deveria exigir reinicio")
	}
	if exigeReinicioSDK(nil) {
		t.Error("nil nao deveria exigir reinicio")
	}
}

// O codigo precisa sobreviver a %w para o worker conseguir decidir se recria o
// SDK: identifica() embrulha o erro com o id do candidato.
func TestExigeReinicioSDKAtravessaWrap(t *testing.T) {
	envolvido := fmt.Errorf("comparar candidato %q: %w", "abc", novoErroSDK(0x010B, "NBioAPI_VerifyMatch"))
	if !exigeReinicioSDK(envolvido) {
		t.Fatal("erro embrulhado perdeu o codigo do SDK")
	}
	if !strings.Contains(envolvido.Error(), "Dispositivo perdido") {
		t.Fatalf("mensagem = %q; queria a descricao do codigo", envolvido)
	}
}

func TestDescreveErroCodigoDesconhecido(t *testing.T) {
	if got := descreveErro(0x0BAD); !strings.Contains(got, "0x0BAD") {
		t.Fatalf("descreveErro(0x0BAD) = %q; queria o codigo no texto", got)
	}
}

// Memoria nativa e onde ficam os parametros de saida das chamadas a DLL, para
// o endereco nao depender da stack da goroutine.
func TestMemoriaNativaLeEscreve(t *testing.T) {
	p := nativoAloca(8)
	if p == 0 {
		t.Fatal("nativoAloca devolveu 0")
	}
	defer nativoLibera(p)

	// LMEM_ZEROINIT: comeca zerada.
	if v, err := leUint32Nativo(p); err != nil || v != 0 {
		t.Fatalf("leitura inicial = %d, %v; queria 0, nil", v, err)
	}
	if err := escreveUint32Nativo(p+4, 0xDEADBEEF); err != nil {
		t.Fatalf("escreveUint32Nativo: %v", err)
	}
	if v, err := leUint32Nativo(p + 4); err != nil || v != 0xDEADBEEF {
		t.Fatalf("leitura = 0x%X, %v; queria 0xDEADBEEF, nil", v, err)
	}
	// Escrever no offset 4 nao pode contaminar o offset 0.
	if v, err := leUint32Nativo(p); err != nil || v != 0 {
		t.Fatalf("offset 0 = %d, %v; queria 0, nil", v, err)
	}
}

// novaInputFIRNativa monta a NBioAPI_INPUT_FIR que vai para VerifyMatch. Se o
// layout sair errado, a DLL le ponteiro invalido e derruba o processo.
func TestNovaInputFIRNativaMontaLayout(t *testing.T) {
	tmpl := templateDeTeste(64)
	p := novaInputFIRNativa(tmpl)
	if p == 0 {
		t.Fatal("novaInputFIRNativa devolveu 0")
	}
	defer nativoLibera(p)

	forma, err := leUint32Nativo(p)
	if err != nil || forma != formTextEncode {
		t.Fatalf("Form = %d, %v; queria %d", forma, err, formTextEncode)
	}
	ptrTextEncode, err := leUint32Nativo(p + 4)
	if err != nil || uintptr(ptrTextEncode) != p+8 {
		t.Fatalf("ponteiro do TEXTENCODE = 0x%X, %v; queria 0x%X", ptrTextEncode, err, p+8)
	}
	isWide, err := leUint32Nativo(p + 8)
	if err != nil || isWide != 0 {
		t.Fatalf("IsWideChar = %d, %v; queria 0", isWide, err)
	}
	ptrTexto, err := leUint32Nativo(p + 12)
	if err != nil || uintptr(ptrTexto) != p+cabecalhoInputFIR {
		t.Fatalf("ponteiro do texto = 0x%X, %v; queria 0x%X", ptrTexto, err, p+cabecalhoInputFIR)
	}
	// O texto tem que chegar intacto e terminado em NUL.
	lido, err := cStrLimitada(uintptr(ptrTexto), maxTemplate)
	if err != nil || lido != tmpl {
		t.Fatalf("texto = %q, %v; queria %q", lido, err, tmpl)
	}
}

func TestCStrLimitadaPonteiroNulo(t *testing.T) {
	if _, err := cStrLimitada(0, maxTemplate); err == nil {
		t.Fatal("queria erro para ponteiro nulo")
	}
}

func TestCStrLimitadaRespeitaLimite(t *testing.T) {
	tmpl := templateDeTeste(200)
	p := novaInputFIRNativa(tmpl)
	if p == 0 {
		t.Fatal("novaInputFIRNativa devolveu 0")
	}
	defer nativoLibera(p)
	if _, err := cStrLimitada(p+cabecalhoInputFIR, 50); err == nil {
		t.Fatal("queria erro quando o texto excede o limite")
	}
}
