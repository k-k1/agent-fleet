// Package bootinstall is the Agent's side of the entrypoint's lean boot-install: it installs one
// pinned CLI into ~/.local and records the outcome for Settings > Toolchains
// (boot_install_status.go reads the file this package appends to).
//
// The shell version ran each install as `( set -e; … ) && ok || WARN`, where errexit is ignored,
// so every integrity check had to be an explicit `|| exit 1`. Here an error is a return value:
// a digest that does not match fails the install and nothing reaches the final path.
package bootinstall

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/installx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Exit codes of `workspace-agent boot-install`, read by entrypoint.sh.
const (
	ExitOK       = 0
	ExitFailed   = 1
	ExitNoSpace  = 3 // skipped for lack of space: the entrypoint stops its other downloads too
	statusOK     = "ok"
	statusSkip   = "skipped"
	statusFailed = "failed"
)

// Failure reason classes written to the status file; the same set boot_install_status.go reads.
const (
	ReasonNoSpace  = "no_space"
	ReasonChecksum = "checksum"
	ReasonNetwork  = "network"
	ReasonOther    = "other"
)

// ErrChecksum marks a downloaded artifact whose digest is not the pinned one.
var ErrChecksum = errors.New("checksum mismatch")

// ErrNetwork marks a failed download, whatever the transport reported (a body cut short
// included).
var ErrNetwork = errors.New("download failed")

// Env is everything an install touches, so tests can point it at a fake home and server.
type Env struct {
	Home       string // ~
	StatusPath string // AF_BOOT_INSTALL_STATUS; empty = recording off
	Pins       map[string]string
	Repin      bool                           // move a drifted copy back to the pin
	Arch       string                         // dpkg-style: amd64 or arm64
	RTKBaseURL string                         // https://github.com/rtk-ai/rtk/releases/download (tests: a local server)
	FreeMiB    func(dir string) (int64, bool) // false when unknown
	Client     *http.Client
	Stdout     io.Writer
	Stderr     io.Writer
	RunTimeout time.Duration // --version probe bound
}

// DefaultEnv is the production environment.
func DefaultEnv(repin bool) Env {
	arch := runtime.GOARCH
	return Env{
		Home:       paths.HomeDir(),
		StatusPath: os.Getenv("AF_BOOT_INSTALL_STATUS"),
		Pins:       installx.ReadBuildPins(),
		Repin:      repin,
		Arch:       arch,
		RTKBaseURL: "https://github.com/rtk-ai/rtk/releases/download",
		FreeMiB:    freeMiB,
		Client:     &http.Client{Timeout: 10 * time.Minute},
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		RunTimeout: 30 * time.Second,
	}
}

func freeMiB(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize) >> 20, true
}

func (e Env) localBin() string { return filepath.Join(e.Home, ".local", "bin") }

// note appends one status line. Never fatal: a status that cannot be written is a missing status.
func (e Env) note(tool, status, reason string) {
	if e.StatusPath == "" {
		return
	}
	f, err := os.OpenFile(e.StatusPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\n", tool, status, reason)
}

// probe runs `p --version` once under the deadline and returns its combined output, so a caller
// gets both the verdict and the reason (a dynamic-linker complaint, say) from a single run.
func (e Env) probe(p string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.RunTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "--version").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runs reports whether the binary at p answers --version in time.
func (e Env) runs(p string) bool {
	_, err := e.probe(p)
	return err == nil
}

func usable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// cliPresent: a copy exists in /usr/local/bin (baked) or ~/.local/bin.
func (e Env) cliPresent(name string) bool {
	if usable("/usr/local/bin/" + name) {
		return true
	}
	_, err := os.Stat(filepath.Join(e.localBin(), name)) // follows links: a dangling one is absent, like `[ -e ]`
	return err == nil
}

// cliUsable: a copy that can run now, in either place.
func (e Env) cliUsable(name string) bool {
	for _, p := range []string{"/usr/local/bin/" + name, filepath.Join(e.localBin(), name)} {
		if usable(p) && e.runs(p) {
			return true
		}
	}
	return false
}

// classify maps a failure to the reason class. no_space is tested first: a full disk also
// breaks downloads and digests, and it is the one the member can fix.
func classify(err error) string {
	var msg string
	if err != nil {
		msg = strings.ToLower(err.Error())
	}
	switch {
	case errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) ||
		strings.Contains(msg, "no space left") || strings.Contains(msg, "quota exceeded"):
		return ReasonNoSpace
	case errors.Is(err, ErrChecksum):
		return ReasonChecksum
	case errors.Is(err, ErrNetwork):
		return ReasonNetwork
	}
	var ue *url.Error
	var ne interface{ Timeout() bool }
	if errors.As(err, &ue) || errors.As(err, &ne) || strings.Contains(msg, "http ") {
		return ReasonNetwork
	}
	return ReasonOther
}

// get downloads url to dest, retrying a few times like curl --retry 3.
func (e Env) get(ctx context.Context, u, dest string) error {
	var last error
	for i := 0; i < 4; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		last = e.getOnce(ctx, u, dest)
		if last == nil {
			return nil
		}
		last = fmt.Errorf("%w: %w", ErrNetwork, last)
		var hs httpStatusError
		if errors.As(last, &hs) && hs >= 400 && hs < 500 && hs != 429 && hs != 408 {
			return last // a 404 will not heal
		}
	}
	return last
}

type httpStatusError int

func (h httpStatusError) Error() string { return fmt.Sprintf("http %d", int(h)) }

func (e Env) getOnce(ctx context.Context, u, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %w", u, httpStatusError(resp.StatusCode))
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// checksumFor returns the digest listed for asset in a sha256sum-style file. Every line naming
// the asset must carry the same valid digest: `sha256sum -c` checked each of them, so a first
// matching line must not hide a later contradicting one.
func checksumFor(file, asset string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	got := ""
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) != 2 || strings.TrimPrefix(fs[1], "*") != asset {
			continue
		}
		sum := strings.ToLower(fs[0])
		if _, err := hex.DecodeString(sum); err != nil || len(sum) != 64 {
			return "", fmt.Errorf("%w: malformed digest listed for %s", ErrChecksum, asset)
		}
		if got != "" && got != sum {
			return "", fmt.Errorf("%w: conflicting digests listed for %s", ErrChecksum, asset)
		}
		got = sum
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if got == "" {
		return "", fmt.Errorf("%w: no checksum listed for %s", ErrChecksum, asset)
	}
	return got, nil
}

// extractMember writes the first regular member of the .tar.gz whose base name is name to dest.
func extractMember(archive, name, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in %s", name, filepath.Base(archive))
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != name {
			continue
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}

// installAtomic copies src to dest through a sibling temp name and a rename, so a failure part-way
// (ENOSPC leaves a truncated file) never leaves anything at dest. The candidate must answer
// --version before it replaces dest: a binary that cannot run here never displaces a working copy.
func (e Env) installAtomic(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Exclusive creation beside dest: a pre-planted link at a predictable name must never lead
	// the write into the copy being replaced.
	out, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".af-new.")
	if err != nil {
		return err
	}
	tmp := out.Name()
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if msg, err := e.probe(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s does not run in this environment, keeping the current copy: %v: %s", filepath.Base(dest), err, msg)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
