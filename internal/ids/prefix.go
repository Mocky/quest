package ids

import (
	"fmt"
	"regexp"
)

var prefixChars = regexp.MustCompile(`^[a-z0-9]+$`)

var reservedPrefixes = map[string]struct{}{
	"ref": {},
}

// ValidatePrefix enforces quest-spec §Prefix validation: 2-8 characters,
// lowercase letters and digits only, must start with a letter, not a
// reserved value. The returned error names which rule failed.
func ValidatePrefix(prefix string) error {
	if n := len(prefix); n < 2 || n > 8 {
		return fmt.Errorf("prefix %q must be 2-8 characters", prefix)
	}
	if !prefixChars.MatchString(prefix) {
		return fmt.Errorf("prefix %q: lowercase letters and digits only", prefix)
	}
	if c := prefix[0]; c < 'a' || c > 'z' {
		return fmt.Errorf("prefix %q must start with a letter", prefix)
	}
	if _, ok := reservedPrefixes[prefix]; ok {
		return fmt.Errorf("prefix %q is a reserved prefix", prefix)
	}
	return nil
}
