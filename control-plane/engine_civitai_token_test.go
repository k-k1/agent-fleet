package main

// The operator's Civitai token registered from the Console, the same shape as the Hugging Face
// token (engine_hf_token_test.go) and pinned for the same reasons: a panel that says
// "registered" while every ingest goes out anonymous, a "remove" that leaves a working token in
// the path the task reads, and the two tokens' secrets never crossing.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// civitaiTokenFixture is a deployment whose stack declares the secret, with a master key so the
// value is really sealed rather than degrading to plaintext.
func civitaiTokenFixture(t *testing.T) (*engineCivitaiTokens, store.Store, *fakeSecrets) {
	t.Helper()
	st := testSettingsStore(t)
	master := sha256.Sum256([]byte("test-master"))
	m := &manager{store: st}
	m.master32 = master[:]
	m.custodian = newLocalCustodian(m.master32)
	sm := &fakeSecrets{}
	def := engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"},
		CivitaiTokenSecret: "arn:aws:secretsmanager:ap-northeast-1:1:secret:af-x-civitai"}
	return newEngineCivitaiTokens(def, st, m, sm), st, sm
}

// A registered token reaches BOTH places, and neither is readable as itself, and it is sealed
// under its OWN key ref — not the Hugging Face token's, which would let one custodian rotation
// silently affect both accounts.
func TestCivitaiTokenIsSealedAtRestAndCarriedToTheSecret(t *testing.T) {
	tok, st, sm := civitaiTokenFixture(t)
	ctx := context.Background()

	if aerr := tok.set(ctx, "civitai_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	if got := sm.last(); got != "civitai_secret_value" {
		t.Errorf("the secret holds %q, want the token — the ingest task reads this one", got)
	}
	stored, err := st.GetSetting(ctx, engineCivitaiTokenSetting)
	if err != nil || stored == "" {
		t.Fatalf("setting: %q %v", stored, err)
	}
	if strings.Contains(stored, "civitai_secret_value") {
		t.Errorf("the settings row holds the token in clear: %q", stored)
	}
	if ref, _ := st.GetSetting(ctx, engineCivitaiTokenRefSetting); ref != engineCivitaiTokenKeyRef {
		t.Errorf("key ref = %q, want %q (an unsealable row is unrecoverable)", ref, engineCivitaiTokenKeyRef)
	}
	if ref, _ := st.GetSetting(ctx, engineCivitaiTokenRefSetting); ref == engineHfTokenKeyRef {
		t.Errorf("the Civitai token is sealed under the Hugging Face token's key ref")
	}
	plain, aerr := tok.plaintext(ctx)
	if aerr != nil || plain != "civitai_secret_value" {
		t.Errorf("plaintext = %q %v, want the token back", plain, aerr)
	}
}

// The view the panel reads never carries the value, and it does carry who and when.
func TestCivitaiTokenViewNeverCarriesTheToken(t *testing.T) {
	tok, _, _ := civitaiTokenFixture(t)
	ctx := context.Background()
	if v := tok.view(ctx); !v.Available || v.Configured {
		t.Fatalf("before registering: %+v, want available and not configured", v)
	}
	if aerr := tok.set(ctx, "civitai_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	v := tok.view(ctx)
	if !v.Configured || v.UpdatedBy != "admin1" || v.UpdatedAt == "" {
		t.Errorf("view = %+v, want configured with who and when", v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "civitai_secret") {
		t.Errorf("the token leaked into the panel's answer: %s", b)
	}
}

// A `PutSecretValue` that fails must leave NOTHING registered, the same order as the Hugging
// Face token's — see engine_hf_token.go's set for why the secret goes first.
func TestCivitaiTokenIsNotRegisteredWhenTheSecretRefusesTheWrite(t *testing.T) {
	tok, st, sm := civitaiTokenFixture(t)
	ctx := context.Background()
	sm.fail = errors.New("AccessDeniedException: not authorized to perform secretsmanager:PutSecretValue")

	aerr := tok.set(ctx, "civitai_secret_value", "admin1")
	if aerr == nil {
		t.Fatal("a refused secret write was reported as success")
	}
	if aerr.code != errCodeCivitaiTokenPutFailed {
		t.Errorf("code = %q, want %q", aerr.code, errCodeCivitaiTokenPutFailed)
	}
	if v, _ := st.GetSetting(ctx, engineCivitaiTokenSetting); v != "" {
		t.Errorf("the setting was written anyway: %q", v)
	}
	if tok.view(ctx).Configured {
		t.Error("the panel would claim a token this deployment cannot use")
	}
}

// Removing it puts the sentinel back rather than leaving the last token in the secret.
func TestCivitaiTokenRemovalPutsTheSentinelBack(t *testing.T) {
	tok, st, sm := civitaiTokenFixture(t)
	ctx := context.Background()
	if aerr := tok.set(ctx, "civitai_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	if aerr := tok.clear(ctx, "admin2"); aerr != nil {
		t.Fatalf("clear: %v", aerr.message)
	}
	if got := sm.last(); got != engineCivitaiTokenSentinel {
		t.Errorf("the secret still holds %q after a removal", got)
	}
	if v, _ := st.GetSetting(ctx, engineCivitaiTokenSetting); v != "" {
		t.Errorf("the sealed row survived the removal: %q", v)
	}
	if tok.view(ctx).Configured {
		t.Error("the panel still claims a token")
	}
}

// Every ingest carries the token into the secret again, the same "no way to detect staleness"
// reasoning as the Hugging Face token's.
func TestCivitaiTokenIsStagedBeforeEveryIngest(t *testing.T) {
	tok, _, sm := civitaiTokenFixture(t)
	ctx := context.Background()
	if aerr := tok.set(ctx, "civitai_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	before := sm.count()
	if aerr := tok.stage(ctx); aerr != nil {
		t.Fatalf("stage: %v", aerr.message)
	}
	if sm.count() != before+1 || sm.last() != "civitai_secret_value" {
		t.Errorf("stage wrote %d time(s) (last %q), want one more write of the token",
			sm.count()-before, sm.last())
	}
	tok2, _, sm2 := civitaiTokenFixture(t)
	if aerr := tok2.stage(ctx); aerr != nil {
		t.Fatalf("stage without a token: %v", aerr.message)
	}
	if sm2.count() != 0 {
		t.Errorf("staging without a registered token wrote %q", sm2.last())
	}
}

// A stack that predates this feature declares no secret at all, and registration is refused in
// a way that names the fix — there is no legacy "the stack has its own" case here, unlike the
// Hugging Face token: Civitai never had a CloudFormation parameter to fall back on.
func TestCivitaiTokenOnAnOldStackRefusesRegistration(t *testing.T) {
	st := testSettingsStore(t)
	m := &manager{store: st}
	tok := newEngineCivitaiTokens(engineIngestDef{TaskDef: "af-ingest"}, st, m, &fakeSecrets{})
	ctx := context.Background()

	if tok.available() {
		t.Fatal("a stack with no secret reported itself as able to keep a token")
	}
	aerr := tok.set(ctx, "civitai_secret_value", "admin1")
	if aerr == nil || aerr.code != errCodeCivitaiTokenUnsupported {
		t.Fatalf("set on an old stack = %v, want %q", aerr, errCodeCivitaiTokenUnsupported)
	}
	v := tok.view(ctx)
	if v.Available || v.Configured {
		t.Errorf("view = %+v, want {available:false configured:false}", v)
	}
}

// errCodeOf is declared in engine_hf_token_test.go and shared by this file.

// The routes: a token goes in, the panel can see THAT it is there and never what it is, and the
// delete takes it away — through the handlers, the same coverage engine_hf_token_test.go gives
// the Hugging Face routes.
func TestCivitaiTokenRoutesRegisterAndRemove(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	master := sha256.Sum256([]byte("test-master"))
	mgr := &manager{store: st}
	mgr.master32 = master[:]
	mgr.custodian = newLocalCustodian(mgr.master32)
	a.memberAuth = memberAuth{mgr}
	sm := &fakeSecrets{}
	def := engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"},
		CivitaiTokenSecret: "arn:aws:secretsmanager:ap-northeast-1:1:secret:af-x-civitai"}
	a.reg.ing = &engineIngester{def: def, cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
		civitaiTokens: newEngineCivitaiTokens(def, st, mgr, sm)}

	call := func(method, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/admin/engines/civitai-token", strings.NewReader(body))
		switch method {
		case "GET":
			a.getCivitaiToken(rec, r, store.Identity{ID: "u1"})
		case "PUT":
			a.putCivitaiToken(rec, r, store.Identity{ID: "u1"})
		case "DELETE":
			a.deleteCivitaiToken(rec, r, store.Identity{ID: "u1"})
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, out := call("GET", ""); code != http.StatusOK || out["configured"] != false || out["available"] != true {
		t.Fatalf("GET before = %d %v", code, out)
	}
	if code, out := call("PUT", `{"token":"  civitai_secret_value\n"}`); code != http.StatusOK || out["configured"] != true {
		t.Fatalf("PUT = %d %v", code, out)
	}
	if got := sm.last(); got != "civitai_secret_value" {
		t.Errorf("the secret holds %q — untrimmed", got)
	}
	if code, out := call("GET", ""); code != http.StatusOK || out["updated_by"] != "u1" {
		t.Errorf("GET after = %d %v, want the actor recorded", code, out)
	}
	if _, out := call("GET", ""); out["token"] != nil {
		t.Error("a route answered with the token itself")
	}
	if code, out := call("DELETE", ""); code != http.StatusOK || out["configured"] != false {
		t.Fatalf("DELETE = %d %v", code, out)
	}
	if sm.last() != engineCivitaiTokenSentinel {
		t.Errorf("after DELETE the secret holds %q", sm.last())
	}
	code, out := call("PUT", `{"token":"   "}`)
	if code != http.StatusBadRequest || !strings.Contains(errCodeOf(out), errCodeCivitaiTokenEmpty) {
		t.Errorf("PUT with an empty token = %d %v", code, out)
	}
}
