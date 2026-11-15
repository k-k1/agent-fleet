package chatx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

var engineOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAFEngineLLM}}

// blockingEngine is a chat engine on the deployment's engine route whose requests wait until
// they are cancelled or released; arrived is signalled once per request.
func blockingEngine(t *testing.T) (arrived chan struct{}, release func(), hits *atomic.Int32) {
	t.Helper()
	arrived, done, n := make(chan struct{}, 4), make(chan struct{}), &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		arrived <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-done:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	old := harness.EngineToken
	harness.EngineToken = func(ctx context.Context, key, session string) (harness.EngineConn, bool) {
		return harness.EngineConn{BaseURL: srv.URL, Token: "t", Route: routes.RouteAFEngineLLM, Binding: "af-engine-llm@llm"}, true
	}
	t.Cleanup(func() { harness.EngineToken = old })
	var once bool
	return arrived, func() {
		if !once {
			once = true
			close(done)
		}
	}, n
}

// sendAsync runs one lcpp chat turn and delivers its error.
func sendAsync(c *ChatConversation) chan error {
	out := make(chan error, 1)
	go func() {
		_, err := lcppChat{}.Send(context.Background(), c, "hi")
		out <- err
	}()
	return out
}

// stillRunning fails when the call answers within a while: a cancel that already happened
// reaches the channel only once its CLI or request has stopped.
func stillRunning(t *testing.T, ch chan error, what string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("stopped by %s: %v", what, err)
	case <-time.After(300 * time.Millisecond):
	}
}

func waitErr(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the call was not stopped")
		return nil
	}
}

// lcpp's chat is judged where it runs, on the route its connection resolved to: a forbidden one is
// refused before any request, with no ledger row (ADR 0138 D12).
func TestLcppChatIsJudgedWhereItRuns(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	_, release, hits := blockingEngine(t)
	release()
	useUtilityPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}})
	_, err := lcppChat{}.Send(context.Background(), &ChatConversation{ID: "l1", Model: "qwen"}, "hi")
	if !errors.Is(err, ErrRoutePolicy) || hits.Load() != 0 {
		t.Errorf("a chat on a forbidden engine: %v, %d requests", err, hits.Load())
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("the refused chat left ledger rows: %+v", rows)
	}
	useUtilityPolicy(t, engineOnly)
	if _, err := (lcppChat{}).Send(context.Background(), &ChatConversation{ID: "l2", Model: "qwen"}, "hi"); err != nil {
		t.Errorf("control: a chat on an allowed engine: %v", err)
	}
}

// A chat call in flight when the policy is tightened to forbid its route is stopped at once and
// reported with the policy's code, leaving no ledger row; a policy change that still allows the
// route, or restricts nothing, leaves it running.
func TestAChatInFlightIsStoppedWhenThePolicyForbidsItsRoute(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	arrived, release, _ := blockingEngine(t)
	t.Cleanup(release)
	s := useUtilityPolicy(t, engineOnly)
	ch := sendAsync(&ChatConversation{ID: "l3", Model: "qwen"})
	<-arrived
	for _, p := range []routepolicy.Policy{
		{Revision: 3, Restricted: true, Allowed: []string{routes.RouteAFEngineLLM, routes.RouteAnthropicAPI}},
		{Revision: 4, Allowed: []string{}},
	} {
		if _, err := s.Accept(p); err != nil {
			t.Fatal(err)
		}
		routepolicy.WaitListeners()
		stillRunning(t, ch, "a policy that allows its route")
	}
	if _, err := s.Accept(routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}}); err != nil {
		t.Fatal(err)
	}
	err := waitErr(t, ch)
	if !errors.Is(err, ErrRoutePolicy) || !errors.Is(err, routepolicy.ErrCancelled) {
		t.Fatalf("the stopped call answered %v", err)
	}
	if status, code := policyErrCode(err); status != http.StatusForbidden || code != routes.CodePolicyForbidden {
		t.Errorf("reported as %d %q", status, code)
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("the stopped call left ledger rows: %+v", rows)
	}
}

// A one-shot in flight on opencode, whose route its runner names after resolving the model, is
// stopped the same way: its CLI is killed and the one-shot answers the policy's refusal.
func TestAOneShotInFlightIsStoppedWhenThePolicyForbidsItsRoute(t *testing.T) {
	writePrefs(t, `{"aiShortModels":{"opencode":"openrouter/some-model"}}`) // sets HOME
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Opencode = map[string]string{"OPENROUTER_API_KEY": "or"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prevPref := opencode.UsagePref
	opencode.UsagePref = func() string { return opencode.UsageZen }
	t.Cleanup(func() { opencode.UsagePref = prevPref })
	fakeOpencode(t, "@opencode/cli", "2.0.28")
	bin, started := t.TempDir(), filepath.Join(t.TempDir(), "started")
	script := "#!/bin/sh\nif [ \"$1\" = run ]; then : > '" + started + "'; exec sleep 30; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := useUtilityPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpenRouterAPI}})
	ch := make(chan error, 1)
	go func() {
		_, _, _, err := oneShotRunOn(context.Background(), session.KindOpencode, "title", OneShotShort, "", "hi", "")
		ch <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the one-shot never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The runner named its route: a change that still allows it leaves the one-shot running.
	if _, err := s.Accept(routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpenRouterAPI, routes.RouteAnthropicAPI}}); err != nil {
		t.Fatal(err)
	}
	routepolicy.WaitListeners()
	stillRunning(t, ch, "a policy that allows its route")
	if _, err := s.Accept(routepolicy.Policy{Revision: 4, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, ch); !errors.Is(err, ErrRoutePolicy) || !errors.Is(err, routepolicy.ErrCancelled) {
		t.Fatalf("the stopped one-shot answered %v", err)
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("the stopped one-shot left ledger rows: %+v", rows)
	}
}

// A CLI chat in flight (claude, on its API key) is stopped the same way: its CLI is killed, the call
// answers the policy's refusal, and no ledger row is written.
func TestACLIChatInFlightIsStoppedWhenThePolicyForbidsItsRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	bin, started := t.TempDir(), filepath.Join(t.TempDir(), "started")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n: > '"+started+"'; exec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := useUtilityPolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}})
	ch := make(chan error, 1)
	go func() {
		_, err := (ClaudeChat{}).Send(context.Background(), &ChatConversation{ID: "0c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f"}, "hi")
		ch <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the chat never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := s.Accept(routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, ch); !errors.Is(err, ErrRoutePolicy) || !errors.Is(err, routepolicy.ErrCancelled) {
		t.Fatalf("the stopped chat answered %v", err)
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("the stopped chat left ledger rows: %+v", rows)
	}
}

// A streamed chat's error event carries the policy's code for a refusal or cancel, and nothing
// more for any other failure.
func TestStreamErrEventCarriesThePolicyCode(t *testing.T) {
	cancelled := fmt.Errorf("%w: %w", ErrRoutePolicy, routepolicy.ErrCancelled)
	if ev := streamErrEvent(cancelled); ev["code"] != routes.CodePolicyForbidden {
		t.Errorf("a policy cancel: %v", ev)
	}
	if ev := streamErrEvent(fmt.Errorf("%w: %w", ErrRoutePolicy, routepolicy.ErrUnconfirmed)); ev["code"] != routes.CodePolicyUnconfirmed {
		t.Errorf("an unconfirmed policy: %v", ev)
	}
	if ev := streamErrEvent(errors.New("rate limited")); ev["code"] != nil {
		t.Errorf("another failure: %v", ev)
	}
}
