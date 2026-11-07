// engine_comfy_guard.go — what the engine gateway lets through to a ComfyUI engine, and whose
// jobs a caller may touch.
//
// ComfyUI has no notion of a user: its history, queue, view and interrupt routes cover every job
// on the box, and one box serves every member and tenant of the deployment. The gateway used to
// relay any method and path, so anybody holding an engine session token could read other people's
// prompts and pictures and stop their jobs. Two rules close that:
//
//  1. Allow-list. Only the routes the image features call are relayed, each with a fixed method.
//     Everything else (queue-wide reads, history-wide reads, free / clear / interrupt-everything,
//     the websocket, object_info, …) is refused for every caller.
//  2. Ownership. The membership that submitted a prompt is recorded when /prompt answers, and
//     status, output and control requests are served only for prompt ids that membership owns.
//     /view is gated the same way through the output files a membership's own history named.
//
// The owner map lives in this process. After a Control Plane restart it is empty, so jobs
// submitted before the restart are refused (fail closed) while new ones work; the Agent's retry
// resubmits, and ComfyUI's own result cache makes that cheap.
//
// No super_admin exemption: a super_admin is an identity role the engine session token does not
// carry, and the operator's own ComfyUI view is the admin panel, not this relay.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// engineJobOwnerTTL outlives any generation (the Agent's run budget is well under it).
	engineJobOwnerTTL = 24 * time.Hour
	// engineJobOwnerMax is a hard cap on each map: past it the oldest entries are evicted.
	engineJobOwnerMax = 50000
	// engineComfyIDMax bounds the length of a prompt id accepted into a path.
	engineComfyIDMax = 128
)

// engineJobOwners maps prompt ids and files to the membership that produced them.
type engineJobOwners struct {
	mu      sync.Mutex
	prompts map[string]jobOwnerEntry
	files   map[string]time.Time // membership \x00 type \x00 subfolder \x00 filename
	max     int
	now     func() time.Time
}

type jobOwnerEntry struct {
	mid string
	at  time.Time
}

func newEngineJobOwners() *engineJobOwners {
	return &engineJobOwners{prompts: map[string]jobOwnerEntry{}, files: map[string]time.Time{},
		max: engineJobOwnerMax, now: time.Now}
}

// addPrompt records the submitter. An id already held by another membership is never taken over:
// ownership is decided by the first recorded submitter.
func (o *engineJobOwners) addPrompt(mid, id string) {
	if o == nil || mid == "" || id == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t := o.now()
	if e, ok := o.prompts[id]; ok && e.mid != mid && t.Sub(e.at) < engineJobOwnerTTL {
		return
	}
	if _, ok := o.prompts[id]; !ok && len(o.prompts) >= o.max {
		o.evictPromptsLocked(t)
	}
	o.prompts[id] = jobOwnerEntry{mid: mid, at: t}
}

func (o *engineJobOwners) ownsPrompt(mid, id string) bool {
	if o == nil || mid == "" || id == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.prompts[id]
	return ok && e.mid == mid && o.now().Sub(e.at) < engineJobOwnerTTL
}

func engineFileKey(mid, kind, sub, name string) string {
	return mid + "\x00" + kind + "\x00" + sub + "\x00" + name
}

func (o *engineJobOwners) addFile(mid, kind, sub, name string) {
	if o == nil || mid == "" || name == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t := o.now()
	k := engineFileKey(mid, kind, sub, name)
	if _, ok := o.files[k]; !ok && len(o.files) >= o.max {
		o.evictFilesLocked(t)
	}
	o.files[k] = t
}

func (o *engineJobOwners) ownsFile(mid, kind, sub, name string) bool {
	if o == nil || mid == "" || name == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	at, ok := o.files[engineFileKey(mid, kind, sub, name)]
	return ok && o.now().Sub(at) < engineJobOwnerTTL
}

// evictCount is how many entries leave when a map is full: expired ones first, then the oldest
// tenth, so the map never exceeds its cap and eviction is amortised.
func (o *engineJobOwners) evictCount() int { return max(1, o.max/10) }

func (o *engineJobOwners) evictPromptsLocked(t time.Time) {
	for k, e := range o.prompts {
		if t.Sub(e.at) >= engineJobOwnerTTL {
			delete(o.prompts, k)
		}
	}
	if len(o.prompts) < o.max {
		return
	}
	keys := make([]string, 0, len(o.prompts))
	for k := range o.prompts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return o.prompts[keys[i]].at.Before(o.prompts[keys[j]].at) })
	for _, k := range keys[:min(len(keys), o.evictCount())] {
		delete(o.prompts, k)
	}
}

func (o *engineJobOwners) evictFilesLocked(t time.Time) {
	for k, at := range o.files {
		if t.Sub(at) >= engineJobOwnerTTL {
			delete(o.files, k)
		}
	}
	if len(o.files) < o.max {
		return
	}
	keys := make([]string, 0, len(o.files))
	for k := range o.files {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return o.files[keys[i]].Before(o.files[keys[j]]) })
	for _, k := range keys[:min(len(keys), o.evictCount())] {
		delete(o.files, k)
	}
}

// jobOwners is the engine's owner map, created on first use.
func (e *engineRuntimeState) jobOwners() *engineJobOwners {
	e.jobsOnce.Do(func() {
		if e.jobs == nil {
			e.jobs = newEngineJobOwners()
		}
	})
	return e.jobs
}

// comfyResponseHook sees the upstream answer before the caller does. It may return a replacement
// body (the queue filter); a nil return keeps the original.
type comfyResponseHook func(status int, body []byte) []byte

// comfyDecision is the guard's verdict on one request. When body is non-nil the relay sends it
// (and ctype, when set) instead of what the caller sent: what is forwarded is what was
// validated, never the caller's bytes.
type comfyDecision struct {
	err   *apiError
	hook  comfyResponseHook
	body  []byte
	ctype string
}

func comfyRefuse(msg string) comfyDecision {
	return comfyDecision{err: &apiError{http.StatusForbidden, "engine_route_forbidden", msg}}
}

// comfyScope is the per-membership folder uploads and outputs are confined to, so the engine's
// shared input and output directories never mix two members' files.
func comfyScope(mid string) string {
	sum := sha256.Sum256([]byte(mid))
	return "af-" + hex.EncodeToString(sum[:8])
}

// comfyPromptIDOK is the shape of a ComfyUI prompt id; anything else never reaches a path.
func comfyPromptIDOK(id string) bool {
	if id == "" || len(id) > engineComfyIDMax {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// comfyGraphClasses are the node classes the Agent's workflow templates emit. Any other class —
// notably a custom node that reads files — is refused, so the set of graph nodes able to touch the
// engine's disk is exactly the two image loaders (checked below) and SaveImage (confined below).
// A template that starts using a new class needs it added here.
var comfyGraphClasses = map[string]bool{
	"CLIPTextEncode": true, "VAEDecode": true, "SaveImage": true, "VAELoader": true, "UNETLoader": true,
	"KSampler": true, "CLIPLoader": true, "LoadImage": true, "LoadImageMask": true,
	"ModelSamplingAuraFlow": true, "VAEEncode": true, "TextEncodeQwenImageEditPlus": true,
	"SetLatentNoiseMask": true, "SamplerCustomAdvanced": true, "RandomNoise": true,
	"KSamplerSelect": true, "FluxKontextMultiReferenceLatentMethod": true, "CheckpointLoaderSimple": true,
	"TripleCLIPLoader": true, "TextEncodeQwenImage21": true, "SplitSigmasDenoise": true,
	"SplitImageWithAlpha": true, "LoraLoader": true, "ImageToMask": true, "ImageScale": true,
	"FluxGuidance": true, "Flux2Scheduler": true, "EmptyLatentImage": true, "DualCLIPLoader": true,
	"ConditioningZeroOut": true, "CLIPSetLastLayer": true, "CFGNorm": true, "CFGGuider": true,
	"BasicScheduler": true, "BasicGuider": true, "EmptySD3LatentImage": true, "EmptyFlux2LatentImage": true,
}

// comfyCheckSubmission validates the /prompt body and returns the body to forward: only the
// known top-level keys (a caller-chosen prompt_id or queue position is refused), only known node
// classes, image loaders confined to the caller's own uploads, and SaveImage confined to the
// caller's own output folder.
func comfyCheckSubmission(owners *engineJobOwners, mid string, body []byte) ([]byte, bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil, false
	}
	for k := range top {
		if k != "prompt" && k != "client_id" && k != "extra_data" {
			return nil, false
		}
	}
	var graph map[string]map[string]json.RawMessage
	if json.Unmarshal(top["prompt"], &graph) != nil || len(graph) == 0 {
		return nil, false
	}
	scope := comfyScope(mid)
	out := make(map[string]map[string]any, len(graph))
	for id, node := range graph {
		var class string
		var inputs map[string]any
		if json.Unmarshal(node["class_type"], &class) != nil || !comfyGraphClasses[class] ||
			json.Unmarshal(node["inputs"], &inputs) != nil || inputs == nil {
			return nil, false
		}
		switch class {
		case "LoadImage", "LoadImageMask":
			ref, ok := inputs["image"].(string)
			if !ok || !comfyOwnedInputRef(owners, mid, ref) {
				return nil, false
			}
		case "SaveImage":
			prefix, ok := inputs["filename_prefix"].(string)
			if !ok {
				return nil, false
			}
			if i := strings.LastIndexAny(prefix, `/\`); i >= 0 {
				prefix = prefix[i+1:]
			}
			if prefix == "" || prefix == "." || prefix == ".." {
				prefix = "af"
			}
			inputs["filename_prefix"] = scope + "/" + prefix
		}
		out[id] = map[string]any{"class_type": class, "inputs": inputs}
	}
	rebuilt := map[string]any{"prompt": out}
	for _, k := range []string{"client_id", "extra_data"} {
		if v, ok := top[k]; ok {
			rebuilt[k] = v
		}
	}
	b, err := json.Marshal(rebuilt)
	return b, err == nil
}

// comfyOwnedInputRef reports whether an image reference names a file this membership uploaded:
// "<folder>/<name>" or a bare name, never an annotated one ("[output]", "[temp]").
func comfyOwnedInputRef(owners *engineJobOwners, mid, ref string) bool {
	if strings.ContainsAny(ref, "[]\\") || strings.Contains(ref, "..") {
		return false
	}
	sub, name := "", ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		sub, name = ref[:i], ref[i+1:]
	}
	return owners.ownsFile(mid, "input", sub, name)
}

// comfyRewriteUpload validates an upload and rebuilds it: input type only, never overwrite, the
// folder fixed to the caller's scope, and a plain file name. Nothing else the caller sent is
// forwarded.
func comfyRewriteUpload(mid, ctype string, body []byte) ([]byte, string, bool) {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", false
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var file []byte
	var name string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", false
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, "", false
		}
		switch p.FormName() {
		case "image":
			if file != nil {
				return nil, "", false
			}
			file, name = data, p.FileName()
		case "type":
			if v := string(data); v != "" && v != "input" {
				return nil, "", false
			}
		case "overwrite":
			if v := strings.ToLower(string(data)); v != "" && v != "false" && v != "0" {
				return nil, "", false
			}
		}
	}
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if file == nil || name == "" || name == "." || name == ".." || len(name) > 200 {
		return nil, "", false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return nil, "", false
		}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", name)
	if err != nil {
		return nil, "", false
	}
	_, _ = part.Write(file)
	_ = mw.WriteField("type", "input")
	_ = mw.WriteField("subfolder", comfyScope(mid))
	if mw.Close() != nil {
		return nil, "", false
	}
	return buf.Bytes(), mw.FormDataContentType(), true
}

// comfyGuard decides one request to a ComfyUI engine.
func comfyGuard(owners *engineJobOwners, mid, method, path, ctype string, query url.Values, rawQuery string, body []byte) comfyDecision {
	switch {
	case method == http.MethodPost && path == "prompt":
		nb, ok := comfyCheckSubmission(owners, mid, body)
		if !ok {
			return comfyRefuse("this prompt is outside what the gateway allows")
		}
		return comfyDecision{body: nb, hook: func(status int, resp []byte) []byte {
			if status >= 300 {
				return nil
			}
			var doc struct {
				PromptID string `json:"prompt_id"`
			}
			if json.Unmarshal(resp, &doc) == nil && comfyPromptIDOK(doc.PromptID) {
				owners.addPrompt(mid, doc.PromptID)
			}
			return nil
		}}

	case method == http.MethodPost && path == "upload/image":
		nb, nct, ok := comfyRewriteUpload(mid, ctype, body)
		if !ok {
			return comfyRefuse("only a plain input upload is allowed")
		}
		return comfyDecision{body: nb, ctype: nct, hook: func(status int, resp []byte) []byte {
			if status >= 300 {
				return nil
			}
			var doc struct {
				Name      string `json:"name"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
			}
			if json.Unmarshal(resp, &doc) == nil && doc.Subfolder == comfyScope(mid) && (doc.Type == "" || doc.Type == "input") {
				owners.addFile(mid, "input", doc.Subfolder, doc.Name)
			}
			return nil
		}}

	case method == http.MethodGet && strings.HasPrefix(path, "history/"):
		id := strings.TrimPrefix(path, "history/")
		if !comfyPromptIDOK(id) || rawQuery != "" || !owners.ownsPrompt(mid, id) {
			return comfyRefuse("this prompt is not yours, or is unknown to this gateway")
		}
		return comfyDecision{hook: func(status int, resp []byte) []byte {
			if status >= 300 {
				return nil
			}
			var byID map[string]struct {
				Outputs map[string]struct {
					Images []struct {
						Filename  string `json:"filename"`
						Subfolder string `json:"subfolder"`
						Type      string `json:"type"`
					} `json:"images"`
				} `json:"outputs"`
			}
			if json.Unmarshal(resp, &byID) != nil {
				return nil
			}
			// Only the entry for the id that was asked for, and only files in the caller's own
			// output folder (where its SaveImage nodes were confined).
			for _, o := range byID[id].Outputs {
				for _, im := range o.Images {
					if im.Subfolder == comfyScope(mid) {
						owners.addFile(mid, im.Type, im.Subfolder, im.Filename)
					}
				}
			}
			return nil
		}}

	case method == http.MethodGet && path == "view":
		for _, k := range []string{"filename", "subfolder", "type"} {
			if len(query[k]) != 1 {
				return comfyRefuse("this file is not an output of your own jobs")
			}
		}
		if !owners.ownsFile(mid, query.Get("type"), query.Get("subfolder"), query.Get("filename")) {
			return comfyRefuse("this file is not an output of your own jobs")
		}
		return comfyDecision{}

	case method == http.MethodGet && path == "queue":
		// Not refused: the Agent reads its own prompt's place in the queue. The answer is cut
		// down to the caller's own entries.
		return comfyDecision{hook: func(status int, resp []byte) []byte {
			if status >= 300 {
				return nil
			}
			return comfyFilterQueue(owners, mid, resp)
		}}

	case method == http.MethodPost && path == "queue":
		var doc map[string]json.RawMessage
		var ids []string
		if json.Unmarshal(body, &doc) != nil || len(doc) != 1 || json.Unmarshal(doc["delete"], &ids) != nil || len(ids) == 0 {
			return comfyRefuse("only deleting your own queued prompts is allowed")
		}
		for _, id := range ids {
			if !owners.ownsPrompt(mid, id) {
				return comfyRefuse("this prompt is not yours, or is unknown to this gateway")
			}
		}
		nb, _ := json.Marshal(map[string][]string{"delete": ids})
		return comfyDecision{body: nb}

	case method == http.MethodPost && path == "interrupt":
		// Exact-key parse: the engine reads exactly "prompt_id", so that key and nothing else
		// is accepted, and the body forwarded is rebuilt from the approved id alone.
		var doc map[string]json.RawMessage
		var id string
		if json.Unmarshal(body, &doc) != nil || len(doc) != 1 || json.Unmarshal(doc["prompt_id"], &id) != nil ||
			!owners.ownsPrompt(mid, id) {
			return comfyRefuse("interrupt needs the id of one of your own prompts")
		}
		nb, _ := json.Marshal(map[string]string{"prompt_id": id})
		return comfyDecision{body: nb}
	}
	return comfyRefuse("this engine route is not available through the gateway")
}

// comfyFilterQueue keeps the entries of GET /queue whose prompt id (the second element of each
// tuple) the caller owns. An answer that is not the expected shape becomes an empty queue rather
// than being passed through.
func comfyFilterQueue(owners *engineJobOwners, mid string, resp []byte) []byte {
	var doc map[string]json.RawMessage
	out := map[string]json.RawMessage{"queue_running": json.RawMessage("[]"), "queue_pending": json.RawMessage("[]")}
	if json.Unmarshal(resp, &doc) == nil {
		for _, k := range []string{"queue_running", "queue_pending"} {
			var entries []json.RawMessage
			if json.Unmarshal(doc[k], &entries) != nil {
				continue
			}
			kept := []json.RawMessage{}
			for _, e := range entries {
				var tuple []json.RawMessage
				if json.Unmarshal(e, &tuple) != nil || len(tuple) < 2 {
					continue
				}
				var id string
				if json.Unmarshal(tuple[1], &id) == nil && owners.ownsPrompt(mid, id) {
					kept = append(kept, e)
				}
			}
			b, _ := json.Marshal(kept)
			out[k] = b
		}
	}
	b, _ := json.Marshal(out)
	return bytes.TrimSpace(b)
}

// comfyReadBody reads the request body once (bounded) so the guard can inspect it and the relay
// can still send it.
func comfyReadBody(r *http.Request) ([]byte, *apiError) {
	b, err := io.ReadAll(io.LimitReader(r.Body, engineMaxRequestBody))
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "bad_request", "could not read the request body"}
	}
	return b, nil
}
