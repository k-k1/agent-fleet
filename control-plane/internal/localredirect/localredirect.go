// Package localredirect holds the one same-origin check every redirect target built
// from request input goes through.
package localredirect

import (
	"net/url"
	"strings"
)

// Safe returns a same-origin path (plus query) that is safe to hand
// to a Location header or an href, or "/" when the input is anything else.
//
// The value returned is the re-serialized form of what was checked, so the bytes
// that are sent are the bytes that were judged. Never validate the raw string and
// then send a re-parsed one: the two can differ.
//
// Rejected: control characters and backslashes anywhere (browsers fold a backslash
// into a slash, so a leading "/\" reads as scheme-relative), any scheme, host or
// user info, and a path that does not start with exactly one "/" in either its
// decoded or its escaped form (so an encoded slash or backslash cannot rebuild a
// scheme-relative URL after one more decoding step).
func Safe(raw string) string {
	if raw == "" || raw[0] != '/' {
		return "/"
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	esc := u.EscapedPath()
	if !singleLeadingSlash(u.Path) || !singleLeadingSlash(esc) {
		return "/"
	}
	// An encoded backslash decodes to a character the check above never saw.
	if strings.Contains(u.Path, "\\") {
		return "/"
	}
	out := esc
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if f := u.EscapedFragment(); f != "" {
		out += "#" + f
	}
	// Re-check the serialized result: it is what reaches the browser.
	if strings.HasPrefix(out, "//") {
		return "/"
	}
	return out
}

func singleLeadingSlash(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//")
}
