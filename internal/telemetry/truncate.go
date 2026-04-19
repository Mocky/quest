package telemetry

import (
	"strconv"
	"unicode/utf8"
)

// Truncate returns s cut to at most max bytes, walking backward from
// the byte boundary to avoid splitting a multi-byte rune. The shared
// helper is used by span attribute values and slog err fields; per
// OBSERVABILITY.md §Standard Field Names and OTEL.md §3.6 / §4.6, this
// is the single source of UTF-8-safe truncation in the codebase.
// Handlers import telemetry.Truncate rather than re-implementing.
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// truncateIDList joins ids with comma separators, capping at max chars.
// When the cut falls mid-list it trims to the last complete ID and
// appends ",...(+N more)" so a trace reader never sees a fragmentary
// ID. Used by RecordPreconditionFailed (OTEL.md §13.3) and
// RecordCycleDetected (§13.4).
func truncateIDList(ids []string, max int) string {
	if len(ids) == 0 || max <= 0 {
		return ""
	}
	var out string
	for i, id := range ids {
		sep := ""
		if i > 0 {
			sep = ","
		}
		if len(out)+len(sep)+len(id) > max {
			remaining := len(ids) - i
			suffix := ",...(+" + strconv.Itoa(remaining) + " more)"
			if len(out)+len(suffix) > max {
				return out
			}
			return out + suffix
		}
		out += sep + id
	}
	return out
}
