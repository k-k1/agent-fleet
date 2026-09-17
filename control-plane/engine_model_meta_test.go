package main

// Names and example images on a catalogue row (ADR 0088).
//
// The fault these cover is not a crash either: every card's title was a row id derived from a
// file name, so a catalogue of twenty read as twenty keys. Two halves — the ingest recording what
// the page said, and the route that fills in a row taken in before the columns existed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// 🔴 Measured live 2026-09-18 against civitai.com: `/api/v1/model-versions/<id>` answers
// `model.name` ("MeinaMix"), the version's own `name` ("Meina V11") and ten `images[]` in the ONE
// document the resolve already decodes for the sha256 and the licence. So the name and the
// picture cost no upstream read of their own, and the fixture is shaped like that answer.
func TestResolveCivitaiCarriesTheNameAndTheExampleImage(t *testing.T) {
	civitaiStub(t, http.StatusOK)

	got, aerr := engineResolveCivitai(t.Context(), engineIngestCivitai{VersionID: 128713})
	if aerr != nil {
		t.Fatalf("resolve: %v", aerr.message)
	}
	if got.DisplayName != "DreamShaper" || got.VersionName != "v8" {
		t.Errorf("name = %q / %q, want the model's name and the version's own", got.DisplayName, got.VersionName)
	}
	// The card and the lightbox, in Civitai's own path vocabulary — the same rewrite the search
	// list makes, because the two draw the same picture and must not disagree about its size.
	if want := "https://image.civitai.com/xG1nkq/3e8b/anim=false,width=256/1.jpeg"; got.ThumbURL != want {
		t.Errorf("thumb_url = %q, want the card size %q", got.ThumbURL, want)
	}
	if want := "https://image.civitai.com/xG1nkq/3e8b/anim=false,width=1024/1.jpeg"; got.PreviewURL != want {
		t.Errorf("preview_url = %q, want the lightbox size %q", got.PreviewURL, want)
	}
}

// An example hosted anywhere but Civitai's own CDN is not drawn. The transform means nothing
// there, and a model page may point an example at any host at all.
func TestResolveCivitaiRefusesAnExampleFromAnotherOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"baseModel":"SD 1.5","name":"v1","model":{"name":"Elsewhere"},
		  "images":[{"url":"https://metadata.example.invalid/private.jpeg"}],
		  "files":[{"name":"m.safetensors","sizeKB":1,"type":"Model","downloadUrl":"x",
		   "hashes":{"SHA256":"879db523c30d3b9017143d56705015e15a2cb5628762c11d086fed9538abd7fd"}}]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	defer func() { engineCivitaiBase = old }()

	got, aerr := engineResolveCivitai(t.Context(), engineIngestCivitai{VersionID: 1})
	if aerr != nil {
		t.Fatalf("resolve: %v", aerr.message)
	}
	if got.PreviewURL != "" || got.ThumbURL != "" {
		t.Errorf("a non-Civitai example was exposed: %q / %q", got.PreviewURL, got.ThumbURL)
	}
	if got.DisplayName != "Elsewhere" {
		t.Errorf("display_name = %q — the name is a separate fact from the picture", got.DisplayName)
	}
}

// The inverse of what the ingest wrote, and of nothing else. A `url:` source and a row with none
// both answer false, for different reasons the refusal has to keep apart.
func TestEngineSourceForReadOnlyAnswersForAPageThatExists(t *testing.T) {
	for _, c := range []struct {
		source string
		want   engineIngestSource
		ok     bool
	}{
		{"civitai:5038", engineIngestSource{Civitai: &engineIngestCivitai{VersionID: 5038}}, true},
		{"hf:unsloth/Qwen3-GGUF/q4.gguf", engineIngestSource{HF: &engineIngestHF{Repo: "unsloth/Qwen3-GGUF", File: "q4.gguf"}}, true},
		{"url:https://example.invalid/x.safetensors", engineIngestSource{}, false},
		{"", engineIngestSource{}, false},
		{"civitai:not-a-number", engineIngestSource{}, false},
		{"hf:owner-only", engineIngestSource{}, false},
	} {
		got, ok := engineSourceForRead(c.source)
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.source, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if (got.Civitai == nil) != (c.want.Civitai == nil) || (got.HF == nil) != (c.want.HF == nil) {
			t.Errorf("%q: shape = %+v, want %+v", c.source, got, c.want)
			continue
		}
		if got.Civitai != nil && *got.Civitai != *c.want.Civitai {
			t.Errorf("%q: civitai = %+v, want %+v", c.source, *got.Civitai, *c.want.Civitai)
		}
		if got.HF != nil && *got.HF != *c.want.HF {
			t.Errorf("%q: hf = %+v, want %+v", c.source, *got.HF, *c.want.HF)
		}
	}
}

// The road back for a row taken in before the columns existed, which is every row a deployment
// already holds.
func TestRefreshModelMetaWritesTheNameOntoAnExistingRow(t *testing.T) {
	h := newEngineLedgerHarness(t)
	civitaiStub(t, http.StatusOK)
	h.row(t, store.EngineModel{ID: "dreamshaper_8_128713", Kind: "checkpoint", BaseModel: "sd15",
		Source: "civitai:128713",
		Files:  []store.EngineModelFile{{S3Key: "image/checkpoints/dreamshaper_8.safetensors"}}})

	rec := h.call(t, h.a.refreshModelMeta, "POST", "/api/admin/engines/image/models/dreamshaper_8_128713/meta",
		"", map[string]string{"id": "dreamshaper_8_128713"})
	if rec.Code != http.StatusOK {
		t.Fatalf("meta = %d: %s", rec.Code, rec.Body.String())
	}
	var answer map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer["display_name"] != "DreamShaper" || answer["found"] != true {
		t.Errorf("answer = %v, want the name it just read", answer)
	}
	// 🔴 On the ROW, not only in the answer. The panel re-reads the catalogue after the press, so
	// a route that answered correctly and stored nothing would look like it worked and lose the
	// name on the next load.
	rows, err := h.st.ListEngineModels(t.Context(), "image")
	if err != nil || len(rows) != 1 {
		t.Fatalf("catalogue = %v %v", rows, err)
	}
	if rows[0].DisplayName != "DreamShaper" || rows[0].VersionName != "v8" ||
		rows[0].ThumbURL != "https://image.civitai.com/xG1nkq/3e8b/anim=false,width=256/1.jpeg" {
		t.Errorf("row = %+v, want the name and the picture written down", rows[0])
	}
	// And the licence acceptance this row carries is untouched: the setter writes four columns,
	// never the row.
	if rows[0].BaseModel != "sd15" || rows[0].Files[0].S3Key != "image/checkpoints/dreamshaper_8.safetensors" {
		t.Errorf("row = %+v, want everything else exactly as it was", rows[0])
	}
}

// A row whose source is the weights themselves, or which has none, is refused with its own code —
// the panel says "there is no page here" rather than offering a button that can never work.
func TestRefreshModelMetaRefusesARowWithNoPage(t *testing.T) {
	h := newEngineLedgerHarness(t)
	h.row(t, store.EngineModel{ID: "seeded", Kind: "checkpoint"})
	h.row(t, store.EngineModel{ID: "by-url", Kind: "checkpoint", Source: "url:https://example.invalid/x.safetensors"})

	for _, id := range []string{"seeded", "by-url"} {
		rec := h.call(t, h.a.refreshModelMeta, "POST", "/api/admin/engines/image/models/"+id+"/meta",
			"", map[string]string{"id": id})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: meta = %d: %s", id, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); !jsonHasCode(got, errCodeEngineNoSource) {
			t.Errorf("%s: refusal = %s, want %s", id, got, errCodeEngineNoSource)
		}
	}
}

func jsonHasCode(body, code string) bool {
	var wrapper struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(body), &wrapper) == nil && wrapper.Error.Code == code
}
