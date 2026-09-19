package main

// engine_storage.go — the read-only S3 view behind the model catalogue.
//
// The bucket is not the catalogue: S3 says whether bytes exist, while the database says which
// keys this deployment knows and which models use them (ADR 0072 decision 2). What is here is the
// port and the cache — HeadObject for one key, ListObjectsV2 for one role's prefix, and a Range
// GetObject of one file's first bytes — plus `engineKnownArtifacts`, the database half every
// reuse decision is made against. It takes no key from a caller: the ledger (engine_objects.go)
// and the plan (engine_plan.go) decide which keys are asked about, each inside the grant its
// caller holds.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	engineStoragePresent = "present"
	engineStorageMissing = "missing"
	engineStorageUnknown = "unknown"

	engineStorageCacheTTL    = 30 * time.Second
	engineStorageHeadTimeout = 8 * time.Second
	engineStorageListTimeout = 20 * time.Second
	engineStorageConcurrency = 4
	// A prefix read is a transfer and not a metadata call: up to 16 MiB (safetensorsHeadMax) of
	// a file whose body is gigabytes, so it gets the listing's budget rather than HeadObject's.
	engineStoragePrefixTimeout = 20 * time.Second
)

// engineStorageMetadataPort is the deployment-neutral object metadata boundary. Implementations
// classify absence themselves because a cloud provider's permission model is part of that
// classification, not something the catalogue or reuse policy should know.
//
// List is the ledger's read (ADR 0085 decision 2) and is the one operation here that answers
// about keys the server does not already know. It returns an error rather than an empty list
// because the two are opposite facts: "this prefix is empty" is a ledger a person can act on,
// and "the listing was refused" must never be drawn as one — an operator who reads an empty
// bucket forgets bytes that are still being paid for.
//
// Prefix is the one operation here that reads object BYTES, and it reads only the first n of
// them. It exists because the facts a file's own header states — does this checkpoint bundle the
// VAE its family decodes with (engine_safetensors.go) — are otherwise readable only at the
// upstream URL, so a row REGISTERED from bytes already in the bucket had no way to learn them at
// all (ADR 0085 P3 dropped the routes that re-read them). It returns an error rather than a short
// buffer for the same reason List does: "the file starts like this" and "the read was refused"
// must not be the same answer, because only the first may be parsed as a verdict.
type engineStorageMetadataPort interface {
	Stat(context.Context, string) engineStorageObjectMetadata
	List(ctx context.Context, prefix string) ([]engineStorageObject, error)
	Prefix(ctx context.Context, key string, n int) ([]byte, error)
}

// engineStorageObject is one object as the bucket lists it. Deliberately three fields: a listing
// carries no checksum and no provenance, and everything else the ledger says about an object
// comes from the database beside it.
type engineStorageObject struct {
	Key          string
	Bytes        int64
	LastModified time.Time
}

type engineStorageObjectMetadata struct {
	State          string
	Bytes          int64
	ChecksumSHA256 string
	VersionID      string
}

type engineStorageCheck struct {
	State          string
	Bytes          int64
	ChecksumSHA256 string
	VersionID      string
	CheckedAt      string
}

type engineStorageCacheEntry struct {
	check   engineStorageCheck
	expires time.Time
}

// engineStorage is deployment-neutral: the route and reuse logic depend on the narrow port,
// while AWS wiring supplies an s3.Client only when the engine table declares its bucket.
type engineStorage struct {
	scope     string
	metadata  engineStorageMetadataPort
	headSlots chan struct{}

	mu    sync.Mutex
	cache map[string]engineStorageCacheEntry
	// deletes is the keys a MODE=delete task was started for and when, so the ledger can draw the
	// press before the bucket answers differently (ADR 0085 decision 7). Guarded by the same
	// mutex as the cache, because the two are always written together.
	deletes map[string]time.Time
}

func newEngineStorage(scope string, metadata engineStorageMetadataPort) *engineStorage {
	return &engineStorage{
		scope: strings.TrimSpace(scope), metadata: metadata,
		headSlots: make(chan struct{}, engineStorageConcurrency), cache: map[string]engineStorageCacheEntry{},
	}
}

func (s *engineStorage) configured() bool {
	return s != nil && s.metadata != nil && strings.TrimSpace(s.scope) != ""
}

func (s *engineStorage) invalidate(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
}

// engineStorageDeleteTTL is how long a key started deleting is still drawn as deleting.
//
// 🔴 A ceiling and not a lifetime. The deletion is an ECS task the CP does not watch — it writes
// no job row (the CP has no s3:DeleteObject, so the task is the principal that acts, ADR 0072
// decision 7) — so nothing here ever learns that it FAILED. Without the ceiling one refused task
// would draw an object as "deleting" until the Control Plane is replaced, and the operator could
// never press 消す again. The measured task takes seconds; fifteen minutes is generous.
const engineStorageDeleteTTL = 15 * time.Minute

// markDeleting records that a MODE=delete task was started for these keys, so the ledger can show
// the press doing something. In memory on purpose: it is progress, not truth — the bucket is the
// truth, and a Control Plane that restarts simply re-reads it.
func (s *engineStorage) markDeleting(keys ...string) {
	if s == nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		if s.deletes == nil {
			s.deletes = map[string]time.Time{}
		}
		s.deletes[key] = now
		delete(s.cache, key) // whatever HeadObject last said about it is about to stop being true
	}
}

// deleting is the keys whose deletion was started and has not aged out, with when it was pressed.
func (s *engineStorage) deleting() map[string]time.Time {
	if s == nil {
		return nil
	}
	cutoff := time.Now().Add(-engineStorageDeleteTTL)
	out := map[string]time.Time{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, at := range s.deletes {
		if at.Before(cutoff) {
			delete(s.deletes, key)
			continue
		}
		out[key] = at
	}
	return out
}

// checks uses a small fixed worker set. A panel can know hundreds of historical keys, but it
// must never turn one request into hundreds of simultaneous SDK calls on the shared CP task.
func (s *engineStorage) checks(ctx context.Context, keys []string) map[string]engineStorageCheck {
	out := make(map[string]engineStorageCheck, len(keys))
	if !s.configured() {
		for _, key := range keys {
			out[key] = engineStorageCheck{State: engineStorageUnknown}
		}
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, engineStorageListTimeout)
	defer cancel()
	now := time.Now()
	var pending []string
	s.mu.Lock()
	for _, key := range keys {
		if c, ok := s.cache[key]; ok && now.Before(c.expires) {
			out[key] = c.check
			continue
		}
		pending = append(pending, key)
	}
	s.mu.Unlock()

	jobs := make(chan string)
	var wg sync.WaitGroup
	workers := engineStorageConcurrency
	if len(pending) < workers {
		workers = len(pending)
	}
	var outMu sync.Mutex
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				check := s.head(ctx, key)
				outMu.Lock()
				out[key] = check
				outMu.Unlock()
				s.mu.Lock()
				s.cache[key] = engineStorageCacheEntry{check: check, expires: time.Now().Add(engineStorageCacheTTL)}
				s.mu.Unlock()
			}
		}()
	}
	for _, key := range pending {
		select {
		case jobs <- key:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			for _, unresolved := range pending {
				if _, ok := out[unresolved]; !ok {
					out[unresolved] = engineStorageCheck{State: engineStorageUnknown}
				}
			}
			return out
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

// list enumerates one prefix, and it is the only read here that does not start from a key the
// server already wrote down. The result is NOT put in the display cache: that cache exists to
// keep a panel refresh from re-heading a hundred known keys, while this answers "what is in the
// bucket" — a question whose stale answer is an object an operator would delete twice.
func (s *engineStorage) list(ctx context.Context, prefix string) ([]engineStorageObject, error) {
	if !s.configured() {
		return nil, errEngineStorageUnconfigured
	}
	ctx, cancel := context.WithTimeout(ctx, engineStorageListTimeout)
	defer cancel()
	objects, err := s.metadata.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, nil
}

var errEngineStorageUnconfigured = errors.New("this deployment declares no model bucket")

// prefix reads the first n bytes of one object, for the parsers that answer a question from a
// file's own header.
//
// Uncached on purpose, and it shares the HeadObject slots rather than getting its own: the answer
// is written into a catalogue row once and never refreshed from here, while the cost of the call
// is a transfer the shared CP task pays — so what matters is that two of these and two HeadObjects
// cannot become four simultaneous S3 calls, not that a second reader gets the bytes free.
func (s *engineStorage) prefix(ctx context.Context, key string, n int) ([]byte, error) {
	if !s.configured() {
		return nil, errEngineStorageUnconfigured
	}
	if n <= 0 {
		return nil, errors.New("a prefix read of no bytes answers nothing")
	}
	ctx, cancel := context.WithTimeout(ctx, engineStoragePrefixTimeout)
	defer cancel()
	select {
	case s.headSlots <- struct{}{}:
		defer func() { <-s.headSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.metadata.Prefix(ctx, key, n)
}

// verify deliberately bypasses the display cache. Reuse is a write to the catalogue and must
// prove the object exists now; a thirty-second-old present result is only a display hint.
func (s *engineStorage) verify(ctx context.Context, key string) engineStorageCheck {
	if !s.configured() {
		return engineStorageCheck{State: engineStorageUnknown}
	}
	check := s.head(ctx, key)
	s.mu.Lock()
	s.cache[key] = engineStorageCacheEntry{check: check, expires: time.Now().Add(engineStorageCacheTTL)}
	s.mu.Unlock()
	return check
}

func (s *engineStorage) head(ctx context.Context, key string) engineStorageCheck {
	c, cancel := context.WithTimeout(ctx, engineStorageHeadTimeout)
	defer cancel()
	select {
	case s.headSlots <- struct{}{}:
		defer func() { <-s.headSlots }()
	case <-c.Done():
		return engineStorageCheck{State: engineStorageUnknown}
	}
	meta := s.metadata.Stat(c, key)
	if c.Err() != nil {
		meta = engineStorageObjectMetadata{State: engineStorageUnknown}
	}
	switch meta.State {
	case engineStoragePresent, engineStorageMissing, engineStorageUnknown:
	default:
		meta = engineStorageObjectMetadata{State: engineStorageUnknown}
	}
	return engineStorageCheck{
		State: meta.State, Bytes: meta.Bytes, ChecksumSHA256: meta.ChecksumSHA256,
		VersionID: meta.VersionID, CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// engineKnownArtifact is the database half of one stored object. artifactIdentity is absent on
// legacy rows on purpose; no amount of matching a human source label can add the missing HF
// revision or Civitai filename after the fact.
type engineKnownArtifact struct {
	S3Key, Source, ArtifactIdentity string
	ModelIDs                        map[string]struct{}
	KVGeom                          engineKVGeometry
	VaeBundled                      string
	Reusable, Ambiguous             bool
	InFlight                        bool
}

func engineKnownArtifacts(models []store.EngineModel, jobs []store.EngineIngestJob) map[string]*engineKnownArtifact {
	out := map[string]*engineKnownArtifact{}
	add := func(key, modelID, source, identity string, reusable, identityRequired bool, kv engineKVGeometry, vae string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		a := out[key]
		if a == nil {
			a = &engineKnownArtifact{S3Key: key, ModelIDs: map[string]struct{}{}}
			out[key] = a
		}
		if modelID = strings.TrimSpace(modelID); modelID != "" {
			a.ModelIDs[modelID] = struct{}{}
		}
		if a.Source == "" {
			a.Source = strings.TrimSpace(source)
		}
		identity = strings.TrimSpace(identity)
		if identity == "" {
			if identityRequired {
				a.Ambiguous = true
				a.Reusable = false
			}
			return
		}
		if a.ArtifactIdentity != "" && a.ArtifactIdentity != identity {
			a.Ambiguous = true
		} else if a.ArtifactIdentity == "" {
			a.ArtifactIdentity = identity
			a.KVGeom = kv
			a.VaeBundled = vae
		}
		if reusable {
			a.Reusable = true
		}
	}
	for _, model := range models {
		for _, file := range model.Files {
			kv := engineKVGeometry{}
			if strings.TrimSpace(file.Flag) == "" {
				// 🔴 The ceiling travels with the rest. Reuse REPLACES the freshly read geometry
				// with this one (engineIngestResolve), so leaving it out wrote a 0 over the row's
				// context_ceiling — and a row with no ceiling is offered no re-fit at all.
				kv = engineKVGeometry{Layers: model.KVLayers, HeadsKV: model.KVHeadsKV,
					KeyLen: model.KVKeyLen, ValLen: model.KVValueLen,
					NextN: model.KVNextN, FullAttnInterval: model.KVFullAttnInterval,
					Ceiling: model.ContextCeiling}
			}
			add(file.S3Key, model.ID, file.Source, file.ArtifactIdentity, true, true, kv, file.VaeBundled)
		}
	}
	// Storage queries return newest jobs first. Only the newest successful upload for one key
	// describes its current server-recorded bytes; older jobs are history, not conflicting
	// provenance. A pending upload blocks reuse because it may replace that key after the HEAD.
	latestDone := map[string]bool{}
	for _, job := range jobs {
		key := strings.TrimSpace(job.S3Key)
		if job.State == store.EngineIngestPending || job.State == store.EngineIngestRunning {
			add(job.S3Key, job.ModelID, job.Source, "", false, false, engineKVGeometry{}, "")
			if a := out[key]; a != nil {
				a.InFlight = true
			}
			continue
		}
		if job.State != store.EngineIngestDone {
			add(job.S3Key, job.ModelID, job.Source, "", false, true, engineKVGeometry{}, "")
			continue
		}
		if latestDone[key] {
			add(job.S3Key, job.ModelID, job.Source, "", false, false, engineKVGeometry{}, "")
			continue
		}
		latestDone[key] = true
		var req engineIngestRequest
		if strings.TrimSpace(job.Spec) == "" || json.Unmarshal([]byte(job.Spec), &req) != nil {
			add(job.S3Key, job.ModelID, job.Source, "", false, true, engineKVGeometry{}, "")
			continue
		}
		add(job.S3Key, job.ModelID, job.Source, req.Resolved.ArtifactIdentity, job.State == store.EngineIngestDone, true,
			req.KVGeom, req.VaeBundled)
	}
	return out
}

func (a engineAdminAPI) engineStorageRows(ctx context.Context, g engineIngestGrant, role string) ([]store.EngineModel, []store.EngineIngestJob, error) {
	if a.mgr == nil || a.mgr.store == nil {
		return nil, nil, nil
	}
	models, err := a.mgr.store.ListEngineModels(ctx, role)
	if err != nil {
		return nil, nil, err
	}
	var jobs []store.EngineIngestJob
	if g.super {
		jobs, err = a.mgr.store.ListEngineIngestJobsForStorage(ctx, role)
	} else {
		jobs, err = a.mgr.store.ListEngineIngestJobsForStorageByTenant(ctx, role, g.tenantID)
	}
	return models, jobs, err
}
