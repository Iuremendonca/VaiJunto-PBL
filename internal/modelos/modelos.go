package modelos

import (
	"fmt"
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

// CidadesValidas contém as siglas aceitas para origem, paradas e destino (5 BA, 5 SE, 5 MG)
var CidadesValidas = []string{
	// Bahia (5)
	"ssa", // Salvador
	"fsa", // Feira de Santana
	"vdc", // Vitória da Conquista
	"ala", // Alagoinhas
	"esp", // Esplanada

	// Sergipe (5)
	"ara", // Aracaju
	"est", // Estância
	"iba", // Itabaiana
	"lag", // Lagarto
	"prp", // Propriá

	// Minas Gerais (5)
	"bh",  // Belo Horizonte
	"moc", // Montes Claros
	"gva", // Governador Valadares
	"teo", // Teófilo Otoni
	"udi", // Uberlândia
}

// SiglaParaNome mapeia as siglas para os nomes completos das cidades
var SiglaParaNome = map[string]string{
	// Bahia
	"ssa": "Salvador",
	"fsa": "Feira de Santana",
	"vdc": "Vitória da Conquista",
	"ala": "Alagoinhas",
	"esp": "Esplanada",

	// Sergipe
	"ara": "Aracaju",
	"est": "Estância",
	"iba": "Itabaiana",
	"lag": "Lagarto",
	"prp": "Propriá",

	// Minas Gerais
	"bh":  "Belo Horizonte",
	"moc": "Montes Claros",
	"gva": "Governador Valadares",
	"teo": "Teófilo Otoni",
	"udi": "Uberlândia",
}

// TextoCidadesAtendidas formata a lista de cidades com suas siglas para exibição nos clientes
func TextoCidadesAtendidas() string {
	var partes []string
	for _, sigla := range CidadesValidas {
		if nome, ok := SiglaParaNome[sigla]; ok {
			partes = append(partes, fmt.Sprintf("%s (%s)", sigla, nome))
		} else {
			partes = append(partes, sigla)
		}
	}
	return strings.Join(partes, ", ")
}

// NormalizarCidade valida a cidade.
// Aceita tanto a sigla (ex: "fsa", "bh", "esp", "ara") quanto o nome por extenso (para compatibilidade).
func NormalizarCidade(cidade string) (string, bool) {
	cidade = strings.TrimSpace(cidade)
	if cidade == "" {
		return "", false
	}
	cidadeLower := strings.ToLower(cidade)
	// 1. Validação direta por sigla (fsa, ssa, ara, esp, bh...)
	for _, sigla := range CidadesValidas {
		if cidadeLower == sigla {
			return sigla, true
		}
	}
	// 2. Se o usuário ou testes digitarem o nome completo por extenso, aceita também
	for _, nome := range SiglaParaNome {
		if strings.EqualFold(cidade, nome) {
			return nome, true
		}
	}
	// 3. Compatibilidade com nomes legados de testes anteriores
	nomesLegados := []string{"Minas", "Santo Amaro"}
	for _, nome := range nomesLegados {
		if strings.EqualFold(cidade, nome) {
			return nome, true
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
	URL_Empresa    string   `json:"url_empresa"`
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
