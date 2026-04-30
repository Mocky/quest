// Command eval-promote moves benchmark entries from the gitignored scratch
// log into the committed benchmarks log, but only the entries whose
// prompt_sha matches the current SHA of the prompt file on disk. Stale
// entries (from intermediate prompt versions during agent iteration) stay
// in scratch and are removed when scratch is truncated at the end of a
// successful promote.
//
// Run via `make eval-promote`.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mocky/quest/internal/eval"
)

func main() {
	scratchPath, err := eval.ScratchLogPath()
	if err != nil {
		die("locate scratch: %v", err)
	}
	benchmarkPath, err := eval.BenchmarkLogPath()
	if err != nil {
		die("locate benchmark log: %v", err)
	}
	root, err := eval.RepoRoot()
	if err != nil {
		die("locate repo root: %v", err)
	}

	scratch, err := eval.ReadBenchmarks(scratchPath)
	if err != nil {
		die("read scratch: %v", err)
	}
	if len(scratch) == 0 {
		fmt.Println("scratch is empty — nothing to promote")
		return
	}

	// Cache current file SHA per prompt path so we hash each prompt once
	// regardless of how many scratch entries reference it.
	currentSHA := map[string]string{}
	hash := func(promptPath string) (string, error) {
		if s, ok := currentSHA[promptPath]; ok {
			return s, nil
		}
		s, err := eval.PromptSHA(filepath.Join(root, promptPath))
		if err != nil {
			return "", err
		}
		currentSHA[promptPath] = s
		return s, nil
	}

	var promoted, stale int
	for _, e := range scratch {
		sha, err := hash(e.PromptPath)
		if err != nil {
			warn("hash %s: %v", e.PromptPath, err)
			continue
		}
		if e.PromptSHA != sha {
			stale++
			continue
		}
		if err := eval.AppendBenchmark(benchmarkPath, e); err != nil {
			die("append: %v", err)
		}
		promoted++
	}

	if err := eval.TruncateScratch(); err != nil {
		die("truncate scratch: %v", err)
	}

	fmt.Printf("promoted %d entries, %d stale entries dropped, scratch truncated\n",
		promoted, stale)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}
