package memconsol

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// capturedLog returns what the Agent log gets while the test runs.
func capturedLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// A dropped invalid answer says so, in the run record and the log, without the model's wording.
func TestDroppedInvalidAnswerKeepsAReason(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	lg := capturedLog(t)
	small := &scriptModel{classify: func(string) (string, error) {
		return `{"operation":"patch","targets":["not-shown-SECRETISH"],"reason":"r"}`, nil
	}}
	rep := mustRun(t, input(small, nil, t0))
	if rep.Dropped != 1 || len(rep.DroppedReasons) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	d := rep.DroppedReasons[0]
	if d.Observation == "" || d.Reason != dropReasonClassify {
		t.Fatalf("reason = %+v", d)
	}
	if !strings.Contains(lg.String(), d.Observation) || !strings.Contains(lg.String(), dropReasonClassify) {
		t.Errorf("log = %q", lg.String())
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b)+lg.String(), "SECRETISH") {
		t.Error("the model's text reached the record or the log")
	}
	if last, _ := LastReport(); len(last.DroppedReasons) != 1 {
		t.Errorf("the stored run record lost the reasons: %+v", last)
	}
}

// An exported memory the checks refuse names the check, and nothing of its text is copied into the
// record or the log.
func TestRefusedExportedMemoryKeepsAReasonWithoutItsText(t *testing.T) {
	f := env(t)
	f.put("project", testProject, memoryx.ConsolMemory{Name: "anchor", Description: "d", Type: "project", Revision: 1, Body: "b"})
	f.forgotten["project:"+testProject+"/old-lesson"] = true
	writeExport(t, exported(1, "create", "old-lesson", "lesson", "revival body text", t0.Add(-48*time.Hour)))
	lg := capturedLog(t)
	rep := mustRun(t, input(&scriptModel{classify: classifyAs("add")}, nil, t0))
	if len(rep.DroppedReasons) != 1 || !strings.Contains(rep.DroppedReasons[0].Reason, "no revival") {
		t.Fatalf("report = %+v", rep)
	}
	b, _ := json.Marshal(rep)
	if all := string(b) + lg.String(); strings.Contains(all, "revival body text") || strings.Contains(all, "old-lesson") {
		t.Error("memory text reached the record or the log")
	}
}

// A patch whose replacement text carries a secret is refused as a secret; the secret itself is
// in neither the record nor the log.
func TestRefusedPatchSecretKeepsAReasonWithoutTheSecret(t *testing.T) {
	secret := "AKIA" + "ABCDEFGHIJKLMNOP"
	f := env(t)
	f.wrong("a", "line one", 1)
	lg := capturedLog(t)
	small := &scriptModel{classify: classifyAs("patch", "a"), patch: patchEdit("line one", "key "+secret)}
	rep := mustRun(t, input(small, nil, t0))
	if len(rep.DroppedReasons) != 1 || !strings.Contains(rep.DroppedReasons[0].Reason, "secret scan") {
		t.Fatalf("report = %+v", rep)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b)+lg.String(), secret) {
		t.Error("the secret reached the record or the log")
	}
}

// The record is bounded: the rest are counted, and no reason outgrows its cap.
func TestDroppedReasonsAreCapped(t *testing.T) {
	w := &worker{rep: &Report{}}
	for i := 0; i < maxDroppedReasons+5; i++ {
		w.noteDrop(Observation{ID: "o"}, strings.Repeat("x", 3*maxDroppedReasonLen))
	}
	if len(w.rep.DroppedReasons) != maxDroppedReasons || w.rep.DroppedReasonsMore != 5 {
		t.Fatalf("kept %d, more %d", len(w.rep.DroppedReasons), w.rep.DroppedReasonsMore)
	}
	if n := len([]rune(w.rep.DroppedReasons[0].Reason)); n != maxDroppedReasonLen {
		t.Fatalf("reason length = %d", n)
	}
}
