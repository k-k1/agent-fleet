package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

var updateKindCatalog = flag.Bool("update-kind-catalog", false,
	"rewrite the CP's checked-in kind catalog from the Agent's registry")

// kindCatalogSnapshotPath is the copy the CP embeds (control-plane/internal/agentkinds).
// go:embed cannot reach outside the CP module, so the file lives there and this test owns it.
const kindCatalogSnapshotPath = "../../control-plane/internal/agentkinds/catalog.json"

func kindCatalogSnapshotBytes(t *testing.T) []byte {
	t.Helper()
	b, err := json.MarshalIndent(buildKindCatalog(false), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// TestKindCatalogSnapshot keeps the CP's embedded catalog equal to the registry. A kind added,
// or a Caps field changed, without regenerating is the drift #1075 shipped (lcpp/muse missing
// from the CP's MCP list).
//
// Update: cd workspace/agent && go test -run TestKindCatalogSnapshot -update-kind-catalog .
func TestKindCatalogSnapshot(t *testing.T) {
	want := kindCatalogSnapshotBytes(t)
	if *updateKindCatalog {
		if err := os.WriteFile(kindCatalogSnapshotPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(kindCatalogSnapshotPath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with -update-kind-catalog)", kindCatalogSnapshotPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale against the kind registry; regenerate:\n"+
			"  cd workspace/agent && go test -run TestKindCatalogSnapshot -update-kind-catalog .\nwant:\n%s",
			kindCatalogSnapshotPath, want)
	}
}

func TestKindCatalogCoversRegistry(t *testing.T) {
	cat := buildKindCatalog(true)
	var kinds []string
	for _, e := range cat.Kinds {
		kinds = append(kinds, e.Kind)
		for field, v := range map[string]string{"mcpClient": e.MCPClient, "instructions": e.Instructions} {
			if v != "yes" && v != "no" && v != "unverified" {
				t.Errorf("%s.%s = %q, want yes / no / unverified", e.Kind, field, v)
			}
		}
		if e.Instructions == "yes" && e.InstructionDelivery == "" {
			t.Errorf("%s: instructions supported but instrDeliveryOf has no answer", e.Kind)
		}
		if e.Instructions == "no" && e.InstructionsReason == "" {
			t.Errorf("%s: instructions unsupported with no reason", e.Kind)
		}
		// Every kind that runs an agent CLI must say whether it takes the member's
		// instructions; only shell and ssm, which run none, may stay unverified.
		if e.MCPClient == "yes" && e.Instructions == "unverified" {
			t.Errorf("%s runs an MCP client but is on neither instrSupportedKinds nor instrUnsupported", e.Kind)
		}
		if e.ManagedOnly && !e.ManagedDefault {
			t.Errorf("%s: managedOnly without managedDefault", e.Kind)
		}
	}
	if !slices.Equal(kinds, sessionx.RegisteredKinds()) {
		t.Errorf("catalog kinds = %v, want the registry's %v", kinds, sessionx.RegisteredKinds())
	}
	// The instruction lists name kinds by string; a typo there is a kind nobody serves.
	for _, k := range instrSupportedKinds {
		if !slices.Contains(kinds, k) {
			t.Errorf("instrSupportedKinds has %q, which is not a registered kind", k)
		}
	}
	for _, u := range instrUnsupported {
		if !slices.Contains(kinds, u.kind) {
			t.Errorf("instrUnsupported has %q, which is not a registered kind", u.kind)
		}
	}
}

func TestHandleAgentKinds(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAgentKinds(rec, httptest.NewRequest(http.MethodGet, "/agents/kinds", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got kindCatalogWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	by := map[string]kindEntryWire{}
	for _, e := range got.Kinds {
		by[e.Kind] = e
	}
	if c := by["claude"]; c.MCPClient != "yes" || !c.Caps.CanFork || c.InstructionDelivery != deliveryFile {
		t.Errorf("claude = %+v", c)
	}
	if s := by["shell"]; s.MCPClient != "no" || s.Instructions != "unverified" || s.ManagedDefault {
		t.Errorf("shell = %+v", s)
	}
	if l := by["lcpp"]; !l.ManagedOnly || !l.ManagedDefault || l.InstructionDelivery != deliveryPrompt {
		t.Errorf("lcpp = %+v", l)
	}
	if c := by["cursor"]; c.Instructions != "no" || c.InstructionsReason != "no_user_scope" || !c.ManagedDefault {
		t.Errorf("cursor = %+v", c)
	}
}

// The CP derives create_session's and the scheduler's managed default from the embedded snapshot
// (agentkinds.ManagedDefaultKinds), so for every registered kind the snapshot must say what the
// registry says: a kind missing from it, one it has extra, or a managedDefault that differs
// would send the CP callers a different driver than the in-container create_session picks.
// TestKindCatalogSnapshot already compares bytes; this names the failure for this one field.
func TestCPManagedDefaultSnapshotMatchesRegistry(t *testing.T) {
	b, err := os.ReadFile(kindCatalogSnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Kinds []struct {
			Kind           string `json:"kind"`
			ManagedDefault bool   `json:"managedDefault"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	inSnap := map[string]bool{}
	for _, e := range snap.Kinds {
		inSnap[e.Kind] = true
		if !slices.Contains(sessionx.RegisteredKinds(), e.Kind) {
			t.Errorf("snapshot lists %q, which is not a registered kind", e.Kind)
			continue
		}
		if e.ManagedDefault != sessionx.ManagedDefaultKind(e.Kind) {
			t.Errorf("%s: snapshot managedDefault=%v, registry says %v; regenerate the snapshot",
				e.Kind, e.ManagedDefault, sessionx.ManagedDefaultKind(e.Kind))
		}
	}
	for _, k := range sessionx.RegisteredKinds() {
		if !inSnap[k] {
			t.Errorf("registered kind %q is missing from the CP snapshot; regenerate it", k)
		}
	}
}
