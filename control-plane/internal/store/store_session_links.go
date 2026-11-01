package store

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The session mirror keeps each session's work-item links (#3134, #3147) so a stopped
// Workspace's rows still show them and the Control Plane can tell which sessions are on an
// item. The list arrives from the Agent, which the CP does not trust to be well formed: it
// is bounded and clipped before it is stored, and anything unreadable mirrors as no links
// rather than failing the write that also carries every other session.

// Bounds on what one session may put into the mirror. They match the Agent's own, in its
// units, so a well-formed list passes untouched: MaxLinks plus the launch item LinksOf puts
// first, the key in bytes, the title and URL in runes.
const (
	mirrorMaxLinks = 51
	mirrorMaxKey   = 200 // bytes
	mirrorMaxTitle = 300 // runes
	mirrorMaxURL   = 500 // runes
	mirrorMaxShort = 40  // bytes: provider, role, timestamp
)

// sessionLinkWire is one entry of the Agent's links array, in its wire shape.
type sessionLinkWire struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Role     string `json:"role"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	At       string `json:"at,omitempty"`
}

// SessionLinksFromJSON reads the relayed links of one session (the Agent's links array). An entry without a provider or a key,
// or with one longer than the Agent would ever write, is dropped; the rest are clipped.
func SessionLinksFromJSON(raw json.RawMessage) []SessionLink {
	if len(raw) == 0 {
		return nil
	}
	var in []sessionLinkWire
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out := make([]SessionLink, 0, min(len(in), mirrorMaxLinks))
	for _, l := range in {
		if len(out) == mirrorMaxLinks {
			break
		}
		// Postgres refuses a NUL in TEXT, and the INSERT failing would roll back the whole
		// workspace's mirror; the Agent passes titles through as given.
		for _, f := range []*string{&l.Provider, &l.Key, &l.Role, &l.Title, &l.URL, &l.At} {
			*f = strings.ReplaceAll(*f, "\x00", "")
		}
		if l.Provider == "" || l.Key == "" || len(l.Provider) > mirrorMaxShort || len(l.Key) > mirrorMaxKey {
			continue
		}
		url := l.URL
		if utf8.RuneCountInString(url) > mirrorMaxURL {
			url = "" // a clipped URL points somewhere else
		}
		out = append(out, SessionLink{
			Provider: l.Provider, Key: l.Key,
			Role:  clipBytes(l.Role, mirrorMaxShort),
			Title: clipRunes(l.Title, mirrorMaxTitle),
			URL:   url,
			At:    clipBytes(l.At, mirrorMaxShort),
		})
	}
	return out
}

// SessionLinksJSON is the stopped list's links for one mirrored session: the same JSON the
// Agent sends, so the Console reads both paths alike. nil (field omitted) for a session with none.
func SessionLinksJSON(links []SessionLink) json.RawMessage {
	if len(links) == 0 {
		return nil
	}
	out := make([]sessionLinkWire, len(links))
	for i, l := range links {
		out[i] = sessionLinkWire{Provider: l.Provider, Key: l.Key, Role: l.Role, Title: l.Title, URL: l.URL, At: l.At}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// clipBytes cuts s to at most n bytes without splitting a UTF-8 sequence.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// clipRunes cuts s to at most n runes, as the Agent clips a title.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
