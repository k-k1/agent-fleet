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

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
	Trending      int64  `json:"trending,omitempty"`
	Gated         bool   `json:"gated,omitempty"`
	License       string `json:"license,omitempty"`
	LicenseName   string `json:"license_name,omitempty"`
	BaseModel     string `json:"base_model,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
}

// engineHFSearchRow is one row of `GET /api/models`. 🔴 What is NOT here is the point: asking
// for `cardData` also delivers `extra_gated_prompt` and asking for `gguf` delivers the whole
// `chat_template`, each over a kilobyte on its own (measured 2026-09-09 on FLUX.1-dev and
// Qwen2.5-Coder-7B-Instruct-GGUF). Decoding into a narrow struct drops them before they can be
// copied onward — a screen that draws 20 rows must not carry 20 kilobytes nobody reads.
type engineHFSearchRow struct {
	ID            string `json:"id"`
	Downloads     int64  `json:"downloads"`
	Likes         int64  `json:"likes"`
	TrendingScore int64  `json:"trendingScore"`
	// Gated is `false`, `"auto"` or `"manual"` — a bool or a string on the same field, which is
	// why the existing engineHFGated is reused rather than a typed one written here.
	Gated        any    `json:"gated"`
	LastModified string `json:"lastModified"`
	CardData     struct {
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
		Name  string `json:"name"`
		Type  string `json:"type"`
		Stats struct {
			DownloadCount int64 `json:"downloadCount"`
			ThumbsUpCount int64 `json:"thumbsUpCount"`
		} `json:"stats"`
		// AllowCommercialUse is a list on this API ("Image", "Rent", "Sell"), and an empty one
		// is the non-commercial case decision 10 wants on screen.
		AllowCommercialUse []string `json:"allowCommercialUse"`
		ModelVersions      []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			BaseModel   string `json:"baseModel"`
			PublishedAt string `json:"publishedAt"`
		} `json:"modelVersions"`
	} `json:"items"`
}

// engineSearchFilter is the per-role narrowing, measured against the live API on 2026-09-09.
// A repository this engine cannot load is a dead end — `resolve` refuses it a moment later —
// and the file picker already refuses to show those, so the repository list must not either.
func engineSearchFilter(kind string) url.Values {
	v := url.Values{}
	if kind == "gguf" {
		// The library tag, which is what a repository of quantised files carries.
		v.Set("filter", "gguf")
		return v
	}
	v.Set("pipeline_tag", "text-to-image")
	return v
}

// engineSearchHF asks Hugging Face. An empty q is a RANKING rather than a mistake: the API
// answers the filter's top rows, which is the "what do people use" half of the picker.
func engineSearchHF(ctx context.Context, q, kind, sort string) ([]engineSearchHit, *apiError) {
	sortBy, ok := engineSortHF(sort)
	if !ok {
		return nil, engineBadSort(sort)
	}
	v := engineSearchFilter(kind)
	if q != "" {
		v.Set("search", q)
	}
	v.Set("sort", sortBy)
	v.Set("direction", "-1")
	v.Set("limit", strconv.Itoa(engineSearchLimit))
	// `expand[]` is what makes one read enough: without it the rows carry neither the gating
	// flag nor the licence, and the panel would have to resolve 20 repositories to draw a list.
	expand := []string{"gated", "downloads", "likes", "trendingScore", "cardData", "lastModified"}
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
			Gated:       engineHFGated(r.Gated),
			License:     engineFirstString(r.CardData.License),
			LicenseName: strings.TrimSpace(r.CardData.LicenseName),
			UpdatedAt:   strings.TrimSpace(r.LastModified),
			// The GGUF numbers are the repository's, i.e. one of its files — a draft for the
			// form, never the value. The resolve of the chosen FILE is what the row is built
			// from (decision 11).
			Bytes:         r.GGUF.Total,
			ContextLength: r.GGUF.ContextLength,
		})
	}
	return out, nil
}

// engineSearchCivitai asks Civitai. The hit carries the newest VERSION's id, because that is
// what an ingest takes — `civitai.com/models/<model>` and the version behind its download
// button are different numbers, and the model id is the one on the page's URL.
func engineSearchCivitai(ctx context.Context, q, kind, sort string) ([]engineSearchHit, *apiError) {
	sortBy, period, ok := engineSortCivitai(sort)
	if !ok {
		return nil, engineBadSort(sort)
	}
	if kind == "gguf" {
		// Civitai hosts image models. Offering it to the llm role would return checkpoints
		// llama.cpp cannot load, which is the dead end this filter exists to prevent.
		return []engineSearchHit{}, nil
	}
	v := url.Values{}
	if q != "" {
		v.Set("query", q)
	}
	v.Set("types", "Checkpoint")
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
		hit := engineSearchHit{
			Source: "civitai", Ref: strconv.Itoa(ver.ID), Name: name,
			Downloads: m.Stats.DownloadCount, Likes: m.Stats.ThumbsUpCount,
			BaseModel: strings.TrimSpace(ver.BaseModel),
			UpdatedAt: strings.TrimSpace(ver.PublishedAt),
		}
		// Civitai has no licence field in Hugging Face's sense (P4 measurement 2), so the one
		// thing it does say about terms is carried as itself: an empty allowCommercialUse is
		// the non-commercial case decision 10 wants visible BEFORE the acceptance.
		if len(m.AllowCommercialUse) == 0 {
			hit.LicenseName = "non-commercial"
		}
		out = append(out, hit)
	}
	return out, nil
}

// searchIngest (POST …/ingest/search) is the repository picker behind the ingest form.
//
// It starts nothing and writes nothing: a bad search costs one metadata read, which is why it
// is safe to call while somebody is typing.
func (a engineAdminAPI) searchIngest(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	e := a.reg.get(strings.TrimSpace(r.PathValue("key")))
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no such engine"})
		return
	}
	var b struct {
		Q      string `json:"q"`
		Source string `json:"source"`
		Sort   string `json:"sort"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	// An empty q is allowed on purpose: with no words it is a ranking of what this engine can
	// load, which is the only way in for somebody who does not know what to type.
	q := strings.TrimSpace(b.Q)
	sort := strings.TrimSpace(b.Sort)
	kind := engineIngestKindFor(e)
	var (
		hits []engineSearchHit
		aerr *apiError
	)
	switch strings.TrimSpace(b.Source) {
	case "civitai":
		hits, aerr = engineSearchCivitai(r.Context(), q, kind, sort)
	case "", "hf":
		hits, aerr = engineSearchHF(r.Context(), q, kind, sort)
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
