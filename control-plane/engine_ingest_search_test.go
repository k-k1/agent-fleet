package main

// Searching for something to take in (ADR 0072 decision 11).
//
// The fixtures are shaped from what the live APIs answered on 2026-09-09, including the two
// fields that made the narrow decode necessary: `cardData.extra_gated_prompt` and
// `gguf.chat_template`, each over a kilobyte on a real row.

import (
	"bytes"
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
  {"id":"Qwen/Qwen2.5-Coder-7B-Instruct-GGUF","downloads":256578,"likes":435,"trendingScore":22,
   "gated":false,"lastModified":"2024-11-01T00:00:00.000Z",
   "cardData":{"license":"apache-2.0","extra_gated_prompt":"PROMPT-PADDING-PROMPT-PADDING"},
   "gguf":{"total":7615616512,"context_length":131072,
           "chat_template":"TEMPLATE-PADDING-TEMPLATE-PADDING"}},
  {"id":"black-forest-labs/FLUX.1-dev","downloads":790579,"likes":14538,
   "trendingScore":0.7000000000000001,
   "gated":"auto","lastModified":"2025-06-27T16:22:19.000Z",
   "cardData":{"license":"other","license_name":"flux-1-dev-non-commercial-license"}}
]`

// 🔴 The upstream row is much bigger than the hit. `expand[]=cardData` also delivers the whole
// gating prompt and `expand[]=gguf` the whole chat template — measured, each over a kilobyte —
// and this list is drawn 20 rows at a time.
func TestSearchCopiesOnlyTheFieldsThePanelDraws(t *testing.T) {
	_, q := hfSearchStub(t, hfSearchBody)
	hits, aerr := engineSearchHF(t.Context(), "qwen", "gguf", "")
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
	// All three ranking numbers ride on every row, whichever one the list was ordered by:
	// sorting by one and showing only that one leaves "why is this here" unanswerable.
	if h.Likes != 435 || h.Trending != 22 {
		t.Errorf("likes/trending = %d/%v, want 435/22", h.Likes, h.Trending)
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
	if _, aerr := engineSearchHF(t.Context(), "qwen", "gguf", ""); aerr != nil {
		t.Fatalf("gguf: %v", aerr.message)
	}
	if q.Get("filter") != "gguf" || q.Get("pipeline_tag") != "" {
		t.Errorf("llm search asked %v, want filter=gguf", *q)
	}
	if q.Get("sort") != "downloads" || q.Get("direction") != "-1" {
		t.Errorf("not ordered by downloads: %v", *q)
	}
	if _, aerr := engineSearchHF(t.Context(), "sdxl", "checkpoint", ""); aerr != nil {
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

	hits, aerr := engineSearchCivitai(t.Context(), "juggernaut", "checkpoint", "")
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
	if hits, aerr := engineSearchCivitai(t.Context(), "juggernaut", "gguf", ""); aerr != nil || len(hits) != 0 {
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

	hits, aerr := engineSearchCivitai(t.Context(), "x", "checkpoint", "")
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
		a.searchIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
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
	// A blank query is the RANKING, not a mistake: it is the only way in for somebody who does
	// not know what to type, and it was a 400 until decision 11 grew its second half.
	if code, out := call(`{"q":"   ","sort":"trending"}`); code != http.StatusOK {
		t.Errorf("ranking with no words = %d %v, want 200", code, out)
	}
	if code, out := call(`{"q":"flux","source":"elsewhere"}`); code != http.StatusBadRequest {
		t.Errorf("unknown source = %d %v, want 400", code, out)
	}
	// An unknown ranking is refused rather than passed on: Civitai answers 400 to one it does
	// not know (measured) and Hugging Face silently ignores it, which is worse — an unranked
	// list that looks ranked.
	if code, out := call(`{"q":"flux","sort":"newest"}`); code != http.StatusBadRequest {
		t.Errorf("unknown sort = %d %v, want 400", code, out)
	}
}

// The rankings are mapped per upstream, never passed through, and a query with no words in it
// is the point of them.
func TestSearchRanksWithoutAQuery(t *testing.T) {
	_, q := hfSearchStub(t, `[]`)
	for _, tc := range []struct{ sort, want string }{
		{"", "downloads"},
		{engineSortDownloads, "downloads"},
		{engineSortTrending, "trendingScore"},
		{engineSortLikes, "likes"},
	} {
		if _, aerr := engineSearchHF(t.Context(), "", "gguf", tc.sort); aerr != nil {
			t.Fatalf("%q: %v", tc.sort, aerr.message)
		}
		if q.Get("sort") != tc.want {
			t.Errorf("sort %q asked for %q, want %q", tc.sort, q.Get("sort"), tc.want)
		}
		// 🔴 No `search=` at all. An empty one is not the same as none on this API, and a
		// ranking is what the caller asked for.
		if _, ok := (*q)["search"]; ok {
			t.Errorf("a wordless ranking still sent search=%q", q.Get("search"))
		}
	}
	if _, aerr := engineSearchHF(t.Context(), "", "gguf", "newest"); aerr == nil {
		t.Error("an unknown ranking was passed to the upstream, which ignores it silently")
	}
}

// Civitai has no trending score, so trending is "most downloaded THIS MONTH" — measured to be
// a genuinely different list from the all-time one.
func TestCivitaiRankingUsesThePeriodForTrending(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	defer func() { engineCivitaiBase = old }()

	if _, aerr := engineSearchCivitai(t.Context(), "", "checkpoint", engineSortTrending); aerr != nil {
		t.Fatalf("trending: %v", aerr.message)
	}
	if got.Get("sort") != "Most Downloaded" || got.Get("period") != "Month" {
		t.Errorf("trending asked %v, want sort=Most Downloaded&period=Month", got)
	}
	if _, aerr := engineSearchCivitai(t.Context(), "", "checkpoint", engineSortLikes); aerr != nil {
		t.Fatalf("likes: %v", aerr.message)
	}
	if got.Get("sort") != "Highest Rated" || got.Get("period") != "" {
		t.Errorf("likes asked %v, want sort=Highest Rated with no period", got)
	}
	// 🔴 Not passed through: this API answers 400 to a sort it does not know (measured).
	if _, aerr := engineSearchCivitai(t.Context(), "", "checkpoint", "newest"); aerr == nil {
		t.Error("an unknown ranking reached Civitai, which answers 400")
	}
}

// 🔴 Looking at what exists must not depend on having an engine, or on its being switched on.
//
// The WIRING half of this is pinned by `testdata/routes.golden`, which is taken with no engine
// table: these routes appear in it only because they are registered outside
// registerEngineRoutes' `if reg == nil` guard. Re-nesting them puts the golden back to 440
// routes, which is what made the panel answer 404 on a deployment without 60-engines.
//
// Two different deployments hit this: one that has not adopted 60-engines at all (the panel is
// empty, and "there is nothing here" is the worst answer to "what could I run?"), and one that
// switched its GPU off to stop paying for it — a decision about this month's bill, not about
// whether an administrator may look at the catalogue.
func TestBrowsingNeedsNoEngineAndSurvivesOneBeingOff(t *testing.T) {
	a, e, _ := engineModelAdminAPI(t)
	hfSearchStub(t, hfSearchBody)

	post := func(path string, h func(http.ResponseWriter, *http.Request, engineIngestGrant),
		key, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		if key != "" {
			r.SetPathValue("key", key)
		}
		h(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// (1) An engine that is OFF still answers. The mode is not consulted anywhere on this path,
	// and this is what pins that: switching the engine off must not take the picker with it.
	if err := a.settings.SetSetting(t.Context(), engineSettingsFor(e.def.Key).mode, engineModeOff); err != nil {
		t.Fatalf("off: %v", err)
	}
	if got := e.mode(t.Context()); got != engineModeOff {
		t.Fatalf("the fixture engine is %q, not off — the case below would prove nothing", got)
	}
	code, out := post("/api/admin/engines/image/ingest/search", a.searchIngest, "image", `{"q":"flux"}`)
	if code != http.StatusOK || len(out["hits"].([]any)) != 2 {
		t.Errorf("search on a switched-off engine = %d %v", code, out)
	}

	// (2) No engine in the path at all, which is the deployment with no engine table.
	code, out = post("/api/admin/engines/search?kind=gguf", a.browseSearch, "", `{"q":"qwen"}`)
	if code != http.StatusOK || len(out["hits"].([]any)) != 2 {
		t.Fatalf("browse = %d %v", code, out)
	}
	// The kind is asked for rather than guessed: quietly answering GGUFs to somebody who asked
	// for checkpoints is a list that looks like an answer.
	if code, out := post("/api/admin/engines/search?kind=lora", a.browseSearch, "", `{"q":"x"}`); code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d %v, want 400", code, out)
	}
}

// The kind reaches the upstream filter on the keyless route too — otherwise "image" would
// browse GGUF repositories and look like it worked.
func TestBrowseKindPicksTheUpstreamFilter(t *testing.T) {
	a, _, _ := engineModelAdminAPI(t)
	_, q := hfSearchStub(t, `[]`)
	for _, tc := range []struct{ kind, filter, pipeline string }{
		{"gguf", "gguf", ""},
		{"checkpoint", "", "text-to-image"},
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/admin/engines/search?kind="+tc.kind, strings.NewReader(`{}`))
		a.browseSearch(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.kind, rec.Code, rec.Body.String())
		}
		if q.Get("filter") != tc.filter || q.Get("pipeline_tag") != tc.pipeline {
			t.Errorf("kind %q asked %v, want filter=%q pipeline_tag=%q", tc.kind, *q, tc.filter, tc.pipeline)
		}
	}
}

// 🔴 `trendingScore` is a SCORE and it comes back fractional. Measured 2026-09-10 on a live
// search for "WAI": two rows of twenty answered 0.1 and 0.7000000000000001, an int64 field made
// encoding/json refuse the WHOLE array, and the panel said "unreadable answer from
// huggingface.co" with no results — for a search that had worked the day before, because the
// popular GGUF rows the earlier probes hit all happened to score whole numbers.
func TestSearchSurvivesAFractionalTrendingScore(t *testing.T) {
	hfSearchStub(t, `[
	  {"id":"John6666/wai-nsfw-illustrious-sdxl-v150-sdxl","downloads":2862,"likes":23,
	   "trendingScore":0.1,"gated":false,"cardData":{"license":"other","license_name":"faipl-1.0-sd"}},
	  {"id":"John6666/wai-nsfw-illustrious-v80-sdxl","downloads":1884,"likes":57,
	   "trendingScore":0.7000000000000001,"gated":false,"cardData":{"license":"other"}},
	  {"id":"martineux/waiIllustriousSDXL_v160","downloads":3134,"likes":5,"trendingScore":1,
	   "gated":false,"cardData":null}]`)

	hits, aerr := engineSearchHF(t.Context(), "WAI", "checkpoint", engineSortDownloads)
	if aerr != nil {
		t.Fatalf("a fractional score lost the whole page: %s", aerr.message)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(hits))
	}
	// Compared through float64 so this test still COMPILES if the field is put back to an
	// integer — then it fails on the line above with the message the panel showed, which is the
	// failure worth keeping, rather than refusing to build.
	if float64(hits[0].Trending) != 0.1 || float64(hits[1].Trending) != 0.7000000000000001 {
		t.Errorf("scores = %v / %v, want them carried as they came", hits[0].Trending, hits[1].Trending)
	}
	// A null cardData is the other shape in that same answer, and it must not take the row with
	// it: the licence is simply unknown there.
	if hits[2].Ref != "martineux/waiIllustriousSDXL_v160" || hits[2].License != "" {
		t.Errorf("row with cardData:null = %+v", hits[2])
	}
}

// The next shape upstream changes will look EXACTLY like the one above from the panel — the
// sentence "unreadable answer from huggingface.co" is the same whatever field moved. The
// decoder's own complaint names it, so it goes to the log: without it the fractional score
// above had to be found by re-fetching the API by hand.
func TestUnreadableAnswerLogsWhichFieldItWas(t *testing.T) {
	hfSearchStub(t, `[{"id":"Qwen/Qwen2.5-Coder-7B-Instruct-GGUF","downloads":"many"}]`)

	var logged bytes.Buffer
	defer captureLog(&logged)()

	hits, aerr := engineSearchHF(t.Context(), "qwen", "gguf", engineSortDownloads)
	if aerr == nil {
		t.Fatalf("a string where a count belongs was accepted: %+v", hits)
	}
	// What reaches the administrator stays the host and nothing else: a decoder's complaint is
	// not something anybody can act on from the panel.
	if !strings.HasPrefix(aerr.message, "unreadable answer from ") || strings.Contains(aerr.message, "unmarshal") {
		t.Errorf("panel message = %q, want the host-only sentence", aerr.message)
	}
	line := logged.String()
	for _, want := range []string{"downloads", "engineHFSearchRow", "/api/models"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q does not say %q — the field has to be findable from it", line, want)
		}
	}
}
