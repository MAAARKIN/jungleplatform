package domain_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

func TestParseMoneyValid(t *testing.T) {
	cases := []struct {
		in    string
		units int64
	}{
		{"25.00", 2500},
		{"0.00", 0},
		{"0", 0},
		{"25", 2500},
		{"25.0", 2500},
		{"0.01", 1},
		{"123456789012.34", 12345678901234},
	}
	for _, c := range cases {
		m, err := domain.ParseMoney(c.in, domain.BRL)
		if err != nil {
			t.Errorf("ParseMoney(%q) error: %v", c.in, err)
			continue
		}
		if m.Units() != c.units {
			t.Errorf("ParseMoney(%q) units = %d, want %d", c.in, m.Units(), c.units)
		}
	}
}

func TestParseMoneyRejectsInvalid(t *testing.T) {
	cases := []string{
		"", " ", "NaN", "nan", "Infinity", "+Inf", "-Inf", "inf",
		"1e5", "1E5", "25.001", "abc", ".25", "25.", "25,00",
		"-25.00", "-0.01", "+25.00", " 25.00", "25.00 ", "0x10", "--5",
	}
	for _, in := range cases {
		if _, err := domain.ParseMoney(in, domain.BRL); err == nil {
			t.Errorf("ParseMoney(%q) expected error, got nil", in)
		}
	}
}

func TestParseMoneyOverflow(t *testing.T) {
	// int64 max is 9223372036854775807; max representable units = 92233720368547758.07
	for _, in := range []string{"92233720368547758.08", "99999999999999999999"} {
		if _, err := domain.ParseMoney(in, domain.BRL); err == nil {
			t.Errorf("ParseMoney(%q) expected overflow error", in)
		}
	}
}

func TestParseMoneyRejectsUnknownCurrency(t *testing.T) {
	if _, err := domain.ParseMoney("25.00", ""); err == nil {
		t.Error("expected error for empty currency")
	}
}

func TestNewMoneyAllowsNegativeForInternalUse(t *testing.T) {
	m, err := domain.NewMoney(-2500, domain.BRL)
	if err != nil {
		t.Fatalf("NewMoney(-2500) error: %v", err)
	}
	if m.Units() != -2500 {
		t.Errorf("units = %d, want -2500", m.Units())
	}
	if _, err := domain.NewMoney(1, ""); err == nil {
		t.Error("expected error for empty currency")
	}
}

func TestStringFormatting(t *testing.T) {
	cases := []struct {
		units int64
		want  string
	}{
		{2500, "25.00"},
		{0, "0.00"},
		{1, "0.01"},
		{-1, "-0.01"},
		{-2500, "-25.00"},
		{12345678901234, "123456789012.34"},
	}
	for _, c := range cases {
		m := domain.NewMoneyUnchecked(c.units, domain.BRL)
		if got := m.String(); got != c.want {
			t.Errorf("String(%d) = %q, want %q", c.units, got, c.want)
		}
	}
}

func TestArithmeticRequiresCompatibleCurrency(t *testing.T) {
	brl := domain.NewMoneyUnchecked(2500, domain.BRL)
	usd := domain.NewMoneyUnchecked(1000, "USD")

	if _, err := brl.Add(usd); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("Add mismatch error = %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("Sub mismatch error = %v", err)
	}
	if _, err := brl.Cmp(usd); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("Cmp mismatch error = %v", err)
	}
}

func TestAddSubNeg(t *testing.T) {
	a := domain.NewMoneyUnchecked(2500, domain.BRL)
	b := domain.NewMoneyUnchecked(2500, domain.BRL)

	sum, err := a.Add(b)
	if err != nil || sum.Units() != 5000 {
		t.Errorf("Add = %d, %v; want 5000", sum.Units(), err)
	}
	diff, err := a.Sub(domain.NewMoneyUnchecked(3000, domain.BRL))
	if err != nil || diff.Units() != -500 {
		t.Errorf("Sub = %d, %v; want -500", diff.Units(), err)
	}
	neg, err := diff.Neg()
	if err != nil || neg.Units() != 500 {
		t.Errorf("Neg = %d, %v; want 500", neg.Units(), err)
	}
}

func TestArithmeticOverflow(t *testing.T) {
	max := domain.NewMoneyUnchecked(math.MaxInt64, domain.BRL)
	one := domain.NewMoneyUnchecked(1, domain.BRL)
	if _, err := max.Add(one); err == nil {
		t.Error("Add overflow: expected error")
	}
	if _, err := max.Sub(domain.NewMoneyUnchecked(-math.MaxInt64, domain.BRL)); err == nil {
		t.Error("Sub overflow: expected error")
	}
	min := domain.NewMoneyUnchecked(math.MinInt64, domain.BRL)
	if _, err := min.Neg(); err == nil {
		t.Error("Neg of MinInt64: expected error")
	}
}

func TestCmp(t *testing.T) {
	a := domain.NewMoneyUnchecked(2500, domain.BRL)
	b := domain.NewMoneyUnchecked(3000, domain.BRL)
	c := domain.NewMoneyUnchecked(2500, domain.BRL)

	if r, _ := a.Cmp(b); r != -1 {
		t.Errorf("Cmp = %d, want -1", r)
	}
	if r, _ := b.Cmp(a); r != 1 {
		t.Errorf("Cmp = %d, want 1", r)
	}
	if r, _ := a.Cmp(c); r != 0 {
		t.Errorf("Cmp = %d, want 0", r)
	}
}

func TestIsZeroIsPositive(t *testing.T) {
	z, err := domain.Zero(domain.BRL)
	if err != nil {
		t.Fatalf("Zero(BRL): %v", err)
	}
	if !z.IsZero() || z.IsPositive() {
		t.Error("Zero(BRL) must be zero and not positive")
	}
	p := domain.NewMoneyUnchecked(1, domain.BRL)
	if !p.IsPositive() {
		t.Error("1 cent must be positive")
	}
	n := domain.NewMoneyUnchecked(-1, domain.BRL)
	if n.IsZero() || n.IsPositive() {
		t.Error("-1 cent must be neither zero nor positive")
	}
}

func TestMarshalJSON(t *testing.T) {
	m := domain.NewMoneyUnchecked(2500, domain.BRL)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Errorf("marshal = %s", b)
	}
}

func TestUnmarshalJSONValid(t *testing.T) {
	var m domain.Money
	if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Units() != 2500 || m.Currency() != domain.BRL {
		t.Errorf("unmarshal = %d %s", m.Units(), m.Currency())
	}
}

func TestUnmarshalJSONRoundTrip(t *testing.T) {
	orig := domain.NewMoneyUnchecked(12345678901234, domain.BRL)
	b, _ := json.Marshal(orig)
	var m domain.Money
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Units() != orig.Units() || m.Currency() != orig.Currency() {
		t.Error("round trip lost value or currency")
	}
}

func TestUnmarshalJSONRejectsInvalid(t *testing.T) {
	cases := []string{
		`{"amount":"25.001","currency":"BRL"}`,
		`{"amount":"1e5","currency":"BRL"}`,
		`{"amount":"-25.00","currency":"BRL"}`,
		`{"amount":"NaN","currency":"BRL"}`,
		`{"amount":"25.00","currency":""}`,
		`{"amount":"25.00"}`,
		`{"amount":25.00,"currency":"BRL"}`,
	}
	for _, in := range cases {
		var m domain.Money
		if err := json.Unmarshal([]byte(in), &m); err == nil {
			t.Errorf("unmarshal(%s) expected error", in)
		}
	}
}

func TestJSONRejectsFloatAmounts(t *testing.T) {
	// amounts must never round-trip through float
	var m domain.Money
	if err := json.Unmarshal([]byte(`{"amount":25.5,"currency":"BRL"}`), &m); err == nil {
		t.Error("numeric (non-string) amount must be rejected")
	}
}

func TestParseMoneyCaseInsensitiveSpecials(t *testing.T) {
	for _, in := range []string{"nAn", "iNfInItY"} {
		if _, err := domain.ParseMoney(in, domain.BRL); err == nil {
			t.Errorf("ParseMoney(%q) expected error", in)
		}
	}
}

func TestStringDoesNotUseFloat(t *testing.T) {
	// value that float64 cannot represent exactly: 0.1+0.2 style check
	m := domain.NewMoneyUnchecked(1, domain.BRL)
	if !strings.HasSuffix(m.String(), "0.01") {
		t.Errorf("String = %q", m.String())
	}
}
