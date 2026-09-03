package mcpx

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// TestMCPAdvertisedInputSchemasAreValid walks the same tool list the stdio server
// advertises in every capability shape. The external compiler is deliberate: a
// hand-written approximation would have accepted enum:null, which Anthropic rejects
// before starting the Claude turn.
func TestMCPAdvertisedInputSchemasAreValid(t *testing.T) {
	oldWrite, oldSelfReport := writeEnabled(), selfReportOnly()
	oldChromium, oldPeer := sessionChromiumEnabled(), mcpPeerMessagingEnabled
	t.Cleanup(func() {
		setFlags(oldWrite, oldSelfReport, oldChromium)
		mcpPeerMessagingEnabled = oldPeer
	})

	variants := []struct {
		name                        string
		write, selfReport, chromium bool
		peer                        bool
	}{
		{name: "assistant-read"},
		{name: "assistant-write", write: true},
		{name: "session", selfReport: true},
		{name: "session-chromium", selfReport: true, chromium: true},
		{name: "session-all", selfReport: true, chromium: true, peer: true},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			setFlags(variant.write, variant.selfReport, variant.chromium)
			mcpPeerMessagingEnabled = variant.peer
			for _, tool := range mcpStdioToolList() {
				name := tool["name"].(string)
				schema, ok := tool["inputSchema"].(map[string]any)
				if !ok {
					t.Errorf("%s: inputSchema が object ではない: %T", name, tool["inputSchema"])
					continue
				}
				if err := validateMCPInputSchema(schema); err != nil {
					t.Errorf("%s: inputSchema が不正: %v", name, err)
				}
			}
		})
	}
}

// TestMCPInputSchemaValidationRejectsKnownBadShapes is the negative control for
// the repository gate itself. One case is the production outage's exact shape; the
// other is structurally valid JSON Schema that cannot describe its required input.
func TestMCPInputSchemaValidationRejectsKnownBadShapes(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		schema     map[string]any
	}{
		{
			name: "nil enum",
			want: "enum",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"intent": map[string]any{"type": "string", "enum": nil},
				},
			},
		},
		{
			name: "unknown required property",
			want: "missing",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{"missing"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMCPInputSchema(tc.schema)
			if err == nil {
				t.Fatal("不正 schema が検査を通った")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want %q を含む", err, tc.want)
			}
		})
	}
}

func validateMCPInputSchema(schema map[string]any) error {
	// MCP transports JSON, while the in-process representation legitimately uses
	// []string for required/enum. Round-trip first so the compiler sees the exact
	// JSON value Claude receives rather than Go's concrete container types.
	b, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("JSON encode: %w", err)
	}
	var wire any
	if err := json.Unmarshal(b, &wire); err != nil {
		return fmt.Errorf("JSON decode: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "urn:agent-fleet:mcp-input-schema"
	if err := compiler.AddResource(resource, wire); err != nil {
		return fmt.Errorf("Draft 2020-12 resource: %w", err)
	}
	if _, err := compiler.Compile(resource); err != nil {
		return fmt.Errorf("Draft 2020-12: %w", err)
	}
	return validateMCPInputSchemaNode("$", schema)
}

// validateMCPInputSchemaNode adds Agent Fleet's tool-contract invariants that the
// JSON Schema metaschema intentionally does not impose (for example, required may
// legally name a property absent from properties, but that is never useful here).
func validateMCPInputSchemaNode(path string, schema map[string]any) error {
	if _, ok := schema["type"]; !ok {
		return fmt.Errorf("%s: type が無い", path)
	}
	if enum, ok := schema["enum"]; ok {
		v := reflect.ValueOf(enum)
		if enum == nil || v.Kind() != reflect.Slice || v.Len() == 0 {
			return fmt.Errorf("%s.enum は空でない配列でなければならない", path)
		}
	}
	for _, keyword := range []string{"minLength", "maxLength"} {
		if value, ok := schema[keyword]; ok {
			if n, ok := schemaNonNegativeInteger(value); !ok || n == 0 {
				return fmt.Errorf("%s.%s は正の整数でなければならない: %v", path, keyword, value)
			}
		}
	}

	properties, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"]; ok {
		for _, name := range schemaStrings(required) {
			if _, exists := properties[name]; !exists {
				return fmt.Errorf("%s.required の %q が properties に無い", path, name)
			}
		}
	}
	for name, child := range properties {
		childSchema, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties.%s が object ではない: %T", path, name, child)
		}
		if err := validateMCPInputSchemaNode(path+".properties."+name, childSchema); err != nil {
			return err
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if err := validateMCPInputSchemaNode(path+".items", items); err != nil {
			return err
		}
	}
	return nil
}

func schemaNonNegativeInteger(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), n >= 0
	case int64:
		return n, n >= 0
	case float64:
		return int64(n), n >= 0 && n == float64(int64(n))
	default:
		return 0, false
	}
}

func schemaStrings(v any) []string {
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
