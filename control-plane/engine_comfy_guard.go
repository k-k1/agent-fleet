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
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// engineJobOwnerTTL outlives any generation (the Agent's run budget is well under it) and
	// bounds the map.
	engineJobOwnerTTL = 24 * time.Hour
	// engineJobOwnerMax caps each map; the oldest half is dropped when it is hit.
	engineJobOwnerMax = 50000
	// engineComfyIDMax bounds the length of a prompt id accepted into a path.
	engineComfyIDMax = 128
)

// engineJobOwners maps prompt ids and output files to the membership that produced them.
type engineJobOwners struct {
	mu      sync.Mutex
	prompts map[string]jobOwnerEntry
	files   map[string]time.Time // membership \x00 type \x00 subfolder \x00 filename
	now     func() time.Time
}

type jobOwnerEntry struct {
	mid string
	at  time.Time
}

func newEngineJobOwners() *engineJobOwners {
	return &engineJobOwners{prompts: map[string]jobOwnerEntry{}, files: map[string]time.Time{}, now: time.Now}
}

func (o *engineJobOwners) addPrompt(mid, id string) {
	if o == nil || mid == "" || id == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t := o.now()
	if len(o.prompts) >= engineJobOwnerMax {
		o.sweepLocked(t)
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
	if len(o.files) >= engineJobOwnerMax {
		o.sweepLocked(t)
	}
	o.files[engineFileKey(mid, kind, sub, name)] = t
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

// sweepLocked drops expired entries, and the older half when that was not enough.
func (o *engineJobOwners) sweepLocked(t time.Time) {
	for k, e := range o.prompts {
		if t.Sub(e.at) >= engineJobOwnerTTL {
			delete(o.prompts, k)
		}
	}
	for k, at := range o.files {
		if t.Sub(at) >= engineJobOwnerTTL {
			delete(o.files, k)
		}
	}
	if len(o.prompts) >= engineJobOwnerMax {
		cut := t.Add(-engineJobOwnerTTL / 2)
		for k, e := range o.prompts {
			if e.at.Before(cut) {
				delete(o.prompts, k)
			}
		}
	}
	if len(o.files) >= engineJobOwnerMax {
		cut := t.Add(-engineJobOwnerTTL / 2)
		for k, at := range o.files {
			if at.Before(cut) {
				delete(o.files, k)
			}
		}
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

func comfyRefuse(msg string) *apiError {
	return &apiError{http.StatusForbidden, "engine_route_forbidden", msg}
}

// comfyPromptIDOK is the shape of a ComfyUI prompt id (a UUID); anything else never reaches a
// path.
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

// comfyGuard decides one request to a ComfyUI engine. It returns a refusal, or nil plus an
// optional response hook.
func comfyGuard(owners *engineJobOwners, mid, method, path string, query url.Values, rawQuery string, body []byte) (*apiError, comfyResponseHook) {
	switch {
	case method == http.MethodPost && path == "prompt":
		return nil, func(status int, resp []byte) []byte {
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
		}

	case method == http.MethodPost && path == "upload/image":
		return nil, nil

	case method == http.MethodGet && strings.HasPrefix(path, "history/"):
		id := strings.TrimPrefix(path, "history/")
		if !comfyPromptIDOK(id) || rawQuery != "" || !owners.ownsPrompt(mid, id) {
			return comfyRefuse("this prompt is not yours, or is unknown to this gateway"), nil
		}
		return nil, func(status int, resp []byte) []byte {
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
			// Only the entry for the id that was asked for: a history answer is a map, and
			// nothing else in it is this caller's.
			for _, o := range byID[id].Outputs {
				for _, im := range o.Images {
					owners.addFile(mid, im.Type, im.Subfolder, im.Filename)
				}
			}
			return nil
		}

	case method == http.MethodGet && path == "view":
		if !owners.ownsFile(mid, query.Get("type"), query.Get("subfolder"), query.Get("filename")) {
			return comfyRefuse("this file is not an output of your own jobs"), nil
		}
		return nil, nil

	case method == http.MethodGet && path == "queue":
		// Not refused: the Agent reads its own prompt's place in the queue. The answer is cut
		// down to the caller's own entries.
		return nil, func(status int, resp []byte) []byte {
			if status >= 300 {
				return nil
			}
			return comfyFilterQueue(owners, mid, resp)
		}

	case method == http.MethodPost && path == "queue":
		var doc map[string]json.RawMessage
		if json.Unmarshal(body, &doc) != nil || len(doc) != 1 {
			return comfyRefuse("only deleting your own queued prompts is allowed"), nil
		}
		raw, ok := doc["delete"]
		var ids []string
		if !ok || json.Unmarshal(raw, &ids) != nil || len(ids) == 0 {
			return comfyRefuse("only deleting your own queued prompts is allowed"), nil
		}
		for _, id := range ids {
			if !owners.ownsPrompt(mid, id) {
				return comfyRefuse("this prompt is not yours, or is unknown to this gateway"), nil
			}
		}
		return nil, nil

	case method == http.MethodPost && path == "interrupt":
		var doc struct {
			PromptID string `json:"prompt_id"`
		}
		if json.Unmarshal(body, &doc) != nil || !owners.ownsPrompt(mid, doc.PromptID) {
			return comfyRefuse("interrupt needs the id of one of your own prompts"), nil
		}
		return nil, nil
	}
	return comfyRefuse("this engine route is not available through the gateway"), nil
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
