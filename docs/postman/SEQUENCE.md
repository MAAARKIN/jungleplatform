# SEQUÊNCIA — Simulação manual completa do fluxo (via Postman)

Este guia assume que o ambiente inteiro já está de pé (`docker compose up --build`) e que você importou a collection `docs/postman/jungle-platform.postman_collection.json` no Postman. A ideia é percorrer **o ciclo de vida completo de uma operação financeira** — da obtenção da identidade até a auditoria do evento publicado — na ordem exata em que o sistema foi desenhado para ser usado, explicando **o que cada peça faz por dentro** e **quais campos de cada resposta você vai reaproveitar na chamada seguinte**.

## Variáveis da collection que este guia usa

| Variável | Preenchida por | Usada em |
| --- | --- | --- |
| `walletId` | script da resposta de "Open wallet" | todas as operações e leituras |
| `transactionId` | script da resposta de qualquer operação | `GET /wagering/transactions/{{transactionId}}` |
| `externalTransactionId` | script da resposta de BET | REFUND/ROLLBACK (campo `referenceExternalTransactionId`) |
| `playerId` | você (valor fixo na collection) | abertura de carteira e operações |
| `providerId` | valor fixo (`provider-a`) | operações de provedor |

Os scripts de teste do Postman preenchem automaticamente `walletId`, `transactionId` e `externalTransactionId` — você só encadeia as chamadas na ordem deste guia.

---

## Fase 0 — O que está rodando (contexto)

Antes da primeira chamada, entenda quem é quem:

| Serviço | Papel no fluxo |
| --- | --- |
| **Keycloak** (`localhost:8180`) | IdP. Quem emite a identidade: todo token carrega um claim `providerId` que define **quem você é** no sistema. Ninguém informa `providerId` "por fora" — ele vem da identidade. |
| **API** (`localhost:8080`, `cmd/api`) | Recebe operações síncronas por HTTP. Valida identidade, aplica regras de negócio, move a carteira, grava o ledger e enfileira eventos — **tudo num único commit SQL**. |
| **Worker** (`cmd/worker`) | Três trabalhos assíncronos: consome a fila SQS das apostas (mesmo caso de uso da API), publica os eventos da outbox na fila `wager-events.fifo` e resolve referências pendentes (reversões que chegaram antes da aposta que referenciam). |
| **PostgreSQL** | Onde a verdade financeira vive: carteiras, transações, ledger append-only, inbox, outbox. Todas as invariantes (saldo ≥ 0, unicidade, imutabilidade do ledger) são impostas por constraints, não por código de aplicação. |
| **LocalStack** | Simula o AWS SQS: `wager-transactions.fifo` (entrada de operações), `wager-transactions-dlq.fifo` (mensagens rejeitadas/esgotadas) e `wager-events.fifo` (eventos de integração publicados). |

---

## Fase 1 — Identidade: obtenha o token do serviço interno

**Endpoint:** nenhum nosso — é o token endpoint do Keycloak, e no Postman você não o chama manualmente.

**O que fazer:** abra a pasta **"Internal Service — Wallets"** da collection, vá na aba **Authorization**, e clique em **Get New Access Token**. A collection já está configurada com o fluxo `client_credentials`, client `internal`, secret `internal-secret`.

**O que isso faz por dentro:** o Postman faz um POST ao Keycloak pedindo um token de máquina-para-máquina. O Keycloak responde com um JWT assinado (RS256) que contém, entre outros, `providerId: "internal"` e uma data de expiração. **Nossa API não sabe sua senha — ela apenas verifica a assinatura do token contra a chave pública do Keycloak (JWKS) e lê o claim `providerId`.** Esse claim é a ÚNICA fonte de autoridade sobre quem você é.

**O que usar a seguir:** o Postman passa a enviar automaticamente `Authorization: Bearer <token>` em todas as requests da pasta. O token vale alguns minutos — se qualquer chamada seguinte devolver `401`, peça um token novo (botão de refresh do token na mesma aba).

> **Conceito importante:** existem três identidades no realm: `internal` (o serviço da casa — único que pode mexer em carteiras), `provider-a` e `provider-b` (provedores de jogos — só podem operar as próprias apostas). Todo o resto do guia alterna entre elas.

---

## Fase 2 — Abrir a carteira (identidade `internal`)

**Endpoint:** `POST {{baseUrl}}/wallets` — request **"Open wallet"**.

**O que esse endpoint faz por dentro:** é a operação de **abertura** — a única escrita permitida para quem não é provedor. Com saldo inicial positivo, num **único commit SQL** ele: (1) cria a linha na tabela `wallets` com `version = 1`; (2) cria uma transação interna do tipo `OPENING` já em `PROCESSED`; (3) grava o **primeiro lançamento do ledger** (um crédito); (4) enfileira dois eventos na outbox (`WagerTransactionProcessed` e `WalletBalanceChanged`). Com saldo inicial `0.00`, ele pula os itens 2–4 (não existe movimentação financeira a auditar) — isso é uma regra do desafio, não um atalho.

**Corpo da request (já preenchido):**
```json
{ "playerId": "{{playerId}}", "initialBalance": { "amount": "1000.00", "currency": "BRL" } }
```

**O que esperar:** `201 Created` com:
```json
{ "id": "6d5b...", "playerId": "0199f2...", "balance": {"amount":"1000.00","currency":"BRL"}, "version": 1 }
```

**O que usar a seguir:** o script de teste da collection copia o campo **`id`** da resposta para a variável `{{walletId}}`. **Toda** chamada seguinte usa essa variável. Guarde também mentalmente o `version: 1` — ele incrementa a cada movimentação de saldo e serve para você detectar "lost updates" na mão.

**Experimentos válidos aqui:**
- Chamar de novo com o mesmo `playerId` → `409 Conflict` (o par `playerId + moeda` identifica uma única carteira — está no banco como constraint).
- Chamar com `amount: "1e5"` → `400` (notação científica é rejeitada no parsing do dinheiro).
- Chamar com saldo `0.00` para outro `playerId` → `201` sem ledger (veja na Fase 3 que não há lançamentos).

---

## Fase 3 — Verificar o estado inicial (leitura)

**Endpoint:** `GET {{baseUrl}}/wallets/{{walletId}}` — request **"Read wallet"**.

**O que faz:** leitura pura do agregado. Você deve ver `balance: 1000.00` e `version: 1`.

**Agora abra o ledger:** `GET {{baseUrl}}/wallets/{{walletId}}/ledger?cursor=&limit=50` — request **"Ledger (paginated)"**.

**O que faz:** percorre o **ledger append-only** — a trilha de auditoria imutável da carteira. Você deve ver **um único lançamento**: `CREDIT` de `1000.00` com `balanceBefore: 0.00` e `balanceAfter: 1000.00`, referenciando a transação `OPENING`. Cada lançamento carrega o saldo antes e depois — é isso que permite à reconciliação reconstruir o saldo somando o ledger e comparar com o armazenado.

**Conceito:** o ledger nunca é editado nem apagado (triggers no banco bloqueiam UPDATE/DELETE). Correções financeiras são **novos lançamentos**, nunca alterações.

---

## Fase 4 — A primeira aposta (BET, identidade `provider-a`)

**Mude de identidade:** abra a pasta **"Provider — Wagering Operations"** e clique em **Get New Access Token** (client `provider-a`, secret `provider-a-secret`). Agora você é um provedor de jogos.

**Endpoint:** `POST {{baseUrl}}/wagering/transactions` — request **"BET"**.

**O que esse endpoint faz por dentro (o coração do sistema):**
1. Lê o header **`Idempotency-Key`** (obrigatório — sem ele, `400`). A collection usa `{{$guid}}`, que gera uma chave nova a cada envio.
2. Confere se já existe transação com essa chave: se existir com **hash de payload idêntico**, devolve o resultado original (replay); se existir com hash diferente, `409`.
3. Confere se o par `(providerId, externalTransactionId)` já foi usado com outra chave → `409`.
4. Faz um `SELECT ... FOR UPDATE` na linha da carteira — **aqui é onde duas apostas concorrentes na mesma carteira se enfileiram**.
5. Aplica o débito no agregado (regra: saldo nunca fica negativo), grava a transação como `PROCESSED`, grava o lançamento de débito no ledger e enfileira `WagerTransactionProcessed` + `WalletBalanceChanged` — tudo num commit.

**Corpo (preenchido):** note o `providerId: "provider-a"` — ele precisa coincidir com o do token (se você enviar `provider-b` no body com token do provider-a, recebe `403`).
```json
{ "providerId": "provider-a", "externalTransactionId": "<gerado>", "playerId": "{{playerId}}", "walletId": "{{walletId}}", "roundId": "{{roundId}}", "gameId": "{{gameId}}", "kind": "BET", "money": { "amount": "25.00", "currency": "BRL" } }
```

**O que esperar:** `200` com:
```json
{ "transactionId": "086a...", "status": "PROCESSED", "balance": {"amount":"975.00","currency":"BRL"}, "idempotentReplay": false, "failureCode": "" }
```

**O que usar a seguir:** o script salva **`transactionId`** em `{{transactionId}}` e o **`externalTransactionId`** do body em `{{externalTransactionId}}` — a Fase 8 (reversões) usa `{{externalTransactionId}}` como `referenceExternalTransactionId`.

---

## Fase 5 — Idempotência na prática (o teste que mais vale a pena fazer à mão)

**5a. Replay:** reenvie o MESMO request do BET, mas **troque o header `Idempotency-Key` pelo mesmo valor do envio anterior** (copie-o do histórico do Postman — na collection de teste, o request "Replay test (fixed Idempotency-Key)" já usa chave fixa para isso).

**O que esperar:** `200` com **`idempotentReplay: true`** e **exatamente o mesmo saldo de antes** (`975.00`), mesmo que a carteira tenha recebido outras movimentações desde então. Nenhum débito novo acontece — a resposta é reconstruída do estado persistido no commit original. O campo `transactionId` será **o mesmo** da primeira chamada.

**5b. Conflito:** reenvie com a MESMA chave, mas mudando o `amount` para `30.00`.

**O que esperar:** `409 Conflict`. Por dentro: o hash canônico do payload (SHA-256 sobre JSON com chaves ordenadas, excluindo a própria chave de idempotência) mudou — mesma chave com conteúdo diferente é uma anomalia que o sistema recusa em vez de adivinhar a intenção.

**5c. Comprovar no ledger:** consulte o ledger de novo. Deve continuar com **um único débito de 25.00**, não importa quantos replays você tenha feito.

---

## Fase 6 — Os outros tipos de operação

Envie da mesma forma (a collection já tem os requests prontos):

**WIN** (`kind: "WIN"`, `amount: "50.00"`): crédito na carteira. Regra: exige valor positivo. Observe no ledger: lançamento `CREDIT` e `version` da carteira sobe. Evento `WalletBalanceChanged` publicado.

**LOSS** (`kind: "LOSS"`, `amount: "0.00"` — o zero é **obrigatório**): registra que o jogador perdeu, **sem mover dinheiro**. Regras: `amount` diferente de `"0.00"` → `400`; moeda diferente da carteira → rejeição `CURRENCY_MISMATCH`. No ledger: **nada**; na outbox: apenas `WagerTransactionProcessed` (sem `WalletBalanceChanged`). Repare que o `balance` da resposta é o saldo corrente, sem alteração.

**Experimento de rejeição:** tente um BET de `100.01` (mais que o saldo). Espere `422` com `status: REJECTED` e `failureCode: "INSUFFICIENT_FUNDS"`. **A rejeição também é persistida e auditável** — e é idempotente: reenvie a mesma operação e receberá o mesmo `422` com `idempotentReplay: true`. Rejeição não é erro de sistema; é um resultado de negócio com registro permanente.

---

## Fase 7 — Consultas de transação

**Por id interno:** `GET {{baseUrl}}/wagering/transactions/{{transactionId}}` — você vê `status`, `kind`, `money`, `failureCode` e `createdAt`. Pendências e códigos de rejeição são consultáveis daqui — um provedor consegue acompanhar o ciclo de vida sem precisar de outro canal.

**Por external id:** `GET {{baseUrl}}/providers/{{providerId}}/wagering/transactions/{{externalTransactionId}}` — a consulta que um provedor real usaria (ele conhece apenas os próprios ids externos). O `providerId` do path precisa ser o da sua identidade; `internal` pode consultar qualquer um.

---

## Fase 8 — Reversões: REFUND e ROLLBACK

Aqui entra o campo que as operações anteriores prepararam: **`referenceExternalTransactionId`** — e nele você vai usar **`{{externalTransactionId}}`**, que o script do BET salvou.

**REFUND** (request **"REFUND (references a BET)"**): devolve **integralmente** um BET processado. Regras por dentro: a referência é resolvida por `(providerId, referenceExternalTransactionId)`; a reversão precisa coincidir com a aposta em provedor, jogador, carteira, moeda, rodada e **valor exato** (reversão parcial não existe no desafio); a direção é um crédito.

**O que esperar:** `200 PROCESSED` com o saldo de volta ao valor pré-aposta, lançamento `CREDIT` no ledger referenciando a transação de refund.

**REFUND duplicado:** reenvie um segundo REFUND contra a MESMA aposta (novo `externalTransactionId`, mesma referência). Espere `422` com `failureCode: "ALREADY_REVERSED"` — o sistema proíbe devolver o mesmo débito duas vezes. **Nota:** um `ROLLBACK` do próprio REFUND (referenciando o refund, não a aposta) é permitido — é a reversão da reversão, com direção débito.

**ROLLBACK** (request **"ROLLBACK (opposite movement of the original)"**): desfaz o movimento **contrário ao original** de um BET (crédito), WIN (débito) ou REFUND (débito). Se o débito da reversão estourasse o saldo disponível → `422` com `failureCode: "REVERSAL_EXCEEDS_BALANCE"` — código **distinto** de `INSUFFICIENT_FUNDS` de propósito, para você distinguir "não tinha saldo pra apostar" de "a reversão não cabe mais na carteira".

---

## Fase 9 — Referência pendente: a reversão que chegou antes da aposta

Cenário real de produção: o provedor manda o REFUND **antes** do BET que ele referencia (mensagens podem chegar fora de ordem).

**Passo 1:** envie o request **"REFUND (references a BET)"** trocando `referenceExternalTransactionId` para um id que ainda não existe (ex.: `bet-que-nao-existe`).

**O que esperar:** `202 Accepted` com `status: PENDING_REFERENCE`. Por dentro: a operação **não é rejeitada nem aplicada** — ela fica duravelmente armazenada à espera da referência, com um evento `WagerTransactionPendingReference` publicado.

**Passo 2:** agora envie o **BET** com `externalTransactionId: bet-que-nao-existe` (a collection: request "BET", trocando o campo).

**Passo 3:** aguarde ~2 segundos (o worker de referências roda a cada 1s) e consulte `GET /wagering/transactions/{{transactionId}}` do refund (a variável `{{transactionId}}` já foi atualizada pelo script do Passo 1).

**O que esperar:** o refund agora está `PROCESSED` — o worker pegou a pendência, encontrou a aposta que chegou, validou a coerência (mesmo provedor/jogador/carteira/moeda/rodada e valor exato) e aplicou o crédito, com ledger e eventos.

**O que acontece se a aposta nunca chegar:** o worker tenta com backoff exponencial durável (sobrevive a restart — os contadores de tentativa estão no banco). Esgotado o TTL (`PENDING_REF_TTL`, default 24h) ou 50 tentativas, a operação vira `REJECTED` com `REFERENCE_NOT_FOUND` + evento de rejeição. (Para simular a expiração manualmente você precisaria de um ambiente com `PENDING_REF_TTL=1s` — os testes de integração fazem exatamente isso.)

**Outra nuance para experimentar:** envie um REFUND referenciando uma aposta **rejeitada** (ex.: a de `100.01` da Fase 6). Espera: `422 REFERENCE_UNSUCCESSFUL` imediato — não faz sentido esperar por uma referência que terminou em rejeição.

---

## Fase 10 — Isolamento entre provedores

**Passo 1:** na pasta do provedor, troque o client do token para `provider-b` / `provider-b-secret` e peça um novo token.

**Passo 2:** com o token do provider-b, tente:
- `GET /wagering/transactions/{{transactionId}}` (transação do provider-a) → **`404`**. O 404 é deliberado: a existência da transação de outro provedor não é revelada.
- `GET /providers/provider-a/wagering/transactions/...` → **`404`**.
- POST de uma operação com `providerId: "provider-a"` no body → **`403`** (impersonação no body é bloqueada — o `providerId` autoritativo é o do token).

**Passo 3:** volte o token para `provider-a` e repita as mesmas consultas → `200`. Isso é o isolamento exigido pelo desafio, inclusive em replays.

---

## Fase 11 — O caminho assíncrono: operação via SQS

Até aqui tudo foi HTTP. O **mesmo caso de uso** também é acionado por fila — com as mesmas garantias de idempotência.

**Passo 1 — publicar uma mensagem na fila de entrada.** O Postman não fala SQS; use o CLI dentro do LocalStack (troque `SEU_WALLET_ID` pelo `{{walletId}}` da collection):

```sh
docker compose exec -T localstack sh -c 'awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-body '\''{"messageId":"manual-sqs-1","type":"WagerTransactionRequested","occurredAt":"2026-10-01T12:00:00.000Z","data":{"providerId":"provider-a","externalTransactionId":"ext-manual-sqs-1","idempotencyKey":"provider-a:ext-manual-sqs-1","playerId":"0199f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"SEU_WALLET_ID","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"10.00","currency":"BRL"}}}'\'' \
  --message-group-id g1 --message-deduplication-id d-manual-1'
```

**O que acontece por dentro:** o worker recebe a mensagem; o `messageId` do envelope (`manual-sqs-1`) é registrado na **inbox** dentro da MESMA transação SQL que processa a aposta; a chave de idempotência vem de `data.idempotencyKey` — a mesma tabela que o HTTP usa. A mensagem é **removida da fila só depois do commit**.

**Passo 2 — comprove:** logs do worker (`docker compose logs worker | grep manual-sqs-1`) mostram `message processed ... status PROCESSED`; a fila fica vazia (`ApproximateNumberOfMessages: 0`); o saldo da carteira caiu 10.00.

**Passo 3 — reenvie a mensagem idêntica** (mesmo `messageId`): o worker registra a reentrega, a inbox diz "já visto com o mesmo hash", **nenhum débito novo acontece** e a mensagem é removida. Você acabou de provar a dedup da aplicação — o desafio exige que ela seja da aplicação, não apenas do broker.

**Passo 4 — o cruzamento HTTP × SQS:** com a chave `provider-a:ext-manual-sqs-1`, faça o **mesmo BET por HTTP** (Postman, request "Replay test (fixed Idempotency-Key)" com essa chave fixa). Espere `200 idempotentReplay: true` — o HTTP encontrou a operação processada **pela via SQS**. As duas portas compartilham a mesma tabela de idempotência.

---

## Fase 12 — Eventos de integração (a outbox em ação)

Toda movimentação que você fez até aqui gerou eventos que ficaram na tabela `outbox` no exato commit da operação. O worker os publica na fila `wager-events.fifo`:

```sh
# quantos eventos pendentes (deve ser 0 — o publisher drena continuamente):
docker compose exec -T postgres psql -U jungle -d jungle -c \
  "SELECT event_type, count(*) FROM outbox GROUP BY event_type;"
# espiar um evento publicado:
docker compose exec -T localstack sh -c 'awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/wager-events.fifo \
  --query Messages[0].Body' | python -m json.tool
```

**O que procurar no envelope:** `eventId` (identidade estável — republicação preserva o mesmo id), `eventType`, `occurredAt` em UTC RFC 3339, e o `data` tipado com money como string decimal. Consumer externos integrariam contra isso.

**Simulação de falha (opcional, reveladora):** mate o worker (`docker compose stop worker`), faça uma operação nova via HTTP (o evento fica pendente na outbox, `published_at IS NULL`), espere ~1 minuto, suba o worker de novo (`docker compose start worker`) — outro publisher reivindica o evento pela lease expirada e o publica **com o mesmo `eventId`**.

---

## Fase 13 — Reconciliação: a prova final

**Endpoint:** `POST {{baseUrl}}/wallets/{{walletId}}/reconciliation`.

**O que faz:** soma **créditos − débitos do ledger inteiro** (incluindo a abertura) e compara com o saldo armazenado. Resposta:
```json
{ "storedBalance": ..., "calculatedBalance": ..., "difference": {"amount":"0.00","currency":"BRL"}, "consistent": true, "checkedEntries": N }
```

**O que esperar depois de todo o guia:** `consistent: true` com `difference: "0.00"` — o saldo que a API devolveu em cada operação é exatamente a soma auditável do ledger. A reconciliação **nunca altera** o saldo; divergência é reportada, não corrigida.

**Experimento (opcional, para ver a divergência):** corrompa manualmente o saldo no banco (`UPDATE wallets SET balance_units = 999 WHERE id = '<walletId>'`), reconcilie de novo → `consistent: false` com a diferença reportada nos dois sentidos e na métrica `reconciliation_divergences_total` — e o saldo permanece 999 (não foi "corrigido").

---

## Fase 14 — Observabilidade do que você acabou de fazer

- **`GET {{baseUrl}}/metrics`**: veja `wager_transactions_total{status=...}` (suas operações por status), `duplicates_total` (as reentregas da Fase 11), `idempotency_conflicts_total` (o 409 da Fase 5b), `outbox_pending` (deve ser 0).
- **Logs JSON**: cada request loga `correlationId` (header `X-Correlation-Id` ou gerado), `providerId`, path e status — sem payloads financeiros. No worker, os logs carregam `messageId` e `transactionId` para seguir a trilha de uma mensagem na fila.
- **Health**: `/health/ready` volta `503` se você parar o Postgres ou o LocalStack — teste se quiser (`docker compose stop postgres` e depois `start`).

---

## Resumo: o fluxo inteiro em uma linha por passo

| # | Ação | Identidade | Saída que alimenta o próximo passo |
| --- | --- | --- | --- |
| 1 | Token `internal` (Postman OAuth2) | internal | header Authorization automático |
| 2 | `POST /wallets` | internal | `id` → `{{walletId}}` |
| 3 | `GET /wallets` + `ledger` | internal | confiança: saldo + primeiro lançamento |
| 4 | Token `provider-a` | provider-a | nova identidade |
| 5 | `POST /wagering/transactions` (BET) | provider-a | `transactionId`, `externalTransactionId` |
| 6 | Replay + conflito da mesma chave | provider-a | prova de idempotência (ledger inalterado) |
| 7 | WIN / LOSS / BET sem saldo | provider-a | regras dos cinco tipos |
| 8 | Consultas por id / external id | provider-a | ciclo de vida visível |
| 9 | REFUND + ROLLBACK (com `{{externalTransactionId}}`) | provider-a | reversões com dedup (`ALREADY_REVERSED`) |
| 10 | REFUND órfão → 202 → BET chega → worker resolve | provider-a + worker | pendência durável |
| 11 | Token `provider-b` → consultas → 404/403 | provider-b | isolamento |
| 12 | Mensagem SQS + reenvio + cruzamento com HTTP | worker | dedup da inbox |
| 13 | Inspecionar `wager-events.fifo` | worker | envelope de eventos com `eventId` |
| 14 | Reconciliação → `consistent: true` | internal | saldo = ledger, provado |
| 15 | Métricas + logs + health | — | observabilidade completa |