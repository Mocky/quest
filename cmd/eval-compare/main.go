// Command eval-compare reads internal/eval/benchmarks.jsonl and prints a
// side-by-side comparison of the current prompt artifact's runs against the
// most recent previous prompt SHA. Per-SHA aggregation (median across N runs,
// pass rate as fraction) keeps a single noisy run from masquerading as signal.
//
// Run via `make eval-compare`.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/mocky/quest/internal/eval"
)

func main() {
	// Read both the canonical (committed) benchmarks and the local (gitignored)
	// scratch log so the table reflects in-progress runs that haven't yet been
	// promoted. Scratch entries that match the current prompt SHA are the
	// "current" cohort agents are evaluating.
	bench, err := readBoth()
	if err != nil {
		die("read benchmark logs: %v", err)
	}
	if len(bench) == 0 {
		fmt.Println("no benchmark entries yet — run `make test-eval` (or `make eval-changed`) first")
		return
	}
	entries := bench

	type key struct{ scenario, model, promptPath string }
	groups := map[key][]eval.BenchmarkEntry{}
	for _, e := range entries {
		groups[key{e.Scenario, e.Model, e.PromptPath}] = append(groups[key{e.Scenario, e.Model, e.PromptPath}], e)
	}

	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].promptPath != keys[j].promptPath {
			return keys[i].promptPath < keys[j].promptPath
		}
		if keys[i].scenario != keys[j].scenario {
			return keys[i].scenario < keys[j].scenario
		}
		return keys[i].model < keys[j].model
	})

	for _, k := range keys {
		runs := groups[k]
		sort.Slice(runs, func(i, j int) bool { return runs[i].Timestamp < runs[j].Timestamp })
		currentSHA, err := eval.PromptSHA(k.promptPath)
		if err != nil {
			warn("hash %s: %v", k.promptPath, err)
			continue
		}
		curr := filterSHA(runs, currentSHA)
		prevSHA := mostRecentDifferentSHA(runs, currentSHA)
		prev := filterSHA(runs, prevSHA)
		printComparison(k.scenario, k.model, k.promptPath, currentSHA, curr, prevSHA, prev)
	}
}

// readBoth concatenates the canonical benchmarks log and the scratch log
// (in that order). Scratch is the harness's working log; benchmarks is the
// committed record. A run that's been promoted will appear in benchmarks
// only (scratch is truncated on promote), so concat without dedup is safe.
func readBoth() ([]eval.BenchmarkEntry, error) {
	benchPath, err := eval.BenchmarkLogPath()
	if err != nil {
		return nil, err
	}
	scratchPath, err := eval.ScratchLogPath()
	if err != nil {
		return nil, err
	}
	bench, err := eval.ReadBenchmarks(benchPath)
	if err != nil {
		return nil, err
	}
	scratch, err := eval.ReadBenchmarks(scratchPath)
	if err != nil {
		return nil, err
	}
	return append(bench, scratch...), nil
}

// filterSHA returns the subset of entries whose PromptSHA equals sha.
// An empty sha returns nil.
func filterSHA(entries []eval.BenchmarkEntry, sha string) []eval.BenchmarkEntry {
	if sha == "" {
		return nil
	}
	var out []eval.BenchmarkEntry
	for _, e := range entries {
		if e.PromptSHA == sha {
			out = append(out, e)
		}
	}
	return out
}

// mostRecentDifferentSHA scans entries from latest to earliest and returns
// the first SHA that doesn't match currentSHA. Empty string if no prior SHA
// exists.
func mostRecentDifferentSHA(entries []eval.BenchmarkEntry, currentSHA string) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].PromptSHA != currentSHA {
			return entries[i].PromptSHA
		}
	}
	return ""
}

func shortSHA(sha string) string {
	if len(sha) < 7 {
		return sha
	}
	return sha[:7]
}

// median returns the median of vals (returns 0 for empty input).
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func extract(runs []eval.BenchmarkEntry, fn func(eval.BenchmarkEntry) float64) []float64 {
	out := make([]float64, len(runs))
	for i, r := range runs {
		out[i] = fn(r)
	}
	return out
}

func passes(runs []eval.BenchmarkEntry) int {
	n := 0
	for _, r := range runs {
		if r.Pass {
			n++
		}
	}
	return n
}

// pctChange formats curr relative to prev as a percent delta. Returns "—" if
// prev is zero (undefined ratio).
func pctChange(curr, prev float64) string {
	if prev == 0 {
		return "—"
	}
	delta := (curr - prev) / prev * 100
	sign := ""
	if delta >= 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.1f%%", sign, delta)
}

// ppChange formats a fraction (0..1) delta in percentage points.
func ppChange(curr, prev float64) string {
	delta := (curr - prev) * 100
	sign := ""
	if delta >= 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.0fpp", sign, delta)
}

type row struct{ name, curr, prev, delta string }

func printComparison(scenario, model, promptPath, currSHA string, currRuns []eval.BenchmarkEntry, prevSHA string, prevRuns []eval.BenchmarkEntry) {
	fmt.Printf("\n%s  scenario=%s  model=%s\n", promptPath, scenario, model)

	if len(currRuns) == 0 {
		fmt.Printf("  current prompt (%s) has no recorded runs — run `make test-eval`\n", shortSHA(currSHA))
		return
	}

	prevHeader := "PREVIOUS (none)"
	hasPrev := len(prevRuns) > 0
	if hasPrev {
		prevHeader = fmt.Sprintf("PREVIOUS (%s, %d runs)", shortSHA(prevSHA), len(prevRuns))
	}
	currHeader := fmt.Sprintf("CURRENT (%s, %d runs)", shortSHA(currSHA), len(currRuns))

	rows := []row{}

	// prompt_tokens — single value per SHA, both entries report it.
	currTokens := float64(currRuns[0].PromptTokens)
	var prevTokens float64
	if hasPrev {
		prevTokens = float64(prevRuns[0].PromptTokens)
	}
	rows = append(rows, makeNumeric("prompt_tokens", currTokens, prevTokens, hasPrev, func(v float64) string {
		return fmt.Sprintf("%.0f", v)
	}))

	// pass_rate — fraction with explicit numerator/denominator + pp delta.
	cp, ct := passes(currRuns), len(currRuns)
	pp, pt := passes(prevRuns), len(prevRuns)
	cRate := float64(cp) / float64(ct)
	currStr := fmt.Sprintf("%d/%d (%.0f%%)", cp, ct, 100*cRate)
	prevStr, deltaStr := "—", "—"
	if hasPrev && pt > 0 {
		pRate := float64(pp) / float64(pt)
		prevStr = fmt.Sprintf("%d/%d (%.0f%%)", pp, pt, 100*pRate)
		deltaStr = ppChange(cRate, pRate)
	}
	rows = append(rows, row{"pass_rate", currStr, prevStr, deltaStr})

	// Median rows.
	rows = append(rows, mkMedian("median_turns", currRuns, prevRuns, hasPrev,
		func(e eval.BenchmarkEntry) float64 { return float64(e.Turns) },
		func(v float64) string { return fmt.Sprintf("%.1f", v) }))

	rows = append(rows, mkMedian("median_cost", currRuns, prevRuns, hasPrev,
		func(e eval.BenchmarkEntry) float64 { return e.CostUSD },
		func(v float64) string { return fmt.Sprintf("$%.4f", v) }))

	rows = append(rows, mkMedian("median_output", currRuns, prevRuns, hasPrev,
		func(e eval.BenchmarkEntry) float64 { return float64(e.Output) },
		func(v float64) string { return fmt.Sprintf("%.0f", v) }))

	rows = append(rows, mkMedian("median_cache_r", currRuns, prevRuns, hasPrev,
		func(e eval.BenchmarkEntry) float64 { return float64(e.CacheRead) },
		func(v float64) string { return fmt.Sprintf("%.0f", v) }))

	// Render.
	const nameW, colW = 18, 30
	fmt.Printf("  %-*s %-*s %-*s %s\n", nameW, "", colW, currHeader, colW, prevHeader, "Δ")
	for _, r := range rows {
		fmt.Printf("  %-*s %-*s %-*s %s\n", nameW, r.name, colW, r.curr, colW, r.prev, r.delta)
	}
}

func makeNumeric(name string, curr, prev float64, hasPrev bool, fmtVal func(float64) string) row {
	r := row{name: name, curr: fmtVal(curr)}
	if hasPrev {
		r.prev = fmtVal(prev)
		r.delta = pctChange(curr, prev)
	} else {
		r.prev = "—"
		r.delta = "—"
	}
	return r
}

func mkMedian(name string, currRuns, prevRuns []eval.BenchmarkEntry, hasPrev bool, get func(eval.BenchmarkEntry) float64, fmtVal func(float64) string) row {
	curr := median(extract(currRuns, get))
	var prev float64
	if hasPrev {
		prev = median(extract(prevRuns, get))
	}
	return makeNumeric(name, curr, prev, hasPrev, fmtVal)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}
