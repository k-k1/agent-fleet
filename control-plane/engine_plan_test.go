package main

// The plan (ADR 0085 decisions 1 and 4): what one press would do, decided by the Control Plane
// from the family's own table and the bytes this deployment already holds.
//
// The faults these cover are the ones that cost af-sandbox four days: a split family's weights
// staged under `image/checkpoints/` where no loader lists them, the same 13 GB file downloaded a
// second time because a reuse compared the wrong thing, and a form that asked an operator which
// of six paths they were on.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineHFRepoStub answers the one call the resolve makes, for the repositories the family table
// names. The shape is hfStub's: `sha` is the commit the identity is built on and `lfs.sha256` is
// what makes a file takeable at all.
//
// Every file is given a DISTINCT sha256 derived from its own name, because the whole planner
// turns on the artifact identity: a stub that answered one hash for everything would make any two
// files look like the same bytes and every assertion below pass for the wrong reason.
func engineHFRepoStub(t *testing.T, repos map[string][]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for repo, files := range repos {
			if !strings.Contains(r.URL.Path, "/api/models/"+repo) {
				continue
			}
			rows := make([]string, 0, len(files))
			for _, f := range files {
				rows = append(rows, `{"rfilename":"`+f+`","size":`+engineStubBytes(f)+
					`,"lfs":{"sha256":"`+engineStubSHA(f)+`"}}`)
			}
			_, _ = w.Write([]byte(`{"sha":"commit-a","gated":false,"cardData":{"license":"apache-2.0"},
				"siblings":[` + strings.Join(rows, ",") + `]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })
}

// engineStubSHA is one file's hash, made of its own name so that two files never collide and the
// same file always resolves to the same identity across two calls.
func engineStubSHA(file string) string {
	sum := ""
	for _, r := range engineBaseName(file) {
		sum += string("0123456789abcdef"[int(r)%16])
	}
	return (sum + strings.Repeat("0", 64))[:64]
}

func engineStubBytes(string) string { return "1000000" }

// engineAnimaRepos is what the two split families' tables actually name, plus a weights file for
// each so a whole row can be planned.
func engineAnimaRepos() map[string][]string {
	return map[string][]string{
		"circlestone-labs/Anima": {
			"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors",
			"split_files/text_encoders/qwen_3_06b_base.safetensors",
			"split_files/vae/qwen_image_vae.safetensors",
		},
		"Comfy-Org/Krea-2": {
			"split_files/diffusion_models/krea2.safetensors",
			"text_encoders/qwen3vl_4b_fp8_scaled.safetensors",
		},
	}
}

// enginePlanAPI is a comfy engine with an ingester that can answer "is it in the bucket".
func enginePlanAPI(t *testing.T) (engineAdminAPI, *engineRuntimeState, store.Store, *fakeEngineStorageHead) {
	t.Helper()
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	head := &fakeEngineStorageHead{states: map[string]string{}, bytes: map[string]int64{}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
		storage: newEngineStorage("image", head),
	}
	return engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}, e, st, head
}

func enginePlanOf(t *testing.T, a engineAdminAPI, e *engineRuntimeState, body string) enginePlan {
	t.Helper()
	var b engineIngestBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("fixture body: %v", err)
	}
	res, aerr := engineIngestResolve(t.Context(), b.Source)
	if aerr != nil {
		t.Fatalf("resolve: %s", aerr.message)
	}
	plan, _ := a.enginePlanFor(t.Context(), engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true}, e, b, res)
	return plan
}

// enginePressBody is a fixture body with the plan_token the CP would have handed the form, so a
// test presses the way the Console does: resolve first, show the plan, send its fingerprint back.
//
// Required since ADR 0085 P3 (`plan_token` is no longer optional), and spliced in rather than
// written into every fixture because the token is a hash of the CP's own answer — a literal in a
// test would be a value nobody can recompute after the planner changes one field.
func enginePressBody(t *testing.T, a engineAdminAPI, role, body string) string {
	t.Helper()
	e := a.reg.get(role)
	if e == nil {
		t.Fatalf("no engine %q in the fixture registry", role)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("fixture body: %v", err)
	}
	token, err := json.Marshal(enginePlanOf(t, a, e, body).PlanToken)
	if err != nil {
		t.Fatal(err)
	}
	raw["plan_token"] = token
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func enginePlanFileFor(plan enginePlan, flag string) (enginePlanFile, bool) {
	for _, f := range plan.Files {
		if f.Flag == flag {
			return f, true
		}
	}
	return enginePlanFile{}, false
}

// 🔴 The act the whole ADR is named after: a family published in three files is ONE press, and
// the person is never asked which role anything plays. Anima first, on a deployment holding
// nothing — every file is a download and the weights land where UNETLoader looks, not in
// `checkpoints/` where the form used to put them.
func TestPlanTakesASplitFamilyInAsOneAct(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, _, _ := enginePlanAPI(t)

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)

	if plan.BaseModel != "anima" || plan.MainFlag != "--diffusion-model" {
		t.Fatalf("plan = family %q main %q, want anima/--diffusion-model", plan.BaseModel, plan.MainFlag)
	}
	// Proposed from the filename, with the container format off and nothing invented.
	if plan.ID != "anima-aesthetic-v1.1" {
		t.Errorf("proposed id = %q", plan.ID)
	}
	if len(plan.Files) != 3 {
		t.Fatalf("plan has %d file(s), want the weights, the encoder and the VAE: %+v", len(plan.Files), plan.Files)
	}
	want := map[string]string{
		"--diffusion-model": "image/diffusion_models/anima-aesthetic-v1.1.safetensors",
		"--clip_l":          "image/text_encoders/qwen_3_06b_base.safetensors",
		"--vae":             "image/vae/qwen_image_vae.safetensors",
	}
	for flag, key := range want {
		f, ok := enginePlanFileFor(plan, flag)
		switch {
		case !ok:
			t.Errorf("the plan says nothing about %s", flag)
		case f.Key != key:
			// The upstream publishes all three under `split_files/…`; a key that kept that
			// directory is a name no ComfyUI loader can ever offer.
			t.Errorf("%s lands at %q, want %q", flag, f.Key, key)
		case f.Action != enginePlanDownload:
			t.Errorf("%s = %q on a deployment that holds nothing", flag, f.Action)
		}
	}
	// 🔴 The VAE comes from Anima's OWN repository. The same bytes live in three repositories and
	// the reuse check compares the identity, not the hash — declaring it from elsewhere would be
	// a second identity, a second download and a second key.
	if vae, _ := enginePlanFileFor(plan, "--vae"); !strings.Contains(vae.Source, "circlestone-labs/Anima") {
		t.Errorf("the VAE is planned from %q, want anima's own repository", vae.Source)
	}
	if plan.BytesToDownload != 3_000_000 {
		t.Errorf("bytes_to_download = %d, want all three files", plan.BytesToDownload)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("a complete plan carries warnings: %v", plan.Warnings)
	}
	if len(plan.PlanToken) != 16 {
		t.Errorf("plan_token = %q, want 16 hex characters", plan.PlanToken)
	}
}

// The second family on the same deployment. Krea 2 reads the SAME Qwen-Image VAE Anima brought
// in, so that file is declared rather than fetched — 253 MB and a Fargate task for nothing — and
// only its own encoder is paid for.
func TestPlanReusesAPartTheDeploymentAlreadyHolds(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)
	vaeKey := "image/vae/qwen_image_vae.safetensors"
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "anima-aesthetic-v1.1", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/anima-aesthetic-v1.1.safetensors"},
			{Flag: "--vae", S3Key: vaeKey, Source: "hf:circlestone-labs/Anima/split_files/vae/qwen_image_vae.safetensors",
				ArtifactIdentity: engineHFArtifactIdentity("circlestone-labs/Anima",
					"split_files/vae/qwen_image_vae.safetensors", "commit-a",
					engineStubSHA("qwen_image_vae.safetensors"))},
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	head.states[vaeKey] = engineStoragePresent

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"Comfy-Org/Krea-2","file":"split_files/diffusion_models/krea2.safetensors"}}}`)

	if plan.BaseModel != "krea2" {
		t.Fatalf("family = %q, want krea2", plan.BaseModel)
	}
	vae, ok := enginePlanFileFor(plan, "--vae")
	if !ok || vae.Action != enginePlanReuse || vae.Key != vaeKey || vae.Source != vaeKey {
		t.Fatalf("the shared VAE = %+v, want a reuse of %s", vae, vaeKey)
	}
	enc, ok := enginePlanFileFor(plan, "--clip_l")
	if !ok || enc.Action != enginePlanDownload {
		t.Fatalf("krea2's own encoder = %+v, want a download", enc)
	}
	// Two downloads, not three: the weights and the encoder.
	if plan.BytesToDownload != 2_000_000 {
		t.Errorf("bytes_to_download = %d, want the two files this deployment does not have", plan.BytesToDownload)
	}
}

// 🔴 The state af-sandbox was actually in on 2026-09-15: about 24 GB of bytes in the bucket, at
// keys no loader lists, with the rows that named them forgotten. The only repair that moved bytes
// was computed FROM A ROW, so there was no road back at all.
//
// The plan reads the bucket instead: the same file, at the wrong key, is a `move` — a server-side
// `aws s3 mv` and 0 bytes of egress — not a second download of a file already paid for.
func TestPlanMovesBytesThatAreHereUnderAKeyNoLoaderLists(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)
	// A finished job is all that is left of the row that fetched it — exactly the ledger entry
	// decision 2 gives a route of its own.
	wrong := "image/checkpoints/split_files/anima-aesthetic-v1.1.safetensors"
	identity := engineHFArtifactIdentity("circlestone-labs/Anima",
		"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors", "commit-a",
		engineStubSHA("anima-aesthetic-v1.1.safetensors"))
	spec, _ := json.Marshal(engineIngestRequest{Resolved: engineResolved{ArtifactIdentity: identity}})
	if err := st.PutEngineIngestJob(t.Context(), store.EngineIngestJob{
		ID: "old", Role: "image", ModelID: "forgotten", S3Key: wrong,
		State: store.EngineIngestDone, Spec: string(spec),
	}); err != nil {
		t.Fatal(err)
	}
	head.states[wrong] = engineStoragePresent

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)

	main, ok := plan.main()
	if !ok || main.Action != enginePlanMove {
		t.Fatalf("the main file = %+v, want a move", main)
	}
	if main.Source != wrong || main.Key != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		t.Errorf("the move goes %q -> %q", main.Source, main.Key)
	}
	// The bytes are this deployment's already; only the two parts it does not hold are paid for.
	if plan.BytesToDownload != 2_000_000 {
		t.Errorf("bytes_to_download = %d, want the main file to cost nothing", plan.BytesToDownload)
	}
}

// A family this provider does not recognise is not planned with a guess. The candidates are the
// closed vocabulary the provider dispatches on — never the upstream's display name, which is what
// ADR 0072 decision 2 forbids and what produced rows that looked complete and refused to generate.
func TestPlanOffersTheVocabularyWhenItCannotNameTheFamily(t *testing.T) {
	engineHFRepoStub(t, map[string][]string{"nobody/Unknown-Arch": {"model.safetensors"}})
	a, e, _, _ := enginePlanAPI(t)

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"nobody/Unknown-Arch","file":"model.safetensors"}}}`)

	if plan.BaseModel != "" {
		t.Fatalf("the CP named a family it cannot know: %q", plan.BaseModel)
	}
	if len(plan.BaseModelCandidates) == 0 || plan.BaseModelCandidates[0] != engineComfyFamilies[0] {
		t.Errorf("candidates = %v, want the provider's own vocabulary", plan.BaseModelCandidates)
	}
	if len(plan.Files) != 1 || plan.Files[0].Key != "image/checkpoints/model.safetensors" {
		t.Errorf("plan = %+v, want the one file it was given", plan.Files)
	}
}

// 🔴 A family nobody has measured parts for says which roles it still needs, and NAMES NO PATH.
// ADR 0072's 2026-09-15 addendum measured what the alternative costs: an entry written from
// memory is a 404 minutes after a press.
func TestPlanNamesTheRolesItCannotSupply(t *testing.T) {
	engineHFRepoStub(t, map[string][]string{"black-forest-labs/FLUX.1-dev": {"flux1-dev.safetensors"}})
	a, e, _, _ := enginePlanAPI(t)

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","base_model":"flux1","license_accepted":true,
	  "source":{"hf":{"repo":"black-forest-labs/FLUX.1-dev","file":"flux1-dev.safetensors"}}}`)

	if len(plan.Files) != 1 {
		t.Fatalf("flux1 has no part table, so the plan must name one file: %+v", plan.Files)
	}
	joined := strings.Join(plan.Warnings, " | ")
	for _, role := range []string{"--clip_l", "--t5xxl", "--vae"} {
		if !strings.Contains(joined, role) {
			t.Errorf("the plan is silent about %s: %q", role, joined)
		}
	}
	if strings.Contains(joined, "image/text_encoders/") {
		t.Errorf("the plan invented a path for a role it has no source for: %q", joined)
	}
}

// The token is a fingerprint of the plan and nothing else: the same bucket and the same request
// answer the same 16 characters twice, and a bucket that changed underneath answers different
// ones. Without the first half every press would be stale; without the second the token would be
// decoration.
func TestPlanTokenIsStableAndMovesWithTheBucket(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)
	body := `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"Comfy-Org/Krea-2","file":"split_files/diffusion_models/krea2.safetensors"}}}`

	first := enginePlanOf(t, a, e, body)
	if again := enginePlanOf(t, a, e, body); again.PlanToken != first.PlanToken {
		t.Fatalf("the same plan hashed twice = %q and %q", first.PlanToken, again.PlanToken)
	}
	vaeKey := "image/vae/qwen_image_vae.safetensors"
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "anima", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{{Flag: "--vae", S3Key: vaeKey,
			ArtifactIdentity: engineHFArtifactIdentity("circlestone-labs/Anima",
				"split_files/vae/qwen_image_vae.safetensors", "commit-a",
				engineStubSHA("qwen_image_vae.safetensors"))}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	head.states[vaeKey] = engineStoragePresent

	if moved := enginePlanOf(t, a, e, body); moved.PlanToken == first.PlanToken {
		t.Error("a download that became a reuse left the token unchanged — the press could not notice")
	}
}

// 🔴 The re-plan at the press, which is the whole reason the token exists: the form's answer can
// be minutes old and this is the call that spends a Fargate task. A stale token is refused with
// the CURRENT plan beside the error, so the next press is one click and not a second resolve.
func TestIngestRefusesAPlanTheBucketHasMovedUnder(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, _, st, _ := enginePlanAPI(t)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(
		`{"kind":"checkpoint","license_accepted":true,"plan_token":"0000000000000000",
		  "source":{"hf":{"repo":"circlestone-labs/Anima",
		  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})

	if rec.Code != http.StatusConflict {
		t.Fatalf("a stale plan = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Error struct {
			Code   string     `json:"code"`
			Holder *apiHolder `json:"holder"`
			Next   *apiNext   `json:"next"`
		} `json:"error"`
		Plan *enginePlan `json:"plan"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer: %v (%s)", err, rec.Body.String())
	}
	if out.Error.Code != errCodeEnginePlanStale {
		t.Errorf("code = %q", out.Error.Code)
	}
	// Decision 5: every refusal in this area names what stands in the way and what to press.
	if out.Error.Holder == nil || out.Error.Next == nil {
		t.Errorf("the refusal carries no holder/next: %s", rec.Body.String())
	}
	if out.Plan == nil || len(out.Plan.PlanToken) != 16 || len(out.Plan.Files) != 3 {
		t.Fatalf("the fresh plan did not ride along: %s", rec.Body.String())
	}
	// Nothing was spent on the refusal.
	if jobs, _ := st.ListEngineIngestJobs(t.Context(), "image", 10); len(jobs) != 0 {
		t.Errorf("a stale plan started %d job(s)", len(jobs))
	}
	// And the token the CP just handed back is the one it accepts.
	ok := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(
		`{"kind":"checkpoint","license_accepted":true,"plan_token":"`+out.Plan.PlanToken+`",
		  "source":{"hf":{"repo":"circlestone-labs/Anima",
		  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`))
	r2.SetPathValue("key", "image")
	a.postIngest(ok, r2, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if ok.Code != http.StatusOK {
		t.Fatalf("the fresh plan's own token = %d (%s)", ok.Code, ok.Body.String())
	}
}

// The press does what the plan said, all the way down: the weights are staged where the loader
// looks, and the parts ride as follow-ups the reconciler keeps once the row exists.
func TestIngestStartsExactlyWhatThePlanNamed(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, _, st, _ := enginePlanAPI(t)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(
		enginePressBody(t, a, "image", `{"kind":"checkpoint","license_accepted":true,
		  "source":{"hf":{"repo":"circlestone-labs/Anima",
		  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("the press = %d (%s)", rec.Code, rec.Body.String())
	}
	var row map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &row)
	if row["action"] != enginePlanDownload {
		t.Errorf("the answer does not say which of the three happened: %v", row["action"])
	}
	jobs, err := st.ListEngineIngestJobs(t.Context(), "image", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %d (%v)", len(jobs), err)
	}
	if jobs[0].S3Key != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		t.Errorf("staged at %q, which UNETLoader does not enumerate", jobs[0].S3Key)
	}
	if jobs[0].ModelID != "anima-aesthetic-v1.1" {
		t.Errorf("row id = %q, want the one the plan proposed", jobs[0].ModelID)
	}
	var spec engineIngestRequest
	if err := json.Unmarshal([]byte(jobs[0].Spec), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.FileFlag != "--diffusion-model" || spec.BaseModel != "anima" {
		t.Errorf("spec = %s / %s, want the family's weights", spec.FileFlag, spec.BaseModel)
	}
	if len(spec.PartsFollowUp) != 2 {
		t.Fatalf("the family's parts were not promised: %+v", spec.PartsFollowUp)
	}
}

// 🔴 A proposed id is made unique against the catalogue, because the row is written by an upsert
// on (role, id): a second Anima taken in under the same proposal would replace a working row's
// files, licence and enabled state minutes after a press, and nothing would connect the two.
func TestPlanProposesAnIdNothingElseHolds(t *testing.T) {
	rows := []store.EngineModel{{ID: "anima-aesthetic-v1.1"}, {ID: "anima-aesthetic-v1.1-2"}}
	if got := enginePlanID("", "split_files/anima-aesthetic-v1.1.safetensors", rows); got != "anima-aesthetic-v1.1-3" {
		t.Errorf("proposed id = %q, want the first free suffix", got)
	}
	// An id the REQUEST named is never rewritten: the refusal for a taken one is the answer, and
	// silently renaming it would register a row under a name nobody asked for.
	if got := enginePlanID("anima-aesthetic-v1.1", "x.safetensors", rows); got != "anima-aesthetic-v1.1" {
		t.Errorf("an explicit id was rewritten to %q", got)
	}
	// 🔴 The quantisation is KEPT (ADR 0090), and used to be stripped on the rule "the
	// quantisation names the file, not the model". That was true while a deployment held one
	// size of a model, and stopped being true when ADR 0089 made taking in a second size one
	// press: measured on unsloth/Qwen3.8-27B-GGUF, all four sizes proposed one id, so the second
	// row became `<name>-2` — a numeric suffix where the distinguishing fact should be, in the id
	// a member picks by.
	a, b := engineIDFromFile("qwen-q4_k_m.gguf"), engineIDFromFile("qwen-q8_0.gguf")
	if a != "qwen-q4_k_m" || b != "qwen-q8_0" {
		t.Errorf("quantisations proposed %q and %q, want each to carry its own", a, b)
	}
	if got := engineIDFromFile("Qwen3.8-27B-UD-IQ2_S.gguf"); got != "qwen3.8-27b-ud-iq2_s" {
		t.Errorf("proposed %q for a real quantisation file", got)
	}
}

// The layout follows the ROLE. A deployment whose image engine is sdcpp has no file vocabulary at
// all and its one whole checkpoint still belongs under `image/checkpoints/`; the llm role is flat.
func TestIngestKeyFollowsTheRole(t *testing.T) {
	cases := []struct {
		role         string
		images, lora bool
		flag, file   string
		want         string
	}{
		{"image", true, false, "--diffusion-model", "split_files/diffusion_models/a.safetensors",
			"image/diffusion_models/a.safetensors"},
		{"image", true, false, "--t5xxl", "t5xxl.safetensors", "image/text_encoders/t5xxl.safetensors"},
		{"image", true, false, "", "sd_xl.safetensors", "image/checkpoints/sd_xl.safetensors"},
		{"image", true, true, "", "anime-lora.safetensors", "image/loras/anime-lora.safetensors"},
		{"llm", false, false, "", "qwen-q4_k_m.gguf", "llm/qwen-q4_k_m.gguf"},
		{"llm", false, true, "", "adapter.gguf", "llm/loras/adapter.gguf"},
	}
	for _, c := range cases {
		if got := engineIngestKeyFor(c.role, c.images, c.flag, c.file, c.lora); got != c.want {
			t.Errorf("%s %s -> %q, want %q", c.role, c.file, got, c.want)
		}
	}
}

// 🔴 The bug that produced "what taking … in would do has changed" on a press that changed
// nothing but the row's name: the id is what the operator calls the row, not a priced decision.
// A token that moved with the id would refuse every press where the person accepted the proposal
// and pressed immediately — which is the case that must never stale.
func TestPlanTokenDoesNotMoveWithTheID(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, _, _ := enginePlanAPI(t)
	body := `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`

	proposed := enginePlanOf(t, a, e, body)
	// A request with a different id but the same source and bucket state must produce the same
	// token, because the operator's choice of name does not change what files cost.
	edited := enginePlanOf(t, a, e, `{"id":"my-custom-name","kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)
	if edited.PlanToken != proposed.PlanToken {
		t.Errorf("token with id %q = %q, token with id %q = %q — id alone must not change the token",
			proposed.ID, proposed.PlanToken, edited.ID, edited.PlanToken)
	}
	// Regression: a DIFFERENT action still moves the token.  Without this half, a token() that
	// returned a constant would satisfy the equality above and nobody would notice.
	engineHFRepoStub(t, map[string][]string{"other/Repo": {"other.safetensors"}})
	different := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"other/Repo","file":"other.safetensors"}}}`)
	if different.PlanToken == proposed.PlanToken {
		t.Error("a plan for a different file produced the same token — the token is not covering the action")
	}
}

// engineLedgerLookup is the seam decision 2's ledger replaces, and both of its proofs are
// load-bearing: a record with no object is a key whose bytes were purged, an object with no
// matching identity is bytes nobody can vouch for.
func TestLedgerLookupNeedsBothProofs(t *testing.T) {
	key, identity := "image/vae/x.safetensors", "hf:a/b@c/x.safetensors#sha256:dd"
	head := &fakeEngineStorageHead{states: map[string]string{}, bytes: map[string]int64{}}
	held := enginePartsHeld{
		known: map[string]*engineKnownArtifact{
			key: {S3Key: key, ArtifactIdentity: identity, Reusable: true, ModelIDs: map[string]struct{}{}},
		},
		storage: newEngineStorage("image", head),
	}
	ctx := context.Background()
	if _, _, ok := engineLedgerLookup(ctx, held, identity); ok {
		t.Error("a record with no object was read as bytes in the bucket")
	}
	head.states[key] = engineStoragePresent
	held.storage.invalidate(key)
	if at, _, ok := engineLedgerLookup(ctx, held, identity); !ok || at != key {
		t.Errorf("lookup = %q %v, want the key", at, ok)
	}
	if _, _, ok := engineLedgerLookup(ctx, held, "hf:somebody/else@main/x#sha256:ff"); ok {
		t.Error("a different upstream file matched the same key")
	}
	// An upload that may still replace those bytes is not something to reuse.
	held.known[key].InFlight = true
	if _, _, ok := engineLedgerLookup(ctx, held, identity); ok {
		t.Error("a key with an ingest in flight was offered for reuse")
	}
}

// engineJobsOf is this role's job rows, for the tests that assert a refusal spent nothing.
func engineJobsOf(t *testing.T, st store.Store, role string) []store.EngineIngestJob {
	t.Helper()
	jobs, err := st.ListEngineIngestJobs(t.Context(), role, 20)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

// 🔴 The actual production incident: a split family's part destination was already declared by
// another row (the clip_l key belonged to a text encoder taken in earlier). The plan must warn
// the operator on the resolve screen, before any licence is accepted. Positive control: the
// warning appears and names the flag and the holder.
func TestPlanWarnsWhenPartDestinationIsHeldByARow(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)
	body := `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`

	// The clean plan's token is the baseline: adding a conflict must move it.
	clean := enginePlanOf(t, a, e, body)
	if len(clean.Warnings) != 0 {
		t.Fatalf("clean plan already carries warnings: %v", clean.Warnings)
	}

	clipKey := "image/text_encoders/qwen_3_06b_base.safetensors"
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "other-encoder",
		Files: []store.EngineModelFile{{Flag: "--clip_l", S3Key: clipKey}},
	}); err != nil {
		t.Fatal(err)
	}
	head.states[clipKey] = engineStoragePresent

	plan := enginePlanOf(t, a, e, body)
	if len(plan.Warnings) == 0 {
		t.Fatal("plan has no warnings; expected one for the taken --clip_l destination")
	}
	joined := strings.Join(plan.Warnings, " | ")
	if !strings.Contains(joined, "--clip_l") {
		t.Errorf("the warning does not name the flag: %q", joined)
	}
	if !strings.Contains(joined, "other-encoder") {
		t.Errorf("the warning does not name the holding row: %q", joined)
	}
	// Row holder: the operator completes or reroutes — "complete" must appear, "dismiss" must not.
	if !strings.Contains(joined, "complete") {
		t.Errorf("the row-held warning does not name the next act (complete): %q", joined)
	}
	if strings.Contains(joined, "dismiss") {
		t.Errorf("the row-held warning mentions 'dismiss', which belongs to a job holder: %q", joined)
	}
	// The conflict warning must move the token: a stale press catches what just changed.
	if plan.PlanToken == clean.PlanToken {
		t.Error("a conflict warning did not move the token — stale detection will not catch it")
	}
	// The follow-up for --clip_l must carry the conflict so followUpFile skips RunTask.
	followUps, _ := enginePlanFollowUps(plan)
	for _, fu := range followUps {
		if fu.Flag == "--clip_l" {
			if fu.Conflict == "" {
				t.Error("the --clip_l follow-up has no Conflict; followUpFile will attempt a download and be refused in the reconciler")
			}
			return
		}
	}
	t.Error("no follow-up for --clip_l found")
}

// Negative control: when no row or job claims the part's destination, the plan must have no
// warning for it. A plan that always warned would make this test green; this one fails it.
func TestPlanNoWarningWhenPartDestinationIsFree(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, _, _ := enginePlanAPI(t)

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)

	// No warnings: a clean deployment has no claimed destinations.
	if len(plan.Warnings) != 0 {
		t.Errorf("clean deployment produced warnings: %v", plan.Warnings)
	}
	// No follow-up should carry a Conflict.
	followUps, _ := enginePlanFollowUps(plan)
	for _, fu := range followUps {
		if fu.Conflict != "" {
			t.Errorf("follow-up for %s carries a Conflict on a clean deployment: %q", fu.Flag, fu.Conflict)
		}
	}
}

// A done ingest job holds the key and bytes are still present: same outcome as a row.
func TestPlanWarnsWhenPartDestinationIsHeldByAJob(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)
	clipKey := "image/text_encoders/qwen_3_06b_base.safetensors"
	if err := st.PutEngineIngestJob(t.Context(), store.EngineIngestJob{
		ID: "j1", Role: "image", ModelID: "other-row", S3Key: clipKey,
		State: store.EngineIngestDone,
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	head.states[clipKey] = engineStoragePresent

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)

	if len(plan.Warnings) == 0 {
		t.Fatal("plan has no warning for a key held by a live ingest job")
	}
	joined := strings.Join(plan.Warnings, " | ")
	if !strings.Contains(joined, "--clip_l") {
		t.Errorf("the warning does not name the flag: %q", joined)
	}
	// Job holder: the operator dismisses the job — "dismiss" must appear, "complete" must not.
	if !strings.Contains(joined, "dismiss") {
		t.Errorf("the job-held warning does not name the next act (dismiss): %q", joined)
	}
	if strings.Contains(joined, "complete") {
		t.Errorf("the job-held warning mentions 'complete', which belongs to a row holder: %q", joined)
	}
}

// An orphaned done-job record — bytes confirmed missing by the bucket — must NOT trigger a
// warning. Without this, the plan would block every re-ingest of a slot whose previous bytes
// were deleted, requiring the operator to dismiss the old job before pressing again.
func TestPlanNoWarningWhenPartDestinationIsOrphanedRecord(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)
	clipKey := "image/text_encoders/qwen_3_06b_base.safetensors"
	if err := st.PutEngineIngestJob(t.Context(), store.EngineIngestJob{
		ID: "j1", Role: "image", ModelID: "old-row", S3Key: clipKey,
		State: store.EngineIngestDone,
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	// Explicitly mark the key as missing (bytes purged). Without this, the fake storage returns
	// engineStorageUnknown (the default for an absent map entry), which engineIngestDestinationUnused
	// treats as "cannot verify → refuse conservatively" — that is correct behaviour, not a test bug.
	head.states[clipKey] = engineStorageMissing

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)

	if len(plan.Warnings) != 0 {
		t.Errorf("orphaned job record triggered a false warning: %v", plan.Warnings)
	}
}

// When the plan carries a conflict for a part, pressing must store the Conflict in the job
// spec so the reconciler's followUpFile skips RunTask rather than racing a refusal.
func TestIngestStoresConflictInPartFollowUpSpec(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, _, st, head := enginePlanAPI(t)
	clipKey := "image/text_encoders/qwen_3_06b_base.safetensors"
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "other-encoder",
		Files: []store.EngineModelFile{{Flag: "--clip_l", S3Key: clipKey}},
	}); err != nil {
		t.Fatal(err)
	}
	head.states[clipKey] = engineStoragePresent

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(
		enginePressBody(t, a, "image", `{"kind":"checkpoint","license_accepted":true,
		  "source":{"hf":{"repo":"circlestone-labs/Anima",
		  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("the press = %d (%s)", rec.Code, rec.Body.String())
	}
	jobs, err := st.ListEngineIngestJobs(t.Context(), "image", 10)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("jobs = %d (%v)", len(jobs), err)
	}
	var spec engineIngestRequest
	if err := json.Unmarshal([]byte(jobs[0].Spec), &spec); err != nil {
		t.Fatal(err)
	}
	// The job spec must carry the Conflict so the reconciler skips the --clip_l download.
	var clipFU *engineVaeFollowUp
	for i := range spec.PartsFollowUp {
		if spec.PartsFollowUp[i].Flag == "--clip_l" {
			clipFU = &spec.PartsFollowUp[i]
			break
		}
	}
	if clipFU == nil {
		t.Fatal("no follow-up for --clip_l in the job spec")
	}
	if clipFU.Conflict == "" {
		t.Error("the --clip_l follow-up has no Conflict in the stored spec; the reconciler will attempt a download")
	}
}

// listCountStore wraps store.Store and records every ListEngineModels call so tests can assert the
// lazy-fetch path without an external counter.
type listCountStore struct {
	store.Store
	calls int
}

func (s *listCountStore) ListEngineModels(ctx context.Context, role string) ([]store.EngineModel, error) {
	if role == "" {
		// Count only the all-roles scan (role == "") used by the destination-conflict check.
		// The role-scoped scan (role != "") is the reuse/move lookup in enginePartsHeld and
		// is unavoidable; counting it here would make the fixture fragile to that path.
		s.calls++
	}
	return s.Store.ListEngineModels(ctx, role)
}

// When all parts are already in the bucket (reuse), no ListEngineModels call must happen.
// The destination-conflict check is only needed for download parts, and a plan that costs
// nothing to download should never pay a full-catalogue scan either.
func TestPlanAllReusePartsSkipListEngineModels(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, e, st, head := enginePlanAPI(t)

	clipKey := "image/text_encoders/qwen_3_06b_base.safetensors"
	vaeKey := "image/vae/qwen_image_vae.safetensors"
	// Declare both part files as existing rows with the correct identity so enginePlanLine
	// returns reuse for each. The stub always answers commit-a and a per-file sha.
	for _, row := range []struct {
		id, flag, key, file string
	}{
		{"enc", "--clip_l", clipKey, "split_files/text_encoders/qwen_3_06b_base.safetensors"},
		{"vae", "--vae", vaeKey, "split_files/vae/qwen_image_vae.safetensors"},
	} {
		if err := st.PutEngineModel(t.Context(), store.EngineModel{
			Role: "image", ID: row.id, Kind: "checkpoint",
			Files: []store.EngineModelFile{{
				Flag: row.flag, S3Key: row.key,
				Source:           "hf:circlestone-labs/Anima/" + row.file,
				ArtifactIdentity: engineHFArtifactIdentity("circlestone-labs/Anima", row.file, "commit-a", engineStubSHA(engineBaseName(row.file))),
			}},
		}); err != nil {
			t.Fatal(err)
		}
		head.states[row.key] = engineStoragePresent
	}
	e.catalog.invalidate()

	cs := &listCountStore{Store: st}
	a.mgr.store = cs

	plan := enginePlanOf(t, a, e, `{"kind":"checkpoint","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`)

	// Both parts are reuse — no download parts — so ListEngineModels must not have been called.
	for _, f := range plan.Files {
		if (f.Flag == "--clip_l" || f.Flag == "--vae") && f.Action != enginePlanReuse {
			t.Errorf("expected reuse for %s, got %s — fixture is wrong", f.Flag, f.Action)
		}
	}
	if cs.calls != 0 {
		t.Errorf("ListEngineModels called %d time(s) for an all-reuse plan, want 0", cs.calls)
	}
}
