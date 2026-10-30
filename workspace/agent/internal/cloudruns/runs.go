// Package cloudruns keeps one row per cloud-wrapper run (af-aws-exec today) in a spool the
// Agent serves to the Console: the session's mirror shows its own rows, and Settings shows
// the recent rows of every session (ADR 0126 decisions 1 and 2).
//
// A row names the identity a command ran as and never the command's arguments, environment
// or output: arguments can carry secrets. Everything that reaches a row is either resolved by
// the wrapper (account, role, region) or passes an allowlist (profile, program, a service and
// operation the CLI itself knows).
//
// The rows are visibility, not an audit: the session is the one the run reports through
// AF_SESSION_NAME, which the agent can change, and a command run without the wrapper leaves
// no row.
package cloudruns

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// perSession is how many rows a session's spool keeps, oldest out first.
	perSession = 200
	// workspaceDir holds the rows of runs that named no session the Agent knows. The leading
	// dot keeps it out of the session namespace (session.ValidName refuses it).
	workspaceDir = ".workspace"
	// workspaceKeep is how long a row in workspaceDir is kept.
	workspaceKeep = 7 * 24 * time.Hour
	// workspaceMax bounds workspaceDir between two prunes by age.
	workspaceMax = 500
)

// Reasons a run was refused before the command started.
const (
	RefusedLogin = "login_required" // exit 3
	RefusedUsage = "usage"          // exit 2
	RefusedOther = "refused"        // exit 1
)

// Row is one run, as stored and as served.
type Row struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Session is the session the run reported, when its name is a valid one.
	Session   string `json:"session,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Account   string `json:"account,omitempty"`
	Role      string `json:"role,omitempty"`
	Region    string `json:"region,omitempty"`
	// Source is the profile a chained profile authenticates through; Account and Role are
	// then the chain's target role.
	Source    string `json:"source,omitempty"`
	Program   string `json:"program,omitempty"`
	Service   string `json:"service,omitempty"`
	Operation string `json:"operation,omitempty"`
	// ExitCode and DurationMs are set when the command has ended.
	ExitCode   *int   `json:"exitCode,omitempty"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	// Refused is set instead when the wrapper refused the run before the command started.
	Refused string `json:"refused,omitempty"`
}

var (
	programRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	profileRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	accountRe = regexp.MustCompile(`^[0-9]{12}$`)
	roleRe    = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
	regionRe  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	fileRe    = regexp.MustCompile(`^[0-9]{19}-[0-9a-f]{8}\.json$`)
)

// keep returns v when it matches re, "" otherwise.
func keep(v string, re *regexp.Regexp) string {
	if re.MatchString(v) {
		return v
	}
	return ""
}

// Identity is what the wrapper resolved the run to.
type Identity struct {
	Account, Role, Region, Source string
}

// Run is one row being written.
type Run struct {
	path  string
	row   Row
	start time.Time
}

// root is the spool's root; a variable so tests can move it.
var root = func() string { return filepath.Join(paths.AgentStateDir(), "cloud-runs") }

// now is the clock; a variable for tests.
var now = time.Now

// dirFor is the spool a run reporting sessionName writes to: that session's, when the Agent
// has a session of that name, and the workspace's otherwise.
func dirFor(sessionName string) string {
	if session.ValidName(sessionName) {
		if _, ok := session.ReadMeta(sessionName); ok {
			return filepath.Join(root(), sessionName)
		}
	}
	return filepath.Join(root(), workspaceDir)
}

// Begin writes the row of a run that is about to start (or that the wrapper refuses, which
// Refuse completes at once). argv is the command; only its program name and, for aws, a
// service and operation the CLI knows are kept. A failure to write is not the run's
// failure: the row is visibility, and the command still runs.
func Begin(provider, sessionName, profile string, argv []string) *Run {
	t := now()
	row := Row{Provider: provider, StartedAt: t.UTC().Format(time.RFC3339Nano), Profile: keep(profile, profileRe)}
	if session.ValidName(sessionName) {
		row.Session = sessionName
	}
	row.Program, row.Service, row.Operation = describe(argv)
	var b [4]byte
	_, _ = rand.Read(b[:])
	row.ID = fmt.Sprintf("%019d-%s", t.UnixNano(), hex.EncodeToString(b[:]))
	dir := dirFor(sessionName)
	r := &Run{path: filepath.Join(dir, row.ID+".json"), row: row, start: t}
	if err := os.MkdirAll(dir, 0o700); err == nil {
		r.write()
		prune(dir, dir == filepath.Join(root(), workspaceDir), t)
	}
	return r
}

// Resolved records the identity the wrapper resolved; it is written with the row's end.
func (r *Run) Resolved(id Identity) {
	if r == nil {
		return
	}
	r.row.Account = keep(id.Account, accountRe)
	r.row.Role = keep(id.Role, roleRe)
	r.row.Region = keep(id.Region, regionRe)
	r.row.Source = keep(id.Source, profileRe)
	r.write()
}

// Finish records how the command ended.
func (r *Run) Finish(code int) {
	if r == nil {
		return
	}
	t := now()
	d := t.Sub(r.start).Milliseconds()
	r.row.EndedAt = t.UTC().Format(time.RFC3339Nano)
	r.row.ExitCode, r.row.DurationMs = &code, &d
	r.write()
}

// Refuse records that the wrapper refused the run before it started.
func (r *Run) Refuse(reason string) {
	if r == nil {
		return
	}
	r.row.EndedAt = now().UTC().Format(time.RFC3339Nano)
	r.row.Refused = reason
	r.write()
}

func (r *Run) write() {
	b, err := json.Marshal(r.row)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".row-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), r.path) != nil {
		os.Remove(tmp.Name())
	}
}

// describe keeps the program's base name and, for `aws <service> <operation>`, the two
// names when the CLI knows both. A first argument that starts with "-" (a global option
// before the service) keeps neither: its value may sit where a service would.
func describe(argv []string) (program, service, operation string) {
	if len(argv) == 0 {
		return "", "", ""
	}
	program = keep(filepath.Base(argv[0]), programRe)
	if program != "aws" || len(argv) < 3 || strings.HasPrefix(argv[1], "-") {
		return program, "", ""
	}
	if knownAWSCommand(argv[1], argv[2]) {
		return program, argv[1], argv[2]
	}
	return program, "", ""
}

// prune keeps the newest rows of dir: perSession of them, or, for the workspace's spool,
// those younger than workspaceKeep and at most workspaceMax.
func prune(dir string, workspace bool, t time.Time) {
	names := rowFiles(dir)
	max := perSession
	if workspace {
		max = workspaceMax
		cut := t.Add(-workspaceKeep).UnixNano()
		kept := names[:0]
		for _, n := range names {
			if stamp(n) < cut {
				os.Remove(filepath.Join(dir, n))
				continue
			}
			kept = append(kept, n)
		}
		names = kept
	}
	for len(names) > max {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// rowFiles lists the row files of dir, oldest first (the name starts with the start time).
func rowFiles(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && fileRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func stamp(name string) int64 {
	var n int64
	fmt.Sscanf(name[:19], "%d", &n)
	return n
}

func readRows(dir string, names []string) []Row {
	out := make([]Row, 0, len(names))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var r Row
		if json.Unmarshal(b, &r) == nil && r.ID+".json" == n {
			out = append(out, r)
		}
	}
	return out
}

// ForSession returns the rows of sessionName, oldest first.
func ForSession(sessionName string) []Row {
	if !session.ValidName(sessionName) {
		return nil
	}
	dir := filepath.Join(root(), sessionName)
	return readRows(dir, rowFiles(dir))
}

// Recent returns the newest n rows across every session and the workspace's spool, newest
// first.
func Recent(n int) []Row {
	ents, err := os.ReadDir(root())
	if err != nil {
		return nil
	}
	type ref struct{ dir, name string }
	var all []ref
	for _, e := range ents {
		if !e.IsDir() || (e.Name() != workspaceDir && !session.ValidName(e.Name())) {
			continue
		}
		dir := filepath.Join(root(), e.Name())
		for _, f := range rowFiles(dir) {
			all = append(all, ref{dir, f})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name > all[j].name })
	if len(all) > n {
		all = all[:n]
	}
	out := make([]Row, 0, len(all))
	for _, a := range all {
		out = append(out, readRows(a.dir, []string{a.name})...)
	}
	return out
}

// Drop removes sessionName's spool, when the session is gone for good.
func Drop(sessionName string) {
	if session.ValidName(sessionName) {
		os.RemoveAll(filepath.Join(root(), sessionName))
	}
}
