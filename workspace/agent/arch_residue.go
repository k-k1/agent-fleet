package main

// arch_residue.go — `workspace-agent notify-arch-residue`, the Console-facing half of
// the architecture self-repair (docs/decisions/0068 decision 4).
//
// af-arch-repair puts back everything it can put back (pip --user, uv tools, npm -g, and
// the entrypoint restores the selected JDK / node). Two things it deliberately cannot:
// build output under ~/repos (uncommitted work lives there) and binaries the member
// dropped into ~/.local/bin by hand (no way to know where they came from). Those it
// writes to ~/.local/share/agent-fleet/arch-residue, and this turns that file into a
// notification.
//
// ## Why this exists at all
//
// The repair script only prints to the container's stdout — the operator's `docker logs`.
// There is no route that shows a member the workspace boot log, and the entrypoint
// `exec`s the agent, so the tmux sessions a member opens are a different stream
// entirely. Every warning about arch residue was therefore written where the member
// could never read it, and the first they knew of it was `Exec format error` weeks
// later with no context.
//
// ## Once, or every time?
//
// Neither, quite. The *event* ("this workspace moved to another CPU family") happens
// once. The *residue* ("demo/node_modules is still x86_64") is a STATE that persists
// until the member rebuilds it — announce it once and a member who missed it never
// learns; announce it every boot and it nags about something they may have chosen to
// live with.
//
// So: re-detect on every boot and key the notification on the CONTENT of what is still
// broken. notice.PutOnce writes once per key, so
//
//   - same broken set  -> same key -> nothing new is queued
//   - one repo fixed   -> different key -> exactly one notification showing what is left
//   - everything fixed -> af-arch-repair removes the file -> nothing is emitted, ever again
//
// No "seen" bookkeeping, no dismiss button, and it stops by itself the moment the member
// fixes the last one. (The CP's notification table also has UNIQUE(event_id) with
// ON CONFLICT DO NOTHING, so a duplicate could not land even if one were queued.)

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

func archResiduePath() string {
	return filepath.Join(homeDir(), ".local", "share", "agent-fleet", "arch-residue")
}

// runNotifyArchResidue is `workspace-agent notify-arch-residue [<from-arch>]`, called by
// the entrypoint right after af-arch-repair. Silent and successful when there is no
// residue — the common case by far, since the repair puts most things back.
func runNotifyArchResidue(args []string) {
	from := ""
	if len(args) > 0 {
		from = args[0]
	}
	b, err := os.ReadFile(archResiduePath())
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return // no residue = say nothing
	}
	body := strings.TrimSpace(string(b))

	repos, bins := parseArchResidue(body)
	ev := notice.New("arch-residue", "", "", "")
	// TargetType must be explicit: left empty it falls back to the CP's default (session), and
	// the notification then points at an empty session name the Console cannot open.
	ev.TargetType = "workspace"
	ev.Payload["from"] = from
	ev.Payload["repos"] = repos
	ev.Payload["bins"] = bins

	// The key is the set of things currently broken — not a version, not a timestamp. That is
	// exactly the property wanted: it changes when the residue is fixed, and only then.
	sum := sha256.Sum256([]byte(body))
	if err := notice.PutOnce("arch-residue:"+hex.EncodeToString(sum[:]), ev); err != nil {
		fmt.Fprintln(os.Stderr, "[notify-arch-residue]", err)
	}
}

// parseArchResidue splits the file af-arch-repair writes:
//
//	repos: demo/node_modules other/target
//	bin: mytool
func parseArchResidue(body string) (repos, bins []string) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "repos:"):
			repos = strings.Fields(strings.TrimPrefix(line, "repos:"))
		case strings.HasPrefix(line, "bin:"):
			bins = strings.Fields(strings.TrimPrefix(line, "bin:"))
		}
	}
	return
}
