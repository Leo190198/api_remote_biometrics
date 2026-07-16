/* Cliente do AgenteBiometria.exe para uso direto no sistema web.
 *
 * No primeiro acesso de uma origem ainda desconhecida, o usuario precisa abrir
 * o icone do agente na bandeja e autorizar o site. A descoberta aguarda essa
 * aprovacao por ate 60 segundos. As autorizacoes ficam salvas por usuario.
 *
 * Uso:
 *   await Biometria.garantirConexao()
 *   const template = await Biometria.enroll()
 *   const lido = await Biometria.capturar()
 *   const confere = await Biometria.comparar(template, lido)
 */
(function (global) {
  'use strict'

  var LS_ADDR = 'bio.endereco'
  var SS_TOKEN = 'bio.token'

  function protocolos(preferido) {
    var padrao = location.protocol === 'https:' ? ['https', 'http'] : ['http', 'https']
    if (!preferido || padrao[0] === preferido) return padrao
    return [preferido, padrao[0] === preferido ? padrao[1] : padrao[0]]
  }

  try {
    var h = new URLSearchParams((location.hash || '').replace(/^#/, ''))
    if (h.get('bioPort')) {
      localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + h.get('bioPort'))
    }
    if (h.get('bioToken')) sessionStorage.setItem(SS_TOKEN, h.get('bioToken'))
    if (h.get('bioPort') || h.get('bioToken')) {
      history.replaceState(null, '', location.pathname + location.search)
    }
  } catch (e) {}

  function endereco() {
    return localStorage.getItem(LS_ADDR) || protocolos()[0] + '://localhost:5000'
  }

  function token() {
    return sessionStorage.getItem(SS_TOKEN) || ''
  }

  function cabecalhos(extra) {
    var headers = Object.assign({}, extra || {})
    var atual = token()
    if (atual) headers['X-Bio-Token'] = atual
    return headers
  }

  function erroConexao(e) {
    var err = new Error(
      'Nao foi possivel falar com o agente em ' + endereco() + '. Erro: ' + e.message,
    )
    err.reconectavel = true
    return err
  }

  function erroToken() {
    sessionStorage.removeItem(SS_TOKEN)
    var err = new Error('A conexao com o agente expirou.')
    err.reconectavel = true
    return err
  }

  async function respostaJSON(r) {
    var dados
    try {
      dados = await r.json()
    } catch (e) {
      dados = null
    }
    if (r.status === 401) throw erroToken()
    if (!r.ok) {
      var mensagem = dados && dados.erro ? dados.erro : 'Agente retornou HTTP ' + r.status
      var err = new Error(mensagem)
      err.status = r.status
      throw err
    }
    return dados
  }

  async function requisicao(path, opcoes) {
    opcoes = opcoes || {}
    var url = endereco().replace(/\/+$/, '') + path
    var fetchOpcoes = {
      method: opcoes.method || 'GET',
      headers: cabecalhos(opcoes.headers),
      signal: opcoes.signal,
    }
    if (opcoes.body !== undefined) {
      fetchOpcoes.headers['Content-Type'] = 'application/json'
      fetchOpcoes.body = JSON.stringify(opcoes.body)
    }
    try {
      return await respostaJSON(await fetch(url, fetchOpcoes))
    } catch (e) {
      if (e.reconectavel || e.status) throw e
      throw erroConexao(e)
    }
  }

  async function tentaComReconexao(exec) {
    try {
      return await exec()
    } catch (e) {
      if (!e.reconectavel) throw e
      await Biometria.descobrir()
      if (!token()) throw e
      return exec()
    }
  }

  async function hello(porta, preferido) {
    var lista = protocolos(preferido)
    for (var i = 0; i < lista.length; i++) {
      var ctl = new AbortController()
      var timeout = setTimeout(function () { ctl.abort() }, 700)
      try {
        var r = await fetch(lista[i] + '://localhost:' + porta + '/api/hello', {
          signal: ctl.signal,
        })
        if (r.status === 403) return null
        if (r.status === 200 || r.status === 202) {
          var dados = await r.json()
          if (dados && dados.token) return { proto: lista[i], dados: dados, pendente: false }
          if (r.status === 202 && dados && dados.autorizacao === 'pendente') {
            return { proto: lista[i], dados: dados, pendente: true }
          }
        }
      } catch (e) {
        // Porta fechada, timeout ou certificado ainda nao confiado.
      } finally {
        clearTimeout(timeout)
      }
    }
    return null
  }

  function espera(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms) })
  }

  function conecta(achado) {
    localStorage.setItem(
      LS_ADDR,
      achado.proto + '://localhost:' + achado.dados.porta,
    )
    sessionStorage.setItem(SS_TOKEN, achado.dados.token)
    return { porta: achado.dados.porta, sessao: achado.dados.sessao }
  }

  async function aguardaAutorizacao(achado, limiteMs) {
    localStorage.setItem(
      LS_ADDR,
      achado.proto + '://localhost:' + achado.dados.porta,
    )
    var fim = Date.now() + limiteMs
    while (Date.now() < fim) {
      await espera(1000)
      var atual = await hello(achado.dados.porta, achado.proto)
      if (atual && !atual.pendente) return conecta(atual)
    }
    return null
  }

  var Biometria = {
    configurar: function (opcoes) {
      opcoes = opcoes || {}
      if (opcoes.endereco) {
        localStorage.setItem(LS_ADDR, String(opcoes.endereco).replace(/\/+$/, ''))
      }
      if (opcoes.porta) {
        localStorage.setItem(LS_ADDR, protocolos()[0] + '://localhost:' + opcoes.porta)
      }
      if (opcoes.token) sessionStorage.setItem(SS_TOKEN, opcoes.token)
    },

    descobrir: async function (inicio, fim) {
      inicio = inicio || 5000
      fim = fim || 5099
      for (var p = inicio; p <= fim; p++) {
        var achado = await hello(p)
        if (!achado) continue
        if (!achado.pendente) return conecta(achado)
        return aguardaAutorizacao(achado, 60000)
      }
      return null
    },

    garantirConexao: async function () {
      if (token() && (await this.disponivel())) return true
      await this.descobrir()
      return token() !== ''
    },

    get endereco() {
      return endereco()
    },

    capturar: function () {
      return tentaComReconexao(function () {
        return requisicao('/api/public/v1/captura/Capturar', { method: 'POST' })
      })
    },

    enroll: function () {
      return tentaComReconexao(function () {
        return requisicao('/api/public/v1/captura/Enroll', { method: 'POST' })
      })
    },

    comparar: function (tmplGuardado, tmplLido) {
      return tentaComReconexao(function () {
        return requisicao('/api/public/v1/captura', {
          method: 'POST',
          body: { BiometriaBenef: tmplGuardado, BiometriaLida: tmplLido },
        })
      })
    },

    identificar: async function (tmplLido, candidatos) {
      var r = await tentaComReconexao(function () {
        return requisicao('/api/public/v1/identificar', {
          method: 'POST',
          body: { lida: tmplLido, candidatos: candidatos || [] },
        })
      })
      return { confere: !!r.confere, id: r.id || '' }
    },

    disponivel: async function () {
      try {
        var r = await requisicao('/api/ping')
        return !!(r && r.ok)
      } catch (e) {
        return false
      }
    },
  }

  global.Biometria = Biometria
})(window)
