package opencode

// Unit tests for the opencode side of forking at a message (docs/log/55).
//
// Two contracts are protected. (1) ResolveForkAt admits nothing but a message of THIS
// conversation; let anything else through and a conversation forked at a point unrelated to
// what the user picked appears, looking perfectly plausible. (2) serveForkSession sends the
// fork point as messageID; if that goes through empty it silently turns into a fork of the
// whole conversation.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// forkAtStore builds a store with one root conversation (2 messages) and one child
// (subagent) conversation, and returns the slot meta that resolves to the root.
func forkAtStore(t *testing.T) session.Meta {
	t.Helper()
	db := newOpencodeLiveStore(t)
	defer db.Close()
	dir := "/home/dev/repos/x"
	const root, child = "ses_root", "ses_child"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, root, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,?,?,2)`, child, root, dir); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id, ses, data string }{
		{"msg_1", root, `{"role":"user","time":{"created":1000}}`},
		{"msg_2", root, `{"role":"assistant","time":{"created":1100,"completed":1200}}`},
		{"msg_9", child, `{"role":"user","time":{"created":1300}}`},
	} {
		if _, err := db.Exec(`INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES(?,?,1,1,?)`,
			m.id, m.ses, m.data); err != nil {
			t.Fatal(err)
		}
	}
	// A fork point can only be passed through the serve API, i.e. managed (ResolveForkAt
	// checks the route too).
	return session.Meta{Dir: dir, Name: "n", Kind: session.KindOpencode, Driver: session.DriverManaged}
}

func TestResolveForkAtPassesAnchorThrough(t *testing.T) {
	m := forkAtStore(t)
	got, err := agentImpl{}.ResolveForkAt(m, agents.ForkPoint{Anchor: "msg_1"})
	if err != nil {
		t.Fatalf("ResolveForkAt(msg_1) error: %v", err)
	}
	// opencode's messageID is exclusive (it cuts just before the named message), so passing
	// the anchor the Console picked through unchanged is correct; shifting it by one here
	// moves the fork point by a whole exchange.
	if got != "msg_1" {
		t.Fatalf("ResolveForkAt(msg_1) = %q; want msg_1", got)
	}
}

// "continue from this message" (Include): cut just before the NEXT user message, so every
// reply in between is carried over. With no next message the whole conversation ("") is the
// right answer - that is what keeping everything to the end means.
func TestResolveForkAtInclude(t *testing.T) {
	db := newOpencodeLiveStore(t)
	dir := "/home/dev/repos/y"
	const ses = "ses_inc"
	if _, err := db.Exec(`INSERT INTO session(id,parent_id,directory,time_created) VALUES(?,NULL,?,1)`, ses, dir); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id, data string }{
		{"msg_1", `{"role":"user","time":{"created":1000}}`},
		{"msg_2", `{"role":"assistant","time":{"created":1100,"completed":1200}}`},
		{"msg_3", `{"role":"user","time":{"created":1300}}`},
		{"msg_4", `{"role":"assistant","time":{"created":1400,"completed":1500}}`},
	} {
		if _, err := db.Exec(`INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES(?,?,1,1,?)`,
			m.id, ses, m.data); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	m := session.Meta{Dir: dir, Name: "n", Kind: session.KindOpencode, Driver: session.DriverManaged}

	got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "msg_1", Include: true})
	if err != nil {
		t.Fatalf("ResolveForkAt(msg_1, include): %v", err)
	}
	if got != "msg_3" {
		t.Fatalf("ResolveForkAt(msg_1, include) = %q; want msg_3 (the NEXT prompt — everything "+
			"between it and the anchor is the reply we are keeping)", got)
	}

	got, err = (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "msg_3", Include: true})
	if err != nil {
		t.Fatalf("ResolveForkAt(msg_3, include): %v", err)
	}
	if got != "" {
		t.Fatalf("ResolveForkAt(last exchange, include) = %q; want \"\" = the whole conversation", got)
	}
}

func TestResolveForkAtRejectsUnusableAnchors(t *testing.T) {
	m := forkAtStore(t)
	for _, tc := range []struct{ name, anchor string }{
		{"empty", ""},
		{"unknown", "msg_nope"},
		// A message of the child (subagent) conversation: it is not part of the parent's
		// id sequence, so forking the parent on it cuts at an unrelated point.
		{"sidechain", "msg_9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: tc.anchor})
			if err == nil {
				t.Fatalf("ResolveForkAt(%q) = %q, nil; want an error", tc.anchor, got)
			}
		})
	}
}

// The CLI (TUI) route has no way to pass a fork point. That is not "a bad anchor" but "this
// route cannot do it", so the answer wraps ErrForkAtRoute and the handler can return
// fork_at_unsupported.
func TestResolveForkAtRefusesCLIRoute(t *testing.T) {
	m := forkAtStore(t)
	m.Driver = session.DriverTUI
	_, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "msg_1"})
	if err == nil {
		t.Fatal("ResolveForkAt on the CLI route = nil error; want a refusal")
	}
	if !errors.Is(err, agents.ErrForkAtRoute) {
		t.Fatalf("error = %v; want it to wrap ErrForkAtRoute so the handler can say "+
			"\"this route cannot do it\" instead of \"this fork point is unusable\"", err)
	}
}

func TestServeForkSessionBody(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ses_new"}`)
	}))
	defer srv.Close()

	// With a fork point: messageID is included.
	id, err := serveForkSession(srv.URL, "ses_src", "/dir", "msg_7")
	if err != nil {
		t.Fatalf("serveForkSession error: %v", err)
	}
	if id != "ses_new" {
		t.Fatalf("id = %q; want ses_new", id)
	}
	if gotPath != "/session/ses_src/fork" {
		t.Fatalf("path = %q", gotPath)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body %q: %v", gotBody, err)
	}
	if body["messageID"] != "msg_7" {
		t.Fatalf("body = %v; want messageID=msg_7", body)
	}

	// Without a fork point: fork the whole conversation (an empty object, as before). Sending
	// messageID as an empty string is rejected by opencode's ^msg pattern, so the key itself
	// must be absent.
	if _, err := serveForkSession(srv.URL, "ses_src", "/dir", ""); err != nil {
		t.Fatalf("serveForkSession (whole) error: %v", err)
	}
	if strings.Contains(gotBody, "messageID") {
		t.Fatalf("whole-conversation fork sent %q; want no messageID", gotBody)
	}
}

func TestServeForkSessionRejectedAnchorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad messageID"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := serveForkSession(srv.URL, "ses_src", "/dir", "msg_7")
	if err == nil {
		t.Fatal("serveForkSession(400) = nil error; want one")
	}
	// "could not parse the answer" would read as a sick daemon; the wording has to say the
	// fork point was rejected. The needle stays Japanese because the message it matches is
	// product text (serveForkSession).
	if !strings.Contains(err.Error(), "分岐点") {
		t.Fatalf("error = %v; want it to name the anchor", err)
	}
}

func TestBuildLaunchRefusesForkAtOnCLIRoute(t *testing.T) {
	// The CLI route's `--session <src> --fork` has no argument for a fork point. Dropping it
	// silently would mean "you picked a point but got the whole conversation", so the launch
	// is refused instead.
	m := session.Meta{Dir: t.TempDir(), Name: "n", Kind: session.KindOpencode, ForkFrom: "ses_src", ForkAt: "msg_1"}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("BuildLaunch with ForkAt on the CLI route = nil error; want a refusal")
	}
}
