package main

// engine_complete.go — the row is the subject (ADR 0085 decision 3).
//
// One request closes the gap between what a family READS and what a row HAS, using what the
// ledger (engine_objects.go) already holds:
//
//	the family's roles − the row's roles = the gap
//	  at the canonical key            → declare, 0 bytes
//	  present at another key          → move (MODE=move), then declare
//	  the row's own weights misplaced → move (what `main_file_fix` was, now one case of four)
//	  not held at all                 → take in from the family's part table
//	  no part table                   → unknown, naming the role a person still has to supply
//
// 🔴 Why the row and never the part. The operator said it in one line on 2026-09-15: choosing a
// destination from a part is backwards — a checkpoint is what a person means, and the parts that
// fit it are what the machine knows. So a part object gets no button of its own anywhere; its
// only acts are the ones a row performs on it. The one place a person ever picks a part is a
// row's 揃える dialog with several candidates for one role, and they pick it FOR this checkpoint.
//
// This is what `POST …/models/{id}/parts` and `…/vae` fold into. Both stay one release
// (ADR 0085 Consequences) and both now answer from this planner.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// What the whole press did. Five words rather than a bool because they send the reader to five
// different places: `moving` finishes in minutes with no bytes crossing the internet and
// `job_started` does not, which a panel that called both "downloading" would have an operator
// waiting for a transfer that is never going to appear.
const (
	engineCompleteNone       = "none"
	engineCompleteAttached   = "attached"
	engineCompleteMoving     = "moving"
	engineCompleteJobStarted = "job_started"
	engineCompleteChoose     = "choose"
	engineCompleteUnknown    = "unknown"
)

// What one missing role's press will do.
const (
	engineCompleteActDeclare  = "declare"
	engineCompleteActMove     = "move"
	engineCompleteActDownload = "download"
	engineCompleteActChoose   = "choose"
	engineCompleteActUnknown  = "unknown"
)

// engineCompleteBody is one press of 揃える.
type engineCompleteBody struct {
	// Check asks what WOULD happen. The panel calls it first so the size and the licence are on
	// screen before the press that spends them — the same two-step the ingest form follows.
	Check bool `json:"check"`
	// Choices is the person's pick for a role with several candidates, by flag. The only place a
	// part is ever chosen, and it is chosen FOR this checkpoint.
	Choices map[string]string `json:"choices"`
	// Replace allows a pick to take a slot the row already fills. Its own field rather than
	// being implied by naming a filled flag: swapping a text encoder changes what an enabled row
	// loads at the next cold start, and that is not something a mis-click should do.
	Replace bool `json:"replace"`
	// LicenseAccepted is the human act, asked for once for the whole set and only when something
	// is actually going to be downloaded — the declared parts were accepted when they were taken
	// in, and asking again for bytes this deployment already owns teaches people to click through
	// dialogs.
	LicenseAccepted bool `json:"license_accepted"`
}

// engineCompleteCandidate is one object a role could be filled from.
type engineCompleteCandidate struct {
	Key    string `json:"key"`
	Source string `json:"source,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// engineCompleteFile is one role's answer.
type engineCompleteFile struct {
	Flag   string `json:"flag"`
	Action string `json:"action"`
	Key    string `json:"key,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Source string `json:"source,omitempty"`
	// Candidates is set only for `choose`, and when it is set nothing happens at all: a press
	// that picked one of several parts on the operator's behalf is the "the default choice was
	// the only one that cannot work" fault ADR 0072's addendum ends with.
	Candidates []engineCompleteCandidate `json:"candidates,omitempty"`
}

// engineCompleteAnswer is the whole press, and it is the same shape for `check` and for the act.
type engineCompleteAnswer struct {
	Action string               `json:"action"`
	Files  []engineCompleteFile `json:"files"`
	// BytesToDownload counts only what will cross the internet. A move is 0 and a declaration is
	// 0, which is the number that makes the difference between the two visible at all.
	BytesToDownload int64            `json:"bytes_to_download"`
	Jobs            []map[string]any `json:"jobs"`
}

// engineCompleteStep is one file's plan with everything the act needs, and it is deliberately
// bigger than its wire form: the wire says what will happen, this carries what to call.
type engineCompleteStep struct {
	wire engineCompleteFile
	// from is the key the bytes are at now, for a move.
	from string
	// to is the canonical key the declaration will name.
	to string
	// object is the ledger row a declare or a move reads its size and provenance off. Nil for
	// the row's OWN weights, which the row already declares — there the provenance comes off the
	// declaration rather than off the bucket.
	object *engineObjectRow
	// file is the declaration this step will write, at the key it will end up at. Carried whole
	// rather than rebuilt from the wire fields: `artifact_identity` is the one thing a move must
	// not drop (a gated repository or a Civitai version taken down can never re-derive it) and it
	// has no wire field of its own here.
	file store.EngineModelFile
	// part is the family's declaration, for a download.
	part *engineFamilyPart
	// resolved is that part's upstream, priced while somebody is still looking at the screen —
	// the download itself is started from here, never from the reconciler.
	resolved engineResolved
	// replaceSlot swaps a file the row already holds instead of adding one.
	replaceSlot bool
}

// engineCompleteMainFix is the row's OWN weights, when they are somewhere no loader lists.
//
// engineMainFileFixFor answers this for a SPLIT family, where the repair also gives the file its
// role flag. This widens it by one case that family cannot express: a whole-checkpoint family
// (sd15 / sdxl / sd35) and a LoRA read the UNFLAGGED slot, so there is no flag to gain — and
// their bytes land under `image/checkpoints/split_files/…` exactly the same way, from exactly the
// same form. Without this the ledger would report `misplaced` on a row whose 揃える answered
// `none`, which is the shape of report this whole ADR is written around.
func engineCompleteMainFix(role, family string, m store.EngineModel) (engineMainFileFix, bool) {
	if fix, ok := engineMainFileFixFor(role, family, m); ok {
		return fix, true
	}
	if engineFamilyMainFlag(family) != "" {
		return engineMainFileFix{}, false
	}
	whole, ok := engineVaeCheckpoint(m)
	if !ok {
		return engineMainFileFix{}, false
	}
	from := strings.TrimSpace(whole.S3Key)
	to := engineComfyKeyFor(role, "", from, engineModelIsLora(m))
	if to == from {
		return engineMainFileFix{}, false
	}
	moved := whole
	moved.S3Key = to
	return engineMainFileFix{Flag: "", From: from, To: to, File: moved}, true
}

// engineCompleteMissing is the gap, by flag and in the family's own order.
//
// The family VAE joins it by the rule engine_vae.go applies today: a single-checkpoint family
// whose checkpoint was READ and carries none. That is not a `required flag` — sd15's template
// reads the checkpoint's own third output — so set subtraction alone would answer `none` for a
// row that cannot generate a picture, which is the fault `vae_missing` exists to mark.
func engineCompleteMissing(provider string, m store.EngineModel, skip string) []string {
	have := map[string]bool{}
	for _, f := range m.Files {
		if strings.TrimSpace(f.S3Key) != "" {
			have[strings.TrimSpace(f.Flag)] = true
		}
	}
	var out []string
	for _, flag := range engineComfyRequiredFlags[strings.TrimSpace(m.BaseModel)] {
		if flag == "" || have[flag] || flag == skip {
			continue // the unflagged slot is the row's own checkpoint, not a part
		}
		out = append(out, flag)
	}
	if engineVaeMissing(provider, m) && !have["--vae"] && !slicesContains(out, "--vae") {
		out = append(out, "--vae")
	}
	return out
}

func slicesContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// engineCompleteParts is the family's declared files by flag, from the two tables that hold them:
// the split family's part list and the single-checkpoint family's VAE.
func engineCompleteParts(family string) map[string]engineFamilyPart {
	out := map[string]engineFamilyPart{}
	for _, p := range engineFamilyPartsFor(family) {
		out[p.Flag] = p
	}
	if v, ok := engineFamilyVaes[strings.TrimSpace(family)]; ok {
		if _, taken := out["--vae"]; !taken {
			out["--vae"] = engineFamilyPart{Flag: "--vae", Repo: v.Repo, File: v.File, S3Key: v.S3Key}
		}
	}
	return out
}

// engineCompletePlan is the planner, and it is deliberately a function of (row, ledger, choices)
// alone: the family × ledger-state table is what a test can enumerate, and every act below reads
// its arguments off the steps it returns.
//
// ctx is used for ONE thing — pricing a part that has to be downloaded — and only for the roles
// the ledger could not fill. A row whose parts are all here reaches no network at all.
func engineCompletePlan(ctx context.Context, role, provider string, m store.EngineModel,
	l *engineLedger, b engineCompleteBody) ([]engineCompleteStep, *apiRefusal) {
	family := strings.TrimSpace(m.BaseModel)
	lora := engineModelIsLora(m)
	var steps []engineCompleteStep

	// The weights first. A row whose parts attach while its own checkpoint stays unreadable is
	// still a row nobody can enable, and the move is the slow half (a server-side copy of up to
	// 13 GB), so it is planned and started before the cheap writes rather than after them.
	fix, hasFix := engineCompleteMainFix(role, family, m)
	if hasFix {
		steps = append(steps, engineCompleteStep{
			wire: engineCompleteFile{Flag: fix.Flag, Action: engineCompleteActMove,
				Key: fix.To, Bytes: fix.File.Bytes, Source: fix.File.Source},
			from: fix.From, to: fix.To, file: fix.File,
		})
	}
	skip := ""
	if hasFix {
		skip = fix.Flag
	}
	missing := engineCompleteMissing(provider, m, skip)
	parts := engineCompleteParts(family)
	// Which loader directories more than one missing role shares. 🔴 This is what keeps a press
	// from putting a T5 encoder in CLIP-L's slot: `text_encoders/` is one directory for three
	// roles, so when a family with no part list is missing two of them, "the one object in that
	// directory" is not an answer — it is a coin toss the row cannot recover from.
	perDir := map[string]int{}
	for _, flag := range missing {
		perDir[engineComfyRoleDir(flag, lora)]++
	}
	used := map[string]bool{}
	for _, f := range m.Files {
		used[strings.TrimSpace(f.S3Key)] = true
	}
	for _, flag := range missing {
		part, hasPart := parts[flag]
		var partPtr *engineFamilyPart
		if hasPart {
			p := part
			partPtr = &p
		}
		step, aerr := engineCompleteStepFor(ctx, role, lora, flag, partPtr, l,
			perDir[engineComfyRoleDir(flag, lora)] > 1, strings.TrimSpace(b.Choices[flag]), used)
		if aerr != nil {
			return nil, aerr
		}
		if step.to != "" {
			used[step.to] = true
		}
		if step.from != "" {
			used[step.from] = true
		}
		steps = append(steps, step)
	}
	// A pick on a slot the row already fills. The same dialog on a filled slot IS the swap
	// (today's `replace`), and the refusal without `replace` is what carries `next` to the
	// Console's button.
	swaps, aerr := engineCompleteSwaps(role, lora, m, l, b)
	if aerr != nil {
		return nil, aerr
	}
	return append(steps, swaps...), nil
}

// engineCompleteStepFor resolves one missing role against the ledger.
func engineCompleteStepFor(ctx context.Context, role string, lora bool, flag string,
	part *engineFamilyPart, l *engineLedger, shared bool, chosen string,
	used map[string]bool) (engineCompleteStep, *apiRefusal) {
	dir := strings.TrimSuffix(engineComfyRoleDir(flag, lora), "/")
	// 1. The person said which one. Their pick wins over every table below it: they are looking
	//    at the row and at the file, and this is the one place a part is ever chosen.
	if chosen != "" {
		object := l.present(chosen)
		if object == nil {
			return engineCompleteStep{}, refuse(http.StatusConflict, errCodeEngineBadBody,
				"the ledger holds nothing usable at "+chosen+" for "+flag,
				&apiHolder{Kind: "object", Key: chosen}, nil)
		}
		return engineCompleteDeclareOrMove(role, lora, flag, object), nil
	}
	if part != nil {
		// 2. The family names the file and the ledger holds it at the canonical key. One write,
		//    nothing downloaded — the common outcome for a deployment already running a family
		//    whose VAE is shared (anima and krea2 read the same Qwen-Image one).
		if object := l.present(part.S3Key); object != nil && !used[part.S3Key] {
			return engineCompleteStep{
				wire: engineCompleteFile{Flag: flag, Action: engineCompleteActDeclare,
					Key: part.S3Key, Bytes: object.Bytes, Source: object.Source},
				to: part.S3Key, object: object, file: engineCompleteDeclaration(flag, part.S3Key, object),
			}, nil
		}
		// 3. The same file, at a key no loader lists. Moved rather than fetched again: the bytes
		//    are this deployment's and already paid for.
		if cands := engineCompleteUnused(l.sameBaseName(path.Base(part.S3Key), dir), used); len(cands) == 1 {
			return engineCompleteDeclareOrMove(role, lora, flag, cands[0]), nil
		} else if len(cands) > 1 {
			return engineCompleteChooseStep(flag, cands), nil
		}
		// 4. Not here. The family says where it comes from, so this is a download with a price
		//    and a licence, both read while somebody is still at the screen.
		res, aerr := engineIngestResolve(ctx, engineIngestSource{HF: &engineIngestHF{Repo: part.Repo, File: part.File}})
		if aerr != nil {
			// Said out loud rather than dropped: a part whose upstream could not be read is one
			// this press cannot promise, and an offer that quietly omitted it would leave the row
			// still incomplete after a button that said it would complete it.
			return engineCompleteStep{
				wire: engineCompleteFile{Flag: flag, Action: engineCompleteActUnknown, Key: part.S3Key},
			}, nil
		}
		p := *part
		return engineCompleteStep{
			wire: engineCompleteFile{Flag: flag, Action: engineCompleteActDownload,
				Key: part.S3Key, Bytes: res.Bytes, Source: res.Source},
			to: part.S3Key, part: &p, resolved: res,
			// The download's own declaration is written by the job when it lands
			// (engineIngester.install), not here.
		}, nil
	}
	// 5. No part list for this family (flux1, sd35, zimage, flux2-klein today). What the CP can
	//    still do is offer what is in the role's directory — and refuse to guess when the
	//    directory serves more than one of the roles this row is missing.
	cands := engineCompleteUnused(l.inDir(dir), used)
	switch {
	case len(cands) == 0:
		return engineCompleteStep{
			wire: engineCompleteFile{Flag: flag, Action: engineCompleteActUnknown},
		}, nil
	case shared || len(cands) > 1:
		return engineCompleteChooseStep(flag, cands), nil
	default:
		return engineCompleteDeclareOrMove(role, lora, flag, cands[0]), nil
	}
}

func engineCompleteUnused(rows []*engineObjectRow, used map[string]bool) []*engineObjectRow {
	out := make([]*engineObjectRow, 0, len(rows))
	for _, row := range rows {
		if !used[row.Key] {
			out = append(out, row)
		}
	}
	return out
}

func engineCompleteChooseStep(flag string, cands []*engineObjectRow) engineCompleteStep {
	list := make([]engineCompleteCandidate, 0, len(cands))
	for _, c := range cands {
		list = append(list, engineCompleteCandidate{Key: c.Key, Source: c.Source, Bytes: c.Bytes})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return engineCompleteStep{
		wire: engineCompleteFile{Flag: flag, Action: engineCompleteActChoose, Candidates: list},
	}
}

// engineCompleteDeclareOrMove is the one rule behind two words: an object already at the key its
// role's loader enumerates is declared, and one anywhere else is moved there first.
func engineCompleteDeclareOrMove(role string, lora bool, flag string, object *engineObjectRow) engineCompleteStep {
	to := engineComfyKeyFor(role, flag, object.Key, lora)
	step := engineCompleteStep{
		wire: engineCompleteFile{Flag: flag, Action: engineCompleteActDeclare,
			Key: to, Bytes: object.Bytes, Source: object.Source},
		to: to, object: object, file: engineCompleteDeclaration(flag, to, object),
	}
	if to != object.Key {
		step.wire.Action, step.from = engineCompleteActMove, object.Key
	}
	return step
}

// engineCompleteDeclaration is one ledger object as the row will declare it. `--vae` answers the
// VAE question by being one, which is what keeps the header scan from ever asking about it.
func engineCompleteDeclaration(flag, key string, object *engineObjectRow) store.EngineModelFile {
	file := store.EngineModelFile{Flag: flag, S3Key: key}
	if object != nil {
		file.Bytes, file.Source, file.ArtifactIdentity = object.Bytes, object.Source, object.ArtifactIdentity
	}
	if flag == "--vae" {
		file.VaeBundled = engineVaeYes
	}
	return file
}

// engineCompleteSwaps is `choices` naming a role the row already fills.
func engineCompleteSwaps(role string, lora bool, m store.EngineModel, l *engineLedger,
	b engineCompleteBody) ([]engineCompleteStep, *apiRefusal) {
	flags := make([]string, 0, len(b.Choices))
	for flag := range b.Choices {
		flags = append(flags, flag)
	}
	sort.Strings(flags)
	var out []engineCompleteStep
	for _, flag := range flags {
		key := strings.TrimSpace(b.Choices[flag])
		held := ""
		for _, f := range m.Files {
			if strings.TrimSpace(f.Flag) == flag {
				held = strings.TrimSpace(f.S3Key)
			}
		}
		if held == "" || held == key {
			continue // a missing role (planned above) or a pick that changes nothing
		}
		if !b.Replace {
			return nil, refuse(http.StatusConflict, errCodeEngineSlotFilled,
				m.ID+" already reads "+held+" as "+engineFlagLabel(flag)+
					" — repeat with replace to swap it for "+key,
				&apiHolder{Kind: "row", ID: m.ID, Key: held},
				&apiNext{Act: "replace", Target: flag})
		}
		object := l.present(key)
		if object == nil {
			return nil, refuse(http.StatusConflict, errCodeEngineBadBody,
				"the ledger holds nothing usable at "+key+" for "+flag,
				&apiHolder{Kind: "object", Key: key}, nil)
		}
		step := engineCompleteDeclareOrMove(role, lora, flag, object)
		step.replaceSlot = true
		out = append(out, step)
	}
	return out, nil
}

// engineCompleteAnswerOf is the plan as the wire reads it, and the one place the top-level word
// is decided.
func engineCompleteAnswerOf(steps []engineCompleteStep) engineCompleteAnswer {
	answer := engineCompleteAnswer{Action: engineCompleteNone, Files: []engineCompleteFile{}}
	seen := map[string]bool{}
	for _, s := range steps {
		answer.Files = append(answer.Files, s.wire)
		seen[s.wire.Action] = true
		if s.wire.Action == engineCompleteActDownload {
			answer.BytesToDownload += s.wire.Bytes
		}
	}
	switch {
	case seen[engineCompleteActChoose]:
		// 🔴 First, and it stops everything. A press that moved two files and then asked a
		// question would have spent the irreversible half before the reversible one.
		answer.Action = engineCompleteChoose
	case seen[engineCompleteActDownload]:
		answer.Action = engineCompleteJobStarted
	case seen[engineCompleteActMove]:
		answer.Action = engineCompleteMoving
	case seen[engineCompleteActDeclare]:
		answer.Action = engineCompleteAttached
	case seen[engineCompleteActUnknown]:
		answer.Action = engineCompleteUnknown
	}
	return answer
}

// completeModel (POST …/models/{id}/complete) is 揃える.
func (a engineAdminAPI) completeModel(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	e, ok := a.engineObjectsEngine(w, r, "completing a row")
	if !ok {
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	var b engineCompleteBody
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b)
	}
	ctx := r.Context()
	ledger, aerr := a.engineLedgerFor(ctx, e.def.Key)
	if aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	answer, _, aerr := a.engineCompleteRun(ctx, r, g, e, strings.TrimSpace(r.PathValue("id")), b, ledger, true)
	if aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// engineCompleteRun plans and — unless `check` — performs. `allowDownload` is false for the
// register road (ADR 0085 decision 3): assigning bytes that are already here is not the human act
// of accepting a licence, so a press that would have to accept one reports the download instead
// of starting it, and the row's own 揃える is where the person accepts it.
func (a engineAdminAPI) engineCompleteRun(ctx context.Context, r *http.Request, g engineIngestGrant,
	e *engineRuntimeState, id string, b engineCompleteBody, ledger *engineLedger,
	allowDownload bool) (engineCompleteAnswer, []map[string]any, *apiRefusal) {
	role := e.def.Key
	m, ok := engineCatalogModel(ctx, e, id)
	if !ok {
		return engineCompleteAnswer{}, nil, refuse(http.StatusNotFound, errCodeEngineModelUnknown,
			"no model "+id+" for engine "+role, nil, nil)
	}
	steps, aerr := engineCompletePlan(ctx, role, e.def.Provider, m, ledger, b)
	if aerr != nil {
		return engineCompleteAnswer{}, nil, aerr
	}
	answer := engineCompleteAnswerOf(steps)
	if b.Check || answer.Action == engineCompleteChoose || answer.Action == engineCompleteNone {
		return answer, nil, nil
	}
	held := a.enginePartsHeld(ctx, g, role)
	var jobs []map[string]any
	for _, s := range steps {
		switch s.wire.Action {
		case engineCompleteActMove:
			job, aerr := a.engineCompleteMove(ctx, r, g, held, ledger, role, id, s)
			if aerr != nil {
				return engineCompleteAnswer{}, jobs, aerr
			}
			jobs = append(jobs, engineIngestJobRow(job))
		case engineCompleteActDeclare:
			if aerr := a.engineCompleteDeclare(ctx, r, g, e, id, s); aerr != nil {
				return engineCompleteAnswer{}, jobs, aerr
			}
		case engineCompleteActDownload:
			if !allowDownload {
				continue
			}
			job, aerr := a.engineCompleteDownload(ctx, r, g, role, id, b, s)
			if aerr != nil {
				return engineCompleteAnswer{}, jobs, aerr
			}
			jobs = append(jobs, engineIngestJobRow(job))
		}
	}
	if !allowDownload {
		// What was NOT done rides in the answer rather than being dropped: the register press
		// declared and moved everything it could, and the parts that still cost money are the
		// row's 揃える, with its licence.
		answer.Action = engineCompleteActionAfterRegister(steps)
	}
	answer.Jobs = jobs
	return answer, jobs, nil
}

// engineCompleteActionAfterRegister is the same precedence with the download not taken: what
// register actually performed.
func engineCompleteActionAfterRegister(steps []engineCompleteStep) string {
	action := engineCompleteNone
	for _, s := range steps {
		switch s.wire.Action {
		case engineCompleteActMove:
			return engineCompleteMoving
		case engineCompleteActDeclare:
			action = engineCompleteAttached
		case engineCompleteActDownload, engineCompleteActUnknown:
			if action == engineCompleteNone {
				action = engineCompleteUnknown
			}
		}
	}
	return action
}

// engineCompleteMove relocates one file's bytes inside the bucket.
//
// 🔴 The declaration is written FIRST, at the key the bytes are at now, and that is not an
// ordering preference: the job's catalogue change is `MoveEngineModelFile(role, id, from, file)`,
// which rewrites a declaration the row already holds. A row that did not name `from` would have
// the task finish and the change fail with "no longer declares". This is the same road the
// af-sandbox operator had to walk by hand — register the WRONG key on purpose, then press 揃える —
// with the CP doing both halves.
func (a engineAdminAPI) engineCompleteMove(ctx context.Context, r *http.Request, g engineIngestGrant,
	held enginePartsHeld, ledger *engineLedger, role, id string, s engineCompleteStep) (store.EngineIngestJob, *apiRefusal) {
	fix := engineMainFileFix{Flag: s.wire.Flag, From: s.from, To: s.to, File: s.file}
	// The destination, by the ledger, before the fence below: a refusal that can name the row or
	// the job holding the key is one the Console can put a button on.
	if aerr := engineCompleteDestinationFree(ledger, s.to, id); aerr != nil {
		return store.EngineIngestJob{}, aerr
	}
	if aerr := a.engineMainFileMovable(ctx, held, role, id, fix); aerr != nil {
		return store.EngineIngestJob{}, aerr
	}
	if s.object != nil && !engineCompleteRowDeclares(ctx, a, role, id, s.from) {
		file := fix.File
		file.S3Key = s.from
		found, err := a.mgr.store.AppendEngineModelFile(ctx, role, id, file)
		if err != nil {
			return store.EngineIngestJob{}, &apiRefusal{apiError: internalErr(err)}
		}
		if !found {
			return store.EngineIngestJob{}, refuse(http.StatusNotFound, errCodeEngineModelUnknown,
				"no model "+id+" for engine "+role, nil, nil)
		}
	}
	job, aerr := a.engineStartMainFileMove(ctx, g, role, id, fix)
	if aerr != nil {
		return store.EngineIngestJob{}, engineRefusalOf(aerr, &apiHolder{Kind: "object", Key: s.to}, nil)
	}
	a.auditFor(r, g, "engine."+role+".model.complete", id+" moving "+s.from+" to "+s.to+" as "+engineFlagLabel(s.wire.Flag))
	if e := a.reg.get(role); e != nil {
		e.catalog.invalidate()
	}
	return job, nil
}

// engineCompleteRowDeclares answers whether the row already names this key, which decides whether
// the move needs a declaration written for it first.
func engineCompleteRowDeclares(ctx context.Context, a engineAdminAPI, role, id, key string) bool {
	e := a.reg.get(role)
	if e == nil {
		return false
	}
	m, ok := engineCatalogModel(ctx, e, id)
	if !ok {
		return false
	}
	for _, f := range m.Files {
		if strings.TrimSpace(f.S3Key) == key {
			return true
		}
	}
	return false
}

// engineCompleteDestinationFree refuses a move onto a key something else holds, in the words the
// Console can act on. The store-level fence (engineIngestDestinationUnused, inside
// engineMainFileMovable) stays behind it and catches the race; this one catches the case an
// operator can do something about.
func engineCompleteDestinationFree(ledger *engineLedger, to, id string) *apiRefusal {
	object := ledger.at(to)
	if object == nil {
		return nil
	}
	for _, by := range object.DeclaredBy {
		if by.ModelID != id {
			return refuse(http.StatusConflict, errCodeEngineBadBody,
				to+" is already declared by "+by.ModelID+", and moving onto it would leave that row"+
					" reading bytes it did not ask for",
				&apiHolder{Kind: "row", ID: by.ModelID, Key: to}, &apiNext{Act: "forget_row", Target: by.ModelID})
		}
	}
	// 🔴 Only a job that could still WRITE these bytes stands in the way (PR #691, and ADR 0085
	// decision 6's definition of a holder). A failed attempt is not a fence: three of them fenced
	// off the very repair they were attempting on af-sandbox, and the store-level check behind
	// this one already knows the difference.
	if ledger.uploading(object) {
		return refuse(http.StatusConflict, errCodeEngineBadBody,
			to+" is being written by an ingest started at "+object.Job.CreatedAt,
			&apiHolder{Kind: "job", ID: object.Job.ID, Key: to}, &apiNext{Act: "wait"})
	}
	return nil
}

// engineCompleteDeclare is the cheap half: the bytes are here, at the key the loader reads, and
// the row gains one line.
func (a engineAdminAPI) engineCompleteDeclare(ctx context.Context, r *http.Request, g engineIngestGrant,
	e *engineRuntimeState, id string, s engineCompleteStep) *apiRefusal {
	file := s.file
	var (
		found bool
		err   error
	)
	if s.replaceSlot {
		found, err = a.mgr.store.ReplaceEngineModelFile(ctx, e.def.Key, id, file, nil)
	} else {
		found, err = a.mgr.store.AppendEngineModelFile(ctx, e.def.Key, id, file)
	}
	if err != nil {
		return &apiRefusal{apiError: internalErr(err)}
	}
	if !found {
		return refuse(http.StatusNotFound, errCodeEngineModelUnknown,
			"no model "+id+" for engine "+e.def.Key, nil, nil)
	}
	e.catalog.invalidate()
	a.auditFor(r, g, "engine."+e.def.Key+".model.complete", id+" declared "+s.to+" (already staged)")
	return nil
}

// engineCompleteDownload is the only half that spends money, and the only one that needs the
// licence.
func (a engineAdminAPI) engineCompleteDownload(ctx context.Context, r *http.Request, g engineIngestGrant,
	role, id string, b engineCompleteBody, s engineCompleteStep) (store.EngineIngestJob, *apiRefusal) {
	if !b.LicenseAccepted {
		return store.EngineIngestJob{}, refuse(http.StatusBadRequest, errCodeIngestNotAccepted,
			"taking "+s.part.Repo+"/"+s.part.File+" in needs its licence accepted first", nil, nil)
	}
	ing := a.reg.ingester()
	if ing == nil {
		return store.EngineIngestJob{}, refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment's engine stack declares no ingest task — stage "+s.part.File+" by hand and attach it",
			nil, nil)
	}
	job, aerr := ing.start(ctx, engineIngestRequest{
		Role: role, ModelID: id, S3Key: s.to,
		AcceptedBy: g.ident.ID, AcceptedTenant: g.tenantID,
		AcceptedLicense: engineLicenceLabel(s.resolved),
		Resolved:        s.resolved, FileFlag: s.wire.Flag, Attach: true,
	})
	if aerr != nil {
		return store.EngineIngestJob{}, engineRefusalOf(aerr, &apiHolder{Kind: "object", Key: s.to}, nil)
	}
	a.auditFor(r, g, "engine."+role+".ingest",
		s.to+" for "+id+" from "+s.resolved.Source+" (licence "+engineLicenceLabel(s.resolved)+" accepted)")
	return job, nil
}

// engineRefusalOf lifts an apiError from code that predates decision 5 into a refusal, so a 409
// this file returns always carries the two fields even when the sentence came from elsewhere.
func engineRefusalOf(aerr *apiError, holder *apiHolder, next *apiNext) *apiRefusal {
	if aerr == nil {
		return nil
	}
	if aerr.status != http.StatusConflict {
		holder, next = nil, nil
	}
	return &apiRefusal{apiError: aerr, Holder: holder, Next: next}
}
