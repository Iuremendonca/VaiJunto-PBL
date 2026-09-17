# Protocolo de Aplicação VaiJunto

Especificação da API remota usada entre os clientes (motorista e passageiro) e o servidor central.
Qualquer linguagem com sockets TCP e um parser JSON consegue implementar um cliente.

## 1. Transporte e enquadramento

| Item | Definição |
|---|---|
| Transporte | TCP (socket nativo), porta padrão **8081** (variável `PORTA` no servidor) |
| Codificação | JSON em **UTF-8** |
| Enquadramento | **uma mensagem por linha**: cada mensagem é um objeto JSON terminado por `\n` (0x0A) |
| Tamanho máximo | **64 KiB** por mensagem, incluindo o `\n` |
| Modelo | requisição → resposta, **uma de cada vez** por conexão (o cliente só envia a próxima requisição depois de ler a resposta da anterior) |
| Conexões | cada cliente mantém **uma conexão persistente** durante todo o uso |

O JSON não pode conter quebras de linha literais fora de strings. Os serializadores comuns (`json.Marshal` em Go, `json.dumps` em Python) já geram tudo numa linha só. Linhas vazias são ignoradas pelo servidor.

**Por que TCP:** uma reserva não pode se perder nem chegar fora de ordem. O TCP garante entrega confiável e ordenada e avisa quando o outro lado cai (EOF ou reset), que é justamente o sinal usado para detectar a saída de um cliente.

## 2. Formato das mensagens

### Requisição (cliente → servidor)

```json
{"acao": "NOME_DA_ACAO", "payload": { ... }}
```

| Campo | Tipo | Descrição |
|---|---|---|
| `acao` | string | operação desejada (seção 5) |
| `payload` | objeto ou `null` | dados da operação; `null` ou ausente nas ações sem dados |

### Resposta (servidor → cliente)

```json
{"status": 200, "mensagem": "texto para o usuário", "payload": { ... }}
```

| Campo | Tipo | Descrição |
|---|---|---|
| `status` | inteiro | código do resultado (seção 3) |
| `mensagem` | string | descrição legível do resultado ou do erro |
| `payload` | qualquer ou `null` | dados de retorno (só em algumas ações de sucesso) |

## 3. Códigos de status

| Código | Significado | Exemplos |
|---|---|---|
| 200 | sucesso | login, busca, reserva, confirmação |
| 201 | criado | cadastro, publicação de carona |
| 400 | requisição inválida | JSON malformado, ação desconhecida, payload no formato errado, dados de negócio inválidos |
| 401 | não autenticado | ação sem login, ID ou senha incorretos |
| 403 | proibido | ação de outro perfil, cancelar viagem de outra pessoa |
| 404 | não encontrado | nenhum itinerário, carrinho vazio ou expirado |
| 409 | conflito | vaga esgotada, carona cancelada, ID já cadastrado, reserva expirada |
| 413 | mensagem grande demais | mais de 64 KiB (o servidor responde e **fecha a conexão**) |
| 500 | erro interno | falha inesperada ao processar **aquela** requisição (o servidor continua no ar) |

## 4. Fluxo de conexão, autenticação e desconexão

```
Cliente                                   Servidor
   |---- conexão TCP --------------------->|  (uma goroutine por conexão)
   |---- CADASTRAR (opcional) ------------>|
   |<--- 201 ------------------------------|
   |---- LOGIN --------------------------->|  a sessão da conexão passa a ser deste usuário
   |<--- 200 + dados do usuário -----------|
   |---- ações do perfil ----------------->|  o ID de quem pede vem SEMPRE da sessão
   |<--- respostas ------------------------|
   |---- LOGOUT (opcional) --------------->|  a sessão é limpa; a conexão continua aberta
   |---- fecha a conexão ----------------->|  o servidor encerra a goroutine
```

Regras:
- **Sessão por conexão.** O login vale só para a conexão onde foi feito. Um novo `LOGIN` na mesma conexão troca de usuário.
- **Perfis.** Cada ação exige um perfil (seção 5). Sem login a resposta é `401`; com o perfil errado, `403`.
- **O payload nunca identifica quem pede.** Campos como `motorista_id` ou `passageiro_id` enviados pelo cliente são ignorados.
- **O cliente escolhe o aplicativo no login.** O campo `tipo` do `LOGIN` precisa bater com o tipo do cadastro, senão a resposta é `403`.
- **Desconexão:**
  - EOF, conexão resetada ou queda do cliente encerram a sessão.
  - Um carrinho pendente **não** é perdido nem fica preso: ele expira pelo cronômetro (seção 6).
  - Uma mensagem pela metade (sem `\n`) é descartada.
- **Timeouts:**
  - O servidor encerra a conexão depois de **30 min** sem nenhuma mensagem.
  - O servidor tem **10 s** para escrever cada resposta. Se o cliente parar de ler, a conexão é fechada.
  - Os clientes esperam no máximo **5 s** para conectar e **15 s** por resposta. Se a conexão cair, o aplicativo avisa e encerra.

## 5. Operações

Datas e horas usam **exatamente** `AAAA-MM-DD HH:MM` (ex.: `2026-10-01 08:00`, e não `8:00`); datas sem hora usam `AAAA-MM-DD`. Nesse formato, a ordem alfabética é a ordem no tempo.

Cidades atendidas: `Feira de Santana`, `Salvador`, `Santo Amaro`, `Alagoinhas`, `Aracaju`, `Esplanada` (o servidor não diferencia maiúsculas de minúsculas e devolve o nome oficial).

| Ação | Perfil | Payload | Sucesso |
|---|---|---|---|
| `CADASTRAR` | público | Usuário completo | 201 |
| `LOGIN` | público | `id`, `senha`, `tipo` | 200 + usuário (sem senha) |
| `LOGOUT` | logado | — | 200 |
| `PUBLICAR_CARONA` | motorista | Carona | 201 + carona criada |
| `CONSULTAR_VIAGENS_MOTORISTA` | motorista | — | 200 + lista de caronas |
| `CANCELAR_CARONA` | motorista | `viagem_id` | 200 + `itinerarios_cancelados` |
| `BUSCAR_ITINERARIOS` | passageiro | Filtro | 200 + lista de itinerários |
| `ADICIONAR_CARRINHO` | passageiro | `trechos`, `continuacao` | 200 |
| `VER_CARRINHO` | passageiro | — | 200 + trechos reservados |
| `EFETIVAR_ITINERARIO` | passageiro | — | 200 |
| `CONSULTAR_VIAGENS_PASSAGEIRO` | passageiro | — | 200 + lista de itinerários |
| `CANCELAR_ITINERARIO` | passageiro | `viagem_id` | 200 |
| `CONTAR_NOTIFICACOES` | logado | — | 200 + `nao_lidas` |
| `ABRIR_NOTIFICACOES` | logado | — | 200 + lista de notificações (zera o contador) |

### 5.1 Estruturas

**Usuário**

| Campo | Tipo | Regra |
|---|---|---|
| `id` | string | 1 a 32 caracteres, sem espaços, único |
| `nome` | string | 1 a 60 caracteres |
| `senha` | string | não vazia; **nunca** aparece nas respostas |
| `tipo` | string | `motorista` ou `passageiro` |

**Carona** (enviada pelo motorista)

| Campo | Tipo | Regra |
|---|---|---|
| `origem`, `destino` | string | cidades atendidas e diferentes entre si |
| `assentos` | inteiro | capacidade do carro, 1 a 8 |
| `trechos` | lista de Trecho | 1 a 10 trechos encadeados de `origem` até `destino`, sem repetir cidade |
| `id`, `motorista_id`, `data_hora` | — | preenchidos pelo servidor (`data_hora` = saída do 1º trecho) |

**Trecho**

| Campo | Tipo | Quem define | Regra |
|---|---|---|---|
| `origem`, `destino` | string | motorista | a origem é o destino do trecho anterior |
| `valor` | número | motorista | 0 a 10000 (preço **por trecho**) |
| `vagas_totais` | inteiro | motorista | 1 até `assentos`; 0 ou ausente = `assentos` |
| `horario_saida`, `horario_chegada` | string | motorista | chegada > saída; saída ≥ chegada do trecho anterior; a 1ª saída precisa estar no futuro |
| `id`, `carona_id`, `motorista_id` | string | servidor | identificação |
| `vagas_livres` | inteiro | servidor | vagas ainda disponíveis **neste** trecho |
| `passageiros` | lista de string | servidor | IDs com embarque confirmado (só aparece para o motorista) |

**Itinerário** (a passagem comprada)

| Campo | Tipo | Descrição |
|---|---|---|
| `id` | string | identificador (`itin_...`) |
| `passageiro_id` | string | dono da passagem |
| `origem`, `destino` | string | começo e fim da viagem |
| `valor_total` | número | soma dos valores dos trechos |
| `trechos` | lista de Trecho | trechos na ordem da viagem (podem ser de motoristas diferentes) |

**Notificação**

| Campo | Tipo | Descrição |
|---|---|---|
| `id` | string | identificador (`not_...`) |
| `data` | string | quando foi gerada (`AAAA-MM-DD HH:MM`) |
| `mensagem` | string | texto do aviso |
| `lida` | bool | `false` = chegou depois da última abertura da central |

> Nas respostas de carrinho, busca e itinerário, `vagas_livres` é apenas uma foto do momento e `passageiros` vem `null`.

### 5.2 CADASTRAR
```
C→S {"acao":"CADASTRAR","payload":{"id":"joao","nome":"João","senha":"123","tipo":"motorista"}}
S→C {"status":201,"mensagem":"Usuário cadastrado com sucesso","payload":null}
```
Erros: `400` (dados inválidos), `409` (ID já existe).

### 5.3 LOGIN / LOGOUT
```
C→S {"acao":"LOGIN","payload":{"id":"joao","senha":"123","tipo":"motorista"}}
S→C {"status":200,"mensagem":"Bem-Vindo João","payload":{"id":"joao","nome":"João","tipo":"motorista"}}

C→S {"acao":"LOGOUT","payload":null}
S→C {"status":200,"mensagem":"Sessão encerrada","payload":null}
```
Erros do login: `401` ("ID ou senha incorretos", a mesma mensagem nos dois casos para não revelar quais IDs existem) e `403` (aplicativo do tipo errado).

### 5.4 PUBLICAR_CARONA
Rota Feira de Santana → Salvador → Aracaju. O carro leva 3 pessoas, mas o motorista oferece só 1 vaga no segundo trecho.
```
C→S {"acao":"PUBLICAR_CARONA","payload":{"origem":"Feira de Santana","destino":"Aracaju","assentos":3,"trechos":[
      {"origem":"Feira de Santana","destino":"Salvador","valor":30,"vagas_totais":3,"horario_saida":"2026-10-01 08:00","horario_chegada":"2026-10-01 10:00"},
      {"origem":"Salvador","destino":"Aracaju","valor":60,"vagas_totais":1,"horario_saida":"2026-10-01 10:30","horario_chegada":"2026-10-01 14:00"}]}}
S→C {"status":201,"mensagem":"Carona publicada com sucesso!","payload":{"id":"carona_1789654903265740252","motorista_id":"joao",
      "origem":"Feira de Santana","destino":"Aracaju","data_hora":"2026-10-01 08:00","assentos":3,"trechos":[
      {"id":"tre_1789654903265742906_0","carona_id":"carona_1789654903265740252","motorista_id":"joao","origem":"Feira de Santana","destino":"Salvador","valor":30,"vagas_totais":3,"vagas_livres":3,"passageiros":[],"horario_saida":"2026-10-01 08:00","horario_chegada":"2026-10-01 10:00"},
      {"id":"tre_1789654903265743755_1","carona_id":"carona_1789654903265740252","motorista_id":"joao","origem":"Salvador","destino":"Aracaju","valor":60,"vagas_totais":1,"vagas_livres":1,"passageiros":[],"horario_saida":"2026-10-01 10:30","horario_chegada":"2026-10-01 14:00"}]}}
```
Erros: `400` com o motivo (ex.: `"o trecho 2 deve sair de Salvador (onde o trecho anterior termina)"`).

### 5.5 CONSULTAR_VIAGENS_MOTORISTA
Lista as caronas ativas do motorista logado, com os passageiros confirmados **em cada trecho**.
```
C→S {"acao":"CONSULTAR_VIAGENS_MOTORISTA","payload":null}
S→C {"status":200,"mensagem":"Caronas encontradas","payload":[{"id":"carona_1789654903265740252","motorista_id":"joao",
      "origem":"Feira de Santana","destino":"Aracaju","data_hora":"2026-10-01 08:00","assentos":3,"trechos":[
      {"id":"tre_1789654903265742906_0", ... ,"vagas_totais":3,"vagas_livres":2,"passageiros":["ana"], ...},
      {"id":"tre_1789654903265743755_1", ... ,"vagas_totais":1,"vagas_livres":0,"passageiros":["ana"], ...}]}]}
```
Sem caronas: `404`.

### 5.6 CANCELAR_CARONA
Cancela a carona e, **em cascata**, a viagem inteira de cada passageiro afetado, devolvendo também as vagas que esses passageiros ocupavam nos carros de outros motoristas.
```
C→S {"acao":"CANCELAR_CARONA","payload":{"viagem_id":"carona_1789654903265740252"}}
S→C {"status":200,"mensagem":"✅ Carona cancelada! 0 viagem(ns) de passageiros foram canceladas em cascata.","payload":{"itinerarios_cancelados":0}}
```
Erros: `400` (sem `viagem_id`), `403` (carona de outro motorista), `404`, `409` (já cancelada).

### 5.7 BUSCAR_ITINERARIOS
| Campo | Tipo | Descrição |
|---|---|---|
| `origem`, `destino` | string | cidades |
| `data` | string `AAAA-MM-DD` | o primeiro trecho sai **neste dia** |
| `horario_minimo` | string `AAAA-MM-DD HH:MM` | o primeiro trecho sai **a partir deste horário** (usado para continuar uma viagem) |

É obrigatório informar `data`, `horario_minimo` ou os dois. A resposta é uma lista de itinerários, e cada itinerário é uma lista de trechos:
- só entram trechos com vaga, que ainda não partiram;
- cada trecho sai da cidade onde o anterior chega, depois que ele chega;
- nenhuma cidade se repete;
- no máximo 6 trechos e 30 opções.

**Ordem:** menos trocas de carro → chegada mais cedo → menor preço.
```
C→S {"acao":"BUSCAR_ITINERARIOS","payload":{"origem":"Feira de Santana","destino":"Aracaju","data":"2026-10-01"}}
S→C {"status":200,"mensagem":"1 itinerário(s) encontrado(s)","payload":[[
      {"id":"tre_1789654903265742906_0","carona_id":"carona_1789654903265740252","motorista_id":"joao","origem":"Feira de Santana","destino":"Salvador","valor":30,"vagas_totais":3,"vagas_livres":3,"passageiros":null,"horario_saida":"2026-10-01 08:00","horario_chegada":"2026-10-01 10:00"},
      {"id":"tre_1789654903265743755_1","carona_id":"carona_1789654903265740252","motorista_id":"joao","origem":"Salvador","destino":"Aracaju","valor":60,"vagas_totais":1,"vagas_livres":1,"passageiros":null,"horario_saida":"2026-10-01 10:30","horario_chegada":"2026-10-01 14:00"}]]}
```
Sem opções: `404`. Filtro inválido: `400`.

### 5.8 ADICIONAR_CARRINHO
Reserva **na hora** uma vaga em cada trecho informado. Só `id` e `carona_id` de cada trecho são usados; preço e horários vêm dos dados oficiais do servidor.

| Campo | Tipo | Descrição |
|---|---|---|
| `trechos` | lista | 1 a 6 trechos, encadeados entre si |
| `continuacao` | bool | `false`: primeiro pedaço de uma busca (abre um carrinho **novo** e devolve as vagas de um carrinho anterior). `true`: próximo pedaço da **mesma** viagem (precisa continuar de onde o carrinho parou) |

Cada reserva renova o prazo do carrinho (padrão 60 s, configurável no servidor com `VAIJUNTO_TEMPO_RESERVA`).
```
C→S {"acao":"ADICIONAR_CARRINHO","payload":{"trechos":[{"id":"tre_1789654903265742906_0","carona_id":"carona_1789654903265740252"}],"continuacao":false}}
S→C {"status":200,"mensagem":"Vaga reservada! Você tem 60 segundos para escolher o próximo trecho ou confirmar a compra.","payload":null}

C→S {"acao":"ADICIONAR_CARRINHO","payload":{"trechos":[{"id":"tre_1789654903265743755_1","carona_id":"carona_1789654903265740252"}],"continuacao":true}}
S→C {"status":200,"mensagem":"Vaga reservada! Você tem 60 segundos para escolher o próximo trecho ou confirmar a compra.","payload":null}
```
Erros:
- `400`: lista vazia, trecho repetido, trechos não encadeados, ou trecho que não continua o carrinho.
- `404`: o trecho não existe nessa carona.
- `409`: vaga esgotada, carona inexistente ou cancelada, trecho já partido, ou carrinho expirado durante a montagem.

Se a requisição falhar, **nenhuma** vaga dela fica reservada.

### 5.9 VER_CARRINHO
```
C→S {"acao":"VER_CARRINHO","payload":null}
S→C {"status":200,"mensagem":"Carrinho recuperado","payload":[{"id":"tre_1789654903265742906_0", ... ,"valor":30, ...},{"id":"tre_1789654903265743755_1", ... ,"valor":60, ...}]}
```
Carrinho vazio ou expirado: `404`.

### 5.10 EFETIVAR_ITINERARIO
Confirma **atomicamente** todo o carrinho: ou todos os trechos viram passagem, ou nenhum vira.
```
C→S {"acao":"EFETIVAR_ITINERARIO","payload":null}
S→C {"status":200,"mensagem":"✅ Compra confirmada com sucesso! Passagens emitidas.","payload":null}
```
Erros: `404` (carrinho vazio ou expirado) e `409` (um dos motoristas cancelou a carona antes da confirmação; as vagas dos outros trechos são devolvidas).

### 5.11 CONSULTAR_VIAGENS_PASSAGEIRO
```
C→S {"acao":"CONSULTAR_VIAGENS_PASSAGEIRO","payload":null}
S→C {"status":200,"mensagem":"Viagens encontradas","payload":[{"id":"itin_1789654903267273740","passageiro_id":"ana",
      "origem":"Feira de Santana","destino":"Aracaju","valor_total":90,"trechos":[ {...}, {...} ]}]}
```
Sem viagens: `404`.

### 5.12 CANCELAR_ITINERARIO
Cancela a viagem inteira (todos os trechos) e devolve as vagas.
```
C→S {"acao":"CANCELAR_ITINERARIO","payload":{"viagem_id":"itin_1789654903267273740"}}
S→C {"status":200,"mensagem":"✅ Viagem cancelada! As vagas foram devolvidas aos motoristas.","payload":null}
```
Erros: `400` (sem `viagem_id`), `403` (viagem de outro passageiro), `404`, `409` (já cancelada).

### 5.13 Central de notificações

O servidor guarda avisos para cada usuário, sem enviar nada por conta própria. O cliente busca os avisos quando quer: o protocolo continua sendo só requisição e resposta.

| Evento | Quem recebe |
|---|---|
| Passageiro confirma uma compra | cada motorista das caronas usadas |
| Passageiro cancela a viagem | cada motorista das caronas usadas |
| Motorista cancela a carona | cada passageiro afetado (a viagem inteira foi cancelada) e os **outros** motoristas dessas viagens (perderam o passageiro) |
| Carrinho expira sem confirmação | o passageiro |

Cada usuário guarda as **50** mais recentes, gravadas em `notificacoes.json`.

**CONTAR_NOTIFICACOES:** quantas chegaram desde a última vez que a central foi aberta (o `[x]` do menu).
```
C→S {"acao":"CONTAR_NOTIFICACOES","payload":null}
S→C {"status":200,"mensagem":"Notificações não lidas","payload":{"nao_lidas":1}}
```

**ABRIR_NOTIFICACOES:** devolve as notificações, **mais novas primeiro**, como estavam antes da abertura (para o cliente destacar as que têm `"lida":false`). Em seguida o servidor marca todas como lidas, o que zera o contador. Sem notificações, a lista vem vazia (`[]`).
```
C→S {"acao":"ABRIR_NOTIFICACOES","payload":null}
S→C {"status":200,"mensagem":"1 notificação(ões)","payload":[{"id":"not_1789660380123456789","data":"2026-09-17 12:13",
      "mensagem":"Sua viagem Feira de Santana → Aracaju (saída 2026-09-29 08:00) foi cancelada porque o motorista Joao cancelou a carona.",
      "lida":false}]}
```

## 6. Regras de sincronização

1. **Uma requisição por vez por conexão.** O servidor processa as mensagens de uma conexão em ordem; conexões diferentes são atendidas em paralelo.
2. **Busca não reserva.** Entre a busca e a reserva, outro passageiro pode ficar com a vaga. Nesse caso o `ADICIONAR_CARRINHO` responde `409`, e o cliente deve buscar de novo ou desistir.
3. **Quem reserva primeiro fica com a vaga.** A vaga é descontada no `ADICIONAR_CARRINHO`, sob o mutex da carona.
4. **Montagem passo a passo.**
   - O cliente reserva um pedaço da viagem por vez: `continuacao=false` no primeiro e `true` nos seguintes.
   - Para o próximo pedaço, busca de novo com `origem` = cidade onde parou e `horario_minimo` = horário de chegada.
5. **Prazo do carrinho.**
   - Cada reserva renova o prazo.
   - Quando ele acaba, o servidor devolve **todas** as vagas do carrinho e o `EFETIVAR_ITINERARIO` passa a responder `404`.
   - A confirmação e a expiração nunca valem as duas: só uma delas fecha o carrinho.
6. **Durabilidade.** A resposta de sucesso de `CADASTRAR`, `PUBLICAR_CARONA`, `EFETIVAR_ITINERARIO` e dos cancelamentos só é enviada depois que o servidor grava a mudança em disco. Uma falha de gravação (ex.: disco cheio) é registrada no log do servidor, e a mudança continua valendo na memória.

## 7. Exemplo de sessão completa (passageiro)

```
(conecta em servidor:8081)
C→S {"acao":"LOGIN","payload":{"id":"ana","senha":"abc","tipo":"passageiro"}}
S→C {"status":200,"mensagem":"Bem-Vindo Ana","payload":{"id":"ana","nome":"Ana","tipo":"passageiro"}}
C→S {"acao":"BUSCAR_ITINERARIOS","payload":{"origem":"Feira de Santana","destino":"Aracaju","data":"2026-10-01"}}
S→C {"status":200,"mensagem":"1 itinerário(s) encontrado(s)","payload":[[{...Feira->Salvador...},{...Salvador->Aracaju...}]]}
C→S {"acao":"ADICIONAR_CARRINHO","payload":{"trechos":[{"id":"tre_..._0","carona_id":"carona_..."}],"continuacao":false}}
S→C {"status":200,"mensagem":"Vaga reservada! ...","payload":null}
C→S {"acao":"BUSCAR_ITINERARIOS","payload":{"origem":"Salvador","destino":"Aracaju","horario_minimo":"2026-10-01 10:00"}}
S→C {"status":200,"mensagem":"1 itinerário(s) encontrado(s)","payload":[[{...Salvador->Aracaju...}]]}
C→S {"acao":"ADICIONAR_CARRINHO","payload":{"trechos":[{"id":"tre_..._1","carona_id":"carona_..."}],"continuacao":true}}
S→C {"status":200,"mensagem":"Vaga reservada! ...","payload":null}
C→S {"acao":"EFETIVAR_ITINERARIO","payload":null}
S→C {"status":200,"mensagem":"✅ Compra confirmada com sucesso! Passagens emitidas.","payload":null}
C→S {"acao":"LOGOUT","payload":null}
S→C {"status":200,"mensagem":"Sessão encerrada","payload":null}
(fecha a conexão)
```

## 8. Exemplos de erro

```
C→S {"acao":"CONSULTAR_VIAGENS_PASSAGEIRO","payload":null}          (sem login)
S→C {"status":401,"mensagem":"faça login para usar esta ação","payload":null}

C→S {isso nao e json
S→C {"status":400,"mensagem":"mensagem malformada: JSON inválido","payload":null}

C→S {"acao":"VOAR"}
S→C {"status":400,"mensagem":"Ação desconhecida: VOAR","payload":null}

C→S {"acao":"LOGIN","payload":"aaaa... (mais de 64 KiB)"}
S→C {"status":413,"mensagem":"mensagem excede o tamanho máximo permitido","payload":null}
(o servidor fecha a conexão)
```
