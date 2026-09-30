package domain

import (
	"time"
)

const (
	EventTypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	EventTypeWagerTransactionRejected         = "WagerTransactionRejected"
	EventTypeWalletBalanceChanged             = "WalletBalanceChanged"
	EventTypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
)

const eventVersion = 1

// Event is the integration envelope published through the outbox. The payload
// (Data) is an immutable snapshot; eventId is fixed by the caller so that
// republishing preserves identity.
type Event struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          any       `json:"data"`
}

// WalletBalanceChangedData is the payload of WalletBalanceChanged.
type WalletBalanceChangedData struct {
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     Direction `json:"direction"`
	Money         Money     `json:"money"`
	BalanceBefore Money     `json:"balanceBefore"`
	BalanceAfter  Money     `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}

// WagerTransactionProcessedData is the payload of WagerTransactionProcessed.
type WagerTransactionProcessedData struct {
	TransactionID         string `json:"transactionId"`
	ProviderID            string `json:"providerId,omitempty"`
	ExternalTransactionID string `json:"externalTransactionId,omitempty"`
	Kind                  Kind   `json:"kind"`
	Money                 Money  `json:"money"`
	ResultBalance         Money  `json:"resultBalance"`
}

// WagerTransactionRejectedData is the payload of WagerTransactionRejected.
type WagerTransactionRejectedData struct {
	TransactionID         string `json:"transactionId"`
	ProviderID            string `json:"providerId,omitempty"`
	ExternalTransactionID string `json:"externalTransactionId,omitempty"`
	Kind                  Kind   `json:"kind"`
	FailureCode           string `json:"failureCode"`
}

// WagerTransactionPendingReferenceData is the payload of
// WagerTransactionPendingReference.
type WagerTransactionPendingReferenceData struct {
	TransactionID                  string `json:"transactionId"`
	ProviderID                     string `json:"providerId,omitempty"`
	ExternalTransactionID          string `json:"externalTransactionId,omitempty"`
	Kind                           Kind   `json:"kind"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
}

func newEnvelope(eventID, eventType, aggregateID, correlationID string, occurredAt time.Time, data any) Event {
	if eventID == "" {
		panic("domain: event id is required")
	}
	return Event{
		EventID:       eventID,
		EventType:     eventType,
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       eventVersion,
		Data:          data,
	}
}

// NewWalletBalanceChanged builds the balance-change event for one movement.
func NewWalletBalanceChanged(eventID, correlationID, walletID, transactionID string, dir Direction, money, balanceBefore, balanceAfter Money, walletVersion int64, occurredAt time.Time) Event {
	data := WalletBalanceChangedData{
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     dir,
		Money:         money,
		BalanceBefore: balanceBefore,
		BalanceAfter:  balanceAfter,
		WalletVersion: walletVersion,
	}
	return newEnvelope(eventID, EventTypeWalletBalanceChanged, walletID, correlationID, occurredAt, data)
}

// NewWagerTransactionProcessed builds the completion event of an operation.
func NewWagerTransactionProcessed(eventID, correlationID, transactionID, providerID, externalTransactionID string, kind Kind, money, resultBalance Money, occurredAt time.Time) Event {
	data := WagerTransactionProcessedData{
		TransactionID:         transactionID,
		ProviderID:            providerID,
		ExternalTransactionID: externalTransactionID,
		Kind:                  kind,
		Money:                 money,
		ResultBalance:         resultBalance,
	}
	return newEnvelope(eventID, EventTypeWagerTransactionProcessed, transactionID, correlationID, occurredAt, data)
}

// NewWagerTransactionRejected builds the definitive-rejection event.
func NewWagerTransactionRejected(eventID, correlationID, transactionID, providerID, externalTransactionID string, kind Kind, failureCode string, occurredAt time.Time) Event {
	data := WagerTransactionRejectedData{
		TransactionID:         transactionID,
		ProviderID:            providerID,
		ExternalTransactionID: externalTransactionID,
		Kind:                  kind,
		FailureCode:           failureCode,
	}
	return newEnvelope(eventID, EventTypeWagerTransactionRejected, transactionID, correlationID, occurredAt, data)
}

// NewWagerTransactionPendingReference builds the waiting-for-reference event.
func NewWagerTransactionPendingReference(eventID, correlationID, transactionID, providerID, externalTransactionID string, kind Kind, referenceExternalTransactionID string, occurredAt time.Time) Event {
	data := WagerTransactionPendingReferenceData{
		TransactionID:                  transactionID,
		ProviderID:                     providerID,
		ExternalTransactionID:          externalTransactionID,
		Kind:                           kind,
		ReferenceExternalTransactionID: referenceExternalTransactionID,
	}
	return newEnvelope(eventID, EventTypeWagerTransactionPendingReference, transactionID, correlationID, occurredAt, data)
}
