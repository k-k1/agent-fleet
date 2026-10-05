package claude

import (
	"regexp"
	"strings"
)

// readToolInstruction ends the attachment line every image-bearing claude prompt carries
// ("Open the following file(s) with the Read tool: <paths>", console/src/lib/pastedImages.ts
// FILE_PROMPT and control-plane/memo.go). Only paths after it are rewritten: a path inside
// the member's own words would stop matching the Console's optimistic echo.
const readToolInstruction = "with the Read tool:"

// imagePathRe matches the image extensions claude's paste handler treats as an image file.
var imagePathRe = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp)$`)

// QuoteImagePaths wraps the absolute image paths of the attachment line in backticks before
// the prompt is typed into claude's composer.
//
// claude treats a single input chunk longer than 800 characters as a paste, splits it at
// every " /" and newline, and turns each piece that is an existing image file into an
// [Image #N] attachment, re-joining the remaining pieces with newlines (measured in the
// 2.1.289 bundle and reproduced through tmux send-keys). For a long prompt with an attached
// image that drops the path from the text, prepends "[Image #N]", and breaks every " /" in
// the member's words onto a new line — so the Console's echo never reconciles (it stays
// "Pending") and the mirror loses the thumbnail. A path wrapped in backticks is never a
// piece of its own, so the prompt is typed verbatim; the Read tool reads the path all the
// same, and the mirror's paste-path match ignores the backticks.
func QuoteImagePaths(text string) string {
	i := strings.LastIndex(text, readToolInstruction)
	if i < 0 {
		return text
	}
	i += len(readToolInstruction)
	var b strings.Builder
	b.WriteString(text[:i])
	rest := text[i:]
	for rest != "" {
		// Copy whitespace verbatim, then handle one token.
		j := strings.IndexFunc(rest, func(r rune) bool { return r != ' ' && r != '\t' && r != '\n' && r != '\r' })
		if j < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:j])
		rest = rest[j:]
		k := strings.IndexAny(rest, " \t\n\r")
		if k < 0 {
			k = len(rest)
		}
		tok := rest[:k]
		if strings.HasPrefix(tok, "/") && imagePathRe.MatchString(tok) {
			tok = "`" + tok + "`"
		}
		b.WriteString(tok)
		rest = rest[k:]
	}
	return b.String()
}
