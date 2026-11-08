package memengine

import (
	"errors"
	"testing"
)

func s(v string) *string { return &v }

func TestApplyEdits(t *testing.T) {
	body := "line one\nline two\nline three\n"
	desc := "a description"
	good, _, err := ApplyEdits(body, desc, []Edit{
		{Old: s("line two\n"), New: s("line two\nadded\n")},
		{Old: s("three"), New: s("3")},
	})
	if err != nil || good != "line one\nline two\nadded\nline 3\n" {
		t.Fatalf("got %q, %v", good, err)
	}
	// Edits are matched against the same original and applied together, in any order.
	rev, _, err := ApplyEdits(body, desc, []Edit{{Old: s("three"), New: s("3")}, {Old: s("one"), New: s("1")}})
	if err != nil || rev != "line 1\nline two\nline 3\n" {
		t.Fatalf("got %q, %v", rev, err)
	}
	_, d, err := ApplyEdits(body, desc, []Edit{{Field: "description", Old: s("a "), New: s("the ")}})
	if err != nil || d != "the description" {
		t.Fatalf("description edit: %q, %v", d, err)
	}
	delBody, _, err := ApplyEdits(body, desc, []Edit{{Old: s("line two\n"), New: s("")}})
	if err != nil || delBody != "line one\nline three\n" {
		t.Fatalf("an empty new deletes: %q, %v", delBody, err)
	}
	same, _, err := ApplyEdits(body, desc, nil)
	if err != nil || same != body {
		t.Fatalf("no edits is a valid answer that changes nothing: %q, %v", same, err)
	}
}

func TestApplyEditsRejects(t *testing.T) {
	body := "alpha beta\nalpha gamma\naaa\n"
	cases := []struct {
		name  string
		edits []Edit
	}{
		{"old absent", []Edit{{Old: s("delta"), New: s("x")}}},
		{"old twice", []Edit{{Old: s("alpha"), New: s("x")}}},
		{"old overlaps itself", []Edit{{Old: s("aa"), New: s("x")}}},
		{"edits overlap", []Edit{{Old: s("alpha beta"), New: s("x")}, {Old: s("beta\nalpha"), New: s("y")}}},
		{"same span twice", []Edit{{Old: s("gamma"), New: s("x")}, {Old: s("gamma"), New: s("y")}}},
		{"empty old", []Edit{{Old: s(""), New: s("x")}}},
		{"old equals new", []Edit{{Old: s("gamma"), New: s("gamma")}}},
		{"new omitted", []Edit{{Old: s("gamma")}}},
		{"old omitted", []Edit{{New: s("x")}}},
		{"unknown field", []Edit{{Field: "title", Old: s("gamma"), New: s("x")}}},
		{"description text is not in the description", []Edit{{Field: "description", Old: s("gamma"), New: s("x")}}},
	}
	for _, c := range cases {
		if _, _, err := ApplyEdits(body, "desc", c.edits); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", c.name, err)
		}
	}
	many := make([]Edit, MaxEdits+1)
	for i := range many {
		many[i] = Edit{Old: s("gamma"), New: s("x")}
	}
	if _, _, err := ApplyEdits(body, "d", many); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many edits: %v", err)
	}
}

func TestParsePatch(t *testing.T) {
	req := PatchRequest{Observation: "o", Name: "n", Description: "d", Body: "keep this\nfix that\n"}
	ok, err := ParsePatch(`{"edits":[{"old":"fix that","new":"fixed"}]}`, req)
	if err != nil || len(ok.Edits) != 1 {
		t.Fatalf("%v %v", ok, err)
	}
	if a, err := ParsePatch(`{"edits":[]}`, req); err != nil || len(a.Edits) != 0 {
		t.Fatalf("empty list: %v %v", a, err)
	}
	for name, in := range map[string]string{
		"edits missing":       `{}`,
		"edits null":          `{"edits":null}`,
		"whole body returned": `{"body":"keep this\nfixed\n"}`,
		"old not in the note": `{"edits":[{"old":"nope","new":"x"}]}`,
		"unknown edit field":  `{"edits":[{"old":"fix that","new":"x","why":"y"}]}`,
		"prose":               `Sure! Here is the patch.`,
		"edits is not a list": `{"edits":{"old":"fix that","new":"x"}}`,
	} {
		if _, err := ParsePatch(in, req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}
