package command

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
)

// showTextClock is the wall-clock source used to compute relative-time
// suffixes on the `started` / `completed` metadata rows. Tests stub it
// via var-replace so golden output is deterministic.
var showTextClock = func() time.Time { return time.Now().UTC() }

// emitShowText renders resp per quest-spec §quest show --format text.
// Width resolution: min(TerminalWidth(w), 100) when w is a TTY, 80
// when piped. Prose sections wrap at width; row-oriented sections
// (Dependencies, PRs, History) and the metadata cluster overflow
// without truncation per spec §Wrap rules.
func emitShowText(w io.Writer, resp showResponse) error {
	width := resolveWrapWidth(w)
	var buf strings.Builder

	buf.WriteString(output.TaskRefLine(resp.ID, resp.Status, resp.Type, resp.Title))
	buf.WriteByte('\n')

	writeMetadataCluster(&buf, resp)
	writeSections(&buf, resp, width)

	_, err := w.Write([]byte(buf.String()))
	return err
}

// resolveWrapWidth picks the wrap column based on spec §Wrap rules.
// A TTY is capped at 100; a pipe (any non-TTY) renders at 80.
func resolveWrapWidth(w io.Writer) int {
	if tw := output.TerminalWidth(w); tw > 0 {
		if tw > 100 {
			return 100
		}
		return tw
	}
	return 80
}

// metaRow is one {key, value} pair in the metadata cluster. Rows are
// computed up-front so the widest-key padding can be resolved in a
// single pass before writing.
type metaRow struct{ key, value string }

// writeMetadataCluster emits the 4-space-indented key/value rows that
// sit directly under the header line per spec §Metadata cluster.
// Values are column-aligned to widestKey+2 spaces. No trailing blank
// line — writeSections inserts the single separator before its first
// heading.
func writeMetadataCluster(buf *strings.Builder, resp showResponse) {
	rows := buildMetadataRows(resp)
	if len(rows) == 0 {
		return
	}
	widest := 0
	for _, r := range rows {
		if len(r.key) > widest {
			widest = len(r.key)
		}
	}
	for _, r := range rows {
		buf.WriteString("    ")
		buf.WriteString(r.key)
		buf.WriteString(strings.Repeat(" ", widest-len(r.key)+2))
		buf.WriteString(r.value)
		buf.WriteByte('\n')
	}
}

// buildMetadataRows produces the ordered list of rows the spec pins:
// parent, tags, exec, metadata, started, completed. Each row is
// omitted when its source field is unset; exec is skipped when all
// three slots (tier/role/session) are nil since the spec's
// "always-rendered" guarantee rests on tier being set.
func buildMetadataRows(resp showResponse) []metaRow {
	var rows []metaRow
	if resp.Parent != nil {
		rows = append(rows, metaRow{
			key:   "parent",
			value: output.TaskRefLine(resp.Parent.ID, resp.Parent.Status, resp.Parent.Type, resp.Parent.Title),
		})
	}
	if len(resp.Tags) > 0 {
		rows = append(rows, metaRow{key: "tags", value: strings.Join(resp.Tags, ", ")})
	}
	if exec := formatExec(resp.Tier, resp.Role, resp.OwnerSession); exec != "" {
		rows = append(rows, metaRow{key: "exec", value: exec})
	}
	if len(resp.Metadata) > 0 {
		rows = append(rows, metaRow{key: "metadata", value: formatMetadata(resp.Metadata)})
	}
	if resp.StartedAt != nil {
		rows = append(rows, metaRow{key: "started", value: formatTimestampWithRel(*resp.StartedAt)})
	}
	if resp.CompletedAt != nil {
		rows = append(rows, metaRow{key: "completed", value: formatTimestampWithRel(*resp.CompletedAt)})
	}
	return rows
}

// formatExec joins {tier, role, session} with " - ". Trailing nulls
// drop entirely; mid-string nulls render as "—" (em-dash) per spec.
// Returns "" when every slot is nil so the caller skips the row.
func formatExec(tier, role, session *string) string {
	parts := []*string{tier, role, session}
	last := -1
	for i, p := range parts {
		if p != nil && *p != "" {
			last = i
		}
	}
	if last < 0 {
		return ""
	}
	out := make([]string, 0, last+1)
	for i := 0; i <= last; i++ {
		if parts[i] != nil && *parts[i] != "" {
			out = append(out, *parts[i])
		} else {
			out = append(out, "—")
		}
	}
	return strings.Join(out, " - ")
}

// formatMetadata renders the metadata map as sorted `key=value` pairs
// joined with ", ". Scalar values render as themselves (string, int,
// bool); non-scalar values (arrays, objects) render as compact JSON so
// a single row always fits on one line even when values nest.
func formatMetadata(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+formatMetaValue(m[k]))
	}
	return strings.Join(parts, ", ")
}

// formatMetaValue stringifies one metadata value: strings as-is,
// integer-valued floats as integers (JSON numbers decode as float64,
// so the most common "priority=3" case reads naturally), bools as
// `true`/`false`, nil as `null`, everything else as compact JSON.
func formatMetaValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// formatTimestamp parses an RFC3339 string and re-formats to the
// spec's minute-precision UTC shape `YYYY-MM-DD HH:MMZ`. On parse
// failure the input is returned verbatim so bad store data is still
// readable (though clearly wrong).
func formatTimestamp(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

// formatTimestampWithRel formats the timestamp and appends a
// parenthesized relative suffix computed against showTextClock().
// Used for `started` and `completed` per spec.
func formatTimestampWithRel(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	stamp := t.UTC().Format("2006-01-02 15:04Z")
	rel := relativeTime(t.UTC(), showTextClock())
	return stamp + "  (" + rel + ")"
}

// relativeTime returns a "(N{d,h,m} ago)" / "(just now)" string per
// spec's informal granularity — within a minute → just now; minutes;
// hours; days. Negative deltas (future timestamps from clock skew)
// clamp to just now.
func relativeTime(when, now time.Time) string {
	diff := now.Sub(when)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff/time.Minute))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(diff/(24*time.Hour)))
	}
}

// writeSections emits each section in spec-pinned order, separated
// by a single blank line from what came before. sections check their
// own presence conditions; sections whose condition fails emit
// nothing (no heading, no body, no blank line).
func writeSections(buf *strings.Builder, resp showResponse, width int) {
	write := func(heading string, emitBody func()) {
		buf.WriteByte('\n')
		buf.WriteString(heading)
		buf.WriteByte('\n')
		emitBody()
	}

	if resp.Description != "" {
		write("Description", func() { writeProse(buf, resp.Description, "    ", width) })
	}
	if resp.Context != "" {
		write("Context", func() { writeProse(buf, resp.Context, "    ", width) })
	}
	if resp.AcceptanceCriteria != nil {
		write("Acceptance criteria", func() { writeProse(buf, *resp.AcceptanceCriteria, "    ", width) })
	}
	if len(resp.Dependencies) > 0 {
		write("Dependencies", func() { writeDependencies(buf, resp.Dependencies) })
	}
	if len(resp.Notes) > 0 {
		write(fmt.Sprintf("Notes (%d)", len(resp.Notes)), func() { writeNotes(buf, resp.Notes, width) })
	}
	if len(resp.PRs) > 0 {
		write("PRs", func() { writePRs(buf, resp.PRs) })
	}
	if resp.Handoff != nil {
		write(handoffHeading(resp), func() { writeProse(buf, *resp.Handoff, "    ", width) })
	}
	// Debrief renders when the task has one OR is in a completed
	// terminal state (spec pin). Both `complete` (legacy) and
	// `completed` (migration 002) count so the renderer survives
	// the transition window without a hard-coded single status.
	debriefDue := resp.Debrief != nil || resp.Status == "completed" || resp.Status == "complete"
	if debriefDue {
		write("Debrief", func() {
			body := "(missing)"
			if resp.Debrief != nil {
				body = *resp.Debrief
			}
			writeProse(buf, body, "    ", width)
		})
	}
	if resp.History != nil {
		hist := *resp.History
		write(fmt.Sprintf("History (%d)", len(hist)), func() { writeHistory(buf, hist) })
	}
}

// handoffHeading builds the `Handoff (session, timestamp)` suffix
// per spec. Missing session/timestamp gracefully degrade — both nil
// collapses to a bare `Handoff` heading.
func handoffHeading(resp showResponse) string {
	var parens []string
	if resp.HandoffSession != nil {
		parens = append(parens, *resp.HandoffSession)
	}
	if resp.HandoffWrittenAt != nil {
		parens = append(parens, formatTimestamp(*resp.HandoffWrittenAt))
	}
	if len(parens) == 0 {
		return "Handoff"
	}
	return "Handoff (" + strings.Join(parens, ", ") + ")"
}

// writeProse writes a paragraph body wrapped to width columns at a
// uniform indent. Paragraph breaks (blank lines in the source) survive
// as blank output lines. Leading/trailing whitespace on the body is
// preserved for paragraph splitting but each paragraph's words are
// rejoined cleanly (no double-space artifacts).
func writeProse(buf *strings.Builder, body, indent string, width int) {
	innerWidth := width - len(indent)
	if innerWidth < 1 {
		innerWidth = 1
	}
	paragraphs := strings.Split(body, "\n\n")
	for i, p := range paragraphs {
		if i > 0 {
			buf.WriteByte('\n')
		}
		lines := wrapLine(p, innerWidth)
		for _, line := range lines {
			buf.WriteString(indent)
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
}

// wrapLine word-wraps text to width columns on whitespace. Returns
// one entry per output line; an empty or whitespace-only input yields
// a single empty line so the caller emits the paragraph as a blank
// indented row.
func wrapLine(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var (
		lines []string
		cur   strings.Builder
	)
	for _, w := range words {
		switch {
		case cur.Len() == 0:
			cur.WriteString(w)
		case cur.Len()+1+len(w) > width:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
		default:
			cur.WriteByte(' ')
			cur.WriteString(w)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// writeDependencies emits the Dependencies section body. The
// link_type column pads to the widest link_type in the list so the
// task-reference cluster (the interesting information) lines up
// vertically. Rows are not wrapped per spec.
func writeDependencies(buf *strings.Builder, deps []store.Dependency) {
	widest := 0
	for _, d := range deps {
		if len(d.LinkType) > widest {
			widest = len(d.LinkType)
		}
	}
	for _, d := range deps {
		buf.WriteString("    ")
		buf.WriteString(d.LinkType)
		buf.WriteString(strings.Repeat(" ", widest-len(d.LinkType)+2))
		buf.WriteString(output.TaskRefLine(d.ID, d.Status, d.Type, d.Title))
		buf.WriteByte('\n')
	}
}

// writeNotes renders each note as `{ts}  {body}` with continuation
// lines hung at the body column per spec. The body column is
// 4-space-indent + timestamp-width (17) + 2-space-gutter = 23.
func writeNotes(buf *strings.Builder, notes []store.Note, width int) {
	const indent = "    "
	const tsWidth = 17 // "YYYY-MM-DD HH:MMZ"
	hangingIndent := indent + strings.Repeat(" ", tsWidth+2)
	innerWidth := width - len(hangingIndent)
	if innerWidth < 1 {
		innerWidth = 1
	}
	for _, n := range notes {
		ts := formatTimestamp(n.Timestamp)
		lines := wrapLine(n.Body, innerWidth)
		buf.WriteString(indent)
		buf.WriteString(ts)
		buf.WriteString("  ")
		buf.WriteString(lines[0])
		buf.WriteByte('\n')
		for _, l := range lines[1:] {
			buf.WriteString(hangingIndent)
			buf.WriteString(l)
			buf.WriteByte('\n')
		}
	}
}

// writePRs renders each PR as `{url}  ({timestamp})`. Long URLs
// overflow — spec forbids truncation in show output.
func writePRs(buf *strings.Builder, prs []store.PR) {
	for _, p := range prs {
		buf.WriteString("    ")
		buf.WriteString(p.URL)
		buf.WriteString("  (")
		buf.WriteString(formatTimestamp(p.AddedAt))
		buf.WriteString(")\n")
	}
}

// writeHistory renders the History block per spec §History layout.
// The role/session pair and action columns both pad to the widest
// entry in the section so details align vertically. Detail formatters
// live in historyDetail, keyed on action.
func writeHistory(buf *strings.Builder, history []historyEntry) {
	type row struct{ ts, rs, action, detail string }
	rows := make([]row, 0, len(history))
	widestRS := 0
	widestAction := 0
	for _, h := range history {
		role := "-"
		sess := "-"
		if h.Role != nil && *h.Role != "" {
			role = *h.Role
		}
		if h.Session != nil && *h.Session != "" {
			sess = *h.Session
		}
		rs := role + "/" + sess
		r := row{
			ts:     formatTimestamp(h.Timestamp),
			rs:     rs,
			action: h.Action,
			detail: historyDetail(h),
		}
		if len(rs) > widestRS {
			widestRS = len(rs)
		}
		if len(h.Action) > widestAction {
			widestAction = len(h.Action)
		}
		rows = append(rows, r)
	}
	for _, r := range rows {
		buf.WriteString("    ")
		buf.WriteString(r.ts)
		buf.WriteString("  ")
		buf.WriteString(r.rs)
		buf.WriteString(strings.Repeat(" ", widestRS-len(r.rs)+2))
		buf.WriteString(r.action)
		if r.detail != "" {
			buf.WriteString(strings.Repeat(" ", widestAction-len(r.action)+2))
			buf.WriteString(r.detail)
		}
		buf.WriteByte('\n')
	}
}

// historyDetail is the per-action detail formatter the spec pins in
// the History-layout table. Actions that carry no inline detail
// (accepted, completed, failed, note_added, handoff_set) return "".
// Actions with optional detail (cancelled, reset) return "" when the
// source field is unset so the renderer emits no trailing spaces.
func historyDetail(h historyEntry) string {
	switch h.Action {
	case "created":
		return formatCreatedPayload(h.Payload)
	case "cancelled", "reset":
		if r, _ := h.Payload["reason"].(string); r != "" {
			return `"` + r + `"`
		}
		return ""
	case "moved":
		oldID, _ := h.Payload["old_id"].(string)
		newID, _ := h.Payload["new_id"].(string)
		return oldID + " -> " + newID
	case "pr_added":
		if u, _ := h.Payload["url"].(string); u != "" {
			return u
		}
		return ""
	case "field_updated":
		return formatFieldUpdatedPayload(h.Payload)
	case "linked", "unlinked":
		lt, _ := h.Payload["link_type"].(string)
		t, _ := h.Payload["target"].(string)
		if lt == "" && t == "" {
			return ""
		}
		return lt + " " + t
	case "tagged", "untagged":
		if tag, _ := h.Payload["tag"].(string); tag != "" {
			return tag
		}
		return ""
	}
	return ""
}

// formatCreatedPayload renders non-default create-time fields as
// space-joined `key=value` pairs. Lists render as `[a,b,c]` per the
// spec example. Keys are sorted alphabetically for stable output.
func formatCreatedPayload(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+formatCreatedValue(payload[k]))
	}
	return strings.Join(parts, " ")
}

// formatCreatedValue stringifies one payload value for the `created`
// detail column. Lists collapse to `[a,b]`; everything else defers to
// formatMetaValue so scalars render in the same shape as the metadata
// cluster.
func formatCreatedValue(v any) string {
	if lst, ok := v.([]any); ok {
		items := make([]string, 0, len(lst))
		for _, el := range lst {
			items = append(items, formatMetaValue(el))
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	return formatMetaValue(v)
}

// formatFieldUpdatedPayload renders `field: old -> new` tuples joined
// with ", ". The underlying payload shape is `{fields: {name: {from,
// to}}}` per the `field_updated` history contract.
func formatFieldUpdatedPayload(payload map[string]any) string {
	fields, ok := payload["fields"].(map[string]any)
	if !ok || len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		m, _ := fields[k].(map[string]any)
		from := formatMetaValue(m["from"])
		to := formatMetaValue(m["to"])
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", k, from, to))
	}
	return strings.Join(parts, ", ")
}
