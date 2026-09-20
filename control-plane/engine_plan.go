package main

// engine_plan.go — what one press of 取り込む will do, decided by the Control Plane
// (ADR 0085 decisions 1 and 4).
//
// Three parties used to decide where a file lands: the form composed the destination key, the CP
// recomputed it and refused a mismatch, and a verified reuse brought in a third — the key some
// earlier job had recorded. Every disagreement between them reached the operator as a refusal
// (`s3Key must be empty or identical`) or, worse, as a file in a directory no ComfyUI loader
// enumerates, which nothing reports until somebody waits out a cold start. The key is a pure
// function of (role, flag, base name), so exactly one party computes it, and it is the one that
// also has to check it.
//
// A plan is that decision, priced, before anything is spent: which files this row needs, at which
// keys, and for each one whether the bytes must be downloaded, are already at the right key
// (`reuse`, 0 bytes) or are here under a name no loader lists (`move`, 0 bytes and a server-side
// `aws s3 mv`). The form shows it and sends back `plan_token`; the CP re-plans at the press and
// refuses a plan that no longer holds, because the answer on the screen can be minutes old and
// this is the call that spends money.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The three things a press can do to one file, and the only three. `reuse` and `move` both cost
// nothing to download: the bytes are already this deployment's.
const (
	enginePlanDownload = "download"
	enginePlanReuse    = "reuse"
	enginePlanMove     = "move"
)

// enginePlanFile is one file the press will put where a loader can find it.
type enginePlanFile struct {
	Flag  string `json:"flag"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	// Action is one of the three constants above.
	Action string `json:"action"`
	// Source is where the bytes come FROM and Key is where they end up. For a download that is
	// the upstream label (`hf:owner/repo/file`); for a reuse and a move it is the S3 key they
	// occupy TODAY — the same key for a reuse, another one for a move. One field with two
	// meanings on purpose: the move's origin then rides inside the plan's hash, so an object that
	// wanders between the form and the press makes the token stale instead of being moved from a
	// key it left.
	Source string `json:"source"`
	Key    string `json:"key"`

	// The plan's working notes, deliberately off the wire and therefore out of the token: the
	// upstream read this line was priced from, and the deployment's own record of the bytes when
	// there is one. Both are what the press needs and neither is a fact the operator decides on.
	resolved engineResolved
	known    *engineKnownArtifact
	// conflict is non-empty when the destination key is already recorded by something this plan
	// cannot prove is the same file. Set only on download parts; carried into the follow-up spec
	// so followUpFile skips the download rather than racing a refused RunTask in the reconciler.
	conflict string
}

// enginePlan is the whole answer for one press.
//
// `warnings` is where a family whose parts nobody has measured says so. It never invents a path:
// ADR 0072's 2026-09-15 addendum measured what that costs — an entry written from memory is a 404
// minutes after a press — so the plan names the ROLE the person will still have to supply and
// stops there.
type enginePlan struct {
	PlanToken string `json:"plan_token"`
	ID        string `json:"id"`
	BaseModel string `json:"base_model,omitempty"`
	// BaseModelCandidates is what the provider dispatches on, offered only when the CP cannot say
	// which family this is. A closed list rather than a free string: storing an upstream display
	// name as a family is what ADR 0072 decision 2 forbids, and it produced rows that looked
	// complete and refused to generate.
	BaseModelCandidates []string         `json:"base_model_candidates,omitempty"`
	MainFlag            string           `json:"main_flag"`
	Files               []enginePlanFile `json:"files"`
	BytesToDownload     int64            `json:"bytes_to_download"`
	Warnings            []string         `json:"warnings"`
}

// token is the plan's fingerprint: sha256 of its own normalised JSON, first 16 hex characters.
//
// Hashed rather than echoed back (ADR 0085 open question 2): the Console re-sends 16 bytes it
// cannot forge, and the CP compares them against a plan it made itself a moment ago. The field
// itself is cleared before hashing, so the value never depends on what it is about to become.
//
// ID is excluded from the hash because it names what the row will be called — a decision the
// operator makes at the press (details form), not part of what the plan priced. The postIngest
// route has its own duplicate-id check; hashing the id here would make every edit to it a 409.
func (p enginePlan) token() string {
	bare := p
	bare.PlanToken = ""
	bare.ID = ""
	raw, err := json.Marshal(bare)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// main is the file the row is named after — the first line of every plan.
func (p enginePlan) main() (enginePlanFile, bool) {
	if len(p.Files) == 0 {
		return enginePlanFile{}, false
	}
	return p.Files[0], true
}

// enginePlanFor works out what taking `b` in would do, without doing any of it.
//
// It answers a second value: what the checkpoint's own header said about the VAE
// (engine_safetensors.go). The resolve route puts that on screen and the press writes it onto the
// file, and reading it HERE is what keeps one header read serving both.
func (a engineAdminAPI) enginePlanFor(ctx context.Context, g engineIngestGrant, e *engineRuntimeState,
	b engineIngestBody, res engineResolved) (enginePlan, string) {
	role, provider := e.def.Key, e.def.Provider
	images := e.def.api() == engineAPIImages
	lora := strings.EqualFold(strings.TrimSpace(b.Kind), engineModelKindLora)
	rows := e.catalog.list(ctx)

	plan := enginePlan{Files: []enginePlanFile{}, Warnings: []string{}}
	// ⚠️ The operator's declaration first, the CP's reading of the upstream second, and the
	// upstream's own string only where nothing dispatches on it. Hugging Face and Civitai publish
	// a display name — "SDXL 1.0", "Flux.1 D" — which is descriptive metadata and not the key the
	// comfy provider picks a workflow with (ADR 0072 decision 2).
	base := strings.TrimSpace(b.BaseModel)
	if base == "" {
		base = enginePlanFamily(provider, b.Source, res)
	}
	if base == "" && engineBaseModelValid(provider, res.BaseModel) {
		base = strings.TrimSpace(res.BaseModel)
	}
	if base != "" {
		plan.BaseModel = base
	} else {
		plan.BaseModelCandidates = engineBaseModelsFor(provider)
	}
	// What role the file being taken in plays. A split family reads no whole checkpoint, so for
	// those the main file is the family's own weights and the empty flag is not an answer at all
	// — which is the mistake every Anima row on af-sandbox was made with, because the form
	// offered "the whole checkpoint" first.
	if !lora {
		plan.MainFlag = engineFamilyMainFlag(base)
	}

	name := engineUpstreamFileName(b.Source, res)
	plan.ID = enginePlanID(b.ID, name, rows)

	held := a.enginePartsHeld(ctx, g, role)
	plan.Files = append(plan.Files, enginePlanLine(ctx, held, plan.MainFlag, name,
		engineIngestKeyFor(role, images, plan.MainFlag, name, lora), res))

	// The family's other files, from the table of what each one reads (engine_family_parts.go).
	// Every one of them is planned the same way the main file is, so a part this deployment
	// already holds is a declaration rather than a second download — the Qwen-Image VAE is shared
	// by two families and the second row of either must not pay for it twice.
	//
	// Row listing is deferred until the first download part: all-reuse plans (the normal state
	// once a shared part is in the bucket) never list the catalogue at all.
	// On error the check is skipped and a warning is emitted (warn-and-skip design, not hard fail:
	// the plan is readable but the destination-overlap guarantee is suspended for this resolve).
	// Cost: at most 1 ListEngineModels(all roles) + 1 EngineIngestJobForS3Key + 1 HeadObject per
	// download part whose destination is already recorded.
	var (
		allRows         []store.EngineModel
		rowsFetched     bool
		rowsFetchOK     bool
	)
	for _, p := range engineFamilyPartsFor(base) {
		pres, aerr := engineIngestResolve(ctx, engineIngestSource{HF: &engineIngestHF{Repo: p.Repo, File: p.File}})
		if aerr != nil {
			// Said out loud rather than dropped: a part whose upstream could not be read is one
			// this press cannot promise, and a plan that quietly omitted it would report a row as
			// complete that is still refused at generation.
			plan.Warnings = append(plan.Warnings, p.Repo+"/"+p.File+" could not be read, so "+
				p.Flag+" will have to be attached to this row afterwards")
			continue
		}
		pname := engineBaseName(p.File)
		pkey := engineIngestKeyFor(role, images, p.Flag, pname, false)
		pf := enginePlanLine(ctx, held, p.Flag, pname, pkey, pres)
		if pf.Action == enginePlanDownload && a.mgr != nil && a.mgr.store != nil {
			if !rowsFetched {
				rowsFetched = true
				var err error
				allRows, err = a.mgr.store.ListEngineModels(ctx, "")
				if err != nil {
					plan.Warnings = append(plan.Warnings,
						"part destination check could not list the catalogue; existing rows were not verified")
				} else {
					rowsFetchOK = true
				}
			}
			if rowsFetchOK {
				if ref := enginePartDestinationCheck(ctx, allRows, a.mgr.store,
					a.engineStorageBytes(), role, pkey); ref != nil {
					pf.conflict = ref.message
					plan.Warnings = append(plan.Warnings, partDestinationWarning(p.Flag, pkey, ref))
				}
			}
		}
		plan.Files = append(plan.Files, pf)
	}

	// And the one header read that decides whether this row could decode a picture at all: does
	// the checkpoint carry the VAE its family reads (ADR 0072 follow-up). Asked of a family that
	// reads a WHOLE checkpoint only — a split family's main file is a diffusion model and the
	// question is meaningless for it, and the families that answer it (engineFamilyVaes) are all
	// of the first kind.
	vae := engineVaeUnknown
	if main, ok := plan.main(); ok && plan.MainFlag == "" {
		vae = engineVaeOfIngest(ctx, provider, b.Kind, main.Key, res, a.hfTokens())
	}
	if vae == engineVaeNo && !enginePlanHasFlag(plan.Files, "--vae") {
		if v, ok := engineFamilyVaes[base]; ok {
			vres, aerr := engineIngestResolve(ctx, engineIngestSource{HF: &engineIngestHF{Repo: v.Repo, File: v.File}})
			if aerr != nil {
				plan.Warnings = append(plan.Warnings, v.Repo+"/"+v.File+" could not be read, so this "+
					base+" checkpoint will have no VAE until one is attached")
			} else {
				vname := engineBaseName(v.File)
				plan.Files = append(plan.Files, enginePlanLine(ctx, held, "--vae", vname,
					engineIngestKeyFor(role, images, "--vae", vname, false), vres))
			}
		}
	}

	// What the family reads and this plan cannot supply. The roles are named and no path is
	// guessed: flux1, sd35, zimage and flux2-klein have no measured part list, and the honest
	// answer is which slots the person will be filling by hand.
	for _, want := range engineComfyRequiredFlags[base] {
		if enginePlanHasFlag(plan.Files, want) {
			continue
		}
		plan.Warnings = append(plan.Warnings, "the "+base+" family also reads a "+engineFlagLabel(want)+
			" file and this deployment has no measured source for it: that role has to be supplied afterwards")
	}

	for _, f := range plan.Files {
		if f.Action == enginePlanDownload {
			plan.BytesToDownload += f.Bytes
		}
	}
	plan.PlanToken = plan.token()
	return plan, vae
}

// enginePlanFamily is the CP's reading of which family this is — "the upstream's metadata AND its
// name" (ADR 0085 decision 4), through the one translator that only ever answers a member of the
// provider's own vocabulary (engine_family_guess.go).
//
// 🔴 The name is consulted because the metadata often says nothing. Measured on the two families
// this ADR was written for: `circlestone-labs/Anima` and `Comfy-Org/Krea-2` publish no
// `cardData.base_model` at all, so a reading from metadata alone leaves the family empty — and an
// empty family is the one field the plan then has to ask a person for, on the very screen this
// ADR exists to stop asking questions on.
//
// The repository's last segment first, because it is the product's name; then the whole path,
// then the filename. A hint that recognises nothing contributes nothing: the guess returns "" and
// the plan offers the vocabulary instead of inventing a family.
func enginePlanFamily(provider string, src engineIngestSource, res engineResolved) string {
	hints := []string{res.BaseModel}
	if src.HF != nil {
		repo := strings.Trim(strings.TrimSpace(src.HF.Repo), "/")
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			hints = append(hints, repo[i+1:])
		}
		hints = append(hints, repo)
	}
	hints = append(hints, engineIDFromFile(engineUpstreamFileName(src, res)))
	for _, h := range hints {
		if fam := engineFamilyGuess(provider, h); fam != "" {
			return fam
		}
	}
	return ""
}

// enginePlanLine prices ONE file against what the deployment already holds.
func enginePlanLine(ctx context.Context, held enginePartsHeld, flag, name, key string,
	res engineResolved) enginePlanFile {
	f := enginePlanFile{
		Flag: flag, Name: name, Bytes: res.Bytes, Action: enginePlanDownload,
		Source: res.Source, Key: key, resolved: res,
	}
	at, known, ok := engineLedgerLookup(ctx, held, res.ArtifactIdentity)
	if !ok {
		return f
	}
	f.Source, f.known = at, known
	if at == key {
		f.Action = enginePlanReuse
	} else {
		f.Action = enginePlanMove
	}
	return f
}

// engineLedgerLookup answers "where in this bucket are these exact bytes already".
//
// 🔴 This is the seam ADR 0085 decision 2's ledger (`GET …/objects`, lane A) replaces, and it is
// one function so that the replacement is one function. What it joins today is what the CP has:
// the keys the catalogue and the finished jobs record, with the upstream identity each was
// verified against before upload (engineKnownArtifacts), and one HeadObject proving bytes occupy
// that key NOW. Neither proof substitutes for the other — a record with no object is a key whose
// bytes were purged, and an object with no record is bytes nobody can state the provenance of.
//
// The scan is in key order because the plan's token is a hash of its own answer: a map iteration
// would make the same bucket produce two different plans and every second press go stale.
func engineLedgerLookup(ctx context.Context, held enginePartsHeld, identity string) (string, *engineKnownArtifact, bool) {
	identity = strings.TrimSpace(identity)
	if identity == "" || held.known == nil || held.storage == nil {
		return "", nil, false
	}
	keys := make([]string, 0, len(held.known))
	for k := range held.known {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := held.known[k]
		switch {
		case a == nil, a.Ambiguous, a.InFlight, !a.Reusable:
			continue
		case a.ArtifactIdentity != identity:
			continue
		}
		if held.storage.verify(ctx, k).State != engineStoragePresent {
			continue
		}
		return k, a, true
	}
	return "", nil, false
}

func enginePlanHasFlag(files []enginePlanFile, flag string) bool {
	for _, f := range files {
		if f.Flag == flag {
			return true
		}
	}
	return false
}

// engineIngestKeyFor is where a file of this role belongs, and the CP is the only party that
// answers it (ADR 0085 decision 1).
//
// 🔴 The layout follows the ROLE, not the provider. The image role is ComfyUI's, one directory
// per loader (engineComfyKeyFor) — and a provider with no file vocabulary at all (sdcpp) still
// stages its one whole file under `image/checkpoints/`, which is where its own loader looks and
// where every such row already is. The llm role is flat under its own prefix, with adapters in
// `llm/loras/`: that is what the preset points at, file by file (ADR 0072 decision 5).
func engineIngestKeyFor(role string, images bool, flag, file string, lora bool) string {
	if images {
		return engineComfyKeyFor(role, flag, file, lora)
	}
	name := engineBaseName(file)
	if lora {
		return strings.TrimSpace(role) + "/loras/" + name
	}
	return strings.TrimSpace(role) + "/" + name
}

// engineBaseName is the file's own name with every directory above it dropped. The layout is flat
// on purpose: each ComfyUI loader enumerates ONE directory and the Agent names a file by its base
// name, so a key that kept the upstream's `split_files/…` is a name no loader can ever offer.
func engineBaseName(file string) string {
	name := strings.TrimSpace(file)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// engineUpstreamFileName is what the file is CALLED upstream, which is what the destination key
// and the proposed id are both built from.
//
// The source's own field first; then the artifact identity, which carries the chosen filename for
// all three sources (`hf:repo@rev/path#…`, `civitai:<version>/<name>#…`, `url:<url>#…`) and is
// the only place a Civitai version's name appears when the request did not pick one; and the
// download URL last, which for Civitai is a numeric endpoint with no name in it at all.
func engineUpstreamFileName(src engineIngestSource, res engineResolved) string {
	switch {
	case src.HF != nil && strings.TrimSpace(src.HF.File) != "":
		return engineBaseName(src.HF.File)
	case src.Civitai != nil && strings.TrimSpace(src.Civitai.File) != "":
		return engineBaseName(src.Civitai.File)
	}
	if id := strings.TrimSpace(res.ArtifactIdentity); id != "" {
		if at := strings.Index(id, "#"); at >= 0 {
			id = id[:at]
		}
		if name := engineBaseName(id); name != "" {
			return name
		}
	}
	u := strings.TrimSpace(res.DownloadURL)
	if at := strings.IndexAny(u, "?#"); at >= 0 {
		u = u[:at]
	}
	return engineBaseName(u)
}

// engineIDExtRe and engineIDQuantRe are the two things a catalogue id is not: the container
// format, and the quantisation tag — which names the FILE rather than the model, so the same
// model at q4 and q8 proposes one id.
var engineIDExtRe = regexp.MustCompile(`(?i)\.(safetensors|gguf|ckpt|pt|sft|bin)$`)

// engineIDFromFile proposes a catalogue id from the name of the file that was picked. The same
// derivation the Console offered while the person typed one (ADR 0085 decision 4 moves it here,
// because the id also has to be unique against a catalogue the form cannot see).
//
// A proposal, never a decision: the person may edit it under 詳細 (details). Deriving
// `sdxl-base-1.0` from `sd_xl_base_1.0` is where this would stop being derivation and start being
// guessing, and it is left to them.
//
// 🔴 The QUANTISATION is kept, and used to be stripped (ADR 0090). The old rule said "the
// quantisation names the file, not the model", which was true while a deployment held one size of
// a model — and stopped being true the moment ADR 0089 made taking in a second size one press.
// Measured on unsloth/Qwen3.8-27B-GGUF: all four of `UD-IQ2_XXS`, `UD-IQ2_S`, `UD-Q2_K_XL` and
// `UD-IQ4_XS` proposed the SAME `qwen3.8-27b-ud`, so the second row taken in became
// `qwen3.8-27b-ud-2` — a numeric suffix in place of the one fact that tells them apart, in the id
// the launch menu shows a member.
func engineIDFromFile(file string) string {
	return strings.ToLower(engineIDExtRe.ReplaceAllString(engineBaseName(file), ""))
}

// enginePlanIDMax bounds the suffix search. A hundred rows of one name is not a catalogue anybody
// is reading, and an unbounded loop here would be a request that never answers.
const enginePlanIDMax = 100

// enginePlanID is the id the row will be created under: what the request asked for, or a proposal
// made unique against the catalogue by suffix.
//
// 🔴 Deterministic to the last branch. The plan's token is a hash of this answer, so an id that
// depended on a clock or a random suffix would make every press stale.
func enginePlanID(asked, file string, rows []store.EngineModel) string {
	if id := strings.TrimSpace(asked); id != "" {
		return id
	}
	base := engineIDFromFile(file)
	if base == "" {
		base = "model"
	}
	taken := make(map[string]bool, len(rows))
	for _, m := range rows {
		taken[m.ID] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; n < enginePlanIDMax; n++ {
		if try := base + "-" + strconv.Itoa(n); !taken[try] {
			return try
		}
	}
	return base + "-" + strconv.Itoa(enginePlanIDMax)
}

// partDestinationWarning builds the one-line warning for a part whose destination key is already
// held, using the holder and next-step carried in ref. The key appears once (in "lands at"),
// and the action the operator needs differs by holder kind: a row is completed or rerouted,
// a job is dismissed (bytes present) or awaited (bucket unverifiable).
func partDestinationWarning(flag, s3key string, ref *apiRefusal) string {
	if ref == nil || ref.Holder == nil || ref.Next == nil {
		return flag + " lands at " + s3key + ", which is already recorded; free that slot before pressing"
	}
	switch ref.Holder.Kind {
	case "row":
		return flag + " lands at " + s3key + ", which the row " + ref.Holder.ID +
			" already declares; complete that row or choose another destination"
	case "job":
		if ref.Next.Act == "dismiss_job" {
			return flag + " lands at " + s3key + ", which an earlier ingest job (" +
				ref.Holder.ID + ") recorded; dismiss that job first"
		}
		return flag + " lands at " + s3key + ", which an ingest job (" + ref.Holder.ID +
			") recorded; the bucket could not confirm whether bytes are still there — press again once the bucket answers"
	default:
		return flag + " lands at " + s3key + ", which is already recorded; free that slot before pressing"
	}
}
