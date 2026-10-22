package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"testing"
)

var toolNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// A strict client (measured: ChatGPT) drops the whole server when one inputSchema is
// not valid JSON Schema, so every tool a principal can see is checked on the wire form.
func TestToolsListSchemasAreValidJSONSchema(t *testing.T) {
	st := memStore(t)
	ctx := context.Background()
	cp := newMemCP(t, st, "running")
	tn := cp.tenantID

	mk := func(email, key, hint, role string) mcpPrincipal {
		id, err := st.UpsertIdentity(ctx, email, key, hint)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, tn, role)
		if err != nil {
			t.Fatal(err)
		}
		return mcpPrincipal{patID: "pat1", identityID: id.ID, membershipID: m.ID}
	}
	roles := map[string]mcpPrincipal{
		"member":       mk("m@example.com", "m-example-com", "", "member"),
		"tenant_admin": mk("t@example.com", "t-example-com", "", "tenant_admin"),
		"super_admin":  mk("s@example.com", "s-example-com", "super_admin", "member"),
	}
	eras := map[string]func() (string, map[string]string){
		"legacy": func() (string, map[string]string) {
			return `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil
		},
		"stateless": func() (string, map[string]string) {
			return statelessBody(1, "tools/list", "", mcpVersionStateless), statelessHdr("tools/list")
		},
	}
	a := New(cp)
	for role, prin := range roles {
		for _, scope := range []string{"read", "write"} {
			for era, mkReq := range eras {
				t.Run(role+"/"+scope+"/"+era, func(t *testing.T) {
					p := prin
					p.scope = scope
					body, hdr := mkReq()
					r, req := eraReq(t, body, hdr)
					resp, status := a.dispatchMCPHTTP(r, &p, req)
					if status != http.StatusOK || resp == nil || resp.Error != nil {
						t.Fatalf("tools/list: status=%d resp=%+v", status, resp)
					}
					raw, err := json.Marshal(resp.Result)
					if err != nil {
						t.Fatal(err)
					}
					var res struct {
						Tools []struct {
							Name        string `json:"name"`
							InputSchema any    `json:"inputSchema"`
						} `json:"tools"`
					}
					if err := json.Unmarshal(raw, &res); err != nil {
						t.Fatal(err)
					}
					if len(res.Tools) == 0 {
						t.Fatal("no tools listed")
					}
					if role != "member" && scope == "write" {
						found := false
						for _, tl := range res.Tools {
							found = found || tl.Name == "list_sessions"
						}
						if !found {
							t.Fatal("admin tools missing; the test would not reach userKeyArg")
						}
					}
					seen := map[string]bool{}
					for _, tl := range res.Tools {
						if !toolNameRE.MatchString(tl.Name) {
							t.Errorf("tool name %q does not match %s", tl.Name, toolNameRE)
						}
						if seen[tl.Name] {
							t.Errorf("tool %q listed twice", tl.Name)
						}
						seen[tl.Name] = true
						for _, msg := range schemaProblems(tl.InputSchema) {
							t.Errorf("%s: %s", tl.Name, msg)
						}
					}
				})
			}
		}
	}
}

// schemaProblems checks a tool's decoded inputSchema against the subset of JSON Schema
// the tool registry uses.
func schemaProblems(schema any) []string {
	top, ok := schema.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("inputSchema is %T, want object", schema)}
	}
	var out []string
	if top["type"] != "object" {
		out = append(out, fmt.Sprintf("top-level type = %v, want \"object\"", top["type"]))
	}
	props, ok := top["properties"].(map[string]any)
	if !ok {
		out = append(out, fmt.Sprintf("properties is %T, want object", top["properties"]))
	}
	if req, present := top["required"]; present {
		list, ok := req.([]any)
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("required is %T, want array (absent when empty)", req))
		case len(list) == 0:
			out = append(out, "required is an empty array; omit it")
		default:
			for _, n := range list {
				s, isStr := n.(string)
				if !isStr {
					out = append(out, fmt.Sprintf("required entry %v is not a string", n))
				} else if _, has := props[s]; !has {
					out = append(out, fmt.Sprintf("required names %q, which is not a property", s))
				}
			}
		}
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case nil:
			out = append(out, "null at "+path)
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if x["type"] == "array" {
				if _, has := x["items"]; !has {
					out = append(out, "array without items at "+path)
				}
			}
			for _, k := range keys {
				walk(path+"."+k, x[k])
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		}
	}
	walk("inputSchema", top)
	return out
}
