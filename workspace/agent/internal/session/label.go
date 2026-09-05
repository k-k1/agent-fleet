package session

import (
	"regexp"
	"strings"
)

// The single definition of the SHAPE of a session's display label (the string passed to
// claude's `--name`), plus the code that reads it back. Building it happens in package
// main's sessionLabelFor and stripping it is spread across Display / CP / Console, so the
// shape itself lives in this one place.
//
// The shape is `[AF:<name>] <title>`. The `[AF]` tag marks a session Agent Fleet started
// so it can be told apart in claude.ai's Remote Control picker, and `:<name>` adds the
// SESSION NAME to it (docs/log/58 §58.16).
//
// The name was added because of misdelivery, and the damage was real. claude's own
// cross-session channel (ListAgents / SendMessage) addresses a target BY THIS LABEL
// STRING. An AF session name is not in that namespace, so `to:"s6bbilu"` never arrives;
// and with the label carrying only the title, two sessions with the same title were
// indistinguishable to the sender — a trial session's report went entirely to an older
// session with the same title (2026-08-31). With the name in the label they are visibly
// different already in the listing.
//
// The old `[AF] <title>` must still parse. The label is fixed at creation (and on title
// change) and baked into meta, so existing sessions keep the old shape. A strip that only
// understands the new shape leaves the tag visible on exactly the rows that predate the
// upgrade — a breakage that is hard to spot in review.
const labelTag = "[AF]"

// labelRe matches the leading tag, both the old `[AF] ` and the new `[AF:<name>] `. The
// name part is restricted to the same character set as ValidName, so a title that itself
// begins with `[AF:` is not misread as a name.
var labelRe = regexp.MustCompile(`^\[AF(?::([A-Za-z0-9][A-Za-z0-9_-]*))?\]\s*`)

// LabelPrefix returns the tag to put at the front of a new label (trailing space
// included). An invalid name falls back to the old `[AF] ` — the label serves display and
// the picker, and failing here to block a launch is not worth it.
func LabelPrefix(name string) string {
	if !ValidName(name) {
		return labelTag + " "
	}
	return "[AF:" + name + "] "
}

// StripLabel removes the tag for display. Without a tag the input is returned unchanged
// (some sessions carry a `--name` set elsewhere).
func StripLabel(label string) string {
	return strings.TrimSpace(labelRe.ReplaceAllString(label, ""))
}

// LabelSessionName reads the session name back out of a label. "" = no tag, the old
// shape, or not an AF label. Never guess: reporting no name is far better than reporting
// the wrong one (the mirror's peer badge is the only trail back to "who did this", and
// another session's name there sends the whole investigation off course).
func LabelSessionName(label string) string {
	m := labelRe.FindStringSubmatch(label)
	if m == nil {
		return ""
	}
	return m[1]
}
