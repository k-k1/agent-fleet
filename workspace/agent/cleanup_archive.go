package main

// Cleanup archive — a gz safety net for destructive tidy-up (docs/log/32). When a session
// or branch is deleted to reclaim space, what would truly be LOST is bundled first into
// a compressed archive under ~/.local/share/agent-fleet/cleanup/ (persists across
// container recreate), so the removal is recoverable:
//   - session: its meta + transcript jsonl(s) — the conversation, otherwise gone.
//   - branch:  its name + tip SHA — a merged branch's commits already live in the
//              target's object store, so recording the ref is enough to recreate it.
// A worktree's working files are reconstructable from git (delete_worktree refuses
// dirty/ahead), so they are NOT archived — only the sessions/branch tied to it are.
//
// Each archive is a self-contained <id>.tar.gz (manifest.json + jsonl files inside),
// with a sidecar <id>.json manifest for cheap listing without extracting.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func cleanupStoreDir() string { return filepath.Join(paths.AgentDataDir(), "cleanup") }

// cleanupArchivedSession is one session captured in an archive: enough to restore the
// listed row (meta) and the conversation (jsonl payloads, stored as tar entries).
type cleanupArchivedSession struct {
	Name       string   `json:"name"`
	Display    string   `json:"display"`
	Kind       string   `json:"kind"`
	Meta       string   `json:"meta"`                 // marshaled session.Meta, replayed on restore
	JSONLPaths []string `json:"jsonlPaths,omitempty"` // original absolute paths (restore targets)
	JSONLNames []string `json:"jsonlNames,omitempty"` // tar entry names, index-aligned with JSONLPaths
}

// cleanupArchivedBranch is a deleted branch's coordinates. The commits survive in the
// repo's object store (merged branches only), so name+SHA recreate the ref.
type cleanupArchivedBranch struct {
	Repo string `json:"repo"`
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type cleanupManifest struct {
	ID        string                   `json:"id"`
	At        string                   `json:"at"` // RFC3339
	Reason    string                   `json:"reason,omitempty"`
	Sessions  []cleanupArchivedSession `json:"sessions,omitempty"`
	Branches  []cleanupArchivedBranch  `json:"branches,omitempty"`
	Worktrees []string                 `json:"worktrees,omitempty"` // names removed (informational)
}

// newCleanupID builds a sortable, unique archive id. now is passed in (never
// time.Now() inside) so tests are deterministic.
func newCleanupID(now time.Time, slug string) string {
	return now.UTC().Format("20060102-150405") + "-" + slug
}

// writeCleanupArchive persists the manifest + jsonl payloads as <id>.tar.gz plus a
// sidecar <id>.json. payloads maps a tar entry name → bytes.
func writeCleanupArchive(m cleanupManifest, payloads map[string][]byte) error {
	dir := cleanupStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := tarAdd(tw, "manifest.json", mj); err != nil {
		return err
	}
	// Deterministic entry order (tests + reproducibility).
	names := make([]string, 0, len(payloads))
	for n := range payloads {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tarAdd(tw, n, payloads[n]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, m.ID+".tar.gz"), buf.Bytes(), 0o600); err != nil {
		return err
	}
	// Sidecar manifest for listing; best-effort (the tar.gz is the source of truth).
	_ = os.WriteFile(filepath.Join(dir, m.ID+".json"), mj, 0o600)
	return nil
}

func tarAdd(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b))}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// listCleanupArchives returns stored manifests, newest first (id is time-sortable).
func listCleanupArchives() []cleanupManifest {
	entries, _ := os.ReadDir(cleanupStoreDir())
	var out []cleanupManifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(cleanupStoreDir(), e.Name()))
		if err != nil {
			continue
		}
		var m cleanupManifest
		if json.Unmarshal(b, &m) == nil && m.ID != "" {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// readCleanupArchive loads a manifest and the tar payloads for restore.
func readCleanupArchive(id string) (cleanupManifest, map[string][]byte, error) {
	var m cleanupManifest
	if filepath.Base(id) != id || strings.Contains(id, "..") {
		return m, nil, fmt.Errorf("invalid archive id")
	}
	b, err := os.ReadFile(filepath.Join(cleanupStoreDir(), id+".tar.gz"))
	if err != nil {
		return m, nil, err
	}
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return m, nil, err
	}
	defer gr.Close()
	payloads := map[string][]byte{}
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		// Cap the per-entry allocation: h.Size comes straight from the (possibly
		// corrupt) tar header, and a huge value would OOM this memory-constrained host.
		const maxCleanupEntryBytes = 64 << 20
		if h.Size < 0 || h.Size > maxCleanupEntryBytes {
			return m, nil, fmt.Errorf("archive %s: entry %s too large (%d bytes)", id, h.Name, h.Size)
		}
		data := make([]byte, h.Size)
		_, _ = io.ReadFull(tr, data)
		if h.Name == "manifest.json" {
			_ = json.Unmarshal(data, &m)
		} else {
			payloads[h.Name] = data
		}
	}
	if m.ID == "" {
		return m, nil, fmt.Errorf("archive %s has no manifest", id)
	}
	return m, payloads, nil
}

// purgeCleanupArchive permanently removes an archive (reclaims its space for good).
func purgeCleanupArchive(id string) error {
	if filepath.Base(id) != id || strings.Contains(id, "..") {
		return fmt.Errorf("invalid archive id")
	}
	_ = os.Remove(filepath.Join(cleanupStoreDir(), id+".json"))
	return os.Remove(filepath.Join(cleanupStoreDir(), id+".tar.gz"))
}

// restoreCleanupArchive replays an archive: re-create each branch ref (name→sha, if
// absent) and each session (meta + jsonl written back). Returns per-item outcomes.
func restoreCleanupArchive(id string) (map[string]any, error) {
	m, payloads, err := readCleanupArchive(id)
	if err != nil {
		return nil, err
	}
	restored := map[string]any{"sessions": []string{}, "branches": []string{}}
	var sessions, branches []string
	for _, s := range m.Sessions {
		var meta session.Meta
		if json.Unmarshal([]byte(s.Meta), &meta) != nil || meta.Name == "" {
			continue
		}
		for i, name := range s.JSONLNames {
			if i >= len(s.JSONLPaths) {
				break
			}
			data, ok := payloads[name]
			if !ok {
				continue
			}
			_ = os.MkdirAll(filepath.Dir(s.JSONLPaths[i]), 0o700)
			_ = os.WriteFile(s.JSONLPaths[i], data, 0o600)
		}
		session.WriteMeta(meta)
		sessions = append(sessions, s.Name)
	}
	for _, b := range m.Branches {
		dir, ok := resolveRepoDir(b.Repo)
		if !ok || b.SHA == "" {
			continue
		}
		// Only create if absent; ignore errors (e.g. SHA gone after a GC).
		if !gitBranchExists(dir, b.Name) {
			if gitCreateBranch(dir, b.Name, b.SHA) {
				branches = append(branches, b.Name)
			}
		}
	}
	restored["sessions"] = sessions
	restored["branches"] = branches
	return restored, nil
}
