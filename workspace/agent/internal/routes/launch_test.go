package routes

import (
	"strings"
	"testing"
)

// The bindings carry no secret and no account id, are stable for the same credential, and change
// with another key or another account (ADR 0138 D5b).
func TestBindings(t *testing.T) {
	k1, k2 := KeyBinding("ANTHROPIC_API_KEY", "sk-one"), KeyBinding("ANTHROPIC_API_KEY", "sk-two")
	if k1 == k2 || k1 != KeyBinding("ANTHROPIC_API_KEY", " sk-one ") || !strings.HasPrefix(k1, "ANTHROPIC_API_KEY@") ||
		strings.Contains(k1, "sk-one") || len(k1) != len("ANTHROPIC_API_KEY@")+16 {
		t.Errorf("KeyBinding: %q %q", k1, k2)
	}
	if KeyBinding("codex-api-key", "sk-one") == k1 {
		t.Error("two slots holding one key must stay apart")
	}
	a1, a2 := AccountBinding("agy-login", "a@example.com"), AccountBinding("agy-login", "b@example.com")
	if a1 == a2 || strings.Contains(a1, "example") || !strings.HasPrefix(a1, "agy-login@") {
		t.Errorf("AccountBinding: %q %q", a1, a2)
	}
	if AccountBinding("cursor-login", "a@example.com") == a1 {
		t.Error("one account on two logins must stay apart")
	}
	if KeyBinding("X", " ") != "" || AccountBinding("x", "") != "" {
		t.Error("an empty credential has no binding")
	}
	var l *Launch
	if l.Clone() != nil {
		t.Error("nil Launch must clone to nil")
	}
}
