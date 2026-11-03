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
	EfetivarReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID, passageiroID string) bool {
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
