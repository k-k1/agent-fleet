package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// scratchAutoRelocate points a freshly created working copy's regenerable build
// output (target, build) at the task-local working disk BEFORE anything builds
// into it — ADR 0044 decision 3 / docs/log/63 §63.5. node_modules and .venv are
// deliberately not relocated: npm and `python3 -m venv` replace or refuse a
// symlinked directory (af-scratch.sh has the details).
//
// Why at creation time and not on demand: on ECS `~` is EFS, which costs ~14.5ms
// per file, so the FIRST build is the expensive one. Relocating afterwards pays
// that cost anyway and then re-reads the whole tree off EFS to move it.
//
// Best effort by construction: no working copy is worth failing a clone over, so
// every error is logged and swallowed. AF_WS_SCRATCH is injected by the CP only
// on the ECS runtime (docker/native put home on a local disk, where there is
// nothing to gain), so this is a no-op everywhere else — checked here as well so
// the common path does not even fork a process.
func scratchAutoRelocate(dir string) {
	if dir == "" || os.Getenv("AF_WS_SCRATCH") == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "af-scratch", "--auto", dir).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		log.Printf("scratch: auto relocate %s failed: %v: %s", dir, err, msg)
		return
	}
	if msg != "" {
		log.Printf("scratch: %s", msg)
	}
}
