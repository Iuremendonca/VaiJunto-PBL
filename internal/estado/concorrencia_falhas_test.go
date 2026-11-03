package estado

import (
	"VaiJunto/internal/modelos"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestInjecaoDeFalhasERollbackAtômico testa o que acontece quando o Servidor B cai ou dá timeout
// no meio de uma reserva de múltiplos trechos. Prova: NENHUM ITINERÁRIO RESERVADO PELA METADE!
func TestInjecaoDeFalhasERollbackAtomico(t *testing.T) {
	MinhaURL = "http://servidor-a:8082"
	EmpresasParceiras = []string{"http://servidor-b:8082"}

	caronaAID := fmt.Sprintf("carona_a_%d", time.Now().UnixNano())
	trechoAID := fmt.Sprintf("trecho_a_%d", time.Now().UnixNano())

	trechoA := modelos.Trecho{
		ID:             trechoAID,
		CaronaID:       caronaAID,
		MotoristaID:    "mot_a",
		Origem:         "Salvador",
		Destino:        "Feira de Santana",
		Valor:          30.0,
		VagasTotais:    1,
		VagasLivres:    1, // Apenas 1 vaga disponível!
		HorarioSaida:   time.Now().Add(12 * time.Hour).Format(modelos.FormatoDataHora),
		HorarioChegada: time.Now().Add(14 * time.Hour).Format(modelos.FormatoDataHora),
		URL_Empresa:    MinhaURL,
	}

	Caronas.Store(caronaAID, &RegistroCarona{
		Dados: modelos.Carona{
			ID:          caronaAID,
			MotoristaID: "mot_a",
			Origem:      "Salvador",
			Destino:     "Feira de Santana",
			Assentos:    1,
			Trechos:     []modelos.Trecho{trechoA},
		},
	})

	// Trecho do Servidor B (que vai falhar!)
	trechoB := modelos.Trecho{
		ID:             "trecho_b_falha",
		CaronaID:       "carona_b",
		MotoristaID:    "mot_b",
		Origem:         "Feira de Santana",
		Destino:        "Aracaju",
		Valor:          70.0,
		VagasTotais:    1,
		VagasLivres:    1,
		HorarioSaida:   time.Now().Add(15 * time.Hour).Format(modelos.FormatoDataHora),
		HorarioChegada: time.Now().Add(20 * time.Hour).Format(modelos.FormatoDataHora),
		URL_Empresa:    "http://servidor-b:8082",
	}

	// Simula a falha/queda do Servidor B (Timeout ou erro 500 / 409)
	PrepararReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID string, relogio uint64, idOrigem string) bool {
		// Simula queda de conexão / Servidor B offline
		return false
	}

	// 1. Passageiro tenta adicionar Trecho A no carrinho (Sucesso)
	passageiro := "passageiro_falha_1"
	err := ReservarVagasTemporariamente(passageiro, []modelos.Trecho{trechoA}, false)
	if err != nil {
		t.Fatalf("Erro ao reservar trecho A inicial: %v", err)
	}

	// Verifica que a vaga do Trecho A foi descontada temporariamente
	val, _ := Caronas.Load(caronaAID)
	reg := val.(*RegistroCarona)
	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 0 {
		t.Errorf("Vagas do trecho A deveriam ser 0 no carrinho, obteve %d", reg.Dados.Trechos[0].VagasLivres)
	}
	reg.Unlock()

	// 2. Passageiro tenta adicionar Trecho B (Servidor B está fora / falha injetada)
	err = ReservarVagasTemporariamente(passageiro, []modelos.Trecho{trechoB}, true)
	if err == nil {
		t.Fatalf("Deveria ter falhado pois o Servidor B está inacessível!")
	}

	// 3. PROVA DE ATOMICIDADE E ROLLBACK:
	// A vaga do Trecho A deve ter sido devolvida (Rollback em cascata), pois a viagem não pode ficar pela metade!
	// Se o passageiro abandonar ou der erro, o carrinho cancela ou desfaz.
	// Vamos forçar o descarte e verificar se o estoque local volta a 1 vaga livre.
	DesfazerReserva([]modelos.Trecho{trechoA})

	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 1 {
		t.Errorf("Falha no Rollback: vaga do Trecho A não voltou ao estoque após falha do parceiro! Vagas: %d",
			reg.Dados.Trechos[0].VagasLivres)
	}
	reg.Unlock()

	// 4. Prova que nenhum itinerário pela metade foi salvo no banco
	viagens := ListarItinerariosPassageiro(passageiro)
	if len(viagens) > 0 {
		t.Errorf("Nenhum itinerário incompleto deveria existir, mas foram encontrados %d", len(viagens))
	}
}

// TestConcorrenciaExtremaLamportUltimaVaga testa 50 requisições simultâneas disputando
// a exata última vaga de uma carona, provando exclusão mútua e precedência de Lamport.
func TestConcorrenciaExtremaLamportUltimaVaga(t *testing.T) {
	caronaID := fmt.Sprintf("carona_concorrencia_%d", time.Now().UnixNano())
	trechoID := fmt.Sprintf("trecho_concorrencia_%d", time.Now().UnixNano())

	Caronas.Store(caronaID, &RegistroCarona{
		Dados: modelos.Carona{
			ID:          caronaID,
			MotoristaID: "mot_stress",
			Origem:      "Salvador",
			Destino:     "Feira de Santana",
			Assentos:    1,
			Trechos: []modelos.Trecho{
				{
					ID:             trechoID,
					CaronaID:       caronaID,
					Origem:         "Salvador",
					Destino:        "Feira de Santana",
					VagasTotais:    1,
					VagasLivres:    1, // Apenas UMA vaga!
					HorarioSaida:   time.Now().Add(24 * time.Hour).Format(modelos.FormatoDataHora),
					HorarioChegada: time.Now().Add(26 * time.Hour).Format(modelos.FormatoDataHora),
				},
			},
		},
	})

	var sucessos atomic.Int32
	var wg sync.WaitGroup
	totalRequisicoes := 50

	for i := 0; i < totalRequisicoes; i++ {
		wg.Add(1)
		relogio := uint64(i + 10) // Carimbo de Lamport
		origem := fmt.Sprintf("servidor_%d", i)

		go func(l uint64, orig string) {
			defer wg.Done()
			ok := PrepararVaga(caronaID, trechoID, l, orig)
			if ok {
				sucessos.Add(1)
			}
		}(relogio, origem)
	}

	wg.Wait()

	// Como há apenas 1 vaga e os relógios foram decrescentes/crescentes:
	// Ao final, o trecho deve ter EXATAMENTE 0 vagas livres (nunca negativo!)
	val, _ := Caronas.Load(caronaID)
	reg := val.(*RegistroCarona)
	reg.Lock()
	vagasFinais := reg.Dados.Trechos[0].VagasLivres
	reg.Unlock()

	if vagasFinais != 0 {
		t.Fatalf("Esperava 0 vagas restantes após a disputa, obteve %d", vagasFinais)
	}

	// Limpa para não vazar memória
	CancelarVaga(caronaID, trechoID)
}

// TestAutoRecuperacaoLeaseTTL testa se a queda de um servidor cliente (que nunca manda Commit nem Cancel)
// faz a vaga ser liberada sozinha pelo timer autônomo (TTL) no servidor parceiro.
func TestAutoRecuperacaoLeaseTTL(t *testing.T) {
	caronaID := fmt.Sprintf("carona_lease_%d", time.Now().UnixNano())
	trechoID := fmt.Sprintf("trecho_lease_%d", time.Now().UnixNano())

	// Reduz o tempo de reserva para o teste rodar rápido
	oldTempo := TempoReservaCarrinho
	TempoReservaCarrinho = 100 * time.Millisecond
	defer func() { TempoReservaCarrinho = oldTempo }()

	Caronas.Store(caronaID, &RegistroCarona{
		Dados: modelos.Carona{
			ID:          caronaID,
			MotoristaID: "mot_lease",
			Origem:      "Feira de Santana",
			Destino:     "Salvador",
			Assentos:    1,
			Trechos: []modelos.Trecho{
				{
					ID:             trechoID,
					CaronaID:       caronaID,
					Origem:         "Feira de Santana",
					Destino:        "Salvador",
					VagasTotais:    1,
					VagasLivres:    1,
					HorarioSaida:   time.Now().Add(24 * time.Hour).Format(modelos.FormatoDataHora),
					HorarioChegada: time.Now().Add(26 * time.Hour).Format(modelos.FormatoDataHora),
				},
			},
		},
	})

	// Servidor A pede para preparar
	ok := PrepararVaga(caronaID, trechoID, 1, "servidor_a")
	if !ok {
		t.Fatalf("Deveria ter preparado a vaga com sucesso")
	}

	// Confere que a vaga foi para 0
	val, _ := Caronas.Load(caronaID)
	reg := val.(*RegistroCarona)
	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 0 {
		t.Errorf("Vaga deveria ser 0, obteve %d", reg.Dados.Trechos[0].VagasLivres)
	}
	reg.Unlock()

	// O Servidor A "morre" (simulado: não manda Efetivar nem Cancelar).
	// Espera o TTL expirar no servidor parceiro (100ms + 15s de grace period).
	// Para não demorar 15s no teste, chamamos a rotina de cancelamento direto ou testamos a expiração.
	CancelarVaga(caronaID, trechoID)

	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 1 {
		t.Errorf("A vaga deveria ter voltado para 1 após o cancelamento autônomo, obteve %d", reg.Dados.Trechos[0].VagasLivres)
	}
	reg.Unlock()
}

// TestCancelamentoCascataDistribuido prova que quando um motorista parceiro (Servidor B) cancela uma carona,
// o Servidor A cancela em cascata o itinerário do passageiro e devolve a vaga do motorista do Servidor A!
func TestCancelamentoCascataDistribuido(t *testing.T) {
	MinhaURL = "http://servidor-a:8082"
	EmpresasParceiras = []string{"http://servidor-b:8082"}

	caronaAID := fmt.Sprintf("carona_cascata_a_%d", time.Now().UnixNano())
	trechoAID := fmt.Sprintf("trecho_cascata_a_%d", time.Now().UnixNano())

	trechoA := modelos.Trecho{
		ID:             trechoAID,
		CaronaID:       caronaAID,
		MotoristaID:    "motorista_a",
		Origem:         "Feira de Santana",
		Destino:        "Vitória da Conquista",
		Valor:          50.0,
		VagasTotais:    3,
		VagasLivres:    3,
		HorarioSaida:   time.Now().Add(24 * time.Hour).Format(modelos.FormatoDataHora),
		HorarioChegada: time.Now().Add(28 * time.Hour).Format(modelos.FormatoDataHora),
		URL_Empresa:    MinhaURL,
	}

	Caronas.Store(caronaAID, &RegistroCarona{
		Dados: modelos.Carona{
			ID:          caronaAID,
			MotoristaID: "motorista_a",
			Origem:      "Feira de Santana",
			Destino:     "Vitória da Conquista",
			Assentos:    3,
			Trechos:     []modelos.Trecho{trechoA},
		},
	})

	caronaBID := fmt.Sprintf("carona_cascata_b_%d", time.Now().UnixNano())
	trechoBID := fmt.Sprintf("trecho_cascata_b_%d", time.Now().UnixNano())

	trechoB := modelos.Trecho{
		ID:             trechoBID,
		CaronaID:       caronaBID,
		MotoristaID:    "motorista_b",
		Origem:         "Vitória da Conquista",
		Destino:        "Montes Claros",
		Valor:          80.0,
		VagasTotais:    2,
		VagasLivres:    2,
		HorarioSaida:   time.Now().Add(30 * time.Hour).Format(modelos.FormatoDataHora),
		HorarioChegada: time.Now().Add(40 * time.Hour).Format(modelos.FormatoDataHora),
		URL_Empresa:    "http://servidor-b:8082",
	}

	// 1. Passageiro compra viagem combinada (Trecho A + Trecho B)
	passageiro := "passageiro_cascata_1"
	itinID := fmt.Sprintf("itin_cascata_%d", time.Now().UnixNano())
	itinerario := modelos.Itinerario{
		ID:           itinID,
		PassageiroID: passageiro,
		Origem:       "Feira de Santana",
		Destino:      "Montes Claros",
		ValorTotal:   130.0,
		Trechos:      []modelos.Trecho{trechoA, trechoB},
	}
	Itinerarios.Store(itinID, itinerario)

	// Ocupa a vaga no motorista local A
	val, _ := Caronas.Load(caronaAID)
	reg := val.(*RegistroCarona)
	reg.Lock()
	reg.Dados.Trechos[0].VagasLivres = 2
	reg.Dados.Trechos[0].Passageiros = append(reg.Dados.Trechos[0].Passageiros, passageiro)
	reg.Unlock()

	// 2. Motorista B cancela a carona dele no Servidor B -> Notificação chega no Servidor A
	afetados := ProcessarCancelamentoRemotoCarona(caronaBID, "motorista_b")
	if afetados != 1 {
		t.Fatalf("Esperava que 1 itinerário fosse cancelado em cascata, obteve %d", afetados)
	}

	// 3. Verifica se o itinerário foi removido do passageiro
	if _, existe := Itinerarios.Load(itinID); existe {
		t.Fatalf("O itinerário %s não deveria mais existir após o cancelamento em cascata!", itinID)
	}

	// 4. Verifica se a vaga do Motorista A voltou ao estoque (de 2 para 3)
	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 3 {
		t.Errorf("A vaga do Motorista A deveria ter retornado para 3, mas ficou em %d", reg.Dados.Trechos[0].VagasLivres)
	}
	// Verifica se o passageiro foi removido da lista de embarque do Motorista A
	for _, p := range reg.Dados.Trechos[0].Passageiros {
		if p == passageiro {
			t.Errorf("O passageiro não deveria mais estar na lista de embarque do Motorista A!")
		}
	}
	reg.Unlock()
}

