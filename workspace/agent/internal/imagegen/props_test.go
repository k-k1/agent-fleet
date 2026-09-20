package imagegen

// Reading a picture's properties back (ADR 0081 decision 3). The three sources are driven
// separately because they fail differently: a sidecar is this Agent's own record, a PNG chunk is
// somebody else's document parsed defensively, and "none" is the honest answer for the vendor
// routes rather than a record full of zeros.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pngWithText puts a text chunk into a real PNG, immediately after IHDR — which is where
// ComfyUI's own SaveImage puts it, and before the first IDAT, which is where the reader stops.
func pngWithText(t *testing.T, base []byte, keyword, text string) []byte {
	t.Helper()
	const sigLen = 8
	ihdrLen := int(binary.BigEndian.Uint32(base[sigLen : sigLen+4]))
	cut := sigLen + 4 + 4 + ihdrLen + 4 // signature + IHDR's length, type, data and CRC

	payload := append([]byte(keyword), 0)
	payload = append(payload, text...)
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(payload)))
	chunk.WriteString("tEXt")
	chunk.Write(payload)
	sum := crc32.NewIEEE()
	sum.Write([]byte("tEXt"))
	sum.Write(payload)
	_ = binary.Write(&chunk, binary.BigEndian, sum.Sum32())

	out := append([]byte{}, base[:cut]...)
	out = append(out, chunk.Bytes()...)
	return append(out, base[cut:]...)
}

// agentGraphJSON is the API graph this Agent really sends, not a hand-written approximation of
// one: if a template's node ids or input names move, this fixture moves with them and the reader
// has to keep up.
func agentGraphJSON(t *testing.T, p comfyParams) string {
	t.Helper()
	g, err := comfyBuildGraph(ComfyFamilySDXL, comfyFiles{Checkpoint: "sd_xl_base_1.0.safetensors"}, p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPropsReadsTheSidecarFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image-1-1.png")
	if err := os.WriteFile(path, tinyPNG(t, 2, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := int64(1234)
	if err := writeSidecar(path, ImageProps{Prompt: "a fox", Seed: &seed, Label: "poster run"}); err != nil {
		t.Fatal(err)
	}
	got := readImageProps(path)
	if got.Source != "sidecar" {
		t.Fatalf("source = %q, want sidecar", got.Source)
	}
	if got.Label != "poster run" {
		t.Errorf("label = %q — the label is the one thing only the sidecar holds", got.Label)
	}
	if got.Seed == nil || *got.Seed != seed {
		t.Errorf("seed = %v", got.Seed)
	}
}

func TestPropsReadsThePNGPromptChunkWhenThereIsNoSidecar(t *testing.T) {
	dir := t.TempDir()
	graph := agentGraphJSON(t, comfyParams{
		Prompt: "a fox in the snow", Negative: "blurry", Seed: 99,
		Width: 1024, Height: 1024, BatchSize: 2,
		Loras: []comfyLora{{Name: "lineart.safetensors", Weight: 0.8}},
	})
	path := filepath.Join(dir, "image-1-1.png")
	if err := os.WriteFile(path, pngWithText(t, tinyPNG(t, 2, 2), "prompt", graph), 0o600); err != nil {
		t.Fatal(err)
	}

	got := readImageProps(path)
	if got.Source != "png" {
		t.Fatalf("source = %q, want png (%+v)", got.Source, got)
	}
	if got.Prompt != "a fox in the snow" {
		t.Errorf("prompt = %q — it is the CLIPTextEncode wired to the sampler's positive input, not the first one in the graph", got.Prompt)
	}
	if got.Negative != "blurry" {
		t.Errorf("negative = %q", got.Negative)
	}
	if got.Model != "sd_xl_base_1.0.safetensors" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Family != "sdxl" {
		t.Errorf("family = %q, want it READ off SaveImage's af-<family> prefix", got.Family)
	}
	if got.Size != "1024x1024" {
		t.Errorf("size = %q", got.Size)
	}
	if got.Seed == nil || *got.Seed != 99 {
		t.Fatalf("seed = %v, want the graph's own 99", got.Seed)
	}
	if got.Params == nil || got.Params.Steps != 20 || got.Params.CFG != 7 ||
		got.Params.Sampler != "dpmpp_2m" || got.Params.Scheduler != "karras" {
		t.Errorf("params = %+v, want the sampler node's own values", got.Params)
	}
	if len(got.Loras) != 1 || got.Loras[0].Name != "lineart.safetensors" || got.Loras[0].Weight != 0.8 {
		t.Errorf("loras = %+v", got.Loras)
	}
}

// A batch PNG carries the graph's BASE seed. The picture's own is base + its index, and the
// index lives only in the file name.
func TestPropsAddsTheBatchIndexToTheBaseSeed(t *testing.T) {
	dir := t.TempDir()
	graph := agentGraphJSON(t, comfyParams{Prompt: "a fox", Seed: 500, Width: 512, Height: 512, BatchSize: 4})
	png := pngWithText(t, tinyPNG(t, 2, 2), "prompt", graph)
	for i, want := range map[int]int64{1: 500, 3: 502} {
		name := filepath.Join(dir, "image-1-"+string(rune('0'+i))+".png")
		if err := os.WriteFile(name, png, 0o600); err != nil {
			t.Fatal(err)
		}
		got := readImageProps(name)
		if got.Seed == nil || *got.Seed != want {
			t.Errorf("%s: seed = %v, want %d", filepath.Base(name), got.Seed, want)
		}
	}
}

// A graph this Agent did not write has numeric node ids, so the class type is the only way in.
func TestPropsFallsBackToClassTypesForAForeignGraph(t *testing.T) {
	dir := t.TempDir()
	graph := `{
	  "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "someones.safetensors"}},
	  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "a cat", "clip": ["4", 1]}},
	  "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "ugly", "clip": ["4", 1]}},
	  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 768, "height": 768, "batch_size": 1}},
	  "3": {"class_type": "KSampler", "inputs": {"seed": 8, "steps": 25, "cfg": 8,
	        "sampler_name": "euler", "scheduler": "normal",
	        "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}}
	}`
	path := filepath.Join(dir, "someone-elses.png")
	if err := os.WriteFile(path, pngWithText(t, tinyPNG(t, 2, 2), "prompt", graph), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readImageProps(path)
	if got.Source != "png" || got.Model != "someones.safetensors" || got.Prompt != "a cat" || got.Negative != "ugly" {
		t.Fatalf("props = %+v", got)
	}
	if got.Family != "" {
		t.Errorf("family = %q, want empty: this graph's prefix is not one of the five, and guessing would be worse than saying nothing", got.Family)
	}
	if got.Size != "768x768" {
		t.Errorf("size = %q", got.Size)
	}
}

// The instruction-edit families (ADR 0094 decision 10) are the case neither half of the reader
// was written for: their text encode is a TextEncodeQwenImageEdit* whose prompt lives in `prompt`
// rather than `text`, and they build their latent from the input picture, so no Empty*LatentImage
// carries the size. Both topologies are driven — 2511 puts a
// FluxKontextMultiReferenceLatentMethod between each conditioning and the sampler, 2509 wires them
// straight — because the hop is what the reader has to walk through to reach the text at all.
func TestPropsReadsTheInstructionEditGraph(t *testing.T) {
	const instruction = `Change the text on the blue sign to "CLOSED".`
	files := comfyFiles{
		DiffusionModel: "qwen_image_edit_2511_fp8mixed.safetensors",
		ClipL:          "qwen_2.5_vl_7b_fp8_scaled.safetensors",
		Vae:            "qwen_image_vae.safetensors",
	}
	for _, f := range []comfyFamily{ComfyFamilyQwenImageEdit2509, ComfyFamilyQwenImageEdit2511} {
		t.Run(string(f), func(t *testing.T) {
			g, err := comfyBuildGraph(f, files, comfyParams{
				Op: OpEdit, Image: "af-input.png", Prompt: instruction, Negative: "extra text",
				Seed: 42, Width: 1024, Height: 1024, BatchSize: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := json.Marshal(g)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "image-1-1.png")
			// 1024x1024, because the size this family reports is the saved picture's own — the
			// graph does not carry one, and 2x2 would let a reader that invented a default pass.
			if err := os.WriteFile(path, pngWithText(t, tinyPNG(t, 1024, 1024), "prompt", string(graph)), 0o600); err != nil {
				t.Fatal(err)
			}

			got := readImageProps(path)
			if got.Source != "png" {
				t.Fatalf("source = %q, want png (%+v)", got.Source, got)
			}
			if got.Prompt != instruction {
				t.Errorf("prompt = %q, want the instruction — TextEncodeQwenImageEdit* spells its text input `prompt`, not `text`", got.Prompt)
			}
			if got.Negative != "extra text" {
				t.Errorf("negative = %q — the family puts the same encode class on the negative side", got.Negative)
			}
			if got.Size != "1024x1024" {
				t.Errorf("size = %q, want the saved picture's own: this family has no Empty*LatentImage to read", got.Size)
			}
			if got.Family != string(f) || got.Op != string(OpEdit) || got.Model != files.DiffusionModel {
				t.Errorf("family/op/model = %q/%q/%q", got.Family, got.Op, got.Model)
			}
		})
	}
}

// A vendor-route picture has neither, and the answer says so rather than showing blanks that
// read as "the seed was 0".
func TestPropsAnswersNoneForAPictureWithNeither(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vendor.png")
	if err := os.WriteFile(path, tinyPNG(t, 2, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readImageProps(path)
	if got.Source != "none" {
		t.Fatalf("source = %q, want none", got.Source)
	}
	if got.Seed != nil || got.Prompt != "" {
		t.Errorf("props = %+v, want nothing invented", got)
	}
}

// The route resolves its path through the Agent's own gate, and re-opening a picture costs a 304.
func TestPropsRouteUsesTheBrowseGateAndCachesByMtime(t *testing.T) {
	root := t.TempDir()
	old := BrowsePath
	BrowsePath = func(p string) (string, string, bool) {
		if strings.Contains(p, "..") {
			return "", "", false
		}
		return filepath.Join(root, p), p, true
	}
	t.Cleanup(func() { BrowsePath = old })

	path := filepath.Join(root, "pic.png")
	if err := os.WriteFile(path, tinyPNG(t, 2, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	seed := int64(5)
	if err := writeSidecar(path, ImageProps{Prompt: "a fox", Seed: &seed}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	HandleProps(rec, httptest.NewRequest(http.MethodGet, "/imagegen/props?path=pic.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	mod := rec.Header().Get("Last-Modified")
	if mod == "" {
		t.Fatal("no Last-Modified: the lightbox would re-fetch the whole record every time it opens")
	}
	var got ImageProps
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Source != "sidecar" {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}

	again := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/imagegen/props?path=pic.png", nil)
	req.Header.Set("If-Modified-Since", mod)
	HandleProps(again, req)
	if again.Code != http.StatusNotModified {
		t.Fatalf("a re-read answered %d, want 304", again.Code)
	}

	denied := httptest.NewRecorder()
	HandleProps(denied, httptest.NewRequest(http.MethodGet, "/imagegen/props?path=../escape.png", nil))
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("a path outside the browse root answered %d, want 400", denied.Code)
	}
}

// The Console's own folder is the product, not a by-product; its trial subfolder is the one
// exception, and a picture's sidecar goes with the picture.
func TestSweepKeepsTheConsoleFolderAndClearsTrialsOnly(t *testing.T) {
	root := t.TempDir()
	write := func(dir, name string, age time.Duration) string {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, tinyPNG(t, 2, 2), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeSidecar(path, ImageProps{Prompt: "x"}); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		for _, p := range []string{path, sidecarPathFor(path)} {
			if err := os.Chtimes(p, when, when); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	session := write(filepath.Join(root, "sid-1"), "image-1-1.png", 40*24*time.Hour)
	keeper := write(filepath.Join(root, consoleDirName), "image-2-1.png", 400*24*time.Hour)
	oldTrial := write(filepath.Join(root, consoleDirName, trialDirName), "image-3-1.png", 8*24*time.Hour)
	freshTrial := write(filepath.Join(root, consoleDirName, trialDirName), "image-4-1.png", time.Hour)

	now := time.Now()
	sweepGeneratedNow(root, now.Add(-generatedTTL), now.Add(-trialTTL))

	if _, err := os.Stat(session); !os.IsNotExist(err) {
		t.Error("an expired session picture survived")
	}
	if _, err := os.Stat(keeper); err != nil {
		t.Error("a keeper in the Console's folder was swept: a person pressed the button for each of these")
	}
	if _, err := os.Stat(oldTrial); !os.IsNotExist(err) {
		t.Error("an expired trial survived the 7-day window")
	}
	if _, err := os.Stat(sidecarPathFor(oldTrial)); !os.IsNotExist(err) {
		t.Error("the sidecar outlived its picture: a record of a picture that is gone is worse than neither")
	}
	if _, err := os.Stat(freshTrial); err != nil {
		t.Error("a fresh trial was swept")
	}
}
