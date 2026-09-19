package mcpc

import "testing"

func TestToolNameForServer_MatchesTheConcreteRegisteredName(t *testing.T) {
	prefixed := PrefixToolName("alpha", "do_thing")
	if got, ok := toolNameForServer(prefixed, "alpha"); !ok || got != "do_thing" {
		t.Fatalf("toolNameForServer(%q, \"alpha\") = (%q, %v), want (\"do_thing\", true)", prefixed, got, ok)
	}
	if _, ok := toolNameForServer(prefixed, "beta"); ok {
		t.Fatalf("toolNameForServer(%q, \"beta\") should not match an unrelated server name", prefixed)
	}
	if _, ok := toolNameForServer(prefixed, "al"); ok {
		t.Fatalf("toolNameForServer(%q, \"al\") should not match a name that is only a substring, not the whole prefix segment", prefixed)
	}
}

func TestToolNameForServer_WrongPrefix(t *testing.T) {
	if _, ok := toolNameForServer("not_a_prefixed_name", "srv"); ok {
		t.Fatal("an unprefixed name should never match")
	}
	if _, ok := toolNameForServer(PrefixToolName("other", "tool"), "srv"); ok {
		t.Fatal("a name prefixed for a different server should not match")
	}
}

// TestToolNameForServer_KnownAmbiguityWhenOneNameExtendsAnother documents, rather than
// hides, a real limitation: mcpreg's name grammar (def.go's nameRe) allows "__" inside a
// server name, so if a deployment ever registers both "my" and "my__server" AND their
// tool names happen to concatenate to identical bytes, the two are indistinguishable
// from the wire string alone. Manager.CallTool then picks whichever of the two
// (equally-trusted, both user/tenant-registered) servers its map iteration visits
// first — see tooldef.go's package comment. This test pins that behavior down instead
// of leaving it as an unverified assumption.
func TestToolNameForServer_KnownAmbiguityWhenOneNameExtendsAnother(t *testing.T) {
	prefixed := PrefixToolName("my", "server__do_thing")
	got1, ok1 := toolNameForServer(prefixed, "my")
	got2, ok2 := toolNameForServer(prefixed, "my__server")
	if !ok1 || got1 != "server__do_thing" {
		t.Fatalf("toolNameForServer(%q, \"my\") = (%q, %v)", prefixed, got1, ok1)
	}
	if !ok2 || got2 != "do_thing" {
		t.Fatalf("toolNameForServer(%q, \"my__server\") = (%q, %v)", prefixed, got2, ok2)
	}
}
