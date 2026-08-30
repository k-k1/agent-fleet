package projcfg

// editjson.go — format-preserving edits to "one JSON object holding a map of
// entries" (docs/log/56 §6 / docs/log/57 憲章4): the shape every v1 project MCP file uses
// except codex's TOML (.mcp.json / opencode.json / .cursor/mcp.json /
// .github/mcp.json / .kiro/settings/mcp.json). json.MarshalIndent over the whole
// file would turn a one-entry change into an all-lines diff — unreviewable, and
// per docs/log/56 §6 "これを守れない道具は使われない". So every edit here touches only
// the byte range of the entry actually changing; everything else in the file is
// copied through untouched.
//
// This is a narrow, hand-written scanner rather than a general JSON editing
// library — the same convention the codebase already uses for codex's TOML
// (materialize_codex.go's line-based editor): the input is already known-valid
// JSON (caller checked Parsable before calling), so this only needs to find byte
// SPANS, not validate syntax. It targets exactly the shape af's own writers
// produce and every target CLI reads: a root object, one member holding a flat map
// of "name": {...} entries, pretty-printed one entry per line. A fully compact
// (single-line) source file is detected and handled by falling back to compact
// serialization for the changed value only, rather than injecting indentation
// nothing else in the file has.

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UpsertJSONEntry inserts or replaces entryName's value under containerKey in src
// (a whole file's bytes), preserving every other byte. If containerKey itself is
// absent from the root object, it is added holding just this one entry. newValue
// is reindented to match the surrounding file (or serialized compactly if the
// source file itself has no newlines).
func UpsertJSONEntry(src []byte, containerKey, entryName string, newValue map[string]any) ([]byte, error) {
	rootStart := skipWS(src, 0)
	if rootStart >= len(src) || src[rootStart] != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	rootMembers, rootEnd, err := scanObjectMembers(src, rootStart)
	if err != nil {
		return nil, err
	}
	compact := !bytes.Contains(src, []byte("\n"))

	contIdx, err := findMemberIndex(src, rootMembers, containerKey)
	if err != nil {
		return nil, err
	}
	if contIdx < 0 {
		// containerKey itself is missing: build it as a fresh object holding just
		// this one entry, and insert THAT as a new root member. json.MarshalIndent
		// handles the nesting correctly on its own — no separate two-level logic
		// needed. -1: the root object has no owning key of its own.
		return insertMember(src, rootStart, rootEnd, rootMembers, containerKey,
			map[string]any{entryName: newValue}, compact, -1)
	}

	cont := rootMembers[contIdx]
	members, contObjEnd, err := scanObjectMembers(src, cont.valStart)
	if err != nil {
		return nil, err
	}
	idx, err := findMemberIndex(src, members, entryName)
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		return insertMember(src, cont.valStart, contObjEnd, members, entryName, newValue, compact, cont.keyStart)
	}
	m := members[idx]
	valueJSON, err := serializeValue(src, m.keyStart, newValue, compact)
	if err != nil {
		return nil, err
	}
	return spliceIn(src, m.valStart, m.valEnd, []byte(valueJSON)), nil
}

// DeleteJSONEntry removes entryName from containerKey's map in src. If it was the
// last remaining entry, containerKey itself is removed from the root object
// entirely (mirroring materialize_json.go: "no key at all, rather than an empty
// object af introduced"). A no-op (returns src unchanged) if the entry — or the
// container itself — is not present.
func DeleteJSONEntry(src []byte, containerKey, entryName string) ([]byte, error) {
	rootStart := skipWS(src, 0)
	if rootStart >= len(src) || src[rootStart] != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	rootMembers, rootEnd, err := scanObjectMembers(src, rootStart)
	if err != nil {
		return nil, err
	}
	contIdx, err := findMemberIndex(src, rootMembers, containerKey)
	if err != nil || contIdx < 0 {
		return src, err
	}
	cont := rootMembers[contIdx]
	members, contObjEnd, err := scanObjectMembers(src, cont.valStart)
	if err != nil {
		return nil, err
	}
	idx, err := findMemberIndex(src, members, entryName)
	if err != nil || idx < 0 {
		return src, err
	}
	if len(members) == 1 {
		return removeMember(src, rootStart, rootEnd, rootMembers, contIdx), nil
	}
	return removeMember(src, cont.valStart, contObjEnd, members, idx), nil
}

// --- member scan -------------------------------------------------------------

// jmember is one direct "key": value pair inside a JSON object, as a set of byte
// offsets into the original source.
type jmember struct {
	keyStart int // index of the key's opening quote
	keyEnd   int // index right after the key's closing quote
	valStart int // index of the value's first byte
	valEnd   int // index right after the value's last byte
	commaPos int // index of the trailing comma after this member, or -1 (last member)
}

// scanObjectMembers scans the object whose opening '{' is at src[objStart],
// returning its direct members in source order and the index of the matching '}'.
func scanObjectMembers(src []byte, objStart int) (members []jmember, objEnd int, err error) {
	if objStart >= len(src) || src[objStart] != '{' {
		return nil, 0, fmt.Errorf("not an object at byte %d", objStart)
	}
	i := objStart + 1
	for {
		i = skipWS(src, i)
		if i >= len(src) {
			return nil, 0, fmt.Errorf("unterminated object starting at byte %d", objStart)
		}
		if src[i] == '}' {
			return members, i, nil
		}
		if src[i] != '"' {
			return nil, 0, fmt.Errorf("expected an object key at byte %d", i)
		}
		keyStart := i
		keyEnd, err := scanString(src, i)
		if err != nil {
			return nil, 0, err
		}
		i = skipWS(src, keyEnd)
		if i >= len(src) || src[i] != ':' {
			return nil, 0, fmt.Errorf("expected ':' at byte %d", i)
		}
		i = skipWS(src, i+1)
		valStart := i
		valEnd, err := scanValue(src, i)
		if err != nil {
			return nil, 0, err
		}
		j := skipWS(src, valEnd)
		commaPos := -1
		if j < len(src) && src[j] == ',' {
			commaPos = j
			i = j + 1
		} else {
			i = valEnd
		}
		members = append(members, jmember{keyStart, keyEnd, valStart, valEnd, commaPos})
	}
}

func findMemberIndex(src []byte, members []jmember, key string) (int, error) {
	for i, m := range members {
		var k string
		if err := json.Unmarshal(src[m.keyStart:m.keyEnd], &k); err != nil {
			return 0, err
		}
		if k == key {
			return i, nil
		}
	}
	return -1, nil
}

func skipWS(src []byte, i int) int {
	for i < len(src) {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString returns the index right after the closing quote of the string
// starting at src[i] (src[i] must be '"').
func scanString(src []byte, i int) (int, error) {
	if i >= len(src) || src[i] != '"' {
		return 0, fmt.Errorf("expected a string at byte %d", i)
	}
	i++
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1, nil
		default:
			i++
		}
	}
	return 0, fmt.Errorf("unterminated string")
}

// scanValue returns the index right after the JSON value starting at src[i].
func scanValue(src []byte, i int) (int, error) {
	if i >= len(src) {
		return 0, fmt.Errorf("unexpected end of input")
	}
	switch src[i] {
	case '"':
		return scanString(src, i)
	case '{':
		return scanBracketed(src, i, '{', '}')
	case '[':
		return scanBracketed(src, i, '[', ']')
	default:
		// number / true / false / null: run to the next structural delimiter.
		// Depth-1 correctness (not full validation) is all that is needed — the
		// caller already confirmed the whole file parses as valid JSON.
		j := i
		for j < len(src) {
			switch src[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				if j == i {
					return 0, fmt.Errorf("empty value at byte %d", i)
				}
				return j, nil
			}
			j++
		}
		if j == i {
			return 0, fmt.Errorf("empty value at byte %d", i)
		}
		return j, nil
	}
}

// scanBracketed finds the byte right after the closing bracket matching the one
// at src[i] (open/close is '{'/'}' or '['/']'), tracking only THAT bracket pair's
// depth. A same-type bracket nested inside the other type (an array inside this
// object, say) is still counted correctly because JSON brackets always nest
// properly in valid input.
func scanBracketed(src []byte, i int, open, close byte) (int, error) {
	if i >= len(src) || src[i] != open {
		return 0, fmt.Errorf("expected %q at byte %d", open, i)
	}
	depth := 0
	j := i
	for j < len(src) {
		c := src[j]
		if c == '"' {
			end, err := scanString(src, j)
			if err != nil {
				return 0, err
			}
			j = end
			continue
		}
		switch c {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j + 1, nil
			}
		}
		j++
	}
	return 0, fmt.Errorf("unterminated %q starting at byte %d", open, i)
}

// --- indent detection ----------------------------------------------------------

// detectLineIndent returns the whitespace prefix of the line containing byte
// offset pos. "" if that prefix is not pure whitespace (pos is not the first
// token on its line — e.g. a fully compact file), so a caller never mistakes
// arbitrary file content for an indent string.
func detectLineIndent(src []byte, pos int) string {
	start := lineStartOf(src, pos)
	prefix := src[start:pos]
	for _, c := range prefix {
		if c != ' ' && c != '\t' {
			return ""
		}
	}
	return string(prefix)
}

func lineStartOf(src []byte, pos int) int {
	idx := bytes.LastIndexByte(src[:pos], '\n')
	return idx + 1
}

// detectIndentUnit returns the leading whitespace of the first indented line in
// src, as a best-effort "one nesting level" unit. Falls back to two spaces
// (jsonConfig's own MarshalIndent unit, materialize_json.go) when nothing
// indented is found.
func detectIndentUnit(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || len(trimmed) == len(line) {
			continue
		}
		return string(line[:len(line)-len(trimmed)])
	}
	return "  "
}

// serializeValue renders newValue as JSON, indented to align under keyPos's own
// line (so it reads as that line's continuation), or compactly when compact is
// set (a single-line source file has no indent style to match).
func serializeValue(src []byte, keyPos int, newValue map[string]any, compact bool) (string, error) {
	if compact {
		b, err := json.Marshal(newValue)
		return string(b), err
	}
	prefix := detectLineIndent(src, keyPos)
	b, err := json.MarshalIndent(newValue, prefix, detectIndentUnit(src))
	return string(b), err
}

// --- splicing --------------------------------------------------------------

func spliceIn(src []byte, start, end int, ins []byte) []byte {
	out := make([]byte, 0, len(src)+len(ins)-(end-start))
	out = append(out, src[:start]...)
	out = append(out, ins...)
	out = append(out, src[end:]...)
	return out
}

// insertMember appends a new "key": newValue member as the LAST member of the
// object at [objStart,objEnd) (objStart points at '{', objEnd at the matching
// '}'), reindented to match an existing sibling member when there is one, or
// derived from ownerKeyStart (the position of the KEY that owns this object —
// e.g. "mcpServers"'s own key — or -1 for the root object, which has none) when
// the object currently has no members to copy from.
func insertMember(src []byte, objStart, objEnd int, members []jmember, key string, newValue map[string]any, compact bool, ownerKeyStart int) ([]byte, error) {
	keyJSON, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	if compact {
		valJSON, err := json.Marshal(newValue)
		if err != nil {
			return nil, err
		}
		sep := ""
		if len(members) > 0 {
			sep = ","
		}
		ins := []byte(sep + string(keyJSON) + ":" + string(valJSON))
		if len(members) == 0 {
			return spliceIn(src, objStart+1, objEnd, ins), nil
		}
		last := members[len(members)-1]
		return spliceIn(src, last.valEnd, last.valEnd, ins), nil
	}

	unit := detectIndentUnit(src)
	// objIndent — the OWNER's own line indent, one level shallower than an entry
	// — is "" for the root object (ownerKeyStart<0, no owning key).
	var objIndent string
	if ownerKeyStart >= 0 {
		objIndent = detectLineIndent(src, ownerKeyStart)
	}
	var entryIndent string
	if len(members) > 0 {
		// Copy an existing sibling's own indent exactly — more robust than
		// re-deriving it, and correct even if the object's members are not all
		// indented from a level this function could otherwise infer.
		entryIndent = detectLineIndent(src, members[0].keyStart)
	} else {
		entryIndent = objIndent + unit
	}
	valJSON, err := json.MarshalIndent(newValue, entryIndent, unit)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		// The closing '}' sits at objIndent — one level shallower than the entry.
		ins := []byte("\n" + entryIndent + string(keyJSON) + ": " + string(valJSON) + "\n" + objIndent)
		return spliceIn(src, objStart+1, objEnd, ins), nil
	}
	last := members[len(members)-1]
	ins := []byte(",\n" + entryIndent + string(keyJSON) + ": " + string(valJSON))
	return spliceIn(src, last.valEnd, last.valEnd, ins), nil
}

// removeMember deletes members[idx] from the object at [objStart,objEnd). When it
// is the only member, the object's interior collapses to empty rather than
// leaving stray whitespace.
func removeMember(src []byte, objStart, objEnd int, members []jmember, idx int) []byte {
	if len(members) == 1 {
		return spliceOut(src, objStart+1, objEnd)
	}
	m := members[idx]
	if idx < len(members)-1 {
		start := lineStartOf(src, m.keyStart)
		end := m.valEnd
		if m.commaPos >= 0 {
			end = m.commaPos + 1
		}
		end = consumeOneNewline(src, end)
		return spliceOut(src, start, end)
	}
	// The member becoming last has no comma of its own to remove; instead the
	// PREVIOUS member's trailing comma must go, so the new last member ends up
	// with none (a trailing comma before '}' is invalid JSON).
	prev := members[idx-1]
	return spliceOut(src, prev.commaPos, m.valEnd)
}

func spliceOut(src []byte, start, end int) []byte {
	out := make([]byte, 0, len(src)-(end-start))
	out = append(out, src[:start]...)
	out = append(out, src[end:]...)
	return out
}

func consumeOneNewline(src []byte, i int) int {
	if i < len(src) && src[i] == '\r' {
		i++
	}
	if i < len(src) && src[i] == '\n' {
		i++
	}
	return i
}
