package notice

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
