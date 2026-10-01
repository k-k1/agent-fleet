package schemagen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// CompatReport is the verdict of Compare: what a newer schema export adds that the generated
// types can ignore, and what it changes that they cannot.
type CompatReport struct {
	// Additions are changes a client built from the older bundle survives: it never sends the
	// new things, and encoding/json drops members it has no field for.
	Additions []string
	// Breaks are changes that can make the older client send what the host rejects, or decode
	// what the host sends into the wrong shape.
	Breaks []string
}

// Compatible reports whether the older bundle's types still speak the newer schema.
func (r *CompatReport) Compatible() bool { return len(r.Breaks) == 0 }

// Compare checks the bundle under oldDir (a package directory holding schema/) against a
// fresh export in newDir (the --out directory of `muse schema generate-json-schema`, which
// holds msp.schema.json and manifest.json directly).
//
// The vendor's fingerprint moves on any change to the schema model, so equality turned every
// additive release into a red contract and a hand re-export before a pin could move. This is
// the rule that replaced it; the fingerprint is still carried, as a record of which export the
// types were rendered from.
//
// Most rules depend on which way a type travels: a new required member is harmless in a result
// the client only decodes, and fatal in params it has to fill. Anything this does not recognise
// is a break: a keyword it has no rule for is compared for equality, so an unfamiliar change
// fails closed rather than passing as an addition.
func Compare(oldDir, newDir string) (*CompatReport, error) {
	var oldMan, newMan manifest
	if err := readJSON(filepath.Join(oldDir, "schema", "manifest.json"), &oldMan); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(newDir, "manifest.json"), &newMan); err != nil {
		return nil, err
	}
	var oldB, newB map[string]any
	if err := readJSON(filepath.Join(oldDir, "schema", "msp.schema.json"), &oldB); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(newDir, "msp.schema.json"), &newB); err != nil {
		return nil, err
	}
	r := &CompatReport{}
	if oldMan.SchemaVersion != newMan.SchemaVersion {
		r.breakf("manifest: schemaVersion %d -> %d", oldMan.SchemaVersion, newMan.SchemaVersion)
	}
	if newMan.Experimental {
		r.breakf("manifest: the export is experimental; the types are rendered from the stable surface")
	}
	compareBundles(r, oldB, newB)
	sort.Strings(r.Additions)
	sort.Strings(r.Breaks)
	return r, nil
}

func (r *CompatReport) addf(format string, a ...any) {
	r.Additions = append(r.Additions, fmt.Sprintf(format, a...))
}

func (r *CompatReport) breakf(format string, a ...any) {
	r.Breaks = append(r.Breaks, fmt.Sprintf(format, a...))
}

// ignoredKeys never change what goes over the wire.
var ignoredKeys = map[string]bool{
	"description": true,
	"$comment":    true,
	"title":       true,
	"examples":    true,
}

// flow says which way a schema node travels. A type reachable both ways gets both rule sets.
type flow uint8

const (
	toHost   flow = 1 << iota // the client builds it: method params, server-request results
	toClient                  // the client decodes it: method results, notifications, server-request params
	bothWays = toHost | toClient
)

func compareBundles(r *CompatReport, oldB, newB map[string]any) {
	// Directions come from the old bundle alone: they describe what the client built from it
	// actually sends and reads. A path only the new schema has (a new notification, a new
	// optional member) is one the client never takes.
	flows := defFlows(oldB)
	defFlow := func(name string) flow {
		if f := flows[name]; f != 0 {
			return f
		}
		// Referenced by nothing today; it may be tomorrow, from either side.
		return bothWays
	}
	for _, k := range unionKeys(oldB, newB) {
		o, n := oldB[k], newB[k]
		switch k {
		case "$defs":
			compareNamed(r, "$defs", asMap(o), asMap(n),
				func(path string) { r.addf("%s: new type", path) },
				func(name, path string, ov, nv any) { compareNode(r, path, ov, nv, defFlow(name)) })
		case "methods":
			// The client picks which methods it calls, so one it has never heard of is unused.
			compareNamed(r, "methods", asMap(o), asMap(n),
				func(path string) { r.addf("%s: new method", path) },
				func(_, path string, ov, nv any) { compareEntry(r, path, ov, nv, toHost, toClient) })
		case "notifications":
			// The notification table is a decode map, not an allow-list: an unknown name is
			// ignored (TestUndeclaredNotificationIsIgnored in the muse driver).
			compareNamed(r, "notifications", asMap(o), asMap(n),
				func(path string) { r.addf("%s: new notification", path) },
				func(_, path string, ov, nv any) { compareEntry(r, path, ov, nv, toClient, toClient) })
		case "requests":
			// A server request waits for an answer the client does not know how to give.
			compareNamed(r, "requests", asMap(o), asMap(n),
				func(path string) { r.breakf("%s: new server request", path) },
				func(_, path string, ov, nv any) { compareEntry(r, path, ov, nv, toClient, toHost) })
		case "errors":
			compareErrors(r, o, n)
		case "capabilities":
			compareCapabilities(r, asMap(o), asMap(n))
		case "reserved", "description", "$schema":
			// Reserved identifiers have no producer, and the other two describe the file.
		default:
			if !equalIgnoring(o, n) {
				r.breakf("%s: changed", k)
			}
		}
	}
}

// defFlows reports, for every $defs entry reachable from an RPC table, which way it travels.
func defFlows(b map[string]any) map[string]flow {
	defs := asMap(b["$defs"])
	flows := map[string]flow{}
	var walk func(v any, f flow)
	walk = func(v any, f flow) {
		switch t := v.(type) {
		case map[string]any:
			if ref, ok := t["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/$defs/")
				if flows[name]&f != f {
					flows[name] |= f
					walk(defs[name], f)
				}
			}
			for _, e := range t {
				walk(e, f)
			}
		case []any:
			for _, e := range t {
				walk(e, f)
			}
		}
	}
	for _, table := range []struct {
		name           string
		params, result flow
	}{
		{"methods", toHost, toClient},
		{"notifications", toClient, toClient},
		{"requests", toClient, toHost},
	} {
		for _, e := range asMap(b[table.name]) {
			em := asMap(e)
			walk(em["params"], table.params)
			walk(em["result"], table.result)
		}
	}
	return flows
}

// compareNamed walks one of the bundle's name → schema tables.
func compareNamed(r *CompatReport, table string, o, n map[string]any, added func(path string), both func(name, path string, ov, nv any)) {
	for _, name := range unionKeys(o, n) {
		path := table + "." + name
		ov, inOld := o[name]
		nv, inNew := n[name]
		switch {
		case !inOld:
			added(path)
		case !inNew:
			r.breakf("%s: removed", path)
		default:
			both(name, path, ov, nv)
		}
	}
}

// compareEntry compares one RPC entry, whose params and result travel in the given directions.
func compareEntry(r *CompatReport, path string, o, n any, params, result flow) {
	om, nm := asMap(o), asMap(n)
	if om == nil || nm == nil {
		if !equalIgnoring(o, n) {
			r.breakf("%s: changed", path)
		}
		return
	}
	for _, k := range unionKeys(om, nm) {
		if ignoredKeys[k] {
			continue
		}
		ov, nv := om[k], nm[k]
		switch k {
		case "params", "result":
			if ov == nil || nv == nil {
				r.breakf("%s.%s: %s", path, k, presence(ov, nv))
				continue
			}
			f := params
			if k == "result" {
				f = result
			}
			compareNode(r, path+"."+k, ov, nv, f)
		default:
			if !equalIgnoring(ov, nv) {
				r.breakf("%s.%s: %s -> %s", path, k, compact(ov), compact(nv))
			}
		}
	}
}

// compareNode compares one schema node travelling in direction f.
func compareNode(r *CompatReport, path string, o, n any, f flow) {
	om, oIsMap := o.(map[string]any)
	nm, nIsMap := n.(map[string]any)
	if !oIsMap || !nIsMap {
		if !equalIgnoring(o, n) {
			r.breakf("%s: changed", path)
		}
		return
	}
	propsDone := false
	for _, k := range unionKeys(om, nm) {
		if ignoredKeys[k] {
			continue
		}
		ov, nv := om[k], nm[k]
		switch k {
		case "properties", "required":
			// Judged together, per property: whether a newly required name is a break
			// depends on whether the property itself is new.
			if !propsDone {
				compareProperties(r, path, om, nm, f)
				propsDone = true
			}
		case "enum":
			compareEnum(r, path, om, nm, f)
		case "items", "additionalProperties":
			if ov == nil || nv == nil {
				r.breakf("%s.%s: %s", path, k, presence(ov, nv))
				continue
			}
			compareNode(r, path+"."+k, ov, nv, f)
		case "anyOf", "oneOf", "allOf":
			oa, na := asSlice(ov), asSlice(nv)
			if len(oa) != len(na) {
				r.breakf("%s.%s: %d arms -> %d", path, k, len(oa), len(na))
				continue
			}
			for i := range oa {
				compareNode(r, fmt.Sprintf("%s.%s[%d]", path, k, i), oa[i], na[i], f)
			}
		default:
			if !equalIgnoring(ov, nv) {
				r.breakf("%s.%s: %s -> %s", path, k, compact(ov), compact(nv))
			}
		}
	}
}

func compareProperties(r *CompatReport, path string, om, nm map[string]any, f flow) {
	op, np := asMap(om["properties"]), asMap(nm["properties"])
	oReq, nReq := asSet(om["required"]), asSet(nm["required"])
	for _, name := range unionKeys(op, np) {
		p := path + "." + name
		ov, inOld := op[name]
		nv, inNew := np[name]
		switch {
		case !inOld && nReq[name] && f&toHost != 0:
			// The client does not send it, and the host requires it.
			r.breakf("%s: new required property", p)
		case !inOld && nReq[name]:
			// Only decoded: encoding/json drops a member the type has no field for.
			r.addf("%s: new required property (host to client only)", p)
		case !inOld:
			r.addf("%s: new optional property", p)
		case !inNew:
			// Whether it was sent or decoded, one side now has a member the other lost.
			r.breakf("%s: removed", p)
		default:
			switch {
			case !oReq[name] && nReq[name] && f&toHost != 0:
				// The client may leave it out, and the host now refuses that.
				r.breakf("%s: required false -> true", p)
			case oReq[name] && !nReq[name] && f&toClient != 0:
				// The client may rely on it, and the host may now leave it out.
				r.breakf("%s: required true -> false", p)
			case oReq[name] != nReq[name]:
				r.addf("%s: required %t -> %t (safe in this direction)", p, oReq[name], nReq[name])
			}
			compareNode(r, p, ov, nv, f)
		}
	}
	// A required name with no property behind it still binds the sender.
	for _, name := range sortedSet(nReq) {
		if _, declared := np[name]; !declared && !oReq[name] && f&toHost != 0 {
			r.breakf("%s.%s: newly required", path, name)
		}
	}
}

// compareEnum lets an enum grow only where the client never has to read the new value. The
// vendor marks most enums "open" and says readers must handle unknown values, but this client
// switches on known ones: an unknown ItemKind drops out of the transcript (transcript.go) and an
// unknown SessionStatus leaves the turn state where it was (handle.go onStatus). So growth in
// anything the client decodes is a break, open or not. The exceptions are membershipEnums.
func compareEnum(r *CompatReport, path string, om, nm map[string]any, f flow) {
	ov, nv := asSlice(om["enum"]), asSlice(nm["enum"])
	if ov == nil || nv == nil {
		r.breakf("%s.enum: %s", path, presence(om["enum"], nm["enum"]))
		return
	}
	oSet, nSet := map[string]bool{}, map[string]bool{}
	for _, v := range ov {
		oSet[compact(v)] = true
	}
	for _, v := range nv {
		nSet[compact(v)] = true
	}
	for _, v := range sortedSet(oSet) {
		if !nSet[v] {
			r.breakf("%s.enum: value %s removed", path, v)
		}
	}
	for _, v := range sortedSet(nSet) {
		switch {
		case oSet[v]:
		case f&toClient != 0 && membershipEnums[path]:
			r.addf("%s.enum: new value %s (the client only tests membership)", path, v)
		case f&toClient != 0:
			r.breakf("%s.enum: new value %s in a type the client decodes", path, v)
		default:
			r.addf("%s.enum: new value %s (host to client never carries it)", path, v)
		}
	}
}

// membershipEnums are decoded enums the client never switches on: it only asks whether a value
// it already knows is present (msp.Granted), so a value it has never heard of is one it ignores,
// and growth is an addition. Each entry must stay true of every reader in the client — a
// `switch` on CapabilityName anywhere puts it back under the strict rule. Without the entry,
// every new grantable capability (1.4.2's `feedback`) is a red contract and a held pin.
var membershipEnums = map[string]bool{
	"$defs.CapabilityName": true,
}

// compareErrors keys the error table by code: a new code is one the client reports generically,
// a moved or removed one is one it may be matching on.
func compareErrors(r *CompatReport, o, n any) {
	byCode := func(v any) map[string]any {
		out := map[string]any{}
		for _, e := range asSlice(v) {
			if m, ok := e.(map[string]any); ok {
				out[compact(m["code"])] = m
			}
		}
		return out
	}
	oc, nc := byCode(o), byCode(n)
	for _, code := range unionKeys(oc, nc) {
		ov, inOld := oc[code]
		nv, inNew := nc[code]
		switch {
		case !inOld:
			r.addf("errors[%s]: new error code", code)
		case !inNew:
			r.breakf("errors[%s]: removed", code)
		case !equalIgnoring(ov, nv):
			r.breakf("errors[%s]: %s -> %s", code, compact(ov), compact(nv))
		}
	}
}

// compareCapabilities compares the grantable names as a set: the client asks for capabilities
// by name, so one that disappears is one it can no longer be granted. The reserved list names
// identifiers with no producer and carries only references, so it is skipped; any other member
// has no rule and must not move.
func compareCapabilities(r *CompatReport, o, n map[string]any) {
	for _, k := range unionKeys(o, n) {
		switch k {
		case "grantable":
			oSet, nSet := asSet(o[k]), asSet(n[k])
			for _, v := range sortedSet(oSet) {
				if !nSet[v] {
					r.breakf("capabilities.grantable: %s removed", v)
				}
			}
			for _, v := range sortedSet(nSet) {
				if !oSet[v] {
					r.addf("capabilities.grantable: %s added", v)
				}
			}
		case "reserved":
		default:
			if !equalIgnoring(o[k], n[k]) {
				r.breakf("capabilities.%s: %s -> %s", k, compact(o[k]), compact(n[k]))
			}
		}
	}
}

// equalIgnoring compares two JSON values with the wire-inert keys removed at every depth.
func equalIgnoring(a, b any) bool {
	return reflect.DeepEqual(stripIgnored(a), stripIgnored(b))
}

func stripIgnored(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if !ignoredKeys[k] {
				out[k] = stripIgnored(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = stripIgnored(e)
		}
		return out
	}
	return v
}

func presence(o, n any) string {
	switch {
	case o == nil && n != nil:
		return "added"
	case o != nil && n == nil:
		return "removed"
	}
	return "changed"
}

func compact(v any) string {
	b, err := json.Marshal(stripIgnored(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asSet(v any) map[string]bool {
	out := map[string]bool{}
	for _, e := range asSlice(v) {
		if s, ok := e.(string); ok {
			out[s] = true
		}
	}
	return out
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return sortedSet(seen)
}

func sortedSet(s map[string]bool) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String renders the report the way the contract workflow prints it.
func (r *CompatReport) String() string {
	var b strings.Builder
	for _, a := range r.Additions {
		fmt.Fprintf(&b, "addition: %s\n", a)
	}
	for _, x := range r.Breaks {
		fmt.Fprintf(&b, "break: %s\n", x)
	}
	return b.String()
}
