package store

// Seam helpers: the small pure functions the store family reached out for when it was
// extracted from `package main` (ADR 0067). They are copies — the originals still live in
// `main.go` / `oauth_bitbucket.go` / `tenant_login.go`, which other tracks own.
//
// The duplication is temporary, and until it is folded into one shared `internal/…`
// helper package, fixing only one of the two copies diverges silently.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
)

// AWSConfigFor is the store family's single door to AWS credentials, used by the
// Secrets Manager lookup behind the Postgres password (store_postgres_secret.go).
//
// It is a variable for the same reason `awsConfigFor` in the owning binary is one:
// the live E2E has to run the PRODUCT under a copy of the CP task role while its own
// verification calls keep the deployer's ambient credentials (docs/log/64 §64.22.3).
// `control-plane/store_seam.go` therefore points this at that variable through a
// closure — resolved at call time, so overriding it in a test still reaches here.
// The default below only matters if nobody wires it.
var AWSConfigFor = func(ctx context.Context, region string) (aws.Config, error) {
	return awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
}

func awsConfigFor(ctx context.Context, region string) (aws.Config, error) {
	return AWSConfigFor(ctx, region)
}

// envOr mirrors main.envOr.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// randHex mirrors main.randHex (oauth_bitbucket.go).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// splitCSV mirrors main.splitCSV.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitCSVLower mirrors main.splitCSVLower (tenant_login.go): lowercased, deduped.
func splitCSVLower(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// splitDomainCSV mirrors main.splitDomainCSV (tenant_login.go): splitCSVLower for
// email domains, tolerating a leading "@".
func splitDomainCSV(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p)), "@")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
