package main

// Searching Hugging Face and Civitai for something to take in (ADR 0072 decision 11).
//
// P4 turned the free-text FILENAME into a picker, because a name one letter short and a
// repository that does not carry the file are the same refusal. The same hole was still open
// one field up: the repository name itself was free text, so the only way in was to look
// `owner/name` up in another window and paste it.
//
// Both APIs answer anonymously (P4 measurements 1 and 2), so this needs no token — decision 6's
// "the CP only ever reads the source" extends here unchanged.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// engineSearchLimit is how many hits are asked for and returned. Twenty is a screen; the
// interesting thing is at the top because the order is a ranking.
const engineSearchLimit = 20

// The rankings, which are also how this answers a query with NO words in it: "show me what
// people use" is the other half of "find the one I already have a name for", and a catalogue
// nobody has browsed is exactly where the second question is unanswerable.
//
// A closed set, mapped per upstream rather than passed through: Civitai answers 400 to a sort
// it does not know (measured), and Hugging Face quietly ignores one, which is worse — an
// unranked list that looks ranked.
const (
	engineSortDownloads = "downloads"
	engineSortTrending  = "trending"
	engineSortLikes     = "likes"
)

// 🔴 There is deliberately no "newest". Measured 2026-09-09: `sort=lastModified` and
// `sort=createdAt` on `filter=gguf` return nothing but bulk automated re-quantisations
// (mradermacher/*-i1-GGUF), every one of them at 0 downloads and 0 likes. A ranking whose first
// screen is always the same uploader's robot is not a way in, and "trending" already answers
// what somebody reaching for "new" wants.
func engineSortHF(sort string) (string, bool) {
	switch sort {
	case "", engineSortDownloads:
		return "downloads", true
	case engineSortTrending:
		return "trendingScore", true
	case engineSortLikes:
		return "likes", true
	}
	return "", false
}

// engineSortCivitai maps the same three. Civitai has no trending score, so "trending" is
// "most downloaded THIS MONTH" — the period is what makes it a different list (measured: it
// answers different models from the all-time one).
func engineSortCivitai(sort string) (value, period string, ok bool) {
	switch sort {
	case "", engineSortDownloads:
		return "Most Downloaded", "", true
	case engineSortTrending:
		return "Most Downloaded", "Month", true
	case engineSortLikes:
		return "Highest Rated", "", true
	}
	return "", "", false
}

// engineSearchHit is one result. A named type, and a NARROW one: what the upstream answers is
// far larger than this (see engineHFSearchRow), and every field here is one the panel draws.
type engineSearchHit struct {
	// Source is "hf" or "civitai" — the panel needs it to know which identifier Ref is.
	Source string `json:"source"`
	// Ref is what the ingest form is filled with: the repository for HF, the VERSION id for
	// Civitai. 🔴 Civitai's ingest takes a version id, not the model id, and the two are
	// different numbers on the same page.
	Ref string `json:"ref"`
	// Name is what a person reads. Same as Ref for HF, the model's title for Civitai.
	Name string `json:"name"`
	// The three numbers a ranking is built on, all three on every row: sorting by one of them
	// and showing only that one leaves "why is this here" unanswerable, and the answer to
	// "is this the one everybody uses" is not the same as "is this what people are looking at
	// this week".
	Downloads int64 `json:"downloads,omitempty"`
	Likes     int64 `json:"likes,omitempty"`
	// Trending is Hugging Face's own score. Civitai publishes none, so a Civitai hit leaves it
	// empty rather than borrowing another number and calling it trending.
	//
	// 🔴 A score, not a count, and it is FRACTIONAL. Measured 2026-09-10 on `search=WAI`:
	// two of twenty rows answered 0.1 and 0.7000000000000001, and an int64 field made
	// encoding/json refuse the whole array — the panel showed "unreadable answer from
	// huggingface.co" and no results at all, for a search that was working perfectly.
	Trending float64 `json:"trending,omitempty"`
	Gated    bool    `json:"gated,omitempty"`
	// GatedKind is WHICH gate, and the two are different amounts of work: "auto" is satisfied
	// by accepting the terms once with the account the deployment's token belongs to, "manual"
	// waits on the author approving that account by hand. Measured 2026-09-12 on
	// `search=gemma`: google/gemma-3-1b-it answers `"manual"` while every other row answers
	// `false`, so the distinction is published and collapsing it loses the one thing that says
	// whether this repository is reachable today.
	GatedKind   string `json:"gated_kind,omitempty"`
	License     string `json:"license,omitempty"`
	LicenseName string `json:"license_name,omitempty"`
	BaseModel   string `json:"base_model,omitempty"`
	// BaseModelSuggest is BaseModel translated into the family vocabulary this deployment's
	// provider dispatches on, or "" when nothing here is confident enough to name one. A
	// SUGGESTION, never the value: ADR 0072 decision 2 keeps the declaration with the operator,
	// and storing an upstream display name produced rows that looked complete and refused to
	// generate.
	BaseModelSuggest string `json:"base_model_suggest,omitempty"`
	// LoginRequired is "yes", "no" or "" (nobody could tell) — three states because Civitai
	// publishes NOTHING that predicts it. Measured 2026-09-12 on the top 20 monthly
	// checkpoints: 13 of 20 answer 401 to an anonymous HEAD of their download URL, and all 20
	// carry `availability: "Public"`, `flags: 0`, `status: "Published"` and
	// `usageControl: "Download"`. So the only honest source is the probe, and a probe that
	// could not run must say so rather than answer "no".
	LoginRequired string `json:"login_required,omitempty"`
	// Restrictions is a closed set of codes the panel turns into tags — see the engineRestrict*
	// constants. Codes rather than sentences: the two sources spell the same restriction
	// differently and the Console holds the locale catalogue.
	Restrictions []string `json:"restrictions,omitempty"`
	// TrainedWords is a LoRA's trigger words, which Civitai publishes in the search answer
	// itself. The one piece of "how do I use this" that arrives structured rather than buried
	// in an HTML description, and a LoRA without its trigger silently does nothing.
	TrainedWords []string `json:"trained_words,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
	// PublishedAt is when the thing first appeared, beside UpdatedAt's "when it last changed".
	// Both, because for a quantisation repository they are a year apart and only the pair
	// answers "is this maintained": measured 2026-09-11, the 30B this deployment runs was
	// created 2025-07-31 and last touched 2026-01-30.
	//
	// 🔴 Display only. The note on engineSortHF stands — ordering by a date returns nothing but
	// bulk automated re-quantisations — so there is still no "newest" ranking to sort by.
	PublishedAt string `json:"published_at,omitempty"`
	// URL is the upstream page, composed HERE and used by the panel as an href verbatim. Not
	// left to the client: the two sources spell it differently and Civitai's needs the MODEL
	// id, which is not Ref (that is the version's) and reaches the panel nowhere else. A third
	// source then costs one change in one place.
	URL           string `json:"url,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
}

// engineHFSearchRow is one row of `GET /api/models`. 🔴 What is NOT here is the point: asking
// for `cardData` also delivers `extra_gated_prompt` and asking for `gguf` delivers the whole
// `chat_template`. Decoding into a narrow struct drops them before they can be copied onward,
// and the difference is not marginal — measured end to end on 2026-09-09, the same 20 rows are
// **211,015 bytes upstream and 5,125 bytes out of this route (41x)**.
type engineHFSearchRow struct {
	ID        string `json:"id"`
	Downloads int64  `json:"downloads"`
	Likes     int64  `json:"likes"`
	// Fractional — see engineSearchHit.Trending. One row of twenty is enough to lose the page.
	TrendingScore float64 `json:"trendingScore"`
	// Gated is `false`, `"auto"` or `"manual"` — a bool or a string on the same field, which is
	// why the existing engineHFGated is reused rather than a typed one written here.
	Gated        any    `json:"gated"`
	LastModified string `json:"lastModified"`
	// When the repository was created. Measured live 2026-09-11: `expand[]=createdAt` answers
	// `"createdAt":"2025-07-31T10:27:38.000Z"` next to `lastModified`, so the pair costs one
	// more expansion on the same read.
	CreatedAt string `json:"createdAt"`
	CardData  struct {
		License     any    `json:"license"`
		LicenseName string `json:"license_name"`
	} `json:"cardData"`
	GGUF struct {
		Total         int64 `json:"total"`
		ContextLength int   `json:"context_length"`
	} `json:"gguf"`
}

// engineCivitaiSearchDoc is one page of `GET /api/v1/models`.
type engineCivitaiSearchDoc struct {
	Items []struct {
		// ID is the MODEL id — the number in the page's URL, and not the one an ingest takes.
		// It is read for exactly that: the link back to the page.
		ID   int    `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
		// CreatedAt is a fallback for the version's publication date. 🔴 Measured live
		// 2026-09-11: `/api/v1/models` answers neither this nor a version `createdAt` —
		// `publishedAt` is the only date on the page — so this is empty in practice and is
		// decoded rather than assumed away.
		CreatedAt string `json:"createdAt"`
		Stats     struct {
			DownloadCount int64 `json:"downloadCount"`
			ThumbsUpCount int64 `json:"thumbsUpCount"`
		} `json:"stats"`
		// The licence matrix and the safety flags, all of which arrive on this same read.
		// AllowCommercialUse is a list on this API ("Image", "Rent", "Sell"), and an empty one
		// is the non-commercial case decision 10 wants on screen.
		engineCivitaiLicenceFacts
		ModelVersions []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			BaseModel string `json:"baseModel"`
			// What this version costs and whether it may be fetched at all.
			engineCivitaiVersionFacts
			// TrainedWords is the LoRA's trigger. Published right here, on the list.
			TrainedWords []string `json:"trainedWords"`
			// DownloadURL is not shown to anybody: it is what the login probe HEADs. The
			// version carries one, and so does each file — the version's is the one that
			// matches what an ingest of this row would fetch.
			DownloadURL string `json:"downloadUrl"`
			Files       []struct {
				engineCivitaiFileFacts
				Type    string `json:"type"`
				Primary bool   `json:"primary"`
			} `json:"files"`
			// The version's own dates. 🔴 Measured live 2026-09-11 on `/api/v1/models`: a
			// version answers `publishedAt` and nothing else — no `updatedAt`, no `createdAt`.
			// Both are still decoded, because a version fetched by id does carry more and this
			// struct must not silently drop a date that appears.
			PublishedAt string `json:"publishedAt"`
			UpdatedAt   string `json:"updatedAt"`
		} `json:"modelVersions"`
	} `json:"items"`
}

// engineSearchFilter is the per-role narrowing, measured against the live API on 2026-09-09.
// A repository this engine cannot load is a dead end — `resolve` refuses it a moment later —
// and the file picker already refuses to show those, so the repository list must not either.
//
// `lora` narrows it again to adapters. Until it existed, choosing "LoRA" in the ingest form
// changed what the row would be REGISTERED as and nothing about the list above it, so looking
// for an adapter returned twenty checkpoints.
//
// 🔴 `filter=lora` is never sent alone. Measured 2026-09-12: `filter=lora` and
// `filter=gguf&filter=lora` both drop the connection (curl exit 56, no status line), while the
// same query with a `pipeline_tag` answers 200. So the pipeline tag is load-bearing here, not
// extra precision.
func engineSearchFilter(kind string, lora bool) url.Values {
	v := url.Values{}
	if kind == "gguf" {
		// The library tag, which is what a repository of quantised files carries.
		v.Set("filter", "gguf")
		if lora {
			v.Add("filter", "lora")
			v.Set("pipeline_tag", "text-generation")
		}
		return v
	}
	v.Set("pipeline_tag", "text-to-image")
	if lora {
		v.Set("filter", "lora")
	}
	return v
}

// engineSearchHF asks Hugging Face. An empty q is a RANKING rather than a mistake: the API
// answers the filter's top rows, which is the "what do people use" half of the picker.
func engineSearchHF(ctx context.Context, req engineSearchReq) ([]engineSearchHit, *apiError) {
	q, kind := req.q, req.kind
	sortBy, ok := engineSortHF(req.sort)
	if !ok {
		return nil, engineBadSort(req.sort)
	}
	v := engineSearchFilter(kind, req.lora)
	if q != "" {
		v.Set("search", q)
	}
	v.Set("sort", sortBy)
	v.Set("direction", "-1")
	v.Set("limit", strconv.Itoa(engineSearchLimit))
	// `expand[]` is what makes one read enough: without it the rows carry neither the gating
	// flag nor the licence, and the panel would have to resolve 20 repositories to draw a list.
	expand := []string{"gated", "downloads", "likes", "trendingScore", "cardData", "lastModified", "createdAt"}
	if kind == "gguf" {
		expand = append(expand, "gguf")
	}
	for _, e := range expand {
		v.Add("expand[]", e)
	}
	var rows []engineHFSearchRow
	if aerr := engineIngestGetJSON(ctx, engineIngestBase+"/api/models?"+v.Encode(), &rows); aerr != nil {
		return nil, aerr
	}
	out := make([]engineSearchHit, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		out = append(out, engineSearchHit{
			Source: "hf", Ref: r.ID, Name: r.ID,
			Downloads: r.Downloads, Likes: r.Likes, Trending: r.TrendingScore,
			Gated:        engineHFGated(r.Gated),
			GatedKind:    engineHFGatedKind(r.Gated),
			Restrictions: engineHFRestrictions(r.Gated),
			License:      engineFirstString(r.CardData.License),
			LicenseName:  strings.TrimSpace(r.CardData.LicenseName),
			UpdatedAt:    strings.TrimSpace(r.LastModified),
			PublishedAt:  strings.TrimSpace(r.CreatedAt),
			URL:          engineIngestBase + "/" + r.ID,
			// The GGUF numbers are the repository's, i.e. one of its files — a draft for the
			// form, never the value. The resolve of the chosen FILE is what the row is built
			// from (decision 11).
			Bytes:         r.GGUF.Total,
			ContextLength: r.GGUF.ContextLength,
		})
	}
	return out, nil
}

// engineCivitaiModelURL is the page a person opens for a hit: the MODEL's page, pointed at the
// VERSION the hit is for.
//
// Both ids are needed and they are different numbers — `/models/<model>` alone opens on
// whatever version is newest today, which is not the one this row's `ref` would take in. A
// model with no id falls back to the version-only form the licence link already uses, which
// Civitai resolves.
func engineCivitaiModelURL(modelID, versionID int) string {
	if modelID <= 0 {
		return engineCivitaiBase + "/models/?modelVersionId=" + strconv.Itoa(versionID)
	}
	return engineCivitaiBase + "/models/" + strconv.Itoa(modelID) + "?modelVersionId=" + strconv.Itoa(versionID)
}

// engineSearchCivitai asks Civitai. The hit carries the newest VERSION's id, because that is
// what an ingest takes — `civitai.com/models/<model>` and the version behind its download
// button are different numbers, and the model id is the one on the page's URL.
func engineSearchCivitai(ctx context.Context, req engineSearchReq) ([]engineSearchHit, *apiError) {
	sortBy, period, ok := engineSortCivitai(req.sort)
	if !ok {
		return nil, engineBadSort(req.sort)
	}
	if req.kind == "gguf" {
		// Civitai hosts image models. Offering it to the llm role would return checkpoints
		// llama.cpp cannot load, which is the dead end this filter exists to prevent.
		return []engineSearchHit{}, nil
	}
	v := url.Values{}
	if req.q != "" {
		v.Set("query", req.q)
	}
	// The type is the whole difference between an adapter list and a checkpoint list here, and
	// it is the field the ingest form's own kind selector now decides.
	if req.lora {
		v.Set("types", "LORA")
	} else {
		v.Set("types", "Checkpoint")
	}
	v.Set("sort", sortBy)
	if period != "" {
		v.Set("period", period)
	}
	v.Set("limit", strconv.Itoa(engineSearchLimit))
	var doc engineCivitaiSearchDoc
	if aerr := engineIngestGetJSON(ctx, engineCivitaiBase+"/api/v1/models?"+v.Encode(), &doc); aerr != nil {
		return nil, aerr
	}
	out := make([]engineSearchHit, 0, len(doc.Items))
	// The URL each hit's login probe HEADs, at the same index. Kept beside the list rather than
	// on the hit: it is a CDN link with a signature on it, and nothing in the panel may follow
	// it — the whole point of the probe is that the bytes are the ingest task's business.
	probe := make([]string, 0, len(doc.Items))
	for _, m := range doc.Items {
		if len(m.ModelVersions) == 0 {
			// Nothing to take in: a model page with no published version has no file behind it.
			continue
		}
		ver := m.ModelVersions[0]
		name := strings.TrimSpace(m.Name)
		if v := strings.TrimSpace(ver.Name); v != "" {
			name += " — " + v
		}
		files := make([]engineCivitaiFileFacts, 0, len(ver.Files))
		for _, f := range ver.Files {
			// The scan verdicts of the file this row would actually take in. A preview image's
			// verdict is not this model's, and the ingest picks the `Model` file.
			if strings.EqualFold(f.Type, "Model") || f.Primary {
				files = append(files, f.engineCivitaiFileFacts)
			}
		}
		hit := engineSearchHit{
			Source: "civitai", Ref: strconv.Itoa(ver.ID), Name: name,
			Downloads: m.Stats.DownloadCount, Likes: m.Stats.ThumbsUpCount,
			BaseModel:        strings.TrimSpace(ver.BaseModel),
			BaseModelSuggest: engineFamilyGuess(req.provider, ver.BaseModel),
			Restrictions:     engineCivitaiRestrictions(m.engineCivitaiLicenceFacts.with(true), ver.engineCivitaiVersionFacts, files),
			TrainedWords:     engineTrimStrings(ver.TrainedWords),
			// 🔴 `publishedAt` is the PUBLICATION date, and it used to ride as `updated_at` —
			// the one thing it is not. Civitai answers no `updatedAt` here at all (measured
			// 2026-09-11), so carrying it as both would print one date twice under two labels,
			// one of them wrong.
			UpdatedAt:   strings.TrimSpace(ver.UpdatedAt),
			PublishedAt: engineFirstNonEmpty(ver.PublishedAt, m.CreatedAt),
			URL:         engineCivitaiModelURL(m.ID, ver.ID),
		}
		// 🔴 The licence name this used to synthesise ("non-commercial", from an empty
		// allowCommercialUse) is GONE, and nothing was lost: the same fact now rides as the
		// `noncommercial` restriction code, which the panel draws in its own vocabulary. Keeping
		// both put two tags saying the same thing on the same card (seen on a real render).
		out = append(out, hit)
		probe = append(probe, strings.TrimSpace(ver.DownloadURL))
	}
	// The one fact on this list that no amount of reading the metadata produces.
	engineProbeCivitaiLogins(ctx, out, probe)
	return out, nil
}

// engineTrimStrings drops the blanks and the surrounding space from a list a source published.
// An empty trigger word draws an empty tag, which reads as a word somebody failed to render.
func engineTrimStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// searchIngest (POST …/{key}/ingest/search) is the repository picker behind one engine's
// ingest form: the engine decides what kind of file is worth offering.
//
// It starts nothing and writes nothing: a bad search costs one metadata read, which is why it
// is safe to call while somebody is typing.
func (a engineAdminAPI) searchIngest(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
	e := a.reg.get(strings.TrimSpace(r.PathValue("key")))
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no such engine"})
		return
	}
	// Deliberately NOT gated on the engine's mode. An engine switched off is a deployment
	// deciding not to pay for a GPU right now, which has nothing to do with whether an
	// administrator may look at what there is to stage for when it comes back.
	//
	// The engine's PROVIDER rides along because it decides the family vocabulary, and a
	// suggestion outside that vocabulary is one the ingest would refuse (engineFamilyGuess).
	a.answerSearch(w, r, engineIngestKindFor(e), e.def.Provider)
}

// browseSearch (POST /api/admin/engines/search) is the same read with no engine in the path.
//
// 🔴 It exists because the panel it lives on is EMPTY on a deployment that has not adopted
// 60-engines, and "there is nothing here" is the worst possible answer to "what could I run?".
// Nothing about asking Hugging Face what exists needs an engine: the CP holds no token, reads
// no bucket and starts no task (decision 6). Staging one still needs a role to stage it INTO,
// so this is browsing and the panel says so.
func (a engineAdminAPI) browseSearch(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
	// With no engine there is nothing to derive the kind from, so the caller states it. An
	// unknown one is not defaulted: quietly answering GGUFs to somebody who asked for
	// checkpoints is a list that looks like an answer.
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	switch kind {
	case "", "gguf":
		kind = "gguf"
	case "checkpoint":
	default:
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"unknown kind " + kind + " (gguf or checkpoint)"})
		return
	}
	// No engine, so no provider, so no family vocabulary to translate into: the browse list
	// says what the source says and suggests nothing. There is nothing to ingest into from
	// here anyway.
	a.answerSearch(w, r, kind, "")
}

// engineSearchReq is one search as both upstreams need it. A struct because the two functions
// take the same five things and a fifth positional string is where the kind and the sort start
// swapping places.
type engineSearchReq struct {
	q, kind, sort string
	// lora narrows the list to adapters. It is NOT a kind: a LoRA for the llm role is still a
	// GGUF and for the image role still a safetensors, so the file vocabulary is unchanged and
	// only the upstream filter moves.
	lora bool
	// provider is whose family vocabulary a suggestion has to be a member of. Empty for the
	// engine-less browse.
	provider string
}

// answerSearch is the body both routes share.
func (a engineAdminAPI) answerSearch(w http.ResponseWriter, r *http.Request, kind, provider string) {
	var b struct {
		Q      string `json:"q"`
		Source string `json:"source"`
		Sort   string `json:"sort"`
		Lora   bool   `json:"lora"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	// An empty q is allowed on purpose: with no words it is a ranking of what this engine can
	// load, which is the only way in for somebody who does not know what to type.
	req := engineSearchReq{
		q:        strings.TrimSpace(b.Q),
		kind:     kind,
		sort:     strings.TrimSpace(b.Sort),
		lora:     b.Lora,
		provider: provider,
	}
	var (
		hits []engineSearchHit
		aerr *apiError
	)
	switch strings.TrimSpace(b.Source) {
	case "civitai":
		hits, aerr = engineSearchCivitai(r.Context(), req)
	case "", "hf":
		hits, aerr = engineSearchHF(r.Context(), req)
	default:
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"source has to be hf or civitai"})
		return
	}
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, engineSearchAnswer{Hits: hits})
}

// engineSearchAnswer wraps the list. An object rather than a bare array so the answer has room
// to say something about itself later without every client changing shape.
type engineSearchAnswer struct {
	Hits []engineSearchHit `json:"hits"`
}

func engineBadSort(sort string) *apiError {
	return &apiError{http.StatusBadRequest, errCodeEngineBadBody,
		"unknown sort " + sort + " (downloads, trending or likes)"}
}
