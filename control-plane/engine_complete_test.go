package main

// The row as the subject (ADR 0085 decision 3): family × ledger state → what one press does.
//
// These are the planner's table, and they are written as a table because that is what the fault
// was. Every wall the af-sandbox operator hit was one cell of it answered by a different code
// path — 揃える said `none` beside a badge reading `不足: --diffusion-model`, and the row was
// holding 4.2 GB of exactly that.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ledgerOf builds a ledger from a bucket listing alone, which is every case below except the ones
// that are about a declaration.
func ledgerOf(keys ...string) *engineLedger {
	objects := make([]engineStorageObject, 0, len(keys))
	for _, key := range keys {
		objects = append(objects, engineStorageObject{Key: key, Bytes: 1_000})
	}
	return engineLedgerJoin("image", objects, nil, nil)
}

func planActions(t *testing.T, m store.EngineModel, l *engineLedger, b engineCompleteBody) map[string]engineCompleteFile {
	t.Helper()
	steps, aerr := engineCompletePlan(t.Context(), "image", "comfy", true, m, l, b)
	if aerr != nil {
		t.Fatalf("plan refused: %v", aerr.message)
	}
	out := map[string]engineCompleteFile{}
	for _, s := range steps {
		out[s.wire.Flag] = s.wire
	}
	return out
}

// anima: both parts are attached already and the row's OWN weights are under
// `image/checkpoints/` — the exact state every Anima row on af-sandbox was in. One move, no
// download, and nothing said about the parts.
func TestCompletePlansOnlyTheMoveWhenThePartsAreAlreadyAttached(t *testing.T) {
	misplaced := "image/checkpoints/split_files/diffusion_models/anima_v1.safetensors"
	m := store.EngineModel{Role: "image", ID: "anima_v1", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{
			{S3Key: misplaced, Bytes: 4_180_000_000, ArtifactIdentity: "hf:circlestone-labs/Anima@main/x#sha256:ab"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen_3_06b_base.safetensors"},
			{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors"},
		}}
	l := ledgerOf(misplaced, "image/text_encoders/qwen_3_06b_base.safetensors",
		"image/vae/qwen_image_vae.safetensors")
	steps, aerr := engineCompletePlan(t.Context(), "image", "comfy", true, m, l, engineCompleteBody{})
	if aerr != nil {
		t.Fatalf("plan refused: %v", aerr.message)
	}
	if len(steps) != 1 {
		t.Fatalf("plan = %+v, want the move alone", steps)
	}
	s := steps[0]
	if s.wire.Action != engineCompleteActMove || s.wire.Flag != "--diffusion-model" ||
		s.from != misplaced || s.to != "image/diffusion_models/anima_v1.safetensors" {
		t.Fatalf("the step = %+v (%s -> %s)", s.wire, s.from, s.to)
	}
	// 🔴 The identity travels with the bytes. A move reaches no upstream, so an identity that
	// could not be re-derived — a gated repository, a Civitai version taken down — must not be
	// dropped from a file that still has one.
	if s.file.ArtifactIdentity != "hf:circlestone-labs/Anima@main/x#sha256:ab" {
		t.Errorf("the move lost the artifact identity: %+v", s.file)
	}
	if answer := engineCompleteAnswerOf(steps); answer.Action != engineCompleteMoving ||
		answer.BytesToDownload != 0 {
		t.Errorf("answer = %+v, want a move that costs nothing", answer)
	}
}

// krea2 reads the SAME Qwen-Image VAE as anima, and a deployment running either already has it.
// One write, zero bytes — the cheap outcome, and the common one.
func TestCompleteDeclaresAPartTheLedgerAlreadyHoldsAtTheRightKey(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "krea2", Kind: "checkpoint", BaseModel: "krea2",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/krea2_raw.safetensors"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors"},
		}}
	l := ledgerOf("image/diffusion_models/krea2_raw.safetensors",
		"image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors",
		"image/vae/qwen_image_vae.safetensors")
	// No HTTP stub is installed, so a plan that reached the network for this part would fail —
	// which is the assertion: this answer comes from the ledger alone.
	files := planActions(t, m, l, engineCompleteBody{})
	vae := files["--vae"]
	if vae.Action != engineCompleteActDeclare || vae.Key != "image/vae/qwen_image_vae.safetensors" {
		t.Fatalf("the VAE = %+v, want it declared from what is already here", vae)
	}
	if len(files) != 1 {
		t.Errorf("plan = %+v, want the VAE alone", files)
	}
}

// A part at the wrong key is MOVED, not fetched again: the bytes are this deployment's and
// already paid for. (af-sandbox held the parts at both the wrong and the right keys.)
func TestCompleteMovesAPartTheLedgerHoldsUnderTheWrongKey(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "krea2", Kind: "checkpoint", BaseModel: "krea2",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/krea2_raw.safetensors"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors"},
		}}
	l := ledgerOf("image/diffusion_models/krea2_raw.safetensors",
		"image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors",
		"image/vae/split_files/vae/qwen_image_vae.safetensors")
	files := planActions(t, m, l, engineCompleteBody{})
	if got := files["--vae"]; got.Action != engineCompleteActMove ||
		got.Key != "image/vae/qwen_image_vae.safetensors" {
		t.Fatalf("the VAE = %+v, want the bytes moved into place rather than downloaded again", got)
	}
}

// A family nobody measured parts for says so, naming the role a person still has to supply. It
// does NOT invent a path: an entry written from memory is a 404 minutes after a press.
func TestCompleteAnswersUnknownForAFamilyWithNoPartTable(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "klein", Kind: "checkpoint", BaseModel: "flux2-klein",
		Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/klein.safetensors"}}}
	files := planActions(t, m, ledgerOf("image/diffusion_models/klein.safetensors"), engineCompleteBody{})
	for _, flag := range []string{"--clip_l", "--vae"} {
		if got := files[flag]; got.Action != engineCompleteActUnknown {
			t.Errorf("%s = %+v, want unknown", flag, got)
		}
	}
	steps, _ := engineCompletePlan(t.Context(), "image", "comfy", true, m, ledgerOf(), engineCompleteBody{})
	if answer := engineCompleteAnswerOf(steps); answer.Action != engineCompleteUnknown {
		t.Errorf("answer = %+v, want unknown", answer)
	}
}

// 🔴 Several candidates is the one case the CP must NOT decide. It answers `choose` and does
// nothing at all — a press that picked one of two VAEs on the operator's behalf is the "the
// default choice was the only one that cannot work" fault this ADR is written around.
func TestCompleteAsksWhenARoleHasSeveralCandidates(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "klein", Kind: "checkpoint", BaseModel: "flux2-klein",
		Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/klein.safetensors"}}}
	l := ledgerOf("image/diffusion_models/klein.safetensors",
		"image/vae/ae.safetensors", "image/vae/qwen_image_vae.safetensors",
		"image/text_encoders/clip_l.safetensors")
	files := planActions(t, m, l, engineCompleteBody{})
	vae := files["--vae"]
	if vae.Action != engineCompleteActChoose || len(vae.Candidates) != 2 ||
		vae.Candidates[0].Key != "image/vae/ae.safetensors" {
		t.Fatalf("the VAE = %+v, want both candidates and no decision", vae)
	}
	// One candidate for a role no other missing role shares a directory with IS decided: the press
	// just uses it.
	if got := files["--clip_l"]; got.Action != engineCompleteActDeclare ||
		got.Key != "image/text_encoders/clip_l.safetensors" {
		t.Errorf("the encoder = %+v, want the single candidate used", got)
	}
	// And the whole press stops: `choose` outranks everything, because a request that moved two
	// files and then asked a question would have spent the irreversible half first.
	steps, _ := engineCompletePlan(t.Context(), "image", "comfy", true, m, l, engineCompleteBody{})
	if answer := engineCompleteAnswerOf(steps); answer.Action != engineCompleteChoose {
		t.Errorf("answer = %+v, want choose", answer)
	}
	// The pick resolves it, and it is made FOR this checkpoint.
	picked := planActions(t, m, l, engineCompleteBody{Choices: map[string]string{"--vae": "image/vae/ae.safetensors"}})
	if got := picked["--vae"]; got.Action != engineCompleteActDeclare || got.Key != "image/vae/ae.safetensors" {
		t.Errorf("the picked VAE = %+v", got)
	}
}

// 🔴 Two missing roles in ONE directory is also a question, even with a single candidate:
// `text_encoders/` is one directory for three roles, and a press that put a T5 in CLIP-L's slot
// would be a row that validates and fails inside ComfyUI.
func TestCompleteWillNotGuessWhichEncoderIsWhich(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "sd35", Kind: "checkpoint", BaseModel: "sd35",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd35.safetensors", VaeBundled: engineVaeYes}}}
	files := planActions(t, m, ledgerOf("image/checkpoints/sd35.safetensors",
		"image/text_encoders/clip_l.safetensors"), engineCompleteBody{})
	for _, flag := range []string{"--clip_l", "--clip_g", "--t5xxl"} {
		if got := files[flag]; got.Action != engineCompleteActChoose {
			t.Errorf("%s = %+v, want the operator asked", flag, got)
		}
	}
}

// A pick on a slot the row already fills is the swap, and it is refused without `replace` —
// with the act that gets past it on the refusal (ADR 0085 decision 5).
func TestCompleteRefusesToTakeAFilledSlotWithoutReplace(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "krea2", Kind: "checkpoint", BaseModel: "krea2",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/krea2_raw.safetensors"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors"},
			{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors"},
		}}
	l := ledgerOf("image/diffusion_models/krea2_raw.safetensors",
		"image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors",
		"image/vae/qwen_image_vae.safetensors", "image/vae/ae.safetensors")
	body := engineCompleteBody{Choices: map[string]string{"--vae": "image/vae/ae.safetensors"}}
	_, aerr := engineCompletePlan(t.Context(), "image", "comfy", true, m, l, body)
	if aerr == nil || aerr.status != http.StatusConflict {
		t.Fatalf("swapping a filled slot = %v, want 409", aerr)
	}
	if aerr.code != errCodeEngineSlotFilled || aerr.Next == nil || aerr.Next.Act != "replace" ||
		aerr.Holder == nil || aerr.Holder.ID != "krea2" {
		t.Fatalf("the refusal carries %+v / %+v, want the row and the act that gets past it",
			aerr.Holder, aerr.Next)
	}
	body.Replace = true
	files := planActions(t, m, l, body)
	if got := files["--vae"]; got.Action != engineCompleteActDeclare || got.Key != "image/vae/ae.safetensors" {
		t.Fatalf("the swap = %+v", got)
	}
}

// The row's own weights, for a family whose template reads a WHOLE checkpoint. engineMainFileFixFor
// has nothing to say about sd15 / sdxl / sd35 — there is no role flag to gain — and their bytes
// land under `image/checkpoints/split_files/…` from exactly the same form.
func TestCompleteMovesAWholeCheckpointThatIsTooDeep(t *testing.T) {
	misplaced := "image/checkpoints/split_files/sdxl_base.safetensors"
	m := store.EngineModel{Role: "image", ID: "sdxl", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: misplaced, VaeBundled: engineVaeYes}}}
	fix, ok := engineCompleteMainFix("image", true, "sdxl", m)
	if !ok || fix.Flag != "" || fix.To != "image/checkpoints/sdxl_base.safetensors" {
		t.Fatalf("fix = %+v (%v), want the unflagged slot moved one directory up", fix, ok)
	}
	// And a file already where its loader looks is not a repair anybody is offered.
	m.Files[0].S3Key = "image/checkpoints/sdxl_base.safetensors"
	if _, ok := engineCompleteMainFix("image", true, "sdxl", m); ok {
		t.Error("a correctly placed checkpoint was offered a move")
	}
}

// The family VAE joins the gap by engine_vae.go's rule and not by set subtraction: sd15's
// template reads the checkpoint's own third output, so `--vae` is not a required flag — and a row
// whose checkpoint was READ and carries none cannot generate a picture at all.
func TestCompleteOffersTheFamilyVaeToACheckpointThatCarriesNone(t *testing.T) {
	m := store.EngineModel{Role: "image", ID: "sdxl", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sdxl.safetensors", VaeBundled: engineVaeNo}}}
	l := ledgerOf("image/checkpoints/sdxl.safetensors", "image/vae/sdxl_vae.safetensors")
	files := planActions(t, m, l, engineCompleteBody{})
	if got := files["--vae"]; got.Action != engineCompleteActDeclare ||
		got.Key != "image/vae/sdxl_vae.safetensors" {
		t.Fatalf("the family VAE = %+v, want the stock file declared from the bucket", got)
	}
	// A checkpoint that carries its own is asked nothing.
	m.Files[0].VaeBundled = engineVaeYes
	if got := planActions(t, m, l, engineCompleteBody{}); len(got) != 0 {
		t.Errorf("a complete row was offered %+v", got)
	}
}

// End to end through the route: the row gains what the bucket holds, and pressing again is a
// no-op rather than a second set of files.
func TestCompleteRouteAttachesWhatIsAlreadyHereAndThenSaysNone(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.put("image/diffusion_models/anima_v1.safetensors", 4_180_000_000)
	h.put("image/text_encoders/qwen_3_06b_base.safetensors", 1_190_000_000)
	h.put("image/vae/qwen_image_vae.safetensors", 253_800_000)
	h.row(t, store.EngineModel{ID: "anima_v1", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/anima_v1.safetensors"}}})

	rec := h.call(t, h.a.completeModel, "POST", "/api/admin/engines/image/models/anima_v1/complete",
		`{}`, map[string]string{"id": "anima_v1"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"attached"`) {
		t.Fatalf("complete = %d %s, want the parts declared and nothing downloaded", rec.Code, rec.Body.String())
	}
	if len(h.ecs.run) != 0 {
		t.Errorf("%d tasks were started for bytes this deployment already holds", len(h.ecs.run))
	}
	m, _ := engineCatalogModel(t.Context(), h.e, "anima_v1")
	if len(engineMissingFileFlags("comfy", m)) != 0 {
		t.Errorf("the row is still refused by the files guard: %+v", m.Files)
	}
	again := h.call(t, h.a.completeModel, "POST", "/api/admin/engines/image/models/anima_v1/complete",
		`{}`, map[string]string{"id": "anima_v1"})
	if !strings.Contains(again.Body.String(), `"action":"none"`) {
		t.Errorf("a second press = %s, want nothing to do", again.Body.String())
	}
}

// `check` prices the press without spending anything — the same two-step the ingest form follows.
func TestCompleteCheckActsOnNothing(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.put("image/vae/qwen_image_vae.safetensors", 253_800_000)
	h.row(t, store.EngineModel{ID: "krea2", Kind: "checkpoint", BaseModel: "krea2",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/krea2.safetensors"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen3vl.safetensors"},
		}})
	rec := h.call(t, h.a.completeModel, "POST", "/api/admin/engines/image/models/krea2/complete",
		`{"check":true}`, map[string]string{"id": "krea2"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"attached"`) {
		t.Fatalf("check = %d %s", rec.Code, rec.Body.String())
	}
	m, _ := engineCatalogModel(t.Context(), h.e, "krea2")
	for _, f := range m.Files {
		if f.Flag == "--vae" {
			t.Fatalf("a check wrote a declaration: %+v", m.Files)
		}
	}
}

// 🔴 Three properties the Console (ADR 0085 P2) builds directly on, pinned here because each one
// is invisible from this side until a button does nothing:
//
//  1. `choose` is only ever asked about a PART. The main file's candidates are register's to
//     resolve, so a `flag: ""` the Console has no dialog for must never come back;
//  2. a job row carries `action`, so a move is not drawn as a download that never transfers;
//  3. `job.id` is the engine_ingest_jobs id — the Console dismisses a failed one with
//     `DELETE …/ingest/{id}`, which is a different id from everything else on the row.
func TestCompleteAnswersWhatTheConsoleDrawsButtonsFrom(t *testing.T) {
	h := newEngineLedgerHarness(t)
	misplaced := "image/checkpoints/split_files/diffusion_models/anima_v1.safetensors"
	h.put(misplaced, 4_180_000_000)
	h.put("image/text_encoders/qwen_3_06b_base.safetensors", 1_190_000_000)
	h.put("image/vae/qwen_image_vae.safetensors", 253_800_000)
	rec := h.call(t, h.a.postObjectRegister, "POST", "/api/admin/engines/image/objects/register",
		`{"key":"`+misplaced+`","base_model":"anima"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("register = %d %s", rec.Code, rec.Body.String())
	}
	var answer engineObjectRegisterAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Jobs) != 1 || answer.Jobs[0]["action"] != enginePlanMove {
		t.Fatalf("jobs = %+v, want one job that says it is a move", answer.Jobs)
	}
	jobID, _ := answer.Jobs[0]["id"].(string)
	stored, err := h.st.ListEngineIngestJobs(t.Context(), "image", 10)
	if err != nil || len(stored) != 1 || stored[0].ID != jobID {
		t.Fatalf("the job row's id is not the ingest job's: %q vs %+v (%v)", jobID, stored, err)
	}
	for _, f := range answer.Complete.Files {
		if f.Action == engineCompleteActChoose && f.Flag == "" {
			t.Errorf("the whole-checkpoint slot was put to the operator as a choice: %+v", f)
		}
	}
	// And the ledger lends that same id to the object, which is what the dismiss button addresses.
	led := h.call(t, h.a.getObjects, "GET", "/api/admin/engines/image/objects", "", nil)
	var ledger engineObjectsResponse
	if err := json.Unmarshal(led.Body.Bytes(), &ledger); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range ledger.Objects {
		if row.Job != nil {
			found = true
			if row.Job.ID != jobID {
				t.Errorf("the ledger's job id = %q, want the ingest job's %q", row.Job.ID, jobID)
			}
		}
	}
	if !found {
		t.Error("the running move is on no object in the ledger")
	}
}

// The planner never asks about the unflagged slot, whatever the row is missing: `engineComfyRequiredFlags`
// lists "" for the whole-checkpoint families and that role is the row's own file, not a part.
func TestCompleteNeverAsksAboutTheWholeCheckpointSlot(t *testing.T) {
	for _, family := range engineComfyFamilies {
		m := store.EngineModel{Role: "image", ID: "x", Kind: "checkpoint", BaseModel: family}
		files := planActions(t, m, ledgerOf("image/checkpoints/a.safetensors",
			"image/checkpoints/b.safetensors", "image/vae/v1.safetensors",
			"image/vae/v2.safetensors", "image/text_encoders/e1.safetensors"), engineCompleteBody{})
		if got, ok := files[""]; ok {
			t.Errorf("%s put the unflagged slot on the wire as %+v", family, got)
		}
	}
}
