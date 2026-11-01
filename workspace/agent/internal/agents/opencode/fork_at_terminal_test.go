package opencode

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// terminalV2Slot is a v2 conversation read through the serve (as the shared-serve Terminal reads
// it) owned by a Terminal slot whose pane was launched in the given mode ("" = no record).
func terminalV2Slot(t *testing.T, mode string) (session.Meta, string) {
	t.Helper()
	_, m := v2Slot(t, "tool.messages.json")
	m.Driver = session.DriverTUI
	sid := session.UUID(m.Dir, m.Name)
	if mode != "" {
		markSlotMode(sid, mode)
		t.Cleanup(func() { slotModes.Remove(sid) })
	}
	var w struct {
		Data []struct{ ID, Type string }
	}
	_ = json.Unmarshal(v2Fixture(t, "tool.messages.json"), &w)
	var user string
	for _, mm := range w.Data {
		if mm.Type == "user" {
			user = mm.ID
		}
	}
	return m, user
}

// A Terminal pane attached to af's serve forks at a message through the serve API, exactly like a
// managed session: the anchor is validated against the serve's conversation and passes through.
func TestResolveForkAtOnSharedTerminal(t *testing.T) {
	useMajor(t, 2)
	t.Setenv(standaloneEnv, "")
	m, user := terminalV2Slot(t, modeShared)
	a := agentImpl{}
	if !a.ForkAtOnTerminal(m) {
		t.Fatal("ForkAtOnTerminal = false for a shared-serve Terminal slot")
	}
	if got, err := a.ResolveForkAt(m, agents.ForkPoint{Anchor: user}); err != nil || got != user {
		t.Fatalf("ResolveForkAt = %q, %v; want the anchor", got, err)
	}
	if _, err := a.ResolveForkAt(m, agents.ForkPoint{Anchor: "msg_elsewhere"}); err == nil || errors.Is(err, agents.ErrForkAtRoute) {
		t.Fatalf("a foreign anchor must be a bad anchor, not a route problem: %v", err)
	}
}

// Everything that is not "pane on the shared serve AND the fork will launch on it" stays refused
// with ErrForkAtRoute: a point cannot be passed on those routes, and a whole-conversation copy in
// its place would look like it worked.
func TestForkAtTerminalStaysManagedOnlyElsewhere(t *testing.T) {
	cases := []struct {
		name string
		env  string
		mode string
	}{
		{"standalone pane", "", modeStandalone},
		{"pane launched before the record existed", "", ""},
		{"shared pane but the next launch is --standalone", "1", modeShared},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useMajor(t, 2)
			t.Setenv(standaloneEnv, c.env)
			m, user := terminalV2Slot(t, c.mode)
			a := agentImpl{}
			if a.ForkAtOnTerminal(m) {
				t.Fatal("ForkAtOnTerminal = true")
			}
			if _, err := a.ResolveForkAt(m, agents.ForkPoint{Anchor: user}); !errors.Is(err, agents.ErrForkAtRoute) {
				t.Fatalf("ResolveForkAt error = %v; want ErrForkAtRoute", err)
			}
		})
	}
}

// A shared-serve Terminal fork that cannot reach the serve at launch (it fell back to
// --standalone) is refused, never widened to the whole conversation.
func TestSharedTerminalForkAtRefusedWhenServeUnavailable(t *testing.T) {
	d := newSharedDaemon(t)
	_ = d
	ensureServe = func() (string, int, error) { return "", 0, ErrNotConnected }
	m := terminalMeta(t, "forkat-fallback")
	m.ForkFrom, m.ForkAt = "ses_src", "msg_5"
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("BuildLaunch = nil error; want a refusal instead of a whole-conversation fork")
	}
}

// The point was resolved against one conversation and ForkFrom may name another by launch time (a
// /new or reconnect in between): the launch refuses and copies nothing.
func TestSharedForkAtRefusesAPointOutsideTheSource(t *testing.T) {
	d := newSharedDaemon(t)
	d.sessions["ses_b"] = sharedMetadata("source-sid", "source")
	d.messageIDs = map[string][]string{"ses_b": {"msg_b1"}}
	m := terminalMeta(t, "forkat-moved")
	m.ForkFrom, m.ForkAt = "ses_b", "msg_a1" // msg_a1 belonged to the earlier conversation
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("BuildLaunch accepted a fork point that is not in the source conversation")
	}
	if f := d.callsMatching("POST /api/session/ses_b/fork"); len(f) != 0 {
		t.Fatalf("a fork was made anyway: %v", f)
	}
}

// The resolver names the conversation it resolved against, also for "include the last exchange"
// where the point itself is "" and could not be checked later.
func TestResolveForkAtPinnedNamesTheSource(t *testing.T) {
	useMajor(t, 2)
	t.Setenv(standaloneEnv, "")
	m, user := terminalV2Slot(t, modeShared)
	point, src, err := (agentImpl{}).ResolveForkAtPinned(m, agents.ForkPoint{Anchor: user, Include: true})
	if err != nil || point != "" || src != "ses_a" {
		t.Fatalf("point %q source %q err %v; want \"\" and ses_a", point, src, err)
	}
}
