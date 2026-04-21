package batch

import (
	stderrors "errors"
	"testing"

	"github.com/mocky/quest/internal/errors"
)

// TestParseCommit_Valid pins the happy paths: canonical BRANCH@HASH,
// branch names containing '@' (split on rightmost '@'), and the 4-char
// minimum hash length.
func TestParseCommit_Valid(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantBranch string
		wantHash   string
	}{
		{name: "simple", input: "master@abc1234", wantBranch: "master", wantHash: "abc1234"},
		{name: "min-4-char-hash", input: "main@abcd", wantBranch: "main", wantHash: "abcd"},
		{name: "long-hash", input: "main@abcdef0123456789abcdef0123456789abcdef01", wantBranch: "main", wantHash: "abcdef0123456789abcdef0123456789abcdef01"},
		{name: "feature-slash-branch", input: "feature/auth@1a2b3c4", wantBranch: "feature/auth", wantHash: "1a2b3c4"},
		{name: "at-sign-in-branch", input: "release@2025@abc1234", wantBranch: "release@2025", wantHash: "abc1234"},
		{name: "double-at-in-branch", input: "user@feature/x@deadbeef", wantBranch: "user@feature/x", wantHash: "deadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCommit("--commit", tc.input)
			if err != nil {
				t.Fatalf("ParseCommit(%q) = %v, want nil", tc.input, err)
			}
			if c.Branch != tc.wantBranch {
				t.Errorf("branch = %q, want %q", c.Branch, tc.wantBranch)
			}
			if c.Hash != tc.wantHash {
				t.Errorf("hash = %q, want %q", c.Hash, tc.wantHash)
			}
		})
	}
}

// TestParseCommit_Invalid pins every rejection shape from spec
// §Commit reference format. Every case must wrap ErrUsage so the
// dispatcher maps to exit 2.
func TestParseCommit_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "no-separator", input: "abc1234"},
		{name: "branch-only", input: "master@"},
		{name: "hash-only", input: "@abc1234"},
		{name: "both-empty", input: "@"},
		{name: "hash-too-short", input: "master@abc"},
		{name: "uppercase-hash", input: "master@ABC1234"},
		{name: "mixed-case-hash", input: "master@AbC1234"},
		{name: "non-hex-g", input: "master@g1234"},
		{name: "non-hex-punct", input: "master@1234-5"},
		{name: "leading-space-hash", input: "master@ abc1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCommit("--commit", tc.input)
			if err == nil {
				t.Fatalf("ParseCommit(%q) = nil, want error", tc.input)
			}
			if !stderrors.Is(err, errors.ErrUsage) {
				t.Errorf("ParseCommit(%q) err = %v, want wraps ErrUsage", tc.input, err)
			}
		})
	}
}

// TestParseCommit_FlagNameInMessage pins that the caller-supplied flag
// name appears in the error message so the CLI stderr tail points the
// agent at the offending flag.
func TestParseCommit_FlagNameInMessage(t *testing.T) {
	_, err := ParseCommit("--commit", "master@")
	if err == nil {
		t.Fatal("expected error")
	}
	got := err.Error()
	if got == "" || !containsAll(got, "--commit", "empty hash") {
		t.Errorf("err = %q, want mention of --commit and empty hash", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !hasSub(s, sub) {
			return false
		}
	}
	return true
}

func hasSub(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
