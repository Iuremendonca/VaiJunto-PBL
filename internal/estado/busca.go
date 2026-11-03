package estado

import (
	"VaiJunto/internal/modelos"
	"VaiJunto/internal/protocolo"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limites da busca (evitam que um grafo grande trave o servidor)
const (
	MaxResultadosBusca    = 30
	maxCaminhosExplorados = 20000
)

//  Monta todos os itinerários possíveis entre origem e destino (locais e federados).

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

	// Calcula a janela de busca: da data informada até +1 dia (ex: 02/11 a 03/11)
	var dataInicio, dataFim string
	if filtro.Data != "" {
		dataInicio = filtro.Data
		if t, err := time.Parse(modelos.FormatoData, filtro.Data); err == nil {
			dataFim = t.AddDate(0, 0, 1).Format(modelos.FormatoData)
		}
	} else if filtro.HorarioMinimo != "" {
		partes := strings.Split(filtro.HorarioMinimo, " ")
		dataInicio = partes[0]
		if t, err := time.Parse(modelos.FormatoData, dataInicio); err == nil {
			dataFim = t.AddDate(0, 0, 1).Format(modelos.FormatoData)
		}
	}

	// 1. SCATTER-GATHER: Consulta concorrente aos servidores parceiros do consórcio
	var remotos []modelos.Trecho
	var muRemotos sync.Mutex
	var wg sync.WaitGroup

	if BuscarTrechosRemotosFunc != nil && len(EmpresasParceiras) > 0 {
		for _, parceiro := range EmpresasParceiras {
			p := parceiro
			if p == MinhaURL || p == "" {
				continue
			}
			wg.Add(1)
			go func(urlParceiro string) {
				defer wg.Done()
				trechos, err := BuscarTrechosRemotosFunc(urlParceiro, dataInicio, dataFim)
				if err == nil && len(trechos) > 0 {
					muRemotos.Lock()
					remotos = append(remotos, trechos...)
					muRemotos.Unlock()
				}
			}(p)
		}
		wg.Wait()
	}

	// 2. Funde trechos locais com os trechos recebidos dos parceiros
	saindoDe := make(map[string][]modelos.Trecho)

	// Adiciona trechos locais
	locais := ListarTrechosDisponiveisLocais(MinhaURL, dataInicio, dataFim)
	for _, t := range locais {
		if t.HorarioSaida >= limite {
			saindoDe[t.Origem] = append(saindoDe[t.Origem], t)
		}
	}

	// Adiciona trechos dos parceiros
	for _, t := range remotos {
		if t.HorarioSaida >= limite && t.VagasLivres > 0 && dataHoraValida(t.HorarioSaida) && dataHoraValida(t.HorarioChegada) {
			t.Passageiros = nil
			saindoDe[t.Origem] = append(saindoDe[t.Origem], t)
		}
	}

	for cidade := range saindoDe {
		lista := saindoDe[cidade]
		sort.Slice(lista, func(i, j int) bool { return lista[i].HorarioSaida < lista[j].HorarioSaida })
	}

	// 3. BFS sobre o Grafo Temporal Distribuído
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
		fila = fila[1:] // tira o primeiro elemento da fila
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

	// 4. Ordena as opções (menor troca de carro, chegada mais cedo, menor preço)
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

	if len(remotos) > 0 {
		fmt.Printf("[BUSCA DISTRIBUÍDA] %d trechos remotos combinados aos locais -> %d itinerários encontrados\n",
			len(remotos), len(resultados))
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
