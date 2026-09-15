package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

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
