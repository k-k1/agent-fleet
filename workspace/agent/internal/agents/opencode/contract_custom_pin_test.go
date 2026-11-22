//go:build contract

// The custom provider's pin against the REAL opencode CLI: the URL the credential policy approved
// and the key the run carries are what a `opencode run` sends to, with v1 (`provider`) and v2
// (`providers`) blocks and model-level settings side by side. The unit tests in custom_pin_test.go
// judge the configuration we write; only the CLI can say which of the competing base URLs it
// honours.
//
//	go test -tags contract -run CustomPin ./internal/agents/opencode/
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

type recvHit struct{ path, auth string }

// receiver is a local model endpoint that records each request and refuses it with a
// non-retryable error, so the CLI stops after the first call.
func receiver(t *testing.T) (url string, hits chan recvHit) {
	t.Helper()
	hits = make(chan recvHit, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hits <- recvHit{r.URL.Path, r.Header.Get("Authorization")}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"contract probe","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

// runCLI runs `opencode run` on model with env until the receiver answers once (or the CLI
// exits, or the budget ends), then stops it.
func runCLI(t *testing.T, env []string, dir, model string, first <-chan recvHit) (recvHit, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "opencode", "run", "--format", "json", "-m", model, "hello")
	cmd.Env, cmd.Dir = env, dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	done := make(chan struct{})
	go func() { _ = cmd.Run(); close(done) }()
	defer func() {
		if t.Failed() || testing.Verbose() {
			t.Logf("opencode output: %.1500s", out.String())
		}
	}()
	var h recvHit
	var got bool
	select {
	case h = <-first:
		got = true
	case <-done:
	}
	cancel()
	<-done
	return h, got
}

// pinFixture is a policy approving a local receiver for the custom route, and that receiver's
// connection; decoy is another local endpoint no config of the run may reach.
func pinFixture(t *testing.T) (approved, decoy string, approvedHits, decoyHits chan recvHit) {
	t.Helper()
	approved, approvedHits = receiver(t)
	decoy, decoyHits = receiver(t)
	useIsolationPolicy(t, routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{},
		Endpoints: []string{"127.0.0.1"}, EndpointRoutes: []string{routes.RouteCustomAPI}})
	useCustom(t, &secrets.CustomAPIConn{Provider: "mine", URL: approved + "/v1", APIKey: "stored"})
	return
}

func expectOnly(t *testing.T, env []string, home string, approvedHits, decoyHits chan recvHit) {
	t.Helper()
	h, ok := runCLI(t, env, home, "mine/m", approvedHits)
	if !ok {
		t.Fatalf("the approved endpoint was never called (opencode %s)", opencodeVersion(t))
	}
	if h.auth != "Bearer run-key" || h.path != "/v1/chat/completions" {
		t.Errorf("approved endpoint got %+v, want the run's key on /v1/chat/completions", h)
	}
	select {
	case d := <-decoyHits:
		t.Errorf("the decoy endpoint was called: %+v", d)
	default:
	}
}

// The run's own configuration points the provider at the decoy in every place a base URL can
// sit; RunPin's environment sends the call to the approved endpoint with the run's key anyway.
// v1 is the `provider` block, v2 the `providers` block (package / settings), each with a
// model-level base URL.
func TestContractCustomPinWinsOverRunAndModelSettings(t *testing.T) {
	requireOpencode(t)
	v1 := func(decoy string) map[string]any {
		return map[string]any{"mine": map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"options": map[string]any{"baseURL": decoy + "/v1", "apiKey": "run-key"},
			"models":  map[string]any{"m": map[string]any{"name": "m", "options": map[string]any{"baseURL": decoy + "/v1"}}},
		}}
	}
	v2 := func(decoy string) map[string]any {
		return map[string]any{"mine": map[string]any{
			"package":  "@ai-sdk/openai-compatible",
			"settings": map[string]any{"baseURL": decoy + "/v1", "apiKey": "run-key"},
			"models":   map[string]any{"m": map[string]any{"settings": map[string]any{"baseURL": decoy + "/v1"}}},
		}}
	}
	for name, build := range map[string]func(decoy string) map[string]any{
		"v1":        func(d string) map[string]any { return map[string]any{"provider": v1(d)} },
		"v2":        func(d string) map[string]any { return map[string]any{"providers": v2(d)} },
		"v1 and v2": func(d string) map[string]any { return map[string]any{"provider": v1(d), "providers": v2(d)} },
	} {
		t.Run(name, func(t *testing.T) {
			_, decoy, approvedHits, decoyHits := pinFixture(t)
			home := t.TempDir()
			inline, _ := json.Marshal(build(decoy))
			env, err := PinRun("mine/m").Env(append(envWithHome(home), routeConfigKey+"="+string(inline)), home)
			if err != nil {
				t.Fatal(err)
			}
			expectOnly(t, env, home, approvedHits, decoyHits)
		})
	}
}

// Measured on the real CLI: with the provider-level pin and NO stripping, a model-level base URL
// in the same inline configuration is what the call goes to. This is why the pin strips every
// base URL below the provider and why a config file that carries one refuses the run: the pin's
// precedence over a model-level setting is not something to rely on.
func TestContractModelLevelBaseURLBeatsTheProviderLevelPin(t *testing.T) {
	requireOpencode(t)
	approved, decoy, approvedHits, decoyHits := pinFixture(t)
	home := t.TempDir()
	inline, _ := json.Marshal(map[string]any{"provider": map[string]any{"mine": map[string]any{
		"npm":     "@ai-sdk/openai-compatible",
		"options": map[string]any{"baseURL": approved + "/v1", "apiKey": "run-key"},
		"models":  map[string]any{"m": map[string]any{"name": "m", "options": map[string]any{"baseURL": decoy + "/v1"}}},
	}}})
	env := append(envWithHome(home), routeConfigKey+"="+string(inline))
	h, ok := runCLI(t, env, home, "mine/m", decoyHits)
	if ok {
		t.Logf("model-level base URL wins: decoy got %+v", h)
		return
	}
	select {
	case h = <-approvedHits:
		t.Fatalf("the provider-level pin wins over a model-level base URL (%+v): the stripping and the file refusal may be relaxed, update the pin's doc", h)
	default:
		t.Fatal("neither endpoint was called")
	}
}
