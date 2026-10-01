package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

func externalInput(kind domain.Kind, m domain.Money) domain.ExternalTransactionInput {
	return domain.ExternalTransactionInput{
		ProviderID:     "provider-a",
		ExternalTransactionID:     "tx-1",
		IdempotencyKey: "provider-a:tx-1",
		PayloadHash:    "abc123",
		PlayerID:       "player-1",
		WalletID:       "wallet-1",
		RoundID:        "round-1",
		GameID:         "fortune-chimp",
		Kind:           kind,
		Money:          m,
	}
}

func TestExternalTransactionStartsPending(t *testing.T) {
	tx, err := domain.NewExternalTransaction(externalInput(domain.KindBet, mustMoney(t, 2500, domain.BRL)), fixedNow)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	if tx.Status() != domain.StatusPending {
		t.Errorf("status = %s, want PENDING", tx.Status())
	}
	if tx.Source() != domain.SourceExternal {
		t.Errorf("source = %s, want EXTERNAL", tx.Source())
	}
	if tx.ID() == "" {
		t.Error("id must be generated")
	}
	if tx.CreatedAt() != fixedNow || tx.UpdatedAt() != fixedNow {
		t.Error("timestamps must come from the passed instant")
	}
}

func TestExternalTransactionCarriesAllFields(t *testing.T) {
	in := externalInput(domain.KindRefund, mustMoney(t, 2500, domain.BRL))
	in.ReferenceExternalTransactionID = "tx-0"
	tx, err := domain.NewExternalTransaction(in, fixedNow)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	if tx.ProviderID() != "provider-a" || tx.ExternalTransactionID() != "tx-1" || tx.IdempotencyKey() != "provider-a:tx-1" {
		t.Error("external identity fields lost")
	}
	if tx.PayloadHash() != "abc123" || tx.PlayerID() != "player-1" || tx.WalletID() != "wallet-1" {
		t.Error("payload fields lost")
	}
	if tx.RoundID() != "round-1" || tx.GameID() != "fortune-chimp" {
		t.Error("rounding fields lost")
	}
	if tx.Money().Units() != 2500 || tx.ReferenceExternalTransactionID() != "tx-0" {
		t.Error("money/reference lost")
	}
}

func TestExternalTransactionRejectsOpening(t *testing.T) {
	if _, err := domain.NewExternalTransaction(externalInput(domain.KindOpening, mustMoney(t, 2500, domain.BRL)), fixedNow); err == nil {
		t.Error("OPENING must be rejected in the external constructor")
	}
}

func TestExternalTransactionRejectsUnknownKind(t *testing.T) {
	if _, err := domain.NewExternalTransaction(externalInput(domain.Kind("GAMBLE"), mustMoney(t, 2500, domain.BRL)), fixedNow); err == nil {
		t.Error("unknown kind must be rejected")
	}
}

func TestExternalTransactionRejectsRequiredFields(t *testing.T) {
	base := externalInput(domain.KindBet, mustMoney(t, 2500, domain.BRL))
	for field, mutate := range map[string]func(*domain.ExternalTransactionInput){
		"providerId":     func(i *domain.ExternalTransactionInput) { i.ProviderID = "" },
		"externalId":     func(i *domain.ExternalTransactionInput) { i.ExternalTransactionID = "" },
		"idempotencyKey": func(i *domain.ExternalTransactionInput) { i.IdempotencyKey = "" },
		"payloadHash":    func(i *domain.ExternalTransactionInput) { i.PayloadHash = "" },
		"playerId":       func(i *domain.ExternalTransactionInput) { i.PlayerID = "" },
		"walletId":       func(i *domain.ExternalTransactionInput) { i.WalletID = "" },
		"roundId":        func(i *domain.ExternalTransactionInput) { i.RoundID = "" },
		"gameId":         func(i *domain.ExternalTransactionInput) { i.GameID = "" },
	} {
		in := base
		mutate(&in)
		if _, err := domain.NewExternalTransaction(in, fixedNow); err == nil {
			t.Errorf("%s: expected error", field)
		}
	}
}

func TestExternalTransactionRejectsZeroValueMoneyPerKind(t *testing.T) {
	zero, _ := domain.Zero(domain.BRL)
	for _, kind := range []domain.Kind{domain.KindBet, domain.KindWin, domain.KindRefund, domain.KindRollback} {
		if _, err := domain.NewExternalTransaction(externalInput(kind, zero), fixedNow); err == nil {
			t.Errorf("%s with zero amount must be rejected", kind)
		}
	}
}

func TestExternalTransactionRejectsNegativeMoney(t *testing.T) {
	neg := domain.NewMoneyUnchecked(-100, domain.BRL)
	for _, kind := range []domain.Kind{domain.KindBet, domain.KindWin, domain.KindRefund, domain.KindRollback, domain.KindLoss} {
		if _, err := domain.NewExternalTransaction(externalInput(kind, neg), fixedNow); err == nil {
			t.Errorf("%s with negative amount must be rejected", kind)
		}
	}
}

func TestLossRequiresZeroAmount(t *testing.T) {
	if _, err := domain.NewExternalTransaction(externalInput(domain.KindLoss, mustMoney(t, 2500, domain.BRL)), fixedNow); err == nil {
		t.Error("LOSS with non-zero amount must be rejected")
	}
	zero, _ := domain.Zero(domain.BRL)
	tx, err := domain.NewExternalTransaction(externalInput(domain.KindLoss, zero), fixedNow)
	if err != nil {
		t.Fatalf("LOSS zero: %v", err)
	}
	if tx.Kind() != domain.KindLoss {
		t.Errorf("kind = %s", tx.Kind())
	}
}

func TestRefundRollbackRequireReference(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindRefund, domain.KindRollback} {
		if _, err := domain.NewExternalTransaction(externalInput(kind, mustMoney(t, 2500, domain.BRL)), fixedNow); err == nil {
			t.Errorf("%s without reference must be rejected", kind)
		}
	}
}

func TestBetAndWinWithoutReferenceAllowed(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindBet, domain.KindWin} {
		if _, err := domain.NewExternalTransaction(externalInput(kind, mustMoney(t, 2500, domain.BRL)), fixedNow); err != nil {
			t.Errorf("%s without reference must be allowed: %v", kind, err)
		}
	}
}

func TestOpeningTransactionInternal(t *testing.T) {
	tx, err := domain.NewOpeningTransaction("player-1", "wallet-1", domain.BRL, 100000, fixedNow)
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}
	if tx.Kind() != domain.KindOpening || tx.Source() != domain.SourceInternal {
		t.Errorf("kind/source = %s/%s", tx.Kind(), tx.Source())
	}
	if tx.Status() != domain.StatusPending {
		t.Errorf("status = %s", tx.Status())
	}
	if tx.Money().Units() != 100000 {
		t.Errorf("money = %d", tx.Money().Units())
	}
	if tx.ProviderID() != "" || tx.ExternalTransactionID() != "" || tx.IdempotencyKey() != "" || tx.PayloadHash() != "" {
		t.Error("opening must not carry external metadata")
	}
	if tx.RoundID() != "" || tx.GameID() != "" || tx.ReferenceExternalTransactionID() != "" {
		t.Error("opening must not carry round/game/reference")
	}
}

func TestOpeningAcceptsZeroAmount(t *testing.T) {
	tx, err := domain.NewOpeningTransaction("player-1", "wallet-1", domain.BRL, 0, fixedNow)
	if err != nil {
		t.Fatalf("opening zero: %v", err)
	}
	if !tx.Money().IsZero() {
		t.Error("zero opening lost")
	}
}

func TestOpeningRejectsNegativeAndEmptyIdentity(t *testing.T) {
	if _, err := domain.NewOpeningTransaction("p", "w", domain.BRL, -1, fixedNow); err == nil {
		t.Error("negative opening must be rejected")
	}
	if _, err := domain.NewOpeningTransaction("", "w", domain.BRL, 0, fixedNow); err == nil {
		t.Error("empty player must be rejected")
	}
	if _, err := domain.NewOpeningTransaction("p", "", domain.BRL, 0, fixedNow); err == nil {
		t.Error("empty wallet must be rejected")
	}
	if _, err := domain.NewOpeningTransaction("p", "w", "", 0, fixedNow); err == nil {
		t.Error("empty currency must be rejected")
	}
}

func TestMarkProcessedStoresResultBalance(t *testing.T) {
	tx := mustExternalBet(t)
	result := mustMoney(t, 97500, domain.BRL)
	if err := tx.MarkProcessed(result); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if tx.Status() != domain.StatusProcessed {
		t.Errorf("status = %s", tx.Status())
	}
	if tx.ResultBalance().Units() != 97500 {
		t.Errorf("resultBalance = %d", tx.ResultBalance().Units())
	}
	if tx.FailureCode() != "" {
		t.Errorf("failureCode = %q", tx.FailureCode())
	}
}

func TestTerminalIsTerminal(t *testing.T) {
	result := mustMoney(t, 97500, domain.BRL)

	processed := mustExternalBet(t)
	if err := processed.MarkProcessed(result); err != nil {
		t.Fatal(err)
	}
	if err := processed.MarkProcessed(result); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("PROCESSED -> MarkProcessed: %v", err)
	}
	if err := processed.MarkRejected("X"); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("PROCESSED -> MarkRejected: %v", err)
	}
	if err := processed.MarkFailed("X"); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("PROCESSED -> MarkFailed: %v", err)
	}
	if err := processed.MarkPendingReference(); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("PROCESSED -> MarkPendingReference: %v", err)
	}

	rejected := mustExternalBet(t)
	if err := rejected.MarkRejected("INSUFFICIENT_FUNDS"); err != nil {
		t.Fatal(err)
	}
	if err := rejected.MarkProcessed(result); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("REJECTED -> MarkProcessed: %v", err)
	}
	if err := rejected.MarkPendingReference(); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("REJECTED -> MarkPendingReference: %v", err)
	}

	failed := mustExternalBet(t)
	if err := failed.MarkFailed("INFRA"); err != nil {
		t.Fatal(err)
	}
	if err := failed.MarkProcessed(result); !errors.Is(err, domain.ErrTerminalState) {
		t.Errorf("FAILED -> MarkProcessed: %v", err)
	}
}

func TestPendingReferenceTransitions(t *testing.T) {
	tx := mustExternalBet(t)
	if err := tx.MarkPendingReference(); err != nil {
		t.Fatalf("MarkPendingReference: %v", err)
	}
	if tx.Status() != domain.StatusPendingReference {
		t.Errorf("status = %s", tx.Status())
	}
	if err := tx.MarkProcessed(mustMoney(t, 97500, domain.BRL)); err != nil {
		t.Errorf("PENDING_REFERENCE -> PROCESSED: %v", err)
	}
}

func TestPendingReferenceCanBeRejected(t *testing.T) {
	tx := mustExternalBet(t)
	if err := tx.MarkPendingReference(); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkRejected("REFERENCE_NOT_FOUND"); err != nil {
		t.Errorf("PENDING_REFERENCE -> REJECTED: %v", err)
	}
	if tx.Status() != domain.StatusRejected || tx.FailureCode() != "REFERENCE_NOT_FOUND" {
		t.Errorf("status/failureCode = %s/%s", tx.Status(), tx.FailureCode())
	}
}

func TestMarkRejectedRequiresFailureCode(t *testing.T) {
	tx := mustExternalBet(t)
	if err := tx.MarkRejected(""); err == nil {
		t.Error("REJECTED without failureCode must be rejected")
	}
	if err := tx.MarkRejected(strings.Repeat("A", 1)); err != nil {
		t.Errorf("valid failureCode: %v", err)
	}
}

func TestMarkFailedRequiresFailureCode(t *testing.T) {
	tx := mustExternalBet(t)
	if err := tx.MarkFailed(""); err == nil {
		t.Error("FAILED without failureCode must be rejected")
	}
}

func TestRehydrateRoundTrip(t *testing.T) {
	in := externalInput(domain.KindRefund, mustMoney(t, 2500, domain.BRL))
	in.ReferenceExternalTransactionID = "tx-0"
	tx, err := domain.NewExternalTransaction(in, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkPendingReference(); err != nil {
		t.Fatal(err)
	}

	restored, err := domain.RehydrateTransaction(domain.TransactionState{
		ID:                             tx.ID(),
		Source:                         tx.Source(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:                     tx.ExternalTransactionID(),
		IdempotencyKey:                 tx.IdempotencyKey(),
		PayloadHash:                    tx.PayloadHash(),
		PlayerID:                       tx.PlayerID(),
		WalletID:                       tx.WalletID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           tx.Kind(),
		Money:                          tx.Money(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ResolvedReferenceTransactionID: "internal-123",
		Status:                         tx.Status(),
		FailureCode:                    tx.FailureCode(),
		ResultBalance:                  tx.ResultBalance(),
		CreatedAt:                      tx.CreatedAt(),
		UpdatedAt:                      tx.UpdatedAt(),
	})
	if err != nil {
		t.Fatalf("RehydrateTransaction: %v", err)
	}
	if restored.ID() != tx.ID() || restored.Status() != domain.StatusPendingReference {
		t.Error("rehydration lost state")
	}
	if restored.ResolvedReferenceTransactionID() != "internal-123" {
		t.Error("rehydration lost resolved reference")
	}
	if restored.CreatedAt() != fixedNow {
		t.Error("rehydration must preserve original timestamps")
	}

	after, err := domain.NewExternalTransaction(in, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt() == tx.UpdatedAt() && after.ID() == tx.ID() {
		t.Error("independent transactions must differ")
	}
}

func TestRehydrateKeepsProcessedState(t *testing.T) {
	tx := mustExternalBet(t)
	if err := tx.MarkProcessed(mustMoney(t, 97500, domain.BRL)); err != nil {
		t.Fatal(err)
	}
	restored, err := domain.RehydrateTransaction(domain.TransactionState{
		ID:             tx.ID(),
		Source:         tx.Source(),
		ProviderID:     tx.ProviderID(),
		ExternalTransactionID:     tx.ExternalTransactionID(),
		IdempotencyKey: tx.IdempotencyKey(),
		PayloadHash:    tx.PayloadHash(),
		PlayerID:       tx.PlayerID(),
		WalletID:       tx.WalletID(),
		RoundID:        tx.RoundID(),
		GameID:         tx.GameID(),
		Kind:           tx.Kind(),
		Money:          tx.Money(),
		Status:         tx.Status(),
		ResultBalance:  tx.ResultBalance(),
		CreatedAt:      tx.CreatedAt(),
		UpdatedAt:      tx.UpdatedAt(),
	})
	if err != nil {
		t.Fatalf("RehydrateTransaction: %v", err)
	}
	if restored.Status() != domain.StatusProcessed || restored.ResultBalance().Units() != 97500 {
		t.Error("processed state lost")
	}
	if err := restored.MarkRejected("X"); !errors.Is(err, domain.ErrTerminalState) {
		t.Error("rehydrated terminal transaction must stay terminal")
	}
}

func TestRehydrateTransactionRejectsInvalidState(t *testing.T) {
	valid := func() domain.TransactionState {
		in := externalInput(domain.KindBet, mustMoney(t, 2500, domain.BRL))
		return domain.TransactionState{
			ID:             "tx-internal",
			Source:         domain.SourceExternal,
			ProviderID:     in.ProviderID,
			ExternalTransactionID:     in.ExternalTransactionID,
			IdempotencyKey: in.IdempotencyKey,
			PayloadHash:    in.PayloadHash,
			PlayerID:       in.PlayerID,
			WalletID:       in.WalletID,
			RoundID:        in.RoundID,
			GameID:         in.GameID,
			Kind:           in.Kind,
			Money:          in.Money,
			Status:         domain.StatusPending,
			CreatedAt:      fixedNow,
			UpdatedAt:      fixedNow,
		}
	}

	if _, err := domain.RehydrateTransaction(valid()); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}

	bad := valid()
	bad.Status = "MAYBE"
	if _, err := domain.RehydrateTransaction(bad); err == nil {
		t.Error("unknown status must be rejected")
	}

	bad = valid()
	bad.Kind = "GAMBLE"
	if _, err := domain.RehydrateTransaction(bad); err == nil {
		t.Error("unknown kind must be rejected")
	}

	bad = valid()
	bad.ID = ""
	if _, err := domain.RehydrateTransaction(bad); err == nil {
		t.Error("empty id must be rejected")
	}

	bad = valid()
	bad.Source = "SOMEWHERE"
	if _, err := domain.RehydrateTransaction(bad); err == nil {
		t.Error("unknown source must be rejected")
	}

	bad = valid()
	bad.IdempotencyKey = ""
	if _, err := domain.RehydrateTransaction(bad); err == nil {
		t.Error("external without idempotency key must be rejected")
	}
}

func TestRehydrateInternalOpening(t *testing.T) {
	state := domain.TransactionState{
		ID:        "tx-internal",
		Source:    domain.SourceInternal,
		PlayerID:  "player-1",
		WalletID:  "wallet-1",
		Kind:      domain.KindOpening,
		Money:     mustMoney(t, 100000, domain.BRL),
		Status:    domain.StatusProcessed,
		CreatedAt: fixedNow,
		UpdatedAt: fixedNow,
	}
	if _, err := domain.RehydrateTransaction(state); err != nil {
		t.Fatalf("internal rehydrate: %v", err)
	}
}

func mustExternalBet(t *testing.T) *domain.WagerTransaction {
	t.Helper()
	tx, err := domain.NewExternalTransaction(externalInput(domain.KindBet, mustMoney(t, 2500, domain.BRL)), fixedNow)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	return tx
}

func TestWhitespaceReferenceRejectedForReversals(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindRefund, domain.KindRollback} {
		in := externalInput(kind, mustMoney(t, 2500, domain.BRL))
		in.ReferenceExternalTransactionID = "   "
		if _, err := domain.NewExternalTransaction(in, fixedNow); !errors.Is(err, domain.ErrMissingReference) {
			t.Errorf("%s with blank reference: %v", kind, err)
		}
	}
}
