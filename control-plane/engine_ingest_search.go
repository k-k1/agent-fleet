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
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
	engineSortUpdated   = "updated"
	engineSortNewest    = "newest"
)

// Hugging Face's initial browse is recently updated. Its model list exposes lastModified as a
// sortable field; direction=-1 below makes the mapping explicit instead of depending on an
// upstream default that may differ between rankings.
func engineSortHF(sort string) (string, bool) {
	switch sort {
	case "", engineSortUpdated:
		return "lastModified", true
	case engineSortDownloads:
		return "downloads", true
	case engineSortTrending:
		return "trendingScore", true
	case engineSortLikes:
		return "likes", true
	}
	return "", false
}

// engineSortCivitai maps its explicit new-arrivals default and the three shared rankings.
// Civitai has no trending score, so "trending" is
// "most downloaded THIS MONTH" — the period is what makes it a different list (measured: it
// answers different models from the all-time one).
func engineSortCivitai(sort string) (value, period string, ok bool) {
	switch sort {
	case "", engineSortNewest:
		return "Newest", "", true
	case engineSortDownloads:
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
	// ModelRef is the stable repository/model identifier above the version selection. It is the
	// repository for Hugging Face and the numeric model id for Civitai.
	ModelRef string `json:"model_ref"`
	// Name is what a person reads. Same as Ref for HF, the model's title for Civitai; versions
	// have their own names in the selection modal and must not change the card identity.
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
	URL string `json:"url,omitempty"`
	// PreviewURL is the example image at lightbox size, ThumbURL the same one at card size. Two
	// URLs rather than one because the card draws a 92x108 box: measured 2026-09-15, the URL
	// Civitai publishes asks for the ORIGINAL, and a page of twenty is ~40 MB of PNG for boxes
	// that show 10 kpx each. On a phone those loads are what fails, and a failed <img> is the
	// broken-glyph placeholder the operator reported. ThumbURL is empty when the source offers
	// no resizing (Hugging Face), and the panel falls back to PreviewURL.
	PreviewURL    string `json:"preview_url,omitempty"`
	ThumbURL      string `json:"thumb_url,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
	// NsfwLevel is Civitai's own content-rating number (0 = safe, higher = more explicit),
	// carried on every Civitai hit regardless of which tab found it. Measured 2026-09-14: a
	// model rated well above what the plain "civitai" tab's default query returns can still
	// answer a nonzero level, so this is not exclusive to the civitai-red tab and must not be
	// drawn as if it were. Absent (0) for Hugging Face, which publishes no such rating.
	NsfwLevel int `json:"nsfw_level,omitempty"`
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
		Thumbnail   string `json:"thumbnail"`
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
		// NsfwLevel is the model's content-rating number, published on the list itself — see
		// engineSearchHit.NsfwLevel.
		NsfwLevel int `json:"nsfwLevel"`
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
			Images []struct {
				URL string `json:"url"`
			} `json:"images"`
			// The version's own dates. 🔴 Measured live 2026-09-11 on `/api/v1/models`: a
			// version answers `publishedAt` and nothing else — no `updatedAt`, no `createdAt`.
			// Both are still decoded, because a version fetched by id does carry more and this
			// struct must not silently drop a date that appears.
			PublishedAt string `json:"publishedAt"`
			UpdatedAt   string `json:"updatedAt"`
		} `json:"modelVersions"`
	} `json:"items"`
	Metadata struct {
		NextCursor json.RawMessage `json:"nextCursor"`
	} `json:"metadata"`
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
// 🔴 The image kind needs TWO, and one of them is the only way to the files this engine loads.
// `pipeline_tag=text-to-image` is a diffusers-era tag: a repository publishing loose safetensors
// for ComfyUI does not carry it, and Hugging Face has no OR — so with that filter alone the
// picker HID exactly the repositories the comfy provider needs. Measured 2026-09-15:
//
//	search=Anima    → circlestone-labs/Anima is #1 unfiltered and ABSENT under the pipeline tag
//	search=Krea-2   → Comfy-Org/Krea-2 is #1 unfiltered and ABSENT under it, while the two rows
//	                  the filter DOES return (krea/Krea-2-Raw, krea/Krea-2-Turbo) are gated
//
// So it steered an operator to a 401 and hid the ungated repackage. `filter=diffusion-single-file`
// is the other half: the library tag Comfy-Org's repositories carry (with `comfyui`), and on its
// own it ranks Comfy-Org/z_image_turbo, Comfy-Org/Krea-2, Comfy-Org/stable-diffusion-v1-5-archive
// — this deployment's own shape of model. Neither filter is a superset of the other: stock SDXL
// is diffusers with a top-level single file and appears only under the first.
//
// ⚠️ `filter=lora` is still never sent alone (measured 2026-09-12: alone, and with `filter=gguf`,
// it drops the connection with no status line). It rides on the pipeline tag in one lane and on
// the library tag in the other, and `filter=diffusion-single-file&filter=lora` answers 200.
func engineSearchFilters(kind string, lora bool) []url.Values {
	if kind == "gguf" {
		v := url.Values{}
		// The library tag, which is what a repository of quantised files carries.
		v.Set("filter", "gguf")
		if lora {
			v.Add("filter", "lora")
			v.Set("pipeline_tag", "text-generation")
		}
		return []url.Values{v}
	}
	diffusers := url.Values{}
	diffusers.Set("pipeline_tag", "text-to-image")
	singleFile := url.Values{}
	singleFile.Set("filter", "diffusion-single-file")
	if lora {
		diffusers.Set("filter", "lora")
		singleFile.Add("filter", "lora")
	}
	return []url.Values{diffusers, singleFile}
}

// engineSearchHF asks Hugging Face. An empty q is a RANKING rather than a mistake: the API
// answers the filter's top rows, which is the "what do people use" half of the picker.
func engineSearchHF(ctx context.Context, req engineSearchReq) ([]engineSearchHit, *apiError) {
	hits, _, aerr := engineSearchHFPage(ctx, req)
	return hits, aerr
}

// engineSearchLaneSep joins one per-lane cursor to the next. Hugging Face's cursor is base64
// (`[A-Za-z0-9_=-]`), so a character outside that alphabet splits them back apart unambiguously;
// a lane that has run out contributes an empty string and keeps its position.
//
// 🔴 Not `|`: that is what Civitai's own cursor is built from ("<timestamp>|<id>"), and the two
// kinds of cursor travel the same wire field. Sharing the character would make a cursor pasted
// or logged from the wrong source split into plausible-looking halves instead of failing.
const engineSearchLaneSep = "~"

func engineSearchHFPage(ctx context.Context, req engineSearchReq) ([]engineSearchHit, string, *apiError) {
	if !engineCursorValid(req.cursor) {
		return nil, "", engineBadCursor()
	}
	lanes := engineSearchFilters(req.kind, req.lora)
	cursors := strings.Split(req.cursor, engineSearchLaneSep)
	// One request per lane, and the page each asks for is the answer's size divided between
	// them: a lane's own next-cursor then points exactly past what was shown, so paging needs no
	// per-lane offset to remember. A run-out lane is skipped rather than re-asked from the top.
	perLane := engineSearchLimit / len(lanes)
	var (
		merged []engineSearchHit
		next   = make([]string, len(lanes))
		seen   = make(map[string]bool, engineSearchLimit)
		sortBy string
		asked  int
	)
	for i, lane := range lanes {
		cursor := ""
		if i < len(cursors) {
			cursor = cursors[i]
		}
		if req.cursor != "" && cursor == "" {
			continue // this lane answered everything it had on an earlier page
		}
		hits, cur, key, aerr := engineSearchHFLane(ctx, req, lane, cursor, perLane)
		if aerr != nil {
			return nil, "", aerr
		}
		sortBy = key
		next[i] = cur
		asked++
		for _, h := range hits {
			if seen[h.Ref] {
				continue // a repository carrying both tags is one row, not two
			}
			seen[h.Ref] = true
			merged = append(merged, h)
		}
	}
	// 🔴 Only when two lanes actually answered. One lane's page is the UPSTREAM's ranking, tie
	// breaks included, and re-sorting it here by the one field this end can see would reorder
	// rows Hugging Face had already separated by something finer.
	if asked > 1 {
		engineSortHits(merged, sortBy)
	}
	if strings.Trim(strings.Join(next, engineSearchLaneSep), engineSearchLaneSep) == "" {
		return merged, "", nil
	}
	return merged, strings.Join(next, engineSearchLaneSep), nil
}

// engineSearchHFLane is one filter's page: the request this function has always made, now with
// the filter and the page size handed to it.
func engineSearchHFLane(ctx context.Context, req engineSearchReq, v url.Values,
	cursor string, limit int) ([]engineSearchHit, string, string, *apiError) {
	q, kind := req.q, req.kind
	sortBy, ok := engineSortHF(req.sort)
	if !ok {
		return nil, "", "", engineBadSort(req.sort)
	}
	if q != "" {
		v.Set("search", q)
	}
	v.Set("sort", sortBy)
	v.Set("direction", "-1")
	v.Set("limit", strconv.Itoa(limit))
	if cursor != "" {
		v.Set("cursor", cursor)
	}
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
	target := engineIngestBase + "/api/models?" + v.Encode()
	head, aerr := engineSearchGetJSON(ctx, target, &rows)
	if aerr != nil {
		return nil, "", "", aerr
	}
	out := make([]engineSearchHit, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		out = append(out, engineSearchHit{
			Source: "hf", Ref: r.ID, ModelRef: r.ID, Name: r.ID,
			Downloads: r.Downloads, Likes: r.Likes, Trending: r.TrendingScore,
			Gated:        engineHFGated(r.Gated),
			GatedKind:    engineHFGatedKind(r.Gated),
			Restrictions: engineHFRestrictions(r.Gated),
			License:      engineFirstString(r.CardData.License),
			LicenseName:  strings.TrimSpace(r.CardData.LicenseName),
			UpdatedAt:    strings.TrimSpace(r.LastModified),
			PublishedAt:  strings.TrimSpace(r.CreatedAt),
			URL:          engineIngestBase + "/" + r.ID,
			PreviewURL:   engineSafeHTTPURL(r.CardData.Thumbnail),
			// The GGUF numbers are the repository's, i.e. one of its files — a draft for the
			// form, never the value. The resolve of the chosen FILE is what the row is built
			// from (decision 11).
			Bytes:         r.GGUF.Total,
			ContextLength: r.GGUF.ContextLength,
		})
	}
	return out, engineHFNextCursor(head), sortBy, nil
}

// engineSortHits puts a merged page back into ONE ranking. Each lane arrives sorted by the same
// key, so this is what keeps "the interesting thing is at the top" true of the union rather than
// of each half — without it a page would read as two lists stapled together, and the row an
// operator wants could sit at position 11 behind a lane that had nothing better to offer.
//
// Stable, so that rows the key cannot separate (two repositories with no downloads yet) keep the
// order the upstream gave them.
func engineSortHits(hits []engineSearchHit, sortBy string) {
	key := func(h engineSearchHit) (float64, string) {
		switch sortBy {
		case "downloads":
			return float64(h.Downloads), ""
		case "likes":
			return float64(h.Likes), ""
		case "trendingScore":
			return h.Trending, ""
		default: // lastModified — RFC 3339, so lexical order is chronological
			return 0, h.UpdatedAt
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		ni, si := key(hits[i])
		nj, sj := key(hits[j])
		if si != "" || sj != "" {
			return si > sj
		}
		return ni > nj
	})
}

// engineCivitaiModelURL is the page a person opens for a hit: the MODEL's page, pointed at the
// VERSION the hit is for, on whichever Civitai host answered the search that found it.
//
// Both ids are needed and they are different numbers — `/models/<model>` alone opens on
// whatever version is newest today, which is not the one this row's `ref` would take in. A
// model with no id falls back to the version-only form the licence link already uses, which
// Civitai resolves.
func engineCivitaiModelURL(host string, modelID, versionID int) string {
	if modelID <= 0 {
		return host + "/models/?modelVersionId=" + strconv.Itoa(versionID)
	}
	return host + "/models/" + strconv.Itoa(modelID) + "?modelVersionId=" + strconv.Itoa(versionID)
}

// engineSearchCivitai asks civitai.com. The hit carries the newest VERSION's id, because that
// is what an ingest takes — `civitai.com/models/<model>` and the version behind its download
// button are different numbers, and the model id is the one on the page's URL.
func engineSearchCivitai(ctx context.Context, req engineSearchReq) ([]engineSearchHit, *apiError) {
	hits, _, aerr := engineSearchCivitaiPage(ctx, req, false)
	return hits, aerr
}

// engineSearchCivitaiRed is the same search with `nsfw=true` against civitai.red — see
// engineCivitaiRedBase. Measured 2026-09-14: without the parameter, NEITHER host returns
// NSFW-flagged models, so this is the one place that parameter is set.
func engineSearchCivitaiRed(ctx context.Context, req engineSearchReq) ([]engineSearchHit, *apiError) {
	hits, _, aerr := engineSearchCivitaiPage(ctx, req, true)
	return hits, aerr
}

func engineSearchCivitaiPage(ctx context.Context, req engineSearchReq, nsfw bool) ([]engineSearchHit, string, *apiError) {
	sortBy, period, ok := engineSortCivitai(req.sort)
	if !ok {
		return nil, "", engineBadSort(req.sort)
	}
	if !engineCursorValid(req.cursor) {
		return nil, "", engineBadCursor()
	}
	if req.kind == "gguf" {
		// Civitai hosts image models. Offering it to the llm role would return checkpoints
		// llama.cpp cannot load, which is the dead end this filter exists to prevent.
		return nil, "", &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"Civitai cannot be used with an LLM engine"}
	}
	host := engineCivitaiBase
	if nsfw {
		host = engineCivitaiRedBase
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
	if nsfw {
		v.Set("nsfw", "true")
	}
	v.Set("limit", strconv.Itoa(engineSearchLimit))
	if req.cursor != "" {
		v.Set("cursor", req.cursor)
	}
	var doc engineCivitaiSearchDoc
	if _, aerr := engineSearchGetJSON(ctx, host+"/api/v1/models?"+v.Encode(), &doc); aerr != nil {
		return nil, "", aerr
	}
	out := make([]engineSearchHit, 0, len(doc.Items))
	// The URL each hit's login probe HEADs, at the same index. Kept beside the list rather than
	// on the hit: it is a CDN link with a signature on it, and nothing in the panel may follow
	// it — the whole point of the probe is that the bytes are the ingest task's business.
	probe := make([]string, 0, len(doc.Items))
	for _, m := range doc.Items {
		if m.ID <= 0 || len(m.ModelVersions) == 0 {
			// Nothing selectable: versionless models have no file, and an id-less model cannot
			// be expanded into the versions endpoint without guessing its identity.
			continue
		}
		ver := m.ModelVersions[0]
		name := strings.TrimSpace(m.Name)
		files := make([]engineCivitaiFileFacts, 0, len(ver.Files))
		for _, f := range ver.Files {
			// The scan verdicts of the file this row would actually take in. A preview image's
			// verdict is not this model's, and the ingest picks the `Model` file.
			if strings.EqualFold(f.Type, "Model") || f.Primary {
				files = append(files, f.engineCivitaiFileFacts)
			}
		}
		hit := engineSearchHit{
			Source: "civitai", Ref: strconv.Itoa(ver.ID), ModelRef: strconv.Itoa(m.ID), Name: name,
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
			URL:         engineCivitaiModelURL(host, m.ID, ver.ID),
			// Carried whichever tab this came from — a rating well above zero shows up under
			// the plain "civitai" tab's own default query too (measured), so it is never safe
			// to assume zero just because this hit is not from the nsfw=true tab.
			NsfwLevel: m.NsfwLevel,
		}
		for _, image := range ver.Images {
			if preview := engineSafeCivitaiPreviewURL(image.URL); preview != "" {
				hit.PreviewURL = engineCivitaiImageVariant(preview, engineCivitaiPreviewTransform)
				hit.ThumbURL = engineCivitaiImageVariant(preview, engineCivitaiThumbTransform)
				break
			}
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
	return out, engineCursorFromJSON(doc.Metadata.NextCursor), nil
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

const engineCursorMax = 4096

// engineCursorValid keeps an upstream cursor opaque while bounding what can be reflected into
// a request. A cursor is only ever added to a fixed upstream URL; it is never followed as one.
func engineCursorValid(cursor string) bool {
	if len(cursor) > engineCursorMax || !utf8.ValidString(cursor) {
		return false
	}
	for _, r := range cursor {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func engineBadCursor() *apiError {
	return &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid search cursor"}
}

// engineCursorFromJSON accepts the string used by the current Civitai API and the numeric shape
// older responses documented. json.Number preserves a large cursor without float rounding.
func engineCursorFromJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && engineCursorValid(s) {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		s = n.String()
		if engineCursorValid(s) {
			return s
		}
	}
	return ""
}

// engineHFNextCursor extracts only the opaque cursor from Hugging Face's RFC 8288-style next
// link. The link itself is never requested, and a different origin/path is ignored.
func engineHFNextCursor(header http.Header) string {
	base, err := url.Parse(engineIngestBase)
	if err != nil {
		return ""
	}
	for _, value := range header.Values("Link") {
		for _, part := range strings.Split(value, ",") {
			pieces := strings.Split(part, ";")
			if len(pieces) < 2 {
				continue
			}
			next := false
			for _, p := range pieces[1:] {
				if strings.TrimSpace(p) == `rel="next"` {
					next = true
					break
				}
			}
			raw := strings.TrimSpace(pieces[0])
			if !next || len(raw) < 3 || raw[0] != '<' || raw[len(raw)-1] != '>' {
				continue
			}
			u, err := url.Parse(raw[1 : len(raw)-1])
			if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.Path != "/api/models" {
				continue
			}
			cursor := u.Query().Get("cursor")
			if cursor != "" && engineCursorValid(cursor) {
				return cursor
			}
		}
	}
	return ""
}

func engineSafeHTTPURL(raw string) string {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return s
}

func engineSafeCivitaiPreviewURL(raw string) string {
	s := engineSafeHTTPURL(raw)
	if s == "" {
		return ""
	}
	u, _ := url.Parse(s)
	host := strings.ToLower(u.Hostname())
	base, _ := url.Parse(engineCivitaiBase)
	baseHost := strings.ToLower(base.Hostname())
	if host != baseHost && host != "civitai.com" && !strings.HasSuffix(host, ".civitai.com") {
		return ""
	}
	return s
}

// The two sizes asked of Civitai's image CDN, in its own path-segment vocabulary.
//
// `anim=false` is not decoration: an example can be a VIDEO, and the API's `images` list says so
// only in a `type` field beside the URL. An .mp4 in an <img> is a broken glyph and nothing else,
// so the still frame is asked for rather than the asset — one rule instead of a second code path
// that would have to drop those rows. Measured 2026-09-15: `anim=false,width=256` answers
// image/jpeg for a video example, and turns a 1.3 MB PNG into 69 kB.
const (
	engineCivitaiPreviewTransform = "anim=false,width=1024"
	engineCivitaiThumbTransform   = "anim=false,width=256"
)

// engineCivitaiImageVariant re-sizes a Civitai image URL by rewriting the transform segment
// (`.../<bucket>/<uuid>/original=true/<id>.jpeg`). The segment is recognised by its `=`, and
// inserted before the filename when the URL carries none — measured 2026-09-15, both forms
// answer 200. An unexpected shape is returned untouched: a wrong URL shows nothing at all,
// where the original merely shows something too large.
func engineCivitaiImageVariant(raw, transform string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parts := strings.Split(u.Path, "/")
	for i, part := range parts {
		if strings.Contains(part, "=") {
			parts[i] = transform
			u.Path = strings.Join(parts, "/")
			return u.String()
		}
	}
	// `["", bucket, uuid, file]` is the shortest path worth rewriting; anything flatter is not
	// the CDN layout this knows how to address.
	if len(parts) < 4 {
		return raw
	}
	u.Path = strings.Join(append(append([]string{}, parts[:len(parts)-1]...), transform, parts[len(parts)-1]), "/")
	return u.String()
}

// engineSearchGetJSONRetries bounds how many times a single search request retries a 503.
// Measured on af-sandbox (2026-09-14): Civitai's search endpoint sheds load with a 503 that
// clears within a second, and until now that failed the whole search on the first bad tick —
// the operator's only recourse was to press search again by hand. This is safe to retry where
// engineIngestGetJSON deliberately is NOT (see its comment): one search is one request per
// keystroke, not one of many probes in an unattended batch.
const engineSearchGetJSONRetries = 3

// engineSearchGetJSON is the search-only variant that retains response headers for Hugging
// Face pagination and retries a transient 503. It applies the same bounded body and error
// mapping as metadata resolution (engineIngestGetJSON) otherwise.
func engineSearchGetJSON(ctx context.Context, target string, out any) (http.Header, *apiError) {
	var lastErr *apiError
	for attempt := 1; attempt <= engineSearchGetJSONRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource, err.Error()}
		}
		resp, err := engineIngestHTTP.Do(req)
		if err != nil {
			return nil, &apiError{http.StatusBadGateway, errCodeIngestSourceUnreach,
				"could not reach " + engineIngestHost(target) + ": " + err.Error()}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, &apiError{http.StatusBadGateway, errCodeIngestSourceForbid,
				engineIngestHost(target) + " refused the lookup (" + resp.Status + ")"}
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = &apiError{http.StatusBadGateway, errCodeIngestSourceError,
				engineIngestHost(target) + " answered " + resp.Status}
			if resp.StatusCode != http.StatusServiceUnavailable || attempt == engineSearchGetJSONRetries {
				return nil, lastErr
			}
			select {
			case <-ctx.Done():
				return nil, lastErr
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			log.Printf("engines: unreadable answer from %s: %v", target, err)
			return nil, &apiError{http.StatusBadGateway, errCodeIngestSourceError,
				"unreadable answer from " + engineIngestHost(target)}
		}
		return resp.Header, nil
	}
	return nil, lastErr
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
	q, kind, sort, cursor string
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
		Cursor string `json:"cursor"`
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
		cursor:   b.Cursor,
		lora:     b.Lora,
		provider: provider,
	}
	var (
		hits       []engineSearchHit
		nextCursor string
		aerr       *apiError
	)
	switch strings.TrimSpace(b.Source) {
	case "civitai":
		hits, nextCursor, aerr = engineSearchCivitaiPage(r.Context(), req, false)
	case "civitai-red":
		hits, nextCursor, aerr = engineSearchCivitaiPage(r.Context(), req, true)
	case "", "hf":
		hits, nextCursor, aerr = engineSearchHFPage(r.Context(), req)
	default:
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
			"source has to be hf, civitai or civitai-red"})
		return
	}
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, engineSearchAnswer{Hits: hits, NextCursor: nextCursor})
}

// engineSearchAnswer wraps the list. An object rather than a bare array so the answer has room
// to say something about itself later without every client changing shape.
type engineSearchAnswer struct {
	Hits       []engineSearchHit `json:"hits"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func engineBadSort(sort string) *apiError {
	return &apiError{http.StatusBadRequest, errCodeEngineBadBody,
		"unknown sort " + sort + " (downloads, trending, likes, updated or newest)"}
}
