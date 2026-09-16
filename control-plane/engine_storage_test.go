package main

import (
	"context"
	"encoding/json"
	"errors"
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
	// listErr makes the bucket refuse to be listed, which is a different answer from an empty
	// bucket and the ledger has to keep them apart.
	listErr error
	// listCalls counts the ledger's reads, so a test can prove a plan answered from the listing
	// rather than from a fan-out of HeadObject.
	listCalls int
	// head is the first bytes of an object, for the header reads (engine_safetensors.go). A key
	// with no entry reads as a refusal rather than as an empty file: "the bucket would not say"
	// and "the file starts with nothing" are the two answers those parsers keep apart.
	head map[string][]byte
	// prefixErr overrides that per key, so a test can make a PRESENT object unreadable — which is
	// what an AccessDenied or a timeout looks like from here.
	prefixErr map[string]error
	// prefixCalls is the window each read asked for, in order, per key: the two-step ladder is
	// the thing a test about a header past the ceiling has to observe.
	prefixCalls map[string][]int
}

func (f *fakeEngineStorageHead) Prefix(_ context.Context, key string, n int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prefixCalls == nil {
		f.prefixCalls = map[string][]int{}
	}
	f.prefixCalls[key] = append(f.prefixCalls[key], n)
	if err := f.prefixErr[key]; err != nil {
		return nil, err
	}
	buf, ok := f.head[key]
	if !ok {
		return nil, errors.New("no such object")
	}
	if len(buf) > n {
		buf = buf[:n]
	}
	return append([]byte(nil), buf...), nil
}

// List answers from the same fixture Stat does: every key the test declared `present` is in the
// bucket. That keeps one map as the fixture's single statement about what exists.
func (f *fakeEngineStorageHead) List(_ context.Context, prefix string) ([]engineStorageObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []engineStorageObject
	for key, state := range f.states {
		if state != engineStoragePresent || !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, engineStorageObject{Key: key, Bytes: f.bytes[key]})
	}
	return out, nil
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
