package schemagen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// baseBundle is a miniature export with one of every construct the rules distinguish.
// StartParams and Mode travel only to the host, StartResult and Reason only to the client, and
// Role and Variants are referenced by nothing, so they get both rule sets.
const baseBundle = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "description": "MSP v1",
  "$defs": {
    "StartParams": {
      "type": "object",
      "properties": {
        "cwd": {"type": "string"},
        "model": {"type": ["string", "null"]},
        "mode": {"$ref": "#/$defs/Mode"}
      },
      "required": ["cwd"]
    },
    "StartResult": {
      "type": "object",
      "properties": {"sessionId": {"type": "string"}, "status": {"$ref": "#/$defs/Reason"}},
      "required": ["sessionId"]
    },
    "Mode": {"type": "string", "enum": ["plan", "act"], "x-msp-openness": "closed"},
    "Reason": {"type": "string", "enum": ["idle", "shutdown"], "x-msp-openness": "open"},
    "Role": {"type": "string", "enum": ["user", "assistant"], "x-msp-openness": "closed"},
    "Variants": {"anyOf": [{"type": "array", "items": {"$ref": "#/$defs/Reason"}}, {"type": "null"}]}
  },
  "methods": {
    "session/start": {"description": "start", "params": {"$ref": "#/$defs/StartParams"}, "result": {"$ref": "#/$defs/StartResult"}}
  },
  "notifications": {
    "session/closed": {"params": {"type": "object", "properties": {"reason": {"$ref": "#/$defs/Reason"}}}}
  },
  "requests": {
    "approval/request": {"params": {"type": "object"}, "result": {"type": "object"}}
  },
  "errors": [{"code": -32001, "kind": "notFound", "retryable": false}],
  "capabilities": {"grantable": ["fs.read"], "reserved": [{"name": "rawLog", "reference": "#1"}]},
  "reserved": []
}`

const baseManifest = `{"experimental": false, "fingerprint": "sha256:aaaa", "schemaVersion": 1}`

// compareMutated writes the base bundle as the checked-in side and a mutated copy as the
// export, then compares them.
func compareMutated(t *testing.T, mutate func(b map[string]any), mutateMan func(m map[string]any)) *CompatReport {
	t.Helper()
	oldDir, newDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(oldDir, "schema", "msp.schema.json"), baseBundle)
	writeFile(t, filepath.Join(oldDir, "schema", "manifest.json"), baseManifest)

	var b, m map[string]any
	if err := json.Unmarshal([]byte(baseBundle), &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(baseManifest), &m); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(b)
	}
	m["fingerprint"] = "sha256:bbbb"
	if mutateMan != nil {
		mutateMan(m)
	}
	writeJSON(t, filepath.Join(newDir, "msp.schema.json"), b)
	writeJSON(t, filepath.Join(newDir, "manifest.json"), m)

	r, err := Compare(oldDir, newDir)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	return r
}

func def(b map[string]any, name string) map[string]any {
	return b["$defs"].(map[string]any)[name].(map[string]any)
}

func props(b map[string]any, name string) map[string]any {
	return def(b, name)["properties"].(map[string]any)
}

// TestCompareIdenticalExportIsClean is the negative control for the addition rules: a new
// fingerprint alone, with nothing else moved, must report nothing at all.
func TestCompareIdenticalExportIsClean(t *testing.T) {
	r := compareMutated(t, nil, nil)
	if len(r.Additions) != 0 || len(r.Breaks) != 0 {
		t.Fatalf("an unchanged schema reported changes:\n%s", r)
	}
}

func TestCompareAdditions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(b map[string]any)
		want   []string
	}{
		{"new type", func(b map[string]any) {
			b["$defs"].(map[string]any)["DeleteParams"] = map[string]any{"type": "object"}
		}, []string{"$defs.DeleteParams: new type"}},
		{"new method", func(b map[string]any) {
			b["methods"].(map[string]any)["session/delete"] = map[string]any{"params": map[string]any{"type": "object"}}
		}, []string{"methods.session/delete: new method"}},
		{"new notification", func(b map[string]any) {
			b["notifications"].(map[string]any)["session/started"] = map[string]any{"params": map[string]any{"type": "object"}}
		}, []string{"notifications.session/started: new notification"}},
		{"new optional property", func(b map[string]any) {
			props(b, "StartParams")["effort"] = map[string]any{"type": "string"}
		}, []string{"$defs.StartParams.effort: new optional property"}},
		{"new required property in a result", func(b map[string]any) {
			props(b, "StartResult")["cursor"] = map[string]any{"type": "string"}
			def(b, "StartResult")["required"] = []any{"sessionId", "cursor"}
		}, []string{"$defs.StartResult.cursor: new required property (host to client only)"}},
		{"param became optional", func(b map[string]any) {
			def(b, "StartParams")["required"] = []any{}
		}, []string{"$defs.StartParams.cwd: required true -> false (safe in this direction)"}},
		{"result member became required", func(b map[string]any) {
			def(b, "StartResult")["required"] = []any{"sessionId", "status"}
		}, []string{"$defs.StartResult.status: required false -> true (safe in this direction)"}},
		{"new value in an enum only the client sends", func(b map[string]any) {
			def(b, "Mode")["enum"] = []any{"plan", "act", "review"}
		}, []string{`$defs.Mode.enum: new value "review" (host to client never carries it)`}},
		{"new error code", func(b map[string]any) {
			b["errors"] = append(b["errors"].([]any), map[string]any{"code": -32002, "kind": "busy"})
		}, []string{"errors[-32002]: new error code"}},
		{"new value in a sent enum, also reached by a new notification", func(b map[string]any) {
			// The old client ignores the new notification, so Mode still only travels to the host.
			def(b, "Mode")["enum"] = []any{"plan", "act", "review"}
			b["notifications"].(map[string]any)["mode/changed"] = map[string]any{"params": map[string]any{"$ref": "#/$defs/Mode"}}
		}, []string{`$defs.Mode.enum: new value "review" (host to client never carries it)`, "notifications.mode/changed: new notification"}},
		{"new grantable capability", func(b map[string]any) {
			b["capabilities"].(map[string]any)["grantable"] = []any{"fs.read", "fs.write"}
		}, []string{"capabilities.grantable: fs.write added"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := compareMutated(t, c.mutate, nil)
			if !r.Compatible() {
				t.Fatalf("an additive change was judged breaking:\n%s", r)
			}
			// Exact, so a checker that starts reporting extra additions goes red here.
			if !reflect.DeepEqual(r.Additions, c.want) {
				t.Fatalf("additions = %q, want %q", r.Additions, c.want)
			}
		})
	}
}

func TestCompareBreaks(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(b map[string]any)
		mutateMan func(m map[string]any)
		want      string
	}{
		{"removed type", func(b map[string]any) {
			delete(b["$defs"].(map[string]any), "Role")
		}, nil, "$defs.Role: removed"},
		{"removed method", func(b map[string]any) {
			delete(b["methods"].(map[string]any), "session/start")
		}, nil, "methods.session/start: removed"},
		{"removed notification", func(b map[string]any) {
			delete(b["notifications"].(map[string]any), "session/closed")
		}, nil, "notifications.session/closed: removed"},
		{"new server request", func(b map[string]any) {
			b["requests"].(map[string]any)["userInput/request"] = map[string]any{"params": map[string]any{"type": "object"}}
		}, nil, "requests.userInput/request: new server request"},
		{"removed property", func(b map[string]any) {
			delete(props(b, "StartParams"), "model")
		}, nil, "$defs.StartParams.model: removed"},
		{"new required property", func(b map[string]any) {
			props(b, "StartParams")["workspace"] = map[string]any{"type": "string"}
			def(b, "StartParams")["required"] = []any{"cwd", "workspace"}
		}, nil, "$defs.StartParams.workspace: new required property"},
		{"existing property became required", func(b map[string]any) {
			def(b, "StartParams")["required"] = []any{"cwd", "model"}
		}, nil, "$defs.StartParams.model: required false -> true"},
		{"result member became optional", func(b map[string]any) {
			def(b, "StartResult")["required"] = []any{}
		}, nil, "$defs.StartResult.sessionId: required true -> false"},
		{"new required property in an unreferenced type", func(b map[string]any) {
			v := def(b, "Role")
			v["properties"] = map[string]any{"x": map[string]any{"type": "string"}}
			v["required"] = []any{"x"}
		}, nil, "$defs.Role.x: new required property"},
		{"changed type", func(b map[string]any) {
			props(b, "StartParams")["cwd"] = map[string]any{"type": "integer"}
		}, nil, `$defs.StartParams.cwd.type: "string" -> "integer"`},
		{"changed ref", func(b map[string]any) {
			b["methods"].(map[string]any)["session/start"].(map[string]any)["params"] = map[string]any{"$ref": "#/$defs/Role"}
		}, nil, `methods.session/start.params.$ref: "#/$defs/StartParams" -> "#/$defs/Role"`},
		{"notification lost its params", func(b map[string]any) {
			delete(b["notifications"].(map[string]any)["session/closed"].(map[string]any), "params")
		}, nil, "notifications.session/closed.params: removed"},
		{"new arm in an anyOf", func(b map[string]any) {
			v := def(b, "Variants")
			v["anyOf"] = append(v["anyOf"].([]any), map[string]any{"type": "string"})
		}, nil, "$defs.Variants.anyOf: 2 arms -> 3"},
		{"new value in a closed enum", func(b map[string]any) {
			def(b, "Role")["enum"] = []any{"user", "assistant", "system"}
		}, nil, `$defs.Role.enum: new value "system" in a type the client decodes`},
		{"new value in an open enum the client decodes", func(b map[string]any) {
			def(b, "Reason")["enum"] = []any{"idle", "shutdown", "evicted"}
		}, nil, `$defs.Reason.enum: new value "evicted" in a type the client decodes`},
		{"value removed from an open enum", func(b map[string]any) {
			def(b, "Reason")["enum"] = []any{"idle"}
		}, nil, `$defs.Reason.enum: value "shutdown" removed`},
		{"enum closed", func(b map[string]any) {
			def(b, "Reason")["x-msp-openness"] = "closed"
		}, nil, `$defs.Reason.x-msp-openness: "open" -> "closed"`},
		{"removed error code", func(b map[string]any) {
			b["errors"] = []any{}
		}, nil, "errors[-32001]: removed"},
		{"changed error", func(b map[string]any) {
			b["errors"] = []any{map[string]any{"code": -32001, "kind": "notFound", "retryable": true}}
		}, nil, `errors[-32001]: {"code":-32001,"kind":"notFound","retryable":false} -> {"code":-32001,"kind":"notFound","retryable":true}`},
		{"removed capability", func(b map[string]any) {
			b["capabilities"].(map[string]any)["grantable"] = []any{}
		}, nil, "capabilities.grantable: fs.read removed"},
		{"unknown capabilities member", func(b map[string]any) {
			b["capabilities"].(map[string]any)["required"] = []any{"fs.read"}
		}, nil, `capabilities.required: null -> ["fs.read"]`},
		{"unknown keyword", func(b map[string]any) {
			props(b, "StartParams")["cwd"].(map[string]any)["format"] = "uri"
		}, nil, `$defs.StartParams.cwd.format: null -> "uri"`},
		{"unknown top-level member", func(b map[string]any) {
			b["extensions"] = map[string]any{"x": true}
		}, nil, "extensions: changed"},
		{"schema version", nil, func(m map[string]any) {
			m["schemaVersion"] = 2
		}, "manifest: schemaVersion 1 -> 2"},
		{"experimental export", nil, func(m map[string]any) {
			m["experimental"] = true
		}, "manifest: the export is experimental; the types are rendered from the stable surface"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := compareMutated(t, c.mutate, c.mutateMan)
			if r.Compatible() {
				t.Fatalf("a breaking change passed:\n%s", r)
			}
			found := false
			for _, b := range r.Breaks {
				if b == c.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("breaks = %q, want one of them to be %q", r.Breaks, c.want)
			}
		})
	}
}

// TestCompareIgnoresDescriptions keeps prose edits, which the vendor makes freely and which
// move the fingerprint, out of both lists.
func TestCompareIgnoresDescriptions(t *testing.T) {
	r := compareMutated(t, func(b map[string]any) {
		b["description"] = "reworded"
		def(b, "StartParams")["description"] = "new prose"
		props(b, "StartParams")["cwd"].(map[string]any)["description"] = "absolute path"
		b["methods"].(map[string]any)["session/start"].(map[string]any)["description"] = "starts a session"
	}, nil)
	if len(r.Additions) != 0 || len(r.Breaks) != 0 {
		t.Fatalf("description edits were reported:\n%s", r)
	}
}

// TestCompareIgnoresReservedCapabilities: the reserved list only carries references for names
// nothing produces, so rewording one is not a protocol change.
func TestCompareIgnoresReservedCapabilities(t *testing.T) {
	r := compareMutated(t, func(b map[string]any) {
		b["capabilities"].(map[string]any)["reserved"] = []any{map[string]any{"name": "rawLog", "reference": "#2"}}
	}, nil)
	if len(r.Additions) != 0 || len(r.Breaks) != 0 {
		t.Fatalf("a reserved-capability reference edit was reported:\n%s", r)
	}
}

// TestDefFlowsFollowRefs pins the direction each miniature type gets, including a type reached
// only through another type's property.
func TestDefFlowsFollowRefs(t *testing.T) {
	var b map[string]any
	if err := json.Unmarshal([]byte(baseBundle), &b); err != nil {
		t.Fatal(err)
	}
	got := defFlows(b)
	want := map[string]flow{"StartParams": toHost, "Mode": toHost, "StartResult": toClient, "Reason": toClient}
	for name, f := range want {
		if got[name] != f {
			t.Errorf("flow of %s = %d, want %d", name, got[name], f)
		}
	}
	for _, name := range []string{"Role", "Variants"} {
		if got[name] != 0 {
			t.Errorf("flow of unreferenced %s = %d, want 0 (Compare treats it as both ways)", name, got[name])
		}
	}
}

// TestCompareCheckedInBundleAgainstItself runs the real bundle through the checker, so a
// construct the miniature above does not have cannot make the checker fail on it.
func TestCompareCheckedInBundleAgainstItself(t *testing.T) {
	exp := t.TempDir()
	for _, name := range []string{"msp.schema.json", "manifest.json"} {
		b, err := os.ReadFile(filepath.Join("..", "schema", name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(exp, name), string(b))
	}
	r, err := Compare("..", exp)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Additions) != 0 || len(r.Breaks) != 0 {
		t.Fatalf("the bundle differs from itself:\n%s", r)
	}
}

func TestCompatReportString(t *testing.T) {
	r := &CompatReport{Additions: []string{"a"}, Breaks: []string{"b"}}
	if got, want := r.String(), "addition: a\nbreak: b\n"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if !strings.Contains(r.String(), "break: ") || r.Compatible() {
		t.Fatal("a report with a break must not be compatible")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
}

// compareRealMutated compares the checked-in bundle with a mutated copy of itself, so a rule
// keyed on a real type name is exercised where that type actually lives.
func compareRealMutated(t *testing.T, mutate func(b map[string]any)) *CompatReport {
	t.Helper()
	exp := t.TempDir()
	var b map[string]any
	raw, err := os.ReadFile(filepath.Join("..", "schema", "msp.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	mutate(b)
	writeJSON(t, filepath.Join(exp, "msp.schema.json"), b)
	man, err := os.ReadFile(filepath.Join("..", "schema", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(exp, "manifest.json"), string(man))
	r, err := Compare("..", exp)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func growEnum(t *testing.T, b map[string]any, name, value string) {
	t.Helper()
	d := def(b, name)
	if d == nil || d["enum"] == nil {
		t.Fatalf("the bundle has no enum %s", name)
	}
	d["enum"] = append(d["enum"].([]any), value)
}

// TestCompareCapabilityNameGrowthIsAnAddition: the client only tests CapabilityName for
// membership, so a capability it has never heard of is no reason to hold a pin.
func TestCompareCapabilityNameGrowthIsAnAddition(t *testing.T) {
	r := compareRealMutated(t, func(b map[string]any) { growEnum(t, b, "CapabilityName", "zzzNotInBundle") })
	if !r.Compatible() {
		t.Fatalf("a new CapabilityName value was judged breaking:\n%s", r)
	}
	want := []string{`$defs.CapabilityName.enum: new value "zzzNotInBundle" (the client only tests membership)`}
	if !reflect.DeepEqual(r.Additions, want) {
		t.Fatalf("additions = %q, want %q", r.Additions, want)
	}
}

// TestCompareDecodedEnumGrowthStillBreaks is the control for the exception above: the client
// switches on SessionStatus and ItemKind, and a value it cannot place must stay red.
func TestCompareDecodedEnumGrowthStillBreaks(t *testing.T) {
	for _, name := range []string{"SessionStatus", "ItemKind"} {
		t.Run(name, func(t *testing.T) {
			r := compareRealMutated(t, func(b map[string]any) { growEnum(t, b, name, "zzzNotInBundle") })
			want := []string{`$defs.` + name + `.enum: new value "zzzNotInBundle" in a type the client decodes`}
			if !reflect.DeepEqual(r.Breaks, want) {
				t.Fatalf("breaks = %q, want %q", r.Breaks, want)
			}
		})
	}
}
