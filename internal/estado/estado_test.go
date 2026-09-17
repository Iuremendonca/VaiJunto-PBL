package estado

// Testes unitários do estado do servidor (regras de negócio e concorrência).
// Rode com o detector de corrida:  go test -race ./...
import (
	"VaiJunto/internal/modelos"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "vaijunto")
	os.Setenv("VAIJUNTO_DADOS", dir)
	os.Exit(m.Run())
}

// Datas relativas a hoje (dia 0 = daqui a 10 dias)
func h(dia int, hora string) string {
	return time.Now().AddDate(0, 0, 10+dia).Format(modelos.FormatoData) + " " + hora
}
func d(dia int) string { return time.Now().AddDate(0, 0, 10+dia).Format(modelos.FormatoData) }

func limpar() {
	for _, mp := range []*sync.Map{&Caronas, &Itinerarios, &CarrinhosPendentes, &Usuarios, &Notificacoes} {
		mp.Range(func(k, _ any) bool { mp.Delete(k); return true })
	}
}

type perna struct {
	de, para    string
	saida, cheg string
	valor       float64
}

func publicarRota(t *testing.T, mot string, assentos int, pernas ...perna) modelos.Carona {
	t.Helper()
	c := modelos.Carona{Origem: pernas[0].de, Destino: pernas[len(pernas)-1].para, Assentos: assentos}
	for _, p := range pernas {
		c.Trechos = append(c.Trechos, modelos.Trecho{Origem: p.de, Destino: p.para, Valor: p.valor, HorarioSaida: p.saida, HorarioChegada: p.cheg})
	}
	res, err := PublicarCarona(mot, c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func publicar(t *testing.T, mot, orig, dest string, vagas int) modelos.Carona {
	t.Helper()
	return publicarRota(t, mot, vagas, perna{orig, dest, h(0, "08:00"), h(0, "09:00"), 10})
}

func trechoN(id string, n int) modelos.Trecho {
	v, _ := Caronas.Load(id)
	r := v.(*RegistroCarona)
	r.Lock()
	defer r.Unlock()
	t := r.Dados.Trechos[n]
	t.Passageiros = append([]string{}, t.Passageiros...)
	return t
}
func trecho(id string) modelos.Trecho { return trechoN(id, 0) }

func comprar(t *testing.T, p string, cs ...modelos.Carona) string {
	t.Helper()
	for i, c := range cs {
		if err := ReservarVagasTemporariamente(p, c.Trechos, i > 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := EfetivarItinerario(p); err != nil {
		t.Fatal(err)
	}
	its := ListarItinerariosPassageiro(p)
	return its[len(its)-1].ID
}

func TestBaldeacaoECancelPassageiro(t *testing.T) {
	limpar()
	c1 := publicarRota(t, "m1", 2, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10})
	c2 := publicarRota(t, "m2", 2, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10})
	id := comprar(t, "p1", c1, c2)
	it := ListarItinerariosPassageiro("p1")[0]
	if len(it.Trechos) != 2 || it.ValorTotal != 20 {
		t.Fatalf("itinerário incompleto: %+v", it)
	}
	if tr := trecho(c1.ID); tr.VagasLivres != 1 || len(tr.Passageiros) != 1 {
		t.Fatalf("c1 %+v", tr)
	}
	if err := CancelarItinerarioPassageiro("outro", id); err == nil {
		t.Fatal("deveria recusar dono errado")
	}
	if err := CancelarItinerarioPassageiro("p1", id); err != nil {
		t.Fatal(err)
	}
	if err := CancelarItinerarioPassageiro("p1", id); err == nil {
		t.Fatal("cancelou duas vezes")
	}
	for _, c := range []modelos.Carona{c1, c2} {
		if tr := trecho(c.ID); tr.VagasLivres != 2 || len(tr.Passageiros) != 0 {
			t.Fatalf("não devolveu %+v", tr)
		}
	}
}

func TestCascataMotorista(t *testing.T) {
	limpar()
	c1 := publicarRota(t, "m1", 3, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10})
	c2 := publicarRota(t, "m2", 3, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10})
	comprar(t, "p1", c1, c2)
	comprar(t, "p2", c2)
	if _, err := CancelarCaronaPeloMotorista("m2", c1.ID); err == nil {
		t.Fatal("deveria recusar motorista errado")
	}
	n, err := CancelarCaronaPeloMotorista("m1", c1.ID)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, existe := Caronas.Load(c1.ID); existe {
		t.Fatal("carona c1 ainda existe")
	}
	if len(ListarItinerariosPassageiro("p1")) != 0 || len(ListarItinerariosPassageiro("p2")) != 1 {
		t.Fatal("cascata errada")
	}
	tr := trecho(c2.ID)
	if tr.VagasLivres != 2 || len(tr.Passageiros) != 1 || tr.Passageiros[0] != "p2" {
		t.Fatalf("c2 %+v", tr)
	}
}

func TestCaronaCanceladaDuranteCarrinho(t *testing.T) {
	limpar()
	c1 := publicarRota(t, "m1", 2, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10})
	c2 := publicarRota(t, "m2", 2, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10})
	ReservarVagasTemporariamente("p1", c1.Trechos, false)
	ReservarVagasTemporariamente("p1", c2.Trechos, true)
	CancelarCaronaPeloMotorista("m1", c1.ID)
	if err := EfetivarItinerario("p1"); err == nil {
		t.Fatal("deveria falhar")
	}
	if tr := trecho(c2.ID); tr.VagasLivres != 2 || len(tr.Passageiros) != 0 {
		t.Fatalf("c2 %+v", tr)
	}
	if len(ListarItinerariosPassageiro("p1")) != 0 {
		t.Fatal("itinerário órfão")
	}
}

func TestFecharCarrinhoUmVencedor(t *testing.T) {
	for i := 0; i < 200; i++ {
		reg := &RegistroCarrinho{Trechos: []modelos.Trecho{{}}, Prazo: time.Now().Add(time.Hour)}
		CarrinhosPendentes.Store("px", reg)
		var wg sync.WaitGroup
		var mu sync.Mutex
		vencedores := 0
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, ok := fecharCarrinho("px", reg); ok {
					mu.Lock()
					vencedores++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if vencedores != 1 {
			t.Fatalf("vencedores=%d", vencedores)
		}
	}
}

func TestCarrinhoExpiracao(t *testing.T) {
	limpar()
	TempoReservaCarrinho = 300 * time.Millisecond
	defer func() { TempoReservaCarrinho = time.Minute }()
	c1 := publicarRota(t, "m1", 1, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10})
	c2 := publicarRota(t, "m2", 1, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10})

	ReservarVagasTemporariamente("p1", c1.Trechos, false)
	if trecho(c1.ID).VagasLivres != 0 {
		t.Fatal("vaga não foi ocupada")
	}
	if err := ReservarVagasTemporariamente("p2", c1.Trechos, false); err == nil {
		t.Fatal("p2 pegou vaga ocupada no carrinho de p1")
	}
	time.Sleep(200 * time.Millisecond)
	if err := ReservarVagasTemporariamente("p1", c2.Trechos, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if tr, err := VerCarrinho("p1"); err != nil || len(tr) != 2 {
		t.Fatalf("carrinho deveria seguir aberto com 2 trechos: %v %v", tr, err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := VerCarrinho("p1"); err == nil {
		t.Fatal("carrinho deveria ter expirado")
	}
	if trecho(c1.ID).VagasLivres != 1 || trecho(c2.ID).VagasLivres != 1 {
		t.Fatal("vagas não voltaram")
	}
	if EfetivarItinerario("p1") == nil {
		t.Fatal("confirmou carrinho expirado")
	}
	ReservarVagasTemporariamente("p1", c1.Trechos, false)
	time.Sleep(500 * time.Millisecond)
	if err := ReservarVagasTemporariamente("p1", c2.Trechos, true); err == nil {
		t.Fatal("aceitou continuação de carrinho expirado")
	}
	if trecho(c1.ID).VagasLivres != 1 || trecho(c2.ID).VagasLivres != 1 {
		t.Fatal("vaga presa após continuação recusada")
	}
}

func TestNovaBuscaDescartaCarrinhoAnterior(t *testing.T) {
	limpar()
	c1 := publicar(t, "m1", "Feira de Santana", "Salvador", 1)
	c2 := publicar(t, "m2", "Alagoinhas", "Esplanada", 1)
	ReservarVagasTemporariamente("p1", c1.Trechos, false)
	ReservarVagasTemporariamente("p1", c2.Trechos, false)
	if trecho(c1.ID).VagasLivres != 1 {
		t.Fatal("vaga do carrinho abandonado não voltou")
	}
	tr, _ := VerCarrinho("p1")
	if len(tr) != 1 || tr[0].CaronaID != c2.ID {
		t.Fatalf("carrinho misturou buscas: %+v", tr)
	}
}

// Item 5: o carrinho guarda o trecho OFICIAL, não o que o cliente mandou
func TestPrecoOficialECaminhoContinuo(t *testing.T) {
	limpar()
	c1 := publicarRota(t, "m1", 2, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 50})
	c2 := publicarRota(t, "m2", 2, perna{"Alagoinhas", "Esplanada", h(0, "11:00"), h(0, "12:00"), 50})
	c3 := publicarRota(t, "m3", 2, perna{"Salvador", "Aracaju", h(0, "09:00"), h(0, "12:00"), 50}) // sai antes de c1 chegar

	adulterado := c1.Trechos
	adulterado[0].Valor = 0
	adulterado[0].HorarioSaida = "2000-01-01 00:00"
	ReservarVagasTemporariamente("p1", adulterado, false)
	tr, _ := VerCarrinho("p1")
	if tr[0].Valor != 50 || tr[0].HorarioSaida != h(0, "08:00") {
		t.Fatalf("carrinho usou dados do cliente: %+v", tr[0])
	}
	if err := ReservarVagasTemporariamente("p1", c2.Trechos, true); err == nil {
		t.Fatal("aceitou trecho que não sai da cidade de chegada")
	}
	if err := ReservarVagasTemporariamente("p1", c3.Trechos, true); err == nil {
		t.Fatal("aceitou trecho que sai antes da chegada")
	}
	if err := ReservarVagasTemporariamente("p1", c1.Trechos, true); err == nil {
		t.Fatal("aceitou trecho repetido")
	}
	if trecho(c2.ID).VagasLivres != 2 || trecho(c3.ID).VagasLivres != 2 || trecho(c1.ID).VagasLivres != 1 {
		t.Fatal("recusas deixaram vaga presa")
	}
	if err := ReservarVagasTemporariamente("p2", append(append([]modelos.Trecho{}, c1.Trechos...), c2.Trechos...), false); err == nil {
		t.Fatal("aceitou lista não contínua")
	}
	if err := ReservarVagasTemporariamente("p2", nil, false); err == nil {
		t.Fatal("aceitou lista vazia")
	}
	if err := ReservarVagasTemporariamente("p2", []modelos.Trecho{{ID: "x", CaronaID: c1.ID}}, false); err == nil {
		t.Fatal("aceitou trecho inexistente")
	}
	if err := EfetivarItinerario("p1"); err != nil {
		t.Fatal(err)
	}
	if v := ListarItinerariosPassageiro("p1")[0].ValorTotal; v != 50 {
		t.Fatalf("valor pago %v", v)
	}
}

// Item 8: validações de publicação e cadastro
func TestValidacoes(t *testing.T) {
	limpar()
	ok := func() modelos.Carona {
		return modelos.Carona{Origem: "feira de santana", Destino: "ARACAJU", Assentos: 3, Trechos: []modelos.Trecho{
			{Origem: "Feira de Santana", Destino: "Salvador", Valor: 10, HorarioSaida: h(0, "08:00"), HorarioChegada: h(0, "10:00")},
			{Origem: "salvador", Destino: "Aracaju", Valor: 10, HorarioSaida: h(0, "10:30"), HorarioChegada: h(0, "14:00")},
		}}
	}
	casos := map[string]func(c *modelos.Carona){
		"sem trechos":          func(c *modelos.Carona) { c.Trechos = nil },
		"cidade inválida":      func(c *modelos.Carona) { c.Trechos[1].Destino = "Recife" },
		"não encadeado":        func(c *modelos.Carona) { c.Trechos[1].Origem = "Alagoinhas" },
		"não termina no dest.": func(c *modelos.Carona) { c.Trechos = c.Trechos[:1] },
		"chegada antes saída":  func(c *modelos.Carona) { c.Trechos[0].HorarioChegada = h(0, "07:00") },
		"sai antes de chegar":  func(c *modelos.Carona) { c.Trechos[1].HorarioSaida = h(0, "09:00") },
		"data mal formatada":   func(c *modelos.Carona) { c.Trechos[0].HorarioSaida = "12" },
		"hora sem zero":        func(c *modelos.Carona) { c.Trechos[0].HorarioSaida = d(0) + " 8:00" },
		"data no passado":      func(c *modelos.Carona) { c.Trechos[0].HorarioSaida = "2020-01-01 08:00" },
		"valor negativo":       func(c *modelos.Carona) { c.Trechos[0].Valor = -1 },
		"vagas > capacidade":   func(c *modelos.Carona) { c.Trechos[1].VagasTotais = 4 },
		"vagas negativas":      func(c *modelos.Carona) { c.Trechos[0].VagasTotais = -1 },
		"assentos 0":           func(c *modelos.Carona) { c.Assentos = 0 },
		"assentos demais":      func(c *modelos.Carona) { c.Assentos = 99 },
		"origem = destino":     func(c *modelos.Carona) { c.Destino = "Feira de Santana" },
		"rota repete cidade": func(c *modelos.Carona) {
			c.Trechos[1].Destino = "Feira de Santana"
			c.Destino = "Feira de Santana"
		},
	}
	for nome, estraga := range casos {
		c := ok()
		estraga(&c)
		if _, err := PublicarCarona("m1", c); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
	c, err := PublicarCarona("m1", ok())
	if err != nil {
		t.Fatal(err)
	}
	if c.Origem != "Feira de Santana" || c.Trechos[1].Origem != "Salvador" || c.DataHora != h(0, "08:00") ||
		c.Trechos[1].VagasTotais != 3 || c.Trechos[1].VagasLivres != 3 || c.MotoristaID != "m1" {
		t.Fatalf("carona mal normalizada: %+v", c)
	}

	usuarios := []modelos.Usuario{
		{ID: "", Nome: "a", Senha: "1", Tipo: "passageiro"},
		{ID: "com espaco", Nome: "a", Senha: "1", Tipo: "passageiro"},
		{ID: "ok1", Nome: "", Senha: "1", Tipo: "passageiro"},
		{ID: "ok2", Nome: "a", Senha: "", Tipo: "passageiro"},
		{ID: "ok3", Nome: "a", Senha: "1", Tipo: "admin"},
	}
	for _, u := range usuarios {
		if CadastrarUsuario(u) == nil {
			t.Errorf("cadastrou usuário inválido %+v", u)
		}
	}
	if err := CadastrarUsuario(modelos.Usuario{ID: "valido", Nome: "V", Senha: "1", Tipo: "Passageiro"}); err != nil {
		t.Fatal(err)
	}
	if u, err := ValidarLogin("valido", "1", "passageiro"); err != nil || u.Senha != "" {
		t.Fatalf("login: %v %+v", err, u)
	}
	if _, err := ValidarLogin("valido", "1", "motorista"); err == nil {
		t.Fatal("login com tipo errado")
	}
}

// Item 7: busca respeita data, conexões, qualquer par da rota e ordena
func TestBuscaItinerarios(t *testing.T) {
	limpar()
	// Rota longa: Feira -> Santo Amaro -> Salvador -> Aracaju
	longa := publicarRota(t, "longo", 2,
		perna{"Feira de Santana", "Santo Amaro", h(0, "06:00"), h(0, "07:00"), 10},
		perna{"Santo Amaro", "Salvador", h(0, "07:10"), h(0, "08:00"), 10},
		perna{"Salvador", "Aracaju", h(0, "08:30"), h(0, "13:00"), 40})
	// Alternativa com baldeação chegando mais cedo, mas com troca de carro
	publicarRota(t, "a", 2, perna{"Santo Amaro", "Salvador", h(0, "07:05"), h(0, "07:40"), 5})
	publicarRota(t, "b", 2, perna{"Salvador", "Aracaju", h(0, "07:50"), h(0, "11:00"), 30})
	// Conexão impossível (sai antes de chegar)
	publicarRota(t, "c", 2, perna{"Salvador", "Esplanada", h(0, "07:30"), h(0, "09:00"), 5})
	// Outro dia
	publicarRota(t, "outrodia", 2, perna{"Santo Amaro", "Salvador", h(1, "07:10"), h(1, "08:00"), 1})

	// Qualquer par da rota: Santo Amaro -> Aracaju
	res, err := BuscarItinerarios(modelos.FiltroBusca{Origem: "santo amaro", Destino: "Aracaju", Data: d(0)})
	if err != nil {
		t.Fatal(err)
	}
	// direto (longo) | a + b (chega 11:00) | a + longo (chega 13:00). "longo" + "b" é impossível.
	if len(res) != 3 {
		for _, r := range res {
			t.Log(r)
		}
		t.Fatalf("esperava 3 itinerários, veio %d", len(res))
	}
	if res[0][0].CaronaID != longa.ID || len(res[0]) != 2 || modelos.Baldeacoes(res[0]) != 0 {
		t.Fatalf("o primeiro deveria ser o direto (sem troca): %+v", res[0])
	}
	if modelos.Baldeacoes(res[1]) != 1 || res[1][len(res[1])-1].HorarioChegada != h(0, "11:00") ||
		res[2][len(res[2])-1].HorarioChegada != h(0, "13:00") {
		t.Fatalf("o segundo deveria ser a baldeação que chega mais cedo: %+v", res[1])
	}
	for _, r := range res {
		for i := 1; i < len(r); i++ {
			if r[i].HorarioSaida < r[i-1].HorarioChegada || r[i].Origem != r[i-1].Destino {
				t.Fatalf("itinerário incoerente: %+v", r)
			}
		}
		if r[0].Passageiros != nil {
			t.Fatal("busca expôs passageiros")
		}
	}

	// Conexão impossível não aparece
	if res, _ := BuscarItinerarios(modelos.FiltroBusca{Origem: "Santo Amaro", Destino: "Esplanada", Data: d(0)}); len(res) != 0 {
		t.Fatalf("mostrou conexão impossível: %+v", res)
	}
	// Data: só o dia pedido
	res, _ = BuscarItinerarios(modelos.FiltroBusca{Origem: "Santo Amaro", Destino: "Salvador", Data: d(1)})
	if len(res) != 1 || res[0][0].MotoristaID != "outrodia" {
		t.Fatalf("filtro de data: %+v", res)
	}
	// Continuação: a partir de um horário
	res, _ = BuscarItinerarios(modelos.FiltroBusca{Origem: "Salvador", Destino: "Aracaju", HorarioMinimo: h(0, "08:00")})
	if len(res) != 1 || res[0][0].CaronaID != longa.ID {
		t.Fatalf("horário mínimo: %+v", res)
	}
	// Sem vaga some da busca
	ReservarVagasTemporariamente("x1", longa.Trechos[2:], false)
	ReservarVagasTemporariamente("x2", longa.Trechos[2:], false)
	res, _ = BuscarItinerarios(modelos.FiltroBusca{Origem: "Salvador", Destino: "Aracaju", HorarioMinimo: h(0, "08:00")})
	if len(res) != 0 {
		t.Fatal("mostrou trecho lotado")
	}
	// Entradas inválidas
	for _, f := range []modelos.FiltroBusca{
		{Origem: "Recife", Destino: "Aracaju", Data: d(0)},
		{Origem: "Salvador", Destino: "Salvador", Data: d(0)},
		{Origem: "Salvador", Destino: "Aracaju"},
		{Origem: "Salvador", Destino: "Aracaju", Data: "17/09/2026"},
		{Origem: "Salvador", Destino: "Aracaju", HorarioMinimo: d(0) + " 8:00"},
	} {
		if _, err := BuscarItinerarios(f); err == nil {
			t.Errorf("aceitou filtro inválido %+v", f)
		}
	}
}

// Item 6: vagas presas por carrinho pendente e listas defasadas são corrigidas ao carregar
func TestReconciliarEstado(t *testing.T) {
	limpar()
	c1 := publicarRota(t, "m1", 3, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10})
	c2 := publicarRota(t, "m2", 3, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10})
	comprar(t, "p1", c1, c2)
	ReservarVagasTemporariamente("p2", c1.Trechos, false) // pendente no momento da "queda"

	// Simula dados defasados no disco
	v, _ := Caronas.Load(c2.ID)
	reg := v.(*RegistroCarona)
	reg.Dados.Trechos[0].Passageiros = []string{"fantasma", "p1"}
	Itinerarios.Store("itin_orfao", modelos.Itinerario{ID: "itin_orfao", PassageiroID: "p3", Trechos: []modelos.Trecho{{ID: "sumiu", CaronaID: "sumiu"}}})
	Itinerarios.Store("itin_vazio", modelos.Itinerario{ID: "itin_vazio", PassageiroID: "p3"})
	CarrinhosPendentes.Range(func(k, _ any) bool { CarrinhosPendentes.Delete(k); return true }) // carrinhos não sobrevivem

	ReconciliarEstado()

	if tr := trecho(c1.ID); tr.VagasLivres != 2 || len(tr.Passageiros) != 1 {
		t.Fatalf("c1 %+v", tr)
	}
	if tr := trecho(c2.ID); tr.VagasLivres != 2 || strings.Join(tr.Passageiros, ",") != "p1" {
		t.Fatalf("c2 %+v", tr)
	}
	if _, ok := Itinerarios.Load("itin_orfao"); ok {
		t.Fatal("itinerário órfão não foi descartado")
	}
	if _, ok := Itinerarios.Load("itin_vazio"); ok {
		t.Fatal("itinerário vazio não foi descartado")
	}
}

// Estresse: compras (com baldeação), cancelamentos de passageiros e de motoristas ao mesmo tempo.
func TestEstresseConcorrente(t *testing.T) {
	limpar()
	var cs []modelos.Carona
	for i := 0; i < 6; i++ {
		cs = append(cs, publicarRota(t, fmt.Sprintf("m%d", i), 5, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10}))
		cs = append(cs, publicarRota(t, fmt.Sprintf("n%d", i), 5, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10}))
	}
	var wg sync.WaitGroup
	for p := 0; p < 60; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			pid := fmt.Sprintf("p%d", p)
			a, b := cs[(p%6)*2], cs[((p/2)%6)*2+1]
			if ReservarVagasTemporariamente(pid, a.Trechos, false) != nil {
				return
			}
			ReservarVagasTemporariamente(pid, b.Trechos, true)
			if EfetivarItinerario(pid) != nil {
				return
			}
			if p%3 == 0 {
				for _, it := range ListarItinerariosPassageiro(pid) {
					CancelarItinerarioPassageiro(pid, it.ID)
				}
			}
		}(p)
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			CancelarCaronaPeloMotorista(fmt.Sprintf("m%d", i), cs[i*2].ID)
			ListarCaronasMotorista(fmt.Sprintf("n%d", i))
			BuscarItinerarios(modelos.FiltroBusca{Origem: "Feira de Santana", Destino: "Aracaju", Data: d(0)})
		}(i)
	}
	wg.Wait()

	pend := 0
	CarrinhosPendentes.Range(func(_, _ any) bool { pend++; return true })
	if pend != 0 {
		t.Fatalf("%d carrinhos pendentes", pend)
	}
	esperado := map[string]int{}
	Itinerarios.Range(func(_, v any) bool {
		for _, tr := range v.(modelos.Itinerario).Trechos {
			if _, ok := Caronas.Load(tr.CaronaID); !ok {
				t.Errorf("itinerário aponta para carona cancelada %s", tr.CaronaID)
			}
			esperado[tr.ID]++
		}
		return true
	})
	Caronas.Range(func(_, v any) bool {
		r := v.(*RegistroCarona)
		for _, tr := range r.Dados.Trechos {
			if tr.VagasLivres+len(tr.Passageiros) != tr.VagasTotais || len(tr.Passageiros) != esperado[tr.ID] {
				t.Errorf("inconsistente %s: livres=%d pass=%d esperado=%d total=%d", tr.ID, tr.VagasLivres, len(tr.Passageiros), esperado[tr.ID], tr.VagasTotais)
			}
		}
		return true
	})
}

// Vagas por trecho: o motorista oferece menos vagas num trecho específico
func TestVagasDiferentesPorTrecho(t *testing.T) {
	limpar()
	c, err := PublicarCarona("m1", modelos.Carona{Origem: "Feira de Santana", Destino: "Aracaju", Assentos: 3, Trechos: []modelos.Trecho{
		{Origem: "Feira de Santana", Destino: "Salvador", Valor: 10, HorarioSaida: h(0, "08:00"), HorarioChegada: h(0, "10:00")}, // 0 = capacidade
		{Origem: "Salvador", Destino: "Aracaju", Valor: 10, VagasTotais: 1, HorarioSaida: h(0, "10:30"), HorarioChegada: h(0, "14:00")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if trechoN(c.ID, 0).VagasTotais != 3 || trechoN(c.ID, 0).VagasLivres != 3 ||
		trechoN(c.ID, 1).VagasTotais != 1 || trechoN(c.ID, 1).VagasLivres != 1 {
		t.Fatalf("vagas iniciais erradas: %+v", c.Trechos)
	}

	// p1 vai de Feira a Aracaju (usa os dois trechos): o trecho 2 lota
	comprar(t, "p1", modelos.Carona{Trechos: c.Trechos})
	if trechoN(c.ID, 0).VagasLivres != 2 || trechoN(c.ID, 1).VagasLivres != 0 {
		t.Fatal("desconto por trecho errado")
	}
	// p2 não consegue ir até Aracaju, nem a busca mostra essa opção...
	if err := ReservarVagasTemporariamente("p2", c.Trechos, false); err == nil {
		t.Fatal("vendeu vaga inexistente no trecho 2")
	}
	if res, _ := BuscarItinerarios(modelos.FiltroBusca{Origem: "Feira de Santana", Destino: "Aracaju", Data: d(0)}); len(res) != 0 {
		t.Fatalf("busca mostrou trecho lotado: %+v", res)
	}
	// ...mas ainda pode ir só até Salvador (o trecho 1 tem vagas)
	if res, _ := BuscarItinerarios(modelos.FiltroBusca{Origem: "Feira de Santana", Destino: "Salvador", Data: d(0)}); len(res) != 1 {
		t.Fatal("busca escondeu o trecho 1 com vagas")
	}
	comprar(t, "p2", modelos.Carona{Trechos: c.Trechos[:1]})
	if trechoN(c.ID, 0).VagasLivres != 1 || trechoN(c.ID, 1).VagasLivres != 0 {
		t.Fatal("desconto do trecho 1 errado")
	}
	// Reinício: as vagas são recalculadas pelo total DE CADA TRECHO
	ReconciliarEstado()
	if trechoN(c.ID, 0).VagasLivres != 1 || trechoN(c.ID, 1).VagasLivres != 0 {
		t.Fatal("reconciliação ignorou as vagas por trecho")
	}
}

// Gravação agrupada: cadastros simultâneos acabam TODOS no arquivo, com menos escritas
func TestPersistenciaConcorrente(t *testing.T) {
	limpar()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := CadastrarUsuario(modelos.Usuario{ID: fmt.Sprintf("u%d", i), Nome: "U", Senha: "1", Tipo: "passageiro"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	bytesJSON, err := os.ReadFile(CaminhoArquivo(ArquivoUsuarios))
	if err != nil {
		t.Fatal(err)
	}
	var gravados map[string]modelos.Usuario
	if err := json.Unmarshal(bytesJSON, &gravados); err != nil {
		t.Fatal(err)
	}
	if len(gravados) != 200 {
		t.Fatalf("o arquivo tem %d usuários, esperado 200", len(gravados))
	}
	if _, err := os.Stat(CaminhoArquivo(ArquivoUsuarios) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("sobrou arquivo temporário")
	}
}

// Central de notificações: quem recebe o quê, contador e limite
func TestNotificacoes(t *testing.T) {
	limpar()
	TempoReservaCarrinho = 200 * time.Millisecond
	defer func() { TempoReservaCarrinho = time.Minute }()
	CadastrarUsuario(modelos.Usuario{ID: "m1", Nome: "Joao", Senha: "1", Tipo: "motorista"})
	CadastrarUsuario(modelos.Usuario{ID: "p1", Nome: "Ana", Senha: "1", Tipo: "passageiro"})

	c1 := publicarRota(t, "m1", 3, perna{"Feira de Santana", "Salvador", h(0, "08:00"), h(0, "10:00"), 10})
	c2 := publicarRota(t, "m2", 3, perna{"Salvador", "Aracaju", h(0, "11:00"), h(0, "15:00"), 10})

	contem := func(usuario, trecho string) bool {
		valor, ok := Notificacoes.Load(usuario)
		if !ok {
			return false
		}
		reg := valor.(*RegistroNotificacoes)
		reg.Lock()
		defer reg.Unlock()
		for _, n := range reg.Lista {
			if strings.Contains(n.Mensagem, trecho) {
				return true
			}
		}
		return false
	}

	// Compra confirmada: os dois motoristas são avisados, com o nome do passageiro
	id := comprar(t, "p1", c1, c2)
	if ContarNotificacoesNaoLidas("m1") != 1 || ContarNotificacoesNaoLidas("m2") != 1 || !contem("m1", "Ana confirmou") {
		t.Fatal("motoristas não foram avisados da compra")
	}
	if ContarNotificacoesNaoLidas("p1") != 0 {
		t.Fatal("passageiro recebeu aviso da própria compra")
	}

	// Abrir zera o contador e devolve as novas marcadas como não lidas (mais novas primeiro)
	lista := AbrirNotificacoes("m1")
	if len(lista) != 1 || lista[0].Lida || ContarNotificacoesNaoLidas("m1") != 0 {
		t.Fatalf("abrir: %+v", lista)
	}
	if lista = AbrirNotificacoes("m1"); len(lista) != 1 || !lista[0].Lida {
		t.Fatal("a segunda abertura deveria mostrar a notificação como lida")
	}
	if lista = AbrirNotificacoes("ninguem"); lista == nil || len(lista) != 0 {
		t.Fatal("usuário sem notificações deveria receber lista vazia")
	}

	// Passageiro cancela: motoristas avisados
	CancelarItinerarioPassageiro("p1", id)
	if !contem("m1", "Ana cancelou") || !contem("m2", "Ana cancelou") || ContarNotificacoesNaoLidas("m1") != 1 {
		t.Fatal("motoristas não foram avisados do cancelamento")
	}

	// Cascata: passageiro avisado com o nome de quem cancelou; o OUTRO motorista também
	comprar(t, "p1", c1, c2)
	AbrirNotificacoes("m1")
	AbrirNotificacoes("m2")
	CancelarCaronaPeloMotorista("m1", c1.ID)
	if !contem("p1", "foi cancelada porque o motorista Joao cancelou a carona Feira de Santana") {
		t.Fatal("passageiro não foi avisado da cascata")
	}
	if !contem("m2", "outra carona da mesma viagem foi cancelada") || ContarNotificacoesNaoLidas("m2") != 1 {
		t.Fatal("o outro motorista não foi avisado")
	}
	if ContarNotificacoesNaoLidas("m1") != 0 {
		t.Fatal("quem cancelou não deveria ser avisado")
	}

	// Expiração do carrinho: passageiro avisado
	AbrirNotificacoes("p1")
	ReservarVagasTemporariamente("p1", c2.Trechos, false)
	time.Sleep(400 * time.Millisecond)
	if ContarNotificacoesNaoLidas("p1") != 1 || !contem("p1", "expirou") {
		t.Fatal("passageiro não foi avisado da expiração")
	}

	// Limite por usuário e persistência
	for i := 0; i < MaxNotificacoesPorUsuario+10; i++ {
		notificar("p9", "aviso %d", i)
	}
	salvarNotificacoes()
	if lista := AbrirNotificacoes("p9"); len(lista) != MaxNotificacoesPorUsuario || lista[0].Mensagem != fmt.Sprintf("aviso %d", MaxNotificacoesPorUsuario+9) {
		t.Fatalf("limite: %d, mais nova: %q", len(lista), lista[0].Mensagem)
	}
	bytesJSON, _ := os.ReadFile(CaminhoArquivo(ArquivoNotificacoes))
	var gravadas map[string][]modelos.Notificacao
	json.Unmarshal(bytesJSON, &gravadas)
	if len(gravadas["p9"]) != MaxNotificacoesPorUsuario || !gravadas["p9"][0].Lida {
		t.Fatal("notificações não foram gravadas com o estado de lidas")
	}
}
