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
//   - **the CP never touches S3** (review R3). The task uploads; the CP learns the outcome from
//     `DescribeTasks` and the reason from the task's log.
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The two APIs' base URLs. Variables rather than constants so a test can answer them locally:
// what this file does with a gated repository's metadata is exactly what has to be pinned, and
// reaching the real Hugging Face from a unit test would pin nothing and fail offline.
var (
	engineIngestBase  = "https://huggingface.co"
	engineCivitaiBase = "https://civitai.com"
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
	Source        string // what a person reads in the job list
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
		return engineResolved{DownloadURL: u, SHA256: strings.ToLower(strings.TrimSpace(src.SHA256)), Source: u}, nil
	}
	return engineResolved{}, &apiError{http.StatusBadRequest, errCodeIngestBadSource, "one of hf, civitai or url is required"}
}

// engineHFDoc is the part of a model's API answer this file reads. One call serves both the
// listing and the resolve, so a person who picks a file from the list is choosing from the same
// answer the sha256 and the licence are then taken out of — the alternative is two reads that
// can disagree across a push to the repository.
type engineHFDoc struct {
	Gated    any `json:"gated"` // false, or "auto" / "manual" — a string is still gated
	CardData struct {
		License     any    `json:"license"` // a string, or a list on some cards
		LicenseName string `json:"license_name"`
		LicenseLink string `json:"license_link"`
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
		ContextLength: doc.GGUF.ContextLength,
		Source:        "hf:" + repo + "/" + file,
	}
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
			out = append(out, engineCandidate{Name: s.Name, Bytes: s.Size, SHA256: strings.ToLower(s.LFS.SHA256)})
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
	Model     struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"model"`
	Files []struct {
		Name        string  `json:"name"`
		SizeKB      float64 `json:"sizeKB"`
		Type        string  `json:"type"`
		DownloadURL string  `json:"downloadUrl"`
		Hashes      struct {
			SHA256 string `json:"SHA256"`
		} `json:"hashes"`
	} `json:"files"`
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
		return engineResolved{
			DownloadURL:   f.DownloadURL,
			SHA256:        strings.ToLower(f.Hashes.SHA256),
			Bytes:         int64(f.SizeKB * 1024),
			LoginRequired: !engineCivitaiAnonymous(ctx, f.DownloadURL),
			// Civitai publishes no licence field of the kind Hugging Face does — the terms are
			// per model on the site. Saying "unknown" is the honest answer; guessing one would
			// put a made-up licence in the panel next to the real ones.
			LicenseName: "see civitai model page",
			LicenseURL:  engineCivitaiBase + "/models/?modelVersionId=" + id,
			BaseModel:   strings.TrimSpace(doc.BaseModel),
			Source:      "civitai:" + id,
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
// One HEAD, short timeout, and it FAILS OPEN in every direction but the two it can read: a
// probe that could not run must not stop an ingest that would have worked, and a CDN that
// dislikes HEAD (405) is not a login wall. Only 401 and 403 — the two Civitai actually answers
// with — are read as "not anonymously".
func engineCivitaiAnonymous(ctx context.Context, target string) bool {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return true
	}
	// Its own budget, well under engineIngestHTTP's: this rides on the admin path while
	// somebody is typing, and the answer is a status line.
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodHead, target, nil)
	if err != nil {
		return true
	}
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
	def     engineIngestDef
	cluster string
	ecs     engineIngestECSAPI
	logs    engineIngestLogsAPI
	store   store.EngineIngestStore
	models  store.EngineModelStore
	// tokens is the operator's Hugging Face token. It hangs here rather than on an engine
	// because the ingest task is deployment-wide, and because this is the only place that
	// needs the value rather than the fact that there is one.
	tokens *engineHfTokens
	// onDone is called after a job created its catalogue row, so the registry can invalidate
	// its cache and the panel shows the new row without waiting for the TTL.
	onDone func(role string)
}

// engineIngestRequest is one "take this in", after the API has validated it.
type engineIngestRequest struct {
	Role, ModelID, Kind, S3Key string
	Description, BaseModel     string
	ContextTokens, MaxOutput   int
	Sizes                      []string
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
	// The attention geometry read from the GGUF header before the job started (engine_gguf.go).
	// Zero means it could not be read — a gated repository with no token, a file that is not a
	// GGUF, an upstream that refused the Range — and the row keeps the floor it always had.
	KVGeom engineKVGeometry
}

// start creates the job row and launches the task. The row is written FIRST: a RunTask that
// succeeds and a CP that dies before recording it is a task nobody can see, which is the one
// outcome with no way back.
func (g *engineIngester) start(ctx context.Context, req engineIngestRequest) (store.EngineIngestJob, *apiError) {
	// The registered token is carried into the stack's secret before EVERY ingest. Not when it
	// looks stale — nothing can look stale here: the CP has no `GetSecretValue`, and a stack
	// rebuilt under a registered token holds the sentinel with no way to notice. Staged before
	// the job row so a deployment that cannot write the secret fails without leaving one.
	if aerr := g.tokens.stage(ctx); aerr != nil {
		return store.EngineIngestJob{}, aerr
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

func (g *engineIngester) runTask(ctx context.Context, req engineIngestRequest) (string, error) {
	if g.ecs == nil || !g.def.ok() {
		return "", fmt.Errorf("this deployment's engine stack declares no ingest task")
	}
	out, err := g.ecs.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(g.cluster),
		TaskDefinition: aws.String(g.def.TaskDef),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        g.def.Subnets,
				SecurityGroups: g.def.SecurityGroups,
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{
				{Name: aws.String("fetch"), Environment: engineIngestEnv(map[string]string{
					"URL": req.Resolved.DownloadURL, "SHA256": req.Resolved.SHA256,
				})},
				{Name: aws.String("upload"), Environment: engineIngestEnv(map[string]string{
					"KEY": req.S3Key,
				})},
			},
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
	j.State, j.Message = store.EngineIngestDone, ""
	_ = g.store.PutEngineIngestJob(ctx, j)
	var req engineIngestRequest
	if g.models == nil || json.Unmarshal([]byte(j.Spec), &req) != nil || req.ModelID == "" {
		// The bytes are in the bucket; the row is not. Said plainly rather than silently: the
		// operator's next move is to register the key by hand, which is a route that exists.
		log.Printf("engines: ingest %s finished but its catalogue row could not be read back: register %s by hand",
			j.ID, j.S3Key)
		return
	}
	file := store.EngineModelFile{Flag: req.FileFlag, S3Key: req.S3Key, Bytes: req.Resolved.Bytes}
	if req.Attach {
		// One PART of a model that already has a row. Only the file is written: the licence
		// acceptance, the family and the enabled flag on that row were decided when it was
		// created, and this download knows none of them.
		found, err := g.models.AppendEngineModelFile(ctx, req.Role, req.ModelID, file)
		if err != nil {
			log.Printf("engines: ingest %s finished but %s could not take the file: %v", j.ID, req.ModelID, err)
			return
		}
		if !found {
			// The row was forgotten while the download ran. The bytes are in the bucket and
			// nothing points at them, which is the one outcome worth spelling out.
			log.Printf("engines: ingest %s finished but %s/%s no longer exists: register %s by hand",
				j.ID, req.Role, req.ModelID, j.S3Key)
			return
		}
		log.Printf("engines: ingest %s done: %s added to %s/%s", j.ID, engineFlagLabel(req.FileFlag), req.Role, req.ModelID)
		if g.onDone != nil {
			g.onDone(req.Role)
		}
		return
	}
	m := store.EngineModel{
		Role: req.Role, ID: req.ModelID, Kind: req.Kind,
		Files:       []store.EngineModelFile{file},
		Description: req.Description, BaseModel: req.BaseModel,
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
		// Where it came from, kept for as long as the MODEL is. The job row holds it too, but a
		// job is a record of an event on its own timeline — it outlives the row it created and
		// says nothing about whether that model still exists (observed on the dev deployment,
		// 2026-09-09: two finished jobs for a model that had been forgotten and purged). The
		// question "which vendor is this model" has to be answerable from the model.
		Source: req.Resolved.Source,
		// What a KV-cache estimate is computed from, read once here and never again: the file
		// is pinned by sha256, so its geometry cannot change under the row (ADR 0074 open
		// question 7).
		KVLayers: req.KVGeom.Layers, KVHeadsKV: req.KVGeom.HeadsKV,
		KVKeyLen: req.KVGeom.KeyLen, KVValueLen: req.KVGeom.ValLen,
	}
	if err := g.models.PutEngineModel(ctx, m); err != nil {
		log.Printf("engines: ingest %s finished but the row could not be written: %v", j.ID, err)
		return
	}
	log.Printf("engines: ingest %s done: %s/%s registered (disabled)", j.ID, req.Role, req.ModelID)
	if g.onDone != nil {
		g.onDone(req.Role)
	}
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
// wrong screen.
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
		// Civitai has no token at all, so both statuses mean the same act (ADR 0072 P2 欠落 5).
		// A job started before the resolve probe existed still lands here.
		return errCodeIngestCivitaiLogin
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
	if g.logs == nil || g.def.LogGroup == "" {
		return ""
	}
	// The stream name ECS composes: <prefix>/<container>/<task id>.
	arn := aws.ToString(t.TaskArn)
	id := arn[strings.LastIndex(arn, "/")+1:]
	lines, err := g.logs.GetLogEvents(ctx, g.def.LogGroup, "ingest-"+container+"/"+container+"/"+id)
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
	if g == nil || g.ecs == nil || !g.def.ok() {
		return fmt.Errorf("this deployment's engine stack declares no ingest task")
	}
	out, err := g.ecs.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(g.cluster),
		TaskDefinition: aws.String(g.def.TaskDef),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        g.def.Subnets,
				SecurityGroups: g.def.SecurityGroups,
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
