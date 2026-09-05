package copilot

// copilot's launch-time model catalog — fetched live, since it depends on the plan
// (docs/log/36 addendum).
//
// The copilot CLI has no enumeration endpoint (/model is TUI-only and ACP's configOptions
// carries no model either — measured), and which models are available depends on the plan
// (Copilot Free offers Auto alone, and even a correct id makes --model fail at launch with
// "not available" — measured). A static list would offer Free-plan users choices that are
// selectable but always fail, so the TUI /model picker is scraped over a PTY to return the
// models this account can actually select:
//   - a Free-family banner ("currently includes only Auto") → empty list, i.e. the picker
//     offers only the default (auto routing)
//   - no banner → the picker's rows are the catalog (the plan is already reflected in them)
// Same agents.Flow foundation and cache (stale-if-error) as agy's /usage scrape. The probe
// runs in a throwaway COPILOT_HOME so it does not pollute the real session list.
// Authentication injects the gh-derived token explicitly (ambient auth is lost in an
// isolated HOME — measured).

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// copilotEfforts are the values `--effort` accepts (v1.0.73 --help). An empty default effort
// means leaving it to the CLI's own default.
var copilotEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// modelsTTL is long because the picker is hit every time the launch modal opens while the
// probe takes seconds (it starts a TUI), and plans and catalogs rarely change.
const modelsTTL = 10 * time.Minute

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = not fetched / failed, non-nil empty = Auto only (settled)

func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, err := probeModels()
	if err != nil {
		return modelsList // stale-if-error: the previous value (nil = the default only)
	}
	modelsList = list
	modelsAt = time.Now()
	return modelsList
}

// probeModels launches a throwaway copilot TUI, opens /model and parses the
// picker. The whole probe runs against a temp COPILOT_HOME.
func probeModels() ([]agents.ModelChoice, error) {
	tok := Token()
	if tok == "" {
		return nil, errNoAuth
	}
	home, err := os.MkdirTemp("", "copilot-models-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	work, err := os.MkdirTemp("", "copilot-models-cwd-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	// Trust up front: a throwaway HOME always raises the trust dialog.
	ensureFolderTrustedIn(home, work)

	bin := os.Getenv("AGENT_COPILOT_BIN")
	if bin == "" {
		bin = "copilot"
	}
	cmd := exec.Command(bin, "--no-remote", "--no-remote-export")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"COPILOT_HOME="+home,
		"COPILOT_AUTO_UPDATE=false",
		"COPILOT_GITHUB_TOKEN="+tok,
		"TERM=xterm-256color",
	)
	f, err := agents.StartFlow(cmd)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Wait for the composer to be drawn (the "/ commands" footer — the same readiness signal
	// paneMode uses).
	if !waitFor(f, "/ commands", 30*time.Second) {
		return nil, errProbeTimeout
	}
	// Confirm the slash menu with the text and the Enter in separate writes (measured:
	// together they are swallowed by paste folding — the same quirk as the TUI path).
	if _, err := f.Ptmx.Write([]byte("/model")); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte("\r")); err != nil {
		return nil, err
	}
	if !waitFor(f, "Search models", 15*time.Second) {
		return nil, errProbeTimeout
	}
	// Give the drawing a beat to settle before parsing the final frame.
	time.Sleep(500 * time.Millisecond)
	return parseModelPicker(f.Clean()), nil
}

var errNoAuth = errStr("copilot models: cannot probe without a GitHub connection")
var errProbeTimeout = errStr("copilot models: the /model picker was never drawn")

type errStr string

func (e errStr) Error() string { return string(e) }

func waitFor(f *agents.Flow, marker string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(f.Clean(), marker) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// freePlanRe matches the /model banner shown when explicit model selection is
// plan-gated (measured on v1.0.73: "Your Copilot Free plan currently includes only Auto,
// which automatically selects ..."). The plan name is left out so a small rewording upstream
// still matches.
var freePlanRe = regexp.MustCompile(`plan currently includes only Auto`)

// modelRowRe matches one picker row's model id (measured vocabulary: gpt-5.6-sol /
// claude-sonnet-4.6 / gemini-3.1-pro-preview / kimi-k2.7-code …). A row stripped of the
// picker's decoration (❯ / ✓ / rules) must match the id shape entirely, which keeps prose
// and footer lines out.
var modelRowRe = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)

// parseModelPicker extracts the selectable model list from the cleaned PTY
// stream. With a Free-family banner it returns "Auto only", i.e. an empty (non-nil) list.
func parseModelPicker(clean string) []agents.ModelChoice {
	if freePlanRe.MatchString(clean) {
		return []agents.ModelChoice{}
	}
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, ln := range strings.Split(clean, "\n") {
		t := strings.TrimSpace(ln)
		// Strip the leading cursor/selection marker and the trailing check mark and
		// scrollbar glyphs.
		t = strings.TrimPrefix(t, "❯")
		t = strings.TrimSuffix(t, "✓")
		for _, glyph := range []string{"█", "┃", "│"} {
			t = strings.ReplaceAll(t, glyph, "")
		}
		t = strings.TrimSpace(t)
		if t == "" || strings.EqualFold(t, "auto") || seen[t] {
			continue
		}
		if !modelRowRe.MatchString(t) {
			continue
		}
		seen[t] = true
		list = append(list, agents.ModelChoice{ID: t, Label: t, Efforts: copilotEfforts})
	}
	if list == nil {
		// Neither a banner nor a row was found: the picker appeared but parsing came up
		// empty, i.e. the drawing drifted. Return an empty list — the picker then offers
		// only the default (auto), which is safer than offering wrong choices. The live
		// test is the primary detector of such a drift.
		return []agents.ModelChoice{}
	}
	return list
}
