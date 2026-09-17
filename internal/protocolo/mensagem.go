package protocolo

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Cada mensagem é um objeto JSON em UTF-8 terminado por '\n' (uma mensagem por linha)
const TamanhoMaximoMensagem = 64 * 1024

var ErrMensagemGrande = errors.New("mensagem excede o tamanho máximo permitido")

// Códigos de status das respostas
const (
	StatusOK                 = 200
	StatusCriado             = 201
	StatusRequisicaoInvalida = 400
	StatusNaoAutenticado     = 401
	StatusProibido           = 403
	StatusNaoEncontrado      = 404
	StatusConflito           = 409
	StatusMensagemGrande     = 413
	StatusErroInterno        = 500
)

type MensagemRequisicao struct {
	Acao    string `json:"acao"`
	Payload any    `json:"payload"` //any utilzado para aceitar qualquer estrutura no payload
}

// Lado do servidor: o payload fica cru até sabermos qual struct a ação espera
type RequisicaoRecebida struct {
	Acao    string          `json:"acao"`
	Payload json.RawMessage `json:"payload"`
}

type MensagemResposta struct {
	Status   int    `json:"status"`
	Mensagem string `json:"mensagem"`
	Payload  any    `json:"payload"`
}

// Converte o payload genérico da resposta para a struct desejada
func (r MensagemResposta) Decodificar(destino any) error {
	bytesPayload, err := json.Marshal(r.Payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytesPayload, destino)
}

// Lê uma mensagem (até o '\n') sem deixar um cliente mandar uma linha infinita
func LerMensagem(leitor *bufio.Reader) ([]byte, error) {
	var mensagem []byte
	for {
		parte, err := leitor.ReadSlice('\n')
		mensagem = append(mensagem, parte...)
		if len(mensagem) > TamanhoMaximoMensagem {
			return nil, ErrMensagemGrande
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // linha maior que o buffer: continua lendo
		}
		return mensagem, err
	}
}

// Serializa e envia uma mensagem, com prazo máximo para a escrita
func EnviarMensagem(conn net.Conn, mensagem any, prazo time.Duration) error {
	bytesMsg, err := json.Marshal(mensagem)
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(prazo))
	_, err = conn.Write(append(bytesMsg, '\n'))
	return err
}

// Lado do cliente: envia a requisição e espera a resposta (as duas com prazo)
func Requisitar(conn net.Conn, leitor *bufio.Reader, acao string, payload any, prazo time.Duration) (MensagemResposta, error) {
	var resposta MensagemResposta

	if err := EnviarMensagem(conn, MensagemRequisicao{Acao: acao, Payload: payload}, prazo); err != nil {
		return resposta, err
	}

	conn.SetReadDeadline(time.Now().Add(prazo))
	dados, err := LerMensagem(leitor)
	if err != nil {
		return resposta, err
	}
	if err := json.Unmarshal(dados, &resposta); err != nil {
		return resposta, errors.New("resposta malformada do servidor")
	}
	return resposta, nil
}
