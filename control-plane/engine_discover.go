package main

// engine_discover.go — the button behind ADR 0082 decisions 6 and 7: what checkpoint, LoRA and
// VAE filenames an external ComfyUI's own /object_info says it currently has on disk, offered
// to the admin panel as CANDIDATES for a row.
//
// What this deliberately does NOT do, and why:
//
//   - it never writes a catalogue row. `base_model` — the one field a comfy row cannot generate
//     without — is not something ComfyUI publishes at all, and a guessed one would silence
//     `base_model_missing`, the row's only mark that it cannot generate (ADR 0082 decision 6,
//     ADR 0072 decision 2). This fills in a filename and, for a checkpoint, a family SUGGESTION
//     from engineFamilyGuess — the same function the ingest form's upstream metadata already
//     goes through — and stops there. Turning a candidate into a row is postModel, unchanged.
//   - it never polls. It runs once, when a person presses the button (ADR 0082 decision 7): the
//     machine on the other end is somebody's own PC, and its files change only when a person put
//     one there.
//   - it never calls ensureReady / ensureStarted. Those exist to buy a GPU box, and an external
//     row has no box here to buy (ADR 0076 decision 4) — this dials the row's URL directly, the
//     same way engineHealthy does, and answers "not reachable" rather than waiting on a start
//     that would never come.
//   - it only ever reads a REMOTE (borrowed) row's mirror over here, never its own network: that
//     catalogue is the far deployment's read-only mirror (ADR 0079 decision 7), and there is
//     nothing on THIS deployment's LAN behind it to discover.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// engineComfyDiscoverLoaders is the ComfyUI node this reads, the enum input it publishes the
// filenames under, and the catalogue kind those filenames become a candidate for. Three
// separate requests rather than the unscoped /object_info dump: that answer runs to several
// megabytes on a real installation (every node ComfyUI knows), while asking by name is the same
// few kilobytes CheckpointLoaderSimple's own enum is.
var engineComfyDiscoverLoaders = []struct{ node, field, kind string }{
	{"CheckpointLoaderSimple", "ckpt_name", "checkpoint"},
	{"LoraLoader", "lora_name", "lora"},
	{"VAELoader", "vae_name", "vae"},
}

// engineDiscoverCandidate is one file the external row's own folders hold, not yet a row.
type engineDiscoverCandidate struct {
	Name string `json:"name"`
	// BaseModelSuggest is engineFamilyGuess read off the filename, present only for a checkpoint
	// and only when it recognises one — never the field a person has not confirmed (ADR 0082
	// decision 6).
	BaseModelSuggest string `json:"base_model_suggest,omitempty"`
}

// engineDiscoverResult is what the button answers with. A nil slice and an empty one are the
// same thing on the wire (both render "[]"); what distinguishes "this loader has nothing" from
// "this loader does not exist on this ComfyUI build" is not carried, on purpose — a person
// reads either as "nothing to offer here" and the distinction changes nothing they would do.
type engineDiscoverResult struct {
	Checkpoints []engineDiscoverCandidate `json:"checkpoints"`
	Loras       []engineDiscoverCandidate `json:"loras"`
	Vaes        []engineDiscoverCandidate `json:"vaes"`
}

// engineDiscoverCache is one row's last discovery, bounded and cached exactly like the warm
// probe next door (ADR 0076 decision 8's 2-second timeout and 10-second TTL, reused rather than
// duplicated): the lock is held ACROSS the call, so two panels open on the same row at once cost
// one round trip to the LAN box rather than two, and the cache is what keeps a person pressing
// the button twice from being a second one.
type engineDiscoverCache struct {
	mu     sync.Mutex
	at     time.Time
	result engineDiscoverResult
	err    error
}

// discoverModels answers the button. ctx carries the caller's deadline; the probe itself is
// bounded far tighter (engineExternalWarmTimeout) regardless of what the caller allows, for the
// same reason the health check is — this runs on the admin panel's own synchronous load path.
func (e *engineRuntimeState) discoverModels(ctx context.Context) (engineDiscoverResult, error) {
	e.discover.mu.Lock()
	defer e.discover.mu.Unlock()
	if !e.discover.at.IsZero() && time.Since(e.discover.at) < engineExternalWarmTTL {
		return e.discover.result, e.discover.err
	}
	res, err := engineDiscoverProbe(ctx, e)
	e.discover.result, e.discover.err, e.discover.at = res, err, time.Now()
	return res, err
}

// engineDiscoverProbe is the three dials, concurrent so the wall-clock cost is one round trip
// and not three, all bounded by the one timeout ADR 0076 decision 8 set for a call an admin
// panel's synchronous list handler pays for.
func engineDiscoverProbe(ctx context.Context, e *engineRuntimeState) (engineDiscoverResult, error) {
	c, cancel := context.WithTimeout(ctx, engineExternalWarmTimeout)
	defer cancel()

	type outcome struct {
		kind  string
		names []string
		err   error
	}
	out := make(chan outcome, len(engineComfyDiscoverLoaders))
	for _, l := range engineComfyDiscoverLoaders {
		go func(node, field, kind string) {
			names, err := engineObjectInfoEnum(c, e, node, field)
			out <- outcome{kind, names, err}
		}(l.node, l.field, l.kind)
	}

	var res engineDiscoverResult
	var firstErr error
	answered := 0
	for range engineComfyDiscoverLoaders {
		o := <-out
		if o.err != nil {
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		answered++
		cands := make([]engineDiscoverCandidate, 0, len(o.names))
		for _, name := range o.names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			cand := engineDiscoverCandidate{Name: name}
			if o.kind == "checkpoint" {
				cand.BaseModelSuggest = engineFamilyGuess(e.def.Provider, name)
			}
			cands = append(cands, cand)
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].Name < cands[j].Name })
		switch o.kind {
		case "checkpoint":
			res.Checkpoints = cands
		case "lora":
			res.Loras = cands
		case "vae":
			res.Vaes = cands
		}
	}
	// Every loader failed the same way a health check does: the host is not answering at all,
	// and the honest answer is the same sentence ensureStarted refuses a generation with, not an
	// empty list that reads as "this PC has nothing installed".
	if answered == 0 {
		return engineDiscoverResult{}, firstErr
	}
	return res, nil
}

// comfyObjectInfoNode is the one shape this reads out of ComfyUI's /object_info/<node> answer:
// each input's enum is `[[...names...], {...}]`, and everything else about the node (outputs,
// category, display name) is ignored.
type comfyObjectInfoNode struct {
	Input struct {
		Required map[string]json.RawMessage `json:"required"`
	} `json:"input"`
}

// engineObjectInfoEnum asks one ComfyUI loader node what it enumerates for one field, and
// returns the file names. A short, capped body: this is a single node's schema, not the whole
// tree — real installations answer the unscoped /object_info in megabytes, which is why this
// asks by name at all.
func engineObjectInfoEnum(ctx context.Context, e *engineRuntimeState, node, field string) ([]string, error) {
	url := strings.TrimRight(e.def.URL, "/") + "/object_info/" + node
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := engineClient.Do(req)
	if err != nil {
		return nil, errNotAnswering(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("%s answered object_info/%s with status %d", e.def.URL, node, resp.StatusCode)
	}
	var doc map[string]comfyObjectInfoNode
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s answered object_info/%s with unreadable JSON: %w", e.def.URL, node, err)
	}
	n, ok := doc[node]
	if !ok {
		// A build of ComfyUI that does not have this node at all — a stripped-down install with
		// no LoraLoader, say. Not "the host is down": the other two loaders may still answer, and
		// engineDiscoverProbe's `answered == 0` is what tells the two cases apart.
		return nil, fmt.Errorf("%s does not have a node called %s", e.def.URL, node)
	}
	raw, ok := n.Input.Required[field]
	if !ok {
		return nil, fmt.Errorf("%s's %s declares no %s input", e.def.URL, node, field)
	}
	var enum []json.RawMessage
	if err := json.Unmarshal(raw, &enum); err != nil || len(enum) == 0 {
		return nil, fmt.Errorf("%s's %s.%s is not an enumeration", e.def.URL, node, field)
	}
	var names []string
	if err := json.Unmarshal(enum[0], &names); err != nil {
		return nil, fmt.Errorf("%s's %s.%s is not a list of names", e.def.URL, node, field)
	}
	return names, nil
}

// errNotAnswering is what a dial failure means for this row: exactly what ensureStarted refuses
// a generation with, so the operator reads the same sentence whichever door found the machine
// off (ADR 0082 decision 7).
func errNotAnswering(e *engineRuntimeState) error {
	return fmt.Errorf("%s", engineExternalUnreachableMsg(e.def))
}

// discoverModelsGrant (POST /api/admin/engines/{key}/discover) is the route the button calls.
// Under ingest authority like the rest of the ingest form (engine_ingest_perm.go) rather than
// super_admin only: this is the other way a row's files get chosen instead of typed, and a
// granted tenant_admin who may take a model in may read what one offers just as well.
func (a engineAdminAPI) discoverModelsGrant(w http.ResponseWriter, r *http.Request, _ engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	// A borrowed row's catalogue is a read-only mirror of the far deployment's (ADR 0079
	// decision 7), and there is nothing on THIS deployment's network behind it — the LAN it
	// would dial is the far side's, not this one's (ADR 0082 decision 7).
	if e.def.remote() {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineNotOurs,
			"engine " + key + " is borrowed from " + e.def.URL + ", and its catalogue is a read-only" +
				" mirror — there is nothing on this deployment's own network to discover"})
		return
	}
	// Scoped to what decision 6 is actually about: an external ComfyUI this CP can dial
	// directly. A managed comfy row is asleep most of the time and starting it to answer this
	// button would be exactly the GPU purchase decision 7 forbids; a non-comfy provider (an LLM
	// role, or the retired sdcpp vocabulary) has no /object_info at all.
	if !e.def.external() || e.def.Provider != "comfy" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineDiscoverUnsupported,
			"engine " + key + " is not an external ComfyUI row — discovery only reads a LAN machine" +
				" this deployment does not manage and never starts"})
		return
	}
	res, err := e.discoverModels(r.Context())
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, errCodeEngineDiscoverUnreachable, err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
