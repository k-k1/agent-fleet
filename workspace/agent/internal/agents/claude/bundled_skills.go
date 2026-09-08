package claude

// Enumerating the skills the installed claude CLI ships (docs/log/50 §9): dataviz, simplify,
// code-review and the like. They are not files anywhere — the CLI is a single ~200 MB binary
// with the definitions as minified JS constants — so the only version-independent source is
// the SDK's init frame, which carries `skills` (bundled + user + project, names only) and
// `slash_commands` (those plus the built-in commands). Emitting the frame takes one user
// message; sending `/help` makes claude answer locally ("/help isn't available in this
// environment"), so the probe costs no model call at all (measured on 2.1.263: input_tokens 0,
// cost 0, about 0.8 s wall).
//
// Caveats, all measured:
//   - the -p list is NOT the TUI's list. The same binary advertised keybindings-help /
//     security-review / init only in the TUI and deep-research / verify / debug / batch /
//     doctor / run-skill-generator only here (gated by model and mode). Treat it as an
//     approximation — an entry may exist that the running session does not accept.
//   - the frame carries names only, no descriptions. The Console keeps a small i18n table for
//     the well-known ones and shows the rest by name.
//   - claude fires its SessionStart hooks for the probe like for any run, so it runs in an
//     empty temp dir (no project hooks/skills) and the result is cached per binary identity.
//     The bundled set depends on the binary, not the working copy, so one probe serves every
//     session until the CLI is updated.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// bundledProbeTimeout bounds one probe. The CLI starts in well under a second on a local disk;
// on network-backed home volumes (ECS) the first start is slower, so this stays loose — the
// picker's fetch is the only caller and it tolerates a one-off wait (the result is cached).
const bundledProbeTimeout = 20 * time.Second

// bundledProbeInput is the one stream-json user message that makes claude emit the init frame
// and then answer locally without a model call.
const bundledProbeInput = `{"type":"user","message":{"role":"user","content":"/help"}}` + "\n"

type bundledCache struct {
	mu    sync.Mutex
	key   string // binary identity the cached names belong to ("" = nothing cached)
	names []string
}

var bundled bundledCache

// binaryIdentity returns a string that changes whenever the claude binary does: the resolved
// path plus its size and mtime. Cheap (two stats), which is why it is used instead of
// `claude --version` (another process start per picker open).
func binaryIdentity() string {
	p, err := exec.LookPath("claude")
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return p + "|" + st.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(st.Size(), 10)
}

// BundledSkills returns the skill names the installed claude CLI advertises (the init frame's
// `skills`, which includes any user/project skills visible from the probe's empty cwd — i.e.
// the user-level ones; callers dedupe against their own scan). nil when claude is not
// installed or the probe fails; a failure is not cached, so the next open retries.
func BundledSkills() []string {
	key := binaryIdentity()
	if key == "" {
		return nil
	}
	bundled.mu.Lock()
	defer bundled.mu.Unlock() // also serialises concurrent probes (one process at a time)
	if bundled.key == key {
		return bundled.names
	}
	names, ok := probeBundledSkills()
	if !ok {
		return nil
	}
	bundled.key, bundled.names = key, names
	return names
}

// probeBundledSkills runs the /help probe and returns the init frame's `skills`.
func probeBundledSkills() ([]string, bool) {
	// A fixed, empty directory rather than MkdirTemp: even with --no-session-persistence claude
	// creates its per-project state dir (`projects/-tmp-<cwd>`, holding only an empty memory/ dir — measured) under the config
	// dir, and a fresh name per probe would litter one per CLI update. Reused, it costs one.
	dir := filepath.Join(os.TempDir(), "af-claude-skills-probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), bundledProbeTimeout)
	defer cancel()
	// The same --settings as a real session (program.go): the probe must not open claude's own
	// cross-session channel either, however briefly.
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence", "--max-turns", "1", "--settings", nativePeerSettings)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewBufferString(bundledProbeInput)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	names, found := parseInitSkills(out)
	// The local /help answer follows within milliseconds; let the process exit on its own
	// (a kill would leave its messaging socket file behind) but never wait past the deadline.
	_, _ = io.Copy(io.Discard, out)
	_ = cmd.Wait()
	return names, found
}

// parseInitSkills reads stream-json lines until the system/init frame and returns its
// `skills`. The frames before init are hook events (SessionStart), which are skipped.
func parseInitSkills(r io.Reader) ([]string, bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // the init frame lists every tool; be generous
	for sc.Scan() {
		var frame struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			Skills  []string `json:"skills"`
		}
		if err := json.Unmarshal(sc.Bytes(), &frame); err != nil {
			continue
		}
		if frame.Type == "system" && frame.Subtype == "init" {
			if frame.Skills == nil {
				frame.Skills = []string{}
			}
			return frame.Skills, true
		}
	}
	return nil, false
}
