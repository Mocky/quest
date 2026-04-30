# quest prompt-update protocol

You are updating one or both of the agent-facing prompts at `docs/prompts/`. These artifacts load on every agent dispatch in every swarm using quest, so changes have leverage — for cost, for correctness, both compounding at scale. Every word matters; every word is also a hint to the model that reads it.

## Before you change anything

1. **Read the current prompt.** Don't propose changes you haven't read.
2. **Read `docs/prompt-eval.md`.** It defines the framework you're operating within — scenarios, catastrophic vs recoverable tags, the Pareto rule. The decisions there bind your choices.
3. **Check the baseline.** `make eval-compare` shows the most recent measured behavior. If the prompt's current SHA isn't yet in `benchmarks.jsonl`, run `make eval-changed` enough times to establish a baseline *before* perturbing it — otherwise you have nothing to compare against.

## Two principles that override everything else

- **Every word is also a hint.** Adding a "don't do X" tells the agent X exists. Default toward silence; add explicit guidance only when the eval shows silent omission leaks. Removing text often *improves* correctness because removed text removes attention pulls.
- **Tokens compound at swarm scale.** The prompt runs on every dispatch. 50 extra tokens × thousands of dispatches/day is real cost. Cut what you can; pay only for what's load-bearing.

Hypothesis-driven prompt changes overfit to imagined failures. Data-driven ones address real ones. If you find yourself adding text to address a behavior you haven't measured, stop and run the eval first.

## Validation

```sh
make eval-changed       # runs scenarios whose current prompt SHA isn't in benchmarks.jsonl
```

Run **at least 5 times** before judging the change. A single run is a sample, not a verdict — agent behavior is stochastic and 1-run swings of ±20% on cost / turns / output are normal noise. K=5 is the floor for catastrophic-tagged scenarios; K=10 is better when uncertain.

Then:

```sh
make eval-compare       # side-by-side: current SHA vs most recent previous SHA
```

The output table aggregates each SHA's runs (median cost, median turns, pass rate over N) and shows the delta. Use `QUEST_EVAL_VERBOSE=1 make eval-changed` if you need to see what commands the agent actually ran on a failing scenario.

To test against a different model: `QUEST_EVAL_MODEL=sonnet make eval-changed`. Default is haiku for cheap iteration; sonnet and opus are more expensive but are required for full validation of catastrophic-tagged scenarios before commit.

## Decision rule

Per `docs/prompt-eval.md`:

- **Catastrophic-tagged scenarios:** every supported model must hit the pass-rate floor (≥99% by default). Any model below floor → reject the change.
- **Recoverable-tagged scenarios:** every supported model must be Pareto-improved or held flat on per-run total cost. Any model worse → reject.

You do **not** average across models. A win on Sonnet that's a wash on Opus is not justification — the shared prompt has to clear the cross-model bar, and that's the constraint of having one prompt.

If the change fails: revert. Don't argue with the eval; it sees what you can't.
If the change passes: commit. Mechanics below.

## Committing

```sh
make eval-promote       # moves scratch entries with current SHA into benchmarks.jsonl, truncates scratch
git add docs/prompts/<changed-file>.md internal/eval/benchmarks.jsonl
git commit
```

The `benchmarks.jsonl` update **must** ship in the same commit as the prompt change. Future agents reading history correlate measured behavior with exact prompt text via the recorded SHA — a prompt-only commit leaves a gap.

## What you don't do

- **Don't promote without comparing.** Promoting unmeasured runs to the canonical record poisons the dataset.
- **Don't fork the prompt per model.** Cross-model robustness is the quality bar. Fork only when measurement shows persistent Pareto tension that no shared change can resolve — and even then, prefer a small per-model patch over a full rewrite.
- **Don't add anti-patterns to the prompt** unless the eval shows silent omission isn't sufficient. The trajectory verifier already catches them; in-prompt counter-guidance costs tokens and may cue the very behavior you're trying to prevent.
- **Don't run K=1.** Single-run conclusions are wrong roughly half the time.
- **Don't write `benchmarks.jsonl` by hand.** Promote from scratch.
- **Don't bypass the eval to ship faster.** There is no git hook enforcing this — discipline is the enforcement.

## Edge cases worth escalating in your debrief

- **One model wins, another loses, no compromise change available.** This is the data point that eventually justifies forking. Don't fork on first sighting; record the observation and leave the prompt as-is.
- **A user-requested change (e.g., for a quest command rename) regresses the eval.** User intent overrides the eval verdict — make the change, run the eval anyway, and surface the regression so a follow-up task can address it.
- **Eval cost is approaching your Max plan window.** Stop, fail with a debrief explaining the budget situation, and let the lead reschedule.
