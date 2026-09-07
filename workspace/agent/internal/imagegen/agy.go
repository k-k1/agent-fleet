package imagegen

// The agy route (ADR 0069): drive the Antigravity CLI's built-in `generate_image` tool through
// one non-interactive print-mode turn and collect the file it wrote.
//
// It is the second Tier-1 provider (decision 3, "existing connection"): it needs no API key and
// no new Connections card, because the container already holds an Antigravity OAuth token when
// the user has signed agy in. It spends the Gemini/Antigravity plan, NOT the ChatGPT one — which
// is the whole reason it is worth having next to the Codex route rather than instead of it.
//
// Everything about the invocation is defensive, and each piece answers a specific failure:
//
//   - an ISOLATED HOME per call, sharing only a symlink to the OAuth token. agy resolves its
//     entire configuration from $HOME and has no per-invocation override for it (no
//     `--ignore-user-config` as codex has, and its MCP config is global-only), so a run under
//     the user's real home would load the whole materialized MCP fleet for one picture — and
//     hand this turn a generate_image of its own. The same trick chatAgyHome already plays for
//     the assistant chat.
//   - NO --dangerously-skip-permissions, and permissions.allow naming exactly one tool. Print
//     mode cannot prompt, so every tool that is not allow-listed is auto-denied (measured
//     2026-09-07: `run_command` came back as denied_actions=[command] and the run ended
//     CANCELED). That is this route's equivalent of codex's `-s read-only`, and it is what makes
//     "the model could not have fabricated a placeholder PNG" true rather than hoped for.
//   - the prompt on STDIN as one `--input-format stream-json` message. `--print` takes its
//     prompt as a flag VALUE, which would put it in every process listing on the host.
//   - the result collected by DIFFING the conversation's own output directory, never by parsing
//     prose. agy's tool result does spell the path out ("Generated image is saved at %s."), and
//     that is exactly the contract the Codex route refused to depend on: a driver model can
//     break it silently.
//   - the RDRAND mask, taken from the one place that decides it. On a host whose kernel has
//     withdrawn RDRAND, agy's FIPS self-test aborts at launch and masking the bit out of
//     OpenSSL's CPU detection is what starts it (ADR 0008). That used to be spelled out here,
//     because whether the product should offer agy as an agent KIND on such a host was still
//     open; it was decided on 2026-09-07, so this route now takes the overlay from
//     `internal/agents/agy` like every other agy spawn — which is also what makes a deployment
//     that refuses the mask (`AF_AGY_RDRAND_MASK=0`) refuse it here, and leaves a host with a
//     working RDRAND untouched.
//
// Measured 2026-09-07 (agy 1.1.5, signed in, gemini-3.8-flash-low as the driver): 25 s wall for
// one image, 16 s of it the turn itself; ~27k input / 75 output tokens; a 122 KB JPEG at
// 1376x768 for AspectRatio 16:9; the file at
// $HOME/.gemini/antigravity-cli/brain/<conversation_id>/<ImageName>_<epoch_ms>.jpg.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// defaultAgyModel is the DRIVER model, not the image model: the picture is produced by whatever
// image model the built-in tool is wired to (measured: gemini-3.1-flash-image, recorded in agy's
// own step store and not on any wire this package reads), so the cheapest capable driver is the
// right default. Override deployment-wide with AF_IMAGEGEN_AGY_MODEL, because model ids move and
// nothing here may depend on one staying valid.
const defaultAgyModel = "gemini-3.8-flash-low"

// agyGenerateTimeout bounds one turn, for the same reason the Codex one does: it sits below the
// 600 s tool_timeout_sec stamped on the af server, so a slow run is reported by us with a real
// reason rather than cut by the client. It is also passed to agy as --print-timeout, whose own
// default is 5 minutes — otherwise agy would give up first and this budget would never apply.
const agyGenerateTimeout = 8 * time.Minute

// agyAspectRatios is the built-in tool's own enum, read out of the CLI's embedded JSON schema
// ("Supported values: '1:1', '2:3', '3:2', '3:4', '4:3', '9:16', '16:9'. Default is '1:1'").
// Unlike the Codex route's size, this one REACHES the tool: a 16:9 request came back 1376x768
// (measured 2026-09-07).
var agyAspectRatios = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9"}

// agyImageName is the ImageName every generation asks for. The parameter is required and the
// tool appends its own millisecond stamp, so the value only decides how the file is spelled
// inside a directory this package deletes; a fixed one keeps the prompt's own words out of a
// file name.
const agyImageName = "af_generated"

type agyProvider struct {
	model string
	// exe is the agy binary; a field so a test can point the provider at a stub.
	exe string
	// token is the real OAuth token, the ONLY thing the throwaway home shares with the user's.
	token string
}

func newAgyProvider() *agyProvider {
	model := os.Getenv("AF_IMAGEGEN_AGY_MODEL")
	if model == "" {
		model = defaultAgyModel
	}
	return &agyProvider{model: model, exe: "agy", token: agy.TokenPath()}
}

func (p *agyProvider) ID() string { return ProviderAgy }

// Caps for the agy route. AspectRatios is populated and Sizes is not, and that difference is the
// point: the built-in tool takes an aspect ratio and has no size, quality or background
// parameter at all. Reporting the ratio list in Sizes would be a lie in the other direction —
// the caller cannot pick 1024x1024 here any more than on the Codex route.
func (p *agyProvider) Caps(string) Caps {
	return Caps{
		// edit rides on the tool's own ImagePaths parameter ("Images to edit, combine, or use
		// as references", maxItems 3). That is the tool contract rather than a measurement: the
		// one live run this was budgeted for produced a picture from text alone.
		Ops:          []Op{OpGenerate, OpEdit},
		AspectRatios: agyAspectRatios,
		// 3 is the tool's own cap; a fourth reference image is rejected by its schema.
		MaxInputs: 3,
		// One call, one picture (measured). Asking for more would be a second call and a second
		// unit of the user's plan, so the honest number is 1 and the core reports the shortfall.
		MaxCount: 1,
	}
}

// Ready is on the tools/list path, which a client calls every turn, so it stays as cheap as the
// Codex one: the binary on PATH and a token on disk. It deliberately does NOT run `agy models`
// or any other CLI probe — that would spawn a process per turn for every session.
//
// There is no exhaustion check to match codex's PlanExhausted: agy's remaining-quota figure only
// comes from scraping its TUI (internal/agents/agy, seconds per scrape), which is far too
// expensive here. An out-of-quota agy therefore answers for itself, and Run's fall-through moves
// on to the next provider.
func (p *agyProvider) Ready(context.Context) bool {
	if _, err := exec.LookPath(p.exe); err != nil {
		return false
	}
	return agy.SignedIn()
}

func (p *agyProvider) Generate(ctx context.Context, req Request) (Result, error) {
	if req.Prompt == "" {
		return Result{}, errors.New("a prompt is required")
	}
	caps := p.Caps(req.Model)
	if !caps.Supports(req.Op) {
		return Result{}, fmt.Errorf("the agy route cannot do %s", req.Op)
	}
	if len(req.Inputs) > caps.MaxInputs {
		return Result{}, fmt.Errorf("at most %d reference images (got %d)", caps.MaxInputs, len(req.Inputs))
	}
	for _, in := range req.Inputs {
		// The tool refuses a relative path outright ("image path must be absolute"); saying so
		// here costs nothing and saves a turn of the user's plan.
		if !filepath.IsAbs(in) {
			return Result{}, fmt.Errorf("reference image paths must be absolute: %s", in)
		}
	}
	if req.Mask != "" {
		// No mask parameter exists on this tool; handing one over as a reference image would
		// silently produce something else entirely.
		return Result{}, errors.New("the agy route has no mask input; use a provider that supports inpainting")
	}

	home, err := p.prepareHome()
	if err != nil {
		return Result{}, err
	}
	// The throwaway home is also the cleanup: the conversation store, the presence lock, the
	// brain directory and the collected sources all live inside it, so nothing accumulates the
	// way $CODEX_HOME/generated_images did (80 MB, measured, before the Codex provider swept).
	defer func() {
		p.foldRotatedToken(home)
		os.RemoveAll(home)
	}()

	ratio, ratioWarn := p.pickAspectRatio(req)

	model := req.Model
	if model == "" {
		model = p.model
	}
	args := []string{
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--disable-slash-commands", "--model", model,
		"--print-timeout", agyGenerateTimeout.String(),
	}
	runCtx, cancel := context.WithTimeout(ctx, agyGenerateTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, p.exe, args...)
	cmd.Dir = filepath.Join(home, "wd")
	cmd.Env = envWithHome(home)
	cmd.Stdin = strings.NewReader(agyStdin(agyPrompt(req, ratio)))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	ev := parseAgyEvents(stdout.Bytes())
	res := Result{
		Provider: ProviderAgy,
		Model:    model,
		Usage: Usage{
			// input_tokens EXCLUDES the cached share here (measured: input 26896 + output 75 =
			// total 26971, with cache_read 16289 outside the total) — the opposite of the codex
			// rollout convention, so it is passed through as the fresh input directly.
			In: ev.usage.InputTokens, Out: ev.usage.OutputTokens,
			CacheRead: ev.usage.CacheReadTokens,
			Measured:  ev.sawUsage,
		},
	}
	if ratioWarn != "" {
		res.Warnings = append(res.Warnings, ratioWarn)
	}
	if len(ev.denied) > 0 {
		// The allow-list is the sandbox, so a denial is a configuration fault of ours, not the
		// model's — and naming the action is the only way it is ever fixed.
		return res, fmt.Errorf("agy denied a tool this route needs (%s); the run was cancelled",
			strings.Join(ev.denied, ", "))
	}
	if runErr != nil && ev.conversationID == "" {
		return res, fmt.Errorf("agy print mode failed: %w: %s", runErr, tail(stderr.String(), 400))
	}
	if ev.err != "" {
		return res, fmt.Errorf("agy returned an error: %s", ev.err)
	}
	if ev.conversationID == "" {
		// Without the conversation id there is no directory that is provably this run's.
		return res, errors.New("agy produced no conversation id, so its output could not be located")
	}

	files, err := agyOutputFiles(filepath.Join(home, ".gemini", "antigravity-cli", "brain", ev.conversationID))
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		// The honest failure the whole defensive shape exists to produce: the model may have
		// found the tool unavailable and merely said so. Its prose is not evidence of a file.
		return res, fmt.Errorf("agy generated no image (%s)", tail(ev.reply, 300))
	}
	if req.Count > 0 && len(files) > req.Count {
		files = files[:req.Count]
	}
	for _, f := range files {
		img, err := readImage(f)
		if err != nil {
			return res, err
		}
		res.Images = append(res.Images, img)
	}
	res.Warnings = append(res.Warnings, agyProducedWarnings(req, ratio, res.Images)...)
	if runErr != nil {
		res.Warnings = append(res.Warnings, "agy exited with an error after producing the image: "+runErr.Error())
	}
	return res, nil
}

// pickAspectRatio resolves what will actually be asked for, and says so when that is not what
// the caller wanted. An unsupported value is DROPPED rather than sent: the tool's parameter is
// an enum, and passing a value outside it would fail the whole call instead of producing a
// picture in the default ratio (ADR 0069 decision 7 — report, do not hide, and do not refuse
// work that can still be done).
func (p *agyProvider) pickAspectRatio(req Request) (ratio, warning string) {
	want := strings.TrimSpace(req.AspectRatio)
	if want == "" || want == "auto" {
		return "", ""
	}
	for _, r := range agyAspectRatios {
		if r == want {
			return want, ""
		}
	}
	return "", fmt.Sprintf("aspect_ratio=%s requested, but the agy route only offers %s; the model's default was used",
		want, strings.Join(agyAspectRatios, " "))
}

// agyProducedWarnings compares what arrived against what was asked for, in the terms THIS route
// can be held to. It is deliberately not an exact-size check: the tool takes a ratio and no
// size, and the ratio it honours is approximate (16:9 came back as 1376x768, 0.8% wide of
// 1.7778). Reporting that as a failure would train a caller to retry a generation that will
// never come out differently — and each retry spends the plan again.
func agyProducedWarnings(req Request, ratio string, images []Image) []string {
	var out []string
	if s := strings.TrimSpace(req.Size); s != "" && s != "auto" {
		out = append(out, "size is not selectable on the agy route (it takes an aspect ratio and no size)")
	}
	if b := strings.TrimSpace(req.Background); b == "transparent" {
		out = append(out, "background=transparent requested, but this route produces JPEG with an opaque background")
	}
	want, ok := parseRatio(ratio)
	if !ok {
		return out
	}
	for _, img := range images {
		if img.Width <= 0 || img.Height <= 0 {
			continue
		}
		got := float64(img.Width) / float64(img.Height)
		if d := got/want - 1; d > agyRatioTolerance || d < -agyRatioTolerance {
			out = append(out, fmt.Sprintf("aspect_ratio=%s requested, %dx%d produced", ratio, img.Width, img.Height))
			break
		}
	}
	return out
}

// agyRatioTolerance is how far the produced picture may sit from the requested ratio before it
// is worth telling the caller about. 5% clears the measured 0.8% miss with room to spare while
// still catching a request that was ignored outright (1:1 against 16:9 is 78% off).
const agyRatioTolerance = 0.05

func parseRatio(s string) (float64, bool) {
	w, h, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(w))
	b, err2 := strconv.Atoi(strings.TrimSpace(h))
	if err1 != nil || err2 != nil || a <= 0 || b <= 0 {
		return 0, false
	}
	return float64(a) / float64(b), true
}

// --- the isolated home --------------------------------------------------------------------

// prepareHome builds the throwaway $HOME one generation runs under. Three files carry the whole
// contract:
//
//   - the OAuth token, as a SYMLINK to the user's real one. The login is the only state shared
//     with the user's agy, and it is read, never written through (a rotation is folded back by
//     foldRotatedToken, which is the one thing a throwaway home would otherwise throw away).
//   - settings.json / config/config.json — workspace trust for the empty working directory,
//     telemetry off, and permissions.allow naming ONLY the image tool. Both files carry the
//     permissions because the effective location has shifted between agy builds (docs/log/32
//     D-5) and the extra copy is harmless.
//   - config/mcp_config.json — EMPTY on purpose. agy's MCP config is global-only, so this is the
//     only way to keep one picture from spawning the user's whole MCP fleet.
func (p *agyProvider) prepareHome() (string, error) {
	home, err := os.MkdirTemp("", "af-imagegen-agy-")
	if err != nil {
		return "", err
	}
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	cfgDir := filepath.Join(home, ".gemini", "config")
	// An EMPTY working directory, so that even a driver that ignored the instructions has
	// nothing of the user's to look at.
	wd := filepath.Join(home, "wd")
	for _, d := range []string{cliDir, cfgDir, wd} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			os.RemoveAll(home)
			return "", err
		}
	}
	if err := os.Symlink(p.token, filepath.Join(cliDir, "antigravity-oauth-token")); err != nil {
		os.RemoveAll(home)
		return "", err
	}
	allow := []string{mcpImageToolName}
	files := map[string]any{
		filepath.Join(cliDir, "settings.json"): map[string]any{
			"enableTelemetry":   false,
			"trustedWorkspaces": []string{wd},
			"permissions":       map[string]any{"allow": allow},
		},
		filepath.Join(cfgDir, "config.json"):     map[string]any{"permissions": map[string]any{"allow": allow}},
		filepath.Join(cfgDir, "mcp_config.json"): map[string]any{"mcpServers": map[string]any{}},
	}
	for path, v := range files {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			os.RemoveAll(home)
			return "", err
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			os.RemoveAll(home)
			return "", err
		}
	}
	return home, nil
}

// mcpImageToolName is agy's own name for the built-in tool — the allow-rule and the prompt have
// to agree on it, and it is not this fleet's `generate_image` MCP tool even though the two are
// spelled the same.
const mcpImageToolName = "generate_image"

// foldRotatedToken copies a refreshed OAuth token back to the user's real one. agy refreshes via
// tmp+rename, which REPLACES the symlink with a real file inside the throwaway home — where it
// would be deleted a moment later, leaving the user's token as stale as it was. Same reconcile
// the assistant chat does, minus the re-linking half: this home has no next run.
func (p *agyProvider) foldRotatedToken(home string) {
	link := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return // still the symlink: nothing was rotated
	}
	li, err := os.Stat(link)
	if err != nil {
		return
	}
	if si, err := os.Stat(p.token); err == nil && !li.ModTime().After(si.ModTime()) {
		return // the shared one is at least as new
	}
	if b, err := os.ReadFile(link); err == nil {
		_ = os.WriteFile(p.token, b, 0o600)
	}
}

// envWithHome is os.Environ() with HOME repointed and this host's agy overlay appended
// (agy.Env — nil where RDRAND is fine or the mask is refused). Both keys are dropped from the
// inherited environment first. Appending alone would in fact win — exec keeps the LAST value of a
// repeated key (measured on go1.26) — but leaving a stale OPENSSL_ia32cap in the slice would have
// the child's environment state two different things about the same CPU feature.
func envWithHome(home string) []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "OPENSSL_ia32cap=") {
			continue
		}
		out = append(out, e)
	}
	return agy.Env(append(out, "HOME="+home))
}

// --- the prompt and the stream ---------------------------------------------------------------

// agyStdin wraps one prompt as the single NDJSON message print mode reads from stdin. The
// envelope is agy's own ({"event":"user","message":{"content":…}}), which is why the prompt
// never has to appear in argv.
func agyStdin(prompt string) string {
	b, _ := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"content": prompt},
	})
	return string(b) + "\n"
}

// agyPrompt is the fixed template. The parameters that DO reach the tool are stated as
// instructions to set them; nothing that the tool has no parameter for is promised, because
// promising it only teaches the driver to claim it honoured it.
func agyPrompt(req Request, ratio string) string {
	var b strings.Builder
	b.WriteString("Generate the image described below using the generate_image tool, then stop.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Call the generate_image tool exactly once. Do not run shell commands, do not write or read any file, do not inspect the working directory.\n")
	fmt.Fprintf(&b, "- Set ImageName to %s.\n", agyImageName)
	if ratio != "" {
		fmt.Fprintf(&b, "- Set AspectRatio to %s.\n", ratio)
	}
	if len(req.Inputs) > 0 {
		b.WriteString("- Pass the reference images listed below as ImagePaths, exactly as written.\n")
	}
	b.WriteString("- If the image generation tool is unavailable, say so in one line and stop. Never draw, script or otherwise fabricate a substitute image.\n")
	b.WriteString("- Do not report a file path and do not summarise the picture; the file is collected from disk.\n")
	if len(req.Inputs) > 0 {
		b.WriteString("\nReference images:\n")
		for _, in := range req.Inputs {
			b.WriteString("- " + in + "\n")
		}
	}
	b.WriteString("\nDescription:\n")
	b.WriteString(req.Prompt)
	b.WriteString("\n")
	return b.String()
}

// agyEvents is what one print-mode stream is worth to this package.
type agyEvents struct {
	conversationID string
	reply          string
	err            string
	// denied is the actions print mode auto-denied. It is the difference between "the model
	// would not" and "we did not allow it", and only the second is ours to fix.
	denied   []string
	usage    agyUsage
	sawUsage bool
}

// agyUsage is the result event's usage block. output_tokens INCLUDES thinking_tokens (measured
// 2026-09-07: 421 output of which 418 thinking, and total 6052 = input 5631 + output 421), so
// adding them would double-count the reasoning.
type agyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

func parseAgyEvents(out []byte) agyEvents {
	var ev agyEvents
	var texts []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var e struct {
			Event string `json:"event"`
			Init  struct {
				ConversationID string `json:"conversation_id"`
			} `json:"init"`
			// conversation_id also rides on the envelope of the init event itself.
			ConversationID string `json:"conversation_id"`
			StepUpdate     struct {
				StepType  string `json:"step_type"`
				State     string `json:"state"`
				TextDelta string `json:"text_delta"`
			} `json:"step_update"`
			Result struct {
				ConversationID string   `json:"conversation_id"`
				Status         string   `json:"status"`
				Response       string   `json:"response"`
				Error          string   `json:"error"`
				Usage          agyUsage `json:"usage"`
				DeniedActions  []struct {
					Action      string `json:"action"`
					DisplayName string `json:"display_name"`
				} `json:"denied_actions"`
			} `json:"result"`
		}
		if json.Unmarshal(ln, &e) != nil {
			continue
		}
		switch e.Event {
		case "init":
			if id := firstNonEmpty(e.ConversationID, e.Init.ConversationID); id != "" {
				ev.conversationID = id
			}
		case "step_update":
			if e.StepUpdate.StepType == "agent_response" && e.StepUpdate.TextDelta != "" {
				texts = append(texts, e.StepUpdate.TextDelta)
			}
		case "result":
			ev.usage, ev.sawUsage = e.Result.Usage, true
			if ev.conversationID == "" {
				ev.conversationID = e.Result.ConversationID
			}
			if e.Result.Response != "" {
				texts = append(texts, e.Result.Response)
			}
			for _, d := range e.Result.DeniedActions {
				ev.denied = append(ev.denied, firstNonEmpty(d.DisplayName, d.Action))
			}
			// CANCELED with no denial and no message still has to say something, or the
			// failure reaches the caller as "no image" with no reason at all.
			switch {
			case e.Result.Error != "":
				ev.err = e.Result.Error
			case e.Result.Status != "" && e.Result.Status != "SUCCESS" && len(e.Result.DeniedActions) == 0:
				ev.err = "the run ended " + e.Result.Status
			}
		}
	}
	ev.reply = strings.TrimSpace(strings.Join(texts, ""))
	return ev
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// agyOutputFiles collects the pictures of ONE conversation. The directory is created by this run
// inside a home this process made moments earlier, so everything in it is ours — but only its
// top level is read: agy keeps its own bookkeeping in the `.system_generated`, `.user_uploaded`
// and `scratch` subdirectories next to the image, and none of that is a picture the caller asked
// for. There is no pre-run snapshot to compare against, because a brand-new home cannot contain
// an earlier run's file.
func agyOutputFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !isImageExt(e.Name()) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func isImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// agyDriverModel is what the status endpoint reports as the model a generation would run on, so
// the answer is not duplicated from the env lookup.
func agyDriverModel() string { return newAgyProvider().model }

// agyUsageKind is the ledger's kind for this route: a real agy process ran, so the row is filed
// under the agy kind and the Antigravity plan — not under the session that asked.
var agyUsageKind = session.KindAgy
