package estado

import (
	"VaiJunto/internal/modelos"
	"VaiJunto/internal/protocolo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Trava para busca de carona
type RegistroCarona struct {
	sync.Mutex
	Dados     modelos.Carona
	Cancelada bool // Marcada pelo motorista ao cancelar. Não vai para o JSON (a carona sai do mapa)
}

// Carrinho do passageiro com cadeado próprio (mesmo padrão do RegistroCarona)
type RegistroCarrinho struct {
	sync.Mutex
	Trechos []modelos.Trecho
	Prazo   time.Time // renovado a cada trecho adicionado
	Fechado bool      // true quando o carrinho já foi efetivado, expirou ou foi substituído
}

// Tempo que as vagas ficam presas no carrinho após o último trecho adicionado
var TempoReservaCarrinho = 1 * time.Minute

var Itinerarios sync.Map //Armazena as viagens confirmadas
var Caronas sync.Map     //armazena as caronas criadas

var Usuarios sync.Map

// Mapa que guarda quem está com vagas "presas" no carrinho
// Chave: passageiroID | Valor: *RegistroCarrinho
var CarrinhosPendentes sync.Map

// Controle de gravação de cada arquivo JSON
type arquivoPersistido struct {
	mu       sync.Mutex    // uma gravação por vez NESTE arquivo (
	pedidos  atomic.Uint64 // contador de pedidos de gravação
	gravados uint64        // até qual pedido o arquivo em disco já está atualizado
}

var arquivosPersistidos sync.Map

// Nomes dos arquivos de persistência
const (
	ArquivoUsuarios    = "usuarios.json"
	ArquivoCaronas     = "caronas.json"
	ArquivoItinerarios = "itinerarios.json"
)

// Pasta dos arquivos JSON (variável VAIJUNTO_DADOS; padrão: pasta atual)
func CaminhoArquivo(nome string) string {
	pasta := os.Getenv("VAIJUNTO_DADOS")
	if pasta == "" {
		pasta = "."
	}
	return filepath.Join(pasta, nome)
}

// Erro de regra de negócio com o status que deve voltar para o cliente
type ErroNegocio struct {
	Status   int
	Mensagem string
}

func (e *ErroNegocio) Error() string { return e.Mensagem }

func falha(status int, formato string, args ...any) error {
	return &ErroNegocio{Status: status, Mensagem: fmt.Sprintf(formato, args...)}
}

// Status do erro (ou o padrão, se não for um ErroNegocio)
func StatusDoErro(err error, padrao int) int {
	var e *ErroNegocio
	if errors.As(err, &e) {
		return e.Status
	}
	return padrao
}

// Pega o horario atual para validacões
func agora() string {
	return time.Now().Format(modelos.FormatoDataHora)
}

func dataHoraValida(valor string) bool {
	return modelos.FormatoExato(modelos.FormatoDataHora, valor)
}

// O próximo trecho sai da cidade onde o anterior chega, e depois que ele chega
func encadeia(anterior, proximo modelos.Trecho) bool {
	return anterior.Destino == proximo.Origem && proximo.HorarioSaida >= anterior.HorarioChegada
}

func validarUsuario(u *modelos.Usuario) error {
	u.ID = strings.TrimSpace(u.ID)
	u.Nome = strings.TrimSpace(u.Nome)
	u.Tipo = strings.ToLower(strings.TrimSpace(u.Tipo))

	if u.ID == "" || len(u.ID) > 32 || strings.ContainsAny(u.ID, " \t\r\n") {
		return falha(protocolo.StatusRequisicaoInvalida, "o ID deve ter de 1 a 32 caracteres, sem espaços")
	}
	if u.Senha == "" {
		return falha(protocolo.StatusRequisicaoInvalida, "a senha não pode ser vazia")
	}
	if u.Nome == "" || len(u.Nome) > 60 {
		return falha(protocolo.StatusRequisicaoInvalida, "o nome deve ter de 1 a 60 caracteres")
	}
	if u.Tipo != modelos.TipoMotorista && u.Tipo != modelos.TipoPassageiro {
		return falha(protocolo.StatusRequisicaoInvalida, "o tipo deve ser 'motorista' ou 'passageiro'")
	}
	return nil
}

// Valida a carona e deixa os nomes de cidade e horários no formato oficial
func validarCarona(c *modelos.Carona) error {
	invalido := func(formato string, args ...any) error {
		return falha(protocolo.StatusRequisicaoInvalida, formato, args...)
	}

	origem, okOrigem := modelos.NormalizarCidade(c.Origem)
	destino, okDestino := modelos.NormalizarCidade(c.Destino)
	if !okOrigem || !okDestino {
		return invalido("origem ou destino contêm cidades não suportadas pelo sistema")
	}
	if origem == destino {
		return invalido("a origem e o destino devem ser diferentes")
	}
	c.Origem, c.Destino = origem, destino

	if c.Assentos < 1 || c.Assentos > modelos.MaxAssentos {
		return invalido("a capacidade do veículo deve ser entre 1 e %d assentos", modelos.MaxAssentos)
	}
	if len(c.Trechos) == 0 {
		return invalido("a carona precisa ter pelo menos 1 trecho")
	}
	if len(c.Trechos) > modelos.MaxTrechosPorCarona {
		return invalido("a carona pode ter no máximo %d trechos", modelos.MaxTrechosPorCarona)
	}

	cidadeAtual := origem
	visitadas := map[string]bool{origem: true}
	chegadaAnterior := ""

	for i := range c.Trechos {
		t := &c.Trechos[i]
		n := i + 1

		trechoOrigem, ok1 := modelos.NormalizarCidade(t.Origem)
		trechoDestino, ok2 := modelos.NormalizarCidade(t.Destino)
		if !ok1 || !ok2 {
			return invalido("o trecho %d possui uma cidade inválida", n)
		}
		if trechoOrigem != cidadeAtual {
			return invalido("o trecho %d deve sair de %s (onde o trecho anterior termina)", n, cidadeAtual)
		}
		if visitadas[trechoDestino] {
			return invalido("o trecho %d volta para %s: a rota não pode repetir cidades", n, trechoDestino)
		}
		visitadas[trechoDestino] = true
		t.Origem, t.Destino = trechoOrigem, trechoDestino

		t.HorarioSaida = strings.TrimSpace(t.HorarioSaida)
		t.HorarioChegada = strings.TrimSpace(t.HorarioChegada)
		if !dataHoraValida(t.HorarioSaida) || !dataHoraValida(t.HorarioChegada) {
			return invalido("trecho %d: use datas no formato AAAA-MM-DD HH:MM", n)
		}
		if t.HorarioChegada <= t.HorarioSaida {
			return invalido("trecho %d: a chegada deve ser depois da saída", n)
		}
		if i == 0 && t.HorarioSaida <= agora() {
			return invalido("a data de partida já passou")
		}
		if chegadaAnterior != "" && t.HorarioSaida < chegadaAnterior {
			return invalido("trecho %d: sai antes de o trecho anterior chegar", n)
		}
		if t.Valor < 0 || t.Valor > 10000 {
			return invalido("trecho %d: valor inválido", n)
		}

		// Vagas por trecho: o motorista pode oferecer menos vagas em um trecho específico.
		// 0 (não informado) usa a capacidade do veículo.
		if t.VagasTotais == 0 {
			t.VagasTotais = c.Assentos
		}
		if t.VagasTotais < 1 || t.VagasTotais > c.Assentos {
			return invalido("trecho %d: as vagas devem ser entre 1 e %d (capacidade do veículo)", n, c.Assentos)
		}

		cidadeAtual = t.Destino
		chegadaAnterior = t.HorarioChegada
	}

	if cidadeAtual != destino {
		return invalido("o último trecho deve terminar em %s", destino)
	}
	return nil
}

func VerCarrinho(passageiroID string) ([]modelos.Trecho, error) {
	valor, existe := CarrinhosPendentes.Load(passageiroID)
	if !existe {
		return nil, falha(protocolo.StatusNaoEncontrado, "Seu carrinho está vazio ou a reserva expirou.")
	}

	reg := valor.(*RegistroCarrinho)
	reg.Lock()
	defer reg.Unlock()
	if reg.Fechado {
		return nil, falha(protocolo.StatusNaoEncontrado, "Seu carrinho está vazio ou a reserva expirou.")
	}

	return append([]modelos.Trecho(nil), reg.Trechos...), nil // devolve uma cópia
}

// Fecha o carrinho e o retira do mapa.
func fecharCarrinho(passageiroID string, reg *RegistroCarrinho) ([]modelos.Trecho, bool) {
	reg.Lock()
	defer reg.Unlock()
	if reg.Fechado {
		return nil, false
	}
	reg.Fechado = true
	CarrinhosPendentes.CompareAndDelete(passageiroID, reg) // só apaga se ainda for este carrinho
	return reg.Trechos, true
}

// Trava todas as caronas usadas pelos trechos, ordem de ID (evita deadlock).
// Caronas que não existem mais no mapa ficam de fora do retorno.
func travarCaronas(trechos []modelos.Trecho) map[string]*RegistroCarona {
	var ids []string
	vistos := make(map[string]bool)
	for _, t := range trechos {
		if !vistos[t.CaronaID] { // a mesma carona pode aparecer em vários trechos
			vistos[t.CaronaID] = true
			ids = append(ids, t.CaronaID)
		}
	}
	sort.Strings(ids)

	registros := make(map[string]*RegistroCarona)
	for _, id := range ids {
		valor, existe := Caronas.Load(id)
		if !existe {
			continue
		}
		reg := valor.(*RegistroCarona)
		reg.Lock()
		registros[id] = reg
	}
	return registros
}

func destravarCaronas(registros map[string]*RegistroCarona) {
	for _, reg := range registros {
		reg.Unlock()
	}
}

// Remove uma ocorrência do passageiro, criando um slice novo
func removerPassageiro(passageiros []string, passageiroID string) []string {
	nova := make([]string, 0, len(passageiros))
	removido := false
	for _, p := range passageiros {
		if p == passageiroID && !removido {
			removido = true
			continue
		}
		nova = append(nova, p)
	}
	return nova
}

// Cópia profunda da carona. Deve ser chamada com o cadeado travado: o slice de trechos
// aponta para a memória original, e o JSON só é gerado depois do Unlock.
func copiarCarona(c modelos.Carona) modelos.Carona {
	copia := c
	copia.Trechos = make([]modelos.Trecho, len(c.Trechos))
	for i, t := range c.Trechos {
		copia.Trechos[i] = t
		copia.Trechos[i].Passageiros = append([]string{}, t.Passageiros...)
	}
	return copia
}

func salvarCaronasEItinerarios() {
	SalvarSyncMap(&Itinerarios, ArquivoItinerarios, func(v any) modelos.Itinerario {
		return v.(modelos.Itinerario)
	})
	SalvarSyncMap(&Caronas, ArquivoCaronas, func(v any) modelos.Carona {
		reg := v.(*RegistroCarona)
		reg.Lock()
		defer reg.Unlock()
		return copiarCarona(reg.Dados)
	})
}

// Lista as caronas publicadas pelo motorista, com os passageiros de cada trecho
func ListarCaronasMotorista(motoristaID string) []modelos.Carona {
	var lista []modelos.Carona

	Caronas.Range(func(key, value any) bool {
		reg := value.(*RegistroCarona)
		reg.Lock()

		if reg.Dados.MotoristaID == motoristaID && !reg.Cancelada {
			lista = append(lista, copiarCarona(reg.Dados))
		}

		reg.Unlock()
		return true
	})

	// Ordena pela data de saída (o formato "YYYY-MM-DD HH:MM" ordena alfabeticamente)
	sort.Slice(lista, func(i, j int) bool {
		return lista[i].DataHora < lista[j].DataHora
	})

	return lista
}

// Lista os itinerários (passagens compradas) do passageiro
func ListarItinerariosPassageiro(passageiroID string) []modelos.Itinerario {
	var lista []modelos.Itinerario

	Itinerarios.Range(func(key, value any) bool {
		itinerario := value.(modelos.Itinerario)
		if itinerario.PassageiroID == passageiroID && len(itinerario.Trechos) > 0 {
			lista = append(lista, itinerario)
		}
		return true
	})

	sort.Slice(lista, func(i, j int) bool {
		return lista[i].Trechos[0].HorarioSaida < lista[j].Trechos[0].HorarioSaida
	})

	return lista
}

func EfetivarItinerario(passageiroID string) error {
	// 1. Tenta resgatar o carrinho
	valor, existe := CarrinhosPendentes.Load(passageiroID)
	if !existe {
		return falha(protocolo.StatusNaoEncontrado, "carrinho vazio ou o tempo da reserva expirou")
	}

	// 2. DESARMA A BOMBA! Fecha e remove o carrinho pendente.
	// Se o cronômetro fechou antes (corrida), as vagas já foram devolvidas e a compra não vale.
	trechosEscolhidos, ok := fecharCarrinho(passageiroID, valor.(*RegistroCarrinho))
	if !ok || len(trechosEscolhidos) == 0 {
		return falha(protocolo.StatusNaoEncontrado, "carrinho vazio ou o tempo da reserva expirou")
	}

	// 3. Cria o Itinerário Definitivo (os trechos do carrinho são cópias oficiais do servidor)
	novoItinerario := modelos.Itinerario{
		ID:           fmt.Sprintf("itin_%d", time.Now().UnixNano()),
		PassageiroID: passageiroID,
		Origem:       trechosEscolhidos[0].Origem,
		Destino:      trechosEscolhidos[len(trechosEscolhidos)-1].Destino,
		ValorTotal:   modelos.ValorTotal(trechosEscolhidos),
		Trechos:      trechosEscolhidos,
	}

	// Trava todas as caronas da viagem de uma vez. Assim o itinerário e as listas de
	// embarque mudam juntos, e um cancelamento do motorista nunca vê metade da compra.
	registros := travarCaronas(trechosEscolhidos)

	caronaCancelada := false
	for _, t := range trechosEscolhidos {
		reg, existe := registros[t.CaronaID]
		if !existe || reg.Cancelada {
			caronaCancelada = true
			break
		}
	}

	if !caronaCancelada {
		// ATUALIZA O MOTORISTA: Coloca o passageiro na lista de embarque
		for _, tReq := range trechosEscolhidos {
			reg := registros[tReq.CaronaID]
			for i, tOficial := range reg.Dados.Trechos {
				if tOficial.ID == tReq.ID {
					reg.Dados.Trechos[i].Passageiros = append(reg.Dados.Trechos[i].Passageiros, passageiroID)
					break
				}
			}
		}

		// Salva no mapa global de viagens do sistema
		Itinerarios.Store(novoItinerario.ID, novoItinerario)
	}

	destravarCaronas(registros)

	if caronaCancelada {
		// Um motorista cancelou enquanto o passageiro decidia: devolve as vagas dos outros
		DesfazerReserva(trechosEscolhidos)
		return falha(protocolo.StatusConflito, "um dos motoristas cancelou a carona antes da confirmação; as vagas foram liberadas")
	}

	// 7. PERSISTE TUDO NO DISCO (fora dos cadeados das caronas, senão dá deadlock)
	salvarCaronasEItinerarios()

	// 8. Avisa cada motorista que ganhou um passageiro
	for _, pedaco := range pedacosPorCarona(trechosEscolhidos) {
		notificar(pedaco[0].MotoristaID, "%s confirmou uma vaga na sua carona: %s.",
			nomeUsuario(passageiroID), descreverTrechos(pedaco))
	}
	salvarNotificacoes()

	return nil
}

func ReservarVagasTemporariamente(passageiroID string, trechosEscolhidos []modelos.Trecho, continuacao bool) error {
	if len(trechosEscolhidos) == 0 {
		return falha(protocolo.StatusRequisicaoInvalida, "nenhum trecho informado")
	}
	if len(trechosEscolhidos) > modelos.MaxTrechosPorItinerario {
		return falha(protocolo.StatusRequisicaoInvalida, "uma viagem pode ter no máximo %d trechos", modelos.MaxTrechosPorItinerario)
	}

	var vagasDescontadas []modelos.Trecho
	pedidos := make(map[string]bool)

	for _, tReq := range trechosEscolhidos {
		if pedidos[tReq.ID] {
			DesfazerReserva(vagasDescontadas) // Rollback!
			return falha(protocolo.StatusRequisicaoInvalida, "o mesmo trecho foi informado duas vezes")
		}
		pedidos[tReq.ID] = true

		valor, existe := Caronas.Load(tReq.CaronaID)
		if !existe {
			DesfazerReserva(vagasDescontadas) // Rollback!
			return falha(protocolo.StatusConflito, "uma das caronas não existe mais (pode ter sido cancelada pelo motorista)")
		}

		reg := valor.(*RegistroCarona)
		reg.Lock()

		var erro error
		var oficial modelos.Trecho

		if reg.Cancelada {
			erro = falha(protocolo.StatusConflito, "uma das caronas foi cancelada pelo motorista durante a reserva")
		} else {
			achou := false
			for i, t := range reg.Dados.Trechos {
				if t.ID != tReq.ID {
					continue
				}
				achou = true
				switch {
				case t.HorarioSaida <= agora():
					erro = falha(protocolo.StatusConflito, "o trecho %s -> %s já partiu", t.Origem, t.Destino)
				case len(vagasDescontadas) > 0 && !encadeia(vagasDescontadas[len(vagasDescontadas)-1], t):
					erro = falha(protocolo.StatusRequisicaoInvalida, "os trechos escolhidos não formam um caminho contínuo")
				case t.VagasLivres <= 0:
					erro = falha(protocolo.StatusConflito, "alguém foi mais rápido! Vagas esgotadas para o trecho %s -> %s", t.Origem, t.Destino)
				default:
					reg.Dados.Trechos[i].VagasLivres-- // DESCONTA A VAGA NA HORA
					oficial = reg.Dados.Trechos[i]
				}
				break
			}
			if !achou {
				erro = falha(protocolo.StatusNaoEncontrado, "trecho não encontrado nesta carona")
			}
		}
		reg.Unlock() // Libera o cadeado da carona

		if erro != nil {
			DesfazerReserva(vagasDescontadas)
			return erro
		}

		oficial.Passageiros = nil // o carrinho não carrega a lista de embarque do motorista
		vagasDescontadas = append(vagasDescontadas, oficial)
	}

	if continuacao {
		if err := continuarCarrinho(passageiroID, vagasDescontadas); err != nil {
			DesfazerReserva(vagasDescontadas) // Rollback: carrinho expirou ou o trecho não continua a viagem
			return err
		}
		return nil
	}

	abrirCarrinhoNovo(passageiroID, vagasDescontadas)
	return nil
}

// Primeiro trecho de uma busca: descarta o carrinho anterior (devolvendo as vagas),
// cria um carrinho novo e aciona o timer
func abrirCarrinhoNovo(passageiroID string, trechos []modelos.Trecho) {
	for {
		if valor, existe := CarrinhosPendentes.Load(passageiroID); existe {
			if antigos, fechou := fecharCarrinho(passageiroID, valor.(*RegistroCarrinho)); fechou {
				DesfazerReserva(antigos)
			}
		}

		novo := &RegistroCarrinho{Trechos: trechos, Prazo: time.Now().Add(TempoReservaCarrinho)}
		if _, jaExiste := CarrinhosPendentes.LoadOrStore(passageiroID, novo); !jaExiste {
			go monitorarTempoCarrinho(passageiroID, novo)
			return
		}
		// Outro carrinho entrou nesse meio-tempo: descarta ele também e tenta de novo
	}
}

// Próximo trecho da mesma viagem: só entra se o carrinho ainda estiver aberto
// e se continuar a viagem de onde o último trecho parou. Renova o prazo.
func continuarCarrinho(passageiroID string, trechos []modelos.Trecho) error {
	expirado := falha(protocolo.StatusConflito, "o tempo da sua reserva expirou e as vagas dos trechos anteriores foram liberadas. Refaça a busca")

	valor, existe := CarrinhosPendentes.Load(passageiroID)
	if !existe {
		return expirado
	}

	reg := valor.(*RegistroCarrinho)
	reg.Lock()
	defer reg.Unlock()
	if reg.Fechado {
		return expirado
	}

	ultimo := reg.Trechos[len(reg.Trechos)-1]
	if !encadeia(ultimo, trechos[0]) {
		return falha(protocolo.StatusRequisicaoInvalida,
			"este trecho não continua a sua viagem: ele deve sair de %s a partir de %s", ultimo.Destino, ultimo.HorarioChegada)
	}
	if len(reg.Trechos)+len(trechos) > modelos.MaxTrechosPorItinerario {
		return falha(protocolo.StatusRequisicaoInvalida, "uma viagem pode ter no máximo %d trechos", modelos.MaxTrechosPorItinerario)
	}
	for _, novo := range trechos {
		for _, existente := range reg.Trechos {
			if novo.ID == existente.ID {
				return falha(protocolo.StatusRequisicaoInvalida, "este trecho já está no seu carrinho")
			}
		}
	}

	reg.Trechos = append(reg.Trechos, trechos...)
	reg.Prazo = time.Now().Add(TempoReservaCarrinho)
	return nil
}

// Devolve as vagas para o sistema
func DesfazerReserva(trechos []modelos.Trecho) {
	for _, tReq := range trechos {
		valor, existe := Caronas.Load(tReq.CaronaID)
		if existe {
			reg := valor.(*RegistroCarona)
			reg.Lock()
			for i, t := range reg.Dados.Trechos {
				if t.ID == tReq.ID {
					reg.Dados.Trechos[i].VagasLivres++ // DEVOLVE A VAGA
					break
				}
			}
			reg.Unlock()
		}
	}
}

// O Cronômetro que roda em background (Goroutine)
func monitorarTempoCarrinho(passageiroID string, carrinho *RegistroCarrinho) {
	for {
		carrinho.Lock()
		fechado := carrinho.Fechado
		restante := time.Until(carrinho.Prazo)
		carrinho.Unlock()

		if fechado {
			return // Já foi confirmado (ou substituído). Pode morrer feliz.
		}
		if restante <= 0 {
			break
		}
		// Ainda há tempo (o prazo pode ter sido renovado por um novo trecho): dorme o que falta
		time.Sleep(restante)
	}

	// Acordou e o prazo acabou! Tenta fechar ESTE carrinho (a confirmação pode ter chegado agora).
	trechosExpirados, fechou := fecharCarrinho(passageiroID, carrinho)
	if !fechou {
		return
	}

	// Se chegou aqui, o tempo esgotou! Rollback das vagas.
	DesfazerReserva(trechosExpirados)

	notificar(passageiroID, "Sua reserva de %s expirou antes da confirmação e as vagas foram liberadas.",
		descreverTrechos(trechosExpirados))
	salvarNotificacoes()

	fmt.Printf("\n[SISTEMA] Tempo esgotado! Carrinho do passageiro %s expirou e as vagas foram devolvidas.\n", passageiroID)
}

func PublicarCarona(motoristaID string, novaCarona modelos.Carona) (modelos.Carona, error) {
	if err := validarCarona(&novaCarona); err != nil {
		return modelos.Carona{}, err
	}

	novaCarona.MotoristaID = motoristaID                            // sempre o usuário logado
	novaCarona.ID = fmt.Sprintf("carona_%d", time.Now().UnixNano()) //gera um id baseado no tempo que passou desde de uma data
	novaCarona.DataHora = novaCarona.Trechos[0].HorarioSaida        // a partida é a saída do primeiro trecho

	//monta as informacoes dos trechos (cada trecho tem o seu próprio controle de vagas)
	for i := range novaCarona.Trechos {
		novaCarona.Trechos[i].ID = fmt.Sprintf("tre_%d_%d", time.Now().UnixNano(), i)
		novaCarona.Trechos[i].CaronaID = novaCarona.ID
		novaCarona.Trechos[i].MotoristaID = motoristaID
		novaCarona.Trechos[i].VagasLivres = novaCarona.Trechos[i].VagasTotais // já validado em validarCarona
		novaCarona.Trechos[i].Passageiros = []string{}
	}

	registro := &RegistroCarona{Dados: copiarCarona(novaCarona)}
	Caronas.Store(novaCarona.ID, registro)

	salvarCaronasEItinerarios()

	return novaCarona, nil
}

func SalvarSyncMap[T any](mapaSync *sync.Map, arquivo string, extrair func(valor any) T) error {
	valor, _ := arquivosPersistidos.LoadOrStore(arquivo, &arquivoPersistido{})
	controle := valor.(*arquivoPersistido)

	meuPedido := controle.pedidos.Add(1)

	controle.mu.Lock()
	defer controle.mu.Unlock()

	if controle.gravados >= meuPedido {
		return nil
	}
	cobertos := controle.pedidos.Load()

	dadosPuros := make(map[string]T)

	// Varre o sync.Map e usa a função "extrair" para pegar apenas o dado limpo
	mapaSync.Range(func(key, value any) bool {
		chaveStr := key.(string)
		dadosPuros[chaveStr] = extrair(value)
		return true
	})

	bytesJSON, err := json.MarshalIndent(dadosPuros, "", "  ")
	if err != nil {
		return err
	}

	// Escrita atômica: grava num arquivo temporário e renomeia por cima do original.
	// Se o servidor cair no meio da gravação, o arquivo antigo continua inteiro.
	caminho := CaminhoArquivo(arquivo)
	temporario := caminho + ".tmp"
	if err := os.WriteFile(temporario, bytesJSON, 0644); err != nil {
		fmt.Printf("[ERRO] Falha ao gravar %s: %v\n", caminho, err)
		return err
	}
	if err := os.Rename(temporario, caminho); err != nil {
		fmt.Printf("[ERRO] Falha ao gravar %s: %v\n", caminho, err)
		return err
	}
	controle.gravados = cobertos
	return nil
}

func CarregarSyncMap[T any](mapaSync *sync.Map, arquivo string, empacotar func(dado T) any) error {
	caminho := CaminhoArquivo(arquivo)
	bytesJSON, err := os.ReadFile(caminho)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Se o arquivo não existir ainda, ignora
		}
		return err
	}

	// Lê o JSON para um mapa comum baseado no Tipo T
	mapaTemporario := make(map[string]T)
	err = json.Unmarshal(bytesJSON, &mapaTemporario)
	if err != nil {
		return err
	}

	// Joga do mapa comum de volta para o sync.Map, usando a função "empacotar"
	for chave, dado := range mapaTemporario {
		mapaSync.Store(chave, empacotar(dado))
	}

	fmt.Printf("[SISTEMA] Dados carregados com sucesso do arquivo %s\n", caminho)
	return nil
}

// Deixa o estado carregado do disco coerente. Deve ser chamada uma única vez,
// na inicialização, ANTES de o servidor aceitar conexões.
//   - Carrinhos não são salvos, então vagas descontadas por carrinhos pendentes no
//     momento de uma queda ficariam presas para sempre: as vagas são recalculadas.
//   - As listas de embarque são reconstruídas a partir dos itinerários (os dois arquivos
//     são gravados em sequência, e uma queda entre eles deixaria as listas defasadas).
//   - Itinerários que apontam para caronas/trechos que não existem mais são descartados.
func ReconciliarEstado() {
	type posicao struct {
		reg    *RegistroCarona
		indice int
	}
	trechos := make(map[string]posicao)

	Caronas.Range(func(_, value any) bool {
		reg := value.(*RegistroCarona)
		for i := range reg.Dados.Trechos {
			t := &reg.Dados.Trechos[i]
			if cidade, ok := modelos.NormalizarCidade(t.Origem); ok {
				t.Origem = cidade
			}
			if cidade, ok := modelos.NormalizarCidade(t.Destino); ok {
				t.Destino = cidade
			}
			t.Passageiros = []string{}
			trechos[t.ID] = posicao{reg, i}
		}
		return true
	})

	// Itinerários em ordem de ID (o ID carrega o horário da compra)
	var ids []string
	Itinerarios.Range(func(key, _ any) bool {
		ids = append(ids, key.(string))
		return true
	})
	sort.Strings(ids)

	descartados := 0
	for _, id := range ids {
		valor, _ := Itinerarios.Load(id)
		itinerario := valor.(modelos.Itinerario)

		valido := len(itinerario.Trechos) > 0
		for _, t := range itinerario.Trechos {
			if p, existe := trechos[t.ID]; !existe || p.reg.Dados.ID != t.CaronaID {
				valido = false
			}
		}
		if !valido {
			Itinerarios.Delete(id)
			descartados++
			continue
		}

		for _, t := range itinerario.Trechos {
			p := trechos[t.ID]
			oficial := &p.reg.Dados.Trechos[p.indice]
			oficial.Passageiros = append(oficial.Passageiros, itinerario.PassageiroID)
		}
	}

	Caronas.Range(func(_, value any) bool {
		reg := value.(*RegistroCarona)
		for i := range reg.Dados.Trechos {
			t := &reg.Dados.Trechos[i]
			t.VagasLivres = t.VagasTotais - len(t.Passageiros)
			if t.VagasLivres < 0 {
				fmt.Printf("[AVISO] Trecho %s tem mais passageiros (%d) que assentos (%d)\n", t.ID, len(t.Passageiros), t.VagasTotais)
				t.VagasLivres = 0
			}
		}
		return true
	})

	fmt.Printf("[SISTEMA] Estado reconciliado: vagas recalculadas, %d itinerário(s) inválido(s) descartado(s)\n", descartados)
	salvarCaronasEItinerarios()
}

// Cancela um itinerário: devolve as vagas e tira o passageiro das listas de embarque.
// Usada pelo cancelamento do passageiro e pelo cancelamento em cascata do motorista.
func cancelarItinerario(itinerarioID string) (modelos.Itinerario, bool) {
	valor, existe := Itinerarios.Load(itinerarioID)
	if !existe {
		return modelos.Itinerario{}, false
	}
	itinerario := valor.(modelos.Itinerario)

	registros := travarCaronas(itinerario.Trechos)
	defer destravarCaronas(registros)

	// Apaga dentro dos cadeados: se dois cancelamentos chegarem juntos, só um devolve as vagas
	if _, removido := Itinerarios.LoadAndDelete(itinerarioID); !removido {
		return modelos.Itinerario{}, false
	}

	for _, t := range itinerario.Trechos {
		reg, existe := registros[t.CaronaID]
		if !existe || reg.Cancelada {
			continue // carona do motorista que cancelou: não há vaga para devolver
		}
		for i := range reg.Dados.Trechos {
			if reg.Dados.Trechos[i].ID == t.ID {
				reg.Dados.Trechos[i].VagasLivres++
				reg.Dados.Trechos[i].Passageiros = removerPassageiro(reg.Dados.Trechos[i].Passageiros, itinerario.PassageiroID)
				break
			}
		}
	}

	return itinerario, true
}

func CancelarItinerarioPassageiro(passageiroID, itinerarioID string) error {
	valor, existe := Itinerarios.Load(itinerarioID)
	if !existe {
		return falha(protocolo.StatusNaoEncontrado, "viagem não encontrada")
	}
	if valor.(modelos.Itinerario).PassageiroID != passageiroID {
		return falha(protocolo.StatusProibido, "esta viagem não pertence a você")
	}

	itinerario, cancelou := cancelarItinerario(itinerarioID)
	if !cancelou {
		return falha(protocolo.StatusConflito, "esta viagem já foi cancelada")
	}

	salvarCaronasEItinerarios()

	// Avisa cada motorista que perdeu o passageiro
	for _, pedaco := range pedacosPorCarona(itinerario.Trechos) {
		notificar(pedaco[0].MotoristaID, "%s cancelou a reserva na sua carona: %s. A vaga voltou a ficar disponível.",
			nomeUsuario(passageiroID), descreverTrechos(pedaco))
	}
	salvarNotificacoes()

	return nil
}

// Cancela a carona e, em cascata, a viagem inteira de cada passageiro afetado.
// Retorna quantos itinerários foram cancelados.
func CancelarCaronaPeloMotorista(motoristaID, caronaID string) (int, error) {

	//Marca a carona como cancelada. A partir daqui ninguém reserva nem compra nela.
	valor, existe := Caronas.Load(caronaID)
	if !existe {
		return 0, falha(protocolo.StatusNaoEncontrado, "carona não encontrada")
	}
	reg := valor.(*RegistroCarona)

	reg.Lock()
	if reg.Dados.MotoristaID != motoristaID {
		reg.Unlock()
		return 0, falha(protocolo.StatusProibido, "esta carona não pertence a você")
	}
	if reg.Cancelada {
		reg.Unlock()
		return 0, falha(protocolo.StatusConflito, "esta carona já foi cancelada")
	}
	reg.Cancelada = true
	descricaoCarona := descreverTrechos(reg.Dados.Trechos) // para os avisos (lida com a trava segura)
	reg.Unlock()

	// Apaga a carona do mapa (some das buscas)
	Caronas.CompareAndDelete(caronaID, reg)

	// Varre os itinerários comprados e cancela as passagens afetadas.
	// Toda compra que pegou o cadeado desta carona antes do PASSO 1 já está no mapa;
	// as que chegarem depois veem a flag e desistem sozinhas.
	afetados := 0
	Itinerarios.Range(func(key, value any) bool {
		itinerario := value.(modelos.Itinerario)

		for _, t := range itinerario.Trechos {
			if t.CaronaID == caronaID {
				// Devolve também as vagas dos OUTROS motoristas da viagem (que não têm culpa)
				if _, cancelou := cancelarItinerario(itinerario.ID); cancelou {
					afetados++
					fmt.Printf("[SISTEMA] Itinerário %s do passageiro %s cancelado em cascata.\n", itinerario.ID, itinerario.PassageiroID)
					avisarCancelamentoEmCascata(itinerario, motoristaID, caronaID, descricaoCarona)
				}
				break
			}
		}

		return true
	})

	salvarCaronasEItinerarios()
	salvarNotificacoes()

	return afetados, nil
}

// Avisa o passageiro que perdeu a viagem e os OUTROS motoristas que perderam esse passageiro
func avisarCancelamentoEmCascata(itinerario modelos.Itinerario, motoristaID, caronaID, descricaoCarona string) {
	viagem := descreverTrechos(itinerario.Trechos)
	if viagem == descricaoCarona {
		notificar(itinerario.PassageiroID, "Sua viagem %s foi cancelada porque o motorista %s cancelou a carona.",
			viagem, nomeUsuario(motoristaID))
	} else {
		notificar(itinerario.PassageiroID,
			"Sua viagem %s foi cancelada porque o motorista %s cancelou a carona %s. As vagas de todos os trechos foram liberadas.",
			viagem, nomeUsuario(motoristaID), descricaoCarona)
	}

	for _, pedaco := range pedacosPorCarona(itinerario.Trechos) {
		if pedaco[0].CaronaID == caronaID {
			continue
		}
		notificar(pedaco[0].MotoristaID,
			"%s saiu da sua carona (%s) porque outra carona da mesma viagem foi cancelada. A vaga voltou a ficar disponível.",
			nomeUsuario(itinerario.PassageiroID), descreverTrechos(pedaco))
	}
}

func CadastrarUsuario(novoUsuario modelos.Usuario) error {
	if err := validarUsuario(&novoUsuario); err != nil {
		return err
	}

	_, existe := Usuarios.LoadOrStore(novoUsuario.ID, novoUsuario) //Tenta carregar o novo usuario Se o usuario existir, vai para existe e retorna o erro
	if existe {
		return falha(protocolo.StatusConflito, "Usuário já cadastrado!")
	}
	SalvarSyncMap(&Usuarios, ArquivoUsuarios, func(valor any) modelos.Usuario {
		return valor.(modelos.Usuario)
	})
	return nil
}

func ValidarLogin(id string, senha string, tipoEsperado string) (modelos.Usuario, error) {

	valor, existe := Usuarios.Load(strings.TrimSpace(id))

	if !existe {
		return modelos.Usuario{}, falha(protocolo.StatusNaoAutenticado, "ID ou senha incorretos")
	}

	usuario := valor.(modelos.Usuario) // o syncmap retorna dado generico, para isso precisa informar que é do tipo usuario

	if senha != usuario.Senha {
		return modelos.Usuario{}, falha(protocolo.StatusNaoAutenticado, "ID ou senha incorretos")
	}

	if usuario.Tipo != tipoEsperado {
		return modelos.Usuario{}, falha(protocolo.StatusProibido, "Acesso negado: você não tem permissão para usar este aplicativo")
	}

	usuario.Senha = ""
	return usuario, nil
}
