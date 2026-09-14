package imagegen

// Shared engine transport: the connection shape every self-hosted image provider reads
// (EngineConn and the catalogue types it carries) and the retry/URL/error helpers both sdcpp.go
// and comfy.go call against the Control Plane's engine gateway. Provider-specific request and
// response shapes stay in their own files.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EngineConn is one reachable self-hosted engine: where to send a request and what to
// present. The Agent's engines.go builds it from the Control Plane's catalogue; this package
// never learns that a Control Plane exists.
type EngineConn struct {
	// BaseURL is absolute and already ends at the engine's /v1, e.g.
	// https://<cp>/engine/image/v1.
	BaseURL string
	Token   string
	// Models are the ids the CATALOGUE declares for this engine. The engine is asleep when
	// this is read, so nothing may be asked of it (ADR 0053 / ADR 0071 decision 8). The FIRST
	// is the one the engine will start with — sd-server holds one checkpoint, chosen by a
	// startup flag — which is why the order matters and is not sorted here.
	Models []string
	// Sizes is the size list DECLARED per model id (ADR 0072 decision 2). Empty for a model
	// whose catalogue entry says nothing, and for a Control Plane older than the catalogue,
	// in which case sdcppSizes falls back to reading the id.
	Sizes map[string][]string
	// BaseModel is decision 2's family label per model id ("sdxl", "sd35", "flux1",
	// "flux2-klein", "zimage", …). sdcpp never reads this — it holds one checkpoint and never
	// asks which family it belongs to — but comfy uses it to pick which workflow template
	// renders the request (ADR 0072 decision 4, phase P2).
	BaseModel map[string]string
	// Files are the on-disk names decision 2 declares for one model — see EngineFile. sdcpp
	// does not use this (it never chooses a checkpoint at request time); comfy reads it to fill
	// in a workflow template's loader nodes.
	Files map[string][]EngineFile
	// Warm is the model id the Control Plane last saw this engine actually answer with (ADR
	// 0072 decision 7's warm_model), or "" when nothing is known to be warm. comfy uses it to
	// pick the default when the caller names no model — a switch costs 1-2.5 minutes of disk
	// re-read (measured), so answering with whatever is already warm is free and answering
	// with an arbitrary "first enabled" model is not.
	Warm string
	// Descriptions is the catalogue's own per-model line (ADR 0072 decision 2), the sentence an
	// agent reads when CHOOSING a checkpoint — "photoreal, SDXL fine-tune" and the like. Empty
	// for a model the catalogue says nothing about, which is not an error: the id alone is a
	// usable, if less helpful, choice.
	Descriptions map[string]string
	// Negatives is the catalogue's own negative prompt per model id: the terms a checkpoint's
	// publisher recommends keeping out, which for the SDXL fine-tunes are half of what makes
	// the model behave as its sample pictures do. A DEFAULT, not a policy — a request's own
	// negative prompt is added to it rather than replacing it, because a caller who names one
	// thing to exclude does not mean "and stop excluding everything else".
	Negatives map[string]string
	// NegativeAlways is the deployment administrator's own exclusion list for THIS engine, added
	// to every request no matter which model or member it came from. Separate from Negatives
	// because it answers a different question (what this deployment will not draw, versus what
	// this checkpoint draws badly) and is written from a different screen by a different role.
	//
	// It is not a content filter and must not be described as one: it reaches the sampler on the
	// families that HAVE a negative branch and nowhere else, so a deployment that needs a
	// guarantee needs one somewhere this cannot promise.
	NegativeAlways string
	// Loras are the enabled fine-tunes this engine holds (ADR 0072 decision 5, phase P3). A flat
	// list rather than a map by model: a LoRA is not owned by a checkpoint, it declares the
	// FAMILY it was trained against and any checkpoint of that family may use it.
	Loras []EngineLora
	// Params is what the catalogue declares about how to RUN each model — see EngineParams.
	// Absent for a model whose row says nothing, which is every model until an administrator
	// declares something and is the case the family templates were written for.
	//
	// sdcpp ignores this: it is handed one checkpoint and a fixed command line at startup, so
	// there is no per-request recipe to override. comfy reads it.
	Params map[string]EngineParams
	// Licenses is what the catalogue row says about where a model's weights came from, per model
	// id (ADR 0081 decision 5). Empty for a Control Plane that does not relay them yet, which is
	// the normal case until lane B lands — the pane shows nothing rather than guessing, and the
	// two lanes are deployed separately.
	Licenses map[string]EngineLicense
}

// EngineLicense is one model's provenance as the catalogue holds it: the licence the weights are
// published under, where to read it, and the page they came from. It is a MEMBER-facing fact,
// not an administrative one — somebody about to publish a picture needs to know what the
// checkpoint's licence says, and the admin screen is not a screen they can open.
type EngineLicense struct {
	Name   string
	URL    string
	Source string
}

// EngineParams are one model's declared generation defaults, as ADR 0072's catalogue holds them.
//
// 🔴 Every field is optional and a zero means UNDECLARED. The provider merges them over the
// family's own recipe one field at a time, so a row that names only `steps` keeps the
// template's sampler — folding this into "params or the recipe" would silently drop the other
// three the moment anybody declared one.
//
// Sampler and Scheduler are ComfyUI's own spellings and are checked against the engine's list
// before they are used: the node input is an enumeration, and the answer to a name it does not
// know is `Value not in list` at generation time, after a cold start (the failure SD3.5's
// missing clip_g produced, ADR 0072 P2 残作業 5).
type EngineParams struct {
	Steps     int     `json:"steps,omitempty"`
	CFG       float64 `json:"cfg,omitempty"`
	Sampler   string  `json:"sampler,omitempty"`
	Scheduler string  `json:"scheduler,omitempty"`
	// ClipSkip is carried but applied by no template: none of the five graphs has a
	// CLIPSetLastLayer node. Kept on the wire so the catalogue's declaration survives a round
	// trip rather than being dropped by the reader that does not use it yet.
	ClipSkip int `json:"clip_skip,omitempty"`
	// Weight is a LoRA row's declared strength — what to use when a caller names the adapter
	// and not a number. See comfyResolveLoras for the order the three answers are tried in.
	Weight float64 `json:"weight,omitempty"`
}

// EngineLora is one LoRA row of the catalogue as the provider needs it. File is what the engine
// sees on disk — the same basename rule EngineFile follows — while ID is what the catalogue,
// the panel and the tool's enum all call it.
type EngineLora struct {
	ID          string
	File        string
	Description string
	// BaseModel is the checkpoint family this LoRA was trained against, in decision 2's own
	// vocabulary. "" when the catalogue declares none, which the comfy provider refuses to pair
	// with anything rather than guess.
	BaseModel string
	// Weight is the strength the catalogue declares for this adapter, or 0 for "not declared".
	// An adapter's usable strength is a property OF the adapter — its author publishes one and
	// 0.6 and 1.2 are different pictures — so it belongs on the row rather than in every
	// caller.
	Weight float64
	// TrainedWords are the trigger words the adapter was trained with (ADR 0081 decision 5).
	// Civitai publishes them and the ingest wizard has always shown them; until they became a
	// column nothing stored them, so a LoRA that needs its trigger loaded and changed nothing
	// visible. Empty for an adapter that declares none, and for a Control Plane older than the
	// column.
	TrainedWords []string
}

// EngineFile is one file ADR 0072 decision 2 declares for a model: the on-disk basename (the
// fetch sidecar mirrors S3 keys onto disk verbatim, so this is also what ComfyUI's loader nodes
// see under their configured model directory) and, for a split model, the flag sd.cpp's own
// spelling gives that part (`--vae` / `--clip_l` / `--t5xxl` / `--diffusion-model`). Empty Flag
// means a single-file model (a plain checkpoint). comfy re-reads sd.cpp's vocabulary rather than
// inventing a second one for the same fact — decision 2 already declares "which flag" per file,
// and sd.cpp's flags already say what each part IS regardless of which engine loads them.
type EngineFile struct {
	Flag string
	Name string
}

// EngineLookup is the seam the Agent fills in, keyed by the images ROW's own key rather than by
// provider kind (ADR 0082 decision 1) — two rows of the same kind (a managed comfy engine and an
// operator's LAN comfy box, say) each get the row that actually matches them rather than
// whichever row of that kind happens to exist. nil, or a lookup that finds no row for this key,
// means this deployment does not declare that row, and it is simply never ready.
var EngineLookup func(ctx context.Context, key string) (EngineConn, bool)

// engineLookupFor binds the package-level, key-keyed EngineLookup to one key, giving each
// provider struct the single-argument shape its own tests already construct directly. nil when
// EngineLookup itself is nil (the normal case for a deployment with no engines at all), so a
// provider's lookup field is never a closure that panics on a nil call.
func engineLookupFor(key string) func(ctx context.Context) (EngineConn, bool) {
	if EngineLookup == nil {
		return nil
	}
	return func(ctx context.Context) (EngineConn, bool) { return EngineLookup(ctx, key) }
}

// EngineImageRow is one images-API engine row as Providers() needs to see it: the row's own KEY,
// which becomes this provider's id everywhere a caller, the stored order, the ledger and the MCP
// surface name it (ADR 0082 decision 1), and the row's declared Provider FIELD, which says which
// client implementation serves it — comfy builds a workflow graph, openai-compat speaks the
// OpenAI Images API. Two rows sharing a Provider are two independent instances of the same code,
// talking to different URLs; that is what makes the key, not the kind, the id.
type EngineImageRow struct {
	Key      string
	Provider string
}

// EngineImageRows lists the images rows this deployment currently declares (only the kinds this
// Agent build implements a client for), or nil when there are none — a dev Agent with no Control
// Plane, or a deployment that runs no image engine at all. Providers() calls it fresh each time,
// the same reason it rebuilds its own provider list fresh: the engine table can change after
// boot, and the network round trip it costs is the Agent's own catalogue cache, not this call.
var EngineImageRows func(ctx context.Context) []EngineImageRow

// engineTimeout bounds one call, and it is deliberately LONGER than the gateway's own wake
// timeout (AF_ENGINE_WAKE_TIMEOUT, 900 s by default). The chain is
// gateway 900 s < this 960 s < the MCP layer's budget: whoever gives up first decides what
// the model is told, and the gateway is the only one of the three that knows WHY the wait was
// long ("the box did not come up", "the engine answered 500"). A bare client-side timeout
// here would replace that with nothing.
//
// A warm engine is nowhere near this: measured on an L4, 512px in 7.8 s and 1024px in 21 s.
// The budget is for the cold case — task creation to listening was 195 s, on top of however
// long a GPU box takes to appear (8 to 88 s across P0's measurements).
const engineTimeout = 16 * time.Minute

// engineClient has no timeout of its own: the bound is the context, so that a caller who hung
// up ends the request immediately rather than at the far end of a 16-minute clock.
var engineClient = &http.Client{}

func engineURL(conn EngineConn, path string) string {
	return strings.TrimRight(conn.BaseURL, "/") + path
}

// engineErrText pulls the message out of an error body, whichever of the two shapes it is in —
// the gateway's {"error":{"message":…}} or sd-server's own — and falls back to the raw text.
func engineErrText(body []byte) string {
	var doc struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &doc) == nil {
		if doc.Error.Message != "" {
			return doc.Error.Message
		}
		if doc.Message != "" {
			return doc.Message
		}
	}
	return tail(strings.TrimSpace(string(body)), 400)
}

// engineRetryable decides whether asking again can plausibly do better.
//
//   - the gateway says 503 for three different facts, so the CODE decides and not the number:
//     `engine_waking` is "on its way, ask again"; `engine_off` is an admin switch and
//     `engine_unavailable` is a start that failed, and asking either of those again for fifteen
//     minutes would turn a clear refusal into a hang.
//   - 504 and 502 are an INGRESS, not the gateway: a proxy between the two decided the wait was
//     too long and answered on its behalf. That is the failure this loop was written for. It
//     stays retryable even though the CP now folds its wait below the ALB's, because the next
//     deployment's proxy is not this one's — and it is also what a Control Plane too old to
//     know `engine_waking` looks like from here, since its 900-second hold never survives to
//     answer at all.
func engineRetryable(status int, body []byte) bool {
	switch status {
	case http.StatusGatewayTimeout, http.StatusBadGateway:
		return true
	case http.StatusServiceUnavailable:
		return sdcppErrCode(body) == "engine_waking"
	}
	return false
}

// engineGaveUp is the end of the budget, said in terms of what was actually happening. A bare
// "context deadline exceeded" after a quarter of an hour of waking a GPU box tells nobody what
// to do next, and this message is read by a person AND by the model that asked for the picture.
func engineGaveUp(attempts int, lastWaking string) error {
	if lastWaking == "" {
		return fmt.Errorf("the fleet's own image engine did not come up within %s (%d attempts)",
			engineTimeout, attempts)
	}
	return fmt.Errorf("the fleet's own image engine did not come up within %s (%d attempts): %s",
		engineTimeout, attempts, lastWaking)
}

// attempt is one round trip, with the body fully read so the connection can be reused for the
// next one. status is 0 only when err is set.
// engineHTTPAttempt is shared with comfy.go — the same gateway, the same retry contract
// (503 engine_waking, 502/504 from an ingress), so one round-trip helper is enough for both.
func engineHTTPAttempt(client *http.Client, httpReq *http.Request) (body []byte, status int, retryAfter time.Duration, err error) {
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reaching the image engine failed: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, sdcppMaxResponse))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading the image engine's answer failed: %w", err)
	}
	return body, resp.StatusCode, sdcppRetryAfter(resp.Header.Get("Retry-After")), nil
}
