package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileConfig mirrors the shape of .quest/config.toml. Fields are
// populated only from the file — environment, flags, and defaults are
// layered on top in Load.
type FileConfig struct {
	IDPrefix string `toml:"id_prefix"`
	// ElevatedRoles lists AGENT_ROLE values that unlock elevated
	// commands. Default when omitted from .quest/config.toml: [] (empty),
	// per spec §Role Gating — workers see only worker commands and no
	// role can reach elevated commands. Distinct from the init template:
	// `quest init` writes elevated_roles = ["planner"] into new configs
	// (spec §quest init), so a freshly-initialized project has "planner"
	// elevated, but a config that drops the line falls back to [].
	ElevatedRoles []string `toml:"elevated_roles"`
}

// ReadFile parses .quest/config.toml under root. A missing file returns
// a zero FileConfig with os.ErrNotExist so callers can distinguish
// "no workspace configured yet" from real parse/permission errors.
// Unknown TOML fields are tolerated and logged at slog.Warn per
// STANDARDS.md §Config File — forward compatibility across versions.
func ReadFile(root string) (FileConfig, error) {
	var cfg FileConfig
	if root == "" {
		return cfg, os.ErrNotExist
	}
	path := filepath.Join(root, ".quest", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, err
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, key := range meta.Undecoded() {
		slog.Warn("unknown config field", "path", path, "key", key.String())
	}
	return cfg, nil
}
