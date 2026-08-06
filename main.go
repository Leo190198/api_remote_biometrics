//go:build windows && 386

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

//go:embed tray-verde.ico
var iconeVerde []byte

//go:embed tray-vermelho.ico
var iconeVermelho []byte

const (
	// O corpo de /captura leva dois templates: 256 KB ja e folgado. Manter os
	// 2 MB antigos so aumentava o pico de memoria do decodificador JSON, que
	// neste binario 386 divide um espaco de enderecamento de ~2 GB.
	maxCorpoComparar    = 256 << 10
	maxCorpoIdentificar = 16 << 20
	// Um FIR texto do NBioBSP tem alguns KB. O limite antigo de 1 MB era ~500x
	// maior que o real e nao protegia contra nada.
	maxTemplate       = 64 << 10
	minTemplate       = 32
	maxCandidatos     = 5000
	maxRequisicoes    = 32
	maxIdentificacoes = 2
)

var (
	versao = "0.0.0-dev"
	commit = "local"

	porta  int
	token  string
	usaTLS bool

	dllMu   sync.RWMutex
	dllPath string

	sdkInst    sdkAPI
	sdkTasks   = make(chan func(), 16)
	criaSDK    = func(path string) (sdkAPI, error) { return novoClienteWorker(path) }
	origens    *gerenciadorOrigens
	ctxApp     context.Context
	cancelaApp context.CancelFunc
	limiteHTTP = make(chan struct{}, maxRequisicoes)
	// /identificar decodifica corpos de ate 16 MB e o pico do encoding/json
	// chega a varias vezes isso. Com os 32 slots gerais de limiteHTTP, um
	// punhado de requisicoes simultaneas estourava a memoria do processo.
	limiteIdentificar = make(chan struct{}, maxIdentificacoes)
)

func caminhoDLL() string {
	dllMu.RLock()
	defer dllMu.RUnlock()
	return dllPath
}

func defineDLL(p string) {
	dllMu.Lock()
	dllPath = p
	dllMu.Unlock()
}

type sdkAPI interface {
	contaDispositivos() (uint32, error)
	capturaTexto(uint16, int32) (string, error)
	comparaTextos(string, string) (bool, error)
	identifica(string, []candidatoJSON) (string, []string, error)
	encerra() error
}

type resultadoSDK[T any] struct {
	valor T
	err   error
}

func sdkThreadMain() {
	runtime.LockOSThread()
	for fn := range sdkTasks {
		fn()
	}
}

func naThreadSDK[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	resultado := make(chan resultadoSDK[T], 1)
	tarefa := func() {
		res := resultadoSDK[T]{}
		defer func() {
			if v := recover(); v != nil {
				res.err = fmt.Errorf("panic no SDK: %v", v)
				registraErro("%v\n%s", res.err, debug.Stack())
			}
			resultado <- res
		}()
		// Quem pediu ja desistiu enquanto a tarefa esperava na fila. Executar
		// agora acenderia o leitor para ninguem e seguraria a thread do SDK
		// por mais 15 ou 30 segundos, empurrando todo o resto para o timeout.
		if res.err = ctx.Err(); res.err != nil {
			return
		}
		res.valor, res.err = fn()
	}
	select {
	case sdkTasks <- tarefa:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	select {
	case res := <-resultado:
		return res.valor, res.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func ensureSDK() (sdkAPI, error) {
	if sdkInst != nil {
		return sdkInst, nil
	}
	caminho := caminhoDLL()
	if caminho == "" {
		// O driver pode ter sido instalado depois que o agente subiu; sem esta
		// nova busca so um reinicio manual resolveria.
		caminho = achaDLL()
		defineDLL(caminho)
	}
	if caminho == "" {
		return nil, errors.New("NBioBSP.dll nao encontrada")
	}
	inst, err := criaSDK(caminho)
	if err != nil {
		return nil, err
	}
	sdkInst = inst
	return inst, nil
}

func escreveJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		registraErro("escrever resposta JSON: %v", err)
	}
}

func escreveErro(w http.ResponseWriter, code int, mensagem string) {
	escreveJSON(w, code, map[string]any{"ok": false, "erro": mensagem})
}

func permiteMetodo(w http.ResponseWriter, r *http.Request, metodo string) bool {
	if r.Method == metodo {
		return true
	}
	w.Header().Set("Allow", metodo)
	escreveErro(w, http.StatusMethodNotAllowed, "metodo nao permitido")
	return false
}

func origemDoHeader(valor string) (string, error) {
	origem, err := normalizaOrigem(valor)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(valor), origem) {
		return "", errors.New("header Origin invalido")
	}
	return origem, nil
}

func aplicaCORS(w http.ResponseWriter, origem string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origem)
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, X-Bio-Token")
	h.Set("Access-Control-Allow-Private-Network", "true")
	h.Add("Vary", "Origin")
	h.Add("Vary", "Access-Control-Request-Private-Network")
}

func middleware(mux *http.ServeMux, autorizacoes *gerenciadorOrigens) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				registraErro("panic HTTP em %s: %v\n%s", r.URL.Path, v, debug.Stack())
				escreveErro(w, http.StatusInternalServerError, "erro interno")
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")

		origem := ""
		if raw := r.Header.Get("Origin"); raw != "" {
			var err error
			origem, err = origemDoHeader(raw)
			if err != nil {
				escreveErro(w, http.StatusForbidden, "origem invalida")
				return
			}
		}
		hello := r.URL.Path == "/api/hello"
		permitida := origem != "" && autorizacoes.permitida(origem)
		if r.Method == http.MethodOptions {
			if origem == "" || (!hello && !permitida) {
				escreveErro(w, http.StatusForbidden, "origem nao autorizada")
				return
			}
			aplicaCORS(w, origem)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if origem != "" {
			aplicaCORS(w, origem)
			if !hello && !permitida {
				escreveErro(w, http.StatusForbidden, "origem nao autorizada")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !hello {
			tok := r.Header.Get("X-Bio-Token")
			if tok == "" && os.Getenv("BIO_TOKEN_QUERY") == "1" {
				tok = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(token)) != 1 {
				escreveErro(w, http.StatusUnauthorized, "token invalido ou ausente")
				return
			}
		}
		select {
		case limiteHTTP <- struct{}{}:
			defer func() { <-limiteHTTP }()
		case <-r.Context().Done():
			return
		default:
			escreveErro(w, http.StatusServiceUnavailable, "agente ocupado")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	if !permiteMetodo(w, r, http.MethodGet) {
		return
	}
	_, portaStr, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		escreveErro(w, http.StatusInternalServerError, "sem porta de origem")
		return
	}
	p, err := strconv.ParseUint(portaStr, 10, 16)
	if err != nil {
		escreveErro(w, http.StatusInternalServerError, "porta de origem invalida")
		return
	}
	mesma, ok := mesmaSessao(uint16(p), uint16(porta))
	if !ok {
		escreveErro(w, http.StatusServiceUnavailable, "chamador nao identificado")
		return
	}
	if !mesma {
		escreveErro(w, http.StatusForbidden, "sessao diferente")
		return
	}
	origem, err := origemDoHeader(r.Header.Get("Origin"))
	if err != nil {
		escreveErro(w, http.StatusBadRequest, "a descoberta exige um header Origin valido")
		return
	}
	if !origens.solicita(origem) {
		escreveJSON(w, http.StatusAccepted, map[string]any{
			"ok": false, "autorizacao": "pendente", "porta": porta,
			"sessao": os.Getenv("SESSIONNAME"),
		})
		return
	}
	escreveJSON(w, http.StatusOK, map[string]any{
		"ok": true, "porta": porta, "token": token,
		"sessao": os.Getenv("SESSIONNAME"), "https": usaTLS, "versao": versao,
	})
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	if permiteMetodo(w, r, http.MethodGet) {
		escreveJSON(w, http.StatusOK, map[string]any{"ok": true, "versao": versao})
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if !permiteMetodo(w, r, http.MethodGet) {
		return
	}
	n, err := naThreadSDK(r.Context(), func() (uint32, error) {
		s, err := ensureSDK()
		if err != nil {
			return 0, err
		}
		return s.contaDispositivos()
	})
	info := map[string]any{
		"dll": dllOuNada(), "arch": "386", "https": usaTLS,
		"versao": versao, "commit": commit,
	}
	// Onde a comparacao acontece e a primeira pergunta quando um agente confere
	// e o outro nao, e ela nao se responde olhando o log da estacao errada.
	if comparadorRemoto != nil {
		info["comparador"] = comparadorRemoto.base
	} else {
		info["comparador"] = "local"
	}
	if err != nil {
		info["ok"] = false
		info["erro"] = err.Error()
		escreveJSON(w, http.StatusServiceUnavailable, info)
		return
	}
	info["dispositivos"] = n
	info["ok"] = n > 0
	if n == 0 {
		info["erro"] = "Nenhum leitor detectado. Verifique o cabo, driver ou redirecionamento RDP."
	}
	escreveJSON(w, http.StatusOK, info)
}

func handleCapturar(w http.ResponseWriter, r *http.Request) {
	capturaEResponde(w, r, purposeVerify, 15000)
}

func handleEnroll(w http.ResponseWriter, r *http.Request) {
	capturaEResponde(w, r, purposeEnroll, 30000)
}

func capturaEResponde(w http.ResponseWriter, r *http.Request, purpose uint16, timeout int32) {
	if !permiteMetodo(w, r, http.MethodPost) {
		return
	}
	// Precisa ser mais folgado que o prazo do worker (timeout + 15s), senao o
	// contexto morre primeiro e a captura segue rodando la, segurando a thread
	// do SDK para uma resposta que ninguem vai mais ler.
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Millisecond+25*time.Second)
	defer cancel()
	template, err := naThreadSDK(ctx, func() (string, error) {
		s, err := ensureSDK()
		if err != nil {
			return "", err
		}
		return s.capturaTexto(purpose, timeout)
	})
	if err != nil || template == "" {
		if err == nil {
			err = errors.New("SDK devolveu template vazio")
		}
		registraErro("captura: %v", err)
		escreveErro(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	registraInfo("captura: devolveu purpose=%d [%s]", purpose, impressaoTemplate(template))
	escreveJSON(w, http.StatusOK, template)
}

type compararJSON struct {
	BiometriaBenef string `json:"BiometriaBenef"`
	BiometriaLida  string `json:"BiometriaLida"`
}

func decodificaJSON(w http.ResponseWriter, r *http.Request, limite int64, destino any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limite)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(destino); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("apenas um objeto JSON e permitido")
		}
		return err
	}
	return nil
}

func handleComparar(w http.ResponseWriter, r *http.Request) {
	if !permiteMetodo(w, r, http.MethodPost) {
		return
	}
	var body compararJSON
	if err := decodificaJSON(w, r, maxCorpoComparar, &body); err != nil {
		escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
		return
	}
	// Normaliza antes de comparar: o valor validado precisa ser exatamente o
	// que chega na DLL, senao um CRLF vindo do banco passa direto pro parser.
	body.BiometriaBenef = normalizaTemplate(body.BiometriaBenef)
	body.BiometriaLida = normalizaTemplate(body.BiometriaLida)
	if body.BiometriaBenef == "" {
		escreveErro(w, http.StatusBadRequest, "BiometriaBenef nao e um template valido")
		return
	}
	if body.BiometriaLida == "" {
		escreveErro(w, http.StatusBadRequest, "BiometriaLida nao e um template valido")
		return
	}
	// A impressao aqui e no worker permitem seguir o template pelo caminho todo
	// e achar em que ponto os bytes deixam de ser os que o SDK gerou. Comparar
	// com a impressao registrada na captura diz se a alteracao veio do sistema
	// web. Nao carrega dado biometrico: so tamanho e um sha256 curto.
	registraInfo("comparacao: recebeu benef=[%s] lida=[%s]",
		impressaoTemplate(body.BiometriaBenef), impressaoTemplate(body.BiometriaLida))
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var ok bool
	var err error
	if comparadorRemoto != nil {
		// Nao passa pela thread do SDK: aqui nao se toca na DLL, e enfileirar a
		// chamada atras de uma captura de 30 segundos so somaria espera.
		ok, err = comparadorRemoto.compara(ctx, body.BiometriaBenef, body.BiometriaLida)
	} else {
		ok, err = naThreadSDK(ctx, func() (bool, error) {
			s, err := ensureSDK()
			if err != nil {
				return false, err
			}
			return s.comparaTextos(body.BiometriaBenef, body.BiometriaLida)
		})
	}
	if err != nil {
		registraErro("comparacao: %v", err)
		escreveErro(w, http.StatusBadGateway, err.Error())
		return
	}
	// O log registrava o que chegou e nunca o veredito. So que "nao confere" e
	// exatamente o sintoma que se investiga depois, quando a leitura ja passou e
	// nao da para repetir: sem esta linha, um relato de campo nao tem como ser
	// conferido contra o que a maquina realmente respondeu.
	registraInfo("comparacao: benef=[%s] confere=%v",
		impressaoTemplate(body.BiometriaBenef), ok)
	escreveJSON(w, http.StatusOK, ok)
}

type candidatoJSON struct {
	ID       string `json:"id"`
	Template string `json:"template"`
}

// resultadoIdentificacao existe porque naThreadSDK carrega um valor so, e a
// identificacao precisa devolver tambem quem ficou de fora.
type resultadoIdentificacao struct {
	id        string
	ignorados []string
}

func handleIdentificar(w http.ResponseWriter, r *http.Request) {
	if !permiteMetodo(w, r, http.MethodPost) {
		return
	}
	// Reserva a vaga antes de decodificar: o objetivo e limitar quantos corpos
	// de 16 MB existem na memoria ao mesmo tempo, nao so quantas comparacoes
	// rodam em paralelo.
	espera := time.NewTimer(2 * time.Second)
	defer espera.Stop()
	select {
	case limiteIdentificar <- struct{}{}:
		defer func() { <-limiteIdentificar }()
	case <-espera.C:
		escreveErro(w, http.StatusServiceUnavailable, "identificacao ocupada, tente novamente")
		return
	case <-r.Context().Done():
		return
	}

	var body struct {
		Lida       string          `json:"lida"`
		Candidatos []candidatoJSON `json:"candidatos"`
	}
	if err := decodificaJSON(w, r, maxCorpoIdentificar, &body); err != nil {
		escreveErro(w, http.StatusBadRequest, "JSON invalido: "+err.Error())
		return
	}
	body.Lida = normalizaTemplate(body.Lida)
	if body.Lida == "" {
		escreveErro(w, http.StatusBadRequest, "template lido invalido")
		return
	}
	if len(body.Candidatos) == 0 || len(body.Candidatos) > maxCandidatos {
		escreveErro(w, http.StatusBadRequest, "quantidade de candidatos invalida")
		return
	}
	ids := make(map[string]struct{}, len(body.Candidatos))
	validos := make([]candidatoJSON, 0, len(body.Candidatos))
	// Comeca vazia, e nao nula, para o campo sair como [] no JSON e o cliente
	// nao precisar tratar null.
	ignorados := []string{}
	for i := range body.Candidatos {
		c := &body.Candidatos[i]
		// Id ausente ou repetido e defeito de quem chamou, e nao dado
		// corrompido: continua sendo 400, porque o resultado seria ambiguo.
		if c.ID == "" || len(c.ID) > 256 {
			escreveErro(w, http.StatusBadRequest, fmt.Sprintf("candidato invalido na posicao %d", i))
			return
		}
		if _, duplicado := ids[c.ID]; duplicado {
			escreveErro(w, http.StatusBadRequest, "id de candidato duplicado: "+c.ID)
			return
		}
		ids[c.ID] = struct{}{}
		// Ja um cadastro corrompido nao pode bloquear a identificacao dos
		// outros: registra qual e, deixa de fora e segue com o resto da lista.
		c.Template = normalizaTemplate(c.Template)
		if c.Template == "" {
			registraErro("identificacao: template ilegivel no candidato %q (posicao %d), ignorado", c.ID, i)
			ignorados = append(ignorados, c.ID)
			continue
		}
		validos = append(validos, *c)
	}
	if len(validos) == 0 {
		escreveErro(w, http.StatusUnprocessableEntity, "nenhum candidato tem template utilizavel")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	var res resultadoIdentificacao
	var err error
	if comparadorRemoto != nil {
		res, err = comparadorRemoto.identifica(ctx, body.Lida, validos)
	} else {
		res, err = naThreadSDK(ctx, func() (resultadoIdentificacao, error) {
			s, err := ensureSDK()
			if err != nil {
				return resultadoIdentificacao{}, err
			}
			id, pulados, err := s.identifica(body.Lida, validos)
			return resultadoIdentificacao{id: id, ignorados: pulados}, err
		})
	}
	if err != nil {
		registraErro("identificacao: %v", err)
		escreveErro(w, http.StatusBadGateway, err.Error())
		return
	}
	ignorados = append(ignorados, res.ignorados...)
	registraInfo("identificacao: %d candidatos, confere=%v id=%q ignorados=%d",
		len(validos), res.id != "", res.id, len(ignorados))
	if len(ignorados) > 0 && res.id == "" {
		// Sem isso o sistema le "nao confere" e conclui que nao e a pessoa,
		// quando na verdade o cadastro dela nao pode nem ser comparado.
		registraErro("identificacao: nenhum candidato conferiu e %d cadastro(s) foram ignorados: %v",
			len(ignorados), ignorados)
	}
	escreveJSON(w, http.StatusOK, map[string]any{
		"ok": true, "confere": res.id != "", "id": res.id,
		"ignorados": ignorados,
	})
}

func escolheListener() (net.Listener, int, error) {
	if valor := os.Getenv("PORTA"); valor != "" {
		p, err := strconv.Atoi(valor)
		if err != nil || p < 1 || p > 65535 {
			return nil, 0, errors.New("PORTA invalida")
		}
		l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			return nil, 0, fmt.Errorf("abrir PORTA %d: %w", p, err)
		}
		return l, p, nil
	}
	for p := 5000; p <= 5099; p++ {
		l, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			return l, p, nil
		}
	}
	return nil, 0, errors.New("sem porta livre entre 5000 e 5099")
}

func geraToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gerar token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func dllOuNada() string {
	if caminho := caminhoDLL(); caminho != "" {
		return caminho
	}
	return "NAO ENCONTRADA"
}

func escreveConfig() error {
	dir, err := garanteDiretorioDados()
	if err != nil {
		return err
	}
	sessao := os.Getenv("SESSIONNAME")
	if sessao == "" {
		sessao = "console"
	}
	segura := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, sessao)
	if segura == "" {
		segura = "console"
	}
	dados, err := json.Marshal(map[string]any{
		"porta": porta, "token": token, "pid": os.Getpid(), "sessao": sessao,
		"https": usaTLS, "versao": versao,
	})
	if err != nil {
		return err
	}
	return gravaArquivoAtomico(filepath.Join(dir, "agente-"+segura+".json"), append(dados, '\n'), 0o600)
}

func urlSistema() string {
	base := os.Getenv("SISTEMA_URL")
	if base == "" {
		return ""
	}
	separador := "#"
	if strings.Contains(base, "#") {
		separador = "&"
	}
	return fmt.Sprintf("%s%sbioPort=%d&bioToken=%s", base, separador, porta, token)
}

func tituloOrigem(origem string) string {
	const max = 72
	if len(origem) <= max {
		return origem
	}
	return origem[:max-3] + "..."
}

func gerenciaMenuOrigens(ctx context.Context, pai, revogar *systray.MenuItem) {
	type aprovacao struct {
		origem string
		ok     bool
	}
	// systray nao fecha ClickedCh quando o item some do menu, entao cada
	// pendencia ganha um canal proprio para a goroutine de escuta terminar
	// junto com o item em vez de ficar bloqueada ate o fim do processo.
	type pendencia struct {
		item     *systray.MenuItem
		cancelar chan struct{}
	}
	decisoes := make(chan aprovacao, maxOrigensPendentes)
	itens := make(map[string]*pendencia)
	remove := func(origem string) {
		p, existe := itens[origem]
		if !existe {
			return
		}
		delete(itens, origem)
		close(p.cancelar)
		p.item.Remove()
	}
	defer func() {
		for origem := range itens {
			remove(origem)
		}
	}()
	for {
		select {
		case origem := <-origens.novas:
			if _, existe := itens[origem]; existe {
				continue
			}
			p := &pendencia{
				item:     pai.AddSubMenuItem(tituloOrigem(origem), "Autorizar esta origem web"),
				cancelar: make(chan struct{}),
			}
			itens[origem] = p
			pai.Enable()
			systray.SetTooltip("Biometria: autorizacao pendente para " + tituloOrigem(origem))
			go func(origem string, p *pendencia) {
				select {
				case _, ok := <-p.item.ClickedCh:
					select {
					case decisoes <- aprovacao{origem: origem, ok: ok}:
					case <-ctx.Done():
					}
				case <-p.cancelar:
				case <-ctx.Done():
				}
			}(origem, p)
		case decisao := <-decisoes:
			remove(decisao.origem)
			if decisao.ok {
				if err := origens.aprova(decisao.origem); err != nil {
					registraErro("salvar origem autorizada: %v", err)
				} else {
					logger.Printf("origem autorizada: %s", decisao.origem)
				}
			}
			if len(itens) == 0 {
				pai.Disable()
			}
		case _, ok := <-revogar.ClickedCh:
			if !ok {
				return
			}
			if err := origens.revogaTodas(); err != nil {
				registraErro("revogar origens: %v", err)
			} else {
				logger.Printf("origens autorizadas foram revogadas")
			}
			for origem := range itens {
				remove(origem)
			}
			pai.Disable()
		case <-ctx.Done():
			return
		}
	}
}

func onReady() {
	systray.SetIcon(iconeVermelho)
	systray.SetTitle("")
	systray.SetTooltip(fmt.Sprintf("Agente de Biometria - porta %d", porta))
	go monitorLeitor(ctxApp)

	var abrir *systray.MenuItem
	if urlSistema() != "" {
		abrir = systray.AddMenuItem("Abrir sistema", "")
		systray.AddSeparator()
	}
	autorizar := systray.AddMenuItem("Autorizar acesso", "Selecione uma origem pendente")
	autorizar.Disable()
	revogar := systray.AddMenuItem("Revogar sites autorizados", "")
	systray.AddSeparator()
	sair := systray.AddMenuItem("Sair", "")
	go gerenciaMenuOrigens(ctxApp, autorizar, revogar)

	go func() {
		for {
			if abrir == nil {
				select {
				case _, ok := <-sair.ClickedCh:
					if ok {
						systray.Quit()
					}
					return
				case <-ctxApp.Done():
					return
				}
			}
			select {
			case _, ok := <-abrir.ClickedCh:
				if !ok {
					return
				}
				if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", urlSistema()).Start(); err != nil {
					registraErro("abrir sistema: %v", err)
				}
			case _, ok := <-sair.ClickedCh:
				if ok {
					systray.Quit()
				}
				return
			case <-ctxApp.Done():
				return
			}
		}
	}()
}

func monitorLeitor(ctx context.Context) {
	// NBioAPI_EnumerateDevice devolve uma lista que o SDK aloca e nao expoe
	// funcao para liberar. A cada 5 segundos eram ~17 mil chamadas por dia,
	// suficiente para um vazamento pequeno virar falta de memoria num processo
	// de 32 bits. 15 segundos ainda detecta o leitor rapido o bastante.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		conectado, err := naThreadSDK(ctx, func() (bool, error) {
			s, err := ensureSDK()
			if err != nil {
				return false, err
			}
			n, err := s.contaDispositivos()
			return n > 0, err
		})
		if err == nil && conectado {
			systray.SetIcon(iconeVerde)
			systray.SetTooltip(fmt.Sprintf("Biometria - leitor conectado - porta %d", porta))
		} else {
			systray.SetIcon(iconeVermelho)
			systray.SetTooltip(fmt.Sprintf("Biometria - sem leitor - porta %d", porta))
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func encerraSDK(ctx context.Context) {
	_, err := naThreadSDK(ctx, func() (struct{}, error) {
		if sdkInst == nil {
			return struct{}{}, nil
		}
		err := sdkInst.encerra()
		sdkInst = nil
		return struct{}{}, err
	})
	if err != nil {
		registraErro("encerrar SDK: %v", err)
	}
}

func executa() int {
	if len(os.Args) > 1 && os.Args[1] == "--gerar-cert" {
		if err := gerarCert(); err != nil {
			fmt.Fprintln(os.Stderr, "gerar-cert:", err)
			return 1
		}
		return 0
	}
	if len(os.Args) > 1 && os.Args[1] == "--autoteste" {
		return rodaAutoteste()
	}
	if len(os.Args) > 2 && os.Args[1] == "--conferir-contra" {
		return confereContra(os.Args[2])
	}
	if len(os.Args) > 1 && os.Args[1] == "--teste-delegacao" {
		return testeDelegacao()
	}
	// O worker herda BIO_FILHO do agente, entao precisa ser testado antes. Ele
	// tambem nasce de um processo nosso, nunca do SCM, entao passa reto pela
	// deteccao de servico logo abaixo.
	if os.Getenv("BIO_WORKER") == "1" {
		return workerMain()
	}
	// Iniciado pelo Gerenciador de Servicos: so o comparador roda assim, e
	// decidir pela origem do processo em vez dos argumentos evita depender de
	// eles chegarem pelo ImagePath, que e detalhe do instalador.
	if souServico() {
		return rodaComoServico()
	}
	// O comparador vem antes do supervisor e da bandeja: e um servico de
	// servidor, sem sessao Windows, sem leitor e sem interface. No terminal ele
	// escreve na tela, o que mantem o --comparador util para diagnostico.
	if os.Getenv("MODO_COMPARADOR") == "1" || (len(os.Args) > 1 && os.Args[1] == "--comparador") {
		return rodaComparador()
	}
	if os.Getenv("BIO_FILHO") != "1" {
		if !instanciaUnica() {
			return 0
		}
		supervisor()
		return 0
	}

	iniciaLog()
	logger.Printf("iniciando versao=%s commit=%s", versao, commit)
	ctxApp, cancelaApp = context.WithCancel(context.Background())
	defer cancelaApp()
	origens = novoGerenciadorOrigens()
	configuraComparador()
	defineDLL(achaDLL())
	// Qual DLL foi escolhida e a primeira pergunta de qualquer diagnostico, e
	// era a unica que o log nao respondia.
	registraInfo("DLL: %s", descreveDLL(caminhoDLL()))

	listener, p, err := escolheListener()
	if err != nil {
		registraErro("listener: %v", err)
		return 1
	}
	porta = p
	token, err = geraToken()
	if err != nil {
		registraErro("token: %v", err)
		_ = listener.Close()
		return 1
	}
	tlsCfg, err := carregaTLS()
	if err != nil {
		registraErro("TLS desabilitado: %v", err)
	}
	usaTLS = tlsCfg != nil
	if err := escreveConfig(); err != nil {
		registraErro("arquivo de descoberta: %v", err)
	}

	go sdkThreadMain()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hello", handleHello)
	mux.HandleFunc("/api/ping", handlePing)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/public/v1/captura/Capturar", handleCapturar)
	mux.HandleFunc("/api/public/v1/captura/Enroll", handleEnroll)
	mux.HandleFunc("/api/public/v1/captura", handleComparar)
	mux.HandleFunc("/api/public/v1/identificar", handleIdentificar)

	servidor := &http.Server{
		Handler:           middleware(mux, origens),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		// Precisa ser maior que o contexto de /identificar (4 min), senao uma
		// busca legitima em milhares de candidatos e cortada na escrita.
		WriteTimeout:   5 * time.Minute,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 32 << 10,
		BaseContext:    func(net.Listener) context.Context { return ctxApp },
	}
	erroServidor := make(chan error, 1)
	go func() {
		err := servidor.Serve(novaListenerMista(listener, tlsCfg))
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			erroServidor <- err
			registraErro("servidor HTTP: %v", err)
			systray.Quit()
			return
		}
		erroServidor <- nil
	}()

	systray.Run(onReady, cancelaApp)
	cancelaApp()
	ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = servidor.Shutdown(ctxShutdown)
	encerraSDK(ctxShutdown)
	if err := <-erroServidor; err != nil {
		return 1
	}
	logger.Printf("encerrado")
	return 0
}

func main() {
	os.Exit(executa())
}
