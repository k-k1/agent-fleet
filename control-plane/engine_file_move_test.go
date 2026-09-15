package main

// Repairing bytes that are in the bucket and in the wrong place (engine_file_move.go).
//
// The state every Anima and Krea 2 row on af-sandbox was in on 2026-09-15, read off the live
// deployment: the family's own weights declared as "the whole checkpoint" and staged under
// `image/checkpoints/split_files/diffusion_models/…`. The row reports `--diffusion-model`
// missing while holding 4.2 GB of it, and before this the remedy answered `none`.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// the af-sandbox row, exactly: identity and all, because the move has to carry it across.
func animaMisplacedRow() store.EngineModel {
	return store.EngineModel{
		Role: "image", ID: "anima-aesthetic-v1.1", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{
			{S3Key: "image/checkpoints/split_files/diffusion_models/anima-aesthetic-v1.1.safetensors",
				Bytes:  4_182_230_656,
				Source: "hf:circlestone-labs/Anima/split_files/diffusion_models/anima-aesthetic-v1.1.safetensors",
				ArtifactIdentity: "hf:circlestone-labs/Anima@f973fc41ec7545364ac9776c2440285f43ff2a30/" +
					"split_files/diffusion_models/anima-aesthetic-v1.1.safetensors#sha256:" + strings.Repeat("a", 64)},
			{Flag: "--clip_l", S3Key: "image/text_encoders/qwen_3_06b_base.safetensors", Bytes: 1_192_135_096},
			{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors", Bytes: 253_806_246},
		},
	}
}

func TestMainFileFixNamesTheRoleAndTheKeyTheLoaderReads(t *testing.T) {
	fix, ok := engineMainFileFixFor("image", "anima", animaMisplacedRow())
	if !ok {
		t.Fatal("the af-sandbox row was not recognised as misplaced weights")
	}
	if fix.Flag != "--diffusion-model" {
		t.Errorf("flag = %q, want the role anima's template reads", fix.Flag)
	}
	if fix.To != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		// 🔴 FLAT. Keeping the repository's own `split_files/…` below the role directory is the
		// second half of the same fault: the Agent names a file by its base name, so a key one
		// directory deeper is as unreadable as one in `checkpoints/`.
		t.Errorf("to = %q, want the role's directory and the base name", fix.To)
	}
	if fix.File.ArtifactIdentity == "" || fix.File.Source == "" || fix.File.Bytes == 0 {
		t.Errorf("the move lost what is known about the bytes: %+v", fix.File)
	}
	if fix.File.Flag != "--diffusion-model" || fix.File.S3Key != fix.To {
		t.Errorf("the declaration to write = %+v, want the new role at the new key", fix.File)
	}

	// And every row there is nothing to say about.
	whole := store.EngineModel{Role: "image", ID: "sdxl", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}}}
	if _, ok := engineMainFileFixFor("image", "sdxl", whole); ok {
		t.Error("a family whose template READS a whole checkpoint was called misplaced")
	}
	ok2 := func(m store.EngineModel) bool {
		_, ok := engineMainFileFixFor("image", "anima", m)
		return ok
	}
	done := animaMisplacedRow()
	done.Files[0] = store.EngineModelFile{Flag: "--diffusion-model",
		S3Key: "image/diffusion_models/anima-aesthetic-v1.1.safetensors"}
	if ok2(done) {
		t.Error("a repaired row is still offered the repair")
	}
	both := animaMisplacedRow()
	both.Files = append(both.Files, store.EngineModelFile{Flag: "--diffusion-model",
		S3Key: "image/diffusion_models/other.safetensors"})
	if ok2(both) {
		// The slot is taken: whatever the unflagged file is, moving it there would give the row
		// two files under one role and the last writer would decide what the loader gets.
		t.Error("a row that already reads weights was offered a second set")
	}
	lora := animaMisplacedRow()
	lora.Kind = "lora"
	if ok2(lora) {
		t.Error("an adapter was offered a checkpoint repair")
	}
}

// 🔴 The whole complaint, end to end: "「不足ファイルを揃える」を押しても、全てが揃わない".
// The parts are attached, the badge still reads `不足: --diffusion-model`, and the answer used
// to be `none`. Now it is one server-side move, and the row is complete when it lands.
func TestFixPartsMovesMisplacedWeightsRatherThanDownloadingThemAgain(t *testing.T) {
	st := ingestStore(t)
	ctx := t.Context()
	if err := st.PutEngineModel(ctx, animaMisplacedRow()); err != nil {
		t.Fatal(err)
	}
	from := animaMisplacedRow().Files[0].S3Key
	head := &fakeEngineStorageHead{states: map[string]string{from: engineStoragePresent}}
	ecsAPI := &fakeIngestECS{}
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"}},
		cluster: "c", ecs: ecsAPI, store: st, models: st, storage: newEngineStorage("models", head),
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	// What the card says before anything is pressed, which is the other half of the fault: the
	// badge named a part to take in and nothing said the row was holding it.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/engines", nil)
	a.get(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if !strings.Contains(rec.Body.String(), `"main_file_fix"`) ||
		!strings.Contains(rec.Body.String(), `"image/diffusion_models/anima-aesthetic-v1.1.safetensors"`) {
		t.Fatalf("the panel is not told where the weights belong: %s", rec.Body.String())
	}

	press := func() (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost,
			"/api/admin/engines/image/models/anima-aesthetic-v1.1/parts", strings.NewReader(`{}`))
		r.SetPathValue("key", "image")
		r.SetPathValue("id", "anima-aesthetic-v1.1")
		a.fixParts(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := press()
	if code != http.StatusOK || out["action"] != "moving" {
		t.Fatalf("fixParts = %d %v, want a move (and NOT a download of bytes this deployment owns)", code, out)
	}
	if len(ecsAPI.run) != 1 {
		t.Fatalf("%d tasks started, want exactly the move", len(ecsAPI.run))
	}
	env := map[string]map[string]string{}
	for _, c := range ecsAPI.run[0].Overrides.ContainerOverrides {
		kv := map[string]string{}
		for _, pair := range c.Environment {
			kv[aws.ToString(pair.Name)] = aws.ToString(pair.Value)
		}
		env[aws.ToString(c.Name)] = kv
	}
	if env["fetch"]["MODE"] != "move" || env["fetch"]["URL"] != "" {
		t.Errorf("the fetch container = %v, want it told to download nothing", env["fetch"])
	}
	if env["upload"]["MODE"] != "move" || env["upload"]["FROM"] != from ||
		env["upload"]["KEY"] != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		t.Errorf("the upload container = %v, want one `aws s3 mv` inside the bucket", env["upload"])
	}

	// Nothing about the row changes at the door: the declaration follows the bytes, and it
	// follows them when the task has actually moved them.
	rows, _ := st.ListEngineModels(ctx, "image")
	if rows[0].Files[0].S3Key != from {
		t.Fatalf("the row was rewritten before the move landed: %+v", rows[0].Files)
	}
	// Pressing again while it runs must not start a second one — the key is in flight.
	if code, out := press(); code != http.StatusConflict {
		t.Errorf("a second press while the move runs = %d %v, want a refusal naming the job", code, out)
	}

	// The task lands.
	jobs, err := st.ListEngineIngestJobs(ctx, "image", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %d (%v)", len(jobs), err)
	}
	var req engineIngestRequest
	if err := json.Unmarshal([]byte(jobs[0].Spec), &req); err != nil {
		t.Fatal(err)
	}
	if err := reg.ing.install(ctx, req, jobs[0].ID); err != nil {
		t.Fatalf("install: %v", err)
	}
	rows, _ = st.ListEngineModels(ctx, "image")
	moved := rows[0].Files[0]
	if moved.Flag != "--diffusion-model" || moved.S3Key != "image/diffusion_models/anima-aesthetic-v1.1.safetensors" {
		t.Fatalf("the row still reads %+v", rows[0].Files)
	}
	if len(rows[0].Files) != 3 {
		t.Errorf("the move left the old declaration behind: %+v", rows[0].Files)
	}
	if moved.Bytes == 0 || moved.Source == "" || moved.ArtifactIdentity == "" {
		t.Errorf("the moved file lost its provenance: %+v", moved)
	}
	if got := engineMissingFileFlags("comfy", rows[0]); len(got) != 0 {
		t.Errorf("the row is still refused for %v", got)
	}
	// And the reconciler seeing the same finished task twice is a no-op, not a failure.
	if err := reg.ing.install(ctx, req, jobs[0].ID); err != nil {
		t.Errorf("a second install of the same finished move failed: %v", err)
	}
}

// 🔴 A failed attempt must not fence off the repair it was attempting. Reported from af-sandbox
// 2026-09-15: the first press ran while the CP still held a deregistered task definition, RunTask
// answered 400, and the three job rows it left behind then refused every retry with "the S3 key
// image/diffusion_models/anima-aesthetic-v1.1.safetensors is already recorded" — a key nothing
// had ever written to, because no task existed to write it.
func TestFixPartsRetriesAfterAnAttemptThatNeverStartedATask(t *testing.T) {
	st := ingestStore(t)
	ctx := t.Context()
	if err := st.PutEngineModel(ctx, animaMisplacedRow()); err != nil {
		t.Fatal(err)
	}
	from := animaMisplacedRow().Files[0].S3Key
	head := &fakeEngineStorageHead{states: map[string]string{from: engineStoragePresent}}
	ecsAPI := &fakeIngestECS{fail: "TaskDefinition is inactive"}
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}},
		cluster: "c", ecs: ecsAPI, store: st, models: st, storage: newEngineStorage("models", head),
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	press := func() (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost,
			"/api/admin/engines/image/models/anima-aesthetic-v1.1/parts", strings.NewReader(`{}`))
		r.SetPathValue("key", "image")
		r.SetPathValue("id", "anima-aesthetic-v1.1")
		a.fixParts(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
		return rec.Code, rec.Body.String()
	}
	if code, body := press(); code != http.StatusBadGateway {
		t.Fatalf("a refused RunTask = %d (%s), want the start failure reported", code, body)
	}
	jobs, _ := st.ListEngineIngestJobs(ctx, "image", 10)
	if len(jobs) != 1 || jobs[0].State != store.EngineIngestFailed || jobs[0].TaskArn != "" {
		t.Fatalf("the wreckage of the attempt = %+v, want one failed job with no task", jobs)
	}

	// The cause is fixed; the press has to work.
	ecsAPI.fail = ""
	code, body := press()
	if code != http.StatusOK || !strings.Contains(body, `"action":"moving"`) {
		t.Fatalf("the retry = %d (%s), want the move to start", code, body)
	}
}

// 🔴 Bytes two rows read are not one row's to move. The other row's declaration would go on
// naming a key with nothing at it — the silent shape of breakage this repo keeps paying for.
func TestFixPartsRefusesToMoveBytesAnotherRowDeclares(t *testing.T) {
	st := ingestStore(t)
	ctx := t.Context()
	row := animaMisplacedRow()
	if err := st.PutEngineModel(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "image", ID: "anima-copy", Kind: "checkpoint", BaseModel: "anima",
		Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: row.Files[0].S3Key}},
	}); err != nil {
		t.Fatal(err)
	}
	from := row.Files[0].S3Key
	head := &fakeEngineStorageHead{states: map[string]string{from: engineStoragePresent}}
	ecsAPI := &fakeIngestECS{}
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}},
		cluster: "c", ecs: ecsAPI, store: st, models: st, storage: newEngineStorage("models", head),
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/admin/engines/image/models/anima-aesthetic-v1.1/parts", strings.NewReader(`{}`))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", "anima-aesthetic-v1.1")
	a.fixParts(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "anima-copy") {
		t.Fatalf("moving shared bytes = %d (%s), want a refusal naming the other row", rec.Code, rec.Body.String())
	}
	if len(ecsAPI.run) != 0 {
		t.Error("the refusal still started a task")
	}
}

// A declaration whose object was purged is a row to take in again. Moving nothing succeeds in
// the task and leaves a row pointing at an empty key, which every check would call complete.
func TestFixPartsRefusesToMoveBytesThatAreNotThere(t *testing.T) {
	st := ingestStore(t)
	if err := st.PutEngineModel(t.Context(), animaMisplacedRow()); err != nil {
		t.Fatal(err)
	}
	from := animaMisplacedRow().Files[0].S3Key
	head := &fakeEngineStorageHead{states: map[string]string{from: engineStorageMissing}}
	ecsAPI := &fakeIngestECS{}
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}},
		cluster: "c", ecs: ecsAPI, store: st, models: st, storage: newEngineStorage("models", head),
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/api/admin/engines/image/models/anima-aesthetic-v1.1/parts", strings.NewReader(`{}`))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", "anima-aesthetic-v1.1")
	a.fixParts(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "taken in again") {
		t.Fatalf("moving absent bytes = %d (%s), want a refusal that says what to do", rec.Code, rec.Body.String())
	}
	if len(ecsAPI.run) != 0 {
		t.Error("the refusal still started a task")
	}
}

// 🔴 The other half of the fault, refused where it is created. Both spellings the form produced
// on af-sandbox are keys no ComfyUI loader can offer, and neither reports anything at
// generation time beyond "the model does not work".
func TestIngestRefusesAKeyTheLoaderCannotList(t *testing.T) {
	st := ingestStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	reg.ing = &engineIngester{
		def:     engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}},
		cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	post := func(key string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		body := `{"id":"anima-aesthetic","kind":"checkpoint","s3Key":"` + key + `","base_model":"anima",
		  "file_flag":"--diffusion-model","license_accepted":true,
		  "source":{"url":"https://example.invalid/a.safetensors","sha256":"` + strings.Repeat("d", 64) + `"}}`
		r := httptest.NewRequest(http.MethodPost, "/api/admin/engines/image/ingest", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
		return rec.Code, rec.Body.String()
	}
	// The role's directory, but the upstream's own path kept below it.
	code, body := post("image/diffusion_models/split_files/diffusion_models/a.safetensors")
	if code != http.StatusBadRequest || !strings.Contains(body, "image/diffusion_models/a.safetensors") {
		t.Errorf("a nested key = %d (%s), want a refusal naming where it belongs", code, body)
	}
	// Another role's directory entirely.
	if code, body := post("image/checkpoints/a.safetensors"); code != http.StatusBadRequest {
		t.Errorf("a checkpoint-directory diffusion model = %d (%s), want a refusal", code, body)
	}
	if code, body := post("image/diffusion_models/a.safetensors"); code != http.StatusOK {
		t.Errorf("the right key = %d (%s), want 200", code, body)
	}
}
