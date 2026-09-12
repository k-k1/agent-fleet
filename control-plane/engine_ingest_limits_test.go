package main

// What a source forbids, as the search list has to show it.
//
// The fixtures are shaped from what the live APIs answered on 2026-09-12, and the numbers in
// the comments are from that same session: 13 of Civitai's top 20 monthly checkpoints answer
// 401 to an anonymous HEAD, and NOTHING in their metadata says so.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hasCode(list []string, code string) bool {
	for _, c := range list {
		if c == code {
			return true
		}
	}
	return false
}

// 🔴 The regression this test exists for: every permission flag Civitai publishes is a
// NEGATIVE — `allowNoCredit: false` means credit is required — so a document that was never
// read says "non-commercial, credit required, no derivatives, same licence only", four
// restrictions nobody published. The resolve reads the model document best-effort, so the
// unread case is not hypothetical: one 500 from Civitai and every row grows four tags.
func TestRestrictionsNeedTheDocumentToHaveBeenRead(t *testing.T) {
	unread := engineCivitaiRestrictions(engineCivitaiLicenceFacts{}, engineCivitaiVersionFacts{}, nil)
	for _, code := range []string{
		engineRestrictNonCommercial, engineRestrictCredit,
		engineRestrictNoDerivatives, engineRestrictSameLicense,
	} {
		if hasCode(unread, code) {
			t.Errorf("%s claimed from a document that was never read: %v", code, unread)
		}
	}
	// The positive control. The same zero values, marked as actually read, ARE those four
	// restrictions — without this the test above would pass on a function that returns nothing.
	read := engineCivitaiRestrictions(engineCivitaiLicenceFacts{}.with(true), engineCivitaiVersionFacts{}, nil)
	for _, code := range []string{
		engineRestrictNonCommercial, engineRestrictCredit,
		engineRestrictNoDerivatives, engineRestrictSameLicense,
	} {
		if !hasCode(read, code) {
			t.Errorf("%s missing from a read document with every permission withheld: %v", code, read)
		}
	}
}

func TestRestrictionsReadTheFactsCivitaiPublishes(t *testing.T) {
	// A permissive model: commercial use allowed in three ways, credit waived, derivatives and
	// relicensing allowed. It must collect NO licence tag at all — a list where every row is
	// tagged tells nobody anything.
	free := engineCivitaiLicenceFacts{
		AllowCommercialUse:    []string{"Image", "Rent", "Sell"},
		AllowNoCredit:         true,
		AllowDerivatives:      true,
		AllowDifferentLicense: true,
		Availability:          "Public",
	}.with(true)
	if got := engineCivitaiRestrictions(free, engineCivitaiVersionFacts{Availability: "Public"}, nil); len(got) != 0 {
		t.Errorf("a fully permissive model collected %v", got)
	}

	// Paid, permanently — measured shape: `"paidAccess":{"permanent":true,"endsAt":null}`.
	paid := engineCivitaiVersionFacts{Availability: "Public"}
	paid.PaidAccess = &struct {
		Permanent bool   `json:"permanent"`
		EndsAt    string `json:"endsAt"`
	}{Permanent: true}
	if got := engineCivitaiRestrictions(free, paid, nil); !hasCode(got, engineRestrictPaid) {
		t.Errorf("permanent paid access = %v, want %s", got, engineRestrictPaid)
	}
	// And the other shape on the same field, which is a different fact: free once the date
	// passes. Measured 2026-09-12: `{"permanent":false,"endsAt":"2026-09-25T…"}`.
	early := engineCivitaiVersionFacts{Availability: "Public"}
	early.PaidAccess = &struct {
		Permanent bool   `json:"permanent"`
		EndsAt    string `json:"endsAt"`
	}{Permanent: false, EndsAt: "2026-09-25T12:31:11.962Z"}
	got := engineCivitaiRestrictions(free, early, nil)
	if !hasCode(got, engineRestrictEarlyAccess) || hasCode(got, engineRestrictPaid) {
		t.Errorf("early access = %v, want %s and not %s", got, engineRestrictEarlyAccess, engineRestrictPaid)
	}

	// usageControl is the one that means "there is nothing here to take in", and it only ever
	// arrives on the per-version document.
	gen := engineCivitaiRestrictions(free, engineCivitaiVersionFacts{UsageControl: "Rent"}, nil)
	if !hasCode(gen, engineRestrictGenerateOnly) {
		t.Errorf("usageControl=Rent = %v, want %s", gen, engineRestrictGenerateOnly)
	}
	if ok := engineCivitaiRestrictions(free, engineCivitaiVersionFacts{UsageControl: "Download"}, nil); hasCode(ok, engineRestrictGenerateOnly) {
		t.Errorf("usageControl=Download must not be a restriction: %v", ok)
	}

	// The scanners. A `.ckpt` that failed the pickle scan is code that would run on the GPU box.
	files := []engineCivitaiFileFacts{{PickleScanResult: "Danger", VirusScanResult: "Success"}}
	if got := engineCivitaiRestrictions(free, engineCivitaiVersionFacts{}, files); !hasCode(got, engineRestrictPickle) {
		t.Errorf("failed pickle scan = %v, want %s", got, engineRestrictPickle)
	}
	clean := []engineCivitaiFileFacts{{PickleScanResult: "Success", VirusScanResult: "Success"}}
	if got := engineCivitaiRestrictions(free, engineCivitaiVersionFacts{}, clean); len(got) != 0 {
		t.Errorf("a clean scan collected %v", got)
	}
}

// Hugging Face's gate has two kinds and they are different amounts of work: "auto" is satisfied
// by accepting the terms with the account the deployment's token belongs to, "manual" waits on
// the author. Measured 2026-09-12 on `search=gemma`: google/gemma-3-1b-it answers "manual".
func TestHFGateKeepsWhichKindOfGate(t *testing.T) {
	for _, tc := range []struct {
		raw   any
		kind  string
		code  string
		gated bool
	}{
		{raw: false, kind: "", code: "", gated: false},
		{raw: "auto", kind: "auto", code: engineRestrictGatedAuto, gated: true},
		{raw: "manual", kind: "manual", code: engineRestrictGatedManual, gated: true},
		// A gate whose kind the API did not name is still a gate, and gets the weaker code.
		{raw: "yes", kind: "", code: engineRestrictGatedAuto, gated: true},
	} {
		if got := engineHFGated(tc.raw); got != tc.gated {
			t.Errorf("gated(%v) = %v, want %v", tc.raw, got, tc.gated)
		}
		if got := engineHFGatedKind(tc.raw); got != tc.kind {
			t.Errorf("gatedKind(%v) = %q, want %q", tc.raw, got, tc.kind)
		}
		got := engineHFRestrictions(tc.raw)
		if tc.code == "" {
			if len(got) != 0 {
				t.Errorf("restrictions(%v) = %v, want none", tc.raw, got)
			}
			continue
		}
		if !hasCode(got, tc.code) {
			t.Errorf("restrictions(%v) = %v, want %s", tc.raw, got, tc.code)
		}
	}
}

// 🔴 The probe is the ONLY source for "you must be logged in", and its three answers are three
// different things. 307 is the normal downloadable case — measured 2026-09-12, an anonymously
// downloadable asset redirects to the CDN and never answers 200 — and a status that answers
// neither question (429, 500) must leave the field empty rather than say "anyone may have it".
func TestLoginProbeReadsTheThreeAnswers(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, engineLoginYes},
		{http.StatusForbidden, engineLoginYes},
		{http.StatusTemporaryRedirect, engineLoginNo},
		{http.StatusOK, engineLoginNo},
		{http.StatusTooManyRequests, ""},
		{http.StatusInternalServerError, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		got := engineProbeLogin(t.Context(), srv.URL)
		srv.Close()
		if got != tc.want {
			t.Errorf("probe(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
	// Nothing to probe is not an answer either.
	if got := engineProbeLogin(t.Context(), ""); got != "" {
		t.Errorf("probe(no url) = %q, want empty", got)
	}
}

// The page is probed in PARALLEL, because twenty round trips one at a time is the difference
// between a search that answers and one somebody gives up on.
func TestLoginProbesRunAcrossThePage(t *testing.T) {
	var inFlight, peak int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
				break
			}
		}
		// Long enough that a serial implementation cannot overlap by accident, short enough
		// that the whole test is a blink.
		<-time.After(30 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	hits := make([]engineSearchHit, 8)
	urls := make([]string, 8)
	for i := range urls {
		urls[i] = srv.URL
	}
	engineProbeCivitaiLogins(t.Context(), hits, urls)
	for i, h := range hits {
		if h.LoginRequired != engineLoginYes {
			t.Errorf("hit %d = %q, want %q", i, h.LoginRequired, engineLoginYes)
		}
	}
	if peak < 2 {
		t.Errorf("peak concurrency = %d — the page was probed one row at a time", peak)
	}
}

// A row with no download URL is skipped rather than probed, and it must not be left claiming
// anything: the panel draws "" as "nobody could tell".
func TestLoginProbeSkipsRowsWithNoURL(t *testing.T) {
	hits := make([]engineSearchHit, 2)
	engineProbeCivitaiLogins(t.Context(), hits, []string{"", "   "})
	for i, h := range hits {
		if h.LoginRequired != "" {
			t.Errorf("hit %d = %q, want empty", i, h.LoginRequired)
		}
	}
}

// The search answer carries the probe's verdict and the licence codes together, which is the
// whole point: the row that cannot be downloaded and the row that may not be sold are different
// refusals and a person picks between them on this list.
func TestCivitaiSearchCarriesRestrictionsAndTheLoginVerdict(t *testing.T) {
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer download.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":1,"name":"Locked","type":"Checkpoint",
		  "allowCommercialUse":[],"allowNoCredit":false,"allowDerivatives":true,
		  "allowDifferentLicense":true,"availability":"Public","nsfw":true,
		  "modelVersions":[{"id":9,"name":"v1","baseModel":"Illustrious","availability":"Public",
		    "downloadUrl":"` + download.URL + `","trainedWords":["trigger word"," "],
		    "files":[{"type":"Model","primary":true,"pickleScanResult":"Success","virusScanResult":"Success"}]}]}]}`))
	}))
	defer api.Close()
	old := engineCivitaiBase
	engineCivitaiBase = api.URL
	defer func() { engineCivitaiBase = old }()

	hits, aerr := engineSearchCivitai(t.Context(), engineSearchReq{kind: "checkpoint", provider: "comfy"})
	if aerr != nil || len(hits) != 1 {
		t.Fatalf("search: %v %v", hits, aerr)
	}
	h := hits[0]
	if h.LoginRequired != engineLoginYes {
		t.Errorf("login_required = %q, want %q", h.LoginRequired, engineLoginYes)
	}
	if !hasCode(h.Restrictions, engineRestrictNonCommercial) || !hasCode(h.Restrictions, engineRestrictCredit) {
		t.Errorf("restrictions = %v, want non-commercial and credit", h.Restrictions)
	}
	if !hasCode(h.Restrictions, engineRestrictNSFW) {
		t.Errorf("restrictions = %v, want nsfw", h.Restrictions)
	}
	if hasCode(h.Restrictions, engineRestrictNoDerivatives) {
		t.Errorf("restrictions = %v, and derivatives ARE allowed on this row", h.Restrictions)
	}
	// Illustrious is an SDXL fine-tune, and the suggestion is what saves the operator from
	// picking a family off a list of five by hand.
	if h.BaseModelSuggest != "sdxl" {
		t.Errorf("base_model_suggest = %q, want sdxl", h.BaseModelSuggest)
	}
	if h.BaseModel != "Illustrious" {
		t.Errorf("base_model = %q — the UPSTREAM string has to survive beside the suggestion", h.BaseModel)
	}
	// The trigger words, with the blank one dropped: an empty tag reads as a word that failed
	// to render.
	if len(h.TrainedWords) != 1 || h.TrainedWords[0] != "trigger word" {
		t.Errorf("trained_words = %v", h.TrainedWords)
	}
}

// Choosing "LoRA" in the ingest form used to change what the row would be REGISTERED as and
// nothing about the list above it, so looking for an adapter returned twenty checkpoints.
func TestLoraNarrowsBothUpstreams(t *testing.T) {
	var civQuery string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		civQuery = r.URL.Query().Get("types")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer api.Close()
	old := engineCivitaiBase
	engineCivitaiBase = api.URL
	defer func() { engineCivitaiBase = old }()
	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{kind: "checkpoint", lora: true}); aerr != nil {
		t.Fatalf("search: %v", aerr.message)
	}
	if civQuery != "LORA" {
		t.Errorf("civitai types = %q, want LORA", civQuery)
	}
	if _, aerr := engineSearchCivitai(t.Context(), engineSearchReq{kind: "checkpoint"}); aerr != nil {
		t.Fatalf("search: %v", aerr.message)
	}
	if civQuery != "Checkpoint" {
		t.Errorf("civitai types = %q, want Checkpoint", civQuery)
	}

	// 🔴 Hugging Face drops the connection on `filter=lora` with no pipeline tag — measured
	// 2026-09-12, curl exit 56 and no status line, for both `filter=lora` and
	// `filter=gguf&filter=lora`. So the pipeline tag is load-bearing and not extra precision.
	for _, tc := range []struct{ kind, pipeline string }{
		{"checkpoint", "text-to-image"},
		{"gguf", "text-generation"},
	} {
		v := engineSearchFilter(tc.kind, true)
		if got := v.Get("pipeline_tag"); got != tc.pipeline {
			t.Errorf("%s lora search pipeline_tag = %q, want %q", tc.kind, got, tc.pipeline)
		}
		if !strings.Contains(strings.Join(v["filter"], ","), "lora") {
			t.Errorf("%s lora search filter = %v, want lora among them", tc.kind, v["filter"])
		}
	}
}
