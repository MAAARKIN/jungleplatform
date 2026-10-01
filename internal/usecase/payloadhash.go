package usecase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// WagerPayload is the business content hashed for idempotency. The idempotency
// key and transport metadata are excluded by construction: they are not fields
// of this struct. Money enters already parsed, so it is normalized to scale 2
// ("25" and "25.00" hash identically).
type WagerPayload struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          domain.Money `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
}

// CanonicalHash hashes the canonical JSON form of v: field values marshaled to
// JSON, re-encoded through generic maps so object keys are alphabetically
// sorted, then SHA-256 hex. Identical business content always produces the
// same hash, regardless of struct declaration order.
func CanonicalHash(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("payloadhash: marshal: %w", err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return "", fmt.Errorf("payloadhash: normalize: %w", err)
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("payloadhash: canonical marshal: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// payload projects the input into the hashed business content.
func (in ProcessWagerInput) payload() WagerPayload {
	return WagerPayload{
		ProviderID:                     in.ProviderID,
		ExternalTransactionID:          in.ExternalTransactionID,
		PlayerID:                       in.PlayerID,
		WalletID:                       in.WalletID,
		RoundID:                        in.RoundID,
		GameID:                         in.GameID,
		Kind:                           string(in.Kind),
		Money:                          in.Money,
		ReferenceExternalTransactionID: in.ReferenceExternalTransactionID,
	}
}
