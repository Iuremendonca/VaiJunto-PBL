package estado

import (
	"VaiJunto/internal/modelos"
	"VaiJunto/internal/protocolo"
	"sort"
	"strings"
)

// Limites da busca (evitam que um grafo grande trave o servidor)
const (
	MaxResultadosBusca    = 30
	maxCaminhosExplorados = 20000
)

//  Monta todos os itinerários possíveis entre origem e destino.

func BuscarItinerarios(filtro modelos.FiltroBusca) ([][]modelos.Trecho, error) {
	origem, okOrigem := modelos.NormalizarCidade(filtro.Origem)
	destino, okDestino := modelos.NormalizarCidade(filtro.Destino)
	if !okOrigem || !okDestino {
		return nil, falha(protocolo.StatusRequisicaoInvalida, "origem ou destino contêm cidades não suportadas pelo sistema")
	}
	if origem == destino {
		return nil, falha(protocolo.StatusRequisicaoInvalida, "a origem e o destino devem ser diferentes")
	}
	if filtro.Data == "" && filtro.HorarioMinimo == "" {
		return nil, falha(protocolo.StatusRequisicaoInvalida, "informe a data da viagem")
	}
	if filtro.Data != "" {
		if !modelos.FormatoExato(modelos.FormatoData, filtro.Data) {
			return nil, falha(protocolo.StatusRequisicaoInvalida, "data inválida: use o formato AAAA-MM-DD")
		}
	}
	if filtro.HorarioMinimo != "" && !dataHoraValida(filtro.HorarioMinimo) {
		return nil, falha(protocolo.StatusRequisicaoInvalida, "horário mínimo inválido: use o formato AAAA-MM-DD HH:MM")
	}

	// Nunca sugere um trecho que já partiu
	limite := agora()
	if filtro.HorarioMinimo > limite {
		limite = filtro.HorarioMinimo
	}

	// 1. Foto dos trechos com vaga. Cada carona fica travada só durante a cópia,
	// e a busca em si roda sem nenhum cadeado.
	saindoDe := make(map[string][]modelos.Trecho)
	Caronas.Range(func(_, value any) bool {
		reg := value.(*RegistroCarona)
		reg.Lock()
		if !reg.Cancelada {
			for _, t := range reg.Dados.Trechos {
				// Trechos com data fora do formato (dados antigos) são ignorados
				if t.VagasLivres > 0 && t.HorarioSaida >= limite && dataHoraValida(t.HorarioSaida) && dataHoraValida(t.HorarioChegada) {
					t.Passageiros = nil // o passageiro não vê quem mais vai no carro
					saindoDe[t.Origem] = append(saindoDe[t.Origem], t)
				}
			}
		}
		reg.Unlock()
		return true
	})
	for cidade := range saindoDe {
		lista := saindoDe[cidade]
		sort.Slice(lista, func(i, j int) bool { return lista[i].HorarioSaida < lista[j].HorarioSaida })
	}

	//  BFS: cada item da fila é um caminho parcial
	var fila [][]modelos.Trecho
	for _, t := range saindoDe[origem] {
		if filtro.Data != "" && !strings.HasPrefix(t.HorarioSaida, filtro.Data+" ") {
			continue
		}
		fila = append(fila, []modelos.Trecho{t})
	}

	var resultados [][]modelos.Trecho
	explorados := 0
	for len(fila) > 0 && explorados < maxCaminhosExplorados {
		caminho := fila[0]
		fila = fila[1:] //tira o primeiro elemento da fila
		explorados++

		ultimo := caminho[len(caminho)-1]
		if ultimo.Destino == destino {
			resultados = append(resultados, caminho)
			continue
		}
		if len(caminho) >= modelos.MaxTrechosPorItinerario {
			continue
		}

		for _, proximo := range saindoDe[ultimo.Destino] {
			if !encadeia(ultimo, proximo) || passaPor(caminho, proximo.Destino) {
				continue
			}
			novoCaminho := make([]modelos.Trecho, len(caminho), len(caminho)+1)
			copy(novoCaminho, caminho)
			fila = append(fila, append(novoCaminho, proximo))
		}
	}

	// Ordena as opções
	sort.SliceStable(resultados, func(i, j int) bool {
		a, b := resultados[i], resultados[j]
		if trocasA, trocasB := modelos.Baldeacoes(a), modelos.Baldeacoes(b); trocasA != trocasB {
			return trocasA < trocasB
		}
		if chegadaA, chegadaB := a[len(a)-1].HorarioChegada, b[len(b)-1].HorarioChegada; chegadaA != chegadaB {
			return chegadaA < chegadaB
		}
		return modelos.ValorTotal(a) < modelos.ValorTotal(b)
	})

	if len(resultados) > MaxResultadosBusca {
		resultados = resultados[:MaxResultadosBusca]
	}
	return resultados, nil
}

func passaPor(caminho []modelos.Trecho, cidade string) bool {
	if caminho[0].Origem == cidade {
		return true
	}
	for _, t := range caminho {
		if t.Destino == cidade {
			return true
		}
	}
	return false
}
