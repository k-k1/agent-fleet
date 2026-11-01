package chatx

import (
	"reflect"
	"strings"
	"testing"
)

// oldCursorField / oldSetCursor are providerCursor's and MarkProviderSynced's per-kind switches
// as they were before the cursor joined providerResumeHandles.
func oldCursorField(c *ChatConversation, agent string) int {
	switch agent {
	case "claude":
		return c.ClaudeMessageCursor
	case "codex":
		return c.CodexMessageCursor
	case "opencode":
		return c.OpencodeMessageCursor
	case "agy":
		return c.AgyMessageCursor
	case "cursor":
		return c.CursorMessageCursor
	case "muse":
		return c.MuseMessageCursor
	}
	return 0
}

func oldSetCursor(c *ChatConversation, agent string, cursor int) {
	switch agent {
	case "claude":
		c.ClaudeMessageCursor = cursor
	case "codex":
		c.CodexMessageCursor = cursor
	case "opencode":
		c.OpencodeMessageCursor = cursor
	case "agy":
		c.AgyMessageCursor = cursor
	case "cursor":
		c.CursorMessageCursor = cursor
	case "muse":
		c.MuseMessageCursor = cursor
	}
}

// Every chat provider, every handle kind and the spellings that must keep missing: the table
// lookup answers what the old switches did, and writes land in the same field.
func TestProviderCursorMatchesOldSwitch(t *testing.T) {
	kinds := append([]string{}, providerResumeKinds...)
	for k := range ChatProviders {
		kinds = append(kinds, k)
	}
	kinds = append(kinds, "", "Claude", "CLAUDE", " claude", "claude-code", "antigravity",
		"cursor-agent", "Codex", "lcpp", "kiro", "copilot", "shell", "ssm", "unknown")
	for _, k := range kinds {
		var got, want ChatConversation
		MarkProviderSynced(&got, k, 7)
		oldSetCursor(&want, k, 7)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MarkProviderSynced(%q): %+v, old %+v", k, got, want)
		}
		full := ChatConversation{ClaudeMessageCursor: 1, CodexMessageCursor: 2, OpencodeMessageCursor: 3,
			AgyMessageCursor: 4, CursorMessageCursor: 5, MuseMessageCursor: 6}
		var none ChatConversation
		for _, c := range []*ChatConversation{&full, &none} {
			h, ok := providerResumeHandleOf(k)
			g := 0
			if ok {
				g = *h.cursor(c)
			}
			if w := oldCursorField(c, k); g != w {
				t.Errorf("cursor(%q) = %d, old %d", k, g, w)
			}
		}
	}
}

// providerCursor over legacy records (a handle but no cursor) and cursor records, for every
// kind: the whole function, not only the lookup.
func TestProviderCursorLegacyAndStored(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "a"}, {Role: "assistant", Agent: "claude", Content: "b"},
		{Role: "user", Content: "c"}, {Role: "assistant", Agent: "codex", Content: "d"},
		{Role: "user", Content: "e"}, {Role: "assistant", Agent: "claude", Content: "f"},
	}
	for _, k := range providerResumeKinds {
		c := ChatConversation{Messages: msgs}
		*mustHandle(t, k).handle(&c) = "h"
		legacy := providerCursor(&c, k)
		MarkProviderSynced(&c, k, 4)
		if got := providerCursor(&c, k); got != 4 {
			t.Errorf("%s: stored cursor 4, providerCursor = %d (legacy %d)", k, got, legacy)
		}
		resetProviderCursors(&c)
		if got := providerCursor(&c, k); got != legacy {
			t.Errorf("%s: after reset providerCursor = %d, want legacy %d", k, got, legacy)
		}
	}
	// An unlisted kind has no cursor and no handle: nothing is replayed.
	if got := providerCursor(&ChatConversation{Messages: msgs}, "Claude"); got != 0 {
		t.Errorf("alias kind: providerCursor = %d, want 0", got)
	}
}

func mustHandle(t *testing.T, kind string) providerResumeHandle {
	t.Helper()
	h, ok := providerResumeHandleOf(kind)
	if !ok {
		t.Fatalf("no handle entry for %q", kind)
	}
	return h
}

// Every *MessageCursor field of ChatConversation must be in the table, or a new backend's
// cursor would survive compaction (resetProviderCursors) and never be written.
func TestEveryMessageCursorFieldIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, h := range providerResumeHandles {
		var c ChatConversation
		*h.cursor(&c) = 99
		rv := reflect.ValueOf(c)
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() == reflect.Int && rv.Field(i).Int() == 99 {
				listed[rv.Type().Field(i).Name] = true
			}
		}
	}
	rt := reflect.TypeOf(ChatConversation{})
	for i := 0; i < rt.NumField(); i++ {
		if n := rt.Field(i).Name; strings.HasSuffix(n, "MessageCursor") && !listed[n] {
			t.Errorf("ChatConversation.%s is not in providerResumeHandles", n)
		}
	}
}
