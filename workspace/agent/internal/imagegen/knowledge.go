package imagegen

// The workspace's image knowledge (ADR 0100 decision 12, layer 2): one Markdown file per model
// and one per family under ~/imagegen-knowledge, each in four sections — a summary, settings,
// prompts and records. Home rather than under the browse root, because survival comes before
// visibility: a browse root under ~/repos would lose the knowledge on the next recreate. Outside
// the Files pane's denylist on purpose, so the member can open and edit it there.
//
// Only the records section is written through the Agent (add_image_knowledge), and only by
// appending. The others are edited by hand or with the agent's own Edit, and are never
// rewritten here — which is also why the summary's size limit is applied when it is READ.

import (
	"bytes"
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

func readKnowledge(scope, key string) (Knowledge, error) {
	path, err := knowledgeFile(scope, key)
	if err != nil {
		return Knowledge{}, err
	}
	k := Knowledge{Scope: scope, Key: strings.TrimSpace(key), Path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return k, err
	}
	s := parseKnowledge(string(b))
	k.Summary, k.Settings, k.Prompts, k.Records = s[0], s[1], s[2], s[3]
	if len(k.Summary) > knowledgeSummaryMax {
		cut := knowledgeSummaryMax
		for cut > 0 && !utf8.RuneStart(k.Summary[cut]) {
			cut--
		}
		k.Summary, k.SummaryTruncated = k.Summary[:cut], true
	}
	return k, nil
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
	out := insertKnowledgeRecord(src, line)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Knowledge{}, err
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
		return Knowledge{}, err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return Knowledge{}, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return Knowledge{}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return Knowledge{}, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		os.Remove(tmp.Name())
		return Knowledge{}, err
	}
	return readKnowledge(in.Scope, in.Key)
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

// HandleKnowledge answers GET /imagegen/knowledge?scope=&key= (one document, by section) and
// POST (KnowledgeAdd: append to the records section — the only write this route makes).
func HandleKnowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		k, err := readKnowledge(r.URL.Query().Get("scope"), r.URL.Query().Get("key"))
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
