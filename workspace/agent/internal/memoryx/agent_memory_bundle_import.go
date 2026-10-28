package memoryx

// A one-shot, member-driven import of AF memory from a memory bundle into this workspace's store
// (#1983, ADR 0108 decision 9 and 10). The bundle is the 0022 export, taken in by the generic
// import (`POST /agents/memory/import`) as refs/imports/<id>/*; that path and restore deliberately
// skip `af/`, because they write without the secret scan decision 9 requires. This is the other
// door, and it goes through the scan and the publish path the claude and muse imports use.
//
// Only the latest text of each memory is read (the head of the imported lineage): the bundle's
// history, authors, revisions, pins and use counts do not travel. What does: name, description,
// type, body and the agent kinds a memory is limited to. What is reset, on purpose:
//
//   - revision: the memory gets the next number of this store (or 1);
//   - authorship: unknown, as for the claude import; `source` records where it came from
//     (`bundle:<import id>:<scope dir>/<name>@<source revision>`) and `source_hash` the file's hash;
//   - created / updated: the time of the import;
//   - pins and use counts: the member pins here again.
//
// Source projects are mapped to local ones by id (the same repository identity) or, when the id
// differs, by the one live local project with the same normalised remote. A project with no
// local match, or two, is listed and not imported. A memory that already exists here is never
// replaced (`exists` when the text differs, `unchanged` when it does not); a memory forgotten
// here, now or in the past, is never brought back. The preview and the apply run the same
// evaluation, and the apply repeats it under the store locks per request.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
)

// agentMemBundleMaxBytes bounds the memory text one evaluation takes on, counting every file by
// its size: two names for one blob are two files to decode, hold and scan, so the limit is on the
// work and the memory kept, not on the bytes read from git. agentMemBundleInfoBudget bounds the
// project records read in all. Variables so a test can lower them.
var (
	agentMemBundleMaxBytes   int64 = 64 << 20
	agentMemBundleInfoBudget int64 = 4 << 20
)

const (
	// agentMemBundleMaxFiles bounds what one preview evaluates; a bundle with more is cut and says so.
	agentMemBundleMaxFiles = 5000
	// agentMemBundleBatch bounds the memories one apply request writes (a commit each, under both locks).
	agentMemBundleBatch = 50
	// agentMemBundleInfoMax bounds a project.json that is read.
	agentMemBundleInfoMax = 64 << 10

	bundleStatusNew       = "new"
	bundleStatusUnchanged = "unchanged"
	bundleStatusExists    = "exists"
	bundleStatusForgotten = "forgotten"
	bundleStatusSecret    = "secret"
	bundleStatusInvalid   = "invalid"

	bundleScopeUser = "user"

	bundleNoLocal    = "no_local_project"
	bundleAmbiguous  = "ambiguous"
	bundleIncomplete = "search_incomplete"
)

// agentMemBundleItem is one memory of the bundle as the preview shows it.
type agentMemBundleItem struct {
	Name        string                `json:"name"`
	Status      string                `json:"status"`
	Reason      string                `json:"reason,omitempty"`
	Description string                `json:"description,omitempty"`
	Type        string                `json:"type,omitempty"`
	Revision    int                   `json:"revision,omitempty"` // the bundle's, for display only
	SourceHash  string                `json:"sourceHash,omitempty"`
	Findings    []memorySecretFinding `json:"findings,omitempty"`
}

// agentMemBundleScope is one source scope: a project of the bundle or the user scope.
type agentMemBundleScope struct {
	// Source is the id in the bundle ("user" for the user scope).
	Source  string           `json:"source"`
	Display string           `json:"display,omitempty"`
	Remote  string           `json:"remote,omitempty"`
	Target  *agentMemProject `json:"target,omitempty"`
	// Match says how the target was found: id | remote | user.
	Match string `json:"match,omitempty"`
	// Reason is set when there is no target: no_local_project | ambiguous | search_incomplete.
	Reason string               `json:"reason,omitempty"`
	Items  []agentMemBundleItem `json:"items"`
	Counts map[string]int       `json:"counts"`
	// Withheld counts files that are not shown: a name that fails the scan or is not a memory name.
	Withheld int `json:"withheld,omitempty"`
}

type agentMemBundlePreview struct {
	ImportID string `json:"importId"`
	// HasAF is false when the bundle carries no AF memory at all.
	HasAF  bool                  `json:"hasAF"`
	Scopes []agentMemBundleScope `json:"scopes"`
	// Withheld counts source projects left out because their directory name fails the scan; they
	// are never named.
	Withheld int `json:"withheld,omitempty"`
	// Truncated: the bundle holds more files than one preview evaluates.
	Truncated bool `json:"truncated,omitempty"`
	// Incomplete: the search for local working copies could not be completed, so an unmatched
	// project may only be one that was not found.
	Incomplete bool `json:"incomplete,omitempty"`

	// byKey indexes the evaluated items for the apply: scope source id, then name.
	byKey map[string]map[string]*agentMemBundleItem
	// entries holds the converted memory of each evaluated item, with its live copy.
	entries map[string]map[string]*agentMemImportItem
}

// agentMemBundleFile is one blob of af/ in the bundle's head.
type agentMemBundleFile struct {
	scope string // source scope id: "user" or a project id
	name  string
	sha   string
	size  int64
	// skip says why the blob is not read at all: too_large | over_budget.
	skip string
}

// agentMemBundleHead resolves an import id to the head commit of its lineage.
func agentMemBundleHead(importID string) (string, error) {
	if !memoryImportIDRe.MatchString(importID) {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadImport, "invalid importId")
	}
	ref, err := memoryImportRef(importID)
	if err != nil {
		return "", err
	}
	sha, err := memoryGitRun("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return "", memoryErrf(http.StatusNotFound, errCodeMemoryBadImport, "import %s is no longer available", importID)
	}
	return sha, nil
}

// agentMemBundleList reads the blobs under af/ of a commit: memory files by scope, and each
// project's project.json. Tombstones, usage sidecars and anything else are not memories.
func agentMemBundleList(head string) (files []agentMemBundleFile, infos map[string]agentMemBundleFile, truncated bool, err error) {
	out, err := memoryGitRun("ls-tree", "-r", "--long", head, "--", agentMemRepoPrefix+"/")
	if err != nil {
		return nil, nil, false, fmt.Errorf("read the bundle: %w", err)
	}
	infos = map[string]agentMemBundleFile{}
	for _, line := range strings.Split(out, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 || f[1] != "blob" {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		if m := agentMemRepoPathRe.FindStringSubmatch(p); m != nil {
			scope := bundleScopeUser
			if m[1] != "user" {
				scope = strings.TrimPrefix(m[1], "projects/")
				if !agentMemValidProjectID(scope) {
					continue
				}
			}
			if !agentMemValidName(m[2]) {
				continue
			}
			if len(files) >= agentMemBundleMaxFiles {
				return files, infos, true, nil
			}
			files = append(files, agentMemBundleFile{scope: scope, name: m[2], sha: f[2], size: size})
			continue
		}
		if rest, ok := strings.CutPrefix(p, agentMemRepoPrefix+"/projects/"); ok {
			if id, tail, ok := strings.Cut(rest, "/"); ok && tail == "project.json" && agentMemValidProjectID(id) {
				infos[id] = agentMemBundleFile{scope: id, name: "project.json", sha: f[2], size: size}
			}
		}
	}
	return files, infos, false, nil
}

// agentMemBundleBlobs reads blobs with one git process, streaming its answer: a blob over maxSize
// is skipped without being buffered, and a sha asked for twice is read once. The caller has already
// dropped what ls-tree says is too large, so this is the second line of defence, not the first.
func agentMemBundleBlobs(shas []string, maxSize int64) (map[string][]byte, error) {
	out := map[string][]byte{}
	seen := map[string]bool{}
	var uniq []string
	for _, s := range shas {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	if len(uniq) == 0 {
		return out, nil
	}
	cmd := memoryGit("cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("read the bundle: %w", err)
	}
	go func() {
		_, _ = io.WriteString(in, strings.Join(uniq, "\n")+"\n")
		_ = in.Close()
	}()
	fail := func(msg string) (map[string][]byte, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("read the bundle: " + msg)
	}
	r := bufio.NewReader(stdout)
	for range uniq {
		line, err := r.ReadString('\n')
		if err != nil {
			return fail("short answer")
		}
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			continue // missing or not a blob: left out, the caller reads it as unreadable
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || n < 0 {
			return fail("bad header")
		}
		if n > maxSize {
			if _, err := io.CopyN(io.Discard, r, n+1); err != nil {
				return fail("short body")
			}
			continue
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return fail("short body")
		}
		_, _ = r.ReadByte() // the newline after the body
		out[f[0]] = body
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("read the bundle: %w", err)
	}
	return out, nil
}

// agentMemBundleSourceInfo reads a bundle project's project.json for display and remote matching,
// holding it to the same checks as a stored one.
func agentMemBundleSourceInfo(raw []byte, id string) *agentMemProject {
	p := &agentMemProject{ID: id, Display: id}
	var v agentMemProject
	if json.Unmarshal(raw, &v) != nil || v.Display == "" || !agentMemCleanText(v.Root+"\n"+v.VCS+"\n"+v.Display) {
		return p
	}
	p.Display, p.VCS = v.Display, v.VCS
	if agentMemRemoteOK(v.Remote) {
		p.Remote = v.Remote
	}
	return p
}

// agentMemBundleTarget maps a source project to a live local one.
func agentMemBundleTarget(src *agentMemProject, live map[string]*agentMemProject, complete bool) (t *agentMemProject, match, reason string) {
	if p := live[src.ID]; p != nil {
		return p, "id", ""
	}
	if !complete {
		// "The one project with this remote" cannot be shown from a search that missed some: an
		// id match above is certain, this one is not.
		return nil, "", bundleIncomplete
	}
	if src.Remote == "" {
		return nil, "", bundleNoLocal
	}
	var found []*agentMemProject
	for _, p := range live {
		if p.Remote == src.Remote {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return nil, "", bundleNoLocal
	case 1:
		return found[0], "remote", ""
	}
	return nil, "", bundleAmbiguous
}

// agentMemBundleEvaluate converts one bundle file and decides its status against the local store.
// It is the claude import's evaluation for a file that is already in memory: every part of the
// file is scanned before anything is shown or written, and the scan has no override.
func agentMemBundleEvaluate(raw []byte, f agentMemBundleFile, importID, scope, rel string, history map[string]bool) (it agentMemBundleItem, conv *agentMemImportItem, withheld bool) {
	if !agentMemImportShowable(f.name) {
		return it, nil, true
	}
	it.Name = f.name
	srcRel := "user"
	if f.scope != bundleScopeUser {
		srcRel = "projects/" + f.scope
	}
	invalid := func(reason string) (agentMemBundleItem, *agentMemImportItem, bool) {
		it.Status, it.Reason = bundleStatusInvalid, reason
		return it, nil, false
	}
	if raw == nil {
		switch {
		case f.skip != "":
			return invalid(f.skip)
		case f.size > agentMemMaxFile:
			return invalid("too_large")
		}
		return invalid("unreadable")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	it.SourceHash = hash
	var findings []memorySecretFinding
	for _, fd := range agentMemScanText("file", string(raw)) {
		if fd.Rule == "nul-byte" {
			return invalid("nul_byte")
		}
		fd.Path = "file"
		findings = append(findings, fd)
	}
	e, ok := agentMemParse(raw)
	if !ok {
		return invalid("no_frontmatter")
	}
	it.Revision = e.Revision
	desc, body, typ := strings.TrimSpace(e.Description), strings.TrimSpace(e.Body), strings.TrimSpace(e.Type)
	switch {
	case desc == "":
		return invalid("no_description")
	case strings.ContainsAny(desc, "\r\n"):
		return invalid("bad_description")
	case body == "":
		return invalid("no_body")
	case utf8.RuneCountInString(desc) > agentMemMaxDescription:
		return invalid("description_too_long")
	case len(body) > agentMemMaxBody:
		return invalid("too_large")
	case len(e.Kinds) > agentMemMaxKinds:
		return invalid("bad_kinds")
	}
	for _, k := range e.Kinds {
		if !agentMemKindRe.MatchString(k) {
			return invalid("bad_kinds")
		}
	}
	if !agentMemTypes[typ] {
		typ = ""
	}
	source := "bundle:" + importID + ":" + srcRel + "/" + f.name + "@" + strconv.Itoa(e.Revision)
	c := agentMemEntry{
		Name: f.name, Scope: scope, Description: desc, Type: typ, Kinds: e.Kinds,
		AuthorKind: agentMemUnknown, AuthorSession: agentMemUnknown,
		Source: source, SourceHash: hash, Body: body,
	}
	if len(findings) == 0 {
		// The raw file is clean; the decoded values can still differ from it (a JSON escape).
		findings = append(findings, agentMemScanText("frontmatter", strings.Join(append([]string{
			e.Name, e.AuthorKind, e.AuthorSession, e.Created, e.Updated, e.Source, e.SourceHash}, e.Kinds...), "\n"))...)
		findings = append(findings, agentMemScanText("description", desc)...)
		findings = append(findings, agentMemScanText("body", body)...)
		findings = append(findings, agentMemScanText("type", e.Type)...)
	}
	if len(findings) == 0 {
		c.Created, c.Updated, c.Revision = "1970-01-01T00:00:00Z", "1970-01-01T00:00:00Z", 1
		findings = append(findings, agentMemScanText("memory", agentMemPublished(c))...)
		findings = append(findings, agentMemScanText("memory", string(agentMemRender(c)))...)
	}
	if len(findings) > 0 {
		it.Status, it.Findings = bundleStatusSecret, findings
		if len(it.Findings) > memorySecretMaxPerFile {
			it.Findings = it.Findings[:memorySecretMaxPerFile]
		}
		return it, nil, false
	}
	it.Description, it.Type = desc, typ

	conv = &agentMemImportItem{Name: f.name, SourceHash: hash, Description: desc, Type: typ, entry: c}
	conv.trailers = []string{"AF-Bundle-Import: " + importID, "AF-Bundle-Source: " + srcRel + "/" + f.name + ".md@" + strconv.Itoa(e.Revision)}
	liveDir, err := agentMemCheckDir(rel, false)
	var live agentMemLoaded
	liveOK := false
	if err == nil {
		live, liveOK, err = agentMemLoadFile(filepath.Join(liveDir, f.name+".md"), scope, f.name)
	}
	if err != nil && !os.IsNotExist(err) {
		return invalid("store_unreadable")
	}
	switch {
	case liveOK && live.Withheld:
		return invalid("store_withheld")
	case liveOK && agentMemBundleSame(live.Entry, c):
		it.Status = bundleStatusUnchanged
	case liveOK:
		// Never replaced: the member's own memory, or one an agent wrote here, is not the bundle's
		// to overwrite. Edit or forget it here and import again to change that.
		it.Status, it.Reason = bundleStatusExists, "local_differs"
	case history[f.name]:
		it.Status = bundleStatusForgotten
	default:
		it.Status = bundleStatusNew
	}
	return it, conv, false
}

// agentMemBundleSame compares what an import carries: description, type, body and kinds.
func agentMemBundleSame(a, b agentMemEntry) bool {
	if a.Description != b.Description || a.Type != b.Type || strings.TrimRight(a.Body, "\n") != strings.TrimRight(b.Body, "\n") {
		return false
	}
	ka, kb := append([]string(nil), a.Kinds...), append([]string(nil), b.Kinds...)
	sort.Strings(ka)
	sort.Strings(kb)
	return strings.Join(ka, ",") == strings.Join(kb, ",")
}

// agentMemBundleBuild evaluates the AF memory of an imported lineage. Callers hold agentMemMu.
//
// Only what is needed is read from the bundle: the small project.json files first, to find each
// scope's target, then the memory files of the scopes that have one, never a blob git says is
// over the limit, each distinct blob once and within agentMemBundleMaxBytes in all. A project
// that is not imported is not read at all.
func agentMemBundleBuild(importID string, live map[string]*agentMemProject, complete bool) (*agentMemBundlePreview, error) {
	head, err := agentMemBundleHead(importID)
	if err != nil {
		return nil, err
	}
	pv := &agentMemBundlePreview{ImportID: importID, Scopes: []agentMemBundleScope{}, Incomplete: !complete,
		byKey:   map[string]map[string]*agentMemBundleItem{},
		entries: map[string]map[string]*agentMemImportItem{}}
	files, infos, truncated, err := agentMemBundleList(head)
	if err != nil {
		return nil, err
	}
	pv.Truncated, pv.HasAF = truncated, len(files) > 0

	byScope := map[string][]agentMemBundleFile{}
	dirty := map[string]bool{}
	for _, f := range files {
		// A scope id is a directory name the bundle's author chose: one that fails the scan is
		// only counted, never named or read.
		if f.scope != bundleScopeUser && !agentMemCleanText(f.scope) {
			dirty[f.scope] = true
			continue
		}
		byScope[f.scope] = append(byScope[f.scope], f)
	}
	pv.Withheld = len(dirty)
	ids := make([]string, 0, len(byScope))
	for id := range byScope {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Each project's record, to match it.
	var infoShas []string
	var infoBytes int64
	for _, id := range ids {
		if i, ok := infos[id]; ok && i.size <= agentMemBundleInfoMax && infoBytes+i.size <= agentMemBundleInfoBudget {
			infoBytes += i.size
			infoShas = append(infoShas, i.sha)
		}
	}
	infoBlobs, err := agentMemBundleBlobs(infoShas, agentMemBundleInfoMax)
	if err != nil {
		return nil, err
	}
	type plan struct {
		sc     agentMemBundleScope
		scope  string
		rel    string
		target *agentMemProject
	}
	plans := map[string]*plan{}
	for _, id := range ids {
		pl := &plan{sc: agentMemBundleScope{Source: id, Items: []agentMemBundleItem{}, Counts: map[string]int{}}, scope: agentMemScopeProject}
		if id == bundleScopeUser {
			pl.scope, pl.rel, pl.sc.Match = agentMemScopeUser, "user", "user"
		} else {
			src := &agentMemProject{ID: id, Display: id}
			if i, ok := infos[id]; ok {
				if raw := infoBlobs[i.sha]; raw != nil {
					src = agentMemBundleSourceInfo(raw, id)
				}
			}
			pl.sc.Display, pl.sc.Remote = src.Display, src.Remote
			var match, reason string
			pl.target, match, reason = agentMemBundleTarget(src, live, complete)
			pl.sc.Target, pl.sc.Match, pl.sc.Reason = pl.target, match, reason
			if pl.target != nil {
				pl.rel = "projects/" + pl.target.ID
			}
		}
		plans[id] = pl
	}

	// The memory blobs of the scopes that will be imported: within the limit git reports, once each,
	// within the byte budget.
	var shas []string
	seen := map[string]bool{}
	var budget int64
	for _, id := range ids {
		if plans[id].rel == "" {
			continue
		}
		for i, f := range byScope[id] {
			switch {
			case f.size > agentMemMaxFile:
				byScope[id][i].skip = "too_large"
			case budget+f.size > agentMemBundleMaxBytes:
				byScope[id][i].skip = "over_budget"
				pv.Truncated = true
			default:
				// Counted per file, not per distinct blob: each name is decoded and held.
				budget += f.size
				if !seen[f.sha] {
					seen[f.sha] = true
					shas = append(shas, f.sha)
				}
			}
		}
	}
	blobs, err := agentMemBundleBlobs(shas, agentMemMaxFile)
	if err != nil {
		return nil, err
	}

	// A name two source scopes would both publish into one target is imported once, from the first
	// scope in id order; the other is `exists`, as it would be once the first has been written.
	claimed := map[string]bool{}
	for _, id := range ids {
		pl := plans[id]
		sc := pl.sc
		if pl.rel == "" {
			// Listed with its counts only: nothing of an unmatched project is read or shown.
			sc.Counts["skipped"] = len(byScope[id])
			pv.Scopes = append(pv.Scopes, sc)
			continue
		}
		history, err := agentMemImportHistory(pl.rel)
		if err != nil {
			return nil, err
		}
		pv.byKey[id], pv.entries[id] = map[string]*agentMemBundleItem{}, map[string]*agentMemImportItem{}
		for _, f := range byScope[id] {
			raw := blobs[f.sha]
			if f.skip != "" {
				raw = nil // not taken on, whatever another name of the same blob made us read
			}
			it, conv, withheld := agentMemBundleEvaluate(raw, f, importID, pl.scope, pl.rel, history)
			if withheld {
				sc.Withheld++
				continue
			}
			if it.Status == bundleStatusNew {
				if key := pl.rel + "/" + f.name; claimed[key] {
					it.Status, it.Reason, conv = bundleStatusExists, "duplicate_in_bundle", nil
				} else {
					claimed[key] = true
				}
			}
			if conv != nil {
				pv.entries[id][f.name] = conv
			}
			sc.Counts[it.Status]++
			sc.Items = append(sc.Items, it)
		}
		sort.SliceStable(sc.Items, func(i, j int) bool {
			a, b := sc.Items[i], sc.Items[j]
			if bundleOrder[a.Status] != bundleOrder[b.Status] {
				return bundleOrder[a.Status] < bundleOrder[b.Status]
			}
			return a.Name < b.Name
		})
		pv.Scopes = append(pv.Scopes, sc)
		last := &pv.Scopes[len(pv.Scopes)-1]
		for i := range last.Items {
			pv.byKey[id][last.Items[i].Name] = &last.Items[i]
		}
	}
	return pv, nil
}

var bundleOrder = map[string]int{
	bundleStatusNew: 0, bundleStatusUnchanged: 1, bundleStatusExists: 2, bundleStatusForgotten: 3,
	bundleStatusSecret: 4, bundleStatusInvalid: 5,
}

// agentMemBundlePreviewFor is the read-only preview.
func agentMemBundlePreviewFor(importID string) (*agentMemBundlePreview, error) {
	live, complete := agentMemLiveProjects()
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	return agentMemBundleBuild(importID, live, complete)
}

// agentMemBundleReqItem is one previewed memory: the source scope, the target project it was
// previewed against ("" for the user scope), the name and the file hash at the preview.
type agentMemBundleReqItem struct {
	Scope      string `json:"scope"`
	Target     string `json:"target,omitempty"`
	Name       string `json:"name"`
	SourceHash string `json:"sourceHash"`
}

type agentMemBundleReq struct {
	ImportID string                  `json:"importId"`
	Items    []agentMemBundleReqItem `json:"items"`
}

type agentMemBundleApplied struct {
	Results []agentMemBundleResult `json:"results"`
}

// agentMemBundleResult is the outcome of one item: imported | skipped, with the reason.
type agentMemBundleResult struct {
	Scope  string `json:"scope"`
	Name   string `json:"name"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// agentMemBundleApply writes the listed items, one commit each. The evaluation is made again
// under the store locks: the preview is a proposal, not a grant.
func agentMemBundleApply(req agentMemBundleReq, now time.Time) (agentMemBundleApplied, error) {
	out := agentMemBundleApplied{Results: []agentMemBundleResult{}}
	if len(req.Items) == 0 || len(req.Items) > agentMemBundleBatch {
		return out, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "send between 1 and %d items per request", agentMemBundleBatch)
	}
	live, complete := agentMemLiveProjects()
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	pv, err := agentMemBundleBuild(req.ImportID, live, complete)
	if err != nil {
		return out, err
	}
	// Group the request by scope so each is written through the shared apply.
	type group struct {
		scope *agentMemBundleScope
		items []agentMemImportReqItem
	}
	groups := map[string]*group{}
	var order []string
	for _, ri := range req.Items {
		res := agentMemBundleResult{Scope: ri.Scope, Name: ri.Name, Result: "skipped"}
		// The scope and the name came from the request and are echoed only when they could be a
		// directory name and a memory name that pass the scan.
		if ri.Scope != bundleScopeUser && (!agentMemValidProjectID(ri.Scope) || !agentMemCleanText(ri.Scope)) {
			res.Scope, res.Name, res.Reason = "", "", "bad_scope"
			out.Results = append(out.Results, res)
			continue
		}
		if !agentMemValidName(ri.Name) || len(agentMemScanText("name", ri.Name)) > 0 {
			res.Name, res.Reason = "", "bad_name"
			out.Results = append(out.Results, res)
			continue
		}
		var sc *agentMemBundleScope
		for i := range pv.Scopes {
			if pv.Scopes[i].Source == ri.Scope {
				sc = &pv.Scopes[i]
			}
		}
		switch {
		case sc == nil:
			res.Reason = "not_found"
		case sc.Target == nil && ri.Scope != bundleScopeUser:
			res.Reason = "no_local_project"
		case sc.Target != nil && sc.Target.ID != ri.Target, sc.Target == nil && ri.Target != "":
			res.Reason = "changed_since_preview"
		default:
			g := groups[ri.Scope]
			if g == nil {
				g = &group{scope: sc}
				groups[ri.Scope] = g
				order = append(order, ri.Scope)
			}
			g.items = append(g.items, agentMemImportReqItem{Name: ri.Name, SourceHash: ri.SourceHash})
			continue
		}
		out.Results = append(out.Results, res)
	}
	for _, id := range order {
		g := groups[id]
		var rel string
		var p *agentMemProject
		if id == bundleScopeUser {
			rel = "user"
		} else {
			p, rel = g.scope.Target, "projects/"+g.scope.Target.ID
		}
		// The shared apply judges each item against what was evaluated just now: status, hash,
		// duplicates, and the forgotten check again at the write.
		byName := map[string]*agentMemImportItem{}
		for name, it := range pv.byKey[id] {
			c := agentMemImportItem{Name: name, SourceHash: it.SourceHash}
			if conv := pv.entries[id][name]; conv != nil {
				c = *conv // a secret or invalid file has none: it stays listed with its status
			}
			c.Status, c.Reason = it.Status, it.Reason
			byName[name] = &c
		}
		applied := agentMemImportApplyRel(rel, p, byName, g.items, now)
		for _, r := range applied.Results {
			out.Results = append(out.Results, agentMemBundleResult{Scope: id, Name: r.Name, Result: r.Result, Reason: r.Reason, Commit: r.Commit})
		}
	}
	return out, nil
}

// HandleAgentMemoryBundlePreview says what importing the AF memory of an imported bundle would do.
// Read-only.
func HandleAgentMemoryBundlePreview(w http.ResponseWriter, r *http.Request) {
	pv, err := agentMemBundlePreviewFor(r.URL.Query().Get("importId"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pv)
}

// HandleAgentMemoryBundleApply imports the listed memories as the member: it answers only a
// request carrying the CP's member-origin mark (ADR 0113), and no MCP tool reaches it.
func HandleAgentMemoryBundleApply(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req agentMemBundleReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemBundleApply(req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
