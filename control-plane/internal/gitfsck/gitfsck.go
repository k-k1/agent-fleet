// Package gitfsck turns on object verification (receive.fsckObjects) for the
// Control Plane's internal bare repositories.
package gitfsck

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
)

// config is the per-repository config that makes git-receive-pack
// verify every incoming object. Defence in depth for the class behind #3484: a
// malformed .gitmodules name, or a tree entry such as ".git" / ".GIT" / "..", is
// rejected at push time instead of reaching a checkout.
//
// Only the message ids below are downgraded; every other id keeps git's own
// severity, so the dangerous classes stay errors: gitmodulesName, gitmodulesPath,
// gitmodulesUrl, gitmodulesUpdate, gitmodulesMissing, gitmodulesBlob,
// gitmodulesLarge, gitmodulesSymlink, hasDot, hasDotdot, hasDotgit, fullPathname.
var config = [][2]string{
	// Cosmetic defects that imported and mirrored history commonly carries. They
	// cannot hurt a checkout, and rejecting them would block a push the user cannot fix
	// without rewriting history.
	{"receive.fsck.badTimezone", "warn"},             // odd +/-HHMM offsets written by old tools
	{"receive.fsck.badDate", "warn"},                 // unparsable author/committer date
	{"receive.fsck.badDateOverflow", "warn"},         // date beyond what the parser accepts
	{"receive.fsck.zeroPaddedDate", "warn"},          // "0123456789" style timestamps
	{"receive.fsck.missingSpaceBeforeDate", "warn"},  // "<email>1234" without the space
	{"receive.fsck.missingSpaceBeforeEmail", "warn"}, // "Name<email>" without the space
	{"receive.fsck.missingNameBeforeEmail", "warn"},  // identity with an empty name
	{"receive.fsck.badEmail", "warn"},                // malformed email in an identity line
	{"receive.fsck.zeroPaddedFilemode", "warn"},      // "040000" for a tree mode (git's default is already warn; pinned so a future default change stays quiet)
	{"receive.fsck.badFilemode", "warn"},             // unusual regular-file modes from non-git tooling
	// receive.fsckObjects goes last: a repository that dies between two writes must be
	// left with the downgrades and no verification, never verification without them.
	// treeNotSorted stays an error: a non-adjacent duplicate entry is reported only as
	// treeNotSorted, so downgrading it would let a broken duplicate tree through.
	{"receive.fsckObjects", "true"},
}

// Ensure sets each key (a value reached through [include] counts as set, hence --includes) in config on the bare repository
// at dir unless the repository already carries a value for it, so an operator's
// explicit per-repository override survives every startup. `git config` takes its own
// lock file, so running beside a push is safe: receive-pack reads the config once at
// start and a push in flight keeps the settings it started with.
func Ensure(ctx context.Context, dir string) error {
	for _, kv := range config {
		// Exit status 1 means "key not set"; anything else is a real failure.
		err := exec.CommandContext(ctx, "git", "--git-dir", dir, "config", "--local", "--includes", "--get", kv[0]).Run()
		if err == nil {
			continue
		}
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return err
		}
		if err := setConfig(ctx, dir, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// setConfig retries a few times: git config fails at once when another writer
// (a second Control Plane, a concurrent Ensure) holds config.lock.
func setConfig(ctx context.Context, dir, key, val string) error {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		out, err := exec.CommandContext(ctx, "git", "--git-dir", dir, "config", "--local", key, val).CombinedOutput()
		if err == nil {
			return nil
		}
		last = &fsckConfigError{key: key, err: err, out: strings.TrimSpace(string(out))}
	}
	return last
}

type fsckConfigError struct {
	key string
	err error
	out string
}

func (e *fsckConfigError) Error() string { return "set " + e.key + ": " + e.err.Error() + ": " + e.out }

// Backfill applies Ensure to every existing bare
// repository under the git tree. A repository that fails is logged and skipped:
// one broken directory must never keep the Control Plane from starting. It returns
// the number of repositories visited and failed.
func Backfill(ctx context.Context, dataRoot string) (ok, failed int) {
	root := datalayout.GitRoot(dataRoot)
	tenants, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0 // no git tree yet
		}
		log.Printf("internal-git: fsck config: read %s: %v", root, err)
		return 0, 1
	}
	for _, td := range tenants {
		if !td.IsDir() {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(root, td.Name()))
		if err != nil {
			failed++
			log.Printf("internal-git: fsck config: read tenant dir %s: %v", td.Name(), err)
			continue
		}
		for _, rd := range repos {
			if !rd.IsDir() || filepath.Ext(rd.Name()) != ".git" {
				continue
			}
			if ctx.Err() != nil {
				return ok, failed
			}
			dir := filepath.Join(root, td.Name(), rd.Name())
			if err := Ensure(ctx, dir); err != nil {
				failed++
				log.Printf("internal-git: fsck config: %s: %v", dir, err)
				continue
			}
			ok++
		}
	}
	return ok, failed
}

// Run is the startup pass: Backfill, then one log line that is printed even when no
// repository was found. Silence is indistinguishable from a pass that never ran or
// scanned the wrong tree (#3926). The line names no path.
func Run(ctx context.Context, dataRoot string) (ok, failed int) {
	ok, failed = Backfill(ctx, dataRoot)
	log.Printf("internal-git: fsck config checked on %d repo(s), %d failed", ok, failed)
	return ok, failed
}
