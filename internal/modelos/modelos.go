package modelos

import (
	"strings"
	"time"
)

// Formato de data/hora usado em todo o sistema (ordena alfabeticamente = ordena no tempo)
const (
	FormatoDataHora = "2006-01-02 15:04"
	FormatoData     = "2006-01-02"
)

// Limites de negócio (validados pelo servidor)
const (
	MaxAssentos             = 8  // capacidade máxima de passageiros de um veículo
	MaxTrechosPorCarona     = 10 // paradas numa mesma rota
	MaxTrechosPorItinerario = 6  // trechos numa mesma viagem de passageiro
	TipoMotorista           = "motorista"
	TipoPassageiro          = "passageiro"
)

// Confere se o texto está EXATAMENTE no formato (ex.: "08:00", e não "8:00").
func FormatoExato(layout, valor string) bool {
	t, err := time.ParseInLocation(layout, valor, time.Local)
	return err == nil && t.Format(layout) == valor
}

var CidadesValidas = []string{
	"Feira de Santana",
	"Salvador",
	"Santo Amaro",
	"Alagoinhas",
	"Aracaju",
	"Esplanada",
}

// Devolve o nome oficial da cidade (ignorando maiúsculas e espaços) e se ela é atendida
func NormalizarCidade(cidade string) (string, bool) {
	for _, c := range CidadesValidas {
		if strings.EqualFold(strings.TrimSpace(cidade), c) {
			return c, true
		}
	}
	return "", false
}

type Usuario struct {
	ID    string `json:"id"`
	Nome  string `json:"nome"`
	Tipo  string `json:"tipo"`
	Senha string `json:"senha,omitempty"` // omitida nas respostas do servidor
}

type CarrinhoPayload struct {
	// Só o id e o carona_id de cada trecho são usados; o resto vem dos dados oficiais do servidor
	Trechos     []Trecho `json:"trechos"`
	Continuacao bool     `json:"continuacao"`
}

type FiltroBusca struct {
	Origem        string `json:"origem"`
	Destino       string `json:"destino"`
	Data          string `json:"data,omitempty"`           // YYYY-MM-DD: o primeiro trecho sai neste dia
	HorarioMinimo string `json:"horario_minimo,omitempty"` // YYYY-MM-DD HH:MM: o primeiro trecho sai a partir daqui
}

type Trecho struct {
	ID             string   `json:"id"`
	CaronaID       string   `json:"carona_id"`
	MotoristaID    string   `json:"motorista_id"`
	Origem         string   `json:"origem"`
	Destino        string   `json:"destino"`
	Valor          float64  `json:"valor"`
	VagasTotais    int      `json:"vagas_totais"` // vagas oferecidas neste trecho
	VagasLivres    int      `json:"vagas_livres"`
	Passageiros    []string `json:"passageiros"`
	HorarioSaida   string   `json:"horario_saida"` // YYYY-MM-DD HH:MM
	HorarioChegada string   `json:"horario_chegada"`
}

// A viagem gerida pelo motorista
type Carona struct {
	ID          string   `json:"id"`
	MotoristaID string   `json:"motorista_id"`
	Origem      string   `json:"origem"`
	Destino     string   `json:"destino"`
	DataHora    string   `json:"data_hora"` // saída do primeiro trecho
	Assentos    int      `json:"assentos"`  // capacidade do veículo: limite (e valor padrão) das vagas de cada trecho
	Trechos     []Trecho `json:"trechos"`
}

// A viagem para o passageiro
type Itinerario struct {
	ID           string   `json:"id"`
	PassageiroID string   `json:"passageiro_id"`
	Origem       string   `json:"origem"`
	Destino      string   `json:"destino"`
	ValorTotal   float64  `json:"valor_total"`
	Trechos      []Trecho `json:"trechos"` // A lista exata de carros que ele vai pegar
}

// Pedido de cancelamento: qual viagem (ID do itinerário ou da carona).
// Quem pede é o usuário logado na conexão.
type CancelamentoPayload struct {
	ViagemID string `json:"viagem_id"`
}

// Aviso guardado na central de notificações do usuário
type Notificacao struct {
	ID       string `json:"id"`
	Data     string `json:"data"` // YYYY-MM-DD HH:MM
	Mensagem string `json:"mensagem"`
	Lida     bool   `json:"lida"` // false = chegou depois da última abertura da central
}

// Quantas vezes o passageiro troca de carro ao longo dos trechos
func Baldeacoes(trechos []Trecho) int {
	trocas := 0
	for i := 1; i < len(trechos); i++ {
		if trechos[i].CaronaID != trechos[i-1].CaronaID {
			trocas++
		}
	}
	return trocas
}

func ValorTotal(trechos []Trecho) float64 {
	var total float64
	for _, t := range trechos {
		total += t.Valor
	}
	return total
}
