package transcript

// Vocabulary and helpers shared by the "files this session changed" list (docs/log/68).
//
// The population comes from the transcript — the record that an agent called an edit
// tool — while the git working-tree state is joined on the Console side keyed by
// `(Repo, Rel)`. The transcript alone keeps reverted edits around, git alone loses the
// per-session axis, so both are layered (decisions/0049 decision 1).
//
// Aggregate over the WHOLE transcript. The mirror only holds a tail window of it, so
// counting on the client falls short on long sessions and the count grows every time the
// user scrolls up.

// FileEdit is ONE edit-family tool call as it sits in the transcript: the target the
// agent wrote (which may be relative — Cwd is how it gets anchored), and the +/- that
// call produced. The aggregation into per-file rows happens in the Agent's handler,
// which is the only place that knows the browse root and the working-copy layout.
type FileEdit struct {
	Path      string // target as the agent wrote it; relative paths resolve against Cwd
	Cwd       string // the turn's working dir ("" = unknown)
	Verb      string // "edit" | "add" | "delete"
	Added     int    // lines added by this call (EditStat)
	Removed   int    // lines removed by this call
	Idx       int    // transcript index of the line/turn this call sits in
	TS        string // RFC3339 of that line/turn ("" if the transcript has none)
	Sidechain bool   // the call happened inside a subagent sidechain
}

// FileTouch is one FILE, folded over the whole session (docs/log/68 §68.8.1). JSON tags are
// paired with the Console's type — Path is what FileView opens, and (Repo, Rel) is the
// join key against GET /fs/changes.
//
// Repo/Rel exist precisely so the join does NOT go through Path: browse-relative
// paths are rooted at browseRoot() (overridable with AF_BROWSE_ROOT) while /fs/changes
// always reports "repos/<repo>/<rel>". They agree by default, so a mismatch would only
// ever show up on a deployment that moved the browse root.
type FileTouch struct {
	Path      string `json:"path"`                // browse-root relative (FileView / fs API)
	Repo      string `json:"repo,omitempty"`      // working-copy folder name; "" = outside ~/repos
	Rel       string `json:"rel,omitempty"`       // repo-relative; "" = outside ~/repos
	Verb      string `json:"verb"`                // "edit" | "add" | "delete" (last call wins)
	Added     int    `json:"added,omitempty"`     // summed over the session; 0 = the kind has no before/after
	Removed   int    `json:"removed,omitempty"`   //
	Count     int    `json:"count"`               // how many edit calls touched this file
	LastIdx   int    `json:"lastIdx"`             // transcript index of the newest call
	LastTS    string `json:"lastTs,omitempty"`    // RFC3339 of the newest call
	Sidechain bool   `json:"sidechain,omitempty"` // ONLY subagents touched it
}

// EditVerb decides how to label an edit-family part. `explicit` is the parser's own
// verdict when it has one (codex reads it straight out of the patch header); otherwise
// it is derived from the captured before/after.
//
// The absence of before/after must NOT be read as "delete". codex is the only parser
// that omits Edits deliberately (its delete branch), and it says so through `explicit`.
// A kind that merely carries no diff bodies (cursor / copilot, docs/log/68 §68.2.1) would
// otherwise have every file it touched labelled "delete".
func EditVerb(explicit string, edits []Edit) string {
	switch explicit {
	case "add", "delete", "edit":
		return explicit
	case "update":
		return "edit" // codex's patch header wording
	}
	if len(edits) == 0 {
		return "edit"
	}
	for _, e := range edits {
		if e.Old != "" {
			return "edit"
		}
	}
	return "add" // every hunk is pure insertion — a Write / Add File
}

// EditStat counts the added/removed lines of a set of before/after blocks the SAME way
// the Console's line differ does (console/src/features/viewer/DiffView.tsx `lineDiff`:
// LCS, plus the identical size guard), so the strip's +N −M can never disagree with the
// diff the row opens.
//
// It runs over the already-capped (CapEdit) bodies, which is what the Console receives —
// counting the full text here would report changes the reader cannot see.
func EditStat(edits []Edit) (added, removed int) {
	for _, e := range edits {
		a, r := editStat1(e.Old, e.New)
		added += a
		removed += r
	}
	return added, removed
}

func editStat1(oldStr, newStr string) (added, removed int) {
	a := splitDiffLines(oldStr)
	b := splitDiffLines(newStr)
	n, m := len(a), len(b)
	// Same guard as lineDiff: an empty side, or a table too large to be worth building,
	// degrades to remove-everything-then-add-everything.
	if n == 0 || m == 0 || n*m > 4_000_000 {
		return m, n
	}
	lcs := lcsLen(a, b)
	return m - lcs, n - lcs
}

// splitDiffLines mirrors lineDiff's splitting: "" is no lines at all, and one trailing
// newline is not a line of its own.
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	if s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// lcsLen is the longest-common-subsequence length over lines, with a rolling row (the
// Console builds the full table because it also needs the path; here only the length
// matters, and this runs on every poll).
func lcsLen(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				cur[j] = prev[j+1] + 1
			} else if prev[j] >= cur[j+1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j+1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[0]
}

// FileEditsInTurn pulls every edit-family part out of one already-parsed turn. This is
// the path the store-backed agents take (codex / opencode): their transcripts are
// re-parsed whole on each poll, so there is nothing cheaper to read than the turns.
func FileEditsInTurn(t Turn) []FileEdit {
	var out []FileEdit
	for _, p := range t.Parts {
		if p.Kind != "tool" || p.File == "" {
			continue
		}
		added, removed := EditStat(p.Edits)
		out = append(out, FileEdit{
			Path: p.File, Cwd: t.Cwd, Verb: EditVerb(p.Verb, p.Edits),
			Added: added, Removed: removed,
			Idx: t.Idx, TS: t.TS, Sidechain: t.Sidechain,
		})
	}
	return out
}
