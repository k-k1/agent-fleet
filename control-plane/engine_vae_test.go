package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// vaeOversizedHeader is a file that DECLARES a header longer than the ceiling. Not a synthetic
// impossibility: it is also the shape a `.ckpt` and a truncated upload land in, and all three have
// to answer "nobody read it" rather than "no VAE in it".
func vaeOversizedHeader() []byte {
	out := make([]byte, 8, 64)
	binary.LittleEndian.PutUint64(out, uint64(safetensorsHeadMax)+1)
	return append(out, []byte(`{"first_stage_model.decoder.conv_in.weight":{}}`)...)
}

// The verdict read out of THIS deployment's bucket, which is the only road a row registered from
// bytes already here has — there is no upstream URL, and there may never have been one the CP can
// still reach (ADR 0085 P3 took the routes that re-read headers away).
func TestEngineVaeOfObjectReadsTheStoredFile(t *testing.T) {
	const key = "image/checkpoints/a.safetensors"
	bundled := safetensorsFixture(t, []string{
		"model.diffusion_model.input_blocks.0.0.weight",
		"first_stage_model.decoder.conv_in.weight",
	})
	none := safetensorsFixture(t, []string{"model.diffusion_model.input_blocks.0.0.weight"})
	for _, tc := range []struct {
		name string
		head []byte
		err  error
		want string
	}{
		{name: "bundled", head: bundled, want: engineVaeYes},
		{name: "none", head: none, want: engineVaeNo},
		// 🔴 The two that must NOT come out "no". A mark is a refusal to enable the row, and
		// neither a header past the ceiling nor a bucket that would not answer is evidence about
		// what the file contains.
		{name: "header past the ceiling", head: vaeOversizedHeader(), want: engineVaeUnknown},
		{name: "unreadable", err: errors.New("AccessDenied"), want: engineVaeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bucket := &fakeEngineStorageHead{head: map[string][]byte{key: tc.head}}
			if tc.err != nil {
				bucket.prefixErr = map[string]error{key: tc.err}
			}
			got := engineVaeOfObject(t.Context(), "comfy", "checkpoint", key, newEngineStorage("models", bucket))
			if got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
			if len(bucket.prefixCalls[key]) == 0 {
				t.Fatal("the bucket was never read")
			}
			if w := bucket.prefixCalls[key][0]; w != safetensorsHeadWindow {
				t.Errorf("the first window was %d, want the cheap one (%d)", w, safetensorsHeadWindow)
			}
		})
	}
}

// Everything the question does not apply to answers unknown without touching the bucket: a
// megabyte pulled per registered part is a cost, and a verdict about a part or a LoRA is a fact
// about the wrong file.
func TestEngineVaeOfObjectAsksOnlyWhereItMeansSomething(t *testing.T) {
	const key = "image/checkpoints/a.safetensors"
	head := map[string][]byte{key: safetensorsFixture(t, []string{"first_stage_model.decoder.conv_in.weight"})}
	for _, tc := range []struct{ name, provider, kind, key string }{
		{"llm role", "llamacpp", "gguf", key},
		{"lora", "comfy", engineModelKindLora, key},
		{"pickle checkpoint", "comfy", "checkpoint", "image/checkpoints/a.ckpt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bucket := &fakeEngineStorageHead{head: head}
			if got := engineVaeOfObject(t.Context(), tc.provider, tc.kind, tc.key,
				newEngineStorage("models", bucket)); got != engineVaeUnknown {
				t.Fatalf("verdict = %q, want unknown", got)
			}
			if len(bucket.prefixCalls) != 0 {
				t.Fatalf("the bucket was read anyway: %v", bucket.prefixCalls)
			}
		})
	}
	// And a deployment with no bucket at all, which must not panic on the way to unknown.
	if got := engineVaeOfObject(t.Context(), "comfy", "checkpoint", key, nil); got != engineVaeUnknown {
		t.Fatalf("verdict with no bucket = %q, want unknown", got)
	}
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

// 🔴 A row nobody has read must NOT be marked broken — every row registered from the bucket is in
// that state (ADR 0085 P3 took the header scan away), and a mark on all of them is a mark worth
// nothing. `vae_missing` is the one mark left: `vae_fix` and `vae_unread` went with the routes
// that acted on them.
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
	for _, gone := range []string{"vae_fix", "vae_unread"} {
		if _, ok := rows["novae"][gone]; ok {
			t.Errorf("the row still carries %s, which left with its route: %v", gone, rows["novae"])
		}
	}
	for _, id := range []string{"unread", "hasvae", "declared"} {
		if _, marked := rows[id]["vae_missing"]; marked {
			t.Errorf("%s carries a mark it has not earned: %v", id, rows[id])
		}
	}
}

// The hand-registration road (`POST …/models`) reads the same header out of the same bucket. It
// is the one a super_admin rebuilds a forgotten row with, and a row rebuilt WITHOUT the verdict is
// the same silent fault the register button had.
func TestPostModelReadsTheVaeVerdictOutOfTheBucket(t *testing.T) {
	h := newEngineLedgerHarness(t)
	const key = "image/checkpoints/hand_registered.safetensors"
	h.put(key, 6_900_000_000)
	h.header(key, safetensorsFixture(t, []string{"model.diffusion_model.input_blocks.0.0.weight"}))
	// A part in the same body: its header says nothing about the checkpoint that loads it, so it
	// is not read at all.
	h.put("image/vae/other.safetensors", 334_600_000)
	h.header("image/vae/other.safetensors", safetensorsFixture(t, []string{"first_stage_model.decoder.conv_in.weight"}))

	code, out := adminModel(t, h.a, "POST", "image", "", `{"id":"hand","kind":"checkpoint","base_model":"sdxl",
		"file_rows":[{"s3Key":"`+key+`"},{"flag":"--clip_l","s3Key":"image/vae/other.safetensors"}]}`)
	if code != http.StatusOK {
		t.Fatalf("register = %d (%v)", code, out)
	}
	m, ok := engineCatalogModel(t.Context(), h.e, "hand")
	if !ok {
		t.Fatal("no row was created")
	}
	byFlag := map[string]store.EngineModelFile{}
	for _, f := range m.Files {
		byFlag[f.Flag] = f
	}
	if byFlag[""].VaeBundled != engineVaeNo {
		t.Fatalf("the checkpoint's verdict = %q, want %q", byFlag[""].VaeBundled, engineVaeNo)
	}
	if byFlag["--clip_l"].VaeBundled != engineVaeUnknown {
		t.Errorf("a part was given a verdict about itself: %+v", byFlag["--clip_l"])
	}
	if len(h.bucket.prefixCalls["image/vae/other.safetensors"]) != 0 {
		t.Errorf("a part's header was read: %v", h.bucket.prefixCalls)
	}
	// And the round trip: a body that CARRIES the verdict is believed, and the bucket is not
	// re-read for it — which is what makes reading a row out of the panel and posting it back
	// restore the row rather than re-derive half of it.
	h2 := newEngineLedgerHarness(t)
	h2.put(key, 6_900_000_000)
	code, out = adminModel(t, h2.a, "POST", "image", "", `{"id":"hand","kind":"checkpoint","base_model":"sdxl",
		"file_rows":[{"s3Key":"`+key+`","vae_bundled":"yes"}]}`)
	if code != http.StatusOK {
		t.Fatalf("round trip = %d (%v)", code, out)
	}
	m, _ = engineCatalogModel(t.Context(), h2.e, "hand")
	if f, _ := engineVaeCheckpoint(m); f.VaeBundled != engineVaeYes {
		t.Fatalf("the posted verdict = %q, want it carried through", f.VaeBundled)
	}
	if len(h2.bucket.prefixCalls) != 0 {
		t.Errorf("the bucket was read for a fact the body already stated: %v", h2.bucket.prefixCalls)
	}
}

// 揃える says what it could not find out. An unread header is not a missing VAE and must not be
// marked as one (the rule the whole three-valued verdict exists for) — but `none` on a row nobody
// has read reads as "nothing is wrong", and the row it is silent about is the one that fails every
// request. So the fact rides in the answer rather than being dropped.
func TestCompleteSaysWhenTheVaeQuestionWasNeverAsked(t *testing.T) {
	h := newEngineLedgerHarness(t)
	const key = "image/checkpoints/seeded.safetensors"
	h.put(key, 6_900_000_000)
	h.row(t, store.EngineModel{ID: "seeded", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: key}}})

	answer := func(t *testing.T) engineCompleteAnswer {
		t.Helper()
		rec := h.call(t, h.a.completeModel, "POST", "/api/admin/engines/image/models/seeded/complete",
			`{"check":true}`, map[string]string{"id": "seeded"})
		if rec.Code != http.StatusOK {
			t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
		}
		var out engineCompleteAnswer
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := answer(t)
	if got.Action != engineCompleteNone {
		t.Fatalf("action = %q, want none — an unread header is not a gap", got.Action)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "seeded.safetensors") {
		t.Fatalf("warnings = %v, want one naming the file nobody read", got.Warnings)
	}
	// The control: the same row with the header READ says nothing. Without it, a warning emitted
	// unconditionally would look identical.
	h.row(t, store.EngineModel{ID: "seeded", Kind: "checkpoint", BaseModel: "sdxl",
		Files: []store.EngineModelFile{{S3Key: key, VaeBundled: engineVaeYes}}})
	if got = answer(t); len(got.Warnings) != 0 {
		t.Fatalf("warnings = %v on a row whose header was read", got.Warnings)
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
