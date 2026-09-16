package main

// The bucket read as the ledger (ADR 0085 decisions 2, 3 and 7).
//
// The fault these cover is not a crash: it is 24 GB the panel could not show, in a deployment
// whose rows had been forgotten — and an operator with no road back to bytes they are paying for.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineLedgerHarness is an admin API with a bucket behind it. The `bucket` map IS the deployment's
// storage: a key `present` in it is an object both the listing and HeadObject answer for, which
// keeps one fixture as the single statement about what exists.
type engineLedgerHarness struct {
	a      engineAdminAPI
	e      *engineRuntimeState
	st     *store.SQL
	bucket *fakeEngineStorageHead
	ecs    *fakeIngestECS
}

func newEngineLedgerHarness(t *testing.T) *engineLedgerHarness {
	t.Helper()
	st := ingestStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	h := &engineLedgerHarness{
		e: e, st: st, ecs: &fakeIngestECS{},
		bucket: &fakeEngineStorageHead{states: map[string]string{}, bytes: map[string]int64{}},
	}
	reg.ing = &engineIngester{
		def: engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"},
			SecurityGroups: []string{"sg-1"}, Bucket: "models"},
		cluster: "c", ecs: h.ecs, store: st, models: st,
		storage: newEngineStorage("models", h.bucket),
	}
	h.a = engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	return h
}

// put stages bytes in the fake bucket.
func (h *engineLedgerHarness) put(key string, bytes int64) {
	h.bucket.states[key] = engineStoragePresent
	h.bucket.bytes[key] = bytes
}

// header stages what the first bytes of an object ARE, which is the only thing the VAE question
// can be answered from. A key put without one is present and unreadable — the ordinary state of
// every object in these tests, and the state whose verdict is "nobody read it".
func (h *engineLedgerHarness) header(key string, head []byte) {
	if h.bucket.head == nil {
		h.bucket.head = map[string][]byte{}
	}
	h.bucket.head[key] = head
}

func (h *engineLedgerHarness) row(t *testing.T, m store.EngineModel) {
	t.Helper()
	m.Role = "image"
	if err := h.st.PutEngineModel(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	h.e.catalog.invalidate()
}

func (h *engineLedgerHarness) call(t *testing.T, fn func(http.ResponseWriter, *http.Request, engineIngestGrant),
	method, path, body string, values map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("key", "image")
	for k, v := range values {
		r.SetPathValue(k, v)
	}
	fn(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	return rec
}

// 🔴 The golden. These are the two shapes af-sandbox's bytes were in on 2026-09-15 (ADR 0085
// Context): a split family's weights under `image/checkpoints/` because the role selector's
// default is "the whole checkpoint", and everything under `split_files/…` because the key kept
// the path inside the upstream repository. Both are files no ComfyUI loader can list, and the
// difference between them and the three correct keys below is the whole of `placement`.
//
// ⚠️ The list is RECONSTRUCTED from the ADR's Context and the part table rather than re-read: the
// operator chose to clear those bytes in P0 (about 24 GB, MODE=delete), so the deployment cannot
// be asked again. What it pins is the classification, which is what the screen acts on.
func TestLedgerPinsTheSandboxPlacementClassification(t *testing.T) {
	want := map[string]struct{ dir, placement string }{
		// The eight the operator could not see, and could not repair without a row.
		"image/checkpoints/split_files/diffusion_models/anima_v1.safetensors":       {"checkpoints", engineObjectPlacementMisplaced},
		"image/checkpoints/split_files/diffusion_models/krea2_raw.safetensors":      {"checkpoints", engineObjectPlacementMisplaced},
		"image/checkpoints/split_files/text_encoders/qwen_3_06b_base.safetensors":   {"checkpoints", engineObjectPlacementMisplaced},
		"image/checkpoints/split_files/vae/qwen_image_vae.safetensors":              {"checkpoints", engineObjectPlacementMisplaced},
		"image/diffusion_models/split_files/diffusion_models/anima_v1.safetensors":  {"diffusion_models", engineObjectPlacementMisplaced},
		"image/text_encoders/split_files/text_encoders/qwen_3_06b_base.safetensors": {"text_encoders", engineObjectPlacementMisplaced},
		"image/text_encoders/text_encoders/qwen3vl_4b_fp8_scaled.safetensors":       {"text_encoders", engineObjectPlacementMisplaced},
		"image/vae/split_files/vae/qwen_image_vae.safetensors":                      {"vae", engineObjectPlacementMisplaced},
		// And the three that were right all along — the parts were at BOTH keys.
		"image/diffusion_models/anima_v1.safetensors":     {"diffusion_models", engineObjectPlacementOK},
		"image/text_encoders/qwen_3_06b_base.safetensors": {"text_encoders", engineObjectPlacementOK},
		"image/vae/qwen_image_vae.safetensors":            {"vae", engineObjectPlacementOK},
		// Listed rather than hidden (ADR 0085 decision 2): not a model file, so the table has no
		// opinion about where it belongs — but it is still bytes in the bucket.
		"image/checkpoints/anima_v1.json": {engineObjectRoleDirOther, engineObjectPlacementOK},
		// The llm role's shards, whose first segment is a model name rather than a loader
		// directory (ADR 0085 open question 3). Under `image/` here only to prove the rule is the
		// path's and not the role's.
		"image/qwen3-30b/model-00001-of-00002.gguf": {engineObjectRoleDirOther, engineObjectPlacementOK},
	}
	for key, w := range want {
		dir := engineObjectRoleDir("image", key)
		if dir != w.dir {
			t.Errorf("%s: role_dir = %q, want %q", key, dir, w.dir)
		}
		if got := engineObjectPlacement("image", key, dir); got != w.placement {
			t.Errorf("%s: placement = %q, want %q", key, got, w.placement)
		}
	}
}

// The classification is engineComfyKeyFor read backwards, so the key that function COMPOSES must
// classify as ok — otherwise the ledger would mark every freshly taken-in file as misplaced and
// the repair button would move files that are already right.
func TestLedgerAgreesWithTheKeyTheIngestComposes(t *testing.T) {
	for _, flag := range engineComfyFileFlags {
		for _, lora := range []bool{false, true} {
			key := engineComfyKeyFor("image", flag, "split_files/x/model.safetensors", lora)
			dir := engineObjectRoleDir("image", key)
			if dir == engineObjectRoleDirOther {
				t.Errorf("%s (flag %q, lora %v) classified as %q", key, flag, lora, dir)
				continue
			}
			if got := engineObjectPlacement("image", key, dir); got != engineObjectPlacementOK {
				t.Errorf("%s (flag %q, lora %v) = %q, want ok", key, flag, lora, got)
			}
		}
	}
}

// The join: the bucket says what exists, the database says who reads it, and a row pointing at
// nothing is a ledger fact rather than a silence.
func TestLedgerJoinsTheBucketWithTheCatalogueAndTheJobs(t *testing.T) {
	now := time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC)
	l := engineLedgerJoin("image",
		[]engineStorageObject{
			{Key: "image/vae/qwen_image_vae.safetensors", Bytes: 253_800_000, LastModified: now},
			{Key: "image/checkpoints/orphan.safetensors", Bytes: 42},
		},
		[]store.EngineModel{{
			Role: "image", ID: "anima-v1", LicenseName: "apache-2.0", LicenseAcceptedTenant: "t1",
			Files: []store.EngineModelFile{
				{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors", Source: "hf:circlestone-labs/Anima"},
				{Flag: "--diffusion-model", S3Key: "image/diffusion_models/gone.safetensors"},
				// Another engine's prefix: its own ledger's business, not this one's.
				{Flag: "", S3Key: "llm/elsewhere.gguf"},
			},
		}},
		[]store.EngineIngestJob{{
			ID: "j1", S3Key: "image/text_encoders/qwen_3_06b_base.safetensors",
			State: store.EngineIngestFailed, Message: "sha256 mismatch", CreatedAt: "2026-09-15T03:42:00Z",
			TenantID: "t2",
		}})

	vae := l.at("image/vae/qwen_image_vae.safetensors")
	if vae == nil || vae.State != engineStoragePresent || len(vae.DeclaredBy) != 1 ||
		vae.DeclaredBy[0].ModelID != "anima-v1" || vae.DeclaredBy[0].Flag != "--vae" {
		t.Fatalf("the declared VAE = %+v", vae)
	}
	if vae.License != "apache-2.0" || vae.LastModified != "2026-09-15T04:00:00Z" {
		t.Errorf("the declared VAE lost its licence or its date: %+v", vae)
	}
	// 🔴 The line the old storage route could not draw at all: bytes nobody reads.
	if orphan := l.at("image/checkpoints/orphan.safetensors"); orphan == nil ||
		len(orphan.DeclaredBy) != 0 || orphan.State != engineStoragePresent {
		t.Errorf("the orphan = %+v, want present and declared by nobody", l.at("image/checkpoints/orphan.safetensors"))
	}
	// And its opposite: a row pointing at a key the bucket does not hold.
	if gone := l.at("image/diffusion_models/gone.safetensors"); gone == nil || gone.State != engineStorageMissing {
		t.Errorf("the purged key = %+v, want missing", l.at("image/diffusion_models/gone.safetensors"))
	}
	if l.at("llm/elsewhere.gguf") != nil {
		t.Error("the ledger claimed a key outside its own engine's prefix")
	}
	// A failed job is its destination object's state, with the one act it earns (decision 6).
	failed := l.at("image/text_encoders/qwen_3_06b_base.safetensors")
	if failed == nil || failed.State != engineObjectFailed || failed.Job == nil || failed.Job.ID != "j1" {
		t.Fatalf("the failed job's key = %+v", failed)
	}
	if failed.Job.Message != "sha256 mismatch" {
		t.Errorf("the failure lost the task's own words: %+v", failed.Job)
	}
	// Visibility follows the job list's rule: each tenant sees what it put there.
	// t1's row declares two of them — including the key whose bytes are gone, which is the one a
	// tenant most needs to see. The orphan and the other tenant's failed job are not its business.
	for _, tc := range []struct {
		tenant string
		want   []string
	}{
		{"t1", []string{"image/diffusion_models/gone.safetensors", "image/vae/qwen_image_vae.safetensors"}},
		{"t2", []string{"image/text_encoders/qwen_3_06b_base.safetensors"}},
		{"t3", nil},
	} {
		got := engineLedgerVisible(l, engineIngestGrant{tenantID: tc.tenant})
		var keys []string
		for _, row := range got {
			keys = append(keys, row.Key)
		}
		if strings.Join(keys, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s sees %v, want %v", tc.tenant, keys, tc.want)
		}
	}
	if got := engineLedgerVisible(l, engineIngestGrant{super: true}); len(got) != 4 {
		t.Errorf("the operator sees %d rows, want every one of them", len(got))
	}
}

// 🔴 An empty ledger and a refused listing are opposite facts, and the act the screen offers on an
// object nothing declares is deletion. Answering "the bucket is empty" for a listing that was
// denied is therefore the one failure this route must never have.
func TestLedgerRefusesRatherThanAnsweringAnEmptyBucket(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.bucket.listErr = errEngineStorageUnconfigured
	rec := h.call(t, h.a.getObjects, "GET", "/api/admin/engines/image/objects", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a refused listing = %d %s, want 503", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"objects":[]`) {
		t.Error("the refusal carried an empty ledger, which reads as an empty bucket")
	}
}

func TestLedgerRouteAnswersTheJoinedTable(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.put("image/checkpoints/split_files/diffusion_models/anima_v1.safetensors", 4_180_000_000)
	h.put("image/vae/qwen_image_vae.safetensors", 253_800_000)
	h.row(t, store.EngineModel{ID: "krea2", BaseModel: "krea2", Kind: "checkpoint",
		Files: []store.EngineModelFile{{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors"}}})

	rec := h.call(t, h.a.getObjects, "GET", "/api/admin/engines/image/objects", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("getObjects = %d %s", rec.Code, rec.Body.String())
	}
	var answer engineObjectsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Objects) != 2 || answer.CheckedAt == "" {
		t.Fatalf("answer = %+v", answer)
	}
	main := answer.Objects[0]
	if main.Placement != engineObjectPlacementMisplaced || main.RoleDir != "checkpoints" ||
		len(main.DeclaredBy) != 0 || main.Bytes != 4_180_000_000 {
		t.Errorf("the misplaced weights = %+v", main)
	}
}

// The one object-side act, and the road back for a deployment whose rows are gone: press 登録 on
// the misplaced main file and the row is rebuilt, the bytes are moved to the key a loader lists,
// and the parts the ledger already holds are attached by the same press.
func TestRegisterRebuildsARowAndMovesItsWeights(t *testing.T) {
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
	if answer.ModelID != "anima_v1" || !answer.Moved {
		t.Fatalf("answer = %+v, want the proposed id and a move", answer)
	}
	m, ok := engineCatalogModel(t.Context(), h.e, "anima_v1")
	if !ok {
		t.Fatal("no row was created")
	}
	// 🔴 Disabled, and with NO licence acceptance: assigning bytes that are already here is not
	// the human act of accepting a licence, and the person pressing may not be the person who did.
	if m.Enabled || m.LicenseAcceptedBy != "" {
		t.Errorf("the registered row = enabled %v, accepted by %q", m.Enabled, m.LicenseAcceptedBy)
	}
	// The declaration names the key the bytes are at NOW. The job rewrites it when the move lands;
	// a row registered at the destination would have nothing to move.
	if len(m.Files) != 3 {
		t.Fatalf("files = %+v, want the weights plus the two parts the ledger held", m.Files)
	}
	var held []string
	for _, f := range m.Files {
		held = append(held, f.Flag+"@"+f.S3Key)
	}
	want := []string{
		"--clip_l@image/text_encoders/qwen_3_06b_base.safetensors",
		"--vae@image/vae/qwen_image_vae.safetensors",
		"@" + misplaced,
	}
	for _, w := range want {
		if !containsString(held, w) {
			t.Errorf("the row does not hold %s: %v", w, held)
		}
	}
	// One task, and it is a MOVE: nothing crossed the internet.
	if len(h.ecs.run) != 1 {
		t.Fatalf("%d tasks were started, want the one move", len(h.ecs.run))
	}
	env := engineTaskEnv(h.ecs.run[0])
	if env["upload"]["MODE"] != "move" || env["upload"]["FROM"] != misplaced ||
		env["upload"]["KEY"] != "image/diffusion_models/anima_v1.safetensors" {
		t.Errorf("the upload container = %v, want one `aws s3 mv` inside the bucket", env["upload"])
	}
}

// 🔴 The side effect the scan's removal left behind (ADR 0085 P3, reported on PR #704): a row
// registered from the bucket never had its checkpoint header read, so an SDXL file published
// without a VAE was neither marked `vae_missing` nor offered the family's VAE by the 揃える the
// same press runs — and the next act on it is Enable, straight into `VAE is invalid: None` on
// every request. The bytes are HERE, so the header is read from the bucket.
//
// The pair is the point. A checkpoint that bundles its own VAE must come out of the same press
// with nothing attached; a rule that attached one to both would put the wrong autoencoder on half
// the catalogue.
func TestRegisterReadsTheVaeVerdictOutOfTheBucket(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tensors []string
		verdict string
		attach  bool
	}{
		{
			name:    "published with no VAE",
			tensors: []string{"model.diffusion_model.input_blocks.0.0.weight"},
			verdict: engineVaeNo, attach: true,
		},
		{
			name: "bundling its own",
			tensors: []string{"model.diffusion_model.input_blocks.0.0.weight",
				"first_stage_model.decoder.conv_in.weight"},
			verdict: engineVaeYes, attach: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEngineLedgerHarness(t)
			const key = "image/checkpoints/somexl_v3.safetensors"
			h.put(key, 6_900_000_000)
			h.header(key, safetensorsFixture(t, tc.tensors))
			// The family's own VAE, already in the bucket: the remedy is then a declaration and
			// this test reaches no network at all.
			h.put(engineFamilyVaes["sdxl"].S3Key, 334_600_000)

			rec := h.call(t, h.a.postObjectRegister, "POST", "/api/admin/engines/image/objects/register",
				`{"key":"`+key+`","base_model":"sdxl"}`, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("register = %d %s", rec.Code, rec.Body.String())
			}
			m, ok := engineCatalogModel(t.Context(), h.e, "somexl_v3")
			if !ok {
				t.Fatal("no row was created")
			}
			main, ok := engineVaeCheckpoint(m)
			if !ok {
				t.Fatalf("the row declares no checkpoint: %+v", m.Files)
			}
			if main.VaeBundled != tc.verdict {
				t.Fatalf("vae_bundled = %q, want %q", main.VaeBundled, tc.verdict)
			}
			var vae string
			for _, f := range m.Files {
				if f.Flag == "--vae" {
					vae = f.S3Key
				}
			}
			if tc.attach && vae != engineFamilyVaes["sdxl"].S3Key {
				t.Fatalf("--vae = %q, want the family's own VAE attached by the same press", vae)
			}
			if !tc.attach && vae != "" {
				t.Fatalf("--vae = %q was attached to a checkpoint that carries one", vae)
			}
			// And the mark follows from the verdict, which is what refuses Enable.
			if got := engineVaeMissing(h.e.def.Provider, m); got {
				t.Errorf("the row is marked vae_missing after 揃える attached %q", vae)
			}
		})
	}
}

// engineTaskEnv is one RunTask's container overrides, by container and variable.
func engineTaskEnv(in ecs.RunTaskInput) map[string]map[string]string {
	env := map[string]map[string]string{}
	for _, c := range in.Overrides.ContainerOverrides {
		kv := map[string]string{}
		for _, pair := range c.Environment {
			kv[aws.ToString(pair.Name)] = aws.ToString(pair.Value)
		}
		env[aws.ToString(c.Name)] = kv
	}
	return env
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// 🔴 A part is not a model. Registering an encoder as its own row is what put parts in the
// registered list beside the checkpoints, where they can never be enabled and help nothing.
func TestRegisterRefusesAPartAndAKeyNobodyOwns(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.put("image/vae/qwen_image_vae.safetensors", 253_800_000)
	rec := h.call(t, h.a.postObjectRegister, "POST", "/api/admin/engines/image/objects/register",
		`{"key":"image/vae/qwen_image_vae.safetensors"}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "揃える") {
		t.Errorf("registering a VAE = %d %s, want a refusal naming the act that was meant",
			rec.Code, rec.Body.String())
	}
	// A key with nothing at it is a 404 and never a row: HeadObject is asked, not the listing.
	h.bucket.states["image/checkpoints/never-uploaded.safetensors"] = engineStorageMissing
	rec = h.call(t, h.a.postObjectRegister, "POST", "/api/admin/engines/image/objects/register",
		`{"key":"image/checkpoints/never-uploaded.safetensors"}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("registering an absent key = %d %s, want 404", rec.Code, rec.Body.String())
	}
	// 🔴 And a bucket that could not be ASKED is a 503, never a 404. Treating an inability to look
	// as absence is what would register a row pointing at nothing — the same rule HeadObject's
	// classification is built on.
	h.bucket.states["image/checkpoints/unaskable.safetensors"] = engineStorageUnknown
	rec = h.call(t, h.a.postObjectRegister, "POST", "/api/admin/engines/image/objects/register",
		`{"key":"image/checkpoints/unaskable.safetensors"}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("registering a key the bucket would not answer about = %d %s, want 503",
			rec.Code, rec.Body.String())
	}
	// And the route is not an existence oracle for somebody else's prefix.
	rec = h.call(t, h.a.postObjectRegister, "POST", "/api/admin/engines/image/objects/register",
		`{"key":"llm/checkpoints/x.gguf"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a key outside the role = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// An object a row declares is not an orphan, and the refusal says whose it is and what to press
// (ADR 0085 decision 5).
func TestDeleteObjectRefusesWhileARowDeclaresIt(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.put("image/vae/qwen_image_vae.safetensors", 253_800_000)
	h.put("image/checkpoints/orphan.safetensors", 42)
	h.row(t, store.EngineModel{ID: "krea2", BaseModel: "krea2", Kind: "checkpoint",
		Files: []store.EngineModelFile{{Flag: "--vae", S3Key: "image/vae/qwen_image_vae.safetensors"}}})

	rec := h.call(t, h.a.deleteObject, "DELETE", "/api/admin/engines/image/objects",
		`{"key":"image/vae/qwen_image_vae.safetensors"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleting a declared key = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Holder apiHolder `json:"holder"`
			Next   apiNext   `json:"next"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Holder.Kind != "row" || body.Error.Holder.ID != "krea2" || body.Error.Next.Act != "forget_row" {
		t.Errorf("the refusal carries %+v / %+v, want the row and the act that frees it",
			body.Error.Holder, body.Error.Next)
	}
	// The orphan goes, and what comes back is a task that was started — the CP has no
	// s3:DeleteObject and never gets one.
	rec = h.call(t, h.a.deleteObject, "DELETE", "/api/admin/engines/image/objects",
		`{"key":"image/checkpoints/orphan.safetensors"}`, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleting"`) {
		t.Fatalf("deleting the orphan = %d %s", rec.Code, rec.Body.String())
	}
	if len(h.ecs.run) != 1 {
		t.Fatalf("%d tasks started, want the one delete", len(h.ecs.run))
	}
}

// The id proposed from a key is the model's name, not the file's: two quantisations of one model
// propose one id, and the catalogue's own collision is what refuses the second.
func TestObjectIDFromKeyDropsTheQuantisationTag(t *testing.T) {
	for key, want := range map[string]string{
		"image/checkpoints/split_files/diffusion_models/anima_v1.safetensors": "anima_v1",
		"image/text_encoders/qwen3vl_4b_fp8_scaled.safetensors":               "qwen3vl_4b_fp8_scaled",
		"llm/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf":                         "qwen2.5-coder-0.5b-instruct",
		"image/checkpoints/Model-BF16.safetensors":                            "model",
	} {
		if got := engineObjectIDFromKey(key); got != want {
			t.Errorf("engineObjectIDFromKey(%q) = %q, want %q", key, got, want)
		}
	}
	// A proposal that collides is suffixed; an id the operator SUPPLIED is refused instead, because
	// answering "anima" with a row called "anima-2" is how the wrong model gets enabled.
	rows := []store.EngineModel{{ID: "anima_v1"}}
	got, aerr := engineObjectProposedID("", "image/checkpoints/anima_v1.safetensors", rows)
	if aerr != nil || got != "anima_v1-2" {
		t.Errorf("proposal = %q %v, want the suffixed id", got, aerr)
	}
	if _, aerr := engineObjectProposedID("anima_v1", "", rows); aerr == nil || aerr.status != http.StatusConflict {
		t.Errorf("a supplied id that collides = %v, want 409", aerr)
	}
}

// 🔴 Measured on af-sandbox (2026-09-15, build 0a89569e): 消す ran MODE=delete, the object really
// went — and the key came back in the ledger as `バイト列がありません` with no button on it,
// because a `done` job still named it. To the operator that reads as "消す did not work".
//
// No bytes and nobody pointing at them is not a ledger entry: a finished job is provenance ON an
// object (decision 6), not a record OF one. `missing` is kept for the case it was written for —
// a ROW pointing at nothing — because that is a fault with a button.
func TestLedgerDropsAKeyWithNeitherBytesNorADeclaration(t *testing.T) {
	l := engineLedgerJoin("image", nil,
		[]store.EngineModel{{Role: "image", ID: "krea2", Files: []store.EngineModelFile{
			{Flag: "--vae", S3Key: "image/vae/declared-but-purged.safetensors"},
		}}},
		[]store.EngineIngestJob{
			// The memory of a delete that worked: the job finished months ago, the object is gone.
			{ID: "j-done", S3Key: "image/checkpoints/deleted.safetensors", State: store.EngineIngestDone},
			// A failed attempt that never wrote anything is still a line: it has a dismiss button.
			{ID: "j-failed", S3Key: "image/checkpoints/never-landed.safetensors",
				State: store.EngineIngestFailed, Message: "sha256 mismatch"},
		})
	var keys []string
	for _, row := range l.objects {
		keys = append(keys, row.Key)
	}
	want := []string{"image/checkpoints/never-landed.safetensors", "image/vae/declared-but-purged.safetensors"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Fatalf("ledger = %v, want %v", keys, want)
	}
	if l.at("image/vae/declared-but-purged.safetensors").State != engineStorageMissing {
		t.Error("a row pointing at nothing lost its missing mark, which is the fault it reports")
	}
}

// The other half of the same complaint: after 消す the list looked identical until the task
// finished, so the press had no visible outcome at all.
func TestDeleteObjectShowsTheDeletionInTheLedger(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.put("image/checkpoints/orphan.safetensors", 42)
	rec := h.call(t, h.a.deleteObject, "DELETE", "/api/admin/engines/image/objects",
		`{"key":"image/checkpoints/orphan.safetensors"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	led := h.call(t, h.a.getObjects, "GET", "/api/admin/engines/image/objects", "", nil)
	var ledger engineObjectsResponse
	if err := json.Unmarshal(led.Body.Bytes(), &ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Objects) != 1 || ledger.Objects[0].Job == nil ||
		ledger.Objects[0].Job.State != engineObjectDeleting {
		t.Fatalf("the ledger = %+v, want the object drawn as deleting", ledger.Objects)
	}
	// No id, because there is no job row to dismiss — the CP started an ECS task it does not own.
	if ledger.Objects[0].Job.ID != "" || ledger.Objects[0].Job.CreatedAt == "" {
		t.Errorf("the deletion = %+v, want no job id and the time it was pressed", ledger.Objects[0].Job)
	}
	// Pressing again is the same act, not a second task and not an error.
	again := h.call(t, h.a.deleteObject, "DELETE", "/api/admin/engines/image/objects",
		`{"key":"image/checkpoints/orphan.safetensors"}`, nil)
	if again.Code != http.StatusOK || len(h.ecs.run) != 1 {
		t.Fatalf("a second press = %d, %d tasks; want 200 and still one task", again.Code, len(h.ecs.run))
	}
	// And once the bytes are gone the line goes with them, rather than turning into `missing`.
	delete(h.bucket.states, "image/checkpoints/orphan.safetensors")
	led = h.call(t, h.a.getObjects, "GET", "/api/admin/engines/image/objects", "", nil)
	if err := json.Unmarshal(led.Body.Bytes(), &ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Objects) != 0 {
		t.Errorf("after the delete landed the ledger still holds %+v", ledger.Objects)
	}
}

// An object being deleted is not one to declare or move onto: the bytes are on their way out.
func TestLedgerWillNotOfferAnObjectThatIsBeingDeleted(t *testing.T) {
	l := engineLedgerJoin("image", []engineStorageObject{
		{Key: "image/vae/qwen_image_vae.safetensors", Bytes: 10},
	}, nil, nil)
	l.applyDeleting(map[string]time.Time{"image/vae/qwen_image_vae.safetensors": time.Now()})
	if l.present("image/vae/qwen_image_vae.safetensors") != nil {
		t.Error("a key with a deletion in flight was offered as usable")
	}
	if len(l.inDir("vae")) != 0 {
		t.Error("a key with a deletion in flight was offered as a candidate")
	}
}
