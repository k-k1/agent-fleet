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
	"errors"
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

	"golang.org/x/sys/unix"

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
	// Only the blank lines around the body are ignored (the import and claude put one after the
	// frontmatter); leading spaces are content, a code block in Markdown.
	body = strings.TrimRight(strings.TrimLeft(body, "\r\n"), " \t\r\n")
	sum := sha256.Sum256([]byte(desc + "\x00" + typ + "\x00" + body))
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
	absent   bool   // there is no such name in the directory
}

func agentMemExportReadNative(mem *os.File, file string) agentMemExportNative {
	var n agentMemExportNative
	f, err := agentMemImportOpen(mem, file)
	if err != nil {
		if err == syscall.ENOENT {
			n.absent = true
			return n
		}
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
	indexOldFH  string // sha256 of the existing MEMORY.md, "" when there is none
	indexNew    string
	dirID       string
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
		if err != nil {
			continue
		}
		p := agentMemProjectInfo(d.Name())
		// A project whose last memory was forgotten still has files AF wrote, and removing them
		// is a write-back like any other.
		if len(es) == 0 && !agentMemExportHasOwnFiles(p) {
			continue
		}
		row := agentMemExportSourceRow{Project: p, Count: len(es)}
		if p.Root == "" {
			row.Reason = "no_root"
		}
		out.Projects = append(out.Projects, row)
	}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].Project.Display < out.Projects[j].Project.Display })
	return out, nil
}

// agentMemExportHasOwnFiles says whether claude's directory for p holds a file a write-back made.
func agentMemExportHasOwnFiles(p *agentMemProject) bool {
	if p.Root == "" {
		return false
	}
	slug := claude.ProjectKey(p.Root)
	if !claudeImportSlugRe.MatchString(slug) {
		return false
	}
	mem, err := agentMemExportOpenExisting(slug)
	if err != nil || mem == nil {
		return false
	}
	defer mem.Close()
	files, _ := agentMemImportFiles(mem)
	for _, f := range files {
		name := strings.TrimSuffix(f, ".md")
		if n := agentMemExportReadNative(mem, f); n.bad == "" && !n.absent && agentMemExportOwn(n.source, p.ID, name) {
			return true
		}
	}
	return false
}

// agentMemExportOpenDir opens <config dir>/projects/<slug>/memory from the filesystem root, every
// component with O_NOFOLLOW: a symlink anywhere on the path, the config dir and its ancestors
// included, is refused rather than followed. With create, a missing component of
// projects/<slug>/memory is made through the already verified parent. The handle that comes back
// is the one to keep using.
func agentMemExportOpenDir(slug string, create bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	cfg := filepath.Clean(claude.ConfigDir())
	if !filepath.IsAbs(cfg) {
		return nil, syscall.EINVAL
	}
	fd, err := syscall.Open("/", flags, 0)
	if err != nil {
		return nil, err
	}
	segs := append(strings.Split(strings.Trim(filepath.ToSlash(cfg), "/"), "/"), "projects", slug, "memory")
	firstNew := len(segs) - 3
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		next, err := syscall.Openat(fd, seg, flags, 0)
		if err == syscall.ENOENT && create && i >= firstNew-1 {
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

// agentMemExportDirID names a directory by device and inode, so a path that now leads to
// another directory is told from the one that was evaluated.
func agentMemExportDirID(d *os.File) string {
	var st syscall.Stat_t
	if d == nil || syscall.Fstat(int(d.Fd()), &st) != nil {
		return "none"
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

// agentMemExportOpenExisting opens the memory directory for evaluation: nil when it does not exist.
func agentMemExportOpenExisting(slug string) (*os.File, error) {
	mem, err := agentMemExportOpenDir(slug, false)
	if err == nil {
		return mem, nil
	}
	if err == syscall.ENOENT || os.IsNotExist(err) {
		return nil, nil
	}
	return nil, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
		"claude's memory directory for this project is not a plain directory path (a symlink?)")
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

// agentMemExportBuild evaluates one project against the pinned native directory mem (nil when it
// does not exist yet). Callers hold agentMemMu (read is enough).
func agentMemExportBuild(projectID, slug string, mem *os.File) (*agentMemExportPreview, error) {
	p := agentMemProjectInfo(projectID)
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

	pv.NativeOK = mem != nil
	pv.dirID = agentMemExportDirID(mem)
	var nativeFiles []string
	if mem != nil {
		nativeFiles, pv.Truncated = agentMemImportFiles(mem)
	}
	// The AF names are looked up one by one, so the cap on the listing below can never make an
	// existing file look absent.
	natives := map[string]*agentMemExportNative{}
	lookup := func(name string) *agentMemExportNative {
		if mem == nil {
			return nil
		}
		if n, ok := natives[name]; ok {
			return n
		}
		n := agentMemExportReadNative(mem, name+".md")
		if n.absent {
			natives[name] = nil
			return nil
		}
		natives[name] = &n
		return &n
	}
	for _, f := range nativeFiles {
		lookup(strings.TrimSuffix(f, ".md"))
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
		n := lookup(e.Name)
		it.native = n
		switch {
		case n == nil:
			it.Status = claudeExportNew
		case n.bad != "":
			it.Status, it.Reason, it.locked = claudeExportConflict, n.bad, true
		case n.parsed && n.hash == hash:
			it.Status = claudeExportUnchanged
			it.nativeFH = n.fileHash
		case n.parsed && agentMemExportOwn(n.source, projectID, e.Name) && n.afHash == n.hash &&
			e.Revision > agentMemExportRecordedRevision(n.source):
			it.Status = claudeExportUpdate
			it.nativeFH = n.fileHash
		default:
			it.Status, it.nativeFH = claudeExportConflict, n.fileHash
			it.Reason = "not_written_by_af"
			if agentMemExportOwn(n.source, projectID, e.Name) {
				it.Reason = "changed_since_write"
				if n.afHash == n.hash {
					// Untouched in claude, but AF is not newer: rolling it forward would be a revert.
					it.Reason = "af_not_newer"
				}
			}
		}
		if n != nil {
			it.Modified = n.mtime.UTC().Format(time.RFC3339)
		}
		pv.add(it)
	}

	names := make([]string, 0, len(natives))
	for n, v := range natives {
		if v != nil && !afNames[n] {
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
	fmt.Fprintf(h, "index|%s|%s|%x|%x\n", pv.Slug, pv.dirID, sha256.Sum256(pv.indexOld), sha256.Sum256([]byte(pv.indexNew)))
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
func (pv *agentMemExportPreview) indexText(over map[string]bool, failed map[string]*agentMemExportNative) (text string, listed, total int) {
	var ranked []agentMemEntry
	for i := range pv.Items {
		it := &pv.Items[i]
		if _, bad := failed[it.Name]; it.entry == nil || bad {
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
	// Natives that stay are listed as they are in the directory now: for a name whose write did
	// not happen that is a fresh reading, not the evaluation's.
	type kept struct {
		name, desc string
		mtime      time.Time
	}
	var rest []kept
	for i := range pv.Items {
		it := &pv.Items[i]
		if seen[it.Name] {
			continue
		}
		n := it.native
		cur, wasFailed := failed[it.Name]
		if wasFailed {
			n = cur
		} else if it.Status != claudeExportNativeOnly && it.Status != claudeExportConflict {
			continue
		}
		if n == nil || n.absent || n.bad != "" {
			continue
		}
		d := n.desc
		if len(agentMemScanText("description", d)) > 0 {
			d = ""
		}
		rest = append(rest, kept{it.Name, d, n.mtime})
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].mtime.After(rest[j].mtime) })
	for _, k := range rest {
		rows = append(rows, claudeExportIndexRow{k.name, k.desc})
	}
	text, listed = claudeExportBuildIndex(rows)
	return text, listed, len(rows)
}

// planIndex fills the preview's view of MEMORY.md (no override assumed).
func (pv *agentMemExportPreview) planIndex(mem *os.File) {
	text, listed, total := pv.indexText(nil, nil)
	pv.IndexListed, pv.IndexMore, pv.indexNew = listed, total-listed, text
	pv.Index = "new"
	if mem == nil {
		return
	}
	old := agentMemExportReadNative(mem, agentMemImportIndex)
	switch {
	case old.absent:
	case old.bad != "":
		pv.Index = "symlink" // not a plain file: left alone
	default:
		pv.indexOld, pv.indexOldFH = old.raw, old.fileHash
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
	_, slug, err := agentMemExportTarget(projectID)
	if err != nil {
		return nil, err
	}
	mem, err := agentMemExportOpenExisting(slug)
	if err != nil {
		return nil, err
	}
	if mem != nil {
		defer mem.Close()
	}
	return agentMemExportBuild(projectID, slug, mem)
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
	// Index is written | unchanged | skipped (left alone on purpose) | failed (it should have been
	// written and was not, or changed under the apply).
	Index string `json:"index"`
}

// agentMemExportTestHook is called at named points of an apply so a test can change the directory
// between the evaluation and the writes. It is nil outside tests.
var agentMemExportTestHook func(stage string)

func agentMemExportHook(stage string) {
	if agentMemExportTestHook != nil {
		agentMemExportTestHook(stage)
	}
}

// agentMemExportTempName is a fresh dot-name in the pinned directory.
func agentMemExportTempName() string {
	return fmt.Sprintf(".tmp-af-%d-%d", os.Getpid(), time.Now().UnixNano())
}

const (
	renameNoReplace = unix.RENAME_NOREPLACE
	renameExchange  = unix.RENAME_EXCHANGE
)

// errAgentMemExportRaced: the leaf was not what the evaluation saw when the commit reached it.
var errAgentMemExportRaced = errors.New("the file changed during the write-back")

// renameat2 renames within the pinned directory with flags. RENAME_NOREPLACE never overwrites;
// RENAME_EXCHANGE swaps two names atomically, which is how a racing file is kept, not replaced.
func agentMemExportRenameat2(mem *os.File, oldName, newName string, flags uint) error {
	fd := int(mem.Fd())
	return unix.Renameat2(fd, oldName, fd, newName, flags)
}

// agentMemExportStage writes data to a fresh temp name and returns it; nothing at a real name
// is visible until the commit.
func agentMemExportStage(mem *os.File, data []byte) (string, error) {
	fd := int(mem.Fd())
	tmp := agentMemExportTempName()
	wfd, err := syscall.Openat(fd, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(wfd), "tmp")
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = syscall.Unlinkat(fd, tmp)
		return "", err
	}
	return tmp, nil
}

// agentMemExportPublishAt makes name hold data. expectFH is the hash of the file that must be
// there ("" = nothing). The new text is complete under a temp name first. With nothing expected it
// is renamed in without replacing; otherwise it is swapped with the leaf, and the file that came
// out is checked: if it is not the expected one the swap is undone, so whatever raced in is put
// back instead of lost.
func agentMemExportPublishAt(mem *os.File, name string, data []byte, expectFH string) error {
	fd := int(mem.Fd())
	tmp, err := agentMemExportStage(mem, data)
	if err != nil {
		return err
	}
	agentMemExportHook("after-stage:" + name)
	if expectFH == "" {
		if err := agentMemExportRenameat2(mem, tmp, name, renameNoReplace); err != nil {
			_ = syscall.Unlinkat(fd, tmp)
			if err == syscall.EEXIST {
				return errAgentMemExportRaced
			}
			return err
		}
		return nil
	}
	if err := agentMemExportRenameat2(mem, tmp, name, renameExchange); err != nil {
		_ = syscall.Unlinkat(fd, tmp)
		if err == syscall.ENOENT {
			return errAgentMemExportRaced
		}
		return err
	}
	if agentMemExportLeafIs(mem, tmp, expectFH) {
		_ = syscall.Unlinkat(fd, tmp)
		return nil
	}
	if err := agentMemExportRenameat2(mem, tmp, name, renameExchange); err != nil {
		return err // the displaced file stays under its temp name rather than being deleted
	}
	_ = syscall.Unlinkat(fd, tmp)
	return errAgentMemExportRaced
}

// agentMemExportRemoveAt removes name when it is still the file with hash expectFH: it is moved
// aside first, checked, and put back if it turns out to be something else.
func agentMemExportRemoveAt(mem *os.File, name, expectFH string) error {
	fd := int(mem.Fd())
	q := agentMemExportTempName()
	agentMemExportHook("before-remove:" + name)
	if err := agentMemExportRenameat2(mem, name, q, renameNoReplace); err != nil {
		if err == syscall.ENOENT {
			return errAgentMemExportRaced
		}
		return err
	}
	if agentMemExportLeafIs(mem, q, expectFH) {
		return syscall.Unlinkat(fd, q)
	}
	if err := agentMemExportRenameat2(mem, q, name, renameNoReplace); err != nil {
		return err // something new is at the name; the moved file stays under its temp name
	}
	return errAgentMemExportRaced
}

// agentMemExportLeafIs says whether the leaf is still what the evaluation saw: absent when fh is
// "", else a regular file whose content hashes to fh.
func agentMemExportLeafIs(mem *os.File, file, fh string) bool {
	n := agentMemExportReadNative(mem, file)
	if fh == "" {
		return n.absent
	}
	return !n.absent && n.bad == "" && n.fileHash == fh
}

// agentMemExportApply writes the previewed project back. The caller does not need the switch on.
// The directory handle opened for the evaluation is the one written through, and every leaf is
// checked against what the evaluation saw immediately before it is touched.
func agentMemExportApply(req agentMemExportReq, now time.Time) (agentMemExportApplied, error) {
	if len(req.Overwrite) > agentMemImportMaxFiles {
		return agentMemExportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "too many names in overwrite")
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	_, slug, err := agentMemExportTarget(req.Project)
	if err != nil {
		return agentMemExportApplied{}, err
	}
	mem, err := agentMemExportOpenExisting(slug)
	if err != nil {
		return agentMemExportApplied{}, err
	}
	defer func() {
		if mem != nil {
			mem.Close()
		}
	}()
	pv, err := agentMemExportBuild(req.Project, slug, mem)
	if err != nil {
		return agentMemExportApplied{}, err
	}
	if pv.Token != req.Token {
		return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"AF memory or claude's files changed since the preview; preview again")
	}
	if pv.Truncated {
		return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"claude's memory directory has too many files to write back safely")
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
	indexText, _, _ := pv.indexText(over, nil)
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
	agentMemExportHook("after-snapshot")

	if mem == nil {
		// The directory did not exist at the evaluation; whatever is there now was not judged.
		m, err := agentMemExportOpenDir(slug, true)
		if err != nil {
			return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
				"claude's memory directory for this project cannot be opened without following a symlink")
		}
		mem = m
		if ents, err := mem.ReadDir(-1); err != nil || len(ents) != 0 {
			return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
				"claude's memory directory appeared during the apply; preview again")
		}
	} else if again, err := agentMemExportOpenDir(slug, false); err != nil || agentMemExportDirID(again) != agentMemExportDirID(mem) {
		if again != nil {
			again.Close()
		}
		return agentMemExportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"claude's memory directory changed during the apply; preview again")
	} else {
		again.Close()
	}

	agentMemExportHook("before-write")
	failed := map[string]*agentMemExportNative{}
	fail := func(name, file string) {
		cur := agentMemExportReadNative(mem, file)
		failed[name] = &cur
	}
	for _, a := range todo {
		res := agentMemExportResult{Name: a.it.Name, Result: a.kind}
		file := a.it.Name + ".md"
		var werr error
		switch {
		case !agentMemExportLeafIs(mem, file, a.it.nativeFH):
			res.Result, res.Reason = "skipped", "changed_since_preview"
			fail(a.it.Name, file)
		case a.kind == "removed":
			werr = agentMemExportRemoveAt(mem, file, a.it.nativeFH)
		default:
			werr = agentMemExportPublishAt(mem, file, a.it.data, a.it.nativeFH)
		}
		if werr == errAgentMemExportRaced {
			res.Result, res.Reason = "skipped", "changed_since_preview"
			fail(a.it.Name, file)
		} else if werr != nil {
			res.Result, res.Reason = "skipped", "write_failed"
			fail(a.it.Name, file)
			fmt.Fprintf(os.Stderr, "agent memory: export: %s\n", agentMemErrKind(werr))
		}
		out.Results = append(out.Results, res)
	}
	// MEMORY.md lists what is in the directory now, so a write that did not happen is not listed.
	if len(failed) > 0 {
		indexText, _, _ = pv.indexText(over, failed)
	}
	switch {
	case pv.Index == "symlink":
		out.Index = "skipped"
	case string(pv.indexOld) == indexText && pv.Index != "new":
		out.Index = "unchanged"
	case !agentMemExportLeafIs(mem, agentMemImportIndex, pv.indexOldFH):
		out.Index = "failed"
	default:
		if err := agentMemExportPublishAt(mem, agentMemImportIndex, []byte(indexText), pv.indexOldFH); err != nil {
			out.Index = "failed"
			fmt.Fprintf(os.Stderr, "agent memory: export: %s\n", agentMemErrKind(err))
		} else {
			out.Index = "written"
		}
	}
	return out, nil
}

// jsonQuote writes a string as a JSON string, which is a valid YAML double-quoted scalar.
func jsonQuote(s string) string { v, _ := json.Marshal(s); return string(v) }
