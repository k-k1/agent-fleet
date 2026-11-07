package relayhdr

import (
	"net/http"
	"testing"
)

// AF_RELAY_EXTRA_HEADERS adds names and prefixes without widening the built-in list.
func TestExtraHeaders(t *testing.T) {
	defer SetExtra(ParseExtra(" x-app-token ,X-Tenant-*"))()
	for _, n := range []string{"X-App-Token", "X-Tenant-Id", "X-Tenant-"} {
		if !Allowed(n) {
			t.Errorf("%s should be allowed", n)
		}
	}
	for _, n := range []string{"X-Forwarded-Access-Token", "X-Other"} {
		if Allowed(n) {
			t.Errorf("%s should not be allowed", n)
		}
	}
}

// Django's AJAX CSRF header (X-CSRFToken) and the common spellings stay; gateway headers go.
func TestKeepAllowed(t *testing.T) {
	h := http.Header{}
	for _, k := range []string{"X-CSRFToken", "X-Csrf-Token", "X-XSRF-TOKEN", "Accept", "Sec-WebSocket-Key"} {
		h.Set(k, "v")
	}
	for _, k := range []string{"Sec-Token-Binding", "Sec-Private-State-Token", "Sec-Redemption-Record", "Sec-Secure-Session-Id"} {
		h.Set(k, "credential")
	}
	for _, k := range []string{"Sec-WebSocket-Protocol", "Sec-Fetch-Mode", "Sec-CH-UA"} {
		h.Set(k, "v")
	}
	h.Set("X-Forwarded-Access-Token", "t")
	h.Set("X-Amzn-Oidc-Data", "d")
	KeepAllowed(h)
	for _, k := range []string{"X-Csrftoken", "X-Csrf-Token", "X-Xsrf-Token", "Accept", "Sec-Websocket-Key"} {
		if h.Get(k) == "" {
			t.Errorf("%s dropped", k)
		}
	}
	for _, k := range []string{"Sec-Websocket-Protocol", "Sec-Fetch-Mode", "Sec-Ch-Ua"} {
		if h.Get(k) == "" {
			t.Errorf("%s dropped", k)
		}
	}
	for _, k := range []string{"Sec-Token-Binding", "Sec-Private-State-Token", "Sec-Redemption-Record", "Sec-Secure-Session-Id"} {
		if h.Get(k) != "" {
			t.Errorf("credential-class %s kept", k)
		}
	}
	if h.Get("X-Forwarded-Access-Token") != "" || h.Get("X-Amzn-Oidc-Data") != "" {
		t.Errorf("gateway headers kept: %v", h)
	}
}
