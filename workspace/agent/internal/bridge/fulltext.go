package bridge

// Full-text bridge (docs/log/37, the future direction): the answer-ready push can carry the final
// assistant turn body so the chat is a self-sufficient remote UI when the
// Console deep link is dead (local-only, externally-unreachable deployment).
// Two concerns live here and are provider-independent: secret scrubbing (the
// body is the agent's own prose but may echo a key) and chunking to Discord's
// per-message limit. Rendering is opt-in per connection (DiscordCreds.FullText).

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secretpat"
)

// discordContentLimit is a conservative cap below Discord's real 2000-char
// message limit — the headroom absorbs the mention prefix (budgeted per chunk)
// and the overflow ellipsis without ever crossing 2000.
const discordContentLimit = 1990

// maxBodyChunks bounds how many messages one turn body may fan into, so a giant
// turn can't flood the thread. Overflow past this is dropped with an ellipsis
// line rather than posted (the full text is always in the Console). Sized so a
// full turn body (bridgeBodyCap runes, session_status.go) splits cleanly across
// enough messages that a normal long answer is delivered WHOLE (docs/log/37 Fix 3 —
// "split it properly"), not truncated to the first few chunks.
const maxBodyChunks = 12

const redactedMark = "[secret redacted]"

// RedactedMark is what every redaction leaves behind, for scrubbers outside this package.
const RedactedMark = redactedMark

// bridgeDivider is a thin horizontal rule appended to the end of a full-text answer
// and a mirrored user input (docs/log/37 Fix ⑤), so consecutive posts / a run of replies
// don't visually merge into one unreadable block. A run of U+2500 renders as a line
// in Discord (which does not honor Markdown "---").
const bridgeDivider = "────────────────────"

// withDivider appends the separator line to a block of text.
func withDivider(s string) string {
	return strings.TrimRight(s, "\n") + "\n" + bridgeDivider
}

// renderBodyForDiscord prepares an assistant/user body for a Discord message: scrub
// secrets, then reflow Markdown tables (which Discord does NOT render) into fenced
// code blocks so their columns stay aligned in monospace (docs/log/37 Fix ④).
func renderBodyForDiscord(body string) string {
	return tablesToCodeBlocks(ScrubSecrets(body))
}

// renderBodyForSlack prepares a body for a Slack message: scrub secrets, reflow tables into
// code blocks, then a light GFM→mrkdwn pass (Slack uses *bold* / has no # headings), so an
// agent's GFM output reads sensibly. Best-effort, like Discord's rendering — a rare bold
// span inside a code fence may be touched; leaking a key matters more than perfect markdown.
func renderBodyForSlack(body string) string {
	return mrkdwnFromGFM(tablesToCodeBlocks(ScrubSecrets(body)))
}

var (
	// gfmHeadingRe matches an ATX heading line (## Title); Slack has no headings, so it
	// becomes a bold line.
	gfmHeadingRe = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.+?)\s*$`)
	// gfmBoldRe matches **bold** (Slack bold is a single *…*).
	gfmBoldRe = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
)

// mrkdwnFromGFM applies the minimal GFM→Slack-mrkdwn fixups that make the biggest legibility
// difference: ATX headings → bold lines, **bold** → *bold*. Everything else (lists, inline
// code, code fences, links) already renders close enough in Slack mrkdwn.
func mrkdwnFromGFM(s string) string {
	s = gfmHeadingRe.ReplaceAllString(s, "*$1*")
	s = gfmBoldRe.ReplaceAllString(s, "*$1*")
	return s
}

// knownSecretPatterns are high-precision provider token shapes — matched
// verbatim regardless of entropy so short, structured credentials still go. ScrubSecrets adds
// secretpat.GitHubStatelessSpans (a stateless GitHub token, ghs_APPID_JWT) and replaces all
// the spans together, so no rule's replacement cuts another's match short.
var knownSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),                                             // Slack tokens
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),                                           // GitHub tokens, classic
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),                                            // AWS access key id
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}`),                                                 // OpenAI-style secret keys
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-]{10,}=*`),                                 // Authorization: Bearer …
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`),            // JWTs
	regexp.MustCompile(`(?is)-----BEGIN[A-Z ]*PRIVATE KEY-----.*?-----END[A-Z ]*PRIVATE KEY-----`), // PEM private keys
}

// envAssignRe catches env/config assignments whose NAME is an UPPERCASE secret
// word (KEY/TOKEN/SECRET/…). The name is case-sensitive on purpose: uppercase
// keeps prose like "the api key: see docs" out of scope while catching
// `AWS_SECRET_ACCESS_KEY=…` / `PASSWORD=hunter2` regardless of value entropy.
var envAssignRe = regexp.MustCompile(`\b([A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|PASS|PWD|CREDENTIAL)[A-Z0-9_]*)(\s*[=:]\s*)(['"]?)([^\s'"]{6,})(['"]?)`)

// isTokenByte reports whether c can be part of an opaque token candidate for the high-entropy
// fallback scan: [A-Za-z0-9+/=_.-].
func isTokenByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("+/=_.-", c) >= 0
}

// ScrubSecrets removes credentials from body text before it reaches a chat wire.
// It is defense-in-depth over the primary guarantee (own both ends + opt-in,
// docs/log/37 §security consistency): known provider token shapes, UPPERCASE env
// assignments, and a high-entropy standalone-token fallback. Best-effort — it
// can over-redact a rare high-entropy prose token, which the user accepted as
// the safer failure than leaking a key.
//
// The passes agree on spans, all in the raw input's offsets, and every span is replaced in one
// go. The assignment pass runs on the text with each known token masked, as it always has,
// and never on the raw input: the mark's brackets are what put a word boundary before a name
// glued after a token (`<token>_PASSWORD: x`), and a raw match can start inside a token and run
// on past it. Its spans are mapped back to the raw input. Two spans then reach past the mark:
// a value the mark's space cut short runs on over the rest of the raw value
// (`API_TOKEN=<token>:private`), and a known token takes the run of token characters glued
// after it (`<token>+private`). Both stop at the next assignment's name and short of a final
// full stop. The entropy scan sees only the text between spans, as when marks sat in their
// place: run on the raw input it would take `NAME=value` and a sentence's final full stop.
// Without a known token the output is the same as running the passes in sequence.
func ScrubSecrets(s string) string {
	known := sortedMerged(append(secretpat.Spans(s, knownSecretPatterns...), secretpat.GitHubStatelessSpans(s)...))
	spans, names := assignmentSpans(s, known)
	spans = append(spans, gluedTails(s, known, names)...)
	return replaceSpansScanning(s, append(spans, known...))
}

// sortedMerged sorts spans and merges those that overlap or touch, dropping empty ones, as
// secretpat.ReplaceSpans does before it places one mark per merged span.
func sortedMerged(spans [][2]int) [][2]int {
	sort.Slice(spans, func(a, b int) bool { return spans[a][0] < spans[b][0] })
	var out [][2]int
	for _, sp := range spans {
		if sp[1] <= sp[0] {
			continue
		}
		if n := len(out); n > 0 && sp[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], sp[1])
			continue
		}
		out = append(out, sp)
	}
	return out
}

// assignmentSpans finds the UPPERCASE assignments in s with each known span (sorted, merged)
// masked, and returns each value's span (quotes included, the name kept) and each name's start,
// both ascending and in s's offsets.
func assignmentSpans(s string, known [][2]int) (spans [][2]int, names []int) {
	masked, markAt := s, []int(nil)
	if len(known) > 0 {
		var b strings.Builder
		b.Grow(len(s))
		last := 0
		for _, k := range known {
			b.WriteString(s[last:k[0]])
			markAt = append(markAt, b.Len())
			b.WriteString(redactedMark)
			last = k[1]
		}
		b.WriteString(s[last:])
		masked = b.String()
	}
	// toRaw maps an offset of masked to s; inside is the mark the offset falls strictly within,
	// whose start in s it then returns, or -1.
	toRaw := func(x int) (raw, inside int) {
		i := sort.Search(len(markAt), func(i int) bool { return markAt[i] >= x }) - 1
		if i < 0 {
			return x, -1
		}
		if end := markAt[i] + len(redactedMark); x >= end {
			return x - end + known[i][1], -1
		}
		return known[i][0], i
	}
	type cut struct {
		span   int
		quoted bool
	}
	var cuts []cut
	// One match at a time, not FindAll: a dense body would hold every match's submatch slice.
	// Searching the slice after a match finds what the regexp's own resume would: \b at the
	// slice's start is judged without the byte before it, but a match ends at whitespace, at the
	// end, or on a quote, and none of these lets a word run on across the cut.
	for p := 0; p < len(masked); {
		m := envAssignRe.FindStringSubmatchIndex(masked[p:])
		if m == nil {
			break
		}
		name, _ := toRaw(p + m[2])
		start, _ := toRaw(p + m[6])
		end, in := toRaw(p + m[11])
		if in >= 0 {
			end = known[in][1]
			cuts = append(cuts, cut{len(spans), m[7] > m[6]})
		}
		names = append(names, name)
		spans = append(spans, [2]int{start, end})
		p += m[1]
	}
	for _, c := range cuts {
		from := spans[c.span][1]
		spans[c.span][1] = valueEnd(s, from, nextAt(names, from, len(s)), c.quoted)
	}
	return spans, names
}

// valueEnd returns where an assignment value that a mark cut short at from ends in s: at
// whitespace, at a quote (taken with it when the value opened with one), or at limit, and short
// of a final full stop when unquoted.
func valueEnd(s string, from, limit int, quoted bool) int {
	j := from
	for j < limit && !isSpaceOrQuote(s[j]) {
		j++
	}
	if quoted && j < limit && (s[j] == '\'' || s[j] == '"') {
		return j + 1
	}
	for j > from && s[j-1] == '.' {
		j--
	}
	return j
}

// gluedTails returns, for each known span (sorted, merged), the run of token characters glued
// after it, up to the next assignment name and short of a final full stop. Known spans in one
// run share its scan, so a long run of joined tokens costs one pass, not one per token.
func gluedTails(s string, known [][2]int, names []int) [][2]int {
	var tails [][2]int
	runEnd, trimEnd, trimAt := -1, -1, 0
	for _, k := range known {
		from := k[1]
		if from >= runEnd {
			for runEnd = from; runEnd < len(s) && isTokenByte(s[runEnd]); runEnd++ {
			}
		}
		end := min(runEnd, nextAt(names, from, len(s)))
		if end != trimEnd {
			trimEnd, trimAt = end, end
			for trimAt > from && s[trimAt-1] == '.' {
				trimAt--
			}
		}
		if at := max(trimAt, from); at > from {
			tails = append(tails, [2]int{from, at})
		}
	}
	return tails
}

// nextAt returns the first of the ascending offsets at or after x, or def.
func nextAt(offsets []int, x, def int) int {
	if i := sort.SearchInts(offsets, x); i < len(offsets) {
		return offsets[i]
	}
	return def
}

// replaceSpansScanning replaces each span of s with the mark, merging overlapping or touching
// spans first, and each high-entropy run of token characters between them. A span ends a run
// as a mark's brackets would.
func replaceSpansScanning(s string, spans [][2]int) string {
	sort.Slice(spans, func(a, b int) bool { return spans[a][0] < spans[b][0] })
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for i := 0; i < len(spans); {
		start, end := spans[i][0], spans[i][1]
		for i++; i < len(spans) && spans[i][0] <= end; i++ {
			end = max(end, spans[i][1])
		}
		if end <= start {
			continue
		}
		writeScanned(&b, s[last:start])
		b.WriteString(redactedMark)
		last = end
	}
	writeScanned(&b, s[last:])
	return b.String()
}

// writeScanned writes g with each run of token characters that looks secret replaced by the mark.
func writeScanned(b *strings.Builder, g string) {
	for i := 0; i < len(g); {
		j := i
		tok := isTokenByte(g[i])
		for j < len(g) && isTokenByte(g[j]) == tok {
			j++
		}
		if tok && looksSecret(g[i:j]) {
			b.WriteString(redactedMark)
		} else {
			b.WriteString(g[i:j])
		}
		i = j
	}
}

// isSpaceOrQuote reports what ends an assignment value: regexp's \s, or a quote.
func isSpaceOrQuote(c byte) bool {
	return strings.IndexByte(" \t\n\f\r'\"", c) >= 0
}

// looksSecret is the entropy heuristic: a long, opaque, mixed alphanumeric token
// with high Shannon entropy. The alpha+digit requirement keeps long normal words
// (and pure-hex-less prose) out; the entropy floor keeps repetitive strings out.
func looksSecret(s string) bool {
	if len(s) < 20 {
		return false
	}
	hasDigit, hasAlpha := false, false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			hasAlpha = true
		}
	}
	if !hasDigit || !hasAlpha {
		return false
	}
	return shannonEntropy(s) >= 3.5
}

// shannonEntropy returns the per-character Shannon entropy (bits) of s.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	n := 0
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
		n++
	}
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// tableSepRe matches a GFM table's separator row (the "---|:--:|---" line under the
// header). It must contain a pipe so a bare "---" horizontal rule isn't mistaken for
// one (the caller also requires the preceding header line to contain a pipe).
var tableSepRe = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)

// tablesToCodeBlocks wraps each Markdown table (which Discord does NOT render) in a
// fenced code block, so its pipes line up in monospace instead of collapsing into an
// unreadable run of text (docs/log/37 Fix ④). A table is a header line containing a pipe
// immediately followed by a separator row; the block extends over the following
// pipe-bearing rows. Everything else is passed through untouched.
func tablesToCodeBlocks(s string) string {
	if !strings.Contains(s, "|") {
		return s // fast path: no tables possible
	}
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if i+1 < len(lines) && strings.Contains(lines[i], "|") &&
			strings.Contains(lines[i+1], "|") && tableSepRe.MatchString(lines[i+1]) {
			j := i + 2
			for j < len(lines) && strings.Contains(lines[j], "|") && strings.TrimSpace(lines[j]) != "" {
				j++
			}
			out = append(out, "```")
			out = append(out, lines[i:j]...)
			out = append(out, "```")
			i = j - 1
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// chunkMessage splits content into Discord-sized pieces (chunkTo with Discord's
// per-message limit). Kept as the Discord entry point; Slack calls chunkTo with
// its own larger limit (docs/log/37 Slack follow-up).
func chunkMessage(content, firstPrefix string) []string {
	return chunkTo(content, firstPrefix, discordContentLimit)
}

// chunkTo splits content into pieces no larger than limit runes. firstPrefix (the
// mention) is prepended to the first chunk and counts against its budget so the
// pinged chunk still fits. Splits prefer a line then a word boundary, falling
// back to a hard rune cut; chunk count is bounded (maxBodyChunks) with an
// ellipsis marking any dropped overflow. Provider-independent — the only
// per-provider knob is the character limit.
func chunkTo(content, firstPrefix string, limit int) []string {
	remaining := []rune(strings.TrimRight(content, "\n"))
	var chunks []string
	for len(remaining) > 0 {
		budget := limit
		if len(chunks) == 0 {
			budget -= len([]rune(firstPrefix))
		}
		if budget < 1 {
			budget = 1
		}
		if len(remaining) <= budget {
			chunks = append(chunks, string(remaining))
			remaining = nil
			break
		}
		cut := splitPoint(remaining, budget)
		chunks = append(chunks, strings.TrimRight(string(remaining[:cut]), " \n"))
		for cut < len(remaining) && (remaining[cut] == '\n' || remaining[cut] == ' ') {
			cut++
		}
		remaining = remaining[cut:]
		if len(chunks) == maxBodyChunks && len(remaining) > 0 {
			chunks[len(chunks)-1] += "\n…"
			remaining = nil
			break
		}
	}
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	chunks[0] = firstPrefix + chunks[0]
	return chunks
}

// splitPoint picks where to cut remaining within budget: the last line break,
// else the last space, preferring a boundary in the back half so chunks stay
// substantial; a hard cut at budget when neither is found.
func splitPoint(r []rune, budget int) int {
	if i := lastRuneIn(r, budget, '\n'); i > budget/2 {
		return i
	}
	if i := lastRuneIn(r, budget, ' '); i > budget/2 {
		return i
	}
	return budget
}

func lastRuneIn(r []rune, limit int, ch rune) int {
	for i := limit - 1; i >= 0; i-- {
		if r[i] == ch {
			return i
		}
	}
	return -1
}
