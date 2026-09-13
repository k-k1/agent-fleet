package store

import (
	"context"
	"path/filepath"
	"testing"
)

func engineModelStore(t *testing.T) *SQL {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// The catalogue's round trip, including the three JSON-text columns. `files` is the one that
// matters most: a split model is several S3 objects with a per-file FLAG, and losing the flag
// turns FLUX's four files into four paths the sidecar has no way to pass to the engine.
func TestEngineModelRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := engineModelStore(t)

	want := EngineModel{
		Role: "image", ID: "flux2-klein-4b", Kind: "checkpoint",
		Files: []EngineModelFile{
			{Flag: "--diffusion-model", S3Key: "image/diffusion_models/klein.safetensors"},
			{Flag: "--t5xxl", S3Key: "image/text_encoders/qwen_3_4b.safetensors"},
		},
		Args: []string{"--type", "q8_0"}, Sizes: []string{"1024x1024", "1216x832"},
		Description: "FLUX.2 klein 4B", VramMiB: 11600,
		License: "apache-2.0", LicenseName: "", LicenseURL: "https://example.invalid/license",
		Precision: "bf16", BaseModel: "flux2-klein",
		// The licence acceptance as a whole tuple (ADR 0072 open question 11). The tenant is
		// what says on whose behalf it was accepted, and the licence beside it is the wording
		// that was accepted rather than the model's current one.
		LicenseAcceptedBy: "u1", LicenseAcceptedAt: "2026-09-10T00:00:00Z",
		LicenseAcceptedTenant: "t-acme", LicenseAcceptedLicense: "apache-2.0",
	}
	if err := st.PutEngineModel(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := st.ListEngineModels(ctx, "image")
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v %+v", err, got)
	}
	g := got[0]
	if len(g.Files) != 2 || g.Files[0].Flag != "--diffusion-model" ||
		g.Files[1].S3Key != "image/text_encoders/qwen_3_4b.safetensors" {
		t.Fatalf("files did not survive: %+v", g.Files)
	}
	if len(g.Args) != 2 || len(g.Sizes) != 2 || g.VramMiB != 11600 || g.BaseModel != "flux2-klein" {
		t.Fatalf("scalars did not survive: %+v", g)
	}
	if g.LicenseAcceptedTenant != "t-acme" || g.LicenseAcceptedLicense != "apache-2.0" ||
		g.LicenseAcceptedBy != "u1" {
		t.Fatalf("the licence acceptance did not survive: %+v", g)
	}
	if g.CreatedAt == "" || g.UpdatedAt == "" {
		t.Fatalf("timestamps unset: %+v", g)
	}
	if g.Enabled || g.Selected || g.Default {
		t.Fatalf("an ingested row must arrive disabled: %+v", g)
	}

	// Listing another role must not see it, and "" must.
	if rows, err := st.ListEngineModels(ctx, "llm"); err != nil || len(rows) != 0 {
		t.Fatalf("llm role: %v %+v", err, rows)
	}
	if rows, err := st.ListEngineModels(ctx, ""); err != nil || len(rows) != 1 {
		t.Fatalf("every role: %v %+v", err, rows)
	}
}

// Selecting is EXCLUSIVE within a role, and it is the invariant the image engine's command
// line depends on: sd-server holds one checkpoint, so two selected rows have no answer to
// "what does it start with".
func TestEngineModelSelectionIsExclusive(t *testing.T) {
	ctx := context.Background()
	st := engineModelStore(t)
	for _, id := range []string{"sdxl-base-1.0", "sdxl-fine-tune", "some-lora"} {
		if err := st.PutEngineModel(ctx, EngineModel{Role: "image", ID: id, Kind: "checkpoint"}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	if ok, err := st.SetEngineModelSelected(ctx, "image", "sdxl-base-1.0"); err != nil || !ok {
		t.Fatalf("select first: %v %v", err, ok)
	}
	if ok, err := st.SetEngineModelSelected(ctx, "image", "sdxl-fine-tune"); err != nil || !ok {
		t.Fatalf("select second: %v %v", err, ok)
	}
	rows, err := st.ListEngineModels(ctx, "image")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var selected []string
	for _, r := range rows {
		if r.Selected {
			selected = append(selected, r.ID)
		}
	}
	if len(selected) != 1 || selected[0] != "sdxl-fine-tune" {
		t.Fatalf("selection is not exclusive: %v", selected)
	}
	// Selecting also enables: an administrator who picked a checkpoint has said it should be
	// loaded, and a selected-but-disabled row would be a checkpoint the sidecar never syncs.
	for _, r := range rows {
		if r.ID == "sdxl-fine-tune" && !r.Enabled {
			t.Fatalf("selecting did not enable: %+v", r)
		}
	}

	// An id that is not there reports false rather than silently clearing the selection —
	// otherwise a typo in the admin panel leaves the role with no checkpoint at all.
	if ok, err := st.SetEngineModelSelected(ctx, "image", "nope"); err != nil || ok {
		t.Fatalf("unknown id: %v %v", err, ok)
	}
	rows, _ = st.ListEngineModels(ctx, "image")
	for _, r := range rows {
		if r.ID == "sdxl-fine-tune" && !r.Selected {
			t.Fatal("a failed selection cleared the previous one")
		}
	}
}

func TestEngineModelEnableAndDelete(t *testing.T) {
	ctx := context.Background()
	st := engineModelStore(t)
	if err := st.PutEngineModel(ctx, EngineModel{Role: "llm", ID: "qwen3", Kind: "gguf"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if ok, err := st.SetEngineModelEnabled(ctx, "llm", "qwen3", true); err != nil || !ok {
		t.Fatalf("enable: %v %v", err, ok)
	}
	rows, _ := st.ListEngineModels(ctx, "llm")
	if len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("not enabled: %+v", rows)
	}
	if ok, err := st.SetEngineModelEnabled(ctx, "llm", "nope", true); err != nil || ok {
		t.Fatalf("unknown id must report false: %v %v", err, ok)
	}
	if ok, err := st.DeleteEngineModel(ctx, "llm", "qwen3"); err != nil || !ok {
		t.Fatalf("delete: %v %v", err, ok)
	}
	if ok, _ := st.DeleteEngineModel(ctx, "llm", "qwen3"); ok {
		t.Fatal("deleting twice reported a row")
	}
}

// The generation defaults a row declares (ADR 0072 decision 4, widened). Three states have to
// survive the round trip and they are three different things: declared, never declared, and
// cleared back to "use the family's recipe".
func TestEngineModelParamsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := engineModelStore(t)

	want := EngineParams{Steps: 30, CFG: 4.5, Sampler: "dpmpp_2m", Scheduler: "karras", ClipSkip: 2}
	m := EngineModel{Role: "image", ID: "some-sdxl", Kind: "checkpoint", BaseModel: "sdxl", Params: &want}
	if err := st.PutEngineModel(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	got := engineModelByID(t, st, "image", "some-sdxl")
	if got.Params == nil {
		t.Fatal("params were not stored")
	}
	if *got.Params != want {
		t.Errorf("params = %+v, want %+v", *got.Params, want)
	}

	// 🔴 Absent, not a struct of zeros. The provider merges these over its family's recipe field
	// by field, and a zero-valued object reaching it would read as "0 steps" rather than "the
	// row says nothing".
	if err := st.PutEngineModel(ctx, EngineModel{Role: "image", ID: "plain", Kind: "checkpoint"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if p := engineModelByID(t, st, "image", "plain").Params; p != nil {
		t.Errorf("a row that declares nothing came back with %+v", p)
	}

	// The targeted update, which is how the panel edits one number without carrying the licence
	// acceptance and the source out and back in again.
	ok, err := st.SetEngineModelParams(ctx, "image", "some-sdxl", &EngineParams{Steps: 12})
	if err != nil || !ok {
		t.Fatalf("set: %v %v", ok, err)
	}
	if p := engineModelByID(t, st, "image", "some-sdxl").Params; p == nil || *p != (EngineParams{Steps: 12}) {
		t.Errorf("after set, params = %+v", p)
	}
	// And the way back: nil clears the declaration. Without this there is no route from "this
	// model runs at 12 steps" back to the family's own recipe.
	if ok, err := st.SetEngineModelParams(ctx, "image", "some-sdxl", nil); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
	}
	if p := engineModelByID(t, st, "image", "some-sdxl").Params; p != nil {
		t.Errorf("after clearing, params = %+v", p)
	}
	// A row that is not there reports false rather than pretending to have written something.
	if ok, err := st.SetEngineModelParams(ctx, "image", "no-such-row", &EngineParams{Steps: 4}); err != nil || ok {
		t.Errorf("set on a missing row = %v %v, want false", ok, err)
	}
}

// engineModelByID reads one row back out of the listing, which is the only way the catalogue is
// read (there is no get-by-id in the interface).
func engineModelByID(t *testing.T, st *SQL, role, id string) EngineModel {
	t.Helper()
	rows, err := st.ListEngineModels(context.Background(), role)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, m := range rows {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no row %s/%s", role, id)
	return EngineModel{}
}

// The window and the VRAM measurement, edited one column at a time (ADR 0079 live run). The
// window's two columns move TOGETHER because the catalogue only ever answers the cap alongside
// the window, so a setter that took one of them would be the API for a row nobody can explain.
func TestEngineModelWindowAndVramAreEditedInPlace(t *testing.T) {
	st := engineModelStore(t)
	ctx := context.Background()
	if err := st.PutEngineModel(ctx, EngineModel{
		Role: "llm", ID: "qwen3", Kind: "gguf", Enabled: true,
		ContextTokens: 262144, MaxOutputTokens: 8192, VramMiB: 40000,
		Source: "hf:vendor/qwen3", LicenseAcceptedBy: "u0",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if ok, err := st.SetEngineModelWindow(ctx, "llm", "qwen3", 16384, 2048); err != nil || !ok {
		t.Fatalf("set window: %v %v", ok, err)
	}
	got := engineModelByID(t, st, "llm", "qwen3")
	if got.ContextTokens != 16384 || got.MaxOutputTokens != 2048 {
		t.Errorf("window = %d/%d, want 16384/2048", got.ContextTokens, got.MaxOutputTokens)
	}
	// Nothing a licence acceptance recorded may ride along with a number being corrected — that
	// is the reason these are targeted UPDATEs and not a read-modify-write.
	if got.Source != "hf:vendor/qwen3" || got.LicenseAcceptedBy != "u0" || !got.Enabled {
		t.Errorf("the row's other fields moved: %+v", got)
	}

	if ok, err := st.SetEngineModelVram(ctx, "llm", "qwen3", 19000); err != nil || !ok {
		t.Fatalf("set vram: %v %v", ok, err)
	}
	if got = engineModelByID(t, st, "llm", "qwen3"); got.VramMiB != 19000 {
		t.Errorf("vram_mib = %d, want 19000", got.VramMiB)
	}
	// 0 withdraws the measurement rather than being refused as "undeclared": the way back to the
	// floor the files imply has to exist, or a number typed once is what that row claims for ever.
	if ok, err := st.SetEngineModelVram(ctx, "llm", "qwen3", 0); err != nil || !ok {
		t.Fatalf("withdraw: %v %v", ok, err)
	}
	if got = engineModelByID(t, st, "llm", "qwen3"); got.VramMiB != 0 {
		t.Errorf("vram_mib = %d, want it withdrawn", got.VramMiB)
	}

	// A row that is not there reports false rather than pretending to have written something.
	if ok, err := st.SetEngineModelWindow(ctx, "llm", "no-such-row", 4096, 1024); err != nil || ok {
		t.Errorf("window on a missing row = %v %v, want false", ok, err)
	}
	if ok, err := st.SetEngineModelVram(ctx, "llm", "no-such-row", 4096); err != nil || ok {
		t.Errorf("vram on a missing row = %v %v, want false", ok, err)
	}
}
