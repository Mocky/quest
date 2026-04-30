# Prompt Evaluation Design

This doc captures the design for evaluating quest's agent-facing prompts. It is the index, not the implementation — concrete details land in the harness code and `docs/prompts/`.

## What we're testing

Quest is agent-first. Whether agents use it correctly depends as much on the *prompt* that teaches them as on the binary itself. Without verification, we don't know whether prompts:

- teach the protocol completely (no missing pieces),
- avoid prompting unwanted behaviors (every word is also a hint),
- carry the right cost-vs-correctness tradeoff for swarm-scale use.

The eval is the verification.

## Prompt artifacts under test

- `docs/prompts/worker.md` — for non-elevated agents. Teaches `show / accept / update / complete / fail`. Workers must not call elevated commands or self-identify with `AGENT_ROLE`.
- `docs/prompts/elevated.md` — for planner / lead / verifier agents. Full surface plus relationship semantics, error handling, idempotency.

**One prompt per role**, not per model. Cross-model robustness is a quality bar; if a prompt only works on the strongest model, the prompt is fragile and gets fixed, not forked. Forking is justified only when measurement shows persistent Pareto tension across models.

## Eval primitives

Three things, no matter how the harness is structured:

1. **Scenarios** — starting state + task statement + optional seed data. One directory per scenario under `testdata/scenarios/`.
2. **Traces** — every action the agent took. Captured by shimming the `quest` binary on `$PATH` to log invocations + env + exit code, and recording the agent's bash-command-string layer separately so env-var prefixes (`AGENT_ROLE=...`) remain visible after the shell strips them.
3. **Verifiers** — predicates over the trace and/or the workspace state at the end of the run.

## Two-axis verification

Both axes run on every scenario:

- **Outcome verifiers** — `quest export` after the run; assert predicates over the materialized state. Catches "did the world end up right?" Reliable because the export format is already a stable, human-readable contract.
- **Trajectory verifiers** — predicates over the captured invocation log. Catches "did the agent get there the right way?" — anti-patterns, missing must-do calls, error recovery shape.

Outcome-only would have missed qst-1e (the dropped `--discovered-from` produced wrong final state, but the diagnostic is in what the agent *didn't* call). Trajectory-only would miss subtle state issues. Both required.

## Anti-patterns (current)

Trajectory verifiers fail the run if any of these are observed:

- `AGENT_ROLE=`, `AGENT_TASK=`, `AGENT_SESSION=` env-var prefix on a `quest` invocation — agent self-identifying. Vigil sets these on dispatch; the agent must not.
- `--text` flag on any `quest` command — text mode is human-facing; agents parse JSON.
- `quest list` from a worker scenario — workers don't survey, they execute their assigned task.
- `quest create` followed by `quest link --<type> <target>` against the same target, where `--<type>` could have been set inline on `create`.

Anti-patterns are added to the verifier *before* they are added to the prompt. A prompt mention of a behavior is also a hint that the behavior exists; we only escalate to in-prompt counter-guidance when the eval shows silent omission isn't sufficient.

The list grows from two sources: known historical failures (qst-1e) and retrospectives on real deliverable work.

## Cost model

The metric is **expected cost per dispatch**, per model:

```
E[cost] ≈ prompt_tokens + E[reasoning_tokens] + E[tool_call_tokens] + E[downstream_cost]
```

The first three are observed in the harness. The fourth is unobservable per-run but matters at scenario-tagging time.

### Scenario criticality tag

- `catastrophic` — failure mode is silent or hard-to-detect; downstream cost is unbounded. Example: a dropped `--discovered-from` link produces a permanent retrospective-graph hole with no run-time signal. **Pass-rate floor required (initial: ≥99%, adjustable to risk tolerance); token cost is irrelevant against the floor.**
- `recoverable` — failure mode produces an in-run cost (extra commands, wasted reasoning) but no permanent corruption. **Optimize on per-run total tokens; correctness floor is set by economics, not policy.**

### Per-model reporting

Every eval run produces a `models × scenarios` matrix, each cell carrying pass rate and total run cost. **Don't blend across models** — a cross-model regression hidden by averaging is the failure mode this disaggregation prevents.

### Acceptance rule for prompt changes

- **Catastrophic scenarios:** every supported model must hit the floor. Any model below floor → reject.
- **Recoverable scenarios:** every supported model must be Pareto-improved or held flat on per-run total cost. Any model worse → reject.

A change that's a clear win on Sonnet and a clear loss on Opus is not a license to fork — it's the data point that, *if it persists*, eventually justifies forking. Until then, the shared prompt has to clear the cross-model bar.

### Pruning sweeps

Periodically (e.g., once a quarter), actively try to remove sections / sentences from each prompt and re-run the eval. If pass rate holds, keep the deletion (token win — also often a correctness improvement, since removed text removes attention pulls). If pass rate drops, revert. Without an active force, prompts only ratchet up.

### Persistence

Two log files, both at `internal/eval/`:

- **`scratch.jsonl`** (gitignored) — every `make test-eval` / `make eval-changed` run appends here. The harness's working log, local-only.
- **`benchmarks.jsonl`** (committed) — the official record. Only `make eval-promote` writes here.

Schema is one flat JSON object per run; identical fields in both files. Grep-friendly so an agent reading the log can group / aggregate without unpacking nested structures. Each entry carries a SHA-256 of the prompt's contents at run time, so changes to the prompt are tracked even when the file path is stable. `prompt_tokens` is a heuristic (`word_count * 1.3`), accurate to ~5% for English markdown — sufficient for tracking changes, not billing.

### Workflow

- `make test-eval` — run all eval scenarios (writes to scratch).
- `make eval-changed` — same, but skip scenarios whose `(scenario, model, prompt_sha)` tuple is already in `benchmarks.jsonl`. This is the "only run prompts that actually changed" path.
- `make eval-compare` — read both logs, group by `(scenario, model, prompt_sha)`, aggregate per group (median cost / median turns / pass rate over N runs), and print a side-by-side table of the current prompt SHA against the most recent different SHA.
- `make eval-promote` — move scratch entries whose `prompt_sha` matches the *current* prompt files on disk into `benchmarks.jsonl`, then truncate scratch. Stale entries (intermediate SHAs from the agent's iteration) are dropped.

Aggregation is per `(scenario, model, prompt_sha)`. A single run is a sample, not a verdict — per-SHA median absorbs run-to-run stochasticity; across-SHA comparison surfaces the prompt-change effect. This is what makes the K-runs framing work end-to-end.

There is deliberately no git hook. Prompt changes can be necessary for reasons unrelated to eval (a command rename in the underlying tool, for example) and forcing a benchmark on every commit would block legitimate work. The agent updating prompts is told via prompt how to handle eval explicitly; CI is the long-term enforcement layer if one is needed.

## Source of scenarios

Real failures from retrospectives, not imagined ones. Each retrospective that surfaces a prompt-related failure mode contributes:

- a `task.md` (the assignment given to the agent),
- a `seed/` (workspace + initial `.quest/` state via a setup script),
- a verifier set (outcome predicates + trajectory rules),
- a criticality tag (`catastrophic` or `recoverable`).

The qst-1e regression is the seeded scenario for v0; everything else is added as the corpus grows. Imagined scenarios overfit to imagined failures; retrospective-sourced scenarios match the failure distribution that actually occurs.

## Runtime

V0 harness uses **Claude Code in headless mode** (e.g., `claude -p`):

- Covered by Max plan budget — no separate API bill.
- Matches the runtime users (and likely vigil) actually use.
- Inherits Claude Code's tool-use loop. A passing eval means "the prompt works *as wrapped by Claude Code*"; for most quest behaviors this distinction is acceptable, and for vigil-dispatched workflows it raises faithfulness rather than lowering it.
- Max plan has 5-hour usage caps. Full matrix runs are batched across windows; per-change smoke tests run against a single model (Sonnet, middle of the distribution).

The agent-execution layer sits behind a small interface so swapping to direct Anthropic API calls (or another provider) later is a single-file change. Reasons we'd flip:

- Need to test non-Claude models (Max covers none of them).
- Eval results vary in ways that suggest Claude Code's wrapper is a confound.
- Need finer introspection than headless mode exposes.

## What we're not doing

- Not creating per-model prompts. Forking is data-justified, not preemptive.
- Not building dashboards, full model matrix, or pruning automation in v0. Add when scenario count and data justify the maintenance cost.
- Not LLM-as-judge for pass/fail. Use it for diagnosis only ("why did this run fail?"), never as the gate.
- Not putting anti-patterns into the prompt unless silent omission demonstrably leaks. Every "don't" is also a hint.
- Not loading the spec into the prompt. Prompts are self-contained; "see the spec" is dead weight at runtime.

## Forward path

1. **Cut a v0 harness** — the qst-1e regression scenario, run end-to-end against Sonnet via Claude Code headless mode. Validates the shape and the drafted prompts.
2. **Settle the scenario fixture format** — file layout, verifier YAML shape — so retrospective-sourced scenarios can be added during the retrospective itself, not as a separate engineering task.
3. **Expand the corpus** as retrospectives produce failure cases; expand the anti-pattern list as new ones surface.
4. **Promote eval components** (full model matrix, dashboards, pruning sweeps, fleet-cost weighting) as data justifies the maintenance cost.

## See also

- `docs/quest-spec.md` — quest's behavioral contract; the source of truth for the protocol the prompts teach.
- `docs/prompts/worker.md`, `docs/prompts/elevated.md` — the prompt artifacts under test.
- `AGENTS.md` — agent guide for the quest codebase, including role-gating and design principles the prompts must respect.
