// wiremap_convert_test.go — proof that a site converted from map to struct changed not one
// byte of the wire (CONTRACT-MAP / leg 3).
//
// The old map literals are copied here and kept. Once a site is converted, production holds
// the original shape nowhere, so the baseline would simply be gone: it moves into the test
// instead of being deleted. The copies are mechanical copies of production and must not be
// edited — edited, they stop being a baseline and become another expression of the new
// implementation.
//
// The harness itself and the controls for its traps are in wiremap_equiv_test.go. This file
// only records which sites were converted and how their equivalence is shown.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/wiretest"
)

// wiremapConvertedMarker is the mark that has to appear in the doc comment of every type
// born from a conversion.
//
// A name suffix (`…Wire`) cannot tell them apart: types that predate CONTRACT-MAP, such as
// `sessionWire`, spell it the same way, so picking by name mixes "needs a proof" with "was
// always a struct". Whether a type replaced a map is provenance, not naming, so the
// provenance goes into the comment and the machine reads that.
const wiremapConvertedMarker = "was: map[string]any"

// wiremapConvertedWireTypes returns the names of the struct types whose doc comment declares
// that they replaced a map.
func wiremapConvertedWireTypes(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE || gd.Doc == nil {
				continue
			}
			if !strings.Contains(gd.Doc.Text(), wiremapConvertedMarker) {
				continue
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); ok {
					out = append(out, ts.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	sort.Strings(out)
	return out
}

// --- (1) egressAPI.checkHosts (Console: EgressCheck) ---

type egressCheckIn struct {
	Configured bool
	Mode       string
	Enforce    bool
	Hosts      map[string]egressHostVerdict
}

func TestWireEquivEgressCheck(t *testing.T) {
	inputs := []egressCheckIn{
		{Configured: true, Mode: "enforce", Enforce: true,
			Hosts: map[string]egressHostVerdict{"a.example": {Host: "a.example", Allowed: true, Proposed: false}}},
		// The caller has already make()d it, so it is never nil there; but an empty map
		// and a nil map are `{}` and `null` on the wire, so measure both.
		{Configured: false, Mode: "log-only", Enforce: false, Hosts: map[string]egressHostVerdict{}},
	}
	got := wiretest.AssertEquiv(t, "egressAPI.checkHosts", inputs,
		func(in egressCheckIn) any { // old (copy of the map literal in egress_member.go)
			return map[string]any{
				"configured": in.Configured, "mode": in.Mode, "enforce": in.Enforce, "hosts": in.Hosts,
			}
		},
		func(in egressCheckIn) any {
			return egressCheckWire{
				Configured: in.Configured, Mode: in.Mode, Enforce: in.Enforce, Hosts: in.Hosts,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (2) adminAPI.hostStats (Console: HostStats) ---

type hostStatsIn struct {
	Load1    float64
	Ncpu     int
	MemUsed  uint64
	MemTotal uint64
}

func TestWireEquivHostStats(t *testing.T) {
	inputs := []hostStatsIn{
		{Load1: 1.25, Ncpu: 8, MemUsed: 3 << 30, MemTotal: 10 << 30},
		// A sample that actually measures that uint64 is not taken back through float64:
		// beyond 2^53 a float64 cannot represent the value exactly.
		{Load1: 0, Ncpu: 1, MemUsed: 1<<53 + 1, MemTotal: 1<<62 + 3},
	}
	got := wiretest.AssertEquiv(t, "adminAPI.hostStats", inputs,
		func(in hostStatsIn) any { // old (copy of the map literal in metrics.go)
			return map[string]any{
				"load1": in.Load1, "ncpu": in.Ncpu, "mem_used": in.MemUsed, "mem_total": in.MemTotal,
			}
		},
		func(in hostStatsIn) any {
			return hostStatsWire{
				Load1: in.Load1, Ncpu: in.Ncpu, MemUsed: in.MemUsed, MemTotal: in.MemTotal,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (3) updateStatus (Console: HostUpdateStatus) ---

type updateStatusIn struct {
	Current   string
	Installed string
	Systemd   bool
}

func TestWireEquivUpdateStatus(t *testing.T) {
	inputs := []updateStatusIn{
		{Current: "v1", Installed: "v2", Systemd: true},
		// installed="" is how "nothing staged" is expressed; the key must keep appearing.
		{Current: "v1", Installed: "", Systemd: false},
		{Current: "v1", Installed: "v1", Systemd: false}, // same version = restartRequired false
	}
	got := wiretest.AssertEquiv(t, "updateStatus", inputs,
		func(in updateStatusIn) any { // old (copy of the map literal in update.go)
			return map[string]any{
				"current":         in.Current,
				"installed":       in.Installed,
				"restartRequired": in.Installed != "" && in.Installed != in.Current,
				"systemd":         in.Systemd,
			}
		},
		func(in updateStatusIn) any {
			return hostUpdateStatusWire{
				Current:         in.Current,
				Installed:       in.Installed,
				RestartRequired: in.Installed != "" && in.Installed != in.Current,
				Systemd:         in.Systemd,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (4) workItemsAPI.workItemsPayload (Console: WorkItemPayload) ---
//
// A shape function, so this one conversion types three sites (list x1 / refresh x2).

type workItemsPayloadIn struct {
	Items     []workItemDTO
	Queries   []workItemQueryDTO
	Sessions  []workItemSessionDTO
	FetchedAt string
	Running   bool
}

func TestWireEquivWorkItemsPayload(t *testing.T) {
	inputs := []workItemsPayloadIn{
		{
			Items:     []workItemDTO{{ID: "i1", Labels: []string{"bug"}}},
			Queries:   []workItemQueryDTO{{ID: "q1", Enabled: true}},
			Sessions:  []workItemSessionDTO{{ID: "s1"}},
			FetchedAt: "2026-09-03T00:00:00Z", Running: true,
		},
		// Production has already done make(…, 0, n), so an empty slice goes out. A nil
		// slice is `null` and an empty slice is `[]` — different, so measure both (the
		// zero-value case, everything nil, is added by the harness itself).
		{
			Items:    []workItemDTO{},
			Queries:  []workItemQueryDTO{},
			Sessions: []workItemSessionDTO{},
		},
	}
	got := wiretest.AssertEquiv(t, "workItemsAPI.workItemsPayload", inputs,
		func(in workItemsPayloadIn) any { // old (copy of the map literal in workitems.go)
			return map[string]any{
				"items": in.Items, "queries": in.Queries, "sessions": in.Sessions,
				"fetchedAt": in.FetchedAt, "running": in.Running,
			}
		},
		func(in workItemsPayloadIn) any {
			return workItemsPayloadWire{
				Items: in.Items, Queries: in.Queries, Sessions: in.Sessions,
				FetchedAt: in.FetchedAt, Running: in.Running,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (5) gitServerAPI.blob (Console: Blob) ---
//
// The four exits each return a different key set, so there is one case per exit.

type gitBlobIn struct {
	Ref, Path string
	Size      int64
	TooLarge  bool
	LFS       bool
	LFSOID    string
	Binary    bool
	Content   *string // nil = no key / &"" = empty file (the key is emitted)
}

func TestWireEquivGitBlob(t *testing.T) {
	empty, text := "", "hello"
	inputs := []gitBlobIn{
		{Ref: "main", Path: "a.txt", Size: 5, Content: &text},
		{Ref: "main", Path: "big.bin", Size: 1 << 30, TooLarge: true},
		{Ref: "main", Path: "x.psd", Size: 12, LFS: true,
			LFSOID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Ref: "main", Path: "y.bin", Size: 3, Binary: true},
		// Empty file. The old shape does emit `"content": ""`; string+omitempty would drop
		// it, so if this case never goes red, making Content a pointer bought nothing.
		{Ref: "main", Path: "empty.txt", Size: 0, Content: &empty},
	}
	got := wiretest.AssertEquiv(t, "gitServerAPI.blob", inputs,
		func(in gitBlobIn) any { // old (copy of resp in internal_git_browse.go)
			resp := map[string]any{"ref": in.Ref, "path": in.Path, "size": in.Size}
			if in.TooLarge {
				resp["too_large"] = true
			}
			if in.LFS {
				resp["lfs"] = true
				if in.LFSOID != "" {
					resp["lfs_oid"] = in.LFSOID
				}
			}
			if in.Binary {
				resp["binary"] = true
			}
			if in.Content != nil {
				resp["content"] = *in.Content
			}
			return resp
		},
		func(in gitBlobIn) any {
			return gitBlobWire{Ref: in.Ref, Path: in.Path, Size: in.Size,
				TooLarge: in.TooLarge, LFS: in.LFS, LFSOID: in.LFSOID,
				Binary: in.Binary, Content: in.Content}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (6) gitServerAPI.repoDTO (Console: InternalRepo) ---

type internalRepoIn struct{ Name, DefaultBranch, CloneURL, CreatedAt string }

func TestWireEquivInternalRepo(t *testing.T) {
	inputs := []internalRepoIn{
		{Name: "web", DefaultBranch: "main", CloneURL: "https://x/git/web", CreatedAt: "2026-09-03T00:00:00Z"},
		{Name: "bare", DefaultBranch: "", CloneURL: "", CreatedAt: ""}, // keys appear even when empty
	}
	got := wiretest.AssertEquiv(t, "gitServerAPI.repoDTO", inputs,
		func(in internalRepoIn) any { // old (copy of the map literal in internal_git.go)
			return map[string]any{
				"name": in.Name, "default_branch": in.DefaultBranch,
				"clone_url": in.CloneURL, "created_at": in.CreatedAt, "provider": "internal",
			}
		},
		func(in internalRepoIn) any {
			return internalRepoWire{Name: in.Name, DefaultBranch: in.DefaultBranch,
				CloneURL: in.CloneURL, CreatedAt: in.CreatedAt, Provider: "internal"}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (7) the two variants of sessionHandoffAPI (Console: HandoffOffer) ---
//
// One TS type, `HandoffOffer`, is filled in by two endpoints between them (create=13 /
// listReceived=16). Measure both, or the narrow one is protected while the wide one is lost.

type handoffIn struct {
	ID, SessionID, SessionName, RecipientUserKey, Title, Status  string
	Branch, RepoRemote, HeadSha, CreatedAt, ExpiresAt, DecidedAt string
	AcceptedSessionName                                          string
	OwnerUserKey, Prompt, SourceSessionKind                      string
}

func handoffOldBase(in handoffIn) map[string]any { // copy of the old handoffOfferDTO
	return map[string]any{
		"id": in.ID, "sessionId": in.SessionID, "sessionName": in.SessionName,
		"recipientUserKey": in.RecipientUserKey, "title": in.Title, "status": in.Status,
		"branch": in.Branch, "repoRemote": in.RepoRemote, "headSha": in.HeadSha,
		"createdAt": in.CreatedAt, "expiresAt": in.ExpiresAt, "decidedAt": in.DecidedAt,
		"acceptedSessionName": in.AcceptedSessionName,
	}
}

func handoffNewBase(in handoffIn) handoffOfferWire {
	return handoffOfferWire{
		ID: in.ID, SessionID: in.SessionID, SessionName: in.SessionName,
		RecipientUserKey: in.RecipientUserKey, Title: in.Title, Status: in.Status,
		Branch: in.Branch, RepoRemote: in.RepoRemote, HeadSha: in.HeadSha,
		CreatedAt: in.CreatedAt, ExpiresAt: in.ExpiresAt, DecidedAt: in.DecidedAt,
		AcceptedSessionName: in.AcceptedSessionName,
	}
}

func handoffInputs() []handoffIn {
	return []handoffIn{
		{ID: "o1", SessionID: "c1", SessionName: "s1", RecipientUserKey: "u2", Title: "t",
			Status: "pending", Branch: "b", RepoRemote: "r", HeadSha: "h",
			CreatedAt: "c", ExpiresAt: "e", DecidedAt: "d", AcceptedSessionName: "a",
			OwnerUserKey: "u1", Prompt: "本文", SourceSessionKind: "claude"},
		// Exactly why omitempty was not used: with no key in keys[…] ownerUserKey is "",
		// and a.open() may return empty. The old shape emits those keys all the same.
		{ID: "o2", Status: "withdrawn", OwnerUserKey: "", Prompt: "", SourceSessionKind: ""},
	}
}

func TestWireEquivHandoffOfferCreate(t *testing.T) {
	got := wiretest.AssertEquiv(t, "sessionHandoffAPI.create", handoffInputs(),
		func(in handoffIn) any { return handoffOldBase(in) },
		func(in handoffIn) any { return handoffNewBase(in) })
	t.Logf("comparison mode: %s", got)
}

func TestWireEquivHandoffOfferInbox(t *testing.T) {
	got := wiretest.AssertEquiv(t, "sessionHandoffAPI.listReceived", handoffInputs(),
		func(in handoffIn) any { // old (copy of the shape that appended three keys to the DTO)
			d := handoffOldBase(in)
			d["ownerUserKey"] = in.OwnerUserKey
			d["prompt"] = in.Prompt
			d["sourceSessionKind"] = in.SourceSessionKind
			return d
		},
		func(in handoffIn) any {
			return handoffOfferInboxWire{
				handoffOfferWire:  handoffNewBase(in),
				OwnerUserKey:      in.OwnerUserKey,
				Prompt:            in.Prompt,
				SourceSessionKind: in.SourceSessionKind,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// TestWireHandoffKeyCountsAreIndependentlyFixed pins the absolute key counts independently.
//
// The equivalence harness only sees "the copy of the old and the new agree". Get the copy
// itself wrong and both are wrong by the same amount while staying green — when two
// supposedly independent measurements agree is exactly when to suspect a systematic error.
// So the number of output keys is pinned as an absolute value that does not depend on the
// copy. That also catches an embedding whose effective json names collide: encoding/json
// emits neither of them, and the count drops here.
func TestWireHandoffKeyCountsAreIndependentlyFixed(t *testing.T) {
	in := handoffInputs()[0]
	for _, tc := range []struct {
		name string
		v    any
		want int
	}{
		{"create (the base shape)", handoffNewBase(in), 13},
		{"listReceived (the inbox)", handoffOfferInboxWire{
			handoffOfferWire:  handoffNewBase(in),
			OwnerUserKey:      in.OwnerUserKey,
			Prompt:            in.Prompt,
			SourceSessionKind: in.SourceSessionKind,
		}, 16},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(m) != tc.want {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Errorf("%s: key count = %d, want %d\n  got: %v\n"+
				"  if it dropped, suspect an embedded field whose effective json name collides "+
				"(encoding/json emits neither when two names clash at the same depth).", tc.name, len(m), tc.want, keys)
		}
	}
}

// TestWireEquivConvertedSitesAreAllCovered checks by machine that every converted shape has
// exactly one equivalence test.
//
// Converting and forgetting the equivalence test passes every gate green — the type check
// succeeds and the site merely disappears from the golden files — and nothing else catches a
// conversion with no proof attached.
func TestWireEquivConvertedSitesAreAllCovered(t *testing.T) {
	// converted wire type -> name of its equivalence test. Add a type, add it here too.
	covered := map[string]string{
		"egressCheckWire":       "TestWireEquivEgressCheck",
		"hostStatsWire":         "TestWireEquivHostStats",
		"hostUpdateStatusWire":  "TestWireEquivUpdateStatus",
		"workItemsPayloadWire":  "TestWireEquivWorkItemsPayload",
		"gitBlobWire":           "TestWireEquivGitBlob",
		"internalRepoWire":      "TestWireEquivInternalRepo",
		"handoffOfferWire":      "TestWireEquivHandoffOfferCreate",
		"handoffOfferInboxWire": "TestWireEquivHandoffOfferInbox",
		// Types living in internal/tenantsrv. Their wire types are unexported, so the
		// proof is inside that package
		// (control-plane/internal/tenantsrv/wiremap_convert_test.go).
		"tenantNetworkWire":      "TestWireEquivTenantNetwork（internal/tenantsrv）",
		"tenantNetworkSavedWire": "TestWireEquivTenantNetworkSaved（internal/tenantsrv）",
		"tenantSlotClassWire":    "TestWireEquivTenantSlotClass（internal/tenantsrv）",
		"tenantLoginWire":        "TestWireEquivTenantLogin（internal/tenantsrv）",
	}
	declared := wiremapConvertedWireTypes(t, ".")
	for _, name := range declared {
		if _, ok := covered[name]; !ok {
			t.Errorf("%s is a wire type CONTRACT-MAP added, but no equivalence test is registered for it. "+
				"Converting and forgetting the proof passes every gate green, so stop here.", name)
		}
	}
	for name := range covered {
		found := false
		for _, d := range declared {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("an equivalence test is registered for %s, but the type is not in the source (if you deleted it, delete it from this table too)", name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("not a single wire type was found (the scan is broken)")
	}
	t.Logf("converted wire types: %d", len(declared))
}
