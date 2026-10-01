// Package sqsconsumer consumes the wager-transactions.fifo queue. Each
// message is handled inside one SQL transaction with the inbox registration,
// the domain changes and the outbox events; the message is removed from the
// queue only after that transaction commits.
package sqsconsumer

import "github.com/maaarkin/jungleplatform/internal/domain"

// Envelope is the durable message identity plus the business payload.
type Envelope struct {
	MessageID  string `json:"messageId"`
	Type       string `json:"type"`
	OccurredAt string `json:"occurredAt"`
	Data       Data   `json:"data"`
}

// Data mirrors the HTTP contract fields plus the idempotency key.
type Data struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	IdempotencyKey                 string       `json:"idempotencyKey"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          domain.Money `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId"`
}

const expectedType = "WagerTransactionRequested"

// valid reports whether the envelope carries a processable payload.
func (e Envelope) valid() bool {
	return e.MessageID != "" &&
		e.Type == expectedType &&
		e.Data.ProviderID != "" &&
		e.Data.ExternalTransactionID != "" &&
		e.Data.IdempotencyKey != "" &&
		e.Data.PlayerID != "" &&
		e.Data.WalletID != "" &&
		e.Data.RoundID != "" &&
		e.Data.GameID != "" &&
		e.Data.Kind != ""
}
