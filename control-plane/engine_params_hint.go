package main

// Reading the generation settings an author wrote down in prose.
//
// 🔴 Why it has to be prose. Measured 2026-09-12, anonymously: `/api/v1/models` carries no
// `meta` on its example images (only `hasMeta`), and `/api/v1/images?modelVersionId=…` answers
// `meta: null` for every row — so the structured generation parameters Civitai holds are not
// readable without an account, and the CP deliberately has none (ADR 0072 decision 6: it only
// ever reads the source anonymously). What IS readable is the description: up to 12 KB of HTML
// per model, with "Steps: 30, CFG 4, DPM++ 2M Karras" somewhere in the middle of it.
//
// So this is a heuristic over text a stranger wrote, and it is treated as one. It fills the
// INGEST FORM, where a person sees the numbers next to the model they are about to take in and
// can change or clear them before anything is stored. Nothing here ever writes a catalogue row.

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineParamsTextCap bounds what is scanned. A model description runs to 12 KB and a Hugging
// Face README to far more, while everything worth finding is stated once — usually in the first
// screen of it, always in a "recommended settings" paragraph rather than in a 300 KB changelog.
const engineParamsTextCap = 64 << 10

// engineParamsHint is the settings found in one document, plus the sentence they were found in.
type engineParamsHint struct {
	Params store.EngineParams
	// Quote is the line the numbers came out of, verbatim and trimmed. It is what makes this
	// honest on screen: a number with the author's own sentence beside it can be judged, and a
	// number alone has to be trusted.
	Quote string
}

// empty says nothing was recognised, which is the normal outcome for most model pages.
func (h engineParamsHint) empty() bool { return h.Params == (store.EngineParams{}) }

var (
	// Both orders, because both are written: "Steps: 30" and "30 steps".
	engineReSteps    = regexp.MustCompile(`(?i)\bsteps?\b\s*[:：=]?\s*(\d{1,3})`)
	engineReStepsRev = regexp.MustCompile(`(?i)\b(\d{1,3})\s*steps?\b`)
	engineReCFG      = regexp.MustCompile(`(?i)\bcfg(?:\s*scale)?\b\s*[:：=]?\s*(\d{1,2}(?:\.\d+)?)`)
	engineReClipSkip = regexp.MustCompile(`(?i)\bclip\s*skip\b\s*[:：=]?\s*(-?\d)`)
	engineReSampler  = regexp.MustCompile(`(?i)\bsampler\b\s*(?:name)?\s*[:：=]?\s*([A-Za-z0-9_+. ]{2,30})`)
	engineReSched    = regexp.MustCompile(`(?i)\bschedule(?:r|type)?\b\s*[:：=]?\s*([A-Za-z_]{3,20})`)
	// A LoRA's strength. "weight 0.8", "strength: 1", "at 0.7 weight" — the number is what is
	// wanted and the word may be on either side.
	engineReWeight    = regexp.MustCompile(`(?i)\b(?:weight|strength)\b\s*[:：=]?\s*(\d?\.\d+|[0-2](?:\.\d+)?)`)
	engineReWeightRev = regexp.MustCompile(`(?i)\b(\d?\.\d+|[0-2](?:\.\d+)?)\s*(?:weight|strength)\b`)
)

// engineParamsFromText is the whole heuristic.
//
// Ranges are checked on every number, and an out-of-range one is DROPPED rather than clamped.
// The patterns match inside ordinary prose — "trained for 25000 steps", "version 2.0 weight" —
// and a training step count silently becoming a sampler step count is exactly the kind of
// plausible wrong number this whole design refuses to produce elsewhere.
func engineParamsFromText(text string) engineParamsHint {
	if len(text) > engineParamsTextCap {
		text = text[:engineParamsTextCap]
	}
	var h engineParamsHint
	quote := ""
	// remember keeps the first line that produced anything, which is the line a person needs to
	// read to judge the rest.
	remember := func(loc []int) {
		if quote == "" && loc != nil {
			quote = engineParamsLineAt(text, loc[0])
		}
	}
	if m := engineReSteps.FindStringSubmatchIndex(text); m != nil {
		if n, ok := engineParamsInt(text[m[2]:m[3]], 1, 150); ok {
			h.Params.Steps = n
			remember(m)
		}
	}
	if h.Params.Steps == 0 {
		if m := engineReStepsRev.FindStringSubmatchIndex(text); m != nil {
			if n, ok := engineParamsInt(text[m[2]:m[3]], 1, 150); ok {
				h.Params.Steps = n
				remember(m)
			}
		}
	}
	if m := engineReCFG.FindStringSubmatchIndex(text); m != nil {
		if f, ok := engineParamsFloat(text[m[2]:m[3]], 0.5, 30); ok {
			h.Params.CFG = f
			remember(m)
		}
	}
	if m := engineReClipSkip.FindStringSubmatchIndex(text); m != nil {
		if n, ok := engineParamsInt(strings.TrimPrefix(text[m[2]:m[3]], "-"), 1, 4); ok {
			h.Params.ClipSkip = n
			remember(m)
		}
	}
	if m := engineReSampler.FindStringSubmatchIndex(text); m != nil {
		sampler, sched := engineSamplerVocab(text[m[2]:m[3]])
		if sampler != "" {
			h.Params.Sampler = sampler
			remember(m)
		}
		if sched != "" {
			h.Params.Scheduler = sched
		}
	}
	if h.Params.Scheduler == "" {
		if m := engineReSched.FindStringSubmatchIndex(text); m != nil {
			if s := engineSchedulerVocab(text[m[2]:m[3]]); s != "" {
				h.Params.Scheduler = s
				remember(m)
			}
		}
	}
	for _, re := range []*regexp.Regexp{engineReWeight, engineReWeightRev} {
		if h.Params.Weight != 0 {
			break
		}
		if m := re.FindStringSubmatchIndex(text); m != nil {
			if f, ok := engineParamsFloat(text[m[2]:m[3]], 0.05, 2); ok {
				h.Params.Weight = f
				remember(m)
			}
		}
	}
	h.Quote = quote
	return h
}

// engineParamsClean is the gate every declared parameter set goes through on its way into the
// catalogue, whichever route wrote it.
//
// It DROPS rather than refuses, for the same reason the row shows `base_model_missing` instead
// of rejecting the row: a number outside the range is one field of a registration that is
// otherwise good, and failing the whole call would lose the four fields that were fine. What it
// must never do is store a value the provider will choke on — `Value not in list` arrives at
// generation time, after a cold start somebody waited through.
//
// Returns nil for "this row declares nothing", so an empty object from a form that was never
// filled in does not become a row that claims to have an opinion.
func engineParamsClean(p *store.EngineParams) *store.EngineParams {
	if p == nil {
		return nil
	}
	out := store.EngineParams{}
	if n, ok := engineParamsInt(strconv.Itoa(p.Steps), 1, 150); ok {
		out.Steps = n
	}
	if f, ok := engineParamsFloat(strconv.FormatFloat(p.CFG, 'f', -1, 64), 0.5, 30); ok {
		out.CFG = f
	}
	if n, ok := engineParamsInt(strconv.Itoa(p.ClipSkip), 1, 4); ok {
		out.ClipSkip = n
	}
	if f, ok := engineParamsFloat(strconv.FormatFloat(p.Weight, 'f', -1, 64), 0.05, 2); ok {
		out.Weight = f
	}
	out.Sampler, out.Scheduler = engineParamsName(p.Sampler, engineSamplerVocabName), engineParamsName(p.Scheduler, engineSchedulerVocab)
	// A sampler phrase can carry the scheduler ("DPM++ 2M Karras"), and a person pasting one
	// into the sampler field has stated both. Only when the scheduler field is otherwise empty:
	// what they typed there wins over what was implied elsewhere.
	if out.Scheduler == "" {
		if _, sched := engineSamplerVocab(p.Sampler); sched != "" {
			out.Scheduler = sched
		}
	}
	if out == (store.EngineParams{}) {
		return nil
	}
	return &out
}

// engineParamsName takes a name as it stands when it already looks like one of ComfyUI's own
// (lower case, digits and underscores), and otherwise puts it through the display-name table.
//
// The pass-through matters: ComfyUI's sampler list is longer than any table written here and
// grows with the engine, so an operator who typed `dpmpp_2m_sde_gpu` must not have it silently
// deleted by a CP that has never heard of it. The cost is that a typo survives to the graph,
// where ComfyUI names it — which is a better error than a field that empties itself.
func engineParamsName(in string, lookup func(string) string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return ""
	}
	if engineIsComfyIdent(s) {
		return strings.ToLower(s)
	}
	return lookup(s)
}

func engineIsComfyIdent(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// engineSamplerVocabName is engineSamplerVocab with the scheduler half dropped, so it fits the
// one-string shape engineParamsName takes.
func engineSamplerVocabName(s string) string {
	name, _ := engineSamplerVocab(s)
	return name
}

// engineParamsLineAt is the line `at` falls in, squeezed and capped. What it is for is being
// READ next to the numbers, so a 4 KB paragraph would defeat the purpose as surely as no quote.
func engineParamsLineAt(text string, at int) string {
	start := strings.LastIndexAny(text[:at], ".\n") + 1
	end := strings.IndexAny(text[at:], "\n")
	if end < 0 {
		end = len(text)
	} else {
		end += at
	}
	line := strings.Join(strings.Fields(text[start:end]), " ")
	// Counted in RUNES, and cut on a rune boundary: these descriptions are full of CJK and
	// emoji, and half a rune reaches the panel as a replacement character.
	const limit = 200
	if r := []rune(line); len(r) > limit {
		line = string(r[:limit]) + "…"
	}
	return line
}

func engineParamsInt(s string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

func engineParamsFloat(s string, lo, hi float64) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < lo || f > hi {
		return 0, false
	}
	return f, true
}

// engineSamplerNames maps what a model card writes onto what ComfyUI's KSampler enumerates.
// 🔴 The two are different vocabularies: "DPM++ 2M Karras" is one phrase on a model page and
// two separate inputs in the graph, and the node answers `Value not in list` for the phrase.
//
// Longest first, because "dpm++ 2m sde" contains "dpm++ 2m".
var engineSamplerNames = []struct{ text, name string }{
	{"dpm++ 2m sde", "dpmpp_2m_sde"},
	{"dpm++ 3m sde", "dpmpp_3m_sde"},
	{"dpm++ 2s a", "dpmpp_2s_ancestral"},
	{"dpm++ 2m", "dpmpp_2m"},
	{"dpm++ sde", "dpmpp_sde"},
	{"dpmpp_2m_sde", "dpmpp_2m_sde"},
	{"dpmpp_2m", "dpmpp_2m"},
	{"dpmpp_sde", "dpmpp_sde"},
	{"res_multistep", "res_multistep"},
	{"res multistep", "res_multistep"},
	{"euler a", "euler_ancestral"},
	{"euler_ancestral", "euler_ancestral"},
	{"euler", "euler"},
	{"heun", "heun"},
	{"lms", "lms"},
	{"ddim", "ddim"},
	{"uni_pc", "uni_pc"},
	{"unipc", "uni_pc"},
	{"lcm", "lcm"},
}

// engineSchedulerNames is ComfyUI's own scheduler list, which model cards happen to spell the
// same way — with one exception everybody writes: "Karras" as part of the sampler's name.
var engineSchedulerNames = []string{
	"karras", "exponential", "sgm_uniform", "simple", "ddim_uniform", "beta", "normal",
}

// engineSamplerVocab reads a sampler phrase, and returns the scheduler too when the phrase
// carried one ("DPM++ 2M Karras" is a sampler AND a scheduler).
func engineSamplerVocab(s string) (sampler, scheduler string) {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, c := range engineSamplerNames {
		if strings.Contains(l, c.text) {
			sampler = c.name
			break
		}
	}
	scheduler = engineSchedulerVocab(l)
	return sampler, scheduler
}

func engineSchedulerVocab(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, n := range engineSchedulerNames {
		if strings.Contains(l, n) || strings.Contains(l, strings.ReplaceAll(n, "_", " ")) {
			return n
		}
	}
	return ""
}

// engineStripHTML turns a description into something the patterns can be run over. Civitai's
// descriptions are HTML, and `<p>Steps: 30</p><p>CFG: 4</p>` has to become two LINES or the
// quote above would swallow the whole document.
func engineStripHTML(in string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(in); i++ {
		switch c := in[i]; {
		case c == '<':
			depth++
			// A block boundary is a line break. Without this every paragraph runs together and
			// one "line" is the entire description.
			b.WriteByte('\n')
		case c == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteByte(c)
		}
	}
	// The handful of entities that actually appear in these descriptions. Anything else is left
	// alone: a stray `&copy;` in a quote is noise, a wrong number is not.
	out := b.String()
	for _, e := range [][2]string{{"&nbsp;", " "}, {"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"}} {
		out = strings.ReplaceAll(out, e[0], e[1])
	}
	return out
}

// engineReadText fetches a document the params are looked for in, capped and best-effort. A
// failure is silence: the hint is a convenience on a form that works without it, and no page on
// the internet is allowed to fail an ingest.
func engineReadText(ctx context.Context, target string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	resp, err := engineIngestHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, engineParamsTextCap))
	return string(body)
}
