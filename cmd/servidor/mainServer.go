package main

import (
	"VaiJunto/internal/estado"
	"VaiJunto/internal/modelos"
	"VaiJunto/internal/protocolo"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
	_ "time/tzdata" // fuso horário embutido
)

const (
	// Conexão sem nenhuma mensagem por esse tempo é encerrada (libera a goroutine)
	tempoOcioso = 30 * time.Minute
	// Prazo para entregar uma resposta (cliente que parou de ler não prende o servidor)
	prazoEscrita = 10 * time.Second
)

// Estado de cada conexão: quem fez login nela. Cada conexão tem a sua goroutine,
// então a sessão não é compartilhada e não precisa de trava.
type sessao struct {
	usuario *modelos.Usuario
}

func (s *sessao) descricao() string {
	if s.usuario == nil {
		return "sem login"
	}
	return s.usuario.Tipo + " " + s.usuario.ID
}

// Perfil exigido por ação: "" = pública, "logado" = qualquer usuário logado.
// Ações fora deste mapa não existem no protocolo.
var perfilExigido = map[string]string{
	"CADASTRAR": "",
	"LOGIN":     "",
	"LOGOUT":    "logado",

	"PUBLICAR_CARONA":             modelos.TipoMotorista,
	"CONSULTAR_VIAGENS_MOTORISTA": modelos.TipoMotorista,
	"CANCELAR_CARONA":             modelos.TipoMotorista,

	"BUSCAR_ITINERARIOS":           modelos.TipoPassageiro,
	"ADICIONAR_CARRINHO":           modelos.TipoPassageiro,
	"VER_CARRINHO":                 modelos.TipoPassageiro,
	"EFETIVAR_ITINERARIO":          modelos.TipoPassageiro,
	"CONSULTAR_VIAGENS_PASSAGEIRO": modelos.TipoPassageiro,
	"CANCELAR_ITINERARIO":          modelos.TipoPassageiro,

	"CONTAR_NOTIFICACOES": "logado",
	"ABRIR_NOTIFICACOES":  "logado",
}

func main() {

	errCaronas := estado.CarregarSyncMap(&estado.Caronas, estado.ArquivoCaronas, func(c modelos.Carona) any {
		return &estado.RegistroCarona{Dados: c}
	})
	errUsuarios := estado.CarregarSyncMap(&estado.Usuarios, estado.ArquivoUsuarios, func(u modelos.Usuario) any {
		return u
	})
	errItin := estado.CarregarSyncMap(&estado.Itinerarios, estado.ArquivoItinerarios, func(i modelos.Itinerario) any {
		return i
	})
	errNotif := estado.CarregarSyncMap(&estado.Notificacoes, estado.ArquivoNotificacoes, func(l []modelos.Notificacao) any {
		return &estado.RegistroNotificacoes{Lista: l}
	})
	// Subir com dados pela metade e depois salvar por cima apagaria o que não foi lido
	for _, err := range []error{errCaronas, errUsuarios, errItin, errNotif} {
		if err != nil {
			fmt.Printf(" ERRO AO CARREGAR OS DADOS: %v\n", err)
			os.Exit(1)
		}
	}

	// Deixa vagas e listas de embarque coerentes antes de aceitar conexões
	estado.ReconciliarEstado()

	// Tempo que as vagas ficam presas no carrinho (ex.: VAIJUNTO_TEMPO_RESERVA=30s)
	if valor := os.Getenv("VAIJUNTO_TEMPO_RESERVA"); valor != "" {
		if duracao, err := time.ParseDuration(valor); err == nil && duracao > 0 {
			estado.TempoReservaCarrinho = duracao
		} else {
			fmt.Printf("[AVISO] VAIJUNTO_TEMPO_RESERVA inválido (%q), usando %v\n", valor, estado.TempoReservaCarrinho)
		}
	}
	fmt.Printf("[SISTEMA] Tempo de reserva do carrinho: %v\n", estado.TempoReservaCarrinho)

	porta := os.Getenv("PORTA")
	if porta == "" {
		porta = "8081"
	}

	listener, err := net.Listen("tcp", ":"+porta)
	if err != nil {
		fmt.Println("Erro ao iniciar servidor:", err)
		return
	}
	fmt.Printf("Servidor rodando na porta %s...\n", porta)

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println("[CONEXÃO] Erro ao aceitar conexão:", err)
			continue
		}
		go tratarConexao(conn) // uma goroutine por cliente
	}
}

func tratarConexao(conn net.Conn) {
	defer conn.Close()

	endereco := conn.RemoteAddr().String()
	fmt.Printf("[CONEXÃO] Cliente conectado: %s\n", endereco)

	var s sessao
	leitor := bufio.NewReader(conn)

	for {
		conn.SetReadDeadline(time.Now().Add(tempoOcioso))
		dados, err := protocolo.LerMensagem(leitor) //le o que veio da requisicao do cliente

		if err != nil {
			var erroRede net.Error
			switch {
			case errors.Is(err, protocolo.ErrMensagemGrande):
				// Não dá para achar o início da próxima mensagem: responde e encerra
				protocolo.EnviarMensagem(conn, erroResposta(protocolo.StatusMensagemGrande, err.Error()), prazoEscrita)
				fmt.Printf("[CONEXÃO] %s enviou uma mensagem grande demais\n", endereco)
			case errors.As(err, &erroRede) && erroRede.Timeout():
				fmt.Printf("[CONEXÃO] %s encerrado por inatividade\n", endereco)
			}
			// EOF ou conexão resetada: o cliente saiu (normal ou abruptamente).
			// Um carrinho pendente dele expira sozinho pelo cronômetro.
			fmt.Printf("[CONEXÃO] Cliente desconectado: %s (%s)\n", endereco, s.descricao())
			return
		}

		if len(dados) <= 1 { // linha vazia
			continue
		}

		resposta := processarRequisicao(&s, dados)

		if err := protocolo.EnviarMensagem(conn, resposta, prazoEscrita); err != nil {
			fmt.Printf("[CONEXÃO] Falha ao responder %s: %v\n", endereco, err)
			return
		}
	}
}

func erroResposta(status int, mensagem string) protocolo.MensagemResposta {
	return protocolo.MensagemResposta{Status: status, Mensagem: mensagem}
}

// Resposta de erro com o status que a regra de negócio indicou
func respostaDoErro(err error, padrao int) protocolo.MensagemResposta {
	return erroResposta(estado.StatusDoErro(err, padrao), err.Error())
}

// Converte o payload cru na struct esperada pela ação
func decodificar(payload json.RawMessage, destino any) error {
	if len(payload) == 0 || string(payload) == "null" {
		return errors.New("payload ausente")
	}
	if err := json.Unmarshal(payload, destino); err != nil {
		return errors.New("payload com formato inválido para esta ação")
	}
	return nil
}

func processarRequisicao(s *sessao, dados []byte) (resposta protocolo.MensagemResposta) {
	// Última linha de defesa: um erro inesperado derruba só esta requisição, nunca o servidor
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[ERRO] Pânico ao processar requisição: %v\n", r)
			resposta = erroResposta(protocolo.StatusErroInterno, "erro interno ao processar a requisição")
		}
	}()

	var req protocolo.RequisicaoRecebida //converte o json para a struct do protocolo
	if err := json.Unmarshal(dados, &req); err != nil {
		return erroResposta(protocolo.StatusRequisicaoInvalida, "mensagem malformada: JSON inválido")
	}

	perfil, existe := perfilExigido[req.Acao]
	if !existe {
		return erroResposta(protocolo.StatusRequisicaoInvalida, "Ação desconhecida: "+req.Acao)
	}
	if perfil != "" {
		if s.usuario == nil {
			return erroResposta(protocolo.StatusNaoAutenticado, "faça login para usar esta ação")
		}
		if perfil != "logado" && s.usuario.Tipo != perfil {
			return erroResposta(protocolo.StatusProibido, "ação permitida apenas para "+perfil)
		}
	}

	// A partir daqui, o ID de quem pede vem SEMPRE da sessão, nunca do payload
	switch req.Acao {
	case "CADASTRAR":
		var usuarioRecebido modelos.Usuario
		if err := decodificar(req.Payload, &usuarioRecebido); err != nil {
			return erroResposta(protocolo.StatusRequisicaoInvalida, err.Error())
		}
		if err := estado.CadastrarUsuario(usuarioRecebido); err != nil { //passa para a função os dados vindo do json
			return respostaDoErro(err, protocolo.StatusRequisicaoInvalida)
		}
		fmt.Printf("Novo %s cadastrado: %s\n", usuarioRecebido.Tipo, usuarioRecebido.ID)
		return protocolo.MensagemResposta{Status: protocolo.StatusCriado, Mensagem: "Usuário cadastrado com sucesso"}

	case "LOGIN":
		var usuarioRecebido modelos.Usuario
		if err := decodificar(req.Payload, &usuarioRecebido); err != nil {
			return erroResposta(protocolo.StatusRequisicaoInvalida, err.Error())
		}
		usuarioLogado, err := estado.ValidarLogin(usuarioRecebido.ID, usuarioRecebido.Senha, usuarioRecebido.Tipo)
		if err != nil {
			return respostaDoErro(err, protocolo.StatusNaoAutenticado)
		}
		s.usuario = &usuarioLogado // a conexão passa a ser deste usuário
		fmt.Printf("Login bem-sucedido: %s\n", usuarioLogado.ID)
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: "Bem-Vindo " + usuarioLogado.Nome, Payload: usuarioLogado}

	case "LOGOUT":
		s.usuario = nil
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: "Sessão encerrada"}

	case "PUBLICAR_CARONA":
		var caronaRecebida modelos.Carona
		if err := decodificar(req.Payload, &caronaRecebida); err != nil {
			return erroResposta(protocolo.StatusRequisicaoInvalida, err.Error())
		}
		carona, err := estado.PublicarCarona(s.usuario.ID, caronaRecebida)
		if err != nil {
			return respostaDoErro(err, protocolo.StatusRequisicaoInvalida)
		}
		return protocolo.MensagemResposta{Status: protocolo.StatusCriado, Mensagem: "Carona publicada com sucesso!", Payload: carona}

	case "CONSULTAR_VIAGENS_MOTORISTA":
		caronas := estado.ListarCaronasMotorista(s.usuario.ID)
		if len(caronas) == 0 {
			return erroResposta(protocolo.StatusNaoEncontrado, "Você ainda não publicou nenhuma carona.")
		}
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: "Caronas encontradas", Payload: caronas}

	case "CANCELAR_CARONA":
		var pedido modelos.CancelamentoPayload
		if err := decodificar(req.Payload, &pedido); err != nil || pedido.ViagemID == "" {
			return erroResposta(protocolo.StatusRequisicaoInvalida, "informe o viagem_id da carona")
		}
		afetados, err := estado.CancelarCaronaPeloMotorista(s.usuario.ID, pedido.ViagemID)
		if err != nil {
			return respostaDoErro(err, protocolo.StatusRequisicaoInvalida)
		}
		fmt.Printf("Motorista %s cancelou a carona %s (%d itinerários afetados)\n", s.usuario.ID, pedido.ViagemID, afetados)
		return protocolo.MensagemResposta{
			Status:   protocolo.StatusOK,
			Mensagem: fmt.Sprintf(" Carona cancelada! %d viagem(ns) de passageiros foram canceladas.", afetados),
			Payload:  map[string]int{"itinerarios_cancelados": afetados},
		}

	//Busca os itinerários possíveis (diretos ou com baldeação)
	case "BUSCAR_ITINERARIOS":
		var filtro modelos.FiltroBusca
		if err := decodificar(req.Payload, &filtro); err != nil {
			return erroResposta(protocolo.StatusRequisicaoInvalida, err.Error())
		}
		itinerarios, err := estado.BuscarItinerarios(filtro)
		if err != nil {
			return respostaDoErro(err, protocolo.StatusRequisicaoInvalida)
		}
		if len(itinerarios) == 0 {
			return erroResposta(protocolo.StatusNaoEncontrado, "Nenhum itinerário com vagas encontrado.")
		}
		return protocolo.MensagemResposta{
			Status:   protocolo.StatusOK,
			Mensagem: fmt.Sprintf("%d itinerário(s) encontrado(s)", len(itinerarios)),
			Payload:  itinerarios,
		}

	case "ADICIONAR_CARRINHO":
		var carrinhoReq modelos.CarrinhoPayload
		if err := decodificar(req.Payload, &carrinhoReq); err != nil {
			return erroResposta(protocolo.StatusRequisicaoInvalida, err.Error())
		}
		err := estado.ReservarVagasTemporariamente(s.usuario.ID, carrinhoReq.Trechos, carrinhoReq.Continuacao)
		if err != nil {
			return respostaDoErro(err, protocolo.StatusConflito)
		}
		return protocolo.MensagemResposta{
			Status: protocolo.StatusOK,
			Mensagem: fmt.Sprintf("Vaga reservada! Você tem %.0f segundos para escolher o próximo trecho ou confirmar a compra.",
				estado.TempoReservaCarrinho.Seconds()),
		}

	case "VER_CARRINHO":
		trechos, err := estado.VerCarrinho(s.usuario.ID)
		if err != nil {
			return respostaDoErro(err, protocolo.StatusNaoEncontrado)
		}
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: "Carrinho recuperado", Payload: trechos}

	case "EFETIVAR_ITINERARIO":
		if err := estado.EfetivarItinerario(s.usuario.ID); err != nil {
			return respostaDoErro(err, protocolo.StatusConflito)
		}
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: " Compra confirmada com sucesso! Passagens emitidas."}

	case "CONSULTAR_VIAGENS_PASSAGEIRO":
		itinerarios := estado.ListarItinerariosPassageiro(s.usuario.ID)
		if len(itinerarios) == 0 {
			return erroResposta(protocolo.StatusNaoEncontrado, "Você ainda não possui viagens confirmadas.")
		}
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: "Viagens encontradas", Payload: itinerarios}

	case "CONTAR_NOTIFICACOES":
		return protocolo.MensagemResposta{
			Status:   protocolo.StatusOK,
			Mensagem: "Notificações não lidas",
			Payload:  map[string]int{"nao_lidas": estado.ContarNotificacoesNaoLidas(s.usuario.ID)},
		}

	case "ABRIR_NOTIFICACOES":
		notificacoes := estado.AbrirNotificacoes(s.usuario.ID)
		mensagem := fmt.Sprintf("%d notificação(ões)", len(notificacoes))
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: mensagem, Payload: notificacoes}

	case "CANCELAR_ITINERARIO":
		var pedido modelos.CancelamentoPayload
		if err := decodificar(req.Payload, &pedido); err != nil || pedido.ViagemID == "" {
			return erroResposta(protocolo.StatusRequisicaoInvalida, "informe o viagem_id do itinerário")
		}
		if err := estado.CancelarItinerarioPassageiro(s.usuario.ID, pedido.ViagemID); err != nil {
			return respostaDoErro(err, protocolo.StatusRequisicaoInvalida)
		}
		fmt.Printf("Passageiro %s cancelou o itinerário %s\n", s.usuario.ID, pedido.ViagemID)
		return protocolo.MensagemResposta{Status: protocolo.StatusOK, Mensagem: "Viagem cancelada! As vagas foram devolvidas aos motoristas."}
	}

	return erroResposta(protocolo.StatusRequisicaoInvalida, "Ação desconhecida: "+req.Acao)
}
