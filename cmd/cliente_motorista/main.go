package main

import (
	"VaiJunto/internal/cli"
	"VaiJunto/internal/modelos"
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func main() {

	// Tenta pegar o endereço do Docker Compose
	enderecoServidor := os.Getenv("SERVER_URL")

	// Se estiver vazio (rodando localmente sem Docker), usa o padrão
	if enderecoServidor == "" {
		enderecoServidor = "localhost:8081"
	}
	//Faz a conexao com o socket do servidor (desiste se o servidor não responder em 5s)
	conn, err := net.DialTimeout("tcp", enderecoServidor, 5*time.Second)
	if err != nil {
		fmt.Println("Erro ao conectar no servidor", err)
		return
	}
	defer conn.Close()

	leitorTeclado := bufio.NewReader(os.Stdin)
	leitorRede := bufio.NewReader(conn) //cria um leitor com buffer com tamanho padrao de 4096 byte, ao inves de ler byte a byte

	for {
		fmt.Println("\n=== PORTAL DO MOTORISTA ===")
		fmt.Println("1. Cadastrar | 2. Login | 0. Sair")
		fmt.Print("Opção: ")
		opcao := cli.LerLinha(leitorTeclado)

		switch opcao {
		case "1":
			usuario := cli.LerDadosUsuario(leitorTeclado, false)
			fmt.Print("Nome: ")
			usuario.Nome = cli.LerLinha(leitorTeclado)
			usuario.Tipo = modelos.TipoMotorista

			resp := cli.Requisitar(conn, leitorRede, "CADASTRAR", usuario)
			fmt.Printf("\n %s\n", resp.Mensagem)

		case "2":
			usuarioLogin := cli.LerDadosUsuario(leitorTeclado, false)
			usuarioLogin.Tipo = modelos.TipoMotorista

			resp := cli.Requisitar(conn, leitorRede, "LOGIN", usuarioLogin)
			fmt.Printf("\n %s\n", resp.Mensagem)

			// Só entra no menu se o login deu certo
			if resp.Status != 200 {
				continue
			}

			// Usa os dados oficiais devolvidos pelo servidor (com o Nome preenchido)
			var usuarioLogado modelos.Usuario
			resp.Decodificar(&usuarioLogado)

			menuMotorista(conn, leitorTeclado, leitorRede, usuarioLogado)

		case "0":
			return
		}
	}
}

func menuMotorista(conn net.Conn, leitorTeclado, leitorRede *bufio.Reader, usuarioLogado modelos.Usuario) {
	for {
		fmt.Printf("\n--- BEM-VINDO, %s ---\n", strings.ToUpper(usuarioLogado.Nome))
		fmt.Println("1. Publicar nova carona")
		fmt.Println("2. Consultar passageiros confirmados")
		fmt.Println("3. Cancelar carona")
		fmt.Printf("4. Central de notificações [%d]\n", cli.ContarNotificacoes(conn, leitorRede))
		fmt.Println("0. Fazer Logout")
		fmt.Print("Escolha uma ação: ")

		opcao := cli.LerLinha(leitorTeclado)

		if opcao == "1" {
			publicarCarona(conn, leitorTeclado, leitorRede)
		} else if opcao == "2" {
			consultarMinhasCaronas(conn, leitorRede)
		} else if opcao == "3" {
			cancelarCarona(conn, leitorTeclado, leitorRede)
		} else if opcao == "4" {
			cli.MostrarCentralNotificacoes(conn, leitorRede)
		} else if opcao == "0" {
			cli.Requisitar(conn, leitorRede, "LOGOUT", nil) // encerra a sessão no servidor
			fmt.Println("Saindo da conta...")
			break
		}
	}
}

func publicarCarona(conn net.Conn, leitorTeclado, leitorRede *bufio.Reader) {
	var c modelos.Carona
	fmt.Println("\n=== CADASTRAR NOVA VIAGEM ===")
	fmt.Printf("Cidades atendidas: %s\n", strings.Join(modelos.CidadesValidas, ", "))

	c.Origem = cli.LerCidade(leitorTeclado, "Origem: ")
	for {
		c.Destino = cli.LerCidade(leitorTeclado, "Destino final: ")
		if c.Destino != c.Origem {
			break
		}
		fmt.Println("O destino deve ser diferente da origem.")
	}
	c.DataHora = cli.LerDataHora(leitorTeclado, "Data e Hora de saida (AAAA-MM-DD HH:MM): ")

	fmt.Printf("Capacidade do carro - máximo de passageiros (1 a %d): ", modelos.MaxAssentos)
	c.Assentos = cli.LerNumero(leitorTeclado, 1, modelos.MaxAssentos)

	fmt.Print("Deseja adicionar paradas? [1]SIM [2]NÃO: ")
	comParadas := cli.LerNumero(leitorTeclado, 1, 2) == 1

	if comParadas {
		fmt.Println("\n--- MONTAR TRECHOS DA VIAGEM ---")
	}

	// Cada trecho sai de onde o anterior chegou. O preço é cobrado por trecho.
	origemAtual := c.Origem
	saidaAtual := c.DataHora

	for {
		var t modelos.Trecho
		t.Origem = origemAtual
		t.HorarioSaida = saidaAtual

		if comParadas {
			fmt.Printf("\nTrecho saindo de [%s] às %s\n", t.Origem, t.HorarioSaida)
			t.Destino = cli.LerCidade(leitorTeclado, "Para qual cidade este trecho vai? ")
		} else {
			t.Destino = c.Destino
		}

		fmt.Printf("Valor da passagem %s -> %s (ex: 45.50): ", t.Origem, t.Destino)
		t.Valor = cli.LerValor(leitorTeclado)

		// Vagas deste trecho: o motorista pode levar menos gente em um trecho específico.
		// Sem paradas, o único trecho é a viagem inteira: vale a capacidade informada.
		t.VagasTotais = c.Assentos
		if comParadas {
			fmt.Printf("Vagas oferecidas no trecho %s -> %s (1 a %d, Enter = %d): ",
				t.Origem, t.Destino, c.Assentos, c.Assentos)
			t.VagasTotais = cli.LerNumeroComPadrao(leitorTeclado, 1, c.Assentos, c.Assentos)
		}

		t.HorarioChegada = cli.LerDataHora(leitorTeclado,
			fmt.Sprintf("Horário previsto de chegada em %s (AAAA-MM-DD HH:MM): ", t.Destino))

		// Adiciona o trecho na viagem
		c.Trechos = append(c.Trechos, t)

		if t.Destino == c.Destino {
			if comParadas {
				fmt.Println("\n>> Destino final alcançado! Viagem finalizada.")
			}
			break
		}
		if len(c.Trechos) >= modelos.MaxTrechosPorCarona {
			fmt.Printf("\n Limite de %d trechos atingido sem chegar em %s. Carona não publicada.\n", modelos.MaxTrechosPorCarona, c.Destino)
			return
		}

		origemAtual = t.Destino //atualiza a origem para o proximo trecho
		saidaAtual = cli.LerDataHora(leitorTeclado,
			fmt.Sprintf("Horário de saída de %s (AAAA-MM-DD HH:MM): ", origemAtual))
	}

	// O servidor valida tudo (encadeamento dos trechos, horários, valores) e responde
	resp := cli.Requisitar(conn, leitorRede, "PUBLICAR_CARONA", c)
	fmt.Printf("\n[Resposta do Servidor] %s\n", resp.Mensagem)
}

func consultarMinhasCaronas(conn net.Conn, leitorRede *bufio.Reader) []modelos.Carona {
	resp := cli.Requisitar(conn, leitorRede, "CONSULTAR_VIAGENS_MOTORISTA", nil)

	if resp.Status != 200 {
		fmt.Printf("\n %s\n", resp.Mensagem)
		return nil
	}

	var caronas []modelos.Carona
	resp.Decodificar(&caronas)

	fmt.Println("\n===  MINHAS CARONAS ===")
	for i, c := range caronas {
		fmt.Printf("\n%d. %s -> %s | Saída: %s | Capacidade do carro: %d | ID: %s\n", i+1, c.Origem, c.Destino, c.DataHora, c.Assentos, c.ID)
		for _, t := range c.Trechos {
			fmt.Printf("   [%s -> %s] %s -> %s | R$ %.2f | Vagas: %d/%d livres\n",
				t.HorarioSaida, t.HorarioChegada, t.Origem, t.Destino, t.Valor, t.VagasLivres, t.VagasTotais)

			if len(t.Passageiros) == 0 {
				fmt.Println("      Passageiros: nenhum confirmado")
			} else {
				fmt.Printf("      Passageiros: %s\n", strings.Join(t.Passageiros, ", "))
			}
		}
	}
	return caronas
}

func cancelarCarona(conn net.Conn, leitorTeclado, leitorRede *bufio.Reader) {
	caronas := consultarMinhasCaronas(conn, leitorRede)
	if len(caronas) == 0 {
		return
	}

	fmt.Print("\nNúmero da carona que deseja cancelar (0 para voltar): ")
	escolha := cli.LerNumero(leitorTeclado, 0, len(caronas))
	if escolha == 0 {
		return
	}
	carona := caronas[escolha-1]

	fmt.Println("\n ATENÇÃO  Todos os passageiros desta carona terão a viagem INTEIRA cancelada.")
	if !cli.Confirmar(leitorTeclado, fmt.Sprintf("Cancelar a carona %s -> %s?", carona.Origem, carona.Destino)) {
		fmt.Println(">> Cancelamento abortado.")
		return
	}

	resp := cli.Requisitar(conn, leitorRede, "CANCELAR_CARONA", modelos.CancelamentoPayload{ViagemID: carona.ID})
	fmt.Printf("\n>> %s\n", resp.Mensagem)
}
