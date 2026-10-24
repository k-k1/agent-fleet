package sessionx

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The lost-log refusal reaches the Console as its own code (409 transcript_lost), wrapped or
// not, so it can offer "start a new conversation" instead of a generic resume failure.
func TestWriteCodexReleasingErrMapsTranscriptLost(t *testing.T) {
	for _, err := range []error{claude.ErrTranscriptLost, fmt.Errorf("wrapped: %w", claude.ErrTranscriptLost)} {
		rec := httptest.NewRecorder()
		if !writeCodexReleasingErr(rec, err) {
			t.Fatalf("%v: not handled", err)
		}
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"transcript_lost"`) {
			t.Fatalf("%v: got %d %s", err, rec.Code, rec.Body.String())
		}
	}
}

// A backup restore that failed on I/O reaches the Console as 409 transcript_restore_failed, with
// an English message carrying the cause, never a Japanese literal.
func TestWriteCodexReleasingErrMapsTranscriptRestore(t *testing.T) {
	err := fmt.Errorf("%w: %w", claude.ErrTranscriptRestore, errors.New("disk full"))
	rec := httptest.NewRecorder()
	if !writeCodexReleasingErr(rec, err) {
		t.Fatal("not handled")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, `"transcript_restore_failed"`) || !strings.Contains(body, "disk full") {
		t.Fatalf("got %d %s", rec.Code, body)
	}
	if strings.ContainsFunc(body, func(r rune) bool { return r > 0x7f }) {
		t.Fatalf("non-English message: %s", body)
	}
}

// "Start a new conversation" on a lost fork must not re-copy the parent: ForkFrom / ForkAt go,
// ForkSids (cache provenance) stays.
func TestStartFreshDropsForkSource(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "forked", Dir: dir, Kind: session.KindClaude,
		ForkFrom: "parent-sid", ForkAt: "anchor", ForkSids: []string{"parent-sid"}})
	claude.NoteTalked(session.UUID(dir, "forked"))

	startFresh("forked")

	m, _ := session.ReadMeta("forked")
	if m.ForkFrom != "" || m.ForkAt != "" || len(m.ForkSids) != 1 {
		t.Fatalf("meta after fresh: %+v", m)
	}
	if claude.HadConversation(session.UUID(dir, "forked")) {
		t.Fatal("conversation marker survived StartFresh")
	}
}

// A stale ?fresh=1 must not touch a conversation that is healthy.
func TestStartFreshIgnoredWhileLogIsHealthy(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: "healthy", Dir: dir, Kind: session.KindClaude, ForkFrom: "p"})
	sid := session.UUID(dir, "healthy")
	logDir := filepath.Join(cfg, "projects", "p")
	_ = os.MkdirAll(logDir, 0o700)
	p := filepath.Join(logDir, sid+".jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"user"}`+"\n"), 0o600)
	claude.NoteTalked(sid)

	startFresh("healthy")

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("healthy log was moved: %v", err)
	}
	if m, _ := session.ReadMeta("healthy"); m.ForkFrom != "p" {
		t.Fatalf("meta changed: %+v", m)
	}
}
