package memoryx

// One-shot copy of AF memory into claude's own auto-memory (ADR 0108 decision 6, the reverse of
// the import). It exists so that a member who turns AF memory off again does not lose what was
// learned through it: while the switch is on claude's native writer is off, so there is no
// concurrent writer to race.
//
// Never continuous. A preview and an apply run the same evaluation; apply repeats it under the
// locks and refuses a request whose token no longer matches. A native file AF did not write, or
// that changed since AF wrote it, is a conflict and is overwritten only when the request names
// it. A native-only file is never deleted. Claude's memory is snapshotted (ADR 0022) before the
// first write, and nothing is written when that fails.
//
// Every write goes through a directory handle opened component by component with O_NOFOLLOW,
// as a temp file plus rename, so a symlink anywhere on the path can neither redirect nor be
// followed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

const (
	claudeExportNew        = "new"
	claudeExportUpdate     = "update"
	claudeExportUnchanged  = "unchanged"
	claudeExportConflict   = "conflict"
	claudeExportNativeOnly = "native_only"
	claudeExportRemove     = "remove"
	claudeExportSecret     = "secret"

	// memoryTriggerPreExport is the snapshot taken before a write-back (ADR 0022).
	memoryTriggerPreExport = "pre-export"

	// Claude loads at most this much of MEMORY.md; the rest is cut off without a word.
	claudeExportIndexMaxLines = 200
	claudeExportIndexMaxBytes = 24 << 10
	claudeExportIndexDescRune = 150
)

// claudeExportNativeNameRe is a native file stem that may be shown and linked from the index.
// Claude's own files use underscores, which AF names do not.
var claudeExportNativeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// agentMemExportHash is what af_hash records: the description, type and body as written. The
// import compares the same function over a native file to tell "still what AF wrote" from an edit.
func agentMemExportHash(desc, typ, body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(desc) + "\x00" + strings.TrimSpace(typ) + "\x00" + strings.TrimSpace(body)))
	return hex.EncodeToString(sum[:])
}

func agentMemExportSource(projectID, name string, rev int) string {
	return projectID + "/" + name + "@" + strconv.Itoa(rev)
}

// agentMemExportRender is the native file for one AF memory, in the shape claude writes
// (nested metadata) with AF's provenance beside the type. AF-only fields are not written.
func agentMemExportRender(projectID string, e agentMemEntry) (data []byte, hash string) {
	hash = agentMemExportHash(e.Description, e.Type, e.Body)
	body := strings.TrimRight(e.Body, "\n")
	var b strings.Builder
	q := func(s string) string { return jsonQuote(s) }
	b.WriteString("---\nname: " + e.Name + "\n")
	b.WriteString("description: " + q(e.Description) + "\n")
	b.WriteString("metadata:\n")
	if e.Type != "" {
		b.WriteString("  type: " + e.Type + "\n")
	}
	b.WriteString("  af_source: " + q(agentMemExportSource(projectID, e.Name, e.Revision)) + "\n")
	b.WriteString("  af_hash: " + q(hash) + "\n")
	b.WriteString("---\n" + body + "\n")
	return []byte(b.String()), hash
}

// agentMemExportScan judges the text about to cross into another store: every field and the
// rendered file. The AF file passed the scan on the way in; this one is the second look.
func agentMemExportScan(e agentMemEntry, data []byte) []memorySecretFinding {
	return append(append(agentMemScanText("description", e.Description), agentMemScanText("body", e.Body)...),
		agentMemScanText("file", string(data))...)
}

// agentMemExportMeta reads the provenance keys under `metadata:`.
func agentMemExportMeta(raw []byte) (source, hash string) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return "", ""
	}
	head, _, _ := strings.Cut(rest, "\n---")
	inMeta := false
	for _, line := range strings.Split(head, "\n") {
		if line == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			k, v, _ := strings.Cut(line, ":")
			inMeta = strings.TrimSpace(k) == "metadata" && strings.TrimSpace(v) == ""
			continue
		}
		if !inMeta {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			var out string
			if json.Unmarshal([]byte(v), &out) == nil {
				v = out
			}
		}
		switch strings.TrimSpace(k) {
		case "af_source":
			source = v
		case "af_hash":
			hash = v
		}
	}
	return source, hash
}

// agentMemExportWrittenBack says whether a claude file is still exactly what a write-back put
// there for this memory: af_source names it and the text still hashes to af_hash. rel is the
// store scope, "projects/<id>".
func agentMemExportWrittenBack(raw []byte, rel, name string, e agentMemEntry) bool {
	source, afHash := agentMemExportMeta(raw)
	projectID, ok := strings.CutPrefix(rel, "projects/")
	return ok && afHash != "" && agentMemExportOwn(source, projectID, name) &&
		afHash == agentMemExportHash(e.Description, e.Type, e.Body)
}

// agentMemExportOwn says whether a native file's af_source names this project's memory `name`.
func agentMemExportOwn(source, projectID, name string) bool {
	rest, ok := strings.CutPrefix(source, projectID+"/"+name+"@")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n >= 1
}

// agentMemExportRecordedRevision is the revision af_source names, 0 when it is not parseable.
func agentMemExportRecordedRevision(source string) int {
	i := strings.LastIndexByte(source, '@')
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(source[i+1:])
	return n
}

// agentMemExportNative is one file of claude's memory directory as read.
type agentMemExportNative struct {
	raw      []byte
	fileHash string // sha256 of the whole file, for the preview token
	mtime    time.Time
	source   string
	afHash   string
	hash     string // agentMemExportHash of what the file says now
	desc     string
	parsed   bool
	bad      string // why it cannot be judged: symlink | too_large | unreadable
}

func agentMemExportReadNative(mem *os.File, file string) agentMemExportNative {
	var n agentMemExportNative
	f, err := agentMemImportOpen(mem, file)
	if err != nil {
		n.bad = "symlink"
		return n
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		n.bad = "symlink"
		return n
	}
	n.mtime = st.ModTime()
	raw, err := agentMemReadHandle(f)
	switch {
	case err == agentMemErrTooLarge:
		n.bad = "too_large"
		return n
	case err != nil:
		n.bad = "unreadable"
		return n
	}
	n.raw = raw
	sum := sha256.Sum256(raw)
	n.fileHash = hex.EncodeToString(sum[:])
	n.source, n.afHash = agentMemExportMeta(raw)
	if e, ok := agentMemParse(raw); ok {
		n.parsed = true
		n.desc = strings.TrimSpace(e.Description)
		n.hash = agentMemExportHash(e.Description, e.Type, e.Body)
	}
	return n
}

// agentMemExportItem is one name as the preview shows it.
type agentMemExportItem struct {
	Name        string                `json:"name"`
	Status      string                `json:"status"`
	Reason      string                `json:"reason,omitempty"`
	Description string                `json:"description,omitempty"`
	Type        string                `json:"type,omitempty"`
	Revision    int                   `json:"revision,omitempty"`
	AFUpdated   string                `json:"afUpdated,omitempty"`
	Modified    string                `json:"nativeModified,omitempty"`
	Findings    []memorySecretFinding `json:"findings,omitempty"`

	data     []byte // the file to write
	nativeFH string // sha256 of the native file the evaluation saw, "" when absent
	locked   bool   // never overwritten, even when named (a symlink or unreadable leaf)
	entry    *agentMemEntry
	native   *agentMemExportNative
}

type agentMemExportPreview struct {
	Project  *agentMemProject     `json:"project"`
	Slug     string               `json:"slug"`
	Items    []agentMemExportItem `json:"items"`
	Counts   map[string]int       `json:"counts"`
	Token    string               `json:"token"`
	NativeOK bool                 `json:"nativeExists"`
	// UserScope counts user-scope AF memories, which have no native counterpart and are not written.
	UserScope int `json:"userScope"`
	// NotForClaude counts AF memories limited to other agent kinds.
	NotForClaude int `json:"notForClaude,omitempty"`
	// Withheld counts files left out because their name or text cannot be shown (AF files that
	// fail the scan, native files with a name that is not a plain file name).
	Withheld int `json:"withheld,omitempty"`
	// Index says what MEMORY.md would become: new | rewrite | unchanged | symlink, with how many
	// memories its lines list and how many fall into the "N more" line.
	Index       string `json:"index"`
	IndexListed int    `json:"indexListed"`
	IndexMore   int    `json:"indexMore"`
	SwitchOn    bool   `json:"switchOn"`
	Truncated   bool   `json:"truncated,omitempty"`
	byName      map[string]*agentMemExportItem
	indexOld    []byte
}

type agentMemExportSources struct {
	Projects []agentMemExportSourceRow `json:"projects"`
}

// agentMemExportSourceRow is one AF project that has project-scope memory.
type agentMemExportSourceRow struct {
	Project *agentMemProject `json:"project"`
	Count   int              `json:"count"`
	// Reason is set when the project cannot be written back: no_root (no main working copy recorded).
	Reason string `json:"reason,omitempty"`
}

// agentMemExportList lists the AF projects that have memory to write back.
func agentMemExportList() (agentMemExportSources, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := agentMemExportSources{Projects: []agentMemExportSourceRow{}}
	dir, err := agentMemCheckDir("projects", false)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, d := range ents {
		if !d.IsDir() || !agentMemValidProjectID(d.Name()) {
			continue
		}
		es, _, err := agentMemLoadDir(agentMemScopeProject, "projects/"+d.Name())
		if err != nil || len(es) == 0 {
			continue
		}
		p := agentMemProjectInfo(d.Name())
		row := agentMemExportSourceRow{Project: p, Count: len(es)}
		if p.Root == "" {
			row.Reason = "no_root"
		}
		out.Projects = append(out.Projects, row)
	}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].Project.Display < out.Projects[j].Project.Display })
	return out, nil
}

// agentMemExportOpenDir opens projects/<slug>/memory below the config dir one component at a
// time with O_NOFOLLOW. With create, a missing component is made. A symlink anywhere is refused.
func agentMemExportOpenDir(slug string, create bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if create {
		if err := os.MkdirAll(claude.ConfigDir(), 0o700); err != nil {
			return nil, err
		}
	}
	fd, err := syscall.Open(filepath.Join(claude.ConfigDir(), "projects"), flags, 0)
	if err == syscall.ENOENT && create {
		if err = syscall.Mkdir(filepath.Join(claude.ConfigDir(), "projects"), 0o700); err == nil || err == syscall.EEXIST {
			fd, err = syscall.Open(filepath.Join(claude.ConfigDir(), "projects"), flags, 0)
		}
	}
	if err != nil {
		return nil, err
	}
	for _, seg := range []string{slug, "memory"} {
		next, err := syscall.Openat(fd, seg, flags, 0)
		if err == syscall.ENOENT && create {
			if err = syscall.Mkdirat(fd, seg, 0o700); err == nil || err == syscall.EEXIST {
				next, err = syscall.Openat(fd, seg, flags, 0)
			}
		}
		_ = syscall.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "memory"), nil
}

// agentMemExportTarget resolves a project id to its record and claude slug.
func agentMemExportTarget(projectID string) (*agentMemProject, string, error) {
	if !agentMemValidProjectID(projectID) {
		return nil, "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "project is not a project id")
	}
	p := agentMemProjectInfo(projectID)
	if p.Root == "" {
		return nil, "", memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this project has no main working copy recorded, so claude's directory for it is unknown")
	}
	slug := claude.ProjectKey(p.Root)
	if !claudeImportSlugRe.MatchString(slug) {
		return nil, "", memoryErrf(http.StatusConflict, errCodeMemoryConflict, "the claude directory name for this project is not usable")
	}
	return p, slug, nil
}

// agentMemExportBuild evaluates one project. Callers hold agentMemMu (read is enough).
func agentMemExportBuild(projectID string) (*agentMemExportPreview, error) {
	p, slug, err := agentMemExportTarget(projectID)
	if err != nil {
		return nil, err
	}
	rel := "projects/" + projectID
	es, withheld, err := agentMemLoadDir(agentMemScopeProject, rel)
	if err != nil {
		return nil, err
	}
	agentMemSetUses(es, rel)
	pv := &agentMemExportPreview{Project: p, Slug: slug, Items: []agentMemExportItem{}, Counts: map[string]int{},
		Withheld: withheld, byName: map[string]*agentMemExportItem{}}
	if AgentMemoryEnabled != nil {
		pv.SwitchOn = AgentMemoryEnabled()
	}
	if us, _, err := agentMemLoadDir(agentMemScopeUser, "user"); err == nil {
		pv.UserScope = len(us)
	}
	// AF names that exist in the store but are withheld (they fail the scan) are not "forgotten":
	// a native copy of one must not be removed because AF's file could not be read.
	protected := map[string]bool{}
	if dir, err := agentMemCheckDir(rel, false); err == nil {
		if ents, err := os.ReadDir(dir); err == nil {
			for _, d := range ents {
				if n, ok := strings.CutSuffix(d.Name(), ".md"); ok {
					protected[n] = true
				}
			}
		}
	}

	var mem *os.File
	if m, err := agentMemExportOpenDir(slug, false); err == nil {
		mem = m
		defer mem.Close()
		pv.NativeOK = true
	} else if !os.IsNotExist(err) && err != syscall.ENOENT {
		return nil, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"claude's memory directory for this project is not a plain directory path (a symlink?)")
	}
	var nativeFiles []string
	if mem != nil {
		nativeFiles, pv.Truncated = agentMemImportFiles(mem)
	}
	natives := map[string]*agentMemExportNative{}
	for _, f := range nativeFiles {
		n := agentMemExportReadNative(mem, f)
		natives[strings.TrimSuffix(f, ".md")] = &n
	}

	afNames := map[string]bool{}
	for i := range es {
		e := es[i]
		if !agentMemAppliesTo(e, "claude") {
			pv.NotForClaude++
			continue
		}
		afNames[e.Name] = true
		it := agentMemExportItem{Name: e.Name, Description: e.Description, Type: e.Type, Revision: e.Revision,
			AFUpdated: e.Updated, entry: &es[i]}
		data, hash := agentMemExportRender(projectID, e)
		it.data = data
		if f := agentMemExportScan(e, data); len(f) > 0 {
			it.Status, it.Findings = claudeExportSecret, f
			if len(it.Findings) > memorySecretMaxPerFile {
				it.Findings = it.Findings[:memorySecretMaxPerFile]
			}
			pv.add(it)
			continue
		}
		n := natives[e.Name]
		it.native = n
		switch {
		case n == nil:
			it.Status = claudeExportNew
		case n.bad != "":
			it.Status, it.Reason, it.locked = claudeExportConflict, n.bad, true
		case n.parsed && n.hash == hash:
			it.Status = claudeExportUnchanged
			it.nativeFH = n.fileHash
		case n.parsed && agentMemExportOwn(n.source, projectID, e.Name) && n.afHash == n.hash:
			it.Status = claudeExportUpdate
			it.nativeFH = n.fileHash
		default:
			it.Status, it.nativeFH = claudeExportConflict, n.fileHash
			it.Reason = "not_written_by_af"
			if agentMemExportOwn(n.source, projectID, e.Name) {
				it.Reason = "changed_since_write"
			}
		}
		if n != nil {
			it.Modified = n.mtime.UTC().Format(time.RFC3339)
		}
		pv.add(it)
	}

	names := make([]string, 0, len(natives))
	for n := range natives {
		if !afNames[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		n := natives[name]
		if !claudeExportNativeNameRe.MatchString(name) || !agentMemImportShowable(name) {
			pv.Withheld++
			continue
		}
		it := agentMemExportItem{Name: name, native: n, Modified: n.mtime.UTC().Format(time.RFC3339), nativeFH: n.fileHash}
		switch {
		case n.bad != "":
			it.Status, it.Reason = claudeExportNativeOnly, n.bad
		case n.parsed && agentMemExportOwn(n.source, projectID, name) && !protected[name]:
			if n.afHash == n.hash {
				it.Status = claudeExportRemove
			} else {
				it.Status, it.Reason = claudeExportConflict, "forgotten_but_changed"
			}
		default:
			it.Status = claudeExportNativeOnly
			if protected[name] && agentMemExportOwn(n.source, projectID, name) {
				it.Reason = "af_withheld"
			}
		}
		pv.add(it)
	}
	sort.SliceStable(pv.Items, func(i, j int) bool {
		a, b := pv.Items[i], pv.Items[j]
		if claudeExportOrder[a.Status] != claudeExportOrder[b.Status] {
			return claudeExportOrder[a.Status] < claudeExportOrder[b.Status]
		}
		return a.Name < b.Name
	})
	for i := range pv.Items {
		pv.byName[pv.Items[i].Name] = &pv.Items[i]
	}
	pv.planIndex(mem)
	pv.Token = pv.token()
	return pv, nil
}

var claudeExportOrder = map[string]int{
	claudeExportNew: 0, claudeExportUpdate: 1, claudeExportRemove: 2, claudeExportConflict: 3,
	claudeExportSecret: 4, claudeExportUnchanged: 5, claudeExportNativeOnly: 6,
}

func (pv *agentMemExportPreview) add(it agentMemExportItem) {
	pv.Counts[it.Status]++
	pv.Items = append(pv.Items, it)
}

// token fingerprints what the preview saw: the apply refuses when the AF text, the native files
// or the statuses moved in between.
func (pv *agentMemExportPreview) token() string {
	h := sha256.New()
	for _, it := range pv.Items {
		fmt.Fprintf(h, "%s|%s|%s|%x\n", it.Name, it.Status, it.nativeFH, sha256.Sum256(it.data))
	}
	fmt.Fprintf(h, "index|%s\n", pv.Index)
	return hex.EncodeToString(h.Sum(nil))
}

// claudeExportIndexRow is one line candidate of MEMORY.md.
type claudeExportIndexRow struct {
	name, desc string
}

func claudeExportLine(r claudeExportIndexRow) string {
	d := strings.Join(strings.Fields(r.desc), " ")
	d = agentMemCutRunes(d, claudeExportIndexDescRune)
	if d == "" {
		return "- [" + r.name + "](" + r.name + ".md)\n"
	}
	return "- [" + r.name + "](" + r.name + ".md) — " + d + "\n"
}

// claudeExportBuildIndex renders MEMORY.md within claude's load limit: lines in rank order, then
// one closing line saying how many did not fit. It returns the text, how many rows it lists.
func claudeExportBuildIndex(rows []claudeExportIndexRow) (text string, listed int) {
	lines := make([]string, len(rows))
	total := 0
	for i, r := range rows {
		lines[i] = claudeExportLine(r)
		total += len(lines[i])
	}
	if len(rows) <= claudeExportIndexMaxLines && total <= claudeExportIndexMaxBytes {
		return strings.Join(lines, ""), len(rows)
	}
	// The closing line counts toward both limits; reserve its widest form.
	more := func(n int) string { return fmt.Sprintf("%d more memories in this directory; search them by name\n", n) }
	reserve := len(more(len(rows)))
	var b strings.Builder
	for i, l := range lines {
		if i+1 > claudeExportIndexMaxLines-1 || b.Len()+len(l)+reserve > claudeExportIndexMaxBytes {
			break
		}
		b.WriteString(l)
		listed++
	}
	b.WriteString(more(len(rows) - listed))
	return b.String(), listed
}

// indexText renders MEMORY.md as it will be once the apply is done. AF's memories come first in
// AF's ranking, then native files that stay, newest first. over names the conflicts that are
// replaced with AF's text.
func (pv *agentMemExportPreview) indexText(over map[string]bool) (text string, listed, total int) {
	var ranked []agentMemEntry
	for i := range pv.Items {
		it := &pv.Items[i]
		if it.entry == nil {
			continue
		}
		switch it.Status {
		case claudeExportNew, claudeExportUpdate, claudeExportUnchanged:
			ranked = append(ranked, *it.entry)
		case claudeExportConflict:
			if over[it.Name] && !it.locked {
				ranked = append(ranked, *it.entry)
			}
		}
	}
	agentMemRank(ranked)
	var rows []claudeExportIndexRow
	seen := map[string]bool{}
	for _, e := range ranked {
		rows = append(rows, claudeExportIndexRow{e.Name, e.Description})
		seen[e.Name] = true
	}
	var rest []*agentMemExportItem
	for i := range pv.Items {
		it := &pv.Items[i]
		if seen[it.Name] || it.native == nil || it.native.bad != "" {
			continue
		}
		if it.Status == claudeExportNativeOnly || it.Status == claudeExportConflict {
			rest = append(rest, it)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].native.mtime.After(rest[j].native.mtime) })
	for _, it := range rest {
		d := it.native.desc
		if len(agentMemScanText("description", d)) > 0 {
			d = ""
		}
		rows = append(rows, claudeExportIndexRow{it.Name, d})
	}
	text, listed = claudeExportBuildIndex(rows)
	return text, listed, len(rows)
}

// planIndex fills the preview's view of MEMORY.md (no override assumed).
func (pv *agentMemExportPreview) planIndex(mem *os.File) {
	var text string
	var listed, total int
	text, listed, total = pv.indexText(nil)
	pv.IndexListed, pv.IndexMore = listed, total-listed
	pv.Index = "new"
	if mem == nil {
		return
	}
	old := agentMemExportReadNative(mem, agentMemImportIndex)
	switch {
	case old.bad == "symlink" && agentMemExportExists(mem, agentMemImportIndex):
		pv.Index = "symlink"
	case old.bad == "":
		pv.indexOld = old.raw
		if string(old.raw) == text {
			pv.Index = "unchanged"
		} else {
			pv.Index = "rewrite"
		}
	}
}

// agentMemExportExists says whether a name is present in the directory, whatever it is.
func agentMemExportExists(mem *os.File, name string) bool {
	fd, err := syscall.Openat(int(mem.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err == nil {
		_ = syscall.Close(fd)
	}
	// ELOOP: the name is a symlink, which is still "there".
	return err == nil || err == syscall.ELOOP
}

// agentMemExportPreviewFor is the read-only preview.
func agentMemExportPreviewFor(projectID string) (*agentMemExportPreview, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	return agentMemExportBuild(projectID)
}

type agentMemExportReq struct {
	Project string `json:"project"`
	Token   string `json:"token"`
	// Overwrite names the conflicts the member chose to replace with AF's version.
	Overwrite []string `json:"overwrite,omitempty"`
}

type agentMemExportResult struct {
	Name   string `json:"name"`
	Result string `json:"result"` // written | updated | removed | skipped
	Reason string `json:"reason,omitempty"`
}

type agentMemExportApplied struct {
	Results []agentMemExportResult `json:"results"`
	// Snapshot is the rev of the snapshot taken before writing ("" when nothing had changed).
	Snapshot string `json:"snapshot,omitempty"`
	Index    string `json:"index"` // written | unchanged | skipped
}

// agentMemExportWriteAt writes name in the pinned directory by temp file and rename.
func agentMemExportWriteAt(mem *os.File, name string, data []byte) error {
	fd := int(mem.Fd())
	tmp := fmt.Sprintf(".tmp-af-%d-%d", os.Getpid(), time.Now().UnixNano())
	wfd, err := syscall.Openat(fd, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(wfd), "tmp")
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = syscall.Unlinkat(fd, tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = syscall.Unlinkat(fd, tmp)
		return err
	}
	if err := syscall.Renameat(fd, tmp, fd, name); err != nil {
		_ = syscall.Unlinkat(fd, tmp)
		return err
	}
	return nil
}

// agentMemExportApply writes the previewed project back. The caller does not need the switch on.
func agentMemExportApply(req agentMemExportReq, now time.Time) (agentMemExportApplied, error) {
	if len(req.Overwrite) > agentMemImportMaxFiles {
		return agentMemExportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "too many names in overwrite")
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	pv, err := agentMemExportBuild(req.Project)
	if err != nil {
		return agentMemExportApplied{}, err
	}
	if pv.Token != req.Token {
		return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"AF memory or claude's files changed since the preview; preview again")
	}
	over := map[string]bool{}
	for _, n := range req.Overwrite {
		if !agentMemNameRe.MatchString(n) && !claudeExportNativeNameRe.MatchString(n) {
			return agentMemExportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "overwrite names a file that is not valid")
		}
		over[n] = true
	}

	out := agentMemExportApplied{Results: []agentMemExportResult{}, Index: "unchanged"}
	type action struct {
		it   *agentMemExportItem
		kind string
	}
	var todo []action
	for i := range pv.Items {
		it := &pv.Items[i]
		switch it.Status {
		case claudeExportNew:
			todo = append(todo, action{it, "written"})
		case claudeExportUpdate:
			todo = append(todo, action{it, "updated"})
		case claudeExportRemove:
			todo = append(todo, action{it, "removed"})
		case claudeExportConflict:
			switch {
			case over[it.Name] && it.locked:
				out.Results = append(out.Results, agentMemExportResult{Name: it.Name, Result: "skipped", Reason: it.Reason})
			case over[it.Name] && it.entry != nil:
				todo = append(todo, action{it, "updated"})
			default:
				out.Results = append(out.Results, agentMemExportResult{Name: it.Name, Result: "skipped", Reason: "conflict"})
			}
		}
	}
	indexText, _, _ := pv.indexText(over)
	indexChange := pv.Index != "symlink" && (pv.Index == "new" || string(pv.indexOld) != indexText)
	if len(todo) == 0 && !indexChange {
		return out, nil
	}

	// Snapshot first: when it fails nothing has been written.
	snap, err := memorySnapshotLocked(memoryTriggerPreExport, now)
	if err != nil {
		return agentMemExportApplied{}, err
	}
	out.Snapshot = snap.Rev

	mem, err := agentMemExportOpenDir(pv.Slug, true)
	if err != nil {
		return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"claude's memory directory for this project cannot be opened without following a symlink")
	}
	defer mem.Close()
	for _, a := range todo {
		res := agentMemExportResult{Name: a.it.Name, Result: a.kind}
		var werr error
		if a.kind == "removed" {
			werr = syscall.Unlinkat(int(mem.Fd()), a.it.Name+".md")
		} else {
			werr = agentMemExportWriteAt(mem, a.it.Name+".md", a.it.data)
		}
		if werr != nil {
			res.Result, res.Reason = "skipped", "write_failed"
			fmt.Fprintf(os.Stderr, "agent memory: export: %s\n", agentMemErrKind(werr))
		}
		out.Results = append(out.Results, res)
	}
	switch {
	case pv.Index == "symlink":
		out.Index = "skipped"
	case indexChange:
		if err := agentMemExportWriteAt(mem, agentMemImportIndex, []byte(indexText)); err != nil {
			out.Index = "skipped"
			fmt.Fprintf(os.Stderr, "agent memory: export: %s\n", agentMemErrKind(err))
		} else {
			out.Index = "written"
		}
	}
	return out, nil
}

// jsonQuote writes a string as a JSON string, which is a valid YAML double-quoted scalar.
func jsonQuote(s string) string { v, _ := json.Marshal(s); return string(v) }
