// Teste automatizado de carga e corretude do VaiJunto.
//
// Sobe dezenas/centenas de clientes TCP simultâneos contra um servidor em execução,
// todos disputando os mesmos trechos, e verifica pelo próprio protocolo que:
//   - nenhum assento é vendido duas vezes (passageiros por trecho <= vagas do trecho);
//   - nenhum itinerário é confirmado pela metade (ou tem todos os trechos, ou não existe);
//   - o cancelamento em cascata de um motorista no meio da disputa não deixa lixo;
//   - carrinhos abandonados não deixam assentos bloqueados para sempre;
//   - e mede o tempo de resposta de cada operação sob carga.
//
// Uso:  go run ./cmd/TesteCarga -servidor localhost:8081 -passageiros 200
//
// ATENÇÃO: o teste cria usuários e caronas de verdade no servidor. Use um servidor de testes.
package main

import (
	"VaiJunto/internal/modelos"
	"VaiJunto/internal/protocolo"
	"bufio"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const prazoRequisicao = 30 * time.Second

// ===================== MÉTRICAS =====================

type metricas struct {
	mu         sync.Mutex
	latencias  map[string][]time.Duration
	falhasRede int
}

func (m *metricas) registrar(acao string, duracao time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.falhasRede++
		return
	}
	m.latencias[acao] = append(m.latencias[acao], duracao)
}

func percentil(ordenadas []time.Duration, p float64) time.Duration {
	if len(ordenadas) == 0 {
		return 0
	}
	indice := int(float64(len(ordenadas)-1) * p)
	return ordenadas[indice]
}

func (m *metricas) relatorio(duracaoDisputa time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var acoes []string
	var todas []time.Duration
	for acao, lista := range m.latencias {
		acoes = append(acoes, acao)
		todas = append(todas, lista...)
	}
	sort.Strings(acoes)

	fmt.Printf("\n%-30s %6s %10s %10s %10s %10s %10s\n", "OPERAÇÃO", "QTD", "MÉDIA", "P50", "P95", "P99", "MÁX")
	linha := func(nome string, lista []time.Duration) {
		ordenadas := append([]time.Duration(nil), lista...)
		sort.Slice(ordenadas, func(i, j int) bool { return ordenadas[i] < ordenadas[j] })
		var soma time.Duration
		for _, d := range ordenadas {
			soma += d
		}
		media := time.Duration(0)
		if len(ordenadas) > 0 {
			media = soma / time.Duration(len(ordenadas))
		}
		fmt.Printf("%-30s %6d %10s %10s %10s %10s %10s\n", nome, len(ordenadas),
			arred(media), arred(percentil(ordenadas, 0.50)), arred(percentil(ordenadas, 0.95)),
			arred(percentil(ordenadas, 0.99)), arred(percentil(ordenadas, 1)))
	}
	for _, acao := range acoes {
		linha(acao, m.latencias[acao])
	}
	linha("TODAS", todas)

	fmt.Printf("\nRequisições com falha de rede: %d\n", m.falhasRede)
	if duracaoDisputa > 0 {
		fmt.Printf("Duração da disputa: %s\n", arred(duracaoDisputa))
	}
}

func arred(d time.Duration) string {
	return d.Round(10 * time.Microsecond).String()
}

// ===================== CLIENTE =====================

type cliente struct {
	conn    net.Conn
	leitor  *bufio.Reader
	medidas *metricas
}

func conectar(endereco string, medidas *metricas) (*cliente, error) {
	conn, err := net.DialTimeout("tcp", endereco, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return &cliente{conn: conn, leitor: bufio.NewReader(conn), medidas: medidas}, nil
}

func (c *cliente) requisitar(acao string, payload any) (protocolo.MensagemResposta, error) {
	inicio := time.Now()
	resposta, err := protocolo.Requisitar(c.conn, c.leitor, acao, payload, prazoRequisicao)
	c.medidas.registrar(acao, time.Since(inicio), err)
	return resposta, err
}

func (c *cliente) entrar(id, nome, tipo string) error {
	if _, err := c.requisitar("CADASTRAR", modelos.Usuario{ID: id, Nome: nome, Senha: "123", Tipo: tipo}); err != nil {
		return err
	}
	resp, err := c.requisitar("LOGIN", modelos.Usuario{ID: id, Senha: "123", Tipo: tipo})
	if err != nil {
		return err
	}
	if resp.Status != protocolo.StatusOK {
		return fmt.Errorf("login de %s falhou: %s", id, resp.Mensagem)
	}
	return nil
}

// ===================== VERIFICAÇÕES =====================

var falhas int

func checar(ok bool, formato string, args ...any) {
	if ok {
		fmt.Printf("  ✔ %s\n", fmt.Sprintf(formato, args...))
		return
	}
	falhas++
	fmt.Printf("  ✘ FALHOU: %s\n", fmt.Sprintf(formato, args...))
}

// Divide um itinerário em pedaços do mesmo carro (como o cliente passageiro faz)
func pedacosPorCarro(itinerario []modelos.Trecho) [][]modelos.Trecho {
	var pedacos [][]modelos.Trecho
	inicio := 0
	for i := 1; i <= len(itinerario); i++ {
		if i == len(itinerario) || itinerario[i].CaronaID != itinerario[inicio].CaronaID {
			pedacos = append(pedacos, itinerario[inicio:i])
			inicio = i
		}
	}
	return pedacos
}

func idsDosTrechos(trechos []modelos.Trecho) string {
	var ids []string
	for _, t := range trechos {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ",")
}

// ===================== TESTE =====================

type resultadoPassageiro struct {
	cli       *cliente
	id        string
	escolhido []modelos.Trecho
	comprou   bool
	etapa     string // onde parou: "rede", "busca", "reserva", "confirmação", "ok"
}

func main() {
	padrao := os.Getenv("SERVER_URL")
	if padrao == "" {
		padrao = "localhost:8081"
	}
	endereco := flag.String("servidor", padrao, "endereço do servidor (host:porta)")
	totalPassageiros := flag.Int("passageiros", 200, "passageiros simultâneos disputando os trechos")
	vagas := flag.Int("vagas", 5, fmt.Sprintf("vagas por trecho nas caronas do teste (1 a %d)", modelos.MaxAssentos))
	cancelar := flag.Bool("cancelar", true, "um motorista cancela a carona no meio da disputa (testa a cascata)")
	espera := flag.Duration("espera", 65*time.Second, "espera para os carrinhos abandonados expirarem antes da última verificação (0 = pula)")
	flag.Parse()

	if *vagas < 1 || *vagas > modelos.MaxAssentos {
		fmt.Printf("-vagas deve ser entre 1 e %d\n", modelos.MaxAssentos)
		os.Exit(2)
	}

	medidas := &metricas{latencias: make(map[string][]time.Duration)}
	prefixo := fmt.Sprintf("t%d_", time.Now().UnixNano()%1_000_000_000) // IDs únicos por execução
	dia := time.Now().AddDate(0, 0, 30).Format(modelos.FormatoData)
	hora := func(hhmm string) string { return dia + " " + hhmm }

	fmt.Printf("=== TESTE DE CARGA VAIJUNTO ===\nServidor: %s | Passageiros: %d | Vagas por trecho: %d | Data das viagens: %s\n",
		*endereco, *totalPassageiros, *vagas, dia)

	// Confere se o servidor está acessível antes de começar
	if teste, err := conectar(*endereco, medidas); err != nil {
		fmt.Printf(" Não foi possível conectar ao servidor %s: %v\n", *endereco, err)
		os.Exit(1)
	} else {
		teste.conn.Close()
	}

	// ---------------- FASE 1 ----------------
	fmt.Println("\n=== FASE 1: 100 clientes cadastram o MESMO usuário ao mesmo tempo ===")
	{
		var wg sync.WaitGroup
		var mu sync.Mutex
		criados, recusados := 0, 0
		largada := make(chan struct{})
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := conectar(*endereco, medidas)
				if err != nil {
					return
				}
				defer c.conn.Close()
				<-largada
				resp, err := c.requisitar("CADASTRAR", modelos.Usuario{ID: prefixo + "duplicado", Nome: "Dup", Senha: "1", Tipo: modelos.TipoPassageiro})
				if err != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if resp.Status == protocolo.StatusCriado {
					criados++
				} else if resp.Status == protocolo.StatusConflito {
					recusados++
				}
			}()
		}
		close(largada)
		wg.Wait()
		checar(criados == 1 && recusados == 99, "exatamente 1 cadastro aceito e 99 recusados (aceitos=%d, recusados=%d)", criados, recusados)
	}

	// ---------------- FASE 2 ----------------
	fmt.Println("\n=== FASE 2: motoristas publicam as caronas disputadas ===")
	// A: Feira -> Salvador           (motorista 1)
	// B: Salvador -> Aracaju         (motorista 2)
	// C: Feira -> Salvador -> Aracaju (motorista 3, com parada; será cancelada se -cancelar)
	// Itinerários possíveis de Feira a Aracaju: C direto | A + B | C(1º trecho) + B
	type motorista struct {
		cli    *cliente
		id     string
		carona modelos.Carona
	}
	rotas := []modelos.Carona{
		{Origem: "Feira de Santana", Destino: "Salvador", Assentos: *vagas, Trechos: []modelos.Trecho{
			{Origem: "Feira de Santana", Destino: "Salvador", Valor: 30, HorarioSaida: hora("08:00"), HorarioChegada: hora("10:00")}}},
		{Origem: "Salvador", Destino: "Aracaju", Assentos: *vagas, Trechos: []modelos.Trecho{
			{Origem: "Salvador", Destino: "Aracaju", Valor: 60, HorarioSaida: hora("10:30"), HorarioChegada: hora("14:00")}}},
		{Origem: "Feira de Santana", Destino: "Aracaju", Assentos: *vagas, Trechos: []modelos.Trecho{
			{Origem: "Feira de Santana", Destino: "Salvador", Valor: 25, HorarioSaida: hora("07:00"), HorarioChegada: hora("09:00")},
			{Origem: "Salvador", Destino: "Aracaju", Valor: 55, HorarioSaida: hora("09:30"), HorarioChegada: hora("13:00")}}},
	}
	var motoristas []*motorista
	for i, rota := range rotas {
		id := fmt.Sprintf("%smotorista%d", prefixo, i+1)
		c, err := conectar(*endereco, medidas)
		if err != nil {
			fmt.Println(" Não foi possível conectar ao servidor:", err)
			os.Exit(1)
		}
		if err := c.entrar(id, fmt.Sprintf("Motorista %d", i+1), modelos.TipoMotorista); err != nil {
			fmt.Println("", err)
			os.Exit(1)
		}
		resp, err := c.requisitar("PUBLICAR_CARONA", rota)
		if err != nil || resp.Status != protocolo.StatusCriado {
			fmt.Printf(" Falha ao publicar carona: %v %s\n", err, resp.Mensagem)
			os.Exit(1)
		}
		m := &motorista{cli: c, id: id}
		resp.Decodificar(&m.carona)
		motoristas = append(motoristas, m)
		fmt.Printf("  %s publicou %s -> %s (%d trecho(s))\n", id, m.carona.Origem, m.carona.Destino, len(m.carona.Trechos))
	}
	caronaCancelada := motoristas[2].carona.ID

	// ---------------- FASE 3 ----------------
	fmt.Printf("\n=== FASE 3: %d passageiros disputam os mesmos trechos ===\n", *totalPassageiros)
	resultados := make([]*resultadoPassageiro, *totalPassageiros)
	var prontos sync.WaitGroup
	var terminados sync.WaitGroup
	largada := make(chan struct{})

	for i := range resultados {
		r := &resultadoPassageiro{id: fmt.Sprintf("%spassageiro%d", prefixo, i), etapa: "rede"}
		resultados[i] = r
		prontos.Add(1)
		terminados.Add(1)
		go func() {
			defer terminados.Done()
			c, err := conectar(*endereco, medidas)
			if err == nil {
				err = c.entrar(r.id, "Passageiro", modelos.TipoPassageiro)
			}
			prontos.Done()
			if err != nil {
				return
			}
			r.cli = c
			<-largada // todos começam juntos

			// Busca os itinerários e escolhe um ao acaso (cada passageiro tem sua preferência)
			r.etapa = "busca"
			resp, err := c.requisitar("BUSCAR_ITINERARIOS", modelos.FiltroBusca{Origem: "Feira de Santana", Destino: "Aracaju", Data: dia})
			if err != nil || resp.Status != protocolo.StatusOK {
				return
			}
			var itinerarios [][]modelos.Trecho
			resp.Decodificar(&itinerarios)
			r.escolhido = itinerarios[rand.IntN(len(itinerarios))]

			// Reserva carro por carro, como o cliente passageiro faz
			r.etapa = "reserva"
			for j, pedaco := range pedacosPorCarro(r.escolhido) {
				resp, err := c.requisitar("ADICIONAR_CARRINHO", modelos.CarrinhoPayload{Trechos: pedaco, Continuacao: j > 0})
				if err != nil || resp.Status != protocolo.StatusOK {
					return // desiste: o que já foi reservado expira sozinho
				}
			}

			r.etapa = "confirmação"
			resp, err = c.requisitar("EFETIVAR_ITINERARIO", nil)
			if err != nil || resp.Status != protocolo.StatusOK {
				return
			}
			r.comprou = true
			r.etapa = "ok"
		}()
	}

	prontos.Wait()
	inicioDisputa := time.Now()
	close(largada)

	var cancelamento sync.WaitGroup
	if *cancelar {
		cancelamento.Add(1)
		go func() {
			defer cancelamento.Done()
			time.Sleep(5 * time.Millisecond) // no meio da disputa
			resp, err := motoristas[2].cli.requisitar("CANCELAR_CARONA", modelos.CancelamentoPayload{ViagemID: caronaCancelada})
			if err == nil {
				fmt.Printf("  (motorista 3 cancelou a carona durante a disputa: %s)\n", resp.Mensagem)
			}
		}()
	}

	terminados.Wait()
	duracaoDisputa := time.Since(inicioDisputa)
	cancelamento.Wait() // a conexão do motorista 3 é usada de novo na verificação

	etapas := map[string]int{}
	compraram := 0
	for _, r := range resultados {
		etapas[r.etapa]++
		if r.comprou {
			compraram++
		}
	}
	fmt.Printf("  Compras confirmadas: %d | Desistências: %v\n", compraram, etapas)

	// ---------------- FASE 4 ----------------
	fmt.Println("\n=== FASE 4: verificação ===")

	// Estado oficial das caronas, na visão dos motoristas
	oficiais := map[string]modelos.Trecho{}
	consultarCaronas := func() {
		oficiais = map[string]modelos.Trecho{}
		for _, m := range motoristas {
			resp, err := m.cli.requisitar("CONSULTAR_VIAGENS_MOTORISTA", nil)
			if err != nil || resp.Status != protocolo.StatusOK {
				continue
			}
			var caronas []modelos.Carona
			resp.Decodificar(&caronas)
			for _, c := range caronas {
				for _, t := range c.Trechos {
					oficiais[t.ID] = t
				}
			}
		}
	}
	consultarCaronas()

	// Itinerários de cada passageiro
	usoPorTrecho := map[string]int{}
	incompletos, divergentes, fantasmas, perdidosSemMotivo, cascata, comTrechoCancelado := 0, 0, 0, 0, 0, 0
	for _, r := range resultados {
		if r.cli == nil {
			continue
		}
		resp, err := r.cli.requisitar("CONSULTAR_VIAGENS_PASSAGEIRO", nil)
		if err != nil {
			continue
		}
		var itinerarios []modelos.Itinerario
		if resp.Status == protocolo.StatusOK {
			resp.Decodificar(&itinerarios)
		}

		switch {
		case !r.comprou && len(itinerarios) > 0:
			fantasmas++ // compra falhou, mas o itinerário existe
		case r.comprou && len(itinerarios) == 0:
			usaCancelada := false
			for _, t := range r.escolhido {
				usaCancelada = usaCancelada || t.CaronaID == caronaCancelada
			}
			if *cancelar && usaCancelada {
				cascata++ // sumiu porque o motorista 3 cancelou: correto
			} else {
				perdidosSemMotivo++
			}
		}

		for _, it := range itinerarios {
			t := it.Trechos
			completo := len(t) > 0 && t[0].Origem == "Feira de Santana" && t[len(t)-1].Destino == "Aracaju"
			for i := 1; completo && i < len(t); i++ {
				completo = t[i].Origem == t[i-1].Destino && t[i].HorarioSaida >= t[i-1].HorarioChegada
			}
			if !completo || modelos.ValorTotal(t) != it.ValorTotal {
				incompletos++
			}
			if idsDosTrechos(t) != idsDosTrechos(r.escolhido) {
				divergentes++
			}
			for _, trecho := range t {
				usoPorTrecho[trecho.ID]++
				if trecho.CaronaID == caronaCancelada && *cancelar {
					comTrechoCancelado++
				}
			}
		}
	}

	checar(incompletos == 0, "nenhum itinerário pela metade (todos vão de Feira de Santana a Aracaju, encadeados) — incompletos: %d", incompletos)
	checar(divergentes == 0, "cada itinerário tem exatamente os trechos que o passageiro escolheu — divergentes: %d", divergentes)
	checar(fantasmas == 0, "nenhuma compra que falhou gerou itinerário — fantasmas: %d", fantasmas)
	checar(perdidosSemMotivo == 0, "nenhuma compra confirmada sumiu sem motivo — perdidas: %d (canceladas em cascata: %d)", perdidosSemMotivo, cascata)
	if *cancelar {
		checar(comTrechoCancelado == 0, "nenhum itinerário usa a carona cancelada")
		_, aindaExiste := oficiais[motoristas[2].carona.Trechos[0].ID]
		checar(!aindaExiste, "a carona cancelada não existe mais no servidor")
	}

	vendasDuplicadas, listasDivergentes, repetidos := 0, 0, 0
	for id, t := range oficiais {
		if len(t.Passageiros) > t.VagasTotais {
			vendasDuplicadas++
		}
		if len(t.Passageiros) != usoPorTrecho[id] {
			listasDivergentes++
		}
		vistos := map[string]bool{}
		for _, p := range t.Passageiros {
			if vistos[p] {
				repetidos++
			}
			vistos[p] = true
		}
		fmt.Printf("    trecho %s -> %s (%s): %d/%d assentos vendidos\n", t.Origem, t.Destino, t.MotoristaID, len(t.Passageiros), t.VagasTotais)
	}
	checar(vendasDuplicadas == 0, "nenhum assento vendido duas vezes (passageiros <= vagas em todos os trechos)")
	checar(listasDivergentes == 0, "listas de embarque dos motoristas batem com os itinerários dos passageiros")
	checar(repetidos == 0, "nenhum passageiro aparece duas vezes no mesmo trecho")

	if *espera > 0 {
		fmt.Printf("\n  Aguardando %s para os carrinhos abandonados expirarem...\n", *espera)
		time.Sleep(*espera)
		consultarCaronas()
		bloqueados := 0
		for _, t := range oficiais {
			if t.VagasLivres != t.VagasTotais-len(t.Passageiros) {
				bloqueados++
				fmt.Printf("    trecho %s: livres=%d, esperado=%d\n", t.ID, t.VagasLivres, t.VagasTotais-len(t.Passageiros))
			}
		}
		checar(bloqueados == 0, "nenhum assento ficou bloqueado por reserva não concluída (vagas livres = vagas - vendidos)")
	}

	for _, r := range resultados {
		if r.cli != nil {
			r.cli.conn.Close()
		}
	}
	for _, m := range motoristas {
		m.cli.conn.Close()
	}

	fmt.Println("\n=== TEMPO DE RESPOSTA SOB CARGA ===")
	medidas.relatorio(duracaoDisputa)

	if falhas > 0 {
		fmt.Printf("\n %d verificação(ões) falharam\n", falhas)
		os.Exit(1)
	}
	fmt.Println("\n Todas as verificações passaram")
}
