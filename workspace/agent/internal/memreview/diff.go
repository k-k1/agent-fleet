package memreview

// The diff of a proposal against the live memory, as the text a git diff of the memory's file would
// show, so the approval screen draws it like a published change. It is computed in memory from the
// two texts: a proposal's text is never written anywhere but the queue, so git (which needs files)
// is not used. A line diff over the rendered fields, not over the stored file, because the file
// carries a revision, dates and an author that a proposal does not change.

import (
	"fmt"
	"strings"
)

const (
	diffContext = 3
	// diffCellCap bounds the table of the line diff (old lines x new lines). A memory can be 200 KiB;
	// past the cap the diff degrades to "all of the old, then all of the new" instead of spending
	// memory on the screen.
	diffCellCap = 4_000_000
)

// proposalDiff is the diff of one proposal. cur is the live memory (nil when there is none).
func proposalDiff(p Proposal, cur *Current) string {
	path := "af/user/" + p.Op.Name + ".md"
	if p.ProjectID != "" {
		path = "af/projects/" + p.ProjectID + "/" + p.Op.Name + ".md"
	}
	var old, next []string
	hasOld, hasNew := false, p.Op.Op != OpForget
	if cur != nil && !cur.Withheld {
		old, hasOld = renderFields(cur.Type, cur.Description, cur.Body), true
	}
	if hasNew {
		next = renderFields(p.Op.Type, p.Op.Description, p.Op.Body)
	}
	return unifiedDiff(path, old, hasOld, next, hasNew)
}

// renderFields is the text a reader compares: the type and description, then the body.
func renderFields(typ, desc, body string) []string {
	out := []string{"type: " + typ, "description: " + desc, ""}
	return append(out, splitLines(body)...)
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

type diffLine struct {
	kind byte // ' ', '-', '+'
	text string
}

// unifiedDiff renders a git-style diff of one file. hasOld / hasNew false mean the file does not
// exist on that side (/dev/null).
func unifiedDiff(path string, old []string, hasOld bool, next []string, hasNew bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	switch {
	case !hasOld:
		b.WriteString("new file mode 100644\n--- /dev/null\n")
	default:
		fmt.Fprintf(&b, "--- a/%s\n", path)
	}
	if !hasNew {
		b.WriteString("deleted file mode 100644\n+++ /dev/null\n")
	} else {
		fmt.Fprintf(&b, "+++ b/%s\n", path)
	}
	lines := editScript(old, next)
	// Hunks: runs of changes with diffContext lines around them, joined when they touch.
	n := len(lines)
	oldNo, newNo := make([]int, n+1), make([]int, n+1) // 1-based numbers of the line at i
	o, w := 1, 1
	for i, l := range lines {
		oldNo[i], newNo[i] = o, w
		if l.kind != '+' {
			o++
		}
		if l.kind != '-' {
			w++
		}
	}
	oldNo[n], newNo[n] = o, w
	for i := 0; i < n; {
		if lines[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-diffContext, 0)
		end := i
		for j := i; j < n; j++ {
			if lines[j].kind != ' ' {
				end = j
				continue
			}
			if j-end > 2*diffContext {
				break
			}
		}
		stop := min(end+diffContext+1, n)
		oc, nc := 0, 0
		for _, l := range lines[start:stop] {
			if l.kind != '+' {
				oc++
			}
			if l.kind != '-' {
				nc++
			}
		}
		from, to := oldNo[start], newNo[start]
		if oc == 0 {
			from--
		}
		if nc == 0 {
			to--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", from, oc, to, nc)
		for _, l := range lines[start:stop] {
			b.WriteByte(l.kind)
			b.WriteString(l.text)
			b.WriteByte('\n')
		}
		i = stop
	}
	return b.String()
}

// editScript is the line diff of a and b: the longest common subsequence kept, the rest removed
// and added. Past diffCellCap it is every old line removed, then every new one added.
func editScript(a, b []string) []diffLine {
	n, m := len(a), len(b)
	var out []diffLine
	if int64(n+1)*int64(m+1) > diffCellCap {
		for _, s := range a {
			out = append(out, diffLine{'-', s})
		}
		for _, s := range b {
			out = append(out, diffLine{'+', s})
		}
		return out
	}
	// lcs[i][j] is the length of the common subsequence of a[i:] and b[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{'-', a[i]})
			i++
		default:
			out = append(out, diffLine{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{'+', b[j]})
	}
	return out
}
