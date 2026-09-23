package imagegen

// Negative controls for the first review round of ADR 0100 P0 lane A (docs/log/114 §5).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// params is merged one level down: the agent's cfg does not erase the member's steps and
// sampler, and the agent may not set a knob outside decision 4's four.
func TestStudioParamsMergeByKnob(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"model":"sdxl-base","params":{"steps":30,"cfg":7,"sampler":"euler"}}`)
	bindForTest(t, s.ID, "s1")
	_, res := putStudio(t, s.ID, `{"author":"agent","session":"s1","draft":{"params":{"cfg":5,"clip_skip":2,"weight":3}}}`)
	p := res.Studio.Draft.Params
	if p == nil || p.Steps != 30 || p.Sampler != "euler" || p.CFG != 5 || p.ClipSkip != 0 {
		t.Fatalf("params = %+v, want steps and sampler kept, cfg 5, no clip_skip", p)
	}
	if droppedReason(res, "params.clip_skip") != dropHumanOnly || droppedReason(res, "params.weight") != dropInvalid {
		t.Errorf("dropped = %+v", res.Dropped)
	}
	last := readStudioLog(s.ID)
	if c := last[len(last)-1].Changes; len(c) != 1 || c[0].Field != "params.cfg" || string(c[0].Before) != "7" {
		t.Errorf("changes = %+v, want one params.cfg change", c)
	}
	// A null knob clears that knob alone; clearing every knob clears params.
	_, res = putStudio(t, s.ID, `{"author":"human","draft":{"params":{"sampler":null,"clip_skip":2}}}`)
	if p := res.Studio.Draft.Params; p == nil || p.Sampler != "" || p.Steps != 30 || p.ClipSkip != 2 {
		t.Errorf("after the member's patch params = %+v", p)
	}
	_, res = putStudio(t, s.ID, `{"author":"human","draft":{"params":{"steps":null,"cfg":null,"clip_skip":null}}}`)
	if res.Studio.Draft.Params != nil {
		t.Errorf("params = %+v, want cleared", res.Studio.Draft.Params)
	}
}

// The view's fitting always ends and always fits, whatever is large — and news it drops is
// counted, never silently gone.
func TestAgentViewFittingEnds(t *testing.T) {
	long := strings.Repeat("x", 20000)
	v := studioAgentView{Studio: "s", Title: long, Draft: ImageStudioDraft{Prompt: long, NegativePrompt: long, Label: long, OutDir: long}}
	for range 5 {
		v.Since.Items = append(v.Since.Items, studioNews{Kind: "failed", Error: long})
	}
	done := make(chan []byte, 1)
	go func() { done <- fitStudioAgentView(&v) }()
	select {
	case b := <-done:
		if len(b) > studioAgentViewMax || !strings.Contains(string(b), `"truncated":true`) {
			t.Fatalf("fitted view is %d bytes: %.200s", len(b), b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fitting the view did not end")
	}

	w := studioAgentView{Studio: "s", Draft: ImageStudioDraft{Prompt: strings.Repeat("y", 7000)}}
	for range 5 {
		w.Since.Items = append(w.Since.Items, studioNews{Kind: "edit", Changes: []string{strings.Repeat("z", 500)}})
	}
	w.Since.More = 2
	b := fitStudioAgentView(&w)
	var got studioAgentView
	_ = json.Unmarshal(b, &got)
	if len(b) > studioAgentViewMax || got.Since.Items == nil || len(got.Since.Items)+got.Since.More != 7 {
		t.Errorf("since after fitting = %d items + %d more, want 7 in all (%d bytes)", len(got.Since.Items), got.Since.More, len(b))
	}
}

func TestStudioShortTextsAreBounded(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{}`)
	long := strings.Repeat("l", studioShortTextMax+1)
	_, res := putStudio(t, s.ID, `{"author":"human","title":"`+long+`","draft":{"label":"`+long+`","out_dir":"`+long+`"}}`)
	for _, f := range []string{"title", "label", "out_dir"} {
		if droppedReason(res, f) != dropInvalid {
			t.Errorf("%s over the limit: dropped %q", f, droppedReason(res, f))
		}
	}
}

func TestAgentTrialRefusesWithoutAProvider(t *testing.T) {
	withStudios(t)
	withStudioProvider(t)
	s := createStudio(t, `{"model":"sdxl-base","prompt":"x"}`)
	bindForTest(t, s.ID, "s1")
	if code, _, body := press(t, s.ID, `{"mode":"agent_trial","session":"s1"}`); code != http.StatusBadRequest || !strings.Contains(body, "no_provider") {
		t.Fatalf("agent trial with no provider = %d %s, want no_provider rather than the first ready one", code, body)
	}
}

func TestStudioRefusesSessionsThatCannotTellWhoTheyAre(t *testing.T) {
	withStudios(t)
	for _, c := range []struct {
		kind, driver string
		refused      bool
	}{
		{session.KindOpencode, session.DriverManaged, true},
		{session.KindCopilot, session.DriverManaged, true},
		{session.KindMuse, session.DriverManaged, true},
		{session.KindOpencode, "", false},
		{session.KindCodex, session.DriverManaged, false},
		{session.KindClaude, "", false},
	} {
		if got := StudioSessionUnsupported(c.kind, c.driver) != ""; got != c.refused {
			t.Errorf("%s/%s refused = %v, want %v", c.kind, c.driver, got, c.refused)
		}
	}
	s := createStudio(t, `{}`)
	session.WriteMeta(session.Meta{Name: "oc1", Kind: session.KindOpencode, Driver: session.DriverManaged})
	rec := studioDo(t, HandleStudioBind, http.MethodPost, "/imagegen/studios/"+s.ID+"/bind", `{"session":"oc1"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "studio_kind_unsupported") {
		t.Fatalf("binding opencode Managed = %d %s", rec.Code, rec.Body)
	}
}

// A missing index is rebuilt before the first append, so the pictures before it stay listed;
// a rebuild is a new generation, and a cursor or read position from the old one reads nothing.
func TestHistoryAppendRebuildsAMissingIndex(t *testing.T) {
	withStudios(t)
	for i := range 2 {
		img := filepath.Join(ConsoleDir(), "old-"+itoa(i)+".png")
		writeFile(t, img, tinyPNG(t, 2, 2))
		_ = writeSidecar(img, ImageProps{Job: "j" + itoa(i), Studio: "a", CreatedAt: "2026-09-23T00:00:0" + itoa(i) + "Z"})
	}
	outside := filepath.Join(os.Getenv("HOME"), "mine", "new.png")
	writeFile(t, outside, tinyPNG(t, 2, 2))
	if err := appendHistory(HistoryItem{Path: outside, Studio: "a"}); err != nil {
		t.Fatal(err)
	}
	idx := readHistory()
	var paths []string
	for _, it := range idx.Items {
		if it.Path != "" {
			paths = append(paths, filepath.Base(it.Path))
		}
	}
	if strings.Join(paths, ",") != "old-0.png,old-1.png,new.png" || idx.Generation == "" {
		t.Fatalf("index = %v (generation %q), want the two older pictures kept", paths, idx.Generation)
	}
	stale := "0000:2"
	rec := studioDo(t, HandleHistory, http.MethodGet, "/imagegen/history?before="+stale, "")
	if strings.Contains(rec.Body.String(), ".png") {
		t.Errorf("a cursor from another generation = %s, want an empty page", rec.Body)
	}
	rec = studioDo(t, HandleHistory, http.MethodGet, "/imagegen/history?before="+idx.Generation+":999", "")
	if strings.Contains(rec.Body.String(), ".png") {
		t.Errorf("a cursor past the end = %s, want an empty page", rec.Body)
	}
	news := studioSinceFor("a", "s1", nil, idx, studioSeen{History: 1, HistoryGen: "0000"}, true)
	if len(news.Items) != 0 {
		t.Errorf("news across a rebuild = %+v, want none", news.Items)
	}
}

// A knowledge file the member made a symlink keeps being one, and keeps its mode.
func TestKnowledgeWritesThroughASymlink(t *testing.T) {
	withStudios(t)
	real := filepath.Join(os.Getenv("HOME"), "notes", "sdxl.md")
	writeFile(t, real, []byte("## 記録\n"))
	_ = os.Chmod(real, 0o644)
	link, _ := knowledgeFile(KnowledgeFamily, "sdxl")
	_ = os.MkdirAll(filepath.Dir(link), 0o700)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := appendKnowledgeRecord(KnowledgeAdd{Scope: KnowledgeFamily, Key: "sdxl", Note: "n"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi.Mode(), err)
	}
	b, _ := os.ReadFile(real)
	fi, _ := os.Stat(real)
	if !strings.Contains(string(b), "user: n") || fi.Mode().Perm() != 0o644 {
		t.Errorf("target = %q mode %v", b, fi.Mode().Perm())
	}
}
