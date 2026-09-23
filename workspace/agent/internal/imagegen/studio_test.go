package imagegen

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

// The agent's writable draft fields are one decision (ADR 0100 decision 4) written twice: here
// and in the Console's wire.ts. The two lists must name the same keys in the same order.
func TestStudioAgentFieldsMatchTheConsole(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "console", "src", "features", "imagegen", "wire.ts"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const STUDIO_AGENT_FIELDS = \[(.*?)\] as const;`).FindSubmatch(src)
	if block == nil {
		t.Fatal("STUDIO_AGENT_FIELDS not found in wire.ts")
	}
	var ts []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		ts = append(ts, string(m[1]))
	}
	if !reflect.DeepEqual(ts, ImageStudioAgentFields) {
		t.Fatalf("Console %v, Go %v", ts, ImageStudioAgentFields)
	}
}
