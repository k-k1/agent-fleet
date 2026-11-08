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

## Gold-set evaluation

```
cd memengine
go run ./cmd/goldeval -cases cases.jsonl -inputs inputs.jsonl -out run \
  -tier small='env CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 claude -p --output-format json --tools "" --system-prompt "You output only the JSON object asked for." --no-session-persistence --disable-slash-commands --strict-mcp-config --setting-sources "" --model haiku' \
  -tier large='... --model sonnet'
```

A tier is `-tier name=command` (any one-shot CLI: prompt on stdin, JSON on stdout) or a preset,
`-tier claude:haiku` / `-tier codex:<model>`. The report is keyed by tier name, so a later run adds a column
by adding a `-tier`. Token usage is parsed per CLI by a small adapter (`memengine.Adapters`: the claude
envelope, codex `--json` events); a CLI that reports none is valid and shows `n/a`. The codex adapter is
covered by a fixture only, not yet run against the CLI.

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

## Replay mode (models that can only answer as sessions)

```
goldeval export-prompts -cases cases.jsonl -inputs inputs.jsonl -out run/prompts.jsonl
goldeval score -cases cases.jsonl -inputs inputs.jsonl -out run/full -answers run/answers-X.jsonl -tier agy:x
```

`prompts.jsonl` has one line per call, `{case_id, call, prompt, schema}`, the prompt being the exact text the
one-shot runner sends (it already asks for one JSON object and nothing else). Patch prompts depend on the
classify answer in a live pipeline, so they are exported for every case whose gold label is patch,
supersede or merge (`gold_conditioned: true` on the last two) and are reported as such: the supersede/merge
rows are scored on validity and kept facts only. `score` reads `{case_id, call, output[, usage]}` lines, runs
the same validation and scoring as a live run, and refreshes the combined report. Usage is optional and shows
`n/a` when absent. The report records each tier's mode: a replay tier is a session that saw earlier cases,
so it is not fully comparable with a live one.
