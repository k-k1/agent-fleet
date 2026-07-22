package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestParseCustomIDRoundTrip(t *testing.T) {
	// question option
	pi, ok := ParseCustomID(customID("q", "sabc", "1", "2", "ff00aa"))
	if !ok || pi.Kind != "q" || pi.Session != "sabc" || pi.QI != 1 || pi.OI != 2 || pi.Fp != "ff00aa" {
		t.Fatalf("q parse=%+v ok=%v", pi, ok)
	}
	// permission / plan
	for _, tc := range []struct{ kind, choice string }{{"p", "allow"}, {"p", "deny"}, {"pl", "approve"}, {"pl", "reject"}} {
		pi, ok := ParseCustomID(customID(tc.kind, tc.choice, "sxyz"))
		if !ok || pi.Kind != tc.kind || pi.Choice != tc.choice || pi.Session != "sxyz" {
			t.Fatalf("%s/%s parse=%+v ok=%v", tc.kind, tc.choice, pi, ok)
		}
	}
	// rejects: not ours / malformed / bad indices
	for _, bad := range []string{"", "x|q|s|0|0|fp", "af", "af|q|s|0|0", "af|q|s|x|0|fp", "af|z|a|b", "af|p|allow"} {
		if _, ok := ParseCustomID(bad); ok {
			t.Errorf("ParseCustomID(%q) accepted, want reject", bad)
		}
	}
}

func TestQuestionFingerprintStable(t *testing.T) {
	raw := json.RawMessage(`[{"question":"Q?","options":[{"label":"A"}]}]`)
	a, b := QuestionFingerprint(raw), QuestionFingerprint(raw)
	if a != b || len(a) != 6 {
		t.Fatalf("fingerprint unstable or wrong length: %q %q", a, b)
	}
	if QuestionFingerprint(raw) == QuestionFingerprint(json.RawMessage(`[{"question":"Q2?"}]`)) {
		t.Fatal("different payloads must fingerprint differently")
	}
}

func TestQuestionMessagesSingleSelect(t *testing.T) {
	raw := json.RawMessage(`[
		{"header":"Env","question":"Which env?","options":[{"label":"dev"},{"label":"prod"}]},
		{"question":"Confirm?","options":[{"label":"yes"},{"label":"no"}]}
	]`)
	msgs := questionMessages("sabc", raw, false)
	if len(msgs) != 2 {
		t.Fatalf("want one message per question, got %d", len(msgs))
	}
	fp := QuestionFingerprint(raw)
	// First question: multi-question index prefix + header + option buttons.
	if !strings.Contains(msgs[0].content, "[1/2]") || !strings.Contains(msgs[0].content, "Which env?") {
		t.Fatalf("q0 heading=%q", msgs[0].content)
	}
	cids := buttonCustomIDs(t, msgs[0])
	if len(cids) != 2 || cids[0] != customID("q", "sabc", "0", "0", fp) || cids[1] != customID("q", "sabc", "0", "1", fp) {
		t.Fatalf("q0 custom_ids=%v", cids)
	}
	if got := buttonCustomIDs(t, msgs[1]); len(got) != 2 || got[1] != customID("q", "sabc", "1", "1", fp) {
		t.Fatalf("q1 custom_ids=%v", got)
	}
}

func TestQuestionMessagesFallbacks(t *testing.T) {
	// multi-select → nil (plain-text fallback)
	if msgs := questionMessages("s", json.RawMessage(`[{"question":"pick","multiSelect":true,"options":[{"label":"a"}]}]`), false); msgs != nil {
		t.Errorf("multi-select must fall back to text, got %d messages", len(msgs))
	}
	// no options → nil
	if msgs := questionMessages("s", json.RawMessage(`[{"question":"free text only"}]`), false); msgs != nil {
		t.Errorf("optionless question must fall back to text")
	}
	// malformed → nil
	if msgs := questionMessages("s", json.RawMessage(`not json`), false); msgs != nil {
		t.Errorf("malformed payload must fall back to text")
	}
}

func TestButtonMessagesPermissionAndPlan(t *testing.T) {
	d := &discordProvider{creds: secrets.DiscordCreds{Lang: ""}}
	perm := d.buttonMessages(Message{Kind: "permission-request", SessionName: "sp"})
	if len(perm) != 1 {
		t.Fatalf("permission messages=%d", len(perm))
	}
	cids := buttonCustomIDs(t, perm[0])
	if len(cids) != 2 || cids[0] != customID("p", "allow", "sp") || cids[1] != customID("p", "deny", "sp") {
		t.Fatalf("permission custom_ids=%v", cids)
	}
	plan := d.buttonMessages(Message{Kind: "plan-approval", SessionName: "sp"})
	pc := buttonCustomIDs(t, plan[0])
	if len(pc) != 2 || pc[0] != customID("pl", "approve", "sp") || pc[1] != customID("pl", "reject", "sp") {
		t.Fatalf("plan custom_ids=%v", pc)
	}
	// answer-ready has no buttons.
	if got := d.buttonMessages(Message{Kind: "answer-ready", SessionName: "sp"}); got != nil {
		t.Errorf("answer-ready must have no buttons, got %d", len(got))
	}
}

// TestSendRendersButtonsInThread: an interactive (Receive+channel) provider posts
// the plain notification AND a buttons message for a question into the session thread.
func TestSendRendersButtonsInThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	type post struct {
		ch         string
		hasButtons bool
	}
	var posts []post
	nextMsg, nextThread := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/channels/42/messages" ||
			r.Method == "POST" && r.URL.Path == "/channels/t1/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, hasComp := body["components"]
			ch := "42"
			if r.URL.Path == "/channels/t1/messages" {
				ch = "t1"
			}
			posts = append(posts, post{ch: ch, hasButtons: hasComp})
			nextMsg++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "m"})
		case r.Method == "POST" && r.URL.Path == "/channels/42/messages/m/threads":
			nextThread++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "t1"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":0}`))
		}
	}))
	t.Cleanup(srv.Close)
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	p := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42",
		Threads: true, Receive: true, MentionUserID: "owner9"}}
	raw := json.RawMessage(`[{"question":"Which env?","options":[{"label":"dev"},{"label":"prod"}]}]`)
	if err := p.Send(Message{Kind: "question", SessionName: "s1", DisplayName: "Proj", Questions: raw}); err != nil {
		t.Fatal(err)
	}
	// Starter (flat, no buttons) seeds the thread; the buttons message lands in the thread.
	if len(posts) != 2 {
		t.Fatalf("posts=%+v, want notification + buttons", posts)
	}
	if posts[0].ch != "42" || posts[0].hasButtons {
		t.Fatalf("starter should be the plain notification: %+v", posts[0])
	}
	if posts[1].ch != "t1" || !posts[1].hasButtons {
		t.Fatalf("buttons message should land in the thread: %+v", posts[1])
	}
}

// TestSendNoButtonsWithoutReceive: buttons need the Receive gateway; without it a
// question is a plain notification only.
func TestSendNoButtonsWithoutReceive(t *testing.T) {
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	p := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42"}} // Receive off
	raw := json.RawMessage(`[{"question":"Which env?","options":[{"label":"dev"},{"label":"prod"}]}]`)
	if err := p.Send(Message{Kind: "question", SessionName: "s1", DisplayName: "Proj", Questions: raw}); err != nil {
		t.Fatal(err)
	}
	if got := *sent; len(got) != 1 {
		t.Fatalf("want a single plain notification, got %d", len(got))
	}
}

// TestGatewayInteractionDispatch: a component INTERACTION_CREATE reaches onInteract;
// a non-component interaction type is ignored.
func TestGatewayInteractionDispatch(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(gwPayload{Op: opHello, D: json.RawMessage(`{"heartbeat_interval":600000}`)})
		_, _, _ = c.ReadMessage() // IDENTIFY
		s1 := 1
		// A non-component interaction (type 2 = application command) — must be ignored.
		_ = c.WriteJSON(gwPayload{Op: opDispatch, T: "INTERACTION_CREATE", S: &s1,
			D: json.RawMessage(`{"id":"i0","type":2,"token":"tk0","data":{"custom_id":"af|p|allow|s1"}}`)})
		s2 := 2
		_ = c.WriteJSON(gwPayload{Op: opDispatch, T: "INTERACTION_CREATE", S: &s2,
			D: json.RawMessage(`{"id":"i1","type":3,"token":"tk1","channel_id":"c1","message":{"id":"m1"},"member":{"user":{"id":"U1"}},"data":{"custom_id":"af|p|allow|s1"}}`)})
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	old := gatewayDialURL
	gatewayDialURL = func(string) (string, error) { return wsURL(srv.URL), nil }
	defer func() { gatewayDialURL = old }()

	got := make(chan gatewayInteraction, 2)
	gw := &gateway{token: "tok", onMsg: func(gatewayMessage) {},
		onInteract: func(gi gatewayInteraction) { got <- gi }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = gw.connectOnce(ctx) }()

	select {
	case gi := <-got:
		// The first (type 2) must have been dropped, so this is the component one.
		if gi.ID != "i1" || gi.Data.CustomID != "af|p|allow|s1" || gi.authorID() != "U1" || gi.Message.ID != "m1" {
			t.Fatalf("unexpected interaction: %+v", gi)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("component INTERACTION_CREATE never reached onInteract")
	}
	if len(got) != 0 {
		t.Fatal("a non-component interaction leaked through")
	}
}

// buttonCustomIDs extracts the custom_id of every button across an outMsg's rows.
func buttonCustomIDs(t *testing.T, om outMsg) []string {
	t.Helper()
	var out []string
	for _, row := range om.components {
		r, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("row is not a map: %T", row)
		}
		comps, _ := r["components"].([]any)
		for _, c := range comps {
			b, ok := c.(map[string]any)
			if !ok {
				t.Fatalf("button is not a map: %T", c)
			}
			out = append(out, b["custom_id"].(string))
		}
	}
	return out
}
