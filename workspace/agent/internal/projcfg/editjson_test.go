package projcfg

import (
	"encoding/json"
	"strings"
	"testing"
)

// diffLines reports which line numbers differ between a and b (1-indexed), for
// asserting "only the entry's own lines changed" (docs/56 §13's golden-file test).
func diffLines(t *testing.T, a, b string) []int {
	t.Helper()
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []int
	max := len(al)
	if len(bl) > max {
		max = len(bl)
	}
	for i := 0; i < max; i++ {
		var av, bv string
		if i < len(al) {
			av = al[i]
		}
		if i < len(bl) {
			bv = bl[i]
		}
		if av != bv {
			out = append(out, i+1)
		}
	}
	return out
}

func TestUpsertJSONEntryReplaceOnlyChangesItsOwnLines(t *testing.T) {
	src := `{
  "mcpServers": {
    "alpha": {
      "command": "old-cmd",
      "args": [
        "x"
      ]
    },
    "beta": {
      "command": "keep-me"
    }
  }
}
`
	out, err := UpsertJSONEntry([]byte(src), "mcpServers", "alpha", map[string]any{
		"command": "new-cmd",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `"new-cmd"`) {
		t.Fatalf("missing new value:\n%s", got)
	}
	if !strings.Contains(got, `"beta"`) || !strings.Contains(got, `"keep-me"`) {
		t.Fatalf("beta entry disturbed:\n%s", got)
	}
	// The replaced entry becomes a single line (a flat one-field object); every
	// OTHER line (including beta, braces, and alpha's own key line) must be
	// byte-identical to the source.
	srcLines := strings.Split(src, "\n")
	gotLines := strings.Split(got, "\n")
	if len(gotLines) >= len(srcLines) {
		t.Fatalf("expected the multi-line alpha value to collapse to fewer lines; got %d vs src %d", len(gotLines), len(srcLines))
	}
	// beta's lines must appear verbatim and unmoved relative to file end.
	for _, want := range []string{`    "beta": {`, `      "command": "keep-me"`, `    }`, `  }`, `}`} {
		found := false
		for _, l := range gotLines {
			if l == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected line %q untouched, not found in:\n%s", want, got)
		}
	}
	if valid, err := isJSON(out); !valid {
		t.Fatalf("result is not valid JSON: %v\n%s", err, got)
	}
}

func TestUpsertJSONEntryInsertNewMatchesSiblingIndent(t *testing.T) {
	src := `{
  "mcpServers": {
    "alpha": {
      "command": "a"
    }
  }
}
`
	out, err := UpsertJSONEntry([]byte(src), "mcpServers", "beta", map[string]any{
		"command": "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	want := `{
  "mcpServers": {
    "alpha": {
      "command": "a"
    },
    "beta": {
      "command": "b"
    }
  }
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Appending after the last member necessarily touches ONE existing line (a
	// trailing comma added to what used to be the last entry) — the theoretical
	// minimum for an insertion, same as a human edit. alpha's CONTENT lines
	// (command, closing brace's indent) beyond that single comma must be untouched.
	diffs := diffLines(t, src, got)
	if len(diffs) == 0 {
		t.Fatalf("expected at least the comma-added line to differ")
	}
	for _, ln := range diffs {
		if ln < 5 { // lines 1-4 (through alpha's "command") must be untouched
			t.Fatalf("alpha's own content was touched (line %d changed)", ln)
		}
	}
}

func TestUpsertJSONEntryCreatesMissingContainerKey(t *testing.T) {
	src := `{
  "$schema": "https://opencode.ai/config.json"
}
`
	out, err := UpsertJSONEntry([]byte(src), "mcp", "srv", map[string]any{
		"type":    "local",
		"command": []any{"/bin/echo"},
		"enabled": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `"$schema": "https://opencode.ai/config.json"`) {
		t.Fatalf("schema line lost:\n%s", got)
	}
	if !strings.Contains(got, `"mcp": {`) || !strings.Contains(got, `"srv": {`) {
		t.Fatalf("mcp/srv not added:\n%s", got)
	}
	if valid, err := isJSON(out); !valid {
		t.Fatalf("not valid JSON: %v\n%s", err, got)
	}
}

func TestUpsertJSONEntryOnEmptyObject(t *testing.T) {
	out, err := UpsertJSONEntry([]byte("{}\n"), "mcpServers", "srv", map[string]any{"command": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := isJSON(out); !valid {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"srv"`) {
		t.Fatalf("got %s", out)
	}
}

func TestUpsertJSONEntryCompactSourceStaysCompact(t *testing.T) {
	src := `{"mcpServers":{"alpha":{"command":"a"}}}`
	out, err := UpsertJSONEntry([]byte(src), "mcpServers", "beta", map[string]any{"command": "b"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "\n") {
		t.Fatalf("expected compact output to stay single-line, got:\n%s", got)
	}
	if valid, err := isJSON([]byte(got)); !valid {
		t.Fatalf("not valid JSON: %v\n%s", err, got)
	}
}

func TestUpsertJSONEntryPreservesExtraKeys(t *testing.T) {
	src := `{
  "mcpServers": {
    "srv": {
      "command": "old"
    }
  }
}
`
	out, err := UpsertJSONEntry([]byte(src), "mcpServers", "srv", map[string]any{
		"command": "new",
		"tools":   []any{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `"tools"`) {
		t.Fatalf("expected the kind-specific extra key to round-trip: %s", got)
	}
}

func TestDeleteJSONEntryMiddleKeepsOthers(t *testing.T) {
	src := `{
  "mcpServers": {
    "alpha": {
      "command": "a"
    },
    "beta": {
      "command": "b"
    },
    "gamma": {
      "command": "g"
    }
  }
}
`
	out, err := DeleteJSONEntry([]byte(src), "mcpServers", "beta")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "beta") {
		t.Fatalf("beta not removed:\n%s", got)
	}
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "gamma") {
		t.Fatalf("sibling removed too:\n%s", got)
	}
	if valid, err := isJSON(out); !valid {
		t.Fatalf("not valid JSON: %v\n%s", err, got)
	}
}

func TestDeleteJSONEntryLastMemberCollapsesTrailingComma(t *testing.T) {
	src := `{
  "mcpServers": {
    "alpha": {
      "command": "a"
    },
    "beta": {
      "command": "b"
    }
  }
}
`
	out, err := DeleteJSONEntry([]byte(src), "mcpServers", "beta")
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "mcpServers": {
    "alpha": {
      "command": "a"
    }
  }
}
`
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestDeleteJSONEntryOnlyEntryRemovesContainerKey(t *testing.T) {
	src := `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "srv": {
      "command": "x"
    }
  }
}
`
	out, err := DeleteJSONEntry([]byte(src), "mcp", "srv")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, `"mcp"`) {
		t.Fatalf("expected the mcp key itself to be removed (materialize_json.go convention): %s", got)
	}
	if !strings.Contains(got, "$schema") {
		t.Fatalf("schema lost: %s", got)
	}
	if valid, err := isJSON(out); !valid {
		t.Fatalf("not valid JSON: %v\n%s", err, got)
	}
}

func TestDeleteJSONEntryMissingIsNoop(t *testing.T) {
	src := `{"mcpServers":{"a":{"command":"x"}}}`
	out, err := DeleteJSONEntry([]byte(src), "mcpServers", "nope")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != src {
		t.Fatalf("expected no-op, got %s", out)
	}
	out, err = DeleteJSONEntry([]byte(src), "noSuchKey", "a")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != src {
		t.Fatalf("expected no-op for missing container, got %s", out)
	}
}

func TestUpsertDeleteRoundTrip(t *testing.T) {
	// upsert then delete must return exactly the original bytes.
	src := `{
  "mcpServers": {
    "alpha": {
      "command": "a"
    }
  }
}
`
	added, err := UpsertJSONEntry([]byte(src), "mcpServers", "beta", map[string]any{"command": "b"})
	if err != nil {
		t.Fatal(err)
	}
	back, err := DeleteJSONEntry(added, "mcpServers", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != src {
		t.Fatalf("round trip mismatch:\ngot:\n%s\nwant:\n%s", back, src)
	}
}

func isJSON(b []byte) (bool, error) {
	var v any
	err := json.Unmarshal(b, &v)
	return err == nil, err
}
