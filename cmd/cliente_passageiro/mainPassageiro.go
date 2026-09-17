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
	// Desiste se o servidor não responder em 5s
	conn, err := net.DialTimeout("tcp", enderecoServidor, 5*time.Second)
	if err != nil {
		fmt.Println("Erro ao conectar no servidor:", err)
		return
	}
	defer conn.Close()

	leitorTeclado := bufio.NewReader(os.Stdin)
	// Um único leitor de rede para a conexão inteira: criar um bufio.Reader novo a cada
	// resposta pode descartar bytes que ficaram no buffer do leitor anterior
	leitorRede := bufio.NewReader(conn)

	for {
		fmt.Println("\n=== PORTAL DO PASSAGEIRO ===")
		fmt.Println("1. Cadastrar | 2. Login | 0. Sair")
		fmt.Print("Opção: ")
		opcao := cli.LerLinha(leitorTeclado)

		if opcao == "1" {
			usuario := cli.LerDadosUsuario(leitorTeclado, false)
			fmt.Print("Nome: ")
			usuario.Nome = cli.LerLinha(leitorTeclado)
			usuario.Tipo = modelos.TipoPassageiro

			resp := cli.Requisitar(conn, leitorRede, "CADASTRAR", usuario)
			fmt.Printf("\n %s\n", resp.Mensagem)

		} else if opcao == "2" {
			usuarioLogin := cli.LerDadosUsuario(leitorTeclado, false)
			usuarioLogin.Tipo = modelos.TipoPassageiro

			resp := cli.Requisitar(conn, leitorRede, "LOGIN", usuarioLogin)
			fmt.Printf("\n %s\n", resp.Mensagem)

			// Só entra no menu se o login deu certo
			if resp.Status != 200 {
				continue
			}

			// Usa os dados oficiais devolvidos pelo servidor (com o Nome preenchido)
			var usuarioLogado modelos.Usuario
			resp.Decodificar(&usuarioLogado)

			menuPassageiro(conn, leitorTeclado, leitorRede, usuarioLogado)

		} else if opcao == "0" {
			break
		}
	}
}

func menuPassageiro(conn net.Conn, leitorTeclado, leitorRede *bufio.Reader, usuario modelos.Usuario) {
	for {
		fmt.Printf("\n--- BEM-VINDO, %s ---\n", strings.ToUpper(usuario.Nome))
		fmt.Println("1. Buscar itinerários")
		fmt.Println("2. Abrir Carrinho")
		fmt.Println("3. Consultar minhas viagens")
		fmt.Println("4. Cancelar viagem")
		fmt.Printf("5. Central de notificações [%d]\n", cli.ContarNotificacoes(conn, leitorRede))
		fmt.Println("0. Fazer Logout")
		fmt.Print("Escolha uma ação: ")

		opcao := cli.LerLinha(leitorTeclado)

		if opcao == "1" {
			iniciarBuscaPassagem(conn, leitorTeclado, leitorRede)
		} else if opcao == "2" {
			abrirCarrinho(conn, leitorTeclado, leitorRede)
		} else if opcao == "3" {
			consultarMinhasViagens(conn, leitorRede)
		} else if opcao == "4" {
			cancelarViagem(conn, leitorTeclado, leitorRede)
		} else if opcao == "5" {
			cli.MostrarCentralNotificacoes(conn, leitorRede)
		} else if opcao == "0" {
			cli.Requisitar(conn, leitorRede, "LOGOUT", nil) // encerra a sessão no servidor
			fmt.Println("Saindo da conta...")
			break
		}
	}
}

// Uma escolha do passageiro: o pedaço da viagem feito no mesmo carro, a partir da cidade atual
type opcaoTrecho struct {
	trechos []modelos.Trecho // o que será reservado agora (trechos seguidos da mesma carona)
	melhor  []modelos.Trecho // o melhor itinerário completo que começa com essa escolha
}

// Agrupa os itinerários pelo primeiro carro. O servidor já manda os itinerários ordenados,
// então o primeiro de cada grupo é o melhor que começa com aquele carro.
func agruparPorPrimeiroCarro(itinerarios [][]modelos.Trecho) []opcaoTrecho {
	var opcoes []opcaoTrecho
	vistos := make(map[string]bool)

	for _, itinerario := range itinerarios {
		n := 1
		for n < len(itinerario) && itinerario[n].CaronaID == itinerario[0].CaronaID {
			n++
		}

		chave := ""
		for _, t := range itinerario[:n] {
			chave += t.ID + "|"
		}
		if vistos[chave] {
			continue
		}
		vistos[chave] = true
		opcoes = append(opcoes, opcaoTrecho{trechos: itinerario[:n], melhor: itinerario})
	}
	return opcoes
}

func iniciarBuscaPassagem(conn net.Conn, leitor, leitorRede *bufio.Reader) {

	fmt.Println("\n=== BUSCAR PASSAGENS ===")
	fmt.Printf("Cidades atendidas: %s\n", strings.Join(modelos.CidadesValidas, ", "))
	origem := cli.LerCidade(leitor, "Origem: ")
	var destino string
	for {
		destino = cli.LerCidade(leitor, "Destino Final: ")
		if destino != origem {
			break
		}
		fmt.Println("O destino deve ser diferente da origem.")
	}
	dataViagem := cli.LerData(leitor, "Data da viagem (ex: 2026-09-20): ")

	// O passageiro monta a viagem escolhendo um carro por vez. Cada escolha já reserva
	// a vaga no servidor, e só são mostradas opções que ainda conseguem chegar ao destino.
	cidadeAtual := origem
	horarioMinimo := ""
	var carrinho []modelos.Trecho

	for cidadeAtual != destino {
		filtro := modelos.FiltroBusca{Origem: cidadeAtual, Destino: destino}
		if len(carrinho) == 0 {
			filtro.Data = dataViagem // o primeiro carro sai no dia escolhido
		} else {
			filtro.HorarioMinimo = horarioMinimo // os próximos saem depois que o anterior chega
		}

		// 1. Pede ao servidor os itinerários possíveis a partir de onde o passageiro está
		resp := cli.Requisitar(conn, leitorRede, "BUSCAR_ITINERARIOS", filtro)
		if resp.Status != 200 {
			fmt.Printf("\n %s\n", resp.Mensagem)
			if len(carrinho) > 0 {
				fmt.Println("As reservas dos trechos anteriores vão expirar sozinhas em breve.")
			}
			return
		}
		var itinerarios [][]modelos.Trecho
		resp.Decodificar(&itinerarios)
		opcoes := agruparPorPrimeiroCarro(itinerarios)

		// 2. O passageiro escolhe qual motorista ele quer para o próximo pedaço
		if len(carrinho) == 0 {
			fmt.Printf("\n>> %d itinerário(s) encontrado(s) de %s para %s.\n", len(itinerarios), origem, destino)
		}
		fmt.Printf("\n--- Escolha o carro saindo de %s ---\n", cidadeAtual)
		escolha := mostrarOpcoesEEscolher(leitor, opcoes)
		if escolha == nil {
			if len(carrinho) > 0 {
				fmt.Println("As reservas dos trechos anteriores vão expirar sozinhas em breve.")
			}
			return
		}

		// 3. Trava a vaga IMEDIATAMENTE no servidor!
		// O primeiro pedaço abre um carrinho novo; os seguintes entram no mesmo carrinho
		fmt.Println(">> Reservando sua vaga no carro...")
		if !enviarParaCarrinho(conn, leitorRede, escolha.trechos, len(carrinho) > 0) {
			fmt.Println(" Não foi possível reservar este trecho. Compra interrompida.")
			return
		}

		// 4. Salva localmente só para mostrar o resumo no final
		carrinho = append(carrinho, escolha.trechos...)

		// 5. O próximo carro sai de onde este chegar, e só depois que ele chegar
		ultimo := escolha.trechos[len(escolha.trechos)-1]
		cidadeAtual = ultimo.Destino
		horarioMinimo = ultimo.HorarioChegada
	}

	// 6. Resumo final
	mostrarResumoReserva(carrinho)
}

func mostrarResumoReserva(carrinho []modelos.Trecho) {
	fmt.Println("\n=== VIAGEM MONTADA COM SUCESSO ===")
	for _, t := range carrinho {
		fmt.Printf("[%s] %s -> %s | Motorista: %s (R$ %.2f)\n", t.HorarioSaida, t.Origem, t.Destino, t.MotoristaID, t.Valor)
	}
	fmt.Printf("TOTAL: R$ %.2f\n", modelos.ValorTotal(carrinho))
	fmt.Println("\nVagas reservadas temporariamente!")
	fmt.Println("Vá ao Menu Principal e escolha 'Abrir Carrinho' para confirmar a compra.")
}

// Retorna nil se o passageiro desistir (opção 0)
func mostrarOpcoesEEscolher(leitor *bufio.Reader, opcoes []opcaoTrecho) *opcaoTrecho {
	for i, opcao := range opcoes {
		inicio := opcao.trechos[0]
		fim := opcao.trechos[len(opcao.trechos)-1]

		fmt.Printf("%d. Motorista %s | %s (%s) -> %s (%s) | R$ %.2f | Paradas: %d\n",
			i+1, inicio.MotoristaID, inicio.Origem, inicio.HorarioSaida, fim.Destino, fim.HorarioChegada,
			modelos.ValorTotal(opcao.trechos), len(opcao.trechos)-1)

		if len(opcao.melhor) == len(opcao.trechos) {
			fmt.Println("     ✔ chega ao destino final")
		} else {
			chegada := opcao.melhor[len(opcao.melhor)-1]
			fmt.Printf("     ↳ depois: chegada em %s às %s | total da viagem R$ %.2f | %d troca(s) de carro\n",
				chegada.Destino, chegada.HorarioChegada, modelos.ValorTotal(opcao.melhor), modelos.Baldeacoes(opcao.melhor))
		}
	}

	fmt.Print("Escolha o número da opção desejada (0 para desistir): ")
	escolha := cli.LerNumero(leitor, 0, len(opcoes))
	if escolha == 0 {
		return nil
	}

	return &opcoes[escolha-1]
}

func enviarParaCarrinho(conn net.Conn, leitorRede *bufio.Reader, trechosEscolhidos []modelos.Trecho, continuacao bool) bool {
	resp := cli.Requisitar(conn, leitorRede, "ADICIONAR_CARRINHO", modelos.CarrinhoPayload{
		Trechos:     trechosEscolhidos,
		Continuacao: continuacao,
	})

	fmt.Printf("\n>> %s\n", resp.Mensagem)
	return resp.Status == 200 // Retorna true se conseguiu travar a vaga
}

func confirmarReserva(conn net.Conn, leitorRede *bufio.Reader) {
	resp := cli.Requisitar(conn, leitorRede, "EFETIVAR_ITINERARIO", nil)
	fmt.Printf("\n>> %s\n", resp.Mensagem)
}

func abrirCarrinho(conn net.Conn, leitor, leitorRede *bufio.Reader) {
	resp := cli.Requisitar(conn, leitorRede, "VER_CARRINHO", nil)

	// Se não achou carrinho, avisa e sai
	if resp.Status != 200 {
		fmt.Printf("\n %s\n", resp.Mensagem)
		return
	}

	// Se achou, decodifica os trechos
	var carrinho []modelos.Trecho
	resp.Decodificar(&carrinho)

	fmt.Println("\n=== 🛒 SEU CARRINHO DE COMPRAS ===")
	for _, t := range carrinho {
		fmt.Printf("[%s] %s -> %s | Motorista: %s (R$ %.2f)\n", t.HorarioSaida, t.Origem, t.Destino, t.MotoristaID, t.Valor)
	}
	fmt.Printf("----------------------------------\n")
	fmt.Printf("TOTAL A PAGAR: R$ %.2f\n", modelos.ValorTotal(carrinho))

	if cli.Confirmar(leitor, "\nDeseja confirmar a compra e emitir as passagens?") {
		fmt.Println(">> Processando sua confirmação de compra...")
		confirmarReserva(conn, leitorRede)
	} else {
		fmt.Println(">> Compra adiada. O seu carrinho vai expirar sozinho em breve.")
	}
}

func consultarMinhasViagens(conn net.Conn, leitorRede *bufio.Reader) []modelos.Itinerario {
	resp := cli.Requisitar(conn, leitorRede, "CONSULTAR_VIAGENS_PASSAGEIRO", nil)

	if resp.Status != 200 {
		fmt.Printf("\n %s\n", resp.Mensagem)
		return nil
	}

	var itinerarios []modelos.Itinerario
	resp.Decodificar(&itinerarios)

	fmt.Println("\n=== MINHAS VIAGENS ===")
	for i, it := range itinerarios {
		fmt.Printf("\n%d. %s -> %s | Total: R$ %.2f | ID: %s\n", i+1, it.Origem, it.Destino, it.ValorTotal, it.ID)
		for _, t := range it.Trechos {
			fmt.Printf("   [%s -> %s] %s -> %s | Motorista: %s (R$ %.2f)\n",
				t.HorarioSaida, t.HorarioChegada, t.Origem, t.Destino, t.MotoristaID, t.Valor)
		}
	}
	return itinerarios
}

func cancelarViagem(conn net.Conn, leitorTeclado, leitorRede *bufio.Reader) {
	itinerarios := consultarMinhasViagens(conn, leitorRede)
	if len(itinerarios) == 0 {
		return
	}

	fmt.Print("\nNúmero da viagem que deseja cancelar (0 para voltar): ")
	escolha := cli.LerNumero(leitorTeclado, 0, len(itinerarios))
	if escolha == 0 {
		return
	}
	itinerario := itinerarios[escolha-1]

	if !cli.Confirmar(leitorTeclado, fmt.Sprintf("Cancelar a viagem %s -> %s (todos os trechos)?", itinerario.Origem, itinerario.Destino)) {
		fmt.Println(">> Cancelamento abortado.")
		return
	}

	resp := cli.Requisitar(conn, leitorRede, "CANCELAR_ITINERARIO", modelos.CancelamentoPayload{ViagemID: itinerario.ID})
	fmt.Printf("\n>> %s\n", resp.Mensagem)
}
