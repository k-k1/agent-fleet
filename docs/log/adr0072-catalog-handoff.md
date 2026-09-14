# ADR 0072 catalog redesign: paused implementation handoff

The user requested a pause at a safe checkpoint, including every child session,
and a handoff to a new parent using **GPT-5.6 sol**, with new children using
**GPT-5.6 terra**. Do not resume the old children or change their models. Launch
successors from committed branches after checking that the old sessions stopped.
This is unfinished implementation, not a completion or deployment report.

## Parent checkpoint

- Branch: `temp/silzntq`; implementation checkpoint: `fb956a4c`.
- Parent working copy was clean and the branch pushed before this handoff note.
- Search backend and two UI follow-ups are integrated. The S3 backend and final
  operation-modal corrections are not yet integrated.
- No deployment or merge into `develop` was performed. Push the working branch;
  do not move or merge into another session's checkout.

## Approved behavior

- Separate Image and LLM views with distinct card bodies filling the pane.
- Header search and sorting; browse and registered-model views; model/LoRA filter.
- Image defaults to Civitai, with HF switching. Civitai uses honestly labelled
  newest publication order because strict upstream modification order is unavailable.
  HF uses last-modified descending. LLM searches HF only.
- Real cursor pagination. Small optional example image on the right, opening a
  lightbox; Escape closes it and restores focus. No empty image placeholder.
- Footer buttons for add, attach part, and replace. Each button fixes the operation
  and opens one simple modal, without operation radios or Next/Back steps.
- Preserve version/file choice, manual URL and S3 registration, history, tokens,
  license and access facts, family VAE, VRAM checks, author parameter hints, and
  trigger words. Keep grants, borrowed read-only engines, and engine-less browsing.
- Start jobs without leaving the catalog; show a new job immediately, restore jobs
  from the server, and refresh catalog/storage after completion. Never auto-enable.
- Show real S3 existence, including missing/unknown and multipart partial states.
  Do not infer presence from completed jobs or claim an entire repository downloaded.
- Reuse requires exact immutable source/file identity and a fresh server existence
  check, while retaining normal add/attach/replace validation.
- Latest visual requirement: use the Console's existing `Button`/`IconButton`
  primitives and variants, including hover, disabled, focus, light and dark themes.
- Preserve existing persisted `engineAdd` layouts. Settings opens the model pane;
  engine operations/GPU configuration remains available separately.

## Child branches and ownership

These are old GPT-5.6 sol sessions. All received a checkpoint-and-stop request and
a stop-after-turn booking. Their final checkpoint SHAs must be collected before
starting successors; do not assume a working tree's partial changes were committed.

| Session | Branch | Scope | Last integrated child commit |
| --- | --- | --- | --- |
| `sekd22a` | `temp/sqykahv` | `console/**` UI | `90b7bd16` (parent `fb956a4c`) |
| `s2maevv` | `temp/sc54zc3` | S3 backend/store/AWS wiring | none; base `15378aec` |
| `solwsa4` | `temp/ssxi2p5` | search backend, then new UI regression test | search `e9dee380` |

The UI branch also has integrated `673e3f96` and `60155633`. The search branch
merged the parent through `e973933a` into `cae3d4d9` for its latest test task.
Cherry-pick only its new regression-test commit, not its parent merge/history.
The new test file is `console/src/features/settings/admin/adminEngineCatalogRegression.dom.test.tsx`;
its assigned cases are legacy Civitai inputs, out-of-order versions/files replies,
and invalid LLM-LoRA family suggestions. Production UI changes belong to the UI
successor, so these can continue independently.

## Integrated search API

- Per-engine POST `ingest/search`: `{q,source,sort,lora,cursor?}` returns
  `{hits,next_cursor?}`. Hits add `model_ref` and safe optional `preview_url`.
- Engine-less POST `/api/admin/engines/search?kind=gguf|checkpoint`; LoRA is the
  JSON `lora` boolean, never `kind=lora`.
- POST `ingest/versions`: `{source,ref,model_ref}` returns
  `{versions:[{ref,name,published_at?,updated_at?}]}`. HF exposes main only;
  Civitai returns public versions and validates model/version association.
- Existing Civitai files/resolve select by exact filename; no candidate file ID
  wire field was added. HF card thumbnail allows credential-free HTTP(S).
- LLM rejects Civitai across browsing, version/file resolution, and ingest.

## S3 integration contract and outstanding review

GET `/api/admin/engines/{key}/storage` returns `{files,checked_at?}`. Each file has
`s3_key`, human `source?`, `state: present|missing|unknown`, `bytes?`, `checked_at?`,
`model_ids`, optional `artifact_identity`, and `reusable`.

Resolve adds `artifact_identity?`. UI reuse requires equality of the resolved
identity and a present file's identity, plus `reusable === true`. POST ingest adds
optional `reuse_s3_key`; the server independently validates identity and current
existence. Human source strings, HF main, or Civitai version alone are insufficient.

The S3 child was implementing a deployment-neutral metadata port with an AWS
adapter, globally bounded HEAD concurrency, a total inventory timeout, a display
cache, and fresh checks for mutations. Catalog and visible job keys only, tenant
isolation, same-role key prefixes, old ambiguous identity refusal, and exact
create/attach/replace store operations must be preserved. The AWS template needs
GetObject and scoped ListBucket semantics: without ListBucket a missing key can
produce 403, which must remain unknown rather than missing.

Additional requests sent before stopping:

1. `finish` must not mark a job done before successful catalog installation and
   merely log an installation failure. New create-race rejection makes this visible.
2. Replacing a different version with the same filename must not overwrite an old
   S3 object before successful registration. Inspect destination-key protection.
3. Preserve human `source`; never repurpose it as `artifact_identity`.

Expect integration conflicts in `engine_admin.go` and route/wire goldens; retain
both search/version and storage changes. The S3 work was still uncommitted at the
initial pause inspection, including new `engine_storage*.go` and tests.

## UI review: unfinished corrections

The role-specific and registered cards are now integrated, but do not accept the
initial UI test count as proof of feature completeness. Old tests were redirected
to `LegacyEngineAddView`; migrate relevant assertions to the real new modal and
remove dead production wizard paths where appropriate.

Previously identified and sent to the UI child:

1. Fix the operation from the pressed card button; remove operation-choice radios.
2. Immediately refresh jobs after start; refresh rows/storage on job completion.
3. Use the immutable S3 contract above; count only present files and distinguish
   zero, checking, and unknown. A failed storage request must not say checking forever.
4. Bind pagination to the submitted query; editing query and clicking More must
   not append a new query's page to the old results.
5. Parse both `civitai:<version>` and `/models/?modelVersionId=<version>` correctly.
6. Guard versions/files responses against stale requests when changing selection.
7. LLM VRAM must include weights plus `kv_mib_per_1k_tokens` times context.
8. LLM LoRA requires a registered non-LoRA model ID; never accept an upstream
   `base_model_suggest` that is not an option.
9. Restore restrictions, commercial use, login requirements and gating acceptance
   facts on cards/resolution, with specific rejection reasons.
10. An unreachable family VAE is disabled; show its repo/file/size/license before
    accepting the additional file.
11. Show/apply/edit `params_hint` and `params_hint_quote`; preserve LoRA trained words.
12. Use existing shared button primitives throughout the new card, toolbar and modal UI.

Preserve old tests from base `15378aec` in `adminEngineModels.dom.test.tsx`:
license (around 1312), preserving window edits (1403), repository candidates (1492),
Civitai account requirement (1585), pasted HF/Civitai URLs (1914/1949), and weights
plus KV cache (2837). Line numbers describe the base, not the expanding new file.

## Verification and successor's first steps

1. Collect final child checkpoints and ensure every old session stopped. Integrate
   safe commits into the new parent's branch; inspect WIP before assuming it builds.
2. Start new **terra** children for remaining UI and backend/test tasks if useful.
   Do not send work to the stopped old sessions. The new parent is **sol**.
3. Complete the API/UI contract and outstanding guards before final acceptance.
4. Build Console, run appropriate typecheck/i18n/DOM tests, then browser acceptance.
5. Update both ADR 0072 documents with actual implementation/verification status;
   they currently record approval, not a false completion. Run scanners and push.

`console-e2e/tests/engine-catalog-mock.spec.ts` serves the real built `console/dist`
with mocked APIs and local Chromium; it needs no running CP or database. Seven
tests are discovered: HF-only LLM, engine-less model/LoRA browse, registered partial
storage and edit dialog, fixed add operation/new job, and thumbnail/lightbox at
1400 dark, 390 dark, and 1400 light. Latest additions were only listed/parsed, not
executed against completed UI. They intentionally assert shared `ui-btn` variants.

Before the latest UI follow-ups, the actual browser run was **3 passed / 1 failed**:
the failure exposed the operation radio strip. Actual screenshots confirmed small
right thumbnails, lightbox focus return, and narrow layout, but native gray buttons
remained wrong. Do not report these as final visual approval. Rebuild before rerun.

Search backend focused tests, UI child's earlier typecheck/i18n/174 DOM tests,
parent docs check (392 files), scanner positive-control tests and staged scans had
passed. S3 final tests and full integrated acceptance remain pending. No live AWS
deployment validation has happened.

Run Node tests from `console/`, cap workers at 2 and heap per command; one heavy
build at a time. Go tests: `GOMAXPROCS=2 go test -p 1 ...` from the correct module.
The parent `console/node_modules` is a shared symlink: never install through it.
Read workspace skills for worktrees/build/browser procedures. Commit messages are
Japanese conventional commits with actual model coauthor; comments are English.
