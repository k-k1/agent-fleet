package tmuxx

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withFakeClock swaps the settle clock for a controllable one and clears the sightings so
// tests never inherit each other's (or a live pane's) state.
func withFakeClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	orig := idleSettleNow
	idleSettleNow = func() time.Time { return now }
	sightMu.Lock()
	sights = map[string]paneSighting{}
	sightMu.Unlock()
	t.Cleanup(func() {
		idleSettleNow = orig
		sightMu.Lock()
		sights = map[string]paneSighting{}
		sightMu.Unlock()
	})
	return &now
}

func TestObserveFrameSettlesOnlyAfterTheWindow(t *testing.T) {
	now := withFakeClock(t)
	if observeFrame("s1", "frame A") {
		t.Fatal("settled on the very first observation - it must err conservative and treat the frame as just changed")
	}
	*now = now.Add(idleSettleWindow - time.Second)
	if observeFrame("s1", "frame A") {
		t.Fatal("settled one second short of the window")
	}
	*now = now.Add(2 * time.Second)
	if !observeFrame("s1", "frame A") {
		t.Fatal("the same frame outlasted the window but did not settle - a stalled session would stick at in progress forever")
	}
}

func TestObserveFrameResetsWhenThePaneRepaints(t *testing.T) {
	now := withFakeClock(t)
	observeFrame("s1", "frame A")
	*now = now.Add(idleSettleWindow - time.Second)
	if observeFrame("s1", "frame B") {
		t.Fatal("settled although the frame changed - a redraw is evidence of life, so the clock must rewind")
	}
	*now = now.Add(idleSettleWindow - time.Second)
	if observeFrame("s1", "frame B") {
		t.Fatal("the rewound window is not in effect")
	}
	*now = now.Add(2 * time.Second)
	if !observeFrame("s1", "frame B") {
		t.Fatal("the new frame filled the window but did not settle")
	}
}

func TestObserveFrameIsPerSession(t *testing.T) {
	now := withFakeClock(t)
	observeFrame("s1", "frame A")
	*now = now.Add(idleSettleWindow / 2)
	observeFrame("s2", "frame A") // same frame, but a different session keeps its own clock
	*now = now.Add(idleSettleWindow/2 + time.Second)
	if !observeFrame("s1", "frame A") {
		t.Error("s1 is past the window")
	}
	if observeFrame("s2", "frame A") {
		t.Error("s2 is still inside its window - clocks leak between sessions")
	}
	ForgetPane("s1")
	if observeFrame("s1", "frame A") {
		t.Error("still settled after ForgetPane - a later session reusing the name inherits its predecessor's clock")
	}
}

// TestStreamingAnswerNeverSettles replays a measured series (testdata/streaming_answer) at
// its real timing and holds that a pane in the middle of drawing the answer body never
// settles.
//
// What it guards is the value of the settle window itself: the longest still period in this
// series is 11.44 seconds, so shrinking idleSettleWindow below that makes this test fail.
// When it fails, the thing to fix is the window, not the test: a shorter window brings back
// the real harm of the badge dropping to waiting for input while the answer still streams
// into the TUI (the stop button disappears, completion notifications fire early, the idle
// verdict is affected).
func TestStreamingAnswerNeverSettles(t *testing.T) {
	frames := streamingFrames(t)
	if len(frames) < 10 {
		t.Fatalf("testdata/streaming_answer is too thin (%d frames)", len(frames))
	}
	now := withFakeClock(t)
	base := *now
	for _, f := range frames {
		// production reads "drawing body text" as waiting - that is what this window exists for.
		if !atIdlePrompt(f.text) {
			t.Fatalf("%s: atIdlePrompt=false - the premise of the measured series changed. Read testdata/streaming_answer/SOURCE.txt and re-record it", f.name)
		}
		*now = base.Add(f.at)
		if observeFrame("streaming", f.text) {
			t.Fatalf("%s (+%s): settled while the answer was still being drawn - idleSettleWindow=%s is too short for this series' longest still period of 11.44s", f.name, f.at, idleSettleWindow)
		}
	}
}

type streamFrame struct {
	name string
	at   time.Duration
	text string
}

func streamingFrames(t *testing.T) []streamFrame {
	t.Helper()
	files, err := filepath.Glob("testdata/streaming_answer/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("testdata/streaming_answer is unreadable: %v", err)
	}
	sort.Strings(files)
	var out []streamFrame
	for _, p := range files {
		name := filepath.Base(p)
		if name == "SOURCE.txt" {
			continue // provenance note; how to re-record is written there
		}
		ms, err := strconv.Atoi(strings.TrimSuffix(name, ".txt"))
		if err != nil {
			t.Fatalf("%s: the file name must be the milliseconds elapsed from the start of the series", name)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, streamFrame{name: name, at: time.Duration(ms) * time.Millisecond, text: string(b)})
	}
	return out
}
