# VaiJunto: Sistema de Caronas Compartilhadas

Protótipo distribuído de caronas de média e longa distância (TEC502, Problema 1).
Motoristas publicam rotas com paradas, e passageiros buscam e reservam assentos **por trecho**, inclusive combinando carros de motoristas diferentes.
Toda a comunicação usa **sockets TCP nativos** com mensagens **JSON**, e o controle de concorrência é feito pelo próprio servidor, sem banco de dados.

- **Linguagem:** Go 1.26, só biblioteca padrão, sem frameworks.
- **Especificação do protocolo:** [`docs/PROTOCOLO.md`](docs/PROTOCOLO.md)

---

## Sumário
1. [Como executar](#1-como-executar)
2. [Arquitetura](#2-arquitetura)
3. [Modelo de dados](#3-modelo-de-dados)
4. [Comunicação e protocolo](#4-comunicação-e-protocolo)
5. [Busca de itinerários](#5-busca-de-itinerários)
6. [Concorrência](#6-concorrência)
7. [Atomicidade da reserva](#7-atomicidade-da-reserva)
8. [Confiabilidade](#8-confiabilidade)
9. [Clientes](#9-clientes)
10. [Testes](#10-testes)
11. [Docker e execução em máquinas distintas](#11-docker-e-execução-em-máquinas-distintas)
12. [Estrutura do projeto](#12-estrutura-do-projeto)

---

## 1. Como executar

### Sem Docker (desenvolvimento)
Cada comando em um terminal. Os dados do servidor ficam na pasta de onde ele é executado.
```bash
cd cmd/servidor && go run .                 # servidor na porta 8081
go run ./cmd/cliente_motorista              # cliente motorista
go run ./cmd/cliente_passageiro             # cliente passageiro
```
Para usar outro servidor: `SERVER_URL=192.168.0.10:8081 go run ./cmd/cliente_passageiro`.

### Com Docker (uma máquina)
```bash
docker compose up --build -d vaijunto-server         # servidor em segundo plano
docker compose run --rm motorista-1                  # cliente motorista (interativo)
docker compose run --rm passageiro-1                 # cliente passageiro (interativo)
docker compose logs -f vaijunto-server               # acompanhar o servidor
```

Para demonstrar a expiração do carrinho sem esperar 1 minuto, suba o servidor com um prazo menor. O log mostra o valor em uso (`Tempo de reserva do carrinho: 20s`).
```bash
VAIJUNTO_TEMPO_RESERVA=20s docker compose up -d vaijunto-server
```

### Variáveis de ambiente

| Variável | Onde | Padrão | Uso |
|---|---|---|---|
| `PORTA` | servidor | `8081` | porta TCP |
| `VAIJUNTO_DADOS` | servidor | pasta atual | pasta dos arquivos JSON |
| `VAIJUNTO_TEMPO_RESERVA` | servidor | `1m` | tempo que as vagas ficam no carrinho, renovado a cada trecho adicionado (ex.: `20s`). No compose, pode ser passado na hora de subir (ver abaixo) |
| `TZ` | servidor | do sistema | fuso para comparar horários (no compose: `America/Bahia`) |
| `SERVER_URL` | clientes / teste | `localhost:8081` | endereço do servidor |
| `IP_EXTERNO` | docker compose | — | `IP:porta` do servidor quando ele está em outra máquina |

---

## 2. Arquitetura

```
 ┌────────────────────┐        TCP / JSON por linha        ┌──────────────────────────────────────┐
 │ Cliente Motorista  │ ─────────────────────────────────▶ │           Servidor central           │
 │ (terminal)         │ ◀───────────────────────────────── │                                      │
 └────────────────────┘                                    │  goroutine por conexão (sessão)      │
 ┌────────────────────┐                                    │           │                          │
 │ Cliente Passageiro │ ─────────────────────────────────▶ │  internal/estado (regras + locks)    │
 │ (terminal)         │ ◀───────────────────────────────── │   Usuarios / Caronas / Itinerarios / │
 └────────────────────┘                                    │   CarrinhosPendentes  (sync.Map)     │
 ┌────────────────────┐                                    │           │                          │
 │ Teste de carga     │ ═══ centenas de conexões ════════▶ │  usuarios, caronas, itinerarios,     │
 └────────────────────┘                                    │  notificacoes .json (grav. atômica)  │
                                                           └──────────────────────────────────────┘
```

| Componente | Papel |
|---|---|
| **Servidor** (`cmd/servidor`) | Única fonte da verdade. Aceita conexões, mantém a sessão de cada uma, valida as mensagens e aplica as regras. |
| **Estado** (`internal/estado`) | Regras de negócio, estruturas em memória, travas, busca, carrinho, cancelamentos e persistência. |
| **Protocolo** (`internal/protocolo`) | Formato das mensagens, leitura com limite de tamanho, envio com prazo e códigos de status. |
| **Modelos** (`internal/modelos`) | Estruturas compartilhadas (carona, trecho, itinerário…) e constantes de negócio. |
| **Cliente motorista / passageiro** | Aplicativos de terminal. Não guardam estado de negócio; só mostram o que o servidor responde. |
| **Teste de carga** (`cmd/TesteCarga`) | Clientes simultâneos que verificam a corretude e medem o tempo de resposta. |

Não há réplicas: um único servidor mantém todo o estado, como pede o enunciado. O estado fica em memória e é gravado em JSON a cada mudança.

---

## 3. Modelo de dados

```
Carona (motorista)                        Itinerario (passageiro = a passagem)
 ├─ id, motorista_id                       ├─ id, passageiro_id
 ├─ origem → destino, data_hora            ├─ origem → destino, valor_total
 ├─ assentos  (capacidade do carro)        └─ trechos[]  ← cópias dos trechos comprados,
 └─ trechos[] ───────────────┐                            podem ser de caronas diferentes
                             ▼
             Trecho (unidade de venda)
              ├─ id, carona_id, motorista_id
              ├─ origem → destino, horario_saida, horario_chegada
              ├─ valor                      (preço por trecho)
              ├─ vagas_totais, vagas_livres (assentos DESTE trecho)
              └─ passageiros[]              (lista de embarque)
```

- **Rota:** sequência ordenada de cidades, representada pelos trechos encadeados da carona (A→B, B→C, …).
- **Assento:** controlado **por trecho**. Um assento ocupado em A→B continua livre em B→C.
  - O motorista informa a capacidade do carro e pode oferecer **menos vagas em um trecho específico** (de 1 até a capacidade).
- **Passageiro em várias cidades:** ele embarca e desembarca em qualquer par de cidades da rota, e cada trecho usado desconta uma vaga.
- **Carrinho:** estado temporário de uma compra em andamento. Guarda as cópias **oficiais** dos trechos já reservados e um prazo.
- **Datas:** `AAAA-MM-DD HH:MM` com dois dígitos, para que a ordem alfabética seja a ordem no tempo.

Mapas em memória (`internal/estado`):

| Mapa | Chave → Valor | Guarda |
|---|---|---|
| `Usuarios` | id → `Usuario` | cadastros |
| `Caronas` | id → `*RegistroCarona` (carona + **mutex próprio**) | a fonte da verdade das vagas |
| `Itinerarios` | id → `Itinerario` | passagens confirmadas |
| `CarrinhosPendentes` | passageiro → `*RegistroCarrinho` (trechos + prazo + **mutex próprio**) | compras em andamento |
| `Notificacoes` | usuário → `*RegistroNotificacoes` (lista + **mutex próprio**) | central de notificações (`notificacoes.json`) |

---

## 4. Comunicação e protocolo

- **Sockets:** `net.Listen("tcp")` no servidor e `net.DialTimeout` nos clientes. `Accept` em laço, com **uma goroutine por conexão**.
- **Enquadramento:** uma mensagem JSON por linha (`\n`), com no máximo 64 KiB.
  - A leitura usa `bufio.Reader.ReadSlice` acumulando até o limite (`protocolo.LerMensagem`). Um cliente não consegue mandar uma "linha infinita".
- **Requisição:** `{"acao": ..., "payload": ...}`. **Resposta:** `{"status": ..., "mensagem": ..., "payload": ...}`.
- **Sessão:** o login fica associado à conexão. Toda ação usa o ID da sessão e **ignora IDs vindos no payload**. Cada ação exige um perfil (motorista ou passageiro).
- **Por que TCP:** reserva é uma operação que não pode se perder nem chegar fora de ordem. O TCP entrega de forma confiável e ordenada, e o fechamento da conexão (EOF/reset) avisa o servidor de que o cliente saiu.
- **Interoperabilidade:** JSON em UTF-8 é independente de linguagem e arquitetura. Um cliente em outra linguagem (Python, Java…) só precisa de um socket TCP e de um parser JSON.

Detalhes, campos de cada operação e exemplos reais: [`docs/PROTOCOLO.md`](docs/PROTOCOLO.md).

### Encapsulamento e validação
1. **JSON inválido:** a linha é recusada com `400`.
2. **Ação:** a ação precisa existir (`400`) e o perfil da sessão precisa ser o exigido (`401`/`403`).
3. **Payload:** fica cru (`json.RawMessage`) até a ação ser conhecida e só então é convertido para a struct esperada. Formato errado ou ausente dá `400`.
4. **Regras de negócio:** a camada de estado valida cidades, formato exato de datas, encadeamento de trechos, horários, valores, vagas, IDs e tipos. Cada erro volta com o status adequado (`400`/`403`/`404`/`409`) e uma mensagem legível.
5. **Falha inesperada:** um `recover` por requisição transforma um pânico em `500` **só naquela requisição**, e o servidor continua atendendo.

---

## 5. Busca de itinerários

`BUSCAR_ITINERARIOS` (`internal/estado/busca.go`):

1. **Foto do grafo.** Cada carona é travada rapidamente para copiar os trechos **com vaga** que ainda não partiram.
   - O grafo tem as cidades como vértices e cada trecho como aresta.
   - A busca em si roda sem nenhuma trava.
2. **BFS sobre caminhos.** A busca parte da origem, e cada estado da fila é um itinerário parcial. Um caminho só é estendido se:
   - o próximo trecho sai da cidade onde o anterior chega;
   - ele sai **depois** que o anterior chega;
   - a cidade de destino dele ainda não foi visitada.
   - Há também um limite de 6 trechos.
3. **Filtro de data.** O primeiro trecho sai na data pedida (`data`) ou a partir de um horário (`horario_minimo`, usado para continuar uma viagem).
4. **Combinação de motoristas.** Como a aresta é o trecho, trechos da mesma carona e de caronas diferentes se combinam naturalmente. Com isso, o passageiro embarca e desembarca em **qualquer par de cidades** da rota do motorista.
5. **Ordenação:** menos trocas de carro → chegada mais cedo → menor preço. São no máximo 30 opções.

No cliente, o passageiro monta a viagem **carro por carro**. O aplicativo agrupa os itinerários pelo primeiro carro e mostra, para cada opção, a melhor chegada possível até o destino. Assim, só aparecem escolhas que ainda chegam ao destino.

---

## 6. Concorrência

- **Uma goroutine por cliente.** Conexões lentas não atrasam as outras, e o runtime do Go multiplexa as goroutines sobre poucas threads do sistema, com I/O não bloqueante (netpoller).
- **Travas de granularidade fina.**
  - `sync.Map` para os índices: leituras concorrentes sem trava global.
  - Cada carona tem **seu próprio `sync.Mutex`** (`RegistroCarona`). Passageiros disputando carros diferentes não se bloqueiam.
  - Cada carrinho tem seu próprio mutex (`RegistroCarrinho`).
- **Cópias fora da trava.** Toda leitura que vira JSON é copiada (cópia profunda) **com a trava segura**, e a serialização acontece depois. Isso evita condições de corrida, verificadas com `go test -race`.
- **Persistência sem gargalo global.**
  - Cada arquivo JSON tem o próprio mutex, então arquivos diferentes são gravados em paralelo.
  - As gravações são **agrupadas**: quem espera e já teve a mudança incluída pela gravação anterior não grava de novo.
  - No teste de carga isso levou o cadastro de ~90 ms para ~12 ms em média.

---

## 7. Atomicidade da reserva

A compra tem duas etapas:

1. **Reserva (`ADICIONAR_CARRINHO`).** Ao escolher o carro de um trecho, a vaga é **descontada na hora**, sob o mutex daquela carona. Quem reserva primeiro fica com o assento.
   - Numa lista de trechos, se **qualquer** um falhar (vaga esgotada, carona cancelada, trecho que não encadeia), os que já foram descontados nessa requisição são devolvidos (*rollback*). A requisição não deixa nada pela metade.
   - Os trechos seguintes da viagem entram no **mesmo carrinho** (`continuacao=true`) e renovam o prazo.
2. **Confirmação (`EFETIVAR_ITINERARIO`).**
   - O carrinho é fechado de forma exclusiva: **ou** a confirmação **ou** o cronômetro de expiração vence, nunca os dois.
   - Em seguida, o servidor trava **todas** as caronas envolvidas **em ordem crescente de ID**, confere que nenhuma foi cancelada e, com todas travadas, inclui o passageiro em cada lista de embarque e grava o itinerário.
   - Se alguma carona foi cancelada, nada é gravado e as vagas dos outros trechos voltam.

**Deadlock:** não ocorre, por duas regras:
- **Uma trava por vez na reserva.** A reserva segura a trava de uma carona só, e a solta antes de pegar a próxima.
- **Ordem global nas demais operações.** Confirmação e cancelamentos travam várias caronas ao mesmo tempo, sempre em **ordem crescente de ID**. Assim, dois passageiros que usam as mesmas caronas em ordens diferentes pedem as travas na mesma sequência e nunca ficam esperando um pelo outro em ciclo.
- A gravação em disco acontece **depois** de soltar as travas das caronas.

**Quando o último trecho não está mais disponível:**
- A reserva desse trecho responde `409`, e as vagas dos trechos anteriores continuam no carrinho até o prazo acabar.
- Ao expirar, o servidor devolve tudo.
- Uma busca nova (`continuacao=false`) descarta o carrinho anterior na hora.

**Cancelamentos:**
- **Passageiro:** trava as caronas da viagem (em ordem), apaga o itinerário e devolve **todas** as vagas.
- **Motorista (cascata):**
  1. Marca a carona como cancelada; a partir daí nenhuma reserva ou confirmação a aceita.
  2. Remove a carona.
  3. Cancela cada itinerário afetado, devolvendo também as vagas nos carros dos outros motoristas.
- **Corrida entre cancelamentos:** o itinerário é removido com `LoadAndDelete` dentro das travas, então dois cancelamentos simultâneos nunca devolvem a mesma vaga duas vezes.

---

## 8. Confiabilidade

| Situação | Tratamento |
|---|---|
| Cliente fecha ou cai no meio de uma reserva | A goroutine termina ao receber EOF/erro. As vagas presas no carrinho voltam quando o prazo expira (cronômetro por carrinho). |
| Assentos bloqueados para sempre | Impossível: todo carrinho tem um cronômetro. Ao reiniciar, as vagas são **recalculadas** a partir das passagens confirmadas (`ReconciliarEstado`). |
| Servidor cai (kill -9) | Os JSON são gravados de forma **atômica** (arquivo `.tmp` + `rename`). Na subida, o servidor reconstrói listas de embarque e vagas e descarta itinerários órfãos. Se um arquivo não puder ser lido, o servidor não sobe (para não sobrescrever dados). |
| Mensagem malformada | Resposta `400`; a conexão continua. |
| Pânico ao processar uma requisição | `recover` responde `500` só para aquela requisição. |
| Mensagem gigante | Resposta `413`, e a conexão é fechada (não há como achar a próxima mensagem). |
| Cliente ocioso | Prazo de leitura de **30 min** (`SetReadDeadline`), renovado a cada mensagem. |
| Cliente que não lê respostas | Prazo de escrita de **10 s** (`SetWriteDeadline`). |
| Servidor lento ou fora do ar (visto pelo cliente) | 5 s para conectar e 15 s por resposta. O cliente mostra "Conexão com o servidor perdida" e encerra. |
| Motorista cancela durante uma compra | A confirmação detecta a carona cancelada, não grava nada e devolve as vagas dos outros trechos. |

---

## 9. Clientes

### Motorista
1. **Cadastrar / Login**
2. **Publicar carona**
   - O motorista informa origem, destino, data e hora, e a capacidade do carro.
   - Com paradas, informa **trecho a trecho**: cidade, valor, **vagas oferecidas no trecho** (Enter = capacidade), chegada e a saída do próximo.
   - As entradas inválidas são pedidas de novo (cidade não atendida, data fora do formato, número fora da faixa).
3. **Consultar passageiros confirmados:** cada carona com seus trechos, vagas livres/totais e os IDs dos passageiros de **cada trecho**.
4. **Cancelar carona:** lista, escolha e confirmação. Avisa que as viagens dos passageiros serão canceladas em cascata.
5. **Central de notificações `[x]`:** avisos de passageiros que confirmaram ou cancelaram uma vaga, ou que saíram da carona porque outra carona da mesma viagem foi cancelada.

### Passageiro
1. **Buscar itinerários**
   - O passageiro informa origem, destino e data, e o servidor devolve os itinerários possíveis.
   - Ele escolhe **o carro de cada pedaço da viagem**, e a vaga é reservada na hora.
   - Cada opção mostra o horário, o preço e a melhor chegada até o destino.
2. **Abrir carrinho:** mostra os trechos reservados e o total, e pede a confirmação da compra.
3. **Consultar minhas viagens:** passagens confirmadas, com o motorista de cada trecho.
4. **Cancelar viagem:** cancela a viagem inteira e devolve as vagas.
5. **Central de notificações `[x]`:** avisos de viagem cancelada pelo motorista e de reserva expirada.

### Central de notificações
- **Sem pop-ups.** O menu mostra `Central de notificações [x]`, onde `x` é quantos avisos chegaram desde a última abertura (`CONTAR_NOTIFICACOES`, pedido a cada vez que o menu aparece).
- **Abrir zera o contador.** Ao abrir (`ABRIR_NOTIFICACOES`), aparecem os avisos mais novos primeiro, com 🆕 nos que ainda não tinham sido vistos, e o contador volta a zero.
- **Protocolo inalterado.** O servidor só guarda os avisos; quem pergunta é o cliente. Isso mantém o modelo requisição e resposta, sem mensagens enviadas pelo servidor por conta própria.
- **Concorrência.** Cada usuário tem sua lista com mutex próprio. O aviso é criado com as travas de carona seguras (a trava da lista nunca espera por outra), e a gravação em disco acontece depois de soltá-las.
- **Limite.** Até 50 avisos por usuário, gravados em `notificacoes.json` e mantidos após reinícios.

---

## 10. Testes

### Testes automatizados (unitários e de protocolo)
```bash
go test -race ./...
```
- **`internal/estado/estado_test.go`:**
  - baldeação, cancelamento e cascata;
  - carona cancelada durante a compra e disputa entre confirmação e expiração;
  - expiração e renovação do carrinho, e nova busca descartando o carrinho anterior;
  - preço oficial (payload adulterado) e trechos não encadeados;
  - validações, busca (data, conexões, pares da rota, ordenação), vagas diferentes por trecho;
  - reconciliação após queda e gravação concorrente;
  - central de notificações (quem é avisado em cada evento, contador que zera ao abrir, limite e gravação);
  - um **teste de estresse** com compras, cancelamentos e cascatas simultâneos que confere as invariantes.
- **`cmd/servidor/mainServer_test.go`** (via `net.Pipe`): mensagens malformadas, sessão e perfis, IDs do payload ignorados, mensagem acima do limite e central de notificações.

### Teste de carga (múltiplos clientes TCP simultâneos)
> ⚠️ O teste cria usuários e caronas de verdade: rode contra um **servidor de testes**.

```bash
# servidor de testes com dados descartáveis e reserva curta
mkdir -p /tmp/vaijunto-teste
cd cmd/servidor && VAIJUNTO_DADOS=/tmp/vaijunto-teste VAIJUNTO_TEMPO_RESERVA=5s go run .

# em outro terminal
go run ./cmd/TesteCarga -servidor localhost:8081 -passageiros 300 -vagas 5 -espera 7s
```

| Opção | Padrão | Descrição |
|---|---|---|
| `-servidor` | `$SERVER_URL` ou `localhost:8081` | servidor alvo |
| `-passageiros` | 200 | passageiros simultâneos |
| `-vagas` | 5 | vagas por trecho nas caronas do teste |
| `-cancelar` | true | um motorista cancela a carona no meio da disputa |
| `-espera` | 65s | espera para os carrinhos abandonados expirarem antes da última verificação (use um valor maior que o `VAIJUNTO_TEMPO_RESERVA` do servidor) |

O que o teste faz:
1. **Fase 1:** 100 conexões cadastram o **mesmo** ID ao mesmo tempo. Exatamente 1 cadastro pode ser aceito.
2. **Fase 2:** três motoristas publicam caronas que se cruzam (direta com parada, e duas que se combinam), gerando 3 itinerários possíveis de Feira de Santana a Aracaju.
3. **Fase 3:** N passageiros ficam prontos e **largam juntos**. Cada um busca, escolhe um itinerário ao acaso, reserva carro por carro e confirma. No meio da disputa, um motorista cancela a carona.
4. **Fase 4:** a verificação é feita só pelo protocolo e confere que:
   - nenhum itinerário ficou pela metade;
   - cada itinerário tem os trechos escolhidos;
   - nenhuma compra que falhou gerou passagem;
   - nenhuma passagem sumiu sem motivo;
   - nenhum itinerário usa a carona cancelada;
   - **nenhum assento foi vendido duas vezes**;
   - as listas de embarque batem com as passagens;
   - depois do prazo, **nenhum assento ficou bloqueado**.
5. **Relatório:** tempo de resposta por operação (média, p50, p95, p99, máximo). O programa sai com código 1 se alguma verificação falhar.

Resultado de referência: 300 passageiros e 5 vagas por trecho, com servidor e teste na mesma máquina (notebook Linux).

| Operação | Qtd | Média | P95 | P99 |
|---|---|---|---|---|
| ADICIONAR_CARRINHO | 193 | 3,96 ms | 9,15 ms | 11,87 ms |
| BUSCAR_ITINERARIOS | 300 | 11,58 ms | 16,86 ms | 18,09 ms |
| CADASTRAR | 403 | 11,83 ms | 24,26 ms | 25,40 ms |
| EFETIVAR_ITINERARIO | 10 | 7,21 ms | 11,53 ms | 11,53 ms |
| LOGIN | 303 | 2,03 ms | 10,21 ms | 14,42 ms |
| **Todas** | 1519 | 6,41 ms | 19,35 ms | 24,61 ms |

Todas as verificações passaram, sem falhas de rede. Nessa execução, os dois trechos que sobraram terminaram com 5/5 assentos vendidos, e 3 passagens foram canceladas em cascata pelo motorista que desistiu.

### Pelo Docker
```bash
docker compose --profile teste run --rm teste-carga -passageiros 300 -espera 70s
# -espera precisa ser maior que o VAIJUNTO_TEMPO_RESERVA do servidor (padrão 1m)
# contra um servidor em outra máquina:
IP_EXTERNO=192.168.0.10:8081 docker compose --profile teste run --rm --no-deps teste-carga
```

---

## 11. Docker e execução em máquinas distintas

Cada componente tem sua imagem (`Dockerfile.servidor`, `Dockerfile.motorista`, `Dockerfile.passageiro`, `Dockerfile.teste`). Todas usam build em dois estágios: compilação em `golang:1.26-alpine` e execução em `alpine`.

**Máquina do servidor:**
```bash
docker compose up --build -d vaijunto-server
```
- A porta 8081 do contêiner é publicada na 8081 da máquina (`ports: "8081:8081"`), e os clientes de outras máquinas se conectam ao **IP da máquina hospedeira**. Libere a porta no firewall se necessário.
- A pasta `cmd/servidor` é montada em `/root/dados` (volume), então os dados sobrevivem à recriação do contêiner.
- A pasta é montada inteira, e não arquivo por arquivo, porque a gravação atômica usa `rename`.

> Se já houver um servidor local usando a porta 8081 (por exemplo, rodando com `go run`), pare-o antes.

**Máquinas dos clientes:**
```bash
IP_EXTERNO=<ip-do-servidor>:8081 docker compose run --rm --no-deps passageiro-1
IP_EXTERNO=<ip-do-servidor>:8081 docker compose run --rm --no-deps motorista-1
```
- `--no-deps` evita subir outro servidor localmente.
- `IP_EXTERNO` substitui o endereço interno (`vaijunto-server:8081`), que só funciona **dentro** da rede do Compose de uma mesma máquina.
- Esse era o problema de conectividade entre máquinas: o nome do serviço só é resolvido pelo DNS interno do Docker, então entre máquinas o cliente precisa usar o IP real do hospedeiro, com a porta publicada.
- Vários clientes: basta repetir o comando em quantos terminais ou máquinas quiser.

**Vantagens:** o mesmo ambiente em qualquer máquina do laboratório, várias instâncias de cliente com um comando, isolamento de dependências, e servidor e dados separados do código.

---

## 12. Estrutura do projeto

```
VaiJunto/
├── cmd/
│   ├── servidor/            # servidor TCP (+ testes de protocolo) e arquivos JSON
│   ├── cliente_motorista/   # aplicativo do motorista
│   ├── cliente_passageiro/  # aplicativo do passageiro
│   └── TesteCarga/          # teste de carga e corretude
├── internal/
│   ├── estado/              # regras, travas, carrinho, busca, notificações, persistência (+ testes)
│   ├── protocolo/           # mensagens, leitura/escrita com limites e prazos
│   ├── modelos/             # estruturas e constantes compartilhadas
│   └── cli/                 # leitura validada do teclado e requisições dos clientes
├── docs/PROTOCOLO.md        # especificação do protocolo
├── docker-compose.yaml
└── Dockerfile.{servidor,motorista,passageiro,teste}
```

### Limitações conhecidas (protótipo)
- As senhas ficam em texto puro no `usuarios.json`, e a conexão não é criptografada.
- Existe um único servidor, sem réplicas (exigência do enunciado).
- As cidades atendidas são uma lista fixa (`modelos.CidadesValidas`).
- Viagens antigas não são removidas automaticamente, apenas deixam de aparecer nas buscas.
