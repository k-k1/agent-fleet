package agents

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestUniqueSlotForSessionID(t *testing.T) {
	ids := map[string]string{"a": "ses_1", "b": "ses_2", "c": "ses_2", "d": "ses_3", "e": "ses_3", "arch": "ses_4"}
	slotID := func(m session.Meta) string { return ids[m.Name] }
	meta := func(name string, archived bool) session.Meta { return session.Meta{Name: name, Archived: archived} }
	all := []session.Meta{meta("a", false), meta("b", false), meta("c", false), meta("d", false), meta("e", false), meta("arch", true)}
	notE := func(m session.Meta) bool { return m.Name != "e" }
	cases := []struct {
		name   string
		id     string
		accept func(session.Meta) bool
		want   string
	}{
		{"single match", "ses_1", nil, "a"},
		{"no match", "ses_9", nil, ""},
		{"empty id never matches", "", nil, ""},
		{"two slots share the id", "ses_2", nil, ""},
		{"archived slot ignored", "ses_4", nil, ""},
		{"accept narrows an ambiguous id to one", "ses_3", notE, "d"},
		{"accept rejecting the only match", "ses_1", func(session.Meta) bool { return false }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := UniqueSlotForSessionID(all, tc.id, slotID, tc.accept)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("got (%q,%v), want %q", got, ok, tc.want)
			}
		})
	}
}
