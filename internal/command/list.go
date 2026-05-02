package command

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mocky/quest/internal/config"
	"github.com/mocky/quest/internal/errors"
	"github.com/mocky/quest/internal/output"
	"github.com/mocky/quest/internal/store"
	"github.com/mocky/quest/internal/suggest"
	"github.com/mocky/quest/internal/telemetry"
)

// listDefaults mirrors spec §`quest list`:
//   - default columns: id, status, blocked-by, title.
//   - default statuses (when --status is omitted): open, accepted,
//     completed, failed. cancelled is excluded from the default listing.
var (
	listDefaultColumns  = []string{"id", "status", "blocked-by", "title"}
	listDefaultStatuses = []string{"open", "accepted", "completed", "failed"}
)

// Bounded enums for filter flags — used by the unknown-value rejection
// checks and the cli.Suggest "did you mean" hint.
var (
	validListStatuses   = []string{"open", "accepted", "completed", "failed", "cancelled"}
	validListTiers      = []string{"T0", "T1", "T2", "T3", "T4", "T5", "T6"}
	validListSeverities = []string{"critical", "high", "medium", "low"}
	validListColumns    = []string{
		"id", "title", "status", "tier", "role", "severity",
		"tags", "parent", "blocked-by", "children",
	}
)

// List handles `quest list [flags]`. The filter flags compose AND
// across dimensions and OR within a dimension (except --tag, which
// is AND within as well). See quest-spec.md §Queries.
func List(ctx context.Context, cfg config.Config, s store.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	_ = stdin

	filter, columns, err := parseListFlags(stderr, args)
	if stderrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	if filter.Ready {
		ctx2, end := telemetry.StoreSpan(ctx, "quest.store.traverse")
		defer func() { end(err) }()
		ctx = ctx2
	}

	tasks, err := s.ListTasks(ctx, filter)
	if err != nil {
		return err
	}
	telemetry.RecordQueryResult(ctx, "list", len(tasks), telemetry.QueryFilter{
		Status:   filter.Statuses,
		Role:     filter.Roles,
		Tier:     filter.Tiers,
		Severity: filter.Severities,
		Ready:    filter.Ready,
	})

	enriched, err := enrichForColumns(ctx, s, tasks, columns)
	if err != nil {
		return err
	}

	if cfg.Output.Text {
		return emitListText(stdout, columns, enriched)
	}
	return emitListJSON(stdout, columns, enriched)
}

// parseListFlags builds the Filter + column projection from args. Each
// enum flag is a fs.Func so multiple occurrences accumulate and each
// accepts a comma-separated list that is split at this layer. Unknown
// values for --status, --tier, --severity, --columns are rejected here
// so the SQL builder in the store can assume a clean filter.
func parseListFlags(stderr io.Writer, args []string) (store.Filter, []string, error) {
	var (
		filter          store.Filter
		statusesSet     bool
		columnsFlagRaw  []string
		columnsProvided bool
	)
	fs := newFlagSet("list", "[flags]",
		"List tasks with filtering.")
	fs.SetOutput(stderr)

	addCSV := func(into *[]string, markSet *bool) func(string) error {
		return func(v string) error {
			if markSet != nil {
				*markSet = true
			}
			for _, part := range strings.Split(v, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				*into = append(*into, part)
			}
			return nil
		}
	}

	fs.Func("status", "STATUSES (comma-separated; repeatable)",
		addCSV(&filter.Statuses, &statusesSet))
	fs.Func("parent", "IDS (comma-separated; repeatable)",
		addCSV(&filter.Parents, nil))
	fs.Func("tag", "TAGS (comma-separated AND; repeatable AND)",
		addCSV(&filter.Tags, nil))
	fs.Func("role", "ROLES (comma-separated; repeatable)",
		addCSV(&filter.Roles, nil))
	fs.Func("tier", "TIERS (comma-separated; repeatable)",
		addCSV(&filter.Tiers, nil))
	fs.Func("severity", "SEVERITIES (comma-separated; repeatable)",
		addCSV(&filter.Severities, nil))
	fs.Func("columns", "COLS (comma-separated)", func(v string) error {
		columnsProvided = true
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			columnsFlagRaw = append(columnsFlagRaw, part)
		}
		return nil
	})
	fs.BoolVar(&filter.Ready, "ready", false, "only tasks whose next transition has no unmet preconditions")

	if err := fs.Parse(args); err != nil {
		if stderrors.Is(err, flag.ErrHelp) {
			return store.Filter{}, nil, err
		}
		return store.Filter{}, nil, fmt.Errorf("list: %s: %w", err.Error(), errors.ErrUsage)
	}
	if fs.NArg() > 0 {
		return store.Filter{}, nil, fmt.Errorf("list: unexpected positional arguments: %w", errors.ErrUsage)
	}

	if err := rejectUnknown("status", filter.Statuses, validListStatuses); err != nil {
		return store.Filter{}, nil, err
	}
	if err := rejectUnknown("tier", filter.Tiers, validListTiers); err != nil {
		return store.Filter{}, nil, err
	}
	if err := rejectUnknown("severity", filter.Severities, validListSeverities); err != nil {
		return store.Filter{}, nil, err
	}

	columns := listDefaultColumns
	if columnsProvided {
		if err := rejectUnknown("column", columnsFlagRaw, validListColumns); err != nil {
			return store.Filter{}, nil, err
		}
		columns = columnsFlagRaw
	}

	// Default status filter (quest-spec.md §Queries > quest list > --status).
	if !statusesSet {
		filter.Statuses = append([]string{}, listDefaultStatuses...)
	}
	filter.Columns = columns
	return filter, columns, nil
}

// rejectUnknown wraps ErrUsage with the spec-pinned "did you mean"
// hint when any value is not in the valid set. The valid list is
// included so the caller can machine-read the enumeration.
func rejectUnknown(kind string, provided, valid []string) error {
	validSet := make(map[string]bool, len(valid))
	for _, v := range valid {
		validSet[v] = true
	}
	for _, p := range provided {
		if validSet[p] {
			continue
		}
		msg := fmt.Sprintf("unknown %s %q", kind, p)
		if hint := suggest.Closest(p, valid); hint != "" {
			msg += fmt.Sprintf("; did you mean %q?", hint)
		}
		msg += fmt.Sprintf("; valid: %s", strings.Join(valid, ","))
		return fmt.Errorf("list: %s: %w", msg, errors.ErrUsage)
	}
	return nil
}

// listRow carries per-task data in an ordered map so JSON / text
// emission honor the --columns order.
type listRow struct {
	cells map[string]any
}

// enrichForColumns fetches the auxiliary rows (tags, blocked-by edges,
// children) for each task based on the requested columns. Skipped when
// a column is not requested so a plain --columns=id,status query stays
// a single table scan.
func enrichForColumns(ctx context.Context, s store.Store, tasks []store.Task, columns []string) ([]listRow, error) {
	needTags := columnRequested(columns, "tags")
	needBlockedBy := columnRequested(columns, "blocked-by")
	needChildren := columnRequested(columns, "children")

	rows := make([]listRow, 0, len(tasks))
	for _, t := range tasks {
		row := listRow{cells: map[string]any{}}
		for _, c := range columns {
			switch c {
			case "id":
				row.cells[c] = t.ID
			case "title":
				row.cells[c] = t.Title
			case "status":
				row.cells[c] = t.Status
			case "tier":
				row.cells[c] = nullString(t.Tier)
			case "role":
				row.cells[c] = nullString(t.Role)
			case "severity":
				row.cells[c] = nullString(t.Severity)
			case "parent":
				row.cells[c] = nullString(t.Parent)
			case "tags":
				if needTags {
					tags, err := s.GetTags(ctx, t.ID)
					if err != nil {
						return nil, err
					}
					row.cells[c] = tags
				}
			case "blocked-by":
				if needBlockedBy {
					ids, err := blockedByIDs(ctx, s, t.ID)
					if err != nil {
						return nil, err
					}
					row.cells[c] = ids
				}
			case "children":
				if needChildren {
					ids, err := childIDs(ctx, s, t.ID)
					if err != nil {
						return nil, err
					}
					row.cells[c] = ids
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func columnRequested(columns []string, name string) bool {
	for _, c := range columns {
		if c == name {
			return true
		}
	}
	return false
}

// blockedByIDs returns the ID list of `blocked-by` targets for id, in
// alphabetical order so the JSON array is stable across calls.
func blockedByIDs(ctx context.Context, s store.Store, id string) ([]string, error) {
	deps, err := s.GetDependencies(ctx, id)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, d := range deps {
		if d.LinkType == "blocked-by" {
			ids = append(ids, d.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// childIDs returns the direct children of id in ID-ascending order.
func childIDs(ctx context.Context, s store.Store, id string) ([]string, error) {
	children, err := s.GetChildren(ctx, id)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// emitListJSON writes the row array with keys in --columns order.
// encoding/json serializes struct fields in definition order but map
// keys alphabetically, so we buffer each row manually to honor the
// requested column order per spec §quest list "Field order ... matches
// the order of --columns (or the default-columns order)".
func emitListJSON(w io.Writer, columns []string, rows []listRow) error {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, row := range rows {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('{')
		for j, c := range columns {
			if j > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(c)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			val, ok := row.cells[c]
			if !ok {
				val = nil
			}
			val = normalizeCell(c, val)
			vb, err := json.Marshal(val)
			if err != nil {
				return err
			}
			buf.Write(vb)
		}
		buf.WriteByte('}')
	}
	buf.WriteByte(']')
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// normalizeCell applies the spec §quest list row-shape rules: nullable
// scalars emit JSON null when unset (never ""); collection columns
// emit [] when unset (never null, never missing).
func normalizeCell(col string, val any) any {
	switch col {
	case "tags", "blocked-by", "children":
		if val == nil {
			return []string{}
		}
		if s, ok := val.([]string); ok && s == nil {
			return []string{}
		}
	}
	return val
}

// listTitleMaxWidth caps the title column's rendered width at the
// task-title byte cap (spec §Field constraints). Anything past 128 is
// pure padding, so we never allocate more regardless of terminal size.
const listTitleMaxWidth = 128

// emitListText renders rows as a plain-text table per spec §Text-mode
// formatting. Helper columns size to their content; the title column
// is allocated the remainder of the terminal width on a TTY (clamped
// to 128) or is unbounded on a non-TTY / unknown-width stdout. The
// final column of every row is emitted without trailing whitespace.
// Text mode is explicitly non-contractual (STANDARDS.md §CLI Surface
// Versioning) -- these rules describe rendering intent and may evolve.
func emitListText(w io.Writer, columns []string, rows []listRow) error {
	return emitListTextWithWidth(w, columns, rows, output.TerminalWidth(w))
}

// emitListTextWithWidth is the rendering core. termWidth is the
// effective stdout column count (0 means "no TTY / unknown", which
// selects the unbounded-title branch). Split out so tests can inject a
// width without depending on the real stdout's TTY-ness.
func emitListTextWithWidth(w io.Writer, columns []string, rows []listRow, termWidth int) error {
	headers := make([]string, len(columns))
	for i, c := range columns {
		headers[i] = strings.ToUpper(c)
	}
	// Render every cell up front so content widths are measured on the
	// exact string the row will emit (array columns joined, nulls as
	// empty). This also lets us skip re-rendering during emission.
	cells := make([][]string, len(rows))
	for i, row := range rows {
		cells[i] = make([]string, len(columns))
		for j, c := range columns {
			cells[i][j] = formatTextCell(row.cells[c])
		}
	}
	widths := make([]int, len(columns))
	titleIdx := -1
	for i, c := range columns {
		if c == "title" {
			titleIdx = i
		}
		widths[i] = len(headers[i])
		for r := range rows {
			if n := len(cells[r][i]); n > widths[i] {
				widths[i] = n
			}
		}
	}
	// Title allocation: on a TTY with known width, the title column
	// gets whatever is left after helpers and gutters, clamped to the
	// 128-byte field cap. If helpers alone overflow the terminal
	// (budget <= 0), leave title at its content width and let the row
	// overflow — narrow terminals are an edge case per spec.
	if titleIdx >= 0 && termWidth > 0 {
		helperTotal := 0
		for i, wdt := range widths {
			if i == titleIdx {
				continue
			}
			helperTotal += wdt
		}
		gutters := 2 * (len(columns) - 1)
		budget := termWidth - helperTotal - gutters
		if budget > listTitleMaxWidth {
			budget = listTitleMaxWidth
		}
		if budget > 0 {
			widths[titleIdx] = budget
		}
	}
	var buf bytes.Buffer
	writeListTextRow(&buf, headers, widths)
	for _, row := range cells {
		writeListTextRow(&buf, row, widths)
	}
	writeListCountFooter(&buf, len(rows))
	_, err := w.Write(buf.Bytes())
	return err
}

// writeListCountFooter appends the spec §quest list count footer: a
// blank line followed by `N tasks` (or `1 task` when exactly one).
// Text-mode only; JSON output is unchanged so the agent contract stays
// stable and agents keep reading the array length directly.
func writeListCountFooter(buf *bytes.Buffer, n int) {
	buf.WriteByte('\n')
	if n == 1 {
		buf.WriteString("1 task\n")
		return
	}
	fmt.Fprintf(buf, "%d tasks\n", n)
}

// writeListTextRow emits one row with a two-space gutter between
// columns. Every column except the last is right-padded to its
// computed width for alignment; the last column is emitted at its
// natural width (after any needed truncation) so copy-paste does not
// pick up invisible trailing whitespace.
func writeListTextRow(buf *bytes.Buffer, row []string, widths []int) {
	last := len(row) - 1
	for i, cell := range row {
		if i > 0 {
			buf.WriteString("  ")
		}
		cell = truncCell(cell, widths[i])
		if i == last {
			buf.WriteString(cell)
		} else {
			buf.WriteString(padRight(cell, widths[i]))
		}
	}
	buf.WriteByte('\n')
}

// truncCell enforces the truncation rule from spec §Text-mode
// formatting: cells longer than w are cut to w-3 and suffixed with
// "...". Widths below 3 have no room for the suffix, so they truncate
// without it -- a defensive tail for pathologically narrow columns.
func truncCell(s string, w int) string {
	if len(s) <= w {
		return s
	}
	if w < 3 {
		return s[:w]
	}
	return s[:w-3] + "..."
}

func formatTextCell(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case *string:
		if val == nil {
			return ""
		}
		return *val
	case []string:
		return strings.Join(val, ",")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
