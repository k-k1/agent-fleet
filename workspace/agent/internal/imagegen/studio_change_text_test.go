package imagegen

import (
	"encoding/json"
	"strings"
	"testing"
)

func draftChange(field string, before, after any) DraftChange {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	return DraftChange{Field: field, Before: b, After: a}
}

// A long prompt edited past its first 120 bytes has to show the edit, not the same head twice.
func TestStudioChangeTextShowsTheEditInALongPrompt(t *testing.T) {
	head := "masterpiece, best quality, (official art:1.2), (concept art:1.2), (mechanical design:1.2), character design, clean lineart, "
	before := head + "hangar deck, orange glowing lines, <lora:mecha:0.8>"
	after := head + "hangar deck, full armor <lora:armor:0.6>, orange glowing lines, <lora:mecha:0.8>"
	got := studioChangeText(draftChange("prompt", before, after))
	b, a, ok := strings.Cut(strings.TrimPrefix(got, "prompt: "), " → ")
	if !ok || b == a {
		t.Fatalf("both sides read the same: %s", got)
	}
	if !strings.Contains(a, "full armor") {
		t.Errorf("the added words are missing: %s", got)
	}
	if !strings.HasPrefix(b, `"…`) || !strings.HasPrefix(a, `"…`) {
		t.Errorf("the dropped head is not marked: %s", got)
	}
	if !strings.Contains(a, "<lora:armor:0.6>") {
		t.Errorf("angle brackets were escaped: %s", got)
	}
	for _, side := range []string{b, a} {
		if len(side) > studioChangeTextMax+len(`"……"`) {
			t.Errorf("side over its cap (%d bytes): %s", len(side), side)
		}
	}
}

func TestStudioChangeTextKeepsShortAndNonTextValues(t *testing.T) {
	for _, c := range []struct {
		change DraftChange
		want   string
	}{
		{draftChange("prompt", "a cat", "a dog"), `prompt: "a cat" → "a dog"`},
		{draftChange("params.cfg", 7, 5), `params.cfg: 7 → 5`},
		{DraftChange{Field: "negativePrompt", After: json.RawMessage(`"blurry"`)}, `negativePrompt: (empty) → "blurry"`},
	} {
		if got := studioChangeText(c.change); got != c.want {
			t.Errorf("studioChangeText = %s, want %s", got, c.want)
		}
	}
}

// Multi-byte text is cut on rune boundaries, and an edit in the middle keeps both ends marked.
func TestStudioChangeTextMiddleEditInJapanese(t *testing.T) {
	head := strings.Repeat("夕暮れの港、", 20)
	tail := strings.Repeat("、静かな波", 20)
	got := studioChangeText(draftChange("prompt", head+"赤い灯台"+tail, head+"白い灯台"+tail))
	if !strings.Contains(got, "赤い灯台") || !strings.Contains(got, "白い灯台") {
		t.Fatalf("the edit is missing: %s", got)
	}
	if strings.Count(got, "…") != 4 {
		t.Errorf("want both ends of both sides marked: %s", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("a rune was split: %s", got)
	}
}
