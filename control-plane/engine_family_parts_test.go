package main

// Taking a SPLIT family in as one act (ADR 0072 decision 2, follow-up to the VAE remedy).
//
// The fault these cover is not a crash: it is three separate downloads, in an order nobody
// documents, ending in a row that is still marked — which is what taking Anima in actually cost
// before this existed.

import (
	"context"
	"encoding/json"
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
	st := ingestStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	// 🔴 The bucket, and it is now part of the fixture rather than an implementation detail: a
	// part is declared onto a second row only when the OBJECT is there (ADR 0072 decision 2's
	// second layer, which ADR 0085 makes the rule for every declaration). Another row's word for
	// it is not proof — its bytes may have been purged.
	head := &fakeEngineStorageHead{states: map[string]string{
		"image/text_encoders/qwen_3_06b_base.safetensors":    engineStoragePresent,
		"image/vae/qwen_image_vae.safetensors":               engineStoragePresent,
		"image/diffusion_models/anima-turbo.safetensors":     engineStoragePresent,
		"image/diffusion_models/anima-aesthetic.safetensors": engineStoragePresent,
	}, bytes: map[string]int64{
		"image/text_encoders/qwen_3_06b_base.safetensors": 1_190_000_000,
		"image/vae/qwen_image_vae.safetensors":            253_800_000,
	}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}},
		cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
		storage: newEngineStorage("models", head),
	}
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

// 🔴 A part is not a model, and since ADR 0085 decision 1 a REQUEST cannot claim otherwise: the
// role a file plays is the family's answer, not a field on the wire. What put encoders in the
// registered list beside the checkpoints — where they can never be enabled and help nothing — was
// a form that let somebody pick `--clip_l` and "new" in the same breath.
//
// So the claim is ignored rather than refused: the plan stages Anima's weights under the role its
// template reads, and the encoder it needs arrives as a part of that row.
func TestIngestIgnoresARoleTheRequestClaims(t *testing.T) {
	engineHFRepoStub(t, engineAnimaRepos())
	a, _, st, _ := enginePlanAPI(t)

	rec := httptest.NewRecorder()
	body := `{"kind":"checkpoint","base_model":"anima","file_flag":"--clip_l","license_accepted":true,
	  "source":{"hf":{"repo":"circlestone-labs/Anima",
	  "file":"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"}}}`
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("the press = %d (%s)", rec.Code, rec.Body.String())
	}
	jobs := engineJobsOf(t, st, "image")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d", len(jobs))
	}
	var spec engineIngestRequest
	if err := json.Unmarshal([]byte(jobs[0].Spec), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.FileFlag != "--diffusion-model" {
		t.Errorf("the request's claimed role won: %q", spec.FileFlag)
	}
	if jobs[0].S3Key != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		t.Errorf("staged at %q — a name no UNETLoader lists", jobs[0].S3Key)
	}
	// And the encoder is promised as a PART of this row rather than becoming a row of its own.
	var flags []string
	for _, fu := range spec.PartsFollowUp {
		flags = append(flags, fu.Flag)
	}
	if len(flags) != 2 {
		t.Errorf("the family's parts = %v, want the encoder and the VAE", flags)
	}
}

// 🔴 Reported from af-sandbox 2026-09-15: "the S3 key image/text_encoders/qwen_3_06b_base
// .safetensors is already recorded; choose a new destination for this download or use verified
// reuse". The key was held by a row made earlier by hand, the plan could not match its identity,
// and the fallback was a download — which engineIngestDestinationUnused refuses for a taken key.
//
// So the plan must not propose that download at all. What an operator can act on is WHO holds
// the key, and that is what comes back.
func TestPartPlanRefusesAKeyHeldBySomethingItCannotMatch(t *testing.T) {
	part := engineFamilyParts["anima"][0]
	if part.Flag != "--clip_l" {
		t.Fatalf("the fixture assumed anima's first part is the encoder, got %s", part.Flag)
	}
	held := enginePartsHeld{known: map[string]*engineKnownArtifact{
		part.S3Key: {
			S3Key:    part.S3Key,
			ModelIDs: map[string]struct{}{"qwen_3_06b_base": {}},
			// Taken in from somewhere else, so the identity cannot match what this plan resolves.
			ArtifactIdentity: "hf:somebody/else@main/encoder.safetensors#sha256:dead",
			Reusable:         true,
		},
	}}
	// The resolve is stubbed to answer, so the fallthrough this test is about is reachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/models/") {
			// The shape hfStub pins: `lfs.sha256` is what the resolve reads.
			_, _ = w.Write([]byte(`{"sha":"commit-a","cardData":{"license":"other"},"siblings":[
				{"rfilename":"` + part.File + `","size":1190000000,
				 "lfs":{"sha256":"` + strings.Repeat("a", 64) + `"}}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })

	fu := enginePartPlan(context.Background(), held, part)
	if fu.Conflict == "" {
		t.Fatalf("plan = %+v, want the taken key reported rather than a download that is refused", fu)
	}
	if !strings.Contains(fu.Conflict, "qwen_3_06b_base") {
		t.Errorf("conflict = %q, want it to name what is holding the key", fu.Conflict)
	}
	if fu.Staged || fu.Resolved.SHA256 != "" {
		t.Errorf("a conflicted part was also offered as something to do: %+v", fu)
	}
	// It costs nothing and it is drawn as a conflict, not as an unreachable upstream: the
	// upstream is fine, the destination is not.
	if enginePartsBytes([]engineVaeFollowUp{fu}) != 0 {
		t.Error("a part that cannot be downloaded was counted into the total")
	}
	rows := enginePartsPlanRows([]engineVaeFollowUp{fu}, []engineFamilyPart{part})
	if rows[0]["conflict"] == nil || rows[0]["unreachable"] != nil {
		t.Errorf("plan row = %+v, want a conflict and not an unreachable source", rows[0])
	}
}

// 🔴 The mistake an operator fell into by DEFAULT, and the one that made every Anima row on
// af-sandbox useless: the form offered "whole checkpoint" first, and a split family reads no
// unflagged file at all. The row then held a 4 GB file under a role no template looks at and
// reported all three parts missing — including the one it was holding.
//
// Nobody is asked any more. The family says which of its files the weights are, and the negative
// control below is what keeps that from becoming a rule about every family: sdxl IS one whole
// checkpoint and stays one.
func TestIngestStagesASplitFamilysWeightsWhereItsLoaderLooks(t *testing.T) {
	engineHFRepoStub(t, map[string][]string{
		"circlestone-labs/Anima": {"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors",
			"split_files/text_encoders/qwen_3_06b_base.safetensors",
			"split_files/vae/qwen_image_vae.safetensors"},
		"stabilityai/sdxl": {"sd_xl_base_1.0.safetensors"},
	})
	a, _, st, _ := enginePlanAPI(t)

	post := func(repo, file string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		body := `{"kind":"checkpoint","license_accepted":true,
		  "source":{"hf":{"repo":"` + repo + `","file":"` + file + `"}}}`
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
		return rec.Code, rec.Body.String()
	}

	if code, body := post("circlestone-labs/Anima",
		"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors"); code != http.StatusOK {
		t.Fatalf("taking anima in = %d (%s), want 200", code, body)
	}
	// 🔴 The negative control: sdxl is one whole checkpoint, its template reads the unflagged
	// slot, and taking one in must stay the ordinary act it has always been.
	if code, body := post("stabilityai/sdxl", "sd_xl_base_1.0.safetensors"); code != http.StatusOK {
		t.Fatalf("taking an sdxl checkpoint in = %d (%s), want 200", code, body)
	}
	want := map[string]string{
		"anima-aesthetic-v1.1": "image/diffusion_models/anima-aesthetic-v1.1.safetensors",
		"sd_xl_base_1.0":       "image/checkpoints/sd_xl_base_1.0.safetensors",
	}
	for _, j := range engineJobsOf(t, st, "image") {
		if key, ok := want[j.ModelID]; ok {
			if j.S3Key != key {
				t.Errorf("%s staged at %q, want %q", j.ModelID, j.S3Key, key)
			}
			delete(want, j.ModelID)
		}
	}
	if len(want) != 0 {
		t.Errorf("no job was written for %v", want)
	}
}
