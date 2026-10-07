package status

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestRaceChildHelper is the "Stop hook" process of TestPersistIfKeepsHookClosedTurn: for each
// line on stdin it persists a closed turn, as `workspace-agent session-status idle` does.
func TestRaceChildHelper(t *testing.T) {
	sid := os.Getenv("AF_STATUS_RACE_SID")
	if sid == "" {
		t.Skip("helper process only")
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		PersistTurnEndFor(sid, "idle", "", "p1")
		fmt.Println("done")
	}
}

// The reverse-heal's write must not overwrite a turn another PROCESS closed after the heal
// decided. Each round: the heal has read the working record (its Rev), the hook process then
// closes the turn, and the heal's write lands immediately after. A blind write loses the
// closed turn in the rounds where the hook wins; PersistIf must lose none.
func TestPersistIfKeepsHookClosedTurn(t *testing.T) {
	sid := "race-sid"
	cmd := exec.Command(os.Args[0], "-test.run=^TestRaceChildHelper$")
	cmd.Env = append(os.Environ(), "AF_STATUS_RACE_SID="+sid)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	out := bufio.NewScanner(stdout)

	const rounds = 150
	lost, healed := 0, 0
	for i := 0; i < rounds; i++ {
		Persist(sid, "working")
		st, ok := Read(sid)
		if !ok {
			t.Fatal("no record after Persist")
		}
		fmt.Fprintln(stdin, "go")
		// Alternate who lands first: the hook usually wins a race against a heal that is
		// still deciding, and the pipe round trip alone would make the heal always first.
		if i%2 == 1 {
			if !out.Scan() {
				t.Fatalf("helper died: %v", out.Err())
			}
		}
		if PersistIf(sid, "working", st.Rev, true) {
			healed++
		}
		if i%2 == 0 && !out.Scan() {
			t.Fatalf("helper died: %v", out.Err())
		}
		got, _ := Read(sid)
		if got.State != "idle" || !got.TurnEnd || got.PromptID != "p1" {
			lost++
		}
		if _, ok := ReadCompletionKey(sid); !ok {
			lost++
		}
		RemoveCompletionKey(sid)
	}
	t.Logf("rounds=%d heal-won-first=%d lost=%d", rounds, healed, lost)
	if lost != 0 {
		t.Fatalf("%d of %d closed turns lost to the heal", lost, rounds)
	}
}

func TestPersistIfRequiresTheDecidedRecord(t *testing.T) {
	sid := "cas-sid"
	if !PersistIf(sid, "working", "", false) {
		t.Fatal("no record decided from, none present: want write")
	}
	if PersistIf(sid, "working", "", false) {
		t.Fatal("a record appeared since the decision: want skip")
	}
	st, _ := Read(sid)
	if PersistIf(sid, "working", st.Rev+"x", true) {
		t.Fatal("stale Rev: want skip")
	}
	if !PersistIf(sid, "working", st.Rev, true) {
		t.Fatal("matching Rev: want write")
	}
}

func TestPersistIfSkipsOnLockTimeoutButHookWrites(t *testing.T) {
	defer func(d time.Duration) { lockWait = d }(lockWait)
	lockWait = 30 * time.Millisecond
	sid := "held-sid"
	Persist(sid, "working")
	st, _ := Read(sid)
	unlock, ok := lockSid(sid)
	if !ok {
		t.Fatal("lock")
	}
	if PersistIf(sid, "working", st.Rev, true) {
		t.Fatal("heal wrote while the lock was held")
	}
	PersistTurnEndFor(sid, "idle", "", "p1") // the hook still writes
	unlock()
	if got, _ := Read(sid); !got.TurnEnd {
		t.Fatal("hook write dropped on lock timeout")
	}
}

func TestRemoveDeletesLockFile(t *testing.T) {
	sid := "rm-sid"
	Persist(sid, "working")
	if _, err := os.Stat(lockPath(sid)); err != nil {
		t.Fatal(err)
	}
	Remove(sid)
	if _, err := os.Stat(lockPath(sid)); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: %v", err)
	}
}
