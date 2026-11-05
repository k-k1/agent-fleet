package claude

import "testing"

func TestStripHeldBidi(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no bidi control is the same string", "a\u200bb\ufeff\nשלום", "a\u200bb\ufeff\nשלום"},
		{"LRM, RLM and ALM on a left-to-right line", "a\u200eb\u200fc\u061cd", "abcd"},
		{"embeddings, overrides and isolates", "\u202aa\u202b\u202c\u202d\u202eb\u2066\u2067\u2068\u2069", "ab"},
		// claude may keep a direction mark next to right-to-left script, so it is left to claude
		// there; an isolate is removed by claude everywhere, so here too.
		{"marks kept on a right-to-left line", "שלום\u200f world\u2066x", "שלום\u200f worldx"},
		{"the line decides, not the whole text", "a\u200eb\nمرحبا\u200e\nc\u200fd", "ab\nمرحبا\u200e\ncd"},
		{"other invisible characters are left to claude", "x\u200b\u00ad\u2060\u200ey", "x\u200b\u00ad\u2060y"},
		{"a BOM is not right-to-left script", "\ufeffa\u200eb", "\ufeffab"},
		{"CRLF kept", "a\u200e\r\nb", "a\r\nb"},
	}
	for _, c := range cases {
		if got := StripHeldBidi(c.in); got != c.want {
			t.Errorf("%s: StripHeldBidi(%+q) = %+q, want %+q", c.name, c.in, got, c.want)
		}
		if again := StripHeldBidi(c.want); again != c.want {
			t.Errorf("%s: not idempotent: %+q -> %+q", c.name, c.want, again)
		}
	}
}

// ShapePrompt is what typePromptText types and what the delivery check and injection tags
// compare: both rewrites, in one form.
func TestShapePromptStripsAndQuotes(t *testing.T) {
	const p = "/home/dev/.cache/agent-fleet/pasted/sid/paste-1.png"
	in := "見て\u200e Open the following file(s) with the Read tool: " + p
	want := "見て Open the following file(s) with the Read tool: `" + p + "`"
	if got := ShapePrompt(in); got != want {
		t.Fatalf("ShapePrompt = %+q, want %+q", got, want)
	}
	if again := ShapePrompt(want); again != want {
		t.Fatalf("not idempotent: %+q", again)
	}
}
