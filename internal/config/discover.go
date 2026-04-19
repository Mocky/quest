package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoWorkspace is returned by DiscoverRoot when no .quest/ directory is
// found walking up from the start directory.
var ErrNoWorkspace = errors.New("no .quest/ workspace found")

// DiscoverRoot walks up from startDir looking for a directory containing
// a .quest/ subdirectory and returns its absolute path. Walk-up stops at
// the first marker — nested quest projects are unsupported per
// quest-spec §Tool Identity.
func DiscoverRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve start dir: %w", err)
	}
	for {
		marker := filepath.Join(dir, ".quest")
		info, err := os.Stat(marker)
		switch {
		case err == nil && info.IsDir():
			return dir, nil
		case err != nil && !os.IsNotExist(err):
			return "", fmt.Errorf("stat %s: %w", marker, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoWorkspace
		}
		dir = parent
	}
}
