package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// bridgeFamilies is every per-membership bridge token the CP injects, with the env name it
// arrives under, the family's key derivation and its verifier.
func bridgeFamilies() []struct {
	env    string
	key    func(root []byte) []byte
	verify func([]byte, string) (string, bool)
} {
	return []struct {
		env    string
		key    func(root []byte) []byte
		verify func([]byte, string) (string, bool)
	}{
		{"AF_MEMO_TOKEN", memoSignKey, verifyMemoToken},
		{"AF_SCHEDULE_TOKEN", scheduleSignKey, verifyScheduleToken},
		{"AF_MCP_TOKEN", mcpsrv.MCPSignKey, mcpsrv.VerifyMCPToken},
		{"AF_DOCS_TOKEN", docsSignKey, verifyDocsToken},
		{"AF_ENGINE_ISSUE_TOKEN", engineSignKey, verifyEngineIssueToken},
		{"AF_GIT_OAUTH_TOKEN", gitOAuthSignKey, verifyGitOAuthToken},
		{"AF_AWS_PROFILES_TOKEN", awsProfilesSignKey, verifyAWSProfilesToken},
		{"AF_GCP_PROFILES_TOKEN", gcpProfilesSignKey, verifyGCPProfilesToken},
		{"AF_BRANCH_RULES_TOKEN", branchRulesSignKey, verifyBranchRulesToken},
	}
}

func envValue(env, name string) string {
	for _, kv := range strings.Fields(env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// Epoch 0 is the key itself: a token minted before the epoch existed keeps verifying.
func TestBridgeKeyEpochZeroIsLegacy(t *testing.T) {
	key := memoSignKey([]byte("master-key-for-bridge-tests-00000"))
	if string(bridgeKeyAt(key, 0)) != string(key) {
		t.Fatal("epoch 0 must be the family key unchanged, or every running workspace loses its tokens on upgrade")
	}
	if string(bridgeKeyAt(key, 1)) == string(key) || string(bridgeKeyAt(key, 1)) == string(bridgeKeyAt(key, 2)) {
		t.Fatal("epochs must give distinct keys")
	}
	legacy := mintMemoToken(key, "M-1")
	if _, ok := verifyMemoToken(bridgeKeyAt(key, 0), legacy); !ok {
		t.Fatal("a legacy token must verify at epoch 0")
	}
	if _, ok := verifyMemoToken(bridgeKeyAt(key, 1), legacy); ok {
		t.Fatal("a legacy token must not verify at epoch 1")
	}
}

// Every credential the CP injects as a bridge token dies when the membership's epoch moves,
// on removal, on re-activation and on an administrator's rotation, and a token minted at the
// new epoch works; none of it needs a restart of the CP.
func TestBridgeTokensDieOnRotationRemovalAndReactivation(t *testing.T) {
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	mgr.master32 = []byte("master-key-for-bridge-tests-00000")
	mgr.publicBaseURL = "https://cp.example"
	memID := membershipIDOf(t, st, victim, tn)

	mint := func() map[string]string {
		t.Helper()
		mv, _, _ := st.GetMembershipByID(ctx, memID)
		res, aerr := mgr.buildResolved(ctx, victim, mv)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(ctx, res); aerr != nil {
			t.Fatal(aerr)
		}
		f.mu.Lock()
		env := f.started[len(f.started)-1]
		f.mu.Unlock()
		if err := st.SetWorkspaceState(ctx, "W-1", "stopped"); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, fam := range bridgeFamilies() {
			out[fam.env] = envValue(env, fam.env)
			if out[fam.env] == "" {
				t.Fatalf("%s was not injected", fam.env)
			}
		}
		return out
	}
	verifies := func(tokens map[string]string) (alive, dead int) {
		for _, fam := range bridgeFamilies() {
			if mid, ok := mgr.verifyBridge(ctx, fam.key(mgr.tokenSigningRoot()), tokens[fam.env], fam.verify); ok && mid == memID {
				alive++
			} else {
				dead++
			}
		}
		return
	}

	gen0 := mint()
	if alive, dead := verifies(gen0); alive != len(gen0) || dead != 0 {
		t.Fatalf("fresh tokens: %d alive, %d dead", alive, dead)
	}
	// Administrator rotation.
	if _, found, err := mgr.rotateBridgeTokens(ctx, memID); err != nil || !found {
		t.Fatalf("rotate: found=%v err=%v", found, err)
	}
	if alive, _ := verifies(gen0); alive != 0 {
		t.Fatalf("%d tokens of the earlier epoch still verify after a rotation", alive)
	}
	gen1 := mint()
	if alive, _ := verifies(gen1); alive != len(gen1) {
		t.Fatalf("tokens minted after the rotation: %d alive of %d", alive, len(gen1))
	}
	// Removal, then re-activation of the same membership: the earlier values must not return.
	if err := st.SetMembershipStatus(ctx, memID, "inactive"); err != nil {
		t.Fatal(err)
	}
	if alive, _ := verifies(gen1); alive != 0 {
		t.Fatalf("%d tokens verify for a removed member", alive)
	}
	if err := st.SetMembershipStatus(ctx, memID, "active"); err != nil {
		t.Fatal(err)
	}
	if alive, _ := verifies(gen0); alive != 0 {
		t.Fatalf("%d tokens of epoch 0 came back with the re-activation", alive)
	}
	if alive, _ := verifies(gen1); alive != 0 {
		t.Fatalf("%d tokens of the epoch before the removal came back with the re-activation", alive)
	}
	if alive, _ := verifies(mint()); alive != len(gen0) {
		t.Fatal("tokens minted after the re-activation must work")
	}
	// Never across memberships: a token for another id does not verify under this one's key.
	other := mintMemoToken(bridgeKeyAt(memoSignKey(mgr.tokenSigningRoot()), 0), "someone-else")
	if _, ok := mgr.verifyBridge(ctx, memoSignKey(mgr.tokenSigningRoot()), other, verifyMemoToken); ok {
		t.Fatal("a token for a membership the store does not know verified")
	}
}

// The one-shot advances every membership, running workspaces then read as stale until they
// are restarted, and the restart clears it.
func TestAdvanceAllBridgeEpochsMarksRunningWorkspacesStale(t *testing.T) {
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	mgr.master32 = []byte("master-key-for-bridge-tests-00000")
	mgr.publicBaseURL = "https://cp.example"
	memID := membershipIDOf(t, st, victim, tn)
	start := func() {
		mv, _, _ := st.GetMembershipByID(ctx, memID)
		res, aerr := mgr.buildResolved(ctx, victim, mv)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(ctx, res); aerr != nil {
			t.Fatal(aerr)
		}
	}
	start()
	if mgr.bridgeTokensStale(ctx, memID) {
		t.Fatal("a freshly started workspace is stale")
	}
	n, err := mgr.advanceAllBridgeEpochs(ctx)
	if err != nil || n < 1 {
		t.Fatalf("advance: n=%d err=%v", n, err)
	}
	if !mgr.bridgeTokensStale(ctx, memID) {
		t.Fatal("a running workspace holding pre-advance tokens must read as restart required")
	}
	if err := st.SetWorkspaceState(ctx, "W-1", "stopped"); err != nil {
		t.Fatal(err)
	}
	start()
	if mgr.bridgeTokensStale(ctx, memID) {
		t.Fatal("the restart must clear it")
	}
}

// The deployment-wide signing version: 0 is the master itself (so tokens minted so far keep
// working), 1 and up derive a root of their own, and every version gives different tokens.
func TestTokenSigningVersion(t *testing.T) {
	master := []byte("master-key-for-bridge-tests-00000")
	if string(tokenSigningRootFor(master, 0)) != string(master) {
		t.Fatal("version 0 must be the master: it is what every token minted so far used")
	}
	v1, v2 := tokenSigningRootFor(master, 1), tokenSigningRootFor(master, 2)
	if string(v1) == string(master) || string(v1) == string(v2) {
		t.Fatal("versions must derive distinct roots, none equal to the credential master")
	}
	tok := mintMemoToken(memoSignKey(v1), "M-1")
	if _, ok := verifyMemoToken(memoSignKey(v2), tok); ok {
		t.Fatal("rotating the version must invalidate the tokens of the previous one")
	}
	t.Setenv(tokenSignVersionEnv, "2")
	if tokenSignVersion() != 2 {
		t.Fatal("AF_TOKEN_SIGN_VERSION=2 not read")
	}
	if err := checkTokenSignVersion(); err != nil {
		t.Fatalf("a valid version was refused: %v", err)
	}
	// A malformed or negative value refuses startup: falling back to 0 would kill every token of
	// a deployment on version 1+ and revive the version-0 tokens of the same epoch.
	for _, bad := range []string{"banana", "-1", "1.5"} {
		t.Setenv(tokenSignVersionEnv, bad)
		if checkTokenSignVersion() == nil {
			t.Errorf("AF_TOKEN_SIGN_VERSION=%q was accepted", bad)
		}
	}
	t.Setenv(tokenSignVersionEnv, "")
	if err := checkTokenSignVersion(); err != nil || tokenSignVersion() != 0 {
		t.Fatalf("unset must stay the legacy version 0: %v", err)
	}
}

// Every *_TOKEN / *_SECRET the CP puts in a workspace's environment must be on the list the
// adapters route through secrets / the env file, or it is back in plaintext task definitions.
func TestSecretEnvNamesCoverCPCredentials(t *testing.T) {
	ctx := context.Background()
	f := &envRecordingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	mgr.master32 = []byte("master-key-for-bridge-tests-00000")
	mgr.publicBaseURL = "https://cp.example"
	mgr.internalGitHost = "af.example"
	memID := membershipIDOf(t, st, victim, tn)
	mv, _, _ := st.GetMembershipByID(ctx, memID)
	if _, aerr := mgr.buildResolved(ctx, victim, mv); aerr != nil {
		t.Fatal(aerr)
	}
	f.mu.Lock()
	env := f.built[len(f.built)-1]
	f.mu.Unlock()
	credential := regexp.MustCompile(`(TOKEN|SECRET|PASSWORD|KEY)$`)
	listed := map[string]bool{}
	for _, n := range runtime.SecretEnvNames() {
		listed[n] = true
	}
	seen := 0
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if credential.MatchString(name) && !listed[name] {
			t.Errorf("%s looks like a credential but is not in runtime.SecretEnvNames (it would reach the task definition in plaintext)", name)
		}
		if listed[name] {
			seen++
		}
	}
	if seen < 10 {
		t.Errorf("only %d of the listed credentials were injected; the fixture no longer exercises them", seen)
	}
}

// A session token minted from the issuing token (30-day, held by a model) is bound to the same
// epoch: a rotation or removal retires it, not only the issuing token.
func TestEngineSessionTokenDiesWithTheEpoch(t *testing.T) {
	ctx := context.Background()
	st, mgr, victim, tn := destroyFixture(t, &envRecordingFactory{})
	mgr.master32 = []byte("master-key-for-bridge-tests-00000")
	memID := membershipIDOf(t, st, victim, tn)
	g := engineGateway{mgr: mgr, reg: &engineRegistry{signKey: engineSignKey(mgr.tokenSigningRoot())}}
	epoch, _ := mgr.bridgeEpoch(ctx, memID)
	tok := mintEngineSessionToken(bridgeKeyAt(g.reg.signKey, epoch), memID, "s1", "llm", timeNowPlus())
	if c, ok := g.verifyEngineSession(ctx, tok, timeNow()); !ok || c.MembershipID != memID {
		t.Fatal("a session token of the current epoch must verify")
	}
	if _, _, err := mgr.rotateBridgeTokens(ctx, memID); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.verifyEngineSession(ctx, tok, timeNow()); ok {
		t.Fatal("a session token of the earlier epoch still verifies after a rotation")
	}
}

func timeNow() time.Time     { return time.Now() }
func timeNowPlus() time.Time { return time.Now().Add(time.Hour) }

// The admin route that hands out a borrowed engine token must keep working after the epoch has
// moved: it mints under the current epoch, and the gateway accepts what it minted.
func TestAdminIssuedEngineTokenWorksAfterRotationAndReactivation(t *testing.T) {
	ctx := context.Background()
	st, mgr, reg, a := issueTokenFixture(t)
	mem := issueTokenMember(t, st, "acme", "borrow-bot", "member")
	g := engineGateway{mgr: mgr, reg: reg}
	fetch := func() string {
		t.Helper()
		rec, out := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
		if rec.Code != 200 {
			t.Fatalf("issue-token = %d %s", rec.Code, rec.Body.String())
		}
		tok, _ := out["token"].(string)
		return tok
	}
	accepted := func(tok string) bool {
		req := httptest.NewRequest("POST", "/internal/engine/token", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		_, _, aerr := g.issuerMembership(req)
		return aerr == nil
	}
	first := fetch()
	if !accepted(first) {
		t.Fatal("a freshly issued token is refused")
	}
	if _, _, err := mgr.rotateBridgeTokens(ctx, mem.ID); err != nil {
		t.Fatal(err)
	}
	if accepted(first) {
		t.Fatal("the token of the earlier epoch still works after a rotation")
	}
	second := fetch()
	if !accepted(second) {
		t.Fatal("a token fetched again after a rotation is refused: the admin route minted under epoch 0")
	}
	if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembershipStatus(ctx, mem.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if accepted(second) {
		t.Fatal("a token of the epoch before the removal came back with the re-activation")
	}
	if !accepted(fetch()) {
		t.Fatal("a token fetched after the re-activation is refused")
	}
}

// rotatingBody advances the epoch on the first Read, i.e. after the issuing token authenticated
// and while issueSessionToken reads its request body.
type rotatingBody struct {
	io.Reader
	before func()
	done   bool
}

func (b *rotatingBody) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		b.before()
	}
	return b.Reader.Read(p)
}

func (b *rotatingBody) Close() error { return nil }

// issueSessionToken signs the session token under the epoch the issuing token verified under. A
// rotation while the body is read must leave the token dead (or the request refused), never
// buy a token that verifies under the new epoch with a credential that was just revoked.
func TestEngineSessionMintAcrossARotationIsDead(t *testing.T) {
	g, mids := engineTenantGateFixture(t)
	mid := mids["allowed"]
	issuing := mintEngineIssueToken(g.reg.signKey, mid)
	req := httptest.NewRequest(http.MethodPost, "/internal/engine/token", nil)
	req.Header.Set("Authorization", "Bearer "+issuing)
	req.Body = &rotatingBody{
		Reader: strings.NewReader(`{"session":"s1","key":"llm"}`),
		before: func() {
			if _, _, err := g.mgr.rotateBridgeTokens(context.Background(), mid); err != nil {
				t.Fatal(err)
			}
		},
	}
	rec := httptest.NewRecorder()
	g.issueSessionToken(rec, req)
	if rec.Code == http.StatusUnauthorized {
		return
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	tok, _ := out["token"].(string)
	if _, ok := g.verifyEngineSession(context.Background(), tok, time.Now()); ok {
		t.Fatal("a revoked issuing token bought a session token that verifies under the new epoch")
	}
}
