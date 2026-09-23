package imagegen

// A studio press (ADR 0100 decision 9), the picture history, get_image_studio's view, the
// persona and the knowledge documents. No real provider runs: the queue drives a gateProvider.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withStudioProvider installs a fleet provider whose catalogue offers one sdxl model.
func withStudioProvider(t *testing.T) *gateProvider {
	t.Helper()
	p := newGateProvider(t)
	withStudio(t, p, StudioModel{ID: "sdxl-base", Label: "SDXL", Family: "sdxl", Knobs: comfyKnobsSampled,
		Ops: []Op{OpGenerate, OpEdit, OpInpaint}, Params: EngineParams{Steps: 20, CFG: 7}})
	return p
}

func press(t *testing.T, id, body string) (int, ImageStudioPressResult, string) {
	t.Helper()
	rec := studioDo(t, HandleStudioPress, http.MethodPost, "/imagegen/studios/"+id+"/press", body)
	var out ImageStudioPressResult
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestStudioPressRecordsTheVersionAroundTheEnqueue(t *testing.T) {
	q := withStudios(t)
	p := withStudioProvider(t)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"a lighthouse","seed_policy":"fixed","seed":7}`)

	code, out, body := press(t, s.ID, `{"mode":"enqueue"}`)
	if code != http.StatusOK || out.Version != "v1" || !out.Recorded || out.Group == "" || len(out.Jobs) != 1 {
		t.Fatalf("press = %d %s", code, body)
	}
	entries := readStudioLog(s.ID)
	pr, res := entries[len(entries)-2], entries[len(entries)-1]
	if pr.Kind != DraftLogPress || pr.Version != "v1" || pr.Mode != "enqueue" || pr.Draft == nil || pr.Draft.Prompt != "a lighthouse" || pr.Author != "human" {
		t.Fatalf("press line = %+v", pr)
	}
	if res.Kind != DraftLogPressResult || res.Version != "v1" || res.State != pressOK || res.Group != out.Group {
		t.Fatalf("result line = %+v", res)
	}

	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "the picture", func() bool { return stateOf(q, out.Jobs[0].ID) == JobDone })
	job := q.List().Jobs[0]
	if job.Studio != s.ID || job.Version != "v1" {
		t.Fatalf("job wire = %s/%s", job.Studio, job.Version)
	}
	props, ok := readSidecar(job.Files[0].Path)
	if !ok || props.Studio != s.ID || props.Version != "v1" {
		t.Fatalf("sidecar = %+v", props)
	}
	hist := studioDo(t, HandleHistory, http.MethodGet, "/imagegen/history?studio="+s.ID, "")
	var page HistoryPage
	_ = json.Unmarshal(hist.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].Version != "v1" || page.Items[0].Path != job.Files[0].Path {
		t.Fatalf("history = %s", hist.Body)
	}

	// Two presses of the same draft are two versions.
	_, out2, _ := press(t, s.ID, `{"mode":"trial"}`)
	if out2.Version != "v2" {
		t.Fatalf("second press = %+v", out2)
	}
}

func TestStudioPressResultIsRetriedOnceAndReportedWhenLost(t *testing.T) {
	withStudios(t)
	withStudioProvider(t)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"x"}`)
	old := studioLogWrite
	t.Cleanup(func() { studioLogWrite = old })
	failAfter := func(ok int) {
		calls := 0
		studioLogWrite = func(f *os.File, b []byte) (int, error) {
			calls++
			if calls > ok {
				return 0, errors.New("disk full")
			}
			return f.Write(b)
		}
	}
	failAfter(1) // the press line lands, both result writes fail
	_, out, _ := press(t, s.ID, `{"mode":"trial"}`)
	if out.Recorded || out.Version != "v1" || len(out.Jobs) != 1 {
		t.Fatalf("press = %+v, want the job queued and recorded:false", out)
	}
	calls := 0
	studioLogWrite = func(f *os.File, b []byte) (int, error) {
		calls++
		if calls == 2 {
			return 0, errors.New("disk full")
		}
		return f.Write(b)
	}
	_, out, _ = press(t, s.ID, `{"mode":"trial"}`)
	if !out.Recorded {
		t.Fatalf("press = %+v, want the immediate retry to have recorded it", out)
	}
	// A press line that cannot be written queues nothing.
	failAfter(0)
	code, _, body := press(t, s.ID, `{"mode":"trial"}`)
	if code != http.StatusInternalServerError || !strings.Contains(body, "press_unrecorded") {
		t.Fatalf("unrecorded press = %d %s", code, body)
	}
}

func TestAgentTrialRefusals(t *testing.T) {
	withStudios(t)
	withStudioProvider(t)
	cases := []struct {
		name, draft, session, code string
		trialOff                   bool
	}{
		{"no model", `{"provider":"comfy","prompt":"x"}`, "s1", "no_model", false},
		{"inpaint without a mask", `{"provider":"comfy","model":"sdxl-base","prompt":"x","op":"inpaint"}`, "s1", "needs_mask", false},
		{"an unfinished draft", `{"provider":"comfy","model":"sdxl-base"}`, "s1", "bad_prompt", false},
		{"trials not allowed", `{"provider":"comfy","model":"sdxl-base","prompt":"x"}`, "s1", "agent_trial_off", true},
		{"another session", `{"provider":"comfy","model":"sdxl-base","prompt":"x"}`, "s2", "studio_not_bound", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := createStudio(t, c.draft)
			bindForTest(t, s.ID, "s1")
			if c.trialOff {
				putStudio(t, s.ID, `{"author":"human","agent_trial":false}`)
			}
			before := len(readStudioLog(s.ID))
			code, _, body := press(t, s.ID, `{"mode":"agent_trial","session":"`+c.session+`"}`)
			if code < 400 || !strings.Contains(body, `"`+c.code+`"`) {
				t.Fatalf("agent trial = %d %s, want %s", code, body, c.code)
			}
			if n := len(readStudioLog(s.ID)); n != before {
				t.Errorf("a refused trial wrote %d log lines", n-before)
			}
		})
	}
}

func TestAgentTrialRunsTheDraftAsOneTrialPicture(t *testing.T) {
	q := withStudios(t)
	p := withStudioProvider(t)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"x","count":3,"jobs":5,"full_steps":true}`)
	bindForTest(t, s.ID, "s1")
	code, out, body := press(t, s.ID, `{"mode":"agent_trial","session":"s1"}`)
	if code != http.StatusOK || len(out.Jobs) != 1 {
		t.Fatalf("agent trial = %d %s", code, body)
	}
	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "the trial", func() bool { return stateOf(q, out.Jobs[0].ID) == JobDone })
	j := q.List().Jobs[0]
	if !j.Trial || j.Count > 1 || j.Model != "sdxl-base" {
		t.Fatalf("job = %+v, want one trial picture on the draft's own model at trial steps", j)
	}
	if e := readStudioLog(s.ID); e[len(e)-2].Author != studioAuthorAgent || e[len(e)-2].Session != "s1" {
		t.Errorf("press line = %+v, want the agent's", e[len(e)-2])
	}
}

func TestStudioTrialPendingIsSaidInTheStudiosTerms(t *testing.T) {
	withStudios(t)
	withStudioProvider(t)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"x"}`)
	bindForTest(t, s.ID, "s1")
	var body string
	var code int
	for range imagegenTrialMax + 2 {
		code, _, body = press(t, s.ID, `{"mode":"agent_trial","session":"s1"}`)
	}
	if code != http.StatusTooManyRequests || !strings.Contains(body, "get_image_studio") {
		t.Fatalf("over the trial cap = %d %s", code, body)
	}
	last := readStudioLog(s.ID)
	if e := last[len(last)-1]; e.Kind != DraftLogPressResult || e.State != pressFailed {
		t.Errorf("the refused press has no failed result: %+v", e)
	}
}

func TestStartupRecoversPressesWithoutAResult(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"prompt":"x"}`)
	unlock := lockStudio(s.ID)
	_ = appendStudioLog(s.ID,
		&DraftLogEntry{Kind: DraftLogPress, Version: "v1"},
		&DraftLogEntry{Kind: DraftLogPress, Version: "v2"},
		&DraftLogEntry{Kind: DraftLogPress, Version: "v3"},
		&DraftLogEntry{Kind: DraftLogPressResult, Version: "v3", State: pressOK})
	unlock()
	// v1's picture made it to disk before the crash; v2's did not.
	img := filepath.Join(ConsoleDir(), "image-1-1.png")
	writeFile(t, img, tinyPNG(t, 2, 2))
	if err := writeSidecar(img, ImageProps{Job: "j9", Group: "g4", Studio: s.ID, Version: "v1", CreatedAt: "2026-09-23T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	recoverStudioPresses()
	got := map[string]DraftLogEntry{}
	for _, e := range readStudioLog(s.ID) {
		if e.Kind == DraftLogPressResult {
			got[e.Version] = e
		}
	}
	if e := got["v1"]; e.State != pressRecovered || e.Group != "g4" || len(e.Jobs) != 1 || e.Jobs[0] != "j9" {
		t.Errorf("v1 = %+v, want recovered from the sidecar", e)
	}
	if got["v2"].State != pressLost {
		t.Errorf("v2 = %+v, want lost", got["v2"])
	}
	if got["v3"].State != pressOK {
		t.Errorf("v3 = %+v, want its own result kept", got["v3"])
	}
	n := len(readStudioLog(s.ID))
	recoverStudioPresses()
	if len(readStudioLog(s.ID)) != n {
		t.Error("a second recovery wrote again")
	}
}

func TestHistoryIsRebuiltFromTheSidecarsAndPaged(t *testing.T) {
	withStudios(t)
	for i := range 5 {
		img := filepath.Join(ConsoleDir(), "image-"+itoa(i)+".png")
		writeFile(t, img, tinyPNG(t, 2, 2))
		studio := "a"
		if i%2 == 1 {
			studio = "b"
		}
		_ = writeSidecar(img, ImageProps{Job: "j" + itoa(i), Studio: studio, Version: "v" + itoa(i), CreatedAt: "2026-09-23T00:00:0" + itoa(i) + "Z"})
	}
	// A reference copy is not a picture made here.
	writeFile(t, filepath.Join(ConsoleInputsDir(), "s0", "00-a.png"), tinyPNG(t, 2, 2))

	rec := studioDo(t, HandleHistory, http.MethodGet, "/imagegen/history?studio=a&limit=2", "")
	var page HistoryPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 2 || page.Items[0].Version != "v4" || page.Items[1].Version != "v2" || page.Before == "" {
		t.Fatalf("first page = %s", rec.Body)
	}
	if _, err := os.Stat(historyPath()); err != nil {
		t.Fatalf("the index was not rebuilt: %v", err)
	}
	rec = studioDo(t, HandleHistory, http.MethodGet, "/imagegen/history?studio=a&limit=2&before="+page.Before, "")
	page = HistoryPage{}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].Version != "v0" || page.Before != "" {
		t.Fatalf("second page = %s", rec.Body)
	}
	// A picture deleted since is left out.
	_ = os.Remove(filepath.Join(ConsoleDir(), "image-4.png"))
	rec = studioDo(t, HandleHistory, http.MethodGet, "/imagegen/history?studio=a", "")
	if strings.Contains(rec.Body.String(), `"v4"`) {
		t.Errorf("a deleted picture is listed: %s", rec.Body)
	}
}

func agentView(t *testing.T, id, sess string) (int, map[string]any, string) {
	t.Helper()
	rec := studioDo(t, HandleStudio, http.MethodGet, "/imagegen/studios/"+id+"?view=agent&session="+sess, "")
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestAgentViewSinceLastCall(t *testing.T) {
	q := withStudios(t)
	p := withStudioProvider(t)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"x"}`)
	bindForTest(t, s.ID, "s1")

	if code, _, _ := agentView(t, s.ID, "s2"); code != http.StatusConflict {
		t.Fatalf("another session's read = %d", code)
	}
	_, v, body := agentView(t, s.ID, "s1")
	since := v["since"].(map[string]any)
	if since["first_call"] != true || len(since["items"].([]any)) != 0 {
		t.Fatalf("first call since = %v", since)
	}
	if !strings.Contains(body, `"dialect":"tags"`) || !strings.Contains(body, `"steps_range":[20,40]`) {
		t.Errorf("model facts lack the family row: %s", body)
	}

	// The agent's own edit is not news; seven of the member's are, folded to five.
	putStudio(t, s.ID, `{"author":"agent","session":"s1","draft":{"negativePrompt":"blur"}}`)
	for i := range 7 {
		putStudio(t, s.ID, `{"author":"human","draft":{"size":"`+[]string{"512x512", "768x768"}[i%2]+`"}}`)
	}
	_, out, _ := press(t, s.ID, `{"mode":"trial"}`)
	<-p.begun
	p.release <- struct{}{}
	waitFor(t, "the trial", func() bool { return stateOf(q, out.Jobs[0].ID) == JobDone })

	_, v, body = agentView(t, s.ID, "s1")
	since = v["since"].(map[string]any)
	items := since["items"].([]any)
	if len(items) != studioSinceMax || since["more"].(float64) != 4 {
		t.Fatalf("since = %d items + %v more, want 5 + 4 (7 edits, 1 press, 1 picture)", len(items), since["more"])
	}
	if first := items[0].(map[string]any); first["kind"] != "result" || first["version"] != "v1" {
		t.Errorf("first item = %v, want the new picture first", first)
	}
	if strings.Contains(body, "negativePrompt:") {
		t.Errorf("the agent's own edit is reported back to it: %s", body)
	}
	_, v, _ = agentView(t, s.ID, "s1")
	if items := v["since"].(map[string]any)["items"].([]any); len(items) != 0 {
		t.Errorf("a second call reports %v again", items)
	}
	// Reading does not move the version the pane saves against.
	r, _ := loadStudio(s.ID)
	w := studioDo(t, HandleStudio, http.MethodGet, "/imagegen/studios/"+s.ID, "")
	if !strings.Contains(w.Body.String(), r.UpdatedAt) || strings.Contains(w.Body.String(), `"seen"`) {
		t.Errorf("the studio's own GET = %s", w.Body)
	}
}

func TestAgentViewStaysUnderItsCap(t *testing.T) {
	withStudios(t)
	withStudioProvider(t)
	long := strings.Repeat("a very long prompt ", 1200)
	s := createStudio(t, `{"provider":"comfy","model":"sdxl-base","prompt":"`+long+`","negativePrompt":"`+long[:3000]+`"}`)
	bindForTest(t, s.ID, "s1")
	_, v, body := agentView(t, s.ID, "s1")
	if len(body) > studioAgentViewMax {
		t.Fatalf("view is %d bytes, over %d", len(body), studioAgentViewMax)
	}
	if v["truncated"] != true || v["draft"].(map[string]any)["prompt"] == "" {
		t.Errorf("a cut view must say so and keep what fits: truncated=%v", v["truncated"])
	}
}

func TestAgentViewWithoutAModelListsTheChoices(t *testing.T) {
	withStudios(t)
	withStudioProvider(t)
	s := createStudio(t, `{"provider":"comfy","prompt":"x","op":"inpaint"}`)
	bindForTest(t, s.ID, "s1")
	_, v, body := agentView(t, s.ID, "s1")
	if v["model"] != nil || !strings.Contains(body, `"models":[{"id":"sdxl-base"`) {
		t.Fatalf("view = %s", body)
	}
	if v["needs_mask"] != true {
		t.Errorf("needs_mask = %v", v["needs_mask"])
	}
	if !strings.Contains(body, `"user_only_fields":["provider","model"`) {
		t.Errorf("user-only fields missing: %s", body)
	}
}

func TestStudioPersonaFollowsTheLocale(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{}`)
	old := Locale
	t.Cleanup(func() { Locale = old })
	for _, lang := range []string{"ja", "en"} {
		Locale = func() string { return lang }
		rec := studioDo(t, HandleStudioPersona, http.MethodGet, "/imagegen/studios/"+s.ID+"/persona", "")
		var p ImageStudioPersona
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.Lang != lang || !strings.Contains(p.Prompt, "get_image_studio") || !strings.Contains(p.Prompt, "set_image_draft") {
			t.Errorf("%s persona = %+v", lang, p)
		}
	}
}

func TestKnowledgeRecordsAreAppendedAndTheRestKept(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"provider":"comfy","model":"org/sdxl-base"}`)
	bindForTest(t, s.ID, "s1")
	add := func(body string) (int, Knowledge) {
		rec := studioDo(t, HandleKnowledge, http.MethodPost, "/imagegen/knowledge", body)
		var k Knowledge
		_ = json.Unmarshal(rec.Body.Bytes(), &k)
		return rec.Code, k
	}
	code, k := add(`{"scope":"model","key":"org/sdxl-base","note":"cfg 5 keeps skin natural","evidence":"v3 vs v4","session":"s1"}`)
	if code != http.StatusOK || !strings.Contains(k.Records, "agent s1: cfg 5 keeps skin natural") || !strings.Contains(k.Records, "v3 vs v4") {
		t.Fatalf("add = %d %+v", code, k)
	}
	if filepath.Base(k.Path) != "org%2Fsdxl-base.md" || filepath.Base(filepath.Dir(k.Path)) != "models" {
		t.Errorf("path = %s, want one file per model id", k.Path)
	}
	// A hand edit puts a summary in and moves the records section up.
	hand := "# sdxl\n\n## 要約\nuse tags\n\n## 記録\n- old\n\n## 設定\nsteps 30\n"
	_ = os.WriteFile(k.Path, []byte(hand), 0o600)
	_, k = add(`{"scope":"model","key":"org/sdxl-base","note":"second"}`)
	raw, _ := os.ReadFile(k.Path)
	if !strings.Contains(string(raw), "- old\n- ") || !strings.Contains(string(raw), "user: second\n\n## 設定\nsteps 30\n") {
		t.Fatalf("file after append:\n%s", raw)
	}
	if k.Summary != "use tags" || k.Settings != "steps 30" {
		t.Errorf("sections = %+v", k)
	}
	if code, _ := add(`{"scope":"model","key":"x","note":"` + strings.Repeat("字", knowledgeNoteMax+1) + `"}`); code != http.StatusBadRequest {
		t.Errorf("an over-long record = %d", code)
	}
	if code, _ := add(`{"scope":"repo","key":"x","note":"n"}`); code != http.StatusBadRequest {
		t.Errorf("a bad scope = %d", code)
	}
	// The summary is cut when READ, and says so; the file keeps all of it.
	_ = os.WriteFile(k.Path, []byte("## Summary\n"+strings.Repeat("s", knowledgeSummaryMax+10)+"\n"), 0o600)
	rec := studioDo(t, HandleKnowledge, http.MethodGet, "/imagegen/knowledge?scope=model&key=org/sdxl-base", "")
	var got Knowledge
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Summary) != knowledgeSummaryMax || !got.SummaryTruncated {
		t.Errorf("summary = %d bytes, truncated %v", len(got.Summary), got.SummaryTruncated)
	}
}
