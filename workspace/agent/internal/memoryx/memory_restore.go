package memoryx

// Agent memory version control (docs/log/39 ④ / ADR 0022 decision 4) — restore.
//
// History is never rewritten. restore writes the contents of a point in time back into live and
// stacks the result as a new commit, and it always takes a pre-restore snapshot before applying,
// so undoing the undo is always possible (★2).
//
//	resolve rev ──▶ ① pre-restore snapshot (preserve the current live)
//	            ──▶ ② staging to the rev's contents (scope-limited checkout)
//	            ──▶ ③ staging → live (overwrite inside the allowlist + delete what vanished)
//	            ──▶ ④ restore snapshot (AF-Trigger: restore, source recorded in a trailer)
//
// ③ is the heart of this file: it guarantees the reverse of "never read what must not enter the
// repo" (★1, memory_roots.go) — "never write or delete outside the allowlist". The live side is
// enumerated only through memoryCollect (the path where the allowlist and the refusal to follow
// symlinks apply), and every write target is checked one segment at a time, refusing a path with
// a symlink anywhere along it.

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// memoryUserErr is the kind of failure caused by bad caller input. The engine classifies it
// here so the REST layer can copy it straight into a status and a stable code.
type memoryUserErr struct {
	Status int
	Code   string
	Msg    string
}

func (e *memoryUserErr) Error() string { return e.Msg }

func memoryErrf(status int, code, format string, args ...any) error {
	return &memoryUserErr{Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// memoryRestoreScope is the restore range: docs/log/39's `{all | projects: [slug...]}` plus
// kinds, for pointing at a whole Scopes=false root such as codex's.
type memoryRestoreScope struct {
	All      bool     `json:"all"`
	Kinds    []string `json:"kinds"`
	Projects []string `json:"projects"`
}

// memoryScopeTarget is a resolved restore unit. Repo is the prefix inside the bare repo, Rel is
// relative to root.Dir ("" = the whole root).
type memoryScopeTarget struct {
	Root memoryRoot
	Repo string
	Rel  string
}

// memoryApplyOpts is the variant of the apply. The zero value is docs/log/39 ④ as written,
// "take only the contents"; Adopt adds re-pointing the lineage (= migration, making the
// imported history this environment's history). The steps that write the imported contents
// into live do not change by a single byte — all that changes is which lineage main points at,
// so the allowlist-derived defence (the reverse of ★1) still applies unchanged.
type memoryApplyOpts struct {
	// Adopt=true: after applying, re-point main at from's lineage. The previous main is parked
	// on a rescue ref (refs/premigrate/<ts>), so no history is ever lost.
	Adopt bool
}

// memoryRestoreResult is the result of one restore. It returns both revs, pre-restore and
// restore, so the UI can show not only "restored" but also "the state before it is here".
type memoryRestoreResult struct {
	From       string             `json:"from"`                 // source snapshot (resolved sha)
	PreRestore string             `json:"preRestore,omitempty"` // preservation snapshot stacked just before
	Rev        string             `json:"rev,omitempty"`        // restore commit
	Committed  bool               `json:"committed"`            // false = the contents were already identical
	Scopes     []string           `json:"scopes"`               // repo-internal prefixes that were applied
	Written    []string           `json:"written"`              // paths written into live (repo notation)
	Deleted    []string           `json:"deleted"`              // paths deleted from live (same notation)
	Projects   []memoryProjectRef `json:"projects"`             // projects the restore commit touched
	Busy       bool               `json:"busy"`                 // the target kind had a running session
	// The rest is for a migration (Adopt) only. Replaced is the sha of the parked previous main
	// and ReplacedRef is where it was parked. Unless the UI can say "the pre-migration history
	// is here", the swap looks like an irreversible operation.
	Adopted     bool   `json:"adopted,omitempty"`
	Replaced    string `json:"replaced,omitempty"`
	ReplacedRef string `json:"replacedRef,omitempty"`
}

// memoryRestore runs the docs/log/39 ④ procedure as written. now comes from the caller so that
// tests can verify deterministically (the same convention snapshot uses).
func memoryRestore(sc memoryRestoreScope, rev, at string, now time.Time) (memoryRestoreResult, error) {
	// Shares staging / the index with snapshot, so it is mutually exclusive with the automatic
	// snapshot loop.
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	res := memoryRestoreResult{Scopes: []string{}, Written: []string{}, Deleted: []string{}, Projects: []memoryProjectRef{}}
	if err := memoryEnsureRepo(); err != nil {
		return res, err
	}
	if !memoryHasCommits() {
		return res, memoryErrf(http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
	}
	from, err := memoryResolveRev(rev, at)
	if err != nil {
		return res, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "%s", err.Error())
	}
	return memoryApplyRevLocked(sc, from, memoryTriggerRestore, nil, now, memoryApplyOpts{})
}

// memoryApplyRev is the shared path that writes a resolved commit's contents back into live,
// scope by scope. restore (go back to a point in history) and import's apply (take the contents
// of an imported lineage) differ only in where the commit comes from, so only the trigger and
// the trailers are parameters.
func memoryApplyRev(sc memoryRestoreScope, from, trigger string, extraTrailers []string, now time.Time, opts memoryApplyOpts) (memoryRestoreResult, error) {
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	if err := memoryEnsureRepo(); err != nil {
		return memoryRestoreResult{}, err
	}
	return memoryApplyRevLocked(sc, from, trigger, extraTrailers, now, opts)
}

// memoryApplyRevLocked is the body, with memorySnapshotMu held (the docs/log/39 ④ procedure
// itself).
func memoryApplyRevLocked(sc memoryRestoreScope, from, trigger string, extraTrailers []string, now time.Time, opts memoryApplyOpts) (memoryRestoreResult, error) {
	res := memoryRestoreResult{From: from, Scopes: []string{}, Written: []string{}, Deleted: []string{}, Projects: []memoryProjectRef{}}
	targets, err := memoryResolveScope(sc)
	if err != nil {
		return res, err
	}
	busy := memoryBusyKinds()
	for _, t := range targets {
		res.Scopes = append(res.Scopes, t.Repo)
		if busy[t.Root.Kind] {
			// Do not stop for a running session (docs/log/39 ④-5: continuing is the default).
			// Whatever it writes afterwards simply shows up as a new snapshot after the restore,
			// so it stays traceable.
			res.Busy = true
		}
	}

	// ① Always preserve the current live. On failure here, back out without breaking anything.
	pre, err := memorySnapshotLocked(memoryTriggerPreRestore, now, "AF-Restore-Rev: "+from)
	if err != nil {
		return res, fmt.Errorf("pre-restore snapshot: %w", err)
	}
	// When nothing changed and no commit was stacked, the latest snapshot already IS "the state
	// before the restore", so return that one: the UI can always point at where undoing the undo
	// leads (★2).
	res.PreRestore = pre.Rev
	if !pre.Committed {
		if head, herr := memoryGitRun("rev-parse", memoryBranch); herr == nil {
			res.PreRestore = head
		}
	}

	// ② staging to the rev's contents (per scope). A prefix missing from the rev means it was
	//    empty back then, so leaving it deleted is the correct restore.
	staging := memoryStagingDir()
	for _, t := range targets {
		dir := filepath.Join(staging, filepath.FromSlash(t.Repo))
		if err := os.RemoveAll(dir); err != nil {
			return res, fmt.Errorf("reset staging %s: %w", t.Repo, err)
		}
		listed, lerr := memoryGitRun("ls-tree", "-r", "--name-only", from, "--", t.Repo)
		if lerr != nil {
			return res, fmt.Errorf("list %s at %s: %w", t.Repo, from[:8], lerr)
		}
		if strings.TrimSpace(listed) == "" {
			continue
		}
		if _, err := memoryGitRun("checkout", from, "--", t.Repo); err != nil {
			return res, fmt.Errorf("checkout %s at %s: %w", t.Repo, from[:8], err)
		}
	}

	// ③ staging → live. Write only inside the allowlist, delete only what vanished in the scope.
	for _, t := range targets {
		written, deleted, err := memoryApplyScopeToLive(t, staging)
		res.Written = append(res.Written, written...)
		res.Deleted = append(res.Deleted, deleted...)
		if err != nil {
			return res, fmt.Errorf("apply %s: %w", t.Repo, err)
		}
	}
	sort.Strings(res.Written)
	sort.Strings(res.Deleted)

	// ③.5 migration (Adopt): only here is main re-pointed at from's lineage. It comes after live
	//     has been written so that a failure anywhere in ①-③ leaves history untouched (a failed
	//     migration must not produce "history swapped, contents old"). The previous main is
	//     parked on a rescue ref, so the pre-swap history stays in the repo, out of gc's reach.
	if opts.Adopt {
		prev, _ := memoryGitRun("rev-parse", "--verify", "--quiet", memoryBranch)
		if prev != "" {
			ref := "refs/premigrate/" + now.UTC().Format("20060102T150405Z")
			if _, err := memoryGitRun("update-ref", ref, prev); err != nil {
				return res, fmt.Errorf("stash the replaced lineage: %w", err)
			}
			res.Replaced, res.ReplacedRef = prev, ref
		}
		if _, err := memoryGitRun("update-ref", "refs/heads/"+memoryBranch, from); err != nil {
			return res, fmt.Errorf("adopt the imported lineage: %w", err)
		}
		res.Adopted = true
	}

	// ④ Stack the applied live as the restore commit. This re-reads live, so "what actually
	//    happened" is settled on the history side (③'s result is not trusted).
	trailers := []string{"AF-Restore-Rev: " + from}
	for _, t := range targets {
		trailers = append(trailers, "AF-Restore-Scope: "+t.Repo)
	}
	if res.Replaced != "" {
		trailers = append(trailers, "AF-Premigrate-Rev: "+res.Replaced)
	}
	trailers = append(trailers, extraTrailers...)
	done, err := memorySnapshotLocked(trigger, now, trailers...)
	if err != nil {
		return res, fmt.Errorf("restore snapshot: %w", err)
	}
	res.Committed, res.Rev, res.Projects = done.Committed, done.Rev, done.Projects

	// A migration records that it happened even when the contents are identical. After the
	// re-point, live usually matches the imported head, so letting ★8's no-change skip run would
	// leave no record anywhere that the lineage was swapped (only the rescue ref). An empty
	// commit is allowed just here, carving into the history through trailers which lineage was
	// adopted when and what it replaced.
	if opts.Adopt && !done.Committed {
		msg := memoryCommitMessage(trigger, now, nil, nil, trailers)
		if _, err := memoryGitRun("commit", "--quiet", "--no-verify", "--allow-empty", "-m", msg); err != nil {
			return res, fmt.Errorf("record the migration: %w", err)
		}
		rev, err := memoryGitRun("rev-parse", memoryBranch)
		if err != nil {
			return res, fmt.Errorf("record the migration: %w", err)
		}
		res.Committed, res.Rev = true, rev
	}
	return res, nil
}

// memoryResolveScope reduces the requested scope to a set of repo-internal prefixes. A kind
// that is not in the declaration table, a root this environment does not have and an invalid
// slug are all rejected here.
func memoryResolveScope(sc memoryRestoreScope) ([]memoryScopeTarget, error) {
	roots := memoryRoots()
	if len(roots) == 0 {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "no memory roots are active")
	}
	byKind := map[string]memoryRoot{}
	for _, r := range roots {
		byKind[r.Kind] = r
	}
	var out []memoryScopeTarget
	whole := map[string]bool{} // kinds restored as a whole root
	add := func(t memoryScopeTarget) {
		for _, e := range out {
			if e.Repo == t.Repo {
				return
			}
		}
		out = append(out, t)
	}

	if sc.All {
		for _, r := range roots {
			whole[r.Kind] = true
			add(memoryScopeTarget{Root: r, Repo: r.RepoPrefix})
		}
	}
	for _, k := range sc.Kinds {
		r, ok := byKind[k]
		if !ok {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "unknown memory kind %q", k)
		}
		whole[r.Kind] = true
		add(memoryScopeTarget{Root: r, Repo: r.RepoPrefix})
	}
	for _, slug := range sc.Projects {
		// Only claude has project granularity (codex divides by entries inside a file).
		r, ok := byKind["claude"]
		if !ok || !r.Scopes {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "project scope is not available")
		}
		if !memorySlugSafe(slug) {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "invalid project %q", slug)
		}
		if whole[r.Kind] {
			continue // covered by the whole root, so the individual entry is ignored
		}
		add(memoryScopeTarget{Root: r, Repo: r.RepoPrefix + "/" + slug, Rel: slug})
	}
	if len(out) == 0 {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadScope, "scope must select at least one root or project")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

// memorySlugSafe checks that a claude project slug is a single path segment. A slug is an
// absolute path with its "/" flattened to "-", so starting with "-" is normal; it only ever
// reaches git as a path after `--`, so all that is blocked here is path escape.
func memorySlugSafe(s string) bool {
	if s == "" || s == "." || len(s) > 255 {
		return false
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	return true
}

// memoryRelInScope reports whether rel (relative to root.Dir) is inside scope (also relative to
// root.Dir; "" means everything).
func memoryRelInScope(scope, rel string) bool {
	if scope == "" {
		return true
	}
	return rel == scope || strings.HasPrefix(rel, scope+"/")
}

// memoryApplyScopeToLive reflects staging's scope subtree into live (the equivalent of
// rsync --delete).
//
// The desired state comes from enumerating the staging side, the current state from
// memoryCollect (through the allowlist). It is this asymmetry that limits deletion candidates
// to files that passed the allowlist: structurally there is no path that deletes anything other
// than memory (transcripts, credentials).
func memoryApplyScopeToLive(t memoryScopeTarget, stagingRoot string) (written, deleted []string, err error) {
	written, deleted = []string{}, []string{}
	src := filepath.Join(stagingRoot, filepath.FromSlash(t.Repo))

	// Desired state: the real files on the staging side. They come from the repo, but pass the
	// allowlist once more before being written into live, so that anything that slipped into the
	// repo cannot dirty live.
	desired := map[string]string{}
	werr := filepath.WalkDir(src, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			if os.IsNotExist(e) && p == src {
				return nil // a scope absent from the rev = "delete everything"
			}
			return e
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		sub, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		rel := path.Join(t.Rel, filepath.ToSlash(sub))
		if !memoryAllowed(t.Root, rel) {
			return nil
		}
		desired[rel] = p
		return nil
	})
	if werr != nil {
		return written, deleted, werr
	}

	// Whatever is in live now, inside the scope and absent after the restore = delete.
	for _, f := range memoryCollect(t.Root) {
		if !memoryRelInScope(t.Rel, f.Rel) {
			continue
		}
		if _, keep := desired[f.Rel]; keep {
			continue
		}
		if rerr := os.Remove(f.Abs); rerr != nil && !os.IsNotExist(rerr) {
			return written, deleted, rerr
		}
		memoryPruneEmptyDirs(t.Root.Dir, f.Abs)
		deleted = append(deleted, t.Root.RepoPrefix+"/"+f.Rel)
	}

	// Write only what changes content (moving mtime for nothing muddies the automatic snapshot's
	// trigger decision).
	rels := make([]string, 0, len(desired))
	for rel := range desired {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		dst, perr := memoryPrepareDest(t.Root.Dir, rel)
		if perr != nil {
			return written, deleted, perr
		}
		same, serr := memorySameContent(desired[rel], dst)
		if serr != nil {
			return written, deleted, serr
		}
		if same {
			continue
		}
		if cerr := memoryCopyFile(desired[rel], dst); cerr != nil {
			return written, deleted, cerr
		}
		written = append(written, t.Root.RepoPrefix+"/"+rel)
	}
	return written, deleted, nil
}

// memoryPrepareDest returns the absolute destination path and creates the directories along the
// way. A symlink anywhere on the path is refused: restoring with an outward link planted inside
// the allowlist could overwrite things outside live, credentials included (the reverse of ★1).
func memoryPrepareDest(rootDir, rel string) (string, error) {
	// Some environments have no root yet: a workspace where claude has never been started (which
	// is exactly the situation when importing another environment's memory right after boot) has
	// no <config>/projects. Every level below is created one Mkdir at a time so the path can be
	// checked for symlinks, so MkdirAll appears only here.
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return "", err
	}
	segs := strings.Split(rel, "/")
	cur := rootDir
	for _, s := range segs[:len(segs)-1] {
		cur = filepath.Join(cur, s)
		st, err := os.Lstat(cur)
		switch {
		case err == nil && st.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("refusing to write through symlink %s", cur)
		case err == nil && !st.IsDir():
			return "", fmt.Errorf("%s is not a directory", cur)
		case err == nil:
		case os.IsNotExist(err):
			if mkerr := os.Mkdir(cur, 0o700); mkerr != nil {
				return "", mkerr
			}
		default:
			return "", err
		}
	}
	dst := filepath.Join(cur, segs[len(segs)-1])
	if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
		// Do not write through the link; replace the link itself with a real file.
		if rerr := os.Remove(dst); rerr != nil {
			return "", rerr
		}
	}
	return dst, nil
}

// memorySameContent reports whether the destination already holds the same content (false when
// it does not exist).
func memorySameContent(src, dst string) (bool, error) {
	a, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	b, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !b.Mode().IsRegular() || a.Size() != b.Size() {
		return false, nil
	}
	ab, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	bb, err := os.ReadFile(dst)
	if err != nil {
		return false, nil
	}
	return string(ab) == string(bb), nil
}

// memoryPruneEmptyDirs folds up directories left empty by a deletion, stopping short of
// rootDir. os.Remove only succeeds on an empty directory, so a branch where something else (a
// transcript, say) remains is left alone.
func memoryPruneEmptyDirs(rootDir, removed string) {
	dir := filepath.Dir(removed)
	for strings.HasPrefix(dir, rootDir+string(os.PathSeparator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// memoryTreeKind is the summary of one kind, as returned by the tree API.
type memoryTreeKind struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Scopes bool   `json:"scopes"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// memoryTreeProject is one project's share, as returned by the tree API.
type memoryTreeProject struct {
	Slug    string `json:"slug"`
	Display string `json:"display"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// memoryTreeAt returns what was there at that point in time. The restore picker reads this: a
// project that is gone today is still present in the snapshot of the time, so using the current
// roots as the choices would defeat the main use case, restoring memory deleted by mistake.
func memoryTreeAt(rev, at string) (string, []memoryTreeKind, []memoryTreeProject, error) {
	if !memoryHasCommits() {
		return "", nil, nil, memoryErrf(http.StatusNotFound, errCodeMemoryNoSnapshots, "no snapshots yet")
	}
	sha, err := memoryResolveRev(rev, at)
	if err != nil {
		return "", nil, nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "%s", err.Error())
	}
	kinds, projects, err := memoryTreeOfRev(sha)
	return sha, kinds, projects, err
}

// memoryTreeOfRev folds a resolved commit's tree by kind and by project. import's preview needs
// the same shape ("what is in the imported lineage"), so it lives here.
func memoryTreeOfRev(sha string) ([]memoryTreeKind, []memoryTreeProject, error) {
	// --long returns "<mode> blob <sha> <size>\t<path>" (size gives the capacity at the time).
	out, err := memoryGitRun("ls-tree", "-r", "--long", sha)
	if err != nil {
		return nil, nil, err
	}
	decls := map[string]memoryRoot{}
	for _, r := range memoryRootDecls() {
		decls[r.Kind] = r
	}
	kinds := []memoryTreeKind{}
	projects := []memoryTreeProject{}
	kindIdx, projIdx := map[string]int{}, map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var size int64
		if fields := strings.Fields(meta); len(fields) >= 4 {
			for _, r := range fields[3] {
				if r < '0' || r > '9' {
					size = 0
					break
				}
				size = size*10 + int64(r-'0')
			}
		}
		kind, _, _ := strings.Cut(p, "/")
		i, seen := kindIdx[kind]
		if !seen {
			d := decls[kind]
			label := d.Label
			if label == "" {
				label = kind
			}
			kinds = append(kinds, memoryTreeKind{Kind: kind, Label: label, Scopes: d.Scopes})
			i = len(kinds) - 1
			kindIdx[kind] = i
		}
		kinds[i].Files++
		kinds[i].Bytes += size
		if slug, ok := memoryScopeSlug(p); ok {
			j, seen := projIdx[slug]
			if !seen {
				projects = append(projects, memoryTreeProject{Slug: slug, Display: memorySlugDisplay(slug)})
				j = len(projects) - 1
				projIdx[slug] = j
			}
			projects[j].Files++
			projects[j].Bytes += size
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Display < projects[j].Display })
	return kinds, projects, nil
}
