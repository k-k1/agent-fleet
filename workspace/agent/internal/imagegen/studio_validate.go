package imagegen

// Saving a draft (ADR 0100 decision 3): every field is checked on its own and an unfinished
// draft is allowed — an empty prompt saves. What a whole request must be (spec(): a prompt, a
// mask for inpaint, a family that reads strength) is checked when a job is made from the draft,
// at a press. The pane and set_image_draft go through the same function; the agent is further
// held to decision 4's fields and to the locks.
//
// The order is: the agent's fields → the value → the lock. A locked field with a bad value is
// reported as invalid, which is the more useful of the two things to say.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// studioDraftKeys is every key of ImageStudioDraft, in its declaration order — the order a
// change list is reported in.
var studioDraftKeys = []string{
	"provider", "model", "op", "prompt", "negativePrompt", "size", "aspectRatio", "count", "inputs",
	"mask", "loras", "seed", "strength", "params", "label", "out_dir", "jobs", "seed_policy",
	"full_steps", "suggest_model",
}

// studioShortTextMax bounds the short free-text fields (title, label, out_dir), which ride on
// every get_image_studio answer and are not the prompt, the one text worth its size.
const studioShortTextMax = 200

// studioMaxInputs bounds the references a draft may name; the widest family reads ten.
const studioMaxInputs = 16

// Why a field was not applied (DroppedField.Reason).
const (
	dropLocked    = "locked"
	dropHumanOnly = "human_only"
	dropInvalid   = "invalid"
)

// studioStrict decodes raw into v and refuses unknown object keys, so a misspelt `param.stpes`
// comes back as invalid rather than as a field that silently does nothing.
func studioStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// validateDraftField checks one field's new value, returning why it cannot be saved or "".
func validateDraftField(key string, raw json.RawMessage) string {
	var s string
	str := func() string {
		if err := json.Unmarshal(raw, &s); err != nil {
			return "must be a string"
		}
		return ""
	}
	switch key {
	case "provider", "model", "prompt", "negativePrompt", "aspectRatio", "suggest_model":
		return str()
	case "label", "out_dir":
		if msg := str(); msg != "" {
			return msg
		}
		if utf8.RuneCountInString(s) > studioShortTextMax {
			return fmt.Sprintf("at most %d characters", studioShortTextMax)
		}
	case "op":
		if msg := str(); msg != "" {
			return msg
		}
		if s != "" && !ValidOp(Op(s)) {
			return "unknown op: " + s
		}
	case "size":
		if msg := str(); msg != "" {
			return msg
		}
		if err := validateRequestSize(s); err != nil {
			return err.Error()
		}
	case "seed_policy":
		if msg := str(); msg != "" {
			return msg
		}
		if !validSeedPolicy(s) {
			return fmt.Sprintf("unknown seed_policy %q: it is one of %s, %s or %s", s, SeedRandom, SeedFixed, SeedSequence)
		}
	case "mask":
		if msg := str(); msg != "" {
			return msg
		}
		if strings.TrimSpace(s) != "" {
			if _, _, err := resolveInputOrigin(s); err != nil {
				return err.Error()
			}
		}
	case "inputs":
		var in []string
		if err := json.Unmarshal(raw, &in); err != nil {
			return "must be a list of paths"
		}
		if len(in) > studioMaxInputs {
			return fmt.Sprintf("at most %d references", studioMaxInputs)
		}
		for _, p := range in {
			if _, _, err := resolveInputOrigin(p); err != nil {
				return err.Error()
			}
		}
	case "count":
		var n int
		if err := json.Unmarshal(raw, &n); err != nil || n < 0 || n > comfyMaxBatch {
			return fmt.Sprintf("count must be a whole number from 1 to %d", comfyMaxBatch)
		}
	case "jobs":
		var n int
		if err := json.Unmarshal(raw, &n); err != nil || n < 0 || n > imagegenQueueMax {
			return fmt.Sprintf("jobs must be a whole number from 1 to %d", imagegenQueueMax)
		}
	case "seed":
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return "seed must be a whole number"
		}
	case "strength":
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil || f <= 0 || f > 1 {
			return "strength must be greater than 0 and at most 1"
		}
	case "params":
		var p EngineParams
		if err := studioStrict(raw, &p); err != nil {
			return "params takes steps, cfg, sampler and scheduler: " + err.Error()
		}
		if err := validateRequestParams(&p); err != nil {
			return err.Error()
		}
	case "loras":
		var ls []loraRequest
		if err := studioStrict(raw, &ls); err != nil {
			return "loras is a list of {name, weight}: " + err.Error()
		}
		for _, l := range ls {
			if strings.TrimSpace(l.Name) == "" {
				return "a lora needs a name"
			}
			if l.Weight < -comfyMaxLoraWeight || l.Weight > comfyMaxLoraWeight {
				return fmt.Sprintf("lora %s: weight must be within ±%g", l.Name, comfyMaxLoraWeight)
			}
		}
	case "full_steps":
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return "must be true or false"
		}
	default:
		return "no such field"
	}
	return ""
}

func draftToMap(d ImageStudioDraft) map[string]json.RawMessage {
	b, _ := json.Marshal(d)
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &m)
	return m
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

// applyDraftPatch applies a merge patch to d as author, with locks applying to the agent only.
// It returns the draft after, the changed fields, and every field it did not apply.
//
// `clear` is set_image_draft's way of saying null (its schema cannot declare a nullable type
// for Gemini-family clients); an explicit null is accepted too.
func applyDraftPatch(d ImageStudioDraft, patch map[string]json.RawMessage, author string, locks []string) (ImageStudioDraft, []DraftChange, []DroppedField) {
	var dropped []DroppedField
	ops := map[string]json.RawMessage{}
	if raw, ok := patch["clear"]; ok {
		var keys []string
		if err := json.Unmarshal(raw, &keys); err != nil {
			dropped = append(dropped, DroppedField{Field: "clear", Reason: dropInvalid, Detail: "clear is a list of field names"})
		}
		for _, k := range keys {
			ops[k] = json.RawMessage("null")
		}
	}
	for k, v := range patch {
		if k != "clear" {
			ops[k] = v
		}
	}
	cur := draftToMap(d)
	keys := make([]string, 0, len(ops))
	for k := range ops {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		raw := ops[k]
		if !slices.Contains(studioDraftKeys, k) {
			dropped = append(dropped, DroppedField{Field: k, Reason: dropInvalid, Detail: "no such field"})
			continue
		}
		if author == studioAuthorAgent && !slices.Contains(ImageStudioAgentFields, k) {
			dropped = append(dropped, DroppedField{Field: k, Reason: dropHumanOnly,
				Detail: "only the user sets this field" + map[bool]string{true: "; propose a model with suggest_model", false: ""}[k == "model"]})
			continue
		}
		if k == "params" && !isJSONNull(raw) {
			merged, bad, ok := mergeDraftParams(cur["params"], raw, author)
			dropped = append(dropped, bad...)
			if !ok {
				continue
			}
			raw = merged
		}
		if !isJSONNull(raw) {
			if msg := validateDraftField(k, raw); msg != "" {
				dropped = append(dropped, DroppedField{Field: k, Reason: dropInvalid, Detail: msg})
				continue
			}
		}
		if author == studioAuthorAgent && slices.Contains(locks, k) {
			dropped = append(dropped, DroppedField{Field: k, Reason: dropLocked, Detail: "the user locked this field"})
			continue
		}
		if isJSONNull(raw) {
			delete(cur, k)
		} else {
			cur[k] = raw
		}
	}
	b, _ := json.Marshal(cur)
	var next ImageStudioDraft
	_ = json.Unmarshal(b, &next)
	return next, draftChanges(d, next), dropped
}

// studioParamKeys are the params a person may set; the agent may set the first four (decision 4
// names steps, cfg, sampler and scheduler). clip_skip rides along for the member because the
// catalogue carries it.
var studioParamKeys = []string{"steps", "cfg", "sampler", "scheduler", "clip_skip"}

// mergeDraftParams is params' own merge patch, one level down: the sampler overlay is four
// independent knobs, and "cfg 5" from the agent must not erase the steps and sampler the member
// set. A null sub-key clears that knob; an empty result clears params. ok is false when nothing
// is to be applied (the value was not an object).
func mergeDraftParams(cur, patch json.RawMessage, author string) (json.RawMessage, []DroppedField, bool) {
	var sub map[string]json.RawMessage
	if err := json.Unmarshal(patch, &sub); err != nil || sub == nil {
		return nil, []DroppedField{{Field: "params", Reason: dropInvalid, Detail: "params is an object of steps, cfg, sampler and scheduler"}}, false
	}
	allowed := studioParamKeys
	if author == studioAuthorAgent {
		allowed = studioParamKeys[:4]
	}
	merged := map[string]json.RawMessage{}
	if len(cur) > 0 {
		_ = json.Unmarshal(cur, &merged)
	}
	var dropped []DroppedField
	keys := make([]string, 0, len(sub))
	for k := range sub {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch {
		case !slices.Contains(allowed, k) && slices.Contains(studioParamKeys, k):
			dropped = append(dropped, DroppedField{Field: "params." + k, Reason: dropHumanOnly, Detail: "only the user sets this"})
		case !slices.Contains(allowed, k):
			dropped = append(dropped, DroppedField{Field: "params." + k, Reason: dropInvalid, Detail: "no such param"})
		case isJSONNull(sub[k]):
			delete(merged, k)
		default:
			merged[k] = sub[k]
		}
	}
	if len(merged) == 0 {
		return json.RawMessage("null"), dropped, true
	}
	b, _ := json.Marshal(merged)
	return b, dropped, true
}

// draftChanges lists the fields that differ between two drafts, in declaration order, with
// their values either side (absent for an empty one).
func draftChanges(before, after ImageStudioDraft) []DraftChange {
	a, b := draftToMap(before), draftToMap(after)
	var out []DraftChange
	for _, k := range studioDraftKeys {
		if bytes.Equal(a[k], b[k]) {
			continue
		}
		if k == "params" {
			// Per knob, so the history reads "params.cfg 7 → 5" rather than two whole objects.
			pa, pb := map[string]json.RawMessage{}, map[string]json.RawMessage{}
			_ = json.Unmarshal(a[k], &pa)
			_ = json.Unmarshal(b[k], &pb)
			for _, sk := range studioParamKeys {
				if !bytes.Equal(pa[sk], pb[sk]) {
					out = append(out, DraftChange{Field: "params." + sk, Before: pa[sk], After: pb[sk]})
				}
			}
			continue
		}
		out = append(out, DraftChange{Field: k, Before: a[k], After: b[k]})
	}
	return out
}

// validLocks keeps the lock list to draft keys, once each.
func validLocks(in []string) ([]string, []DroppedField) {
	var out []string
	var dropped []DroppedField
	for _, k := range in {
		switch {
		case !slices.Contains(studioDraftKeys, k):
			dropped = append(dropped, DroppedField{Field: "locks", Reason: dropInvalid, Detail: "no such field: " + k})
		case !slices.Contains(out, k):
			out = append(out, k)
		}
	}
	return out, dropped
}

// studioNeedsMask is decision 4's derived value: an inpaint with no mask yet.
func studioNeedsMask(d ImageStudioDraft) bool {
	return d.Op == string(OpInpaint) && strings.TrimSpace(d.Mask) == ""
}
