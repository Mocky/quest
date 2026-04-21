package batch

import (
	stderrors "errors"
	"testing"

	"github.com/mocky/quest/internal/errors"
)

// TestValidateTier pins the spec §Model tiers enum. Every T0-T6 must
// pass; empty must pass (unset); any other string must return an
// ErrUsage-wrapped error. The loop over ValidTiers guards against
// accidental list drift — adding a tier to the slice automatically
// extends the allow-list coverage.
func TestValidateTier(t *testing.T) {
	for _, ok := range ValidTiers {
		if err := ValidateTier(ok); err != nil {
			t.Errorf("ValidateTier(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidateTier(""); err != nil {
		t.Errorf("ValidateTier(\"\") = %v, want nil", err)
	}
	for _, bad := range []string{"T7", "t1", "tier-3", "3", "T10", "T-1"} {
		err := ValidateTier(bad)
		if err == nil {
			t.Errorf("ValidateTier(%q) = nil, want error", bad)
			continue
		}
		if !stderrors.Is(err, errors.ErrUsage) {
			t.Errorf("ValidateTier(%q) error = %v, want ErrUsage", bad, err)
		}
	}
}

// TestValidateTierMessage pins the human-readable stderr tail for the
// tier enum so agents grepping for the rejection hint remain stable.
func TestValidateTierMessage(t *testing.T) {
	err := ValidateTier("T9")
	if err == nil {
		t.Fatal("ValidateTier(\"T9\") = nil, want error")
	}
	want := `unknown tier "T9" (want one of T0, T1, T2, T3, T4, T5, T6)`
	if got := err.Error(); got[:len(want)] != want {
		t.Errorf("ValidateTier message prefix = %q, want %q", got, want)
	}
}

// TestValidateSeverity pins the spec §Planning fields severity enum.
// Every ValidSeverities entry and the empty string must pass; anything
// else — including casing variants — must return ErrUsage. The loop
// over ValidSeverities auto-extends coverage if the list grows.
func TestValidateSeverity(t *testing.T) {
	for _, ok := range ValidSeverities {
		if err := ValidateSeverity(ok); err != nil {
			t.Errorf("ValidateSeverity(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidateSeverity(""); err != nil {
		t.Errorf("ValidateSeverity(\"\") = %v, want nil", err)
	}
	for _, bad := range []string{"Critical", "CRITICAL", "urgent", "trivial", "0"} {
		err := ValidateSeverity(bad)
		if err == nil {
			t.Errorf("ValidateSeverity(%q) = nil, want error", bad)
			continue
		}
		if !stderrors.Is(err, errors.ErrUsage) {
			t.Errorf("ValidateSeverity(%q) error = %v, want ErrUsage", bad, err)
		}
	}
}

// TestValidateSeverityMessage pins the stderr tail so agents grepping
// for the rejection hint remain stable.
func TestValidateSeverityMessage(t *testing.T) {
	err := ValidateSeverity("urgent")
	if err == nil {
		t.Fatal("ValidateSeverity(\"urgent\") = nil, want error")
	}
	want := `unknown severity "urgent" (want one of critical, high, medium, low)`
	if got := err.Error(); got[:len(want)] != want {
		t.Errorf("ValidateSeverity message prefix = %q, want %q", got, want)
	}
}
