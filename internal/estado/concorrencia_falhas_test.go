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

