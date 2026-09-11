package main

// engine_pending.go — what the instance has NOT synced yet (ADR 0072 P2 欠落 7 and open
// question 3).
//
// The fetch sidecar releases the engine as soon as the START model is on the instance and
// keeps downloading the rest in the background: `engine may start; 6 file(s) still to sync`.
// The engine is then genuinely up — health passes, the gateway's `engine_waking` is over — and
// a request naming one of the models still coming down the wire gets ComfyUI's bare 400:
//
//	Value not in list: unet_name: 'z_image_turbo_bf16.safetensors' not in ['flux-2-klein-4b.safetensors']
//
// Measured on af-sandbox: a second instance spent ~270 seconds syncing 12 files / 48 GB, and
// that whole window is this. Nothing told the caller — or the operator — that the answer was
// "not yet" rather than "never".
//
// So the sidecar writes the keys it has not fetched to `<base>/<key>/pending` and the gateway
// reads it. What that buys is one word: the refusal becomes `engine_waking`, which every
// caller in the fleet already retries, instead of a validation error from an engine that is
// telling the truth about its own disk.
//
// 🔴 It does NOT change what is OFFERED. `generate_image`'s model enum stays "enabled",
// because "on the instance" is a property of one instance at one moment — it changes under a
// session that already read the catalogue, and a menu that flickers with a download is worse
// than a request that waits.

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// enginePendingParamName is the sidecar's side of the contract: one parameter per engine, a
// JSON array of the S3 keys it has not yet placed on the instance, next to the active set it
// is working from.
func enginePendingParamName(base, key string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + key + "/pending"
}

// enginePending reads that parameter, cached.
//
// The cache is the catalogue's TTL and for the same reason: this is consulted on the same
// requests, and one SSM call per completion would be the mistake reading the engine table per
// request would have been. Ten seconds also bounds how long a request can be told to wait
// for a file that has just landed — the caller retries, and the next read is current.
type enginePending struct {
	api   engineSSMAPI
	param string

	mu sync.Mutex
	// keys is what the instance still owes. nil means UNKNOWN — no parameter, an unreadable
	// one, or a deployment whose sidecar predates this — and unknown must never hold a
	// request: the fleet ran without this check at all until now.
	keys   map[string]struct{}
	at     time.Time
	loaded bool
	// loggedAt is when each (role, model) last put a refusal line in the log. Keyed rather
	// than a single timestamp because two models can be syncing at once and the operator
	// needs to see both. Bounded by the catalogue: the guard only ever reaches the log for
	// an id the enabled catalogue holds.
	loggedAt map[string]time.Time
}

// enginePendingLogEvery is how often ONE (role, model) may appear in the log while it is
// syncing.
//
// The caller retries a 503 every few seconds — generate_image's provider does it for up to
// a quarter of an hour — and a ~270 second sync is the measured case, so logging every
// refusal turns one wait into dozens of identical lines and buries whatever else the CP was
// saying. A minute keeps the fact visible for the whole window at a handful of lines.
//
// A var only so a test can defeat the throttle and show that the throttle — rather than the
// refusal simply never happening twice — is what suppresses the second line. Never written
// at runtime.
var enginePendingLogEvery = 60 * time.Second

func newEnginePending(api engineSSMAPI, base, key string) *enginePending {
	param := enginePendingParamName(base, key)
	if api == nil || param == "" {
		return nil // a dev CP with an inline table: nothing publishes this and nothing reads it
	}
	return &enginePending{api: api, param: param}
}

// missing answers which of a model's files the instance has not got yet, in the order the row
// declares them. Empty for everything this cannot answer — see `keys`.
func (p *enginePending) missing(ctx context.Context, m store.EngineModel) []string {
	if p == nil {
		return nil
	}
	keys := p.read(ctx)
	if len(keys) == 0 {
		return nil
	}
	var out []string
	for _, f := range m.Files {
		k := strings.TrimSpace(f.S3Key)
		if k == "" {
			continue
		}
		if _, ok := keys[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// claimLogSlot reports whether a refusal for this (role, model) may be logged now, and
// records it when it may. It shares the mutex with the cache above on purpose: the refusal
// it is rate-limiting is decided from that same cached read, one call apart.
//
// 🔴 It MARKS. Calling it to ask the question and then not logging silences the next
// minute for that pair.
func (p *enginePending) claimLogSlot(role, id string, now time.Time) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := role + "\x00" + id
	if last, ok := p.loggedAt[k]; ok && now.Sub(last) < enginePendingLogEvery {
		return false
	}
	if p.loggedAt == nil {
		p.loggedAt = map[string]time.Time{}
	}
	p.loggedAt[k] = now
	return true
}

func (p *enginePending) read(ctx context.Context) map[string]struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded && time.Since(p.at) < engineCatalogCacheTTL {
		return p.keys
	}
	out, err := p.api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(p.param)})
	if err != nil {
		// Including ParameterNotFound, which is the ordinary state of a deployment whose
		// engine stack predates this: the answer is "unknown", the request goes through, and
		// the behaviour is exactly what it was before the check existed. Cached like a real
		// answer so a missing parameter is not one SSM call per request.
		p.keys, p.at, p.loaded = nil, time.Now(), true
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &list); err != nil {
		// 🔴 Not treated as "nothing pending". A parameter that cannot be read is a sidecar
		// this CP does not understand, and guessing "everything is there" is the guess that
		// reproduces the bare 400 this exists to replace.
		log.Printf("engines: %s is not a list of keys: %v", p.param, err)
		p.keys, p.at, p.loaded = nil, time.Now(), true
		return nil
	}
	keys := make(map[string]struct{}, len(list))
	for _, k := range list {
		if k = strings.TrimSpace(k); k != "" {
			keys[k] = struct{}{}
		}
	}
	p.keys, p.at, p.loaded = keys, time.Now(), true
	return keys
}
