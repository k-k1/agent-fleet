package imagegen

// The workspace's image knowledge (ADR 0100 decision 12, layer 2): one Markdown file per model
// and one per family under ~/imagegen-knowledge, each in four sections — a summary, settings,
// prompts and records. Home rather than under the browse root, because survival comes before
// visibility: a browse root under ~/repos would lose the knowledge on the next recreate. Outside
// the Files pane's denylist on purpose, so the member can open and edit it there.
//
// The agent writes the records section through the Agent (add_image_knowledge), and only by
// appending; the others are its own Edit's. The member edits the file in the Files pane — or,
// when the browse root is not home and the Files pane cannot reach it, through the pane's notes
// editor (PUT, all four sections at once, refused when the file changed since it was read). The
// summary's size limit is applied when it is READ, since neither writer is held to it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// knowledgeSummaryMax is the summary's read limit. The summary rides on every get_image_studio
// answer, which makes it a per-turn cost; past it the answer says it was cut rather than
// dropping the rest silently.
const knowledgeSummaryMax = 1024

// knowledgeNoteMax bounds one record (note and evidence together), in characters.
const knowledgeNoteMax = 2000

func knowledgeDir() string { return filepath.Join(paths.HomeDir(), "imagegen-knowledge") }

// knowledgeFile is where scope/key lives. The key is a model id or a family name, escaped into
// one path segment: ids may carry a slash (`org/model`) and must still be one file.
func knowledgeFile(scope, key string) (string, error) {
	key = strings.TrimSpace(key)
	sub := map[string]string{KnowledgeFamily: "families", KnowledgeModel: "models"}[scope]
	if sub == "" {
		return "", fmt.Errorf(`scope is %q or %q`, KnowledgeFamily, KnowledgeModel)
	}
	name := url.PathEscape(key)
	if key == "" || len(name) > 200 || strings.HasPrefix(name, ".") || strings.ContainsRune(key, 0) {
		return "", errors.New("key is a model id or a family name")
	}
	return filepath.Join(knowledgeDir(), sub, name+".md"), nil
}

// The section headings, in both languages the file may have been started in.
var knowledgeHeadings = [4][2]string{
	{"要約", "Summary"},
	{"設定", "Settings"},
	{"プロンプト", "Prompts"},
	{"記録", "Records"},
}

func knowledgeHeadingIndex(line string) int {
	h, ok := strings.CutPrefix(strings.TrimRight(line, " \t\r"), "## ")
	if !ok {
		return -1
	}
	h = strings.TrimSpace(h)
	for i, pair := range knowledgeHeadings {
		if strings.EqualFold(h, pair[0]) || strings.EqualFold(h, pair[1]) {
			return i
		}
	}
	return -1
}

// parseKnowledge splits a document into its four sections. Text before the first heading (the
// title) belongs to none; a heading that is not one of the four stays inside the section it is in.
func parseKnowledge(src string) [4]string {
	var out [4]string
	cur := -1
	for _, line := range strings.SplitAfter(src, "\n") {
		if i := knowledgeHeadingIndex(line); i >= 0 {
			cur = i
			continue
		}
		if cur >= 0 {
			out[cur] += line
		}
	}
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

func knowledgeTemplate(key string) string {
	col := 0
	if studioLocale() == "en" {
		col = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", key)
	for _, h := range knowledgeHeadings {
		fmt.Fprintf(&b, "\n## %s\n", h[col])
	}
	return b.String()
}

func readKnowledge(scope, key string) (Knowledge, error) { return readKnowledgeCut(scope, key, true) }

// readKnowledgeCut reads scope/key; cut applies the summary's read limit. Only the pane's editor
// reads it whole: an edit saved from a cut summary would delete the rest of it.
func readKnowledgeCut(scope, key string, cut bool) (Knowledge, error) {
	path, err := knowledgeFile(scope, key)
	if err != nil {
		return Knowledge{}, err
	}
	k := Knowledge{Scope: scope, Key: strings.TrimSpace(key), Path: path, FilesPath: knowledgeFilesPath(path)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return k, err
	}
	k.Version = knowledgeVersion(b)
	s := parseKnowledge(string(b))
	k.Summary, k.Settings, k.Prompts, k.Records = s[0], s[1], s[2], s[3]
	if cut && len(k.Summary) > knowledgeSummaryMax {
		at := knowledgeSummaryMax
		for at > 0 && !utf8.RuneStart(k.Summary[at]) {
			at--
		}
		k.Summary, k.SummaryTruncated = k.Summary[:at], true
	}
	return k, nil
}

// knowledgeVersion names a file's content for KnowledgeEdit's check. A hash rather than the
// mtime: the agent's Edit and the Files pane can both write within the same clock tick.
func knowledgeVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// knowledgeFilesPath is path as the Files pane names it, or "" when the pane cannot open it:
// outside the browse root, or under a denylisted folder of it.
func knowledgeFilesPath(path string) string {
	if BrowseRootDir == nil || PathDenied == nil {
		return ""
	}
	rel, err := filepath.Rel(filepath.Clean(BrowseRootDir()), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if PathDenied(rel) {
		return ""
	}
	return rel
}

var knowledgeMu sync.Mutex

// appendKnowledgeRecord adds one dated line to the records section, creating the file from the
// template when there is none. Everything outside that section is written back byte for byte.
func appendKnowledgeRecord(in KnowledgeAdd, now time.Time) (Knowledge, error) {
	path, err := knowledgeFile(in.Scope, in.Key)
	if err != nil {
		return Knowledge{}, err
	}
	note, evidence := strings.TrimSpace(in.Note), strings.TrimSpace(in.Evidence)
	if note == "" {
		return Knowledge{}, errors.New("note is empty")
	}
	if n := utf8.RuneCountInString(note) + utf8.RuneCountInString(evidence); n > knowledgeNoteMax {
		return Knowledge{}, fmt.Errorf("a record is at most %d characters (got %d); keep the finding and its evidence short", knowledgeNoteMax, n)
	}
	who := "user"
	if in.Session != "" {
		who = "agent " + in.Session
	}
	line := fmt.Sprintf("- %s · %s: %s", now.Format("2006-01-02"), who, note)
	if evidence != "" {
		label := "根拠"
		if studioLocale() == "en" {
			label = "evidence"
		}
		line += fmt.Sprintf(" (%s: %s)", label, evidence)
	}
	// One record is one list item: its own line breaks are indented into it.
	line = strings.ReplaceAll(line, "\n", "\n  ") + "\n"

	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		src = []byte(knowledgeTemplate(strings.TrimSpace(in.Key)))
	} else if err != nil {
		return Knowledge{}, err
	}
	if err := writeKnowledgeFile(path, insertKnowledgeRecord(src, line)); err != nil {
		return Knowledge{}, err
	}
	return readKnowledge(in.Scope, in.Key)
}

// errKnowledgeChanged refuses a KnowledgeEdit made over a version that is no longer the file's.
var errKnowledgeChanged = errors.New("the notes changed since they were read; read them again and reapply the edit")

// editKnowledge replaces the four sections of scope/key, when the file is still the version the
// editor read. The title and anything before the first section are kept, and each section keeps
// the heading spelling the file already uses.
func editKnowledge(in KnowledgeEdit) (Knowledge, error) {
	path, err := knowledgeFile(in.Scope, in.Key)
	if err != nil {
		return Knowledge{}, err
	}
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()
	src, err := os.ReadFile(path)
	cur := ""
	switch {
	case errors.Is(err, os.ErrNotExist):
		src = []byte(knowledgeTemplate(strings.TrimSpace(in.Key)))
	case err != nil:
		return Knowledge{}, err
	default:
		cur = knowledgeVersion(src)
	}
	if in.Version != cur {
		return Knowledge{}, errKnowledgeChanged
	}
	body := [4]string{in.Summary, in.Settings, in.Prompts, in.Records}
	if n := utf8.RuneCountInString(strings.Join(body[:], "")); n > knowledgeEditMax {
		return Knowledge{}, fmt.Errorf("the notes are at most %d characters (got %d)", knowledgeEditMax, n)
	}
	if err := writeKnowledgeFile(path, rebuildKnowledge(src, body)); err != nil {
		return Knowledge{}, err
	}
	return readKnowledge(in.Scope, in.Key)
}

// knowledgeEditMax bounds what the pane's editor may write: a document every studio turn reads
// the summary of, not a place for pasted logs.
const knowledgeEditMax = 64 * 1024

// rebuildKnowledge is src with its four sections replaced by body, in the fixed order. A section
// heading the file already has keeps its spelling; a missing one follows the file's language
// (English when any of its headings is), else the member's.
func rebuildKnowledge(src []byte, body [4]string) []byte {
	var found [4]string
	sawEN, sawJA := false, false
	var pre strings.Builder
	inPre := true
	for _, line := range strings.SplitAfter(string(src), "\n") {
		if i := knowledgeHeadingIndex(line); i >= 0 {
			inPre = false
			found[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimRight(line, " \t\r\n"), "## "))
			if strings.EqualFold(found[i], knowledgeHeadings[i][1]) {
				sawEN = true
			} else {
				sawJA = true
			}
			continue
		}
		if inPre {
			pre.WriteString(line)
		}
	}
	english := sawEN || (!sawJA && studioLocale() == "en")
	blocks := []string{}
	if t := strings.TrimRight(pre.String(), "\n"); t != "" {
		blocks = append(blocks, t)
	}
	for i, pair := range knowledgeHeadings {
		h := found[i]
		if h == "" {
			h = pair[0]
			if english {
				h = pair[1]
			}
		}
		blocks = append(blocks, "## "+h)
		if t := strings.TrimSpace(strings.ReplaceAll(body[i], "\r\n", "\n")); t != "" {
			blocks = append(blocks, t)
		}
	}
	return []byte(strings.Join(blocks, "\n\n") + "\n")
}

// writeKnowledgeFile replaces the file at path with out.
func writeKnowledgeFile(path string, out []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// The member owns this file: a symlink they made (to share one document between two keys,
	// or to keep it in a repository) is written THROUGH, and the file keeps its mode. A rename
	// onto the link's own name would replace the link with a copy.
	target, mode := path, os.FileMode(0o600)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
	}
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".knowledge-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// insertKnowledgeRecord puts line at the end of the records section: before the next of the
// four headings when records is not last, at the end of the file when it is, and under a new
// records heading when a hand-edited file has lost it.
func insertKnowledgeRecord(src []byte, line string) []byte {
	lines := bytes.SplitAfter(src, []byte("\n"))
	in, at := false, -1
	for i, l := range lines {
		idx := knowledgeHeadingIndex(string(l))
		if idx == 3 {
			in = true
			continue
		}
		if in && idx >= 0 {
			at = i
			break
		}
	}
	var b bytes.Buffer
	if !in {
		b.Write(bytes.TrimRight(src, "\n"))
		heading := knowledgeHeadings[3][0]
		if studioLocale() == "en" {
			heading = knowledgeHeadings[3][1]
		}
		fmt.Fprintf(&b, "\n\n## %s\n\n%s", heading, line)
		return b.Bytes()
	}
	if at < 0 {
		head := bytes.TrimRight(src, "\n")
		b.Write(head)
		b.WriteString("\n")
		if knowledgeHeadingIndex(lastLine(head)) == 3 {
			b.WriteString("\n")
		}
		b.WriteString(line)
		return b.Bytes()
	}
	// Before the next heading, keeping one blank line between the records and it.
	head := bytes.TrimRight(bytes.Join(lines[:at], nil), "\n")
	b.Write(head)
	b.WriteString("\n")
	if knowledgeHeadingIndex(lastLine(head)) == 3 {
		b.WriteString("\n")
	}
	b.WriteString(line)
	b.WriteString("\n")
	b.Write(bytes.Join(lines[at:], nil))
	return b.Bytes()
}

func lastLine(b []byte) string {
	b = bytes.TrimRight(b, "\n")
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return string(b[i+1:])
	}
	return string(b)
}

// HandleKnowledge answers GET /imagegen/knowledge?scope=&key=[&full=1] (one document, by section;
// full skips the summary's read limit, for the editor), POST
// (KnowledgeAdd: append to the records section) and PUT (KnowledgeEdit: the pane's editor).
func HandleKnowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var body KnowledgeEdit
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		k, err := editKnowledge(body)
		if errors.Is(err, errKnowledgeChanged) {
			httpx.WriteErr(w, http.StatusPreconditionFailed, "knowledge_changed", err.Error())
			return
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_knowledge", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, k)
		return
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		k, err := readKnowledgeCut(q.Get("scope"), q.Get("key"), q.Get("full") != "1")
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_knowledge", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, k)
		return
	}
	var body KnowledgeAdd
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	k, err := appendKnowledgeRecord(body, studioNow())
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_knowledge", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, k)
}
