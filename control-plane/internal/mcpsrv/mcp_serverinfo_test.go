package mcpsrv

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// identCP is testCP with a chosen ServerIdentity.
type identCP struct {
	testCP
	id ServerIdentity
}

func (c identCP) ServerIdentity(context.Context) ServerIdentity { return c.id }

func infoVia(t *testing.T, id ServerIdentity, body string, hdr map[string]string) (top, meta, legacy map[string]any) {
	t.Helper()
	r, req := eraReq(t, body, hdr)
	resp, status := API{cp: identCP{id: id}}.dispatchMCPHTTP(r, &mcpPrincipal{patID: "p"}, req)
	if status != http.StatusOK || resp == nil || resp.Error != nil {
		t.Fatalf("status=%d resp=%+v", status, resp)
	}
	res, _ := resp.Result.(map[string]any)
	if m, ok := res["_meta"].(map[string]any); ok {
		meta, _ = m[metaServerInfo].(map[string]any)
	}
	top, _ = res["serverInfo"].(map[string]any)
	return top, meta, nil
}

func TestMCPServerInfoDescribesServer(t *testing.T) {
	id := ServerIdentity{Version: "1.4.2", BrandLabel: "thyme", PublicBaseURL: "https://af.example.test"}
	top, meta, _ := infoVia(t, id, statelessBody(1, "server/discover", "", mcpVersionStateless), statelessHdr("server/discover"))
	wantIcons := []map[string]any{
		{"src": "https://af.example.test/brand/icon-192.png", "mimeType": "image/png", "sizes": []string{"192x192"}},
		{"src": "https://af.example.test/brand/icon-512.png", "mimeType": "image/png", "sizes": []string{"512x512"}},
	}
	for label, info := range map[string]map[string]any{"top-level": top, "_meta": meta} {
		if info["name"] != "agent-fleet" || info["title"] != "Agent Fleet · thyme" || info["version"] != "1.4.2" ||
			info["websiteUrl"] != "https://af.example.test" || info["description"] == "" || info["description"] == nil {
			t.Errorf("%s serverInfo = %v", label, info)
		}
		if !reflect.DeepEqual(info["icons"], wantIcons) {
			t.Errorf("%s icons = %v", label, info["icons"])
		}
	}

	// The initialize answer (legacy era) gains the same fields.
	r, req := eraReq(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, nil)
	resp, _ := API{cp: identCP{id: id}}.dispatchMCPHTTP(r, &mcpPrincipal{patID: "p"}, req)
	got, _ := resp.Result.(map[string]any)["serverInfo"].(map[string]any)
	if got["title"] != "Agent Fleet · thyme" || got["version"] != "1.4.2" || got["name"] != "agent-fleet" {
		t.Errorf("initialize serverInfo = %v", got)
	}
}

func TestMCPServerInfoWithoutHTTPSBaseHasNoIcons(t *testing.T) {
	for _, base := range []string{"", "http://af.example.test"} {
		top, _, _ := infoVia(t, ServerIdentity{Version: "dev", PublicBaseURL: base},
			statelessBody(1, "server/discover", "", mcpVersionStateless), statelessHdr("server/discover"))
		for _, k := range []string{"icons", "websiteUrl"} {
			if _, ok := top[k]; ok {
				t.Errorf("base %q: %s must be omitted, got %v", base, k, top[k])
			}
		}
		if top["title"] != "Agent Fleet" {
			t.Errorf("base %q: title = %v, want plain Agent Fleet without a label", base, top["title"])
		}
	}
}

// PUBLIC_BASE_URL is operator-typed: only its https origin may reach the icon URLs.
func TestMCPServerInfoNormalisesBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://af.example.test/":         "https://af.example.test",
		"https://af.example.test:8443":     "https://af.example.test:8443",
		"https://af.example.test/console/": "https://af.example.test",
		"https://af.example.test?x=1":      "https://af.example.test",
		"https://af.example.test/#frag":    "https://af.example.test",
		"https:///missing-host":            "",
		"https://user:pw@af.example.test":  "",
		"ftp://af.example.test":            "",
		"not a url":                        "",
	}
	for in, want := range cases {
		info := serverInfo(ServerIdentity{PublicBaseURL: in})
		got, _ := info["websiteUrl"].(string)
		if got != want {
			t.Errorf("%q: websiteUrl = %q, want %q", in, got, want)
		}
		if want == "" {
			if _, ok := info["icons"]; ok {
				t.Errorf("%q: icons must be omitted", in)
			}
			continue
		}
		icons := info["icons"].([]map[string]any)
		if icons[1]["src"] != want+"/brand/icon-512.png" {
			t.Errorf("%q: icon src = %v", in, icons[1]["src"])
		}
	}
}
