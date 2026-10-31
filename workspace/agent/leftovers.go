package main

// Leftovers in home that nothing reads again, removed once per Agent boot and on request
// from the Settings "Machine" tab.
//
//   GET    /cleanup/leftovers        → per kind, what a prune would remove right now
//   DELETE /cleanup/leftovers/{kind} → remove it
//
// The boot pass and the button run the same functions, so the keep rules live only here:
//
//   - chromium: throwaway profiles (scoped_dir*) that `chromium --headless` without
//     --user-data-dir makes and removes only on a clean exit; a killed run leaves ~84M.
//   - af-work: ~/.af-work/<name> whose name is no session (live or in the trash) and no
//     working copy's directory name (Managed sessions key it by working copy). Boot removes
//     only the ones the Agent marked as its own; the rest wait for the button.
//   - node: patches the entrypoint's per-boot install superseded; nodeBinFor always takes
//     the highest patch of a major, so the older ones are never run again.
//   - kiro: kas/<version>-<hash> of kiro versions other than the installed one and the pin.
//
// Measured on one Workspace: 6.6G + 743M of scoped_dir, 1.8G of orphaned ~/.af-work, 408M
// of node patches and 528M of kiro (docs/log/116).
//
// Unlike the tool caches (tool_caches.go) removing these costs nothing later, which is why
// they go without asking at boot and sit in their own section in the Console.
//
// What always stays, whatever the kind: anything changed within freshVersionAge (it may
// belong to something being set up right now), anything a process has as its executable, a
// mapping, an open file or its working directory (versionsInUse), and anything the kind
// cannot classify. When in doubt nothing of that kind goes.

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// leftoverKind is one kind of leftover the prune knows how to judge.
type leftoverKind struct {
	Name string
	// mention is what an unreadable process's argv has to contain for it to count as
	// possibly using this kind (cliVersionStore.cmdlineMentions).
	mention string
	// roots are the directories whose children are candidates.
	roots func(home string) []string
	// candidates returns the children of roots that are leftovers by this kind's own rule;
	// the shared keep rules are applied afterwards. roots holds only the ones that exist.
	candidates func(home string, roots []string, pins map[string]string) []leftover
	// place is the folder the Console shows for the row.
	place func(home string) string
	// prepare, when set, runs at the start of every prune (not of a survey) for what the
	// judgement needs written first (af-work stamps archived sessions that have no date).
	prepare func()
}

// leftover is one directory to remove, plus files beside it that go with it.
type leftover struct {
	root, name string
	// extra names siblings in root removed after the directory (kiro's <dir>.lock).
	extra []string
	// needsConfirm = the boot pass leaves it alone and only the Settings button removes it:
	// the directory is not provably ours (af-work without the Agent's marker).
	needsConfirm bool
	// stillDue, when set, is asked again right before the removal: the verdict was reached
	// from a listing, and the state it rested on (an archived session) may have changed since.
	stillDue func() bool
}

func (l leftover) path() string { return filepath.Join(l.root, l.name) }

var leftoverKinds = []leftoverKind{
	{Name: "chromium", mention: "chrom", roots: chromiumProfileRoots, candidates: chromiumLeftovers,
		place: func(h string) string { return filepath.Join(h, ".config", "chromium-headless") }},
	{Name: "af-work", mention: ".af-work", roots: afWorkRoots, candidates: afWorkLeftovers, prepare: stampArchivedSessions,
		place: func(h string) string { return filepath.Join(h, ".af-work") }},
	{Name: "node", mention: "node", roots: func(string) []string { return []string{nvmNodeRoot()} }, candidates: nodeLeftovers,
		place: func(string) string { return nvmNodeRoot() }},
	{Name: "kiro", mention: "kiro", roots: kiroKasRoots, candidates: kiroLeftovers,
		place: func(h string) string { return kiroKasRoots(h)[0] }},
}

func findLeftoverKind(name string) (leftoverKind, bool) {
	for _, k := range leftoverKinds {
		if k.Name == name {
			return k, true
		}
	}
	return leftoverKind{}, false
}

// leftoverMu keeps the boot pass and the Console's survey and delete from judging and
// removing the same tree at once.
var leftoverMu sync.Mutex

// pruneable returns this kind's leftovers that pass the shared keep rules.
func (k leftoverKind) pruneable(home string, pins map[string]string) []leftover {
	var roots []string
	for _, r := range k.roots(home) {
		if info, err := os.Stat(r); err == nil && info.IsDir() {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	cands := k.candidates(home, roots, pins)
	if len(cands) == 0 {
		return nil
	}
	store := cliVersionStore{name: k.Name, mention: k.mention, roots: roots,
		exeVersion: func(string) (string, bool, bool) { return "", false, true }}
	inUse, err := store.versionsInUse()
	if err != nil {
		log.Printf("leftovers: %s: %v; keeping all of it", k.Name, err)
		return nil
	}
	var out []leftover
	for _, c := range cands {
		if inUse[c.name] {
			continue
		}
		used := false
		for _, x := range c.extra {
			used = used || inUse[x]
		}
		if used {
			continue
		}
		// Lstat: a symlink is not ours to follow, and its target may be anything.
		info, err := os.Lstat(c.path())
		if err != nil || !info.IsDir() || pruneNow().Sub(info.ModTime()) < freshVersionAge {
			continue
		}
		out = append(out, c)
	}
	return out
}

// leftoverSize is what removing l frees.
func leftoverSize(l leftover, budget *int) (bytes int64, files int) {
	bytes, files = walkSize(l.path(), budget)
	for _, x := range l.extra {
		if info, err := os.Lstat(filepath.Join(l.root, x)); err == nil && info.Mode().IsRegular() {
			bytes += info.Size()
			files++
		}
	}
	return bytes, files
}

// removeLeftover is removeTree; a var so a test can make one removal fail.
var removeLeftover = removeTree

type leftoverRemoved struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
	// Failed is the first removal error when some went and some did not; the Console says so
	// next to the count instead of reporting a clean success.
	Failed string `json:"failed,omitempty"`
}

// prune removes this kind's leftovers and says what went. boot is the unattended pass: it
// skips what needsConfirm, which only the button (boot=false) removes. Every removal is
// logged by path, so a loss can be explained per directory. The caller holds leftoverMu.
func (k leftoverKind) prune(home string, pins map[string]string, boot bool) (leftoverRemoved, error) {
	res := leftoverRemoved{Kind: k.Name}
	var firstErr error
	if k.prepare != nil {
		k.prepare()
	}
	for _, l := range k.pruneable(home, pins) {
		if boot && l.needsConfirm {
			continue
		}
		budget := toolCacheMaxEntries
		bytes, _ := leftoverSize(l, &budget)
		// After the size walk, right before the removal: the walk is the long part, and a
		// restore landing during it must still keep the folder.
		if l.stillDue != nil && !l.stillDue() {
			continue
		}
		if err := removeLeftover(l.path()); err != nil {
			log.Printf("leftovers: remove %s: %v", l.path(), err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, x := range l.extra {
			_ = os.Remove(filepath.Join(l.root, x))
		}
		log.Printf("leftovers: removed %s (%d KiB)", l.path(), bytes>>10)
		res.Count++
		res.Bytes += bytes
	}
	return res, firstErr
}

// workspacePins reads versions.json. ok=false outside a Workspace image (e.g. the native
// runtime on someone's own machine), where home is not ours to tidy.
func workspacePins() (map[string]string, bool) {
	b, err := os.ReadFile(buildPinsPath)
	if err != nil {
		return nil, false
	}
	pins := map[string]string{}
	if json.Unmarshal(b, &pins) != nil {
		return nil, false
	}
	return pins, true
}

// leftoverInterval is how often the unattended pass runs after the boot one: Agents stay up
// for weeks, and an archived session's folder comes due while one is running.
const leftoverInterval = 24 * time.Hour

// pruneLeftoversDaily runs the unattended pass every interval until stop closes. It shares
// leftoverMu with the boot pass and the Settings button, so no two of them judge or remove at
// once; a tick that finds one running waits for it and then judges afresh.
func pruneLeftoversDaily(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			pruneLeftoversAtBoot()
		}
	}
}

// pruneLeftoversAtBoot is the unattended pass: run at boot and again by pruneLeftoversDaily.
func pruneLeftoversAtBoot() {
	pins, ok := workspacePins()
	if !ok {
		return
	}
	leftoverMu.Lock()
	defer leftoverMu.Unlock()
	for _, k := range leftoverKinds {
		r, _ := k.prune(homeDir(), pins, true)
		if r.Count > 0 {
			log.Printf("leftovers: removed %d %s (%d MiB)", r.Count, r.Kind, r.Bytes>>20)
		}
	}
}

// --- chromium ---------------------------------------------------------------------------

// chromiumProducts are the profile directory names the chromium builds in the image use
// under ~/.config and ~/.cache.
var chromiumProducts = []string{"chromium-headless", "chromium", "google-chrome-for-testing"}

// hostnameFn is os.Hostname; a var so tests can set it.
var hostnameFn = os.Hostname

func chromiumProfileRoots(home string) []string {
	var out []string
	for _, base := range []string{".config", ".cache"} {
		for _, p := range chromiumProducts {
			out = append(out, filepath.Join(home, base, p))
		}
	}
	return out
}

// chromiumLeftovers takes every scoped_dir* whose profile no live chromium holds. The cache
// half has no lock of its own, so it follows its twin under ~/.config: gone (the clean exit
// removed only the profile) or dead means the cache half is dead too. Other entries —
// Default, Local State, a profile somebody chose — are never candidates.
func chromiumLeftovers(home string, roots []string, _ map[string]string) []leftover {
	var out []leftover
	for _, root := range roots {
		product := filepath.Base(root)
		twinRoot := filepath.Join(home, ".config", product)
		ents, _ := os.ReadDir(root)
		for _, e := range ents {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "scoped_dir") {
				continue
			}
			if chromiumProfileLive(filepath.Join(twinRoot, e.Name())) {
				continue
			}
			out = append(out, leftover{root: root, name: e.Name()})
		}
	}
	return out
}

// chromiumProfileLive reads the profile's SingletonLock, a symlink to "<hostname>-<pid>".
// It is dead when missing (so is a twin that is gone), when it names another host (a container since recreated: every
// lock on the measured Workspace did) or when the pid is gone here. Anything it cannot read
// counts as live.
func chromiumProfileLive(dir string) bool {
	t, err := os.Readlink(filepath.Join(dir, "SingletonLock"))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(t, '-')
	if i <= 0 {
		return true
	}
	pid, err := strconv.Atoi(t[i+1:])
	if err != nil || pid <= 0 {
		return true
	}
	host, err := hostnameFn()
	if err != nil {
		return true
	}
	if t[:i] != host {
		return false
	}
	return pidAlive(pid)
}

// pidAlive says whether pid exists in this container; unreadable counts as alive.
func pidAlive(pid int) bool {
	_, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid)))
	return !os.IsNotExist(err)
}

// --- af-work ----------------------------------------------------------------------------

func afWorkRoots(home string) []string { return []string{filepath.Join(home, ".af-work")} }

// archivedScratchTTL is how long an archived session's ~/.af-work/<name> lives after the
// session was archived.
const archivedScratchTTL = 7 * 24 * time.Hour

// afWorkLeftovers takes the directories no session and no working copy is named after, plus
// the folder of each session archived archivedScratchTTL ago or more. Deleting a session
// removes its directory already (removeSessionSideFiles); what is left predates that, was
// keyed by a working copy that is gone, or belongs to a session on the shelf.
//
// Only a directory carrying paths.SessionWorkDirMarker (written where the Agent creates it)
// is removed at boot; any other is a candidate for the Settings button alone, because members
// put their own folders there and a restart must not delete them silently.
//
// Kept whatever the age: a locked session's folder, and a folder named like a working copy or
// like another session's working directory (Managed sessions key it by working copy).
func afWorkLeftovers(_ string, roots []string, _ map[string]string) []leftover {
	scan, ok := scanAfWorkSessions()
	if !ok {
		return nil
	}
	known := scan.known
	repos, err := os.ReadDir(gitx.ReposRoot())
	if err != nil && !os.IsNotExist(err) {
		return nil
	}
	for _, e := range repos {
		known[e.Name()] = true
	}
	var out []leftover
	for _, root := range roots {
		ents, _ := os.ReadDir(root)
		for _, e := range ents {
			if !e.IsDir() || known[e.Name()] {
				continue
			}
			_, err := os.Lstat(filepath.Join(root, e.Name(), paths.SessionWorkDirMarker))
			l := leftover{root: root, name: e.Name(), needsConfirm: err != nil}
			if scan.expired[e.Name()] {
				name := e.Name()
				l.stillDue = func() bool { return archivedScratchDue(name) }
			}
			out = append(out, l)
		}
	}
	return out
}

// afWorkScan is what one read of the session store says about ~/.af-work.
type afWorkScan struct {
	// known holds every name whose folder stays: sessions that are live, locked, in the trash
	// or archived less than archivedScratchTTL ago (or with no usable date yet), and each live
	// session's working-copy name.
	known map[string]bool
	// expired holds archived, unlocked sessions whose date is archivedScratchTTL old. Their
	// own name is left out of known; their working-copy name is not.
	expired map[string]bool
	// unstamped names the archived sessions with no usable archivedAt.
	unstamped []string
}

// archivedScratchDue reads name's meta as it is now: still archived, unlocked and dated a
// week ago or more. A restore that raced the listing makes it false.
func archivedScratchDue(name string) bool {
	m, ok := session.ReadMeta(name)
	if !ok {
		return false
	}
	return m.Archived && !m.Locked && archivedAtDue(m.ArchivedAt)
}

// archivedAtDue says whether an archivedAt stamp is old enough. An empty, unparseable or
// future value is not: when in doubt the folder stays.
func archivedAtDue(stamp string) bool {
	t, err := time.Parse(time.RFC3339, stamp)
	return err == nil && pruneNow().Sub(t) >= archivedScratchTTL
}

// archivedStampFutureSlack is how far past now a stamp may sit before it is taken as written
// by a clock that was wrong, not as a date.
const archivedStampFutureSlack = 24 * time.Hour

// stampArchivedSessions gives every archived session that has no date "now", so a session
// archived before archivedAt existed goes a week after the rollout, not at the first boot. The
// date is written through the session lock onto the meta as it is at that moment (a stale
// snapshot written back would undo a lock or bring back a deleted session). Nothing is
// stamped when the session store cannot be read in full.
func stampArchivedSessions() {
	scan, ok := scanAfWorkSessions()
	if !ok {
		return
	}
	for _, name := range scan.unstamped {
		sessionx.UpdateSessionMeta(name, func(cur *session.Meta) bool {
			if !cur.Archived || cur.ArchivedAt != "" && !archivedStampUnusable(cur.ArchivedAt) {
				return false
			}
			cur.ArchivedAt = pruneNow().UTC().Format(time.RFC3339)
			return true
		})
	}
}

// archivedStampUnusable: unparseable, or further ahead of now than a clock step explains. The
// next stamping pass replaces it with now, so a bad clock cannot keep a folder for ever.
func archivedStampUnusable(stamp string) bool {
	t, err := time.Parse(time.RFC3339, stamp)
	return err != nil || t.Sub(pruneNow()) > archivedStampFutureSlack
}

// afWorkKnownNames is scanAfWorkSessions reduced to the names that stay. ok=false when any
// record cannot be read or parsed.
func afWorkKnownNames() (map[string]bool, bool) {
	scan, ok := scanAfWorkSessions()
	if !ok {
		return nil, false
	}
	return scan.known, true
}

// scanAfWorkSessions collects every session name (live and in the trash) and each live
// session's working-copy name. ok=false when any record cannot be read or parsed:
// session.ListMetas and listCleanupArchives skip such a record silently, and WriteMeta is not
// atomic, so a meta caught mid-write would make that session's directory look orphaned.
// Without a readable session store every name looks orphaned, so that is false too.
func scanAfWorkSessions() (afWorkScan, bool) {
	scan := afWorkScan{known: map[string]bool{}, expired: map[string]bool{}}
	known := scan.known
	read := func(dir string, into func([]byte) bool) bool {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if os.IsNotExist(err) {
				continue // removed since the listing: a delete, not a record to protect
			}
			if err != nil || !into(b) {
				return false
			}
		}
		return true
	}
	if !read(session.MetaDir(), func(b []byte) bool {
		var m session.Meta
		if json.Unmarshal(b, &m) != nil || m.Name == "" {
			return false
		}
		switch {
		case m.Archived && !m.Locked && archivedAtDue(m.ArchivedAt):
			scan.expired[m.Name] = true
		default:
			known[m.Name] = true
			if m.Archived && (m.ArchivedAt == "" || archivedStampUnusable(m.ArchivedAt)) {
				scan.unstamped = append(scan.unstamped, m.Name)
			}
		}
		if m.Dir != "" {
			known[filepath.Base(m.Dir)] = true
		}
		return true
	}) {
		return afWorkScan{}, false
	}
	trash := cleanupStoreDir()
	if _, err := os.Stat(trash); err == nil || !os.IsNotExist(err) {
		if !read(trash, func(b []byte) bool {
			var m cleanupManifest
			if json.Unmarshal(b, &m) != nil || m.ID == "" {
				return false
			}
			for _, s := range m.Sessions {
				known[s.Name] = true
			}
			return true
		}) {
			return afWorkScan{}, false
		}
	}
	return scan, true
}

// --- node -------------------------------------------------------------------------------

// nodeLeftovers keeps, per major, the highest runnable patch (bin/node present, compared as
// nodeBinFor does, so it is the one sessions get), any version an nvm alias names exactly, and
// any version on the Agent's own PATH (what the entrypoint put there). A name that is not a
// version, or a version without bin/node (half-installed, or not nvm's layout), is left alone:
// counting it as the highest would delete the only patch that runs.
func nodeLeftovers(home string, roots []string, _ map[string]string) []leftover {
	keep := map[string]bool{}
	for _, v := range nvmAliasTargets(home) {
		keep[v] = true
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		for _, r := range roots {
			if v := versionUnder(r, p); v != "" {
				keep[v] = true
			}
		}
	}
	var out []leftover
	for _, root := range roots {
		ents, _ := os.ReadDir(root)
		best := map[int][]int{}
		type ver struct {
			name string
			v    []int
		}
		var vers []ver
		for _, e := range ents {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
				continue
			}
			v := parseDotted(strings.TrimPrefix(e.Name(), "v"))
			if len(v) != 3 {
				continue
			}
			if info, err := os.Stat(filepath.Join(root, e.Name(), "bin", "node")); err != nil || info.IsDir() {
				continue
			}
			vers = append(vers, ver{e.Name(), v})
			if b, ok := best[v[0]]; !ok || compareDotted(v, b) > 0 {
				best[v[0]] = v
			}
		}
		for _, x := range vers {
			if keep[x.name] || compareDotted(x.v, best[x.v[0]]) == 0 {
				continue
			}
			out = append(out, leftover{root: root, name: x.name})
		}
	}
	return out
}

// nvmAliasTargets returns the exact versions ("v22.23.1") that someone's own aliases under
// ~/.nvm/alias name. Aliases that name a major or another alias ("22", "lts/*") resolve to
// the highest patch, which is kept anyway. alias/lts/ is skipped: it is nvm's copy of the
// remote LTS index, rewritten by every ls-remote, not anyone's choice (measured: lts/jod
// named a superseded v22.23.2 and would have kept it for good).
func nvmAliasTargets(home string) []string {
	var out []string
	root := filepath.Join(home, ".nvm", "alias")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == filepath.Join(root, "lts") {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		v := "v" + strings.TrimPrefix(strings.TrimSpace(string(b)), "v")
		if len(parseDotted(v[1:])) == 3 {
			out = append(out, v)
		}
		return nil
	})
	return out
}

// --- kiro -------------------------------------------------------------------------------

var kiroKasName = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)-[0-9a-f]+$`)

func kiroKasRoots(home string) []string {
	return []string{filepath.Join(home, ".local", "share", "kiro-cli", "kas")}
}

// kiroLeftovers keeps the installed version (install-kiro's marker), the pin, the highest
// version present, and any version whose .lock names a live pid. Without the marker which
// version is current cannot be told and nothing goes.
func kiroLeftovers(home string, roots []string, pins map[string]string) []leftover {
	b, err := os.ReadFile(kiroVersionMarkerPath(filepath.Join(home, ".local", "bin")))
	cur := strings.TrimSpace(string(b))
	if err != nil || cur == "" {
		return nil
	}
	keep := map[string]bool{cur: true, pins["kiro"]: true}
	var out []leftover
	for _, root := range roots {
		ents, _ := os.ReadDir(root)
		var highest []int
		var cands []leftover
		var candVers []string
		for _, e := range ents {
			m := kiroKasName.FindStringSubmatch(e.Name())
			if !e.IsDir() || m == nil {
				continue
			}
			if v := parseDotted(m[1]); highest == nil || compareDotted(v, highest) > 0 {
				highest = v
			}
			if keep[m[1]] || kiroLockLive(filepath.Join(root, e.Name()+".lock")) {
				continue
			}
			cands = append(cands, leftover{root: root, name: e.Name(), extra: []string{e.Name() + ".lock"}})
			candVers = append(candVers, m[1])
		}
		for i, c := range cands {
			if compareDotted(parseDotted(candVers[i]), highest) != 0 {
				out = append(out, c)
			}
		}
	}
	return out
}

// kiroLockLive reads {"pid":N} from a kas lock; a lock that is missing is not held, one that
// cannot be read or parsed counts as held.
func kiroLockLive(path string) bool {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	var l struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &l) != nil || l.PID <= 0 {
		return true
	}
	return pidAlive(l.PID)
}

// --- HTTP -------------------------------------------------------------------------------

type leftoverRow struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
	usagePlace
}

type leftoverUsage struct {
	Kinds []leftoverRow `json:"kinds"`
	// Unsupported = not a Workspace image; the Console hides the section.
	Unsupported bool `json:"unsupported,omitempty"`
	// Truncated = a walk hit its entry cap; the figures are lower bounds.
	Truncated  bool   `json:"truncated,omitempty"`
	MeasuredAt string `json:"measured_at"`
}

// handleLeftoverUsage (GET /cleanup/leftovers). No answer is held: a survey is a press in
// the Console, and a held figure would disagree with what the delete right after it takes.
func handleLeftoverUsage(w http.ResponseWriter, r *http.Request) {
	u := &leftoverUsage{MeasuredAt: time.Now().UTC().Format(time.RFC3339), Kinds: []leftoverRow{}}
	pins, ok := workspacePins()
	if !ok {
		u.Unsupported = true
		httpx.WriteJSON(w, http.StatusOK, u)
		return
	}
	leftoverMu.Lock()
	defer leftoverMu.Unlock()
	home := homeDir()
	for _, k := range leftoverKinds {
		row := leftoverRow{Kind: k.Name, usagePlace: placeOf(k.place(home))}
		budget := toolCacheMaxEntries
		for _, l := range k.pruneable(home, pins) {
			b, _ := leftoverSize(l, &budget)
			row.Count++
			row.Bytes += b
		}
		u.Truncated = u.Truncated || budget <= 0
		u.Kinds = append(u.Kinds, row)
	}
	httpx.WriteJSON(w, http.StatusOK, u)
}

// handleDeleteLeftovers (DELETE /cleanup/leftovers/{kind}) runs the boot prune for one kind;
// the keep rules are judged again here, not taken from the survey.
func handleDeleteLeftovers(w http.ResponseWriter, r *http.Request) {
	k, ok := findLeftoverKind(r.PathValue("kind"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_kind", "unknown kind: "+r.PathValue("kind"))
		return
	}
	pins, ok := workspacePins()
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, "unsupported", "not a Workspace image")
		return
	}
	leftoverMu.Lock()
	res, err := k.prune(homeDir(), pins, false)
	leftoverMu.Unlock()
	if err != nil && res.Count == 0 {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	if err != nil {
		res.Failed = err.Error()
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
