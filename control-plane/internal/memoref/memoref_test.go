package memoref

import (
	"strings"
	"testing"
)

func known(p string) bool { return p == "github" || p == "jira" || p == "bitbucket" }

func TestNormalize(t *testing.T) {
	const u = "https://github.com/acme/app/issues/12"
	for _, c := range []struct {
		name string
		in   Ref
		want Ref
		ok   bool
	}{
		{"none", Ref{}, Ref{}, true},
		{"github", Ref{"github", "acme/app#12", u}, Ref{"github", "acme/app#12", u}, true},
		{"folded", Ref{" JIRA ", " proj-12 ", "http://j.example/b"}, Ref{"jira", "PROJ-12", "http://j.example/b"}, true},
		{"bitbucket", Ref{"bitbucket", "a/b#3", u}, Ref{"bitbucket", "a/b#3", u}, true},
		{"unknown provider", Ref{"gitlab", "a/b#3", u}, Ref{}, false},
		{"missing url", Ref{"github", "a/b#3", ""}, Ref{}, false},
		{"missing key", Ref{"github", "", u}, Ref{}, false},
		{"missing provider", Ref{"", "a/b#3", u}, Ref{}, false},
		{"bare number", Ref{"github", "#3", u}, Ref{}, false},
		{"zero", Ref{"github", "a/b#0", u}, Ref{}, false},
		{"jira shape for github", Ref{"github", "PROJ-1", u}, Ref{}, false},
		{"github shape for jira", Ref{"jira", "a/b#1", u}, Ref{}, false},
		{"key too long", Ref{"github", strings.Repeat("a", KeyMax) + "/b#1", u}, Ref{}, false},
		{"javascript url", Ref{"github", "a/b#3", "javascript:alert(1)"}, Ref{}, false},
		{"no host", Ref{"github", "a/b#3", "https:///x"}, Ref{}, false},
		{"space in url", Ref{"github", "a/b#3", "https://x.example/a b"}, Ref{}, false},
		{"url too long", Ref{"github", "a/b#3", "https://x.example/" + strings.Repeat("a", URLMax)}, Ref{}, false},
	} {
		got, err := Normalize(c.in, known)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
	}
	if Ident("github", "Acme/App#1") != Ident("github", "acme/app#1") || Ident("github", "a/b#1") == Ident("bitbucket", "a/b#1") || Ident("github", "") != "" {
		t.Error("Ident")
	}
}
