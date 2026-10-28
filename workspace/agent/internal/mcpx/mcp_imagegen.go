package mcpx

// The generate_image tool (ADR 0069): its definition, the availability question asked at
// tools/list time, the call itself, and the progress heartbeat that keeps a client's per-call
// clock from cutting a generation in half.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// mcpImageGenStatus is the Agent's answer to "can this session generate images, and how".
// It reports FACTS; the rule that turns them into a yes or no lives in mcpImageGenAdvertise,
// where the tool list is built.
type mcpImageGenStatus struct {
	Enabled      bool                  `json:"enabled"`
	Provider     string                `json:"provider"`
	Service      string                `json:"service"`
	Ready        bool                  `json:"ready"`
	Kind         string                `json:"kind"`
	Model        string                `json:"model"`
	Ops          []string              `json:"ops"`
	AspectRatios []string              `json:"aspectRatios"`
	Providers    []mcpImageGenProvider `json:"providers"`
}

// mcpImageGenProvider is one ready provider: what it is called, and what it can do. The
// per-provider list is what makes an explicit `provider` argument honest — the tool's enums are
// built from the providers this session may actually name, not from the first one.
type mcpImageGenProvider struct {
	ID string `json:"id"`
	// Service is the image service the id stands for ("GPT Image", "Gemini …"). It is what the
	// tool description says out loud: the enum values are CLI names, and a session asked for a
	// picture "from Gemini" cannot map that onto `agy` on its own.
	Service      string   `json:"service"`
	Model        string   `json:"model"`
	Ops          []string `json:"ops"`
	AspectRatios []string `json:"aspectRatios"`
	// Models is every checkpoint this provider may be asked for by name (ADR 0072 decision 5,
	// phase P2) — absent for a provider with nothing to choose between, the same rule
	// `provider` itself follows for a session with only one route.
	Models []mcpImageGenModel `json:"models,omitempty"`
	// Loras is every fine-tune this provider will accept (ADR 0072 decision 5, phase P3). One
	// entry is already a choice — with it or without it — so unlike Models there is no
	// "more than one" rule.
	Loras []mcpImageGenLora `json:"loras,omitempty"`
	// Seed is whether this route lets the caller pin the sampler's seed (ADR 0069 follow-up).
	Seed bool `json:"seed,omitempty"`
	// Negative is whether any model on this route samples with a negative branch (ADR 0072
	// follow-up, negative prompts).
	Negative bool `json:"negative,omitempty"`
	// Strength is whether this route lets the caller say how much of the input picture an edit
	// changes (ADR 0069 follow-up, strength).
	Strength bool `json:"strength,omitempty"`
	// MaxInputs is the most reference pictures any model on this route reads (ADR 0094 decision
	// 5). 0 for an Agent too old to report it, and the schema then keeps the fixed ceiling it
	// always had — an older Agent refuses the extras itself, so guessing low here would only take
	// away an argument that works.
	MaxInputs int `json:"max_inputs,omitempty"`
	// Samplers and Schedulers are the names this route is willing to SEND — the Agent's own
	// allow-list, not ComfyUI's whole enumeration. The tool's enum is built from these rather
	// than from a list spelled out here, for the reason the pane's form already relies on: a
	// surface that offers a name the Agent would then refuse by name is the pair disagreeing in
	// front of the caller, and this binary is the one that knows which names it sends.
	//
	// Empty for the vendor routes, which build no sampler graph and have neither.
	Samplers   []string `json:"samplers,omitempty"`
	Schedulers []string `json:"schedulers,omitempty"`
}

// mcpImageGenModel is one checkpoint `model` may name.
type mcpImageGenModel struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Warm is the checkpoint a request naming none would get (ADR 0072 decision 7) — surfaced
	// so an agent can say "the warm one is fine" instead of naming a cold one and paying a
	// 1-2.5 minute switch it did not need to ask for.
	Warm bool `json:"warm,omitempty"`
}

// mcpImageGenLora is one fine-tune `loras` may name. baseModel is on the wire because the enum
// cannot be narrowed to the chosen checkpoint (it is built at tools/list, before `model` exists),
// so the description has to say which family each one belongs to and the Agent refuses the
// pairings that do not fit.
type mcpImageGenLora struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	BaseModel   string `json:"baseModel,omitempty"`
	// TrainedWords are the words the adapter was trained with (ADR 0081 decision 5). The Agent
	// has published them on this very route since that ADR; this struct is where they used to
	// be dropped, and a field absent from it is a field that does not exist downstream — the
	// same silent loss engineCatalogModelRow warns about, one wire further along.
	//
	// They belong in the SCHEMA rather than in an answer the caller could fetch separately,
	// because the moment they are needed is while the prompt is being written: a LoRA applied
	// without its trigger loads, costs the same generation, and changes nothing visible. A
	// second tool would move that fact one round trip away and reintroduce the failure as
	// "forgot to call it" — see the `loras` description in imageGenSchema.
	TrainedWords []string `json:"trained_words,omitempty"`
}

// agentImageGenStatus asks the Agent over the loopback REST every other session tool already
// uses. The short timeout is deliberate: this sits on the tools/list path, which a client
// calls at the start of every turn, so a wedged Agent must cost the turn a moment rather than
// the whole 15 s default.
func agentImageGenStatus(session string) (mcpImageGenStatus, error) {
	var st mcpImageGenStatus
	out, err := agentDoTimeout(http.MethodGet, "/imagegen/status?session="+url.QueryEscape(session), nil, 3*time.Second)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return st, err
	}
	return st, nil
}

// imageGenArgs is the tool's arguments, already split out of the shared argument struct.
type imageGenArgs struct {
	op, provider, prompt, size, aspectRatio, background string
	count                                               int
	inputs                                              []string
	// mask is an absolute path, and only inpaint uses it. It arrives here rather than being
	// folded into inputs because a mask is not a reference image: handing one to a route that
	// has no mask parameter produces a picture OF the mask, which is why the Codex provider
	// refuses it outright (ADR 0069) and the self-hosted one requires it (ADR 0071 P1).
	mask string
	// model names a checkpoint (ADR 0072 decision 5, phase P2) — "" leaves it to the provider's
	// own default, which for the fleet's own engines is the warm one when known (decision 7).
	model string
	// loras are the fine-tunes to apply on top of it (phase P3), forwarded as they arrived: an
	// unknown name and a family that does not match the checkpoint are the Agent's refusals to
	// make, by name, rather than something to drop here.
	loras []imageGenLoraArg
	// seed pins the sampler's starting noise. A POINTER, because 0 is a usable seed and an
	// absent argument is not the same request as `"seed": 0`.
	seed *int64
	// negativePrompt is what to keep out of the picture. Forwarded as typed: what it is added to
	// (the catalogue row's own, the deployment's list) is the Agent's business, not this layer's.
	negativePrompt string
	// strength is how much of the input picture an edit changes. A POINTER so that 0 reaches the
	// Agent as the request it is and gets refused by value there, rather than being read here as
	// "not given" and silently becoming the default.
	strength *float64
	// params is the sampler overlay (steps, cfg, sampler, scheduler, clip_skip, guidance, shift).
	// A POINTER because an absent object and an empty one must not become the same request
	// downstream, and forwarded as typed: which family reads which knob, and what the ceilings are, is the Agent's
	// answer — this layer knows neither and must not narrow either.
	params *imageGenParamsArg
}

// imageGenParamsArg is the tool's `params` object. Its keys are imagegen.EngineParams' own JSON
// spellings, so it rides to the Agent under the same key and in the same shape the job queue's
// route and the catalogue row already use — one fact, one wire.
type imageGenParamsArg struct {
	Steps     int     `json:"steps,omitempty"`
	CFG       float64 `json:"cfg,omitempty"`
	Sampler   string  `json:"sampler,omitempty"`
	Scheduler string  `json:"scheduler,omitempty"`
	ClipSkip  int     `json:"clip_skip,omitempty"`
	Guidance  float64 `json:"guidance,omitempty"`
	Shift     float64 `json:"shift,omitempty"`
}

// imageGenLoraArg is one entry of the tool's `loras` argument.
type imageGenLoraArg struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight,omitempty"`
}

// mcpImageGenCallTimeout is this layer's budget for one generation. It must EXCEED every
// provider's own so that a slow generation is reported by the provider with a real reason,
// rather than by this client as a bare timeout. The chain, longest last:
//
//	engine gateway 15 min  <  sdcpp provider 16 min                      <  this 33 min
//	engine gateway 15 min  <  comfy wake 16 min + generation 15 min = 31 <  this 33 min
//	codex provider  8 min                                                <  this
//
// 33 rather than the 18 it grew to, because ADR 0094 split the comfy provider's one budget in
// two: waking a GPU box and making the picture are different waits and no longer share a clock
// (engineTimeout, engineRunTimeout). 18 would put THIS layer first past the post on a cold
// instruction-edit run, which is the one case where the provider has something specific to say —
// that the engine kept the picture and asking again collects it.
//
// It was 18 rather than the 10 it started at for the reason that still holds: a request to a
// stopped image engine is held by the Control Plane while a GPU box is bought, booted, and loaded
// with a checkpoint (ADR 0071 decision 5). Waiting is what the progress heartbeat below exists to
// make survivable.
//
// It is also why the budget is bounded rather than left open: RunStdio's loop dispatches
// serially, so for as long as a generation is in flight this server reads nothing else from
// stdin — including notifications/cancelled. That is tolerable because the one client on this
// pipe is the agent waiting on this very call, but an unbounded wait would turn a wedged
// provider into a wedged Agent Fleet server.
//
// ⚠️ A codex session is capped below this by its own tool_timeout_sec (600 s, stamped by the
// materializer), so on that kind a cold engine start can still be cut short by the client.
const mcpImageGenCallTimeout = 33 * time.Minute

func mcpGenerateImage(req mcpReq, a imageGenArgs) []byte {
	if strings.TrimSpace(a.prompt) == "" {
		return mcpToolErr(req.ID, "prompt（生成する絵の説明）が必要です")
	}
	self, err := mcpOwningSession()
	if err != nil {
		return mcpToolErr(req.ID, err.Error())
	}
	// The boundary of ADR 0100 decision 3: a session bound to a studio since the client's last
	// tools/list still has this tool in the set the call side checks against, so the binding is
	// read again here, from the meta, on every call.
	if studioBoundSession(self) {
		return mcpToolErr(req.ID, mcpStudioNoGenerateImage)
	}
	body, _ := json.Marshal(map[string]any{
		"session": self, "op": a.op, "provider": a.provider, "prompt": a.prompt,
		"size": a.size, "aspectRatio": a.aspectRatio, "background": a.background,
		"count": a.count, "inputs": absFromCWDAll(a.inputs), "mask": absFromCWD(a.mask), "model": a.model,
		"loras": a.loras, "seed": a.seed, "negativePrompt": a.negativePrompt,
		"strength": a.strength, "params": a.params,
	})

	// The heartbeat runs for as long as the Agent is working. Without it opencode cuts the
	// call at 60 s flat (measured: "MCP error -32001: Request timed out" at 60.0 s), while a
	// notification every 10 s took a 90 s call through cleanly; claude ignores progress for
	// timeout purposes and needs nothing; codex is covered by the tool_timeout_sec the
	// materializer stamps.
	stop := startProgressHeartbeat(req, "画像を生成しています…")
	out, err := agentDoTimeout(http.MethodPost, "/imagegen/generate", body, mcpImageGenCallTimeout)
	stop()
	if err != nil {
		return mcpToolErr(req.ID, "画像を生成できませんでした: "+agentErrDetail(err))
	}
	var res struct {
		Files []struct {
			Path   string `json:"path"`
			Name   string `json:"name"`
			MIME   string `json:"mime"`
			Bytes  int64  `json:"bytes"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			// Seed is the sampler noise THIS picture came from (ADR 0081 decision 3), on the one
			// route that has one. It is relayed because "it was random and I cannot get it back"
			// is the complaint every image tool collects: without it, an agent that made a good
			// picture cannot make a variation of that picture, only of that prompt.
			Seed *int64 `json:"seed"`
		} `json:"files"`
		Provider    string   `json:"provider"`
		Model       string   `json:"model"`
		Region      string   `json:"region"`
		Destination string   `json:"destination"`
		Warnings    []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Files) == 0 {
		return mcpToolErr(req.ID, "画像生成の結果を読み取れませんでした")
	}
	value := map[string]any{
		"files":    res.Files,
		"provider": res.Provider,
	}
	if res.Model != "" {
		value["model"] = res.Model
	}
	if res.Region != "" {
		value["region"] = res.Region
	}
	// Where the prompt went, when the id does not say it on its own (ADR 0069 decision 11):
	// `res.Provider` IS the row's own key since ADR 0082 P0 ("comfy-lan", not a bare
	// "openai-compat"), so a caller can already tell rows apart by `value["provider"]` alone.
	// Destination stays a fixed sentence for what even the key cannot say — whether THIS row is
	// this fleet's own GPU or a metered external service (ADR 0083) — because that fact is not on
	// the wire anywhere a provider may honestly read it.
	if res.Destination != "" {
		value["destination"] = res.Destination
	}
	// Always present, empty included: a caller that only looks for the key when something went
	// wrong is the caller that reports a size it never got.
	value["warnings"] = append([]string{}, res.Warnings...)
	value["note"] = "パスは生成済みのファイル。絵を確認する必要があるときだけ開くこと。warnings は実際に何が起きたかで、無視して寸法が合っている前提の説明をしないこと。"
	return mcpStructuredResult(req.ID, value)
}

// agentErrDetail keeps the Agent's own message (why the provider refused, whether Codex is
// logged in) in the model-visible error. A generic "generation failed" would leave the model
// nothing to act on, and the two ends of the range — "you are not logged in" and "the prompt
// was refused" — call for opposite responses.
func agentErrDetail(err error) string {
	var httpErr *agentHTTPError
	if !errors.As(err, &httpErr) {
		return err.Error()
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &body) == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return httpErr.Error()
}

// --- progress heartbeat -------------------------------------------------------------------

// mcpProgressEvery is the heartbeat interval. 10 s against opencode's 60 s ceiling leaves
// five missed notifications of headroom before the client would give up. A var only so a test
// can shorten it — a test that waited for the real interval would be a ten-second test.
var mcpProgressEvery = 10 * time.Second

// startProgressHeartbeat emits notifications/progress for this request until the returned
// stop is called. It is a no-op when the client sent no progressToken (the token is what a
// notification is addressed to, and an unaddressed one is dropped) or when there is no stdout
// to write to, which is the case in the unit tests.
func startProgressHeartbeat(req mcpReq, message string) (stop func()) {
	token := mcpProgressToken(req)
	if len(token) == 0 || stdioOut == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(mcpProgressEvery)
		defer ticker.Stop()
		var n int
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				n++
				// progress must increase; there is no total, because the provider cannot say
				// how far along an image is.
				notif, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/progress",
					"params": map[string]any{
						"progressToken": token,
						"progress":      n,
						"message":       fmt.Sprintf("%s (%ds)", message, n*int(mcpProgressEvery/time.Second)),
					},
				})
				stdioOut.writeLine(notif)
			}
		}
	}()
	return func() {
		close(done)
		// Wait for the goroutine to leave before the caller writes the response, so a
		// heartbeat can never be emitted after the result it belongs to.
		<-finished
	}
}

// mcpProgressToken reads params._meta.progressToken. It is returned as raw JSON because the
// spec allows a string OR a number and the notification has to echo back exactly what arrived
// — re-typing it here is how a client stops recognising its own token.
func mcpProgressToken(req mcpReq) json.RawMessage {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &p) != nil {
		return nil
	}
	tok := p.Meta["progressToken"]
	if len(tok) == 0 || string(tok) == "null" {
		return nil
	}
	return tok
}

// mcpToolGenerateImage is the one image generation tool (ADR 0069). Named as a constant
// because the call dispatch and the scope check have to agree on it — but the tool literal
// below spells the name out, because the advertised-schema gate finds the declared tools by
// parsing this file's AST and only recognises a STRING literal for "name". Using the constant
// there would drop the tool from that scan without failing anything.
// TestImageGenToolNameMatchesConstant holds the two spellings together.
const mcpToolGenerateImage = "generate_image"

// imageGenSchema is the image generation tool, advertised only under
// `--self-report --image-gen` AND only to the sessions mcpImageGenAdvertise picks.
//
// The offer is asked for at list time rather than hard-coded: which providers this session may
// name, and the operations they support between them. A route that cannot inpaint must not
// advertise inpaint, and every answer changes with a login or a reordered preference.
//
// The result is a PATH, not the image bytes. A measured PNG from this route is 848 KB, which
// is ~1.1 MB of base64 in a tool result that then rides in the session's context for the rest
// of the conversation; the file is on a disk the session can read, so handing back the path
// costs nothing and the model opens it only if it actually needs to look.
func imageGenSchema(offer imageGenOffer) map[string]any {
	// aspect_ratio is offered ONLY when a provider on this list has ratios of its own, because
	// unlike size it is a parameter that really reaches the tool where it exists (measured: agy
	// honours 16:9, codex's route exposes no such parameter at all). Advertising it everywhere
	// would repeat exactly the mistake size documents — a knob the caller turns and nothing
	// moves.
	props := map[string]any{
		"prompt": map[string]any{"type": "string", "minLength": 1,
			"description": "What to draw. English or Japanese"},
		"op": map[string]any{"type": "string", "enum": offer.Ops,
			"description": "Which operation (generate when omitted). This list holds only what the providers available now can actually do"},
		"size": map[string]any{"type": "string",
			"description": "Requested dimensions: WxH such as \"1024x1024\", or \"auto\". **It may not be honoured, and then the real dimensions are in warnings**"},
		"background": map[string]any{"type": "string", "enum": []string{"auto", "opaque", "transparent"},
			"description": "Requested background. Some models do not support transparent, and then it goes into warnings"},
		"count": map[string]any{"type": "integer", "minimum": 1, "maximum": 4,
			"description": "Requested number of images (1 when omitted). A shortfall goes into warnings"},
		// The ceiling is the offer's union (ADR 0094 decision 5), not a literal: since the
		// instruction-edit families it is no longer the same on every route, and a schema that
		// says 5 where the Agent refuses above 1 spends a round trip and a refusal to teach that.
		// The old literal is the fallback for an Agent that does not report one.
		"inputs": map[string]any{"type": "array", "maxItems": maxInputsOrDefault(offer.MaxInputs),
			"items": map[string]any{"type": "string"},
			"description": fmt.Sprintf(
				"Absolute paths of reference images (up to %d), for editing or as a style reference."+
					" A model may take fewer, and then the call is refused by name."+
					" Only files under the file browser's root (usually home) or the generated-images folder are read; copy one from /tmp there first",
				maxInputsOrDefault(offer.MaxInputs))},
	}
	// mask goes with inpaint and nothing else. A mask handed to a route that has no mask
	// parameter does not fail — it produces a picture OF the mask — so the parameter is offered
	// only where some provider can actually take one (ADR 0071 P1).
	// The literal, because this package cannot import internal/imagegen — the ops arrive as
	// strings from the Agent's own status route, which is where the vocabulary is defined.
	for _, op := range offer.Ops {
		if op == "inpaint" {
			props["mask"] = map[string]any{"type": "string",
				"description": "Absolute path of the mask image. Only with op=inpaint; it marks the area to repaint (a provider that takes no mask rejects it)"}
			break
		}
	}
	if len(offer.AspectRatios) > 0 {
		props["aspect_ratio"] = map[string]any{"type": "string", "enum": offer.AspectRatios,
			"description": "Requested aspect ratio. Some providers really do take it (the dimensions themselves cannot be chosen; the real size comes close to the ratio, and any drift goes into warnings). Sent to a provider that does not take it, it goes into warnings"}
	}
	// provider is offered only when there is a real choice. With one entry the argument would
	// be a decoration that still lets a caller pin the route it happened to see today.
	if len(offer.Providers) > 1 {
		props["provider"] = map[string]any{"type": "string", "enum": offer.Providers,
			"description": "Which service generates it. **Leaving it unset is the default**: the user's configured order is followed, and a failure moves on to the next provider. " +
				"Naming one uses that one alone and does not fall through on failure (nothing is billed to a service the user did not name). " +
				"**Specify it only when the user named a service** (\"make it with X\", \"I want to compare both\"). Each provider spends a different plan, so calling it twice to compare draws down two separate plans. " +
				"What they can do differs too (only some take an aspect ratio). " +
				"**The enum values are CLI names, not service names** - look up which reaches which image service in the table at the start of this tool's description"}
	}
	// model, like provider, is offered only when there is a real choice (ADR 0072 decision 5,
	// phase P2) — a single-entry list is the fleet's own image engine holding one checkpoint,
	// and an argument that can only ever mean the default is not a choice.
	if len(offer.Models) > 1 {
		enum := make([]string, 0, len(offer.Models))
		var lines []string
		for _, m := range offer.Models {
			enum = append(enum, m.ID)
			line := m.ID
			if m.Description != "" {
				line += " — " + m.Description
			}
			if m.Warm {
				line += "（現在ロード済み。切り替え不要）"
			}
			lines = append(lines, line)
		}
		props["model"] = map[string]any{"type": "string", "enum": enum,
			"description": "Which checkpoint to use. **Leaving it unset uses whichever is currently loaded, if any — the cheapest choice.** " +
				"Naming a different one makes the engine switch, which can take 1-2.5 minutes on top of the actual generation (this shows up in warnings, not as a hang). " +
				"**Specify it only when the user asked for a specific look** — otherwise prefer the one marked as already loaded. " +
				"Available: " + strings.Join(lines, " / ")}
	}
	// seed is offered only where a route actually takes one — today the fleet's own ComfyUI
	// engine alone. One line of description on purpose: every advertised tool's text is a fixed
	// cost paid by every session on every tools/list, so a parameter earns words by being hard
	// to use rather than by existing.
	//
	// The ceiling is JavaScript's safe-integer limit rather than the sampler's own range: this
	// number is carried as JSON to clients that parse it into a double, and anything above 2^53-1
	// comes back as a DIFFERENT seed than the one that was sent — which would silently break the
	// one property a seed exists to provide.
	if offer.Seed {
		props["seed"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991,
			"description": "Fix the sampler's starting noise so two calls differing in ONE thing can be compared. Omit it and every call is a different picture"}
	}
	// negative_prompt, on the same rule and for a sharper reason: writing what to avoid INTO the
	// prompt conditions on it instead ("no text" puts text in the picture), so a route without a
	// negative branch cannot be worked around from the caller's side. Offered where at least one
	// model has one; a model that has not says so in warnings.
	if offer.Negative {
		props["negative_prompt"] = map[string]any{"type": "string",
			"description": "What to keep OUT of the picture, as a comma-separated list of words. It is ADDED to what the model and the deployment already exclude, never replaces them. Not every checkpoint has a negative prompt at all, and one that does not says so in warnings"}
	}
	// strength, on the same rule as the two above, and offered only alongside the op it belongs
	// to: it is what an edit does to the caller's picture, and on a route with no edit it would
	// be a knob attached to nothing. inpaint is deliberately NOT a reason to offer it — that op
	// repaints its masked area in full by design, and a number the caller could set but the graph
	// must ignore is the kind of argument this schema exists to keep out.
	if offer.Strength && slices.Contains(offer.Ops, "edit") {
		props["strength"] = map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1,
			// offer.Strength is a UNION across models (ADR 0094 decision 11: comfyProvider's
			// Caps("") advertises "yes, some checkpoint on this route reads it"), not a promise
			// that whichever one answers a given call does. Since decision 2 a checkpoint can
			// answer false (Qwen-Image-Edit fixes its denoise at 1) and be refused with 400 — so
			// "0.6 when omitted" alone would have an agent hit that refusal on every call against
			// such a row, with nothing here explaining why.
			"description": "How much of the input picture an edit changes, 0-1 (0.6 when omitted). Small values keep the composition and correct it; 1 redraws from the prompt alone. **Only with op=edit** — inpaint always repaints its masked area fully, and says so in warnings. Some checkpoints on this route do not take it at all (an instruction-editing model fixes its denoise by construction) and answer 400 if it is sent"}
	}
	// params — the sampler overlay, and the fourth word ADR 0069 let into the vocabulary. Offered
	// only where a route BUILDS the sampler graph, which is what having names to send means: the
	// vendor routes have no steps and no sampler, and a request that carries them there comes back
	// as a warning rather than being dropped.
	//
	// The enums are the AGENT's own allow-lists, relayed rather than spelled out here. This
	// package cannot import internal/imagegen, and a second copy of those names would be a
	// schema that offers what the Agent then refuses by name — the one failure the pane's form
	// already takes this list to avoid.
	if len(offer.Samplers) > 0 {
		params := map[string]any{
			// The bounds are literals for the same reason the LoRA cap is: internal/imagegen holds
			// the real ones (paramsMaxSteps / paramsMaxCFG) and refuses past them by value. These
			// only save a caller the round trip.
			"steps": map[string]any{"type": "integer", "minimum": 1, "maximum": 150,
				"description": "Sampling steps: slower, not automatically better"},
			"cfg": map[string]any{"type": "number", "minimum": 0, "maximum": 30,
				"description": "How hard the sampler is pushed toward the prompt"},
			"sampler":   map[string]any{"type": "string", "enum": offer.Samplers},
			"scheduler": map[string]any{"type": "string", "enum": offer.Schedulers},
			// One family each; a family that does not read one says so in warnings.
			"clip_skip": map[string]any{"type": "integer", "minimum": 1, "maximum": 12,
				"description": "sd15/sdxl only: stop the text encoder n layers from the end (the WebUI's \"Clip skip\"; sdxl already runs at 2)"},
			"guidance": map[string]any{"type": "number", "minimum": 0, "maximum": 30,
				"description": "flux1 only: FLUX.1's distilled guidance, what its model cards call CFG (default 3.5; 3.5-5 for illustration)"},
			"shift": map[string]any{"type": "number", "minimum": 0, "maximum": 100,
				"description": "anima only: sampling shift (default 3; 14-24 steadies composition at high resolution)"},
		}
		if len(offer.Schedulers) == 0 {
			delete(params, "scheduler")
		}
		props["params"] = map[string]any{
			"type": "object", "additionalProperties": false, "properties": params,
			"description": "Sampler settings. **Leave it unset unless the user asked for one** — each field omitted runs at what this checkpoint's own entry declares, which is what its publisher recommends, and the fields are independent (naming `sampler` alone keeps the published steps and cfg). " +
				"**`steps` and `cfg` are per-checkpoint and this schema cannot tell you which**: a distilled \"Turbo\" checkpoint runs at ~8 steps and cfg 1, a base one at 30+ and cfg 4-7, so a guessed number burns both the picture and the GPU turn. " +
				"`sampler` and `scheduler` are safe to name — every checkpoint takes any of them, and one a family ignores comes back in warnings",
		}
	}
	// loras is offered as soon as ONE exists, unlike model: applying it or not applying it are
	// already two different pictures, so a single-entry list is a real choice (ADR 0072
	// decision 5, phase P3).
	//
	// The enum is every LoRA, not the ones that fit the checkpoint, because a tool schema is
	// built here — at tools/list, before `model` is chosen — and an enum cannot depend on
	// another argument. That is why each line carries the family it was trained for: the caller
	// pairs them, and the Agent refuses a pairing that cannot work rather than generating a
	// picture in which the LoRA silently did nothing.
	if len(offer.Loras) > 0 {
		enum := make([]string, 0, len(offer.Loras))
		var lines []string
		for _, l := range offer.Loras {
			enum = append(enum, l.Name)
			line := l.Name
			if l.BaseModel != "" {
				line += " (" + l.BaseModel + ")"
			}
			if l.Description != "" {
				line += " — " + l.Description
			}
			// The trigger words ride the schema rather than an answer to a second tool, because
			// the moment they are needed is while the prompt is being written and a caller that
			// has to fetch them can forget to: an adapter applied without its trigger loads,
			// costs the whole generation, and changes nothing visible. Cheap here, too — this
			// block exists only for a deployment that enabled a LoRA at all, and grows by one
			// short clause per adapter.
			// Comma-separated, while the LINES are joined by " / ": a word list using the same
			// separator as the list of adapters would leave no boundary between one adapter's
			// last trigger and the next adapter's name.
			if len(l.TrainedWords) > 0 {
				line += " — triggers: " + strings.Join(l.TrainedWords, ", ")
			}
			lines = append(lines, line)
		}
		props["loras"] = map[string]any{
			// The cap is a literal for the same reason the op names are: this package cannot
			// import internal/imagegen, where comfyMaxLoras enforces it for real. This one only
			// saves a caller the round trip.
			"type": "array", "maxItems": 4,
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name":   map[string]any{"type": "string", "enum": enum},
					"weight": map[string]any{"type": "number", "minimum": 0, "maximum": 2, "description": "How strongly to apply it, 0-2. Omit it to get the strength the adapter's author published, which is the right answer unless the picture says otherwise"},
				},
				"required": []string{"name"},
			},
			"description": "Fine-tunes to apply on top of the checkpoint, in order. **Leave it unset unless the user asked for that look** — each one is a style, not an improvement. " +
				"A LoRA only works on the checkpoint family it was trained for (named in brackets below); asking for a mismatched pair is refused by name, so pick one whose family matches the model you chose. " +
				"A line's `triggers:` are the words that adapter was trained on: put one in `prompt` yourself, or it loads and changes nothing. " +
				"Available: " + strings.Join(lines, " / ")}
	}
	return map[string]any{
		"name": "generate_image",
		"description": "Agent Fleet: generate an image from a prompt and return the FILE PATH of what was produced. " +
			imageGenRoutesNote(offer) +
			"What comes back is the path, not the image, so open it with your own image-reading means only when " +
			"you actually have to look at the picture (an image is several MB, and embedding it in the result " +
			"crowds the context of every later turn). " +
			"What is produced outlives the conversation and can also be opened from the Console's file viewer. " +
			"**size / background / count are requests, not guarantees.** What actually happened is in the returned " +
			"warnings (ask for 1024x1024 and 1254x1254 can come back). " +
			"Do not ignore warnings and then explain as if the size matched. For the same reason there is no point " +
			"regenerating because the dimensions differ (it will not change). " +
			"**One call really costs money** - on an external service's route it spends the user's plan quota " +
			"(images burn it several times faster than text), and on a fleet-hosted engine it wakes a GPU box. " +
			"Do not fire off several to see, and do not hammer it for small adjustments. " +
			"The prompt is sent to an image service outside this container (the destination is in the returned " +
			"provider and destination; a fleet engine means the fleet's own box), so keep confidential " +
			"information out of it.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": props,
			"required":   []string{"prompt"},
		},
	}
}

// imageGenRoutesNote spells out WHICH image service each offered route reaches, and which one
// this session does not get.
//
// Without it the tool names only CLIs. A codex session whose single route was `agy` had no
// wording anywhere that said "Gemini" — not in the description, and not in an enum, because a
// one-entry offer drops the `provider` parameter entirely — and answered a request to compare
// the two with "the Gemini route is not available in this session" while holding it (measured
// 2026-09-08). The same silence hides the other half: that the missing service is the one the
// session's OWN built-in image tool produces, which is what makes such a comparison possible at
// all.
func imageGenRoutesNote(offer imageGenOffer) string {
	routes := make([]string, 0, len(offer.Providers))
	for _, id := range offer.Providers {
		if s := offer.Services[id]; s != "" {
			routes = append(routes, id+" = "+s)
		} else {
			routes = append(routes, id)
		}
	}
	note := "**Image services reachable from this tool**: " + strings.Join(routes, " / ") + ". "
	if len(offer.Providers) == 1 {
		note += "That is the only choice, so this route is always the one used. "
	}
	if id := offer.SelfExcluded; id != "" {
		what := "the route through " + id
		if s := offer.Services[id]; s != "" {
			what = s
		}
		note += "**This is a " + id + " session, so " + what + " alone is not reachable from this tool** - " +
			"the exclusion exists so the same CLI is not started a second time and the same plan paid twice, " +
			"not because that service is unavailable. Use your own built-in image generation when you need it " +
			"(to put both side by side, call the built-in tool once and this tool once). "
	}
	return note
}

// imageGenOffer is what THIS session may be told about generate_image: which providers it is
// allowed to name, and the vocabulary those providers between them support.
// mcpImageGenDefaultMaxInputs is what the `inputs` array was bounded by before any Agent reported
// a ceiling, and is what an older one still gets. It is not a capability claim — every provider
// refuses what it cannot take — so it stays where it was rather than being narrowed to the
// smallest family: lowering it would withhold an argument that works on the routes that have
// always taken five.
const mcpImageGenDefaultMaxInputs = 5

func maxInputsOrDefault(n int) int {
	if n <= 0 {
		return mcpImageGenDefaultMaxInputs
	}
	return n
}

type imageGenOffer struct {
	// Providers is every provider this session may use, in the effective order. The first is
	// what an unspecified `provider` routes to.
	Providers []string
	// Ops and AspectRatios are the UNION over Providers. A union rather than the first
	// provider's own list, because the caller can now name any of them — and because even
	// without naming one, auto already routes an op only the second provider supports TO that
	// provider (chooseImageProviders filters by op). Advertising only the first one's ops hid
	// that. What a NAMED provider cannot do is refused by name at call time, and an
	// unhonourable aspect ratio comes back in warnings, so the union promises nothing false.
	Ops, AspectRatios []string
	// Models is the UNION of every offered provider's checkpoints (ADR 0072 decision 5, phase
	// P2), by the same reasoning as Ops/AspectRatios above. In practice at most one ready
	// provider ever has more than a single entry (the fleet's own image engine, sdcpp XOR
	// comfy — 60-engines.yaml never runs both), so this is rarely a real union in fact, but it
	// is still the right SHAPE: a session offered two model-bearing providers must not have one
	// silently hidden.
	Models []mcpImageGenModel
	// Loras is the union of every offered provider's fine-tunes (ADR 0072 decision 5, phase P3),
	// by the same reasoning as Models.
	Loras []mcpImageGenLora
	// Seed is true when ANY offered provider takes a pinned seed. A union like the rest: the
	// argument is advertised when it reaches something, and a route that ignores it says so in
	// the result's warnings rather than dropping it silently.
	Seed bool
	// Negative is true when ANY offered provider has a model that samples with a negative
	// branch, by the same union rule as Seed.
	Negative bool
	// Strength is true when ANY offered provider lets the caller say how much of the input
	// picture an edit changes, by the same union rule as Seed.
	Strength bool
	// MaxInputs is the LARGEST number of reference pictures any offered provider reads (ADR 0094
	// decision 5), by the same union rule as Seed. It bounds the `inputs` array in the schema,
	// which is a connect-time snapshot with no provider or model chosen — so a union is the only
	// honest ceiling, and what one provider or checkpoint actually takes is refused by name at
	// call time.
	MaxInputs int
	// Samplers and Schedulers are the union of the offered providers' own allow-lists, and their
	// presence is also what says `params` reaches anything at all: only a route that BUILDS the
	// sampler graph has names to send, so an empty pair is exactly the case where the argument
	// would be a knob attached to nothing.
	//
	// A union of NAMES rather than a boolean, unlike Seed and the rest, because the tool has to
	// put them in an enum — and a name this binary will not send must not appear there, or the
	// schema promises something the Agent refuses one round trip later.
	Samplers, Schedulers []string
	// Services maps a provider id to the image service it reaches, for EVERY ready provider —
	// including the one dropped below, which the description has to be able to name.
	Services map[string]string
	// SelfExcluded is the provider id left out for being this session's own CLI, "" when none
	// was. The tool says so rather than staying silent: a session that cannot see why a service
	// is missing reports it as unavailable, and its own built-in tool — the one thing that can
	// still produce that picture — goes unused.
	SelfExcluded string
}

// mcpImageGenAdvertise decides whether THIS session is offered generate_image, and with what.
// ok=false means the tool is simply absent from tools/list.
//
// The rule (ADR 0069 decision 8): a session never gets a route that drives its OWN CLI — no
// codex provider for a Codex session, no agy provider for an agy session. That session already
// has the CLI's built-in image tool, and going out through a second process of the same CLI
// would double the cost for nothing. It is applied per PROVIDER rather than to the effective one
// only, because a session can now name a provider: a Codex session is offered agy and not codex,
// and the tool disappears entirely only when nothing is left. Because the rule is evaluated at
// list time, a changed order or a new login needs no re-materialize.
//
// It costs one loopback GET per tools/list, and only for users who turned the feature on. An
// unreachable Agent means the tool could not work anyway, so it is not advertised — better
// than advertising a tool whose every call fails.
func mcpImageGenAdvertise() (offer imageGenOffer, ok bool) {
	if !mcpImageGenEnabled {
		return offer, false
	}
	self, err := mcpListOwningSession()
	if err != nil {
		// Without a session name the Agent cannot key the output directory or the usage row,
		// so the tool has nowhere to put its result — unless the call will name it: tools/list
		// carries no caller stamp, but a child shared by several live Managed opencode sessions
		// is offered the tool on their behalf and each call's stamp decides whose it is (#989).
		// Any one that is not a studio session stands in for the list; they are all one kind.
		for _, name := range mcpStampedFolderSessions() {
			if !studioBoundSession(name) {
				self = name
				break
			}
		}
		if self == "" {
			return offer, false
		}
	} else if studioBoundSession(self) {
		// A studio session makes pictures with the person's button, not this tool (ADR 0100
		// decision 3). Leaving it out is only how it looks; mcpGenerateImage checks again, because
		// the call side trusts the tools/list the client last saw, which can predate the binding.
		return offer, false
	}
	st, err := agentImageGenStatus(self)
	if err != nil || !st.Enabled || !st.Ready {
		return offer, false
	}
	ready := st.Providers
	if len(ready) == 0 && st.Provider != "" {
		// An Agent that predates the per-provider list (this child can outlive an Agent update)
		// still answers with the effective one in the flat fields. Fall back to it rather than
		// dropping the tool: one provider is what the feature shipped with.
		ready = []mcpImageGenProvider{{ID: st.Provider, Service: st.Service, Model: st.Model, Ops: st.Ops, AspectRatios: st.AspectRatios}}
	}
	seenOp, seenRatio, seenModel := map[string]bool{}, map[string]bool{}, map[string]bool{}
	seenLora := map[string]bool{}
	seenSampler, seenScheduler := map[string]bool{}, map[string]bool{}
	offer.Services = map[string]string{}
	for _, p := range ready {
		if p.Service != "" {
			offer.Services[p.ID] = p.Service
		}
		if p.ID == st.Kind {
			// This session's own CLI — it already has the built-in tool. Recorded, not just
			// skipped, so the description can say which service went missing and why.
			offer.SelfExcluded = p.ID
			continue
		}
		offer.Providers = append(offer.Providers, p.ID)
		for _, op := range p.Ops {
			if !seenOp[op] {
				seenOp[op] = true
				offer.Ops = append(offer.Ops, op)
			}
		}
		for _, r := range p.AspectRatios {
			if !seenRatio[r] {
				seenRatio[r] = true
				offer.AspectRatios = append(offer.AspectRatios, r)
			}
		}
		for _, m := range p.Models {
			if !seenModel[m.ID] {
				seenModel[m.ID] = true
				offer.Models = append(offer.Models, m)
			}
		}
		for _, l := range p.Loras {
			if l.Name != "" && !seenLora[l.Name] {
				seenLora[l.Name] = true
				offer.Loras = append(offer.Loras, l)
			}
		}
		offer.Seed = offer.Seed || p.Seed
		offer.Negative = offer.Negative || p.Negative
		offer.Strength = offer.Strength || p.Strength
		if p.MaxInputs > offer.MaxInputs {
			offer.MaxInputs = p.MaxInputs
		}
		for _, s := range p.Samplers {
			if !seenSampler[s] {
				seenSampler[s] = true
				offer.Samplers = append(offer.Samplers, s)
			}
		}
		for _, s := range p.Schedulers {
			if !seenScheduler[s] {
				seenScheduler[s] = true
				offer.Schedulers = append(offer.Schedulers, s)
			}
		}
	}
	if len(offer.Providers) == 0 || len(offer.Ops) == 0 {
		return imageGenOffer{}, false
	}
	return offer, true
}

// imageGenTools is generate_image, its schema built from offer. The call side is refused without
// --image-gen for the same reason as the peer tools: the advertised set is the scope boundary,
// and a guessed name in tools/call must not reach a route that spends the user's plan quota.
func imageGenTools(offer imageGenOffer) []toolSpec {
	return []toolSpec{{
		name:    mcpToolGenerateImage,
		session: func() map[string]any { return imageGenSchema(offer) },
		refuse: func() string {
			if !mcpImageGenEnabled {
				return "このセッションは画像生成を許可されていません（設定 > エージェント）"
			}
			return ""
		},
		handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
			return mcpGenerateImage(req, imageGenArgs{
				op: a.Op, provider: a.Provider, prompt: a.Prompt, size: a.Size,
				aspectRatio: a.AspectRatio, background: a.Background, count: a.Count,
				inputs: a.Inputs, mask: a.Mask, model: a.Model, loras: a.Loras, seed: a.Seed,
				negativePrompt: a.NegativePrompt, strength: a.Strength, params: a.Params,
			})
		},
	}}
}

// mcpStdioImageGenTools is the image generation family's session list for offer.
func mcpStdioImageGenTools(offer imageGenOffer) []map[string]any {
	return sessionTools(imageGenTools(offer))
}
