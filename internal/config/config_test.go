package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkQuest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".quest"), 0o755); err != nil {
		t.Fatalf("mkdir .quest: %v", err)
	}
}

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	mkQuest(t, root)
	if err := os.WriteFile(filepath.Join(root, ".quest", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

func TestDiscoverRoot(t *testing.T) {
	t.Run("at start dir", func(t *testing.T) {
		root := t.TempDir()
		mkQuest(t, root)
		got, err := DiscoverRoot(root)
		if err != nil {
			t.Fatalf("DiscoverRoot: %v", err)
		}
		if got != root {
			t.Errorf("got %q, want %q", got, root)
		}
	})

	t.Run("one level up", func(t *testing.T) {
		root := t.TempDir()
		mkQuest(t, root)
		child := filepath.Join(root, "pkg")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatalf("mkdir pkg: %v", err)
		}
		got, err := DiscoverRoot(child)
		if err != nil {
			t.Fatalf("DiscoverRoot: %v", err)
		}
		if got != root {
			t.Errorf("got %q, want %q", got, root)
		}
	})

	t.Run("many levels up", func(t *testing.T) {
		root := t.TempDir()
		mkQuest(t, root)
		deep := filepath.Join(root, "a", "b", "c", "d", "e")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatalf("mkdir deep: %v", err)
		}
		got, err := DiscoverRoot(deep)
		if err != nil {
			t.Fatalf("DiscoverRoot: %v", err)
		}
		if got != root {
			t.Errorf("got %q, want %q", got, root)
		}
	})

	t.Run("innermost wins", func(t *testing.T) {
		outer := t.TempDir()
		mkQuest(t, outer)
		inner := filepath.Join(outer, "inner")
		if err := os.Mkdir(inner, 0o755); err != nil {
			t.Fatalf("mkdir inner: %v", err)
		}
		mkQuest(t, inner)
		child := filepath.Join(inner, "pkg")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatalf("mkdir pkg: %v", err)
		}
		got, err := DiscoverRoot(child)
		if err != nil {
			t.Fatalf("DiscoverRoot: %v", err)
		}
		if got != inner {
			t.Errorf("got %q, want inner %q", got, inner)
		}
	})

	t.Run("no workspace", func(t *testing.T) {
		start := t.TempDir()
		_, err := DiscoverRoot(start)
		if !errors.Is(err, ErrNoWorkspace) {
			t.Errorf("err = %v, want ErrNoWorkspace", err)
		}
	})
}

func TestReadFile(t *testing.T) {
	t.Run("valid file", func(t *testing.T) {
		root := t.TempDir()
		writeConfig(t, root, `id_prefix = "proj"
elevated_roles = ["planner", "lead"]
`)
		got, err := ReadFile(root)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if got.IDPrefix != "proj" {
			t.Errorf("IDPrefix = %q, want proj", got.IDPrefix)
		}
		want := []string{"planner", "lead"}
		if len(got.ElevatedRoles) != len(want) {
			t.Fatalf("ElevatedRoles = %v, want %v", got.ElevatedRoles, want)
		}
		for i, v := range want {
			if got.ElevatedRoles[i] != v {
				t.Errorf("ElevatedRoles[%d] = %q, want %q", i, got.ElevatedRoles[i], v)
			}
		}
	})

	t.Run("missing file", func(t *testing.T) {
		root := t.TempDir()
		mkQuest(t, root)
		_, err := ReadFile(root)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("empty root path", func(t *testing.T) {
		_, err := ReadFile("")
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("malformed TOML", func(t *testing.T) {
		root := t.TempDir()
		writeConfig(t, root, "id_prefix = \x00not toml\n")
		_, err := ReadFile(root)
		if err == nil {
			t.Fatal("err = nil, want parse error")
		}
		if errors.Is(err, os.ErrNotExist) {
			t.Errorf("err classified as not-exist; wanted a parse error: %v", err)
		}
		if !strings.Contains(err.Error(), "parse") {
			t.Errorf("err = %v, want to mention parse", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		root := t.TempDir()
		writeConfig(t, root, "")
		got, err := ReadFile(root)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if got.IDPrefix != "" || got.ElevatedRoles != nil {
			t.Errorf("got %+v, want zero FileConfig", got)
		}
	})

	t.Run("unknown field warns", func(t *testing.T) {
		root := t.TempDir()
		writeConfig(t, root, `id_prefix = "proj"
future_field = "someday"
`)
		rec := captureSlog(t)
		got, err := ReadFile(root)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if got.IDPrefix != "proj" {
			t.Errorf("IDPrefix = %q, want proj", got.IDPrefix)
		}
		if !rec.has("unknown config field") {
			t.Errorf("no WARN record for unknown field; records: %v", rec.records())
		}
	})

	t.Run("enforce_session_ownership", func(t *testing.T) {
		cases := []struct {
			name string
			body string
			want bool
		}{
			{"absent defaults false", `id_prefix = "proj"` + "\n", false},
			{"explicit false", `id_prefix = "proj"` + "\nenforce_session_ownership = false\n", false},
			{"explicit true", `id_prefix = "proj"` + "\nenforce_session_ownership = true\n", true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				root := t.TempDir()
				writeConfig(t, root, tc.body)
				got, err := ReadFile(root)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				if got.EnforceSessionOwnership != tc.want {
					t.Errorf("EnforceSessionOwnership = %v, want %v", got.EnforceSessionOwnership, tc.want)
				}
			})
		}
	})
}
