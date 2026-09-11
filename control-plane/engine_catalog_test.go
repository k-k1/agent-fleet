package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func catalogStore(t *testing.T) *store.SQL {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// The active set is what the BOX reads, and SSM's Standard tier refuses a value over 4,096
// CHARACTERS — measured against the real API on 2026-09-08, where 4,200 came back
// `ValidationException`. ADR 0072 decision 2 pins the shape against a catalogue of 20 models
// and 20 LoRAs, which is what "a deployment that actually uses this" looks like.
//
// This is the test that keeps the document small. Every field added to engineActiveSet is
// multiplied by forty here, and the failure it prevents is silent: an over-long PutParameter
// is refused, the parameter keeps yesterday's value, and the engine goes on loading the old
// model with nothing in the panel to say why.
func TestEngineActiveSetFitsInSSMStandardTier(t *testing.T) {
	var rows []store.EngineModel
	for i := 0; i < 20; i++ {
		rows = append(rows, store.EngineModel{
			Role: "image", ID: fmt.Sprintf("sdxl-community-fine-tune-%02d", i), Kind: "checkpoint",
			Enabled: true, Selected: i == 0,
			Files: []store.EngineModelFile{
				{S3Key: fmt.Sprintf("image/checkpoints/sdxl_community_fine_tune_%02d.safetensors", i)},
			},
			// The parts that must NOT reach the box: they are the reason the document fits.
			Description: strings.Repeat("a long description an agent reads when it chooses. ", 4),
			License:     "creativeml-openrail-m", LicenseName: "openrail", VramMiB: 7379,
			Sizes: []string{"1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"},
		})
		rows = append(rows, store.EngineModel{
			Role: "image", ID: fmt.Sprintf("watercolour-style-lora-v%02d", i), Kind: "lora",
			Enabled: true, BaseModel: "sdxl",
			Files: []store.EngineModelFile{
				{S3Key: fmt.Sprintf("image/loras/watercolour_style_lora_v%02d.safetensors", i)},
			},
			Description: strings.Repeat("what this LoRA does to a picture. ", 4),
		})
	}
	value, err := engineActiveSetJSON(buildEngineActiveSet("image", rows))
	if err != nil {
		t.Fatalf("20 models and 20 LoRAs must fit: %v", err)
	}
	t.Logf("20 models + 20 LoRAs = %d characters of %d", len(value), engineActiveSetMaxChars)

	// And the limit is really enforced, rather than being a constant nobody compares against.
	var many []store.EngineModel
	for i := 0; i < 200; i++ {
		many = append(many, store.EngineModel{
			Role: "image", ID: fmt.Sprintf("model-%03d", i), Enabled: true,
			Files: []store.EngineModelFile{{S3Key: fmt.Sprintf("image/checkpoints/m%03d.safetensors", i)}},
		})
	}
	if _, err := engineActiveSetJSON(buildEngineActiveSet("image", many)); err == nil {
		t.Fatal("an oversized active set was accepted — SSM would refuse it and nothing would notice")
	}
}

// What the box is told, and what it is deliberately not told.
func TestBuildEngineActiveSet(t *testing.T) {
	rows := []store.EngineModel{
		{Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true,
			Files:       []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
			Description: "the one everybody has", License: "openrail++", VramMiB: 7379},
		{Role: "image", ID: "klein-4b", Kind: "checkpoint", Enabled: true, Selected: true,
			Files: []store.EngineModelFile{
				{S3Key: "image/diffusion_models/klein.safetensors"},
				{Flag: "--t5xxl", S3Key: "image/text_encoders/qwen_3_4b_fp8.safetensors"},
			},
			Args: []string{"--type", "q8_0"}},
		{Role: "image", ID: "off-one", Kind: "checkpoint", Enabled: false,
			Files: []store.EngineModelFile{{S3Key: "image/checkpoints/nope.safetensors"}}},
		{Role: "image", ID: "watercolour", Kind: "lora", Enabled: true,
			Files: []store.EngineModelFile{{S3Key: "image/loras/watercolour.safetensors"}}},
	}
	set := buildEngineActiveSet("image", rows)
	if set.Start != "klein-4b" {
		t.Errorf("start = %q, want the SELECTED checkpoint", set.Start)
	}
	if len(set.Models) != 2 {
		t.Fatalf("a disabled model reached the box: %+v", set.Models)
	}
	if len(set.Loras) != 1 || set.Loras[0] != "image/loras/watercolour.safetensors" {
		t.Errorf("loras = %+v", set.Loras)
	}
	// A LoRA must never be something the engine is started with, however the list is ordered.
	for _, m := range set.Models {
		if m.ID == "watercolour" {
			t.Error("a LoRA was listed as a model")
		}
	}
	// The split model keeps its flags, which is the whole reason the flag is stored rather
	// than a role name that would need a mapping table on the box.
	var klein engineActiveModel
	for _, m := range set.Models {
		if m.ID == "klein-4b" {
			klein = m
		}
	}
	if len(klein.Files) != 2 {
		t.Fatalf("split model files = %+v", klein.Files)
	}
	// The flagless part is a bare string (it goes to the engine's own -m) and the other keeps
	// its literal flag — the shorthand that pays for itself once per model in the deployment.
	if k, ok := klein.Files[0].(string); !ok || k != "image/diffusion_models/klein.safetensors" {
		t.Errorf("first file = %#v, want a bare key", klein.Files[0])
	}
	if f, ok := klein.Files[1].(engineActiveFile); !ok || f.Flag != "--t5xxl" {
		t.Errorf("second file = %#v, want the flagged form", klein.Files[1])
	}

	// Nothing a person reads goes to the box. The 4,096-character budget is the reason, and a
	// field that slipped in here would only be noticed once a real catalogue stopped fitting.
	raw, _ := json.Marshal(set)
	for _, leaked := range []string{"the one everybody has", "openrail++", "7379"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("the active set carries %q, which belongs in the panel: %s", leaked, raw)
		}
	}

	// Nothing selected: the first enabled model is started with, rather than nothing — an
	// engine with models and no start flag comes up as the placeholder, which reads exactly
	// like a broken deploy.
	plain := buildEngineActiveSet("llm", []store.EngineModel{
		{Role: "llm", ID: "a", Enabled: true, Files: []store.EngineModelFile{{S3Key: "llm/a.gguf"}}},
	})
	if plain.Start != "a" {
		t.Errorf("start = %q with nothing selected", plain.Start)
	}
}

// A database error must never read as "there are no models": that answer stops a running GPU
// and answers 503 to everybody using it (see the engineCatalog type comment).
func TestEngineCatalogHasModels(t *testing.T) {
	ctx := context.Background()
	st := catalogStore(t)

	if !newEngineCatalog(nil, "llm").hasModels(ctx) {
		t.Error("a CP with no store must not report an empty catalogue")
	}
	c := newEngineCatalog(st, "llm")
	if c.hasModels(ctx) {
		t.Error("an empty catalogue reported models")
	}
	// A LoRA on its own is not something an engine can be started with.
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "llm", ID: "l", Kind: "lora", Enabled: true,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	c.invalidate()
	if c.hasModels(ctx) {
		t.Error("a LoRA alone counted as something to serve")
	}
	if err := st.PutEngineModel(ctx, store.EngineModel{
		Role: "llm", ID: "m", Kind: "gguf", Enabled: true,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	c.invalidate()
	if !c.hasModels(ctx) {
		t.Error("an enabled model was not seen")
	}
}

func TestEngineActiveParamName(t *testing.T) {
	if got := engineActiveParamName("/af-ws/engines", "llm"); got != "/af-ws/engines/llm/active" {
		t.Errorf("got %q", got)
	}
	// A trailing slash in the stack's parameter must not produce a doubled one: SSM treats
	// //llm as a different name, so the box would read a parameter nobody writes.
	if got := engineActiveParamName("/af-ws/engines/", "image"); got != "/af-ws/engines/image/active" {
		t.Errorf("got %q", got)
	}
	// No base = an inline dev table. Nothing reads the parameter, so nothing is published.
	if got := engineActiveParamName("", "llm"); got != "" {
		t.Errorf("got %q", got)
	}
}

// The two documents the dev deployment's image role is driven with in ADR 0072's P0
// measurements, byte for byte.
//
// They are here because the live check could not go through the admin API: driving it needs a
// super_admin browser session, and the P0 measurement was made from AWS credentials alone. So
// the active set was published by hand — and this is what makes that a faithful stand-in for
// what the Control Plane's own publishActiveSet would have written, rather than a plausible
// hand-typed JSON that happens to work.
//
// The link this does NOT cover is the admin route writing the row, which is
// TestEngineAdminModelLifecycle's job.
func TestEngineActiveSetForTheDevDeployment(t *testing.T) {
	sdxl := store.EngineModel{
		Role: "image", ID: "sdxl-base-1.0", Kind: "checkpoint", Enabled: true, Selected: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/sd_xl_base_1.0.safetensors"}},
	}
	jugg := store.EngineModel{
		Role: "image", ID: "juggernaut-xl-v9", Kind: "checkpoint", Enabled: true,
		Files: []store.EngineModelFile{{S3Key: "image/checkpoints/juggernaut_xl_v9.safetensors"}},
	}
	got, err := engineActiveSetJSON(buildEngineActiveSet("image", []store.EngineModel{sdxl, jugg}))
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	want := `{"v":1,"key":"image","start":"sdxl-base-1.0","models":[` +
		`{"id":"sdxl-base-1.0","f":["image/checkpoints/sd_xl_base_1.0.safetensors"]},` +
		`{"id":"juggernaut-xl-v9","f":["image/checkpoints/juggernaut_xl_v9.safetensors"]}]}`
	if got != want {
		t.Errorf("before the switch:\n got %s\nwant %s", got, want)
	}

	// After the administrator presses "start with this" on the second checkpoint. The store
	// keeps `selected` exclusive within a role, so exactly one row carries it.
	sdxl.Selected, jugg.Selected = false, true
	got, err = engineActiveSetJSON(buildEngineActiveSet("image", []store.EngineModel{sdxl, jugg}))
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	want = `{"v":1,"key":"image","start":"juggernaut-xl-v9","models":[` +
		`{"id":"sdxl-base-1.0","f":["image/checkpoints/sd_xl_base_1.0.safetensors"]},` +
		`{"id":"juggernaut-xl-v9","f":["image/checkpoints/juggernaut_xl_v9.safetensors"]}]}`
	if got != want {
		t.Errorf("after the switch:\n got %s\nwant %s", got, want)
	}
}

// The llm role's active set once the second GGUF is in the bucket, pinned byte for byte.
//
// It is pinned for the same reason the image pair above is: a live verification run publishes
// this document to SSM by hand (driving the admin API needs a super_admin browser session,
// which AWS credentials are not), and a hand-written document that differs from what
// publishActiveSet writes would verify the wrong thing.
func TestEngineActiveSetForTheDevDeploymentLlm(t *testing.T) {
	qwen30b := store.EngineModel{
		Role: "llm", ID: "qwen3-coder-30b-a3b", Kind: "gguf", Enabled: true, Default: true,
		Files: []store.EngineModelFile{
			{S3Key: "llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf", Bytes: 18553648864},
		},
		ContextTokens: 32768, MaxOutputTokens: 4096,
	}
	coder15b := store.EngineModel{
		Role: "llm", ID: "qwen2.5-coder-1.5b", Kind: "gguf", Enabled: true,
		Files: []store.EngineModelFile{
			{S3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", Bytes: 1117320768},
		},
		ContextTokens: 32768, MaxOutputTokens: 4096,
	}
	got, err := engineActiveSetJSON(buildEngineActiveSet("llm", []store.EngineModel{qwen30b, coder15b}))
	if err != nil {
		t.Fatalf("active set: %v", err)
	}
	want := `{"v":1,"key":"llm","start":"qwen3-coder-30b-a3b","models":[` +
		`{"id":"qwen3-coder-30b-a3b","f":["llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"],"c":32768},` +
		`{"id":"qwen2.5-coder-1.5b","f":["llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf"],"c":32768}]}`
	if got != want {
		t.Errorf("the llm active set is\n got %s\nwant %s", got, want)
	}
	// The declared size is the panel's "sync +N s", and it stays OUT of the active set: the box
	// gets S3 keys and flags and nothing else, because the document has 4,096 characters to live
	// in (ADR 0072 decision 2).
	if strings.Contains(got, "1117320768") {
		t.Error("a file size reached the active set")
	}
	if s := engineSyncSecs(coder15b); s != 11 {
		t.Errorf("sync estimate for the 1.1 GB model = %d s, want 11", s)
	}
	if s := engineSyncSecs(qwen30b); s != 179 {
		t.Errorf("sync estimate for the 18.5 GB model = %d s, want 179", s)
	}
	// Undeclared sizes print nothing rather than "+0 s".
	if s := engineSyncSecs(store.EngineModel{Files: []store.EngineModelFile{{S3Key: "llm/x.gguf"}}}); s != 0 {
		t.Errorf("an undeclared size estimated %d s", s)
	}
}

// ADR 0072 decision 5, the llm half: a LoRA is PINNED to the model it names, and the box is
// told so per model rather than as a directory to scan. The image role is the opposite (adapters
// are chosen per request from a directory), which is why this is asserted on the shape and not
// just on the file list.
func TestBuildEngineActiveSetPinsLorasToTheirBase(t *testing.T) {
	base := store.EngineModel{
		Role: "llm", ID: "qwen3", Kind: "gguf", Enabled: true, Default: true,
		Files: []store.EngineModelFile{{S3Key: "llm/q.gguf"}}, ContextTokens: 4096,
	}
	other := store.EngineModel{
		Role: "llm", ID: "small", Kind: "gguf", Enabled: true,
		Files: []store.EngineModelFile{{S3Key: "llm/s.gguf"}},
	}
	plain := store.EngineModel{
		Role: "llm", ID: "house-style", Kind: "lora", Enabled: true, BaseModel: "qwen3",
		Files: []store.EngineModelFile{{S3Key: "llm/loras/house.gguf"}},
	}
	weighted := store.EngineModel{
		Role: "llm", ID: "terse", Kind: "lora", Enabled: true, BaseModel: "qwen3",
		Files: []store.EngineModelFile{{S3Key: "llm/loras/terse.gguf"}},
		Args:  []string{"--scale", "0.8"},
	}
	orphan := store.EngineModel{
		Role: "llm", ID: "for-a-model-nobody-has", Kind: "lora", Enabled: true, BaseModel: "gone",
		Files: []store.EngineModelFile{{S3Key: "llm/loras/orphan.gguf"}},
	}
	off := store.EngineModel{
		Role: "llm", ID: "parked", Kind: "lora", Enabled: false, BaseModel: "qwen3",
		Files: []store.EngineModelFile{{S3Key: "llm/loras/parked.gguf"}},
	}
	rows := []store.EngineModel{base, other, plain, weighted, orphan, off}
	set := buildEngineActiveSet("llm", rows)

	var got engineActiveModel
	for _, m := range set.Models {
		if m.ID == "qwen3" {
			got = m
		}
		if m.ID == "small" && len(m.Lo) > 0 {
			t.Errorf("an adapter was pinned to a model that did not name it: %+v", m.Lo)
		}
	}
	want := []string{"llm/loras/house.gguf:1", "llm/loras/terse.gguf:0.8"}
	if len(got.Lo) != len(want) {
		t.Fatalf("pinned = %+v, want %+v", got.Lo, want)
	}
	for i := range want {
		if got.Lo[i] != want[i] {
			t.Errorf("pinned[%d] = %q, want %q", i, got.Lo[i], want[i])
		}
	}
	// A disabled adapter is not pinned AND not staged: switching one off has to take it out of
	// both, or the panel says "off" while the engine is still started with it.
	for _, k := range set.Loras {
		if strings.Contains(k, "parked") {
			t.Error("a disabled LoRA was staged on the box")
		}
	}
	// The orphan still travels as a file — it is enabled, and an administrator enabling the base
	// afterwards should not wait for a download — but it is pinned to nothing.
	for _, m := range set.Models {
		for _, p := range m.Lo {
			if strings.Contains(p, "orphan") {
				t.Errorf("an adapter whose base is not in the catalogue was pinned to %q", m.ID)
			}
		}
	}

	// Positive control on the scale: nonsense and out-of-range are the default, never echoed
	// into the command line. `,` and `:` are llama.cpp's separators, so a value carrying one
	// would move the boundary between two adapters.
	for _, bad := range [][]string{
		{"--scale", "9"}, {"--scale", "-1"}, {"--scale", "x"}, {"--scale", "0.5,/etc/passwd:1"},
		{"--scale"}, {"--jinja", "true"},
	} {
		l := store.EngineModel{
			Role: "llm", ID: "l", Kind: "lora", Enabled: true, BaseModel: "qwen3", Args: bad,
			Files: []store.EngineModelFile{{S3Key: "llm/loras/l.gguf"}},
		}
		if got := engineLorasPinnedTo("qwen3", []store.EngineModel{base, l}); len(got) != 1 || got[0] != "llm/loras/l.gguf:1" {
			t.Errorf("args %v gave %v, want the default scale", bad, got)
		}
	}

	// And the key itself cannot carry a separator either, whatever wrote the row.
	if err := engineActiveKeyOK("llm/loras/a,b.gguf"); err == nil {
		t.Error("a key holding a comma was accepted; it would split into two adapter paths")
	}
	if err := engineActiveKeyOK("llm/loras/a:1.gguf"); err == nil {
		t.Error("a key holding a colon was accepted; it would read as FNAME:SCALE")
	}
	if err := engineActiveKeyOK("llm/loras/ok.gguf"); err != nil {
		t.Errorf("an ordinary key was refused: %v", err)
	}
}

// The panel has to say when an adapter is pinned to nothing. Enabled, staged on the box, and
// doing absolutely nothing looks identical to one that works.
func TestLoraBaseMissingIsVisible(t *testing.T) {
	base := store.EngineModel{Role: "llm", ID: "qwen3", Kind: "gguf", Enabled: true,
		Files: []store.EngineModelFile{{S3Key: "llm/q.gguf"}}}
	rows := []store.EngineModel{
		base,
		{Role: "llm", ID: "ok", Kind: "lora", Enabled: true, BaseModel: "qwen3"},
		{Role: "llm", ID: "orphan", Kind: "lora", Enabled: true, BaseModel: "gone"},
		{Role: "llm", ID: "nameless", Kind: "lora", Enabled: true},
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"ok", true}, {"orphan", false}, {"nameless", false}} {
		var lora store.EngineModel
		for _, m := range rows {
			if m.ID == tc.id {
				lora = m
			}
		}
		if got := engineLoraBasePresent(lora, rows); got != tc.want {
			t.Errorf("%s: base present = %v, want %v", tc.id, got, tc.want)
		}
	}
	// A base that exists but is SWITCHED OFF is the same answer: the preset section it would be
	// pinned into is not written at all.
	off := []store.EngineModel{
		{Role: "llm", ID: "qwen3", Kind: "gguf", Enabled: false},
		{Role: "llm", ID: "ok", Kind: "lora", Enabled: true, BaseModel: "qwen3"},
	}
	if engineLoraBasePresent(off[1], off) {
		t.Error("a disabled base counted as present")
	}
	// And a LoRA never counts as another LoRA's base.
	lora2lora := []store.EngineModel{
		{Role: "llm", ID: "a", Kind: "lora", Enabled: true},
		{Role: "llm", ID: "b", Kind: "lora", Enabled: true, BaseModel: "a"},
	}
	if engineLoraBasePresent(lora2lora[1], lora2lora) {
		t.Error("a LoRA was accepted as the base of another LoRA")
	}
}

// The comfy family vocabulary lives twice — here and in the Agent, which is the side that
// actually dispatches on it — because Go cannot share a constant across two modules. This is
// the check that keeps the copies honest: add a sixth family to the Agent and forget the CP,
// and the Console never offers it while ComfyUI happily supports it; drop one from the Agent
// and the CP keeps accepting rows that can no longer generate.
//
// Reading the Agent's SOURCE rather than importing it is the same trade
// TestSharedContractMachineryIsIdentical makes. ⚠️ A parse that finds NOTHING is Fatal, not an
// empty set that compares equal to an empty set: a rename of the constant type would otherwise
// turn this test green at the exact moment it stopped measuring anything.
func TestComfyFamiliesMatchTheAgent(t *testing.T) {
	const src = "../workspace/agent/internal/imagegen/comfy_workflows.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %s: %v — this test is the only thing pinning the two copies together", src, err)
	}
	re := regexp.MustCompile(`comfyFamily\s*=\s*"([a-z0-9-]+)"`)
	found := re.FindAllStringSubmatch(string(b), -1)
	if len(found) == 0 {
		t.Fatalf("no `comfyFamily = \"...\"` constants in %s — the declaration was renamed and this"+
			" check silently stopped measuring the vocabulary", src)
	}
	theirs := make([]string, 0, len(found))
	for _, m := range found {
		theirs = append(theirs, m[1])
	}
	assertSameVocabulary(t, "checkpoint families", engineComfyFamilies, theirs)

	// The FILE vocabulary rides the same wire to the same panel and drifts the same way. Parsed
	// from the Agent's own list rather than from resolveComfyFiles' switch: the switch maps each
	// flag onto a different struct field, so it cannot BE the list, and comfy_workflows_test.go
	// is what pins the list to the switch.
	flagRe := regexp.MustCompile(`(?s)comfyFileFlags\s*=\s*\[\]string\{(.*?)\}`)
	fm := flagRe.FindStringSubmatch(string(b))
	if fm == nil {
		t.Fatalf("no `comfyFileFlags = []string{...}` in %s — renamed, and this check stopped measuring it", src)
	}
	lit := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(fm[1], -1)
	if len(lit) == 0 {
		t.Fatalf("comfyFileFlags in %s parsed as empty", src)
	}
	theirFlags := make([]string, 0, len(lit))
	for _, m := range lit {
		theirFlags = append(theirFlags, m[1])
	}
	assertSameVocabulary(t, "file flags", engineComfyFileFlags, theirFlags)
}

// Which files each family NEEDS, which the CP now refuses to enable a row without (ADR 0072 P2
// 欠落 10) — and which lives in the Agent as one guard per template, not as a list.
//
// So this test builds the list out of those guards: every `f.X == ""` inside a comfyGraph*
// function is a file that family's template reads, and resolveComfyFiles' own switch is what
// says which flag fills `f.X`. Reading the guards rather than a list the Agent could keep in
// step by hand is the point — the guards ARE what refuses at generation, and a copy that
// tracked anything else would drift towards agreeing with itself.
//
// ⚠️ Every parse step is Fatal on an empty result. A rename here must fail loudly rather than
// turn this into an empty set comparing equal to an empty set.
func TestComfyRequiredFilesMatchTheAgent(t *testing.T) {
	const src = "../workspace/agent/internal/imagegen/comfy_workflows.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %s: %v — this test is the only thing pinning the two copies together", src, err)
	}
	text := string(b)

	// `case "--clip_l":` → `f.ClipL = name`, i.e. which field a flag fills.
	flagOf := map[string]string{}
	for _, m := range regexp.MustCompile(`case\s+"([^"]*)":\s*\n\s*f\.(\w+)\s*=\s*name`).FindAllStringSubmatch(text, -1) {
		flagOf[m[2]] = m[1]
	}
	if len(flagOf) == 0 {
		t.Fatalf("resolveComfyFiles' switch in %s parsed as empty — the flag-to-field map is gone", src)
	}

	// One template at a time: the family it names in its refusals, and the fields it refuses on.
	bodies := regexp.MustCompile(`(?s)func comfyGraph\w+\(f comfyFiles[^)]*\)[^{]*\{(.*?)\n\}`).FindAllStringSubmatch(text, -1)
	if len(bodies) == 0 {
		t.Fatalf("no comfyGraph* templates found in %s", src)
	}
	famRe := regexp.MustCompile(`errComfyMissingFile\("([a-z0-9.-]+)"`)
	needRe := regexp.MustCompile(`f\.(\w+)\s*==\s*""`)
	theirs := map[string][]string{}
	for _, body := range bodies {
		fam := famRe.FindStringSubmatch(body[1])
		if fam == nil {
			continue // a template with no required file at all would be legitimate
		}
		for _, n := range needRe.FindAllStringSubmatch(body[1], -1) {
			flag, ok := flagOf[n[1]]
			if !ok {
				t.Fatalf("%s requires f.%s, which resolveComfyFiles fills from no flag at all", fam[1], n[1])
			}
			theirs[fam[1]] = append(theirs[fam[1]], flag)
		}
	}
	if len(theirs) != len(engineComfyFamilies) {
		t.Fatalf("read requirements for %d families out of %d (%v) — the templates were restructured"+
			" and this check stopped measuring them", len(theirs), len(engineComfyFamilies), theirs)
	}
	for fam, want := range theirs {
		mine, ok := engineComfyRequiredFlags[fam]
		if !ok {
			t.Errorf("the CP declares no required files for %q, so it would enable a row that"+
				" cannot generate", fam)
			continue
		}
		assertSameVocabulary(t, "required files for "+fam, mine, want)
	}
	for fam := range engineComfyRequiredFlags {
		if _, ok := theirs[fam]; !ok {
			t.Errorf("the CP requires files for %q, which is not a family the Agent dispatches on", fam)
		}
	}
}

func assertSameVocabulary(t *testing.T, what string, mine, theirs []string) {
	t.Helper()
	a := append([]string(nil), mine...)
	b := append([]string(nil), theirs...)
	sort.Strings(a)
	sort.Strings(b)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("the %s have drifted apart:\n  control-plane: %q\n  agent:         %q\n"+
			" - the CP serves this list to the Console and validates rows against it; the Agent is"+
			" what actually reads it. Make the same change in both", what, a, b)
	}
}

// What the vocabulary is FOR: refusing a row that would be registered, enabled, offered in
// generate_image's model enum, and only then fail at generation.
func TestEngineBaseModelValidation(t *testing.T) {
	for _, c := range []struct {
		provider, baseModel string
		want                bool
		why                 string
	}{
		{"comfy", "sdxl", true, "a declared family"},
		{"comfy", "flux2-klein", true, "a declared family with a dash"},
		{"comfy", "", false, "no family at all — the seeded row's shape, and ComfyUI cannot use it"},
		{"comfy", "SDXL 1.0", false, "Civitai's display name, which is what the ingest path used to store"},
		{"comfy", "sdxl-turbo", false, "a plausible-looking id that names no template"},
		{"sdcpp", "", true, "sdcpp holds one checkpoint and never reads this"},
		{"sdcpp", "SDXL 1.0", true, "and so has no opinion about how it is spelled"},
		{"llamacpp", "anything", true, "nor has any other provider"},
	} {
		if got := engineBaseModelValid(c.provider, c.baseModel); got != c.want {
			t.Errorf("engineBaseModelValid(%q, %q) = %v, want %v — %s", c.provider, c.baseModel, got, c.want, c.why)
		}
	}
	if engineBaseModelsFor("sdcpp") != nil {
		t.Error("sdcpp was given a vocabulary; a panel would then offer a choice that changes nothing")
	}
	if len(engineBaseModelsFor("comfy")) == 0 {
		t.Error("comfy has no vocabulary — the Console has nothing to build a selector from")
	}
}
