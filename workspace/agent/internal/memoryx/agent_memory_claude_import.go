package memoryx

// One-time import of claude's own auto-memory into AF memory (ADR 0108 decision 6, step 1).
//
// The source is <claude config>/projects/<slug>/memory/*.md. A slug is claude's encoding of a
// working directory and cannot be decoded, so the mapping runs forward: the key of every
// working copy under ~/repos is computed, and a slug is matched against those keys.
//
// Everything is read-only until apply. A preview and an apply run the same evaluation; apply
// repeats it under the store's locks and refuses an item whose file changed since the preview.
// The secret scan has no override here: a file with a hit is listed with masked findings and
// skipped, and the member fixes the claude file and imports again.
//
// Nothing the member's own files carry reaches a response, a log or a commit before it passed
// the scan: a slug or file name that fails it is only counted, never named.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

const (
	// agentMemImportMaxItems bounds one apply: it holds both store locks for a commit per item.
	agentMemImportMaxItems = 50
	// agentMemImportMaxFiles bounds one preview; a directory with more is cut and says so.
	agentMemImportMaxFiles = 2000
	// agentMemImportIndex is claude's own index of its memory; it is not a memory.
	agentMemImportIndex = "MEMORY.md"

	// claudeImportRefresh is the Reason of an update that replaces a copy imported while long
	// descriptions were cut; claude's file is not newer, AF's text is simply incomplete.
	claudeImportRefresh = "refresh_shortened"

	claudeImportNew       = "new"
	claudeImportUpdate    = "update"
	claudeImportUnchanged = "unchanged"
	claudeImportForgotten = "forgotten"
	claudeImportSecret    = "secret"
	claudeImportInvalid   = "invalid"

	claudeImportNoProject = "no_project"
	claudeImportAmbiguous = "ambiguous"
)

var claudeImportSlugRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,255}$`)

// agentMemImportSource is one claude project that has memory files.
type agentMemImportSource struct {
	Slug  string `json:"slug"`
	Count int    `json:"count"`
	// Project is where the files would go; nil when Reason says why not (no_project: no working
	// copy under ~/repos has this slug; ambiguous: two projects do).
	Project *agentMemProject `json:"project,omitempty"`
	Reason  string           `json:"reason,omitempty"`
}

type agentMemImportSources struct {
	Sources []agentMemImportSource `json:"sources"`
	// Withheld counts projects left out because their slug fails the scan or is not a plain
	// directory.
	Withheld int `json:"withheld,omitempty"`
}

// agentMemImportItem is one claude file as the preview shows it.
type agentMemImportItem struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	SourceHash  string `json:"sourceHash,omitempty"`
	// SourceModified is the claude file's mtime; AFUpdated is when the AF memory last changed.
	SourceModified string                `json:"sourceModified,omitempty"`
	AFUpdated      string                `json:"afUpdated,omitempty"`
	Findings       []memorySecretFinding `json:"findings,omitempty"`

	entry agentMemEntry // the converted memory, for apply
	live  *agentMemEntry
}

type agentMemImportPreview struct {
	Slug     string               `json:"slug"`
	Project  *agentMemProject     `json:"project,omitempty"`
	Reason   string               `json:"reason,omitempty"`
	Items    []agentMemImportItem `json:"items"`
	Counts   map[string]int       `json:"counts"`
	Withheld int                  `json:"withheld,omitempty"`
	Truncate bool                 `json:"truncated,omitempty"`
	byName   map[string]*agentMemImportItem
}

// agentMemImportProjects maps every claude slug a working copy under ~/repos could have to its
// project. A worktree and its main clone give one project id, so they agree; a slug that two
// different ids claim is recorded as ambiguous.
func agentMemImportProjects() (map[string]*agentMemProject, map[string]bool) {
	byKey := map[string]*agentMemProject{}
	ambiguous := map[string]bool{}
	blocked := map[string]bool{}
	ents, err := os.ReadDir(gitx.ReposRoot())
	if err != nil {
		return byKey, ambiguous
	}
	// Each working copy costs a few git calls (~55 ms); a few at a time keeps a long ~/repos
	// under a second without a burst of processes on the shared host.
	type found struct {
		dir string
		p   *agentMemProject
	}
	results := make([]found, len(ents))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, d := range ents {
		dir := filepath.Join(gitx.ReposRoot(), d.Name())
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				results[i] = found{dir, agentMemProjectFor(dir)}
			}
		}()
	}
	wg.Wait()
	for _, r := range results {
		p := r.p
		if p == nil {
			continue
		}
		// A project whose root or display fails the scan still takes part in the ambiguity
		// check (dropping it first would let a colliding slug look unique); it is only never
		// shown, so it is removed from the result afterwards.
		if !agentMemCleanText(p.Root + "\n" + p.Display + "\n" + p.ID) {
			blocked[p.ID] = true
		}
		for _, k := range []string{claude.ProjectKey(r.dir), claude.ProjectKey(p.Root)} {
			if prev, ok := byKey[k]; ok && prev.ID != p.ID {
				ambiguous[k] = true
				continue
			}
			byKey[k] = p
		}
	}
	for k, p := range byKey {
		if blocked[p.ID] {
			delete(byKey, k)
		}
	}
	return byKey, ambiguous
}

// agentMemImportMemoryDir opens projects/<slug>/memory one component at a time with O_NOFOLLOW,
// and the caller reads every file relative to the returned handle. A check followed by a path
// read would let a parent be swapped for a symlink in between; a pinned handle cannot be.
// ok=false: there is no such directory, or a component is a symlink.
func agentMemImportMemoryDir(slug string) (*os.File, bool) {
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := syscall.Open(filepath.Join(claude.ConfigDir(), "projects"), flags, 0)
	if err != nil {
		return nil, false
	}
	for _, seg := range []string{slug, "memory"} {
		next, err := syscall.Openat(fd, seg, flags, 0)
		_ = syscall.Close(fd)
		if err != nil {
			return nil, false
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "memory"), true
}

// agentMemImportList returns the sources.
func agentMemImportList() agentMemImportSources {
	out := agentMemImportSources{Sources: []agentMemImportSource{}}
	root := filepath.Join(claude.ConfigDir(), "projects")
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	byKey, ambiguous := agentMemImportProjects()
	for _, d := range ents {
		slug := d.Name()
		mem, ok := agentMemImportMemoryDir(slug)
		if !ok {
			continue
		}
		files, _ := agentMemImportFiles(mem)
		mem.Close()
		if len(files) == 0 {
			continue
		}
		if !claudeImportSlugRe.MatchString(slug) || !agentMemCleanText(slug) {
			out.Withheld++
			continue
		}
		s := agentMemImportSource{Slug: slug, Count: len(files)}
		switch p := byKey[slug]; {
		case ambiguous[slug]:
			s.Reason = claudeImportAmbiguous
		case p == nil:
			s.Reason = claudeImportNoProject
		default:
			s.Project = p
		}
		out.Sources = append(out.Sources, s)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Slug < out.Sources[j].Slug })
	return out
}

// agentMemImportFiles lists the .md files of a memory directory, claude's index excluded.
func agentMemImportFiles(mem *os.File) (files []string, truncated bool) {
	ents, err := mem.ReadDir(-1)
	if err != nil {
		return nil, false
	}
	for _, d := range ents {
		n := d.Name()
		if !strings.HasSuffix(n, ".md") || n == agentMemImportIndex || d.IsDir() {
			continue
		}
		if len(files) >= agentMemImportMaxFiles {
			return files, true
		}
		files = append(files, n)
	}
	return files, false
}

// agentMemImportResolve finds the source and, when it can be imported, its project.
func agentMemImportResolve(slug string) (mem *os.File, p *agentMemProject, reason string, err error) {
	// The slug is never echoed: a directory name can be a token, and the list withholds those.
	if !claudeImportSlugRe.MatchString(slug) || !agentMemCleanText(slug) {
		return nil, nil, "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "slug is not a claude project directory name")
	}
	mem, ok := agentMemImportMemoryDir(slug)
	if !ok {
		return nil, nil, "", memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no claude memory for that project")
	}
	byKey, ambiguous := agentMemImportProjects()
	switch p = byKey[slug]; {
	case ambiguous[slug]:
		return mem, nil, claudeImportAmbiguous, nil
	case p == nil:
		return mem, nil, claudeImportNoProject, nil
	}
	return mem, p, "", nil
}

// agentMemImportHistory is every name this project's scope ever had in the history, and the
// names with a tombstone: what must never be brought back. One git call for the whole scope.
func agentMemImportHistory(rel string) (map[string]bool, error) {
	seen := map[string]bool{}
	if memoryHasCommits() {
		out, err := memoryGitRun("log", memoryBranch, "--no-renames", "--name-only", "--format=",
			"--", agentMemRepoPrefix+"/"+rel)
		if err != nil {
			return nil, err
		}
		prefix := agentMemRepoPrefix + "/" + rel + "/"
		for _, line := range strings.Split(out, "\n") {
			if n, ok := strings.CutPrefix(line, prefix); ok {
				if n, ok = strings.CutSuffix(n, ".md"); ok && agentMemValidName(n) {
					seen[n] = true
				}
			}
		}
	}
	// Not knowing is not "nothing was forgotten": a tombstone directory that cannot be listed
	// fails the whole evaluation, and a write checks the candidate's own tombstone again.
	dir, err := agentMemCheckDir(rel+"/"+agentMemTombDir, false)
	switch {
	case err == nil:
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, d := range ents {
			seen[d.Name()] = true
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	return seen, nil
}

// agentMemImportLegacyShortened says whether live is an import made when a description over 300
// bytes was cut and its full text put in front of the body: the cut text is derived from the
// claude description and the body is that description, a blank line and the claude body. Both
// have to match exactly, so an AF memory that merely looks similar is never taken for one.
func agentMemImportLegacyShortened(live agentMemEntry, desc, body string) bool {
	const oldMax = 300
	if len(desc) <= oldMax {
		return false
	}
	n := oldMax - len("…")
	for n > 0 && !utf8.RuneStart(desc[n]) {
		n--
	}
	cut := strings.TrimRight(desc[:n], " ") + "…"
	return live.Description == cut && live.Body == desc+"\n\n"+body
}

// agentMemImportShowable says whether a file name may be put in a response.
func agentMemImportShowable(name string) bool {
	if len(name) > 100 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return agentMemCleanText(name)
}

// agentMemImportOpen opens one file of the pinned memory directory without following a symlink.
func agentMemImportOpen(mem *os.File, file string) (*os.File, error) {
	fd, err := syscall.Openat(int(mem.Fd()), file, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "claude-memory"), nil
}

// agentMemImportEvaluate reads one claude file and decides its status. withheld=true: the file
// name itself cannot be shown, so the caller only counts it. Callers hold agentMemMu.
func agentMemImportEvaluate(mem *os.File, file, slug, rel string, history map[string]bool) (it agentMemImportItem, withheld bool) {
	stem := strings.TrimSuffix(file, ".md")
	if !agentMemImportShowable(stem) {
		return it, true
	}
	it.Name = stem
	invalid := func(reason string) (agentMemImportItem, bool) {
		it.Status, it.Reason = claudeImportInvalid, reason
		return it, false
	}
	if !agentMemValidName(stem) {
		return invalid("bad_name")
	}
	f, err := agentMemImportOpen(mem, file)
	if err != nil {
		return invalid("symlink")
	}
	defer f.Close()
	// The mtime is the opened file's, not a second look at the path.
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return invalid("symlink")
	}
	raw, err := agentMemReadHandle(f)
	switch {
	case err == agentMemErrTooLarge:
		return invalid("too_large")
	case err != nil:
		return invalid("symlink")
	}
	sum := sha256.Sum256(raw)
	it.SourceHash = hex.EncodeToString(sum[:])
	it.SourceModified = st.ModTime().UTC().Format(time.RFC3339)

	// NUL is judged on the raw file: the scanner cannot vouch for binary, and a parse would hide it.
	var findings []memorySecretFinding
	for _, f := range agentMemScanText("file", string(raw)) {
		switch f.Rule {
		case "nul-byte":
			return invalid("nul_byte")
		}
		// A hit anywhere in the file counts, fields that are not imported included.
		f.Path = "file"
		findings = append(findings, f)
	}
	e, ok := agentMemParse(raw)
	if !ok {
		return invalid("no_frontmatter")
	}
	desc := strings.TrimSpace(e.Description)
	body := strings.TrimSpace(e.Body)
	switch {
	case desc == "":
		return invalid("no_description")
	case strings.ContainsAny(desc, "\r\n"):
		return invalid("bad_description")
	case body == "":
		return invalid("no_body")
	}
	typ := strings.TrimSpace(e.Type)
	if !agentMemTypes[typ] {
		typ = ""
	}
	if utf8.RuneCountInString(desc) > agentMemMaxDescription {
		// Claude's own files stay far below this (the longest measured is 1,800 characters). It is
		// refused rather than cut: a cut description is what made the write-back read a
		// faithful import as a conflict.
		return invalid("description_too_long")
	}
	if len(body) > agentMemMaxBody {
		return invalid("too_large")
	}

	source := "claude:projects/" + slug + "/memory/" + file
	conv := agentMemEntry{
		Name: stem, Scope: agentMemScopeProject, Description: desc, Type: typ,
		AuthorKind: agentMemUnknown, AuthorSession: agentMemUnknown,
		Source: source, SourceHash: it.SourceHash, Body: body,
	}
	// Finding paths are fixed field names, so a hit never hands back text from the file.
	if len(findings) == 0 {
		// The raw file is clean; the decoded values can still differ from it (a JSON escape).
		// Every decoded frontmatter field, the ones that are replaced or dropped included.
		findings = append(findings, agentMemScanText("frontmatter", strings.Join(append([]string{
			e.Name, e.AuthorKind, e.AuthorSession, e.Created, e.Updated, e.Source, e.SourceHash}, e.Kinds...), "\n"))...)
		findings = append(findings, agentMemScanText("description", desc)...)
		findings = append(findings, agentMemScanText("body", body)...)
		findings = append(findings, agentMemScanText("type", e.Type)...)
	}
	if len(findings) == 0 {
		conv.Created, conv.Updated, conv.Revision = "1970-01-01T00:00:00Z", "1970-01-01T00:00:00Z", 1
		findings = append(findings, agentMemScanText("memory", agentMemPublished(conv))...)
		findings = append(findings, agentMemScanText("memory", string(agentMemRender(conv)))...)
	}
	if len(findings) > 0 {
		it.Status, it.Findings = claudeImportSecret, findings
		if len(it.Findings) > memorySecretMaxPerFile {
			it.Findings = it.Findings[:memorySecretMaxPerFile]
		}
		return it, false
	}
	it.Description, it.Type, it.entry = desc, typ, conv

	liveDir, err := agentMemCheckDir(rel, false)
	var live agentMemLoaded
	liveOK := false
	if err == nil {
		live, liveOK, err = agentMemLoadFile(filepath.Join(liveDir, stem+".md"), agentMemScopeProject, stem)
	}
	if err != nil && !os.IsNotExist(err) {
		return invalid("store_unreadable")
	}
	switch {
	case liveOK && live.Withheld:
		return invalid("store_withheld")
	case liveOK:
		le := live.Entry
		it.live, it.AFUpdated = &le, le.Updated
		at, perr := time.Parse(time.RFC3339, le.Updated)
		switch {
		case le.SourceHash == it.SourceHash && agentMemImportLegacyShortened(le, desc, body) && agentMemImportStateFor(rel, stem) == agentMemImportIntact:
			// Imported when long descriptions were cut. Claude's file is what was imported and AF
			// has not touched the copy since, so the full text replaces the cut one.
			it.Status, it.Reason = claudeImportUpdate, claudeImportRefresh
		case le.SourceHash == it.SourceHash:
			it.Status = claudeImportUnchanged
		case agentMemExportWrittenBack(raw, rel, stem, e):
			// A write-back (agent_memory_claude_export.go) stamps the file's mtime after AF's
			// update; without this the import would read its own output as a newer claude edit.
			it.Status = claudeImportUnchanged
		case perr != nil:
			// Overwriting needs proof that the claude file is newer.
			return invalid("store_unreadable")
		case st.ModTime().After(at):
			it.Status = claudeImportUpdate
		default:
			it.Status = claudeImportUnchanged
		}
	case history[stem]:
		it.Status = claudeImportForgotten
	default:
		it.Status = claudeImportNew
	}
	return it, false
}

// agentMemImportStateFor reads the stored file of a memory and asks the history whether it is
// still the import's own text.
func agentMemImportStateFor(rel, name string) agentMemImportState {
	raw, ok, err := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(rel), name+".md"))
	if err != nil || !ok {
		return agentMemImportUnknown
	}
	return agentMemImportStateOf(rel+"/"+name+".md", raw)
}

var claudeImportOrder = map[string]int{
	claudeImportNew: 0, claudeImportUpdate: 1, claudeImportUnchanged: 2,
	claudeImportForgotten: 3, claudeImportSecret: 4, claudeImportInvalid: 5,
}

// agentMemImportBuild evaluates every file of a source. Callers hold agentMemMu.
func agentMemImportBuild(slug string) (*agentMemImportPreview, error) {
	mem, p, reason, err := agentMemImportResolve(slug)
	if err != nil {
		return nil, err
	}
	defer mem.Close()
	pv := &agentMemImportPreview{Slug: slug, Project: p, Reason: reason, Items: []agentMemImportItem{},
		Counts: map[string]int{}, byName: map[string]*agentMemImportItem{}}
	if p == nil {
		return pv, nil
	}
	rel := "projects/" + p.ID
	history, err := agentMemImportHistory(rel)
	if err != nil {
		return nil, err
	}
	files, trunc := agentMemImportFiles(mem)
	pv.Truncate = trunc
	for _, f := range files {
		it, withheld := agentMemImportEvaluate(mem, f, slug, rel, history)
		if withheld {
			pv.Withheld++
			continue
		}
		pv.Counts[it.Status]++
		pv.Items = append(pv.Items, it)
	}
	sort.SliceStable(pv.Items, func(i, j int) bool {
		a, b := pv.Items[i], pv.Items[j]
		if claudeImportOrder[a.Status] != claudeImportOrder[b.Status] {
			return claudeImportOrder[a.Status] < claudeImportOrder[b.Status]
		}
		return a.Name < b.Name
	})
	for i := range pv.Items {
		pv.byName[pv.Items[i].Name] = &pv.Items[i]
	}
	return pv, nil
}

// agentMemImportPreviewFor is the read-only preview; it works with the switch off.
func agentMemImportPreviewFor(slug string) (*agentMemImportPreview, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	return agentMemImportBuild(slug)
}

// agentMemImportReqItem names one previewed item and the file content it was previewed at.
type agentMemImportReqItem struct {
	Name       string `json:"name"`
	SourceHash string `json:"sourceHash"`
}

type agentMemImportReq struct {
	Project string                  `json:"project"`
	Slug    string                  `json:"slug"`
	Items   []agentMemImportReqItem `json:"items"`
}

// agentMemImportResult is the outcome of one item: imported | updated | skipped.
type agentMemImportResult struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type agentMemImportApplied struct {
	Results []agentMemImportResult `json:"results"`
}

// agentMemImportApply imports the listed items, one commit each. The caller has checked the
// switch. Each item is evaluated again under the locks: the preview is a proposal, not a grant.
func agentMemImportApply(req agentMemImportReq, now time.Time) (agentMemImportApplied, error) {
	if len(req.Items) == 0 || len(req.Items) > agentMemImportMaxItems {
		return agentMemImportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"send between 1 and %d items per request", agentMemImportMaxItems)
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	pv, err := agentMemImportBuild(req.Slug)
	if err != nil {
		return agentMemImportApplied{}, err
	}
	if pv.Project == nil {
		return agentMemImportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this claude project cannot be imported (%s)", pv.Reason)
	}
	if pv.Project.ID != req.Project {
		return agentMemImportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the project this claude directory maps to has changed; preview it again")
	}
	rel := "projects/" + pv.Project.ID
	out := agentMemImportApplied{Results: []agentMemImportResult{}}
	seen := map[string]bool{}
	for _, ri := range req.Items {
		res := agentMemImportResult{Name: ri.Name, Result: "skipped"}
		if !agentMemValidName(ri.Name) || len(agentMemScanText("name", ri.Name)) > 0 {
			// The name came from the request and is not echoed.
			res.Name, res.Reason = "", "bad_name"
			out.Results = append(out.Results, res)
			continue
		}
		it := pv.byName[ri.Name]
		switch {
		case seen[ri.Name]:
			res.Reason = "duplicate"
		case it == nil:
			res.Reason = "not_found"
		case it.SourceHash != ri.SourceHash:
			res.Reason = "changed_since_preview"
		case it.Status != claudeImportNew && it.Status != claudeImportUpdate:
			res.Reason = "status_" + it.Status
		default:
			commit, err := agentMemImportWrite(*pv.Project, rel, it, now)
			if errors.Is(err, errAgentMemImportForgotten) {
				res.Reason = "status_forgotten"
			} else if err != nil {
				res.Reason = "write_failed"
				// Only the kind of failure is logged: a path in the error carries a name.
				log.Printf("agent memory: import: %s", agentMemErrKind(err))
			} else {
				res.Result, res.Commit = "imported", commit
				if it.Status == claudeImportUpdate {
					res.Result = "updated"
				}
			}
		}
		seen[ri.Name] = true
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// errAgentMemImportForgotten: a tombstone says this name was forgotten, whatever the history lists.
var errAgentMemImportForgotten = errors.New("memory was forgotten")

// agentMemImportWrite publishes one converted memory with the member as the commit's author.
func agentMemImportWrite(p agentMemProject, rel string, it *agentMemImportItem, now time.Time) (string, error) {
	e := it.entry
	stamp := now.UTC().Format(time.RFC3339)
	e.Created, e.Updated = stamp, stamp
	if it.live != nil {
		e.Revision, e.Created, e.Kinds, e.Pinned = it.live.Revision+1, it.live.Created, it.live.Kinds, it.live.Pinned
	} else {
		tomb, err := agentMemTombRevision(rel, e.Name)
		if err != nil {
			return "", err
		}
		if tomb > 0 {
			return "", errAgentMemImportForgotten
		}
		e.Revision = tomb + 1
	}
	data := agentMemRender(e)
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return "", &agentMemSecretErr{Findings: f}
	}
	repoRel := rel + "/" + e.Name + ".md"
	changes := []agentMemChange{{Rel: repoRel, Data: data}}
	extra, err := agentMemProjectInfoChange(p)
	if err != nil {
		return "", err
	}
	changes = append(changes, extra...)
	return agentMemApplyLocked(changes, "import", repoRel, agentMemMember, now)
}
