package imagegen

import "strings"

// StudioSignalPrefix opens the one line the Console appends to a message sent from an image
// studio (ADR 0100 decision 5), e.g. "[studio v4 · draft changed · 2 new results →
// get_image_studio]". It tells the agent the studio moved; it is not something the member said.
//
// The line is always LAST, and the prefix never starts with "<": the mirror's isNoise hides a
// user turn whose text starts with "<" outright (console/src/features/mirror/transcript/model.ts),
// and the automatic title reads the opening of the first turns, which a trailing line leaves
// alone. The Console's copy of this constant is STUDIO_SIGNAL_PREFIX in that model.ts;
// studio_signal_test.go holds the two equal.
const StudioSignalPrefix = "[studio "

// StripStudioSignal removes the studio signal line from the end of a message, and the blank
// space before it. Only the last line is looked at: the same words anywhere else are the
// member's own text. Anything that is not a whole "[studio …]" line comes back untouched.
func StripStudioSignal(text string) string {
	body := strings.TrimRight(text, " \t\r\n")
	i := strings.LastIndexByte(body, '\n')
	last := body[i+1:]
	if !strings.HasPrefix(strings.TrimLeft(last, " \t"), StudioSignalPrefix) || !strings.HasSuffix(last, "]") {
		return text
	}
	if i < 0 {
		return ""
	}
	return strings.TrimRight(body[:i], " \t\r\n")
}
