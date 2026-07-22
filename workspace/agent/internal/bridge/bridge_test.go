package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestEnqueueWritesBridgedKindsOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Enqueue(Message{Kind: "answer-ready", SessionName: "s1", DisplayName: "P"})
	Enqueue(Message{Kind: "chat-context-pressure", DisplayName: "conv"}) // Console-only kind
	names := queueFiles(queueDir())
	if len(names) != 1 {
		t.Fatalf("queue=%v, want exactly the answer-ready entry", names)
	}
	q, ok := readQueued(filepath.Join(queueDir(), names[0]))
	if !ok || q.Kind != "answer-ready" || q.SessionName != "s1" || q.CreatedAt == "" {
		t.Fatalf("entry=%+v ok=%v", q, ok)
	}
}

func TestEnqueuePrunesOldestBeyondBound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < maxQueue+7; i++ {
		Enqueue(Message{Kind: "question", DisplayName: fmt.Sprintf("d%d", i)})
	}
	if n := len(queueFiles(queueDir())); n != maxQueue {
		t.Fatalf("queue size=%d, want %d", n, maxQueue)
	}
}

func TestEventToggleSemantics(t *testing.T) {
	if !EventEnabled(nil, "exit") {
		t.Fatal("empty selection must mean everything on")
	}
	if EventEnabled([]string{"question"}, "exit") {
		t.Fatal("unselected key must be off")
	}
	if eventKeyFor("plan-approval") != "question" {
		t.Fatal("plan-approval must ride the question toggle")
	}
	if eventKeyFor("chat-auto-paused") != "" {
		t.Fatal("chat-* kinds must not be bridged")
	}
}

// fakeDiscord serves the three REST shapes the provider uses and records sends.
func fakeDiscord(t *testing.T, wantToken string) (*httptest.Server, *[]map[string]string) {
	t.Helper()
	var sent []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/users/@me":
			_ = json.NewEncoder(w).Encode(map[string]string{"username": "af-bot"})
		case r.Method == "GET" && r.URL.Path == "/oauth2/applications/@me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "app1", "name": "af"})
		case r.Method == "GET" && r.URL.Path == "/users/@me/guilds":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "g1", "name": "My Guild"}})
		case r.Method == "GET" && r.URL.Path == "/guilds/g1/channels":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "c1", "name": "general", "type": 0},
				{"id": "v1", "name": "voice", "type": 2},
			})
		case r.Method == "POST" && r.URL.Path == "/users/@me/channels":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "dm-" + body["recipient_id"]})
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/channels/"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, map[string]string{"channel": strings.Split(r.URL.Path, "/")[2], "content": body["content"]})
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "m1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

func TestDiscordSendToChannelAndDM(t *testing.T) {
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	// Channel destination.
	p := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42"}}
	if err := p.Send(Message{Kind: "answer-ready", DisplayName: "Proj", SessionKind: "claude"}); err != nil {
		t.Fatal(err)
	}
	// DM destination: resolves the channel once and caches via callback.
	var cached string
	dm := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", UserID: "777"},
		cacheDM: func(id string) { cached = id }}
	if err := dm.Send(Message{Kind: "exit", DisplayName: "Proj", Detail: "oom"}); err != nil {
		t.Fatal(err)
	}
	if cached != "dm-777" {
		t.Fatalf("cached DM channel=%q", cached)
	}
	got := *sent
	if len(got) != 2 || got[0]["channel"] != "42" || got[1]["channel"] != "dm-777" {
		t.Fatalf("sent=%v", got)
	}
	if !strings.Contains(got[0]["content"], "応答あり") || !strings.Contains(got[0]["content"], "Proj") {
		t.Fatalf("content=%q", got[0]["content"])
	}
	if !strings.Contains(got[1]["content"], "OOM") {
		t.Fatalf("exit content=%q", got[1]["content"])
	}
}

func TestDiscordBotNameUnauthorized(t *testing.T) {
	srv, _ := fakeDiscord(t, "right")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })
	if name, err := DiscordBotName("right"); err != nil || name != "af-bot" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if _, err := DiscordBotName("wrong"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err=%v, want ErrUnauthorized", err)
	}
}

// The setup-wizard REST shapes (docs/37 P1 追補): app info → invite URL,
// guild list, and text-channel filtering for the picker.
func TestDiscordWizardEndpoints(t *testing.T) {
	srv, _ := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	app, err := DiscordAppInfo("tok")
	if err != nil || app.ID != "app1" {
		t.Fatalf("app=%+v err=%v", app, err)
	}
	url := DiscordInviteURL(app.ID)
	if !strings.Contains(url, "client_id=app1") || !strings.Contains(url, "permissions=3072") || !strings.Contains(url, "scope=bot") {
		t.Fatalf("invite url=%q", url)
	}
	gs, err := DiscordGuilds("tok")
	if err != nil || len(gs) != 1 || gs[0].Name != "My Guild" {
		t.Fatalf("guilds=%+v err=%v", gs, err)
	}
	chs, err := DiscordGuildChannels("tok", "g1")
	if err != nil || len(chs) != 1 || chs[0].ID != "c1" {
		t.Fatalf("channels=%+v err=%v (voice must be filtered)", chs, err)
	}
}

// flakyProvider fails the first n sends, then succeeds; records deliveries.
type flakyProvider struct {
	failLeft int
	events   []string
	got      []Message
}

func (f *flakyProvider) Name() string          { return "flaky" }
func (f *flakyProvider) Caps() Caps            { return Caps{CanSend: true} }
func (f *flakyProvider) Wants(key string) bool { return EventEnabled(f.events, key) }
func (f *flakyProvider) Send(m Message) error {
	if f.failLeft > 0 {
		f.failLeft--
		return fmt.Errorf("boom")
	}
	f.got = append(f.got, m)
	return nil
}

func TestDrainRetriesThenDrops(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Enqueue(Message{Kind: "answer-ready", DisplayName: "A"})

	// Fails forever: after maxAttempts drains the entry is dropped.
	p := &flakyProvider{failLeft: 1 << 30}
	for i := 0; i < maxAttempts; i++ {
		drainWith([]Provider{p})
	}
	if n := len(queueFiles(queueDir())); n != 0 {
		t.Fatalf("queue after %d failed drains=%d, want dropped", maxAttempts, n)
	}

	// Fails once, then delivers on the retry tick.
	Enqueue(Message{Kind: "question", DisplayName: "B"})
	p2 := &flakyProvider{failLeft: 1}
	drainWith([]Provider{p2})
	if n := len(queueFiles(queueDir())); n != 1 {
		t.Fatalf("queue after first failure=%d, want kept for retry", n)
	}
	drainWith([]Provider{p2})
	if len(p2.got) != 1 || p2.got[0].DisplayName != "B" {
		t.Fatalf("delivered=%v", p2.got)
	}
}

func TestDrainFiltersByEventToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Enqueue(Message{Kind: "answer-ready", DisplayName: "A"})
	Enqueue(Message{Kind: "session-report", DisplayName: "R"})
	p := &flakyProvider{events: []string{"session-report"}}
	drainWith([]Provider{p})
	if len(p.got) != 1 || p.got[0].Kind != "session-report" {
		t.Fatalf("delivered=%v, want session-report only", p.got)
	}
	if n := len(queueFiles(queueDir())); n != 0 {
		t.Fatalf("queue=%d, filtered entries must be consumed too", n)
	}
}

func TestDrainClearsQueueWhenUnconfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Enqueue(Message{Kind: "answer-ready", DisplayName: "A"})
	DrainOnce() // empty secrets store → no providers → queue cleared
	if n := len(queueFiles(queueDir())); n != 0 {
		t.Fatalf("queue=%d, want cleared with no provider", n)
	}
}

func TestTextNeverEmptyAndCarriesDisplay(t *testing.T) {
	os.Unsetenv("AF_CP_BASE_URL")
	m := Message{Kind: "permission-request", DisplayName: "秘密の花園", SessionKind: "codex"}
	txt := m.Text()
	if !strings.Contains(txt, "許可待ち") || !strings.Contains(txt, "秘密の花園") || !strings.Contains(txt, "codex") {
		t.Fatalf("text=%q", txt)
	}
}
