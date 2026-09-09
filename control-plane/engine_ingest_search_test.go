package main

// Searching for something to take in (ADR 0072 decision 11).
//
// The fixtures are shaped from what the live APIs answered on 2026-09-09, including the two
// fields that made the narrow decode necessary: `cardData.extra_gated_prompt` and
// `gguf.chat_template`, each over a kilobyte on a real row.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// hfSearchStub answers /api/models the way Hugging Face does and records the query it was
// asked, so a test can assert the FILTER as well as the answer.
func hfSearchStub(t *testing.T, body string) (*httptest.Server, *url.Values) {
	t.Helper()
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })
	return srv, &got
}

const hfSearchBody = `[
  {"id":"Qwen/Qwen2.5-Coder-7B-Instruct-GGUF","downloads":256578,"likes":435,
   "gated":false,"lastModified":"2024-11-01T00:00:00.000Z",
   "cardData":{"license":"apache-2.0","extra_gated_prompt":"PROMPT-PADDING-PROMPT-PADDING"},
   "gguf":{"total":7615616512,"context_length":131072,
           "chat_template":"TEMPLATE-PADDING-TEMPLATE-PADDING"}},
  {"id":"black-forest-labs/FLUX.1-dev","downloads":790579,"likes":14538,
   "gated":"auto","lastModified":"2025-06-27T16:22:19.000Z",
   "cardData":{"license":"other","license_name":"flux-1-dev-non-commercial-license"}}
]`

// 🔴 The upstream row is much bigger than the hit. `expand[]=cardData` also delivers the whole
// gating prompt and `expand[]=gguf` the whole chat template — measured, each over a kilobyte —
// and this list is drawn 20 rows at a time.
func TestSearchCopiesOnlyTheFieldsThePanelDraws(t *testing.T) {
	_, q := hfSearchStub(t, hfSearchBody)
	hits, aerr := engineSearchHF(t.Context(), "qwen", "gguf")
	if aerr != nil {
		t.Fatalf("search: %v", aerr.message)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	b, _ := json.Marshal(hits)
	for _, leak := range []string{"PROMPT-PADDING", "TEMPLATE-PADDING", "extra_gated_prompt", "chat_template"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("%q reached the panel's answer: %s", leak, b)
		}
	}
	// What IS carried: enough to choose without resolving 20 repositories first.
	h := hits[0]
	if h.Ref != "Qwen/Qwen2.5-Coder-7B-Instruct-GGUF" || h.Downloads != 256578 ||
		h.License != "apache-2.0" || h.Bytes != 7615616512 || h.ContextLength != 131072 {
		t.Errorf("hit = %+v", h)
	}
	if h.Gated {
		t.Error("an ungated repository is reported as gated")
	}
	// `gated` is a bool on one row and the string "auto" on the next — the same field, two
	// types, which is why the verdict goes through engineHFGated rather than a typed field.
	if !hits[1].Gated {
		t.Error(`gated:"auto" was not read as gated — the refusal would arrive nine minutes into a task instead`)
	}
	if hits[1].LicenseName != "flux-1-dev-non-commercial-license" {
		t.Errorf("license_name = %q; `license:\"other\"` alone tells nobody the terms", hits[1].LicenseName)
	}
	if q.Get("expand[]") == "" || !strings.Contains(strings.Join((*q)["expand[]"], ","), "gated") {
		t.Errorf("no expand[]=gated in %v — without it a row carries neither gating nor licence", *q)
	}
}

// The filter is per ROLE, for the same reason the file picker has one: a repository this engine
// cannot load is a dead end that `resolve` refuses a moment later.
func TestSearchFiltersByWhatTheEngineCanLoad(t *testing.T) {
	_, q := hfSearchStub(t, `[]`)
	if _, aerr := engineSearchHF(t.Context(), "qwen", "gguf"); aerr != nil {
		t.Fatalf("gguf: %v", aerr.message)
	}
	if q.Get("filter") != "gguf" || q.Get("pipeline_tag") != "" {
		t.Errorf("llm search asked %v, want filter=gguf", *q)
	}
	if q.Get("sort") != "downloads" || q.Get("direction") != "-1" {
		t.Errorf("not ordered by downloads: %v", *q)
	}
	if _, aerr := engineSearchHF(t.Context(), "sdxl", "checkpoint"); aerr != nil {
		t.Fatalf("checkpoint: %v", aerr.message)
	}
	if q.Get("pipeline_tag") != "text-to-image" || q.Get("filter") != "" {
		t.Errorf("image search asked %v, want pipeline_tag=text-to-image", *q)
	}
	// The chat engine's search must not ask for `gguf` on the image role: the expansion exists
	// to fill the window field, which sd-server has no use for.
	if strings.Contains(strings.Join((*q)["expand[]"], ","), "gguf") {
		t.Errorf("the image search expanded gguf: %v", (*q)["expand[]"])
	}
}

// 🔴 A Civitai hit carries the VERSION id. The model id is what is in the page's URL, and an
// ingest given that number resolves the wrong thing or nothing at all.
func TestCivitaiSearchCarriesTheVersionIdNotTheModelId(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("types"); got != "Checkpoint" {
			t.Errorf("types = %q, want Checkpoint", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
		  {"id":133005,"name":"Juggernaut XL","type":"Checkpoint",
		   "stats":{"downloadCount":1632949,"thumbsUpCount":36215},
		   "allowCommercialUse":["Image"],
		   "modelVersions":[{"id":1759168,"name":"Ragnarok","baseModel":"SDXL 1.0",
		                     "publishedAt":"2025-05-07T21:02:16.940Z"}]},
		  {"id":9,"name":"No published version","type":"Checkpoint","modelVersions":[]}]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	defer func() { engineCivitaiBase = old }()

	hits, aerr := engineSearchCivitai(t.Context(), "juggernaut", "checkpoint")
	if aerr != nil {
		t.Fatalf("search: %v", aerr.message)
	}
	// The second item has nothing behind it: a model page with no published version is a dead
	// end, and this list exists to show none.
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (the version-less model must not be offered)", len(hits))
	}
	if hits[0].Ref != "1759168" {
		t.Errorf("ref = %q, want the version id 1759168 (133005 is the MODEL id)", hits[0].Ref)
	}
	if hits[0].BaseModel != "SDXL 1.0" || hits[0].Downloads != 1632949 {
		t.Errorf("hit = %+v", hits[0])
	}
	// Civitai is not asked for GGUFs at all: it hosts image models, and llama.cpp can load none
	// of them.
	if hits, aerr := engineSearchCivitai(t.Context(), "juggernaut", "gguf"); aerr != nil || len(hits) != 0 {
		t.Errorf("civitai for the llm role = %v %v, want nothing", hits, aerr)
	}
}

// Non-commercial is on screen BEFORE the acceptance, which is decision 10's rule. Civitai has
// no licence field in Hugging Face's sense, so the one thing it does say about terms is the
// empty `allowCommercialUse`.
func TestCivitaiSearchMarksNonCommercial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"name":"X","allowCommercialUse":[],
		  "modelVersions":[{"id":5,"baseModel":"SDXL 1.0"}]}]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	defer func() { engineCivitaiBase = old }()

	hits, aerr := engineSearchCivitai(t.Context(), "x", "checkpoint")
	if aerr != nil || len(hits) != 1 {
		t.Fatalf("search: %v %v", hits, aerr)
	}
	if hits[0].LicenseName != "non-commercial" {
		t.Errorf("license_name = %q, want the non-commercial mark", hits[0].LicenseName)
	}
}

// The route: an engine's search reaches the right upstream and starts nothing.
func TestSearchRouteAnswersHitsAndRefusesAnEmptyQuery(t *testing.T) {
	a, _, _ := engineModelAdminAPI(t)
	hfSearchStub(t, hfSearchBody)

	call := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest/search", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.searchIngest(rec, r, store.Identity{ID: "u1"})
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := call(`{"q":"flux"}`)
	if code != http.StatusOK {
		t.Fatalf("search = %d %v", code, out)
	}
	hits, _ := out["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("hits = %v", out["hits"])
	}
	// A blank search would ask the upstream for its most-downloaded models, which is not a
	// search and is not what the button says.
	if code, out := call(`{"q":"   "}`); code != http.StatusBadRequest {
		t.Errorf("empty q = %d %v, want 400", code, out)
	}
	if code, out := call(`{"q":"flux","source":"elsewhere"}`); code != http.StatusBadRequest {
		t.Errorf("unknown source = %d %v, want 400", code, out)
	}
}
