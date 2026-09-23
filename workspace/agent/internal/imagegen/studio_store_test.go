package imagegen

// The studio store (ADR 0100 decisions 2, 3, 4 and 9): the per-field save, the agent's fields
// and the locks, If-Match, the edit log and its torn lines, rewind, and the binding.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withStudios gives the test its own HOME, sessions dir and browse root, and a job queue.
func withStudios(t *testing.T) *jobQueue {
	t.Helper()
	q := withJobQueue(t)
	home := os.Getenv("HOME")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	withInputGate(t, home)
	oldAlive, oldCAS, oldBind := StudioSessionAlive, SessionStudioCAS, BindStudioSession
	StudioSessionAlive = func(string) bool { return false }
	SessionStudioCAS = func(name, from, to string) {
		m, ok := session.ReadMeta(name)
		if ok && m.Studio == from {
			m.Studio = to
			session.WriteMeta(m)
		}
	}
	t.Cleanup(func() { StudioSessionAlive, SessionStudioCAS, BindStudioSession = oldAlive, oldCAS, oldBind })
	return q
}

func studioDo(t *testing.T, h http.HandlerFunc, method, target, body string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	// The mux fills PathValue in production; here the id is the path's fourth segment.
	if parts := strings.Split(strings.SplitN(target, "?", 2)[0], "/"); len(parts) > 3 {
		req.SetPathValue("id", parts[3])
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func createStudio(t *testing.T, draft string) ImageStudioWire {
	t.Helper()
	rec := studioDo(t, HandleStudios, http.MethodPost, "/imagegen/studios", `{"title":"t","draft":`+draft+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var w ImageStudioWire
	if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func putStudio(t *testing.T, id, body string, header ...string) (int, ImageStudioPatchResult) {
	t.Helper()
	rec := studioDo(t, HandleStudio, http.MethodPut, "/imagegen/studios/"+id, body, header...)
	var out ImageStudioPatchResult
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func droppedReason(res ImageStudioPatchResult, field string) string {
	for _, d := range res.Dropped {
		if d.Field == field {
			return d.Reason
		}
	}
	return ""
}

// bindForTest binds a session to the studio on both sides, as a create does.
func bindForTest(t *testing.T, studio, name string) {
	t.Helper()
	session.WriteMeta(session.Meta{Name: name, Kind: session.KindClaude, Studio: studio})
	if _, err := bindStudioSession(studio, name, ""); err != nil {
		t.Fatal(err)
	}
}

func TestStudioSavesEachFieldOnItsOwn(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"provider":"comfy"}`)
	if !s.AgentTrial || s.ID == "" || s.UpdatedAt == "" {
		t.Fatalf("created = %+v, want an id, a version and agent trials on by default", s)
	}
	code, res := putStudio(t, s.ID, `{"author":"human","draft":{"prompt":"","size":"1001x1000",
		"params":{"steps":200},"negativePrompt":"blurry","seed_policy":"fixed","count":9,"op":"paint"}}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	for _, f := range []string{"size", "params", "count", "op"} {
		if droppedReason(res, f) != dropInvalid {
			t.Errorf("%s: dropped %q, want invalid (%+v)", f, droppedReason(res, f), res.Dropped)
		}
	}
	d := res.Studio.Draft
	if d.NegativePrompt != "blurry" || d.SeedPolicy != "fixed" || d.Size != "" || d.Params != nil {
		t.Fatalf("draft = %+v, want the valid fields saved and the invalid ones not", d)
	}
	// An empty prompt is an unfinished draft, not an invalid one.
	if droppedReason(res, "prompt") != "" {
		t.Errorf("an empty prompt was refused: %+v", res.Dropped)
	}
}

func TestStudioAgentIsHeldToItsFieldsAndTheLocks(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"provider":"comfy","prompt":"a cat","negativePrompt":"blurry"}`)
	bindForTest(t, s.ID, "s1")
	if code, _ := putStudio(t, s.ID, `{"author":"human","locks":["prompt","nope"]}`); code != http.StatusOK {
		t.Fatal(code)
	}

	code, res := putStudio(t, s.ID, `{"author":"agent","session":"s1","draft":{"prompt":"a dog","model":"x",
		"params":{"cfg":5},"suggest_model":"sdxl-base","clear":["negativePrompt"]},"title":"mine"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	if droppedReason(res, "prompt") != dropLocked || droppedReason(res, "model") != dropHumanOnly || droppedReason(res, "title") != dropHumanOnly {
		t.Fatalf("dropped = %+v, want prompt locked, model and title the user's", res.Dropped)
	}
	d := res.Studio.Draft
	if d.Prompt != "a cat" || d.Model != "" || d.Params == nil || d.Params.CFG != 5 || d.SuggestModel != "sdxl-base" || d.NegativePrompt != "" {
		t.Fatalf("draft = %+v", d)
	}
	if !slices.Equal(res.Studio.Locks, []string{"prompt"}) {
		t.Errorf("locks = %v, want the unknown key left out", res.Studio.Locks)
	}
	// An explicit null clears too.
	if _, res := putStudio(t, s.ID, `{"author":"agent","session":"s1","draft":{"suggest_model":null}}`); res.Studio.Draft.SuggestModel != "" {
		t.Errorf("null did not clear: %+v", res.Studio.Draft)
	}
	// The call-time check of decision 2: only the bound session writes as the agent.
	if code, _ := putStudio(t, s.ID, `{"author":"agent","session":"s2","draft":{"size":"512x512"}}`); code != http.StatusConflict {
		t.Errorf("another session's write = %d, want 409", code)
	}
	// The member is not held to the locks.
	if _, res := putStudio(t, s.ID, `{"author":"human","draft":{"prompt":"a fox"}}`); res.Studio.Draft.Prompt != "a fox" {
		t.Errorf("the member's edit of a locked field was dropped: %+v", res.Dropped)
	}
	// Every agent edit is logged as the agent's, with its session.
	var agentEdits int
	for _, e := range readStudioLog(s.ID) {
		if e.Kind == DraftLogEdit && e.Author == studioAuthorAgent && e.Session == "s1" {
			agentEdits++
		}
	}
	if agentEdits != 2 {
		t.Errorf("agent edits in the log = %d, want 2", agentEdits)
	}
}

func TestStudioIfMatch(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{}`)
	code, res := putStudio(t, s.ID, `{"author":"human","draft":{"prompt":"one"}}`, "If-Match", `"`+s.UpdatedAt+`"`)
	if code != http.StatusOK {
		t.Fatalf("PUT with the current version = %d", code)
	}
	if res.Studio.UpdatedAt == s.UpdatedAt {
		t.Fatal("UpdatedAt did not move")
	}
	if code, _ := putStudio(t, s.ID, `{"author":"human","draft":{"prompt":"two"}}`, "If-Match", `"`+s.UpdatedAt+`"`); code != http.StatusPreconditionFailed {
		t.Fatalf("PUT with a stale version = %d, want 412", code)
	}
	w := studioDo(t, HandleStudio, http.MethodGet, "/imagegen/studios/"+s.ID, "")
	if !strings.Contains(w.Body.String(), `"prompt":"one"`) {
		t.Fatalf("the stale write landed: %s", w.Body)
	}
}

func TestStudioLogSurvivesTornAppends(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"prompt":"a"}`)
	unlock := lockStudio(s.ID)
	defer unlock()

	// A write that fails half-way is cut back, and the next line starts a line.
	old := studioLogWrite
	studioLogWrite = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, errors.New("disk full")
	}
	if err := appendStudioLog(s.ID, &DraftLogEntry{Kind: DraftLogEdit, Author: "human"}); err == nil {
		t.Fatal("the failing write reported success")
	}
	studioLogWrite = old
	// A fragment left by a crash (no newline at all) is cut before the next append too.
	f, _ := os.OpenFile(studioLogPath(s.ID), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"seq":99,"kind":"ed`)
	f.Close()
	if err := appendStudioLog(s.ID,
		&DraftLogEntry{Kind: DraftLogPress, Version: "v1"},
		&DraftLogEntry{Kind: DraftLogPressResult, Version: "v1", State: pressOK, Group: "g1"},
		&DraftLogEntry{Kind: DraftLogPressResult, Version: "v1", State: pressRecovered, Group: "g2"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(studioLogPath(s.ID))
	if strings.Contains(string(raw), `"seq":99`) || strings.Count(string(raw), "\n") != 4 {
		t.Fatalf("a fragment survived:\n%s", raw)
	}
	entries := readStudioLog(s.ID)
	var seqs []int
	var results []string
	for _, e := range entries {
		seqs = append(seqs, e.Seq)
		if e.Kind == DraftLogPressResult {
			results = append(results, e.Group)
		}
	}
	if !slices.Equal(seqs, []int{1, 2, 3}) {
		t.Errorf("seqs = %v, want 1 (create), then 2 and 3 — the failed write consumed none", seqs)
	}
	if !slices.Equal(results, []string{"g1"}) {
		t.Errorf("press results = %v, want only the first per version", results)
	}
	// A line that does not parse is skipped, not fatal.
	f, _ = os.OpenFile(studioLogPath(s.ID), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("not json\n")
	f.Close()
	if n := len(readStudioLog(s.ID)); n != 3 {
		t.Errorf("entries after a garbage line = %d, want 3", n)
	}
}

func TestStudioRewindRestoresEveryFieldAndKeepsTheLocks(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"prompt":"first","size":"512x512"}`)
	putStudio(t, s.ID, `{"author":"human","draft":{"prompt":"second","size":"768x768"},"locks":["prompt"]}`)
	first := readStudioLog(s.ID)[0]

	rec := studioDo(t, HandleStudioRewind, http.MethodPost, "/imagegen/studios/"+s.ID+"/rewind", `{"to":`+itoa(first.Seq)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rewind = %d %s", rec.Code, rec.Body)
	}
	var w ImageStudioWire
	_ = json.Unmarshal(rec.Body.Bytes(), &w)
	if w.Draft.Prompt != "first" || w.Draft.Size != "512x512" {
		t.Fatalf("draft = %+v, want the first draft whole, the locked prompt included", w.Draft)
	}
	if !slices.Equal(w.Locks, []string{"prompt"}) {
		t.Errorf("locks = %v, want them left as they were", w.Locks)
	}
	entries := readStudioLog(s.ID)
	last := entries[len(entries)-1]
	if last.Kind != DraftLogRewind || last.RewindTo != first.Seq || len(entries) != 3 {
		t.Fatalf("log = %+v, want the history kept and a rewind line at the end", entries)
	}
	if len(last.Changes) != 2 || string(last.Changes[0].Before) != `"second"` {
		t.Errorf("rewind changes = %+v, want before = now and after = the target", last.Changes)
	}
	if rec := studioDo(t, HandleStudioRewind, http.MethodPost, "/imagegen/studios/"+s.ID+"/rewind", `{"to":999}`); rec.Code != http.StatusNotFound {
		t.Errorf("rewind to nothing = %d", rec.Code)
	}
}

func TestStudioDraftLogPages(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"prompt":"0"}`)
	for i := 1; i <= 24; i++ {
		putStudio(t, s.ID, `{"author":"human","draft":{"prompt":"`+itoa(i)+`"}}`)
	}
	w := studioDo(t, HandleStudio, http.MethodGet, "/imagegen/studios/"+s.ID, "")
	var wire ImageStudioWire
	_ = json.Unmarshal(w.Body.Bytes(), &wire)
	if len(wire.RecentLog) != studioRecentLog || wire.RecentLog[studioRecentLog-1].Seq != 25 {
		t.Fatalf("recent log = %d entries, want the newest %d", len(wire.RecentLog), studioRecentLog)
	}
	rec := studioDo(t, HandleStudioDraftLog, http.MethodGet, "/imagegen/studios/"+s.ID+"/draft-log?before=20&limit=10", "")
	var page DraftLogPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Entries) != 10 || page.Entries[0].Seq != 10 || page.Entries[9].Seq != 19 || page.Before != 10 {
		t.Fatalf("page = %d entries from %d, before %d", len(page.Entries), page.Entries[0].Seq, page.Before)
	}
}

func TestStudioBindingOnCreate(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{}`)
	alive := map[string]bool{"s1": true}
	StudioSessionAlive = func(n string) bool { return alive[n] }
	if _, err := bindStudioSession(s.ID, "s1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := bindStudioSession(s.ID, "s2", ""); !errors.Is(err, ErrStudioBound) {
		t.Fatalf("taking a live session's studio: %v, want ErrStudioBound", err)
	}
	alive["s1"] = false
	replaced, err := bindStudioSession(s.ID, "s2", "")
	if err != nil || replaced != "s1" {
		t.Fatalf("taking a stopped session's studio: %q %v, want s1 replaced", replaced, err)
	}
	// A recreate moves it only while it still names the previous slot.
	if _, err := bindStudioSession(s.ID, "s3", "s1"); !errors.Is(err, ErrStudioBound) {
		t.Fatalf("recreate from a slot it no longer names: %v", err)
	}
	if _, err := bindStudioSession(s.ID, "s3", "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := bindStudioSession("00000000-0000-4000-8000-000000000000", "s1", ""); !errors.Is(err, ErrStudioNotFound) {
		t.Fatalf("no studio: %v", err)
	}
}

func TestStartStudiosSettlesOneSidedBindings(t *testing.T) {
	withStudios(t)
	gone := createStudio(t, `{}`)
	kept := createStudio(t, `{}`)
	older := createStudio(t, `{}`)
	newer := createStudio(t, `{}`)
	// gone names a session that was deleted.
	session.WriteMeta(session.Meta{Name: "dead", Kind: session.KindClaude})
	if _, err := bindStudioSession(gone.ID, "dead", ""); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(session.MetaPath("dead"))
	// kept names s1, whose meta never got the copy.
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindClaude})
	_, _ = bindStudioSession(kept.ID, "s1", "")
	// s2 claims a studio that does not name it back.
	session.WriteMeta(session.Meta{Name: "s2", Kind: session.KindClaude, Studio: gone.ID})
	// two studios name s3; the newer one keeps it.
	session.WriteMeta(session.Meta{Name: "s3", Kind: session.KindClaude, Studio: older.ID})
	_, _ = bindStudioSession(older.ID, "s3", "")
	rec, _ := loadStudio(newer.ID)
	rec.Session = "s3"
	touchStudio(rec)
	_ = saveStudio(rec)

	BindStudioSession = nil
	StartStudios()
	if BindStudioSession == nil {
		t.Fatal("the store did not install BindStudioSession")
	}
	want := map[string]string{gone.ID: "", kept.ID: "s1", older.ID: "", newer.ID: "s3"}
	for id, sess := range want {
		if r, _ := loadStudio(id); r.Session != sess {
			t.Errorf("studio %s names %q, want %q", id, r.Session, sess)
		}
	}
	for name, studio := range map[string]string{"s1": kept.ID, "s2": "", "s3": newer.ID} {
		if m, _ := session.ReadMeta(name); m.Studio != studio {
			t.Errorf("meta %s names %q, want %q", name, m.Studio, studio)
		}
	}
}

func TestStudioBindRoute(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{}`)
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindClaude})
	if rec := studioDo(t, HandleStudioBind, http.MethodPost, "/imagegen/studios/"+s.ID+"/bind", `{"session":"s1"}`); rec.Code != http.StatusOK {
		t.Fatalf("bind = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("s1"); m.Studio != s.ID {
		t.Fatalf("meta = %q", m.Studio)
	}
	if rec := studioDo(t, HandleStudioBind, http.MethodPost, "/imagegen/studios/"+s.ID+"/bind", `{"session":""}`); rec.Code != http.StatusOK {
		t.Fatalf("unbind = %d", rec.Code)
	}
	r, _ := loadStudio(s.ID)
	if m, _ := session.ReadMeta("s1"); m.Studio != "" || r.Session != "" {
		t.Fatalf("after unbind: meta %q, studio %q", m.Studio, r.Session)
	}
}

func TestStudioDeleteKeepsThePicturesAndUnbinds(t *testing.T) {
	withStudios(t)
	s := createStudio(t, `{"prompt":"x"}`)
	bindForTest(t, s.ID, "s1")
	if rec := studioDo(t, HandleStudio, http.MethodDelete, "/imagegen/studios/"+s.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if _, err := os.Stat(studioLogPath(s.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the log survived the studio")
	}
	if m, _ := session.ReadMeta("s1"); m.Studio != "" {
		t.Errorf("meta still names the deleted studio")
	}
	if rec := studioDo(t, HandleStudio, http.MethodGet, "/imagegen/studios/"+s.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d", rec.Code)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
