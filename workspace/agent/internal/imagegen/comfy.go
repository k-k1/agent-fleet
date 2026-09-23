package imagegen

// comfy — the fleet's own image engine (ComfyUI) as an imagegen provider (ADR 0072 decision 4,
// phase P2).
//
// Unlike sdcpp, this is not a one-request-in, one-answer-out OpenAI-compatible call: ComfyUI's
// native API is async by design (POST /prompt returns a queue id at once; the picture is fetched
// once /history/<id> reports it done, and the bytes come from a THIRD call, GET /view). That
// shape is exactly why decision 4 makes ComfyUI the image role's long-term answer over sd-server
// — a synchronous /v1/images/generations request that takes longer than the ingress's 60-second
// idle timeout is unservable (the "60-second規則"), while three short round trips never sit on
// one open connection long enough to hit it, however long the generation between them takes.
//
// The engine gateway (control-plane/engine_gateway.go's dial) still holds the FIRST of those
// three calls while a stopped engine wakes, the same way it holds sdcpp's single call — so the
// retry-on-503-engine_waking loop below is a straight port of sdcpp.go's send(), reused rather
// than reinvented. /prompt is where a COLD engine is met, but it is not the only call that can
// meet a waking one: the box can be replaced between the submit and the poll that follows, and
// a caller was measured receiving `/history answered 503 … retry` from a poll that then retried
// nothing (ADR 0072 欠落 9). So awaitHistory waits a retryable answer out too.
//
// Every checkpoint switch — including the very first request against a just-started engine — is
// EBS-read time on top of generation (measured 1-2.5 minutes, ADR 0072 "実測で解けた点" 5), which
// is why this file is careful to warn about it (comfySwitchWarning) rather than let a caller read
// a slow answer as a broken one.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type comfyProvider struct {
	// key is this provider's id everywhere outside this file — the images row's own key (ADR
	// 0082 decision 1). Empty in a hand-built test double, which is why ID() falls back to the
	// bare kind name rather than an empty string.
	key    string
	lookup func(ctx context.Context) (EngineConn, bool)
	client *http.Client
}

func newComfyProviderFor(key string) *comfyProvider {
	return &comfyProvider{key: key, lookup: engineLookupFor(key), client: engineClient}
}

func (p *comfyProvider) ID() string {
	if p.key != "" {
		return p.key
	}
	return ProviderComfy
}

// Ready follows sdcpp's own rule exactly: "this deployment has this engine and we hold a token
// for it", never "the engine is up". See sdcppProvider.Ready for why that is the honest answer.
func (p *comfyProvider) Ready(ctx context.Context) bool {
	_, ok := p.conn(ctx)
	return ok
}

func (p *comfyProvider) conn(ctx context.Context) (EngineConn, bool) {
	if p.lookup == nil {
		return EngineConn{}, false
	}
	c, ok := p.lookup(ctx)
	if !ok || strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Token) == "" {
		return EngineConn{}, false
	}
	return c, true
}

// DefaultModel is what a request naming no model gets (ADR 0072 decision 7): whatever the
// Control Plane last saw this engine actually answer with — free, because it is already loaded
// — falling back to the catalogue's first declared model only when nothing is known to be warm
// yet (a just-started engine, or a CP that restarted and lost its in-memory state).
func (p *comfyProvider) DefaultModel() string {
	c, ok := p.conn(context.Background())
	if !ok {
		return ""
	}
	if c.Warm != "" {
		return c.Warm
	}
	if len(c.Models) > 0 {
		return c.Models[0]
	}
	return ""
}

// Caps is per (provider, model) as everywhere else in this package (ADR 0069 decision 5) — but
// here the model argument is the point of the whole phase: comfy is the first provider in this
// package for which Caps genuinely differs across MULTIPLE models on the same running engine,
// because switching which one answers is exactly what decision 4 buys.
//
// Ops, MaxInputs and Strength are FAMILY attributes (ADR 0094 decisions 2/3/5) — every family
// through krea2 answers the same way they always did (all three ops, one input, strength on an
// edit); Qwen-Image-Edit is the first to differ (edit only, denoise fixed at 1, decision 2's実測
// C). A model with no declared family gets the old, permissive shape: the request is refused
// later by errComfyFamilyNotDeclared with a better message than a capability flag ever could.
//
// model == "" answers with capsUnion instead of resolving DefaultModel() itself — ADR 0094
// decision 11: this is the value both HandleStatus's advertised set (http.go's `p.Caps("")`) and
// chooseImageProviders' candidate filter read for a request naming no model, and the warm
// default's own answer would make an op only SOME rows offer disappear the moment an edit-only
// checkpoint happens to be warm.
func (p *comfyProvider) Caps(model string) Caps {
	conn, _ := p.conn(context.Background())
	if strings.TrimSpace(model) == "" {
		return p.capsUnion(conn)
	}
	family, ok := comfyFamilyFor(conn, model)
	ops, strength, maxInputs := comfyDefaultOps, true, 1
	if ok {
		ops, strength, maxInputs = comfyFamilyOps(family), comfyFamilyStrength(family), comfyFamilyMaxInputs(family)
	}
	return Caps{
		Ops:       ops,
		Sizes:     comfySizesFor(conn, model),
		MaxInputs: maxInputs,
		MaxCount:  comfyMaxBatch,
		Loras:     comfyLoraInfos(conn),
		// The one route where a seed reaches the sampler: it is this package that builds the
		// graph, so the seed is an input this file writes rather than a field a vendor API has
		// to expose (ADR 0069 follow-up, seed).
		Seed: true,
		// Per MODEL, because the answer really does differ between two checkpoints on the same
		// running engine — which is the case Caps was made per (provider, model) for.
		Negative: comfyModelTakesNegative(conn, model),
		Strength: strength,
		// The one route that builds the sampler graph, so the one route where steps, cfg, sampler
		// and scheduler are real (ADR 0081 decision 4). WHICH of the four a given family reads is
		// a per-request warning, not a capability flag — see comfyIgnoredParamWarnings.
		Params: true,
	}
}

// capsUnion is Caps("") — ADR 0094 decision 11: the union of every enabled model's own answer,
// not the warm default's alone. It is the value both the advertised set (http.go's HandleStatus,
// `caps := p.Caps("")`) and the candidate filter (imagegen.go's chooseImageProviders, through
// Run()'s capsOf) read for a request naming no model, and both break the same way without it: an
// edit-only checkpoint happening to be warm would make `op=generate` disappear from what this
// provider advertises AND from what it is offered candidacy for, sending a member's next
// text-to-image call to a provider that spends their own plan quota (measured once already for a
// DIFFERENT capability, ADR 0071 P1 — see imagegen.go's fallbackWarnings).
//
// A model named explicitly still goes through Caps(model) above and gets the strict, per-family
// answer — this union only ever widens what is ADVERTISED and CANDIDATE, never what one specific
// request may do (comfyStrengthRefusal and comfySizeRefusal resolve the family, not this union,
// before refusing).
func (p *comfyProvider) capsUnion(conn EngineConn) Caps {
	out := Caps{MaxCount: comfyMaxBatch, Loras: comfyLoraInfos(conn), Seed: true, Params: true}
	if len(conn.Models) == 0 {
		// No catalogue at all: the old, permissive defaults, so an engine with no rows yet still
		// advertises something rather than nothing.
		out.Ops, out.Strength, out.MaxInputs = comfyDefaultOps, true, 1
		return out
	}
	seenOp := map[Op]bool{}
	for _, id := range conn.Models {
		family, ok := comfyFamilyFor(conn, id)
		ops, strength, maxInputs := comfyDefaultOps, true, 1
		if ok {
			ops, strength, maxInputs = comfyFamilyOps(family), comfyFamilyStrength(family), comfyFamilyMaxInputs(family)
		}
		for _, op := range ops {
			if !seenOp[op] {
				seenOp[op] = true
				out.Ops = append(out.Ops, op)
			}
		}
		if strength {
			out.Strength = true
		}
		if maxInputs > out.MaxInputs {
			out.MaxInputs = maxInputs
		}
		if comfyModelTakesNegative(conn, id) {
			out.Negative = true
		}
		if s := comfySizesFor(conn, id); len(s) > 0 {
			out.Sizes = appendMissing(out.Sizes, s)
		}
	}
	return out
}

// appendMissing appends every element of add not already in have, preserving have's order and
// add's order within the appended tail — used to union several models' own size lists without
// naming any one of them twice.
func appendMissing(have, add []string) []string {
	seen := make(map[string]bool, len(have))
	for _, s := range have {
		seen[s] = true
	}
	for _, s := range add {
		if !seen[s] {
			seen[s] = true
			have = append(have, s)
		}
	}
	return have
}

// comfyDefaultOps is every op the fleet's ComfyUI provider offered before ADR 0094 made Ops a
// family attribute, and stays the answer for a model with no declared family (or none at all,
// p.Caps("") on an engine with no catalogue) — a request against one is refused before a graph
// exists anyway, so there is no capability to narrow.
var comfyDefaultOps = []Op{OpGenerate, OpEdit, OpInpaint}

// comfyFamilyOps is ADR 0094 decision 3: which ops a family's template can build at all. Every
// family through krea2 has an image-to-image path (LoadImage + VAEEncode, plus
// SetLatentNoiseMask for a mask) alongside its plain generate — Qwen-Image-Edit has NO GENERATE.
// It has no path that starts from an empty latent: TextEncodeQwenImageEditPlus with no image input
// is not a documented use of the node.
//
// `inpaint` was unclaimed on the 2509/2511 families until somebody ran it, which is decision 3's
// own rule and the one ADR 0072 paid for on SD3.5. It was run on 2026-09-21 (実測 G and I) and
// their rows claim it; qwen-image-2.1 still does not, for the same reason they did not.
//
// qwen-image-2.1 is why this reads a declared list rather than "is it an instruction edit"
// (ADR 0098): it is the first family that instruction-edits AND generates from a prompt alone, so
// those two stopped being the same question.
func comfyFamilyOps(family comfyFamily) []Op {
	if r, ok := comfyFamilyRowFor(family); ok && len(r.Ops) > 0 {
		return r.Ops
	}
	return comfyDefaultOps
}

// comfyFamilyInstructionEdit is "this family edits by instruction" — the picture conditions the
// sampler and the denoise is fixed at 1 — which is the axis ADR 0094 decision 2 turns on, not the
// family name and not the version.
//
// 🔴 It reads FixedDenoiseEdit and NOT the wiring pointer, and the two are different questions
// since ADR 0098: both Qwen-Image-Edit topologies go through comfyGraphQwenImageEdit, while
// qwen-image-2.1 edits the same WAY through a builder of its own. Reading the pointer here would
// hand that family a `strength` its sampler cannot spend, which is 実測 C's failure — the picture
// comes back unedited with no error and no warning.
func comfyFamilyInstructionEdit(family comfyFamily) bool {
	r, ok := comfyFamilyRowFor(family)
	return ok && r.FixedDenoiseEdit
}

// comfyFamilyStrength is ADR 0094 decision 2: whether Request.Strength reaches this family's
// sampler at all. Qwen-Image-Edit's denoise is fixed at 1 by construction
// (comfyGraphQwenImageEdit) — instruction editing conditions the sampler through the picture
// itself, not through how far a partial denoise is allowed to travel — so a caller's strength has
// nowhere to go. 実測 C is the same failure this exists to prevent: the same request at denoise
// 0.6 came back unedited, with no error and no warning.
func comfyFamilyStrength(family comfyFamily) bool {
	return !comfyFamilyInstructionEdit(family)
}

// comfyFamilyMaxInputs is ADR 0094 decision 5: how many reference pictures a family's template
// can actually READ. Every family through krea2 has one LoadImage and nowhere to put a second, so
// the answer stays 1 for them; the instruction-edit families wire
// TextEncodeQwenImageEditPlus's image2 and take 2 (P3).
//
// 🔴 The number and the code path move together — decision 5's own 「宣言と経路は同じフェーズに
// 入れる」. Raising this without widening the upload and the template is not a smaller version of
// the feature: the extra pictures pass comfyCheckInputs, are never wired, and the caller gets a
// picture that ignored them with no warning anywhere (the same shape of lie as 実測 C).
//
// 3 rather than 2 because three were MEASURED (実測 F, 2026-09-21, 2511 on a 22,000-rung box):
// asked for the plant from picture 2 and the rubber duck from picture 3 side by side, both arrived
// with their colour and shape intact and nothing else in the scene moved. It is 3 and not more
// because that is where the node stops — TextEncodeQwenImageEditPlus takes image1..image3.
//
// 🔴 The number was NOT raised on the strength of "the wiring is a loop, so more must work". That
// inference is what decision 3 forbade for inpaint, and until the run above this function
// deliberately answered 2 with image3 unwired-by-absence.
func comfyFamilyMaxInputs(family comfyFamily) int {
	if r, ok := comfyFamilyRowFor(family); ok && r.RefInputs > 0 {
		return r.RefInputs
	}
	return 1
}

// comfyFamilyHasNoSizes is ADR 0094 decision 4: Qwen-Image-Edit's output size is decided by
// FluxKontextImageScale from the INPUT PICTURE's own aspect ratio, so no size a request or a
// catalogue row could name would reach the sampler at all. comfySizesFor checks this BEFORE the
// row's own declared list — the one family where the family's answer wins over the row's, because
// the row's list would otherwise offer a control that silently does nothing.
//
// 🔴 Read off the op list and not off "does it edit by instruction" (ADR 0098). The two agreed
// while every instruction-edit family was also edit-only; qwen-image-2.1 is not, and its GENERATE
// path fills an EmptyLatentImage from whatever size is chosen. Answering true for it would delete
// the size control from the one op that reads one. The derivation is the real statement of
// decision 4: a size only ever reaches an EmptyLatentImage, so a family with no generate path has
// nowhere to put one.
func comfyFamilyHasNoSizes(family comfyFamily) bool {
	for _, op := range comfyFamilyOps(family) {
		if op == OpGenerate {
			return false
		}
	}
	return true
}

// comfyFamilyKnobs is the subset of `steps cfg sampler scheduler negative strength` a family's
// template actually reads. It is the single declaration of ADR 0081 decision 4's table (widened
// by ADR 0094 decision 12 to include `strength`), and the status route serves it to the Console
// (decision 5) so the form greys a field out on the AGENT's word rather than on a second copy of
// this table that can disagree with the graphs.
//
// The sampler half is the row's own (comfyFamilyRow.SamplerKnobs, declared next to the recipe it
// describes); `negative` and `strength` are appended here from the same answers Caps gives, so
// the form and the capability cannot disagree about either.
//
// 🔴 The copy is not ceremony. Six rows share one backing array (comfyKnobsSampled), and
// appending to a slice this function does not own writes `negative` into every one of them the
// day any row is declared with spare capacity. Today's literals have none, which is exactly what
// makes it the kind of bug that arrives with an unrelated edit.
func comfyFamilyKnobs(family comfyFamily) []string {
	row, _ := comfyFamilyRowFor(family)
	knobs := append([]string(nil), row.SamplerKnobs...)
	if comfyFamilyTakesNegative(family) {
		knobs = append(knobs, "negative")
	}
	if comfyFamilyStrength(family) {
		knobs = append(knobs, "strength")
	}
	return knobs
}

// comfyModelKnobs is comfyFamilyKnobs for ONE ROW: the family's list, minus the negative prompt
// when that row is declared at cfg 1 and cancels it. The form draws its fields from this, and
// Caps.Negative answers the same question through comfyModelTakesNegative — a field offered for
// a value the capability says is ignored is the pair disagreeing in front of the member.
func comfyModelKnobs(conn EngineConn, family comfyFamily, model string) []string {
	knobs := comfyFamilyKnobs(family)
	if comfyModelTakesNegative(conn, model) {
		return knobs
	}
	out := knobs[:0:0]
	for _, k := range knobs {
		if k != "negative" {
			out = append(out, k)
		}
	}
	return out
}

func comfyFamilyReadsKnob(family comfyFamily, knob string) bool {
	for _, k := range comfyFamilyKnobs(family) {
		if k == knob {
			return true
		}
	}
	return false
}

// comfyIgnoredParamWarnings says out loud which of the caller's own knobs this family does not
// read (ADR 0081 decision 4: "what a family ignores is reported, not swallowed").
//
// It reports the CALLER's values only. A catalogue row that declares a cfg for flux1 is the
// administrator's business and was already written before this request existed; a member who
// typed 7 into a cfg box and got a picture sampled without it has been told nothing unless this
// says so.
func comfyIgnoredParamWarnings(family comfyFamily, p *EngineParams) []string {
	if p == nil {
		return nil
	}
	var out []string
	if p.CFG > 0 && !comfyFamilyReadsKnob(family, "cfg") {
		out = append(out, fmt.Sprintf("cfg=%g was not applied: the %s family folds its guidance into the conditioning"+
			" (a distilled path sampled at a fixed 1), so a guidance scale has nowhere to go here", p.CFG, family))
	}
	if strings.TrimSpace(p.Scheduler) != "" && !comfyFamilyReadsKnob(family, "scheduler") {
		out = append(out, fmt.Sprintf("scheduler=%s was not applied: the %s family's schedule is derived from the"+
			" picture's size (Flux2Scheduler), not chosen by name", strings.TrimSpace(p.Scheduler), family))
	}
	return out
}

// comfyModelTakesNegative answers whether a negative prompt can move THIS MODEL's picture. A
// model whose family is not declared answers false: the request will be refused before a graph
// exists, and "yes it would have been honoured" is not a useful thing to have said.
//
// 🔴 The family is necessary and not sufficient. A guided template still samples at whatever cfg
// the catalogue row declares, and at cfg 1 classifier-free guidance is `uncond + 1*(cond -
// uncond)` — cond exactly, whatever is wired into the negative branch. That is not a corner case
// any more: the distilled member of a guided family is the NORMAL row for two of the seven
// families now (Krea 2 Turbo declares cfg 1, Anima-Turbo does too), and answering with the
// family alone would tell a member their negative prompt reaches a picture it cannot touch.
func comfyModelTakesNegative(conn EngineConn, model string) bool {
	family, ok := comfyFamilyFor(conn, model)
	if !ok || !comfyFamilyTakesNegative(family) {
		return false
	}
	return comfyFamilyRecipeFor(family).with(conn.Params[model]).CFG != 1
}

// comfyFamilyTakesNegative is which of the templates a negative prompt can actually move — the
// row's own Guided, where the reason each family answers as it does is written beside its recipe.
//
// ⚠️ It is the family's TEMPLATE, not the answer a member gets: anima and krea2 are guided
// because their graphs encode a real negative, while their distilled variants (Anima-Turbo,
// Krea 2 Turbo) declare cfg 1 and cancel it anyway. comfyModelTakesNegative is what puts the
// two facts together, and it is the one every caller asks.
func comfyFamilyTakesNegative(family comfyFamily) bool {
	r, ok := comfyFamilyRowFor(family)
	return ok && r.Guided
}

// comfyNegativeFor composes the negative prompt one request samples against, out of the three
// places that get a say (ADR 0072 follow-up, negative prompts):
//
//	the catalogue row's own default  +  the caller's  +  the engine's administrator list
//
// ADDED, not overridden, in that order. A caller naming one thing to exclude does not mean "and
// stop excluding everything the checkpoint's publisher recommends", and the administrator's list
// is last because it is the one part no request may drop.
//
// Empty when nobody said anything, and the TEMPLATE — not this — decides what an empty negative
// means for its family (comfyGraphSDXL keeps the fixed default it was measured with).
func comfyNegativeFor(conn EngineConn, model string, req Request) string {
	parts := make([]string, 0, 3)
	for _, s := range []string{conn.Negatives[model], req.NegativePrompt, conn.NegativeAlways} {
		if v := strings.TrimSpace(s); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// comfyNegativeIgnoredWarning is said when a family with no negative branch was asked to exclude
// something that the CALLER did not type: the catalogue's default for this model, and the
// deployment's own exclusion list. The caller's own negative_prompt is reported by the core
// against Caps.Negative (requestWarnings), and saying the same thing twice in one result teaches
// the reader to skim warnings.
//
// 🔴 That split only holds because requestWarnings asks Caps about the RESOLVED model
// (imagegen.go's Run, jobs.go's finish — both read res.Model, not the request's own, since ADR
// 0094 decision 11 made Caps("") a union and the request may have named no model at all). Ask it
// with an empty or pre-resolution model instead and the caller's own negative_prompt on a cfg-1
// warm row would be dropped with NO warning from anywhere — the "most expensive kind of lie"
// Caps.Negative's own doc comment warns about. That gap lived here once (a comfy-only, half of
// the picture fix); putting it in requestWarnings instead means EVERY provider gets the same
// guarantee, not only this one.
//
// It is said even though nobody is at fault, because the administrator's list silently not
// applying is precisely the failure this whole path exists to prevent.
//
// It reads the MODEL and not just the family for the reason comfyModelTakesNegative does: a row
// of a guided family declared at cfg 1 drops the administrator's list exactly as silently as a
// distilled family does, and it is the same warning either way — only the reason differs.
func comfyNegativeIgnoredWarning(conn EngineConn, model string, family comfyFamily) string {
	if comfyModelTakesNegative(conn, model) {
		return ""
	}
	var what []string
	if strings.TrimSpace(conn.Negatives[model]) != "" {
		what = append(what, "this model's own negative prompt")
	}
	if strings.TrimSpace(conn.NegativeAlways) != "" {
		what = append(what, "the keywords this deployment excludes")
	}
	if len(what) == 0 {
		return ""
	}
	if comfyFamilyTakesNegative(family) {
		return fmt.Sprintf("%s was not applied: this model is declared at cfg 1, where guidance is"+
			" `cond` exactly and the %s family's negative branch cancels out, so nothing can be"+
			" excluded from it", strings.Join(what, " and "), family)
	}
	return fmt.Sprintf("%s was not applied: the %s family samples without a negative branch"+
		" (a distilled model at cfg 1), so nothing can be excluded from it",
		strings.Join(what, " and "), family)
}

// comfyLoraInfos is every LoRA the catalogue enables for this engine (ADR 0072 decision 5, phase
// P3), NOT the ones that fit `model` — see Caps.Loras for why an enum may not depend on another
// argument. The family goes out with each entry so the caller can pair them itself; a pairing
// that does not fit is refused by comfyResolveLoras when the request arrives.
func comfyLoraInfos(conn EngineConn) []LoraInfo {
	if len(conn.Loras) == 0 {
		return nil
	}
	out := make([]LoraInfo, 0, len(conn.Loras))
	for _, l := range conn.Loras {
		if strings.TrimSpace(l.ID) == "" || strings.TrimSpace(l.File) == "" {
			continue // a row with no id or no file cannot be named or loaded
		}
		out = append(out, LoraInfo{Name: l.ID, Description: l.Description, BaseModel: l.BaseModel})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Models implements ModelLister (ADR 0072 decision 5, phase P2): every checkpoint the catalogue
// currently enables for this engine, in the same order EngineConn.Models declares them (the
// selected/default one first — see engineImageModelIDs in the Agent's engines.go), each marked
// warm when it is the one decision 7's warm_model names.
func (p *comfyProvider) Models(ctx context.Context) []ModelInfo {
	conn, ok := p.conn(ctx)
	if !ok {
		return nil
	}
	out := make([]ModelInfo, 0, len(conn.Models))
	for _, id := range conn.Models {
		out = append(out, ModelInfo{ID: id, Label: conn.Labels[id],
			Description: conn.Descriptions[id], Warm: id != "" && id == conn.Warm})
	}
	return out
}

// Studio implements StudioLister (ADR 0081 decision 5): the member-facing catalogue, built from
// the rows this provider already receives on GET /internal/engine/catalog.
//
// 🔴 A model the engine could not actually run is NOT listed. The catalogue already withholds it
// from generation — no declared family means no workflow template, and a family whose files are
// not declared fails inside comfyBuildGraph — so offering it in a form would be offering a
// button that produces an error message after a cold start. A disabled entry with a tooltip is
// the administrator's screen, not the member's.
func (p *comfyProvider) Studio(ctx context.Context) (Studio, bool) {
	conn, ok := p.conn(ctx)
	if !ok {
		return Studio{}, false
	}
	out := Studio{
		Samplers:       comfySortedNames(comfySamplerNames),
		Schedulers:     comfySortedNames(comfySchedulerNames),
		NegativeAlways: conn.NegativeAlways,
		LoraWeightMax:  comfyMaxLoraWeight,
	}
	for _, id := range conn.Models {
		family, ok := comfyFamilyFor(conn, id)
		if !ok {
			continue // no declared family: there is no template, so there is nothing to offer
		}
		if _, err := comfyBuildGraph(family, resolveComfyFiles(conn.Files[id]), comfyParams{Prompt: "x"}); err != nil {
			continue // a file the family needs is not declared; the same refusal a request would get
		}
		lic := conn.Licenses[id]
		out.Models = append(out.Models, StudioModel{
			ID: id, Label: conn.Labels[id], Description: conn.Descriptions[id], Family: string(family),
			Sizes: comfySizesFor(conn, id), Params: comfyEffectiveDefaults(conn, family, id),
			Negative: conn.Negatives[id], Knobs: comfyModelKnobs(conn, family, id), Ops: comfyFamilyOps(family),
			MaxInputs:   comfyFamilyMaxInputs(family),
			Warm:        id != "" && id == conn.Warm,
			LicenseName: lic.Name, LicenseURL: lic.URL, SourceURL: lic.Source,
		})
	}
	for _, l := range conn.Loras {
		if strings.TrimSpace(l.ID) == "" || strings.TrimSpace(l.File) == "" {
			continue
		}
		out.Loras = append(out.Loras, StudioLora{
			Name: l.ID, Description: l.Description, BaseModel: l.BaseModel,
			TrainedWords: l.TrainedWords, Weight: l.Weight,
		})
	}
	return out, true
}

// comfyEffectiveDefaults is what will run when the member types nothing: the family's own recipe
// with the catalogue row laid over it, field by field — the same merge the template does, which
// is why it is that function and not a second reading of the same two sources.
func comfyEffectiveDefaults(conn EngineConn, family comfyFamily, model string) EngineParams {
	r := comfyFamilyRecipeFor(family).with(conn.Params[model])
	return EngineParams{Steps: r.Steps, CFG: r.CFG, Sampler: r.Sampler, Scheduler: r.Scheduler}
}

// comfySortedNames spells an allow-list for the wire. Sorted, because a map range would reorder
// it on every read and the status answer has to be the same bytes for the same state.
func comfySortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// comfySizesFor prefers the catalogue's own declaration (ADR 0072 decision 2) and falls back to
// the sizes this model's FAMILY was trained at (comfyDefaultSizes).
//
// A model whose family is not declared falls back to the megapixel list. It cannot generate at
// all until somebody declares one, so the list served for it decides nothing — and answering
// with SD1.5's 512 presets there would be a guess about an undeclared row, which is the thing
// decision 2 exists to prevent.
//
// 🔴 One family wins even over the row's OWN declaration (ADR 0094 decision 4,
// comfyFamilyHasNoSizes): Qwen-Image-Edit's output size is decided by FluxKontextImageScale from
// the input picture's aspect ratio, so a row's `sizes` there would offer a control that silently
// does nothing. That check runs BEFORE conn.Sizes[model] for exactly that reason.
func comfySizesFor(conn EngineConn, model string) []string {
	family, ok := comfyFamilyFor(conn, model)
	if ok && comfyFamilyHasNoSizes(family) {
		return nil
	}
	if s := conn.Sizes[model]; len(s) > 0 {
		return s
	}
	if !ok {
		return comfyMegapixelSizes
	}
	return comfySizesForFamily(family)
}

// comfyFamilyFor reads the catalogue's declared family for a model id. False when undeclared or
// unrecognised — comfy refuses rather than guessing a family from the id string the way sdcpp's
// legacy fallback does, because decision 2 exists precisely so a family is a declared fact, not
// something read off a naming convention that will eventually collide.
func comfyFamilyFor(conn EngineConn, model string) (comfyFamily, bool) {
	got := comfyFamily(strings.TrimSpace(conn.BaseModel[model]))
	for _, f := range comfyFamilies {
		if f == got {
			return f, true
		}
	}
	return "", false
}

// errComfyFamilyNotDeclared separates the two ways this fails, because the fix differs and only
// one of them looks wrong on the admin screen. NOTHING declared is a row the catalogue seeded
// (the seed cannot know a family) or one written before the Control Plane validated it. SOMETHING
// declared that names no template is almost always an upstream display name — "SDXL 1.0",
// "Flux.1 D" — which is what Hugging Face and Civitai publish and what the ingest path used to
// store; that row looks complete in the panel and fails only here.
func errComfyFamilyNotDeclared(model, declared string) error {
	if d := strings.TrimSpace(declared); d != "" {
		return fmt.Errorf("model %s declares the checkpoint family %q, which names no workflow template"+
			" — the catalogue's base_model has to be one of %s", model, d, comfyFamilyList())
	}
	return fmt.Errorf("model %s declares no checkpoint family, so there is no workflow template to build"+
		" — set the catalogue's base_model to one of %s", model, comfyFamilyList())
}

func errUnknownComfyFamily(family comfyFamily) error {
	return fmt.Errorf("no workflow template for checkpoint family %q", string(family))
}

// comfyMaxLoras bounds one request's chain. Each entry is a node ComfyUI loads a file for, and
// four already stacks more style than anyone can steer; the cap exists so a caller cannot turn
// one call into an unbounded pile of disk reads on a box the deployment pays for by the hour.
const comfyMaxLoras = 4

// comfyMaxBatch is how many pictures one graph may produce at once (ComfyUI's batch_size). It is
// faster per picture on a card with headroom and an out-of-memory five minutes into a cold start
// on one without, and nothing in a batch can be cancelled separately — which is why the pane's
// "count" means JOBS and this stays an advanced field.
const comfyMaxBatch = 4

// comfyMaxLoraWeight is decision 5's declared range, 0-2. LoraLoader itself accepts -100 to 100,
// which is a knob for someone watching the result, not for a model that cannot see the picture.
const comfyMaxLoraWeight = 2.0

// comfyResolveLoras turns the request's LoRA names into the chain a template renders, and is
// where ADR 0072's refusal lives (decision 5, レビュー決定 5): it is the AGENT that says no, not
// the Control Plane, because the pairing ends up inside a workflow graph and the gateway must
// not read request bodies to police one (decision 4's "素通し").
//
// The mismatch it refuses — an SD1.5 LoRA asked for on an SDXL checkpoint — has no failure of its
// own: the tensor names simply do not match, and the engine either warns and ignores them or
// produces a quietly degraded picture. Both reach the caller as "the LoRA did nothing", which is
// indistinguishable from a bug in the prompt. So it is refused before any GPU is woken.
func comfyResolveLoras(conn EngineConn, family comfyFamily, model string, want []LoraRef) ([]comfyLora, error) {
	if len(want) == 0 {
		return nil, nil
	}
	if len(conn.Loras) == 0 {
		return nil, fmt.Errorf("no LoRA is enabled on this engine, so %q cannot be applied"+
			" — enable one in the admin panel's model catalogue first", want[0].Name)
	}
	if len(want) > comfyMaxLoras {
		return nil, fmt.Errorf("%d LoRAs asked for, and this route applies at most %d in one request", len(want), comfyMaxLoras)
	}
	byName := map[string]EngineLora{}
	for _, l := range conn.Loras {
		byName[l.ID] = l
	}
	out := make([]comfyLora, 0, len(want))
	seen := map[string]bool{}
	for _, w := range want {
		l, ok := byName[w.Name]
		if !ok || strings.TrimSpace(l.File) == "" {
			return nil, fmt.Errorf("no LoRA named %q on this engine — the catalogue enables %s",
				w.Name, comfyLoraNameList(conn))
		}
		if seen[w.Name] {
			return nil, fmt.Errorf("LoRA %q asked for twice; name it once with the strength you want", w.Name)
		}
		seen[w.Name] = true
		if got := comfyFamily(strings.TrimSpace(l.BaseModel)); got != family {
			return nil, errComfyLoraFamilyMismatch(w.Name, l.BaseModel, model, family)
		}
		// Three answers, in this order: what the CALLER asked for, what the catalogue row
		// declares for this adapter, and 1. The caller wins because they are looking at the
		// picture; the row comes next because its author published a strength and the agent
		// naming a LoRA has no way to know it (ADR 0072 decision 5).
		weight := w.Weight
		if weight == 0 {
			weight = l.Weight
		}
		if weight == 0 {
			weight = 1 // nobody stated one — see LoraRef.Weight
		}
		if weight < 0 || weight > comfyMaxLoraWeight {
			return nil, fmt.Errorf("LoRA %q asked for at strength %g, and the range is 0-%g",
				w.Name, w.Weight, comfyMaxLoraWeight)
		}
		out = append(out, comfyLora{Name: l.File, Weight: weight})
	}
	return out, nil
}

// comfyTriggerWarnings names every applied LoRA whose trigger words are nowhere in the prompt.
//
// It is the other half of the family mismatch above, and the half that cannot be refused: the
// pairing is legal, the adapter loads, the generation is paid for in full — and the picture comes
// back looking like the one without it, because the words the adapter was trained to answer to
// were never said. There is no failure to observe, which is exactly why it has to be spoken here
// rather than left to the caller to notice.
//
// A warning and not an error, deliberately, for three reasons: some adapters genuinely need no
// trigger (their author publishes none, and those rows are skipped outright), a word may be
// reached through a synonym this substring test cannot see, and a caller who meant to run without
// the trigger — to measure what the adapter does on its own — must still be able to. ANY of the
// published words counts: they are alternatives, not a checklist.
func comfyTriggerWarnings(conn EngineConn, want []LoraRef, prompt string) []string {
	if len(want) == 0 {
		return nil
	}
	byName := map[string]EngineLora{}
	for _, l := range conn.Loras {
		byName[l.ID] = l
	}
	lower := strings.ToLower(prompt)
	var out []string
	for _, w := range want {
		l, ok := byName[w.Name]
		if !ok || len(l.TrainedWords) == 0 {
			continue
		}
		said := false
		for _, t := range l.TrainedWords {
			if t = strings.TrimSpace(t); t != "" && strings.Contains(lower, strings.ToLower(t)) {
				said = true
				break
			}
		}
		if said {
			continue
		}
		out = append(out, fmt.Sprintf(
			"LoRA %s answers to %s, and the prompt says none of them — it loaded and probably changed nothing;"+
				" put one in the prompt and generate again",
			l.ID, strings.Join(l.TrainedWords, " / ")))
	}
	return out
}

// errComfyLoraFamilyMismatch separates the two ways a pairing fails, because an operator fixes
// them differently: a LoRA that declares a DIFFERENT family was registered for other checkpoints
// and is being used on the wrong one, while a LoRA that declares NOTHING is a catalogue row
// nobody finished — and that row would otherwise be paired with anything at all.
func errComfyLoraFamilyMismatch(name, declared, model string, family comfyFamily) error {
	if d := strings.TrimSpace(declared); d != "" {
		return fmt.Errorf("LoRA %s was trained for the %s checkpoint family and %s is %s"+
			" — they cannot be combined; a mismatched LoRA does not fail, it quietly does nothing to the picture",
			name, d, model, string(family))
	}
	return fmt.Errorf("LoRA %s declares no checkpoint family, so there is no way to tell whether it fits %s (%s)"+
		" — set the catalogue's base_model to one of %s", name, model, string(family), comfyFamilyList())
}

// comfyLoraNameList spells the enabled LoRAs with the family each belongs to, because "that name
// does not exist" without the alternatives costs the caller another turn to find out what does.
func comfyLoraNameList(conn EngineConn) string {
	out := make([]string, 0, len(conn.Loras))
	for _, l := range conn.Loras {
		if l.BaseModel != "" {
			out = append(out, l.ID+" ("+l.BaseModel+")")
			continue
		}
		out = append(out, l.ID)
	}
	return strings.Join(out, ", ")
}

func errComfyMissingFile(family, role string) error {
	return fmt.Errorf("the %s checkpoint's catalogue entry has no %s file declared", family, role)
}

// comfySwitchWarning says out loud when a request is about to pay the checkpoint-switch cost
// (ADR 0072 "実測で解けた点" 5: 1-2.5 minutes of EBS re-read, on top of the actual generation) —
// a caller who asked for a cold checkpoint and waited two extra minutes deserves to be told WHY,
// the same reasoning that makes sdcpp's retry loop report `lastWaking` rather than staying mute.
// A heuristic, not a guarantee (another request could have changed what is warm in between), and
// silent when nothing is known to be warm at all — a just-started engine pays this cost on
// EVERY first request regardless of which model is asked for, so naming one as "the switch"
// would blame the wrong thing.
func comfySwitchWarning(conn EngineConn, model string) string {
	if conn.Warm == "" || conn.Warm == model {
		return ""
	}
	return fmt.Sprintf(
		"switching the engine's checkpoint from %s to %s — this can take 1-2.5 minutes (EBS re-read), not a stall",
		conn.Warm, model)
}

// comfyFirstModelForOp is ADR 0094 decision 11's remaining half: when a request names no model
// and the warm one's family does not offer the op asked for, this looks for the first catalogue
// row (in EngineConn.Models' own order) that does, rather than fail the op outright and let Run()
// fall through to a provider that spends a member's own plan quota. False when nothing on this
// engine offers it at all.
// 🔴 A row whose family offers the op but whose declared files are incomplete is skipped, not
// returned: without this check a row missing a required file would be "found", Generate() would
// then fail building its graph, and Run() falls through to a provider that spends a member's own
// plan anyway — exactly the outcome this whole remap exists to avoid. This is the same sanity
// probe Studio() already uses to withhold an unusable row from the member-facing catalogue.
func comfyFirstModelForOp(conn EngineConn, op Op) (string, bool) {
	for _, id := range conn.Models {
		family, ok := comfyFamilyFor(conn, id)
		if !ok || !comfyFamilySupportsOp(family, op) {
			continue
		}
		if _, err := comfyBuildGraph(family, resolveComfyFiles(conn.Files[id]), comfyParams{Prompt: "x"}); err != nil {
			continue // this row's files are incomplete; the same refusal a real request would get
		}
		return id, true
	}
	return "", false
}

// comfyResolveFamily is the "受付時に model → family を解く" step ADR 0094 decisions 2 and 4 ask
// for, shared by the two edge refusals below. It is deliberately conservative: a request naming
// NEITHER a provider nor a model cannot be resolved to a family here — the auto router has not
// run yet, and a request that might not even reach this engine must not be refused for it. named
// is the model actually resolved against (the caller's own, or the provider's warm default),
// which the caller uses to name it in the refusal.
//
// 🔴 op is what keeps this from refusing a request Generate() would never actually run against
// the row it resolved here. A request that names a provider but no model, against an engine
// whose WARM row cannot do the op it asked for, is exactly the case Generate() itself remaps to
// the first row that can (comfyFirstModelForOp, decision 11's third bullet) — so refusing here on
// the warm row's own family would 400 a request before it ever reaches the row that will actually
// answer it (measured: `provider=comfy` with no model, `op=generate`, `size=1024x1024`, against
// an engine whose warm checkpoint is this family, refused `bad_size` even though the request
// would have landed on an ordinary row that takes any size). An EXPLICIT model has no such
// escape — Generate() honours it and refuses it as asked — so the op gate applies only to the
// implicit, warm-default resolution.
func comfyResolveFamily(pref, model, op string) (family comfyFamily, named string, ok bool) {
	pref = strings.TrimSpace(pref)
	model = strings.TrimSpace(model)
	if pref == "" && model == "" {
		return "", "", false
	}
	for _, p := range Providers() {
		cp, isComfy := p.(*comfyProvider)
		if !isComfy {
			continue
		}
		if pref != "" && pref != "auto" && cp.ID() != pref {
			continue
		}
		conn, ready := cp.conn(context.Background())
		if !ready {
			continue
		}
		m := model
		if m == "" {
			if pref == "" || pref == "auto" {
				// No model named either: this row is not necessarily the one auto-routing lands
				// on, so nothing here is resolved for it.
				continue
			}
			m = cp.DefaultModel()
		}
		f, familyOk := comfyFamilyFor(conn, m)
		if !familyOk {
			continue
		}
		// 🔴 Applies to an EXPLICIT model too (sfiowgj review): when the op itself is wrong for
		// this model, THAT is the reason the request fails — decision 13 refuses it by name with
		// the ops it can do, and an explicit model's own op mismatch is refused inside Generate()
		// ("cannot do %s"). Skipping the edge check here never lets a mismatched request through
		// silently; it just leaves the refusal to the more specific one downstream instead of
		// this function reporting size/strength for an op the model was never going to run under.
		if op != "" && !comfyFamilySupportsOp(f, Op(op)) {
			continue
		}
		return f, m, true
	}
	return "", "", false
}

// comfyFamilySupportsOp is comfyFamilyOps as a membership test.
func comfyFamilySupportsOp(family comfyFamily, op Op) bool {
	for _, o := range comfyFamilyOps(family) {
		if o == op {
			return true
		}
	}
	return false
}

// comfyStrengthRefusal is ADR 0094 decision 2's edge check, shared by HandleGenerate and
// jobs_http.go's spec(): a resolved family that does not read Strength is refused BY VALUE before
// any GPU is woken, the same way an out-of-range strength already is. Empty when the family
// cannot be resolved from what the request named (comfyResolveFamily) — a request naming neither
// a provider nor a model reaches Generate() instead, and requestWarnings' own `!caps.Strength`
// branch (imagegen.go) catches it there, asking Caps about the RESOLVED model rather than the
// request's own (the same fix that closes comfyNegativeIgnoredWarning's twin gap).
func comfyStrengthRefusal(pref, model, op string) string {
	family, named, ok := comfyResolveFamily(pref, model, op)
	if !ok || comfyFamilyStrength(family) {
		return ""
	}
	return fmt.Sprintf("model %s does not take strength: the %s family fixes its denoise at 1 by"+
		" construction (instruction editing), so the amount has nowhere to go", named, family)
}

// comfySizeRefusal is decision 4's edge check, in the same shape as comfyStrengthRefusal above.
func comfySizeRefusal(pref, model, op, size string) string {
	if s := strings.TrimSpace(size); s == "" || s == "auto" {
		return ""
	}
	family, named, ok := comfyResolveFamily(pref, model, op)
	if !ok || !comfyFamilyHasNoSizes(family) {
		return ""
	}
	return fmt.Sprintf("model %s does not take size: the %s family's output size is decided from"+
		" the input picture's own aspect ratio, so no candidate has anywhere to go", named, family)
}

func (p *comfyProvider) Generate(ctx context.Context, req Request) (Result, error) {
	conn, ok := p.conn(ctx)
	if !ok {
		return Result{}, errors.New("this deployment runs no self-hosted image engine")
	}
	explicitModel := strings.TrimSpace(req.Model) != ""
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.DefaultModel()
	}
	if model == "" {
		return Result{}, errors.New("no enabled checkpoint in the catalogue")
	}
	caps := p.Caps(model)
	if !caps.Supports(req.Op) {
		// ADR 0094 decision 11's third bullet: a request naming no model resolved to the WARM
		// row, and its family does not offer this op — falling through to the caller's own error
		// would make Run() try the next provider in the effective order, which is a member's own
		// plan quota (imagegen.go's fallbackWarnings measured exactly this accident once already,
		// ADR 0071 P1). An explicit model is honoured as asked and refused as asked: the caller
		// named it on purpose.
		if explicitModel {
			return Result{}, fmt.Errorf("the self-hosted image engine cannot do %s", req.Op)
		}
		alt, found := comfyFirstModelForOp(conn, req.Op)
		if !found {
			return Result{}, fmt.Errorf("the self-hosted image engine cannot do %s", req.Op)
		}
		// comfySwitchWarning below reads conn.Warm against the model actually used, so switching
		// to alt here is what makes it fire and explain the checkpoint change.
		model, caps = alt, p.Caps(alt)
	}
	if req.Prompt == "" {
		return Result{}, errors.New("a prompt is required")
	}
	family, ok := comfyFamilyFor(conn, model)
	if !ok {
		return Result{}, errComfyFamilyNotDeclared(model, conn.BaseModel[model])
	}
	if err := comfyCheckInputs(req, caps); err != nil {
		return Result{}, err
	}
	files := resolveComfyFiles(conn.Files[model])
	loras, err := comfyResolveLoras(conn, family, model, req.Loras)
	if err != nil {
		return Result{}, err
	}

	w, h, ok := parseSize(req.Size)
	if !ok {
		w, h = comfyDefaultSize(family)
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}
	seed, err := comfySeedFor(req)
	if err != nil {
		return Result{}, err
	}
	params := comfyParams{
		Op: req.Op, Prompt: req.Prompt, Negative: comfyNegativeFor(conn, model, req),
		Seed: seed, Width: w, Height: h,
		BatchSize: count, Loras: loras, Strength: req.Strength,
		// The catalogue row for THIS model with the caller's own overlay laid over it, field by
		// field (ADR 0081 decision 4). What the template then does with it is one more merge —
		// family recipe ← this — so the whole order is recipe ← row ← request, and a caller who
		// names only `steps` changes only the steps.
		Params: comfyEffectiveParams(conn.Params[model], req.Params),
	}

	switchWarning := comfySwitchWarning(conn, model)

	// Two clocks, both hung off the CALLER's context so that hanging up still ends the request at
	// once. The wake budget covers everything up to the engine accepting the prompt; what follows
	// is the GPU's own work and gets engineRunTimeout. One budget for both is what ADR 0094's P2
	// acceptance measured going wrong: the wake ate a third of it and the sampling was cut off.
	callerCtx := ctx
	ctx, cancelWake := context.WithTimeout(callerCtx, engineTimeout)
	defer cancelWake()

	// The uploads come FIRST, and not only because the graph has to name them: they are now the
	// call that meets a cold engine, so they carry the wake retry /prompt used to be alone in
	// needing. Reading the picture's real dimensions here rather than trusting req.Size is what
	// keeps klein's schedule honest — Flux2Scheduler derives its shift from a width and height,
	// and an edit's size is the input picture's, not the caller's.
	var sizeWarning string
	if params.isImageToImage() {
		req.reportPhase(PhaseUploading)
		for _, in := range req.Inputs {
			up, err := p.uploadImage(ctx, conn, req, in)
			if err != nil {
				return Result{}, err
			}
			params.Images = append(params.Images, up.name)
			// The FIRST reference decides the output's dimensions, and only it: every family that
			// reads a second one composes it INTO the first one's frame (FluxKontextImageScale
			// scales image1 and the rest ride the same latent), so measuring the others here would
			// report a size no picture ever had.
			if len(params.Images) == 1 && up.width > 0 && up.height > 0 {
				if req.Size != "" && req.Size != "auto" && (up.width != w || up.height != h) {
					sizeWarning = fmt.Sprintf(
						"size=%s requested, but %s keeps the input picture's own %dx%d", req.Size, req.Op, up.width, up.height)
				}
				params.Width, params.Height = up.width, up.height
			}
		}
		if req.Op == OpInpaint {
			mask, err := p.uploadImage(ctx, conn, req, req.Mask)
			if err != nil {
				return Result{}, err
			}
			// 🔴 The instruction-edit families, and only they, need the mask to be the picture's
			// own size. Their template sends BOTH through FluxKontextImageScale so that the crop
			// it applies is the same for both (comfyQwenEditNoiseMask), and that node picks its
			// target from the width and height it is given — a mask of some other shape resolves a
			// different target and lands somewhere else, silently. Every other family stretches the
			// mask over the whole frame with no crop, where a different size is still "the same
			// region of the picture" and has always been allowed.
			if comfyFamilyInstructionEdit(family) && mask.width > 0 && mask.height > 0 &&
				(mask.width != params.Width || mask.height != params.Height) {
				return Result{}, fmt.Errorf("the %s family needs the mask to be the input picture's own size"+
					" (%dx%d), and this one is %dx%d — its frame is rescaled from the picture's aspect ratio,"+
					" so a mask of another shape would be applied to a different area than the one drawn",
					family, params.Width, params.Height, mask.width, mask.height)
			}
			params.Mask = mask.name
		}
	}

	graph, err := comfyBuildGraph(family, files, params)
	if err != nil {
		return Result{}, err
	}

	promptID, err := p.submit(ctx, conn, req, graph, model)
	if err != nil {
		return Result{}, err
	}
	// The engine has taken the request and named it: from here a cancel has something to aim at,
	// and the alternative — the bare /interrupt — would kill another workspace's picture.
	req.reportUpstream(promptID)
	req.reportPhase(PhaseRunning)
	// The wake is over — the box answered. Releasing its budget here rather than letting the
	// deferred cancel run at the end of the function is the whole point: what is left of it must
	// not be what the generation gets to spend.
	cancelWake()
	runCtx, cancelRun := context.WithTimeout(callerCtx, engineRunTimeout)
	defer cancelRun()
	hist, err := p.awaitHistory(runCtx, conn, req, promptID)
	if err != nil {
		return Result{}, err
	}
	req.reportPhase(PhaseFetching)
	images, err := p.fetchImages(runCtx, conn, hist, seed)
	if err != nil {
		return Result{}, err
	}

	warnings := comfyWarnings(req)
	if ignored := comfyNegativeIgnoredWarning(conn, model, family); ignored != "" {
		warnings = append(warnings, ignored)
	}
	warnings = append(warnings, comfyIgnoredParamWarnings(family, req.Params)...)
	warnings = append(warnings, comfyTriggerWarnings(conn, req.Loras, req.Prompt)...)
	if switchWarning != "" {
		warnings = append(warnings, switchWarning)
	}
	if sizeWarning != "" {
		warnings = append(warnings, sizeWarning)
	}
	if cached := comfyCacheWarning(hist); cached != "" {
		warnings = append(warnings, cached)
	}
	return Result{
		Images: images,
		// The row's own key (ADR 0082 decision 1), not the bare kind name: two comfy rows on one
		// deployment answer with different ids, and this is the one fact that tells them apart in
		// the ledger and in generate_image's own result.
		Provider:    p.ID(),
		Model:       model,
		Destination: "the fleet's own GPU engine（この配備が動かす自前のエンジン）",
		Warnings:    warnings,
		CostUSD:     0,
		Usage:       Usage{Measured: false},
	}, nil
}

// comfyCheckInputs refuses a request whose op and attachments do not match, before anything is
// uploaded and before a GPU is woken. The same three rules sdcpp checks, in the same order.
func comfyCheckInputs(req Request, caps Caps) error {
	if len(req.Inputs) > caps.MaxInputs {
		// Pluralised, because since ADR 0094 decision 5 the ceiling is no longer always 1 and
		// "at most 2 reference image" is how a reader learns the message is generated.
		noun := "images"
		if caps.MaxInputs == 1 {
			noun = "image"
		}
		return fmt.Errorf("at most %d reference %s (got %d)", caps.MaxInputs, noun, len(req.Inputs))
	}
	if req.Op != OpGenerate && len(req.Inputs) == 0 {
		return fmt.Errorf("%s needs an input image", req.Op)
	}
	if req.Op == OpInpaint && strings.TrimSpace(req.Mask) == "" {
		return errors.New("inpaint needs a mask image")
	}
	return nil
}

// comfyMaxUpload bounds one uploaded picture. The ceiling is not this file's to pick: the engine
// gateway buffers a request body through io.LimitReader at 32 MiB (engineMaxRequestBody), and
// LimitReader TRUNCATES rather than failing — so a larger picture would arrive at ComfyUI as a
// corrupt file and be refused with a decoder error naming nothing the caller can act on. Refusing
// here says which file and how big.
const comfyMaxUpload = 24 << 20

// comfyUpload is what POST /upload/image answered: the name the engine filed the picture under,
// plus the dimensions read locally on the way past.
type comfyUpload struct {
	name          string
	width, height int
}

// uploadImage puts one local file into ComfyUI's own input directory and answers with the name a
// graph may then reference.
//
// This call exists because LoadImage's `image` input is an ENUMERATION over that directory
// (nodes.py, v0.34.0) — there is no "load this path" node, and a path from this container would
// mean nothing on the engine's disk anyway. It is the same shape of trap SD3.5's TripleCLIPLoader
// was: a value that looks like a file name and is really a member of a list the server builds.
//
// The uploaded name is the file's own CONTENT HASH, which buys two things. ComfyUI renames a
// colliding upload to `x (1).png` unless the bytes are identical, so a fixed name would leave a
// growing pile of near-duplicates in the input directory; a hash collides only with itself, and
// the server then recognises the duplicate and keeps the one it has. The extension is preserved
// because the enum LoadImage builds is filtered by content type, which is read off the name.
//
// 🔴 The answer's `name` is used, never the one that was sent. They differ exactly when the server
// decided to rename, and a graph naming the file it MEANT to upload would fail validation against
// a directory listing that has the other one.
func (p *comfyProvider) uploadImage(ctx context.Context, conn EngineConn, req Request, path string) (comfyUpload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return comfyUpload{}, fmt.Errorf("could not read %s: %w", path, err)
	}
	if len(raw) > comfyMaxUpload {
		return comfyUpload{}, fmt.Errorf("%s is %d bytes, over this route's %d-byte limit for one picture",
			path, len(raw), comfyMaxUpload)
	}
	up := comfyUpload{name: comfyUploadName(raw, path)}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		up.width, up.height = cfg.Width, cfg.Height
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", up.name)
	if err != nil {
		return comfyUpload{}, err
	}
	if _, err := part.Write(raw); err != nil {
		return comfyUpload{}, err
	}
	// type=input is where LoadImage looks by default, so the graph can name the file with no
	// `[type]` annotation. Deliberately no overwrite: identical bytes are recognised as a
	// duplicate and nothing is written at all.
	for k, v := range map[string]string{"type": "input", "subfolder": ""} {
		if err := mw.WriteField(k, v); err != nil {
			return comfyUpload{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return comfyUpload{}, err
	}
	body := buf.Bytes()
	ctype := mw.FormDataContentType()

	answer, err := p.sendWithWake(ctx, conn, req, "/upload/image", func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL(conn, "/upload/image"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", ctype)
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		return httpReq, nil
	})
	if err != nil {
		return comfyUpload{}, err
	}
	var doc struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if json.Unmarshal(answer, &doc) != nil || doc.Name == "" {
		return comfyUpload{}, fmt.Errorf("the image engine's /upload/image answer had no name: %s", tail(string(answer), 400))
	}
	up.name = doc.Name
	if doc.Subfolder != "" {
		// The graph names a path relative to the input directory, the same shape the loras list
		// uses. Nothing here asks for a subfolder, so this only ever fires if a future engine
		// starts choosing one.
		up.name = doc.Subfolder + "/" + doc.Name
	}
	return up, nil
}

// comfyUploadName is the content hash plus an extension ComfyUI's own content-type filter will
// accept. The source file's extension is preferred and the bytes decide when it says nothing —
// a name with no usable extension is one LoadImage's enum drops, which reads as "the upload
// worked and the graph is wrong".
func comfyUploadName(raw []byte, path string) string {
	sum := sha256.Sum256(raw)
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
	default:
		switch http.DetectContentType(raw) {
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		default:
			ext = ".png"
		}
	}
	return "af-" + hex.EncodeToString(sum[:8]) + ext
}

// sendWithWake runs one request against the engine through the same retry-on-503-engine_waking
// loop /prompt uses, remaking the request per attempt because a body reader cannot be replayed.
// what names the call in the failure, so a refusal says which of the four endpoints refused.
//
// It is also where the job list learns that the box is being STARTED (ADR 0081 decision 2): the
// gateway's 503 is the only signal anywhere that the several minutes about to pass are a cold
// start rather than a stall, and it is seen here and nowhere else.
func (p *comfyProvider) sendWithWake(ctx context.Context, conn EngineConn, req Request, what string, make func() (*http.Request, error)) ([]byte, error) {
	lastWaking := ""
	for attempt := 1; ; attempt++ {
		httpReq, err := make()
		if err != nil {
			return nil, err
		}
		respBody, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return nil, engineGaveUp(attempt, lastWaking)
			}
			return nil, err
		}
		if status < 300 {
			return respBody, nil
		}
		if !engineRetryable(status, respBody) {
			return nil, fmt.Errorf("the image engine's %s answered %d %s: %s",
				what, status, http.StatusText(status), engineErrText(respBody))
		}
		lastWaking = engineErrText(respBody)
		req.reportPhase(PhaseWaking)
		select {
		case <-ctx.Done():
			return nil, engineGaveUp(attempt, lastWaking)
		case <-time.After(retryAfter):
		}
	}
}

// comfyWarnings is what this route knows it cannot honour, mirroring sdcppWarnings.
func comfyWarnings(req Request) []string {
	var out []string
	if b := strings.ToLower(strings.TrimSpace(req.Background)); b == "transparent" {
		out = append(out, "background=transparent requested, opaque produced (this engine's checkpoints have no alpha channel)")
	}
	return out
}

// comfySeedFor is the seed this request samples from: the caller's, when they pinned one, and a
// fresh random one otherwise.
//
// A pinned seed is what makes two requests comparable, which is the only way to show that one
// changed thing — a LoRA, a checkpoint — is what changed the picture (ADR 0072 phase P3). The
// default stays random because that is what a caller who says nothing means, and because of the
// cache below.
func comfySeedFor(req Request) (int64, error) {
	if req.Seed != nil {
		return *req.Seed, nil
	}
	return comfyRandomSeed()
}

// comfyRandomSeed picks a fresh seed for a request that pinned none. ComfyUI caches a node's
// output by its inputs (measured, bench-image-engine.py), so replaying an identical graph answers
// the second call from cache in half a second rather than generating anything — which is the
// RIGHT answer for a caller who pinned a seed and is asking for the same picture, and a confusing
// one for a caller who did not. comfyCacheWarning says which of the two happened.
func comfyRandomSeed() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("could not pick a seed: %w", err)
	}
	n := int64(binary.BigEndian.Uint64(b[:]) & math.MaxInt64)
	return n, nil
}

// submit is POST /prompt, through the same retry-on-503-engine_waking loop as sdcpp.go's send.
// For a plain generate it is the first call of the three and therefore the one that meets a
// stopped engine; for edit and inpaint the uploads got there first, which is exactly why they
// share this loop rather than each having their own.
func (p *comfyProvider) submit(ctx context.Context, conn EngineConn, req Request, graph comfyGraph, model string) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": graph, "client_id": "af-agent"})
	if err != nil {
		return "", err
	}
	respBody, err := p.sendWithWake(ctx, conn, req, "/prompt", func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL(conn, "/prompt"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		// Declares which checkpoint this request used, for the gateway's warm-model tracking
		// (ADR 0072 decision 7) — ComfyUI's /prompt answer carries only a queue id, never a
		// model name, so this header is the only way the CP learns what became warm.
		httpReq.Header.Set("X-AF-Model", model)
		return httpReq, nil
	})
	if err != nil {
		return "", err
	}
	var doc struct {
		PromptID string `json:"prompt_id"`
		Error    any    `json:"error"`
	}
	if json.Unmarshal(respBody, &doc) != nil || doc.PromptID == "" {
		return "", fmt.Errorf("the image engine's /prompt answer had no prompt_id: %s", tail(string(respBody), 400))
	}
	return doc.PromptID, nil
}

// comfyPollEvery is how often /history is asked once the engine has accepted the prompt (i.e.
// after submit already succeeded, so the engine is confirmed up and this is not the wake dance).
// A var only so a test can shorten it.
var comfyPollEvery = 1 * time.Second

// comfyHistory is the fields this package reads out of GET /history/<id>. ComfyUI's own document
// nests one more level (keyed by the prompt id itself), which awaitHistory unwraps.
type comfyHistory struct {
	Status struct {
		Completed bool   `json:"completed"`
		StatusStr string `json:"status_str"`
		Messages  []any  `json:"messages"`
	} `json:"status"`
	Outputs map[string]struct {
		Images []struct {
			Filename  string `json:"filename"`
			Subfolder string `json:"subfolder"`
			Type      string `json:"type"`
		} `json:"images"`
	} `json:"outputs"`
}

// awaitHistory polls until ComfyUI reports the queued prompt done (success or error), bounded by
// ctx — the same overall budget submit's caller set, so a generation that never finishes is cut
// off by the request's own timeout rather than looping forever.
//
// A retryable answer here is waited out exactly as submit waits one out, and the reason is a
// message a caller actually received (ADR 0072 欠落 9): a box swapped out mid-poll made the
// gateway answer `503 engine_waking`, whose own text ends in "retry" — and this loop returned it
// as a failure without retrying anything. Generation is 47-78 seconds cold (measured), so the
// window in which the box can change under a poll is wide open, not theoretical.
//
// What a retry cannot recover is the QUEUE: a restarted ComfyUI holds no history for a prompt id
// the previous process accepted, so once a wake has been seen, a 200 that does not carry this
// prompt means the work is gone. That is reported rather than polled for, because the alternative
// is silence until the whole generation budget runs out.
func (p *comfyProvider) awaitHistory(ctx context.Context, conn EngineConn, req Request, promptID string) (comfyHistory, error) {
	lastWaking, sawWaking := "", false
	for {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, engineURL(conn, "/history/"+url.PathEscape(promptID)), nil)
		if err != nil {
			return comfyHistory{}, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
		body, status, retryAfter, err := engineHTTPAttempt(p.client, httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return comfyHistory{}, comfyPollTimedOut(promptID, lastWaking, ctx.Err())
			}
			return comfyHistory{}, err
		}
		wait := comfyPollEvery
		switch {
		case status >= 300 && !engineRetryable(status, body):
			return comfyHistory{}, fmt.Errorf("the image engine's /history answered %d %s: %s",
				status, http.StatusText(status), engineErrText(body))
		case status >= 300:
			lastWaking, sawWaking = engineErrText(body), true
			// The box was replaced under the poll: the job list goes back to saying "starting",
			// because that is what the next several minutes are.
			req.reportPhase(PhaseWaking)
			// The gateway's own Retry-After, not the poll interval: it is answering for a box
			// that is being started, and asking every second only adds requests to a wake.
			wait = retryAfter
		default:
			if sawWaking {
				// The box answered again: whatever the list said while it was starting, the
				// picture is being made now.
				req.reportPhase(PhaseRunning)
			}
			var byID map[string]comfyHistory
			if err := json.Unmarshal(body, &byID); err != nil {
				return comfyHistory{}, fmt.Errorf("the image engine's /history answer was not JSON: %w", err)
			}
			hist, known := byID[promptID]
			if known {
				if hist.Status.StatusStr == "error" {
					return comfyHistory{}, fmt.Errorf("the image engine failed the request: %s%s",
						comfyErrorMessages(hist), comfyErrorHint(hist))
				}
				if hist.Status.Completed {
					return hist, nil
				}
			}
			// An UNKNOWN prompt id is normal while the picture is being made — ComfyUI's history
			// holds finished prompts only, and the queued one lives in /queue. It stops being
			// normal once this engine has restarted under us — and a wake alone does not prove
			// that: the gateway also answers engine_waking for a box that is up but too busy to
			// answer its health probe (a T4 editing at 35 s/step), so /queue is asked first.
			if !known && sawWaking && !p.stillQueued(ctx, conn, promptID) {
				return comfyHistory{}, fmt.Errorf(
					"the image engine restarted while this picture was being made, and the queued request did not survive it (%s)"+
						" — ask again; nothing was generated", lastWaking)
			}
		}
		select {
		case <-ctx.Done():
			return comfyHistory{}, comfyPollTimedOut(promptID, lastWaking, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// stillQueued is whether the engine still holds this prompt, asked only after a wake was seen.
// An answer that cannot be read counts as "no": the restart is then reported as before, which
// is the direction that stops the wait rather than letting it run silent to the budget.
func (p *comfyProvider) stillQueued(ctx context.Context, conn EngineConn, promptID string) bool {
	held, err := p.queueHolds(ctx, conn, promptID)
	return err == nil && held
}

// comfyPollTimedOut names the engine's own last word when the wait ran out during a wake, so a
// timeout that happened BECAUSE the box was being replaced does not read as a stalled generation.
//
// 🔴 It also says the picture is RECOVERABLE, and that is not a nicety. Giving up here does not
// interrupt the prompt: the GPU keeps working and keeps billing, and ComfyUI then holds the
// result — measured (ADR 0094, 2026-09-21) after a 960 s timeout, the identical request 22
// seconds later came back in 1.07 s with the same picture out of the engine's own cache. A caller
// told only "timed out" pays for that picture and never collects it.
func comfyPollTimedOut(promptID, lastWaking string, err error) error {
	if lastWaking != "" {
		return fmt.Errorf("waiting for the image engine timed out while it was still starting (%s): %w", lastWaking, err)
	}
	if promptID == "" {
		return fmt.Errorf("waiting for the image engine timed out: %w", err)
	}
	return fmt.Errorf("waiting for the image engine timed out, but it is still making this picture"+
		" (prompt %s) and was not interrupted — asking again with the same request collects it"+
		" from the engine rather than paying for it twice: %w", promptID, err)
}

// comfyErrorMessages renders ComfyUI's execution_error message list, capped: it carries a full
// Python traceback per node, and the caller only needs enough to know which node and why.
func comfyErrorMessages(hist comfyHistory) string {
	b, err := json.Marshal(hist.Status.Messages)
	if err != nil {
		return "unknown error"
	}
	return tail(string(b), 800)
}

// comfyErrorHint translates the one execution error whose cause is a CATALOGUE fact rather than
// anything the caller did: a checkpoint published with no VAE tensors. ComfyUI answers it with a
// Python traceback ending in `ERROR: VAE is invalid: None`, which tells a session nothing it can
// act on — `generate_image` has no VAE argument, so retrying, changing the op or changing the
// size all fail the same way, each after the 1-2.5 minute checkpoint switch (measured 2026-09-11
// on this deployment: generate died in VAEDecode, edit in VAEEncode, same model).
//
// It reads the WHOLE message list rather than the tail comfyErrorMessages shows: the exception
// message sorts before the traceback in ComfyUI's own error dict, so on a long traceback the one
// line this matches on is the first thing the 800-character cap drops.
func comfyErrorHint(hist comfyHistory) string {
	b, err := json.Marshal(hist.Status.Messages)
	if err != nil || !strings.Contains(string(b), "VAE is invalid") {
		return ""
	}
	return " — this checkpoint carries no VAE of its own, so nothing could encode or decode the" +
		" picture. Every op fails the same way until the catalogue row for this model declares its" +
		" family's VAE as a separate file (`--vae`, an SDXL-family checkpoint takes an sdxl_vae);" +
		" until then, ask for another model"
}

// comfySaveNode is the id every template gives its SaveImage node. It is the graph's terminal
// output, so "was this node cached" is the same question as "was any picture made at all".
const comfySaveNode = "save"

// comfyCacheWarning says, only when it actually happened, that the engine returned a picture it
// already had instead of generating one.
//
// The signal is ComfyUI's OWN `execution_cached` status message, which lists the node ids it
// skipped (execution.py, v0.34.0) and rides in /history's status.messages — the same field the
// error path already reads. So this is a fact the engine reported, not a guess from a suspiciously
// short elapsed time.
//
// Why warn at all, given that a repeat of an identical seeded request SHOULD return the identical
// picture: because the two readings of a half-second answer are opposite. A caller comparing
// "with the LoRA" against "without" wants to know nothing was recomputed if the graphs happened to
// match; a caller who changed something the graph does not carry (ADR 0069 has no negative prompt,
// no steps, no cfg) would otherwise conclude the engine ignored a change that never reached it.
// Silent when nothing was cached, which is every first call — so it costs the common path nothing.
func comfyCacheWarning(hist comfyHistory) string {
	for _, m := range hist.Status.Messages {
		pair, ok := m.([]any)
		if !ok || len(pair) < 2 {
			continue
		}
		if event, _ := pair[0].(string); event != "execution_cached" {
			continue
		}
		data, ok := pair[1].(map[string]any)
		if !ok {
			continue
		}
		nodes, _ := data["nodes"].([]any)
		for _, n := range nodes {
			if id, _ := n.(string); id == comfySaveNode {
				return "this picture came from the engine's cache, not from a new generation — " +
					"the graph was identical to one it had already run (same seed, prompt, size and model). " +
					"Anything you changed that is not one of those does not reach this route"
			}
		}
	}
	return ""
}

// fetchImages downloads every output image GET /view names, in the order ComfyUI's own outputs
// map iterates — a batch of N (Request.Count) all rides on the SAME node, so this is not
// re-deriving what "count" meant, only reading off what the graph actually produced.
//
// seed is the graph's own, and each picture is stamped with seed+i (ADR 0081 decision 3): that
// is how ComfyUI derives a batch's noise from one number, so the second picture of a batch of
// four is reproducible only under seed+1 and never under the seed the request carried.
func (p *comfyProvider) fetchImages(ctx context.Context, conn EngineConn, hist comfyHistory, seed int64) ([]Image, error) {
	var out []Image
	for _, o := range hist.Outputs {
		for _, im := range o.Images {
			img, err := p.viewOne(ctx, conn, im.Filename, im.Subfolder, im.Type)
			if err != nil {
				return nil, err
			}
			s := seed + int64(len(out))
			img.Seed = &s
			out = append(out, img)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the image engine reported success but produced no image")
	}
	return out, nil
}

func (p *comfyProvider) viewOne(ctx context.Context, conn EngineConn, filename, subfolder, kind string) (Image, error) {
	q := url.Values{"filename": {filename}, "subfolder": {subfolder}, "type": {kind}}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, engineURL(conn, "/view?"+q.Encode()), nil)
	if err != nil {
		return Image{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
	body, status, _, err := engineHTTPAttempt(p.client, httpReq)
	if err != nil {
		return Image{}, err
	}
	if status >= 300 {
		return Image{}, fmt.Errorf("the image engine's /view answered %d %s: %s",
			status, http.StatusText(status), engineErrText(body))
	}
	img := Image{Bytes: body, MIME: comfyMIMEFor(filename)}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err == nil {
		img.Width, img.Height = cfg.Width, cfg.Height
	}
	return img, nil
}

// comfyEffectiveParams lays the caller's overlay over the catalogue row's, field by field (ADR
// 0081 decision 4). The template then lays the result over its family recipe (comfyRecipe.with),
// so the whole order is recipe ← row ← request.
//
// Field by field for the same reason the other two merges are: a caller who types a step count
// into a form and nothing else has not asked for the administrator's declared sampler to be
// reset to whatever a zero value happens to mean.
//
// 🔴 An unknown sampler or scheduler name never arrives here. comfyRecipe.with would IGNORE it
// and keep the family's own — the right answer for an administrator's old row, and the wrong one
// for a member's form, where a typed value that silently does nothing is indistinguishable from
// a broken feature. The jobs route refuses it by name with 400 bad_params first
// (validateRequestParams), which is why this merge does not repeat the check.
func comfyEffectiveParams(row EngineParams, req *EngineParams) EngineParams {
	if req == nil {
		return row
	}
	if req.Steps > 0 {
		row.Steps = req.Steps
	}
	if req.CFG > 0 {
		row.CFG = req.CFG
	}
	if s := strings.TrimSpace(req.Sampler); s != "" {
		row.Sampler = s
	}
	if s := strings.TrimSpace(req.Scheduler); s != "" {
		row.Scheduler = s
	}
	return row
}

// comfyCancelTimeout bounds a cancel. It is SHORT, and deliberately not the generation's own
// budget: a cancel is a request against a box that is either up (in which case it answers at
// once) or asleep (in which case there is nothing running to cancel), so waiting out a wake
// would buy a GPU to interrupt a picture that no longer exists.
const comfyCancelTimeout = 15 * time.Second

// Cancel implements Canceller (ADR 0081 decision 2) for the fleet's own ComfyUI.
//
// Two upstream calls, because the engine has two places a request can be: /queue holds what has
// not started, /interrupt stops what has. Verified against ComfyUI's server.py on 2026-09-13:
// `POST /queue {"delete":[id]}` removes a pending item, and `POST /interrupt` with a
// `prompt_id` interrupts THAT prompt only.
//
// 🔴 The bare /interrupt — the same route with no body — is what the upstream UI's stop button
// sends, and it interrupts whatever the box happens to be executing. The box is shared across
// every workspace of the deployment, so sending it would cancel somebody else's picture, with
// nothing anywhere saying why theirs failed. This function therefore refuses an empty id
// instead of falling back to it.
func (p *comfyProvider) Cancel(ctx context.Context, upstream string) error {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return errors.New("this picture has not reached the engine yet, so there is no queued request to take back")
	}
	conn, ok := p.conn(ctx)
	if !ok {
		return errors.New("this deployment runs no self-hosted image engine")
	}
	ctx, cancel := context.WithTimeout(ctx, comfyCancelTimeout)
	defer cancel()
	if pending, err := p.queuePending(ctx, conn); err == nil && pending[upstream] {
		// Still waiting its turn upstream: dropping it costs no sampling at all, and unlike an
		// interrupt it cannot be confused with the job that is actually executing.
		return p.postCancel(ctx, conn, "/queue", map[string]any{"delete": []string{upstream}})
	}
	return p.postCancel(ctx, conn, "/interrupt", map[string]any{"prompt_id": upstream})
}

// queuePending is the set of prompt ids the engine holds but has not started. GET /queue answers
// `{"queue_running": [...], "queue_pending": [...]}`, each entry a heterogeneous array whose
// SECOND element is the prompt id (server.py's own queue tuple); anything shaped otherwise is
// skipped rather than guessed at.
func (p *comfyProvider) queuePending(ctx context.Context, conn EngineConn) (map[string]bool, error) {
	pending, _, err := p.queue(ctx, conn)
	return pending, err
}

// queueHolds reports whether the engine still has this prompt, running or waiting its turn.
func (p *comfyProvider) queueHolds(ctx context.Context, conn EngineConn, promptID string) (bool, error) {
	pending, running, err := p.queue(ctx, conn)
	if err != nil {
		return false, err
	}
	return pending[promptID] || running[promptID], nil
}

// queue reads GET /queue into its two sets of prompt ids: pending, then running.
func (p *comfyProvider) queue(ctx context.Context, conn EngineConn) (map[string]bool, map[string]bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, engineURL(conn, "/queue"), nil)
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
	body, status, _, err := engineHTTPAttempt(p.client, httpReq)
	if err != nil {
		return nil, nil, err
	}
	if status >= 300 {
		return nil, nil, fmt.Errorf("the image engine's /queue answered %d %s: %s",
			status, http.StatusText(status), engineErrText(body))
	}
	var doc struct {
		Running [][]any `json:"queue_running"`
		Pending [][]any `json:"queue_pending"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, err
	}
	return comfyQueueIDs(doc.Pending), comfyQueueIDs(doc.Running), nil
}

// comfyQueueIDs is the prompt ids out of one of /queue's lists.
func comfyQueueIDs(entries [][]any) map[string]bool {
	out := map[string]bool{}
	for _, entry := range entries {
		if len(entry) < 2 {
			continue
		}
		if id, ok := entry[1].(string); ok && id != "" {
			out[id] = true
		}
	}
	return out
}

// postCancel sends one cancel call. It does NOT go through sendWithWake: a 503 engine_waking
// means the box that held this request is gone, which is the outcome a cancel was asking for.
func (p *comfyProvider) postCancel(ctx context.Context, conn EngineConn, path string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL(conn, path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+conn.Token)
	respBody, status, _, err := engineHTTPAttempt(p.client, httpReq)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("the image engine's %s answered %d %s: %s",
			path, status, http.StatusText(status), engineErrText(respBody))
	}
	return nil
}

// comfyMIMEFor reads the extension because /view answers with the file's own bytes and no JSON
// envelope to carry a declared format in (unlike sdcpp's output_format field) — SaveImage's own
// default, and every template here, produces PNG, so anything else is a future template's doing.
func comfyMIMEFor(filename string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".jpg"), strings.HasSuffix(strings.ToLower(filename), ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(strings.ToLower(filename), ".webp"):
		return "image/webp"
	default:
		return "image/png"
	}
}
