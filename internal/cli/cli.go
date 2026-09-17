package cli

import (
	"VaiJunto/internal/modelos"
	"VaiJunto/internal/protocolo"
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Prazo máximo para o servidor responder uma requisição
const PrazoResposta = 15 * time.Second

// Envia a requisição e espera a resposta. Se a conexão cair (servidor fora do ar,
// rede interrompida ou encerrada por inatividade), encerra o cliente com uma mensagem clara.
func Requisitar(conn net.Conn, leitorRede *bufio.Reader, acao string, payload any) protocolo.MensagemResposta {
	resposta, err := protocolo.Requisitar(conn, leitorRede, acao, payload, PrazoResposta)
	if err != nil {
		fmt.Printf("\n Conexão com o servidor perdida (%v). Encerrando o aplicativo.\n", err)
		os.Exit(1)
	}
	return resposta
}

// Quantas notificações chegaram desde a última abertura da central (para o [x] do menu)
func ContarNotificacoes(conn net.Conn, leitorRede *bufio.Reader) int {
	resposta := Requisitar(conn, leitorRede, "CONTAR_NOTIFICACOES", nil)
	var dados struct {
		NaoLidas int `json:"nao_lidas"`
	}
	resposta.Decodificar(&dados)
	return dados.NaoLidas
}

// Abre a central de notificações (o servidor zera o contador ao abrir)
func MostrarCentralNotificacoes(conn net.Conn, leitorRede *bufio.Reader) {
	resposta := Requisitar(conn, leitorRede, "ABRIR_NOTIFICACOES", nil)
	if resposta.Status != 200 {
		fmt.Printf("\n %s\n", resposta.Mensagem)
		return
	}

	var notificacoes []modelos.Notificacao
	resposta.Decodificar(&notificacoes)

	fmt.Println("\n===  CENTRAL DE NOTIFICAÇÕES ===")
	if len(notificacoes) == 0 {
		fmt.Println("Nenhuma notificação por enquanto.")
		return
	}
	for _, n := range notificacoes { // mais novas primeiro
		marca := "   "
		if !n.Lida {
			marca = "NOVA " // chegou desde a última abertura
		}
		fmt.Printf("%s[%s] %s\n", marca, n.Data, n.Mensagem)
	}
}

// Lê uma linha do teclado (encerra o aplicativo se a entrada for fechada, ex.: Ctrl+D)
func LerLinha(leitor *bufio.Reader) string {
	linha, err := leitor.ReadString('\n')
	if err != nil && linha == "" {
		fmt.Println("\nEntrada encerrada. Saindo...")
		os.Exit(0)
	}
	return strings.TrimSpace(linha)
}

func LerDadosUsuario(leitor *bufio.Reader, completo bool) modelos.Usuario {

	var u modelos.Usuario
	fmt.Printf("ID: ")
	u.ID = LerLinha(leitor) //retira espacos vazios e o \n

	fmt.Printf("Senha: ")
	u.Senha = LerLinha(leitor)

	if completo {
		fmt.Print("Nome: ")
		u.Nome = LerLinha(leitor)

		fmt.Print("Tipo (motorista / passageiro): ")
		u.Tipo = strings.ToLower(LerLinha(leitor))
	}
	return u

}

// Lê um número entre min e max, repetindo até o usuário digitar um valor válido
func LerNumero(leitor *bufio.Reader, min, max int) int {
	for {
		numero, err := strconv.Atoi(LerLinha(leitor))
		if err == nil && numero >= min && numero <= max {
			return numero
		}
		fmt.Printf("Opção inválida. Digite um número entre %d e %d: ", min, max)
	}
}

// Igual ao LerNumero, mas Enter vazio devolve o valor padrão
func LerNumeroComPadrao(leitor *bufio.Reader, min, max, padrao int) int {
	for {
		entrada := LerLinha(leitor)
		if entrada == "" {
			return padrao
		}
		numero, err := strconv.Atoi(entrada)
		if err == nil && numero >= min && numero <= max {
			return numero
		}
		fmt.Printf("Opção inválida. Digite um número entre %d e %d (Enter = %d): ", min, max, padrao)
	}
}

// Lê um valor em reais (aceita 45.50 ou 45,50)
func LerValor(leitor *bufio.Reader) float64 {
	for {
		entrada := strings.ReplaceAll(LerLinha(leitor), ",", ".")
		valor, err := strconv.ParseFloat(entrada, 64)
		if err == nil && valor >= 0 && valor <= 10000 {
			return valor
		}
		fmt.Print("Valor inválido. Digite um valor como 45.50: ")
	}
}

// Lê uma cidade atendida, devolvendo o nome oficial
func LerCidade(leitor *bufio.Reader, pergunta string) string {
	for {
		fmt.Print(pergunta)
		if cidade, ok := modelos.NormalizarCidade(LerLinha(leitor)); ok {
			return cidade
		}
		fmt.Printf("Cidade não atendida. Opções: %s\n", strings.Join(modelos.CidadesValidas, ", "))
	}
}

// Lê uma data e hora no formato AAAA-MM-DD HH:MM
func LerDataHora(leitor *bufio.Reader, pergunta string) string {
	for {
		fmt.Print(pergunta)
		entrada := LerLinha(leitor)
		if modelos.FormatoExato(modelos.FormatoDataHora, entrada) {
			return entrada
		}
		fmt.Println("Formato inválido. Exemplo: 2026-09-20 08:30")
	}
}

// Lê uma data no formato AAAA-MM-DD
func LerData(leitor *bufio.Reader, pergunta string) string {
	for {
		fmt.Print(pergunta)
		entrada := LerLinha(leitor)
		if modelos.FormatoExato(modelos.FormatoData, entrada) {
			return entrada
		}
		fmt.Println("Formato inválido. Exemplo: 2026-09-20")
	}
}

// Pergunta S/N e devolve true se a resposta for S
func Confirmar(leitor *bufio.Reader, pergunta string) bool {
	fmt.Print(pergunta + " (S/N): ")
	return strings.ToUpper(LerLinha(leitor)) == "S"
}
