package consorcio

import (
	"VaiJunto/internal/modelos"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cliente HTTP com Timeout configurado (Exigência do PBL para evitar travamentos)
var httpClient = &http.Client{
	Timeout: 5 * time.Second,
}

// BuscarTrechosRemotos consulta trechos disponíveis de uma empresa parceira (Scatter-Gather)
func BuscarTrechosRemotos(urlEmpresa string, datas ...string) ([]modelos.Trecho, error) {
	urlEmpresa = strings.TrimRight(urlEmpresa, "/")
	endpoint := urlEmpresa + "/api/trechos/disponiveis"

	var dataInicio, dataFim string
	if len(datas) > 0 {
		dataInicio = datas[0]
	}
	if len(datas) > 1 {
		dataFim = datas[1]
	}

	var params []string
	if dataInicio != "" {
		params = append(params, "data_inicio="+url.QueryEscape(dataInicio))
	}
	if dataFim != "" {
		params = append(params, "data_fim="+url.QueryEscape(dataFim))
	}
	if len(params) > 0 {
		endpoint += "?" + strings.Join(params, "&")
	}

	fmt.Printf("[CLIENTE-REST] Disparando Scatter-Gather para %s (Filtro: %s a %s)\n", endpoint, dataInicio, dataFim)
	resposta, err := httpClient.Get(endpoint)
	if err != nil {
		fmt.Printf("[CLIENTE-REST] Erro de conexão com %s: %v\n", urlEmpresa, err)
		return nil, fmt.Errorf("falha ao consultar %s: %w", urlEmpresa, err)
	}
	defer resposta.Body.Close()

	if resposta.StatusCode != http.StatusOK {
		fmt.Printf("[CLIENTE-REST] Parceiro %s respondeu com status %d\n", urlEmpresa, resposta.StatusCode)
		return nil, fmt.Errorf("empresa %s respondeu com status %d", urlEmpresa, resposta.StatusCode)
	}

	var trechos []modelos.Trecho
	if err := json.NewDecoder(resposta.Body).Decode(&trechos); err != nil {
		return nil, fmt.Errorf("falha ao decodificar trechos de %s: %w", urlEmpresa, err)
	}

	// Garante que todos os trechos recebam a URL da empresa fornecedora
	for i := range trechos {
		trechos[i].URL_Empresa = urlEmpresa
	}

	fmt.Printf("[CLIENTE-REST] Sucesso: %d trecho(s) obtido(s) de %s\n", len(trechos), urlEmpresa)
	return trechos, nil
}

// PrepararReservaRemota executa a Fase 1 do 2PC em uma empresa parceira recebendo um Trecho
func PrepararReservaRemota(urlEmpresa string, trecho modelos.Trecho) bool {
	return PrepararReservaRemotaPorID(urlEmpresa, trecho.CaronaID, trecho.ID)
}

// PrepararReservaRemotaPorID executa a Fase 1 do 2PC passando os IDs diretamente e carimbo de Lamport
func PrepararReservaRemotaPorID(urlEmpresa, caronaID, trechoID string, extras ...any) bool {
	urlEmpresa = strings.TrimRight(urlEmpresa, "/")
	endpoint := urlEmpresa + "/api/reservas/preparar"

	var relogio uint64
	var idOrigem string
	if len(extras) > 0 {
		if r, ok := extras[0].(uint64); ok {
			relogio = r
		}
	}
	if len(extras) > 1 {
		if id, ok := extras[1].(string); ok {
			idOrigem = id
		}
	}

	if relogio == 0 {
		relogio = Tick()
	}
	if idOrigem == "" {
		idOrigem = MeuID
	}

	payload := PedidoReserva{
		CaronaID:      caronaID,
		TrechoID:      trechoID,
		RelogioLogico: relogio,
		IDOrigem:      idOrigem,
	}
	fmt.Printf("[CLIENTE-REST 2PC] Enviando Fase 1 (Preparar) para %s [Carona: %s, Trecho: %s, Lamport: %d, Origem: %s]...\n",
		urlEmpresa, caronaID, trechoID, relogio, idOrigem)
	ok := dispararPOST(endpoint, payload)
	fmt.Printf("[CLIENTE-REST 2PC] Resposta Fase 1 de %s: sucesso=%v\n", urlEmpresa, ok)
	return ok
}

// EfetivarReservaRemota executa a Fase 2 (Commit) do 2PC em uma empresa parceira recebendo um Trecho
func EfetivarReservaRemota(urlEmpresa string, trecho modelos.Trecho, passageiroID string, extras ...string) bool {
	return EfetivarReservaRemotaPorID(urlEmpresa, trecho.CaronaID, trecho.ID, passageiroID, extras...)
}

// EfetivarReservaRemotaPorID executa a Fase 2 (Commit) do 2PC passando os IDs diretamente e nome do passageiro se houver
func EfetivarReservaRemotaPorID(urlEmpresa, caronaID, trechoID, passageiroID string, extras ...string) bool {
	urlEmpresa = strings.TrimRight(urlEmpresa, "/")
	endpoint := urlEmpresa + "/api/reservas/efetivar"

	var passNome string
	if len(extras) > 0 {
		passNome = extras[0]
	}

	payload := PedidoReserva{
		CaronaID:       caronaID,
		TrechoID:       trechoID,
		PassageiroID:   passageiroID,
		PassageiroNome: passNome,
	}
	fmt.Printf("[CLIENTE-REST 2PC] Enviando Fase 2 (Commit/Efetivar) para %s [Carona: %s, Trecho: %s, Passageiro: %s (%s)]...\n",
		urlEmpresa, caronaID, trechoID, passageiroID, passNome)
	ok := dispararPOST(endpoint, payload)
	fmt.Printf("[CLIENTE-REST 2PC] Resposta Fase 2 de %s: sucesso=%v\n", urlEmpresa, ok)
	return ok
}

// CancelarReservaRemota executa a Fase 2 (Rollback/Abort) do 2PC em uma empresa parceira recebendo um Trecho
func CancelarReservaRemota(urlEmpresa string, trecho modelos.Trecho, extras ...string) bool {
	return CancelarReservaRemotaPorID(urlEmpresa, trecho.CaronaID, trecho.ID, extras...)
}

// CancelarReservaRemotaPorID executa a Fase 2 (Rollback/Abort) do 2PC passando os IDs diretamente e passageiro se houver
func CancelarReservaRemotaPorID(urlEmpresa, caronaID, trechoID string, extras ...string) bool {
	urlEmpresa = strings.TrimRight(urlEmpresa, "/")
	endpoint := urlEmpresa + "/api/reservas/cancelar"

	var passID string
	var passNome string
	if len(extras) > 0 {
		passID = extras[0]
	}
	if len(extras) > 1 {
		passNome = extras[1]
	}

	payload := PedidoReserva{
		CaronaID:       caronaID,
		TrechoID:       trechoID,
		PassageiroID:   passID,
		PassageiroNome: passNome,
	}
	fmt.Printf("[CLIENTE-REST 2PC] Enviando ROLLBACK (Cancelar) para %s [Carona: %s, Trecho: %s, Passageiro: %s (%s)]...\n",
		urlEmpresa, caronaID, trechoID, passID, passNome)
	ok := dispararPOST(endpoint, payload)
	fmt.Printf("[CLIENTE-REST 2PC] Resposta Rollback de %s: sucesso=%v\n", urlEmpresa, ok)
	return ok
}

// NotificarCancelamentoCaronaRemota avisa as empresas parceiras que uma carona foi cancelada pelo motorista (cascata)
func NotificarCancelamentoCaronaRemota(urlEmpresa, caronaID, motoristaID string) bool {
	urlEmpresa = strings.TrimRight(urlEmpresa, "/")
	endpoint := urlEmpresa + "/api/caronas/cancelada"

	payload := map[string]string{
		"carona_id":    caronaID,
		"motorista_id": motoristaID,
	}
	fmt.Printf("[CLIENTE-REST CASCATA] Notificando cancelamento da carona %s para parceiro %s...\n", caronaID, urlEmpresa)
	ok := dispararPOST(endpoint, payload)
	fmt.Printf("[CLIENTE-REST CASCATA] Resposta de %s: sucesso=%v\n", urlEmpresa, ok)
	return ok
}

// Função auxiliar genérica para disparar requisições POST com JSON
func dispararPOST(endpoint string, dados any) bool {
	corpoJSON, err := json.Marshal(dados)
	if err != nil {
		return false
	}

	resposta, err := httpClient.Post(endpoint, "application/json", bytes.NewBuffer(corpoJSON))
	if err != nil {
		return false // Erro de rede ou timeout
	}
	defer resposta.Body.Close()

	return resposta.StatusCode == http.StatusOK
}
