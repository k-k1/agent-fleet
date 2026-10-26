package chatx

// The resume-handle table (providerResumeHandles) replaced three hand-kept lists. These tests
// pin the answers the old code gave and hold the table to ChatConversation and ChatProviders.

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// legacyResumeFields is the old providerHasResume switch plus clearProviderSessions, as data.
var legacyResumeFields = map[string]string{
	"claude":   "ClaudeSessionID",
	"codex":    "CodexSessionID",
	"opencode": "OpencodeSessionID",
	"agy":      "AgyConversationID",
	"cursor":   "CursorSessionID",
}

func TestResumeKindsAreUnchanged(t *testing.T) {
	if want := []string{"claude", "codex", "opencode", "agy", "cursor"}; !slices.Equal(providerResumeKinds, want) {
		t.Fatalf("providerResumeKinds = %v, want %v", providerResumeKinds, want)
	}
	for kind, field := range legacyResumeFields {
		var c ChatConversation
		reflect.ValueOf(&c).Elem().FieldByName(field).SetString("h")
		if !providerHasResume(&c, kind) {
			t.Errorf("%s: providerHasResume false with %s set", kind, field)
		}
		if !anyProviderResume(&c) {
			t.Errorf("%s: anyProviderResume false with %s set", kind, field)
		}
		for other := range legacyResumeFields {
			if other != kind && providerHasResume(&c, other) {
				t.Errorf("%s handle read as %s's", kind, other)
			}
		}
		clearProviderSessions(&c)
		if got := reflect.ValueOf(c).FieldByName(field).String(); got != "" {
			t.Errorf("%s: clearProviderSessions left %s = %q", kind, field, got)
		}
	}
}

// Case and spelling variants, and kinds with no handle, never report one.
func TestProviderHasResumeRefusesAlternateSpellings(t *testing.T) {
	c := ChatConversation{ClaudeSessionID: "a", CodexSessionID: "b", OpencodeSessionID: "c",
		AgyConversationID: "d", CursorSessionID: "e", MuseSessionID: "f"}
	for _, k := range []string{"", "Claude", "CLAUDE", " claude", "claude-code", "antigravity",
		"cursor-agent", "Codex", "lcpp", "shell"} {
		if providerHasResume(&c, k) {
			t.Errorf("providerHasResume(%q) = true", k)
		}
	}
}

// resumeHandleOutsideTable are handle fields that deliberately have no providerResumeHandles
// entry. muse is a gap, not a decision: its handle is neither counted by anyProviderResume nor
// cleared by clearProviderSessions, so compacting a muse conversation keeps the old native
// session (#2790). Fixing it is a behaviour change, kept out of the slice that introduced this guard.
var resumeHandleOutsideTable = []string{"MuseSessionID"}

// Every *SessionID / *ConversationID field of ChatConversation is a resume handle and must be
// in the table; a new backend's field fails here instead of surviving compaction.
func TestEveryResumeHandleFieldIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, h := range providerResumeHandles {
		var c ChatConversation
		p := h.handle(&c)
		*p = "x"
		rv := reflect.ValueOf(c)
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() == reflect.String && rv.Field(i).String() == "x" {
				listed[rv.Type().Field(i).Name] = true
			}
		}
	}
	rt := reflect.TypeOf(ChatConversation{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if !strings.HasSuffix(name, "SessionID") && !strings.HasSuffix(name, "ConversationID") {
			continue
		}
		if !listed[name] && !slices.Contains(resumeHandleOutsideTable, name) {
			t.Errorf("ChatConversation.%s looks like a resume handle but is not in providerResumeHandles", name)
		}
	}
}

// Every chat provider either holds a handle (in the table) or is named as one that does not.
func TestEveryChatProviderIsResumableOrExcluded(t *testing.T) {
	noHandle := []string{lcppKind, session.KindMuse}
	for k := range ChatProviders {
		if slices.Contains(providerResumeKinds, k) == slices.Contains(noHandle, k) {
			t.Errorf("chat provider %q: in providerResumeKinds=%v, in the exclusion list=%v — exactly one must hold",
				k, slices.Contains(providerResumeKinds, k), slices.Contains(noHandle, k))
		}
	}
}

// oneShotKinds as the pre-change literal, and its alias refusals.
func TestOneShotKindsAreUnchanged(t *testing.T) {
	want := []string{"claude", "codex", "opencode", "cursor", "agy", "muse"}
	if !slices.Equal(oneShotKinds, want) {
		t.Errorf("oneShotKinds = %v, want %v", oneShotKinds, want)
	}
	for _, k := range want {
		if !oneShotRunnable(k) {
			t.Errorf("oneShotRunnable(%q) = false", k)
		}
	}
	for _, k := range []string{"", "Claude", "CLAUDE", " claude", "claude-code", "antigravity",
		"cursor-agent", "kiro", "copilot", "lcpp", "Muse", "shell", "ssm"} {
		if oneShotRunnable(k) {
			t.Errorf("oneShotRunnable(%q) = true", k)
		}
	}
}
