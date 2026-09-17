package main

// Testes do servidor pela conexão (protocolo): mensagens malformadas, sessão,
// perfis e limite de tamanho. Usa net.Pipe, sem abrir portas.
import (
	"VaiJunto/internal/estado"
	"VaiJunto/internal/protocolo"
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	pasta, _ := os.MkdirTemp("", "vaijunto-servidor")
	os.Setenv("VAIJUNTO_DADOS", pasta) // nunca grava nos arquivos reais
	os.Exit(m.Run())
}

type conexaoTeste struct {
	t      *testing.T
	conn   net.Conn
	leitor *bufio.Reader
}

func abrirConexao(t *testing.T) *conexaoTeste {
	ladoServidor, ladoCliente := net.Pipe()
	go tratarConexao(ladoServidor)
	t.Cleanup(func() { ladoCliente.Close() })
	return &conexaoTeste{t: t, conn: ladoCliente, leitor: bufio.NewReader(ladoCliente)}
}

// Envia uma linha crua e devolve a resposta
func (c *conexaoTeste) enviar(linha string) protocolo.MensagemResposta {
	c.t.Helper()
	c.conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.conn.Write([]byte(linha + "\n")); err != nil {
		c.t.Fatalf("falha ao enviar: %v", err)
	}
	dados, err := protocolo.LerMensagem(c.leitor)
	if err != nil {
		c.t.Fatalf("falha ao ler resposta: %v", err)
	}
	var resposta protocolo.MensagemResposta
	if err := json.Unmarshal(dados, &resposta); err != nil {
		c.t.Fatalf("resposta não é JSON: %s", dados)
	}
	return resposta
}

func (c *conexaoTeste) acao(acao string, payload any) protocolo.MensagemResposta {
	c.t.Helper()
	bytesReq, _ := json.Marshal(protocolo.MensagemRequisicao{Acao: acao, Payload: payload})
	return c.enviar(string(bytesReq))
}

func esperarStatus(t *testing.T, nome string, resposta protocolo.MensagemResposta, status int) {
	t.Helper()
	if resposta.Status != status {
		t.Errorf("%s: status %d (%s), esperado %d", nome, resposta.Status, resposta.Mensagem, status)
	}
}

func TestMensagensMalformadas(t *testing.T) {
	limparUsuarios()
	c := abrirConexao(t)
	esperarStatus(t, "JSON inválido", c.enviar("{isso nao e json"), 400)
	esperarStatus(t, "JSON truncado", c.enviar(`{"acao":"LOGIN","payload":{"id":`), 400)
	esperarStatus(t, "ação desconhecida", c.enviar(`{"acao":"DERRUBAR"}`), 400)
	esperarStatus(t, "sem ação", c.enviar(`{}`), 400)
	esperarStatus(t, "payload ausente", c.enviar(`{"acao":"LOGIN"}`), 400)
	esperarStatus(t, "payload de tipo errado", c.enviar(`{"acao":"CADASTRAR","payload":"abc"}`), 400)

	// Linha vazia é ignorada e a conexão segue funcionando
	c.conn.Write([]byte("\n"))
	esperarStatus(t, "depois da linha vazia", c.enviar(`{"acao":"DERRUBAR"}`), 400)

	c.acao("CADASTRAR", map[string]string{"id": "pm", "nome": "P", "senha": "1", "tipo": "passageiro"})
	esperarStatus(t, "login", c.acao("LOGIN", map[string]string{"id": "pm", "senha": "1", "tipo": "passageiro"}), 200)
	esperarStatus(t, "carrinho vazio", c.acao("ADICIONAR_CARRINHO", map[string]any{"trechos": []any{}}), 400)
	esperarStatus(t, "trecho inexistente", c.acao("ADICIONAR_CARRINHO", map[string]any{"trechos": []any{map[string]string{"id": "x", "carona_id": "y"}}}), 409)
	esperarStatus(t, "efetivar sem carrinho", c.acao("EFETIVAR_ITINERARIO", nil), 404)
	esperarStatus(t, "busca com data errada", c.acao("BUSCAR_ITINERARIOS", map[string]string{"origem": "Salvador", "destino": "Aracaju", "data": "amanhã"}), 400)
	esperarStatus(t, "cancelar sem id", c.acao("CANCELAR_ITINERARIO", map[string]string{}), 400)
}

// Cada teste começa sem usuários (o estado em memória é do processo inteiro)
func limparUsuarios() {
	estado.Usuarios.Range(func(chave, _ any) bool { estado.Usuarios.Delete(chave); return true })
}

func TestSessaoEPerfis(t *testing.T) {
	limparUsuarios()
	motorista := abrirConexao(t)
	passageiro := abrirConexao(t)

	esperarStatus(t, "cadastro motorista", motorista.acao("CADASTRAR", map[string]string{"id": "mot", "nome": "M", "senha": "1", "tipo": "motorista"}), 201)
	esperarStatus(t, "cadastro repetido", motorista.acao("CADASTRAR", map[string]string{"id": "mot", "nome": "M", "senha": "1", "tipo": "motorista"}), 409)
	esperarStatus(t, "cadastro passageiro", passageiro.acao("CADASTRAR", map[string]string{"id": "pas", "nome": "P", "senha": "1", "tipo": "passageiro"}), 201)

	// Sem login
	esperarStatus(t, "consulta sem login", passageiro.acao("CONSULTAR_VIAGENS_PASSAGEIRO", nil), 401)
	esperarStatus(t, "publicar sem login", motorista.acao("PUBLICAR_CARONA", map[string]any{}), 401)

	// Login
	esperarStatus(t, "senha errada", motorista.acao("LOGIN", map[string]string{"id": "mot", "senha": "x", "tipo": "motorista"}), 401)
	esperarStatus(t, "app errado", passageiro.acao("LOGIN", map[string]string{"id": "mot", "senha": "1", "tipo": "passageiro"}), 403)
	resposta := motorista.acao("LOGIN", map[string]string{"id": "mot", "senha": "1", "tipo": "motorista"})
	esperarStatus(t, "login motorista", resposta, 200)
	if bytesPayload, _ := json.Marshal(resposta.Payload); strings.Contains(string(bytesPayload), "senha") {
		t.Error("o login devolveu a senha")
	}
	esperarStatus(t, "login passageiro", passageiro.acao("LOGIN", map[string]string{"id": "pas", "senha": "1", "tipo": "passageiro"}), 200)

	// Perfil errado
	esperarStatus(t, "passageiro publica", passageiro.acao("PUBLICAR_CARONA", map[string]any{}), 403)
	esperarStatus(t, "motorista busca", motorista.acao("BUSCAR_ITINERARIOS", map[string]any{}), 403)

	// O dono da carona é o usuário da sessão, não o do payload
	dia := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	resposta = motorista.acao("PUBLICAR_CARONA", map[string]any{
		"motorista_id": "outra_pessoa", "origem": "Salvador", "destino": "Aracaju", "assentos": 2,
		"trechos": []map[string]any{{"origem": "Salvador", "destino": "Aracaju", "valor": 10,
			"horario_saida": dia + " 08:00", "horario_chegada": dia + " 12:00"}},
	})
	esperarStatus(t, "publicar", resposta, 201)
	if carona, _ := resposta.Payload.(map[string]any); carona["motorista_id"] != "mot" {
		t.Errorf("carona ficou com dono %v", carona["motorista_id"])
	}

	// Logout encerra a sessão
	esperarStatus(t, "logout", passageiro.acao("LOGOUT", nil), 200)
	esperarStatus(t, "consulta depois do logout", passageiro.acao("CONSULTAR_VIAGENS_PASSAGEIRO", nil), 401)
}

func TestMensagemGrandeDemais(t *testing.T) {
	c := abrirConexao(t)
	c.conn.SetDeadline(time.Now().Add(2 * time.Second))

	// net.Pipe não tem buffer: escreve numa goroutine enquanto lê a resposta
	go c.conn.Write([]byte(`{"acao":"LOGIN","payload":"` + strings.Repeat("a", protocolo.TamanhoMaximoMensagem+10) + "\"}\n"))

	dados, err := protocolo.LerMensagem(c.leitor)
	if err != nil {
		t.Fatalf("esperava a resposta 413: %v", err)
	}
	var resposta protocolo.MensagemResposta
	json.Unmarshal(dados, &resposta)
	esperarStatus(t, "mensagem gigante", resposta, protocolo.StatusMensagemGrande)

	if _, err := protocolo.LerMensagem(c.leitor); err == nil {
		t.Error("a conexão deveria ter sido encerrada")
	}
}

func TestCentralNotificacoes(t *testing.T) {
	limparUsuarios()
	c := abrirConexao(t)
	esperarStatus(t, "contar sem login", c.acao("CONTAR_NOTIFICACOES", nil), 401)
	esperarStatus(t, "abrir sem login", c.acao("ABRIR_NOTIFICACOES", nil), 401)

	c.acao("CADASTRAR", map[string]string{"id": "notif", "nome": "N", "senha": "1", "tipo": "motorista"})
	c.acao("LOGIN", map[string]string{"id": "notif", "senha": "1", "tipo": "motorista"})

	resposta := c.acao("CONTAR_NOTIFICACOES", nil)
	esperarStatus(t, "contar", resposta, 200)
	if dados, _ := resposta.Payload.(map[string]any); dados["nao_lidas"] != float64(0) {
		t.Errorf("contador inicial: %v", resposta.Payload)
	}

	resposta = c.acao("ABRIR_NOTIFICACOES", nil)
	esperarStatus(t, "abrir", resposta, 200)
	if lista, ok := resposta.Payload.([]any); !ok || len(lista) != 0 {
		t.Errorf("central vazia deveria ser uma lista vazia: %#v", resposta.Payload)
	}
}
