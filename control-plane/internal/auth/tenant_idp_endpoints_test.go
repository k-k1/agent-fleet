package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func tenantRow(issuer string) store.TenantIdP {
	return store.TenantIdP{
		Name: "idp1", Kind: TenantIdPKindOIDC, Issuer: issuer, ClientID: "c",
		Trust: TrustIssuer, AllowedDomains: "sub.example.org",
	}
}

func TestTenantIssuerMustBeHTTPS(t *testing.T) {
	for _, iss := range []string{"http://localhost:8080", "http://127.0.0.1/realm", "http://idp.example.org", "ftp://x.example.org", "https://"} {
		if ValidTenantIssuerURL(iss) {
			t.Errorf("%q must not be a valid tenant issuer", iss)
		}
		if _, err := BuildTenantProvider(tenantRow(iss), store.TenantRef{Slug: "sub"}, "s"); err == nil {
			t.Errorf("BuildTenantProvider accepted %q", iss)
		}
	}
	if !ValidTenantIssuerURL("https://idp.example.org/realm") {
		t.Error("an https issuer must be valid")
	}
	// The env path keeps its loopback carve-out.
	if !ValidIssuerURL("http://localhost:8080") {
		t.Error("env issuer carve-out must stay")
	}
}

func TestEndpointHostAllowed(t *testing.T) {
	ok := [][2]string{
		{"login.example.org", "login.example.org"},
		{"login.example.org", "auth.example.org"},
		{"example.org", "tenant.login.example.org"},
		{"accounts.google.com", "oauth2.googleapis.com"},
		{"accounts.google.com", "openidconnect.googleapis.com"},
		{"cognito-idp.ap-northeast-1.amazonaws.com", "pool.auth.ap-northeast-1.amazoncognito.com"},
		{"127.0.0.1", "127.0.0.1"},
	}
	for _, c := range ok {
		if !endpointHostAllowed(c[0], c[1]) {
			t.Errorf("%s -> %s should be allowed", c[0], c[1])
		}
	}
	bad := [][2]string{
		{"login.example.org", "login.example.net"},
		{"login.example.org", "example.org.evil.test"},
		{"login.example.org", "notexample.org"},
		{"a.herokuapp.com", "b.herokuapp.com"}, // shared hosting: a different site
		{"127.0.0.1", "127.0.0.2"},
		{"login.example.org", "127.0.0.1"},
		{"example.org", "oauth2.googleapis.com"},
		{"login.example.org", ""},
	}
	for _, c := range bad {
		if endpointHostAllowed(c[0], c[1]) {
			t.Errorf("%s -> %s should be refused", c[0], c[1])
		}
	}
}

// rtFunc serves the discovery document from memory, so no socket is involved.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func discoveryClient(body string) *http.Client {
	return &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
}

func TestTenantDiscoveryEndpointHostsArePinned(t *testing.T) {
	const issuer = "https://idp.example.org"
	cases := map[string]struct {
		doc string
		ok  bool
	}{
		"same site":                {`{"issuer":"` + issuer + `","authorization_endpoint":"https://idp.example.org/a","token_endpoint":"https://auth.example.org/t","userinfo_endpoint":"https://idp.example.org/u"}`, true},
		"token elsewhere":          {`{"issuer":"` + issuer + `","authorization_endpoint":"https://idp.example.org/a","token_endpoint":"https://collector.example.net/t"}`, false},
		"userinfo elsewhere":       {`{"issuer":"` + issuer + `","authorization_endpoint":"https://idp.example.org/a","token_endpoint":"https://idp.example.org/t","userinfo_endpoint":"https://other.example.net/u"}`, false},
		"literal internal address": {`{"issuer":"` + issuer + `","authorization_endpoint":"https://idp.example.org/a","token_endpoint":"https://10.0.0.5/t"}`, false},
		"plain http token":         {`{"issuer":"` + issuer + `","authorization_endpoint":"https://idp.example.org/a","token_endpoint":"http://idp.example.org/t"}`, false},
	}
	for name, c := range cases {
		p, err := BuildTenantProvider(tenantRow(issuer), store.TenantRef{Slug: "sub"}, "s")
		if err != nil {
			t.Fatal(err)
		}
		op := p.(*OIDCProvider)
		op.HTTPClient = discoveryClient(c.doc)
		_, err = op.endpoints(context.Background())
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
	}
	// An env provider is the operator's own and keeps endpoints as given.
	env := &OIDCProvider{ProviderID: "x", Issuer: issuer, HTTPClient: discoveryClient(
		`{"issuer":"` + issuer + `","authorization_endpoint":"https://idp.example.org/a","token_endpoint":"https://elsewhere.example.net/t"}`)}
	if _, err := env.endpoints(context.Background()); err != nil {
		t.Errorf("env provider must not be pinned: %v", err)
	}
}

// A tenant provider built the production way refuses to connect to a loopback or private
// issuer at all, and never follows a redirect.
func TestTenantProviderClientIsPublicOnlyAndDoesNotRedirect(t *testing.T) {
	for _, iss := range []string{"https://127.0.0.1", "https://[::1]", "https://10.1.2.3", "https://169.254.169.254"} {
		p, err := BuildTenantProvider(tenantRow(iss), store.TenantRef{Slug: "sub"}, "s")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.(*OIDCProvider).endpoints(context.Background())
		if !errors.Is(err, egressallow.ErrRefusedAddr) {
			t.Errorf("%s: want the dial refused, got %v", iss, err)
		}
	}
	redirects := 0
	c := NewTenantHTTPClient(oidcClientTimeout)
	c.Transport = rtFunc(func(r *http.Request) (*http.Response, error) {
		redirects++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://idp.example.org/next"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	resp, err := c.Get("https://idp.example.org/")
	if err != nil || resp.StatusCode != 302 || redirects != 1 {
		t.Fatalf("redirect must be returned, not followed: resp=%v err=%v hits=%d", resp, err, redirects)
	}
}
