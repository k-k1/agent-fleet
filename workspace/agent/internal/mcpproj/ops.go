package mcpproj

// ops.go — docs/56 §5/§10: the plan → apply operation model. Plan computes what
// applying ops would do WITHOUT writing; Apply performs the same computation and
// then writes, refusing (ErrPlanStale) if any file it would WRITE has changed
// since the hash the caller supplies was computed. No plan is stored server-side
// (docs/56 §5's "純粋なワンショット") — planHash is a pure function of (ops,
// current file bytes), so Apply recomputes and compares rather than looking
// anything up.
//
// v1 (P1) supports exactly two op kinds (docs/56 §12's phase table): "copy" (with
// onConflict resolution — the motivating novel-lab case has a conflict on day
// one) and "ignore" (§7.5 operation A). Freestanding upsert/delete/untrack CRUD
// is P2.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/projcfg"
)

// Op is one requested change (docs/56 §10's wire shape).
type Op struct {
	Op string `json:"op"` // "copy" | "ignore"

	// copy
	From        *OpEntryRef `json:"from,omitempty"`
	To          *OpFileRef  `json:"to,omitempty"`
	As          string      `json:"as,omitempty"`
	OnConflict  string      `json:"onConflict,omitempty"` // "overwrite" | "skip" | "rename"
	WithSecrets bool        `json:"withSecrets,omitempty"`
	Dialect     string      `json:"dialect,omitempty"` // "as-is" | "translate" | "expand"

	// ignore
	File  string `json:"file,omitempty"`
	Where string `json:"where,omitempty"` // "exclude" | "gitignore"
}

type OpEntryRef struct {
	File string `json:"file"`
	Name string `json:"name"`
}

type OpFileRef struct {
	File string `json:"file"`
}

// OpResult is one op's outcome, in the SAME order as the request.
type OpResult struct {
	Index  int    `json:"index"`
	Status string `json:"status"`           // "ok" | "skipped" | "error"
	Reason string `json:"reason,omitempty"` // a code (Code* consts below)

	// copy
	File         string  `json:"file,omitempty"`         // destination file
	ResolvedName string  `json:"resolvedName,omitempty"` // may differ from As under onConflict=rename
	Before       *Server `json:"before,omitempty"`       // masked; nil = no prior entry
	After        *Server `json:"after,omitempty"`        // masked
	// GateCode echoes the destination kind's docs/56 §8 gate — "書いた" (this
	// result) is not "効いている" until the gate clears.
	GateCode string `json:"gateCode,omitempty"`

	// ignore
	IgnoreFile     string `json:"ignoreFile,omitempty"`
	AlreadyPresent bool   `json:"alreadyPresent,omitempty"`
}

// PlanResult is what both Plan and Apply return — Plan's is a preview, Apply's
// describes what actually happened, but the shape is identical (docs/56 §5).
type PlanResult struct {
	PlanHash string     `json:"planHash"`
	Ops      []OpResult `json:"ops"`
	Warnings []Warning  `json:"warnings,omitempty"`
}

// ErrPlanStale: a file an op would WRITE changed since planHash was computed
// (docs/56 §5's optimistic lock — the working copy is shared with other sessions
// and the user's own editor).
var ErrPlanStale = errors.New("plan is stale: a file this operation would write has changed")

// Op result codes (docs/56 §10's one-reason-one-code, extended to op outcomes).
const (
	CodeCopySourceUnreadable = "mcp_project_copy_source_unreadable"
	CodeCopySourceMissing    = "mcp_project_copy_source_missing"
	CodeCopyDestUnreadable   = "mcp_project_copy_dest_unreadable"
	CodeCopyConflict         = "mcp_project_copy_conflict" // onConflict=skip
	CodeUnknownOp            = "mcp_project_unknown_op"
)

// Plan computes ops' effect without writing anything.
func Plan(dir string, ops []Op) (PlanResult, error) {
	return run(dir, ops, false)
}

// Apply performs ops for real, after confirming planHash still matches the
// CURRENT on-disk state of every file ops would write.
func Apply(dir string, ops []Op, planHash string) (PlanResult, error) {
	cur, err := computeHash(dir, ops)
	if err != nil {
		return PlanResult{}, err
	}
	if cur != planHash {
		return PlanResult{}, ErrPlanStale
	}
	return run(dir, ops, true)
}

// fileState is one touched file's in-memory working copy for the duration of a
// single Plan/Apply call — so a request with two ops touching the same file (two
// copies into one destination) sees the first op's effect when the second runs,
// and so apply writes each touched file exactly once at the end.
type fileState struct {
	absPath  string
	spec     fileSpec
	exists   bool
	parsable bool
	tracked  projcfg.TrackState
	raw      []byte
	servers  map[string]Server
	changed  bool
}

func run(dir string, ops []Op, write bool) (PlanResult, error) {
	vcs := projcfg.DetectVCS(dir)
	cache := map[string]*fileState{}
	res := PlanResult{}

	for i, op := range ops {
		var r OpResult
		var warnings []Warning
		switch op.Op {
		case "copy":
			r, warnings = applyCopyOp(dir, vcs, op, cache)
		case "ignore":
			r = applyIgnoreOp(dir, op, write)
		default:
			r = OpResult{Status: "error", Reason: CodeUnknownOp}
		}
		r.Index = i
		res.Ops = append(res.Ops, r)
		res.Warnings = append(res.Warnings, warnings...)
	}

	if write {
		for _, fs := range cache {
			if !fs.changed {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(fs.absPath), 0o755); err != nil {
				return res, err
			}
			if err := projcfg.WriteFileKeepMode(fs.absPath, fs.raw); err != nil {
				return res, err
			}
		}
	}

	hash, err := computeHash(dir, ops)
	if err != nil {
		return res, err
	}
	res.PlanHash = hash
	sortWarnings(res.Warnings)
	return res, nil
}

// --- copy ----------------------------------------------------------------

func applyCopyOp(dir, vcs string, op Op, cache map[string]*fileState) (OpResult, []Warning) {
	if op.From == nil || op.To == nil {
		return OpResult{Status: "error", Reason: CodeUnknownOp}, nil
	}

	src, err := loadFileState(dir, vcs, op.From.File, cache)
	if err != nil || !src.exists || !src.parsable {
		return OpResult{Status: "error", Reason: CodeCopySourceUnreadable, File: op.To.File}, nil
	}
	source, ok := src.servers[op.From.Name]
	if !ok {
		return OpResult{Status: "error", Reason: CodeCopySourceMissing, File: op.To.File}, nil
	}

	dst, err := loadFileState(dir, vcs, op.To.File, cache)
	if err != nil || (dst.exists && !dst.parsable) {
		return OpResult{Status: "error", Reason: CodeCopyDestUnreadable, File: op.To.File}, nil
	}

	name := op.As
	if name == "" {
		name = op.From.Name
	}
	existing, conflict := dst.servers[name]
	var beforePtr *Server
	if conflict {
		switch op.OnConflict {
		case "skip":
			m := maskServer(existing)
			return OpResult{
				Status: "skipped", Reason: CodeCopyConflict, File: op.To.File,
				ResolvedName: name, Before: &m,
			}, nil
		case "rename":
			name = uniqueName(name, dst.servers)
			conflict = false
		case "overwrite", "":
			m := maskServer(existing)
			beforePtr = &m
		default:
			return OpResult{Status: "error", Reason: CodeUnknownOp, File: op.To.File}, nil
		}
	}

	toWrite := source
	toWrite.Name = name
	if !op.WithSecrets {
		toWrite.Env = emptyValues(source.Env)
		toWrite.Headers = emptyValues(source.Headers)
	}
	destKind := ""
	if len(dst.spec.kinds) > 0 {
		destKind = dst.spec.kinds[0]
	}
	toWrite = convertServer(toWrite, op.Dialect, destKind)
	toWrite.Name = name

	warnings := nameWarnings(name, op.To.File)
	warnings = append(warnings, serverDialectWarnings(toWrite, op.To.File, dst.spec.kinds)...)
	warnings = append(warnings, secretWarnings(toWrite, op.To.File, dst.tracked.Tracked, dst.tracked.Uncertain)...)

	if err := applyServerToFileState(dst, name, toWrite); err != nil {
		return OpResult{Status: "error", Reason: CodeCopyDestUnreadable, File: op.To.File}, nil
	}

	afterMasked := maskServer(toWrite)
	return OpResult{
		Status: "ok", File: op.To.File, ResolvedName: name,
		Before: beforePtr, After: &afterMasked, GateCode: gateCodeFor(destKind),
	}, warnings
}

func loadFileState(dir, vcs, relPath string, cache map[string]*fileState) (*fileState, error) {
	if fs, ok := cache[relPath]; ok {
		return fs, nil
	}
	spec, ok := specFor(relPath)
	if !ok {
		return nil, fmt.Errorf("unsupported project file: %q", relPath)
	}
	fs := &fileState{absPath: filepath.Join(dir, filepath.FromSlash(relPath)), spec: spec, servers: map[string]Server{}}
	b, err := os.ReadFile(fs.absPath)
	switch {
	case err == nil:
		fs.exists = true
		fs.tracked = projcfg.Track(dir, vcs, relPath)
		fs.raw = b
		var perr error
		if spec.specKind == "" {
			fs.servers, perr = parseCodexServers(string(b))
		} else {
			var obj map[string]any
			if obj, perr = decodeJSONObject(b); perr == nil {
				fs.servers, perr = parseJSONServers(obj, mcpreg.JSONEntrySpellings[spec.specKind])
			}
		}
		fs.parsable = perr == nil
		if fs.servers == nil {
			fs.servers = map[string]Server{}
		}
	case os.IsNotExist(err):
		fs.parsable = true // nothing to fail to parse
	default:
		return nil, err
	}
	cache[relPath] = fs
	return fs, nil
}

// applyServerToFileState upserts name/s into fs's in-memory bytes+servers (does
// not touch disk — run() flushes changed files once, at the end, on Apply only).
func applyServerToFileState(fs *fileState, name string, s Server) error {
	if fs.spec.specKind == "" {
		block := buildCodexBlock(name, s)
		src := ""
		if fs.raw != nil {
			src = string(fs.raw)
		}
		fs.raw = []byte(projcfg.UpsertCodexBlock(src, name, block))
	} else {
		sp := mcpreg.JSONEntrySpellings[fs.spec.specKind]
		entry := buildJSONEntry(s, sp)
		base := fs.raw
		if base == nil {
			base = seedBytes(fs.spec)
		}
		newBytes, err := projcfg.UpsertJSONEntry(base, sp.ServersKey, name, entry)
		if err != nil {
			return err
		}
		fs.raw = newBytes
	}
	fs.exists = true
	fs.parsable = true
	fs.changed = true
	fs.servers[name] = s
	return nil
}

// seedBytes is the base a brand-new file starts from — never fully compact even
// with no seed content, or UpsertJSONEntry's compact-source detection would
// mistake "nothing here yet" for "this file's own style is minified" and write a
// single-line file no CLI would ever produce itself (docs/56 §6: match what the
// CLI itself would write).
func seedBytes(spec fileSpec) []byte {
	if spec.seed == nil {
		return []byte("{}\n")
	}
	b, err := json.MarshalIndent(spec.seed, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(b, '\n')
}

func specFor(path string) (fileSpec, bool) {
	for _, s := range fileSpecs {
		if s.path == path {
			return s, true
		}
	}
	return fileSpec{}, false
}

func gateCodeFor(kind string) string {
	for _, k := range kindInfos {
		if k.Kind == kind {
			return k.GateCode
		}
	}
	return ""
}

func emptyValues(m map[string]string) map[string]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = ""
	}
	return out
}

// uniqueName appends "-2", "-3", … to base until the result is absent from
// existing (docs/56 §10's onConflict="rename").
func uniqueName(base string, existing map[string]Server) string {
	if _, ok := existing[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

// --- ignore ----------------------------------------------------------------

func applyIgnoreOp(dir string, op Op, write bool) OpResult {
	if op.File == "" || (op.Where != projcfg.IgnoreExclude && op.Where != projcfg.IgnoreGitignore) {
		return OpResult{Status: "error", Reason: CodeUnknownOp}
	}
	path, err := projcfg.IgnoreFilePath(dir, op.Where)
	if err != nil {
		return OpResult{Status: "error", Reason: CodeUnknownOp}
	}
	already, err := projcfg.HasIgnorePattern(dir, op.Where, op.File)
	if err != nil {
		return OpResult{Status: "error", Reason: CodeUnknownOp, IgnoreFile: path}
	}
	if already {
		return OpResult{Status: "ok", IgnoreFile: path, AlreadyPresent: true}
	}
	if write {
		if err := projcfg.AddIgnorePattern(dir, op.Where, op.File); err != nil {
			return OpResult{Status: "error", Reason: CodeUnknownOp, IgnoreFile: path}
		}
	}
	return OpResult{Status: "ok", IgnoreFile: path}
}

// --- planHash ----------------------------------------------------------------

// computeHash hashes the CURRENT on-disk content of every file ops would WRITE
// (a copy op's source is read-only and excluded — Apply re-reads it fresh
// regardless of hash coverage; only concurrent writes to a file THIS request
// would also write need to be caught).
func computeHash(dir string, ops []Op) (string, error) {
	paths, err := writeTargets(dir, ops)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(h, "%s\x00%x\x00", p, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeTargets(dir string, ops []Op) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, op := range ops {
		switch op.Op {
		case "copy":
			if op.To != nil {
				add(filepath.Join(dir, filepath.FromSlash(op.To.File)))
			}
		case "ignore":
			path, err := projcfg.IgnoreFilePath(dir, op.Where)
			if err != nil {
				return nil, err
			}
			add(path)
		}
	}
	sort.Strings(out)
	return out, nil
}
