package notice

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestOutboxPersistsListsAndAcknowledges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("question", "s1", "claude", "Project")
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	// docs/log/37 contract 4: Put fans the event out into the chat-bridge queue too.
	bq, _ := os.ReadDir(filepath.Join(home, ".local", "state", "agent-fleet", "bridge-queue"))
	if len(bq) != 1 {
		t.Fatalf("bridge queue entries=%d, want 1", len(bq))
	}
	got := List()
	if len(got) != 1 || got[0].ID != e.ID || got[0].SessionName != "s1" {
		t.Fatalf("events=%+v", got)
	}
	Ack([]string{e.ID, "../unsafe"})
	if got := List(); len(got) != 0 {
		t.Fatalf("events after ack=%+v", got)
	}
}

// Full-text bridge (docs/log/37): the answer-ready event's body payload rides into the
// bridge queue entry so a full-text-mode provider can render it.
func TestPutCarriesBodyIntoBridgeQueue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("answer-ready", "s1", "claude", "Project")
	e.Payload["body"] = "final turn prose"
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".local", "state", "agent-fleet", "bridge-queue")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("bridge queue entries=%d, want 1", len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var q struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	if q.Kind != "answer-ready" || q.Body != "final turn prose" {
		t.Fatalf("queued entry=%+v, want body carried", q)
	}
}

// The permission token rides the permission-request event into the bridge queue, where the
// Allow/Deny buttons take it (#3358).
func TestPutCarriesPermissionTokenIntoBridgeQueue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("permission-request", "s1", "claude", "Project")
	e.Payload["permissionToken"] = "1abc"
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".local", "state", "agent-fleet", "bridge-queue")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("bridge queue entries=%d, want 1", len(entries))
	}
	b, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	var q struct {
		PermissionToken string `json:"permissionToken"`
	}
	if err := json.Unmarshal(b, &q); err != nil || q.PermissionToken != "1abc" {
		t.Fatalf("queued entry=%s err=%v, want the token carried", b, err)
	}
}

// Two processes racing on one key (claude runs hooks in parallel) must deliver once:
// a stat-then-write marker lets both through.
func TestPutOnceConcurrentSameKeyDeliversOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = PutOnce("race:k", New("terminal-notification", "s1", "claude", "P"))
		}()
	}
	close(start)
	wg.Wait()
	if got := List(); len(got) != 1 {
		t.Fatalf("%d events for one key, want 1", len(got))
	}
}

// A Put that fails must not leave the key claimed, or the event is lost for good.
func TestPutOnceFailedPutReleasesKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outbox := dir()
	if err := os.MkdirAll(filepath.Dir(outbox), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outbox, nil, 0o600); err != nil { // a file where the dir goes
		t.Fatal(err)
	}
	if err := PutOnce("retry:k", New("question", "s1", "claude", "P")); err == nil {
		t.Fatal("PutOnce succeeded with the outbox blocked")
	}
	if err := os.Remove(outbox); err != nil {
		t.Fatal(err)
	}
	if err := PutOnce("retry:k", New("question", "s1", "claude", "P")); err != nil {
		t.Fatal(err)
	}
	if got := List(); len(got) != 1 {
		t.Fatalf("%d events after the retry, want 1", len(got))
	}
}

// A process killed inside PutOnce (SIGKILL, OOM, a Workspace stop) after it got past
// the marker check but before the event reached the outbox must not suppress the
// event: the next call for the key has to deliver it.
func TestPutOnceKilledMidwayDoesNotSuppressKey(t *testing.T) {
	if os.Getenv("NOTICE_PUTONCE_HELPER") == "1" {
		beforePutOnce = func() {
			os.Stdout.WriteString("inside\n")
			time.Sleep(time.Minute)
		}
		_ = PutOnce("killed:k", New("answer-ready", "s1", "claude", "P"))
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPutOnceKilledMidwayDoesNotSuppressKey$")
	cmd.Env = append(os.Environ(), "NOTICE_PUTONCE_HELPER=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "inside\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper never reached the Put: %q %v", line, err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	done := make(chan error, 1)
	go func() { done <- PutOnce("killed:k", New("answer-ready", "s1", "claude", "P")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PutOnce blocked on the dead process's lock")
	}
	if got := List(); len(got) != 1 {
		t.Fatalf("%d events after the killed attempt, want 1", len(got))
	}
}

// An oversized payload is shrunk in the outbox copy: short identifiers survive, long strings
// are cut on a rune boundary, a long question list keeps each question's text (Web Push reads
// it), and the entry is marked even when the caller set the mark itself. The chat bridge still
// gets the event as given.
func TestPutBoundsTheOutboxPayload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("question", "s1", "claude", "Project")
	e.Payload["conversation_id"] = "c1"
	e.Payload["count"] = 3
	e.Payload[payloadTruncatedKey] = false
	e.Payload["body"] = strings.Repeat("あ", maxPayloadValue) // 3 bytes per rune
	preview := strings.Repeat("p", maxPayloadBytes)
	e.Payload["questions"] = json.RawMessage(`[{"question":"Which way?","header":"Way","options":[{"label":"A","preview":"` + preview + `"}]}]`)
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".local", "state", "agent-fleet", "notification-outbox", e.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxPayloadBytes+1024 {
		t.Fatalf("outbox entry is %d bytes, want about %d at most", len(b), maxPayloadBytes)
	}
	got := List()
	if len(got) != 1 {
		t.Fatalf("events=%d, want 1", len(got))
	}
	p := got[0].Payload
	body, _ := p["body"].(string)
	if p[payloadTruncatedKey] != true || p["conversation_id"] != "c1" || p["count"] != float64(3) ||
		len(body) > maxPayloadValue || !utf8.ValidString(body) || body == "" {
		t.Fatalf("bounded payload: truncated=%v conversation_id=%v count=%v body=%d bytes valid=%v",
			p[payloadTruncatedKey], p["conversation_id"], p["count"], len(body), utf8.ValidString(body))
	}
	qb, _ := json.Marshal(p["questions"])
	if string(qb) != `[{"header":"Way","question":"Which way?"}]` {
		t.Fatalf("questions = %s, want the question text and header only", qb)
	}
	bq, _ := os.ReadDir(filepath.Join(home, ".local", "state", "agent-fleet", "bridge-queue"))
	if len(bq) != 1 {
		t.Fatalf("bridge queue entries=%d, want 1", len(bq))
	}
	bb, _ := os.ReadFile(filepath.Join(home, ".local", "state", "agent-fleet", "bridge-queue", bq[0].Name()))
	if !strings.Contains(string(bb), preview) {
		t.Fatalf("bridge queue entry lost the full questions")
	}
}

// A payload within the bound is stored unchanged, without the mark.
func TestPutKeepsAPayloadWithinTheBound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := New("answer-ready", "s1", "claude", "Project")
	e.Payload["body"] = strings.Repeat("x", maxPayloadBytes-64)
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	got := List()
	if len(got) != 1 || got[0].Payload["body"] != e.Payload["body"] || got[0].Payload[payloadTruncatedKey] != nil {
		t.Fatalf("payload within the bound was changed")
	}
}

// What List does with each shape an outbox file's payload can have. An object an older Agent
// wrote past the bound is listed shrunk, and so can be acked, instead of staying queued ahead of
// the entries after it. A missing or null payload lists as before. A payload that is not an
// object, at any size, is skipped as before (it never decoded into the map), so it is never
// acked and removed.
func TestListBoundsEachStoredPayloadShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := filepath.Join(home, ".local", "state", "agent-fleet", "notification-outbox")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	long := strings.Repeat("q", 2*maxPayloadBytes)
	files := map[string]string{
		"big":         `{"questions":[{"question":"Q?","options":"` + long + `"}],"key":"k"}`,
		"bare":        ``,
		"null":        `null`,
		"small":       `{"a":1}`,
		"string":      `"` + long + `"`,
		"array":       `[1,2]`,
		"smallstring": `"x"`,
	}
	for id, payload := range files {
		body := `{"id":"` + id + `","kind":"question","sessionName":"s","createdAt":"` + now + `"`
		if payload != "" {
			body += `,"payload":` + payload
		}
		if err := os.WriteFile(filepath.Join(d, id+".json"), []byte(body+"}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "broken.json"), []byte(`{"id":"broken","payload":{`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := map[string]Event{}
	for _, e := range List() {
		got[e.ID] = e
	}
	ids := make([]string, 0, len(got))
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "bare,big,null,small" {
		t.Fatalf("listed %v, want bare,big,null,small", ids)
	}
	if b, _ := json.Marshal(got["big"].Payload); string(b) != `{"key":"k","payloadTruncated":true,"questions":[{"question":"Q?"}]}` {
		t.Fatalf("oversized stored payload listed as %s", b)
	}
	if got["bare"].Payload != nil || got["null"].Payload != nil || got["small"].Payload["a"] != float64(1) {
		t.Fatalf("bare=%v null=%v small=%v", got["bare"].Payload, got["null"].Payload, got["small"].Payload)
	}
}

// The bound holds on the file Put writes, not on an earlier encoding: invalid UTF-8 inside a
// json.RawMessage value grows threefold when repaired, and is repaired before the payload is
// measured and shrunk.
func TestPutBoundsThePayloadOnDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := New("answer-ready", "s1", "claude", "Project")
	e.Payload["body"] = strings.Repeat("x", maxPayloadBytes)
	for i := 0; i < 8; i++ {
		e.Payload[fmt.Sprintf("raw%d", i)] = json.RawMessage(`"` + strings.Repeat("\xff", 6000) + `"`)
	}
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".local", "state", "agent-fleet", "notification-outbox", e.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Payload) > maxPayloadBytes || !utf8.Valid(f.Payload) {
		t.Fatalf("payload on disk is %d bytes (bound %d), valid UTF-8 %v", len(f.Payload), maxPayloadBytes, utf8.Valid(f.Payload))
	}
	if got := List(); len(got) != 1 || got[0].Payload[payloadTruncatedKey] != true {
		t.Fatalf("listed %d events", len(got))
	}
}

// The Agent's copy of the shrink rule (control-plane/notification.go has the same): a mistyped
// header costs only the header, and fitString keeps the longest head that fits.
func TestShrinkRuleMatchesTheControlPlane(t *testing.T) {
	got := questionHeads([]byte(`[{"question":"Which way?","header":7,"options":[]},{"question":3,"header":"H"},{},{},5]`), maxPayloadValue)
	if string(got) != `[{"question":"Which way?"},{"question":"","header":"H"},{"question":""},{"question":""}]` {
		t.Fatalf("questionHeads = %s", got)
	}
	head := strings.Repeat("😀", 160)
	if b := fitString(head+strings.Repeat("<", 1216), 1856); len(b) > 1856 || len(b) < 1850 || !strings.HasPrefix(string(b), `"`+head) {
		t.Fatalf("fitString kept %d bytes", len(b))
	}
}
