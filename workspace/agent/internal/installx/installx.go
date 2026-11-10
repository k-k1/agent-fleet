// Package installx holds the helpers the on-demand installers share: where installs
// stage, the image's pin list, a checksum gate, and the `--version` probe. The per-kind
// installers (internal/agents/kiro, internal/agents/muse) and the tool installers left in
// package main (gcloud, databases, chromium, …) all call it, so the helpers cannot live
// in either of them.
package installx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// BuildPinsPath is the pin list the Dockerfile writes from its ARGs. It is a var only so
// tests can swap it; never rewritten at runtime.
var BuildPinsPath = "/usr/local/share/agent-fleet/versions.json"

// ReadBuildPins returns the image's pins, or an empty map when the file is absent or
// unparsable.
func ReadBuildPins() map[string]string {
	pins := map[string]string{}
	if b, err := os.ReadFile(BuildPinsPath); err == nil {
		_ = json.Unmarshal(b, &pins)
	}
	return pins
}

// ShareDir is the per-user root for on-demand tool installs
// (~/.local/share/agent-fleet — home volume, persists across restarts, shared
// between the docker and native runtimes).
func ShareDir() string {
	return filepath.Join(paths.HomeDir(), ".local", "share", "agent-fleet")
}

// LocalBin is ~/.local/bin, where the on-demand installers place binaries.
func LocalBin() string {
	return filepath.Join(paths.HomeDir(), ".local", "bin")
}

// FileExecutable reports whether path is an executable regular file.
func FileExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0111 != 0
}

// VerifySha256 compares a file's digest against the expected hex sum.
func VerifySha256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// RunCmd runs name with args, forwarding stderr.
func RunCmd(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stderr = os.Stderr
	return c.Run()
}

// ProbeTimeout bounds one `--version` exec. It is loose on purpose: home on network
// storage makes the first read of a CLI an order slower, and a broken binary is bounded by
// the caller's overall budget instead.
const ProbeTimeout = 15 * time.Second

// probeSlots caps how many version probes run at once. With one goroutine per tool all
// exec'ing at the same time, a Workspace with few CPUs makes slow-starting CLIs time out on
// the load we create ourselves (waiting for a slot does not eat the exec deadline — that one
// starts once the process does).
var probeSlots = make(chan struct{}, 4)

// VerNumRe extracts the version number from --version output (1.2 / 1.2.3 / 3.11.2, …).
var VerNumRe = regexp.MustCompile(`[0-9]+\.[0-9]+(\.[0-9]+)?`)

// ExtractVer returns the first version number in raw, or raw itself when there is none.
func ExtractVer(raw string) string {
	if m := VerNumRe.FindString(raw); m != "" {
		return m
	}
	return raw
}

// Probed is what one version probe saw.
type Probed struct {
	Path    string
	Version string // extracted number; same as Raw when none matches
	Raw     string // first line of the --version output
}

// ProbeVersion reads the version of the binary at path. nil when there is no binary.
func ProbeVersion(ctx context.Context, path string, args, env []string) *Probed {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil
	}
	if len(args) == 0 {
		args = []string{"--version"}
	}
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		return &Probed{Path: path, Raw: "(timeout)"}
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return &Probed{Path: path, Raw: "(timeout)"}
	}
	if err != nil && len(out) == 0 {
		return &Probed{Path: path, Raw: "(取得失敗)"}
	}
	raw := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return &Probed{Path: path, Version: ExtractVer(raw), Raw: raw}
}
