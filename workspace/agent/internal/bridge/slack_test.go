package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// slackPost is one recorded chat.postMessage / chat.update the fake server saw.
type slackPost struct {
	method   string // "chat.postMessage" | "chat.update"
	channel  string
	threadTS string
	text     string
	blocks   []any
}

// slackRecorder collects what the fake server saw.
//
// **なぜ mutex と wait があるか（フレーク実測 2026-08-13）**: 受信側の一部経路は
// わざと非同期で、遅いオペレーターターンを読み取りゴルーチンから外して走らせ、
// 終わってから返信を post する（slack_socket.go の routeSlackOperator）。テストが
// 「ターンが呼ばれた」だけを待って終わると、その post は **次のテストが差し替えた
// slackAPIBase**（パッケージ変数）へ飛び、次のテストの記録に混ざる。実際に
// TestSlackFlatSend が「投稿1件のはずが2件」で落ちた。加えて、サーバー側の append と
// テスト側の読み出しが同時に走るので slice 自体もデータ競合になる。
//
// よって記録は mutex で守り、非同期 post を伴うテストは wait で**実際に届くまで待つ**
// （＝ゴルーチンを次のテストへ持ち越さない）。
type slackRecorder struct {
	mu    sync.Mutex
	posts []slackPost
}

func (r *slackRecorder) add(p slackPost) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, p)
}

// all returns a snapshot copy — 呼び出し後にサーバーが書いても手元は壊れない。
func (r *slackRecorder) all() []slackPost {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]slackPost(nil), r.posts...)
}

func (r *slackRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.posts)
}

func (r *slackRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = nil
}

// wait blocks until at least n posts have arrived, and fails the test if they don't.
// 非同期 post を待ち切ることが目的なので、成功時にはもう書き手がいない。
func (r *slackRecorder) wait(t *testing.T, n int) []slackPost {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := r.all(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d slack post(s), saw %d: %+v", n, r.len(), r.all())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeSlack serves the Web API methods the provider uses and records every post/update. It
// returns incrementing message ts so a thread root is captured. Auth is the bot bearer token.
func fakeSlack(t *testing.T, wantBotToken string) *slackRecorder {
	t.Helper()
	rec := &slackRecorder{}
	tsN := 1000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		auth := r.Header.Get("Authorization")
		ok := func(extra map[string]any) {
			m := map[string]any{"ok": true}
			for k, v := range extra {
				m[k] = v
			}
			_ = json.NewEncoder(w).Encode(m)
		}
		switch method {
		case "auth.test":
			ok(map[string]any{"team": "Acme", "team_id": "T1", "user": "af-bot", "user_id": "UBOT", "url": "https://acme.slack.com"})
		case "apps.connections.open":
			ok(map[string]any{"url": "wss://example.invalid/link"})
		case "users.conversations":
			ok(map[string]any{"channels": []map[string]string{{"id": "C1", "name": "general"}}})
		case "users.lookupByEmail":
			ok(map[string]any{"user": map[string]string{"id": "UME"}})
		case "conversations.open":
			ok(map[string]any{"channel": map[string]string{"id": "D-" + strFrom(body["users"])}})
		case "reactions.add":
			ok(nil)
		case "chat.postMessage", "chat.update":
			if auth != "Bearer "+wantBotToken {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
				return
			}
			blk, _ := body["blocks"].([]any)
			rec.add(slackPost{
				method: method, channel: strFrom(body["channel"]), threadTS: strFrom(body["thread_ts"]),
				text: strFrom(body["text"]), blocks: blk,
			})
			tsN++
			ok(map[string]any{"ts": strconv.Itoa(tsN), "channel": strFrom(body["channel"])})
		default:
			ok(nil)
		}
	}))
	old := slackAPIBase
	slackAPIBase = srv.URL
	t.Cleanup(func() { slackAPIBase = old; srv.Close() })
	return rec
}

func strFrom(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// TestSlackFlatSend: a DM (no thread) posts the headline to the resolved DM channel.
func TestSlackFlatSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	sp := &slackProvider{creds: secrets.SlackCreds{BotToken: "xoxb-tok", UserID: "U9"}}
	if err := sp.Send(Message{Kind: "answer-ready", SessionName: "s1", DisplayName: "Sess"}); err != nil {
		t.Fatal(err)
	}
	if posts.len() != 1 || posts.all()[0].channel != "D-U9" || posts.all()[0].threadTS != "" {
		t.Fatalf("flat DM post wrong: %+v", posts.all())
	}
	if !strings.Contains(posts.all()[0].text, "Sess") {
		t.Fatalf("headline missing display name: %q", posts.all()[0].text)
	}
}

// TestSlackThreadedSendAndResume: thread mode seeds a root then posts replies into it; the
// store persists the root ts; a resume from the delivered cursor posts nothing new.
func TestSlackThreadedSendAndResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	sp := &slackProvider{creds: secrets.SlackCreds{BotToken: "xoxb-tok", ChannelID: "C1", UserID: "U9", Threads: true, FullText: true}}
	// A long body → multiple chunks → a seed root + threaded replies.
	long := strings.Repeat("あ", slackContentLimit) + strings.Repeat("い", 200)
	m := Message{Kind: "answer-ready", SessionName: "s1", DisplayName: "Sess", Body: long}
	delivered, err := sp.SendFrom(m, 0)
	if err != nil {
		t.Fatal(err)
	}
	if delivered < 2 {
		t.Fatalf("want a seed + at least one threaded reply, delivered=%d", delivered)
	}
	if posts.all()[0].threadTS != "" {
		t.Fatalf("seed must be top-level, got thread_ts=%q", posts.all()[0].threadTS)
	}
	root := "1001" // first ts returned by the fake
	for _, p := range posts.all()[1:] {
		if p.threadTS != root {
			t.Fatalf("reply not threaded to root %s: %+v", root, p)
		}
	}
	if ref, ok := slackThreads.load()["s1"]; !ok || ref.Thread != root || ref.Channel != "C1" {
		t.Fatalf("thread store not persisted: %+v ok=%v", ref, ok)
	}
	before := posts.len()
	// Resume from the full delivered count — nothing new should post.
	if d2, err := sp.SendFrom(m, delivered); err != nil || d2 != delivered {
		t.Fatalf("resume changed count: d2=%d err=%v", d2, err)
	}
	if posts.len() != before {
		t.Fatalf("resume duplicated posts: before=%d after=%d", before, posts.len())
	}
}

// TestSlackSessionReportSuppressedInThread: in thread mode a session-report is a no-op success
// (answer-ready already delivered the completion to the thread).
func TestSlackSessionReportSuppressedInThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	sp := &slackProvider{creds: secrets.SlackCreds{BotToken: "xoxb-tok", ChannelID: "C1", UserID: "U9", Threads: true}}
	if _, err := sp.SendFrom(Message{Kind: "session-report", SessionName: "s1", DisplayName: "S"}, 0); err != nil {
		t.Fatal(err)
	}
	if posts.len() != 0 {
		t.Fatalf("session-report must be suppressed in thread mode, posts=%+v", posts.all())
	}
}

// TestSlackButtonsRendered: an interactive connection appends Block Kit buttons carrying the
// same custom_id scheme for permission and question events.
func TestSlackButtonsRendered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	sp := &slackProvider{creds: secrets.SlackCreds{BotToken: "xoxb-tok", ChannelID: "C1", UserID: "U9", Receive: true}}
	if err := sp.Send(Message{Kind: "permission-request", SessionName: "s7"}); err != nil {
		t.Fatal(err)
	}
	var cids []string
	for _, p := range posts.all() {
		cids = append(cids, slackButtonCustomIDs(p.blocks)...)
	}
	foundAllow := false
	for _, c := range cids {
		if pi, ok := ParseCustomID(c); ok && pi.Kind == "p" && pi.Session == "s7" && pi.Choice == "allow" {
			foundAllow = true
		}
	}
	if !foundAllow {
		t.Fatalf("permission allow button not rendered; cids=%v", cids)
	}
}

// TestSlackMirrorInputGated: mirror echoes into an existing thread when on, nothing when opted
// out.
func TestSlackMirrorInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	posts := fakeSlack(t, "xoxb-tok")
	s, _ := secrets.Load()
	s.Slack = &secrets.SlackCreds{BotToken: "xoxb-tok", ChannelID: "C1", UserID: "U9", Threads: true}
	_ = s.Save()
	slackThreads.save(threadMap{"s1": {Channel: "C1", Thread: "1001"}})
	mirrorSlackInput("s1", "手動入力")
	if posts.len() == 0 || posts.all()[0].threadTS != "1001" || !strings.Contains(posts.all()[0].text, "手動入力") {
		t.Fatalf("mirror should echo into the thread: %+v", posts.all())
	}
	// Opt out → no echo.
	posts.reset()
	s.Slack.MirrorInputOff = true
	_ = s.Save()
	mirrorSlackInput("s1", "また入力")
	if posts.len() != 0 {
		t.Fatalf("opted-out mirror must not post: %+v", posts.all())
	}
}

// TestRenderBodyForSlack: secrets scrubbed, GFM heading/bold → mrkdwn, a table wrapped in a
// code fence.
func TestRenderBodyForSlack(t *testing.T) {
	body := "## 見出し\n**太字** と xoxb-123456789012-abcdefghijklmnopqrstuvwx\n\n| A | B |\n|---|---|\n| 1 | 2 |"
	out := renderBodyForSlack(body)
	if strings.Contains(out, "xoxb-123456789012-abcdefghijklmnopqrstuvwx") {
		t.Fatalf("secret leaked: %q", out)
	}
	if strings.Contains(out, "**太字**") || strings.Contains(out, "## 見出し") {
		t.Fatalf("GFM not converted to mrkdwn: %q", out)
	}
	if !strings.Contains(out, "*見出し*") || !strings.Contains(out, "*太字*") {
		t.Fatalf("expected mrkdwn bold: %q", out)
	}
	if !strings.Contains(out, "```") {
		t.Fatalf("table should be fenced: %q", out)
	}
}

// slackButtonCustomIDs walks Block Kit actions blocks and returns each button's value (the
// custom_id).
func slackButtonCustomIDs(blocks []any) []string {
	var out []string
	for _, b := range blocks {
		blk, _ := b.(map[string]any)
		if blk["type"] != "actions" {
			continue
		}
		els, _ := blk["elements"].([]any)
		for _, e := range els {
			el, _ := e.(map[string]any)
			if v, ok := el["value"].(string); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// TestSlackMentionTimeGate: a read-only event skips the mention inside the quiet window and
// re-adds it after a lull; an action event always mentions.
func TestSlackMentionTimeGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sp := &slackProvider{creds: secrets.SlackCreds{BotToken: "x", ChannelID: "C1", UserID: "U9", Threads: true}}
	slackThreads.save(threadMap{"s1": {Channel: "C1", Thread: "1001", LastPostAt: time.Now().UTC().Format(time.RFC3339)}})
	if sp.shouldMention(Message{Kind: "answer-ready", SessionName: "s1"}) {
		t.Fatal("read-only event within the quiet window must not mention")
	}
	if !sp.shouldMention(Message{Kind: "question", SessionName: "s1"}) {
		t.Fatal("an action event must always mention")
	}
	slackThreads.save(threadMap{"s1": {Channel: "C1", Thread: "1001", LastPostAt: time.Now().Add(-2 * mentionQuietWindow).UTC().Format(time.RFC3339)}})
	if !sp.shouldMention(Message{Kind: "answer-ready", SessionName: "s1"}) {
		t.Fatal("after a lull the read-only event must mention again")
	}
}
