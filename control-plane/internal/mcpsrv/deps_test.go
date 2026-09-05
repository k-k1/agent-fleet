package mcpsrv

// The mcpsrv unit tests have no package main, so the CP-side seam is wired with fakes
// (the same idea as workspace/agent/internal/mcpx/deps_test.go).
//
// Only the four the tests in this package actually reach are wired — store, master32,
// custodian and the token signing master. The rest panic instead of returning a value: a
// fake that returned zero values would let a test that forgot to wire something go green
// on an empty result. (The real wiring is main's alias_mcp.go.)
//
// No exhaustiveness check is needed here, unlike the reflect-based one in mcpx: the seam
// is an interface rather than a struct of function fields, so a missing implementation is
// a compile error.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type testCP struct {
	store     store.Store
	master32  []byte
	custodian KeyCustodian
}

var _ CP = testCP{}

func (c testCP) Store() store.Store             { return c.store }
func (c testCP) Custodian() KeyCustodian        { return c.custodian }
func (c testCP) Master32() []byte               { return c.master32 }
func (c testCP) TokenSignMaster() []byte        { return c.master32 }
func (c testCP) EvictMembershipCache(string)    { unwired("EvictMembershipCache") }
func (c testCP) TenantSel(*http.Request) string { unwired("TenantSel"); return "" }

// ScopeRank is wired for real even in the fake: New uses it to catch a stale copy, and
// panicking here would put that check out of reach of the mcpsrv unit tests. The values
// are not a copy of pat.go's — they only express that a ladder exists (the real ones
// arrive through cpDeps from alias_mcp.go).
func (c testCP) ScopeRank(scope string) int {
	switch scope {
	case "read":
		return 1
	case "write":
		return 2
	case "admin:dangerous":
		return 3
	}
	return 0
}

func (c testCP) HashPAT(string) string    { unwired("HashPAT"); return "" }
func (c testCP) EgressDefaults() []string { unwired("EgressDefaults"); return nil }

func (c testCP) RuntimeFor(store.Workspace, string, ...string) runtime.Runtime {
	unwired("RuntimeFor")
	return nil
}

func (c testCP) ResolveByMembership(context.Context, string, string) (*Resolved, *APIError) {
	unwired("ResolveByMembership")
	return nil, nil
}

func (c testCP) WorkspaceStateByMembership(context.Context, string) (string, string) {
	unwired("WorkspaceStateByMembership")
	return "", ""
}

func (c testCP) StopWorkspaceByMembership(context.Context, string) error {
	unwired("StopWorkspaceByMembership")
	return nil
}

func (c testCP) ResolveWorkspaceSize(context.Context, store.Workspace) (int64, int, int) {
	unwired("ResolveWorkspaceSize")
	return 0, 0, 0
}

func (c testCP) ResolveSlotClass(context.Context, store.Workspace) (string, string) {
	unwired("ResolveSlotClass")
	return "", ""
}

func (c testCP) CountRunningInTenant(context.Context, string) (int, error) {
	unwired("CountRunningInTenant")
	return 0, nil
}

func (c testCP) AgentSessions(context.Context, runtime.Runtime) ([]Session, error) {
	unwired("AgentSessions")
	return nil, nil
}

func (c testCP) TenantAdminFor(http.ResponseWriter, *http.Request, string) (store.Identity, store.Tenant, bool) {
	unwired("TenantAdminFor")
	return store.Identity{}, store.Tenant{}, false
}

func (c testCP) AgentText(context.Context, runtime.Runtime, string, string, []byte) (string, error) {
	unwired("AgentText")
	return "", nil
}

func (c testCP) ScheduleGuardErr(context.Context, string, string, string) error {
	unwired("ScheduleGuardErr")
	return nil
}

func (c testCP) MemoList(context.Context, string) (any, error) {
	unwired("MemoList")
	return nil, nil
}

func (c testCP) MemoCreate(context.Context, store.MembershipView, string, string, string, string, string) (any, error) {
	unwired("MemoCreate")
	return nil, nil
}

func (c testCP) MemoUpdate(context.Context, string, string, *string, *string, *string, *string, *int) (any, error) {
	unwired("MemoUpdate")
	return nil, nil
}

func (c testCP) MemoFlush(context.Context, runtime.Runtime, string, string, []string) (map[string]any, error) {
	unwired("MemoFlush")
	return nil, nil
}

func (c testCP) TenantLimits(string) (int, int) { unwired("TenantLimits"); return 0, 0 }

func (c testCP) HostStats() (float64, int, uint64, uint64) {
	unwired("HostStats")
	return 0, 0, 0, 0
}

func unwired(name string) {
	panic("mcpsrv test: CP." + name + " is not wired in testCP — wire it in the test that needs it")
}

// testCustodian stands in for the CP's localCustodian (custodian.go, package main),
// which this package cannot construct. It is deliberately NOT a copy of that crypto —
// duplicating AES-GCM here would create a second source for something custodian.go
// already tests. What the header tests actually assert about the custodian SEAM is
// four things, and this fake keeps every one of them load-bearing:
//
//   - the stored value is not the plaintext JSON (base64 of a framed blob),
//   - Wrap/Unwrap round-trips,
//   - keyRef is authenticated, so a row sealed for tenant-a does NOT open under
//     tenant-b (the AAD property),
//   - a corrupt ciphertext is an ERROR, never an empty header map.
type testCustodian struct{ master []byte }

func newTestCustodian(master []byte) testCustodian { return testCustodian{master: master} }

func (c testCustodian) Wrap(_ context.Context, keyRef string, dek []byte) (string, error) {
	if len(c.master) == 0 {
		return "", errors.New("no master key")
	}
	return base64.StdEncoding.EncodeToString(append([]byte(keyRef+"\x00"), dek...)), nil
}

func (c testCustodian) Unwrap(_ context.Context, keyRef, ciphertext string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	ref, body, found := bytes.Cut(raw, []byte{0})
	if !found || string(ref) != keyRef {
		return nil, errors.New("ciphertext is not readable under " + keyRef)
	}
	return body, nil
}
