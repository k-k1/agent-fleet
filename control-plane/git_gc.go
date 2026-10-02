package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// gitGC periodically maintains the internal bare repositories (docs/reference/
// internal-git-provider, P2/P3):
//
//   - `git gc --auto` repacks loose objects that pushes leave behind.
//   - LFS orphan prune deletes large objects no reachable pointer references
//     anymore (force-push, history rewrite, deleted pointer), freeing disk and
//     the tenant's capacity quota.
//   - LFS ledger reconcile deletes ledger rows whose object file never landed
//     (a failed publish, or a crash between the ledger write and the rename), which
//     would otherwise over-count the quota until the same oid is uploaded again.
//
// It runs SEQUENTIALLY with --auto so the memory footprint stays tiny on the
// shared host (see host-oom-fleet-risk). Off when the interval is 0
// (AF_GIT_GC_INTERVAL=0).
// gitGCStore is gitGC's narrow store view (docs/log/23 P2-W3): tenant slug→id
// lookup + the LFS object ledger it reconciles. Standalone components should
// depend on the sub-interfaces they use, not the full Store.
type gitGCStore interface {
	store.TenantStore
	store.LFSObjectStore
}

type gitGC struct {
	store    gitGCStore
	dataRoot string
	interval time.Duration
	// lfsGrace protects an object whose file mtime is younger than this from
	// pruning, so GC never races an in-flight push (git-lfs uploads the object
	// BEFORE it pushes the ref that references it). 0 disables the grace (tests).
	lfsGrace time.Duration
}

func newGitGC(st gitGCStore, dataRoot string, interval, lfsGrace time.Duration) *gitGC {
	return &gitGC{store: st, dataRoot: dataRoot, interval: interval, lfsGrace: lfsGrace}
}

func (g *gitGC) run(ctx context.Context) {
	// A first sweep shortly after boot, then on the interval.
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.sweep(ctx)
			t.Reset(g.interval)
		}
	}
}

// sweep runs maintenance on every bare under the git tree. Errors are logged and
// skipped so one bad repo never stalls the rest.
func (g *gitGC) sweep(ctx context.Context) {
	root := filepath.Join(g.dataRoot, datalayout.GitDir)
	tenants, err := os.ReadDir(root)
	if err != nil {
		return // no git tree yet (nothing created) — nothing to do
	}
	swept, failed := 0, 0
	for _, td := range tenants {
		if !td.IsDir() {
			continue
		}
		slug := td.Name()
		repos, err := os.ReadDir(filepath.Join(root, slug))
		if err != nil {
			continue
		}
		for _, rd := range repos {
			if !rd.IsDir() || filepath.Ext(rd.Name()) != ".git" {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			dir := filepath.Join(root, slug, rd.Name())
			cmd := exec.CommandContext(ctx, "git", "--git-dir", dir, "gc", "--auto", "--quiet")
			if out, err := cmd.CombinedOutput(); err != nil {
				failed++
				log.Printf("git-gc: %s: %v: %s", dir, err, out)
				continue
			}
			swept++
			repoName := strings.TrimSuffix(rd.Name(), ".git")
			g.pruneLFS(ctx, slug, repoName, dir)
		}
	}
	if swept > 0 || failed > 0 {
		log.Printf("git-gc: swept %d bare repo(s), %d failed", swept, failed)
	}
}

// pruneLFS reconciles a repo's LFS ledger with its object files, then deletes LFS
// objects that no reachable git pointer references, both subject to the grace window.
// Conservative: on any enumeration or store error it deletes nothing further. No git
// cost for repos with no LFS objects.
func (g *gitGC) pruneLFS(ctx context.Context, slug, repo, bareDir string) {
	tenant, ok, err := g.store.GetTenantBySlug(ctx, slug)
	if err != nil {
		log.Printf("lfs-gc: %s/%s: tenant lookup failed, skipping the pass: %v", slug, repo, err)
		return
	}
	if !ok {
		return // can't resolve the tenant → don't touch the ledger/objects
	}
	if err := g.reconcileLFSLedger(ctx, tenant.ID, slug, repo, bareDir); err != nil {
		log.Printf("lfs-gc: %s/%s: ledger reconcile failed, skipping the pass: %v", slug, repo, err)
		return
	}
	objRoot := filepath.Join(bareDir, "lfs", "objects")
	if fi, err := os.Stat(objRoot); err != nil || !fi.IsDir() {
		return // repo has no LFS objects
	}
	referenced, err := referencedLFSOIDs(ctx, bareDir)
	if err != nil {
		log.Printf("lfs-gc: %s: enumerate refs failed, skipping prune: %v", bareDir, err)
		return
	}

	cutoff := time.Now().Add(-g.lfsGrace)
	var freed, bytes int64
	_ = filepath.WalkDir(objRoot, func(path string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		oid := d.Name()
		if !validOID(oid) || referenced[oid] {
			return nil // not an object file, or still referenced → keep
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if g.lfsGrace > 0 && info.ModTime().After(cutoff) {
			return nil // too young — might be an in-flight push
		}
		// Ledger row first: if it cannot go, the file stays and the next sweep retries,
		// rather than a removed file leaving a row that over-counts the quota forever.
		if err := g.store.DeleteLFSObject(ctx, tenant.ID, repo, oid); err != nil {
			log.Printf("lfs-gc: %s/%s: ledger delete %s failed, keeping the object: %v", slug, repo, oid, err)
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Already gone (a repo delete got there first) counts as removed. Otherwise
			// the file stays, so its row has to come back: an upload of an existing
			// object is a no-op and would never write it again.
			if perr := g.store.PutLFSObject(ctx, tenant.ID, repo, oid, info.Size()); perr != nil {
				log.Printf("lfs-gc: %s/%s: remove %s failed (%v) and restoring its ledger row failed: %v", slug, repo, oid, err, perr)
			} else {
				log.Printf("lfs-gc: %s/%s: remove %s failed, object kept: %v", slug, repo, oid, err)
			}
			return nil
		}
		freed++
		bytes += info.Size()
		return nil
	})
	if freed > 0 {
		log.Printf("lfs-gc: %s/%s: pruned %d orphan object(s), %d bytes", slug, repo, freed, bytes)
	}
}

// reconcileLFSLedger deletes ledger rows older than the grace window whose object file
// is absent. Such a row is left by an upload whose publish failed or whose process died
// between the ledger write and the rename; the upload path keeps it on purpose (a
// concurrent upload of the same oid may rely on it) and only a retry of the same oid would
// ever correct it. The grace window is what keeps an upload in flight from losing its
// row: PutLFSObject restarts a row's age, and the delete re-checks the age atomically.
//
// A zero grace skips it: a row written this second, whose upload is between the ledger
// write and the rename, would count as stale.
//
// Any store error is returned at once, so the caller skips the rest of the pass.
func (g *gitGC) reconcileLFSLedger(ctx context.Context, tenantID, slug, repo, bareDir string) error {
	if g.lfsGrace <= 0 {
		return nil
	}
	rows, err := g.store.ListLFSObjects(ctx, tenantID, repo)
	if err != nil {
		return fmt.Errorf("list ledger: %w", err)
	}
	cutoff := time.Now().UTC().Add(-g.lfsGrace).Format(time.RFC3339)
	for _, o := range rows {
		if !validOID(o.OID) || o.CreatedAt > cutoff {
			continue
		}
		// Only a file that is certainly absent drops its row; any other stat error keeps it.
		p := filepath.Join(bareDir, "lfs", "objects", o.OID[0:2], o.OID[2:4], o.OID)
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		// Checked after the object, not before: a rename moves the bare before it moves the
		// rows, so an object missing because the whole repo moved mid-sweep shows up here
		// as a missing bare, and the rows are left for RenameGitRepo to carry.
		if _, err := os.Stat(bareDir); err != nil {
			return nil
		}
		gone, err := g.store.DeleteStaleLFSObject(ctx, tenantID, repo, o.OID, cutoff)
		if err != nil {
			return fmt.Errorf("delete ledger row %s: %w", o.OID, err)
		}
		if gone {
			log.Printf("lfs-gc: %s/%s: removed ledger row %s (%d bytes, recorded %s): its object file is absent",
				slug, repo, o.OID, o.Size, o.CreatedAt)
		}
	}
	return nil
}

var lfsPointerOID = regexp.MustCompile(`(?m)^oid sha256:([0-9a-f]{64})$`)

// pointerMaxBytes bounds which reachable blobs GC reads looking for a pointer. A
// git-lfs pointer file is ~130 bytes; 1 KiB covers pointers carrying extension
// keys while keeping GC from streaming real (large) blobs.
const pointerMaxBytes = 1024

// referencedLFSOIDs returns the set of LFS oids referenced by pointer blobs in the
// repo. It scans EVERY object (reachable or not) via cat-file so an object kept
// alive only by an as-yet-unpruned dangling commit is treated as referenced —
// conservative on purpose (git gc prunes those later; the next sweep reclaims the
// object once the pointer is truly gone).
func referencedLFSOIDs(ctx context.Context, bareDir string) (map[string]bool, error) {
	// Both passes run with --no-replace-objects: a pushed refs/replace/<pointer> would
	// otherwise make pass 2 read the replacement's content, so the pointer's oid drops out
	// of the set with no error and its object is deleted.
	//
	// Pass 1: headers of all objects; keep small blobs (pointer candidates).
	check := exec.CommandContext(ctx, "git", "--no-replace-objects", "--git-dir", bareDir,
		"cat-file", "--batch-check", "--batch-all-objects", "--unordered")
	out, err := check.Output()
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			continue
		}
		if sz, err := strconv.Atoi(f[2]); err == nil && sz > 0 && sz <= pointerMaxBytes {
			candidates = append(candidates, f[0])
		}
	}
	if len(candidates) == 0 {
		return map[string]bool{}, nil
	}

	// Pass 2: stream the candidate blobs' contents and extract pointer oids.
	batch := lfsCatFileBatch(ctx, bareDir)
	stdin, err := batch.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := batch.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := batch.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer stdin.Close()
		w := bufio.NewWriter(stdin)
		for _, sha := range candidates {
			w.WriteString(sha)
			w.WriteByte('\n')
		}
		w.Flush()
	}()

	referenced, readErr := readPointerBatch(stdout, candidates)
	// Close both ends before Wait even when the read stopped early: unless that releases
	// cat-file's write with EPIPE, a full pipe keeps the child alive, Wait() blocks
	// forever and every later GC stops with it.
	stdin.Close()
	stdout.Close()
	waitErr := batch.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("cat-file --batch: %w", waitErr)
	}
	return referenced, nil
}

// lfsCatFileBatch builds pass 2's reader. A variable so a test can substitute a stream
// that dies part-way.
var lfsCatFileBatch = func(ctx context.Context, bareDir string) *exec.Cmd {
	return exec.CommandContext(ctx, "git", "--no-replace-objects", "--git-dir", bareDir, "cat-file", "--batch")
}

// readPointerBatch parses `git cat-file --batch` output answering candidates, asked in
// that order, and returns the pointer oids the blobs carry. Any response it cannot
// account for is an error, never a shorter set: pruneLFS reads an oid absent from the
// set as unreferenced and deletes the object.
func readPointerBatch(r io.Reader, candidates []string) (map[string]bool, error) {
	br := bufio.NewReader(r)
	referenced := map[string]bool{}
	for i, want := range candidates {
		header, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("cat-file --batch: response %d of %d: %w", i+1, len(candidates), err)
		}
		// "<sha> missing" fails here too: pass 1 saw the blob, so it vanished mid-sweep
		// and this sweep's picture is stale. The next sweep retries.
		f := strings.Fields(header)
		if len(f) != 3 || f[0] != want || f[1] != "blob" {
			return nil, fmt.Errorf("cat-file --batch: unexpected header %q for %s", strings.TrimSpace(header), want)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size <= 0 || size > pointerMaxBytes {
			return nil, fmt.Errorf("cat-file --batch: bad size in header %q", strings.TrimSpace(header))
		}
		content := make([]byte, size+1) // the blob plus its trailing LF
		if _, err := io.ReadFull(br, content); err != nil {
			return nil, fmt.Errorf("cat-file --batch: content of %s: %w", want, err)
		}
		if content[size] != '\n' {
			return nil, fmt.Errorf("cat-file --batch: content of %s not followed by LF", want)
		}
		if m := lfsPointerOID.FindSubmatch(content[:size]); m != nil {
			referenced[string(m[1])] = true
		}
	}
	return referenced, nil
}
