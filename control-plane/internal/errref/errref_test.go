package errref

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

// The reference in the message is the one in the log line, and the log line carries the
// original text on a single line.
func TestLogAndMessageShareRef(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	ref := Log(errors.New("pq: relation \"secret_table\" does not exist\nsecond line"))
	if len(ref) != 8 {
		t.Fatalf("ref %q", ref)
	}
	if msg := Message(ref); strings.Contains(msg, "secret_table") || !strings.Contains(msg, ref) {
		t.Fatalf("message %q", msg)
	}
	out := strings.TrimSpace(buf.String())
	if !strings.Contains(out, "ref="+ref) || !strings.Contains(out, "secret_table") || strings.Contains(out, "\n") {
		t.Fatalf("log %q", out)
	}
	if Log(errors.New("x")) == ref {
		t.Fatal("refs repeat")
	}
}
