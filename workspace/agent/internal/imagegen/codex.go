package imagegen

// The Codex route (ADR 0069 decision 4): drive the Codex CLI's built-in `image_gen` tool
// through one non-interactive `codex exec` turn and collect the file it wrote.
//
// It needs no API key — it runs on the user's ChatGPT login — and it is the only route that
// exists at all for a Claude or opencode session, since Anthropic ships no image-generation
// API.
//
// Everything about the invocation is defensive, and each piece answers a specific failure:
//
//   - `-s read-only`: upstream has an open report (openai/codex#19133) that a session can
//     find image_gen unavailable and fall back to SCRIPTING a placeholder PNG. Under a
//     read-only sandbox the model cannot write such a file. A read-only sandbox is enough
//     because image_gen is a native tool, not a shell command, so it writes outside the
//     sandbox anyway (measured 2026-09-06).
//   - the prompt on STDIN: an argv prompt lands in every process listing on the host.
//   - the result collected by DIFFING the generated-images directory, never by parsing the
//     model's prose. The prior-art bridge parses `SAVED: <path>` out of stdout, which is a
//     contract the driver model can break silently — and a collector that only looks in the
//     generated-images directory cannot pick up a scripted placeholder either, so the call
//     fails honestly instead of returning a fake.
//   - `--ignore-user-config`: the user's own config.toml is where the fleet materializes
//     every registered MCP server, af's own included (mcpreg/materialize_codex.go). Loading
//     it here would spawn that whole fleet for one image — and hand this exec a
//     generate_image tool of its own. Auth still comes from CODEX_HOME.
//
// Measured 2026-09-06 (codex-cli 0.153.4, auth_mode=chatgpt): 27.6 s and 37 s per image,
// ~50-60k input tokens per run, and the file at $CODEX_HOME/generated_images/<thread_id>/.
// The file name changed between 0.144 (`exec-*.png`) and 0.153 (`call_*.png`) — which is
// exactly why nothing here matches on a name.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registers DecodeConfig for the format codex may emit
	_ "image/jpeg" // ditto
	_ "image/png"  // the format every measured run produced
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// defaultCodexModel is the driver model, not the image model: `image_gen` is backed by
// gpt-image-2 whatever runs the turn, so the cheapest capable tier is the right default.
// Measured on 2026-09-06; override deployment-wide with AF_IMAGEGEN_CODEX_MODEL, because
// model ids move and nothing here may depend on one staying valid.
const defaultCodexModel = "gpt-5.4-mini"

// codexGenerateTimeout bounds one turn. It sits below the 600 s tool_timeout_sec the codex
// materializer stamps on the af server, so a slow run is reported by us with a real reason
// rather than cut by the client with "timed out awaiting tools/call". The public Images API
// has been reported at ~235 s for a high-quality large image, so the budget is well clear of
// a normal run's 30 s.
const codexGenerateTimeout = 8 * time.Minute

type codexProvider struct {
	model string
	// home is $CODEX_HOME. Held rather than read per call so a test can point the whole
	// provider at a temporary tree.
	home string
	// exe is the Codex binary. A field for the same reason.
	exe string
}

func newCodexProvider() *codexProvider {
	model := os.Getenv("AF_IMAGEGEN_CODEX_MODEL")
	if model == "" {
		model = defaultCodexModel
	}
	return &codexProvider{model: model, home: paths.CodexHome(), exe: "codex"}
}

func (p *codexProvider) ID() string { return ProviderCodex }

// DefaultModel is the DRIVER model, which is what this route can name: the image model behind
// the built-in tool is gpt-image-2 whatever runs the turn, and nothing in the CLI reports it.
func (p *codexProvider) DefaultModel() string { return p.model }

// Caps for the Codex route. Sizes and Backgrounds are deliberately EMPTY: measured twice on
// 2026-09-06, once with the size in the prose and once with size/quality/background spelled
// out as tool parameters, both runs produced 1254x1254 and the driver said outright that
// "this tool only accepts prompt text and image references here". The parameters exist in
// the backend but the tool the driver sees does not expose them, so this is the permanent
// story for this route, not a gap waiting to be filled (ADR 0069, open question 2).
// Exact sizes arrive with a provider that supports them natively.
func (p *codexProvider) Caps(string) Caps {
	return Caps{
		Ops: []Op{OpGenerate, OpEdit},
		// 5 is the built-in tool's own reference-image cap; asking for more produces the
		// `num_last_images_to_include must be between 1 and 5` error and one wasted round trip.
		MaxInputs: 5,
		MaxCount:  4,
	}
}

// Ready reports whether this container can run the route at all: the CLI on PATH and a Codex
// login on disk. It deliberately does NOT shell out to `codex login status` the way
// agents/codex.Status does — Ready is on the tools/list path, which the client calls on every
// turn, and a CLI spawn there would be paid for by every session.
func (p *codexProvider) Ready(ctx context.Context) bool {
	if _, err := exec.LookPath(p.exe); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(p.home, "auth.json"))
	if err != nil {
		return false
	}
	var a struct {
		AuthMode string `json:"auth_mode"`
	}
	// Only the mode is read, never a token. api-key mode is accepted as well: it works, it
	// just bills the API instead of the ChatGPT plan, which is the user's own arrangement.
	if json.Unmarshal(b, &a) != nil || a.AuthMode == "" {
		return false
	}
	// A login is not the same as quota. Being logged in and OUT of plan quota is the case a
	// readiness check that stops at auth.json cannot see: auto would pick this route, spend
	// nothing, and fail — instead of stepping aside for a provider that can actually run.
	// Only the account's own verdict counts; an unreachable endpoint leaves codex ready and
	// lets it give its own answer (codex.PlanExhausted returns known=false).
	if exhausted, known := planExhausted(ctx); known && exhausted {
		return false
	}
	return true
}

// planExhausted is a var so a test can drive the readiness branch without a network. In
// production it is the Codex account view (internal/agents/codex), which caches the call.
var planExhausted = codex.PlanExhausted

func (p *codexProvider) Generate(ctx context.Context, req Request) (Result, error) {
	if req.Prompt == "" {
		return Result{}, errors.New("a prompt is required")
	}
	caps := p.Caps(req.Model)
	if !caps.Supports(req.Op) {
		return Result{}, fmt.Errorf("the codex route cannot do %s", req.Op)
	}
	if len(req.Inputs) > caps.MaxInputs {
		return Result{}, fmt.Errorf("at most %d reference images (got %d)", caps.MaxInputs, len(req.Inputs))
	}

	// An EMPTY working directory, so that even a driver that ignored the instructions has
	// nothing of the user's to read. --skip-git-repo-check is what lets codex run outside a
	// repository at all.
	work, err := os.MkdirTemp("", "af-imagegen-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(work)

	root := p.generatedRoot()
	before, err := snapshotFiles(root)
	if err != nil {
		return Result{}, err
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	args := []string{
		"-a", "never", "-s", "read-only", "exec", "--json",
		"--skip-git-repo-check", "--ephemeral", "--ignore-user-config",
		"--color", "never", "-C", work, "-m", model,
	}
	for _, in := range req.Inputs {
		args = append(args, "-i", in)
	}
	if req.Mask != "" {
		// The built-in tool has no mask parameter; passing one as a reference image would
		// silently produce something else entirely. Refuse instead (Op inpaint is already
		// refused above; this catches a mask handed to edit).
		return Result{}, errors.New("the codex route has no mask input; use a provider that supports inpainting")
	}
	args = append(args, "-") // prompt on stdin

	runCtx, cancel := context.WithTimeout(ctx, codexGenerateTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, p.exe, args...)
	cmd.Dir = work
	cmd.Stdin = strings.NewReader(codexPrompt(req))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	ev := parseCodexEvents(stdout.Bytes())
	res := Result{
		Provider: ProviderCodex,
		Model:    model,
		Usage: Usage{
			In: ev.freshInput(), Out: ev.usage.OutputTokens,
			CacheRead: ev.usage.CachedInputTokens, CacheCreate: ev.usage.CacheWriteInputTokens,
			Measured: ev.sawUsage,
		},
	}
	if runErr != nil && ev.threadID == "" {
		return res, fmt.Errorf("codex exec failed: %w: %s", runErr, tail(stderr.String(), 400))
	}
	if ev.err != "" {
		return res, fmt.Errorf("codex returned an error: %s", ev.err)
	}
	if ev.threadID == "" {
		// Without the thread id there is no directory that is provably OURS, and picking the
		// newest file anywhere under the root would hand this session another session's image.
		return res, errors.New("codex produced no thread id, so its output could not be located")
	}

	files, err := newFilesUnder(filepath.Join(root, ev.threadID), before)
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		// The honest failure the whole defensive shape exists to produce: the model may have
		// found image_gen unavailable and merely said so. Its prose is not evidence of a file.
		return res, fmt.Errorf("codex generated no image (%s)", tail(ev.reply, 300))
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
	// The source copies are pure waste once the core has stored its own: the measured
	// container had let $CODEX_HOME/generated_images grow to 80 MB at 2-3 MB an image with
	// nothing sweeping it. Only this run's own files are touched.
	removeCollected(files, filepath.Join(root, ev.threadID))
	if runErr != nil {
		res.Warnings = append(res.Warnings, "codex exited with an error after producing the image: "+runErr.Error())
	}
	return res, nil
}

func (p *codexProvider) generatedRoot() string {
	return filepath.Join(p.home, "generated_images")
}

// codexPrompt is the fixed template. It starts with the `$imagegen` trigger, which is what
// loads Codex's own image-generation skill; the run that used it produced no
// `num_last_images_to_include` error, unlike the two runs before it (measured 2026-09-06).
//
// The requested size / background / count are stated as best-effort only. They are measured
// NOT to take effect through an agentic exec, and promising them in the template would just
// teach the driver to claim it honoured them.
func codexPrompt(req Request) string {
	var b strings.Builder
	b.WriteString("$imagegen\n")
	b.WriteString("Generate the image described below using the built-in image generation tool, then stop.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Use the built-in image generation tool only. Do not run shell commands, do not write or read any file, do not inspect the working directory.\n")
	b.WriteString("- If the image generation tool is unavailable, say so in one line and stop. Never draw, script or otherwise fabricate a substitute image.\n")
	b.WriteString("- Do not report a file path and do not summarise the picture; the file is collected from disk.\n")
	if n := req.Count; n > 1 {
		fmt.Fprintf(&b, "- Produce %d images.\n", n)
	}
	if req.Size != "" && req.Size != "auto" {
		fmt.Fprintf(&b, "- Aim for %s if the tool lets you choose; if it does not, generate anyway and do not retry.\n", req.Size)
	}
	if req.Background == "transparent" {
		b.WriteString("- Aim for a transparent background if the tool supports it; if it does not, generate anyway and do not retry.\n")
	}
	if len(req.Inputs) > 0 {
		b.WriteString("- Use the attached images as references.\n")
	}
	b.WriteString("\nDescription:\n")
	b.WriteString(req.Prompt)
	b.WriteString("\n")
	return b.String()
}

// --- the `codex exec --json` stream ------------------------------------------------------

// codexEvents is what one exec's JSONL stream is worth to this package. It is a deliberate
// sibling of chatx.parseCodexExecEvents rather than a call into it: the chat's parser also
// owns the reply text, the conversation's thread bookkeeping and the context-fill snapshot,
// none of which apply here, and imagegen must stay a leaf package.
type codexEvents struct {
	threadID string
	reply    string
	err      string
	usage    codexUsage
	sawUsage bool
}

// codexUsage is turn.completed's usage block. input_tokens INCLUDES the cached share (the
// rollout token_count convention), so the fresh input is input - cached.
type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
}

func (e codexEvents) freshInput() int {
	if n := e.usage.InputTokens - e.usage.CachedInputTokens; n > 0 {
		return n
	}
	return 0
}

func parseCodexEvents(out []byte) codexEvents {
	var ev codexEvents
	var texts []string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var e struct {
			Type     string     `json:"type"`
			ThreadID string     `json:"thread_id"`
			Message  string     `json:"message"`
			Usage    codexUsage `json:"usage"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(ln, &e) != nil {
			continue
		}
		switch e.Type {
		case "thread.started":
			ev.threadID = e.ThreadID
		case "item.completed":
			if e.Item.Type == "agent_message" && e.Item.Text != "" {
				texts = append(texts, e.Item.Text)
			}
		case "turn.completed":
			ev.usage, ev.sawUsage = e.Usage, true
		case "turn.failed", "error":
			switch {
			case e.Error.Message != "":
				ev.err = e.Error.Message
			case e.Message != "":
				ev.err = e.Message
			default:
				ev.err = "turn failed"
			}
		}
	}
	ev.reply = strings.TrimSpace(strings.Join(texts, "\n"))
	return ev
}

// --- collecting the files ----------------------------------------------------------------

// snapshotFiles records every file under root before the run. A missing root is not an error:
// it is simply the first generation on this container.
func snapshotFiles(root string) (map[string]bool, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			seen[p] = true
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return seen, nil
}

// newFilesUnder is the directory diff: the files that appeared in this thread's directory,
// sorted by name so the order is the same on every run. Scoping it to the thread directory is
// what makes a concurrent generation in another session impossible to pick up; comparing
// against the pre-run snapshot as well covers a thread id that somehow repeats.
func newFilesUnder(dir string, before map[string]bool) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !before[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func removeCollected(files []string, dir string) {
	for _, f := range files {
		_ = os.Remove(f)
	}
	// Only when it is empty — never a recursive delete of a directory whose contents we did
	// not put there.
	_ = os.Remove(dir)
}

// readImage loads one produced file. The dimensions come from the file itself, because on
// this route they are the only truthful answer to "what size did I get" — the request's size
// is not honoured.
func readImage(path string) (Image, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	img := Image{Bytes: b, MIME: mimeOf(path)}
	// A format DecodeConfig does not know leaves the dimensions at zero rather than guessing;
	// an unknown size must not be reported as 0x0 pixels of content.
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
		img.Width, img.Height = cfg.Width, cfg.Height
	}
	return img, nil
}

func mimeOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// tail keeps the last n characters, so a long CLI error is quotable without dragging a whole
// stream into an error message.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

// codexDriverModel is what the status endpoint reports as the model a generation would run
// on, so the answer is not duplicated from the env lookup.
func codexDriverModel() string { return newCodexProvider().model }
