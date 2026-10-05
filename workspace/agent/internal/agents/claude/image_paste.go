package claude

import (
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
)

// readToolInstructions are the attachment lines an image-bearing claude prompt ends with
// ("<instruction> <paths>"): console/src/lib/pastedImages.ts FILE_PROMPT (also sent by the
// memo flush, control-plane/memo.go) and the older IMG_PROMPT wording.
var readToolInstructions = []string{
	"Open the following file(s) with the Read tool:",
	"Open the following image(s) with the Read tool:",
}

// imagePathRe matches the image extensions claude's paste handler treats as an image file.
var imagePathRe = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp)$`)

// attachmentDirRe matches the directories Agent Fleet saves attachments to: the session/chat
// paste uploads (sessionx/session_paste.go) and the memo images (memo_paste.go). Only those
// are quoted. A path the member typed stays bare, because the Console's echo keeps it bare
// and only skips the paths it recognises as its own uploads (pastedImages.ts PASTE_PATH_RE).
var attachmentDirRe = regexp.MustCompile(`/agent-fleet/(?:pasted/[^/]+|memo-images)/[^/]+$`)

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
//
// Whatever compares the typed text against the transcript or the pane (injection source
// tags, delivery confirmation) has to compare this form, not the prompt it was given.
func QuoteImagePaths(text string) string {
	// An image studio send carries its signal line after the attachment line
	// (composerSend.ts withStudioSignal); keep it aside, untouched.
	body := imagegen.StripStudioSignal(text)
	signal := text[len(body):]
	i := -1
	for _, instr := range readToolInstructions {
		if j := strings.LastIndex(body, instr); j >= 0 && j+len(instr) > i {
			i = j + len(instr)
		}
	}
	if i < 0 {
		return text
	}
	// Only a tail made of absolute paths alone is our attachment line. Anything else — the
	// member's own text, or a tail already quoted — is typed as it is, which also makes the
	// rewrite idempotent: the delivery check re-applies it to the prompt it holds.
	quote := func(tok string) bool { return imagePathRe.MatchString(tok) && attachmentDirRe.MatchString(tok) }
	fields := strings.Fields(body[i:])
	hasImage := false
	for _, f := range fields {
		if !strings.HasPrefix(f, "/") {
			return text
		}
		hasImage = hasImage || quote(f)
	}
	if !hasImage {
		return text
	}
	var b strings.Builder
	b.WriteString(body[:i])
	rest := body[i:]
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
		if quote(tok) {
			tok = "`" + tok + "`"
		}
		b.WriteString(tok)
		rest = rest[k:]
	}
	b.WriteString(signal)
	return b.String()
}
