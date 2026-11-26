package mcpreg

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// captureNotices replaces the held-call notification with a recorder.
func captureNotices(t *testing.T) func() []PendingElicitation {
	t.Helper()
	var mu sync.Mutex
	var got []PendingElicitation
	old := noticeHeld
	noticeHeld = func(p PendingElicitation) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}
	t.Cleanup(func() { noticeHeld = old })
	return func() []PendingElicitation {
		mu.Lock()
		defer mu.Unlock()
		return append([]PendingElicitation(nil), got...)
	}
}

func waitNotices(t *testing.T, notices func() []PendingElicitation, n int) []PendingElicitation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := notices(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("notices: %d, want %d", len(notices()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestElicitRelayNotifiesOncePerHeldCall(t *testing.T) {
	resetElicitations(t)
	notices := captureNotices(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	fakeCaller(t, 4242, "AF_SESSION_NAME=fix-mail\x00")
	ch := e.startCall(elicitCallBody, sseHeaders)

	p := waitPending(t, 1)[0]
	// The notice is filed just after the call is listed, on the relay's goroutine.
	got := waitNotices(t, notices, 1)
	if got[0].ID != p.ID || got[0].Session != "fix-mail" {
		t.Fatalf("notices %+v for held call %s", got, p.ID)
	}
	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	waitCall(t, ch)
	if n := len(notices()); n != 1 {
		t.Fatalf("%d notices after the answer, want 1", n)
	}
}

// A call refused before it is held (the list is full) is not announced: there is nothing to answer.
func TestElicitRelayDoesNotNotifyARefusedCall(t *testing.T) {
	resetElicitations(t)
	notices := captureNotices(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	old := elicitMaxHeldOne
	elicitMaxHeldOne = 1
	t.Cleanup(func() { elicitMaxHeldOne = old })
	waitCall(t, e.startCall(elicitCallBody, sseHeaders))
	if got := notices(); len(got) != 0 {
		t.Fatalf("a refused call was announced: %+v", got)
	}
}

func TestHeldNoticeCarriesOnlyTheIDAndAKnownSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "fix-mail", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-11-27T00:00:00Z"})
	session.WriteMeta(session.Meta{Name: "shelved", Kind: session.KindShell, Dir: "/w", CreatedAt: "2026-11-27T00:00:00Z", Archived: true})
	for _, tc := range []struct {
		name, session, wantTarget, wantSession string
	}{
		{"known session", "fix-mail", "session", "fix-mail"},
		// A name with no session behind it, or none at all (a shared daemon's call), must not
		// point the notification at a session the Console cannot open.
		{"unknown session", "gone", "workspace", ""},
		{"unattributed", "", "workspace", ""},
		// The Console's session list leaves archived sessions out, so it could not open one.
		{"archived session", "shelved", "workspace", ""},
		{"malformed name", "../fix-mail", "workspace", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, ev := range notice.List() {
				notice.Ack([]string{ev.ID})
			}
			putHeldNotice(PendingElicitation{ID: "e1", ServerID: "mcp-x", ServerName: "gmail", Tool: "send_mail",
				Session: tc.session, Arguments: []byte(`{"to":"a@example.com"}`)})
			evs := notice.List()
			if len(evs) != 1 {
				t.Fatalf("%d notices", len(evs))
			}
			ev := evs[0]
			if ev.Kind != NoticeKindElicitation || ev.TargetType != tc.wantTarget || ev.SessionName != tc.wantSession {
				t.Fatalf("notice %+v", ev)
			}
			if !reflect.DeepEqual(ev.Payload, map[string]any{"elicitationId": "e1"}) {
				t.Fatalf("payload %v: the outbox is writable by every agent, so it carries the id alone", ev.Payload)
			}
		})
	}
}

func TestOriginOfKeepsNoPathQueryOrUser(t *testing.T) {
	u, err := url.Parse("https://user:secret@mcp.example.com:8443/mcp/v1?key=k#f")
	if err != nil {
		t.Fatal(err)
	}
	if got := originOf(u); got != "https://mcp.example.com:8443" {
		t.Fatalf("origin %q", got)
	}
	if got := originOf(nil); got != "" {
		t.Fatalf("origin of nil %q", got)
	}
}

// The list hands the arguments to the browser as text, which cannot carry bytes that are not
// UTF-8: such a call is refused instead of being shown as something else.
func TestElicitRelayRefusesArgumentsThatAreNotUTF8(t *testing.T) {
	resetElicitations(t)
	_, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, "", elicitCall{params: map[string]json.RawMessage{
		"arguments": json.RawMessage("{\"to\":\"a\xff\"}"),
	}}, inputRequired{})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("err %v", err)
	}
	if n := len(PendingElicitations()); n != 0 {
		t.Fatalf("%d held", n)
	}
}
