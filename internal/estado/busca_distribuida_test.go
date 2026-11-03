package estado

import (
	"VaiJunto/internal/modelos"
	"fmt"
	"testing"
	"time"
)

func TestBuscaDistribuidaEReservaMultiEmpresas(t *testing.T) {
	MinhaURL = "http://servidor-a:8082"
	EmpresasParceiras = []string{"http://servidor-b:8082"}

	dataAmanha := time.Now().Add(24 * time.Hour).Format("2006-01-02")
	dataDepois := time.Now().Add(48 * time.Hour).Format("2006-01-02")

	// 1. Cadastra carona local no Servidor A: Feira de Santana -> Vitória da Conquista
	caronaLocalID := fmt.Sprintf("carona_local_%d", time.Now().UnixNano())
	trechoLocalID := fmt.Sprintf("trecho_local_%d", time.Now().UnixNano())

	trechoLocal := modelos.Trecho{
		ID:             trechoLocalID,
		CaronaID:       caronaLocalID,
		MotoristaID:    "motorista_local",
		Origem:         "Feira de Santana",
		Destino:        "Vitória da Conquista",
		Valor:          50.0,
		VagasTotais:    3,
		VagasLivres:    3,
		HorarioSaida:   dataAmanha + " 08:00",
		HorarioChegada: dataAmanha + " 14:00",
		URL_Empresa:    MinhaURL,
	}

	Caronas.Store(caronaLocalID, &RegistroCarona{
		Dados: modelos.Carona{
			ID:          caronaLocalID,
			MotoristaID: "motorista_local",
			Origem:      "Feira de Santana",
			Destino:     "Vitória da Conquista",
			Assentos:    3,
			Trechos:     []modelos.Trecho{trechoLocal},
		},
	})

	// 2. Mock do Servidor B: Vitória da Conquista -> Montes Claros (Minas)
	trechoRemotoID := fmt.Sprintf("trecho_remoto_%d", time.Now().UnixNano())
	trechoRemoto := modelos.Trecho{
		ID:             trechoRemotoID,
		CaronaID:       "carona_remota_b",
		MotoristaID:    "motorista_b",
		Origem:         "Vitória da Conquista",
		Destino:        "Montes Claros",
		Valor:          80.0,
		VagasTotais:    2,
		VagasLivres:    2,
		HorarioSaida:   dataAmanha + " 16:00",
		HorarioChegada: dataDepois + " 08:00",
		URL_Empresa:    "http://servidor-b:8082",
	}

	// Simula a resposta do Scatter-Gather
	BuscarTrechosRemotosFunc = func(urlEmpresa, dataInicio, dataFim string) ([]modelos.Trecho, error) {
		if urlEmpresa == "http://servidor-b:8082" {
			return []modelos.Trecho{trechoRemoto}, nil
		}
		return nil, nil
	}

	preparadoRemoto := false
	PrepararReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID string, relogio uint64, idOrigem string) bool {
		if urlEmpresa == "http://servidor-b:8082" && trechoID == trechoRemotoID {
			preparadoRemoto = true
			return true
		}
		return false
	}

	efetivadoRemoto := false
	EfetivarReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID, passageiroID string, extras ...string) bool {
		if urlEmpresa == "http://servidor-b:8082" && trechoID == trechoRemotoID {
			efetivadoRemoto = true
			return true
		}
		return false
	}

	// 3. Testa a Busca Distribuída
	filtro := modelos.FiltroBusca{
		Origem:  "Feira de Santana",
		Destino: "Montes Claros",
		Data:    dataAmanha,
	}

	itinerarios, err := BuscarItinerarios(filtro)
	if err != nil {
		t.Fatalf("Erro ao buscar itinerários distribuídos: %v", err)
	}

	if len(itinerarios) == 0 {
		t.Fatalf("Esperava encontrar rota combinando Servidor A + Servidor B, mas retornou vazio")
	}

	rota := itinerarios[0]
	if len(rota) != 2 {
		t.Fatalf("Esperava rota com 2 trechos (baldeação em Conquista), obteve %d", len(rota))
	}

	if rota[0].Origem != "Feira de Santana" || rota[0].Destino != "Vitória da Conquista" {
		t.Errorf("Primeiro trecho incorreto: %+v", rota[0])
	}
	if rota[1].Origem != "Vitória da Conquista" || rota[1].Destino != "Montes Claros" {
		t.Errorf("Segundo trecho incorreto: %+v", rota[1])
	}

	// 4. Simula o passageiro montando a viagem trecho a trecho (Carrinho)
	passageiroID := "passageiro_teste_1"

	// 4.1 Adiciona Trecho 1 (Local)
	err = ReservarVagasTemporariamente(passageiroID, []modelos.Trecho{rota[0]}, false)
	if err != nil {
		t.Fatalf("Falha ao adicionar primeiro trecho local ao carrinho: %v", err)
	}

	// 4.2 Adiciona Trecho 2 (Remoto do Servidor B)
	err = ReservarVagasTemporariamente(passageiroID, []modelos.Trecho{rota[1]}, true)
	if err != nil {
		t.Fatalf("Falha ao adicionar segundo trecho remoto ao carrinho: %v", err)
	}

	if !preparadoRemoto {
		t.Fatalf("O servidor parceiro B não foi acionado para preparar a vaga remota!")
	}

	// 5. Efetiva o itinerário (Confirmar Compra)
	err = EfetivarItinerario(passageiroID)
	if err != nil {
		t.Fatalf("Erro ao efetivar itinerário: %v", err)
	}

	if !efetivadoRemoto {
		t.Fatalf("O trecho remoto no Servidor B não recebeu o Commit (efetivação)!")
	}

	// Verifica se o itinerário final foi registrado no passageiro
	viagens := ListarItinerariosPassageiro(passageiroID)
	if len(viagens) == 0 {
		t.Fatalf("Itinerário final não foi salvo para o passageiro")
	}

	if len(viagens[0].Trechos) != 2 {
		t.Errorf("Itinerário do passageiro deveria ter 2 trechos, obteve %d", len(viagens[0].Trechos))
	}
}

func TestConsorcioTresServidoresABC(t *testing.T) {
	MinhaURL = "http://servidor-a:8082"
	EmpresasParceiras = []string{"http://servidor-b:9082", "http://servidor-c:10082"}

	dataAmanha := time.Now().Add(24 * time.Hour).Format("2006-01-02")
	dataDepois := time.Now().Add(48 * time.Hour).Format("2006-01-02")

	// 1. Trecho Empresa A (Local): Feira de Santana -> Vitória da Conquista
	caronaAID := fmt.Sprintf("carona_a_%d", time.Now().UnixNano())
	trechoAID := fmt.Sprintf("trecho_a_%d", time.Now().UnixNano())
	trechoA := modelos.Trecho{
		ID:             trechoAID,
		CaronaID:       caronaAID,
		MotoristaID:    "mot_a",
		Origem:         "Feira de Santana",
		Destino:        "Vitória da Conquista",
		Valor:          50.0,
		VagasTotais:    3,
		VagasLivres:    3,
		HorarioSaida:   dataAmanha + " 08:00",
		HorarioChegada: dataAmanha + " 13:00",
		URL_Empresa:    MinhaURL,
	}
	Caronas.Store(caronaAID, &RegistroCarona{
		Dados: modelos.Carona{
			ID:          caronaAID,
			MotoristaID: "mot_a",
			Origem:      "Feira de Santana",
			Destino:     "Vitória da Conquista",
			Assentos:    3,
			Trechos:     []modelos.Trecho{trechoA},
		},
	})

	// 2. Trecho Empresa B (Remoto): Vitória da Conquista -> Montes Claros
	trechoBID := fmt.Sprintf("trecho_b_%d", time.Now().UnixNano())
	trechoB := modelos.Trecho{
		ID:             trechoBID,
		CaronaID:       "carona_b",
		MotoristaID:    "mot_b",
		Origem:         "Vitória da Conquista",
		Destino:        "Montes Claros",
		Valor:          80.0,
		VagasTotais:    2,
		VagasLivres:    2,
		HorarioSaida:   dataAmanha + " 15:00",
		HorarioChegada: dataAmanha + " 21:00",
		URL_Empresa:    "http://servidor-b:9082",
	}

	// 3. Trecho Empresa C (Remoto): Montes Claros -> Minas
	trechoCID := fmt.Sprintf("trecho_c_%d", time.Now().UnixNano())
	trechoC := modelos.Trecho{
		ID:             trechoCID,
		CaronaID:       "carona_c",
		MotoristaID:    "mot_c",
		Origem:         "Montes Claros",
		Destino:        "Minas",
		Valor:          90.0,
		VagasTotais:    4,
		VagasLivres:    4,
		HorarioSaida:   dataAmanha + " 23:00",
		HorarioChegada: dataDepois + " 06:00",
		URL_Empresa:    "http://servidor-c:10082",
	}

	// Mock do Scatter-Gather: Servidor A consulta B e C
	BuscarTrechosRemotosFunc = func(urlEmpresa, dataInicio, dataFim string) ([]modelos.Trecho, error) {
		if urlEmpresa == "http://servidor-b:9082" {
			return []modelos.Trecho{trechoB}, nil
		}
		if urlEmpresa == "http://servidor-c:10082" {
			return []modelos.Trecho{trechoC}, nil
		}
		return nil, nil
	}

	prepB, prepC := false, false
	PrepararReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID string, relogio uint64, idOrigem string) bool {
		if urlEmpresa == "http://servidor-b:9082" && trechoID == trechoBID {
			prepB = true
			return true
		}
		if urlEmpresa == "http://servidor-c:10082" && trechoID == trechoCID {
			prepC = true
			return true
		}
		return false
	}

	efetB, efetC := false, false
	EfetivarReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID, passageiroID string, extras ...string) bool {
		if urlEmpresa == "http://servidor-b:9082" && trechoID == trechoBID {
			efetB = true
			return true
		}
		if urlEmpresa == "http://servidor-c:10082" && trechoID == trechoCID {
			efetC = true
			return true
		}
		return false
	}

	// 4. Passageiro no Servidor A busca rota de Feira de Santana para Minas
	itinerarios, err := BuscarItinerarios(modelos.FiltroBusca{
		Origem:  "Feira de Santana",
		Destino: "Minas",
		Data:    dataAmanha,
	})
	if err != nil {
		t.Fatalf("Erro na busca de 3 servidores: %v", err)
	}
	if len(itinerarios) == 0 {
		t.Fatalf("Nenhum itinerário de 3 empresas encontrado")
	}

	rota := itinerarios[0]
	if len(rota) != 3 {
		t.Fatalf("Esperava rota com 3 trechos (A -> B -> C), mas obteve %d", len(rota))
	}

	// 5. Reserva no Carrinho: Adiciona os 3 trechos
	passID := fmt.Sprintf("pass_abc_%d", time.Now().UnixNano())
	if err := ReservarVagasTemporariamente(passID, []modelos.Trecho{rota[0]}, false); err != nil {
		t.Fatalf("Falha trecho A: %v", err)
	}
	if err := ReservarVagasTemporariamente(passID, []modelos.Trecho{rota[1]}, true); err != nil {
		t.Fatalf("Falha trecho B: %v", err)
	}
	if err := ReservarVagasTemporariamente(passID, []modelos.Trecho{rota[2]}, true); err != nil {
		t.Fatalf("Falha trecho C: %v", err)
	}

	if !prepB || !prepC {
		t.Fatalf("Fase 1 (Preparar) não alcançou B (%v) ou C (%v)", prepB, prepC)
	}

	// 6. Efetivação 2PC
	if err := EfetivarItinerario(passID); err != nil {
		t.Fatalf("Erro ao efetivar viagem de 3 empresas: %v", err)
	}

	if !efetB || !efetC {
		t.Fatalf("Fase 2 (Commit) não alcançou B (%v) ou C (%v)", efetB, efetC)
	}

	viagens := ListarItinerariosPassageiro(passID)
	if len(viagens) == 0 || len(viagens[0].Trechos) != 3 {
		t.Fatalf("Viagem de 3 empresas não foi devidamente registrada")
	}
}
