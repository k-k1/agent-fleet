package opencode

import (
	"sync"
	"testing"
	"time"
)

// lateLog is a status log whose lines appear after a delay, like the plugin's late idle report.
type lateLog struct {
	mu   sync.Mutex
	text string
}

func (l *lateLog) read() string { l.mu.Lock(); defer l.mu.Unlock(); return l.text }
func (l *lateLog) appendAfter(d time.Duration, s string) {
	go func() {
		time.Sleep(d)
		l.mu.Lock()
		l.text += s
		l.mu.Unlock()
	}()
}

func TestAwaitStatusReports(t *testing.T) {
	const sid = "sid-t3"
	cases := []struct {
		name     string
		initial  string
		lateLine string        // appended after delay; "" for never
		delay    time.Duration // 0: no late line
		want     []string
		wantMiss []string
	}{
		{"already there", "session-status working sid-t3\nsession-status idle sid-t3\n", "", 0, []string{"working", "idle"}, nil},
		{"idle arrives late", "session-status working sid-t3\n", "session-status idle sid-t3\n", 150 * time.Millisecond, []string{"working", "idle"}, nil},
		{"idle never arrives", "session-status working sid-t3\n", "", 0, []string{"working", "idle"}, []string{"idle"}},
		{"idle under another sid", "session-status working sid-t3\n", "session-status idle sid-t1\n", 50 * time.Millisecond, []string{"working", "idle"}, []string{"idle"}},
		{"sid that merely has the prefix", "session-status working sid-t3\nsession-status idle sid-t30\n", "", 0, []string{"working", "idle"}, []string{"idle"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &lateLog{text: tc.initial}
			if tc.lateLine != "" {
				l.appendAfter(tc.delay, tc.lateLine)
			}
			miss, last := awaitStatusReports(l.read, sid, tc.want, 600*time.Millisecond, 10*time.Millisecond)
			if len(miss) != len(tc.wantMiss) || (len(miss) > 0 && miss[0] != tc.wantMiss[0]) {
				t.Fatalf("missing = %v, want %v (log:\n%s)", miss, tc.wantMiss, last)
			}
			if len(tc.wantMiss) > 0 && last == "" {
				t.Fatalf("no last log returned for the failure message")
			}
		})
	}
}

func TestPollUntil(t *testing.T) {
	n := 0
	if !pollUntil(time.Second, time.Millisecond, func() bool { n++; return n >= 3 }) {
		t.Fatal("condition that turns true was reported false")
	}
	if pollUntil(50*time.Millisecond, 5*time.Millisecond, func() bool { return false }) {
		t.Fatal("never-true condition was reported true")
	}
}
