// Package schemagen renders an MSP schema bundle into the msp package's Go wire types.
//
// Muse Session Protocol v1 is a published product surface, not a debug seam: the vendor
// exports the whole schema offline (`muse schema generate-json-schema`) and stamps it with a
// fingerprint. Hand-writing 234 types against that would rot silently on the next release, so
// the types are generated and the fingerprint is carried into the generated file, where
// msp's fingerprint_test.go compares it against the binary's own export (ADR 0095
// consequences, "drift has a lock").
//
// It is a package rather than a bare `main` so the drift test can call Render directly: the
// check that types_gen.go is what this generator produces today has to be hermetic, and
// shelling out to `go run` in a test is neither fast nor available everywhere.
package schemagen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// schemaBundle is the subset of the export this generator consumes. The remaining top-level
// members (capabilities, reserved) name things a v1 client neither sends nor decodes.
type schemaBundle struct {
	Defs          map[string]*node     `json:"$defs"`
	Methods       map[string]*rpcEntry `json:"methods"`
	Notifications map[string]*rpcEntry `json:"notifications"`
	Requests      map[string]*rpcEntry `json:"requests"`
	Errors        []errEntry           `json:"errors"`
}

type rpcEntry struct {
	Description string `json:"description"`
	Params      *node  `json:"params"`
	Result      *node  `json:"result"`
}

type errEntry struct {
	Code          int      `json:"code"`
	Kind          string   `json:"kind"`
	OverrideKinds []string `json:"overrideKinds"`
	Retryable     bool     `json:"retryable"`
}

// node is one JSON Schema node. Type is `string` or `[]string` in the export, so it is decoded
// late; everything else the bundle uses is a plain member.
type node struct {
	Ref                  string           `json:"$ref"`
	Description          string           `json:"description"`
	Type                 json.RawMessage  `json:"type"`
	Enum                 []string         `json:"enum"`
	Properties           map[string]*node `json:"properties"`
	Required             []string         `json:"required"`
	Items                *node            `json:"items"`
	AnyOf                []*node          `json:"anyOf"`
	OneOf                []*node          `json:"oneOf"`
	AdditionalProperties *node            `json:"additionalProperties"`
}

// types reports the node's declared JSON types, normalising the string and []string spellings.
func (n *node) types() []string {
	if len(n.Type) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(n.Type, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(n.Type, &many) == nil {
		return many
	}
	return nil
}

// nullable reports whether the node admits an explicit JSON null, in either of the two
// spellings the export uses: `"type": ["string", "null"]` and an `anyOf` arm of type null.
// The distinction matters because MSP states that omitted and explicit null select the same
// default for several params, so both map to the same Go pointer.
func (n *node) nullable() bool {
	for _, t := range n.types() {
		if t == "null" {
			return true
		}
	}
	for _, a := range n.AnyOf {
		for _, t := range a.types() {
			if t == "null" {
				return true
			}
		}
	}
	return false
}

// manifest is the schema bundle's sidecar: the fingerprint the vendor computes over its own
// schema model. It is the value the drift test compares, not a hash of the JSON file (the
// rendered file is not byte-stable across releases that do not change the model).
type manifest struct {
	Experimental  bool   `json:"experimental"`
	Fingerprint   string `json:"fingerprint"`
	SchemaVersion int    `json:"schemaVersion"`
}

// Render reads the schema bundle under pkgDir/schema and returns the formatted contents of
// pkgDir/types_gen.go.
func Render(pkgDir string) ([]byte, error) {
	var bundle schemaBundle
	if err := readJSON(filepath.Join(pkgDir, "schema", "msp.schema.json"), &bundle); err != nil {
		return nil, err
	}
	var man manifest
	if err := readJSON(filepath.Join(pkgDir, "schema", "manifest.json"), &man); err != nil {
		return nil, err
	}
	return render(&bundle, &man)
}

// Fingerprint reports the bundle's own fingerprint, for the drift test's error message.
func Fingerprint(pkgDir string) (string, error) {
	var man manifest
	if err := readJSON(filepath.Join(pkgDir, "schema", "manifest.json"), &man); err != nil {
		return "", err
	}
	return man.Fingerprint, nil
}

// Write renders and overwrites pkgDir/types_gen.go.
func Write(pkgDir string) error {
	src, err := Render(pkgDir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pkgDir, "types_gen.go"), src, 0o644)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func render(b *schemaBundle, man *manifest) ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by ./gen from schema/msp.schema.json. DO NOT EDIT.\n\n")
	fmt.Fprintf(&out, "package msp\n\n")
	fmt.Fprintf(&out, "import \"encoding/json\"\n\n")

	writeHeaderConsts(&out, man)
	writeRPCNames(&out, "Method", b.Methods)
	writeRPCNames(&out, "Notification", b.Notifications)
	writeRPCNames(&out, "ServerRequest", b.Requests)
	writeErrorCodes(&out, b.Errors)
	writePayloadMap(&out, b)

	names := make([]string, 0, len(b.Defs))
	for n := range b.Defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := writeDef(&out, b, goName(n), b.Defs[n]); err != nil {
			return nil, err
		}
	}

	src, err := format.Source(out.Bytes())
	if err != nil {
		// A gofmt failure here is a generator bug, and the only way to find it is to read
		// the line it names, so the unformatted source travels with the error.
		return nil, fmt.Errorf("gofmt: %w\n--- unformatted output ---\n%s", err, out.Bytes())
	}
	return src, nil
}

func writeHeaderConsts(out *bytes.Buffer, man *manifest) {
	fmt.Fprintf(out, "// SchemaFingerprint is the vendor's fingerprint of the schema model these types were\n")
	fmt.Fprintf(out, "// rendered from. fingerprint_test.go asserts the installed binary still exports it; a\n")
	fmt.Fprintf(out, "// mismatch means the wire moved under us, which is a red build rather than a silent\n")
	fmt.Fprintf(out, "// decode failure at runtime.\n")
	fmt.Fprintf(out, "const SchemaFingerprint = %q\n\n", man.Fingerprint)
	fmt.Fprintf(out, "// SchemaVersion is MSP's own version, carried in `initialize`.\n")
	fmt.Fprintf(out, "const SchemaVersion = %d\n\n", man.SchemaVersion)
}

func writeRPCNames(out *bytes.Buffer, kind string, entries map[string]*rpcEntry) {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "// %s names on the wire.\nconst (\n", kind)
	for _, n := range names {
		fmt.Fprintf(out, "\t%s%s = %q\n", kind, goName(strings.NewReplacer("/", "_", ".", "_").Replace(n)), n)
	}
	fmt.Fprintf(out, ")\n\n")
}

func writeErrorCodes(out *bytes.Buffer, errs []errEntry) {
	sorted := append([]errEntry(nil), errs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Kind < sorted[j].Kind })
	fmt.Fprintf(out, "// Error codes, by the schema's own `kind` name.\nconst (\n")
	for _, e := range sorted {
		fmt.Fprintf(out, "\tErrCode%s = %d\n", goName(e.Kind), e.Code)
	}
	fmt.Fprintf(out, ")\n\n")

	fmt.Fprintf(out, "// retryableErrorCodes is the schema's own retryable set. A client that retries on\n")
	fmt.Fprintf(out, "// anything else is guessing; the schema says which failures are transport-level.\n")
	fmt.Fprintf(out, "var retryableErrorCodes = map[int]bool{\n")
	for _, e := range sorted {
		if e.Retryable {
			fmt.Fprintf(out, "\t%d: true,\n", e.Code)
		}
	}
	fmt.Fprintf(out, "}\n\n")
}

// writePayloadMap emits the notification name to params type mapping the dispatcher needs.
// Generated rather than hand-kept: a notification added in 1.4 that nobody maps is exactly the
// drift the fingerprint test catches, and this table is what makes the catch actionable.
func writePayloadMap(out *bytes.Buffer, b *schemaBundle) {
	names := make([]string, 0, len(b.Notifications))
	for n := range b.Notifications {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "// notificationParams names the params type of every notification the schema declares.\n")
	fmt.Fprintf(out, "// The value is the Go type name, for the decode table in notify.go.\n")
	fmt.Fprintf(out, "var notificationParams = map[string]string{\n")
	for _, n := range names {
		p := b.Notifications[n].Params
		t := ""
		if p != nil {
			t = refName(p.Ref)
		}
		fmt.Fprintf(out, "\t%q: %q,\n", n, t)
	}
	fmt.Fprintf(out, "}\n\n")
}

func writeDef(out *bytes.Buffer, b *schemaBundle, name string, n *node) error {
	doc(out, name, n.Description)
	switch {
	case len(n.Enum) > 0:
		fmt.Fprintf(out, "type %s string\n\n", name)
		fmt.Fprintf(out, "const (\n")
		for _, v := range n.Enum {
			fmt.Fprintf(out, "\t%s%s %s = %q\n", name, goName(v), name, v)
		}
		fmt.Fprintf(out, ")\n\n")
		// The values as a list, in the bundle's own order. A caller that has to OFFER an
		// enum rather than recognize one — the Console's reasoning-effort picker is the
		// first — would otherwise hand-keep a second copy, and a value the vendor adds in
		// 1.4 would silently never be offered. Generated, it moves with the bundle and the
		// drift lock covers it.
		fmt.Fprintf(out, "// %sValues are every %s the bundle declares, in schema order.\n", name, name)
		fmt.Fprintf(out, "var %sValues = []%s{\n", name, name)
		for _, v := range n.Enum {
			fmt.Fprintf(out, "\t%s%s,\n", name, goName(v))
		}
		fmt.Fprintf(out, "}\n\n")
		return nil
	case len(n.OneOf) > 0:
		// The only union in v1 is SessionMcpServerConfig, a closed union discriminated by
		// `transport`. AF writes it and never decodes it, so a flat struct carrying every
		// arm's members — with the discriminator required — is both sufficient and the
		// shape that makes an invalid combination obvious at the call site.
		return writeFlatUnion(out, name, n)
	case len(n.Properties) > 0:
		return writeStruct(out, b, name, n)
	case isObject(n):
		// A named object with no members is a real wire value, not free-form JSON: the
		// presentation receipt has to go out as `{}`. Rendering it the way an untyped
		// `properties: {}` PROPERTY is rendered — json.RawMessage — would marshal the zero
		// value as `""`, which is not an object and is not what the schema declares.
		fmt.Fprintf(out, "type %s struct{}\n\n", name)
		return nil
	case len(n.types()) > 1:
		// RequestId: integer|string. Carried verbatim so an id round-trips in the spelling
		// the peer chose — the schema is explicit that 1 and "1" are different ids.
		fmt.Fprintf(out, "type %s = json.RawMessage\n\n", name)
		return nil
	default:
		fmt.Fprintf(out, "type %s %s\n\n", name, scalarType(n))
		return nil
	}
}

func writeFlatUnion(out *bytes.Buffer, name string, n *node) error {
	merged := map[string]*node{}
	required := map[string]int{}
	for _, arm := range n.OneOf {
		for pn, p := range arm.Properties {
			if _, seen := merged[pn]; !seen {
				merged[pn] = p
			}
		}
		for _, r := range arm.Required {
			required[r]++
		}
	}
	fmt.Fprintf(out, "type %s struct {\n", name)
	for _, pn := range sortedKeys(merged) {
		// Only a member required by every arm is unconditionally required; the rest belong
		// to one transport and must stay omitempty.
		req := required[pn] == len(n.OneOf)
		writeField(out, nil, pn, merged[pn], req)
	}
	fmt.Fprintf(out, "}\n\n")
	return nil
}

func writeStruct(out *bytes.Buffer, b *schemaBundle, name string, n *node) error {
	req := map[string]bool{}
	for _, r := range n.Required {
		req[r] = true
	}
	fmt.Fprintf(out, "type %s struct {\n", name)
	for _, pn := range sortedKeys(n.Properties) {
		writeField(out, b, pn, n.Properties[pn], req[pn])
	}
	fmt.Fprintf(out, "}\n\n")
	return nil
}

func writeField(out *bytes.Buffer, b *schemaBundle, jsonName string, p *node, required bool) {
	typ := goType(p)
	tag := jsonName
	// A required, non-nullable member is always on the wire, so it stays a value and keeps
	// its zero value meaningful. Everything else is a pointer or a nil-able type with
	// omitempty, because MSP distinguishes "omitted" from "present and zero" in several
	// places (usedPercent 0 and an absent quota are not the same answer).
	optional := !required || p.nullable()
	if optional {
		tag += ",omitempty"
		if needsPointer(typ) {
			typ = "*" + typ
		}
	}
	if d := p.Description; d != "" {
		for _, line := range wrapComment(d, 88) {
			fmt.Fprintf(out, "\t// %s\n", line)
		}
	}
	fmt.Fprintf(out, "\t%s %s `json:%q`\n", goName(jsonName), typ, tag)
}

// needsPointer reports whether an optional field of this type needs one. Slices, maps and
// json.RawMessage already have a nil that omitempty understands; a struct or a scalar does not.
func needsPointer(typ string) bool {
	return !strings.HasPrefix(typ, "[]") && !strings.HasPrefix(typ, "map[") && typ != "json.RawMessage"
}

func goType(p *node) string {
	if p.Ref != "" {
		return refName(p.Ref)
	}
	if len(p.AnyOf) > 0 {
		// Every anyOf in v1 is "this, or null"; the null arm is already handled by
		// nullable() making the field a pointer, so the type is the other arm's.
		for _, a := range p.AnyOf {
			if len(a.types()) == 1 && a.types()[0] == "null" {
				continue
			}
			return goType(a)
		}
	}
	for _, t := range p.types() {
		switch t {
		case "null":
			continue
		case "array":
			if p.Items == nil {
				return "[]json.RawMessage"
			}
			return "[]" + goType(p.Items)
		case "object":
			if p.AdditionalProperties != nil {
				return "map[string]" + goType(p.AdditionalProperties)
			}
			// A declared-but-empty property set is the schema's spelling for free-form
			// JSON (`Notification.params`, `SuccessResponse.result`): keep it verbatim so
			// the dispatcher can decode it into the type the method names.
			return "json.RawMessage"
		default:
			return scalarType(p)
		}
	}
	return "json.RawMessage"
}

func scalarType(p *node) string {
	for _, t := range p.types() {
		switch t {
		case "string":
			return "string"
		case "integer":
			return "int64"
		case "number":
			return "float64"
		case "boolean":
			return "bool"
		}
	}
	return "json.RawMessage"
}

// refName renders a `#/$defs/X` reference as the Go type name X is generated under. Schema
// names are already PascalCase, but they spell initialisms the JSON way (`RequestId`,
// `SessionMcpServerConfig`), so they go through the same normaliser as field names — otherwise
// the package would carry `RequestId` next to a `RequestID` field.
func refName(ref string) string {
	return goName(strings.TrimPrefix(ref, "#/$defs/"))
}

func doc(out *bytes.Buffer, name, desc string) {
	if desc == "" {
		return
	}
	lines := wrapComment(desc, 88)
	for i, l := range lines {
		if i == 0 {
			fmt.Fprintf(out, "// %s %s\n", name, lowerFirstWord(l, name))
			continue
		}
		fmt.Fprintf(out, "// %s\n", l)
	}
}

// lowerFirstWord keeps Go's "Name ..." doc convention without duplicating the identifier when
// the vendor's own sentence already opens with it.
func lowerFirstWord(line, name string) string {
	if strings.HasPrefix(line, "`"+name+"`") {
		return strings.TrimSpace(strings.TrimPrefix(line, "`"+name+"`"))
	}
	return line
}

func wrapComment(s string, width int) []string {
	s = strings.ReplaceAll(s, "\n", " ")
	var lines []string
	var cur strings.Builder
	for _, w := range strings.Fields(s) {
		if cur.Len() > 0 && cur.Len()+1+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// initialisms are the words Go spells in caps. Keep this list in sync with what the schema
// actually uses: a name this misses compiles fine but reads wrong, and renaming an exported
// type later is a breaking change inside the package.
var initialisms = map[string]string{
	"id": "ID", "ids": "IDs", "url": "URL", "uri": "URI", "api": "API",
	"mcp": "MCP", "json": "JSON", "rpc": "RPC", "http": "HTTP", "os": "OS",
	"cwd": "Cwd", "ms": "Ms", "ttl": "TTL", "sha256": "SHA256", "utf8": "UTF8",
}

// goName renders a wire name (camelCase, kebab-case, snake_case or a slashed method name) as
// an exported Go identifier.
func goName(s string) string {
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '-' || r == '/' || r == '.' || r == ' ':
			flush()
		case r >= 'A' && r <= 'Z':
			// A capital starts a word unless it continues a run of capitals (SHA256).
			if i > 0 && cur.Len() > 0 {
				prev := rune(s[i-1])
				if !(prev >= 'A' && prev <= 'Z') {
					flush()
				}
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	var b strings.Builder
	for _, p := range parts {
		if up, ok := initialisms[strings.ToLower(p)]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// isObject reports whether the node declares exactly the JSON object type.
func isObject(n *node) bool {
	t := n.types()
	return len(t) == 1 && t[0] == "object"
}
