// Package domain holds the financial core: value objects, aggregates and
// integration events. It must not depend on Fx, HTTP, SQS or persistence.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var ErrCurrencyMismatch = errors.New("domain: currency mismatch")
var ErrInvalidMoney = errors.New("domain: invalid money value")
var ErrMoneyOverflow = errors.New("domain: money overflow")

type Currency string

const BRL Currency = "BRL"

const moneyScale = 100

// Money is an immutable monetary value: int64 minor units (fixed scale 2)
// plus an ISO 4217 currency. At scale 2 the int64 range covers about
// ±9.2e14 major units. Negative units are valid only for internal
// arithmetic; external financial inputs are never negative.
type Money struct {
	units    int64
	currency Currency
}

// Zero returns the zero amount for the given currency.
func Zero(c Currency) (Money, error) {
	if c == "" {
		return Money{}, ErrInvalidMoney
	}
	return Money{currency: c}, nil
}

// NewMoney builds Money from minor units. Negative units are allowed for
// internal arithmetic only. The currency must be a non-empty ISO 4217 code.
func NewMoney(units int64, c Currency) (Money, error) {
	if c == "" {
		return Money{}, ErrInvalidMoney
	}
	return Money{units: units, currency: c}, nil
}

// NewMoneyUnchecked builds Money from known-valid parts. It exists for tests
// and rehydration where validation happened earlier; it panics on an empty
// currency because that is a programming error, not business input.
func NewMoneyUnchecked(units int64, c Currency) Money {
	if c == "" {
		panic("domain: NewMoneyUnchecked requires a currency")
	}
	return Money{units: units, currency: c}
}

// ParseMoney parses a fixed-scale decimal string (e.g. "25.00") into Money.
// It rejects: empty input, whitespace, NaN/Infinity (case-insensitive),
// scientific notation, scale above two decimals, missing integer part,
// negative values and int64 overflow. Accepted forms: "0", "25", "25.0",
// "25.00" — all normalize to scale 2.
func ParseMoney(amount string, c Currency) (Money, error) {
	if c == "" {
		return Money{}, ErrInvalidMoney
	}
	s := amount
	if s == "" {
		return Money{}, fmt.Errorf("%w: empty amount", ErrInvalidMoney)
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "nan") || strings.Contains(lower, "inf") {
		return Money{}, fmt.Errorf("%w: %q is not a finite decimal", ErrInvalidMoney, amount)
	}
	if strings.ContainsAny(lower, "eE") {
		return Money{}, fmt.Errorf("%w: %q uses scientific notation", ErrInvalidMoney, amount)
	}
	if s[0] == '-' {
		return Money{}, fmt.Errorf("%w: %q is negative", ErrInvalidMoney, amount)
	}
	if s[0] == '+' {
		return Money{}, fmt.Errorf("%w: %q has a sign prefix", ErrInvalidMoney, amount)
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return Money{}, fmt.Errorf("%w: %q has invalid character %q", ErrInvalidMoney, amount, r)
		}
	}

	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if intPart == "" {
		return Money{}, fmt.Errorf("%w: %q misses the integer part", ErrInvalidMoney, amount)
	}
	if hasDot && (len(fracPart) > 2 || fracPart == "") {
		return Money{}, fmt.Errorf("%w: %q exceeds scale 2", ErrInvalidMoney, amount)
	}

	units, err := parseUnits(intPart, fracPart)
	if err != nil {
		return Money{}, err
	}
	return Money{units: units, currency: c}, nil
}

// parseUnits converts integer and fraction digit strings to minor units with
// overflow checks at every step.
func parseUnits(intPart, fracPart string) (int64, error) {
	var units int64
	for i := 0; i < len(intPart); i++ {
		d := int64(intPart[i] - '0')
		if units > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("%w: integer part overflows", ErrMoneyOverflow)
		}
		units = units*10 + d
	}
	frac := fracPart
	for len(frac) < 2 {
		frac += "0"
	}
	for i := 0; i < 2; i++ {
		d := int64(frac[i] - '0')
		if units > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("%w: fraction part overflows", ErrMoneyOverflow)
		}
		units = units*10 + d
	}
	return units, nil
}

// Units returns the minor-unit value.
func (m Money) Units() int64 { return m.units }

// Currency returns the ISO 4217 code.
func (m Money) Currency() Currency { return m.currency }

// IsZero reports whether the value is zero.
func (m Money) IsZero() bool { return m.units == 0 }

// IsPositive reports whether the value is strictly greater than zero.
func (m Money) IsPositive() bool { return m.units > 0 }

// Add sums two amounts of the same currency with overflow checking.
func (m Money) Add(o Money) (Money, error) {
	if m.currency != o.currency {
		return Money{}, ErrCurrencyMismatch
	}
	sum := m.units + o.units
	if (o.units > 0 && sum < m.units) || (o.units < 0 && sum > m.units) {
		return Money{}, ErrMoneyOverflow
	}
	return Money{units: sum, currency: m.currency}, nil
}

// Sub subtracts o from m (same currency) with overflow checking.
func (m Money) Sub(o Money) (Money, error) {
	neg, err := o.Neg()
	if err != nil {
		return Money{}, err
	}
	return m.Add(neg)
}

// Neg negates the value, rejecting MinInt64 (which has no positive pair).
func (m Money) Neg() (Money, error) {
	if m.units == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{units: -m.units, currency: m.currency}, nil
}

// Cmp compares two amounts of the same currency: -1, 0 or 1.
func (m Money) Cmp(o Money) (int, error) {
	if m.currency != o.currency {
		return 0, ErrCurrencyMismatch
	}
	switch {
	case m.units < o.units:
		return -1, nil
	case m.units > o.units:
		return 1, nil
	default:
		return 0, nil
	}
}

// String renders the value in fixed scale 2, e.g. "25.00", "-0.01".
func (m Money) String() string {
	sign := ""
	u := m.units
	if u < 0 {
		sign = "-"
		u = -u
	}
	return sign + strconv.FormatInt(u/moneyScale, 10) + "." + fmt.Sprintf("%02d", u%moneyScale)
}

// MarshalJSON encodes as {"amount":"25.00","currency":"BRL"} — the amount is
// always a fixed-scale decimal string, never a number.
func (m Money) MarshalJSON() ([]byte, error) {
	if m.currency == "" {
		return nil, ErrInvalidMoney
	}
	return []byte(`{"amount":"` + m.String() + `","currency":"` + string(m.currency) + `"}`), nil
}

// UnmarshalJSON parses {"amount":"<decimal string>","currency":"<ISO 4217>"}.
// The amount must be a JSON string; numeric amounts are rejected so that no
// value ever round-trips through a float.
func (m *Money) UnmarshalJSON(data []byte) error {
	var raw struct {
		Amount   *string   `json:"amount"`
		Currency *Currency `json:"currency"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: malformed money JSON", ErrInvalidMoney)
	}
	if raw.Amount == nil || raw.Currency == nil || *raw.Currency == "" {
		return fmt.Errorf("%w: amount and currency are required", ErrInvalidMoney)
	}
	parsed, err := ParseMoney(*raw.Amount, *raw.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
