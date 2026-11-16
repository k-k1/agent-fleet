package opencode

import (
	"context"
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func always([]string, string) bool { return true }
func never([]string, string) bool  { return false }

func storeKey(t *testing.T, env, v string) {
	t.Helper()
	if err := secrets.Update(func(s *secrets.Data) error {
		if s.Opencode == nil {
			s.Opencode = map[string]string{}
		}
		s.Opencode[env] = v
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func storedKey(t *testing.T, env string) string {
	t.Helper()
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	return s.Opencode[env]
}

// A key is removed only while it is the confirmed one and still forbidden: a replacement, or a
// policy that no longer forbids it, leaves the slot as it is.
func TestRemoveKeyIsConditional(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	credbind.ResetForTest()
	oldNote := noteRestart
	noteRestart = func(string) {}
	t.Cleanup(func() { noteRestart = oldNote })
	storeKey(t, "OPENROUTER_API_KEY", "sk-or-1")
	confirmed := credbind.Key("OPENROUTER_API_KEY", "sk-or-1")
	storeKey(t, "OPENROUTER_API_KEY", "sk-or-2")
	if err := (agentImpl{}).RemoveCredential(context.Background(), "key:OPENROUTER_API_KEY", confirmed, always); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Errorf("replaced key: %v", err)
	}
	if storedKey(t, "OPENROUTER_API_KEY") != "sk-or-2" {
		t.Fatal("the replacement was removed")
	}
	current := credbind.Key("OPENROUTER_API_KEY", "sk-or-2")
	if err := (agentImpl{}).RemoveCredential(context.Background(), "key:OPENROUTER_API_KEY", current, never); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Errorf("no longer forbidden: %v", err)
	}
	if err := (agentImpl{}).RemoveCredential(context.Background(), "key:OPENROUTER_API_KEY", current, always); err != nil {
		t.Fatal(err)
	}
	if storedKey(t, "OPENROUTER_API_KEY") != "" {
		t.Error("the confirmed key is still stored")
	}
	if err := (agentImpl{}).RemoveCredential(context.Background(), "bogus", current, always); !errors.Is(err, agents.ErrUnknownCredential) {
		t.Errorf("unknown id: %v", err)
	}
}

// The account is judged on the credential serve holds now, never on the status cache: a cache
// still naming the old credential does not let the replacement be deleted, and only the
// confirmed credential's id is ever sent to DELETE.
func TestRemoveAccountReadsServeNotTheCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	credbind.ResetForTest()
	d := newFakeDaemon(t)
	d.setConns([]map[string]any{{"type": "credential", "id": "cred_1", "label": "Personal"}})
	if st := oauthStatus(); st.id != "cred_1" {
		t.Fatalf("cache not primed: %+v", st)
	}
	listed := (agentImpl{}).StoredCredentials(context.Background())
	if len(listed) != 1 || listed[0].ID != "account" {
		t.Fatalf("listed %+v", listed)
	}
	// Replaced in serve; the status cache still says cred_1.
	d.setConns([]map[string]any{{"type": "credential", "id": "cred_2", "label": "Personal"}})
	if st := oauthStatus(); st.id != "cred_1" {
		t.Fatalf("the cache moved on its own (%+v): the test no longer covers a stale cache", st)
	}
	if err := (agentImpl{}).RemoveCredential(context.Background(), "account", listed[0].Binding, always); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Errorf("stale binding: %v", err)
	}
	if got := d.deletes(); len(got) != 0 {
		t.Fatalf("a DELETE was sent for a replaced account: %v", got)
	}
	if err := (agentImpl{}).RemoveCredential(context.Background(), "account", accountBinding("cred_2"), never); !errors.Is(err, agents.ErrCredentialChanged) {
		t.Errorf("no longer forbidden: %v", err)
	}
	if err := (agentImpl{}).RemoveCredential(context.Background(), "account", accountBinding("cred_2"), always); err != nil {
		t.Fatal(err)
	}
	if got := d.deletes(); len(got) != 1 || got[0] != "/api/credential/cred_2" {
		t.Errorf("deletes %v, want only cred_2", got)
	}
}
