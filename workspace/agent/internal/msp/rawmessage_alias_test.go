package msp

import (
	"os"
	"regexp"
	"testing"
)

// TestNoDefinedRawMessageTypes: a generated `type X json.RawMessage` has no UnmarshalJSON, so
// X decodes as a plain []byte and an array or object in it fails the whole enclosing message.
// That is how every Muse 1.4.0 model/list answer was dropped (#1344). Every free-form type must
// be an alias.
func TestNoDefinedRawMessageTypes(t *testing.T) {
	b, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^type (\w+) json\.RawMessage$`)
	for _, m := range re.FindAllSubmatch(b, -1) {
		t.Errorf("type %s is a defined json.RawMessage; generate it as an alias", m[1])
	}
	// The control: the aliases this must not flag are there to be seen.
	if !regexp.MustCompile(`(?m)^type ModelReasoningEffortVariants = json\.RawMessage$`).Match(b) {
		t.Error("ModelReasoningEffortVariants is not an alias")
	}
}
