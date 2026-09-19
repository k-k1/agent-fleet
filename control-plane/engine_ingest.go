package main

// engine_ingest.go — taking a model into the bucket from the Console (ADR 0072 decision 6,
// phase P4).
//
// Until now a model got into a deployment by an operator assembling an `aws ecs run-task` by
// hand: a URL, a sha256 copied out of the Hugging Face API, a bucket key, a subnet list. Every
// one of those is a place to make a silent mistake, and the sha256 — the only thing that says
// the bytes are the bytes — is the easiest to leave out.
//
// So the Control Plane resolves the source and starts the task. Three things decide the shape:
//
//   - **the CP never holds the Hugging Face token.** Measured (2026-09-09): a GATED repository
//     answers `api/models/<repo>?blobs=true` ANONYMOUSLY with its licence, its gating flag and
//     every file's sha256 and size; only the file download is 401. So the CP can resolve
//     everything and the token stays where ADR 0072 decision 6 put it — in the ingest task.
//   - **the CP never writes S3.** The task uploads and deletes; the CP has read-only HeadObject
//     access to the two model prefixes so it can distinguish a saved object from a stale job.
//   - **a job outlives the request.** It is a row (store_engine_ingest.go), reconciled against
//     ECS, because a download runs for minutes and a CP can be replaced inside one.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The APIs' base URLs. Variables rather than constants so a test can answer them locally: what
// this file does with a gated repository's metadata is exactly what has to be pinned, and
// reaching the real Hugging Face from a unit test would pin nothing and fail offline.
//
// engineCivitaiRedBase is civitai.red, the sister domain Civitai split off 2026-04-15 for NSFW
// browsing (same account, database and model/version ids as civitai.com — "Two Front Doors").
// Measured 2026-09-14: the anonymous `/api/v1/models` search omits NSFW-flagged results on
// EITHER host unless the request also carries `nsfw=true` — the domain itself gates nothing.
// So this only exists to point the search UI's "include NSFW" tab at the host Civitai's own
// site now treats as canonical for that content; resolve/ingest/download stay on
// engineCivitaiBase, which already answers a chosen NSFW version's metadata and file
// identically to civitai.red (same measurement).
var (
	engineIngestBase     = "https://huggingface.co"
	engineCivitaiBase    = "https://civitai.com"
	engineCivitaiRedBase = "https://civitai.red"
)

// engineIngestHTTP talks to Hugging Face and Civitai. A real timeout because these are metadata
// calls on the admin path — the DOWNLOAD is the task's job and takes minutes, this takes
// milliseconds or it is broken.
var engineIngestHTTP = &http.Client{Timeout: 20 * time.Second}

// engineIngestSource is where a file comes from, exactly one of the three.
type engineIngestSource struct {
	HF      *engineIngestHF      `json:"hf"`
	Civitai *engineIngestCivitai `json:"civitai"`
	// URL is the escape hatch for a file neither API describes. sha256 is REQUIRED with it: a
	// download nobody can verify is the one thing this route exists to stop being normal.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type engineIngestHF struct {
	Repo     string `json:"repo"`
	File     string `json:"file"`
	Revision string `json:"revision"`
}

type engineIngestCivitai struct {
	VersionID int    `json:"versionId"`
	File      string `json:"file"`
}

// engineResolved is what the source turned out to be. Everything here is written into the
// catalogue row when the task succeeds, so the row says where its bytes came from without
// anybody retyping it.
type engineResolved struct {
	DownloadURL string
	SHA256      string
	Bytes       int64
	License     string
	LicenseName string
	LicenseURL  string
	BaseModel   string
	Gated       bool
	// LoginRequired is Civitai's answer to "may anybody download this", and it is a DIFFERENT
	// fact from Gated: gating is a repository's terms, which an operator's token satisfies,
	// while this is a per-uploader switch with no token to satisfy it here at all (ADR 0072 P2
	// 欠落 5). Measured on af-sandbox: five assets split 200 / 401 / 403, and the metadata call
	// that says everything else about them answers 200 for all five.
	LoginRequired bool
	// Restrictions is the same closed vocabulary the search list uses (engineRestrict*). The
	// resolve sees strictly more than the list does — the per-version document carries
	// `usageControl`, which `/api/v1/models` does not — so a row can pick up a mark here that
	// the card it was chosen from could not show.
	Restrictions []string
	// TrainedWords is a LoRA's trigger. Without it an adapter loads, changes nothing visible,
	// and looks like a broken ingest.
	TrainedWords []string
	// BaseModelSuggest is BaseModel translated into the provider's family vocabulary, or "".
	// A suggestion for the form — ADR 0072 decision 2 keeps the declaration with the operator.
	BaseModelSuggest string
	// What the publisher calls this, which version of it this is, and one example image at the
	// card and lightbox sizes (ADR 0088). Stored on the row, unlike ParamsHint above: the id is
	// derived from a FILE name and is therefore not a name anybody chose, so without these the
	// catalogue can only draw a key.
	//
	// 🔴 They cost no upstream read of their own. Civitai answers all four in the version
	// document this already decodes for the sha256 and the licence (measured live 2026-09-18:
	// `model.name`, `name` and ten `images[]`), and Hugging Face's `cardData.thumbnail` rides on
	// the model document the resolve has already fetched.
	DisplayName, VersionName string
	PreviewURL, ThumbURL     string
	// ParamsHint is what the author's own description says about how to run this, read out of
	// prose (engine_params_hint.go). Offered to the form, never stored from here.
	ParamsHint engineParamsHint
	Source     string // what a person reads in the job list
	// ArtifactIdentity is the immutable machine answer to "are these the same bytes from the
	// same selected source". It is persisted per S3 object and compared verbatim on reuse;
	// Source remains the backwards-compatible human label and is never promoted into this.
	ArtifactIdentity string
	// The model's OWN maximum, straight off the GGUF header Hugging Face has already parsed
	// (`gguf.context_length` on the same call this reads everything else from). 🔴 It is a
	// suggestion, never the value: unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF says 262144 and
	// this deployment runs it at 32768, because what the architecture allows and what fits in
	// an L4 are different questions. The panel offers it and says whose number it is.
	ContextLength int
}

// engineCandidate is one file a repository offers. The list exists because a filename is
// something a person retypes from another window, and 🔴 a name one letter short (measured
// 2026-09-09: `flux1-dev.safetensor`) is indistinguishable from a name that is simply not there.
// Everything here comes from the same answer the resolve reads.
type engineCandidate struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// Role says whether this file is a MODEL somebody can take in on its own, or one of the
	// three things a quantisation repository keeps beside its models (ADR 0089). Measured on
	// unsloth/Qwen3.8-27B-GGUF, 2026-09-18: thirty `.gguf` files, of which one is an importance
	// matrix, two are vision projectors and one is the second half of a split — so a plain list
	// of "every .gguf" buries the fourteen quantisations that are the point of the repository.
	//
	// 🔴 Classified, never filtered. This route is also the manual file picker, where hiding a
	// file is how somebody ends up comparing two strings across two windows (the fault the list
	// exists to fix). The panel folds what it does not need and can always open it again.
	Role string `json:"role,omitempty"`
}

// The three values engineCandidate.Role takes. Read off the NAME, which is a convention and not
// a header fact — the cost of being wrong is one row folded into the wrong group on a screen that
// can unfold it, and the alternative is one ranged read per file of a thirty-file repository.
//
// Shards are not among them: engineIngestWanted drops every `-00002-of-00003` before this is
// reached, and has since ADR 0072, because taking one in downloads gigabytes and builds a row
// nothing can load.
const (
	engineCandidateModel     = "model"
	engineCandidateProjector = "projector"
	engineCandidateImatrix   = "imatrix"
)

func engineCandidateRole(name string) string {
	base := strings.ToLower(engineBaseName(name))
	switch {
	case strings.HasPrefix(base, "imatrix"):
		return engineCandidateImatrix
	case strings.HasPrefix(base, "mmproj"):
		return engineCandidateProjector
	}
	return engineCandidateModel
}

// engineIngestExts says which files are worth offering for a role. A repository holds READMEs,
// configs and preview images too, and a list that includes them buries the two or three lines
// that are actually the model.
func engineIngestExts(kind string) []string {
	if kind == "gguf" {
		return []string{".gguf"}
	}
	return []string{".safetensors", ".ckpt", ".pt", ".sft"}
}

func engineIngestWanted(name string, exts []string) bool {
	l := strings.ToLower(name)
	if engineIngestShard(l) {
		return false
	}
	for _, e := range exts {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// engineIngestShard spots one piece of a file split across several
// (`…-00001-of-00003.safetensors`, Hugging Face's convention for a repository stored in the
// diffusers layout). 🔴 Measured 2026-09-09 on black-forest-labs/FLUX.1-dev: nine .safetensors,
// five of them shards. A single shard is not a model — taking one in downloads gigabytes and
// produces a catalogue row nothing can load, which is the dead end this list exists to avoid.
// Whole multi-part staging is its own job (ADR 0072 open questions), not one entry in a picker.
var engineShardRe = regexp.MustCompile(`-\d{5}-of-\d{5}\.[a-z]+$`)

func engineIngestShard(lower string) bool { return engineShardRe.MatchString(lower) }

// engineIngestResolve turns a source into something the task can be told to fetch.
func engineIngestResolve(ctx context.Context, src engineIngestSource) (engineResolved, *apiError) {
	switch {
	case src.HF != nil:
		return engineResolveHF(ctx, *src.HF)
	case src.Civitai != nil:
		return engineResolveCivitai(ctx, *src.Civitai)
	case strings.TrimSpace(src.URL) != "":
		u := strings.TrimSpace(src.URL)
		if !strings.HasPrefix(u, "https://") {
			return engineResolved{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "the url must be https"}
		}
		// ⚠️ Not optional here, unlike on the two APIs that publish one. Nothing else in this
		// path can tell a truncated download from a complete one.
		if len(strings.TrimSpace(src.SHA256)) != 64 {
			return engineResolved{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
				"a plain url needs its sha256 (64 hex characters) — nothing else can verify the download"}
		}
		hash := strings.ToLower(strings.TrimSpace(src.SHA256))
		return engineResolved{
			DownloadURL: u, SHA256: hash, Source: u,
			ArtifactIdentity: "url:" + u + "#sha256:" + hash,
		}, nil
	}
	return engineResolved{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "one of hf, civitai or url is required"}
}

func engineHFArtifactIdentity(repo, file, revision, sha256 string) string {
	return "hf:" + repo + "@" + revision + "/" + file + "#sha256:" + strings.ToLower(sha256)
}

func engineCivitaiArtifactIdentity(versionID int, file, sha256 string) string {
	return "civitai:" + strconv.Itoa(versionID) + "/" + file + "#sha256:" + strings.ToLower(sha256)
}

// engineSourceURL turns a recorded source back into the page a person can open, and is the
// inverse of the two `Source:` lines above. Empty when it cannot be composed, which the panel
// draws as plain text — a broken link in an operator's console is worse than a string.
//
// It is composed HERE and not in the panel for the same reason a search hit's `url` is (see
// engineCivitaiModelURL): the two vendors spell it differently, and Civitai's needs a fact the
// source string does not look like it carries.
//
// 🔴 `civitai:<id>` is a model VERSION id, NOT the model id in the page's URL. The two are
// different numbers, so `civitai.com/models/<id>` opens A DIFFERENT MODEL. The version-only form
// is the one Civitai resolves, and it is already what this file hands to LicenseURL.
//
// 🔴 A plain URL is deliberately NOT linked. `url:` sources are the direct download of the
// weights — 22 GB in ADR 0072's table — and a text link in a panel that says "where this came
// from" must not be a click that starts one.
//
// ⚠️ Hugging Face keeps no revision in the source string, so the file link is `main`: if the
// repository moved the file, a 404 is the honest answer and the repository root is one click up.
// A two-segment source is left unlinked rather than guessed at — `hf:gpt2/model.gguf` is
// indistinguishable from a legacy single-segment repository whose file happens to be named like
// a repository, and the row cannot tell which it is.
func engineSourceURL(source string) string {
	s := strings.TrimSpace(source)
	switch {
	case strings.HasPrefix(s, "hf:"):
		parts := strings.SplitN(strings.TrimPrefix(s, "hf:"), "/", 3)
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return ""
		}
		return engineIngestBase + "/" + parts[0] + "/" + parts[1] + "/blob/main/" + parts[2]
	case strings.HasPrefix(s, "civitai:"):
		id := strings.TrimPrefix(s, "civitai:")
		if _, err := strconv.Atoi(id); err != nil {
			return ""
		}
		// 🔴 `/model-versions/<id>` and NOT `/models/?modelVersionId=<id>`. A recorded source
		// carries the VERSION id and nothing else — the model id and its slug are a different
		// number and a different string — and the query form answers 200 with the model LIST,
		// which reads as a working link that goes to the wrong page (reported from the panel,
		// 2026-09-15). Measured the same day: `/model-versions/5038` answers 308 to
		// `/models/4451?modelVersionId=5038`, which 307s on to the slug. The redirect knows the
		// two facts this end does not, so it is the link to hand out.
		return engineCivitaiBase + "/model-versions/" + id
	}
	return ""
}

// engineHFDoc is the part of a model's API answer this file reads. One call serves both the
// listing and the resolve, so a person who picks a file from the list is choosing from the same
// answer the sha256 and the licence are then taken out of — the alternative is two reads that
// can disagree across a push to the repository.
type engineHFDoc struct {
	// SHA is the repository commit the requested revision resolved to. `main` is mutable, so the
	// literal request spelling cannot be the identity of bytes saved for later reuse.
	SHA      string `json:"sha"`
	Gated    any    `json:"gated"` // false, or "auto" / "manual" — a string is still gated
	CardData struct {
		License     any    `json:"license"` // a string, or a list on some cards
		LicenseName string `json:"license_name"`
		LicenseLink string `json:"license_link"`
		// What this repository was built from — a quantisation names the model it quantised, a
		// fine-tune the checkpoint it started from. Same shape problem as License: a string on
		// most cards and a list on some. It is read for ONE purpose, suggesting the family in
		// the ingest form, and is never stored as the family itself (decision 2).
		BaseModel any `json:"base_model"`
		// The picture the author put on the model card, when there is one (ADR 0088). The same
		// field the search list reads, on a document this route has already fetched. Hugging
		// Face offers no resizing, so it is the one size there is — the panel falls back to the
		// preview wherever it would draw a thumbnail.
		Thumbnail string `json:"thumbnail"`
	} `json:"cardData"`
	// Hugging Face parses the GGUF header itself and publishes the result here. Absent for
	// every repository that holds no GGUF, which is why the field is optional everywhere it
	// is read rather than a reason to fail.
	GGUF struct {
		ContextLength int    `json:"context_length"`
		Architecture  string `json:"architecture"`
	} `json:"gguf"`
	Siblings []struct {
		Name string `json:"rfilename"`
		Size int64  `json:"size"`
		LFS  struct {
			SHA256 string `json:"sha256"`
		} `json:"lfs"`
	} `json:"siblings"`
}

// engineReadHF fetches a model's metadata, returning the revision it settled on so callers can
// build a download URL against the same one.
func engineReadHF(ctx context.Context, repo, revision string) (engineHFDoc, string, *apiError) {
	rev := strings.TrimSpace(revision)
	if rev == "" {
		rev = "main"
	}
	api := engineIngestBase + "/api/models/" + repo + "?blobs=true"
	if rev != "main" {
		api = engineIngestBase + "/api/models/" + repo + "/revision/" + url.PathEscape(rev) + "?blobs=true"
	}
	var doc engineHFDoc
	if aerr := engineIngestGetJSON(ctx, api, &doc); aerr != nil {
		return engineHFDoc{}, rev, aerr
	}
	return doc, rev, nil
}

// engineResolveHF reads the model card and the file's LFS metadata.
//
// The licence is taken as TWO fields on purpose (ADR 0072 decision 10, review R8): Hugging Face
// answers `license: "other"` for both non-commercial models in the ADR's table and puts the real
// terms in `license_name`, so a catalogue that copies only the first shows them as "other".
func engineResolveHF(ctx context.Context, hf engineIngestHF) (engineResolved, *apiError) {
	repo := strings.Trim(strings.TrimSpace(hf.Repo), "/")
	file := strings.TrimPrefix(strings.TrimSpace(hf.File), "/")
	if repo == "" || file == "" {
		return engineResolved{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "hf needs a repo and a file"}
	}
	doc, rev, aerr := engineReadHF(ctx, repo, hf.Revision)
	if aerr != nil {
		return engineResolved{}, aerr
	}
	var found bool
	out := engineResolved{
		License:       engineFirstString(doc.CardData.License),
		LicenseName:   strings.TrimSpace(doc.CardData.LicenseName),
		LicenseURL:    strings.TrimSpace(doc.CardData.LicenseLink),
		Gated:         engineHFGated(doc.Gated),
		Restrictions:  engineHFRestrictions(doc.Gated),
		BaseModel:     engineFirstString(doc.CardData.BaseModel),
		ContextLength: doc.GGUF.ContextLength,
		Source:        "hf:" + repo + "/" + file,
		// The repository id IS the name here, and it is still worth storing (ADR 0088): the row
		// id is the file's stem, so `qwen3_coder_30b_q4` loses both the owner and the
		// quantiser that `unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF` names.
		DisplayName: repo,
		PreviewURL:  engineSafeHTTPURL(strings.TrimSpace(doc.CardData.Thumbnail)),
	}
	// The model card, as text. It is the one place a Hugging Face author writes down how to run
	// the thing, and reading it costs one more GET on a route that has already made two.
	out.ParamsHint = engineParamsFromText(engineReadText(ctx,
		engineIngestBase+"/"+repo+"/raw/"+url.PathEscape(rev)+"/README.md"))
	for _, s := range doc.Siblings {
		if s.Name != file {
			continue
		}
		found, out.Bytes, out.SHA256 = true, s.Size, strings.ToLower(s.LFS.SHA256)
		break
	}
	if !found {
		return engineResolved{}, &apiError{http.StatusNotFound, errCodeIngestFileUnknown,
			"the repository does not list " + file}
	}
	if out.LicenseURL == "" {
		out.LicenseURL = engineIngestBase + "/" + repo
	}
	out.DownloadURL = engineIngestBase + "/" + repo + "/resolve/" + url.PathEscape(rev) + "/" + file
	// A repository may list a file with no LFS pointer (a small config, or a plain upload). The
	// download would still work; the verification would not, and that is the half that matters.
	if len(out.SHA256) != 64 {
		return engineResolved{}, &apiError{http.StatusBadGateway, errCodeIngestNoChecksum,
			"Hugging Face publishes no sha256 for " + file + " — take it in with an explicit url and sha256"}
	}
	// Only the API's resolved commit is immutable. A missing SHA must not be replaced by the
	// request spelling (`main`, a tag, or a branch): the ingest may proceed for compatibility,
	// but that object is deliberately unavailable for verified reuse.
	if resolvedRevision := strings.TrimSpace(doc.SHA); resolvedRevision != "" {
		out.ArtifactIdentity = engineHFArtifactIdentity(repo, file, resolvedRevision, out.SHA256)
	}
	return out, nil
}

// engineIngestList answers "what does this source offer", so a filename is picked rather than
// retyped. Only files that can actually be taken in are returned: one with no sha256 would fail
// the resolve a moment later, and offering it is offering a dead end.
//
// A plain url addresses one file and has no listing — the caller keeps its own field for that.
func engineIngestList(ctx context.Context, src engineIngestSource, kind string) ([]engineCandidate, *apiError) {
	exts := engineIngestExts(kind)
	switch {
	case src.HF != nil:
		repo := strings.Trim(strings.TrimSpace(src.HF.Repo), "/")
		if repo == "" {
			return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "hf needs a repo"}
		}
		doc, _, aerr := engineReadHF(ctx, repo, src.HF.Revision)
		if aerr != nil {
			return nil, aerr
		}
		out := []engineCandidate{}
		for _, s := range doc.Siblings {
			if len(s.LFS.SHA256) != 64 || !engineIngestWanted(s.Name, exts) {
				continue
			}
			out = append(out, engineCandidate{Name: s.Name, Bytes: s.Size,
				SHA256: strings.ToLower(s.LFS.SHA256), Role: engineCandidateRole(s.Name)})
		}
		return engineSortCandidates(out), nil
	case src.Civitai != nil:
		if src.Civitai.VersionID <= 0 {
			return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "civitai needs a versionId"}
		}
		doc, aerr := engineReadCivitai(ctx, src.Civitai.VersionID)
		if aerr != nil {
			return nil, aerr
		}
		out := []engineCandidate{}
		for _, f := range doc.Files {
			if len(f.Hashes.SHA256) != 64 || !strings.EqualFold(f.Type, "Model") {
				continue
			}
			out = append(out, engineCandidate{
				Name: f.Name, Bytes: int64(f.SizeKB * 1024), SHA256: strings.ToLower(f.Hashes.SHA256),
				Role: engineCandidateRole(f.Name),
			})
		}
		return engineSortCandidates(out), nil
	}
	return nil, &apiError{http.StatusBadRequest, errCodeIngestBadSource,
		"only a Hugging Face repository or a Civitai version can be listed"}
}

// engineSortCandidates puts them in the order a person reads them: the repository's own top
// level first, then by name.
//
// Top level first because that is where the single-file checkpoint lives — FLUX.1-dev keeps
// `flux1-dev.safetensors` beside a whole diffusers tree of components, and the components are
// legitimate to stage one day (ADR 0072 decision 2's `text_encoders/` and `vae/`) but are never
// what somebody opening this picker came for. Within a level, by name, which for a GGUF
// repository groups the quantisations (…-q4_k_m, …-q5_k_m, …-q8_0) into the sequence somebody
// is choosing along.
func engineSortCandidates(c []engineCandidate) []engineCandidate {
	sort.SliceStable(c, func(i, j int) bool {
		ti, tj := !strings.Contains(c[i].Name, "/"), !strings.Contains(c[j].Name, "/")
		if ti != tj {
			return ti
		}
		return c[i].Name < c[j].Name
	})
	return c
}

// engineCivitaiDoc is one model VERSION. Shared by the listing and the resolve for the same
// reason as the Hugging Face one.
type engineCivitaiDoc struct {
	BaseModel string `json:"baseModel"`
	// What this version costs and whether it may be fetched at all. 🔴 `usageControl` is here
	// and NOT in `/api/v1/models` (measured 2026-09-12), so a model that Civitai will only run
	// on its own site looks ordinary in the search list and is caught at exactly this point.
	engineCivitaiVersionFacts
	// ModelID is what the model's own document is read by, for the description the recommended
	// settings are usually in — a VERSION description is a changelog ("less flat, more
	// details"), the MODEL description is the page people write their settings on.
	ModelID int `json:"modelId"`
	// Description is the version's own, in HTML. Read first because when it does carry settings
	// they are this version's, which beats the model's older ones.
	Description string `json:"description"`
	// Name is the VERSION's name ("Meina V11", "Hard"), beside Model.Name below, which is the
	// model's ("MeinaMix"). The pair is what tells two rows of the same model apart — the one
	// thing a row id derived from a file name cannot express (ADR 0088).
	Name         string   `json:"name"`
	TrainedWords []string `json:"trainedWords"`
	Model        struct {
		Name string `json:"name"`
		Type string `json:"type"`
		NSFW bool   `json:"nsfw"`
		POI  bool   `json:"poi"`
	} `json:"model"`
	// The version's example images, the same list the search answer carries. Only the URL is
	// read: an example can be a video, and the transform the panel's URL asks for answers a
	// still frame for one (engineCivitaiImageVariant), so `type` decides nothing here.
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
	Files []struct {
		engineCivitaiFileFacts
		Name        string  `json:"name"`
		SizeKB      float64 `json:"sizeKB"`
		Type        string  `json:"type"`
		DownloadURL string  `json:"downloadUrl"`
		Hashes      struct {
			SHA256 string `json:"SHA256"`
		} `json:"hashes"`
	} `json:"files"`
}

// engineCivitaiModelDoc is the model behind a version, read for one thing only: the description
// its author wrote the recommended settings into.
type engineCivitaiModelDoc struct {
	Description string `json:"description"`
	engineCivitaiLicenceFacts
}

func engineReadCivitai(ctx context.Context, versionID int) (engineCivitaiDoc, *apiError) {
	var doc engineCivitaiDoc
	id := strconv.Itoa(versionID)
	if aerr := engineIngestGetJSON(ctx, engineCivitaiBase+"/api/v1/model-versions/"+id, &doc); aerr != nil {
		return engineCivitaiDoc{}, aerr
	}
	return doc, nil
}

// engineResolveCivitai reads a model VERSION, which is what a Civitai download URL addresses.
//
// Measured 2026-09-09 (ADR 0072 open question 4, which the draft could not answer because the
// developer site was 404): `api/v1/model-versions/<id>` answers anonymously with
// `files[].hashes.SHA256` (upper-case hex), `files[].sizeKB` (kilobytes, fractional),
// `files[].downloadUrl`, `baseModel` and `model.type`.
func engineResolveCivitai(ctx context.Context, c engineIngestCivitai) (engineResolved, *apiError) {
	if c.VersionID <= 0 {
		return engineResolved{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "civitai needs a versionId"}
	}
	doc, aerr := engineReadCivitai(ctx, c.VersionID)
	if aerr != nil {
		return engineResolved{}, aerr
	}
	id := strconv.Itoa(c.VersionID)
	want := strings.TrimSpace(c.File)
	for _, f := range doc.Files {
		if want != "" && f.Name != want {
			continue
		}
		if want == "" && !strings.EqualFold(f.Type, "Model") {
			continue // the version also carries config files and preview images
		}
		if len(f.Hashes.SHA256) != 64 {
			continue
		}
		// The model document, for the licence matrix and for the description the settings are
		// written in. One more read on a route that has already made one, and a failure is
		// silence rather than a refusal: neither of the two things it carries is worth losing
		// an ingest over.
		var model engineCivitaiModelDoc
		if doc.ModelID > 0 {
			if aerr := engineIngestGetJSON(ctx, engineCivitaiBase+"/api/v1/models/"+strconv.Itoa(doc.ModelID), &model); aerr == nil {
				model.engineCivitaiLicenceFacts = model.with(true)
			}
		}
		model.NSFW = model.NSFW || doc.Model.NSFW
		model.POI = model.POI || doc.Model.POI
		// The version's own description first: when it states settings they are this version's.
		hint := engineParamsFromText(engineStripHTML(doc.Description))
		if hint.empty() {
			hint = engineParamsFromText(engineStripHTML(model.Description))
		}
		hash := strings.ToLower(f.Hashes.SHA256)
		images := make([]string, 0, len(doc.Images))
		for _, image := range doc.Images {
			images = append(images, image.URL)
		}
		preview, thumb := engineCivitaiPreviewPair(images)
		return engineResolved{
			DownloadURL:   f.DownloadURL,
			SHA256:        hash,
			Bytes:         int64(f.SizeKB * 1024),
			LoginRequired: !engineCivitaiAnonymous(ctx, f.DownloadURL),
			Restrictions: engineCivitaiRestrictions(model.engineCivitaiLicenceFacts,
				doc.engineCivitaiVersionFacts, []engineCivitaiFileFacts{f.engineCivitaiFileFacts}),
			TrainedWords: engineTrimStrings(doc.TrainedWords),
			ParamsHint:   hint,
			// Civitai publishes no licence field of the kind Hugging Face does — the terms are
			// per model on the site. Saying "unknown" is the honest answer; guessing one would
			// put a made-up licence in the panel next to the real ones.
			LicenseName: "see civitai model page",
			// The SAME redirect engineSourceURL hands out, and for the same reason: from here
			// the version id is all there is, and the query form lands on the model list. A
			// "see the model page" link that opens a list is the one kind of broken link nobody
			// reports, because it opens something.
			LicenseURL:       engineCivitaiBase + "/model-versions/" + id,
			BaseModel:        strings.TrimSpace(doc.BaseModel),
			Source:           "civitai:" + id,
			ArtifactIdentity: engineCivitaiArtifactIdentity(c.VersionID, f.Name, hash),
			// What a person recognises this by (ADR 0088), off the same document.
			DisplayName: strings.TrimSpace(doc.Model.Name),
			VersionName: strings.TrimSpace(doc.Name),
			PreviewURL:  preview,
			ThumbURL:    thumb,
		}, nil
	}
	return engineResolved{}, &apiError{http.StatusNotFound, errCodeIngestFileUnknown,
		"that version publishes no file with a sha256" + engineIngestNamed(want)}
}

// engineCivitaiAnonymous asks the one question the metadata call cannot answer: may these bytes
// be fetched by somebody with no account?
//
// 🔴 ADR 0072 P2 欠落 5. `api/v1/model-versions/<id>` answers 200 with the hash, the size and
// the download URL for assets whose uploader has switched "you must be logged in to download"
// on — so `resolve` said `can_ingest: true`, the job ran, and the Fargate task died nine
// minutes later with `curl: (22) … error: 401`. What reaches the operator is an exit code.
// Measured on af-sandbox: five assets, answers split 200 / 401 / 403, per uploader.
//
// 🔥 Measured 2026-09-15: this used to be a HEAD, and that is wrong now. Civitai's download URL
// redirects (307) to a Cloudflare R2 presigned URL, and R2 signs the presigned URL for GET only
// — a HEAD against it comes back 403 no matter who uploaded the file or what they restricted.
// Confirmed live against a completely unwalled, 200k-download asset: HEAD 403, `GET` with
// `Range: bytes=0-0` 206. A HEAD-based probe therefore reads *every* Civitai asset as needing an
// account. A ranged GET costs one byte and is answered the same way the real download is.
//
// Short timeout, and it FAILS OPEN in every direction but the two it can read: a probe that
// could not run must not stop an ingest that would have worked, and a CDN that dislikes ranged
// GETs (405) is not a login wall. Only 401 and 403 — the two Civitai actually answers with —
// are read as "not anonymously".
func engineCivitaiAnonymous(ctx context.Context, target string) bool {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return true
	}
	// Its own budget, well under engineIngestHTTP's: this rides on the admin path while
	// somebody is typing, and the answer is a status line.
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, target, nil)
	if err != nil {
		return true
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	return resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden
}

func engineIngestNamed(f string) string {
	if f == "" {
		return ""
	}
	return " called " + f
}

// engineHFGated reads the gating flag, which is `false` or a string ("auto" / "manual").
func engineHFGated(v any) bool {
	switch g := v.(type) {
	case bool:
		return g
	case string:
		return strings.TrimSpace(g) != "" && !strings.EqualFold(g, "false")
	}
	return false
}

// engineFirstString reads a card field that is a string on most models and a list on some.
func engineFirstString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		if len(t) > 0 {
			if s, ok := t[0].(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// engineIngestGetJSON deliberately makes ONE round trip and never retries a 503: this backs both
// metadata resolution and the VAE header probe (engine_vae.go), and those callers each have
// their OWN budget for how many times to ask a source that has already refused — a scan of many
// rows caps how many DIFFERENT models it probes after a few come back unreadable
// (engineVaeScanFails), and multiplying every one of those into several requests would blow that
// budget silently. A 503 that clears in under a second is instead retried where it is safe to —
// the search path (engineSearchGetJSON) — because that is one request per user keystroke, not
// one per row of a batch.
func engineIngestGetJSON(ctx context.Context, target string, out any) *apiError {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return &apiError{http.StatusBadRequest, errCodeIngestBadSource, err.Error()}
	}
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		// The CP reaches these through the NAT. A deployment that blocks outbound traffic gets
		// this, and the answer is the manual `run-task`, so say which way the call was going.
		return &apiError{http.StatusBadGateway, errCodeIngestSourceUnreach,
			"could not reach " + engineIngestHost(target) + ": " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &apiError{http.StatusBadGateway, errCodeIngestSourceForbid,
			engineIngestHost(target) + " refused the lookup (" + resp.Status + ")"}
	}
	if resp.StatusCode != http.StatusOK {
		return &apiError{http.StatusBadGateway, errCodeIngestSourceError,
			engineIngestHost(target) + " answered " + resp.Status}
	}
	if err := json.Unmarshal(body, out); err != nil {
		// 🔴 The panel gets the host and nothing else — an administrator can do nothing with a
		// decoder's complaint — so without this line the ONE thing worth knowing is written
		// down nowhere: "unreadable answer from huggingface.co" is the same sentence whatever
		// upstream changed shape. Measured 2026-09-10: a fractional `trendingScore` emptied
		// every text-to-image search and the field had to be found by re-fetching the API by
		// hand. encoding/json names the field, the value and the Go type it would not fit; the
		// target rides along because that is what makes the row fetchable again, and it
		// carries no token (decision 6: the CP only ever reads the source anonymously).
		log.Printf("engines: unreadable answer from %s: %v", target, err)
		return &apiError{http.StatusBadGateway, errCodeIngestSourceError, "unreadable answer from " + engineIngestHost(target)}
	}
	return nil
}

func engineIngestHost(target string) string {
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return u.Host
	}
	return target
}

// --- running the task ---------------------------------------------------------

// engineIngestECSAPI is the narrow ECS port for this file, so a test drives the whole job
// lifecycle without AWS.
type engineIngestECSAPI interface {
	RunTask(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

// engineIngestLogsAPI reads the task's own words. Optional: a deployment whose stack predates
// the log grant gets exit codes and no sentence, which is worse but not broken.
type engineIngestLogsAPI interface {
	GetLogEvents(ctx context.Context, group, stream string) ([]string, error)
}

// engineIngester starts ingest tasks and reconciles them against ECS.
type engineIngester struct {
	// defMu guards everything the TABLE decides, because the table reloader replaces it while
	// requests are reading it (engines.go, startIngest). Before this the block was read once at
	// boot and a later table was silently ignored — see adopt.
	defMu sync.RWMutex
	def   engineIngestDef
	// tokens is the operator's Hugging Face token. It hangs here rather than on an engine
	// because the ingest task is deployment-wide, and because this is the only place that
	// needs the value rather than the fact that there is one.
	tokens *engineHfTokens
	// civitaiTokens is the same registration for Civitai, carried through its own secret
	// (engine_civitai_token.go) rather than sharing the Hugging Face one: the two are
	// unrelated accounts, and the fetch container picks between them by the download's host.
	civitaiTokens *engineCivitaiTokens
	cluster       string
	ecs           engineIngestECSAPI
	logs          engineIngestLogsAPI
	store         store.EngineIngestStore
	models        store.EngineModelStore
	storageMu     sync.RWMutex
	storage       *engineStorage
	// onDone is called after a job created its catalogue row, so the registry can invalidate
	// its cache and the panel shows the new row without waiting for the TTL.
	onDone func(role string)
}

// ingestDef is what the stack declares RIGHT NOW, not what it declared at boot.
//
// 🔴 The difference is a whole deployment's ingest. A CloudFormation update that touches the
// ingest task definition registers a new revision and DEREGISTERS the previous one, and the
// table publishes `!Ref`'s ARN — the revision included, until the fix beside this one. An
// ingester that kept the block it booted with then answered `InvalidParameterException:
// TaskDefinition is inactive` for every ingest, forever, and the only way out was replacing the
// Control Plane. Measured on af-sandbox 2026-09-15, one stack update after the table in SSM
// already carried the new revision.
func (g *engineIngester) ingestDef() engineIngestDef {
	if g == nil {
		return engineIngestDef{}
	}
	g.defMu.RLock()
	defer g.defMu.RUnlock()
	return g.def
}

// hfTokens and civitai are the secret carriers built from that block, read through the same
// lock because adopt rebuilds them when the stack names different secrets.
func (g *engineIngester) hfTokens() *engineHfTokens {
	if g == nil {
		return nil
	}
	g.defMu.RLock()
	defer g.defMu.RUnlock()
	return g.tokens
}

func (g *engineIngester) civitai() *engineCivitaiTokens {
	if g == nil {
		return nil
	}
	g.defMu.RLock()
	defer g.defMu.RUnlock()
	return g.civitaiTokens
}

// adopt takes the table's current ingest block, and reports whether anything moved.
//
// `secrets` rebuilds the two token carriers, and is called only when the stack names a different
// secret than the one they hold: they are handed out by pointer to the admin routes, so
// replacing them on every poll would be churn where the answer never changed.
func (g *engineIngester) adopt(def engineIngestDef,
	secrets func(engineIngestDef) (*engineHfTokens, *engineCivitaiTokens)) bool {
	if g == nil || !def.ok() {
		return false
	}
	g.defMu.Lock()
	defer g.defMu.Unlock()
	if reflect.DeepEqual(g.def, def) {
		return false
	}
	was := g.def
	g.def = def
	if secrets != nil && (was.TokenSecret != def.TokenSecret ||
		was.CivitaiTokenSecret != def.CivitaiTokenSecret || was.HasToken != def.HasToken) {
		g.tokens, g.civitaiTokens = secrets(def)
	}
	log.Printf("engines: ingest declaration adopted from the table (task definition %q -> %q)",
		was.TaskDef, def.TaskDef)
	return true
}

func (g *engineIngester) storageChecker() *engineStorage {
	if g == nil {
		return nil
	}
	g.storageMu.RLock()
	defer g.storageMu.RUnlock()
	return g.storage
}

func (g *engineIngester) setStorage(storage *engineStorage) bool {
	if g == nil || storage == nil || strings.TrimSpace(storage.scope) == "" {
		return false
	}
	g.storageMu.Lock()
	defer g.storageMu.Unlock()
	if g.storage != nil && g.storage.scope == storage.scope && g.storage.metadata != nil {
		return false
	}
	g.storage = storage
	return true
}

// engineIngestRequest is one "take this in", after the API has validated it.
type engineIngestRequest struct {
	Role, ModelID, Kind, S3Key string
	Description, BaseModel     string
	ContextTokens, MaxOutput   int
	Sizes                      []string
	// Params is what the form declared about how to run this model, already cleaned. It rides
	// through the JOB rather than being written up front because the row does not exist until
	// the download succeeds — and a row with settings and no weights would be a model the
	// catalogue offers and the box cannot load.
	Params *store.EngineParams
	// The licence acceptance, as (tenant, member, licence) — the fourth part of the tuple
	// (the timestamp) is taken when the row is finally written. AcceptedTenant is empty for a
	// super_admin, who acts for the deployment and has no tenant to act for.
	//
	// ⚠️ This struct is what job.Spec holds, so a job started before these fields existed
	// deserializes with them empty. That is the right answer, not a gap: nobody recorded them.
	AcceptedBy, AcceptedTenant, AcceptedLicense string
	Resolved                                    engineResolved
	// FileFlag is what this file IS within the model — the literal engine flag it is passed to
	// (`--vae`, `--t5xxl`), empty for a whole checkpoint. Without it every ingest produced a
	// one-file, unlabelled row and no split model could be assembled by taking parts in (ADR
	// 0072 P2 欠落 6).
	FileFlag string
	// Attach adds this file to the row ModelID already names instead of creating one. The two
	// are separate acts and only one of them may land on an id the catalogue already holds:
	// creating would upsert a working row's files, licence and enabled flag away.
	Attach bool
	// Replace swaps the file that row holds under the SAME flag, leaving every other column
	// alone. The third act, and the one a row's own checkpoint needs: Attach refuses a flag that
	// is taken and cannot touch the unlabelled slot at all, so without this the only way to move
	// a model to another quantisation was to forget the row — with its licence acceptance, its
	// family, its params and its enabled state — and build it again.
	Replace bool
	// MoveFrom is the key these bytes are ALREADY at, when the job's work is to relocate them
	// inside the bucket instead of downloading anything: the task copies server-side and deletes
	// the source, and the row's declaration is rewritten from MoveFrom to S3Key with FileFlag.
	//
	// 🔴 The fourth act, and the one a row staged in the wrong directory needs. Nothing else can
	// repair it: the file a ComfyUI loader cannot list is not missing, so taking it in again is a
	// second copy of a 4–13 GB file, and re-labelling alone leaves the bytes where no loader
	// looks. Empty for every ordinary ingest, which is what makes the task's default MODE the
	// download it has always been.
	MoveFrom string
	// The attention geometry read from the GGUF header before the job started (engine_gguf.go).
	// Zero means it could not be read — a gated repository with no token, a file that is not a
	// GGUF, an upstream that refused the Range — and the row keeps the floor it always had.
	KVGeom engineKVGeometry
	// VaeBundled is the same kind of fact for the image role, read from the safetensors header
	// before the job started (engine_safetensors.go): does this checkpoint carry the VAE its
	// family decodes with. Written onto the FILE, so it survives the row being edited and moves
	// with a replacement.
	VaeBundled string
	// PartsFollowUp is the same promise for a SPLIT family's own parts — the text encoder and
	// VAE a diffusion model cannot generate without (engine_family_parts.go). A list rather than
	// one, because a family needs all of them or the row stays refused.
	PartsFollowUp []engineVaeFollowUp
	// PartsMove is, per file flag, the key a follow-up part's bytes are at TODAY, when the plan
	// found them in the bucket under a name no loader lists (ADR 0085 decision 1). Without it
	// that second job would be a download of bytes this deployment has already bought; with it it
	// is one server-side `aws s3 mv`. Empty for every part that is downloaded or already at its
	// own key.
	PartsMove map[string]string
	// VaeFollowUp is the second file this ingest promised: the family's own VAE, attached to the
	// row this job creates. Decided while somebody was still at the form — the job finishes in
	// the reconciler, where a resolve failure has nobody to report itself to.
	VaeFollowUp *engineVaeFollowUp
}

// start creates the job row and launches the task. The row is written FIRST: a RunTask that
// succeeds and a CP that dies before recording it is a task nobody can see, which is the one
// outcome with no way back.
func (g *engineIngester) start(ctx context.Context, req engineIngestRequest) (store.EngineIngestJob, *apiError) {
	// postIngest checks this before resolving the source so the normal request pays no download
	// for a recorded key. Keep the same fence here because follow-up VAEs start from the
	// reconciler, not that route; otherwise two completed checkpoints could upload different
	// bytes to the family's fixed VAE key before either attachment is registered.
	if ref := engineIngestDestinationUnused(ctx, g.models, g.store, g.storageChecker(), req.Role, req.S3Key); ref != nil {
		// The holder and the next act are dropped here and only here: this method answers
		// *apiError to callers that predate them, and the routes that can draw a button
		// (postIngest) ask the same question themselves before calling it.
		return store.EngineIngestJob{}, ref.Plain()
	}
	// The registered tokens are carried into the stack's secrets before EVERY ingest, both of
	// them regardless of which source this job is for. Not when it looks stale — nothing can
	// look stale here: the CP has no `GetSecretValue`, and a stack rebuilt under a registered
	// token holds the sentinel with no way to notice. Staged before the job row so a
	// deployment that cannot write a secret fails without leaving one.
	//
	// A move is the exception and it is not an optimisation: it reaches no upstream at all, so a
	// deployment whose secret write is refused would be blocked from repairing a row over a
	// credential neither container is going to read.
	if req.MoveFrom == "" {
		if aerr := g.hfTokens().stage(ctx); aerr != nil {
			return store.EngineIngestJob{}, aerr
		}
		if aerr := g.civitai().stage(ctx); aerr != nil {
			return store.EngineIngestJob{}, aerr
		}
	}
	spec, _ := json.Marshal(req)
	job := store.EngineIngestJob{
		ID: store.NewID(), Role: req.Role, ModelID: req.ModelID, S3Key: req.S3Key,
		Source: req.Resolved.Source, State: store.EngineIngestPending,
		Bytes: req.Resolved.Bytes, Spec: string(spec), StartedBy: req.AcceptedBy,
		// Whose grant this ran under, so the panel can show one tenant its own downloads
		// while they are still downloads (ADR 0072 open question 11). The same value lands on
		// the catalogue row at the end, but that is minutes away and this list is what
		// somebody is watching in the meantime.
		TenantID: req.AcceptedTenant,
	}
	if err := g.store.PutEngineIngestJob(ctx, job); err != nil {
		return job, internalErr(err)
	}
	arn, err := g.runTask(ctx, req)
	if err != nil {
		job.State, job.Message = store.EngineIngestFailed, err.Error()
		_ = g.store.PutEngineIngestJob(ctx, job)
		return job, &apiError{http.StatusBadGateway, errCodeIngestStartFailed, err.Error()}
	}
	job.TaskArn, job.State = arn, store.EngineIngestRunning
	if err := g.store.PutEngineIngestJob(ctx, job); err != nil {
		return job, internalErr(err)
	}
	log.Printf("engines: ingest %s started for %s/%s (%s)", job.ID, req.Role, req.ModelID, job.Source)
	return job, nil
}

// reuse is what happens when the bytes are already this deployment's (ADR 0085 decision 1).
//
// Two shapes, and the plan decides which: at the CANONICAL key there is nothing to do but
// declare it, so the catalogue transition is applied immediately and no token is staged and no
// ECS task started; at ANOTHER key the bytes have to be relocated first, which is a task
// (`MODE=move`, a server-side `aws s3 mv`) and therefore the ordinary start — re-fetching a
// 13 GB file to put it one directory higher is the repair this deployment refuses to make.
func (g *engineIngester) reuse(ctx context.Context, req engineIngestRequest) (store.EngineIngestJob, *apiError) {
	if strings.TrimSpace(req.MoveFrom) != "" {
		return g.start(ctx, req)
	}
	spec, _ := json.Marshal(req)
	job := store.EngineIngestJob{
		ID: store.NewID(), Role: req.Role, ModelID: req.ModelID, S3Key: req.S3Key,
		Source: req.Resolved.Source, State: store.EngineIngestPending,
		Bytes: req.Resolved.Bytes, Spec: string(spec), StartedBy: req.AcceptedBy,
		TenantID: req.AcceptedTenant,
	}
	if err := g.store.PutEngineIngestJob(ctx, job); err != nil {
		return job, internalErr(err)
	}
	if err := g.install(ctx, req, job.ID); err != nil {
		job.State, job.Message = store.EngineIngestFailed, err.Error()
		_ = g.store.PutEngineIngestJob(ctx, job)
		return job, &apiError{http.StatusConflict, errCodeEngineBadBody, err.Error()}
	}
	job.State, job.Message = store.EngineIngestDone, ""
	if err := g.store.PutEngineIngestJob(ctx, job); err != nil {
		return job, internalErr(err)
	}
	log.Printf("engines: ingest %s reused %s for %s/%s", job.ID, req.S3Key, req.Role, req.ModelID)
	return job, nil
}

func (g *engineIngester) runTask(ctx context.Context, req engineIngestRequest) (string, error) {
	def := g.ingestDef()
	if g.ecs == nil || !def.ok() {
		return "", fmt.Errorf("this deployment's engine stack declares no ingest task")
	}
	out, err := g.ecs.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(g.cluster),
		TaskDefinition: aws.String(def.TaskDef),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        def.Subnets,
				SecurityGroups: def.SecurityGroups,
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: engineIngestOverrides(req),
		},
	})
	if err != nil {
		return "", err
	}
	for _, f := range out.Failures {
		// A RunTask that placed nothing answers 200 with a failure list, and the reason
		// ("RESOURCE:MEMORY", "Capacity is unavailable") is the only useful sentence in it.
		return "", fmt.Errorf("ECS refused the task: %s %s", aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Tasks) == 0 {
		return "", fmt.Errorf("ECS started no task and gave no reason")
	}
	return aws.ToString(out.Tasks[0].TaskArn), nil
}

// engineIngestOverrides is what the two containers are told to do: fetch-and-upload for an
// ordinary ingest, and a server-side relocation when the bytes are already in the bucket.
//
// 🔴 MODE=move skips the download entirely — `fetch` exits at once and `upload` runs one
// `aws s3 mv`, which S3 performs inside the bucket. That is the whole reason a misplaced 13 GB
// file is repairable at all: the alternative is re-fetching bytes this deployment already owns,
// and the fetch container's ephemeral disk would have to hold them again.
func engineIngestOverrides(req engineIngestRequest) []ecstypes.ContainerOverride {
	if strings.TrimSpace(req.MoveFrom) != "" {
		return []ecstypes.ContainerOverride{
			{Name: aws.String("fetch"), Environment: engineIngestEnv(map[string]string{"MODE": "move"})},
			{Name: aws.String("upload"), Environment: engineIngestEnv(map[string]string{
				"MODE": "move", "FROM": req.MoveFrom, "KEY": req.S3Key,
			})},
		}
	}
	return []ecstypes.ContainerOverride{
		{Name: aws.String("fetch"), Environment: engineIngestEnv(map[string]string{
			"URL": req.Resolved.DownloadURL, "SHA256": req.Resolved.SHA256,
		})},
		{Name: aws.String("upload"), Environment: engineIngestEnv(map[string]string{
			"KEY": req.S3Key,
		})},
	}
}

func engineIngestEnv(kv map[string]string) []ecstypes.KeyValuePair {
	out := make([]ecstypes.KeyValuePair, 0, len(kv))
	for k, v := range kv {
		out = append(out, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
	}
	return out
}

// reconcile brings every unfinished job up to date with ECS, and creates the catalogue row for
// the ones that finished. Called on a timer and whenever the panel asks for the list — the
// panel's own call is what makes a job look live while somebody is watching it.
func (g *engineIngester) reconcile(ctx context.Context) {
	if g == nil || g.store == nil || g.ecs == nil {
		return
	}
	jobs, err := g.store.ListActiveEngineIngestJobs(ctx)
	if err != nil || len(jobs) == 0 {
		return
	}
	arns := make([]string, 0, len(jobs))
	byArn := map[string]store.EngineIngestJob{}
	for _, j := range jobs {
		if j.TaskArn == "" {
			continue
		}
		arns = append(arns, j.TaskArn)
		byArn[j.TaskArn] = j
	}
	if len(arns) == 0 {
		return
	}
	out, err := g.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(g.cluster), Tasks: arns,
	})
	if err != nil {
		log.Printf("engines: ingest reconcile failed: %v", err)
		return
	}
	for _, t := range out.Tasks {
		j, ok := byArn[aws.ToString(t.TaskArn)]
		if !ok || strings.ToUpper(aws.ToString(t.LastStatus)) != "STOPPED" {
			continue
		}
		g.finish(ctx, j, t)
	}
	// A task ECS no longer knows about (they are forgotten an hour after stopping) is not
	// "still running": a CP that was down through the whole download comes back to exactly
	// this, and leaving the job at `running` for ever is the one state nobody can clear.
	for _, f := range out.Failures {
		if j, ok := byArn[aws.ToString(f.Arn)]; ok {
			j.State = store.EngineIngestFailed
			j.Message = "ECS no longer knows this task (" + aws.ToString(f.Reason) + ") — check the bucket before retrying"
			_ = g.store.PutEngineIngestJob(ctx, j)
		}
	}
}

// finish records the outcome of one stopped task, and creates the catalogue row when it worked.
func (g *engineIngester) finish(ctx context.Context, j store.EngineIngestJob, t ecstypes.Task) {
	failed := ""
	for _, c := range t.Containers {
		if c.ExitCode != nil && *c.ExitCode != 0 {
			failed = aws.ToString(c.Name)
		}
	}
	if failed == "" && aws.ToString(t.StoppedReason) != "" && len(t.Containers) == 0 {
		failed = "task"
	}
	if failed != "" {
		j.State = store.EngineIngestFailed
		j.Message = g.why(ctx, t, failed)
		if j.Message == "" {
			j.Message = "the " + failed + " container failed (" + aws.ToString(t.StoppedReason) + ")"
		}
		_ = g.store.PutEngineIngestJob(ctx, j)
		log.Printf("engines: ingest %s failed: %s", j.ID, j.Message)
		return
	}
	if storage := g.storageChecker(); storage != nil {
		storage.invalidate(j.S3Key)
	}
	var req engineIngestRequest
	if g.models == nil || json.Unmarshal([]byte(j.Spec), &req) != nil || req.ModelID == "" {
		// The bytes are in the bucket; the row is not. A failed state keeps the panel from calling
		// the operation successful and names the manual recovery path without deleting the object.
		j.State = store.EngineIngestFailed
		j.Message = "the object was stored but its catalogue request could not be read back; register " + j.S3Key + " by hand"
		_ = g.store.PutEngineIngestJob(ctx, j)
		log.Printf("engines: ingest %s: %s", j.ID, j.Message)
		return
	}
	// A move empties its source, and a cached "present" there is exactly what the next parts plan
	// would reuse — bytes it would then find gone at generation time.
	if req.MoveFrom != "" {
		if storage := g.storageChecker(); storage != nil {
			storage.invalidate(req.MoveFrom)
		}
	}
	if err := g.install(ctx, req, j.ID); err != nil {
		j.State = store.EngineIngestFailed
		j.Message = "the object was stored but its catalogue change did not apply: " + err.Error()
		_ = g.store.PutEngineIngestJob(ctx, j)
		log.Printf("engines: ingest %s: %s", j.ID, j.Message)
		return
	}
	j.State, j.Message = store.EngineIngestDone, ""
	if err := g.store.PutEngineIngestJob(ctx, j); err != nil {
		log.Printf("engines: ingest %s installed its catalogue row but could not record completion: %v", j.ID, err)
	}
}

// install is the common, synchronous catalogue transition for a downloaded or reused object.
// The three store methods enforce the operation at write time: create cannot overwrite a row
// that won a race, attach requires a row, and replace requires the named slot.
func (g *engineIngester) install(ctx context.Context, req engineIngestRequest, jobID string) error {
	if g.models == nil {
		return fmt.Errorf("no model catalogue")
	}
	file := store.EngineModelFile{
		Flag: req.FileFlag, S3Key: req.S3Key, Bytes: req.Resolved.Bytes, Source: req.Resolved.Source,
		ArtifactIdentity: req.Resolved.ArtifactIdentity,
		// What the header said before the download started. A file taken in AS a VAE answers the
		// question by being one, which is what keeps the scan from ever asking about it again.
		VaeBundled: req.VaeBundled,
	}
	if strings.TrimSpace(req.FileFlag) == "--vae" {
		file.VaeBundled = engineVaeYes
	}
	if req.MoveFrom != "" {
		// The bytes did not change, so neither does what is known about them: the size, the
		// provenance and the immutable identity are the ones the row already carried, and the
		// move deliberately re-uses them rather than resolving the upstream again (it may be
		// gated, moved or gone — none of which would make these bytes any less this file).
		found, err := g.models.MoveEngineModelFile(ctx, req.Role, req.ModelID, req.MoveFrom, file)
		if err != nil {
			return err
		}
		if found {
			log.Printf("engines: ingest %s done: %s/%s now reads %s as %s (moved from %s)",
				jobID, req.Role, req.ModelID, req.S3Key, engineFlagLabel(req.FileFlag), req.MoveFrom)
			if g.onDone != nil {
				g.onDone(req.Role)
			}
			return nil
		}
		// 🔴 The row does not declare the old key — which is the NORMAL case since ADR 0085
		// decision 1, not a failure. The plan moves bytes this deployment already holds under a
		// name no loader lists into a row that does not exist yet (an object a forgotten row left
		// behind) or into a slot that row never had. The task has already put the bytes at their
		// destination, so what is left is the ordinary write: appended for a part, created for the
		// model itself. Only a replacement still fails here, because it names a file to swap and
		// there is none.
		if req.Replace {
			return fmt.Errorf("%s/%s no longer declares %s", req.Role, req.ModelID, req.MoveFrom)
		}
		if req.Attach {
			found, err := g.models.AppendEngineModelFile(ctx, req.Role, req.ModelID, file)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("%s/%s no longer exists", req.Role, req.ModelID)
			}
			log.Printf("engines: ingest %s done: %s added to %s/%s (moved from %s)",
				jobID, engineFlagLabel(req.FileFlag), req.Role, req.ModelID, req.MoveFrom)
			if g.onDone != nil {
				g.onDone(req.Role)
			}
			return nil
		}
		// and on to the create below, with the file at its destination
	}
	if req.Replace {
		// 🔴 The geometry is written again, and only for the slot it describes. The row holds ONE
		// set of KV numbers and until now only the create path wrote them, so a gguf swapped for
		// another quantisation kept the previous file's geometry and went on estimating VRAM off
		// a file that no longer exists. A text encoder's replacement has no such opinion, so it
		// passes nil and the checkpoint's numbers stand.
		var kv *store.EngineModelKV
		if strings.TrimSpace(req.FileFlag) == "" {
			kv = &store.EngineModelKV{Layers: req.KVGeom.Layers, HeadsKV: req.KVGeom.HeadsKV,
				KeyLen: req.KVGeom.KeyLen, ValueLen: req.KVGeom.ValLen,
				NextN: req.KVGeom.NextN, FullAttnInterval: req.KVGeom.FullAttnInterval,
				Ceiling: req.KVGeom.Ceiling}
		}
		found, err := g.models.ReplaceEngineModelFile(ctx, req.Role, req.ModelID, file, kv)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s/%s no longer declares %s", req.Role, req.ModelID, engineFlagLabel(req.FileFlag))
		}
		// 🔴 The OLD object stays in the bucket. The CP has no s3:DeleteObject (ADR 0072 decision
		// 7) and the only principal that has is the ingest task — but this runs in the job
		// reconciler, with nobody waiting on an answer and no way to report one, and the keys it
		// would hand over are shared: `text_encoders/` is pointed at from more than one row
		// (measured on af-sandbox: `clip_l.safetensors` from two). A purge that cannot report
		// what it refused to delete is how a model nobody touched stops loading. The panel says
		// the bytes stay; deleting them is `?purge=1` on a row somebody chose to forget.
		log.Printf("engines: ingest %s done: %s of %s/%s replaced by %s (the previous object stays in the bucket)",
			jobID, engineFlagLabel(req.FileFlag), req.Role, req.ModelID, req.S3Key)
		if g.onDone != nil {
			g.onDone(req.Role)
		}
		return nil
	}
	if req.Attach {
		// One PART of a model that already has a row. Only the file is written: the licence
		// acceptance, the family and the enabled flag on that row were decided when it was
		// created, and this download knows none of them.
		found, err := g.models.AppendEngineModelFile(ctx, req.Role, req.ModelID, file)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s/%s no longer exists", req.Role, req.ModelID)
		}
		log.Printf("engines: ingest %s done: %s added to %s/%s", jobID, engineFlagLabel(req.FileFlag), req.Role, req.ModelID)
		if g.onDone != nil {
			g.onDone(req.Role)
		}
		return nil
	}
	m := store.EngineModel{
		Role: req.Role, ID: req.ModelID, Kind: req.Kind,
		Files:       []store.EngineModelFile{file},
		Description: req.Description, BaseModel: req.BaseModel, Params: req.Params,
		ContextTokens: req.ContextTokens, MaxOutputTokens: req.MaxOutput, Sizes: req.Sizes,
		License:     req.Resolved.License,
		LicenseName: req.Resolved.LicenseName,
		LicenseURL:  req.Resolved.LicenseURL,
		// ⚠️ Created DISABLED whatever the request said. "The file is in the bucket" and
		// "members may use it" are different facts, and the second is a separate, deliberate
		// press of Enable — which is also the moment somebody reads the licence line.
		Enabled:           false,
		LicenseAcceptedBy: req.AcceptedBy, LicenseAcceptedAt: store.NowTS(),
		// Under whose grant, and to what. The tenant is what makes the acceptance auditable
		// after this job row is gone (ADR 0072 open question 11); the licence string is the
		// wording that was accepted, which the row's own License may no longer match.
		LicenseAcceptedTenant:  req.AcceptedTenant,
		LicenseAcceptedLicense: req.AcceptedLicense,
		CommercialUse:          engineCommercialUse(req.Resolved),
		// What the publisher calls it, and the picture that shows what it draws (ADR 0088).
		// Off the same resolve as the licence beside it, so this costs no read of its own — and
		// like the licence it is a snapshot, which the metadata route re-takes on request.
		DisplayName: req.Resolved.DisplayName,
		VersionName: req.Resolved.VersionName,
		PreviewURL:  req.Resolved.PreviewURL,
		ThumbURL:    req.Resolved.ThumbURL,
		// Where it came from, kept for as long as the MODEL is. The job row holds it too, but a
		// job is a record of an event on its own timeline — it outlives the row it created and
		// says nothing about whether that model still exists (observed on the dev deployment,
		// 2026-09-09: two finished jobs for a model that had been forgotten and purged). The
		// question "which vendor is this model" has to be answerable from the model.
		Source: req.Resolved.Source,
		// The words this adapter answers to, kept rather than shown once and dropped (ADR 0081
		// decision 5). The resolve has read them all along and the wizard displayed them — with
		// no column they died with the form, and the adapter that reached the box changed
		// nothing visible.
		TrainedWords: req.Resolved.TrainedWords,
		// What a KV-cache estimate is computed from, read once here and never again: the file
		// is pinned by sha256, so its geometry cannot change under the row (ADR 0074 open
		// question 7).
		KVLayers: req.KVGeom.Layers, KVHeadsKV: req.KVGeom.HeadsKV,
		KVKeyLen: req.KVGeom.KeyLen, KVValueLen: req.KVGeom.ValLen,
		KVNextN: req.KVGeom.NextN, KVFullAttnInterval: req.KVGeom.FullAttnInterval,
		// The architecture's own limit, kept so the row can be re-fitted later without going
		// back through the ingest. 🔴 Stored, never APPLIED: what the model allows and what fits
		// on the card are different questions (ADR 0089), and only the first is the publisher's
		// to answer.
		ContextCeiling: req.Resolved.ContextLength,
	}
	created, err := g.models.CreateEngineModel(ctx, m)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("%s/%s was created before this object could be installed", req.Role, req.ModelID)
	}
	log.Printf("engines: ingest %s done: %s/%s registered (disabled)", jobID, req.Role, req.ModelID)
	if g.onDone != nil {
		g.onDone(req.Role)
	}
	g.followUpVae(ctx, req)
	return nil
}

// followUpVae gives a row that was just created the VAE its checkpoint does not carry, either by
// declaring a file this deployment already holds or by starting a second download.
//
// It runs HERE, after the row exists, because an attach needs something to attach to. Everything
// that could have been decided earlier was: the plan was resolved while the operator was at the
// form, so this path makes no judgement and can report nothing — a failure is a log line and a
// row that keeps its `vae_missing` mark, which is the state the panel's own button fixes.
func (g *engineIngester) followUpVae(ctx context.Context, req engineIngestRequest) {
	if fu := req.VaeFollowUp; fu != nil {
		g.followUpFile(ctx, req, *fu)
	}
	// The SPLIT families' parts (engine_family_parts.go) ride the same path: same promise, same
	// place it has to be kept, and the only thing that differs is which flag the file lands
	// under — which is why the loop is over one list rather than a second copy of this function.
	for _, fu := range req.PartsFollowUp {
		g.followUpFile(ctx, req, fu)
	}
}

// followUpFile keeps ONE of those promises.
func (g *engineIngester) followUpFile(ctx context.Context, req engineIngestRequest, fu engineVaeFollowUp) {
	if g.models == nil {
		return
	}
	if fu.Conflict != "" {
		// Planned as unstartable and never offered, so reaching here means a job spec outlived
		// the catalogue it was planned against. Logged rather than attempted: the download would
		// be refused for the taken key, minutes later, in a reconciler nobody is watching.
		log.Printf("engines: %s/%s left %s alone — %s is %s",
			req.Role, req.ModelID, fu.Flag, fu.S3Key, fu.Conflict)
		return
	}
	flag := strings.TrimSpace(fu.Flag)
	if flag == "" {
		flag = "--vae"
	}
	// The bytes are here, under a name no loader lists: one server-side move rather than a second
	// download of a file this deployment already owns (ADR 0085 decision 1). Attached, because
	// the row this follows up on exists by now.
	if from := strings.TrimSpace(req.PartsMove[flag]); from != "" {
		if _, aerr := g.start(ctx, engineIngestRequest{
			Role: req.Role, ModelID: req.ModelID, S3Key: fu.S3Key, MoveFrom: from,
			AcceptedBy: req.AcceptedBy, AcceptedTenant: req.AcceptedTenant,
			Resolved: fu.Resolved, FileFlag: flag, Attach: true,
		}); aerr != nil {
			log.Printf("engines: %s/%s could not move %s to %s (%s): attach it from the panel",
				req.Role, req.ModelID, from, fu.S3Key, aerr.message)
			return
		}
		log.Printf("engines: %s/%s is moving %s to %s", req.Role, req.ModelID, from, fu.S3Key)
		return
	}
	if fu.Staged {
		file := store.EngineModelFile{
			Flag: flag, S3Key: fu.S3Key, Bytes: fu.Bytes, Source: fu.Source,
			ArtifactIdentity: fu.ArtifactIdentity,
		}
		if flag == "--vae" {
			// The mark the VAE remedy exists to clear. A text encoder has no such question.
			file.VaeBundled = engineVaeYes
		}
		found, err := g.models.AppendEngineModelFile(ctx, req.Role, req.ModelID, file)
		if err != nil || !found {
			log.Printf("engines: %s/%s did not take %s %s (found=%v): attach it from the panel",
				req.Role, req.ModelID, flag, fu.S3Key, found)
			return
		}
		log.Printf("engines: %s/%s declared %s, which this deployment already held", req.Role, req.ModelID, fu.S3Key)
		if g.onDone != nil {
			g.onDone(req.Role)
		}
		return
	}
	// 🔴 The licence tuple is the one the operator accepted at the form, carried rather than
	// re-read: they saw this file's terms beside the checkpoint's own (`family_vae` on the
	// resolve), and an acceptance recorded against anybody else would be a fiction.
	if _, aerr := g.start(ctx, engineIngestRequest{
		Role: req.Role, ModelID: req.ModelID, S3Key: fu.S3Key,
		AcceptedBy: req.AcceptedBy, AcceptedTenant: req.AcceptedTenant,
		AcceptedLicense: engineLicenceLabel(fu.Resolved),
		Resolved:        fu.Resolved, FileFlag: flag, Attach: true,
	}); aerr != nil {
		log.Printf("engines: %s/%s could not start the %s ingest (%s): attach it from the panel",
			req.Role, req.ModelID, flag, aerr.message)
		return
	}
	log.Printf("engines: %s/%s is taking %s in (%s)", req.Role, req.ModelID, flag, fu.S3Key)
}

// engineIngestFailureCode reads the ONE actionable thing out of a failed task's own words: the
// HTTP status the download earned, which for a gated repository is the difference between two
// completely different fixes.
//
// 🔴 Measured on af-sandbox (ADR 0072 P5 実機検証): with one registered token, FLUX.1-dev came
// down and SD3.5 Medium died on `curl: (22) The requested URL returned error: 403`, and
// accepting that repository's terms on Hugging Face with the token's own account fixed it.
// **401 and 403 are not the same failure**: 401 is a token that is not reaching the task
// (register one, check the secret), 403 is a token that arrived and an account that has not
// accepted THIS repository. A panel that said "gated" to both sends half its readers to the
// wrong screen. Civitai's own token (registered the same way, `engine_civitai_token.go`) earns
// the same two-way split.
//
// Read out of the message rather than carried on the job row: the status is the task's, the
// row has no column for it, and the classification is a pure function this file can be tested
// on with both statuses. Anchored on the phrasings the fetch container actually prints, so a
// filename containing 403 is not a diagnosis.
func engineIngestFailureCode(source, msg string) string {
	switch m := engineIngestStatusRe.FindStringSubmatch(msg); {
	case m == nil:
		return ""
	case strings.HasPrefix(source, "civitai:"):
		if m[1] == "403" {
			return errCodeIngestCivitaiLogin
		}
		return errCodeIngestCivitaiNoToken
	case !strings.HasPrefix(source, "hf:"):
		return "" // a plain URL's 401 is the operator's own server, and this cannot advise on it
	case m[1] == "403":
		return errCodeIngestGatedNotAccepted
	default:
		return errCodeIngestGatedNoToken
	}
}

// engineIngestStatusRe matches the status in what the fetch container prints — curl's
// `The requested URL returned error: 403`, and the bare `HTTP/1.1 401` of a verbose run.
var engineIngestStatusRe = regexp.MustCompile(`(?i)(?:error:\s*|HTTP/[\d.]+\s+)(401|403)\b`)

// engineFlagLabel names a file's role in a log line. The empty flag is a whole checkpoint, and
// printing it as `""` reads as a bug in the line rather than as the normal case it is.
func engineFlagLabel(flag string) string {
	if strings.TrimSpace(flag) == "" {
		return "the checkpoint"
	}
	return flag
}

// why reads the last words of the failed container's log.
func (g *engineIngester) why(ctx context.Context, t ecstypes.Task, container string) string {
	group := g.ingestDef().LogGroup
	if g.logs == nil || group == "" {
		return ""
	}
	// The stream name ECS composes: <prefix>/<container>/<task id>.
	arn := aws.ToString(t.TaskArn)
	id := arn[strings.LastIndex(arn, "/")+1:]
	lines, err := g.logs.GetLogEvents(ctx, group, "ingest-"+container+"/"+container+"/"+id)
	if err != nil || len(lines) == 0 {
		return ""
	}
	// The last line that says something about the failure, not merely the last line: the fetch
	// prints its progress and then its complaint.
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" {
			return l
		}
	}
	return ""
}

// engineCommercialUse answers "may THIS DEPLOYMENT use it for a paid service" from the licence
// (ADR 0072 decision 10). Three values, and `unknown` is a real answer — the alternative is a
// list of every licence in the world, and a wrong "yes" is the expensive direction.
func engineCommercialUse(r engineResolved) string {
	name := strings.ToLower(r.LicenseName + " " + r.License)
	switch {
	case strings.Contains(name, "non-commercial"), strings.Contains(name, "noncommercial"),
		strings.Contains(name, "-nc"), strings.Contains(name, "cc-by-nc"):
		return "no"
	case strings.Contains(name, "apache-2.0"), strings.Contains(name, "mit"),
		strings.Contains(name, "openrail++"), strings.Contains(name, "creativeml-openrail-m"),
		strings.Contains(name, "bsd"):
		return "yes"
	}
	return "unknown"
}

// run reconciles unfinished jobs on a timer.
//
// The panel's own list call reconciles too, and that is the one somebody is watching; this loop
// is for the jobs nobody has open — a download started before lunch has to reach `done` and
// write its catalogue row whether or not the screen is up.
func (g *engineIngester) run(ctx context.Context) {
	t := time.NewTicker(engineIngestPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.reconcile(ctx)
		}
	}
}

// engineIngestPoll is deliberately slow. A download takes minutes; the panel refreshes itself
// while it is open, and DescribeTasks on every unfinished job every few seconds would be an API
// call per job per tick for a number nobody is reading.
const engineIngestPoll = 30 * time.Second

// engineIngestLogs reads a task's log group. Its own type because the CP has no CloudWatch Logs
// client anywhere else — this is the only place it needs one, and it is scoped to this stack's
// group by the policy 60-engines attaches.
type engineIngestLogs struct{ api *cloudwatchlogs.Client }

func newEngineIngestLogs(cfg aws.Config) *engineIngestLogs {
	return &engineIngestLogs{api: cloudwatchlogs.NewFromConfig(cfg)}
}

func (l *engineIngestLogs) GetLogEvents(ctx context.Context, group, stream string) ([]string, error) {
	if l == nil || l.api == nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := l.api.GetLogEvents(c, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		// The tail is what says why: the fetch prints its progress and then its complaint.
		Limit:         aws.Int32(20),
		StartFromHead: aws.Bool(false),
	})
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(out.Events))
	for _, e := range out.Events {
		lines = append(lines, aws.ToString(e.Message))
	}
	return lines, nil
}

// deleteObjects removes staged files from the bucket, in the ingest task's MODE=delete.
//
// ⚠️ This is the one thing the Control Plane genuinely cannot do itself. Its task role has no
// S3 action at all (ADR 0072 review R3) and the ADR chose to keep it that way, so "delete the
// bytes" is a job handed to the principal that put them there.
func (g *engineIngester) deleteObjects(ctx context.Context, keys []string) error {
	def := g.ingestDef()
	if g == nil || g.ecs == nil || !def.ok() {
		return fmt.Errorf("this deployment's engine stack declares no ingest task")
	}
	out, err := g.ecs.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(g.cluster),
		TaskDefinition: aws.String(def.TaskDef),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        def.Subnets,
				SecurityGroups: def.SecurityGroups,
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{
				{Name: aws.String("fetch"), Environment: engineIngestEnv(map[string]string{"MODE": "delete"})},
				// One task per model, not per file: a split model's parts belong to the same
				// decision, and the container loops over a space-separated list.
				{Name: aws.String("upload"), Environment: engineIngestEnv(map[string]string{
					"MODE": "delete", "KEY": strings.Join(keys, " "),
				})},
			},
		},
	})
	if err != nil {
		return err
	}
	for _, f := range out.Failures {
		return fmt.Errorf("ECS refused the task: %s %s", aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	log.Printf("engines: deleting %d staged file(s) from the bucket", len(keys))
	return nil
}
