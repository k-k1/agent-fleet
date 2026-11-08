package memconsol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memreview"
)

var t0 = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

const testProject = "demo-project"

// fakeStore is an in-memory memory store. Publish is the only write and it records every call, so
// a test can prove "nothing reached the store" by looking at published.
type fakeStore struct {
	mu        sync.Mutex
	mems      map[string][]memoryx.ConsolMemory // by ScopeKey
	forgotten map[string]bool                   // ScopeKey + "/" + name
	published []memoryx.ConsolItem
	pubIDs    map[string]string // publish id -> commit
	failAt    int               // 1-based Publish call that fails with a store error; 0 never
	pubErr    error
}

func newFake() *fakeStore {
	return &fakeStore{mems: map[string][]memoryx.ConsolMemory{}, forgotten: map[string]bool{}}
}

func (f *fakeStore) put(scope, pid string, m memoryx.ConsolMemory) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ScopeKey(scope, pid)
	for i := range f.mems[k] {
		if f.mems[k][i].Name == m.Name {
			f.mems[k][i] = m
			return
		}
	}
	f.mems[k] = append(f.mems[k], m)
}

func (f *fakeStore) Scopes() ([]memoryx.ConsolScope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []memoryx.ConsolScope{{Scope: "user"}}
	if len(f.mems["project:"+testProject]) > 0 || true {
		out = append(out, memoryx.ConsolScope{Scope: "project", ProjectID: testProject})
	}
	return out, nil
}

func (f *fakeStore) Memories(scope, pid string) ([]memoryx.ConsolMemory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]memoryx.ConsolMemory(nil), f.mems[ScopeKey(scope, pid)]...), nil
}

func (f *fakeStore) Target(scope, pid, name string) (memoryx.ReviewTargetState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := memoryx.ReviewTargetState{Forgotten: f.forgotten[ScopeKey(scope, pid)+"/"+name]}
	for _, m := range f.mems[ScopeKey(scope, pid)] {
		if m.Name == name {
			st.Exists, st.Revision, st.Body, st.Description, st.Type = true, m.Revision, m.Body, m.Description, m.Type
		}
	}
	return st, nil
}

func (f *fakeStore) Publish(id string, it memoryx.ConsolItem, now time.Time) (memoryx.ConsolPublished, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pubErr != nil {
		return memoryx.ConsolPublished{}, f.pubErr
	}
	if f.failAt > 0 && len(f.published)+1 == f.failAt {
		f.failAt = 0
		return memoryx.ConsolPublished{}, errors.New("disk full")
	}
	f.published = append(f.published, it)
	if f.pubIDs == nil {
		f.pubIDs = map[string]string{}
	}
	commit := fmt.Sprintf("%040x", len(f.published))
	f.pubIDs[id] = commit
	// Publishing moves the memory on, as the real store does.
	k := ScopeKey(it.Scope, it.ProjectID)
	for i := range f.mems[k] {
		if f.mems[k][i].Name == it.Name {
			f.mems[k][i].Revision++
		}
	}
	return memoryx.ConsolPublished{Commit: commit, Revision: it.Revision + 1}, nil
}

func (f *fakeStore) Published(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pubIDs[id], nil
}

func (f *fakeStore) Validate(scope, name, description, memType, body string) error {
	if description == "" || memType == "" || body == "" {
		return errors.New("description, type and body are required")
	}
	return nil
}

func (f *fakeStore) nPublished() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

// env points the consolidation directory at a temp dir and installs the fake store.
func env(t *testing.T) *fakeStore {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	f := newFake()
	old := store
	store = f
	t.Cleanup(func() { store = old })
	return f
}

// scriptModel answers by prompt kind. classify and patch take the prompt and return the JSON
// text, or an error. Calls are counted per kind.
type scriptModel struct {
	mu       sync.Mutex
	classify func(prompt string) (string, error)
	patch    func(prompt string) (string, error)
	nClass   int
	nPatch   int
	after    func(n int) // called after every call with the running total
}

func (m *scriptModel) Call(ctx context.Context, prompt string) (memengine.Reply, error) {
	m.mu.Lock()
	var f func(string) (string, error)
	if strings.Contains(prompt, "## Note ") {
		m.nPatch++
		f = m.patch
	} else {
		m.nClass++
		f = m.classify
	}
	n := m.nClass + m.nPatch
	m.mu.Unlock()
	if m.after != nil {
		defer m.after(n)
	}
	if f == nil {
		return memengine.Reply{}, errors.New("no script")
	}
	text, err := f(prompt)
	if err != nil {
		return memengine.Reply{}, err
	}
	return memengine.Reply{Text: text, Usage: memengine.Usage{InputTokens: 1000, OutputTokens: 100}, UsageKnown: true}, nil
}

func (m *scriptModel) calls() int { m.mu.Lock(); defer m.mu.Unlock(); return m.nClass + m.nPatch }

func classifyAs(op string, targets ...string) func(string) (string, error) {
	return func(string) (string, error) {
		b, _ := json.Marshal(map[string]any{"operation": op, "targets": targets, "reason": "because"})
		return string(b), nil
	}
}

func patchEdit(old, new string) func(string) (string, error) {
	return func(string) (string, error) {
		b, _ := json.Marshal(map[string]any{"edits": []map[string]string{{"old": old, "new": new}}})
		return string(b), nil
	}
}

type fakeTurns struct{ started func() bool }

func (f fakeTurns) StartedSince(time.Time) bool { return f.started != nil && f.started() }

func input(small, large memengine.Model, now time.Time) RunInput {
	return RunInput{Models: Models{Small: small, Large: large}, Roles: map[string]string{"small": "claude:haiku", "large": "claude:sonnet"},
		Config: DefaultConfig(), Now: now}
}

// wrong adds a memory carrying a wrong report, which is ripe on its own.
func (f *fakeStore) wrong(name, body string, rev int) {
	f.put("project", testProject, memoryx.ConsolMemory{Name: name, Description: "d " + name, Type: "project", Revision: rev, Body: body,
		Reasons: []memoryx.ConsolReason{{By: "feedback", Rule: "wrong", Note: name + " names a retired branch", Revision: rev}}})
}

func mustRun(t *testing.T, in RunInput) Report {
	t.Helper()
	rep, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep
}

func pendingList(t *testing.T, now time.Time) []Proposal {
	t.Helper()
	ps, err := ListProposals(StatusPending, now)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func stateOf(t *testing.T) *State {
	t.Helper()
	s, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeExport(t *testing.T, ps ...memreview.ExportedProposal) []byte {
	t.Helper()
	b, err := json.Marshal(memreview.Export{Version: memreview.ExportVersion, Source: "adr-0117-review-queue", ExportedAt: t0, Proposals: ps, Rejected: []memreview.ExportedRejection{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(memreview.ExportPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memreview.ExportPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func exported(n int, op, name, reason, body string, created time.Time) memreview.ExportedProposal {
	return memreview.ExportedProposal{ID: fmt.Sprintf("%020x", n), RunID: fmt.Sprintf("%020x", 99), Session: "s", Scope: "project", ProjectID: testProject,
		Op: op, Name: name, Type: "project", Description: "d " + name, Body: body, Reason: reason, Evidence: []int{1}, Created: created}
}

func exportPathForTest() string { return memreview.ExportPath() }

func statStateFile() (os.FileInfo, error) { return os.Stat(statePath()) }
