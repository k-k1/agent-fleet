package bridge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// captureDiscordPosts is captureDiscord that also keeps request bodies, so a posted reply's
// text can be asserted.
func captureDiscordPosts(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"m1"}`))
	}))
	t.Cleanup(srv.Close)
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })
	return &posts
}

func cmdMsg(author, channel, content string) gatewayMessage {
	var m gatewayMessage
	m.ID = "MSG1"
	m.ChannelID = channel
	m.Content = content
	m.Author.ID = author
	return m
}

func TestParseChatCommand(t *testing.T) {
	for in, want := range map[string]chatCommand{
		"!attach sess-b":      {"attach", "sess-b"},
		"/attach  sess-b ":    {"attach", "sess-b"},
		"!START fix the bug":  {"start", "fix the bug"},
		"!start":              {"start", ""},
		"!start line1\nline2": {"start", "line1\nline2"},
	} {
		if got, ok := parseChatCommand(in); !ok || got != want {
			t.Errorf("parseChatCommand(%q) = %+v,%v; want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"attach sess-b", "!starter x", "please !start", "!attached", "hello"} {
		if _, ok := parseChatCommand(in); ok {
			t.Errorf("parseChatCommand(%q) must not be a command", in)
		}
	}
}

func attachDeps(inject *[]string) ReceiverDeps {
	return ReceiverDeps{
		Inject: func(name, text, source string) (string, error) {
			*inject = append(*inject, name+"|"+text+"|"+source)
			return "", nil
		},
		CheckAttach: func(s string) (string, error) {
			if s == "sess-shell" {
				return "REFUSED", errors.New("kind")
			}
			return "", nil
		},
	}
}

// After "!attach", the thread's replies reach the NEW session and nothing reaches the old
// one; the old session no longer owns a thread (its next notification starts a fresh one).
func TestAttachRebindsThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}, "sess-b": {Channel: "C1", Thread: "T-b"}})
	posts := captureDiscordPosts(t)
	var injected []string
	deps := attachDeps(&injected)

	routeInbound(cmdMsg("U1", "T-a", "!attach sess-b"), "tok", "U1", deps)
	if len(injected) != 0 {
		t.Fatalf("a command must not be injected as a reply: %v", injected)
	}
	m := loadThreads()
	if _, ok := m["sess-a"]; ok {
		t.Fatalf("old session must lose the thread: %v", m)
	}
	if m["sess-b"].Thread != "T-a" || m["sess-b"].Channel != "C1" {
		t.Fatalf("sess-b must own T-a: %v", m)
	}
	if name, _ := ThreadToSession("T-a"); name != "sess-b" {
		t.Fatalf("T-a routes to %q", name)
	}
	if _, ok := ThreadToSession("T-b"); ok {
		t.Fatal("sess-b's previous thread must stop routing")
	}
	if len(*posts) != 1 || !strings.Contains((*posts)[0], "POST /channels/T-a/messages") {
		t.Fatalf("confirmation not posted in the thread: %v", *posts)
	}

	routeInbound(cmdMsg("U1", "T-a", "continue"), "tok", "U1", deps)
	if len(injected) != 1 || injected[0] != "sess-b|continue|discord" {
		t.Fatalf("reply after rebind went to the wrong session: %v", injected)
	}
}

// Authorization: the commands ride the reply gate, so a stranger, a bot, and an unknown
// thread can change nothing — and a refused target leaves the binding as it was.
func TestAttachAuthorization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}})
	posts := captureDiscordPosts(t)
	var injected []string
	deps := attachDeps(&injected)
	started := 0
	deps.Start = func(string, string, string, string) (string, error) { started++; return "x", nil }

	bot := cmdMsg("U1", "T-a", "!attach sess-b")
	bot.Author.Bot = true
	for _, m := range []gatewayMessage{
		cmdMsg("U-stranger", "T-a", "!attach sess-b"),
		cmdMsg("U-stranger", "T-a", "!start do evil"),
		bot,
		cmdMsg("U1", "T-unknown", "!attach sess-b"),
		cmdMsg("U1", "T-unknown", "!start x"),
	} {
		routeInbound(m, "tok", "U1", deps)
	}
	if started != 0 || len(injected) != 0 || len(*posts) != 0 {
		t.Fatalf("gate leaked: started=%d injected=%v posts=%v", started, injected, *posts)
	}
	if loadThreads()["sess-a"].Thread != "T-a" {
		t.Fatalf("binding changed: %v", loadThreads())
	}

	routeInbound(cmdMsg("U1", "T-a", "!attach sess-shell"), "tok", "U1", deps)
	if loadThreads()["sess-a"].Thread != "T-a" {
		t.Fatalf("a refused target must not rebind: %v", loadThreads())
	}
	if len(*posts) != 1 || !strings.Contains((*posts)[0], "REFUSED") {
		t.Fatalf("the refusal reason must be posted: %v", *posts)
	}
}

// With the commands unwired the text is an ordinary reply, never silently dropped.
func TestCommandsUnwiredFallThroughToInject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}})
	captureDiscordPosts(t)
	var injected []string
	deps := ReceiverDeps{Inject: func(n, tx, s string) (string, error) { injected = append(injected, n+"|"+tx); return "", nil }}
	routeInbound(cmdMsg("U1", "T-a", "!attach sess-b"), "tok", "U1", deps)
	if len(injected) != 1 || injected[0] != "sess-a|!attach sess-b" {
		t.Fatalf("unwired command must be injected verbatim: %v", injected)
	}
}

func TestStartCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"sess-a": {Channel: "C1", Thread: "T-a"}})
	posts := captureDiscordPosts(t)
	var injected, started []string
	deps := attachDeps(&injected)
	deps.Start = func(from, prompt, source, key string) (string, error) {
		started = append(started, from+"|"+prompt+"|"+source+"|"+key)
		return "STARTED-OK", nil
	}
	routeInbound(cmdMsg("U1", "T-a", "!start fix the bug"), "tok", "U1", deps)
	if len(started) != 1 || started[0] != "sess-a|fix the bug|discord|discord:MSG1" || len(injected) != 0 {
		t.Fatalf("started=%v injected=%v", started, injected)
	}
	if len(*posts) != 1 || !strings.Contains((*posts)[0], "STARTED-OK") {
		t.Fatalf("result not posted: %v", *posts)
	}
	// "!start" with no prompt is usage, not a session.
	routeInbound(cmdMsg("U1", "T-a", "!start"), "tok", "U1", deps)
	if len(started) != 1 {
		t.Fatalf("empty !start must not start: %v", started)
	}
}

// Two racing rebinds of one thread: exactly one wins and the thread ends bound to exactly one
// session (never both, never neither).
func TestRebindRaceOneWinner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < 50; i++ {
		saveThreads(threadMap{"a": {Channel: "C", Thread: "T"}})
		var wg sync.WaitGroup
		wins := make(chan string, 2)
		for _, to := range []string{"b", "c"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, ok := discordThreads.rebind("T", "a", to); ok {
					wins <- to
				}
			}()
		}
		wg.Wait()
		close(wins)
		if len(wins) != 1 {
			t.Fatalf("iteration %d: %d winners", i, len(wins))
		}
		owners := 0
		for _, r := range loadThreads() {
			if r.Thread == "T" {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("iteration %d: thread has %d owners: %v", i, owners, loadThreads())
		}
	}
}

// The sender seeding a thread for a session that was just re-bound must not undo the rebind.
func TestSeedKeepsConcurrentRebind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveThreads(threadMap{"a": {Channel: "C", Thread: "T"}})
	if _, _, ok := discordThreads.rebind("T", "a", "b"); !ok {
		t.Fatal("rebind failed")
	}
	discordThreads.seed("b", threadRef{Channel: "C", Thread: "T-new"}) // sender raced the rebind
	if got := loadThreads()["b"].Thread; got != "T" {
		t.Fatalf("seed clobbered the rebind: b -> %q", got)
	}
	discordThreads.seed("b", threadRef{Channel: "C2", Thread: "T-c2"}) // stale channel is replaced
	if got := loadThreads()["b"].Thread; got != "T-c2" {
		t.Fatalf("a stale-channel mapping must be replaced: b -> %q", got)
	}
}

func TestSlackAttachRebinds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	slackThreads.save(threadMap{"sess-a": {Channel: "C1", Thread: "root-a"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1.2"}`))
	}))
	defer srv.Close()
	old := slackAPIBase
	slackAPIBase = srv.URL
	defer func() { slackAPIBase = old }()
	var injected []string
	deps := attachDeps(&injected)
	creds := slackReceiveCreds{botToken: "xoxb-tok", boundUser: "U9", botUserID: "UBOT"}

	routeSlackInbound(slackInboundMsg{User: "UOTHER", Text: "!attach sess-b", Channel: "C1", TS: "t0", ThreadTS: "root-a"}, creds, deps)
	if n, _ := slackThreads.threadToSession("root-a"); n != "sess-a" {
		t.Fatalf("stranger rebound a Slack thread: %q", n)
	}
	routeSlackInbound(slackInboundMsg{User: "U9", Text: "!attach sess-b", Channel: "C1", TS: "t1", ThreadTS: "root-a"}, creds, deps)
	if n, _ := slackThreads.threadToSession("root-a"); n != "sess-b" {
		t.Fatalf("root-a routes to %q, want sess-b", n)
	}
	if len(injected) != 0 {
		t.Fatalf("command injected: %v", injected)
	}
}
