package mcpproj

// Inspect assembles one Snapshot (docs/56 P0's whole deliverable): read every v1
// target file inside dir, parse it, warn about it, and mask every secret before it
// leaves this package.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/projcfg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fileSpec is one v1 target file (docs/56 §4.3).
type fileSpec struct {
	path  string   // repo-relative
	kinds []string // agent kinds that actually READ this file
	// specKind selects which mcpreg.JSONEntrySpellings entry parses this file.
	// "" means codex's TOML shape (parseCodexServers) instead of a JSON spelling.
	specKind string
	// seed is written into a file P1 creates from scratch (opencode's "$schema"),
	// matching what that CLI's own writer would produce (docs/56 §6) — never
	// applied to a file that already exists. nil for kinds whose own `mcp add`
	// writes no header of its own.
	seed map[string]any
}

// fileSpecs is v1's target list (docs/56 §4.3). .mcp.json and .github/mcp.json are
// both parsed with claude's spelling: docs/56 §2.3 measured .mcp.json as one format
// shared by claude and copilot, and .github/mcp.json is assumed the same generic
// "mcpServers" shape pending the real measurement docs/56 §14 item 4 defers to P3.
var fileSpecs = []fileSpec{
	{path: ".mcp.json", kinds: []string{session.KindClaude, session.KindCopilot}, specKind: session.KindClaude},
	{path: "opencode.json", kinds: []string{session.KindOpencode}, specKind: session.KindOpencode,
		seed: map[string]any{"$schema": "https://opencode.ai/config.json"}},
	{path: ".codex/config.toml", kinds: []string{session.KindCodex}},
	{path: ".cursor/mcp.json", kinds: []string{session.KindCursor}, specKind: session.KindCursor},
	{path: ".github/mcp.json", kinds: []string{session.KindCopilot}, specKind: session.KindClaude},
	{path: ".kiro/settings/mcp.json", kinds: []string{session.KindKiro}, specKind: session.KindKiro},
}

// kindInfos is the static per-kind facts docs/56 §2.1 / §2.2 / §8 measured,
// independent of what any particular working copy's files contain — every kind
// appears here even with no file (docs/57 憲章「未検証バッジ」/「対象外」の行).
var kindInfos = []KindInfo{
	{Kind: session.KindClaude, HasProjectScope: true, GateCode: "approval", Dialects: []string{DialectDollarBrace}},
	{Kind: session.KindCodex, HasProjectScope: true, GateCode: "trust"},
	{Kind: session.KindOpencode, HasProjectScope: true, GateCode: "none", Dialects: []string{DialectEnvBrace}},
	{Kind: session.KindCursor, HasProjectScope: true, GateCode: "approval", Dialects: []string{DialectDollarBrace, DialectDollarEnvBrace}},
	{Kind: session.KindCopilot, HasProjectScope: true, GateCode: "none", Dialects: []string{DialectDollarBrace}},
	{Kind: session.KindKiro, HasProjectScope: true, Unverified: true},
	{Kind: session.KindAgy, HasProjectScope: false},
}

// Inspect gathers dir's project-scope MCP servers into a Snapshot. dir must already
// be a validated, existing working copy — repo NAME resolution/existence checks
// stay in package main (see the package doc). repoName is the display name only
// (Snapshot.Repo); dir is what is actually read.
func Inspect(dir, repoName string) (Snapshot, error) {
	vcs := projcfg.DetectVCS(dir)
	snap := Snapshot{
		Repo:     repoName,
		VCS:      vcs,
		Worktree: vcs == projcfg.VCSGit && projcfg.IsWorktree(dir),
		Kinds:    append([]KindInfo(nil), kindInfos...),
	}

	var locs []serverLocation
	for _, spec := range fileSpecs {
		f, raw, err := readProjectFile(dir, vcs, spec)
		if err != nil {
			return Snapshot{}, err
		}
		if f.Exists && !f.Parsable {
			snap.Warnings = append(snap.Warnings, Warning{Severity: "red", Code: CodeFileUnreadable, File: spec.path})
		}
		for _, name := range sortedNames(raw) {
			s := raw[name]
			snap.Warnings = append(snap.Warnings, nameWarnings(s.Name, spec.path)...)
			snap.Warnings = append(snap.Warnings, serverDialectWarnings(s, spec.path, spec.kinds)...)
			snap.Warnings = append(snap.Warnings, secretWarnings(s, spec.path, f.Tracked, f.TrackedUncertain)...)
			locs = append(locs, serverLocation{file: spec.path, s: s})
			f.Servers = append(f.Servers, maskServer(s))
		}
		snap.Files = append(snap.Files, f)
	}

	snap.Warnings = append(snap.Warnings, divergenceWarnings(locs)...)
	sortWarnings(snap.Warnings)
	return snap, nil
}

// readProjectFile reads and parses one target file. A missing file is not an error
// (File.Exists stays false); an unparsable one is reported via File.Note and never
// touched further (docs/57 憲章3) — only a real I/O error (permission, …) short-
// circuits the whole snapshot, since every OTHER file is independently readable.
func readProjectFile(dir, vcs string, spec fileSpec) (File, map[string]Server, error) {
	f := File{Path: spec.path, Kinds: spec.kinds}
	full := filepath.Join(dir, filepath.FromSlash(spec.path))
	b, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil, nil
		}
		return f, nil, fmt.Errorf("%s: %w", spec.path, err)
	}
	f.Exists = true
	st := projcfg.Track(dir, vcs, spec.path)
	f.Tracked, f.Ignored, f.TrackedUncertain = st.Tracked, st.Ignored, st.Uncertain

	var raw map[string]Server
	var perr error
	if spec.specKind == "" {
		raw, perr = parseCodexServers(string(b))
	} else {
		var obj map[string]any
		if obj, perr = decodeJSONObject(b); perr == nil {
			raw, perr = parseJSONServers(obj, mcpreg.JSONEntrySpellings[spec.specKind])
		}
	}
	if perr != nil {
		f.Note = perr.Error()
		return f, nil, nil
	}
	f.Parsable = true
	return f, raw, nil
}

func sortedNames(m map[string]Server) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func sortWarnings(ws []Warning) {
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Server < b.Server
	})
}
