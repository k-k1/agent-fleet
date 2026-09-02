package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// gitGC periodically maintains the internal bare repositories (docs/reference/
// internal-git-provider, P2/P3):
//
//   - `git gc --auto` repacks loose objects that pushes leave behind.
//   - LFS orphan prune deletes large objects no reachable pointer references
//     anymore (force-push, history rewrite, deleted pointer), freeing disk and
//     the tenant's capacity quota.
//
// It runs SEQUENTIALLY with --auto so the memory footprint stays tiny on the
// shared host (see host-oom-fleet-risk). Off when the interval is 0
// (AF_GIT_GC_INTERVAL=0).
// gitGCStore is gitGC's narrow store view (docs/log/23 P2-W3): tenant slug→id
// lookup + the LFS object ledger it reconciles. Standalone components should
// depend on the sub-interfaces they use, not the full Store.
type gitGCStore interface {
	TenantStore
	LFSObjectStore
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
	root := filepath.Join(g.dataRoot, "git")
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

// pruneLFS deletes LFS objects under a repo that no reachable git pointer
// references, subject to the grace window. Conservative: on any enumeration error
// it deletes nothing. No-op (and no git cost) for repos with no LFS objects.
func (g *gitGC) pruneLFS(ctx context.Context, slug, repo, bareDir string) {
	objRoot := filepath.Join(bareDir, "lfs", "objects")
	if fi, err := os.Stat(objRoot); err != nil || !fi.IsDir() {
		return // repo has no LFS objects
	}
	referenced, err := referencedLFSOIDs(ctx, bareDir)
	if err != nil {
		log.Printf("lfs-gc: %s: enumerate refs failed, skipping prune: %v", bareDir, err)
		return
	}
	tenant, ok, err := g.store.GetTenantBySlug(ctx, slug)
	if err != nil || !ok {
		return // can't resolve the tenant → don't touch the ledger/objects
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
		if err := os.Remove(path); err != nil {
			return nil
		}
		_ = g.store.DeleteLFSObject(ctx, tenant.ID, repo, oid)
		freed++
		bytes += info.Size()
		return nil
	})
	if freed > 0 {
		log.Printf("lfs-gc: %s/%s: pruned %d orphan object(s), %d bytes", slug, repo, freed, bytes)
	}
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
	// Pass 1: headers of all objects; keep small blobs (pointer candidates).
	check := exec.CommandContext(ctx, "git", "--git-dir", bareDir,
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
	referenced := map[string]bool{}
	if len(candidates) == 0 {
		return referenced, nil
	}

	// Pass 2: stream the candidate blobs' contents and extract pointer oids.
	batch := exec.CommandContext(ctx, "git", "--git-dir", bareDir, "cat-file", "--batch")
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
	// 途中の break でも子を確実に回収する: stdout/stdin を閉じて cat-file の書き込みを
	// EPIPE で解かないと、パイプ詰まりで子が生き続け Wait() が永久ブロック→以後の
	// GC が全停止する。
	defer func() {
		stdin.Close()
		stdout.Close()
		_ = batch.Wait()
	}()
	go func() {
		defer stdin.Close()
		w := bufio.NewWriter(stdin)
		for _, sha := range candidates {
			w.WriteString(sha)
			w.WriteByte('\n')
		}
		w.Flush()
	}()

	r := bufio.NewReader(stdout)
	for {
		header, err := r.ReadString('\n')
		if err != nil {
			break
		}
		f := strings.Fields(strings.TrimRight(header, "\n"))
		if len(f) != 3 || f[1] != "blob" { // "missing" or unexpected — nothing to read
			continue
		}
		size, err := strconv.Atoi(f[2])
		if err != nil {
			break
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(r, content); err != nil {
			break
		}
		r.ReadByte() // trailing LF after content
		if m := lfsPointerOID.FindSubmatch(content); m != nil {
			referenced[string(m[1])] = true
		}
	}
	return referenced, nil
}
