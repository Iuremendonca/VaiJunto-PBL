package main

import (
	"VaiJunto/internal/consorcio"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	alvo := flag.String("alvo", "http://localhost:9082", "URL do servidor parceiro para testar")
	caronaID := flag.String("carona", "carona_1", "ID da carona para simular")
	trechoID := flag.String("trecho", "trecho_1", "ID do trecho para simular")
	passageiroID := flag.String("passageiro", "pass_teste", "ID do passageiro para efetivar")
	flag.Parse()

	fmt.Println("=================================================================")
	fmt.Printf("🚀 INICIANDO SIMULAÇÃO DE COMUNICAÇÃO DO CONSÓRCIO\n")
	fmt.Printf("🎯 Servidor Alvo: %s\n", *alvo)
	fmt.Println("=================================================================")

	// 1. Scatter-Gather: Consulta de Trechos Disponíveis
	fmt.Println("\n[ETAPA 1] Testando Scatter-Gather (GET /api/trechos/disponiveis)...")
	trechos, err := consorcio.BuscarTrechosRemotos(*alvo)
	if err != nil {
		fmt.Printf("❌ Falha na conexão com %s: %v\n", *alvo, err)
		os.Exit(1)
	}
	fmt.Printf("✅ Sucesso! O servidor parceiro retornou %d trecho(s) disponível(is).\n", len(trechos))
	for i, t := range trechos {
		fmt.Printf("   [%d] %s -> %s (Carona: %s, Vagas: %d, Saída: %s, Empresa: %s)\n",
			i+1, t.Origem, t.Destino, t.CaronaID, t.VagasLivres, t.HorarioSaida, t.URL_Empresa)
		if i == 0 && *caronaID == "carona_1" && *trechoID == "trecho_1" {
			*caronaID = t.CaronaID
			*trechoID = t.ID
		}
	}

	time.Sleep(1 * time.Second)

	// 2. Fase 1 do 2PC: Preparar
	fmt.Printf("\n[ETAPA 2] Testando Fase 1 do 2PC - Preparar (POST /api/reservas/preparar)...\n")
	fmt.Printf("   -> Solicitando bloqueio de 1 vaga na Carona %s, Trecho %s\n", *caronaID, *trechoID)
	okPrep := consorcio.PrepararReservaRemotaPorID(*alvo, *caronaID, *trechoID)
	if okPrep {
		fmt.Println("✅ Fase 1 APROVADA: Servidor parceiro bloqueou a vaga com sucesso!")
	} else {
		fmt.Println("⚠️  Fase 1 RECUSADA: Vaga esgotada ou carona/trecho não existe no parceiro.")
		fmt.Println("   (Dica: se for uma carona que não existe, o parceiro rejeita com 409 Conflict, como esperado pelo 2PC!)")
	}

	time.Sleep(1 * time.Second)

	// 3. Rollback da Fase 1: Cancelar
	fmt.Printf("\n[ETAPA 3] Testando Abort/Rollback - Cancelar (POST /api/reservas/cancelar)...\n")
	fmt.Printf("   -> Solicitando devolução da vaga na Carona %s, Trecho %s\n", *caronaID, *trechoID)
	okCanc := consorcio.CancelarReservaRemotaPorID(*alvo, *caronaID, *trechoID)
	if okCanc {
		fmt.Println("✅ Rollback CONCLUÍDO: Vaga devolvida ao estoque do parceiro!")
	} else {
		fmt.Println("❌ Erro ao cancelar no parceiro.")
	}

	time.Sleep(1 * time.Second)

	// 4. Ciclo Completo: Preparar + Efetivar (Commit)
	fmt.Printf("\n[ETAPA 4] Testando Ciclo Completo de Commit (Fase 1 -> Fase 2)...\n")
	fmt.Printf("   -> 4.1 Preparando vaga...\n")
	if consorcio.PrepararReservaRemotaPorID(*alvo, *caronaID, *trechoID) {
		fmt.Println("   -> 4.2 Confirmando commit definitivo para o passageiro " + *passageiroID + "...")
		if consorcio.EfetivarReservaRemotaPorID(*alvo, *caronaID, *trechoID, *passageiroID) {
			fmt.Println("🎉 COMMIT REALIZADO COM SUCESSO! Reserva finalizada no parceiro.")
		} else {
			fmt.Println("❌ Falha no Commit da Fase 2.")
		}
	}

	fmt.Println("\n=================================================================")
	fmt.Println("🏁 SIMULAÇÃO CONCLUÍDA")
	fmt.Println("=================================================================")
}

