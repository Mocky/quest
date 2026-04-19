package ids

import (
	"strings"
	"testing"
)

func TestValidatePrefix(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		wantErr string
	}{
		{"simple lowercase", "proj", ""},
		{"letters and digits", "p2", ""},
		{"min length", "ab", ""},
		{"max length", "abcdefgh", ""},
		{"too short", "p", "must be 2-8 characters"},
		{"too long", "abcdefghi", "must be 2-8 characters"},
		{"empty", "", "must be 2-8 characters"},
		{"starts with digit", "1proj", "must start with a letter"},
		{"contains hyphen", "my-proj", "lowercase letters and digits only"},
		{"contains dot", "my.proj", "lowercase letters and digits only"},
		{"contains underscore", "my_proj", "lowercase letters and digits only"},
		{"uppercase", "Proj", "lowercase letters and digits only"},
		{"reserved ref", "ref", "reserved prefix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePrefix(tt.prefix)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePrefix(%q) unexpected error: %v", tt.prefix, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePrefix(%q) error = nil, want error containing %q", tt.prefix, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidatePrefix(%q) error = %q, want substring %q",
					tt.prefix, err.Error(), tt.wantErr)
			}
		})
	}
}
