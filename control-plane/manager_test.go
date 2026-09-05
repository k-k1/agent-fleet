package main

import (
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"testing"
)

// rootedDataDir must re-base a workspace's on-disk root onto the CURRENT dataRoot
// so a restore/move to a different DATA_DIR (or a changed WS_DATA) keeps mounting
// the right home instead of silently creating an empty one. See P3-10 stage 3 /
// docs/log/p3-10-packaging.md §20.3(B).
func TestRootedDataDir(t *testing.T) {
	m := &manager{dataRoot: "/srv/agent-fleet/data", defaultTenantID: "T-default"}

	cases := []struct {
		name string
		ws   store.Workspace
		want string
	}{
		{
			name: "default tenant flat path re-rooted",
			ws:   store.Workspace{TenantID: "T-default", DataDir: "/old/root/dev-example-com"},
			want: "/srv/agent-fleet/data/dev-example-com",
		},
		{
			name: "non-default tenant nested path re-rooted (slug/key kept)",
			ws:   store.Workspace{TenantID: "T-acme", DataDir: "/old/root/acme-team/dev-example-com"},
			want: "/srv/agent-fleet/data/acme-team/dev-example-com",
		},
		{
			name: "idempotent when already current (default)",
			ws:   store.Workspace{TenantID: "T-default", DataDir: "/srv/agent-fleet/data/alice"},
			want: "/srv/agent-fleet/data/alice",
		},
		{
			name: "key containing dashes is not mis-split (default = last segment only)",
			ws:   store.Workspace{TenantID: "T-default", DataDir: "/old/root/a-b-c-d"},
			want: "/srv/agent-fleet/data/a-b-c-d",
		},
		{
			name: "empty data dir passes through",
			ws:   store.Workspace{TenantID: "T-default", DataDir: ""},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.rootedDataDir(tc.ws); got != tc.want {
				t.Fatalf("rootedDataDir(%q, tenant=%s) = %q, want %q", tc.ws.DataDir, tc.ws.TenantID, got, tc.want)
			}
		})
	}
}
