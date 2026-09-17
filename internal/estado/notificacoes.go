package estado

import (
	"VaiJunto/internal/modelos"
	"fmt"
	"sync"
	"time"
)

// Quantas notificações cada usuário guarda
const MaxNotificacoesPorUsuario = 50

const ArquivoNotificacoes = "notificacoes.json"

// Central de notificações de um usuário
type RegistroNotificacoes struct {
	sync.Mutex
	Lista []modelos.Notificacao // da mais antiga para a mais nova
}

var Notificacoes sync.Map

// Guarda uma notificação para o usuário.
func notificar(usuarioID, formato string, args ...any) {
	valor, _ := Notificacoes.LoadOrStore(usuarioID, &RegistroNotificacoes{})
	reg := valor.(*RegistroNotificacoes)

	reg.Lock()
	defer reg.Unlock()

	reg.Lista = append(reg.Lista, modelos.Notificacao{
		ID:       fmt.Sprintf("not_%d", time.Now().UnixNano()),
		Data:     agora(),
		Mensagem: fmt.Sprintf(formato, args...),
	})
	if excesso := len(reg.Lista) - MaxNotificacoesPorUsuario; excesso > 0 {
		reg.Lista = append([]modelos.Notificacao(nil), reg.Lista[excesso:]...)
	}
}

func salvarNotificacoes() {
	SalvarSyncMap(&Notificacoes, ArquivoNotificacoes, func(v any) []modelos.Notificacao {
		reg := v.(*RegistroNotificacoes)
		reg.Lock()
		defer reg.Unlock()
		return append([]modelos.Notificacao(nil), reg.Lista...)
	})
}

// Quantas notificações chegaram desde a última vez que o usuário abriu a central
func ContarNotificacoesNaoLidas(usuarioID string) int {
	valor, existe := Notificacoes.Load(usuarioID)
	if !existe {
		return 0
	}
	reg := valor.(*RegistroNotificacoes)
	reg.Lock()
	defer reg.Unlock()

	naoLidas := 0
	for _, n := range reg.Lista {
		if !n.Lida {
			naoLidas++
		}
	}
	return naoLidas
}

// Abre a central: devolve as notificações (mais novas primeiro) como estavam,
// para o cliente destacar as novas, e marca todas como lidas (zera o contador).
func AbrirNotificacoes(usuarioID string) []modelos.Notificacao {
	lista := []modelos.Notificacao{}

	valor, existe := Notificacoes.Load(usuarioID)
	if !existe {
		return lista
	}
	reg := valor.(*RegistroNotificacoes)

	reg.Lock()
	marcou := false
	for i := len(reg.Lista) - 1; i >= 0; i-- {
		lista = append(lista, reg.Lista[i])
		if !reg.Lista[i].Lida {
			reg.Lista[i].Lida = true
			marcou = true
		}
	}
	reg.Unlock()

	if marcou {
		salvarNotificacoes()
	}
	return lista
}

func nomeUsuario(id string) string {
	if valor, existe := Usuarios.Load(id); existe {
		if nome := valor.(modelos.Usuario).Nome; nome != "" {
			return nome
		}
	}
	return id
}

func descreverTrechos(trechos []modelos.Trecho) string {
	if len(trechos) == 0 {
		return ""
	}
	return fmt.Sprintf("%s → %s (saída %s)", trechos[0].Origem, trechos[len(trechos)-1].Destino, trechos[0].HorarioSaida)
}

// Divide uma viagem em pedaços feitos no mesmo carro (um aviso por motorista e pedaço)
func pedacosPorCarona(trechos []modelos.Trecho) [][]modelos.Trecho {
	var pedacos [][]modelos.Trecho
	inicio := 0
	for i := 1; i <= len(trechos); i++ {
		if i == len(trechos) || trechos[i].CaronaID != trechos[inicio].CaronaID {
			pedacos = append(pedacos, trechos[inicio:i])
			inicio = i
		}
	}
	return pedacos
}
