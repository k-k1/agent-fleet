# 0085. The model catalogue's second shape — the bucket is the ledger, and taking a model in is one press

English | [日本語](0085-model-ledger-and-one-press-ingest.ja.md)

- Status: **drafted** (2026-09-15). Not built. Every "exists" / "does not exist" claim below was
  checked by grep on `57ac9515` (develop plus PR #691, which this ADR assumes merged first) and
  every "measured" claim was read off af-sandbox's admin API the same day.
  Status update (2026-09-24): built, all merged on 2026-09-15. P1 (CP) landed as #695 (the one-press ingest, `control-plane/engine_plan.go`) and #696 / #697 (the bucket read as the ledger), P2 (Console) as #694, and P3 as #704 (the grace routes and fields removed, `plan_token` required). Whether the measurements P1 and P2 name were run on af-sandbox is not recorded here.
- Supersedes, in part: [0072](0072-engine-model-catalog.md) decision 6's request shape (the caller
  names the S3 key; `attach` / `replace` / `reuse_s3_key` as three request modes) and the four
  2026-09-15 addenda's remedies (`POST …/models/{id}/parts`, `main_file_fix`, the job-history
  "register with this key" button). Decision 2's two-layer truth (S3 says what exists, the
  database says how it is offered) **stands and is what this ADR finally builds on**; decision 2's
  manifest file next to every object was never written (`ingest-upload.sh` writes the object and
  nothing else) and is retired here.
- See also: [0071](0071-self-hosted-inference-engines.md) (the instance mirrors the bucket; ComfyUI's
  layout) / [0082](0082-many-image-engines-at-once.md) (several image rows; the external row's
  `/discover`, which is the same idea read off an instance this deployment does not own) /
  [0084](0084-engine-indicator-and-tenant-gate.md) (the tenant gate the ledger's visibility
  follows) / [0069](0069-image-generation-providers.md) (what the Agent names a file by)

## Context

### The premise changed

ADR 0072 was written for an operator who knows what a diffusion model, a text encoder and a VAE
are, and who is willing to say which one a file is. The person this deployment is for said, on
2026-09-15, after four fixes in four days:

> 満足に使えていない。場当たり的な対応ではなく、設計や、モーダルも含めて根本的に見直したい。
> （画像生成の理解が乏しい）利用者が、疑問に思わず、検索しすぐに取り込み使える機能にしたい。

"Search, take it in, use it — without a question coming up." That is the requirement. Every
decision below is measured against it, and the operator-declares-everything shape of 0072's
ingest form is what it replaces.

### What broke, in order, and why each fix found the next wall

| PR | What it fixed | The wall behind it |
|---|---|---|
| #684 | A split family (anima / krea2) is taken in as one act; an existing row is completed with one press | The main file was still "the whole checkpoint", a role no split-family template reads |
| #688 | The key must be `<role dir>/<base name>` or no ComfyUI loader lists it; existing rows are repaired by `MODE=move`; the Console composes the key flat; the CP refuses any other key | `reuse_s3_key` still carries the OLD key, so a re-ingest from Hugging Face now answers 400 `s3Key must be empty or identical` (`engine_admin.go:1892`) |
| #690 | The ingest task definition is named by family, and the CP re-reads the ingest block on every table reload | The three failed attempts had written job rows that hold the three destination keys |
| #691 | A `failed` job that never got a task is not a holder | (open at the time of writing) |

And two things measured on af-sandbox on 2026-09-15 that no PR addresses:

1. **The job history's "register with this key" does nothing visible.** `onReuse`
   (`adminEngineAdd.tsx:632`) only pre-fills the manual form, and that form sits inside a closed
   `<details>` (`adminEngineAdd.tsx:626`) ABOVE the job list. Nothing opens or scrolls.
2. **After the rows were forgotten there was no road back.** The operator pressed 登録を消す on
   both families. The bytes stayed (4.18 GB and 13.1 GB at the wrong keys, plus the parts at both
   wrong and right keys — about 24 GB, all `present` by HeadObject). The only repair that moves
   bytes is `engineMainFileFixFor` (`engine_file_move.go:52`), which is computed **from a row**.
   No row, no repair. The manual road was: forget three failed jobs (which turned out not to land
   from the Console — the API `DELETE` answered 200 when called directly), register a row with
   the WRONG key on purpose under the SAME id the old job used (because `engineKnownArtifacts`,
   `engine_storage.go:206`, counts a done job's `model_id` as a holder and the move refuses a
   different id as "also declared by"), then press 揃える. Nobody can be expected to find that.

### Three roots

Every wall above is one of these three, and fixing symptoms one at a time is why each fix
uncovered the next.

1. **Three parties decide the destination key.** The Console composes it
   (`adminEngineAdd.tsx:994`, `engineIngestPrefix` + base name); the CP recomputes it and refuses a
   mismatch (`postIngest`, the `engineComfyKeyFor` check); verified reuse brings in a third — the
   key some earlier job recorded — and demands `s3Key` equal it. The key is a pure function of
   `(role, flag, base name)` (`engine_catalog.go:290`). Only one party should compute it, and it
   is the one that also has to check it.
2. **The CP has no ledger of the bytes.** `GET …/storage` "returns only keys the server already
   knows" (`engine_storage.go:302`): a HeadObject fan-out over the keys rows and jobs name. Forget
   the row and the job, and the object still costs money but no longer exists to the Console —
   the ack dialog before forgetting a job (`admin.engines_ingest_job_forget_last`) says exactly
   this. Meanwhile the CP task role **already holds** `s3:ListBucket` on the bucket with a prefix
   condition `llm/*`, `image/*` (`60-engines.yaml:299-304`); the comment beside it says "the CP
   has no list operation", which is a statement about the code, not the permission.
3. **Repairs hang off a row's attributes.** `files_missing`, `main_file_fix`, `vae_missing` are
   computed per row, and every remedy route takes a row id. The bucket-side view — "this object
   is under a directory no loader lists" — cannot be expressed, so an orphan object has no
   button.

Add to these the vocabulary. Six ways to get bytes into a row, each with its own preconditions,
refusals and screen: new / attach / replace (three modes of one request) / verified reuse
(automatic, inside the same request) / 揃える (parts + move) / manual S3 registration (+ its
pre-fill from a job). Eight acts on screen, 214 `admin.catalog_*` / `admin.engines_ingest_*` /
`admin.engines_model_*` strings in the ja catalogue. The operator's every wall had the shape
"I fell into a different path": attach when replace was meant, `new` with a part, a reuse that
now contradicts a key rule written after it.

### What is right and stays

- **The layout.** `image/<role dir>/<base name>`, one directory per loader, the Agent names by
  base name (`workspace/agent/engines.go:140`). Everything here is built to make that layout
  unbreakable rather than to check it after the fact.
- **The two-layer truth** (0072 decision 2). S3 says what exists; the database says what is
  offered. This ADR gives the first layer a route of its own.
- **`MODE=move`** (contract 3). Bytes already paid for are relocated server-side, never fetched
  twice.
- **`enabled` is a human act.** Nothing in the bucket enables a row (0072's rejected "derive the
  catalogue from ListObjects" is still rejected — the ledger lists, it does not offer).
- **The family vocabulary and the parts table** (`engineComfyRequiredFlags`,
  `engineFamilyParts`). The knowledge of what a family reads is in the CP; the change is that
  the CP uses it instead of asking.
- **The tenant grant** (`allow_engine_ingest`, 0072 open question 11). The ledger is scoped by
  the same rule as the job list.

## Decision

### Decision 1 — the CP decides the destination key; the wire stops carrying one

`POST …/ingest` loses `s3Key` and `reuse_s3_key`. The CP computes the key from
`(role, flag, base name of the upstream file)` with `engineComfyKeyFor`, for every provider that
has a file vocabulary; for `sdcpp` / `openai-compat` rows nothing changes (one whole file under
`checkpoints/`). The `engineComfyKeyFor` mismatch refusal added by #688 becomes unreachable and
is deleted with the test that pinned the Console's copy to it (`contract_engine_keys_test.go`);
`engineIngestPrefix` leaves the Console.

Reuse is no longer a request mode. It is what the CP does when the ledger (decision 2) already
holds an object whose recorded artifact identity equals the one the resolve just computed:

- at the canonical key → declare it, download nothing (today's reuse);
- at another key, present → **move** it to the canonical key (today's `MODE=move`), then declare;
- absent → download.

The response says which of the three happened. The 400 `s3Key must be empty or identical`
cannot occur because there is no `s3Key`.

### Decision 2 — the bucket is the ledger: `GET /api/admin/engines/{key}/objects`

One route lists the role's prefix with `ListObjectsV2` (paginated, 1,000 per page; the sandbox
bucket has under a hundred objects) and joins every object with what the database knows about
it. **No IAM change**: the permission exists (`60-engines.yaml:299-304`); the comment beside it
is rewritten to say what it is now for. The CP still never reads object bytes.

Each object answers:

| field | from |
|---|---|
| `key`, `bytes`, `last_modified` | S3 |
| `role_dir` (`checkpoints` / `diffusion_models` / `text_encoders` / `vae` / `loras` / `other`) | the key's path, by `engineComfyRoleDir`'s table read backwards |
| `placement`: `ok` / `misplaced` | whether the key equals `engineComfyKeyFor(role, flag-of-role_dir, base name)`; `misplaced` for `image/checkpoints/split_files/…` and for any depth above one |
| `declared_by[]` | rows whose `files[]` name this key, with the flag each uses |
| `source`, `artifact_identity`, `license` | the newest `done` job for this key, else the declaring row, else empty |
| `job` | the newest job whose destination is this key, when `pending` / `running` / `failed`, with its message |

Plus rows the database names whose object is **absent** (today's `missing`), listed under the
same route as `state: missing`, because "a row points at nothing" is a ledger fact too.

`GET …/storage` stays one release as a projection of this table (the registered cards' badge
reads it) and is then removed.

**Scope.** A super_admin sees the whole prefix. A tenant_admin granted `allow_engine_ingest`
sees the objects its own tenant's jobs produced or its rows declare — the rule the job list
already applies (`engine_ingest_perm.go:182`) — and nothing else in the prefix. The bucket is the
operator's; a granted tenant sees what it put there.

### Decision 3 — two acts, both on the model: **take in** and **complete**. Parts are never the subject

Everything an operator can do to get bytes into a row is one of two requests, and both start
from the model (the checkpoint row), never from a part. The operator said why in one line:
choosing a destination from a part is backwards — a checkpoint is what a person means, and the
parts that fit it are what the machine knows.

- **Take in**: `POST …/ingest` — upstream → object → row, in one press (decision 4).
- **Complete**: `POST …/models/{id}/complete` — the row is the subject; the CP looks at what the
  family reads, what the row has, and what the ledger (decision 2) holds, and closes the gap:
  - a missing role whose file the ledger already holds at the canonical key → declared, 0 bytes;
  - held at another key, present → moved (`MODE=move`), then declared;
  - the row's own main file at a wrong key → moved (today's `main_file_fix`, now just one case);
  - not held → taken in from the family's part table (`engineFamilyParts`), or listed as
    `unknown` for a family with no table, naming the role the person still has to supply.
  - **Candidates.** For a missing role the CP ranks the ledger's objects in that role's
    directory: the family's declared part first (by artifact identity), then any object whose
    recorded family fits. With exactly one candidate the press just uses it. With several, the
    row's 揃える dialog shows them — the only place a person ever picks a part, and they pick it
    FOR this checkpoint. `{choices: {"--vae": key}}` in the body carries the pick.
  - **Swapping** a part a row already has is the same dialog on a filled slot (today's
    `replace`): `{choices: {flag: key}, replace: true}`; the refusal without `replace` carries
    `next` (decision 5).
  - A part object gets **no button of its own** in the ledger. Its only acts are the ones a
    row performs on it, plus deletion when nothing declares it (decision 7).

**Registering a checkpoint that is already in the bucket** is the one object-side act, and it
is still an act on a model: `POST …/objects/register` `{key, base_model?, id?}` for an object
in `checkpoints/` or `diffusion_models/` (or misplaced under a path that names one of them)
that no row declares. The CP proposes `id` and `base_model` the way decision 4's plan does,
creates the row disabled (`license_accepted_by` empty — assigning bytes that are already here is
not the human act of accepting a licence), moves the object to the canonical key if it is
misplaced, and then runs **complete** on the new row. This is the road for a deployment that
finds itself where af-sandbox was on 2026-09-15 (bytes present, rows gone): press 登録 on each
misplaced main file; the encoders and the VAE the ledger holds are attached by the same press. A key not in the bucket is refused (HeadObject; the CP can).
`POST …/models` — the JSON round trip a super_admin uses to rebuild a row from the row's own
answer — keeps accepting unverified keys and is the only route that does; its form leaves the
Console.

`attach`, `replace` and `reuse_s3_key` leave `POST …/ingest`. `POST …/models/{id}/parts` and
`…/vae` fold into **complete**. The 揃える button stays; what it does is now "declare whatever
the ledger already has for this checkpoint, move what is misplaced, take in the rest".

### Decision 4 — taking a model in is one press, and the CP decides what that press does

`POST …/ingest/resolve` (which the form already calls while the person is looking) answers a
**plan**, not a set of facts to be turned into form fields:

```
{ id, base_model, main_flag,
  files: [ {flag, name, bytes, action: "download"|"reuse"|"move", source} … ],
  bytes_to_download, license, license_name, commercial_use,
  gated / login_required (with the deployment's token state),
  warnings: [ … ] }
```

- `id` is proposed from the file name (`engineIdFromFile`) and made unique against the catalogue
  by suffix; the person may edit it under 詳細 and nowhere else.
- `base_model` is the CP's reading (`engineFamilyGuess` on the upstream's metadata and name);
  when the CP cannot say, the plan says so and the family selector is the ONE field the card
  shows — with the candidates the provider knows, never a free string.
- `main_flag` and `files[]` come from `engineFamilyMainFlag` and `engineFamilyParts`; a part the
  ledger already holds is `reuse` (0 bytes), one at a wrong key is `move` (0 bytes), the rest
  `download`. The family's VAE joins `files[]` by the rule `engine_vae.go` applies today.
- The card shows the plan as one sentence per file with its cost, the licence with its
  commercial-use verdict, and one button, 取り込む. No role selector, no key field, no parts
  checkbox, no attach/replace choice. The form's 詳細 holds the id, the description and the
  generation defaults.
- `POST …/ingest` takes `{source, plan_token, license_accepted}` where `plan_token` is the
  plan's hash: the CP re-plans at start (the same rule the VAE read has today — the form's
  answer can be minutes old, and this is the call that spends money) and refuses with a fresh
  plan if anything material changed (bytes, licence, a key that became held).

A family with no part list (flux1, sd35, zimage, flux2-klein today) is planned as far as the CP
can: main file at the canonical key, parts listed as `unknown` with the roles the family reads,
and the card says which roles the person will have to assign afterwards. The plan never invents a
path (0072's 2026-09-15 addendum: "an entry written from memory is a 404 minutes after a press").

### Decision 5 — every refusal names the holder and the next act

The `apiError` for this area gains two structured fields:

```
{ code, message,
  holder?: { kind: "row"|"job"|"object"|"task", id, key },
  next?:   { act: "register"|"complete"|"replace"|"forget_row"|"dismiss_job"|"wait", target } }
```

A refusal in `engine_admin.go`, `engine_file_move.go`, `engine_family_parts.go` that is a 409
MUST carry both; a test enumerates the 409 sites and fails on one without them. The Console
renders `next` as the button on the error line. "The S3 key … is already recorded; choose a new
destination for this download or use verified reuse" becomes "held by the failed job of 03:42
— dismiss it" with a button that does.

### Decision 6 — jobs are the object's progress, not a second list

The job history tab goes. A `pending` / `running` job is its destination object's `uploading`
state in the ledger (with progress when the task reports it); a `failed` job is a `failed` entry
on that key with the task's message and one act, dismiss. A `done` job is provenance on the
object (`source`, `artifact_identity`) and nothing else — it is no longer "the last written
record of a key", because the bucket is, so the ack dialog before forgetting one goes with it.

#691's rule ("a `failed` job with no `task_arn` is not a holder") is kept as written and becomes
the definition of a holder: **an object, or a task that could have written one.**

`ListEngineIngestJobs`' fixed limit of 20 (`engine_ingest_perm.go:183`) goes with the tab; the
ledger is paginated by the bucket.

### Decision 7 — repair and cleanup start from the ledger, but the button is on the model

- A misplaced **main file** (an object under a path naming `checkpoints/` or `diffusion_models/`
  that no row declares) shows 登録 (decision 3's `register`). A misplaced main file a row
  DOES declare is fixed by that row's 揃える.
- A misplaced or orphan **part** shows no assign button. It is attached — and moved — by the
  揃える of whichever checkpoint reads it, and until then it shows only what it is, who (nobody)
  declares it, and 消す.
- `DELETE …/objects` `{key}` runs `MODE=delete` (the CP has no `s3:DeleteObject`, 0072
  decision 7) and is refused while any row declares the key (holder = that row). `DELETE
  …/models/{id}?purge=1` stays and is the same act applied to a row's keys.
- Row-side marks stay (`files_missing`, `vae_missing`, `base_model_missing`) because they answer
  "why can this row not be enabled"; `main_file_fix` goes as a wire field, because "misplaced"
  is the ledger's fact and 揃える reads the ledger.

### Decision 8 — what the screen is

One pane, two tabs, no wizard:

- **探す (search)** — as today. A card's button opens the plan (decision 4). One press.
- **登録済み (registered)** — the rows, as today, each with 有効にする / これで起動する / 揃える /
  編集 / 登録を消す. 揃える opens only when there is something to choose or to confirm (several
  candidates for a role, a download to pay for); otherwise it just acts. Below the rows,
  **バケツ (the ledger)**: every object with its state; a main file nobody declares carries 登録,
  an orphan carries 消す, everything else carries nothing. Orphans and misplaced objects sort
  first. This replaces the job history, the manual S3 form and the "register with this key"
  button.

The operation dialog keeps a manual entry (owner/repository or URL) for a model the search does
not find; it feeds the same plan.

## Alternatives rejected

- **A: only decision 1** (the CP decides the key; reuse becomes move). One PR, fixes the 400 —
  and leaves the dead button, the no-row-no-repair hole and the vocabulary. It is the first PR of
  this ADR, not a stopping point.
- **C: retire the job table now** (jobs become object attributes in the store). The right end
  state; it moves tenant visibility and the reconciler's ownership of tasks into a new table in
  the same release as the UI change. Decision 6 gets the screen there without the store change;
  the table can follow when it is the next thing in the way.
- **Keep six paths, improve the messages.** Decision 5's structured refusals would help every
  path — but the walls came from having paths at all, and a naive user should not learn which
  one they are on.
- **Compose the key in the Console with a better table.** The Console cannot know what is in the
  bucket; every rule about the key is a rule about the bucket.
- **Derive `enabled` from the ledger.** Still rejected (0072).
- **Write the manifest `<file>.json` and read it back.** Decision 2 of 0072 promised it; nothing
  writes it; the database already holds everything the manifest would. Listing the bucket needs
  no sidecar file.

## Consequences

- **Wire.** `POST …/ingest` loses `s3Key`, `reuse_s3_key`, `attach`, `replace`,
  `with_family_parts`, `with_family_vae`, `file_flag`; gains `plan_token`. New:
  `GET …/objects`, `POST …/objects/register`, `DELETE …/objects`, `POST …/models/{id}/complete`.
  Removed after one release: `GET …/storage`, `GET/DELETE …/ingest[/{id}]`,
  `POST …/models/{id}/parts`, `…/vae`, `…/models/vae-scan`. `main_file_fix` leaves the row.
  There is no third client: the Agent never calls admin routes, so the Console and CP change in
  one deployment and the one-release grace is for the operator's scripts.
  🔴 Removed at once on 2026-09-15 by the operator's decision, with no grace release (this is a
  development deployment only, with no third client and no scripts). "Removed after one release"
  above was carried out the same day, in P3.
- **IAM.** None. The CFN comment at `60-engines.yaml:292` is corrected.
- **Store.** No schema change in this ADR. `engine_ingest_jobs` stays as the task ledger the
  reconciler owns; the Console stops reading it directly.
- **Console.** `adminEngineAdd.tsx` (1,167 lines) and the halves of `adminEngineModels.tsx` it
  imports are rewritten around the plan and the ledger. Most of the 214 strings go.
- **Tests.** `contract_engine_keys_test.go` is deleted (the contract has one side now). New:
  the 409-carries-holder-and-next scan; the planner (family × ledger state → actions); the
  ledger's placement classification on the sandbox's real key list, pinned as a golden.
- **Docs.** ADR 0072's four 2026-09-15 addenda get one 🔴 line each pointing here. The member
  guide's engine page describes 探す → 取り込む → 有効にする and nothing else.

## Phases

- **P0** — merge #691 as is. Write this ADR (this document). **Clear af-sandbox rather than
  recover it**: the operator chose, on 2026-09-15, to discard the two families' bytes and their
  job rows and take them in again through the new road — that re-ingest is P2's acceptance run.
  Done the same day through the CP: the anima row purged, two throwaway rows registered over
  the six keys no row named and purged, the eight related jobs dismissed (about 24 GB,
  `MODE=delete`, three tasks). The only bytes left under `image/` are the ones the remaining
  rows declare.
- **P1 (CP)** — decisions 1, 2, 5, and `complete` / `register` (3). `POST …/ingest` accepts the old body one
  release with `s3Key` ignored when it equals the computed key and refused with `next` when it
  does not. Measure on af-sandbox: the ledger lists the 24 GB the operator could not see; 登録 on
  each misplaced main file rebuilds anima and krea2 with their parts, with no remembered id.
- **P2 (Console)** — decision 4 and 8: the plan card, the ledger under 登録済み, the old
  surfaces removed. Measure: a person who has not read this ADR searches "anima", presses once,
  enables, generates.
- **P3** — decisions 6 and 7's remainder; remove the grace routes; the docs.
  🔴 Done at once on 2026-09-15 by the operator's decision (the CP half): the grace routes and the
  grace fields are gone and `plan_token` is required.

## Open questions

1. **Who may register and complete.** Today `postModel` is super_admin and the ingest is
   granted-tenant. `register` creates rows from bytes the operator owns; the draft says
   super_admin only, with the granted tenant able to register objects its own jobs produced.
   `complete` follows the ingest's grant. To be confirmed before P1.
2. **`plan_token` versus re-sending the plan.** A hash is small and forgery-proof; re-sending the
   plan is debuggable. Hash, unless P1 finds it hides a diff the operator needed to see.
3. **The llm role.** The ledger applies as is (`llm/*`, one directory, shards under
   `llm/<name>/`), with `placement` always `ok`. Nothing in this ADR changes llm behaviour; the
   screen gains the ledger there too.
   🔴 **Resolved on 2026-09-19, and "nothing changes" was the wrong half of it.** Listing the llm
   prefix needed no special case and got none, but **decision 3's one object-side act did**: 登録
   was spelled as ComfyUI's two loader directories, so the three GGUFs an operator reported under
   `llm/` — bytes this deployment is paying for, that no row declared — were each drawn with 消す
   and nothing else. The list without the act is the fault this ADR was written around, one role
   later. What a model's own weights ARE is now asked of the role: the image layout is the loader
   directory at any depth, the llm layout is flat (`llm/<file>.gguf` and nothing deeper, so
   `llm/loras/…` is an adapter and `llm/<name>/shard.gguf` is one piece of a file). The press
   reads the GGUF header out of the same bytes, for the reason ADR 0074's follow-up gives.
   🔴 Found by the same pass and older than it: **揃える composed its destination with ComfyUI's
   layout whatever the role**, so every llm row was planned a move to `llm/checkpoints/…` — a
   directory `llama-server` never looks in. The repair press would have taken a working model out
   of service. The destination is the ROLE's layout now (`engineIngestKeyFor`); for the image role
   the two are the same function.
4. **Why the Console's 履歴を消す did not reach the CP on 2026-09-15** while the same `DELETE`
   answered 200 from curl. Unexplained; the tab goes in P2, but if the cause is in `apiJSON` or
   the pane it will bite the new buttons too. To be reproduced headless before P2.
