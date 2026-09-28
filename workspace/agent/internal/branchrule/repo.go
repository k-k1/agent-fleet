package branchrule

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// FilePath is the committed declaration; GitflowFile is git-flow-next's shared config.
const (
	FilePath    = ".agent-fleet/branches"
	GitflowFile = ".gitflow"
	// maxCommitted bounds a committed file: anyone who can push writes it.
	maxCommitted = 16 << 10
)

type kv struct{ key, value string }

// Repo is the repository layer and what the resolve learned while reading it.
type Repo struct {
	Layer    Layer
	Warnings []Warning
	// Gitflow is "declared", "absent" or "suggest" (decision 9).
	Gitflow string
	// Bitbucket is "" for a non-Bitbucket origin, else ok / default / pending / error /
	// none (no connection or no answer yet to use).
	Bitbucket          string
	BitbucketFetchedAt int64
}

func warn(code, msg string) Warning { return Warning{code, msg} }

// readCommitted reads path from the blob at HEAD and parses it as git config. A symlink
// is a path string there and never followed, uncommitted edits do not count, and
// --no-includes is required: reading from stdin, git follows include.path by default
// (measured, docs/log/123 §6).
func readCommitted(dir, path string) ([]kv, bool, []Warning) {
	out, err := gitx.Run(dir, "ls-tree", "-l", "HEAD", "--", path)
	if err != nil || out == "" {
		return nil, false, nil
	}
	meta, _, _ := strings.Cut(out, "\t")
	f := strings.Fields(meta)
	if len(f) != 4 || f[1] != "blob" || (f[0] != "100644" && f[0] != "100755") {
		return nil, true, []Warning{warn("not_regular_file", path+" at HEAD is not a regular file; ignored")}
	}
	if n, err := strconv.Atoi(f[3]); err != nil || n > maxCommitted {
		return nil, true, []Warning{warn("file_too_large", path+" is larger than 16 KiB; ignored")}
	}
	blob, err := gitx.Cmd(dir, "cat-file", "blob", f[2]).Output()
	if err != nil {
		return nil, true, []Warning{warn("read_failed", path+": "+err.Error())}
	}
	kvs, err := parseConfig(blob)
	if err != nil {
		return nil, true, []Warning{warn("parse_failed", path+" is not valid git-config syntax; ignored")}
	}
	return kvs, true, nil
}

// parseConfig runs `git config --no-includes --null --file - --list` over b.
func parseConfig(b []byte) ([]kv, error) {
	cmd := gitx.Cmd("", "config", "--no-includes", "--null", "--file", "-", "--list")
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return splitNull(out), nil
}

// splitNull parses --null output: "key\nvalue\0", or "key\0" for a valueless key.
func splitNull(out []byte) []kv {
	var kvs []kv
	for _, e := range strings.Split(string(out), "\x00") {
		if e == "" {
			continue
		}
		k, v, _ := strings.Cut(e, "\n")
		kvs = append(kvs, kv{k, v})
	}
	return kvs
}

// readCloneGitflow reads the gitflow.* keys of the clone's config in one call, so a
// resolve under CloneLock never sees an Initialize Git Flow half-way (decision 9).
func readCloneGitflow(dir string) []kv {
	out, err := gitx.Cmd(dir, "config", "--null", "--get-regexp", `^gitflow\.`).Output()
	if err != nil {
		return nil // exit 1 = no key
	}
	return splitNull(out)
}

var cloneLocks sync.Map

// CloneLock serialises reads and writes of one parent clone's gitflow.* keys. Worktrees
// share the clone's config, so the lock is keyed by the common git dir.
func CloneLock(dir string) *sync.Mutex {
	key := dir
	if out, err := gitx.Run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil && out != "" {
		key = filepath.Clean(out)
	}
	m, _ := cloneLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// ValidBranch reports whether v can name a branch. A value that starts with "-" would
// reach git as an option and `@{-N}` is expanded by --branch, so both are refused here
// before git sees them.
func ValidBranch(v string) bool {
	if v == "" || strings.HasPrefix(v, "-") || strings.Contains(v, "@{") {
		return false
	}
	return gitx.Cmd("", "check-ref-format", "--branch", v).Run() == nil
}

// ValidBase accepts the two keywords or a branch name.
func ValidBase(v string) bool {
	return v == "head" || v == "default" || ValidBranch(v)
}

// ValidPrefix checks a prefix as `<prefix>x`: it may be empty or end in "/", which
// check-ref-format rejects on its own (measured: `feature/` exits 128).
func ValidPrefix(v string) bool {
	return v == "" || ValidBranch(v+"x")
}

// validTagPrefix checks git-flow's versiontag, which is a tag and may be empty.
func validTagPrefix(v string) bool {
	if strings.HasPrefix(v, "-") || strings.Contains(v, "@{") {
		return false
	}
	return gitx.Cmd("", "check-ref-format", "refs/tags/"+v+"1.0").Run() == nil
}

// fileRule turns `.agent-fleet/branches` into a rule; only the known keys are read.
func fileRule(kvs []kv) (Rule, []Warning) {
	r := Rule{Source: FilePath, Keys: map[string]string{}, Types: map[string]KindRule{}}
	var warns []Warning
	declared := map[string]bool{}
	for _, e := range kvs {
		switch {
		case e.key == "naming.name":
			r.Name = str(e.value)
			r.Keys["name"] = e.key
		case e.key == "naming.base":
			if !ValidBase(e.value) {
				warns = append(warns, warn("bad_ref", FilePath+": "+e.key+" = "+quote(e.value)+" is not a branch name; ignored"))
				continue
			}
			r.Base = str(e.value)
			r.Keys["base"] = e.key
		case strings.HasPrefix(e.key, "type."):
			rest := strings.TrimPrefix(e.key, "type.")
			i := strings.LastIndex(rest, ".")
			if i < 0 {
				warns = append(warns, warn("unknown_key", FilePath+": "+e.key+" is not a known key; ignored"))
				continue
			}
			kind, field := rest[:i], rest[i+1:]
			if !ValidKind(kind) {
				warns = append(warns, warn("unknown_kind", FilePath+": kind "+quote(kind)+" is not in the vocabulary; ignored"))
				continue
			}
			kr := r.Types[kind]
			switch field {
			case "prefix":
				if !ValidPrefix(e.value) {
					warns = append(warns, warn("bad_ref", FilePath+": "+e.key+" = "+quote(e.value)+" is not a branch prefix; ignored"))
					continue
				}
				kr.Prefix = str(e.value)
			case "base":
				if !ValidBase(e.value) {
					warns = append(warns, warn("bad_ref", FilePath+": "+e.key+" = "+quote(e.value)+" is not a branch name; ignored"))
					continue
				}
				kr.Base = str(e.value)
			case "from":
				if v := strings.TrimSpace(e.value); v != "" {
					kr.From = append(kr.From, v)
				}
			default:
				warns = append(warns, warn("unknown_key", FilePath+": "+e.key+" is not a known key; ignored"))
				continue
			}
			r.Types[kind] = kr
			r.Keys["types."+kind+"."+field] = e.key
			declared[kind] = true
		default:
			warns = append(warns, warn("unknown_key", FilePath+": "+e.key+" is not a known key; ignored"))
		}
	}
	r.Declares = sortedKinds(declared)
	return r, warns
}

func sortedKinds(set map[string]bool) []string {
	var out []string
	for _, k := range Kinds {
		if set[k] {
			out = append(out, k)
		}
	}
	return out
}

func (r Rule) empty() bool {
	if r.Name != nil || r.Base != nil || len(r.Declares) > 0 {
		return false
	}
	for _, k := range r.Types {
		if k.Prefix != nil || k.Base != nil || len(k.From) > 0 {
			return false
		}
	}
	return true
}

var avhPrefixKinds = []string{"feature", "bugfix", "release", "hotfix", "support"}

// gitflowRules reads git-flow's keys: the native form (gated on gitflow.version) and the
// avh form (gated on both branch keys), native first so it wins field by field. strict
// warns about keys outside gitflow.*, which only a committed .gitflow can carry.
func gitflowRules(kvs []kv, source string, strict bool) ([]Rule, []Warning) {
	var warns []Warning
	last := map[string]string{}
	branches := map[string]map[string]string{}
	for _, e := range kvs {
		if !strings.HasPrefix(e.key, "gitflow.") {
			if strict {
				warns = append(warns, warn("unknown_key", source+": "+e.key+" is not a known key; ignored"))
			}
			continue
		}
		last[e.key] = e.value
		if rest, ok := strings.CutPrefix(e.key, "gitflow.branch."); ok {
			if i := strings.LastIndex(rest, "."); i > 0 {
				name, field := rest[:i], rest[i+1:]
				if branches[name] == nil {
					branches[name] = map[string]string{}
				}
				branches[name][field] = e.value
			}
		}
	}
	if pv, ok := last["gitflow.prefix.versiontag"]; ok && !validTagPrefix(pv) {
		warns = append(warns, warn("bad_ref", source+": gitflow.prefix.versiontag = "+quote(pv)+" is not a tag prefix; ignored"))
	}
	var rules []Rule

	if last["gitflow.version"] != "" {
		r := Rule{Source: source, Keys: map[string]string{}, Types: map[string]KindRule{}}
		declared := map[string]bool{}
		names := make([]string, 0, len(branches))
		for n := range branches {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			b := branches[n]
			if b["type"] != "topic" {
				continue
			}
			if !ValidKind(n) {
				warns = append(warns, warn("topic_not_kind", source+": topic branch "+quote(n)+" is not a kind; ignored"))
				continue
			}
			kr := KindRule{}
			key := "gitflow.branch." + n + "."
			if p, ok := b["prefix"]; ok {
				if ValidPrefix(p) {
					kr.Prefix = str(p)
					r.Keys["types."+n+".prefix"] = key + "prefix"
				} else {
					warns = append(warns, warn("bad_ref", source+": "+key+"prefix = "+quote(p)+" is not a branch prefix; ignored"))
				}
			}
			for _, f := range []string{"startpoint", "parent"} {
				v, ok := b[f]
				if !ok || v == "" {
					continue
				}
				if !ValidBranch(v) {
					warns = append(warns, warn("bad_ref", source+": "+key+f+" = "+quote(v)+" is not a branch name; ignored"))
					continue
				}
				kr.Base = str(v)
				r.Keys["types."+n+".base"] = key + f
				break
			}
			r.Types[n] = kr
			declared[n] = true
		}
		r.Declares = sortedKinds(declared)
		if !r.empty() {
			rules = append(rules, r)
		}
	}

	master, develop := last["gitflow.branch.master"], last["gitflow.branch.develop"]
	if master != "" && develop != "" {
		ok := true
		for _, k := range []string{"gitflow.branch.master", "gitflow.branch.develop"} {
			if !ValidBranch(last[k]) {
				warns = append(warns, warn("bad_ref", source+": "+k+" = "+quote(last[k])+" is not a branch name; ignored"))
				ok = false
			}
		}
		if ok {
			r := Rule{Source: source, Keys: map[string]string{}, Types: map[string]KindRule{}}
			declared := map[string]bool{}
			for _, k := range avhPrefixKinds {
				kr := KindRule{Base: str(develop)}
				r.Keys["types."+k+".base"] = "gitflow.branch.develop"
				if k == "hotfix" {
					kr.Base = str(master)
					r.Keys["types."+k+".base"] = "gitflow.branch.master"
				}
				key := "gitflow.prefix." + k
				if p, ok := last[key]; ok {
					if ValidPrefix(p) {
						kr.Prefix = str(p)
						r.Keys["types."+k+".prefix"] = key
						declared[k] = true
					} else {
						warns = append(warns, warn("bad_ref", source+": "+key+" = "+quote(p)+" is not a branch prefix; ignored"))
					}
				}
				r.Types[k] = kr
			}
			r.Declares = sortedKinds(declared)
			rules = append(rules, r)
		}
	}
	return rules, warns
}

// ReadOptions carries what reading the repository layer needs besides the directory.
type ReadOptions struct {
	// ID is origin's host/owner/repo.
	ID        string
	Bitbucket *BitbucketCache
	Refresh   bool
}

// ReadRepo reads the repository layer from dir (decision 3). Its sources merge field by
// field, strongest first: .agent-fleet/branches, .gitflow, the clone's gitflow.* keys,
// Bitbucket's branching model.
func ReadRepo(ctx context.Context, dir string, opt ReadOptions) Repo {
	res := Repo{Layer: Layer{Name: "repository", Repository: true}}
	add := func(r Rule) {
		if !r.empty() {
			res.Layer.Rules = append(res.Layer.Rules, r)
		}
	}
	if kvs, ok, w := readCommitted(dir, FilePath); ok {
		res.Warnings = append(res.Warnings, w...)
		if kvs != nil {
			r, w := fileRule(kvs)
			res.Warnings = append(res.Warnings, w...)
			add(r)
		}
	}
	gitflow := false
	if kvs, ok, w := readCommitted(dir, GitflowFile); ok {
		res.Warnings = append(res.Warnings, w...)
		rs, w := gitflowRules(kvs, GitflowFile, true)
		res.Warnings = append(res.Warnings, w...)
		for _, r := range rs {
			add(r)
			gitflow = true
		}
	}
	lock := CloneLock(dir)
	lock.Lock()
	cfg := readCloneGitflow(dir)
	lock.Unlock()
	rs, w := gitflowRules(cfg, "git config", false)
	res.Warnings = append(res.Warnings, w...)
	for _, r := range rs {
		add(r)
		gitflow = true
	}

	if ws, repo, ok := bitbucketRepo(opt.ID); ok && opt.Bitbucket != nil {
		body, at, state, err := opt.Bitbucket.Get(ctx, ws+"/"+repo, opt.Refresh)
		if !at.IsZero() {
			res.BitbucketFetchedAt = at.Unix()
		}
		switch state {
		case statePending:
			res.Bitbucket = "pending"
			res.Warnings = append(res.Warnings, warn("bitbucket_pending",
				"Bitbucket's branching model has not arrived yet; the result does not include it"))
		case stateError:
			res.Bitbucket = "error"
			if !errors.Is(err, ErrNoConnection) {
				res.Warnings = append(res.Warnings, warn("bitbucket_error", "Bitbucket branching model: "+errText(err)))
			} else {
				res.Bitbucket = "none"
			}
		default:
			r, counted, perr := bitbucketRule(body)
			switch {
			case perr != nil:
				res.Bitbucket = "error"
				res.Warnings = append(res.Warnings, warn("bitbucket_error", "Bitbucket branching model: "+perr.Error()))
			case counted:
				res.Bitbucket = "ok"
				add(r)
			default:
				res.Bitbucket = "default"
			}
		}
	}

	switch {
	case gitflow:
		res.Gitflow = "declared"
	case len(res.Layer.Rules) == 0 && refExists(dir, "refs/remotes/origin/develop"):
		res.Gitflow = "suggest"
	default:
		res.Gitflow = "absent"
	}
	return res
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func refExists(dir, ref string) bool {
	return gitx.Cmd(dir, "show-ref", "--verify", "--quiet", ref).Run() == nil
}

// ResolveBase turns a resolved base into a branch that exists (decision 5). `head` is the
// clone's current branch; `default` is origin/HEAD; a name must exist locally or on
// origin. A resolved base that does not exist warns and falls back to `head`. It is never
// used for a base the person typed: that one is used as given.
func ResolveBase(dir, base string) (value, branch string, w *Warning) {
	head := func() string {
		out, _ := gitx.Run(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
		return out
	}
	switch base {
	case "", "head":
		return "head", head(), nil
	case "default":
		out, err := gitx.Run(dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		if err == nil && out != "" {
			return "default", strings.TrimPrefix(out, "origin/"), nil
		}
		return "head", head(), &Warning{"base_missing", "origin/HEAD is not set; the base is the current branch"}
	}
	if ValidBranch(base) && (refExists(dir, "refs/heads/"+base) || refExists(dir, "refs/remotes/origin/"+base)) {
		return base, base, nil
	}
	return "head", head(), &Warning{"base_missing", "the base " + quote(base) + " exists neither locally nor on origin; the base is the current branch"}
}

// CheckName is POST …/branch-name/check: the ref-name check plus the prefix warning.
func CheckName(name string, kinds []KindView) []Warning {
	var ws []Warning
	if !ValidBranch(name) {
		ws = append(ws, warn("bad_ref", quote(name)+" is not a valid branch name"))
	}
	return append(ws, CheckPrefix(name, kinds)...)
}
