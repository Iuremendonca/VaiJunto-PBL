package consorcio

import (
	"VaiJunto/internal/estado"
	"VaiJunto/internal/modelos"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// MinhaURL guarda o endereço HTTP base desta empresa para os outros servidores (ex.: "http://localhost:8082")
var MinhaURL string

// Parceiros armazena a lista de URLs dos servidores consorciados
var Parceiros []string

// PedidoReserva representa o corpo JSON enviado nas operações do 2PC e Lamport
type PedidoReserva struct {
	CaronaID       string `json:"carona_id"`
	TrechoID       string `json:"trecho_id"`
	ID             string `json:"id,omitempty"` // suporte caso o cliente envie um modelos.Trecho
	PassageiroID   string `json:"passageiro_id,omitempty"`
	PassageiroNome string `json:"passageiro_nome,omitempty"`
	RelogioLogico  uint64 `json:"relogio_logico,omitempty"`
	IDOrigem       string `json:"id_origem,omitempty"`
}

// ObterTrechoID retorna o ID do trecho seja pelo campo trecho_id ou id
func (p *PedidoReserva) ObterTrechoID() string {
	if p.TrechoID != "" {
		return p.TrechoID
	}
	return p.ID
}

// IniciarAPI liga o servidor HTTP em uma porta dedicada
func IniciarAPI(porta string) {
	if MinhaURL == "" {
		MinhaURL = os.Getenv("URL_EMPRESA")
		if MinhaURL == "" {
			MinhaURL = "http://localhost:" + porta
		}
	}

	carregarParceiros()

	// Conecta os hooks no pacote estado para orquestração distribuída
	estado.MinhaURL = MinhaURL
	estado.EmpresasParceiras = Parceiros
	estado.BuscarTrechosRemotosFunc = func(urlEmpresa, dataInicio, dataFim string) ([]modelos.Trecho, error) {
		return BuscarTrechosRemotos(urlEmpresa, dataInicio, dataFim)
	}
	estado.PrepararReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID string, relogio uint64, idOrigem string) bool {
		return PrepararReservaRemotaPorID(urlEmpresa, caronaID, trechoID, relogio, idOrigem)
	}
	estado.EfetivarReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID, passageiroID string, extras ...string) bool {
		return EfetivarReservaRemotaPorID(urlEmpresa, caronaID, trechoID, passageiroID, extras...)
	}
	estado.CancelarReservaRemotaFunc = func(urlEmpresa, caronaID, trechoID string, extras ...string) bool {
		return CancelarReservaRemotaPorID(urlEmpresa, caronaID, trechoID, extras...)
	}
	estado.NotificarCancelamentoCaronaFunc = NotificarCancelamentoCaronaRemota

	mux := http.NewServeMux() // Cria um roteador HTTP

	// Define as rotas da API REST
	mux.HandleFunc("/api/trechos/disponiveis", handleTrechosDisponiveis)
	mux.HandleFunc("/api/reservas/preparar", handlePreparar)
	mux.HandleFunc("/api/reservas/efetivar", handleEfetivar)
	mux.HandleFunc("/api/reservas/cancelar", handleCancelar)
	mux.HandleFunc("/api/caronas/cancelada", handleCaronaCancelada)

	// Inicia o worker de recuperação e retentativa em background (garante entrega de cancelamentos)
	go IniciarWorkerRetentativas()

	fmt.Printf("[REST] Servidor do Consórcio rodando na porta %s (URL pública: %s | Parceiros: %v)\n",
		porta, MinhaURL, Parceiros)
	if err := http.ListenAndServe(":"+porta, mux); err != nil {
		fmt.Printf("[REST] Erro no servidor HTTP: %v\n", err)
	}
}

func carregarParceiros() {
	Parceiros = nil
	raw := os.Getenv("EMPRESAS_PARCEIRAS")
	if raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				Parceiros = append(Parceiros, p)
			}
		}
		return
	}

	// Fallback padrão para desenvolvimento local entre as 3 empresas
	// se EMPRESAS_PARCEIRAS não for informada explicitamente
	portasPadrao := []string{"8082", "9082", "10082"}
	for _, p := range portasPadrao {
		urlCandidata := "http://localhost:" + p
		if urlCandidata != MinhaURL && !strings.HasSuffix(MinhaURL, ":"+p) {
			Parceiros = append(Parceiros, urlCandidata)
		}
	}
}

// Handler para Scatter-Gather: devolve todos os trechos com vagas desta empresa
func handleTrechosDisponiveis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	dataInicio := r.URL.Query().Get("data_inicio")
	dataFim := r.URL.Query().Get("data_fim")

	trechos := estado.ListarTrechosDisponiveisLocais(MinhaURL, dataInicio, dataFim)
	fmt.Printf("[REST %s] GET /api/trechos/disponiveis atendido -> enviando %d trecho(s) ao parceiro (Filtro: %s a %s)\n",
		MinhaURL, len(trechos), dataInicio, dataFim)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(trechos)
}

// Handler da Fase 1 (Preparação / Trancar Vaga com Relógio de Lamport)
func handlePreparar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var req PedidoReserva
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	trechoID := req.ObterTrechoID()
	if req.CaronaID == "" || trechoID == "" {
		http.Error(w, "carona_id e trecho_id são obrigatórios", http.StatusBadRequest)
		return
	}

	// Sincroniza o relógio de Lamport local caso tenha vindo na requisição
	if req.RelogioLogico > 0 {
		Atualizar(req.RelogioLogico)
	}

	sucesso := estado.PrepararVaga(req.CaronaID, trechoID, req.RelogioLogico, req.IDOrigem)
	fmt.Printf("[REST %s] FASE 1 (Preparar) -> Carona %s, Trecho %s [Lamport: %d, Origem: %s]: bloqueio_sucesso=%v\n",
		MinhaURL, req.CaronaID, trechoID, req.RelogioLogico, req.IDOrigem, sucesso)

	w.Header().Set("Content-Type", "application/json")
	if sucesso {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":    "vaga_bloqueada",
			"carona_id": req.CaronaID,
			"trecho_id": trechoID,
		})
	} else {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"erro":      "vaga_esgotada_ou_invalida",
			"carona_id": req.CaronaID,
			"trecho_id": trechoID,
		})
	}
}

// Handler da Fase 2 (Efetivar / Confirmar Vaga com Sucesso)
func handleEfetivar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var req PedidoReserva
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	trechoID := req.ObterTrechoID()
	if req.CaronaID == "" || trechoID == "" {
		http.Error(w, "carona_id e trecho_id são obrigatórios", http.StatusBadRequest)
		return
	}

	sucesso := estado.EfetivarVaga(req.CaronaID, trechoID, req.PassageiroID, req.PassageiroNome)
	fmt.Printf("[REST %s] FASE 2 (Efetivar/Commit) -> Carona %s, Trecho %s para Passageiro %s (%s): efetivado=%v\n",
		MinhaURL, req.CaronaID, trechoID, req.PassageiroID, req.PassageiroNome, sucesso)

	w.Header().Set("Content-Type", "application/json")
	if sucesso {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":    "reserva_efetivada",
			"carona_id": req.CaronaID,
			"trecho_id": trechoID,
		})
	} else {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"erro":      "falha_ao_efetivar",
			"carona_id": req.CaronaID,
			"trecho_id": trechoID,
		})
	}
}

// Handler de Abort / Rollback (Cancelar Vaga previamente bloqueada)
func handleCancelar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var req PedidoReserva
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	trechoID := req.ObterTrechoID()
	if req.CaronaID == "" || trechoID == "" {
		http.Error(w, "carona_id e trecho_id são obrigatórios", http.StatusBadRequest)
		return
	}

	estado.CancelarVaga(req.CaronaID, trechoID, req.PassageiroID, req.PassageiroNome)
	fmt.Printf("[REST %s] ROLLBACK (Cancelar) -> Carona %s, Trecho %s [Passageiro: %s (%s)]: vaga devolvida e passageiro removido\n",
		MinhaURL, req.CaronaID, trechoID, req.PassageiroID, req.PassageiroNome)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "reserva_cancelada",
		"carona_id": req.CaronaID,
		"trecho_id": trechoID,
	})
}

// Handler para cancelamento em cascata avisado por empresa parceira do consórcio
func handleCaronaCancelada(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CaronaID    string `json:"carona_id"`
		MotoristaID string `json:"motorista_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	if req.CaronaID == "" {
		http.Error(w, "carona_id é obrigatório", http.StatusBadRequest)
		return
	}

	afetados := estado.ProcessarCancelamentoRemotoCarona(req.CaronaID, req.MotoristaID)
	fmt.Printf("[REST %s] CASCATA: Carona parceira %s foi cancelada -> %d itinerário(s) local(is) cancelado(s)\n",
		MinhaURL, req.CaronaID, afetados)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":                "processado",
		"carona_id":             req.CaronaID,
		"itinerarios_afetados": afetados,
	})
}
