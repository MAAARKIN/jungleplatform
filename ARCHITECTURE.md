# Decisões de Arquitetura — Jungle Platform

Decisões tomadas durante a implementação do desafio, cada uma com a alternativa considerada e o custo se estiver errada.

## 1. Representação de dinheiro

`Money` é um value object imutável: `int64` em unidades mínimas (escala fixa 2) + `Currency` ISO 4217.

- **Por que não uma biblioteca decimal (`cockroachdb/apd`)**: o contrato fixa a escala em 2. Uma biblioteca decimal ainda exigiria a mesma validação de entrada (rejeitar notação científica, escala > 2) e introduz risco de arredondamento silencioso via seus contextos aritméticos. `int64` cobre ±9,2×10¹⁴ unidades principais (~±92 trilhões de BRL), documentado no tipo.
- Overflow é verificado dígito a dígito no parsing e em toda operação aritmética (`Add`/`Sub`/`Neg`).
- A serialização é sempre string decimal de escala 2 (`"25.00"`); o unmarshal de JSON rejeita amounts numéricos, garantindo que nenhum valor passe por float.
- **Custo se errado**: se uma moeda exigir escala ≠ 2 ou valores além do int64, o tipo precisa ser refeito. Como o tipo carrega a moeda e impõe compatibilidade, a mudança fica contida.

## 2. Fronteira de transação

Toda mutação financeira acontece dentro de `postgres.WithinTx`: o `pgx.Tx` fica guardado no contexto (`domain.TxKey`) e todos os repositórios entram nele via `QuerierFor`. Uma única transação SQL cobre: atualização da carteira, registro da wager transaction, lançamento no ledger, registro/conclusão da inbox e eventos da outbox.

- Repositórios recebem apenas `context.Context` — tipos pgx não vazam para o domínio.
- `usecase.ProcessWager.Execute` embrulha tudo; o tratamento da inbox no consumer envolve `Execute` na *mesma* transação (inbox + domínio + outbox commitam atomicamente).
- `WithinTx` aninhado abre uma nova transação do pool (usado pelo consumer em volta do `Execute`); a transação interna é onde o dinheiro se move, a externa cobre a inbox — veja §7 o argumento de ordenação.
- **Custo se errado**: uso incorreto do aninhamento poderia commitar a inbox sem o movimento. Mitigado pela ordenação: `Complete` da inbox acontece depois de `Execute` retornar, na transação externa.

## 3. Controle de concorrência

**Lock pessimista por linha de carteira** (`SELECT ... FOR UPDATE` via `Wallets.GetForUpdate`).

- Escritores da mesma carteira se serializam no banco; carteiras distintas progridem em paralelo (provado por `TestDistinctWalletsProcessInParallel`: 8 carteiras × 5 operações em 3 instâncias, sem lock global).
- Alternativas consideradas: controle otimista com retry (move a complexidade para a política de retry e conflitos Lidam com dinheiro) e atualização atômica condicionada (`UPDATE ... WHERE balance >= x` — espalharia regras de negócio em strings SQL). O `FOR UPDATE` mantém todas as regras no agregado e é trivial de justificar em testes (`TestGetForUpdateSerializesWriters`).
- Não-negatividade, unicidade e imutabilidade do ledger são impostas **pelo banco** (CHECK/UNIQUE/triggers), não pelo lock — o lock é só um dispositivo de eficiência.

## 4. Idempotência

- Header `Idempotency-Key` (HTTP) / `data.idempotencyKey` (SQS) é obrigatório.
- O hash do payload é SHA-256 sobre **JSON canônico** (chaves ordenadas alfabeticamente via re-encoding em mapas) apenas dos campos de negócio — a chave em si e metadados de transporte são excluídos por construção. Money é normalizado para escala 2 antes do hash (`"25"` ≡ `"25.00"`).
- Mesma chave + mesmo hash → resultado persistido com `idempotentReplay: true` e o **saldo observado no processamento original**.
- Mesma chave + hash diferente → `409 Conflict`. Mesmo `(providerId, externalTransactionId)` sob outra chave → conflito (índice único parcial).
- **Tratamento de corrida**: duas chamadas concorrentes que passam do check inicial se serializam no lock da carteira; a que perde recebe 23505 no INSERT, sua transação faz rollback e o use case re-lê **fora da transação abortada** (`resolveRaceByKey`) para virar replay ou conflito. Encontrado pelo teste de 50 replays paralelos.
- Idempotência é persistente (tabela), nunca em memória; a inbox deduplica reentregas por `(consumer, messageId)` + hash do corpo em todas as instâncias (nome do consumer é constante).

## 5. Reversões (REFUND/ROLLBACK)

- Resolução da referência por `(providerId, referenceExternalTransactionId)`; a referência interna resolvida é persistida para auditoria.
- A reversão precisa coincidir com a referência em provedor, jogador, carteira, moeda, rodada e valor exato (reversões parciais fora de escopo).
- Direção: REFUND de um BET credita; ROLLBACK move o contrário do original (BET→crédito, WIN/REFUND→débito).
- **Política de dupla reversão**: qualquer reversão processada ou em voo sobre a mesma referência bloqueia outra (`ALREADY_REVERSED`). Um BET é devolvido exatamente uma vez e REFUND+ROLLBACK não devolvem o mesmo débito duas vezes. Reversão de uma reversão (ROLLBACK de um REFUND) referencia outra transação e é permitida.
- Reversão que deixaria o saldo negativo → `REVERSAL_EXCEEDS_BALANCE` (distinto de `INSUFFICIENT_FUNDS`, como o desafio exige).
- Referência existe mas não foi processada: ainda em voo (`PENDING`/`PENDING_REFERENCE`) → a reversão espera (`PENDING_REFERENCE` + `WagerTransactionPendingReference`); terminou sem sucesso → rejeição imediata com `REFERENCE_UNSUCCESSFUL`.

## 6. Referências pendentes

Operações em `PENDING_REFERENCE` são retomadas por um worker dedicado via `ProcessWager.Resume` — o mesmo código de validação e movimento do caminho síncrono. Retries usam backoff exponencial durável (`reference_attempts`, `next_reference_attempt_at` persistidos; `2^attempts` segundos com cap de 60s). Esgotamento (TTL `PENDING_REF_TTL`, default 24h, ou 50 tentativas) → `REJECTED` com `REFERENCE_NOT_FOUND` + evento de rejeição. Rejeições são terminais e auditáveis.

## 7. Inbox e ciclo de vida das mensagens (SQS)

- Fila `wager-transactions.fifo`, DLQ via redrive (`maxReceiveCount=3` no compose).
- Por mensagem: uma transação SQL = `TryRegister` na inbox (hash do corpo) → `ProcessWager.Execute` → `Complete` na inbox. **Remoção da fila só após o commit.**
- Reentrega da mesma `messageId`: hash coincide → nada a fazer → deleta; hash difere → anomalia, mensagem fica para redrive (`ErrMessageHashMismatch`).
- Rejeições de negócio confirmadas são terminais → deleta. Falhas transitórias → ficam para reentrega com backoff da visibility timeout; esgotado → DLQ.
- Payloads inválidos (não parseáveis) ficam para redrive após `maxReceiveCount`.
- SIGTERM: para de poll, conclui a mensagem em voo dentro da janela de graça; mensagens não consumidas voltam a ficar visíveis e a inbox absorve os replays.
- O nome do consumer é a constante `sqs-worker` em toda instância para que a inbox dedup entre processos.

## 8. Outbox transacional

- Eventos são gravados na tabela `outbox` na mesma transação SQL da mudança de domínio; o publisher é o único emissor.
- Publisher: claim (`FOR UPDATE SKIP LOCKED` + lease de 60s em `next_attempt_at`) → publica em `wager-events.fifo` → `MarkPublished`. Falha → backoff exponencial (`attempts`). Claim de publisher morto expira e outra instância reivindica.
- At-least-once com identidade estável: `MessageDeduplicationId = eventId`; republicação preserva o `eventId`, então consumidores deduplicam. Falha de `MarkPublished` após envio bem-sucedido é apenas logada (o reclaim por lease reenviaria; o dedup do broker absorve).
- Envelope: `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` (opcional), `occurredAt` (UTC RFC 3339), `version` (versão do schema do evento, fixada pelo construtor), `data` (snapshot tipado; money como strings decimais).
- Eventos: `WagerTransactionProcessed` (inclui LOSS), `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference`.

## 9. Autenticação e autorização

- Keycloak (recomendado pelo desafio), `client_credentials` para comunicação serviço-a-serviço; o realm é provisionado automaticamente pelo Compose (`--import-realm`), com clients `internal`, `provider-a`, `provider-b`, cada um carregando um claim `providerId` (mapper hardcoded).
- O issuer é fixado pelo `frontendUrl` do realm (`http://localhost:8180/realms/jungle`) para que os tokens carreguem `iss` estável independente da topologia de rede; o processo pode buscar o JWKS em host diferente (`KEYCLOAK_JWKS_URL`) do issuer que valida (`KEYCLOAK_ISSUER_URL`). Validação: RS256 via JWKS com refresh em background, issuer compatível, `exp` obrigatório, `providerId` obrigatório.
- **O `providerId` sempre vem do token, nunca do body.** Um provedor que envia `providerId` de outro no body recebe 403. Operações de carteira (abertura, leitura, ledger, reconciliação) são restritas à identidade `internal`. Isolamento nas consultas: provedor buscando transação de outro recebe 404 (a existência não é revelada).
- Alternativa considerada: permissões embutidas em roles do realm. A abordagem por claim mantém o mapeamento identidade→provedor dentro do provisionamento do IdP, que é justamente a peça que o desafio pede para justificar.

## 10. Composição Fx e shutdown

- `fx.Module("api"|"worker")` compartilhando `storageProviders()` (pool, repos, use cases, métricas) mais providers de transporte por app. Interfaces do domínio são ligadas com `fx.Annotate(..., fx.As(...))`; use cases são conectados via construtores.
- Lifecycle: server/workers iniciam em `OnStart` sob um contexto cancelável; `OnStop` cancela e espera (server: drenagem HTTP graciosa; workers: concluem trabalho em voo ou liberam visibilidade). Dependências fecham após os componentes que as usam.
- `context.Context` não é injetável pelo Fx (limitação do framework); os entrypoints usam `context.Background()` nos construtores.

## 11. LocalStack e infraestrutura de teste

- Peculiaridades do LocalStack 4.14 tratadas: `SetQueueAttributes` persiste mas **não enforça** atributos; atributos de fila precisam ser definidos no create (feito no `init.sh` via CLI interna do container); nomes `.fifo` exigem o atributo `FifoQueue` (erro enganoso de "invalid name" caso contrário); scripts de init precisam ser LF (CRLF quebra o shebang).
- Testes de integração rodam no banco dedicado `jungle_test` (`TEST_POSTGRES_DSN`); dados de desenvolvimento em `jungle` nunca são tocados. As suítes compartilham o banco, por isso `go test -p 1` nas execuções de integração.

## 12. Limitações e interpretações conhecidas

- Operações sem dependência de referência concluem de forma síncrona em um único commit (sem estágio de aceite assíncrono); o caminho de referência pendente cobre o caso assíncrono.
- Apenas BRL nos cenários principais (permitido pelo enunciado); `Money` carrega a moeda e operações com moedas incompatíveis são testadas.
- Tracing com OpenTelemetry, dashboards, partidas dobradas e testes de carga são diferenciais opcionais não implementados.
- O publisher da outbox agrupa mensagens FIFO por `eventId` — não há contrato de ordenação entre eventos; consumidores que exigirem ordenação por agregado agrupariam por `aggregateId`.
- `WORKER_MODE` na config é resquício do skeleton inicial e não é usado (os entrypoints já separam os papéis).
- O LocalStack descarta atributos definidos após a criação da fila — a fila de testes é provisionada no init do container com os atributos de redrive/visibilidade enforçados.