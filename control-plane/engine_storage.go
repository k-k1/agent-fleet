package main

// engine_storage.go — the read-only S3 view behind the model catalogue.
//
// The bucket is not the catalogue: S3 says whether bytes exist, while the database says which
// keys this deployment knows and which models use them. This file never lists a bucket and never
// accepts a key from the storage route. It applies HeadObject only to catalogue and ingest-job
// keys already visible to the caller, keeping a granted tenant administrator inside both their
// engine role and their own job history.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
type engineStorageMetadataPort interface {
	Stat(context.Context, string) engineStorageObjectMetadata
	List(ctx context.Context, prefix string) ([]engineStorageObject, error)
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
				kv = engineKVGeometry{Layers: model.KVLayers, HeadsKV: model.KVHeadsKV,
					KeyLen: model.KVKeyLen, ValLen: model.KVValueLen}
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

type engineStorageFileRow struct {
	S3Key            string   `json:"s3_key"`
	Source           string   `json:"source,omitempty"`
	ArtifactIdentity string   `json:"artifact_identity,omitempty"`
	Reusable         bool     `json:"reusable"`
	State            string   `json:"state"`
	Bytes            int64    `json:"bytes,omitempty"`
	Checked          string   `json:"checked_at,omitempty"`
	ModelIDs         []string `json:"model_ids"`
}

type engineStorageResponse struct {
	Files     []engineStorageFileRow `json:"files"`
	CheckedAt string                 `json:"checked_at,omitempty"`
}

// getStorage returns only keys the server already knows. It has no query/body key parameter,
// so the route cannot be turned into an S3 existence oracle for another role or prefix.
func (a engineAdminAPI) getStorage(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return
	}
	if a.refuseBorrowedWrite(w, e, "checking model storage") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), engineStorageListTimeout)
	defer cancel()
	models, jobs, err := a.engineStorageRows(ctx, g, key)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	known := engineKnownArtifacts(models, jobs)
	keys := make([]string, 0, len(known))
	checkKeys := make([]string, 0, len(known))
	for s3key := range known {
		keys = append(keys, s3key)
		if strings.HasPrefix(s3key, key+"/") {
			checkKeys = append(checkKeys, s3key)
		}
	}
	sort.Strings(keys)
	var storage *engineStorage
	if ing := a.reg.ingester(); ing != nil {
		storage = ing.storageChecker()
	}
	checks := storage.checks(ctx, checkKeys)
	rows := make([]engineStorageFileRow, 0, len(keys))
	topChecked := ""
	for _, s3key := range keys {
		a := known[s3key]
		check := checks[s3key]
		ids := make([]string, 0, len(a.ModelIDs))
		for id := range a.ModelIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		row := engineStorageFileRow{
			S3Key: s3key, Source: a.Source, State: check.State, Bytes: check.Bytes,
			Checked: check.CheckedAt, ModelIDs: ids,
		}
		if !a.Ambiguous {
			row.ArtifactIdentity = a.ArtifactIdentity
			row.Reusable = check.State == engineStoragePresent && a.Reusable && !a.InFlight && a.ArtifactIdentity != ""
		}
		if row.State == "" {
			row.State = engineStorageUnknown
		}
		if row.Checked > topChecked {
			topChecked = row.Checked
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, engineStorageResponse{Files: rows, CheckedAt: topChecked})
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

// verifyEngineReuse joins the two independent proofs reuse needs. HeadObject proves that some
// bytes occupy the known key now; the persisted artifact identity proves which exact upstream
// file and revision those bytes were verified against before upload. Neither proof substitutes
// for the other.
func (a engineAdminAPI) verifyEngineReuse(ctx context.Context, g engineIngestGrant, role, key string,
	resolved engineResolved, ing *engineIngester) (*engineKnownArtifact, engineStorageCheck, *apiError) {
	models, jobs, err := a.engineStorageRows(ctx, g, role)
	if err != nil {
		return nil, engineStorageCheck{}, internalErr(err)
	}
	known := engineKnownArtifacts(models, jobs)[key]
	if known == nil {
		return nil, engineStorageCheck{}, &apiError{http.StatusBadRequest, errCodeEngineBadBody,
			"reuse_s3_key is not present in this engine's catalogue or visible job history"}
	}
	if known.Ambiguous {
		if known.ArtifactIdentity == "" {
			return nil, engineStorageCheck{}, &apiError{http.StatusConflict, errCodeEngineBadBody,
				"reuse_s3_key has no immutable stored artifact identity; legacy source text is not enough to reuse it"}
		}
		return nil, engineStorageCheck{}, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"reuse_s3_key has conflicting stored artifact identities and cannot be reused safely"}
	}
	if known.InFlight {
		return nil, engineStorageCheck{}, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"reuse_s3_key has an ingest still in flight and cannot be reused safely"}
	}
	if !known.Reusable || known.ArtifactIdentity == "" {
		return nil, engineStorageCheck{}, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"reuse_s3_key has no immutable stored artifact identity; legacy source text is not enough to reuse it"}
	}
	if strings.TrimSpace(resolved.ArtifactIdentity) == "" || resolved.ArtifactIdentity != known.ArtifactIdentity {
		return nil, engineStorageCheck{}, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"reuse_s3_key belongs to a different source file, revision, or sha256"}
	}
	if ing == nil || ing.storageChecker() == nil {
		return nil, engineStorageCheck{}, &apiError{http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"the deployment has no model storage checker, so it cannot prove reuse_s3_key exists"}
	}
	check := ing.storageChecker().verify(ctx, key)
	switch check.State {
	case engineStoragePresent:
		return known, check, nil
	case engineStorageMissing:
		return nil, check, &apiError{http.StatusConflict, errCodeEngineBadBody,
			"reuse_s3_key is recorded but the S3 object is missing"}
	default:
		return nil, check, &apiError{http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"the deployment could not verify reuse_s3_key in S3; access denied and missing configuration are not treated as absence"}
	}
}
