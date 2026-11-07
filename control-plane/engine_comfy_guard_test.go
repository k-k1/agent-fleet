package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// comfyOwnerStub is a ComfyUI that numbers its prompts and answers the routes the image
// features use. It records every upstream path so a refused request can be shown never to have
// reached the engine.
type comfyOwnerStub struct {
	mu   sync.Mutex
	hits []string
	n    int
}

func (s *comfyOwnerStub) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...)
}

func (s *comfyOwnerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/system_stats" {
		_, _ = w.Write([]byte(`{"system":{}}`))
		return
	}
	s.mu.Lock()
	s.hits = append(s.hits, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	switch {
	case r.Method == "POST" && r.URL.Path == "/prompt":
		s.mu.Lock()
		s.n++
		id := "pid-" + string(rune('a'+s.n-1))
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": id})
	case strings.HasPrefix(r.URL.Path, "/history/"):
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		_ = json.NewEncoder(w).Encode(map[string]any{id: map[string]any{
			"status":  map[string]any{"completed": true},
			"outputs": map[string]any{"9": map[string]any{"images": []map[string]any{{"filename": "out-" + id + ".png", "subfolder": "", "type": "output"}}}},
		}})
	case r.URL.Path == "/view":
		_, _ = w.Write([]byte("PNG:" + r.URL.Query().Get("filename")))
	case r.URL.Path == "/queue" && r.Method == "GET":
		_, _ = w.Write([]byte(`{"queue_running":[[0,"pid-a",{},{},[]]],"queue_pending":[[1,"pid-b",{},{},[]]]}`))
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

// The relay serves a member's own job end to end and refuses everything else: another member's
// prompt id, its output files, queue-wide and admin routes, and jobs the owner map does not know.
func TestComfyGatewayBindsJobsToTheSubmitter(t *testing.T) {
	ctx := context.Background()
	st := testSettingsStore(t)
	stub := &comfyOwnerStub{}
	up := httptest.NewServer(stub)
	defer up.Close()

	e := newTestExternalEngine(t, up.URL, st)
	reg := &engineRegistry{
		byKey:   map[string]*engineRuntimeState{"image": e},
		signKey: engineSignKey([]byte(strings.Repeat("k", 32))),
	}
	mgr := &manager{store: st}
	a := engineAdminAPI{memberAuth{mgr}, reg, st}
	g := engineGateway{mgr: mgr, reg: reg}
	if code, out := adminModel(t, a, "POST", "image", "",
		`{"id":"sdxl-base-1.0","kind":"checkpoint","base_model":"sdxl",
		  "files":[{"s3Key":"checkpoints/sd_xl_base_1.0.safetensors"}]}`); code != http.StatusOK {
		t.Fatalf("POST model = %d (%v)", code, out)
	}
	if code, out := adminModel(t, a, "PUT", "image", "sdxl-base-1.0", `{"enabled":true,"selected":true}`); code != http.StatusOK {
		t.Fatalf("PUT model = %d (%v)", code, out)
	}
	e.catalog.invalidate()

	tn, err := st.CreateTenant(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(email, slug string) string {
		ident, err := st.UpsertIdentity(ctx, email, slug, "")
		if err != nil {
			t.Fatal(err)
		}
		mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		return mintEngineSessionToken(reg.signKey, mem.ID, "s-1", "image", time.Now().Add(time.Hour))
	}
	alice, bob := mk("alice@example.invalid", "alice-example-invalid"), mk("bob@example.invalid", "bob-example-invalid")

	call := func(tok, method, path, rawQuery, body string) (int, string) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/engine/image/v1/"+path, strings.NewReader(body))
		r.URL.RawQuery = rawQuery
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-AF-Model", "sdxl-base-1.0")
		r.SetPathValue("key", "image")
		r.SetPathValue("path", path)
		g.serve(rec, r)
		return rec.Code, rec.Body.String()
	}

	// Alice submits, polls and fetches her own picture.
	if code, out := call(alice, "POST", "prompt", "", `{"prompt":{}}`); code != 200 || !strings.Contains(out, "pid-a") {
		t.Fatalf("alice submit = %d %s", code, out)
	}
	if code, out := call(alice, "GET", "history/pid-a", "", ""); code != 200 || !strings.Contains(out, "out-pid-a.png") {
		t.Fatalf("alice history = %d %s", code, out)
	}
	if code, out := call(alice, "GET", "view", "filename=out-pid-a.png&subfolder=&type=output", ""); code != 200 || out != "PNG:out-pid-a.png" {
		t.Fatalf("alice view = %d %s", code, out)
	}
	if code, _ := call(alice, "POST", "upload/image", "", `x`); code != 200 {
		t.Fatalf("upload = %d", code)
	}
	// Bob submits his own.
	if code, out := call(bob, "POST", "prompt", "", `{"prompt":{}}`); code != 200 || !strings.Contains(out, "pid-b") {
		t.Fatalf("bob submit = %d %s", code, out)
	}

	before := len(stub.paths())
	refused := []struct{ who, method, path, q, body, why string }{
		{bob, "GET", "history/pid-a", "", "", "another member's history"},
		{bob, "GET", "view", "filename=out-pid-a.png&subfolder=&type=output", "", "another member's output"},
		{bob, "GET", "view", "filename=out-pid-b.png&subfolder=&type=output", "", "an output never read through history"},
		{bob, "POST", "interrupt", "", `{"prompt_id":"pid-a"}`, "interrupt of another's prompt"},
		{bob, "POST", "interrupt", "", ``, "bare interrupt"},
		{bob, "POST", "queue", "", `{"delete":["pid-a"]}`, "queue delete of another's prompt"},
		{bob, "POST", "queue", "", `{"clear":true}`, "queue clear"},
		{bob, "GET", "history", "", "", "history-wide read"},
		{bob, "GET", "history/pid-unknown", "", "", "an id the map never saw"},
		{bob, "POST", "free", "", `{"unload_models":true}`, "free"},
		{bob, "GET", "object_info", "", "", "an unlisted route"},
		{alice, "DELETE", "prompt", "", "", "a wrong method on a listed route"},
		{alice, "GET", "history/pid-a/../pid-b", "", "", "a traversal-shaped id"},
	}
	for _, c := range refused {
		if code, _ := call(c.who, c.method, c.path, c.q, c.body); code != http.StatusForbidden {
			t.Errorf("%s: %s %s = %d, want 403", c.why, c.method, c.path, code)
		}
	}
	if after := stub.paths(); len(after) != before {
		t.Errorf("refused requests reached the engine: %v", after[before:])
	}

	// The queue answer is cut down to the caller's own entries.
	if code, out := call(alice, "GET", "queue", "", ""); code != 200 || !strings.Contains(out, "pid-a") || strings.Contains(out, "pid-b") {
		t.Errorf("alice queue = %d %s, want only her own entry", code, out)
	}
	// Control of her own job still works.
	if code, _ := call(alice, "POST", "interrupt", "", `{"prompt_id":"pid-a"}`); code != 200 {
		t.Errorf("alice interrupt of her own prompt = %d", code)
	}
	if code, _ := call(alice, "POST", "queue", "", `{"delete":["pid-a"]}`); code != 200 {
		t.Errorf("alice queue delete of her own prompt = %d", code)
	}

	// A restart empties the owner map: old jobs are refused, new ones work.
	e.jobs = newEngineJobOwners()
	if code, _ := call(alice, "GET", "history/pid-a", "", ""); code != http.StatusForbidden {
		t.Errorf("history of a pre-restart job = %d, want 403", code)
	}
	if code, _ := call(alice, "POST", "prompt", "", `{"prompt":{}}`); code != 200 {
		t.Errorf("submit after restart = %d", code)
	}
	if code, _ := call(alice, "GET", "history/pid-c", "", ""); code != 200 {
		t.Errorf("history of a post-restart job = %d", code)
	}
}
