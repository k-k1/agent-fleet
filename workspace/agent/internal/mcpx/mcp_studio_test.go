package mcpx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const testStudioID = "0b9d1f2e-7c4a-4e1b-9a3d-5f6e7a8b9c0d"

// bindStudioForTest writes a meta for name bound to testStudioID and the studio's own file
// naming name back, under a fresh HOME and sessions dir.
func bindStudioForTest(t *testing.T, name string, agentTrial bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	session.WriteMeta(session.Meta{Name: name, Kind: session.KindClaude, Studio: testStudioID})
	writeStudioFileForTest(t, studioFile{Session: name, AgentTrial: agentTrial})
}

func writeStudioFileForTest(t *testing.T, f studioFile) {
	t.Helper()
	if err := os.MkdirAll(paths.ImagegenStudiosDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(filepath.Join(paths.ImagegenStudiosDir(), testStudioID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func withSessionSurface(t *testing.T, source string) {
	t.Helper()
	oldWrite, oldSelfReport, oldChromium := writeEnabled(), selfReportOnly(), sessionChromiumEnabled()
	oldImageGen, oldSource := mcpImageGenEnabled, mcpSourceSession
	t.Cleanup(func() {
		setFlags(oldWrite, oldSelfReport, oldChromium)
		mcpImageGenEnabled, mcpSourceSession = oldImageGen, oldSource
	})
	setFlags(false, true, false)
	mcpImageGenEnabled = false
	mcpSourceSession = source
	stubAliveProbeFailing(t)
}

// stubAliveProbeFailing points the loopback Agent client at a server that fails every request,
// so nothing here reaches the real Agent on this host and the cwd guess cannot be narrowed.
func stubAliveProbeFailing(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"unexpected","message":"not stubbed"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
}

func studioAdvertised() map[string]bool {
	out := map[string]bool{}
	for _, tool := range mcpStdioToolList() {
		out[tool["name"].(string)] = true
	}
	return out
}

func TestStudioToolsFollowTheBinding(t *testing.T) {
	withSessionSurface(t, "slot01")
	studioTools := []string{"get_image_studio", "set_image_draft", "add_image_knowledge", "run_image_trial"}

	t.Run("bound, trials allowed", func(t *testing.T) {
		bindStudioForTest(t, "slot01", true)
		got := studioAdvertised()
		for _, n := range studioTools {
			if !got[n] {
				t.Errorf("%s not advertised to a bound session", n)
			}
		}
	})
	t.Run("bound, trials off", func(t *testing.T) {
		bindStudioForTest(t, "slot01", false)
		got := studioAdvertised()
		if got["run_image_trial"] || !got["get_image_studio"] {
			t.Errorf("advertised %v: want the studio tools without run_image_trial", got)
		}
	})
	t.Run("not bound", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
		session.WriteMeta(session.Meta{Name: "slot01", Kind: session.KindClaude})
		got := studioAdvertised()
		for _, n := range studioTools {
			if got[n] {
				t.Errorf("%s advertised to a session with no studio", n)
			}
		}
	})
}

// Unidentifiable: two sessions share the folder, one of them bound. The tools are offered and
// every call is refused with the reason.
func TestStudioToolsWhenTheSessionCannotBeTold(t *testing.T) {
	withSessionSurface(t, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	cwd, _ := os.Getwd()
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindOpencode, Dir: cwd, Studio: testStudioID})
	session.WriteMeta(session.Meta{Name: "b", Kind: session.KindOpencode, Dir: cwd})

	if !studioAdvertised()["get_image_studio"] {
		t.Fatal("studio tools hidden from an unidentifiable session whose folder has a bound session")
	}
	out := string(mcpStudioCall(mcpReq{ID: json.RawMessage("1")}, "get_image_studio", nil))
	if !strings.Contains(out, "特定できません") {
		t.Fatalf("call from an unidentifiable session = %s, want the identity refusal", out)
	}

	// With no bound session in the folder, nothing is offered.
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindOpencode, Dir: cwd})
	if studioAdvertised()["get_image_studio"] {
		t.Fatal("studio tools offered in a folder where no session is bound to a studio")
	}
}

// The studio side is the truth: a meta that still claims the studio after the studio moved on
// is refused, and so is a trial the studio does not allow.
func TestStudioCallChecksTheStudioSide(t *testing.T) {
	withSessionSurface(t, "slot01")
	bindStudioForTest(t, "slot01", false)
	id := json.RawMessage("1")
	if out := string(mcpStudioCall(mcpReq{ID: id}, "run_image_trial", nil)); !strings.Contains(out, "許可されていません") {
		t.Fatalf("trial with trials off = %s", out)
	}
	writeStudioFileForTest(t, studioFile{Session: "other", AgentTrial: true})
	if out := string(mcpStudioCall(mcpReq{ID: id}, "get_image_studio", nil)); !strings.Contains(out, "結びが変わりました") {
		t.Fatalf("call after the studio was rebound = %s", out)
	}
}

// generate_image is left out of a studio session's list AND refused on the call, because the
// call side checks the last tools/list, which can predate the binding.
func TestGenerateImageRefusedInAStudioSession(t *testing.T) {
	withSessionSurface(t, "slot01")
	mcpImageGenEnabled = true
	stubImageGenStatus(t, mcpImageGenStatus{
		Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"},
	})
	bindStudioForTest(t, "slot01", true)
	if studioAdvertised()[mcpToolGenerateImage] {
		t.Fatal("generate_image advertised to a studio session")
	}
	out := string(mcpGenerateImage(mcpReq{ID: json.RawMessage("1")}, imageGenArgs{prompt: "a cat"}))
	if !strings.Contains(out, "画像スタジオに結ばれています") {
		t.Fatalf("generate_image in a studio session = %s, want the studio refusal", out)
	}
}

// An agent's relative reference means its own working folder, not the browse root the Agent
// resolves relative paths against: the MCP child makes it absolute before relaying.
func TestRelativeReferencesAreMadeAbsoluteFromTheSessionFolder(t *testing.T) {
	cwd, _ := os.Getwd()
	want := filepath.Join(cwd, "docs/ref.png")

	t.Run("set_image_draft", func(t *testing.T) {
		withSessionSurface(t, "slot01")
		bindStudioForTest(t, "slot01", true)
		got := make(chan map[string]any, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			got <- body
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		u, _ := url.Parse(srv.URL)
		t.Setenv("AGENT_ADDR", u.Host)
		mcpStudioCall(mcpReq{ID: json.RawMessage("1")}, "set_image_draft",
			json.RawMessage(`{"prompt":"snow","inputs":["docs/ref.png","/abs/x.png"]}`))
		body := <-got
		draft, _ := body["draft"].(map[string]any)
		inputs, _ := draft["inputs"].([]any)
		if len(inputs) != 2 || inputs[0] != want || inputs[1] != "/abs/x.png" || draft["prompt"] != "snow" {
			t.Fatalf("relayed draft = %v, want inputs [%s /abs/x.png] and the rest untouched", draft, want)
		}
	})
	t.Run("generate_image", func(t *testing.T) {
		withSessionSurface(t, "slot01")
		mcpImageGenEnabled = true
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
		session.WriteMeta(session.Meta{Name: "slot01", Kind: session.KindClaude})
		got := make(chan map[string]any, 1)
		stubAgentForImageGen(t, mcpImageGenStatus{Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"edit"}},
			func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				got <- body
				http.Error(w, `{"error":{"code":"x","message":"stop here"}}`, http.StatusBadGateway)
			})
		mcpGenerateImage(mcpReq{ID: json.RawMessage("1")}, imageGenArgs{op: "edit", prompt: "snow", inputs: []string{"docs/ref.png"}, mask: "m.png"})
		body := <-got
		inputs, _ := body["inputs"].([]any)
		if len(inputs) != 1 || inputs[0] != want || body["mask"] != filepath.Join(cwd, "m.png") {
			t.Fatalf("relayed inputs/mask = %v / %v, want absolute from %s", body["inputs"], body["mask"], cwd)
		}
	})
}

// stubStudioAgent answers the press and a job list that reports the job in `states` order, one
// state per poll, and remembers the press body.
func stubStudioAgent(t *testing.T, states ...string) *[]string {
	t.Helper()
	var pressed []string
	poll := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/press"):
			b, _ := io.ReadAll(r.Body)
			pressed = append(pressed, r.URL.Path+" "+string(b))
			_, _ = w.Write([]byte(`{"version":"v3","group":"g1","jobs":[{"id":"j7","position":1}],"recorded":true}`))
		case r.URL.Path == "/imagegen/jobs":
			st := states[min(poll, len(states)-1)]
			poll++
			_, _ = w.Write([]byte(`{"jobs":[{"id":"j6","state":"done"},{"id":"j7","state":"` + st +
				`","files":[{"path":"/p/trial.png","seed":42}],"elapsed_ms":900,"error":"boom"}]}`))
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	oldWait, oldPoll := mcpStudioTrialWait, mcpStudioTrialPoll
	mcpStudioTrialPoll = time.Millisecond
	t.Cleanup(func() { mcpStudioTrialWait, mcpStudioTrialPoll = oldWait, oldPoll })
	return &pressed
}

// run_image_trial takes no arguments, presses as the agent, and waits for THAT job.
func TestRunImageTrialWaitsForItsJob(t *testing.T) {
	withSessionSurface(t, "slot01")
	bindStudioForTest(t, "slot01", true)
	pressed := stubStudioAgent(t, "queued", "running", "done")
	out := string(mcpStudioCall(mcpReq{ID: json.RawMessage("1")}, "run_image_trial", json.RawMessage(`{"prompt":"sneaky"}`)))
	if !strings.Contains(out, "/p/trial.png") || !strings.Contains(out, `v3`) {
		t.Fatalf("trial = %s", out)
	}
	var res struct {
		Result struct {
			Structured struct {
				Seed *int64 `json:"seed"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Result.Structured.Seed == nil || *res.Result.Structured.Seed != 42 {
		t.Errorf("the trial's own seed is not in the answer: %s", out)
	}
	if len(*pressed) != 1 || !strings.Contains((*pressed)[0], `"mode":"agent_trial"`) ||
		!strings.Contains((*pressed)[0], `"session":"slot01"`) || strings.Contains((*pressed)[0], "sneaky") {
		t.Fatalf("press = %v, want the agent trial with the session and no arguments of the call", *pressed)
	}
}

func TestRunImageTrialReportsAFailureAndATimeout(t *testing.T) {
	withSessionSurface(t, "slot01")
	bindStudioForTest(t, "slot01", true)
	stubStudioAgent(t, "running", "failed")
	if out := string(mcpStudioCall(mcpReq{ID: json.RawMessage("1")}, "run_image_trial", nil)); !strings.Contains(out, "boom") || !strings.Contains(out, "isError") {
		t.Fatalf("failed trial = %s", out)
	}
	stubStudioAgent(t, "waking")
	mcpStudioTrialWait = 20 * time.Millisecond
	out := string(mcpStudioCall(mcpReq{ID: json.RawMessage("1")}, "run_image_trial", nil))
	if !strings.Contains(out, "j7") || !strings.Contains(out, "get_image_studio") || strings.Contains(out, "isError") {
		t.Fatalf("slow trial = %s, want the job id and where the result will show", out)
	}
}

// A Managed create starts the CLI before the meta is written, and the CLI lists the tools at
// once: with no meta yet, the studio naming the session is what decides — studio tools offered,
// generate_image withheld. Measured with codex Managed, which never lists again.
func TestStudioToolsBeforeTheMetaIsWritten(t *testing.T) {
	withSessionSurface(t, "slot01")
	mcpImageGenEnabled = true
	stubImageGenStatus(t, mcpImageGenStatus{
		Enabled: true, Ready: true, Provider: "codex", Kind: "claude", Ops: []string{"generate"},
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	if !studioAdvertised()[mcpToolGenerateImage] {
		t.Fatal("generate_image not advertised without a studio: the check below would prove nothing")
	}
	writeStudioFileForTest(t, studioFile{Session: "slot01", AgentTrial: true})
	got := studioAdvertised()
	if !got["get_image_studio"] || !got["run_image_trial"] {
		t.Errorf("advertised %v: want the studio tools while the meta is not written yet", got)
	}
	if got["generate_image"] {
		t.Error("generate_image advertised to a session its studio names")
	}
	// Another session's studio is not this one's.
	writeStudioFileForTest(t, studioFile{Session: "slot02", AgentTrial: true})
	if studioAdvertised()["get_image_studio"] {
		t.Error("studio tools offered for a studio that names another session")
	}
}
