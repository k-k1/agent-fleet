package copilot

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// syncSessionSince is the first copilot whose privacy flags are spelled differently. 1.0.103
// removed --no-remote (remote control is now only switched on inside a session, with
// `/remote on`) and renamed --no-remote-export to --sync-session=off. Each version rejects the
// other's spelling and exits before drawing anything (measured: 1.0.103 prints "--no-remote was
// removed", 1.0.102 "unexpected argument '--sync-session'"), so the wrong pair means the
// session never starts. A self-updating Workspace runs a newer copilot than the image pin, so
// the choice has to follow the binary actually installed.
var syncSessionSince = [3]int{1, 0, 103}

// privacyFlags returns the flags that keep a session's GitHub sync and remote steering off
// (the docs/log/36 contract), in the spelling bin understands. A version that cannot be read
// gets the current spelling: the old one only ever applies to releases already published.
func privacyFlags(bin string) []string {
	if v, ok := installedVersion(bin); ok && versionLess(v, syncSessionSince) {
		return []string{"--no-remote", "--no-remote-export"}
	}
	return []string{"--sync-session=off"}
}

var copilotVersionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

type versionEntry struct {
	v  [3]int
	ok bool
	at time.Time
}

var (
	versionMu    sync.Mutex
	versionCache = map[string]versionEntry{}
)

// versionTTL bounds how long a probed version is trusted: copilot updates itself in place, and
// a launch after that must not reuse the old spelling. One probe costs about 200 ms.
const versionTTL = time.Minute

// installedVersion runs `<bin> --version` ("GitHub Copilot CLI 1.0.103.") and caches the
// result per bin for versionTTL.
func installedVersion(bin string) ([3]int, bool) {
	versionMu.Lock()
	defer versionMu.Unlock()
	if e, hit := versionCache[bin]; hit && time.Since(e.at) < versionTTL {
		return e.v, e.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = append(cmd.Environ(), "COPILOT_AUTO_UPDATE=false")
	out, err := cmd.Output()
	e := versionEntry{at: time.Now()}
	if err == nil {
		e.v, e.ok = parseCopilotVersion(string(out))
	}
	versionCache[bin] = e
	return e.v, e.ok
}

func parseCopilotVersion(s string) ([3]int, bool) {
	m := copilotVersionRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
