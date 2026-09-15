package main

// Taking a SPLIT family in as one act (ADR 0072 decision 2, follow-up to the VAE remedy).
//
// The fault these cover is not a crash: it is three separate downloads, in an order nobody
// documents, ending in a row that is still marked — which is what taking Anima in actually cost
// before this existed.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// 🔴 The table is the thing a wrong entry ruins minutes later, so it is checked against the
// vocabulary it is written in: every part names a flag its family's template actually reads
// (engineComfyRequiredFlags), and names a Hugging Face repository rather than a bare file.
func TestFamilyPartsNameFilesTheirFamilyReads(t *testing.T) {
	for family, parts := range engineFamilyParts {
		want := map[string]bool{}
		for _, flag := range engineComfyRequiredFlags[family] {
			want[flag] = true
		}
		if len(want) == 0 {
			t.Errorf("%s declares parts but the provider requires no files for it", family)
			continue
		}
		for _, p := range parts {
			if !want[p.Flag] {
				t.Errorf("%s declares a %s part, which its template does not read (%v)",
					family, p.Flag, engineComfyRequiredFlags[family])
			}
			if !strings.Contains(p.Repo, "/") || p.File == "" || !strings.HasPrefix(p.S3Key, "image/") {
				t.Errorf("%s's %s part is not a repository, file and image key: %+v", family, p.Flag, p)
			}
		}
		// The diffusion model is the row's own file and is never a "part" — offering it would
		// mean downloading the thing that created the row a second time.
		for _, p := range parts {
			if p.Flag == "--diffusion-model" || p.Flag == "" {
				t.Errorf("%s offers its own checkpoint as a part", family)
			}
		}
	}
}

// 🔴 Two families, ONE file. Anima and Krea 2 both read the Qwen-Image VAE and it is the same
// bytes — but the reuse check compares the artifact identity, which carries the repository, so
// declaring them from different repositories would download it twice into two keys. This is the
// test that keeps the table honest about that.
func TestFamilyPartsShareOneSourceForOneKey(t *testing.T) {
	byKey := map[string]engineFamilyPart{}
	for family, parts := range engineFamilyParts {
		for _, p := range parts {
			if seen, ok := byKey[p.S3Key]; ok && (seen.Repo != p.Repo || seen.File != p.File) {
				t.Errorf("%s declares %s from %s/%s while another family declares it from %s/%s:"+
					" the identities differ, so it would be downloaded twice",
					family, p.S3Key, p.Repo, p.File, seen.Repo, seen.File)
			}
			byKey[p.S3Key] = p
		}
	}
}

// What a row is still missing is read off the row, so a part attached by hand — or one this
// deployment declared from somewhere else — is not offered again.
func TestPartsMissingReadsTheRowsOwnDeclarations(t *testing.T) {
	row := store.EngineModel{BaseModel: "anima", Files: []store.EngineModelFile{
		{Flag: "--diffusion-model", S3Key: "image/diffusion_models/anima.safetensors"},
		{Flag: "--vae", S3Key: "image/vae/somebody-elses-vae.safetensors"},
	}}
	missing := enginePartsMissing("anima", row)
	if len(missing) != 1 || missing[0].Flag != "--clip_l" {
		t.Fatalf("missing = %+v, want only the text encoder", missing)
	}
	full := row
	full.Files = append(full.Files, store.EngineModelFile{Flag: "--clip_l", S3Key: "image/text_encoders/x.safetensors"})
	if got := enginePartsMissing("anima", full); len(got) != 0 {
		t.Errorf("a complete row still wants %+v", got)
	}
	// A family with no part list is not a family with no parts — it is one nobody measured, and
	// the answer is nothing rather than a guess.
	if got := enginePartsMissing("flux2-klein", store.EngineModel{BaseModel: "flux2-klein"}); got != nil {
		t.Errorf("an unmeasured family offered %+v", got)
	}
}

// 🔴 The point of the whole feature for a deployment that already runs one of these families:
// bytes it already has are DECLARED, not downloaded again. A row declaring the key is the
// cheapest proof and costs no S3 call at all.
func TestPartPlanDeclaresWhatARowAlreadyHas(t *testing.T) {
	held := enginePartsHeld{rows: []store.EngineModel{{
		ID: "krea2-turbo", Files: []store.EngineModelFile{{
			Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors",
			Bytes: 253_806_1, Source: "hf:circlestone-labs/Anima/split_files/vae/qwen_image_vae.safetensors",
			ArtifactIdentity: "hf:circlestone-labs/Anima@main/split_files/vae/qwen_image_vae.safetensors#sha256:a705",
		}},
	}}}
	vae := engineFamilyParts["anima"][1]
	if vae.Flag != "--vae" {
		t.Fatalf("the fixture assumed anima's second part is the VAE, got %s", vae.Flag)
	}
	// No HTTP stub is installed: a plan that reached the network here would fail, which is the
	// assertion — this path answers from the catalogue alone.
	fu := enginePartPlan(context.Background(), held, vae)
	if !fu.Staged || fu.S3Key != vae.S3Key || fu.Flag != "--vae" {
		t.Fatalf("plan = %+v, want the key declared rather than fetched", fu)
	}
	if fu.ArtifactIdentity == "" || fu.Source == "" {
		t.Errorf("the reused declaration lost where the bytes came from: %+v", fu)
	}
	if enginePartsBytes([]engineVaeFollowUp{fu}) != 0 {
		t.Error("a part already here was counted as something to download")
	}
}

// The remedy for rows that already exist — which is every row on a deployment that took this
// family in before the checkbox existed.
func TestFixPartsCompletesAnExistingRowFromWhatIsAlreadyHere(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	ctx := t.Context()
	// One row of the family already holds both parts; the new row holds only its diffusion model.
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "anima-turbo", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/anima-turbo.safetensors"},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen_3_06b_base.safetensors", Bytes: 1_190_000_000},
			{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors", Bytes: 253_800_000},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "anima-aesthetic", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/anima-aesthetic.safetensors"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/models/anima-aesthetic/parts", strings.NewReader(`{}`))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", "anima-aesthetic")
	a.fixParts(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("fixParts = %d %s", rec.Code, rec.Body.String())
	}
	// 🔴 `attached`, not `job_started`: nothing was downloaded and no licence was asked for,
	// because the bytes are already this deployment's under a licence somebody accepted when
	// they were taken in.
	if !strings.Contains(rec.Body.String(), `"action":"attached"`) {
		t.Errorf("answer = %s, want the parts declared from what is already here", rec.Body.String())
	}
	rows := e.catalog.list(ctx)
	var fixed store.EngineModel
	for _, m := range rows {
		if m.ID == "anima-aesthetic" {
			fixed = m
		}
	}
	if got := enginePartsMissing("anima", fixed); len(got) != 0 {
		t.Errorf("the row is still missing %+v", got)
	}
	if len(engineMissingFileFlags("comfy", fixed)) != 0 {
		t.Errorf("the row is still refused by the files guard: %+v", fixed.Files)
	}
	// Pressing again is a no-op rather than a second set of files.
	again := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/api/admin/engines/image/models/anima-aesthetic/parts", strings.NewReader(`{}`))
	r2.SetPathValue("key", "image")
	r2.SetPathValue("id", "anima-aesthetic")
	a.fixParts(again, r2, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if !strings.Contains(again.Body.String(), `"action":"none"`) {
		t.Errorf("a second press = %s, want nothing to do", again.Body.String())
	}
}

// A family nobody measured parts for says so, rather than answering an empty plan that reads as
// "this row is fine".
func TestFixPartsRefusesAFamilyWithNoPartList(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "klein", Kind: "checkpoint", BaseModel: "flux2-klein",
		Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/klein.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/models/klein/parts", strings.NewReader(`{}`))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", "klein")
	a.fixParts(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "flux2-klein") {
		t.Errorf("fixParts on an unmeasured family = %d %s, want 400 naming it", rec.Code, rec.Body.String())
	}
}

// 🔴 A part is not a model. Registering one as its own row is what put encoders in the
// registered list beside the checkpoints, where they can never be enabled and help nothing.
func TestIngestRefusesToRegisterAPartAsItsOwnRow(t *testing.T) {
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
	}
	e.catalog.invalidate()
	rec := httptest.NewRecorder()
	body := `{"id":"qwen_3_06b_base","kind":"checkpoint","base_model":"anima","file_flag":"--clip_l",
	  "s3Key":"image/text_encoders/qwen_3_06b_base.safetensors","license_accepted":true,
	  "source":{"url":"https://example.invalid/qwen.safetensors","sha256":"` + strings.Repeat("c", 64) + `"}}`
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a part registered as a row = %d %s, want 400", rec.Code, rec.Body.String())
	}
	// The message has to name the act the person meant, which is on the same screen.
	if !strings.Contains(rec.Body.String(), "attach") {
		t.Errorf("the refusal does not say what to do instead: %s", rec.Body.String())
	}
}
