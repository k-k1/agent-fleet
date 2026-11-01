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

func TestResolveForkAtRejectsUnusableAnchors(t *testing.T) {
	_, m := v2Slot(t, "tool.messages.json")
	for _, tc := range []struct{ name, anchor string }{
		{"empty", ""},
		{"unknown", "msg_nope"},
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
	m := session.Meta{Dir: "/home/dev/repos/x", Name: "n", Kind: session.KindOpencode, Driver: session.DriverTUI}
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
		_, _ = io.WriteString(w, `{"data":{"id":"ses_new"}}`)
	}))
	defer srv.Close()

	// With a fork point: the message the copy stops before is included.
	id, err := serveForkSession(srv.URL, "ses_src", "msg_7")
	if err != nil {
		t.Fatalf("serveForkSession error: %v", err)
	}
	if id != "ses_new" {
		t.Fatalf("id = %q; want ses_new", id)
	}
	if gotPath != "/api/session/ses_src/fork" {
		t.Fatalf("path = %q", gotPath)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body %q: %v", gotBody, err)
	}
	if body["before"] != "msg_7" {
		t.Fatalf("body = %v; want before=msg_7", body)
	}

	// Without a fork point: fork the whole conversation (an empty object). An empty anchor is
	// not a message id, so the key itself must be absent.
	if _, err := serveForkSession(srv.URL, "ses_src", ""); err != nil {
		t.Fatalf("serveForkSession (whole) error: %v", err)
	}
	if gotBody != "{}" {
		t.Fatalf("whole-conversation fork sent %q; want {}", gotBody)
	}
}

func TestServeForkSessionRejectedAnchorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad messageID"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := serveForkSession(srv.URL, "ses_src", "msg_7")
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
