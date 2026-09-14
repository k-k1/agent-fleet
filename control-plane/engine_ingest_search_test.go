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
   "gated":false,"lastModified":"2024-11-01T00:00:00.000Z","createdAt":"2024-09-18T09:12:03.000Z",
   "cardData":{"license":"apache-2.0","thumbnail":"https://cdn-uploads.huggingface.co/model.png",
               "extra_gated_prompt":"PROMPT-PADDING-PROMPT-PADDING"},
   "gguf":{"total":7615616512,"context_length":131072,
           "chat_template":"TEMPLATE-PADDING-TEMPLATE-PADDING"}},
  {"id":"black-forest-labs/FLUX.1-dev","downloads":790579,"likes":14538,
   "trendingScore":0.7000000000000001,
   "gated":"auto","lastModified":"2025-06-27T16:22:19.000Z","createdAt":"2024-07-31T15:04:01.000Z",
   "cardData":{"license":"other","license_name":"flux-1-dev-non-commercial-license"}}
]`

// 🔴 The upstream row is much bigger than the hit. `expand[]=cardData` also delivers the whole
// gating prompt and `expand[]=gguf` the whole chat template — measured, each over a kilobyte —
// and this list is drawn 20 rows at a time.
func TestSearchCopiesOnlyTheFieldsThePanelDraws(t *testing.T) {
	_, q := hfSearchStub(t, hfSearchBody)
	hits, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "qwen", kind: "gguf", sort: ""})
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
	if h.ModelRef != h.Ref {
		t.Errorf("model_ref = %q, want the Hugging Face repository %q", h.ModelRef, h.Ref)
	}
	if h.PreviewURL != "https://cdn-uploads.huggingface.co/model.png" {
		t.Errorf("preview_url = %q, want the published model-card thumbnail", h.PreviewURL)
	}
	if hits[1].PreviewURL != "" {
		t.Errorf("preview_url = %q for a card that publishes no thumbnail", hits[1].PreviewURL)
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

// Where a hit came FROM, and when it first appeared (ADR 0072 decision 11).
//
// The link is composed by the CP and used by the panel as an href verbatim, so what has to be
// right is here: the two sources spell a page differently, and Civitai's needs the MODEL id —
// a number the panel never otherwise sees, because `ref` is the version's.
//
// Dates are also the explicit initial rankings: updated repositories on Hugging Face and new
// model arrivals on Civitai remain separate meanings in the API.
func TestSearchHitsCarryTheirPageAndPublicationDate(t *testing.T) {
	_, q := hfSearchStub(t, hfSearchBody)
	hits, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "qwen", kind: "gguf", sort: ""})
	if aerr != nil {
		t.Fatalf("search: %v", aerr.message)
	}
	// Asked for on the same read: without the expansion the field is simply absent, and the
	// panel would show a repository with no age (measured live 2026-09-11 that this is the
	// spelling that answers it).
	if !strings.Contains(strings.Join((*q)["expand[]"], ","), "createdAt") {
		t.Errorf("createdAt was not expanded: %v", (*q)["expand[]"])
	}
	if hits[0].PublishedAt != "2024-09-18T09:12:03.000Z" {
		t.Errorf("published_at = %q, want the repository's createdAt", hits[0].PublishedAt)
	}
	// Both dates, and they are NOT the same one twice: created a year before it was last
	// touched is the shape that says "maintained".
	if hits[0].UpdatedAt != "2024-11-01T00:00:00.000Z" {
		t.Errorf("updated_at = %q, want lastModified", hits[0].UpdatedAt)
	}
	if hits[0].URL != engineIngestBase+"/Qwen/Qwen2.5-Coder-7B-Instruct-GGUF" {
		t.Errorf("url = %q", hits[0].URL)
	}

	// Civitai: the MODEL id is in the path and the VERSION id in the query, because
	// `/models/<model>` alone opens on whatever version is newest today — not the one this row
	// would take in. Verified against the live site 2026-09-11: the composed form answers 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[
		  {"id":133005,"name":"Juggernaut XL","type":"Checkpoint","allowCommercialUse":["Image"],
		   "modelVersions":[{"id":1759168,"name":"Ragnarok","baseModel":"SDXL 1.0",
		                     "publishedAt":"2025-05-07T21:02:16.940Z",
		                     "images":[{"url":"javascript:alert(1)"},
							   {"url":"https://image.civitai.com/model.jpeg"}]}]}]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	defer func() { engineCivitaiBase = old }()

	civ, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "juggernaut", kind: "checkpoint", sort: ""})
	if aerr != nil || len(civ) != 1 {
		t.Fatalf("civitai search: %v %v", civ, aerr)
	}
	if civ[0].URL != srv.URL+"/models/133005?modelVersionId=1759168" {
		t.Errorf("url = %q, want the model page pointed at this version", civ[0].URL)
	}
	// 🔴 `publishedAt` is the PUBLICATION date and it used to ride as `updated_at`. Civitai
	// answers no `updatedAt` at all here (measured live 2026-09-11), so a row that carried it
	// as both would print one date twice, under two labels, one of them wrong.
	if civ[0].PublishedAt != "2025-05-07T21:02:16.940Z" {
		t.Errorf("published_at = %q, want the version's publishedAt", civ[0].PublishedAt)
	}
	if civ[0].UpdatedAt != "" {
		t.Errorf("updated_at = %q — Civitai published no such date, so the row must not claim one",
			civ[0].UpdatedAt)
	}
	if civ[0].PreviewURL != "https://image.civitai.com/model.jpeg" {
		t.Errorf("preview_url = %q, want the first safe HTTP(S) image", civ[0].PreviewURL)
	}
	if civ[0].Name != "Juggernaut XL" {
		t.Errorf("name = %q, want the model title without a version suffix", civ[0].Name)
	}
	if got := engineSafeCivitaiPreviewURL("https://metadata.example.invalid/private.jpeg"); got != "" {
		t.Errorf("a non-Civitai preview origin was exposed: %q", got)
	}
	for _, unsafe := range []string{"javascript:alert(1)", "https://user:password@example.com/x.png"} {
		if got := engineSafeHTTPURL(unsafe); got != "" {
			t.Errorf("unsafe HTTP thumbnail %q was exposed as %q", unsafe, got)
		}
	}
}

// A model with no id still gets a page: the version-only form Civitai resolves, which is what
// the licence link has always used. Nothing measured answers that, but the id is upstream's to
// omit and a row whose link is `/models/0?…` is worse than one that is a little less precise.
func TestCivitaiModelURLFallsBackToTheVersion(t *testing.T) {
	if got := engineCivitaiModelURL("https://civitai.example", 0, 501240); got != "https://civitai.example/models/?modelVersionId=501240" {
		t.Errorf("url with no model id = %q", got)
	}
	if got := engineCivitaiModelURL("https://civitai.example", 4201, 501240); got != "https://civitai.example/models/4201?modelVersionId=501240" {
		t.Errorf("url = %q", got)
	}
}

// The filter is per ROLE, for the same reason the file picker has one: a repository this engine
// cannot load is a dead end that `resolve` refuses a moment later.
func TestSearchFiltersByWhatTheEngineCanLoad(t *testing.T) {
	_, q := hfSearchStub(t, `[]`)
	if _, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "qwen", kind: "gguf", sort: ""}); aerr != nil {
		t.Fatalf("gguf: %v", aerr.message)
	}
	if q.Get("filter") != "gguf" || q.Get("pipeline_tag") != "" {
		t.Errorf("llm search asked %v, want filter=gguf", *q)
	}
	if q.Get("sort") != "lastModified" || q.Get("direction") != "-1" {
		t.Errorf("initial Hugging Face browse is not ordered by updated descending: %v", *q)
	}
	if _, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "sdxl", kind: "checkpoint", sort: ""}); aerr != nil {
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

	hits, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "juggernaut", kind: "checkpoint", sort: ""})
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
	if hits[0].ModelRef != "133005" {
		t.Errorf("model_ref = %q, want the model id 133005", hits[0].ModelRef)
	}
	if hits[0].BaseModel != "SDXL 1.0" || hits[0].Downloads != 1632949 {
		t.Errorf("hit = %+v", hits[0])
	}
	// Civitai is not asked for GGUFs at all: it hosts image models, and llama.cpp can load none
	// of them.
	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "juggernaut", kind: "gguf", sort: ""}); aerr == nil || aerr.status != http.StatusBadRequest {
		t.Errorf("civitai for the llm role = %v, want a 400 refusal", aerr)
	}
}

// serviceUnavailableThenOK answers 503 for the first `fails` requests and 200 (with `body`)
// after that, and counts how many requests it saw.
func serviceUnavailableThenOK(t *testing.T, fails int, body string) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests <= fails {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s, &requests
}

// 🔴 Measured on af-sandbox (2026-09-14): Civitai's search endpoint answers 503 under load and
// clears within a second, which used to fail the whole search on the FIRST bad tick — an
// operator's only recourse was to press search again by hand. engineSearchGetJSON now retries a
// 503 in place; this is the positive control that the retry actually fires and the eventual
// success is still returned, not swallowed as an error. Deliberately NOT on engineIngestGetJSON
// (see its comment) — that one backs the VAE scan's own per-batch budget, and multiplying every
// probe by three would blow it silently.
func TestCivitaiSearchRetries503ThenSucceeds(t *testing.T) {
	s, requests := serviceUnavailableThenOK(t, 2, `{"items":[]}`)
	defer s.Close()
	old := engineCivitaiBase
	engineCivitaiBase = s.URL
	defer func() { engineCivitaiBase = old }()

	hits, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "juggernaut", kind: "checkpoint", sort: ""})
	if aerr != nil {
		t.Fatalf("search: %v", aerr.message)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %+v, want none from an empty page", hits)
	}
	if *requests != 3 {
		t.Errorf("requests = %d, want 3 (2 failures + the retry that succeeded)", *requests)
	}
}

// A 503 that never clears must still surface as an error to the panel (the "取り込み元が想定外
// の応答を返しました" banner) rather than retrying forever or reporting nothing.
func TestCivitaiSearchGivesUpAfterExhaustingRetries(t *testing.T) {
	s, requests := serviceUnavailableThenOK(t, 99, "")
	defer s.Close()
	old := engineCivitaiBase
	engineCivitaiBase = s.URL
	defer func() { engineCivitaiBase = old }()

	_, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "juggernaut", kind: "checkpoint", sort: ""})
	if aerr == nil {
		t.Fatal("search of a permanently-503 host returned no error")
	}
	if aerr.status != http.StatusBadGateway || !strings.Contains(aerr.message, "503") {
		t.Errorf("error = %+v, want a 502 mentioning the upstream's 503", aerr)
	}
	if *requests != engineSearchGetJSONRetries {
		t.Errorf("requests = %d, want exactly %d (bounded retry, not unbounded)", *requests, engineSearchGetJSONRetries)
	}
}

// 401/403 mean the asset needs an account NOW, not "ask again in a second" — retrying would
// only make a login wall look like flakiness.
func TestCivitaiSearchDoesNotRetryAuthRefusals(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer s.Close()
	old := engineCivitaiBase
	engineCivitaiBase = s.URL
	defer func() { engineCivitaiBase = old }()

	_, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "juggernaut", kind: "checkpoint", sort: ""})
	if aerr == nil || aerr.code != errCodeIngestSourceForbid {
		t.Fatalf("error = %+v, want errCodeIngestSourceForbid", aerr)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want exactly 1 (a 401 is not retried)", requests)
	}
}

// The civitai-red tab is the only place `nsfw=true` is ever sent, and the only place
// engineCivitaiRedBase is ever the host — see engineSearchCivitaiPage. Measured 2026-09-14
// against the live API: neither host returns an NSFW-flagged model without the parameter, so
// this is load-bearing, not a nicety. nsfwLevel rides on every hit regardless of tab.
func TestCivitaiRedSearchSetsNsfwAndUsesTheRedHost(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
		  {"id":827184,"name":"WAI-illustrious-SDXL","type":"Checkpoint","nsfwLevel":60,
		   "stats":{"downloadCount":1511569,"thumbsUpCount":85086},
		   "allowCommercialUse":["Image"],
		   "modelVersions":[{"id":2883731,"name":"v17.0","baseModel":"Illustrious",
		                     "publishedAt":"2026-04-23T13:02:02.382Z"}]}]}`))
	}))
	defer srv.Close()
	oldRed := engineCivitaiRedBase
	engineCivitaiRedBase = srv.URL
	defer func() { engineCivitaiRedBase = oldRed }()
	// The plain host must not be reachable from this path — a fallback to it would defeat the
	// point of choosing the tab.
	oldPlain := engineCivitaiBase
	engineCivitaiBase = "https://civitai.example.invalid"
	defer func() { engineCivitaiBase = oldPlain }()

	hits, aerr := engineSearchCivitaiRed(t.Context(), engineSearchReq{q: "wai", kind: "checkpoint", sort: ""})
	if aerr != nil {
		t.Fatalf("civitai-red search: %v", aerr.message)
	}
	if got.Get("nsfw") != "true" {
		t.Errorf("query = %v, want nsfw=true", got)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].NsfwLevel != 60 {
		t.Errorf("nsfw_level = %d, want 60", hits[0].NsfwLevel)
	}
	if hits[0].URL != srv.URL+"/models/827184?modelVersionId=2883731" {
		t.Errorf("url = %q, want the version page on the RED host", hits[0].URL)
	}
}

// Non-commercial is on screen BEFORE the acceptance, which is decision 10's rule. Civitai has
// no licence field in Hugging Face's sense, so the one thing it does say about terms is the
// empty `allowCommercialUse`.
//
// 🔴 It rides as a RESTRICTION CODE now, not as a synthesised licence name. The panel drew both
// and they said the same thing, which on a real render was two tags on one card — and the code
// is the one with a word in the locale catalogue. What must not change is that the fact reaches
// the card at all, which is what this test pins.
func TestCivitaiSearchMarksNonCommercial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":2,"name":"X","allowCommercialUse":[],
		  "modelVersions":[{"id":5,"baseModel":"SDXL 1.0"}]}]}`))
	}))
	defer srv.Close()
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	defer func() { engineCivitaiBase = old }()

	hits, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "x", kind: "checkpoint", sort: ""})
	if aerr != nil || len(hits) != 1 {
		t.Fatalf("search: %v %v", hits, aerr)
	}
	if !hasCode(hits[0].Restrictions, engineRestrictNonCommercial) {
		t.Errorf("restrictions = %v, want the non-commercial mark", hits[0].Restrictions)
	}
	// And not twice: the licence name is left as the source gave it (nothing), because a
	// synthesised one would be a second tag saying what the code above already says.
	if hits[0].LicenseName != "" {
		t.Errorf("license_name = %q — the restriction code is where this fact lives now", hits[0].LicenseName)
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
		{"", "lastModified"},
		{engineSortUpdated, "lastModified"},
		{engineSortDownloads, "downloads"},
		{engineSortTrending, "trendingScore"},
		{engineSortLikes, "likes"},
	} {
		if _, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "", kind: "gguf", sort: tc.sort}); aerr != nil {
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
	if _, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "", kind: "gguf", sort: "newest"}); aerr == nil {
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

	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "", kind: "checkpoint", sort: engineSortTrending}); aerr != nil {
		t.Fatalf("trending: %v", aerr.message)
	}
	if got.Get("sort") != "Most Downloaded" || got.Get("period") != "Month" {
		t.Errorf("trending asked %v, want sort=Most Downloaded&period=Month", got)
	}
	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "", kind: "checkpoint", sort: engineSortLikes}); aerr != nil {
		t.Fatalf("likes: %v", aerr.message)
	}
	if got.Get("sort") != "Highest Rated" || got.Get("period") != "" {
		t.Errorf("likes asked %v, want sort=Highest Rated with no period", got)
	}
	// 🔴 Not passed through: this API answers 400 to a sort it does not know (measured).
	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "", kind: "checkpoint", sort: engineSortNewest}); aerr != nil {
		t.Errorf("new arrivals: %v", aerr)
	}
	if got.Get("sort") != "Newest" || got.Get("period") != "" {
		t.Errorf("new arrivals asked %v, want sort=Newest with no period", got)
	}
	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{q: "", kind: "checkpoint", sort: engineSortUpdated}); aerr == nil {
		t.Error("Hugging Face's updated ranking reached Civitai")
	}
}

// Pagination passes only the opaque token back to the fixed upstream endpoint. In particular,
// Hugging Face's Link URL is not followed: accepting an arbitrary next URL here would turn the
// admin route into an SSRF primitive.
func TestSearchPaginationUsesOpaqueBoundedCursors(t *testing.T) {
	var got url.Values
	requests := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		got = r.URL.Query()
		w.Header().Set("Link", "<"+srv.URL+"/api/models?cursor=next%3D%3D>; rel=\"next\"")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })

	_, next, aerr := engineSearchHFPage(t.Context(), engineSearchReq{
		q: "flux", kind: "checkpoint", sort: engineSortUpdated, cursor: "current==",
	})
	if aerr != nil {
		t.Fatalf("HF page: %v", aerr.message)
	}
	if got.Get("cursor") != "current==" || next != "next==" {
		t.Errorf("cursor request/answer = %q/%q, want current==/next==", got.Get("cursor"), next)
	}
	a, _, _ := engineModelAdminAPI(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest/search",
		strings.NewReader(`{"source":"hf","sort":"updated","cursor":"current=="}`))
	r.SetPathValue("key", "image")
	a.searchIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
	var answer map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &answer)
	if rec.Code != http.StatusOK || answer["next_cursor"] != "next==" {
		t.Errorf("wire pagination = %d %v", rec.Code, answer)
	}

	h := http.Header{}
	h.Set("Link", `<https://metadata.example.invalid/api/models?cursor=secret>; rel="next"`)
	if cursor := engineHFNextCursor(h); cursor != "" {
		t.Errorf("a different origin supplied next cursor %q", cursor)
	}

	before := requests
	if _, _, aerr := engineSearchHFPage(t.Context(), engineSearchReq{
		kind: "checkpoint", cursor: "bad\ncursor",
	}); aerr == nil || aerr.status != http.StatusBadRequest {
		t.Errorf("control-character cursor = %v, want 400", aerr)
	}
	if requests != before {
		t.Error("an invalid cursor reached the upstream")
	}
	if engineCursorValid(strings.Repeat("x", engineCursorMax+1)) {
		t.Error("an unbounded cursor was accepted")
	}
}

func TestCivitaiPaginationUsesMetadataCursor(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"items":[],"metadata":{"nextCursor":"2026-09-13 14:37:43.239|2935601"}}`))
	}))
	t.Cleanup(srv.Close)
	old := engineCivitaiBase
	engineCivitaiBase = srv.URL
	t.Cleanup(func() { engineCivitaiBase = old })

	_, next, aerr := engineSearchCivitaiPage(t.Context(), engineSearchReq{
		kind: "checkpoint", sort: engineSortNewest, cursor: "2026-09-12 10:00:00|10",
	}, false)
	if aerr != nil {
		t.Fatalf("Civitai page: %v", aerr.message)
	}
	if got.Get("cursor") != "2026-09-12 10:00:00|10" || next != "2026-09-13 14:37:43.239|2935601" {
		t.Errorf("cursor request/answer = %q/%q", got.Get("cursor"), next)
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

	hits, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "WAI", kind: "checkpoint", sort: engineSortDownloads})
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

	hits, aerr := engineSearchHF(t.Context(), engineSearchReq{q: "qwen", kind: "gguf", sort: engineSortDownloads})
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
