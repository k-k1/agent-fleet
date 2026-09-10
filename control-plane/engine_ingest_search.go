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
	Trending    float64 `json:"trending,omitempty"`
	Gated       bool    `json:"gated,omitempty"`
	License     string  `json:"license,omitempty"`
	LicenseName string  `json:"license_name,omitempty"`
	BaseModel   string  `json:"base_model,omitempty"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
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
		// AllowCommercialUse is a list on this API ("Image", "Rent", "Sell"), and an empty one
		// is the non-commercial case decision 10 wants on screen.
		AllowCommercialUse []string `json:"allowCommercialUse"`
		ModelVersions      []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			BaseModel string `json:"baseModel"`
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
			Gated:       engineHFGated(r.Gated),
			License:     engineFirstString(r.CardData.License),
			LicenseName: strings.TrimSpace(r.CardData.LicenseName),
			UpdatedAt:   strings.TrimSpace(r.LastModified),
			PublishedAt: strings.TrimSpace(r.CreatedAt),
			URL:         engineIngestBase + "/" + r.ID,
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
			// 🔴 `publishedAt` is the PUBLICATION date, and it used to ride as `updated_at` —
			// the one thing it is not. Civitai answers no `updatedAt` here at all (measured
			// 2026-09-11), so carrying it as both would print one date twice under two labels,
			// one of them wrong.
			UpdatedAt:   strings.TrimSpace(ver.UpdatedAt),
			PublishedAt: engineFirstNonEmpty(ver.PublishedAt, m.CreatedAt),
			URL:         engineCivitaiModelURL(m.ID, ver.ID),
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
	a.answerSearch(w, r, engineIngestKindFor(e))
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
	a.answerSearch(w, r, kind)
}

// answerSearch is the body both routes share.
func (a engineAdminAPI) answerSearch(w http.ResponseWriter, r *http.Request, kind string) {
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
