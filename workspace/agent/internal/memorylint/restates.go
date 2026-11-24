package memorylint

// The redundancy check (ADR 0130 decision 11): a memory most of whose text a repository document
// it anchors already holds repeats the document, and the member can supersede it by that document.
// It is advice, like oversize: the memory is not doubted, only shown to be redundant.
//
// It is not part of Lint: it reads whole documents, so it runs only where a member asked for the
// report (Restates), never on the paths that lint-apply or promotion take.
//
// The comparison is textual and script-neutral: both texts are reduced to their letters and digits,
// lower-cased, and the memory's runs of restateShingle runes are looked up in the document. That
// reads Japanese (no spaces) and English alike, and ignores Markdown, wrapping and punctuation.

import (
	"fmt"
	"hash/fnv"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// restateShingle is the length of one compared run, in runes after normalisation: long enough
	// that a shared run is a shared phrase, not a shared word.
	restateShingle = 12
	// restateMinShingles is the least a memory must have to be compared: a one-liner shares most of
	// itself with any document on the subject.
	restateMinShingles = 30
	// RestateThreshold is the share of the memory's runs the document must hold, in percent.
	RestateThreshold = 80
	// restateMaxDocBytes bounds one document read; a larger one is not compared.
	restateMaxDocBytes = 1 << 20
	// restateMaxDocs bounds the documents one run reads.
	restateMaxDocs = 200
)

// FileSizer is implemented by a Repo that can tell a file's size without reading it; Restates
// asks it first, so a document over the bound is never loaded.
type FileSizer interface {
	FileSize(path string) (int64, error)
}

// rootDocRe finds a bare document name in a body ("See README.md"): the path rule needs a slash,
// so a top-level document named in prose would never be compared.
var rootDocRe = regexp.MustCompile(`[A-Za-z0-9_.-]+\.(?i:md|markdown|txt|rst|adoc)`)

// rootDocAt says whether the name at body[i:j] stands alone: written bare or as "./name", and not
// followed by more of a name ("README.md.bak", "README.mdx").
func rootDocAt(body string, i, j int) bool {
	pathByte := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
	}
	if j < len(body) && (pathByte(body[j]) || body[j] == '+' || body[j] == '@' || body[j] == '.' && j+1 < len(body) && pathByte(body[j+1])) {
		return false
	}
	if i == 0 {
		return true
	}
	if body[i-1] == '/' {
		// "./name": the dot must begin the token, so "x./name", "../name" and URLs stay out.
		return i >= 2 && body[i-2] == '.' && (i == 2 || !pathByte(body[i-3]) && strings.IndexByte("./~:$*{<%\\", body[i-3]) < 0)
	}
	// A name the regexp picked up again after a character it does not take ("Guide+README.md",
	// "x@README.md") is the tail of a longer one.
	return strings.IndexByte("~:$*{<%\\+@", body[i-1]) < 0
}

// restateDocExt are the file kinds that are documents. A memory naming a script or a source file
// is about it, not a copy of it.
var restateDocExt = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".rst": true, ".adoc": true}

// restateNorm keeps the letters and digits of s, lower-cased.
func restateNorm(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out = append(out, unicode.ToLower(r))
		}
	}
	return out
}

func restateHash(rs []rune) uint64 {
	h := fnv.New64a()
	var b []byte
	for _, r := range rs {
		b = utf8.AppendRune(b, r)
	}
	h.Write(b)
	return h.Sum64()
}

// restateShingles is the set of runs of a normalised text.
func restateShingles(rs []rune) map[uint64]bool {
	out := map[uint64]bool{}
	for i := 0; i+restateShingle <= len(rs); i++ {
		out[restateHash(rs[i:i+restateShingle])] = true
	}
	return out
}

// restateDocs lists the documents a memory anchors, in its body or its stored anchors, that the
// revision holds as files.
func restateDocs(m Memory, rc *repoCtx) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range append(extractPaths(m.Body, rc.topDirs), extractAnchorPaths(m.Anchors, rc.topDirs)...) {
		if !seen[a.text] && rc.files[a.text] && restateDocExt[strings.ToLower(path.Ext(a.text))] {
			seen[a.text] = true
			out = append(out, a.text)
		}
	}
	// A top-level document has no directory, so the path rule above never sees it: a bare name in
	// the body that is a file at the root, or a stored anchor naming one, is taken as it is.
	for _, loc := range rootDocRe.FindAllStringIndex(m.Body, -1) {
		if !rootDocAt(m.Body, loc[0], loc[1]) {
			continue // the tail of a longer path or a URL, or the head of a longer name
		}
		if a := m.Body[loc[0]:loc[1]]; !seen[a] && rc.files[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, a := range m.Anchors {
		a = strings.TrimPrefix(strings.TrimSpace(a), "./")
		if !seen[a] && rc.files[a] && restateDocExt[strings.ToLower(path.Ext(a))] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// Restates reports the memories most of whose text a document they anchor already holds
// (CheckRestatesDoc), with notes for what it could not read. It needs a Repo that reads files.
func Restates(mems []Memory, repo Repo) ([]Finding, []string) {
	fr, ok := repo.(FileReader)
	if !ok {
		return nil, nil
	}
	rc, _, err := loadRepo(repo, "")
	if err != nil {
		return nil, []string{fmt.Sprintf("redundancy check: %v", err)}
	}
	sz, _ := repo.(FileSizer)
	return restateFindings(mems, rc, fr, sz)
}

// restateFindings runs the check over mems. Each document is read once and scanned once, for the
// runs of every memory that anchors it.
func restateFindings(mems []Memory, rc *repoCtx, fr FileReader, sz FileSizer) ([]Finding, []string) {
	type use struct {
		memory string
		sh     map[uint64]bool
	}
	byDoc := map[string][]use{}
	for _, m := range mems {
		docs := restateDocs(m, rc)
		if len(docs) == 0 {
			continue
		}
		sh := restateShingles(restateNorm(m.Body))
		if len(sh) < restateMinShingles {
			continue
		}
		for _, d := range docs {
			byDoc[d] = append(byDoc[d], use{m.Name, sh})
		}
	}
	docs := make([]string, 0, len(byDoc))
	for d := range byDoc {
		docs = append(docs, d)
	}
	sort.Strings(docs)
	var notes []string
	if len(docs) > restateMaxDocs {
		notes = append(notes, fmt.Sprintf("redundancy check: %d documents are anchored, only the first %d were read", len(docs), restateMaxDocs))
		docs = docs[:restateMaxDocs]
	}
	var out []Finding
	unread := 0
	for _, d := range docs {
		if sz != nil {
			if n, err := sz.FileSize(d); err != nil || n > restateMaxDocBytes {
				unread++
				continue
			}
		}
		b, err := fr.ReadFile(d)
		if err != nil || len(b) > restateMaxDocBytes {
			unread++
			continue
		}
		uses := byDoc[d]
		want := map[uint64]bool{}
		for _, u := range uses {
			for h := range u.sh {
				want[h] = true
			}
		}
		present := map[uint64]bool{}
		rs := restateNorm(string(b))
		for i := 0; i+restateShingle <= len(rs); i++ {
			if h := restateHash(rs[i : i+restateShingle]); want[h] {
				present[h] = true
			}
		}
		for _, u := range uses {
			hit := 0
			for h := range u.sh {
				if present[h] {
					hit++
				}
			}
			if hit*100 >= RestateThreshold*len(u.sh) {
				out = append(out, Finding{Memory: u.memory, Check: CheckRestatesDoc, Subject: d})
			}
		}
	}
	if unread > 0 {
		notes = append(notes, fmt.Sprintf("redundancy check: %d documents were not read (unreadable or over %d bytes)", unread, restateMaxDocBytes))
	}
	return out, notes
}
