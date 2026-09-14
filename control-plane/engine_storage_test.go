package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type fakeEngineStorageHead struct {
	mu         sync.Mutex
	states     map[string]string
	bytes      map[string]int64
	calls      map[string]int
	active     int
	maxActive  int
	blockDelay time.Duration
}

func (f *fakeEngineStorageHead) Stat(ctx context.Context, key string) engineStorageObjectMetadata {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[key]++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	if f.blockDelay > 0 {
		timer := time.NewTimer(f.blockDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	f.active--
	state, size := f.states[key], f.bytes[key]
	f.mu.Unlock()
	switch state {
	case engineStoragePresent:
		return engineStorageObjectMetadata{State: engineStoragePresent, Bytes: size}
	case engineStorageMissing:
		return engineStorageObjectMetadata{State: engineStorageMissing}
	default:
		return engineStorageObjectMetadata{State: engineStorageUnknown}
	}
}

func TestEngineStorageListsOnlyKnownRoleKeysAndCachesHeads(t *testing.T) {
	st := ingestStore(t)
	ctx := t.Context()
	id := engineHFArtifactIdentity("org/repo", "model.safetensors", "commit-a", strings.Repeat("a", 64))
	for _, m := range []store.EngineModel{
		{Role: "image", ID: "a", Files: []store.EngineModelFile{{S3Key: "image/models/a.safetensors", Source: "hf:org/repo/model.safetensors", ArtifactIdentity: id}}},
		{Role: "image", ID: "b", Files: []store.EngineModelFile{{S3Key: "image/models/a.safetensors", ArtifactIdentity: id}}},
		{Role: "image", ID: "legacy", Files: []store.EngineModelFile{{S3Key: "image/models/legacy.safetensors", Source: "hf:org/repo/model.safetensors"}}},
		{Role: "image", ID: "cross-prefix", Files: []store.EngineModelFile{{S3Key: "llm/cross-role.gguf", Source: "hf:org/cross/file.gguf", ArtifactIdentity: "cross"}}},
		{Role: "llm", ID: "other-role", Files: []store.EngineModelFile{{S3Key: "llm/secret.gguf", ArtifactIdentity: "other"}}},
	} {
		if err := st.PutEngineModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	jobReq := engineIngestRequest{Resolved: engineResolved{ArtifactIdentity: "civitai:7/file.safetensors#sha256:" + strings.Repeat("b", 64)}}
	spec, _ := json.Marshal(jobReq)
	if err := st.PutEngineIngestJob(ctx, store.EngineIngestJob{
		ID: "j1", Role: "image", ModelID: "orphan", S3Key: "image/models/orphan.safetensors",
		Source: "civitai:7", State: store.EngineIngestDone, Spec: string(spec), TenantID: "t-acme",
	}); err != nil {
		t.Fatal(err)
	}
	head := &fakeEngineStorageHead{
		states: map[string]string{
			"image/models/a.safetensors":      engineStoragePresent,
			"image/models/legacy.safetensors": engineStorageMissing,
			"image/models/orphan.safetensors": engineStorageUnknown,
		},
		bytes: map[string]int64{"image/models/a.safetensors": 1234},
	}
	reg, e := newAdminTestRegistry(t, &engineTestECS{}, st)
	e.catalog = newEngineCatalog(st, "image")
	reg.ing = &engineIngester{storage: newEngineStorage("models", head), store: st, models: st}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}

	request := func() engineStorageResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/admin/engines/image/storage", nil)
		r.SetPathValue("key", "image")
		a.getStorage(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
		if rec.Code != http.StatusOK {
			t.Fatalf("storage = %d (%s)", rec.Code, rec.Body.String())
		}
		var out engineStorageResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := request()
	request()
	if len(out.Files) != 4 {
		t.Fatalf("files = %+v", out.Files)
	}
	if out.Files[0].S3Key != "image/models/a.safetensors" || out.Files[0].State != engineStoragePresent ||
		out.Files[0].Bytes != 1234 || out.Files[0].Source != "hf:org/repo/model.safetensors" ||
		out.Files[0].ArtifactIdentity != id || !out.Files[0].Reusable || strings.Join(out.Files[0].ModelIDs, ",") != "a,b" {
		t.Errorf("present row = %+v", out.Files[0])
	}
	if out.Files[1].State != engineStorageMissing || out.Files[1].Source == "" ||
		out.Files[1].ArtifactIdentity != "" || out.Files[1].Reusable {
		t.Errorf("legacy missing row = %+v", out.Files[1])
	}
	if out.Files[2].State != engineStorageUnknown || out.Files[2].Source != "civitai:7" ||
		out.Files[2].ArtifactIdentity == "" || out.Files[2].Reusable {
		t.Errorf("job-only unknown row = %+v", out.Files[2])
	}
	if out.Files[3].S3Key != "llm/cross-role.gguf" || out.Files[3].State != engineStorageUnknown || out.Files[3].Reusable {
		t.Errorf("cross-prefix row = %+v", out.Files[3])
	}
	if out.CheckedAt == "" {
		t.Error("an attempted storage check has no checked_at")
	}
	head.mu.Lock()
	defer head.mu.Unlock()
	if len(head.calls) != 3 || head.calls["llm/secret.gguf"] != 0 || head.calls["llm/cross-role.gguf"] != 0 {
		t.Errorf("HeadObject calls escaped the known image keys: %v", head.calls)
	}
	for key, calls := range head.calls {
		if calls != 1 {
			t.Errorf("%s was checked %d times inside the cache TTL", key, calls)
		}
	}
}

func TestEngineStorageNarrowsJobKeysToTheGrantingTenant(t *testing.T) {
	st := ingestStore(t)
	for _, job := range []store.EngineIngestJob{
		{ID: "mine", Role: "image", ModelID: "mine", S3Key: "image/models/mine.safetensors", State: store.EngineIngestDone, TenantID: "t-acme"},
		{ID: "theirs", Role: "image", ModelID: "theirs", S3Key: "image/models/theirs.safetensors", State: store.EngineIngestDone, TenantID: "t-other"},
	} {
		req := engineIngestRequest{Resolved: engineResolved{ArtifactIdentity: "artifact:" + job.ID}}
		spec, _ := json.Marshal(req)
		job.Spec = string(spec)
		if err := st.PutEngineIngestJob(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	head := &fakeEngineStorageHead{states: map[string]string{"image/models/mine.safetensors": engineStoragePresent}}
	reg, e := newAdminTestRegistry(t, &engineTestECS{}, st)
	e.catalog = newEngineCatalog(st, "image")
	reg.ing = &engineIngester{storage: newEngineStorage("models", head), store: st, models: st}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/engines/image/storage", nil)
	r.SetPathValue("key", "image")
	a.getStorage(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, tenantID: "t-acme"})
	var out engineStorageResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("storage = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(out.Files) != 1 || out.Files[0].S3Key != "image/models/mine.safetensors" {
		t.Fatalf("tenant storage = %+v", out.Files)
	}
	head.mu.Lock()
	defer head.mu.Unlock()
	if head.calls["image/models/theirs.safetensors"] != 0 {
		t.Errorf("another tenant's job key reached S3: %v", head.calls)
	}
}

func TestEngineKnownArtifactsUsesOnlyTheNewestSuccessfulUpload(t *testing.T) {
	key := "image/models/a.safetensors"
	current := "hf:org/repo@new/a.safetensors#sha256:new"
	old := "hf:org/repo@old/a.safetensors#sha256:old"
	spec := func(identity string) string {
		b, _ := json.Marshal(engineIngestRequest{Resolved: engineResolved{ArtifactIdentity: identity}})
		return string(b)
	}
	known := engineKnownArtifacts(
		[]store.EngineModel{{Role: "image", ID: "a", Files: []store.EngineModelFile{{S3Key: key, ArtifactIdentity: current}}}},
		[]store.EngineIngestJob{
			{ID: "new", S3Key: key, State: store.EngineIngestDone, Spec: spec(current)},
			{ID: "old", S3Key: key, State: store.EngineIngestDone, Spec: spec(old)},
		},
	)[key]
	if known == nil || known.Ambiguous || known.ArtifactIdentity != current || !known.Reusable {
		t.Fatalf("current artifact provenance = %+v", known)
	}

	withActive := engineKnownArtifacts(
		[]store.EngineModel{{Role: "image", ID: "a", Files: []store.EngineModelFile{{S3Key: key, ArtifactIdentity: current}}}},
		[]store.EngineIngestJob{
			{ID: "running", S3Key: key, State: store.EngineIngestRunning},
			{ID: "new", S3Key: key, State: store.EngineIngestDone, Spec: spec(current)},
		},
	)[key]
	if withActive == nil || !withActive.InFlight {
		t.Fatalf("active upload did not fence reuse: %+v", withActive)
	}
}

func TestEngineKnownArtifactsRefusesMixedLegacyProvenance(t *testing.T) {
	key := "image/models/a.safetensors"
	identity := "hf:org/repo@commit/a.safetensors#sha256:new"
	known := engineKnownArtifacts([]store.EngineModel{
		{Role: "image", ID: "current", Files: []store.EngineModelFile{{S3Key: key, ArtifactIdentity: identity}}},
		{Role: "image", ID: "legacy", Files: []store.EngineModelFile{{S3Key: key, Source: "hf:org/repo/a.safetensors"}}},
	}, nil)[key]
	if known == nil || !known.Ambiguous || known.Reusable {
		t.Fatalf("mixed legacy provenance remained reusable: %+v", known)
	}
}

func TestEngineStorageRefusesABorrowedRole(t *testing.T) {
	st := ingestStore(t)
	reg, e := newAdminTestRegistry(t, &engineTestECS{}, st)
	e.def.Lifecycle, e.def.URL = engineLifecycleRemote, "https://far.invalid"
	head := &fakeEngineStorageHead{}
	reg.ing = &engineIngester{storage: newEngineStorage("models", head), store: st, models: st}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/engines/image/storage", nil)
	r.SetPathValue("key", "image")
	a.getStorage(rec, r, engineIngestGrant{super: true})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errCodeEngineNotOurs) {
		t.Fatalf("borrowed storage = %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestEngineStorageBoundsHeadConcurrency(t *testing.T) {
	head := &fakeEngineStorageHead{states: map[string]string{}, bytes: map[string]int64{}, blockDelay: 10 * time.Millisecond}
	var keys []string
	for i := range 12 {
		key := "image/models/" + string(rune('a'+i))
		keys = append(keys, key)
		head.states[key] = engineStoragePresent
	}
	got := newEngineStorage("models", head).checks(t.Context(), keys)
	if len(got) != len(keys) {
		t.Fatalf("checks = %d, want %d", len(got), len(keys))
	}
	head.mu.Lock()
	defer head.mu.Unlock()
	if head.maxActive > engineStorageConcurrency || head.maxActive < 2 {
		t.Fatalf("maximum HeadObject concurrency = %d, want 2..%d", head.maxActive, engineStorageConcurrency)
	}
}

func TestEngineStorageBoundsHeadConcurrencyAcrossRequests(t *testing.T) {
	head := &fakeEngineStorageHead{states: map[string]string{}, blockDelay: 20 * time.Millisecond}
	storage := newEngineStorage("models", head)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for request := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var keys []string
			for i := range 8 {
				key := "image/models/" + string(rune('a'+request*8+i))
				keys = append(keys, key)
				head.mu.Lock()
				head.states[key] = engineStoragePresent
				head.mu.Unlock()
			}
			storage.checks(t.Context(), keys)
		}()
	}
	close(start)
	wg.Wait()
	head.mu.Lock()
	defer head.mu.Unlock()
	if head.maxActive > engineStorageConcurrency || head.maxActive < 2 {
		t.Fatalf("maximum cross-request metadata concurrency = %d, want 2..%d", head.maxActive, engineStorageConcurrency)
	}
}

func TestEngineStorageStopsInventoryWhenTheRequestEnds(t *testing.T) {
	head := &fakeEngineStorageHead{states: map[string]string{}, blockDelay: time.Second}
	var keys []string
	for i := range 20 {
		key := "image/models/" + string(rune('a'+i))
		keys = append(keys, key)
		head.states[key] = engineStoragePresent
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer cancel()
	started := time.Now()
	got := newEngineStorage("models", head).checks(ctx, keys)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancelled inventory returned after %s", elapsed)
	}
	for _, key := range keys {
		if got[key].State != engineStorageUnknown {
			t.Errorf("%s after cancellation = %+v", key, got[key])
		}
	}
}

func TestEngineStorageWithoutConfigurationIsUnknown(t *testing.T) {
	got := (*engineStorage)(nil).checks(t.Context(), []string{"image/models/a.safetensors"})
	if got["image/models/a.safetensors"].State != engineStorageUnknown ||
		got["image/models/a.safetensors"].CheckedAt != "" {
		t.Fatalf("unconfigured storage check = %+v", got)
	}
}

func TestEngineTableReloadAddsStorageToAnExistingIngester(t *testing.T) {
	ing := &engineIngester{}
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}, ing: ing}
	head := &fakeEngineStorageHead{}
	reg.startIngest = func(def engineIngestDef) bool {
		return ing.setStorage(newEngineStorage(def.Bucket, head))
	}
	raw := `{"engines":[],"ingest":{"taskDef":"ingest:1","subnets":["subnet-a"],"bucket":"models"}}`
	reloader := newEngineTableReloader(&fakePendingSSM{value: raw}, "/af-ws/engines", reg, "")
	if !reloader.tick(t.Context()) {
		t.Fatal("adding the bucket to an existing ingest runner reported no live change")
	}
	if got := ing.storageChecker(); got == nil || got.scope != "models" || got.metadata == nil {
		t.Fatalf("storage checker = %+v", got)
	}
}

func TestEngineIngestReusesOnlyAnExistingExactArtifact(t *testing.T) {
	srv := hfStub(t)
	old := engineIngestBase
	engineIngestBase = srv.URL
	t.Cleanup(func() { engineIngestBase = old })
	resolved, aerr := engineResolveHF(t.Context(), engineIngestHF{
		Repo: "black-forest-labs/FLUX.1-dev", File: "flux1-dev.safetensors",
	})
	if aerr != nil {
		t.Fatal(aerr.message)
	}
	st := ingestStore(t)
	key := "image/loras/flux1-dev.safetensors"
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "saved", Kind: "lora",
		Files: []store.EngineModelFile{{S3Key: key, Source: resolved.Source, ArtifactIdentity: resolved.ArtifactIdentity}},
	}); err != nil {
		t.Fatal(err)
	}
	head := &fakeEngineStorageHead{
		states: map[string]string{key: engineStoragePresent}, bytes: map[string]int64{key: resolved.Bytes},
	}
	ecsAPI := &fakeIngestECS{}
	reg, e := newAdminTestRegistry(t, &engineTestECS{}, st)
	e.catalog = newEngineCatalog(st, "image")
	reg.ing = &engineIngester{
		def: engineIngestDef{TaskDef: "ingest", Subnets: []string{"subnet-1"}},
		ecs: ecsAPI, store: st, models: st, storage: newEngineStorage("models", head),
	}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, reg, st}
	body := `{"id":"reused","kind":"lora","reuse_s3_key":"` + key + `","license_accepted":true,
		"source":{"hf":{"repo":"black-forest-labs/FLUX.1-dev","file":"flux1-dev.safetensors"}}}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/engines/image/ingest", strings.NewReader(body))
	r.SetPathValue("key", "image")
	a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}, super: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("reuse = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(ecsAPI.run) != 0 {
		t.Fatalf("reuse started %d ECS tasks", len(ecsAPI.run))
	}
	rows, err := st.ListEngineModels(t.Context(), "image")
	if err != nil || len(rows) != 2 {
		t.Fatalf("models = %+v (%v)", rows, err)
	}
	var reused store.EngineModel
	for _, row := range rows {
		if row.ID == "reused" {
			reused = row
		}
	}
	if reused.ID == "" || reused.Enabled || reused.Files[0].S3Key != key ||
		reused.Files[0].ArtifactIdentity != resolved.ArtifactIdentity {
		t.Errorf("reused model = %+v", reused)
	}
	jobs, _ := st.ListEngineIngestJobs(t.Context(), "image", 10)
	if len(jobs) != 1 || jobs[0].State != store.EngineIngestDone || jobs[0].TaskArn != "" {
		t.Errorf("reuse job = %+v", jobs)
	}
}

func TestEngineReuseRefusesLegacyIdentityBeforeHeadObject(t *testing.T) {
	st := ingestStore(t)
	key := "image/models/legacy.safetensors"
	if err := st.PutEngineModel(t.Context(), store.EngineModel{
		Role: "image", ID: "legacy", Files: []store.EngineModelFile{{S3Key: key, Source: "hf:org/repo/model.safetensors"}},
	}); err != nil {
		t.Fatal(err)
	}
	head := &fakeEngineStorageHead{states: map[string]string{key: engineStoragePresent}}
	a := engineAdminAPI{memberAuth{&manager{store: st}}, &engineRegistry{}, st}
	ing := &engineIngester{storage: newEngineStorage("models", head)}
	_, _, aerr := a.verifyEngineReuse(t.Context(), engineIngestGrant{super: true}, "image", key,
		engineResolved{ArtifactIdentity: "hf:org/repo@commit/model.safetensors#sha256:" + strings.Repeat("a", 64)}, ing)
	if aerr == nil || !strings.Contains(aerr.message, "legacy source text") {
		t.Fatalf("legacy reuse error = %#v", aerr)
	}
	head.mu.Lock()
	defer head.mu.Unlock()
	if len(head.calls) != 0 {
		t.Errorf("legacy identity still reached HeadObject: %v", head.calls)
	}
}
