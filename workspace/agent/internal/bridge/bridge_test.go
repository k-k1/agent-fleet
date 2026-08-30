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
	"time"

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
		case r.Method == "GET" && r.URL.Path == "/guilds/g1":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "g1", "owner_id": "owner9"})
		case r.Method == "GET" && r.URL.Path == "/users/owner9":
			_ = json.NewEncoder(w).Encode(map[string]string{"username": "kei_k", "global_name": "Kei"})
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
	if !strings.Contains(got[0]["content"], "入力待ち") || !strings.Contains(got[0]["content"], "Proj") {
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

// The setup-wizard REST shapes (docs/log/37 P1 追補): app info → invite URL,
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
	if !strings.Contains(url, "client_id=app1") || !strings.Contains(url, "permissions=292057779264") || !strings.Contains(url, "scope=bot") {
		t.Fatalf("invite url=%q", url)
	}
	owner, err := DiscordGuildOwner("tok", "g1")
	if err != nil || owner != "owner9" {
		t.Fatalf("owner=%q err=%v", owner, err)
	}
	if name, err := DiscordUserName("tok", "owner9"); err != nil || name != "Kei" {
		t.Fatalf("owner name=%q err=%v", name, err)
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

// TestDiscordThreadPerSession covers the docs/log/37 P1.5 thread lifecycle against
// a stateful fake: starter message → thread creation → posts land in the
// thread → archived thread revives on post → a hand-deleted thread (404)
// recreates → sessions get separate threads → session-less events stay flat.
func TestDiscordThreadPerSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	type post struct{ ch, content string }
	var posts []post
	type threadState struct {
		archived, deleted bool
		name              string
	}
	threads := map[string]*threadState{}
	nextMsg, nextThread := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == "POST" && len(parts) == 3 && parts[0] == "channels" && parts[2] == "messages":
			ch := parts[1]
			if th, ok := threads[ch]; ok {
				if th.deleted {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"code":10003,"message":"Unknown Channel"}`))
					return
				}
				if th.archived {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"code":50083,"message":"Thread is archived"}`))
					return
				}
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			posts = append(posts, post{ch, body["content"].(string)})
			nextMsg++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("m%d", nextMsg)})
		case r.Method == "POST" && len(parts) == 5 && parts[0] == "channels" && parts[4] == "threads":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			nextThread++
			id := fmt.Sprintf("t%d", nextThread)
			threads[id] = &threadState{name: body["name"].(string)}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
		case r.Method == "PATCH" && len(parts) == 2 && parts[0] == "channels":
			if th, ok := threads[parts[1]]; ok {
				th.archived = false
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": parts[1]})
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
		Threads: true, MentionUserID: "owner9"}}
	send := func(kind, session, display string) {
		t.Helper()
		if err := p.Send(Message{Kind: kind, SessionName: session, DisplayName: display}); err != nil {
			t.Fatalf("send %s/%s: %v", kind, session, err)
		}
	}

	// 1) First event: starter in the channel (with mention) + thread created.
	send("answer-ready", "s1", "Proj A")
	if len(posts) != 1 || posts[0].ch != "42" || !strings.HasPrefix(posts[0].content, "<@owner9> ") {
		t.Fatalf("starter=%+v", posts)
	}
	if th := threads["t1"]; th == nil || th.name != "Proj A" {
		t.Fatalf("threads=%+v", threads)
	}
	// 2) Second event lands in the thread.
	send("question", "s1", "Proj A")
	if posts[len(posts)-1].ch != "t1" {
		t.Fatalf("second post=%+v", posts[len(posts)-1])
	}
	// 3) Archived thread revives transparently. (answer-ready, not session-report:
	// session-report is now suppressed in thread mode — see TestSessionReportThreadSuppressed.)
	threads["t1"].archived = true
	send("answer-ready", "s1", "Proj A")
	last := posts[len(posts)-1]
	if last.ch != "t1" || threads["t1"].archived {
		t.Fatalf("archive revive failed: %+v archived=%v", last, threads["t1"].archived)
	}
	// 4) Hand-deleted thread → mapping dropped, new starter + new thread.
	threads["t1"].deleted = true
	send("answer-ready", "s1", "Proj A")
	if posts[len(posts)-1].ch != "42" || threads["t2"] == nil {
		t.Fatalf("recreate failed: %+v threads=%+v", posts[len(posts)-1], threads)
	}
	send("question", "s1", "Proj A")
	if posts[len(posts)-1].ch != "t2" {
		t.Fatalf("post after recreate=%+v", posts[len(posts)-1])
	}
	// 5) A different session gets its own thread.
	send("answer-ready", "s2", "Proj B")
	if threads["t3"] == nil || threads["t3"].name != "Proj B" {
		t.Fatalf("threads=%+v", threads)
	}
	// 6) Session-less events (bridge-test) stay flat in the channel.
	send("bridge-test", "", "")
	if posts[len(posts)-1].ch != "42" {
		t.Fatalf("session-less post=%+v", posts[len(posts)-1])
	}
	if _, err := os.Stat(discordThreads.path()); err != nil {
		t.Fatalf("thread store not persisted: %v", err)
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

// TestDiscord429RetriesInline: a 429 is retried inline (respecting retry_after)
// instead of failing the post — the fix that stops a duplicate storm (docs/log/37 重複対策).
func TestDiscord429RetriesInline(t *testing.T) {
	oldCap, oldN := discordRetryCap, discordRateRetries
	discordRetryCap, discordRateRetries = 20*time.Millisecond, 3
	t.Cleanup(func() { discordRetryCap, discordRateRetries = oldCap, oldN })

	var attempts, delivered int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 { // rate-limit the first two tries
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":0.01,"message":"rate limited"}`))
			return
		}
		delivered++
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "m1"})
	}))
	t.Cleanup(srv.Close)
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	if _, err := discordPostMessage("tok", "42", "hi"); err != nil {
		t.Fatalf("429 should be retried to success, got %v", err)
	}
	if attempts != 3 || delivered != 1 {
		t.Fatalf("attempts=%d delivered=%d, want 3 tries then a single delivery", attempts, delivered)
	}
}

// resumableFake records the `from` cursor of each SendFrom call and fails once at a
// given sub-message index, so the drain's resume path can be asserted end to end.
type resumableFake struct {
	total  int
	failAt int   // fail once at this index; set to -1 after firing
	calls  []int // the `from` each SendFrom was invoked with
	posts  int   // successful sub-message posts (must never double-count)
}

func (r *resumableFake) Name() string         { return "rfake" }
func (r *resumableFake) Caps() Caps           { return Caps{CanSend: true} }
func (r *resumableFake) Wants(string) bool    { return true }
func (r *resumableFake) Send(m Message) error { _, err := r.SendFrom(m, 0); return err }
func (r *resumableFake) SendFrom(m Message, from int) (int, error) {
	r.calls = append(r.calls, from)
	for i := from; i < r.total; i++ {
		if r.failAt == i {
			r.failAt = -1
			return i, fmt.Errorf("boom at %d", i)
		}
		r.posts++
	}
	return r.total, nil
}

// TestDrainResumesWithoutDuplicate: a partial failure persists the delivery cursor,
// so the retry tick posts only the undelivered tail — no sub-message twice (docs/log/37 重複対策).
func TestDrainResumesWithoutDuplicate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Enqueue(Message{Kind: "answer-ready", DisplayName: "A"})
	p := &resumableFake{total: 5, failAt: 3}

	drainWith([]Provider{p}) // posts 0,1,2 then fails at 3 → entry kept, cursor=3
	if n := len(queueFiles(queueDir())); n != 1 {
		t.Fatalf("entry should be kept after partial failure, queue=%d", n)
	}
	drainWith([]Provider{p}) // resumes from 3 → posts 3,4 → done
	if n := len(queueFiles(queueDir())); n != 0 {
		t.Fatalf("entry should be gone after full delivery, queue=%d", n)
	}
	if p.posts != 5 {
		t.Fatalf("posts=%d, want exactly 5 (no duplicate re-post)", p.posts)
	}
	if len(p.calls) != 2 || p.calls[0] != 0 || p.calls[1] != 3 {
		t.Fatalf("resume cursor calls=%v, want [0 3]", p.calls)
	}
}

// TestSessionReportThreadSuppressed: session-report is a no-op in thread mode (the
// completion is already delivered by answer-ready — docs/log/37 Fix ①c), but still posts
// flat / in DM.
func TestSessionReportThreadSuppressed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	// Thread mode: suppressed (no post, no error, cursor unchanged).
	threaded := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42", Threads: true}}
	if n, err := threaded.SendFrom(Message{Kind: "session-report", SessionName: "s1", DisplayName: "P"}, 0); err != nil || n != 0 {
		t.Fatalf("session-report in thread mode: n=%d err=%v, want no-op", n, err)
	}
	if len(*sent) != 0 {
		t.Fatalf("session-report must not post in thread mode, sent=%v", *sent)
	}

	// Flat channel (no threads): still delivered.
	flat := &discordProvider{creds: secrets.DiscordCreds{Token: "tok", ChannelID: "42"}}
	if err := flat.Send(Message{Kind: "session-report", SessionName: "s1", DisplayName: "P"}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("session-report must still post flat, sent=%v", *sent)
	}
}

// TestMirrorUserInput: a Console prompt echoes into an existing session thread with the
// 🧑 marker (docs/log/37 Fix ②); opt-out and a thread-less session post nothing.
func TestMirrorUserInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, sent := fakeDiscord(t, "tok")
	old := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = old })

	save := func(off bool) {
		s := &secrets.Data{Discord: &secrets.DiscordCreds{
			Token: "tok", ChannelID: "42", Threads: true, MirrorInputOff: off}}
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}
	save(false)
	saveThreads(threadMap{"s1": {Channel: "42", Thread: "t9"}})

	MirrorUserInput("s1", "please refactor foo")
	got := *sent
	if len(got) != 1 || got[0]["channel"] != "t9" {
		t.Fatalf("mirror should post once into the thread, got %v", got)
	}
	if !strings.HasPrefix(got[0]["content"], "🧑 ") || !strings.Contains(got[0]["content"], "please refactor foo") {
		t.Fatalf("mirrored content=%q", got[0]["content"])
	}

	// A session with no thread yet: nothing posted (an echo never creates a thread).
	MirrorUserInput("no-thread", "hello")
	if len(*sent) != 1 {
		t.Fatalf("thread-less session must not post, sent=%v", *sent)
	}
	// Opt-out: nothing posted.
	save(true)
	MirrorUserInput("s1", "should be skipped")
	if len(*sent) != 1 {
		t.Fatalf("opt-out must not post, sent=%v", *sent)
	}
}

func TestTextNeverEmptyAndCarriesDisplay(t *testing.T) {
	os.Unsetenv("AF_CP_BASE_URL")
	m := Message{Kind: "permission-request", DisplayName: "秘密の花園", SessionKind: "codex"}
	txt := m.Text("")
	if !strings.Contains(txt, "許可待ち") || !strings.Contains(txt, "秘密の花園") || !strings.Contains(txt, "Codex") {
		t.Fatalf("text=%q", txt)
	}
	if strings.Contains(txt, "http") {
		t.Fatalf("no base URL configured — text must carry no link: %q", txt)
	}
}

// The concise bilingual format + the pane deep link (?session= — consumed by
// the Console's consumeSessionDeepLink).
func TestTextEnglishAndDeepLink(t *testing.T) {
	t.Setenv("AF_CP_BASE_URL", "https://cp.example/")
	m := Message{Kind: "answer-ready", SessionName: "sabc123", DisplayName: "Proj", SessionKind: "claude"}
	en := m.Text("en")
	if !strings.Contains(en, "awaiting your input") || !strings.Contains(en, "(Claude Code)") ||
		!strings.Contains(en, "<https://cp.example/?session=sabc123>") {
		t.Fatalf("en text=%q", en)
	}
	ja := m.Text("")
	if !strings.Contains(ja, "入力待ち") || !strings.Contains(ja, "（Claude Code）") ||
		!strings.Contains(ja, "<https://cp.example/?session=sabc123>") {
		t.Fatalf("ja text=%q", ja)
	}
	if strings.Contains(ja, "agent-fleet】") {
		t.Fatalf("prefix must be gone: %q", ja)
	}
}

// TestMentionTimeGate: action/abnormal events always mention; a read-only event
// (answer-ready) mentions only when its thread has been quiet past the window (docs/log/37).
func TestMentionTimeGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := mentionQuietWindow
	mentionQuietWindow = time.Hour // well above RFC3339's second granularity
	t.Cleanup(func() { mentionQuietWindow = old })

	d := &discordProvider{creds: secrets.DiscordCreds{
		Token: "tok", ChannelID: "C1", MentionUserID: "U1", Threads: true}}

	// Action/abnormal kinds always ping, even with a fresh thread post.
	saveThreads(threadMap{"s1": {Channel: "C1", Thread: "T1",
		LastPostAt: time.Now().UTC().Format(time.RFC3339)}})
	for _, k := range []string{"question", "plan-approval", "permission-request", "exit"} {
		if !d.shouldMention(Message{Kind: k, SessionName: "s1"}) {
			t.Fatalf("%s must always mention", k)
		}
	}

	// answer-ready: no thread yet → mention (first post seeds it).
	if !d.shouldMention(Message{Kind: "answer-ready", SessionName: "new"}) {
		t.Fatal("first answer-ready (no thread) should mention")
	}
	// Recent post → suppressed.
	if d.shouldMention(Message{Kind: "answer-ready", SessionName: "s1"}) {
		t.Fatal("answer-ready within quiet window should NOT mention")
	}
	// After the window elapses → mention returns.
	saveThreads(threadMap{"s1": {Channel: "C1", Thread: "T1",
		LastPostAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)}})
	if !d.shouldMention(Message{Kind: "answer-ready", SessionName: "s1"}) {
		t.Fatal("answer-ready after quiet window should mention")
	}
}

// TestFullTextBodyOnly: full-text mode posts the scrubbed answer body alone — no
// headline/link preface (docs/log/37 全文整理 2026-07-22).
func TestFullTextBodyOnly(t *testing.T) {
	srv, sent := fakeDiscord(t, "tok")
	oldBase := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() { discordAPIBase = oldBase })

	d := &discordProvider{creds: secrets.DiscordCreds{
		Token: "tok", ChannelID: "42", FullText: true}}
	if err := d.Send(Message{Kind: "answer-ready", DisplayName: "Proj",
		SessionKind: "claude", Body: "Build is green."}); err != nil {
		t.Fatal(err)
	}
	got := (*sent)[0]["content"]
	if got != "Build is green.\n"+bridgeDivider {
		t.Fatalf("full-text content should be body-only + divider, got %q", got)
	}
}
