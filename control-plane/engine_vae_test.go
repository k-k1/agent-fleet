package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// vaeTestHF stands in for Hugging Face: the metadata call the resolve makes, and the file itself
// answered as a safetensors header with or without the VAE tensors.
//
// The file is served whole rather than honouring the Range — which is also what a real server
// that ignores a Range does, and the path that has to work either way.
func vaeTestHF(t *testing.T, bundled bool) *httptest.Server {
	t.Helper()
	names := []string{"model.diffusion_model.input_blocks.0.0.weight"}
	if bundled {
		names = append(names, "first_stage_model.decoder.conv_in.weight")
	}
	body := safetensorsFixture(t, names)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/models/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"cardData":{"license":"mit"},"siblings":[{"rfilename":"m.safetensors",` +
				`"size":100,"lfs":{"sha256":"` + strings.Repeat("a", 64) + `"}}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/m.safetensors") {
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	restore := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = restore })
	return srv
}

func vaeTestRow(t *testing.T, st store.Store, id, verdict string, files ...store.EngineModelFile) {
	t.Helper()
	all := []store.EngineModelFile{{
		S3Key: "image/checkpoints/" + id + ".safetensors", Source: "hf:acme/" + id + "/m.safetensors",
		VaeBundled: verdict,
	}}
	all = append(all, files...)
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: id, Kind: "checkpoint", BaseModel: "sdxl", Files: all,
	}); err != nil {
		t.Fatal(err)
	}
}

func vaeComfyAPI(t *testing.T) (engineAdminAPI, *engineRuntimeState, store.Store) {
	t.Helper()
	st := testSettingsStore(t)
	e := newTestComfyEngine(t, "http://127.0.0.1:1", &engineTestECS{})
	e.settings, e.ctrl = st, nil
	e.catalog = newEngineCatalog(st, "image")
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{"image": e}}
	return engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}, e, st
}

func vaeModelRows(t *testing.T, a engineAdminAPI, e *engineRuntimeState) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	rows, _ := a.row(t.Context(), e)["model_rows"].([]map[string]any)
	for _, r := range rows {
		id, _ := r["id"].(string)
		out[id] = r
	}
	return out
}

// 🔴 The three states have to stay three. A row nobody has read must NOT be marked broken —
// every row taken in before this existed is in that state, and a mark on all of them is a mark
// worth nothing.
func TestEngineVaeMarksOnlyWhatWasRead(t *testing.T) {
	a, e, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "unread", engineVaeUnknown)
	vaeTestRow(t, st, "hasvae", engineVaeYes)
	vaeTestRow(t, st, "novae", engineVaeNo)
	vaeTestRow(t, st, "declared", engineVaeNo,
		store.EngineModelFile{Flag: "--vae", S3Key: "image/vae/sdxl_vae.safetensors"})
	e.catalog.invalidate()

	rows := vaeModelRows(t, a, e)
	if rows["novae"]["vae_missing"] != true {
		t.Errorf("a checkpoint read as carrying no VAE is not marked: %v", rows["novae"])
	}
	if rows["novae"]["vae_fix"] != "stabilityai/sdxl-vae/sdxl_vae.safetensors" {
		t.Errorf("the mark does not name the file that fixes it: %v", rows["novae"]["vae_fix"])
	}
	if _, marked := rows["unread"]["vae_missing"]; marked {
		t.Errorf("a row nobody has read was marked broken: %v", rows["unread"])
	}
	if rows["unread"]["vae_unread"] != true {
		t.Errorf("an unread row is not offered to the scan: %v", rows["unread"])
	}
	for _, id := range []string{"hasvae", "declared"} {
		if _, marked := rows[id][" vae_missing"]; marked {
			t.Errorf("%s was marked: %v", id, rows[id])
		}
		if rows[id]["vae_missing"] == true || rows[id]["vae_unread"] == true {
			t.Errorf("%s is neither missing nor unread, but carries a mark: %v", id, rows[id])
		}
	}
}

// Switching a known-broken row on is refused, and there is no confirm to repeat with: the
// failure is inside the engine on every request, after the box has paid the checkpoint switch.
func TestEngineVaeGuardRefusesEnable(t *testing.T) {
	a, e, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "novae", engineVaeNo)
	vaeTestRow(t, st, "unread", engineVaeUnknown)
	e.catalog.invalidate()

	code, out := adminModel(t, a, "PUT", "image", "novae", `{"enabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("enabling a checkpoint with no VAE = %d (%v), want 409", code, out)
	}
	msg, _ := out["error"].(map[string]any)
	if msg["code"] != errCodeEngineVaeMissing {
		t.Fatalf("code = %v, want %s", msg["code"], errCodeEngineVaeMissing)
	}
	if !strings.Contains(msg["message"].(string), "stabilityai/sdxl-vae") {
		t.Errorf("the refusal does not say what fixes it: %v", msg["message"])
	}
	// The positive control for that refusal: the SAME row shape with the header unread enables
	// normally. Without this, a guard that refused everything would look identical.
	if code, out = adminModel(t, a, "PUT", "image", "unread", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling a row nobody has read = %d (%v), want 200 — unknown is not a fault", code, out)
	}
}

func vaePost(t *testing.T, a engineAdminAPI, path, id, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/models"+path, strings.NewReader(body))
	r.SetPathValue("key", "image")
	r.SetPathValue("id", id)
	if id == "" {
		a.scanVae(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	} else {
		a.fixVae(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The scan is what turns "nobody asked" into a mark, without an operator deciding anything. It
// reads the file at its SOURCE — the CP has no S3 permission and no bucket name (review R3).
func TestEngineVaeScanReadsTheSource(t *testing.T) {
	vaeTestHF(t, false)
	a, e, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "novae", engineVaeUnknown)
	e.catalog.invalidate()

	code, out := vaePost(t, a, "/vae-scan", "", `{}`)
	if code != http.StatusOK {
		t.Fatalf("scan = %d (%v)", code, out)
	}
	read, _ := out["read"].([]any)
	if len(read) != 1 {
		t.Fatalf("read %v, want the one unread row", out["read"])
	}
	if got := read[0].(map[string]any)["vae_bundled"]; got != engineVaeNo {
		t.Fatalf("verdict = %v, want %q", got, engineVaeNo)
	}
	// Written onto the FILE, so the mark survives a restart and the panel does not read the
	// upstream again on every load.
	rows := vaeModelRows(t, a, e)
	if rows["novae"]["vae_missing"] != true {
		t.Fatalf("the verdict did not reach the row: %v", rows["novae"])
	}
	// And a second scan has nothing left to do, which is what keeps a panel load from being an
	// upstream read.
	if _, out = vaePost(t, a, "/vae-scan", "", `{}`); len(out["read"].([]any)) != 0 {
		t.Errorf("the second scan read %v again", out["read"])
	}
}

// A checkpoint that DOES bundle one clears the question rather than earning a mark — the same
// call, the same row shape, the opposite file.
func TestEngineVaeScanClearsAGoodCheckpoint(t *testing.T) {
	vaeTestHF(t, true)
	a, e, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "hasvae", engineVaeUnknown)
	e.catalog.invalidate()

	if code, out := vaePost(t, a, "/vae-scan", "", `{}`); code != http.StatusOK {
		t.Fatalf("scan = %d (%v)", code, out)
	}
	rows := vaeModelRows(t, a, e)
	if rows["hasvae"]["vae_missing"] == true || rows["hasvae"]["vae_unread"] == true {
		t.Fatalf("a checkpoint that bundles its VAE still carries a mark: %v", rows["hasvae"])
	}
}

// The one press. When this deployment already holds the family's VAE, the fix is a declaration
// and nothing is downloaded — no task, no licence to accept, no minutes.
func TestEngineVaeFixAttachesWhatIsAlreadyHere(t *testing.T) {
	vaeTestHF(t, false)
	a, e, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "novae", engineVaeNo)
	// Another row already declares the family VAE, which is how the bytes got into the bucket.
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "other", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{
			{S3Key: "image/checkpoints/other.safetensors"},
			{Flag: "--vae", S3Key: "image/vae/sdxl_vae.safetensors", Bytes: 335, Source: "hf:stabilityai/sdxl-vae/sdxl_vae.safetensors"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	// The plan first, which is what the panel shows before the press.
	code, out := vaePost(t, a, "/vae", "novae", `{"check":true}`)
	if code != http.StatusOK || out["action"] != "attach" || out["staged"] != true {
		t.Fatalf("check = %d %v, want an attach of the file already here", code, out)
	}
	if code, out = vaePost(t, a, "/vae", "novae", `{}`); code != http.StatusOK {
		t.Fatalf("fix = %d (%v)", code, out)
	}
	if out["action"] != "attached" {
		t.Fatalf("action = %v, want attached (nothing to download)", out["action"])
	}
	rows := vaeModelRows(t, a, e)
	if rows["novae"]["vae_missing"] == true {
		t.Fatalf("the row is still marked after the fix: %v", rows["novae"])
	}
	files, _ := rows["novae"]["file_rows"].([]map[string]any)
	found := false
	for _, f := range files {
		if f["flag"] == "--vae" && f["s3Key"] == "image/vae/sdxl_vae.safetensors" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the row does not declare the VAE: %v", files)
	}
	// And now it may be switched on, which is the whole point of the press.
	if code, out = adminModel(t, a, "PUT", "image", "novae", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enabling the fixed row = %d (%v), want 200", code, out)
	}
}

// A header that says the checkpoint is fine ends the question instead of buying a download.
func TestEngineVaeFixDoesNothingForAGoodCheckpoint(t *testing.T) {
	vaeTestHF(t, true)
	a, _, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "hasvae", engineVaeNo) // a stale "no" on the row
	code, out := vaePost(t, a, "/vae", "hasvae", `{}`)
	if code != http.StatusOK || out["action"] != "none" {
		t.Fatalf("fix on a checkpoint that has one = %d %v, want no action", code, out)
	}
	if out["vae_bundled"] != engineVaeYes {
		t.Fatalf("the verdict was not re-read: %v", out["vae_bundled"])
	}
}

// 🔴 An unreadable header buys nothing on its own. "Nobody could look" is not evidence of a
// fault, and a deployment that downloaded 335 MB on it would do so for every row whose source
// has gone away.
func TestEngineVaeFixRefusesOnAnUnreadableHeader(t *testing.T) {
	a, _, st := vaeComfyAPI(t)
	// No source at all: seeded, or registered by hand from a key in the bucket.
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "seeded", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/seeded.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	code, out := vaePost(t, a, "/vae", "seeded", `{}`)
	if code != http.StatusBadGateway {
		t.Fatalf("fix on an unreadable row = %d (%v), want a refusal", code, out)
	}
	msg, _ := out["error"].(map[string]any)
	if msg["code"] != errCodeEngineVaeUnreadable {
		t.Fatalf("code = %v, want %s", msg["code"], errCodeEngineVaeUnreadable)
	}
}

func TestEngineVaeSourceOf(t *testing.T) {
	src, ok := engineVaeSourceOf("hf:acme/model/file.safetensors")
	if !ok || src.HF == nil || src.HF.Repo != "acme/model" || src.HF.File != "file.safetensors" {
		t.Fatalf("hf source = %+v (%v)", src, ok)
	}
	src, ok = engineVaeSourceOf("civitai:362358")
	if !ok || src.Civitai == nil || src.Civitai.VersionID != 362358 {
		t.Fatalf("civitai source = %+v (%v)", src, ok)
	}
	if _, ok = engineVaeSourceOf(""); ok {
		t.Error("an empty source was accepted")
	}
	if _, ok = engineVaeSourceOf("hf:acme/model"); ok {
		t.Error("a two-segment hf source was accepted — there is no file in it to read")
	}
}

// vaeTestDown stands in for an upstream that will not answer — Civitai's 503 on af-sandbox.
func vaeTestDown(t *testing.T) *int {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	restore := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = restore })
	return &hits
}

// 🔴 The diagnosis and the remedy have DIFFERENT upstreams, and the one press must not be taken
// down by the one it does not need. Measured on af-sandbox (2026-09-13): the row's source is a
// Civitai version, Civitai answered 503, and the fix — which downloads `stabilityai/sdxl-vae`
// from Hugging Face — was refused because the header could not be read AGAIN.
func TestEngineVaeFixStandsOnWhatTheRowRecorded(t *testing.T) {
	vaeTestDown(t)
	a, e, st := vaeComfyAPI(t)
	// Marked by an earlier scan, when the source still answered.
	vaeTestRow(t, st, "novae", engineVaeNo)
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "other", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{
			{S3Key: "image/checkpoints/other.safetensors"},
			{Flag: "--vae", S3Key: "image/vae/sdxl_vae.safetensors", Bytes: 335},
		},
	}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()

	code, out := vaePost(t, a, "/vae", "novae", `{"check":true}`)
	if code != http.StatusOK {
		t.Fatalf("check with the source down = %d (%v), want the plan anyway", code, out)
	}
	// …and it says the re-read failed rather than pretending it happened.
	if out["recheck_failed"] == nil {
		t.Fatalf("the answer does not say the source could not be read again: %v", out)
	}
	if out["action"] != "attach" {
		t.Fatalf("action = %v, want the attach this deployment can do without that source", out["action"])
	}
	if code, out = vaePost(t, a, "/vae", "novae", `{}`); code != http.StatusOK || out["action"] != "attached" {
		t.Fatalf("fix with the source down = %d %v, want it to go through", code, out)
	}
	rows := vaeModelRows(t, a, e)
	if rows["novae"]["vae_missing"] == true {
		t.Fatalf("the row is still marked after the fix: %v", rows["novae"])
	}
}

// And the other half of that rule: with NOTHING recorded, an unreadable header still buys
// nothing. "Nobody could look" is not a fault, and a deployment that downloaded on it would do
// so for every row whose source has gone away.
func TestEngineVaeFixUnknownStillRefusesWhenTheSourceIsDown(t *testing.T) {
	vaeTestDown(t)
	a, e, st := vaeComfyAPI(t)
	vaeTestRow(t, st, "unread", engineVaeUnknown)
	e.catalog.invalidate()

	code, out := vaePost(t, a, "/vae", "unread", `{}`)
	if code != http.StatusBadGateway {
		t.Fatalf("fix on an unknown row with the source down = %d (%v), want a refusal", code, out)
	}
	// The escape exists and is named in the refusal, for the operator who has watched the row
	// fail in the engine.
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "force") {
		t.Errorf("the refusal does not name the way past it: %s", msg)
	}
	if code, out = vaePost(t, a, "/vae", "unread", `{"force":true,"licenseAccepted":true,"check":true}`); code != http.StatusOK {
		t.Fatalf("check with force = %d (%v), want the plan", code, out)
	}
}

// 🔴 A scan must not hammer an upstream that is refusing. This runs on a panel load, so "one
// request per row per reload" is how a rate limit becomes permanent.
func TestEngineVaeScanStopsOnARefusingUpstream(t *testing.T) {
	hits := vaeTestDown(t)
	a, e, st := vaeComfyAPI(t)
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		vaeTestRow(t, st, id, engineVaeUnknown)
	}
	e.catalog.invalidate()

	code, out := vaePost(t, a, "/vae-scan", "", `{}`)
	if code != http.StatusOK {
		t.Fatalf("scan = %d (%v)", code, out)
	}
	read, _ := out["read"].([]any)
	if len(read) != engineVaeScanFails {
		t.Fatalf("read %d rows against a refusing upstream, want it to stop after %d", len(read), engineVaeScanFails)
	}
	// The rest are reported as left rather than silently skipped: an operator who sees two marks
	// appear has to know whether that was all of them.
	if out["left"] != float64(2) {
		t.Fatalf("left = %v, want the two rows it did not reach", out["left"])
	}
	// And each attempt is ONE round trip to that host, not the three a full resolve makes.
	if *hits > engineVaeScanFails {
		t.Fatalf("%d requests to a host that had already refused, want at most %d", *hits, engineVaeScanFails)
	}
}
