package memengine

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCompose(t *testing.T) {
	req := ComposeRequest{Operation: OpMerge, Observation: "o", Sources: []Note{{Name: "a"}, {Name: "b"}}}
	ok := `{"name":"ab","description":"d","type":"project","body":"text"}`
	a, err := ParseCompose(ok, req)
	if err != nil || a.Name != "ab" || a.Body != "text" {
		t.Fatalf("got %+v, %v", a, err)
	}
	for name, text := range map[string]string{
		"not json":       `nope`,
		"unknown field":  `{"name":"ab","description":"d","type":"project","body":"x","extra":1}`,
		"bad name":       `{"name":"Ab C","description":"d","type":"project","body":"x"}`,
		"reused name":    `{"name":"a","description":"d","type":"project","body":"x"}`,
		"empty body":     `{"name":"ab","description":"d","type":"project","body":" \n"}`,
		"multiline desc": `{"name":"ab","description":"d\ne","type":"project","body":"x"}`,
		"no type":        `{"name":"ab","description":"d","type":"","body":"x"}`,
		"huge body":      `{"name":"ab","description":"d","type":"project","body":"` + strings.Repeat("x", MaxComposeBody+1) + `"}`,
	} {
		if _, err := ParseCompose(text, req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ParseCompose(ok, ComposeRequest{Sources: make([]Note, MaxComposeSources+1)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many sources: err = %v", err)
	}
}

func TestComposePromptShowsWholeNotes(t *testing.T) {
	p := ComposeRequest{Operation: OpSupersede, Observation: "obs", Sources: []Note{{Name: "old", Description: "dd", Body: strings.Repeat("line\n", 400)}}}.Prompt()
	for _, want := range []string{"## Observation\nobs", "### old\ndescription: dd", ComposeSchema} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Count(p, "line\n") != 400 {
		t.Error("the prompt cut the note")
	}
	if !strings.Contains(ComposeRequest{Operation: OpAdd}.Prompt(), "(none)") {
		t.Error("an add shows no notes")
	}
}
