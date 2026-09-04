package memoryx

// Version control for agent memory (docs/log/39 ⑤ / ADR 0022 decision 5) — import.
//
//	receive (multipart) ──▶ verify ──▶ take in as refs/imports/<id>/* ──▶ preview
//	                                                                       │
//	                  pick project/kind, then "replace = a new commit" ◀────┘
//
// Two ideas at the core of the design:
//
//   - Accept it as an independent lineage. A bundle is fetched into `refs/imports/<id>/*`
//     and never grafted onto the local main. tar.gz is brought into the same shape (extract
//     -> write-tree against a dedicated index -> commit-tree -> the same ref space), so
//     preview and apply each have a single path. What was not applied also stays in local
//     history, so importing is never something to regret.
//
//   - Applying is a selective replace, not a 3-way merge. A semantic conflict between .md
//     files cannot be resolved mechanically (ADR 0022 decision 5). Only the selected scope
//     is written to live through the same path as restore, and the result is committed with
//     AF-Trigger: import — so an import can be rolled back too.
//
// ★3 (an import is external input): a size cap, tar traversal defence, rejection of entries
// outside the allowlist, and a mandatory bundle verify. On top of that the step that writes
// to live goes through the same memoryApplyScopeToLive as restore, so whatever the repo
// contains, not one byte is written outside the allowlist.

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	memoryImportDefaultMax = 64 << 20 // default cap on the received size (docs/log/39 ★3)
	memoryImportMaxEntries = 20000    // cap on the number of tar entries
	memoryImportMaxFile    = 8 << 20  // cap on a single file in the tar
	memoryImportKeepRefs   = 10       // how many imported lineages are kept

	// How to apply (REST's mode). replace = take only the content of the selected scope
	// (the default; the history stays your own). migrate = swap the history along with it
	// (a migration; the scope is fixed at everything).
	memoryImportModeReplace = "replace"
	memoryImportModeMigrate = "migrate"
)

// memoryImportIDRe is the shape of an importId (the same as on the generating side). apply
// uses a value that came from the URL or the body as a ref name, so only what passes here is
// handed to git.
var memoryImportIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]+)?$`)

func memoryImportMaxBytes() int64 {
	if v := strings.TrimSpace(os.Getenv("AF_MEMORY_IMPORT_MAX")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return memoryImportDefaultMax
}

// memoryImportPreview summarises the lineage that was taken in. The Console shows it so the
// user can pick the scope to apply.
type memoryImportPreview struct {
	ImportID  string              `json:"importId"`
	Format    string              `json:"format"` // bundle | tar
	Ref       string              `json:"ref"`    // refs/imports/<id>/<name>
	Head      string              `json:"head"`   // head commit that was taken in
	HeadTs    string              `json:"headTs,omitempty"`
	Snapshots int                 `json:"snapshots"` // commits contained in the lineage
	Kinds     []memoryTreeKind    `json:"kinds"`
	Projects  []memoryTreeProject `json:"projects"`
	// Unavailable are kinds this environment does not have (e.g. codex memories not
	// enabled). They are not offered for selection.
	Unavailable []string `json:"unavailable"`
	// Rejected are paths outside the allowlist, so they were not taken in / will not be
	// applied.
	Rejected []string `json:"rejected"`
	// Secrets is the secret scan over the imported content, for information only: an import
	// brings in the user's own data, so it is not blocked. Raw values are not included.
	Secrets []memorySecretFinding `json:"secrets"`
	// SecretScanFailed marks a failure of the scan itself, kept distinct from "nothing
	// found". Since an import is not blocked, this is not an error, just the fact.
	SecretScanFailed bool `json:"secretScanFailed,omitempty"`
}

// memoryImportPrepare verifies what was received, takes it in under refs/imports/<id>/* and
// returns a preview. src is the stored temporary file; name is the original file name, used
// as a hint when guessing the format.
func memoryImportPrepare(src, name string, now time.Time) (memoryImportPreview, error) {
	var pv memoryImportPreview
	if err := memoryEnsureRepo(); err != nil {
		return pv, err
	}
	format, err := memoryDetectFormat(src, name)
	if err != nil {
		return pv, err
	}
	id, err := memoryNewImportID(now)
	if err != nil {
		return pv, err
	}
	pv.ImportID, pv.Format = id, format

	switch format {
	case memoryFormatBundle:
		pv.Ref, err = memoryImportBundle(src, id)
	default:
		pv.Ref, pv.Rejected, err = memoryImportTar(src, id, now)
	}
	if err != nil {
		return pv, err
	}

	head, err := memoryGitRun("rev-parse", "--verify", "--quiet", pv.Ref+"^{commit}")
	if err != nil || head == "" {
		return pv, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "the uploaded file contains no memory history")
	}
	pv.Head = head
	if ts, terr := memoryGitRun("log", "-1", "--pretty=format:%aI", head); terr == nil {
		pv.HeadTs = ts
	}
	if c, cerr := memoryGitRun("rev-list", "--count", head); cerr == nil {
		pv.Snapshots, _ = strconv.Atoi(c)
	}
	kinds, projects, terr := memoryTreeOfRev(head)
	if terr != nil {
		return pv, terr
	}
	pv.Kinds, pv.Projects = kinds, projects

	// A kind with no root to land in on this environment is not offered (e.g. an environment
	// where codex memories are not enabled).
	active := map[string]bool{}
	for _, r := range memoryRoots() {
		active[r.Kind] = true
	}
	pv.Unavailable = []string{}
	for _, k := range kinds {
		if !active[k.Kind] {
			pv.Unavailable = append(pv.Unavailable, k.Kind)
		}
	}
	// A bundle's contents cannot be filtered, so paths outside the allowlist are listed here
	// and shown. memoryApplyScopeToLive rejects them at apply time as well, but knowing in
	// advance is kinder.
	if format == memoryFormatBundle {
		pv.Rejected = memoryRejectedPaths(head)
	}
	if pv.Rejected == nil {
		pv.Rejected = []string{}
	}
	if pv.Secrets, err = memoryScanRevTree(head); err != nil {
		pv.SecretScanFailed = true // do not let a failure look like "nothing found"
	}
	if pv.Secrets == nil {
		pv.Secrets = []memorySecretFinding{}
	}
	memoryPruneImportRefs(memoryImportKeepRefs)
	_, _ = memoryGitRun("gc", "--auto", "--quiet") // ★8
	return pv, nil
}

// memoryDetectFormat decides the format from the magic bytes; the extension is not trusted.
func memoryDetectFormat(src, name string) (string, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case strings.HasPrefix(string(head), "# v2 git bundle"), strings.HasPrefix(string(head), "# v3 git bundle"):
		return memoryFormatBundle, nil
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return memoryFormatTar, nil
	}
	return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport,
		"unsupported file %q: expected a git bundle or a .tar.gz produced by export", filepath.Base(name))
}

// memoryNewImportID builds the id in refs/imports/<id>. A collision within the same second
// is avoided with a sequence number.
func memoryNewImportID(now time.Time) (string, error) {
	base := now.UTC().Format("20060102T150405Z")
	for i := 0; i < 100; i++ {
		id := base
		if i > 0 {
			id = base + "-" + strconv.Itoa(i+1)
		}
		out, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/"+id+"/")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			return id, nil
		}
	}
	return "", errors.New("could not allocate an import id")
}

// memoryImportBundle verifies the bundle and fetches it into refs/imports/<id>/*. verify is
// mandatory because of ★3 (external input) — a corrupt or truncated bundle dies here.
func memoryImportBundle(src, id string) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	if _, err := memoryGitRun("bundle", "verify", abs); err != nil {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "git bundle verify failed: %v", err)
	}
	if _, err := memoryGitRun("fetch", "--no-write-fetch-head", abs,
		"+refs/heads/*:refs/imports/"+id+"/*"); err != nil {
		return "", fmt.Errorf("fetch bundle: %w", err)
	}
	refs, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/"+id+"/")
	if err != nil {
		return "", err
	}
	list := []string{}
	for _, r := range strings.Split(refs, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			list = append(list, r)
		}
	}
	if len(list) == 0 {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "the bundle carries no branches")
	}
	// Prefer main (the only branch snapshots are stacked on); otherwise take the first.
	want := "refs/imports/" + id + "/" + memoryBranch
	for _, r := range list {
		if r == want {
			return r, nil
		}
	}
	sort.Strings(list)
	return list[0], nil
}

// memoryImportTar extracts the tar.gz while verifying it and commits into the same ref space
// as a bundle. It extracts into the work dir, touching neither live nor staging.
func memoryImportTar(src, id string, now time.Time) (ref string, rejected []string, err error) {
	dir := filepath.Join(memoryWorkDir(), "import-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir) // the extracted tree is done with once it is committed
	rejected, err = memoryExtractTar(src, dir)
	if err != nil {
		return "", rejected, err
	}
	// add/write-tree run against a dedicated index. GIT_INDEX_FILE and GIT_WORK_TREE are
	// swapped for this operation alone so that staging and the bare repo's index stay clean.
	idx := filepath.Join(memoryWorkDir(), "import-"+id+".index")
	defer os.Remove(idx)
	run := func(args ...string) (string, error) {
		cmd := memoryGit(args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+idx, "GIT_WORK_TREE="+dir)
		out, rerr := cmd.Output()
		s := strings.TrimSpace(string(out))
		if rerr != nil {
			var ee *exec.ExitError
			if errors.As(rerr, &ee) && len(ee.Stderr) > 0 {
				rerr = fmt.Errorf("%v: %s", rerr, strings.TrimSpace(string(ee.Stderr)))
			}
		}
		return s, rerr
	}
	if _, err := run("add", "-A", "."); err != nil {
		return "", rejected, fmt.Errorf("stage imported tree: %w", err)
	}
	tree, err := run("write-tree")
	if err != nil || tree == "" {
		return "", rejected, fmt.Errorf("write imported tree: %w", err)
	}
	msg := "import: " + now.UTC().Format(time.RFC3339) + " (tar)\n\nAF-Trigger: " + memoryTriggerImport + "\n"
	commit, err := run("commit-tree", tree, "-m", msg)
	if err != nil || commit == "" {
		return "", rejected, fmt.Errorf("commit imported tree: %w", err)
	}
	ref = "refs/imports/" + id + "/" + memoryBranch
	if _, err := memoryGitRun("update-ref", ref, commit); err != nil {
		return "", rejected, err
	}
	return ref, rejected, nil
}

// memoryExtractTar extracts the tar.gz into dst. An entry that does not match the allowlist,
// traverses out, or is not a regular file is dropped into rejected without being written
// (the same shape as the guard in cleanup_archive.go).
func memoryExtractTar(src, dst string) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "not a gzip archive: %v", err)
	}
	defer gr.Close()

	decls := memoryRootDecls()
	rejected := []string{}
	var total int64
	tr := tar.NewReader(gr)
	for i := 0; ; i++ {
		if i > memoryImportMaxEntries {
			return rejected, memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "archive has too many entries")
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rejected, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "corrupt archive: %v", err)
		}
		name := filepath.ToSlash(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag == tar.TypeDir {
			continue // created on demand as the parent of a file that has content
		}
		if h.Typeflag != tar.TypeReg {
			rejected = append(rejected, name) // symlink / hardlink / device are not accepted
			continue
		}
		if name == "manifest.json" {
			continue // self-description; not under version control, so not taken in
		}
		if !memoryImportPathAllowed(decls, name) {
			rejected = append(rejected, name)
			continue
		}
		if h.Size > memoryImportMaxFile {
			rejected = append(rejected, name)
			continue
		}
		total += h.Size
		if total > memoryImportMaxBytes() {
			return rejected, memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "archive contents exceed the import size limit")
		}
		out := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return rejected, err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return rejected, err
		}
		if _, err := io.CopyN(w, tr, h.Size); err != nil && err != io.EOF {
			_ = w.Close()
			return rejected, err
		}
		if err := w.Close(); err != nil {
			return rejected, err
		}
	}
	sort.Strings(rejected)
	return rejected, nil
}

// memoryImportPathAllowed answers whether a path inside the repo falls within the allowlist
// of the declared roots. It judges against memoryRootDecls, not the roots enabled on this
// environment: an environment without codex enabled can still take codex content in, and it
// is rejected only at the step that writes to live.
func memoryImportPathAllowed(decls []memoryRoot, repoPath string) bool {
	if repoPath == "" || strings.HasPrefix(repoPath, "/") || strings.Contains(repoPath, "..") ||
		strings.HasPrefix(repoPath, ".git/") || strings.Contains(repoPath, "/.git/") {
		return false
	}
	for _, r := range decls {
		if !strings.HasPrefix(repoPath, r.RepoPrefix+"/") {
			continue
		}
		return memoryAllowed(r, strings.TrimPrefix(repoPath, r.RepoPrefix+"/"))
	}
	return false
}

// memoryRejectedPaths lists the paths in rev's tree that fall outside the allowlist (used
// for bundles).
func memoryRejectedPaths(rev string) []string {
	out, err := memoryGitRun("ls-tree", "-r", "--name-only", rev)
	if err != nil {
		return []string{}
	}
	decls := memoryRootDecls()
	rejected := []string{}
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !memoryImportPathAllowed(decls, p) {
			rejected = append(rejected, p)
		}
	}
	return rejected
}

// memoryPruneImportRefs drops old imported lineages (★8, holding down repo growth). Content
// that was applied survives as the import commit on main, so deleting the ref loses nothing.
func memoryPruneImportRefs(keep int) {
	out, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/")
	if err != nil {
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	byID := map[string][]string{}
	for _, ref := range strings.Split(out, "\n") {
		ref = strings.TrimSpace(ref)
		rest, ok := strings.CutPrefix(ref, "refs/imports/")
		if !ok {
			continue
		}
		id, _, ok := strings.Cut(rest, "/")
		if !ok || id == "" {
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
		byID[id] = append(byID[id], ref)
	}
	if len(ids) <= keep {
		return
	}
	sort.Strings(ids) // an id is a UTC timestamp, so lexical order is chronological order
	for _, id := range ids[:len(ids)-keep] {
		for _, ref := range byID[id] {
			_, _ = memoryGitRun("update-ref", "-d", ref)
		}
	}
}

// memoryImportApply applies only the selected scope of an imported lineage to live. It is
// the same path as restore (take a pre-restore snapshot, write only inside the allowlist,
// commit the result); only the trigger is import — so an import can be rolled back too.
//
// opts.Adopt=true is a migration (docs/log/39 ⑤-migration): it carries over the history as
// well as the content. A bundle carries all of the other side's snapshots, yet the default
// apply uses only the newest tree and leaves the past it carried buried in refs/imports
// (pruned past 10). A migration repoints main at that lineage, so the other side's history
// becomes this environment's history and the existing listing, diff and rollback features
// all work on it unchanged.
func memoryImportApply(importID string, sc memoryRestoreScope, now time.Time, opts memoryApplyOpts) (memoryRestoreResult, error) {
	var res memoryRestoreResult
	if !memoryImportIDRe.MatchString(importID) {
		return res, memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "invalid importId")
	}
	ref, err := memoryImportRef(importID)
	if err != nil {
		return res, err
	}
	sha, err := memoryGitRun("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return res, memoryErrf(http.StatusNotFound, errCodeMemoryBadImport, "import %s is no longer available", importID)
	}
	trailers := []string{"AF-Import-Id: " + importID, "AF-Import-Ref: " + ref}
	if opts.Adopt {
		// A migration's scope is fixed at everything. Replacing only part of it leaves the
		// history (the other side's lineage) at odds with live (a mix of yours and theirs),
		// and then there is no way to explain what a later rollback means. To pick a scope,
		// use the default apply, which keeps your own history.
		sc = memoryRestoreScope{All: true}
		trailers = append(trailers, "AF-Import-Mode: migrate")
	}
	return memoryApplyRev(sc, sha, memoryTriggerImport, trailers, now, opts)
}

// memoryImportRef looks up the ref for an importId, preferring main.
func memoryImportRef(importID string) (string, error) {
	out, err := memoryGitRun("for-each-ref", "--format=%(refname)", "refs/imports/"+importID+"/")
	if err != nil {
		return "", err
	}
	list := []string{}
	for _, r := range strings.Split(out, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			list = append(list, r)
		}
	}
	if len(list) == 0 {
		return "", memoryErrf(http.StatusNotFound, errCodeMemoryBadImport, "import %s is no longer available", importID)
	}
	want := "refs/imports/" + importID + "/" + memoryBranch
	for _, r := range list {
		if r == want {
			return r, nil
		}
	}
	sort.Strings(list)
	return list[0], nil
}
