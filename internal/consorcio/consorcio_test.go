package consorcio

import (
	"VaiJunto/internal/estado"
	"VaiJunto/internal/modelos"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

func TestComunicacaoEntreServidores(t *testing.T) {
	os.Setenv("VAIJUNTO_DADOS", t.TempDir())
	porta := "18082"
	urlTeste := "http://127.0.0.1:" + porta
	MinhaURL = urlTeste

	// Sobe o servidor REST em background
	go IniciarAPI(porta)

	// Aguarda o servidor estar pronto para conexões
	pronto := false
	for i := 0; i < 20; i++ {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+porta, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			pronto = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !pronto {
		t.Fatalf("Servidor REST não subiu na porta %s a tempo", porta)
	}

	// 1. Cadastra uma carona de teste no estado local
	caronaID := fmt.Sprintf("carona_teste_%d", time.Now().UnixNano())
	trechoID := fmt.Sprintf("trecho_teste_%d", time.Now().UnixNano())

	trechoTeste := modelos.Trecho{
		ID:             trechoID,
		CaronaID:       caronaID,
		MotoristaID:    "mot_1",
		Origem:         "Salvador",
		Destino:        "Feira de Santana",
		Valor:          35.0,
		VagasTotais:    3,
		VagasLivres:    3,
		HorarioSaida:   time.Now().Add(2 * time.Hour).Format(modelos.FormatoDataHora),
		HorarioChegada: time.Now().Add(3 * time.Hour).Format(modelos.FormatoDataHora),
	}

	carona := modelos.Carona{
		ID:          caronaID,
		MotoristaID: "mot_1",
		Origem:      "Salvador",
		Destino:     "Feira de Santana",
		Assentos:    3,
		Trechos:     []modelos.Trecho{trechoTeste},
	}
	estado.Caronas.Store(caronaID, &estado.RegistroCarona{Dados: carona})

	// 2. Testar Scatter-Gather: BuscarTrechosRemotos
	trechos, err := BuscarTrechosRemotos(urlTeste)
	if err != nil {
		t.Fatalf("Erro ao buscar trechos remotos: %v", err)
	}
	achou := false
	for _, tr := range trechos {
		if tr.ID == trechoID {
			achou = true
			if tr.URL_Empresa != urlTeste {
				t.Errorf("Esperava URL_Empresa %s, obteve %s", urlTeste, tr.URL_Empresa)
			}
			break
		}
	}
	if !achou {
		t.Fatalf("Trecho recém cadastrado não foi retornado pelo Scatter-Gather")
	}

	// 3. Testar Fase 1 do 2PC: PrepararReservaRemota
	okPrep := PrepararReservaRemota(urlTeste, trechoTeste)
	if !okPrep {
		t.Fatalf("Falha ao preparar reserva remota")
	}

	// Verifica se a vaga foi descontada no estado
	val, _ := estado.Caronas.Load(caronaID)
	reg := val.(*estado.RegistroCarona)
	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 2 {
		t.Errorf("Vagas livres deveriam ser 2 após preparar, obteve %d", reg.Dados.Trechos[0].VagasLivres)
	}
	reg.Unlock()

	// 4. Testar Cancelar / Rollback: CancelarReservaRemota
	okCanc := CancelarReservaRemota(urlTeste, trechoTeste)
	if !okCanc {
		t.Fatalf("Falha ao cancelar reserva remota")
	}
	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 3 {
		t.Errorf("Vagas livres deveriam ser restauradas para 3, obteve %d", reg.Dados.Trechos[0].VagasLivres)
	}
	reg.Unlock()

	// 5. Testar Commit: Preparar e Efetivar
	okPrep2 := PrepararReservaRemota(urlTeste, trechoTeste)
	if !okPrep2 {
		t.Fatalf("Falha no segundo preparo de reserva")
	}
	okEfet := EfetivarReservaRemota(urlTeste, trechoTeste, "passageiro_teste")
	if !okEfet {
		t.Fatalf("Falha ao efetivar reserva remota")
	}

	reg.Lock()
	if len(reg.Dados.Trechos[0].Passageiros) == 0 || reg.Dados.Trechos[0].Passageiros[0] != "passageiro_teste" {
		t.Errorf("Passageiro não foi registrado na lista de embarque após efetivar")
	}
	reg.Unlock()

	// 6. Testar Cancelar Viagem pelo Passageiro Remoto: Devolve vaga, remove da lista de embarque e notifica motorista
	okCanc2 := CancelarReservaRemota(urlTeste, trechoTeste, "passageiro_teste", "Carlos")
	if !okCanc2 {
		t.Fatalf("Falha ao cancelar reserva remota com passageiro")
	}

	reg.Lock()
	if reg.Dados.Trechos[0].VagasLivres != 3 {
		t.Errorf("Vagas livres deveriam voltar para 3 após cancelamento, obteve %d", reg.Dados.Trechos[0].VagasLivres)
	}
	if len(reg.Dados.Trechos[0].Passageiros) != 0 {
		t.Errorf("Passageiro deveria ter sido removido da lista de embarque, mas restaram: %v", reg.Dados.Trechos[0].Passageiros)
	}
	reg.Unlock()

	naoLidas := estado.ContarNotificacoesNaoLidas("mot_1")
	if naoLidas == 0 {
		t.Errorf("Motorista mot_1 deveria ter notificações não lidas após confirmação e cancelamento")
	}
	notifs := estado.AbrirNotificacoes("mot_1")
	if len(notifs) == 0 {
		t.Fatalf("Nenhuma notificação encontrada para mot_1")
	}
	if !contains(notifs[0].Mensagem, "Carlos") || !contains(notifs[0].Mensagem, "cancelou a reserva") {
		t.Errorf("Mensagem de notificação inesperada: %s", notifs[0].Mensagem)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

