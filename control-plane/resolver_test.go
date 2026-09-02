package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// docs/log/23 P2-W2: manager.mu を DB I/O 跨ぎで保持する直列化をやめた際の要件 —
// 同一メンバーシップの並行「初回」resolve が workspace を二重作成しないこと
// （per-membership build ロックが守る）、かつ全 goroutine が同じ workspace に
// 解決すること。
func TestBuildResolvedSingleFlight(t *testing.T) {
	// Swapped on the adapters' package, not through an alias here: a `var x = pkg.Y`
	// on this side is a COPY, and reassigning it would leave the adapter calling the
	// real docker binary while the test believed it was stubbed.
	prev := runtime.DockerInspectOut
	runtime.DockerInspectOut = func(args ...string) ([]byte, error) { return nil, errors.New("no docker in tests") }
	t.Cleanup(func() { runtime.DockerInspectOut = prev })

	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	mgr := &manager{
		rts:             map[string]cachedRT{},
		store:           st,
		dataRoot:        t.TempDir(),
		authMode:        "dev",
		devUser:         "race",
		provisionMode:   "auto",
		defaultTenantID: dt.ID,
		conns:           newConnRegistry(),
	}
	if mgr.rtFactory, err = newRuntimeFactory("local", mgr); err != nil {
		t.Fatalf("factory: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	wsIDs := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, aerr := mgr.resolveFull(ctx, "race", "", "")
			if aerr != nil {
				t.Errorf("resolveFull: %v", aerr.message)
				return
			}
			wsIDs[i] = res.ws.ID
		}(i)
	}
	wg.Wait()

	wss, err := st.ListWorkspaces(ctx, dt.ID)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(wss) != 1 {
		t.Fatalf("workspace records: want 1 got %d (duplicate create under concurrency)", len(wss))
	}
	for i, id := range wsIDs {
		if id != wss[0].ID {
			t.Fatalf("goroutine %d resolved to %q, want %q", i, id, wss[0].ID)
		}
	}
}
