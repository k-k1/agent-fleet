package main

import (
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
)

// A tenant row's issuer must be https; the loopback http carve-out belongs to env config.
func TestValidateTenantIdPBodyRefusesHTTPIssuer(t *testing.T) {
	body := func(issuer string) tenantIdPBody {
		return tenantIdPBody{Name: "idp1", Kind: auth.TenantIdPKindOIDC, Issuer: issuer, ClientID: "c", Trust: auth.TrustIssuer}
	}
	for _, iss := range []string{"http://localhost:8080", "http://127.0.0.1/realm", "http://idp.example.org"} {
		aerr := validateTenantIdPBody(body(iss), []string{"sub.example.org"}, nil, nil)
		if aerr == nil || aerr.status != http.StatusBadRequest || aerr.code != "tenant_idp_issuer_invalid" {
			t.Errorf("%s: want tenant_idp_issuer_invalid, got %+v", iss, aerr)
		}
	}
	if aerr := validateTenantIdPBody(body("https://idp.example.org"), []string{"sub.example.org"}, nil, nil); aerr != nil {
		t.Errorf("https issuer refused: %+v", aerr)
	}
}
