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
