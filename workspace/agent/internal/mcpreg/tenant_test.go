package mcpreg

// The Workspace side of tenant distribution (docs/log/48 P4). Three things are pinned here:
//
//   - A distributed definition can never run a command. This is the third line of defence in
//     ADR0031 decision 2, and the only check that runs on the very machine the command would
//     execute on.
//   - fail-open: the cache survives even while the CP is down.
//   - a user_secret is filled with the member's own value (the tenant distributes only the name).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func tenantDef(name string) ServerDef {
	return ServerDef{
		ID: "t-" + name, Name: name, Origin: OriginTenant, Transport: TransportHTTP,
		URL: "https://" + name + ".corp.example/mcp", Enabled: true,
		Targets: Targets{Assistant: true, Session: true},
	}
}

// serveTenant stands in for the CP's GET /internal/mcp-servers and points the env at it.
func serveTenant(t *testing.T, body any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_MCP_TOKEN", "tok")
}

func TestAcceptTenantDropsStdio(t *testing.T) {
	// The CP has no column to hold a command and its API refuses stdio — but this is the
	// check that runs where the command would actually execute, so it must not trust either.
	stdio := ServerDef{ID: "t1", Name: "evil", Transport: TransportStdio, Command: "/bin/sh"}
	kept, dropped := acceptTenant([]ServerDef{stdio, tenantDef("wiki")})
	if dropped != 1 || len(kept) != 1 || kept[0].Name != "wiki" {
		t.Fatalf("stdio was not dropped: kept=%+v dropped=%d", kept, dropped)
	}
	// Even a "remote" definition that smuggles a command along is refused — Validate's
	// CodeHTTPNoCommand rule — so there is no shape that reaches materialize with a command.
	sneaky := tenantDef("sneaky")
	sneaky.Command = "/bin/sh"
	if kept, dropped := acceptTenant([]ServerDef{sneaky}); dropped != 1 || len(kept) != 0 {
		t.Fatalf("an http definition smuggling a command was accepted: kept=%+v dropped=%d", kept, dropped)
	}
}

func TestAcceptTenantForcesOriginAndEnabled(t *testing.T) {
	// A definition claiming to be a user row must not be able to bypass the "tenant rows
	// are read-only" rule by lying about its origin.
	d := tenantDef("wiki")
	d.Origin, d.Enabled = OriginUser, false
	kept, _ := acceptTenant([]ServerDef{d})
	if len(kept) != 1 || kept[0].Origin != OriginTenant || !kept[0].Enabled {
		t.Fatalf("origin/enabled were not normalised: %+v", kept)
	}
}

func TestFetchTenantWritesCacheAndDetectsChange(t *testing.T) {
	withTempHome(t)
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}, "unreadable": 0})

	res, err := FetchTenant()
	if err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}
	if !res.Changed || res.Servers != 1 {
		t.Fatalf("the first fetch was not treated as a change: %+v", res)
	}
	if reg, err := Load(); err != nil || len(dropAF(reg.Servers)) != 1 || dropAF(reg.Servers)[0].Origin != OriginTenant {
		t.Fatalf("the cache does not show up in the registry: %+v %v", reg, err)
	}

	// The second fetch has identical contents → changed=false. Were it true, every CLI's config
	// would be rewritten every 5 minutes, racing claude's own writes to .claude.json (§8.2).
	res2, err := FetchTenant()
	if err != nil {
		t.Fatalf("FetchTenant 2: %v", err)
	}
	if res2.Changed {
		t.Fatal("re-fetching identical contents was treated as a change")
	}
	// The fetch time still moves forward (a stale "last fetched" in the Console makes a
	// just-checked registry look stale).
	if res2.FetchedAt < res.FetchedAt {
		t.Fatalf("fetchedAt went backwards: %d -> %d", res.FetchedAt, res2.FetchedAt)
	}
	if got := loadTenantCache().FetchedAt; got != res2.FetchedAt {
		t.Fatalf("fetchedAt must be written to the cache even with no change: %d != %d", got, res2.FetchedAt)
	}
}

func TestFetchTenantKeepsCacheWhenCPFails(t *testing.T) {
	// fail-open (§6). Made fail-closed, a momentary CP outage would strip MCP from every
	// member's sessions.
	withTempHome(t)
	serveTenant(t, map[string]any{"servers": []ServerDef{tenantDef("wiki")}})
	if _, err := FetchTenant(); err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}
	t.Setenv("AF_CP_BASE_URL", "http://127.0.0.1:1") // unreachable
	if _, err := FetchTenant(); err == nil {
		t.Fatal("an unreachable CP did not produce an error")
	}
	if reg, _ := Load(); len(dropAF(reg.Servers)) != 1 {
		t.Fatalf("a failed fetch wiped the cache: %+v", dropAF(reg.Servers))
	}
}

func TestFetchTenantWithoutBridge(t *testing.T) {
	withTempHome(t)
	t.Setenv("AF_CP_BASE_URL", "")
	t.Setenv("AF_MCP_TOKEN", "")
	if _, err := FetchTenant(); err != ErrTenantBridgeOff {
		t.Fatalf("err = %v, want ErrTenantBridgeOff", err)
	}
	// Unconfigured is a normal state, so no cache file is created either.
	if _, err := os.Stat(tenantCachePath()); !os.IsNotExist(err) {
		t.Fatalf("a cache was created with the bridge unconfigured: %v", err)
	}
}

func TestFetchTenantRejectsBadToken(t *testing.T) {
	withTempHome(t)
	serveTenant(t, map[string]any{"servers": []ServerDef{}})
	t.Setenv("AF_MCP_TOKEN", "wrong")
	if _, err := FetchTenant(); err == nil {
		t.Fatal("a 401 was not returned as an error")
	}
}

// --- user_secret --------------------------------------------------------------------

func TestUserSecretIsHeldBackUntilFilled(t *testing.T) {
	withTempHome(t)
	d := tenantDef("tickets")
	d.UserSecret = true
	d.Headers = map[string]string{"Authorization": ""} // only the name is distributed
	serveTenant(t, map[string]any{"servers": []ServerDef{d}})
	if _, err := FetchTenant(); err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}

	// Until the value is filled in, nothing is materialized and nothing is wired into the
	// assistant (better to leave it out than to start it and let it fail).
	got, err := Get(d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if Ready(got) {
		t.Fatal("a user_secret definition with no value entered is Ready")
	}
	if defs, _ := ForSession("claude"); len(dropAF(defs)) != 0 {
		t.Fatalf("no value entered, yet it is a materialize target: %+v", dropAF(defs))
	}

	if err := SetTenantSecrets(d.ID, map[string]string{"Authorization": "Bearer mine"}); err != nil {
		t.Fatalf("SetTenantSecrets: %v", err)
	}
	got, err = Get(d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Headers["Authorization"] != "Bearer mine" || !Ready(got) {
		t.Fatalf("the member's value was not merged in: %+v", got.Headers)
	}
	if defs, _ := ForSession("claude"); len(dropAF(defs)) != 1 {
		t.Fatalf("still not a materialize target after the value was entered: %+v", dropAF(defs))
	}

	// Masked round trip: the Console sends a stored value back as ***.
	if err := SetTenantSecrets(d.ID, map[string]string{"Authorization": MaskedValue}); err != nil {
		t.Fatalf("SetTenantSecrets masked: %v", err)
	}
	if got, _ := Get(d.ID); got.Headers["Authorization"] != "Bearer mine" {
		t.Fatalf("the value was lost in the masked round trip: %q", got.Headers["Authorization"])
	}
	// An empty string means "clear it".
	if err := SetTenantSecrets(d.ID, map[string]string{"Authorization": ""}); err != nil {
		t.Fatalf("SetTenantSecrets clear: %v", err)
	}
	if got, _ := Get(d.ID); Ready(got) {
		t.Fatal("still Ready after the value was cleared")
	}
}

func TestSetTenantSecretsRefusals(t *testing.T) {
	withTempHome(t)
	plain := tenantDef("wiki") // not a user_secret
	us := tenantDef("tickets")
	us.UserSecret = true
	us.Headers = map[string]string{"Authorization": ""}
	serveTenant(t, map[string]any{"servers": []ServerDef{plain, us}})
	if _, err := FetchTenant(); err != nil {
		t.Fatalf("FetchTenant: %v", err)
	}

	if err := SetTenantSecrets("nope", map[string]string{"A": "b"}); err != ErrNotFound {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
	// If a member could overwrite the value of a definition distributed WITH its value, the
	// connection would no longer use the credentials the tenant intended. Writable only when
	// the definition is a user_secret.
	if err := SetTenantSecrets(plain.ID, map[string]string{"Authorization": "mine"}); err != ErrReadOnly {
		t.Fatalf("definition that is not a user_secret: err = %v, want ErrReadOnly", err)
	}
	// A header the tenant did not distribute is dropped silently (storing it would keep a value
	// nobody ever reads).
	if err := SetTenantSecrets(us.ID, map[string]string{"X-Unasked": "v"}); err != nil {
		t.Fatalf("SetTenantSecrets: %v", err)
	}
	s, _ := secrets.Load()
	if _, ok := s.MCPSecrets[us.ID]; ok {
		t.Fatalf("a header outside the distribution was stored: %+v", s.MCPSecrets)
	}
	if err := SetTenantSecrets(us.ID, map[string]string{"Authorization": "a\nb"}); err == nil {
		t.Fatal("a header value containing a newline was accepted")
	}
}

func TestWithMemberSecretsIgnoresStaleNames(t *testing.T) {
	// A local value for a header the tenant no longer asks for is not sent — which headers go
	// out is the tenant's decision alone.
	got := withMemberSecrets(
		map[string]string{"Authorization": ""},
		map[string]string{"Authorization": "mine", "X-Old": "stale"},
	)
	if got["Authorization"] != "mine" {
		t.Fatalf("the member's own value is missing: %+v", got)
	}
	if _, ok := got["X-Old"]; ok {
		t.Fatalf("a header outside the distribution is being sent: %+v", got)
	}
}

func TestMaskedKeepsUnsetValuesVisible(t *testing.T) {
	// Not filled in ("") means "nobody has entered one", not "a secret is being hidden". Masking
	// it as *** would hide from the Console that the member is the one who has to enter it.
	m := Masked(ServerDef{Headers: map[string]string{"Authorization": "", "X-Team": "sre"}})
	if m.Headers["Authorization"] != "" || m.Headers["X-Team"] != MaskedValue {
		t.Fatalf("wrong masking: %+v", m.Headers)
	}
}
