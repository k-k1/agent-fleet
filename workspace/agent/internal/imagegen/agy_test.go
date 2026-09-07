package imagegen

// The agy route's unit suite. The real CLI is never driven from here: one run costs a real
// image against the user's Antigravity plan, and the end-to-end proof lives in
// imagegen_live_test.go behind AF_IMAGEGEN_LIVE=1.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeAgy writes a stand-in `agy` that reads the NDJSON prompt off stdin, emits the print-mode
// event stream, and drops files where the real CLI's generate_image does — under the ISOLATED
// home the provider just made, which the script finds through $HOME exactly as agy would.
//
// files is keyed by the path under brain/, so a test can place a picture in the conversation's
// directory and rubbish in the sub-directories agy keeps its own bookkeeping in.
func fakeAgy(t *testing.T, files map[string][]byte, stream string, exit int) (exe, argvLog, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "agy")
	argvLog = filepath.Join(dir, "argv.txt")
	stdinLog = filepath.Join(dir, "stdin.txt")

	var body bytes.Buffer
	body.WriteString("#!/bin/sh\n")
	// Both are recorded rather than discarded: the prompt must never reach argv (it would land
	// in every process listing on this host), and that is only checkable if the argv is kept.
	body.WriteString("printf '%s\\n' \"$@\" > '" + argvLog + "'\n")
	body.WriteString("cat > '" + stdinLog + "'\n")
	for name, data := range files {
		src := filepath.Join(dir, strings.ReplaceAll(name, "/", "_")+".src")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		full := "\"$HOME\"/.gemini/antigravity-cli/brain/" + name
		body.WriteString("mkdir -p \"$(dirname " + full + ")\"\n")
		body.WriteString("cp '" + src + "' " + full + "\n")
	}
	for _, ln := range bytes.Split([]byte(stream), []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		body.WriteString("cat <<'AFEOF'\n" + string(ln) + "\nAFEOF\n")
	}
	body.WriteString("exit " + strconv.Itoa(exit) + "\n")
	if err := os.WriteFile(exe, body.Bytes(), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe, argvLog, stdinLog
}

// The measured shape of a successful run (2026-09-07): input_tokens EXCLUDES the cached share,
// and output_tokens INCLUDES thinking_tokens.
const agyHappyStream = `{"event":"init","conversation_id":"conv-1","init":{"model":"m","tools":["generate_image"]}}
{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"generate_image"}}
{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"done"}}
{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"done","usage":{"input_tokens":26896,"output_tokens":75,"thinking_tokens":40,"cache_read_tokens":16289,"total_tokens":26971}}}`

// newAgyTestProvider is a provider whose token is a real file, so the symlink the isolated home
// depends on has something to point at.
func newAgyTestProvider(t *testing.T, exe string) *agyProvider {
	t.Helper()
	token := filepath.Join(t.TempDir(), "antigravity-oauth-token")
	if err := os.WriteFile(token, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &agyProvider{model: "m", exe: exe, token: token}
}

func TestAgyCollectsFromConversationDir(t *testing.T) {
	exe, _, _ := fakeAgy(t, map[string][]byte{
		"conv-1/af_generated_1788760465897.png": tinyPNG(t, 16, 9),
		// agy's own bookkeeping sits beside the picture. Collecting it would hand the caller a
		// text file as an image.
		"conv-1/.system_generated/steps/2/output.txt": []byte("Generated image is saved at ..."),
		"conv-1/scratch/notes.txt":                    []byte("scratch"),
		// Another conversation's output must be unreachable even inside our own home.
		"conv-2/other.png": tinyPNG(t, 4, 4),
	}, agyHappyStream, 0)
	p := newAgyTestProvider(t, exe)

	res, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want exactly this conversation's one", len(res.Images))
	}
	if res.Images[0].Width != 16 || res.Images[0].Height != 9 {
		t.Fatalf("dimensions = %dx%d, want them read from the file",
			res.Images[0].Width, res.Images[0].Height)
	}
	if res.Provider != ProviderAgy || res.Model != "m" {
		t.Fatalf("provenance = %s/%s", res.Provider, res.Model)
	}
	// input_tokens is already the fresh input on this route, and thinking is inside output.
	if !res.Usage.Measured || res.Usage.In != 26896 || res.Usage.Out != 75 || res.Usage.CacheRead != 16289 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

// The isolated home is the cleanup: nothing may be left for the next run to find, the way
// $CODEX_HOME/generated_images grew to 80 MB before the Codex provider swept it.
func TestAgyLeavesNoHomeBehind(t *testing.T) {
	exe, _, _ := fakeAgy(t, map[string][]byte{"conv-1/a.png": tinyPNG(t, 8, 8)}, agyHappyStream, 0)
	p := newAgyTestProvider(t, exe)
	before := tempEntries(t)
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"}); err != nil {
		t.Fatal(err)
	}
	for name := range tempEntries(t) {
		if !before[name] && strings.HasPrefix(name, "af-imagegen-agy-") {
			t.Fatalf("the throwaway home %s survived the run", name)
		}
	}
}

func tempEntries(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	ents, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		out[e.Name()] = true
	}
	return out
}

// The whole reason the route is worth having: the aspect ratio really does reach the tool, so
// it has to reach the PROMPT — and the prompt has to reach the child on stdin, never in argv.
func TestAgyPassesAspectRatioAndKeepsThePromptOffArgv(t *testing.T) {
	exe, argvLog, stdinLog := fakeAgy(t, map[string][]byte{"conv-1/a.png": tinyPNG(t, 16, 9)}, agyHappyStream, 0)
	p := newAgyTestProvider(t, exe)

	const secret = "a red maple leaf, flat vector style"
	if _, err := p.Generate(context.Background(), Request{
		Op: OpGenerate, Prompt: secret, AspectRatio: "16:9",
	}); err != nil {
		t.Fatal(err)
	}

	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), secret) {
		t.Fatalf("the prompt reached argv, where every process listing on the host can read it:\n%s", argv)
	}
	if !strings.Contains(string(argv), "stream-json") {
		t.Fatalf("argv = %s, want the stdin-reading print mode", argv)
	}

	in, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(in), &msg); err != nil {
		t.Fatalf("stdin was not agy's own message envelope: %v (%s)", err, in)
	}
	if msg.Event != "user" || !strings.Contains(msg.Message.Content, secret) {
		t.Fatalf("stdin message = %+v", msg)
	}
	if !strings.Contains(msg.Message.Content, "Set AspectRatio to 16:9") {
		t.Fatalf("the requested ratio never reached the prompt:\n%s", msg.Message.Content)
	}
	// The one instruction the honest-failure rule rests on.
	if !strings.Contains(msg.Message.Content, "Never draw, script or otherwise fabricate") {
		t.Fatalf("the prompt does not forbid fabricating a substitute:\n%s", msg.Message.Content)
	}
}

// The isolated home IS the sandbox: an empty MCP config so one picture does not spawn the
// user's whole materialized MCP fleet, and an allow-list of exactly one tool so print mode
// auto-denies everything else (measured: run_command comes back as a denied action).
func TestAgyIsolatedHomeIsTheSandbox(t *testing.T) {
	p := newAgyTestProvider(t, "agy")
	home, err := p.prepareHome()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)

	link := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	target, err := os.Readlink(link)
	if err != nil || target != p.token {
		t.Fatalf("token link = %q (%v), want a symlink to the user's real token", target, err)
	}

	var mcp struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	readJSON(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), &mcp)
	if len(mcp.MCPServers) != 0 {
		t.Fatalf("mcp servers = %v, want none — agy's MCP config is global-only", mcp.MCPServers)
	}

	var settings struct {
		Telemetry   bool     `json:"enableTelemetry"`
		Trusted     []string `json:"trustedWorkspaces"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	readJSON(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), &settings)
	if settings.Telemetry {
		t.Fatal("telemetry was left on")
	}
	if len(settings.Permissions.Allow) != 1 || settings.Permissions.Allow[0] != "generate_image" {
		t.Fatalf("allow = %v, want exactly the image tool", settings.Permissions.Allow)
	}
	if len(settings.Trusted) != 1 || settings.Trusted[0] != filepath.Join(home, "wd") {
		t.Fatalf("trusted workspaces = %v, want only the empty working dir", settings.Trusted)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// The honest failure: the model's prose says the picture exists, and no file does. agy even
// spells the path out in its tool result — which is exactly the contract this route refuses to
// depend on, because a driver model can break it silently.
func TestAgyFailsWhenNoFileAppeared(t *testing.T) {
	exe, _, _ := fakeAgy(t, nil,
		`{"event":"init","conversation_id":"conv-1"}
{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"Generated image is saved at /tmp/out.png.","usage":{"input_tokens":10,"output_tokens":2}}}`, 0)
	p := newAgyTestProvider(t, exe)

	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"})
	if err == nil {
		t.Fatal("a run that produced no file was reported as a success")
	}
	if !strings.Contains(err.Error(), "generated no image") {
		t.Fatalf("err = %v", err)
	}
}

// A denied tool is OUR configuration fault, not the model's refusal, and the two need opposite
// responses — so the action agy denied has to survive into the error.
func TestAgyReportsADeniedTool(t *testing.T) {
	exe, _, _ := fakeAgy(t, nil,
		`{"event":"init","conversation_id":"conv-1"}
{"event":"result","result":{"conversation_id":"conv-1","status":"CANCELED","usage":{"input_tokens":10,"output_tokens":2},"denied_actions":[{"action":"command","display_name":"RunCommand"}]}}`, 0)
	p := newAgyTestProvider(t, exe)

	_, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"})
	if err == nil || !strings.Contains(err.Error(), "RunCommand") {
		t.Fatalf("err = %v, want the denied action named", err)
	}
}

// Without a conversation id there is no directory that is provably this run's.
func TestAgyRefusesWithoutConversationID(t *testing.T) {
	exe, _, _ := fakeAgy(t, map[string][]byte{"conv-1/a.png": tinyPNG(t, 8, 8)},
		`{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":1,"output_tokens":1}}}`, 0)
	p := newAgyTestProvider(t, exe)
	if _, err := p.Generate(context.Background(), Request{Op: OpGenerate, Prompt: "a cat"}); err == nil {
		t.Fatal("output was collected with no conversation to attribute it to")
	}
}

// An unsupported ratio is dropped and reported, not sent: the parameter is an enum, and a value
// outside it would fail the whole call instead of producing a picture in the default ratio.
func TestAgyAspectRatioOutsideTheEnumIsReported(t *testing.T) {
	p := &agyProvider{}
	if r, w := p.pickAspectRatio(Request{AspectRatio: "16:9"}); r != "16:9" || w != "" {
		t.Fatalf("ratio = %q warning = %q, want it passed through", r, w)
	}
	r, w := p.pickAspectRatio(Request{AspectRatio: "21:9"})
	if r != "" || !strings.Contains(w, "21:9") || !strings.Contains(w, "16:9") {
		t.Fatalf("ratio = %q warning = %q, want it dropped and reported", r, w)
	}
	if r, w := p.pickAspectRatio(Request{AspectRatio: "auto"}); r != "" || w != "" {
		t.Fatalf("ratio = %q warning = %q, want auto to be silent", r, w)
	}
}

// The ratio is honoured approximately (measured: 16:9 came back 1376x768, 0.8% wide). Warning
// about that would train a caller to retry a generation that never comes out differently — and
// each retry spends the plan again. Ignoring the ratio outright still has to be reported.
func TestAgyProducedWarnings(t *testing.T) {
	req := Request{AspectRatio: "16:9"}
	if got := agyProducedWarnings(req, "16:9", []Image{{Width: 1376, Height: 768}}); len(got) != 0 {
		t.Fatalf("warnings = %v, want none for the measured 0.8%% miss", got)
	}
	got := agyProducedWarnings(req, "16:9", []Image{{Width: 1024, Height: 1024}})
	if len(got) != 1 || !strings.Contains(got[0], "1024x1024") {
		t.Fatalf("warnings = %v, want the ignored ratio reported", got)
	}
	// The two parameters this route simply does not have.
	got = agyProducedWarnings(Request{Size: "1024x1024", Background: "transparent"}, "", nil)
	if len(got) != 2 {
		t.Fatalf("warnings = %v, want both size and background reported as unavailable", got)
	}
}

// Ready is on the tools/list path, so it may only look at the filesystem — never spawn a CLI.
func TestAgyReadyIsTokenAndBinaryOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	exe, _, _ := fakeAgy(t, nil, "", 0)
	t.Setenv("PATH", filepath.Dir(exe)+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := &agyProvider{model: "m", exe: "agy"}
	if p.Ready(context.Background()) {
		t.Fatal("ready with no token on disk")
	}
	tok := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.MkdirAll(filepath.Dir(tok), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tok, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !p.Ready(context.Background()) {
		t.Fatal("not ready with the binary on PATH and a token on disk")
	}

	p2 := &agyProvider{model: "m", exe: "agy-that-does-not-exist"}
	if p2.Ready(context.Background()) {
		t.Fatal("ready with no binary")
	}
}

// A rotated token must survive the throwaway home. agy refreshes via tmp+rename, which replaces
// the symlink with a real file — deleting the home a moment later would drop the new token and
// leave the user's own as stale as it was.
func TestAgyFoldsARotatedTokenBack(t *testing.T) {
	p := newAgyTestProvider(t, "agy")
	home, err := p.prepareHome()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	link := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte(`{"rotated":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p.foldRotatedToken(home)
	b, err := os.ReadFile(p.token)
	if err != nil || !strings.Contains(string(b), "rotated") {
		t.Fatalf("shared token = %s (%v), want the refreshed one", b, err)
	}
}

// The mask goes on THIS child and nowhere else: whether the product should offer agy sessions
// on a host with no RDRAND is a separate decision, and this must not answer it by accident.
func TestAgyEnvCarriesTheMaskAndTheIsolatedHome(t *testing.T) {
	t.Setenv("HOME", "/home/real")
	t.Setenv("OPENSSL_ia32cap", "leftover")
	env := envWithHome("/tmp/iso")
	var home, mask int
	for _, e := range env {
		switch {
		case e == "HOME=/tmp/iso":
			home++
		case e == agyRDRANDMask:
			mask++
		case strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "OPENSSL_ia32cap="):
			t.Fatalf("a stale %q survived; exec does not dedupe, so the first one would win", e)
		}
	}
	if home != 1 || mask != 1 {
		t.Fatalf("env = %d homes, %d masks", home, mask)
	}
	if os.Getenv("HOME") != "/home/real" {
		t.Fatal("the Agent's own environment was modified")
	}
}
