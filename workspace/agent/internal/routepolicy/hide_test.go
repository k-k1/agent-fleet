package routepolicy

import (
	"path/filepath"
	"slices"
	"testing"
)

// The listing endpoints hide by the held policy only while it restricts: nothing held, or an
// open policy, hides nothing; a restricting one narrows a list to what it allows and hides a
// harness none of whose routes it allows, never one with no route at all.
func TestHiding(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "rp.json"), "")
	old := Active
	Active = func() *Store { return s }
	t.Cleanup(func() { Active = old })
	if _, ok := Hiding(); ok {
		t.Fatal("nothing held hides something")
	}
	if _, err := s.Accept(open1); err != nil {
		t.Fatal(err)
	}
	if _, ok := Hiding(); ok {
		t.Fatal("an open policy hides something")
	}
	if _, err := s.Accept(strict2); err != nil {
		t.Fatal(err)
	}
	p, ok := Hiding()
	if !ok {
		t.Fatal("a restricting policy hides nothing")
	}
	if got := p.Filter([]string{"openai-api", "anthropic-api", "anthropic-subscription"}); !slices.Equal(got, []string{"anthropic-api"}) {
		t.Errorf("filter = %v", got)
	}
	if p.Filter([]string{"openai-api"}) != nil {
		t.Error("a list with nothing allowed is not empty")
	}
	if !p.HidesKind([]string{"chatgpt-subscription", "openai-api"}) || p.HidesKind([]string{"openai-api", "anthropic-api"}) {
		t.Error("a kind is hidden exactly when none of its routes is allowed")
	}
	if p.HidesKind(nil) {
		t.Error("a kind with no route (it runs no model) was hidden")
	}
}
