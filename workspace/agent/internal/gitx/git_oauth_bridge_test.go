package gitx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// docs/log/71 §71.8. The workspace side of moving Bitbucket's refresh grant into the CP.
// What must not regress:
//
//   - a successful bridge refresh DROPS the legacy client key/secret (that is the
//     migration: the tenant's app credential stops living on member disks)
//   - the legacy direct grant is used only as a fallback, and only while those legacy
//     values are still there
//   - with neither, the failure says so instead of looking like a revoked token

func withAgentHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "") // plaintext store: this is about the fields, not the seal
}

// ★ The point of the whole change, as one assertion: after the CP answers, the store no
// longer holds the tenant's client secret.
func TestBitbucketRefreshViaCPClearsTheStoredClientSecret(t *testing.T) {
	withAgentHome(t)
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"rot","expires_in":3600}`))
	}))
	defer srv.Close()

	c := secrets.BitbucketCreds{
		AccessToken: "stale", RefreshToken: "r0", Expiry: 1,
		Key: "legacy-key", Secret: "legacy-secret", // written by a pre-docs/log/71 connect
	}
	nc, err := refreshBitbucketViaCP(secrets.CPBridge{BaseURL: srv.URL, Token: "afo_tok"}, c)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if nc.Key != "" || nc.Secret != "" {
		t.Fatalf("the legacy app credential must be dropped once the bridge works: %+v", nc)
	}
	if nc.AccessToken != "fresh" || nc.RefreshToken != "rot" {
		t.Fatalf("creds: %+v", nc)
	}
	if nc.Expiry <= time.Now().Unix() {
		t.Fatalf("expiry must move forward: %d", nc.Expiry)
	}
	if gotAuth != "Bearer afo_tok" {
		t.Fatalf("bridge auth: %q", gotAuth)
	}
	// Only the member's own refresh token goes up — the request must not carry the
	// client secret back to the CP that already has it.
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("body: %q", gotBody)
	}
	if sent["refresh_token"] != "r0" || len(sent) != 1 {
		t.Fatalf("request body: %v", sent)
	}
}

// The fallback exists for exactly one situation: a store written before the change, on a
// workspace that cannot reach the bridge. It must not become the normal path, and it
// must not be reachable once the legacy values are gone.
func TestBitbucketRefreshFallsBackOnlyWhileLegacyCredsRemain(t *testing.T) {
	withAgentHome(t)
	// A bridge that is configured but broken.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()
	// The legacy direct grant against bitbucket.org.
	var directCalls int
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls++
		_, _ = w.Write([]byte(`{"access_token":"by-fallback","expires_in":7200}`))
	}))
	defer direct.Close()
	oldURL := bbTokenURL
	bbTokenURL = direct.URL
	defer func() { bbTokenURL = oldURL }()

	if err := secrets.Update(func(cur *secrets.Data) error {
		cur.GitOAuthBridge = &secrets.CPBridge{BaseURL: down.URL, Token: "afo_tok"}
		return nil
	}); err != nil {
		t.Fatalf("seed bridge: %v", err)
	}

	legacy := secrets.BitbucketCreds{AccessToken: "stale", RefreshToken: "r0", Key: "k", Secret: "s"}
	nc, err := RefreshBitbucket(legacy)
	if err != nil || nc.AccessToken != "by-fallback" {
		t.Fatalf("legacy creds must still refresh when the bridge is down: %+v %v", nc, err)
	}
	if directCalls == 0 {
		t.Fatal("the direct grant was never attempted")
	}

	// Same broken bridge, but a store that has already been migrated: there is nothing
	// to fall back to, and the error must be the bridge's, not a bogus "revoked token".
	directCalls = 0
	migrated := secrets.BitbucketCreds{AccessToken: "stale", RefreshToken: "r0"}
	if _, err := RefreshBitbucket(migrated); err == nil {
		t.Fatal("a failing bridge with no fallback must be an error")
	}
	if directCalls != 0 {
		t.Fatalf("the direct grant must not run without stored client creds, got %d calls", directCalls)
	}
}

// With no bridge and no legacy creds the message has to name the cause. Otherwise this
// surfaces as git prompting for a password on a connection the member never touched.
func TestBitbucketRefreshWithoutBridgeOrLegacyCredsSaysWhy(t *testing.T) {
	withAgentHome(t)
	_, err := RefreshBitbucket(secrets.BitbucketCreds{AccessToken: "stale", RefreshToken: "r0"})
	if err == nil {
		t.Fatal("want an error")
	}
	if got := err.Error(); !strings.Contains(got, "bridge") {
		t.Fatalf("the error must name the missing bridge, got %q", got)
	}
}
