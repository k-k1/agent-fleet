package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// comfyOwnerStub is a ComfyUI that numbers its prompts and answers the routes the image
// features use. It records every upstream path so a refused request can be shown never to have
// reached the engine.
type comfyOwnerStub struct {
	mu       sync.Mutex
	hits     []string
	bodies   map[string]string // "METHOD path" -> last body
	prefixes map[string]string // prompt id -> SaveImage prefix it was submitted with
	n        int
}

func (s *comfyOwnerStub) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...)
}

func (s *comfyOwnerStub) lastBody(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[key]
}

func (s *comfyOwnerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/system_stats" {
		_, _ = w.Write([]byte(`{"system":{}}`))
		return
	}
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	if s.bodies == nil {
		s.bodies, s.prefixes = map[string]string{}, map[string]string{}
	}
	s.hits = append(s.hits, r.Method+" "+r.URL.Path)
	s.bodies[r.Method+" "+r.URL.Path] = string(raw)
	s.mu.Unlock()
	switch {
	case r.Method == "POST" && r.URL.Path == "/prompt":
		var doc struct {
			Prompt map[string]struct {
				Inputs map[string]any `json:"inputs"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(raw, &doc)
		s.mu.Lock()
		s.n++
		id := "pid-" + string(rune('a'+s.n-1))
		for _, n := range doc.Prompt {
			if p, ok := n.Inputs["filename_prefix"].(string); ok {
				s.prefixes[id] = p
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": id})
	case r.Method == "POST" && r.URL.Path == "/upload/image":
		r.Body = io.NopCloser(bytes.NewReader(raw))
		_ = r.ParseMultipartForm(1 << 20)
		_, fh, _ := r.FormFile("image")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": fh.Filename, "subfolder": r.FormValue("subfolder"), "type": r.FormValue("type")})
	case strings.HasPrefix(r.URL.Path, "/history/"):
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		s.mu.Lock()
		prefix := s.prefixes[id]
		s.mu.Unlock()
		sub, base := "", prefix
		if i := strings.LastIndex(prefix, "/"); i >= 0 {
			sub, base = prefix[:i], prefix[i+1:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{id: map[string]any{
			"status":  map[string]any{"completed": true},
			"outputs": map[string]any{"9": map[string]any{"images": []map[string]any{{"filename": base + "-" + id + ".png", "subfolder": sub, "type": "output"}}}},
		}})
	case r.URL.Path == "/view":
		_, _ = w.Write([]byte("PNG:" + r.URL.Query().Get("filename")))
	case r.URL.Path == "/queue" && r.Method == "GET":
		_, _ = w.Write([]byte(`{"queue_running":[[0,"pid-a",{},{},[]]],"queue_pending":[[1,"pid-b",{},{},[]]]}`))
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

func comfyTestPrompt(extra string) string {
	return `{"prompt":{"1":{"class_type":"SaveImage","inputs":{"filename_prefix":"af-x","images":["2",0]}},` +
		`"2":{"class_type":"VAEDecode","inputs":{}}` + extra + `},"client_id":"af-agent"}`
}

func comfyTestUpload(t *testing.T, fields map[string]string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("image", "hash.png")
	_, _ = part.Write([]byte("png-bytes"))
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return buf.String(), mw.FormDataContentType()
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

	callCT := func(tok, method, path, rawQuery, body, ctype string) (int, string) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/engine/image/v1/"+path, strings.NewReader(body))
		r.URL.RawQuery = rawQuery
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-AF-Model", "sdxl-base-1.0")
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		r.SetPathValue("key", "image")
		r.SetPathValue("path", path)
		g.serve(rec, r)
		return rec.Code, rec.Body.String()
	}
	call := func(tok, method, path, rawQuery, body string) (int, string) {
		return callCT(tok, method, path, rawQuery, body, "")
	}
	upload := func(tok string, fields map[string]string) (int, string) {
		b, ct := comfyTestUpload(t, fields)
		return callCT(tok, "POST", "upload/image", "", b, ct)
	}

	// Alice uploads an input (her file lands in her own folder), submits, polls and fetches.
	code, out := upload(alice, nil)
	var upl struct{ Name, Subfolder string }
	_ = json.Unmarshal([]byte(out), &upl)
	if code != 200 || upl.Name != "hash.png" || !strings.HasPrefix(upl.Subfolder, "af-") {
		t.Fatalf("alice upload = %d %s", code, out)
	}
	aliceRef := upl.Subfolder + "/" + upl.Name
	load := func(ref string) string {
		return `,"3":{"class_type":"LoadImage","inputs":{"image":"` + ref + `"}}`
	}
	if code, out := call(alice, "POST", "prompt", "", comfyTestPrompt(load(aliceRef))); code != 200 || !strings.Contains(out, "pid-a") {
		t.Fatalf("alice submit = %d %s", code, out)
	}
	code, out = call(alice, "GET", "history/pid-a", "", "")
	var hist map[string]struct {
		Outputs map[string]struct {
			Images []struct{ Filename, Subfolder, Type string }
		}
	}
	_ = json.Unmarshal([]byte(out), &hist)
	img := hist["pid-a"].Outputs["9"].Images
	if code != 200 || len(img) != 1 || img[0].Subfolder != upl.Subfolder {
		t.Fatalf("alice history = %d %s (outputs must land in her own folder)", code, out)
	}
	aliceView := "filename=" + img[0].Filename + "&subfolder=" + img[0].Subfolder + "&type=output"
	if code, out := call(alice, "GET", "view", aliceView, ""); code != 200 || out != "PNG:"+img[0].Filename {
		t.Fatalf("alice view = %d %s", code, out)
	}
	// Bob submits his own (no file input).
	if code, out := call(bob, "POST", "prompt", "", comfyTestPrompt("")); code != 200 || !strings.Contains(out, "pid-b") {
		t.Fatalf("bob submit = %d %s", code, out)
	}

	before := len(stub.paths())
	refused := []struct{ who, method, path, q, body, why string }{
		{bob, "GET", "history/pid-a", "", "", "another member's history"},
		{bob, "GET", "view", aliceView, "", "another member's output"},
		{bob, "POST", "interrupt", "", `{"prompt_id":"pid-a"}`, "interrupt of another's prompt"},
		{bob, "POST", "interrupt", "", ``, "bare interrupt"},
		{bob, "POST", "interrupt", "", `{"PROMPT_ID":"pid-b","prompt_id":"pid-a"}`, "interrupt with a second key"},
		{bob, "POST", "interrupt", "", `{"Prompt_Id":"pid-b"}`, "interrupt with a differently cased key"},
		{bob, "POST", "queue", "", `{"delete":["pid-a"]}`, "queue delete of another's prompt"},
		{bob, "POST", "queue", "", `{"clear":true}`, "queue clear"},
		{bob, "GET", "history", "", "", "history-wide read"},
		{bob, "GET", "history/pid-unknown", "", "", "an id the map never saw"},
		{bob, "POST", "free", "", `{"unload_models":true}`, "free"},
		{bob, "GET", "object_info", "", "", "an unlisted route"},
		{alice, "DELETE", "prompt", "", "", "a wrong method on a listed route"},
		{alice, "GET", "history/pid-a/../pid-b", "", "", "a traversal-shaped id"},
		{bob, "POST", "prompt", "", `{"prompt_id":"pid-a","prompt":{"1":{"class_type":"SaveImage","inputs":{"filename_prefix":"x"}}}}`, "a caller-chosen prompt id"},
		{bob, "POST", "prompt", "", comfyTestPrompt(load(aliceRef)), "a graph loading another member's upload"},
		{bob, "POST", "prompt", "", comfyTestPrompt(load("other.png")), "a graph loading an unknown file"},
		{bob, "POST", "prompt", "", comfyTestPrompt(load(aliceRef + " [output]")), "an annotated image reference"},
		{bob, "POST", "prompt", "", comfyTestPrompt(`,"3":{"class_type":"SomeCustomFileReader","inputs":{}}`), "an unknown node class"},
	}
	for _, c := range refused {
		if code, _ := call(c.who, c.method, c.path, c.q, c.body); code != http.StatusForbidden {
			t.Errorf("%s: %s %s = %d, want 403", c.why, c.method, c.path, code)
		}
	}
	for _, f := range []map[string]string{{"type": "output"}, {"type": "temp"}, {"overwrite": "true"}} {
		if code, _ := upload(bob, f); code != http.StatusForbidden {
			t.Errorf("upload with %v = %d, want 403", f, code)
		}
	}
	if after := stub.paths(); len(after) != before {
		t.Errorf("refused requests reached the engine: %v", after[before:])
	}

	// What reaches the engine is the validated rewrite: SaveImage is confined to the caller's
	// folder, and a control body carries only the approved id.
	if p := stub.lastBody("POST /prompt"); !strings.Contains(p, `"filename_prefix":"af-`) || !strings.Contains(p, "/af-x") {
		t.Errorf("SaveImage prefix was not confined: %s", p)
	}
	if code, _ := call(bob, "POST", "interrupt", "", `{ "prompt_id" : "pid-b" }`); code != 200 {
		t.Errorf("bob interrupt of his own prompt = %d", code)
	}
	if got := stub.lastBody("POST /interrupt"); got != `{"prompt_id":"pid-b"}` {
		t.Errorf("interrupt body forwarded = %s, want the canonical one", got)
	}

	// The queue answer is cut down to the caller's own entries.
	if code, out := call(alice, "GET", "queue", "", ""); code != 200 || !strings.Contains(out, "pid-a") || strings.Contains(out, "pid-b") {
		t.Errorf("alice queue = %d %s, want only her own entry", code, out)
	}
	if code, _ := call(alice, "POST", "queue", "", `{"delete":["pid-a"]}`); code != 200 {
		t.Errorf("alice queue delete of her own prompt = %d", code)
	}

	// A restart empties the owner map: old jobs are refused, new ones work.
	e.jobs = newEngineJobOwners()
	if code, _ := call(alice, "GET", "history/pid-a", "", ""); code != http.StatusForbidden {
		t.Errorf("history of a pre-restart job = %d, want 403", code)
	}
	if code, _ := call(alice, "POST", "prompt", "", comfyTestPrompt("")); code != 200 {
		t.Errorf("submit after restart = %d", code)
	}
	if code, _ := call(alice, "GET", "history/pid-c", "", ""); code != 200 {
		t.Errorf("history of a post-restart job = %d", code)
	}
}

// A submission answered with an id another membership already holds does not move ownership.
func TestComfyJobOwnersNeverTakeOver(t *testing.T) {
	o := newEngineJobOwners()
	o.addPrompt("M-A", "p1")
	o.addPrompt("M-B", "p1")
	if !o.ownsPrompt("M-A", "p1") || o.ownsPrompt("M-B", "p1") {
		t.Fatal("a later submitter took over an existing prompt id")
	}
}

// The owner maps hold a fixed number of entries however fast they are filled.
func TestComfyJobOwnersAreBounded(t *testing.T) {
	o := newEngineJobOwners()
	o.max = 20
	for i := 0; i < 500; i++ {
		id := "p" + strings.Repeat("x", i%7) + string(rune('A'+i%26)) + time.Duration(i).String()
		o.addPrompt("M", id)
		o.addFile("M", "output", "af-x", id)
		if len(o.prompts) > o.max || len(o.files) > o.max {
			t.Fatalf("after %d inserts: %d prompts, %d files, cap %d", i+1, len(o.prompts), len(o.files), o.max)
		}
	}
	o.addPrompt("M", "fresh")
	if !o.ownsPrompt("M", "fresh") {
		t.Error("the newest entry was evicted")
	}
}

// Every node class the Agent's workflow builders can emit is in the allow-list. The Agent is a
// separate Go module, so this scans its sources: both literal ClassType values and the class
// names handed to comfyRequestLatent, which builds a node from its argument.
func TestComfyGraphClassesCoverTheAgentTemplates(t *testing.T) {
	files, err := filepath.Glob("../workspace/agent/internal/imagegen/comfy*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Agent workflow sources found (%v)", err)
	}
	re := regexp.MustCompile(`ClassType:\s*"([A-Za-z0-9_]+)"|"class_type":\s*"([A-Za-z0-9_]+)"|comfyRequestLatent\(.*, "([A-Za-z0-9_]+)"\)`)
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			for _, c := range m[1:] {
				if c != "" {
					seen[c] = true
				}
			}
		}
	}
	if len(seen) < 30 || !seen["EmptySD3LatentImage"] || !seen["EmptyFlux2LatentImage"] {
		t.Fatalf("the scan found %d classes and missed the dynamic latent ones: %v", len(seen), seen)
	}
	for c := range seen {
		if !comfyGraphClasses[c] {
			t.Errorf("the Agent emits node class %s, which the gateway would refuse", c)
		}
	}
}
