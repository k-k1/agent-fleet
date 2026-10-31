package chatx

// The deny-all one-shot route (chat_denyall.go). A fake `claude` on PATH stands in for the CLI:
// it logs every invocation (argv and the first stdin line) and prints whatever stream, stderr and
// exit status the test scripted for the probe (the first invocation) and for the real run (every
// later one). Nothing here starts a real CLI.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const secretPrompt = "TRANSCRIPT-MARKER please summarise"

const (
	initClean   = `{"type":"system","subtype":"init","tools":[],"mcp_servers":[],"model":"fake"}`
	probeResult = `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"result":"local","total_cost_usd":0,"usage":{}}`
	runResult   = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"FAKE-REPLY","total_cost_usd":0.001,"usage":{"input_tokens":10,"output_tokens":2}}`
	assistText  = `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`
)

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// fakeRun scripts one invocation.
type fakeRun struct {
	Out    string
	Stderr string
	Exit   int
	Sleep  int // seconds
}

var (
	cleanProbe = fakeRun{Out: lines(initClean, probeResult)}
	cleanRun   = fakeRun{Out: lines(initClean, assistText, runResult)}
)

// fakeDenyAllClaude installs a fake claude and returns the directory holding the invocation log.
func fakeDenyAllClaude(t *testing.T, probe, run fakeRun) string {
	t.Helper()
	bin, logDir := t.TempDir(), t.TempDir()
	for i, r := range []fakeRun{probe, run} {
		k := i + 1
		must := func(name, body string) {
			if err := os.WriteFile(filepath.Join(logDir, fmt.Sprintf("%s.%d", name, k)), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		must("script.out", r.Out)
		must("script.err", r.Stderr)
		must("script.exit", fmt.Sprint(r.Exit))
		must("script.sleep", fmt.Sprint(r.Sleep))
	}
	script := `#!/bin/sh
LOG="` + logDir + `"
n=$(cat "$LOG/count" 2>/dev/null || echo 0)
n=$((n+1)); echo $n > "$LOG/count"
k=$n; [ $k -gt 2 ] && k=2
echo "$*" > "$LOG/argv.$n"
head -n 1 > "$LOG/stdin.$n"
cat "$LOG/script.out.$k"
cat "$LOG/script.err.$k" >&2
sleep "$(cat "$LOG/script.sleep.$k")"
exit "$(cat "$LOG/script.exit.$k")"
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return logDir
}

func denyAllEnv(t *testing.T) {
	t.Helper()
	withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Cleanup(SetHeadlessAvailableForTest("claude", true))
}

func invocations(t *testing.T, logDir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(logDir, "count"))
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range strings.TrimSpace(string(b)) {
		n = n*10 + int(c-'0')
	}
	return n
}

func readLog(t *testing.T, logDir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(logDir, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

func runDenyAll(t *testing.T) (string, error) {
	t.Helper()
	return OneShotDenyAll(context.Background(), usagex.FeatureMemoryReview, OneShotShort, "persona", secretPrompt)
}

func TestOneShotDenyAllRunsAfterAProbeThatShowsNothing(t *testing.T) {
	denyAllEnv(t)
	logDir := fakeDenyAllClaude(t, cleanProbe, cleanRun)

	reply, err := runDenyAll(t)
	if err != nil {
		t.Fatalf("OneShotDenyAll: %v", err)
	}
	if reply != "FAKE-REPLY" {
		t.Fatalf("reply = %q", reply)
	}
	if n := invocations(t, logDir); n != 2 {
		t.Fatalf("claude ran %d times, want the probe and the call", n)
	}
	// The probe never carries the transcript; the call does.
	if got := strings.TrimSpace(readLog(t, logDir, "stdin.1")); got != "/help" {
		t.Fatalf("the probe stdin = %q", got)
	}
	if got := strings.TrimSpace(readLog(t, logDir, "stdin.2")); got != secretPrompt {
		t.Fatalf("the call stdin = %q", got)
	}
	// Both runs use the pinned deny-all argv, so the probe proves what the call will have.
	for _, f := range []string{"argv.1", "argv.2"} {
		argv := readLog(t, logDir, f)
		for _, want := range []string{"--strict-mcp-config", "--no-chrome", "--output-format stream-json", "--tools "} {
			if !strings.Contains(argv, want) {
				t.Fatalf("%s lacks %q: %s", f, want, argv)
			}
		}
	}
}

// The reviewer's finding on #2335: strict MCP alone leaves Claude in Chrome connected when the
// environment enables it. A flag is not evidence, so a probe that cannot be trusted stops the call
// before the transcript is handed over. Each case is a stream the probe must refuse.
func TestOneShotDenyAllRefusesAProbeItCannotTrust(t *testing.T) {
	toolUse := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]}}`
	cases := map[string]fakeRun{
		"lists a tool":       {Out: lines(`{"type":"system","subtype":"init","tools":["mcp__claude-in-chrome__navigate"],"mcp_servers":[]}`, probeResult)},
		"lists a server":     {Out: lines(`{"type":"system","subtype":"init","tools":[],"mcp_servers":[{"name":"claude-in-chrome"}]}`, probeResult)},
		"no tools field":     {Out: lines(`{"type":"system","subtype":"init","mcp_servers":[]}`, probeResult)},
		"no servers field":   {Out: lines(`{"type":"system","subtype":"init","tools":[]}`, probeResult)},
		"null lists":         {Out: lines(`{"type":"system","subtype":"init","tools":null,"mcp_servers":null}`, probeResult)},
		"list of wrong type": {Out: lines(`{"type":"system","subtype":"init","tools":"none","mcp_servers":[]}`, probeResult)},
		"empty event":        {Out: lines(`{"type":"system","subtype":"init"}`, probeResult)},
		// Two inits that each lack what the other has must not add up to a clean one.
		"lists split over two inits": {Out: lines(`{"type":"system","subtype":"init","tools":[]}`, `{"type":"system","subtype":"init","mcp_servers":[]}`, probeResult)},
		"clean init then a bare one": {Out: lines(initClean, `{"type":"system","subtype":"init"}`, probeResult)},
		"no init":                    {Out: lines(probeResult)},
		"a line that is not JSON":    {Out: lines(initClean, "plain text", probeResult)},
		"a truncated event":          {Out: lines(initClean, `{"type":"assistant","mess`, probeResult)},
		// What the run did contradicts what it said it had.
		"a tool call after a clean init": {Out: lines(initClean, toolUse, probeResult)},
		"a tool result":                  {Out: lines(initClean, `{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}`, probeResult)},
		"an mcp block":                   {Out: lines(initClean, `{"type":"assistant","message":{"content":[{"type":"mcp_tool_use"}]}}`, probeResult)},
		"a streamed tool call":           {Out: lines(initClean, `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}`, probeResult)},
		"activity before the init":       {Out: lines(assistText, initClean, probeResult)},
		"a permission denial":            {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"result":"x","total_cost_usd":0,"permission_denials":[{"tool_name":"Bash"}]}`)},
		// The probe must be a clean, local, successful run.
		"exits non-zero after a clean init":            {Out: lines(initClean), Exit: 3},
		"exits non-zero after a clean init and result": {Out: lines(initClean, probeResult), Exit: 3},
		"no result":         {Out: lines(initClean)},
		"an error result":   {Out: lines(initClean, `{"type":"result","subtype":"error","is_error":true,"num_turns":0,"result":"x","total_cost_usd":0}`)},
		"reached the model": {Out: lines(initClean, runResult)},
		// Evidence has to be present: a missing or null field is unknown, not zero.
		"a bare result":                    {Out: lines(initClean, `{"type":"result"}`)},
		"null measurements":                {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":null,"num_turns":null,"total_cost_usd":null,"usage":{}}`)},
		"no is_error":                      {Out: lines(initClean, `{"type":"result","subtype":"success","num_turns":0,"total_cost_usd":0,"usage":{}}`)},
		"no num_turns":                     {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0,"usage":{}}`)},
		"no cost":                          {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"usage":{}}`)},
		"no usage":                         {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0}`)},
		"an error subtype that looks free": {Out: lines(initClean, `{"type":"result","subtype":"error_max_turns","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{}}`)},
		"tokens at zero turns and cost":    {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{"input_tokens":100,"output_tokens":1}}`)},
		"input tokens only":                {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{"input_tokens":100}}`)},
		"cache tokens only":                {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{"cache_read_input_tokens":5}}`)},
		"per-model usage":                  {Out: lines(initClean, `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{},"modelUsage":{"m":{}}}`)},
		// A later local-looking result must not erase the evidence of an earlier paid one.
		"a model result then a local one": {Out: lines(initClean, runResult, probeResult)},
		"a local result then a model one": {Out: lines(initClean, probeResult, runResult)},
		"activity after the result":       {Out: lines(initClean, probeResult, assistText)},
	}
	for name, probe := range cases {
		t.Run(name, func(t *testing.T) {
			denyAllEnv(t)
			logDir := fakeDenyAllClaude(t, probe, cleanRun)
			reply, err := runDenyAll(t)
			if !errors.Is(err, ErrDenyAllUnavailable) {
				t.Fatalf("err = %v, want ErrDenyAllUnavailable", err)
			}
			if reply != "" {
				t.Fatalf("reply = %q", reply)
			}
			if n := invocations(t, logDir); n != 1 {
				t.Fatalf("claude ran %d times: only the probe may run", n)
			}
			if strings.Contains(readLog(t, logDir, "stdin.1"), "TRANSCRIPT-MARKER") {
				t.Fatal("the transcript reached a backend that was not verified")
			}
		})
	}
}

// A probe that does not finish in time proves nothing, even if it printed a clean init first.
//
// The budget is a wall-clock deadline that starts before the fake CLI's shell does. It has to be
// far longer than a loaded host needs to fork and run the script's first lines: at 300ms the
// probe was sometimes killed before it recorded its invocation, and the "exactly one run"
// check below failed although the code under test was right (measured: a 1ms budget fails it
// every time with the same message). The fake sleeps well past the budget, so the budget
// is what ends the probe. The error text pins that path, so any other failure of the probe
// cannot pass for the timeout.
func TestOneShotDenyAllRefusesAProbeThatTimesOut(t *testing.T) {
	denyAllEnv(t)
	old := denyAllProbeBudget
	denyAllProbeBudget = 3 * time.Second
	t.Cleanup(func() { denyAllProbeBudget = old })
	logDir := fakeDenyAllClaude(t, fakeRun{Out: lines(initClean, probeResult), Sleep: 20}, cleanRun)
	_, err := runDenyAll(t)
	if !errors.Is(err, ErrDenyAllUnavailable) || !strings.Contains(err.Error(), "did not finish in time") {
		t.Fatalf("err = %v, want ErrDenyAllUnavailable from the probe timeout", err)
	}
	// "At most": a stall longer than the budget can still kill the probe before it records itself,
	// and that must not read as a failure. The timeout path is pinned by the error text above.
	if n := invocations(t, logDir); n > 1 {
		t.Fatalf("claude ran %d times: the transcript run started after a probe that timed out", n)
	}
}

// The probe is not given the transcript, but a CLI's config, endpoint and auth errors can still
// carry secrets: whatever it printed stays out of the error.
func TestOneShotDenyAllNeverQuotesWhatTheProbePrinted(t *testing.T) {
	for name, probe := range map[string]fakeRun{
		"no init, stderr": {Stderr: "PRIVATE-CONFIG-SECRET", Exit: 3},
		"init, stderr":    {Out: lines(initClean), Stderr: "PRIVATE-CONFIG-SECRET", Exit: 3},
		"stdout noise":    {Out: lines("PRIVATE-CONFIG-SECRET")},
	} {
		t.Run(name, func(t *testing.T) {
			denyAllEnv(t)
			fakeDenyAllClaude(t, probe, cleanRun)
			_, err := runDenyAll(t)
			if !errors.Is(err, ErrDenyAllUnavailable) || strings.Contains(err.Error(), "PRIVATE-CONFIG-SECRET") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The probe can pass and the real run still differ (an account connector that appears in between,
// a config written meanwhile): the call's own stream is checked and its reply thrown away.
func TestOneShotDenyAllDiscardsAReplyFromARunThatHadOrUsedTools(t *testing.T) {
	for name, run := range map[string]fakeRun{
		"a tool in the init":                 {Out: lines(`{"type":"system","subtype":"init","tools":["Bash"],"mcp_servers":[]}`, runResult)},
		"a server in the init":               {Out: lines(`{"type":"system","subtype":"init","tools":[],"mcp_servers":[{"name":"x"}]}`, runResult)},
		"a tool call":                        {Out: lines(initClean, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`, runResult)},
		"no init":                            {Out: lines(runResult)},
		"a result that does not say success": {Out: lines(initClean, `{"type":"result","result":"FAKE-REPLY"}`)},
		"two results":                        {Out: lines(initClean, runResult, runResult)},
		"split inits":                        {Out: lines(`{"type":"system","subtype":"init","tools":[]}`, `{"type":"system","subtype":"init","mcp_servers":[]}`, runResult)},
	} {
		t.Run(name, func(t *testing.T) {
			denyAllEnv(t)
			logDir := fakeDenyAllClaude(t, cleanProbe, run)
			reply, err := runDenyAll(t)
			if err == nil || reply != "" {
				t.Fatalf("reply = %q, err = %v: such a run must not return a reply", reply, err)
			}
			if invocations(t, logDir) != 2 {
				t.Fatal("expected the probe and the call")
			}
		})
	}
}

// A failed run that was given the transcript must not put what the process printed into its error.
func TestOneShotDenyAllErrorsDoNotQuoteStderr(t *testing.T) {
	denyAllEnv(t)
	fakeDenyAllClaude(t, cleanProbe, fakeRun{Out: lines(initClean), Stderr: "STDERR-ECHO transcript text", Exit: 3})
	_, err := runDenyAll(t)
	if err == nil || strings.Contains(err.Error(), "STDERR-ECHO") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
}

// A feature pinned to a vendor that cannot run deny-all fails: the transcript does not move to
// another vendor behind the member's back, and nothing is started.
func TestOneShotDenyAllNeverMovesAPinnedFeatureToAnotherVendor(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureAgents":{"memory.review":"codex"}}`)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Cleanup(SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(SetHeadlessAvailableForTest("codex", true))
	logDir := fakeDenyAllClaude(t, cleanProbe, cleanRun)

	_, err := runDenyAll(t)
	if !errors.Is(err, ErrDenyAllUnavailable) {
		t.Fatalf("err = %v, want ErrDenyAllUnavailable", err)
	}
	if invocations(t, logDir) != 0 {
		t.Fatal("a CLI was started for a pin that cannot qualify")
	}
}

// With no pin, a shared order that leads with a kind that cannot qualify skips it.
func TestOneShotDenyAllSkipsKindsThatCannotQualify(t *testing.T) {
	writeUIPrefs(t, `{"aiAssistOrder":["codex","claude"]}`)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Cleanup(SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(SetHeadlessAvailableForTest("codex", true))
	logDir := fakeDenyAllClaude(t, cleanProbe, cleanRun)

	if _, err := runDenyAll(t); err != nil {
		t.Fatalf("err = %v", err)
	}
	if invocations(t, logDir) != 2 {
		t.Fatal("claude should have run")
	}
}

func TestOneShotDenyAllWithoutAQualifyingBackend(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(SetHeadlessAvailableForTest("claude", false))
	for _, k := range []string{"codex", "opencode", "cursor", "agy", "muse"} {
		t.Cleanup(SetHeadlessAvailableForTest(k, true))
	}
	logDir := fakeDenyAllClaude(t, cleanProbe, cleanRun)
	_, err := runDenyAll(t)
	if n := invocations(t, logDir); !errors.Is(err, ErrDenyAllUnavailable) || n != 0 {
		t.Fatalf("err = %v, invocations = %d", err, n)
	}
}

func TestParseClaudeStreamKeepsTheResultAndDistrustsAnythingOdd(t *testing.T) {
	s := parseClaudeStream([]byte(lines(initClean, assistText, runResult)))
	if !s.initSeen || s.bad != "" || !s.resultSeen || s.result.Result != "FAKE-REPLY" || s.facts.numTurns == nil || *s.facts.numTurns != 1 {
		t.Fatalf("%+v", s)
	}
	// Other system events and unrelated fields called "tools" or "message" are not violations.
	s = parseClaudeStream([]byte(lines(initClean, `{"type":"system","subtype":"hook_started","tools":"x","message":5}`, `{"type":"rate_limit_event","event":"x"}`, runResult)))
	if s.bad != "" {
		t.Fatalf("an unrelated event was distrusted: %q", s.bad)
	}
	for name, out := range map[string]string{
		"two tools":    lines(`{"type":"system","subtype":"init","tools":["a","b"],"mcp_servers":[]}`),
		"junk line":    lines(initClean, "{not json"),
		"empty":        "",
		"result alone": lines(runResult),
	} {
		if s := parseClaudeStream([]byte(out)); s.bad == "" && s.initSeen {
			t.Errorf("%s: trusted", name)
		} else if name == "empty" && s.initSeen {
			t.Errorf("%s: init seen", name)
		}
	}
}

func TestDenyAllKindIsClaudeOnly(t *testing.T) {
	for _, k := range []string{"codex", "opencode", "cursor", "agy", "muse", "kiro", "copilot", "lcpp", ""} {
		if DenyAllKind(k) {
			t.Fatalf("%q must not qualify until a deny-all argv is verified for it", k)
		}
	}
	if !DenyAllKind("claude") {
		t.Fatal("claude qualifies")
	}
}

// Every kind a one-shot can run on is either a qualified backend with a runner or an unqualified
// one with a recorded limitation, never both and never neither: a new kind has to be classified
// before it can be picked for a feature that reads a third party's text.
func TestDenyAllLimitsCoverEveryOneShotKind(t *testing.T) {
	for _, k := range oneShotKinds {
		run, qualified := denyAllBackends[k]
		why, limited := denyAllLimits[k]
		switch {
		case qualified && limited:
			t.Errorf("%s is both qualified and listed with a limitation", k)
		case !qualified && !limited:
			t.Errorf("%s is neither qualified nor given a limitation", k)
		case qualified && run == nil:
			t.Errorf("%s is qualified with no runner", k)
		case limited && strings.TrimSpace(why) == "":
			t.Errorf("%s has an empty limitation", k)
		}
	}
	for k := range denyAllLimits {
		if !oneShotRunnable(k) {
			t.Errorf("denyAllLimits names %q, which is not a one-shot kind", k)
		}
	}
	for k := range denyAllBackends {
		if !oneShotRunnable(k) {
			t.Errorf("denyAllBackends names %q, which is not a one-shot kind", k)
		}
	}
}

// withDenyAllBackend installs a runner for kind for the test's duration.
func withDenyAllBackend(t *testing.T, kind string, run denyAllRunner) {
	t.Helper()
	old, had := denyAllBackends[kind]
	denyAllBackends[kind] = run
	t.Cleanup(func() {
		if had {
			denyAllBackends[kind] = old
		} else {
			delete(denyAllBackends, kind)
		}
	})
}

// The run is dispatched on the kind that was resolved. Before the table carried a runner per
// kind, any kind added to the list would have been verified and run as claude.
func TestOneShotDenyAllRunsTheResolvedKindsOwnRunner(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureAgents":{"memory.review":"codex"}}`)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Cleanup(SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(SetHeadlessAvailableForTest("codex", true))
	logDir := fakeDenyAllClaude(t, cleanProbe, cleanRun)
	var got string
	withDenyAllBackend(t, "codex", func(_ context.Context, _, prompt, _ string) (string, error) {
		got = prompt
		return "CODEX-REPLY", nil
	})

	reply, err := runDenyAll(t)
	if err != nil || reply != "CODEX-REPLY" || got != secretPrompt {
		t.Fatalf("reply = %q, err = %v, prompt seen = %q", reply, err, got)
	}
	if n := invocations(t, logDir); n != 0 {
		t.Fatalf("claude was started %d times for a codex run", n)
	}
}

// A runner that is missing (nil) is no qualification: the kind is skipped, a pin on it fails, and
// nothing is started or panics.
func TestOneShotDenyAllTreatsANilRunnerAsUnqualified(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureAgents":{"memory.review":"codex"}}`)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Cleanup(SetHeadlessAvailableForTest("claude", true))
	t.Cleanup(SetHeadlessAvailableForTest("codex", true))
	logDir := fakeDenyAllClaude(t, cleanProbe, cleanRun)
	withDenyAllBackend(t, "codex", nil)

	if DenyAllKind("codex") {
		t.Fatal("a kind with no runner qualifies")
	}
	if _, err := runDenyAll(t); !errors.Is(err, ErrDenyAllUnavailable) {
		t.Fatalf("err = %v, want ErrDenyAllUnavailable", err)
	}
	if invocations(t, logDir) != 0 {
		t.Fatal("a CLI was started")
	}
}

// The cached resolution says "unavailable" once the only qualifying kind is confirmed unreachable,
// and stays unknown while its cache entry is cold: GET /ai-assist/resolution and the memory
// review status share this answer.
func TestResolveDenyAllCachedWithoutAQualifyingBackend(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(SetHeadlessAvailableForTest("claude", false))
	if kind, source, ok := ResolveDenyAllCached("memory.review"); !ok || source != OneShotSourceUnavailable || kind != "" {
		t.Fatalf("got %q %q %v, want unavailable", kind, source, ok)
	}
	t.Cleanup(SetHeadlessAvailableForTest("claude", true))
	if kind, source, ok := ResolveDenyAllCached("memory.review"); !ok || source != OneShotSourceDefault || kind != "claude" {
		t.Fatalf("got %q %q %v, want default claude", kind, source, ok)
	}
}

// A deny-all kind that turns out reachable on the second read keeps the answer unknown: the
// verdict "unavailable" needs every kind confirmed unreachable in the same pass.
func TestResolveDenyAllCachedNeverCallsAReachableKindUnavailable(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(ClearHeadlessAvailableForTest("claude"))
	if _, source, ok := ResolveDenyAllCached("memory.review"); ok || source != "" {
		t.Fatalf("cold: got %q %v, want unknown", source, ok)
	}
}

// A call cut by its context must return promptly even though the killed CLI left a child holding
// its stdout (the fake's `sleep`): without WaitDelay Output waits for that child, and the caller's
// slot with it.
func TestOneShotDenyAllReturnsWhenTheCallIsCutAndAChildOutlivesIt(t *testing.T) {
	denyAllEnv(t)
	logDir := fakeDenyAllClaude(t, cleanProbe, fakeRun{Out: lines(initClean, assistText, runResult), Sleep: 20})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, err := OneShotDenyAll(ctx, usagex.FeatureMemoryReview, OneShotShort, "persona", secretPrompt)
		done <- result{err}
	}()
	// Cut only once the real run (the second invocation, after the probe) has started and had time
	// to start its `sleep` child, so the path under test is the one reached, not the probe's.
	deadline := time.Now().Add(30 * time.Second)
	for invocations(t, logDir) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the real run never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	start := time.Now()
	select {
	case r := <-done:
		if r.err == nil || errors.Is(r.err, ErrDenyAllUnavailable) {
			t.Fatalf("err = %v, want a failure of the cut call itself", r.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("the call was still blocked %s after its context ended", time.Since(start))
	}
}
