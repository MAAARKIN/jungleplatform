package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

type Source string

const (
	SourceInternal Source = "INTERNAL"
	SourceExternal Source = "EXTERNAL"
)

var externalKinds = map[Kind]bool{
	KindBet: true, KindWin: true, KindLoss: true, KindRefund: true, KindRollback: true,
}

// ExternalTransactionInput carries the validated business payload for an
// externally originated operation (HTTP or SQS).
type ExternalTransactionInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          Money
	ReferenceExternalTransactionID string
}

// TransactionState is the persisted representation used for rehydration.
type TransactionState struct {
	ID                             string
	Source                         Source
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          Money
	ReferenceExternalTransactionID string
	ResolvedReferenceTransactionID string
	Status                         Status
	FailureCode                    string
	ResultBalance                  Money
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// WagerTransaction is the audit aggregate for one operation. It starts in
// PENDING and reaches exactly one terminal state: PROCESSED, REJECTED or
// FAILED. Terminal states never transition again.
type WagerTransaction struct {
	id                             string
	source                         Source
	providerID                     string
	externalTransactionID          string
	idempotencyKey                 string
	payloadHash                    string
	playerID                       string
	walletID                       string
	roundID                        string
	gameID                         string
	kind                           Kind
	money                          Money
	referenceExternalTransactionID string
	resolvedReferenceTransactionID string
	status                         Status
	failureCode                    string
	resultBalance                  Money
	hasResultBalance               bool
	createdAt                      time.Time
	updatedAt                      time.Time
}

// NewExternalTransaction validates and creates an externally originated
// operation. OPENING is rejected (internal only). Per-kind money rules:
// BET/WIN/REFUND/ROLLBACK require a positive amount, LOSS requires zero;
// REFUND/ROLLBACK require referenceExternalTransactionId.
func NewExternalTransaction(in ExternalTransactionInput, now time.Time) (*WagerTransaction, error) {
	if in.Kind == KindOpening {
		return nil, fmt.Errorf("%w: OPENING is internal only", ErrInvalidInput)
	}
	if !externalKinds[in.Kind] {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidInput, in.Kind)
	}
	if in.ProviderID == "" || in.ExternalTransactionID == "" || in.IdempotencyKey == "" ||
		in.PayloadHash == "" || in.PlayerID == "" || in.WalletID == "" ||
		in.RoundID == "" || in.GameID == "" {
		return nil, fmt.Errorf("%w: provider, external, idempotency, hash, player, wallet, round and game are required", ErrInvalidInput)
	}
	if in.Money.currency == "" {
		return nil, fmt.Errorf("%w: money currency is required", ErrInvalidInput)
	}
	switch in.Kind {
	case KindLoss:
		if !in.Money.IsZero() {
			return nil, fmt.Errorf("%w: LOSS requires a zero amount", ErrInvalidInput)
		}
	case KindRefund, KindRollback:
		if !in.Money.IsPositive() {
			return nil, fmt.Errorf("%w: %s requires a positive amount", ErrInvalidInput, in.Kind)
		}
		if strings.TrimSpace(in.ReferenceExternalTransactionID) == "" {
			return nil, fmt.Errorf("%w: %s requires referenceExternalTransactionId", ErrMissingReference, in.Kind)
		}
	default:
		if !in.Money.IsPositive() {
			return nil, fmt.Errorf("%w: %s requires a positive amount", ErrInvalidInput, in.Kind)
		}
	}
	now = now.UTC()
	return &WagerTransaction{
		id:                             uuid.NewString(),
		source:                         SourceExternal,
		providerID:                     in.ProviderID,
		externalTransactionID:          in.ExternalTransactionID,
		idempotencyKey:                 in.IdempotencyKey,
		payloadHash:                    in.PayloadHash,
		playerID:                       in.PlayerID,
		walletID:                       in.WalletID,
		roundID:                        in.RoundID,
		gameID:                         in.GameID,
		kind:                           in.Kind,
		money:                          in.Money,
		referenceExternalTransactionID: in.ReferenceExternalTransactionID,
		status:                         StatusPending,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// NewOpeningTransaction creates the internal OPENING record for a wallet with
// a positive initial balance. External metadata does not apply to this origin.
func NewOpeningTransaction(playerID, walletID string, c Currency, units int64, now time.Time) (*WagerTransaction, error) {
	if playerID == "" || walletID == "" {
		return nil, fmt.Errorf("%w: player and wallet are required", ErrInvalidInput)
	}
	money, err := NewMoney(units, c)
	if err != nil {
		return nil, err
	}
	if units < 0 {
		return nil, fmt.Errorf("%w: opening amount cannot be negative", ErrInvalidInput)
	}
	now = now.UTC()
	return &WagerTransaction{
		id:        uuid.NewString(),
		source:    SourceInternal,
		playerID:  playerID,
		walletID:  walletID,
		kind:      KindOpening,
		money:     money,
		status:    StatusPending,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// RehydrateTransaction rebuilds a persisted transaction without reapplying
// transitions or emitting events. It validates the stored state so a corrupt
// row never becomes a live aggregate.
func RehydrateTransaction(s TransactionState) (*WagerTransaction, error) {
	if s.ID == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	switch s.Source {
	case SourceInternal, SourceExternal:
	default:
		return nil, fmt.Errorf("%w: unknown source %q", ErrInvalidInput, s.Source)
	}
	switch s.Kind {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidInput, s.Kind)
	}
	switch s.Status {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
	default:
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, s.Status)
	}
	if s.Money.currency == "" {
		return nil, fmt.Errorf("%w: money currency is required", ErrInvalidInput)
	}
	if s.PlayerID == "" || s.WalletID == "" {
		return nil, fmt.Errorf("%w: player and wallet are required", ErrInvalidInput)
	}
	if s.Source == SourceExternal && (s.ProviderID == "" || s.ExternalTransactionID == "" || s.IdempotencyKey == "") {
		return nil, fmt.Errorf("%w: external transactions require provider, external id and idempotency key", ErrInvalidInput)
	}
	if s.Source == SourceInternal {
		if s.Kind != KindOpening {
			return nil, fmt.Errorf("%w: internal source is reserved to OPENING", ErrInvalidInput)
		}
		if s.ProviderID != "" || s.ExternalTransactionID != "" || s.IdempotencyKey != "" {
			return nil, fmt.Errorf("%w: internal transactions must not carry external metadata", ErrInvalidInput)
		}
	}
	return &WagerTransaction{
		id:                             s.ID,
		source:                         s.Source,
		providerID:                     s.ProviderID,
		externalTransactionID:          s.ExternalTransactionID,
		idempotencyKey:                 s.IdempotencyKey,
		payloadHash:                    s.PayloadHash,
		playerID:                       s.PlayerID,
		walletID:                       s.WalletID,
		roundID:                        s.RoundID,
		gameID:                         s.GameID,
		kind:                           s.Kind,
		money:                          s.Money,
		referenceExternalTransactionID: s.ReferenceExternalTransactionID,
		resolvedReferenceTransactionID: s.ResolvedReferenceTransactionID,
		status:                         s.Status,
		failureCode:                    s.FailureCode,
		resultBalance:                  s.ResultBalance,
		hasResultBalance:               s.ResultBalance.currency != "",
		createdAt:                      s.CreatedAt.UTC(),
		updatedAt:                      s.UpdatedAt.UTC(),
	}, nil
}

// ID returns the internal transaction identifier.
func (t *WagerTransaction) ID() string { return t.id }

// Source returns INTERNAL or EXTERNAL.
func (t *WagerTransaction) Source() Source { return t.source }

// ProviderID returns the provider, empty for internal transactions.
func (t *WagerTransaction) ProviderID() string { return t.providerID }

// ExternalTransactionID returns the provider-side transaction id, empty for internal.
func (t *WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }

// IdempotencyKey returns the idempotency key, empty for internal transactions.
func (t *WagerTransaction) IdempotencyKey() string { return t.idempotencyKey }

// PayloadHash returns the canonical payload hash.
func (t *WagerTransaction) PayloadHash() string { return t.payloadHash }

// PlayerID returns the owning player.
func (t *WagerTransaction) PlayerID() string { return t.playerID }

// WalletID returns the affected wallet.
func (t *WagerTransaction) WalletID() string { return t.walletID }

// RoundID returns the game round.
func (t *WagerTransaction) RoundID() string { return t.roundID }

// GameID returns the game identifier.
func (t *WagerTransaction) GameID() string { return t.gameID }

// Kind returns the operation kind.
func (t *WagerTransaction) Kind() Kind { return t.kind }

// Money returns the operation amount.
func (t *WagerTransaction) Money() Money { return t.money }

// ReferenceExternalTransactionID returns the referenced external transaction id.
func (t *WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}

// ResolvedReferenceTransactionID returns the resolved internal reference id.
func (t *WagerTransaction) ResolvedReferenceTransactionID() string {
	return t.resolvedReferenceTransactionID
}

// Status returns the current state.
func (t *WagerTransaction) Status() Status { return t.status }

// FailureCode returns the stable failure or rejection code, empty when none.
func (t *WagerTransaction) FailureCode() string { return t.failureCode }

// ResultBalance returns the wallet balance observed at processing time; it is
// the zero Money when the transaction has not been processed.
func (t *WagerTransaction) ResultBalance() Money { return t.resultBalance }

// HasResultBalance reports whether a processing result balance was recorded.
func (t *WagerTransaction) HasResultBalance() bool { return t.hasResultBalance }

// CreatedAt returns the creation instant (UTC).
func (t *WagerTransaction) CreatedAt() time.Time { return t.createdAt }

// UpdatedAt returns the last transition instant (UTC).
func (t *WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }

func (t *WagerTransaction) isTerminal() bool {
	return t.status == StatusProcessed || t.status == StatusRejected || t.status == StatusFailed
}

func (t *WagerTransaction) touch(now time.Time) {
	t.updatedAt = now.UTC()
}

// MarkProcessed moves PENDING or PENDING_REFERENCE to PROCESSED, recording the
// wallet balance observed at processing time.
func (t *WagerTransaction) MarkProcessed(resultBalance Money) error {
	if t.isTerminal() {
		return fmt.Errorf("%w: %s is terminal", ErrTerminalState, t.status)
	}
	if resultBalance.currency == "" {
		return fmt.Errorf("%w: result balance is required", ErrInvalidInput)
	}
	t.status = StatusProcessed
	t.resultBalance = resultBalance
	t.hasResultBalance = true
	t.touch(time.Now())
	return nil
}

// MarkPendingReference moves PENDING to PENDING_REFERENCE while the referenced
// operation has not arrived yet.
func (t *WagerTransaction) MarkPendingReference() error {
	if t.isTerminal() {
		return fmt.Errorf("%w: %s is terminal", ErrTerminalState, t.status)
	}
	if t.status != StatusPending {
		return fmt.Errorf("%w: %s cannot wait for reference", ErrInvalidTransition, t.status)
	}
	t.status = StatusPendingReference
	t.touch(time.Now())
	return nil
}

// MarkRejected moves a non-terminal transaction to REJECTED with a stable
// failure code. A rejection is a confirmed business outcome.
func (t *WagerTransaction) MarkRejected(failureCode string) error {
	if t.isTerminal() {
		return fmt.Errorf("%w: %s is terminal", ErrTerminalState, t.status)
	}
	if failureCode == "" {
		return fmt.Errorf("%w: rejection requires a failure code", ErrInvalidInput)
	}
	t.status = StatusRejected
	t.failureCode = failureCode
	t.touch(time.Now())
	return nil
}

// MarkFailed moves a non-terminal transaction to FAILED after a permanent
// infrastructure error, recorded for audit.
func (t *WagerTransaction) MarkFailed(failureCode string) error {
	if t.isTerminal() {
		return fmt.Errorf("%w: %s is terminal", ErrTerminalState, t.status)
	}
	if failureCode == "" {
		return fmt.Errorf("%w: failure requires a failure code", ErrInvalidInput)
	}
	t.status = StatusFailed
	t.failureCode = failureCode
	t.touch(time.Now())
	return nil
}
