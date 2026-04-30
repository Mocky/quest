# Adding an Eval Scenario

This doc walks through adding a new scenario to the prompt-evaluation harness at `internal/eval/`. Each scenario exercises one specific behavior that an agent-facing prompt at `docs/prompts/` is responsible for teaching; "passing" means the agent reading the prompt does the right thing in that situation.

Read `docs/prompt-eval.md` first — it defines the framework (scenarios, criticality tags, the Pareto rule). This doc covers the mechanics.

## Files you'll touch

```
internal/eval/eval_test.go                        (add Test<Scenario> function + verifiers)
internal/eval/testdata/<scenario_name>/           (new)
  ├── task.md                                     (the agent's user-message task)
  └── seed/
      ├── setup.sh                                (executable; bootstraps the workspace)
      └── ...                                     (anything setup.sh references)
internal/eval/benchmarks.jsonl                    (gains baseline entries via promote)
```

## When to add a scenario

The gating question: **does this scenario come from a real failure?** Two acceptable sources:

- A retrospective surfaced an actual prompt-related failure. Encode it.
- A known historical failure isn't yet covered (the qst-1e regression is the seeded example).

Sources that are *not* acceptable:

- "I imagine this might fail." Imagined scenarios overfit to imagined failures. The eval is data-driven; scenarios come from data.
- "This would be nice to have." Coverage-for-coverage's-sake adds maintenance burden without addressing real risk.

If you can't point to a real failure, stop here.

## Pick a name and a prompt to test

**Naming convention:** snake_case, describe the *behavior being tested*, not the failure mode. Examples:

- `discovered_from_link` — does the agent set `--discovered-from` when filing a discovery?
- `parent_complete_blocks_on_children` — does the agent recover gracefully when `complete` fails because children aren't terminal?
- `crash_recovery_handoff` — does a recovering worker read the prior session's handoff before accepting?

Avoid `qst_1e_regression` — encode the *behavior*, not the bug ID. Bug IDs become opaque; behavior names stay legible.

**Prompt mapping:** which prompt does this scenario exercise?

- Elevated commands (`create`, `link`, `cancel`, `move`, `batch`, …) → `docs/prompts/elevated.md`
- Worker commands only (`show`, `accept`, `update`, `complete`, `fail`) → `docs/prompts/worker.md`

If the scenario needs a planner-then-worker handoff, the harness only runs one agent per scenario — it tests whichever prompt the *agent in the scenario* is given.

## Tag the criticality

Decide before you write the verifiers:

- **catastrophic** — failure is silent or hard to detect after the fact; downstream cost is unbounded. The qst-1e dropped link is canonical: no error, no signal, just a hole in the retrospective graph that surfaces months later. Pass-rate floor required (default ≥99%, see `docs/prompt-eval.md`).
- **recoverable** — failure produces in-run cost (extra commands, wasted reasoning, slower completion) but no permanent corruption. Optimize on per-run total cost; correctness floor set by economics.

This tag determines the decision rule when the scenario fails or regresses. Encode it as a comment in the test function's docstring so future readers know.

## Write the fixtures

### `task.md`

The user-message content the agent receives. Imagine vigil dispatching with this — concrete, scoped, no instructions about how to use quest (those come from the prompt under test). It should:

- Name the assigned task ID explicitly (`test-01` is the convention; the seed creates this).
- Describe what work to do.
- Optionally include the boundary condition the scenario tests (e.g., "while reviewing X you'll notice Y; do Z about it").

Reference: `internal/eval/testdata/discovered_from/task.md`.

### `seed/setup.sh`

Executable bash script taking a workspace directory as its single argument; bootstraps it for the agent. Conventions:

- `set -euo pipefail` at the top.
- `quest init --prefix test` — `test` is the convention so eval task IDs (`test-01`, `test-02`) don't visually clash with the project's own task IDs.
- Create and accept whatever tasks the scenario needs as starting state.
- Copy in any source files the agent will edit.

The script runs *before* the agent is spawned. After it finishes, the workspace should be in the exact state the agent walks into.

Reference: `internal/eval/testdata/discovered_from/seed/setup.sh`.

### Other seed files

Source files, prior PR descriptions, debriefs, anything the scenario needs. Put them in `seed/` and have `setup.sh` copy them. `quest create --description @file.md` is the standard way to seed long content into a task.

## Add the test function

Open `internal/eval/eval_test.go` and add a new test. The existing `TestDiscoveredFromRegression` is the template — copy the structure, replace scenario-specific bits.

What stays mostly verbatim:

- The `if os.Getenv("QUEST_EVAL_SKIP_IF_BENCHMARKED") != ""` skip block.
- `defer reportScenario(t, scenario, &result)`.
- `setupSeed` / `os.ReadFile(promptXxxPath)` / `os.ReadFile(task.md)` / `runAgent` calls.
- The `if os.Getenv("QUEST_EVAL_VERBOSE") != ""` verbose-logging block.

What you write fresh:

- A `verifyOutcome<Scenario>(workdir)` function — predicates over the post-state captured by `quest export`.
- A `verifyTrajectory<Scenario>(calls)` function — predicates over the captured Bash invocation log.

Verifiers are scenario-specific because they encode what "right" means for that scenario. Don't try to generalize prematurely — copy + adapt.

**Verifier alignment with criticality:**

- For *catastrophic* scenarios, the **outcome verifier** must catch the silent-corruption case. The trajectory verifier is supplemental — it tells you *why* the outcome was wrong.
- For *recoverable* scenarios, the **trajectory verifier** carries more weight — it catches inefficient patterns that produce correct outcomes at high cost.

### If the scenario tests a non-elevated prompt

The harness currently hardcodes `promptElevatedPath` in `appendBenchmarkLine` and the test body. To test against `worker.md`, add:

```go
const (
    promptWorkerPath      = "../../docs/prompts/worker.md"
    promptWorkerCanonical = "docs/prompts/worker.md"
)
```

…and refactor `appendBenchmarkLine` to take the prompt path + canonical name as parameters. This is a minor but real refactor; if you're adding the first worker scenario, that work is part of your task.

## Validate

Run only the new test:

```sh
go test -tags eval -count=1 -v -run TestYourScenario ./internal/eval/...
```

Iterate on fixture and verifier predicates until:

- The test passes when the agent does the right thing.
- The test fails *with a clear error* when the agent does the wrong thing. Sabotage `task.md` deliberately to confirm the verifier catches the failure mode you intend to catch; revert before final commit.

Use `QUEST_EVAL_VERBOSE=1` to see what the agent actually did when debugging a confusing failure.

**Cross-model check:** before committing, run at least once against a different model (e.g., `QUEST_EVAL_MODEL=sonnet`). A scenario that only passes on one model isn't a scenario, it's a model-specific quirk.

## Establish a baseline

Once the test is correct, run K times to populate `benchmarks.jsonl`:

```sh
make eval-changed       # writes runs to scratch.jsonl
make eval-changed       # repeat — 5+ for catastrophic, 3+ for recoverable
# ...
make eval-promote       # move scratch entries to benchmarks.jsonl
```

Inspect with `make eval-compare`. Confirm:

- Pass rate matches expectations (100% on a stable scenario; less is fine but tells you the scenario is inherently lossy and that's a property to flag in your debrief).
- Cost is reasonable for the swarm budget you're targeting.
- No anti-patterns trigger the trajectory verifier on normal runs.

## Commit

A single commit with all the pieces:

```sh
git add internal/eval/eval_test.go \
        internal/eval/testdata/<scenario_name>/ \
        internal/eval/benchmarks.jsonl
git commit
```

The commit message should:

- Describe what behavior the scenario tests and which prompt it exercises.
- Reference the failure that motivated it (retrospective ID, qst-XX, or explicit observed-failure description).
- State the criticality tag.

Future readers use the commit to understand *why* the scenario exists. A scenario whose motivation can't be reconstructed is at risk of being deleted as noise during cleanups.

## When to factor out helpers

For 1–3 scenarios, copy-paste from `TestDiscoveredFromRegression` is the right approach — duplication is cheap and the abstraction's right shape isn't yet clear. After ~4 scenarios, the duplication will start dictating the abstraction; extract a `runScenario(t, opts)` helper at that point. Don't refactor preemptively.
