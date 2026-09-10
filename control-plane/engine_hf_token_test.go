package main

// The operator's Hugging Face token registered from the Console (ADR 0072 decision 6 as
// revised, phase P5). What is pinned here is the part that has no second chance to be noticed:
// a panel that says "registered" while every ingest goes out anonymous, a "remove" that leaves
// a working token in the path the task reads, and a stack that was rebuilt under a registered
// token and now holds the sentinel.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeSecrets is the Secrets Manager the CP is allowed to have: it writes, and there is no
// read. Every value it was ever given is kept so a test can assert the LAST one — the ingest
// reads whatever is current, not what was written first.
type fakeSecrets struct {
	mu     sync.Mutex
	writes []string
	fail   error
}

func (f *fakeSecrets) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput,
	_ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	f.writes = append(f.writes, aws.ToString(in.SecretString))
	return &secretsmanager.PutSecretValueOutput{}, nil
}

func (f *fakeSecrets) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return ""
	}
	return f.writes[len(f.writes)-1]
}

func (f *fakeSecrets) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// hfTokenFixture is a deployment whose stack declares the secret (i.e. P5 or later), with a
// master key so the value is really sealed rather than degrading to plaintext.
func hfTokenFixture(t *testing.T) (*engineHfTokens, store.Store, *fakeSecrets) {
	t.Helper()
	st := testSettingsStore(t)
	master := sha256.Sum256([]byte("test-master"))
	m := &manager{store: st}
	m.master32 = master[:]
	m.custodian = newLocalCustodian(m.master32)
	sm := &fakeSecrets{}
	def := engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"},
		TokenSecret: "arn:aws:secretsmanager:ap-northeast-1:1:secret:af-x-hf"}
	return newEngineHfTokens(def, st, m, sm), st, sm
}

// A registered token reaches BOTH places, and neither of them is readable as itself: the
// settings row is ciphertext, and the only plaintext copy went to the secret the CP cannot
// read back.
func TestHfTokenIsSealedAtRestAndCarriedToTheSecret(t *testing.T) {
	tok, st, sm := hfTokenFixture(t)
	ctx := context.Background()

	if aerr := tok.set(ctx, "hf_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	if got := sm.last(); got != "hf_secret_value" {
		t.Errorf("the secret holds %q, want the token — the ingest task reads this one", got)
	}
	stored, err := st.GetSetting(ctx, engineHfTokenSetting)
	if err != nil || stored == "" {
		t.Fatalf("setting: %q %v", stored, err)
	}
	if strings.Contains(stored, "hf_secret_value") {
		t.Errorf("the settings row holds the token in clear: %q", stored)
	}
	if ref, _ := st.GetSetting(ctx, engineHfTokenRefSetting); ref != engineHfTokenKeyRef {
		t.Errorf("key ref = %q, want %q (an unsealable row is unrecoverable)", ref, engineHfTokenKeyRef)
	}
	// And it comes back out: a sealed value nobody can open is the same as no token, discovered
	// nine minutes into a Fargate task.
	plain, aerr := tok.plaintext(ctx)
	if aerr != nil || plain != "hf_secret_value" {
		t.Errorf("plaintext = %q %v, want the token back", plain, aerr)
	}
}

// The view the panel reads never carries the value, and it does carry who and when.
func TestHfTokenViewNeverCarriesTheToken(t *testing.T) {
	tok, _, _ := hfTokenFixture(t)
	ctx := context.Background()
	if v := tok.view(ctx); !v.Available || v.Configured {
		t.Fatalf("before registering: %+v, want available and not configured", v)
	}
	if aerr := tok.set(ctx, "hf_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	v := tok.view(ctx)
	if !v.Configured || v.UpdatedBy != "admin1" || v.UpdatedAt == "" {
		t.Errorf("view = %+v, want configured with who and when", v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "hf_secret") {
		t.Errorf("the token leaked into the panel's answer: %s", b)
	}
}

// 🔴 A `PutSecretValue` that fails must leave NOTHING registered. The other order — save, then
// try to carry — produces a deployment whose panel says a token is registered and whose every
// ingest goes out anonymous, and the 401 that follows names the licence, not the token.
func TestHfTokenIsNotRegisteredWhenTheSecretRefusesTheWrite(t *testing.T) {
	tok, st, sm := hfTokenFixture(t)
	ctx := context.Background()
	sm.fail = errors.New("AccessDeniedException: not authorized to perform secretsmanager:PutSecretValue")

	aerr := tok.set(ctx, "hf_secret_value", "admin1")
	if aerr == nil {
		t.Fatal("a refused secret write was reported as success")
	}
	if aerr.code != errCodeHfTokenPutFailed {
		t.Errorf("code = %q, want %q (IAM is the thing to go and look at)", aerr.code, errCodeHfTokenPutFailed)
	}
	if v, _ := st.GetSetting(ctx, engineHfTokenSetting); v != "" {
		t.Errorf("the setting was written anyway: %q", v)
	}
	if tok.view(ctx).Configured {
		t.Error("the panel would claim a token this deployment cannot use")
	}
	if tok.configured(ctx) {
		t.Error("a gated ingest would be allowed to start and fail with a 401 about the licence")
	}
}

// Removing it puts the sentinel back rather than leaving the last token in the secret. Nobody
// would check for that: the row is gone, the panel is honest, and the task still has a token.
func TestHfTokenRemovalPutsTheSentinelBack(t *testing.T) {
	tok, st, sm := hfTokenFixture(t)
	ctx := context.Background()
	if aerr := tok.set(ctx, "hf_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	if aerr := tok.clear(ctx, "admin2"); aerr != nil {
		t.Fatalf("clear: %v", aerr.message)
	}
	if got := sm.last(); got != engineHfTokenSentinel {
		t.Errorf("the secret still holds %q after a removal", got)
	}
	if v, _ := st.GetSetting(ctx, engineHfTokenSetting); v != "" {
		t.Errorf("the sealed row survived the removal: %q", v)
	}
	if tok.view(ctx).Configured {
		t.Error("the panel still claims a token")
	}
}

// 🔴 Every ingest carries the token into the secret again. Not "when it looks stale" — nothing
// can look stale here, because the CP has no GetSecretValue: a stack torn down and stood up
// again holds the sentinel while the DB holds a token, and the only symptom would be a 401.
func TestHfTokenIsStagedBeforeEveryIngest(t *testing.T) {
	tok, _, sm := hfTokenFixture(t)
	ctx := context.Background()
	if aerr := tok.set(ctx, "hf_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	before := sm.count()
	if aerr := tok.stage(ctx); aerr != nil {
		t.Fatalf("stage: %v", aerr.message)
	}
	if sm.count() != before+1 || sm.last() != "hf_secret_value" {
		t.Errorf("stage wrote %d time(s) (last %q), want one more write of the token",
			sm.count()-before, sm.last())
	}
	// With no token registered there is nothing to carry, and staging must not overwrite the
	// secret with an empty value (Secrets Manager refuses one) or with the sentinel (a stack
	// parameter deployment would lose its own token).
	tok2, _, sm2 := hfTokenFixture(t)
	if aerr := tok2.stage(ctx); aerr != nil {
		t.Fatalf("stage without a token: %v", aerr.message)
	}
	if sm2.count() != 0 {
		t.Errorf("staging without a registered token wrote %q", sm2.last())
	}
}

// A stack that predates P5 declares `hasToken` and no secret. Registration is refused in a way
// that names the fix, and the deployment keeps working: its gated repositories still resolve.
func TestHfTokenOnAPreP5StackRefusesRegistrationAndKeepsWorking(t *testing.T) {
	st := testSettingsStore(t)
	m := &manager{store: st}
	tok := newEngineHfTokens(engineIngestDef{TaskDef: "af-ingest", HasToken: true}, st, m, &fakeSecrets{})
	ctx := context.Background()

	if tok.available() {
		t.Fatal("a stack with no secret reported itself as able to keep a token")
	}
	aerr := tok.set(ctx, "hf_secret_value", "admin1")
	if aerr == nil || aerr.code != errCodeHfTokenUnsupported {
		t.Fatalf("set on a pre-P5 stack = %v, want %q", aerr, errCodeHfTokenUnsupported)
	}
	if !tok.configured(ctx) {
		t.Error("the stack's own token stopped counting — gated repositories would be refused")
	}
	v := tok.view(ctx)
	if v.Available || !v.Configured || !v.StackToken {
		t.Errorf("view = %+v, want {available:false configured:true stack_token:true}", v)
	}
}

// The gated verdict follows the DB, not the stack. Same deployment, same stack, same
// repository: registering a token turns the refusal into a start.
func TestGatedIngestFollowsTheRegisteredToken(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	master := sha256.Sum256([]byte("test-master"))
	mgr := &manager{store: st}
	mgr.master32 = master[:]
	mgr.custodian = newLocalCustodian(mgr.master32)
	a.memberAuth = memberAuth{mgr}
	sm := &fakeSecrets{}
	def := engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"}, SecurityGroups: []string{"sg-1"},
		TokenSecret: "arn:aws:secretsmanager:ap-northeast-1:1:secret:af-x-hf"}
	a.reg.ing = &engineIngester{def: def, cluster: "c", ecs: &fakeIngestECS{fail: "not reached"},
		store: st, models: st, tokens: newEngineHfTokens(def, st, mgr, sm)}

	gated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gated":"auto","cardData":{"license":"other"},"siblings":[
		  {"rfilename":"flux1-dev.safetensors","lfs":{"sha256":"` + strings.Repeat("b", 64) + `","size":100}}]}`))
	}))
	defer gated.Close()
	restore := engineIngestBase
	engineIngestBase = gated.URL
	defer func() { engineIngestBase = restore }()

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := `{"id":"flux1-dev","kind":"checkpoint","s3Key":"image/checkpoints/flux1-dev.safetensors",
		  "license_accepted":true,"source":{"hf":{"repo":"black-forest-labs/FLUX.1-dev","file":"flux1-dev.safetensors"}}}`
		r := httptest.NewRequest("POST", "/api/admin/engines/image/ingest", strings.NewReader(body))
		r.SetPathValue("key", "image")
		a.postIngest(rec, r, engineIngestGrant{ident: store.Identity{ID: "u1"}})
		return rec
	}

	rec := post()
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errCodeIngestGatedNoToken) {
		t.Fatalf("without a token: %d %s, want a %s refusal before any task",
			rec.Code, rec.Body.String(), errCodeIngestGatedNoToken)
	}
	if sm.count() != 0 {
		t.Error("a refused ingest still wrote to the secret")
	}

	if aerr := a.reg.ing.tokens.set(context.Background(), "hf_secret_value", "admin1"); aerr != nil {
		t.Fatalf("set: %v", aerr.message)
	}
	staged := sm.count()
	rec = post()
	if strings.Contains(rec.Body.String(), errCodeIngestGatedNoToken) {
		t.Fatalf("still refused as gated after the token was registered: %s", rec.Body.String())
	}
	// It gets past the gate and fails for its own reason (this fixture's ECS refuses), which is
	// what proves the gate was the only thing in the way. On the way through it carried the
	// token into the secret AGAIN — the write is per ingest, not per registration.
	if sm.count() != staged+1 || sm.last() != "hf_secret_value" {
		t.Errorf("the ingest staged %d write(s) (last %q), want one write of the token",
			sm.count()-staged, sm.last())
	}
}

// errCodeOf digs the machine code out of the {"error":{"code":…}} envelope the API writes.
func errCodeOf(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

// The routes: a token goes in, the panel can see THAT it is there and never what it is, and
// the delete takes it away. Through the handlers, because a set/clear that works and a route
// nobody wired look identical from the store.
func TestHfTokenRoutesRegisterAndRemove(t *testing.T) {
	a, _, st := engineModelAdminAPI(t)
	master := sha256.Sum256([]byte("test-master"))
	mgr := &manager{store: st}
	mgr.master32 = master[:]
	mgr.custodian = newLocalCustodian(mgr.master32)
	a.memberAuth = memberAuth{mgr}
	sm := &fakeSecrets{}
	def := engineIngestDef{TaskDef: "af-ingest", Subnets: []string{"subnet-1"},
		TokenSecret: "arn:aws:secretsmanager:ap-northeast-1:1:secret:af-x-hf"}
	a.reg.ing = &engineIngester{def: def, cluster: "c", ecs: &fakeIngestECS{}, store: st, models: st,
		tokens: newEngineHfTokens(def, st, mgr, sm)}

	call := func(method, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/admin/engines/hf-token", strings.NewReader(body))
		switch method {
		case "GET":
			a.getHfToken(rec, r, store.Identity{ID: "u1"})
		case "PUT":
			a.putHfToken(rec, r, store.Identity{ID: "u1"})
		case "DELETE":
			a.deleteHfToken(rec, r, store.Identity{ID: "u1"})
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, out := call("GET", ""); code != http.StatusOK || out["configured"] != false || out["available"] != true {
		t.Fatalf("GET before = %d %v", code, out)
	}
	if code, out := call("PUT", `{"token":"  hf_secret_value\n"}`); code != http.StatusOK || out["configured"] != true {
		t.Fatalf("PUT = %d %v", code, out)
	}
	// Trimmed on the way in: a trailing newline travels into the Authorization header and
	// earns a 401 that reads exactly like an unaccepted licence.
	if got := sm.last(); got != "hf_secret_value" {
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
	if sm.last() != engineHfTokenSentinel {
		t.Errorf("after DELETE the secret holds %q", sm.last())
	}
	// An empty token is a mistake, not a removal: the delete route is how a token goes away.
	code, out := call("PUT", `{"token":"   "}`)
	if code != http.StatusBadRequest || !strings.Contains(errCodeOf(out), errCodeHfTokenEmpty) {
		t.Errorf("PUT with an empty token = %d %v", code, out)
	}
}
