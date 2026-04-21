//go:build integration

package command_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mocky/quest/internal/command"
)

// TestVersionHelpShortCircuits pins the STANDARDS.md §`--help` Convention
// for version. The previous handler ran flag parsing for the first time
// here — before qst-1f it emitted the version payload on stdout even
// when --help was present. Help must leave stdout empty (no leaked
// version JSON), write usage to stderr, and exit 0.
func TestVersionHelpShortCircuits(t *testing.T) {
	var out, errb bytes.Buffer
	err := command.Version(context.Background(), baseCfg(), nil,
		[]string{"--help"}, strings.NewReader(""), &out, &errb)
	if err != nil {
		t.Fatalf("Version: err = %v, want nil", err)
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty (no version payload)", out.String())
	}
	if !strings.Contains(errb.String(), "Usage of version") {
		t.Errorf("stderr missing usage text; got %q", errb.String())
	}
}
