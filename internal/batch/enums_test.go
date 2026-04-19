package batch

import (
	stderrors "errors"
	"testing"

	"github.com/mocky/quest/internal/errors"
)

// TestValidateType pins the spec §Core fields `type` enum. Empty is
// "unset" and must pass; every ValidTypes entry must pass; any other
// string must return an ErrUsage-wrapped error.
func TestValidateType(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty", input: "", wantErr: false},
		{name: "task", input: "task", wantErr: false},
		{name: "bug", input: "bug", wantErr: false},
		{name: "unknown", input: "epic", wantErr: true},
		{name: "uppercase rejected", input: "Task", wantErr: true},
		{name: "whitespace rejected", input: " task", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateType(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateType(%q) = nil, want error", tc.input)
				}
				if !stderrors.Is(err, errors.ErrUsage) {
					t.Errorf("ValidateType(%q) error = %v, want ErrUsage", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateType(%q) = %v, want nil", tc.input, err)
			}
		})
	}
}

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

// TestValidateTypeMessage locks the human-readable message shape so
// the CLI stderr tail stays predictable for agents grepping errors.
func TestValidateTypeMessage(t *testing.T) {
	err := ValidateType("epic")
	if err == nil {
		t.Fatal("ValidateType(\"epic\") = nil, want error")
	}
	want := `unknown type "epic" (want task or bug)`
	if got := err.Error(); got[:len(want)] != want {
		t.Errorf("ValidateType message prefix = %q, want %q", got, want)
	}
}

// TestValidateTierMessage mirrors TestValidateTypeMessage for tier.
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
