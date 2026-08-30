package bridge

// 全文ブリッジ (docs/log/37 将来の方向): the answer-ready push can carry the final
// assistant turn body so the chat is a self-sufficient remote UI when the
// Console deep link is dead (local-only, externally-unreachable deployment).
// Two concerns live here and are provider-independent: secret scrubbing (the
// body is the agent's own prose but may echo a key) and chunking to Discord's
// per-message limit. Rendering is opt-in per connection (DiscordCreds.FullText).

import (
	"math"
	"regexp"
	"strings"
)

// discordContentLimit is a conservative cap below Discord's real 2000-char
// message limit — the headroom absorbs the mention prefix (budgeted per chunk)
// and the overflow ellipsis without ever crossing 2000.
const discordContentLimit = 1990

// maxBodyChunks bounds how many messages one turn body may fan into, so a giant
// turn can't flood the thread. Overflow past this is dropped with an ellipsis
// line rather than posted (the full text is always in the Console). Sized so a
// full turn body (bridgeBodyCap runes, session_status.go) splits cleanly across
// enough messages that a normal long answer is delivered WHOLE (docs/log/37 Fix ③ —
// 「うまく分割」), not truncated to the first few chunks.
const maxBodyChunks = 12

const redactedMark = "[secret redacted]"

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
// verbatim regardless of entropy so short, structured credentials still go.
var knownSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),                                             // Slack tokens
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),                                           // GitHub tokens
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

// tokenRe finds opaque token candidates for the high-entropy fallback scan.
var tokenRe = regexp.MustCompile(`[A-Za-z0-9+/=_.\-]{20,}`)

// ScrubSecrets removes credentials from body text before it reaches a chat wire.
// It is defense-in-depth over the primary guarantee (own both ends + opt-in,
// docs/log/37「セキュリティ整合」): known provider token shapes, then UPPERCASE env
// assignments, then a high-entropy standalone-token fallback. Best-effort — it
// can over-redact a rare high-entropy prose token, which the user accepted as
// the safer failure than leaking a key.
func ScrubSecrets(s string) string {
	for _, re := range knownSecretPatterns {
		s = re.ReplaceAllString(s, redactedMark)
	}
	s = envAssignRe.ReplaceAllString(s, `$1$2`+redactedMark)
	s = tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		if looksSecret(tok) {
			return redactedMark
		}
		return tok
	})
	return s
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
// its own larger limit (docs/log/37 Slack 追随).
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
