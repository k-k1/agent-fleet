package mcpx

// The per-call identity a Managed opencode session delivers through the AF plugin (#989):
// opencode stamps its own session id on each af tools/call as mcpCallerSIDArg, and
// mcpOwningSession may use it only as a key into AF's slot → opencode-session mapping.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// callerTestEnv is ownerTestEnv plus an isolated HOME, so the opencode sid store the
// resolution reads is this test's own, with the conditions under which a stamp is believed:
// the caller plugin installed and af under a rotated name.
func callerTestEnv(t *testing.T, alive map[string]bool) (cwd string, probed *[]string) {
	t.Helper()
	callerTrustEnv(t)
	cwd, probed = ownerTestEnv(t, alive)
	return cwd, probed
}

func callerTrustEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	plugin := filepath.Join(home, ".config", "opencode", "plugin")
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "agent-fleet-caller.js"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldName, oldCaller := mcpAFServerName, mcpCallerSID
	mcpAFServerName = func() string { return "af_0123abcd" }
	t.Cleanup(func() { mcpAFServerName, mcpCallerSID = oldName, oldCaller })
}

// writeOpencodeSlot records an opencode session in dir and the opencode session id AF mapped
// its slot to — what the Managed driver writes when it creates the conversation.
func writeOpencodeSlot(t *testing.T, name, dir, ses string) {
	t.Helper()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindOpencode})
	if ses != "" {
		agents.NewSidStore("opencode-sid").Write(session.UUID(dir, name), ses)
	}
}

// The acceptance shape: two live Managed opencode sessions in one worktree. Without the
// stamp this is the ambiguity refusal; with it each call resolves to its own session.
func TestMCPOwningSessionResolvesStampedOpencodeCaller(t *testing.T) {
	cwd, _ := callerTestEnv(t, map[string]bool{"ocfirst": true, "ocsecond": true})
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	writeOpencodeSlot(t, "ocsecond", cwd, "ses_second")

	for ses, want := range map[string]string{"ses_first": "ocfirst", "ses_second": "ocsecond"} {
		mcpCallerSID = ses
		got, err := mcpOwningSession()
		if err != nil || got != want {
			t.Fatalf("stamp %s: mcpOwningSession() = %q, %v; want %q", ses, got, err, want)
		}
	}

	mcpCallerSID = ""
	if got, err := mcpOwningSession(); err == nil {
		t.Fatalf("unstamped call resolved to %q; want the pre-existing ambiguity refusal", got)
	}
}

// A stamp is a claim, not a name: one that matches nothing AF mapped, or matches a session
// the caller cannot be, must leave the answer exactly where the cwd fallback puts it.
func TestMCPOwningSessionDoesNotTrustUnverifiedStamp(t *testing.T) {
	cases := []struct {
		name  string
		stamp string
	}{
		{"forged id", "ses_forged"},
		{"stopped session's id", "ses_dead"},
		{"another folder's id", "ses_elsewhere"},
		{"a non-opencode slot's id", "ses_codex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cwd, _ := callerTestEnv(t, map[string]bool{"livea": true, "liveb": true, "deadsess": false, "elsewhere": true, "codexmate": true})
			writeOpencodeSlot(t, "livea", cwd, "ses_a")
			writeOpencodeSlot(t, "liveb", cwd, "ses_b")
			writeOpencodeSlot(t, "deadsess", cwd, "ses_dead")
			writeOpencodeSlot(t, "elsewhere", cwd+"-other", "ses_elsewhere")
			session.WriteMeta(session.Meta{Name: "codexmate", Dir: cwd, Kind: "codex"})
			agents.NewSidStore("opencode-sid").Write(session.UUID(cwd, "codexmate"), "ses_codex")

			mcpCallerSID = c.stamp
			got, err := mcpOwningSession()
			if err == nil {
				t.Fatalf("stamp %q resolved to %q; want the cwd fallback's ambiguity refusal", c.stamp, got)
			}
		})
	}
}

// An unverified stamp falls back rather than failing: the cwd answer the call had before the
// plugin existed must still be reached.
func TestMCPOwningSessionUnmatchedStampKeepsCwdFallback(t *testing.T) {
	cwd, _ := callerTestEnv(t, map[string]bool{"livesess": true, "deadsess": false})
	writeOpencodeSlot(t, "livesess", cwd, "ses_live")
	writeOpencodeSlot(t, "deadsess", cwd, "ses_dead")

	mcpCallerSID = "ses_forged"
	got, err := mcpOwningSession()
	if err != nil || got != "livesess" {
		t.Fatalf("mcpOwningSession() = %q, %v; want the cwd fallback's live session", got, err)
	}
}

// A slot launched into a subdir runs its MCP child there, not in the worktree root.
func TestMCPOwningSessionMatchesStampFromSubdir(t *testing.T) {
	root, _ := callerTestEnv(t, map[string]bool{"subsess": true, "rootsess": true})
	sub := root + "/pkg"
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "subsess", Dir: root, Subdir: "pkg", Kind: session.KindOpencode})
	agents.NewSidStore("opencode-sid").Write(session.UUID(root, "subsess"), "ses_sub")
	writeOpencodeSlot(t, "rootsess", root, "ses_root")
	t.Chdir(sub)

	mcpCallerSID = "ses_sub"
	got, err := mcpOwningSession()
	if err != nil || got != "subsess" {
		t.Fatalf("mcpOwningSession() = %q, %v; want the subdir slot", got, err)
	}
}

// AF_SESSION_NAME is a per-process fact the Agent put there; a per-call claim never overrides it.
func TestMCPOwningSessionEnvBeatsStamp(t *testing.T) {
	cwd, probed := callerTestEnv(t, map[string]bool{"ocfirst": true})
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	mcpSourceSession = "named01"
	mcpCallerSID = "ses_first"

	got, err := mcpOwningSession()
	if err != nil || got != "named01" {
		t.Fatalf("mcpOwningSession() = %q, %v; want the AF_SESSION_NAME slot", got, err)
	}
	if len(*probed) != 0 {
		t.Fatalf("liveness probes = %v, want none when AF_SESSION_NAME decides", *probed)
	}
}

func TestTakeCallerSID(t *testing.T) {
	plain := json.RawMessage(`{"b":1, "a":"x"}`)
	if rest, sid := takeCallerSID(plain); sid != "" || !bytes.Equal(rest, plain) {
		t.Fatalf("unstamped args = %s, %q; want them byte-for-byte and no id", rest, sid)
	}
	rest, sid := takeCallerSID(json.RawMessage(`{"a":"x","_af_caller_sid":"ses_1"}`))
	if sid != "ses_1" || string(rest) != `{"a":"x"}` {
		t.Fatalf("stamped args = %s, %q; want the key removed and its value returned", rest, sid)
	}
	rest, sid = takeCallerSID(json.RawMessage(`{"a":"x","_af_caller_sid":7}`))
	if sid != "" || strings.Contains(string(rest), mcpCallerSIDArg) {
		t.Fatalf("non-string stamp = %s, %q; want it removed and ignored", rest, sid)
	}
	for _, in := range []string{``, `null`, `[1]`} {
		if rest, sid := takeCallerSID(json.RawMessage(in)); sid != "" || string(rest) != in {
			t.Fatalf("args %q = %s, %q; want them untouched", in, rest, sid)
		}
	}
}

// End to end through tools/call: the stamp decides the owner, never reaches the Agent as an
// argument, and does not outlive the call it arrived on.
func TestMCPStdioCallUsesAndStripsCallerStamp(t *testing.T) {
	callerTrustEnv(t)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	withMCPFlags(t, false, true, true)
	oldSource, oldCaller := mcpSourceSession, mcpCallerSID
	t.Cleanup(func() { mcpSourceSession, mcpCallerSID = oldSource, oldCaller })
	mcpSourceSession = ""
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	writeOpencodeSlot(t, "ocsecond", cwd, "ses_second")

	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			_, _ = fmt.Fprintf(w, `{"alive":true,"ready":true,"name":%q}`, path.Base(path.Dir(r.URL.Path)))
			return
		}
		lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"id":"ba_1","state":"attached","openUrl":"/open/browser-attachment/ba_1","viewer":false,"controlMode":"user-control","handoff":{"message":"m","completionLabel":"c","allowCancel":false,"controlMode":"user-control","result":"pending"}}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	_ = structuredMCPValue(t, callChromiumMCP(t, "request_browser_action", map[string]any{
		"attachment_id": "ba_1", "message": "m", mcpCallerSIDArg: "ses_second",
	}))
	if lastBody["sessionName"] != "ocsecond" {
		t.Fatalf("handoff REST body = %#v, want sessionName resolved from the stamp", lastBody)
	}
	for k := range lastBody {
		if strings.Contains(k, "caller") {
			t.Fatalf("handoff REST body carries %q: the stamp must not travel past the server", k)
		}
	}
	if mcpCallerSID != "" {
		t.Fatalf("mcpCallerSID = %q after the call; a stamp must not leak into the next call", mcpCallerSID)
	}
}

// callerImageGenAgent serves session liveness, /imagegen/status and /imagegen/generate, and
// records which session each generate was filed under and which session the status was asked for.
func callerImageGenAgent(t *testing.T, alive map[string]bool) (generatedFor, statusFor *[]string) {
	t.Helper()
	var gen, stat []string
	var mu sync.Mutex // the watcher test hits this from two goroutines at once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/status") && strings.HasPrefix(r.URL.Path, "/sessions/"):
			a, ok := alive[path.Base(path.Dir(r.URL.Path))]
			if !ok {
				http.Error(w, `{"code":"boom"}`, http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintf(w, `{"alive":%t,"ready":%t}`, a, a)
		case r.URL.Path == "/imagegen/status":
			stat = append(stat, r.URL.Query().Get("session"))
			_, _ = w.Write([]byte(`{"enabled":true,"ready":true,"kind":"opencode","providers":[{"id":"agy","ops":["generate"]}]}`))
		case r.URL.Path == "/imagegen/generate":
			var body struct {
				Session string `json:"session"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gen = append(gen, body.Session)
			_, _ = w.Write([]byte(`{"files":[{"path":"/tmp/i.png","name":"i.png","mime":"image/png","bytes":1}],"provider":"agy"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	return &gen, &stat
}

func callerListNames(t *testing.T) map[string]bool {
	t.Helper()
	res := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	out := map[string]bool{}
	result, _ := res["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, tool := range tools {
		if m, ok := tool.(map[string]any); ok {
			name, _ := m["name"].(string)
			out[name] = true
		}
	}
	return out
}

// The acceptance shape for generate_image: tools/list carries no stamp, so in a folder whose
// shared child serves two live Managed opencode sessions the owner is ambiguous at list time.
// The tool is still offered, and each call lands under the session that stamped it.
func TestGenerateImageOfferedToSharedOpencodeChildAndFiledByStamp(t *testing.T) {
	callerTrustEnv(t)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	withImageGen(t, true)
	oldCaller := mcpCallerSID
	t.Cleanup(func() { mcpCallerSID = oldCaller })
	mcpSourceSession = ""
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	writeOpencodeSlot(t, "ocsecond", cwd, "ses_second")
	generated, _ := callerImageGenAgent(t, map[string]bool{"ocfirst": true, "ocsecond": true})

	if !callerListNames(t)[mcpToolGenerateImage] {
		t.Fatal("generate_image not offered to a child shared by two live opencode sessions")
	}
	for _, ses := range []string{"ses_second", "ses_first"} {
		resp := callGenerateImage(t, map[string]any{"prompt": "a cat", mcpCallerSIDArg: ses})
		if strings.Contains(resp, `"isError":true`) {
			t.Fatalf("stamp %s: generate_image refused: %s", ses, resp)
		}
	}
	if strings.Join(*generated, ",") != "ocsecond,ocfirst" {
		t.Fatalf("generations filed under %v, want each stamping session in turn", *generated)
	}

	// Without the stamp (plugin removed) the call is refused with the ambiguity, never filed
	// under a guess.
	resp := callGenerateImage(t, map[string]any{"prompt": "a cat"})
	if !strings.Contains(resp, `"isError":true`) || len(*generated) != 2 {
		t.Fatalf("unstamped call = %s (filed %v); want a refusal and nothing generated", resp, *generated)
	}
}

// The list-time offer is only made where every call CAN carry a stamp: a live session of
// another kind in the folder, a failed liveness probe, or only studio sessions keep the tool
// off the list as before.
func TestGenerateImageNotOfferedWhereTheStampCannotDecide(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, cwd string)
		alive map[string]bool
	}{
		{
			name: "a live codex session shares the folder",
			setup: func(t *testing.T, cwd string) {
				writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
				session.WriteMeta(session.Meta{Name: "codexmate", Dir: cwd, Kind: "codex"})
			},
			alive: map[string]bool{"ocfirst": true, "codexmate": true},
		},
		{
			name: "liveness cannot be read",
			setup: func(t *testing.T, cwd string) {
				writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
				writeOpencodeSlot(t, "ocbroken", cwd, "ses_broken")
			},
			alive: map[string]bool{"ocfirst": true},
		},
		{
			name: "every live session is a studio session",
			setup: func(t *testing.T, cwd string) {
				for _, n := range []string{"ocfirst", "ocsecond"} {
					session.WriteMeta(session.Meta{Name: n, Dir: cwd, Kind: session.KindOpencode, Studio: "st-" + n})
				}
			},
			alive: map[string]bool{"ocfirst": true, "ocsecond": true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callerTrustEnv(t)
			t.Setenv("AF_SESSIONS_DIR", t.TempDir())
			t.Chdir(t.TempDir())
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			withImageGen(t, true)
			mcpSourceSession = ""
			tc.setup(t, cwd)
			callerImageGenAgent(t, tc.alive)
			if callerListNames(t)[mcpToolGenerateImage] {
				t.Fatal("generate_image offered where no call could tell its caller apart")
			}
		})
	}
}

// A stamp is believed only where it can only have come from the plugin. With the plugin gone,
// or af under the legacy bare name the plugin does not recognise, the model's own value arrives
// untouched — naming a live neighbour's id must not make the call that neighbour's.
func TestMCPOwningSessionIgnoresStampThePluginDidNotGuarantee(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T)
	}{
		{"plugin removed", func(t *testing.T) {
			if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".config", "opencode", "plugin", "agent-fleet-caller.js")); err != nil {
				t.Fatal(err)
			}
		}},
		{"legacy af server name", func(t *testing.T) { mcpAFServerName = func() string { return "af" } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, _ := callerTestEnv(t, map[string]bool{"ocfirst": true, "ocsecond": true})
			writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
			writeOpencodeSlot(t, "ocsecond", cwd, "ses_second")
			mcpCallerSID = "ses_second"
			if got, err := mcpOwningSession(); err != nil || got != "ocsecond" {
				t.Fatalf("control: trusted stamp resolved to %q, %v; want ocsecond", got, err)
			}
			tc.break_(t)
			if got, err := mcpOwningSession(); err == nil {
				t.Fatalf("untrusted stamp resolved to %q; want the cwd fallback's ambiguity refusal", got)
			}
			if got := mcpStampedFolderSessions(); got != nil {
				t.Fatalf("list-time offer made for %v with an untrusted stamp", got)
			}
		})
	}
}

// A live session AF has not mapped yet cannot be matched by any stamp, so a list-time offer
// would be refused on every one of its calls.
func TestGenerateImageNotOfferedWhileASessionIsUnmapped(t *testing.T) {
	cwd, _ := callerTestEnv(t, map[string]bool{"ocfirst": true, "ocfresh": true})
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	writeOpencodeSlot(t, "ocfresh", cwd, "")
	if got := mcpStampedFolderSessions(); got != nil {
		t.Fatalf("mcpStampedFolderSessions() = %v, want nil while ocfresh has no mapping", got)
	}
}

// The list belongs to the whole shared child. Building it while a stamped call is in flight —
// the watcher does exactly that beside a long generate_image — must neither read the stamp nor
// resolve to the caller.
func TestListOwnerNeverReadsTheCallStamp(t *testing.T) {
	cwd, _ := callerTestEnv(t, map[string]bool{"ocfirst": true, "ocsecond": true})
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	writeOpencodeSlot(t, "ocsecond", cwd, "ses_second")
	mcpCallerSID = "ses_second"
	if got, err := mcpListOwningSession(); err == nil {
		t.Fatalf("list-time owner = %q; a list must not take the in-flight call's stamp", got)
	}
}

// Run with -race: the watcher's list derivation beside stamped calls on the dispatch goroutine.
// CI does not pass -race, so there the guard is TestListOwnerNeverReadsTheCallStamp; this one
// is for a local `go test -race` (it reports the race in mcpCallerSession when the list path
// is pointed back at the stamp).
func TestToolListWatcherBesideStampedCallsIsRaceFree(t *testing.T) {
	callerTrustEnv(t)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	withImageGen(t, true)
	withMCPFlags(t, false, true, true)
	mcpSourceSession = ""
	writeOpencodeSlot(t, "ocfirst", cwd, "ses_first")
	writeOpencodeSlot(t, "ocsecond", cwd, "ses_second")
	callerImageGenAgent(t, map[string]bool{"ocfirst": true, "ocsecond": true})
	t.Cleanup(forgetAdvertised)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = mcpStdioToolList()
		}
	}()
	for i := 0; i < 20; i++ {
		_ = callGenerateImage(t, map[string]any{"prompt": "a cat", mcpCallerSIDArg: "ses_first"})
	}
	<-done
}
