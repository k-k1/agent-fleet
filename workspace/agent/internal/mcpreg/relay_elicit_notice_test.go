package mcpreg

import (
	"reflect"
	"sync"
	"testing"

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

func TestElicitRelayNotifiesOncePerHeldCall(t *testing.T) {
	resetElicitations(t)
	notices := captureNotices(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	fakeCaller(t, 4242, "AF_SESSION_NAME=fix-mail\x00")
	ch := e.startCall(elicitCallBody, sseHeaders)

	p := waitPending(t, 1)[0]
	got := notices()
	if len(got) != 1 || got[0].ID != p.ID || got[0].Session != "fix-mail" {
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
	for _, tc := range []struct {
		name, session, wantTarget, wantSession string
	}{
		{"known session", "fix-mail", "session", "fix-mail"},
		// A name with no session behind it, or none at all (a shared daemon's call), must not
		// point the notification at a session the Console cannot open.
		{"unknown session", "gone", "workspace", ""},
		{"unattributed", "", "workspace", ""},
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
