# memengine

The model-facing core of cultivated memory ([ADR 0130](../docs/decisions/0130-cultivated-memory.md),
decisions 6 and 8): two bounded model calls with fixed JSON schemas, the deterministic validation every
answer must pass, and the model as a plug-in.

- **Own module, no dependency on this repository.** `independence_test.go` fails on any import of an
  `github.com/k-k1/agent-fleet/...` package, and on a `require`/`replace` in `go.mod`. The directory
  name is neutral on purpose: the module is meant to be split into its own repository with its history
  once a second consumer exists or the format reaches v1.
- **Classify** (`classify.go`): one observation plus snippets of at most `MaxSnippets` memories of the
  topic in, `add | patch | supersede | merge | no-op` out. `Operation.Escalates` marks supersede and
  merge as the larger tier's.
- **Patch** (`patch.go`): one note in, `{old, new}` edits out, never a body. `ApplyEdits` checks that
  each `old` occurs exactly once, edits do not overlap, and the patch applies.
- **Invalid output is dropped, not retried** (`ErrInvalid`).
- **Model** (`model.go`): a one-shot command, prompt on stdin, JSON on stdout, configured by argv. It
  accepts the `claude -p --output-format json` envelope and records the token usage it reports. No SDK,
  no key.

## Roles, not models

The engine knows two roles: `small` (the default for every operation) and `large` (the escalation for
supersede and merge, `Pipeline`). The shipped config (`models.json`) maps each role to a candidate
`<runner>:<model id>`; a runner is an argv template with `{model}` in it (`claude` and `codex` are built in,
a session runner such as `agy-session` is replay-only). No model ID is in the code (`TestNoModelIDsInCode`),
so a lineup change is a data change, and tests use fake runners only.

A retired model fails safe: its command exits with an error, the call is an error (not an invalid output),
nothing is applied, and the other role's model is never tried in its place (`TestRetiredModelFailsSafe`).

## When a new model appears: re-run, compare, re-map

1. Add it to `candidates` in the config (`<runner>:<model id>` and a relative `cost`).
2. Live runner: `goldeval -config models.json -bar bar.json -out run -tier <runner>:<model id>`.
   Session-only model: `goldeval export-prompts`, have the session answer `prompts.jsonl` into an answers
   file, then `goldeval score -answers ... -tier <runner>:<model id>` (see Replay mode).
3. Read `run/report.md`. Every row records the exact model ID, the runner, live or replay, the UTC date and
   the gold-set version (a content hash of `cases.jsonl`); a run scored against another gold-set version is
   listed but not judged. Compare candidates against the bar and read the suggested mapping.
4. A replay winner is a hint: confirm it with a live run before mapping, since a session sees earlier cases.
5. Re-map by editing `roles` in the config. Nothing else changes. When the bar itself should move, edit
   the bar file and say why in the commit; the bar is the member's to set.

## The bar is data

`bar.json` holds, per operation, the maximum wrong changes, the maximum invalid or failed answers,
the minimum fix rate (correct answers, or applied patches for `patch-call`) and, for `patch-call`, the
maximum lost facts; and which operations each role must pass. `report.md` shows pass/fail per candidate per
operation and suggests the cheapest passing candidate for each role. The shipped values were adopted by the member on 2026-11-09 as proposed
(ADR 0130, note of that date); the roles in `models.json` are `small` = `claude:haiku`, `large` = `claude:sonnet`.

## Gold-set evaluation

```
cd memengine
go run ./cmd/goldeval -cases cases.jsonl -inputs inputs.jsonl -out run \
  -config models.json -bar bar.json -tier claude:<model id> -tier codex:<model id>
```

A tier is `-tier <runner>:<model id>` (runner templates from the config or the built-ins) or
`-tier name=command` (any one-shot CLI: prompt on stdin, JSON on stdout). With no `-tier`, the config's
live candidates run. The report is keyed by tier name, so a later run adds a column
by adding a `-tier`. Token usage is parsed per CLI by a small adapter (`memengine.Adapters`: the claude
envelope, codex `--json` events); a CLI that reports none is valid and shows `n/a`. The codex adapter is
covered by a fixture only, not yet run against the CLI.

Keep `-out` outside the repository: the run files hold the models' raw answers, which quote memory text
(`memengine/run/` is git-ignored as a backstop). `-gold-conditioned` makes the live run also make the patch
call for supersede/merge cases, as replay does (validity and kept facts only).

`cases.jsonl` is the member-labelled gold set. `inputs.jsonl` holds, per case id, the memories it cites
(`name`, `scope`, `revision`, `cited_revision`, `revision_mismatch`, `description`, `body`). Both are
private and stay out of the repository. Answers and usage go to `-out/<tier>.run.json`, the scores to
`report.md` and `report.json`; a rerun reuses answers already there (`-resume=false` to pay again).

`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` matters for cost: without it every call carries the host's project and
user instruction files (about 9–19 k input tokens measured), with it about 0.9 k.

Scoring is per labelled operation: wrong changes, missed fixes, invalid outputs, tokens per call; for patch
cases also lost facts and new facts missing. Must-survive items are prose, so the fact check is anchor-based
(see `internal/goldset/facts.go`) and is calibrated against the member's own reference patches, which must
lose nothing. Tier agreement is reported as a secondary measure only.

## Tuning the classify prompt without overfitting

`-split tune|heldout|all` (live runs and `score`) keeps one deterministic half of the gold set: a case is TUNE
or HELD-OUT by the low bit of the first byte of SHA-256(case id), so the halves cannot be chosen by looking at
scores and survive additions to the set. Iterate on `tune` only, then report both halves; use one `-out` per
prompt version and split (a rerun reuses answers already there, whatever the prompt). Run-to-run noise on the
small halves is several cases, so a single run proves little; the prompt must stay generic (no memory names,
paths or phrases from the private set). `score` refuses two answers for the same (case, call), within a file or
across files, rather than letting the last one win.

## Replay mode (models that can only answer as sessions)

```
goldeval export-prompts -cases cases.jsonl -inputs inputs.jsonl -out run
goldeval score -cases cases.jsonl -inputs inputs.jsonl -out run/full \
  -answers run/answers-X-classify.jsonl -answers run/answers-X-patch.jsonl -tier <runner>:<model id>
```

`export-prompts` writes two files, one line per call, `{case_id, call, prompt, schema}`, each prompt being the
exact text the one-shot runner sends (it already asks for one JSON object and nothing else):

- `prompts-classify.jsonl`: all classify calls and nothing else.
- `prompts-patch.jsonl`: the patch calls, for every case whose gold label is patch, supersede or merge
  (`gold_conditioned: true` on the last two; they are scored on validity and kept facts only).

**Procedure: answer the classify file completely first, in a fresh session, and only then hand over the patch
file.** A patch line exists only for cases whose gold label is patch, supersede or merge, so a session that
sees one next to its classify line can read the label off it (measured: a replay that did so scored far above
the live runs). `score` records which answers files each tier came from (name, content hash, number of
classify and patch answers) and treats the classification scores as valid only when the classify answers
came from a classify-only file; otherwise the report says so and the bar fails the classification operations.
That the session was fresh is procedural and not checked.

`output` in an answers line may be a string (fences or prose around the JSON are tolerated) or a JSON object;
either is validated like a live answer. `score` runs the same validation and scoring as a live run and
refreshes the combined report. Usage is optional and shows `n/a` when absent. The report records each tier's
mode: a replay tier is a session that saw earlier cases, so it is not fully comparable with a live one.
